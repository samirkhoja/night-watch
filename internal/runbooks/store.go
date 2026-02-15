package runbooks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	storeDirName    = "runbooks-installed"
	packagesDirName = "packages"
	registryName    = "registry.json"
	registryVersion = 1
	maxFiles        = 1000
	maxFileBytes    = 1 << 20
)

type InstallOptions struct {
	Source string
	Name   string
	Ref    string
	Subdir string
	Force  bool
}

type Record struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Source      string    `json:"source"`
	Ref         string    `json:"ref,omitempty"`
	Subdir      string    `json:"subdir,omitempty"`
	InstalledAt time.Time `json:"installed_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	FileCount   int       `json:"file_count"`
	ContentHash string    `json:"content_hash"`
	PackageDir  string    `json:"package_dir"`
	Files       []string  `json:"files,omitempty"`
}

type registry struct {
	Version int      `json:"version"`
	Records []Record `json:"records"`
}

type Manager struct {
	storeDir    string
	packagesDir string
	registry    string
	mu          sync.Mutex
}

var gitLookPath = exec.LookPath
var gitCombinedOutput = func(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	return cmd.CombinedOutput()
}

func NewManager(configDir string) *Manager {
	base := strings.TrimSpace(configDir)
	if base == "" {
		base = "."
	}
	storeDir := filepath.Clean(filepath.Join(base, storeDirName))
	return &Manager{
		storeDir:    storeDir,
		packagesDir: filepath.Join(storeDir, packagesDirName),
		registry:    filepath.Join(storeDir, registryName),
	}
}

func (m *Manager) StoreDir() string {
	if m == nil {
		return ""
	}
	return m.storeDir
}

func (m *Manager) Ensure() error {
	if m == nil {
		return errors.New("runbook manager is nil")
	}
	if err := os.MkdirAll(m.packagesDir, 0o755); err != nil {
		return fmt.Errorf("create runbooks packages dir: %w", err)
	}
	if _, err := os.Stat(m.registry); errors.Is(err, os.ErrNotExist) {
		return m.saveRegistry(registry{Version: registryVersion, Records: []Record{}})
	} else if err != nil {
		return fmt.Errorf("stat runbook registry: %w", err)
	}
	return nil
}

func (m *Manager) Install(ctx context.Context, opts InstallOptions) (Record, error) {
	if m == nil {
		return Record{}, errors.New("runbook manager is nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.Ensure(); err != nil {
		return Record{}, err
	}
	source := strings.TrimSpace(opts.Source)
	if source == "" {
		return Record{}, errors.New("runbook source is required")
	}

	materialized, err := m.materializeSource(ctx, source, strings.TrimSpace(opts.Ref))
	if err != nil {
		return Record{}, err
	}
	defer materialized.cleanup()

	root := materialized.root
	subdir := strings.TrimSpace(opts.Subdir)
	if materialized.singleFile != "" && subdir != "" {
		return Record{}, errors.New("--subdir cannot be used when source is a single markdown file")
	}
	if subdir != "" {
		root = filepath.Clean(filepath.Join(root, subdir))
		if !isPathWithinRoot(materialized.root, root) {
			return Record{}, fmt.Errorf("subdir %q resolves outside source root", subdir)
		}
	}

	paths, err := collectMarkdownFiles(root, materialized.singleFile)
	if err != nil {
		return Record{}, err
	}
	contentHash, err := hashFiles(root, paths)
	if err != nil {
		return Record{}, err
	}

	name := strings.TrimSpace(opts.Name)
	if name == "" {
		name = deriveRunbookName(materialized.displaySource)
	}
	id := slugify(name)
	if id == "" {
		id = "runbook"
	}

	reg, err := m.loadRegistry()
	if err != nil {
		return Record{}, err
	}
	existingIdx := -1
	for i := range reg.Records {
		if reg.Records[i].ID == id {
			existingIdx = i
			break
		}
	}
	if existingIdx >= 0 && !opts.Force {
		return Record{}, fmt.Errorf("runbook %q already installed; use --force to replace", id)
	}

	targetDir := m.packageDirForID(id)
	targetExists := false
	if info, err := os.Stat(targetDir); err == nil {
		targetExists = info.IsDir()
	} else if !errors.Is(err, os.ErrNotExist) {
		return Record{}, fmt.Errorf("stat runbook package dir: %w", err)
	}
	if targetExists && existingIdx < 0 && !opts.Force {
		return Record{}, fmt.Errorf("runbook package %q already exists on disk; use --force to replace", id)
	}

	stagingDir, err := os.MkdirTemp(m.packagesDir, id+".staging-*")
	if err != nil {
		return Record{}, fmt.Errorf("create runbook staging dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(stagingDir) }()
	relFiles := make([]string, 0, len(paths))
	for _, src := range paths {
		rel, err := filepath.Rel(root, src)
		if err != nil {
			return Record{}, fmt.Errorf("resolve runbook relative path: %w", err)
		}
		rel = filepath.Clean(rel)
		dst := filepath.Join(stagingDir, rel)
		if !isPathWithinRoot(stagingDir, dst) {
			return Record{}, fmt.Errorf("resolved destination %q escapes package dir", dst)
		}
		if err := copyRunbookFile(src, dst); err != nil {
			return Record{}, err
		}
		relFiles = append(relFiles, filepath.ToSlash(rel))
	}
	sort.Strings(relFiles)

	now := time.Now().UTC()
	record := Record{
		ID:          id,
		Name:        name,
		Source:      materialized.displaySource,
		Ref:         strings.TrimSpace(opts.Ref),
		Subdir:      subdir,
		InstalledAt: now,
		UpdatedAt:   now,
		FileCount:   len(relFiles),
		ContentHash: contentHash,
		PackageDir:  targetDir,
		Files:       relFiles,
	}
	if existingIdx >= 0 {
		record.InstalledAt = reg.Records[existingIdx].InstalledAt
		reg.Records[existingIdx] = record
	} else {
		reg.Records = append(reg.Records, record)
	}
	sort.Slice(reg.Records, func(i, j int) bool {
		return reg.Records[i].ID < reg.Records[j].ID
	})

	backupDir := ""
	if targetExists {
		backupDir = filepath.Join(m.packagesDir, id+".backup-"+strconv.FormatInt(time.Now().UnixNano(), 10))
		if err := os.Rename(targetDir, backupDir); err != nil {
			return Record{}, fmt.Errorf("backup existing runbook package: %w", err)
		}
	}
	if err := os.Rename(stagingDir, targetDir); err != nil {
		if backupDir != "" {
			_ = os.Rename(backupDir, targetDir)
		}
		return Record{}, fmt.Errorf("activate runbook package: %w", err)
	}

	if err := m.saveRegistry(reg); err != nil {
		_ = os.RemoveAll(targetDir)
		if backupDir != "" {
			_ = os.Rename(backupDir, targetDir)
		}
		return Record{}, err
	}
	if backupDir != "" {
		_ = os.RemoveAll(backupDir)
	}
	return record, nil
}

func (m *Manager) List() ([]Record, error) {
	if m == nil {
		return nil, errors.New("runbook manager is nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.Ensure(); err != nil {
		return nil, err
	}
	reg, err := m.loadRegistry()
	if err != nil {
		return nil, err
	}
	out := make([]Record, len(reg.Records))
	copy(out, reg.Records)
	return out, nil
}

func (m *Manager) Inspect(id string) (Record, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Record{}, errors.New("runbook id is required")
	}
	items, err := m.List()
	if err != nil {
		return Record{}, err
	}
	for _, item := range items {
		if item.ID == id {
			return item, nil
		}
	}
	return Record{}, fmt.Errorf("runbook %q not found", id)
}

func (m *Manager) Remove(id string) error {
	if m == nil {
		return errors.New("runbook manager is nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("runbook id is required")
	}
	if err := m.Ensure(); err != nil {
		return err
	}
	reg, err := m.loadRegistry()
	if err != nil {
		return err
	}
	idx := -1
	for i := range reg.Records {
		if reg.Records[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("runbook %q not found", id)
	}
	targetDir := m.packageDirForID(id)
	if err := os.RemoveAll(targetDir); err != nil {
		return fmt.Errorf("remove runbook package: %w", err)
	}
	reg.Records = append(reg.Records[:idx], reg.Records[idx+1:]...)
	return m.saveRegistry(reg)
}

func (m *Manager) packageDirForID(id string) string {
	return filepath.Join(m.packagesDir, id)
}

func (m *Manager) loadRegistry() (registry, error) {
	raw, err := os.ReadFile(m.registry)
	if err != nil {
		return registry{}, fmt.Errorf("read runbook registry: %w", err)
	}
	var reg registry
	if err := json.Unmarshal(raw, &reg); err != nil {
		return registry{}, fmt.Errorf("parse runbook registry: %w", err)
	}
	if reg.Version == 0 {
		reg.Version = registryVersion
	}
	if reg.Records == nil {
		reg.Records = []Record{}
	}
	return reg, nil
}

func (m *Manager) saveRegistry(reg registry) error {
	reg.Version = registryVersion
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal runbook registry: %w", err)
	}
	tmp := m.registry + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write runbook registry temp: %w", err)
	}
	if err := os.Rename(tmp, m.registry); err != nil {
		return fmt.Errorf("replace runbook registry: %w", err)
	}
	return nil
}

type materializedSource struct {
	root          string
	singleFile    string
	displaySource string
	cleanup       func()
}

func (m *Manager) materializeSource(ctx context.Context, source string, ref string) (materializedSource, error) {
	if localRoot, singleFile, ok := localSourceRoot(source); ok {
		displaySource := localRoot
		if singleFile != "" {
			displaySource = singleFile
		}
		return materializedSource{
			root:          localRoot,
			singleFile:    singleFile,
			displaySource: displaySource,
			cleanup:       func() {},
		}, nil
	}
	if !looksLikeGitSource(source) {
		return materializedSource{}, fmt.Errorf("source %q is not a local path and not recognized as git source", source)
	}
	if _, err := gitLookPath("git"); err != nil {
		return materializedSource{}, errors.New("git is required for git runbook sources. install git and ensure it is available on PATH")
	}
	tempRoot, err := os.MkdirTemp("", "nwatch-runbook-*")
	if err != nil {
		return materializedSource{}, fmt.Errorf("create temp dir: %w", err)
	}
	cleanup := func() {
		_ = os.RemoveAll(tempRoot)
	}
	repoDir := filepath.Join(tempRoot, "repo")
	if err := cloneGitRepository(ctx, source, ref, repoDir); err != nil {
		cleanup()
		return materializedSource{}, err
	}
	return materializedSource{
		root:          repoDir,
		displaySource: source,
		cleanup:       cleanup,
	}, nil
}

func cloneGitRepository(ctx context.Context, source string, ref string, dst string) error {
	ref = strings.TrimSpace(ref)
	args := []string{"clone"}
	if ref == "" {
		args = append(args, "--depth", "1")
	}
	args = append(args, "--", source, dst)
	output, err := gitCombinedOutput(ctx, args...)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("git clone timed out: %w", ctx.Err())
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return fmt.Errorf("git clone canceled: %w", ctx.Err())
		}
		return fmt.Errorf(
			"git clone failed (check repository URL and git authentication): %s",
			firstNonBlank(strings.TrimSpace(string(output)), err.Error()),
		)
	}
	if ref == "" {
		return nil
	}
	checkoutOutput, err := gitCombinedOutput(ctx, "-C", dst, "checkout", "--", ref)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("git checkout %q timed out: %w", ref, ctx.Err())
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return fmt.Errorf("git checkout %q canceled: %w", ref, ctx.Err())
		}
		return fmt.Errorf(
			"git checkout %q failed (check the ref exists): %s",
			ref,
			firstNonBlank(strings.TrimSpace(string(checkoutOutput)), err.Error()),
		)
	}
	return nil
}

func localSourceRoot(source string) (string, string, bool) {
	source = strings.TrimSpace(source)
	if source == "" {
		return "", "", false
	}
	resolved := source
	if !filepath.IsAbs(resolved) {
		if abs, err := filepath.Abs(resolved); err == nil {
			resolved = abs
		}
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", "", false
	}
	if info.IsDir() {
		return filepath.Clean(resolved), "", true
	}
	if !strings.EqualFold(filepath.Ext(info.Name()), ".md") {
		return "", "", false
	}
	resolved = filepath.Clean(resolved)
	return filepath.Dir(resolved), resolved, true
}

func collectMarkdownFiles(root string, singleFile string) ([]string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("runbook source root is required")
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("stat runbook source root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("runbook source root %q is not a directory", root)
	}
	singleFile = strings.TrimSpace(singleFile)
	if singleFile != "" {
		if !filepath.IsAbs(singleFile) {
			singleFile = filepath.Clean(filepath.Join(root, singleFile))
		}
		if !isPathWithinRoot(root, singleFile) {
			return nil, fmt.Errorf("single-file source %q is outside root %q", singleFile, root)
		}
		if !strings.EqualFold(filepath.Ext(singleFile), ".md") {
			return nil, fmt.Errorf("source file %q must be markdown", singleFile)
		}
		singleInfo, err := os.Stat(singleFile)
		if err != nil {
			return nil, fmt.Errorf("stat source file: %w", err)
		}
		if singleInfo.Size() > maxFileBytes {
			return nil, fmt.Errorf("runbook file %q exceeds %d bytes", singleFile, maxFileBytes)
		}
		return []string{singleFile}, nil
	}
	var files []string
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(d.Name()), ".md") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > maxFileBytes {
			return fmt.Errorf("runbook file %q exceeds %d bytes", path, maxFileBytes)
		}
		files = append(files, filepath.Clean(path))
		if len(files) > maxFiles {
			return fmt.Errorf("runbook file limit exceeded (%d)", maxFiles)
		}
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("scan markdown files: %w", walkErr)
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, errors.New("no markdown files found in source")
	}
	return files, nil
}

func hashFiles(root string, paths []string) (string, error) {
	root = filepath.Clean(root)
	hasher := sha256.New()
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return "", fmt.Errorf("stat runbook file: %w", err)
		}
		if info.Size() > maxFileBytes {
			return "", fmt.Errorf("runbook file %q exceeds %d bytes", path, maxFileBytes)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read runbook file: %w", err)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return "", fmt.Errorf("resolve hash path: %w", err)
		}
		rel = filepath.ToSlash(filepath.Clean(rel))
		hasher.Write([]byte(rel))
		hasher.Write([]byte{0})
		hasher.Write(data)
		hasher.Write([]byte{0})
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func copyFile(src string, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create runbook file dir: %w", err)
	}
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open runbook source file: %w", err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open runbook destination file: %w", err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy runbook file: %w", err)
	}
	return nil
}

var copyRunbookFile = copyFile

func deriveRunbookName(source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return "runbook"
	}
	base := filepath.Base(source)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	base = strings.TrimSuffix(base, ".git")
	base = strings.TrimSpace(base)
	if base == "" {
		return "runbook"
	}
	return base
}

func slugify(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	var builder strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
			lastDash = false
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && builder.Len() > 0 {
				builder.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(builder.String(), "-")
	return out
}

func looksLikeGitSource(source string) bool {
	source = strings.TrimSpace(source)
	if source == "" {
		return false
	}
	lower := strings.ToLower(source)
	return strings.HasPrefix(lower, "http://") ||
		strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(lower, "ssh://") ||
		strings.HasPrefix(lower, "git://") ||
		strings.HasPrefix(lower, "git@")
}

func isPathWithinRoot(root string, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	if rel == "." || rel == "" {
		return true
	}
	return !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != ".."
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
