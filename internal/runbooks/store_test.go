package runbooks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestInstallListInspectRemoveLocalRunbook(t *testing.T) {
	configDir := t.TempDir()
	sourceDir := filepath.Join(t.TempDir(), "runbooks")
	if err := os.MkdirAll(filepath.Join(sourceDir, "aws"), 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "README.md"), []byte("# Overview"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "aws", "incident.md"), []byte("steps"), 0o644); err != nil {
		t.Fatalf("write incident: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "notes.txt"), []byte("ignore"), 0o644); err != nil {
		t.Fatalf("write txt: %v", err)
	}

	manager := NewManager(configDir)
	record, err := manager.Install(context.Background(), InstallOptions{
		Source: sourceDir,
		Name:   "AWS Incidents",
	})
	if err != nil {
		t.Fatalf("install returned error: %v", err)
	}
	if record.ID != "aws-incidents" {
		t.Fatalf("unexpected id: %q", record.ID)
	}
	if record.FileCount != 2 {
		t.Fatalf("unexpected file count: %d", record.FileCount)
	}
	if _, err := os.Stat(filepath.Join(record.PackageDir, "README.md")); err != nil {
		t.Fatalf("expected copied README.md: %v", err)
	}
	if _, err := os.Stat(filepath.Join(record.PackageDir, "aws", "incident.md")); err != nil {
		t.Fatalf("expected copied aws/incident.md: %v", err)
	}

	list, err := manager.List()
	if err != nil {
		t.Fatalf("list returned error: %v", err)
	}
	if len(list) != 1 || list[0].ID != record.ID {
		t.Fatalf("unexpected list output: %#v", list)
	}

	inspected, err := manager.Inspect(record.ID)
	if err != nil {
		t.Fatalf("inspect returned error: %v", err)
	}
	if inspected.ContentHash == "" {
		t.Fatalf("expected content hash")
	}
	if len(inspected.Files) != 2 {
		t.Fatalf("unexpected inspect files: %#v", inspected.Files)
	}

	if err := manager.Remove(record.ID); err != nil {
		t.Fatalf("remove returned error: %v", err)
	}
	after, err := manager.List()
	if err != nil {
		t.Fatalf("list after remove returned error: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("expected no runbooks after remove, got %d", len(after))
	}
}

func TestInstallDuplicateRequiresForce(t *testing.T) {
	configDir := t.TempDir()
	sourceDir := filepath.Join(t.TempDir(), "runbooks")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	filePath := filepath.Join(sourceDir, "incident.md")
	if err := os.WriteFile(filePath, []byte("v1"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	manager := NewManager(configDir)
	if _, err := manager.Install(context.Background(), InstallOptions{
		Source: sourceDir,
		Name:   "Primary Runbook",
	}); err != nil {
		t.Fatalf("first install returned error: %v", err)
	}

	if _, err := manager.Install(context.Background(), InstallOptions{
		Source: sourceDir,
		Name:   "Primary Runbook",
	}); err == nil {
		t.Fatal("expected duplicate install error without force")
	}

	if err := os.WriteFile(filePath, []byte("v2"), 0o644); err != nil {
		t.Fatalf("update source: %v", err)
	}
	forced, err := manager.Install(context.Background(), InstallOptions{
		Source: sourceDir,
		Name:   "Primary Runbook",
		Force:  true,
	})
	if err != nil {
		t.Fatalf("forced install returned error: %v", err)
	}
	if forced.ContentHash == "" {
		t.Fatal("expected content hash after forced install")
	}
}

func TestInstallRejectsSourceWithoutMarkdown(t *testing.T) {
	configDir := t.TempDir()
	sourceDir := filepath.Join(t.TempDir(), "runbooks")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "notes.txt"), []byte("no markdown"), 0o644); err != nil {
		t.Fatalf("write txt: %v", err)
	}

	manager := NewManager(configDir)
	if _, err := manager.Install(context.Background(), InstallOptions{Source: sourceDir}); err == nil {
		t.Fatal("expected install error when no markdown files exist")
	}
}

func TestInstallSingleFileRejectsSubdir(t *testing.T) {
	configDir := t.TempDir()
	sourceDir := t.TempDir()
	filePath := filepath.Join(sourceDir, "incident.md")
	if err := os.WriteFile(filePath, []byte("steps"), 0o644); err != nil {
		t.Fatalf("write markdown file: %v", err)
	}

	manager := NewManager(configDir)
	if _, err := manager.Install(context.Background(), InstallOptions{
		Source: filePath,
		Subdir: "nested",
	}); err == nil {
		t.Fatal("expected error when --subdir used with single markdown file")
	}
}

func TestInstallForceCopyFailureKeepsPreviousPackage(t *testing.T) {
	configDir := t.TempDir()
	sourceDir := t.TempDir()
	filePath := filepath.Join(sourceDir, "incident.md")
	if err := os.WriteFile(filePath, []byte("version-one"), 0o644); err != nil {
		t.Fatalf("write source markdown file: %v", err)
	}

	manager := NewManager(configDir)
	original, err := manager.Install(context.Background(), InstallOptions{
		Source: sourceDir,
		Name:   "Primary Runbook",
	})
	if err != nil {
		t.Fatalf("initial install returned error: %v", err)
	}
	originalBytes, err := os.ReadFile(filepath.Join(original.PackageDir, "incident.md"))
	if err != nil {
		t.Fatalf("read initial package file: %v", err)
	}

	if err := os.WriteFile(filePath, []byte("version-two"), 0o644); err != nil {
		t.Fatalf("update source markdown file: %v", err)
	}

	originalCopy := copyRunbookFile
	t.Cleanup(func() {
		copyRunbookFile = originalCopy
	})
	copyRunbookFile = func(src string, dst string) error {
		return errors.New("forced copy failure")
	}

	if _, err := manager.Install(context.Background(), InstallOptions{
		Source: sourceDir,
		Name:   "Primary Runbook",
		Force:  true,
	}); err == nil {
		t.Fatal("expected forced install to fail when copy fails")
	}

	current, err := manager.Inspect("primary-runbook")
	if err != nil {
		t.Fatalf("inspect after failure returned error: %v", err)
	}
	if current.ContentHash != original.ContentHash {
		t.Fatalf("expected original hash to remain after failed force install: got=%q want=%q", current.ContentHash, original.ContentHash)
	}

	currentBytes, err := os.ReadFile(filepath.Join(current.PackageDir, "incident.md"))
	if err != nil {
		t.Fatalf("read package file after failure: %v", err)
	}
	if string(currentBytes) != string(originalBytes) {
		t.Fatalf("expected package contents to remain unchanged after failed force install")
	}
}

func TestConcurrentInstallPreservesAllRegistryUpdates(t *testing.T) {
	configDir := t.TempDir()
	sourceDir := t.TempDir()
	filePath := filepath.Join(sourceDir, "incident.md")
	if err := os.WriteFile(filePath, []byte("steps"), 0o644); err != nil {
		t.Fatalf("write source markdown file: %v", err)
	}

	manager := NewManager(configDir)
	const workers = 20
	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for i := 0; i < workers; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := manager.Install(context.Background(), InstallOptions{
				Source: sourceDir,
				Name:   fmt.Sprintf("Runbook %02d", i),
			})
			if err != nil {
				errCh <- err
			}
		}()
	}

	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent install returned error: %v", err)
		}
	}

	items, err := manager.List()
	if err != nil {
		t.Fatalf("list returned error: %v", err)
	}
	if len(items) != workers {
		t.Fatalf("expected %d installed runbooks, got %d", workers, len(items))
	}
}

func TestInstallGitSourceRequiresGitBinary(t *testing.T) {
	configDir := t.TempDir()
	manager := NewManager(configDir)

	originalLookPath := gitLookPath
	t.Cleanup(func() {
		gitLookPath = originalLookPath
	})
	gitLookPath = func(file string) (string, error) {
		if file == "git" {
			return "", errors.New("not found")
		}
		return "", nil
	}

	_, err := manager.Install(context.Background(), InstallOptions{
		Source: "https://github.com/example/runbooks.git",
	})
	if err == nil {
		t.Fatal("expected install to fail when git is unavailable")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "git is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCloneGitRepositoryUsesSeparator(t *testing.T) {
	originalRunner := gitCombinedOutput
	t.Cleanup(func() {
		gitCombinedOutput = originalRunner
	})

	var calls [][]string
	gitCombinedOutput = func(ctx context.Context, args ...string) ([]byte, error) {
		_ = ctx
		copied := append([]string(nil), args...)
		calls = append(calls, copied)
		return []byte("ok"), nil
	}

	source := "--upload-pack=evil"
	if err := cloneGitRepository(context.Background(), source, "", "/tmp/dst"); err != nil {
		t.Fatalf("cloneGitRepository returned error: %v", err)
	}
	if len(calls) == 0 {
		t.Fatal("expected git command to be invoked")
	}

	cloneArgs := calls[0]
	sepIndex := -1
	for i, arg := range cloneArgs {
		if arg == "--" {
			sepIndex = i
			break
		}
	}
	if sepIndex < 0 {
		t.Fatalf("expected git clone args to include -- separator, got: %#v", cloneArgs)
	}
	if sepIndex+1 >= len(cloneArgs) || cloneArgs[sepIndex+1] != source {
		t.Fatalf("expected source after -- separator, got: %#v", cloneArgs)
	}
}
