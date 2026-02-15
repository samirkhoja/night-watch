package sessionlog

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/samirkhoja/night-watch/internal/config"
	"github.com/samirkhoja/night-watch/internal/llm"
)

const sessionsDirName = "sessions"

type Manager struct {
	dir string
}

type SessionMeta struct {
	ID           string
	Path         string
	CreatedAt    time.Time
	Preview      string
	MessageCount int
}

type reloadPayload struct {
	Messages []llm.Message `json:"messages"`
}

func NewManager(configDir string) *Manager {
	return &Manager{
		dir: filepath.Join(configDir, sessionsDirName),
	}
}

func (m *Manager) Save(cfg config.Config, history []llm.Message) (SessionMeta, error) {
	messages := normalizeMessages(history)
	if len(messages) == 0 {
		return SessionMeta{}, nil
	}

	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return SessionMeta{}, fmt.Errorf("create sessions dir: %w", err)
	}

	now := time.Now().UTC()
	path, id, err := m.nextSessionPath(now)
	if err != nil {
		return SessionMeta{}, err
	}

	preview := summarizeText(firstUserMessage(messages), 96)
	if preview == "" {
		preview = "session transcript"
	}

	frontMatter := map[string]string{
		"id":             id,
		"created_at":     now.Format(time.RFC3339),
		"llm_provider":   strings.TrimSpace(cfg.LLMProvider),
		"llm_model":      strings.TrimSpace(cfg.LLMModel),
		"cloud_provider": strings.TrimSpace(cfg.CloudProvider),
		"message_count":  strconv.Itoa(len(messages)),
		"preview":        sanitizeFrontMatterValue(preview),
	}

	content, err := buildMarkdown(frontMatter, messages)
	if err != nil {
		return SessionMeta{}, err
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return SessionMeta{}, fmt.Errorf("write session file: %w", err)
	}

	return SessionMeta{
		ID:           id,
		Path:         path,
		CreatedAt:    now,
		Preview:      preview,
		MessageCount: len(messages),
	}, nil
}

func (m *Manager) nextSessionPath(now time.Time) (string, string, error) {
	baseID := now.Format("20060102-150405")
	for suffix := 0; suffix < 1000; suffix++ {
		id := baseID
		if suffix > 0 {
			id = fmt.Sprintf("%s-%02d", baseID, suffix)
		}
		path := filepath.Join(m.dir, fmt.Sprintf("session-%s.md", id))
		_, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			return path, id, nil
		}
		if err != nil {
			return "", "", fmt.Errorf("stat session file: %w", err)
		}
	}
	return "", "", errors.New("unable to allocate session filename")
}

func (m *Manager) List() ([]SessionMeta, error) {
	entries, err := os.ReadDir(m.dir)
	if errors.Is(err, os.ErrNotExist) {
		return []SessionMeta{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read sessions dir: %w", err)
	}

	var metas []SessionMeta
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "session-") || !strings.HasSuffix(name, ".md") {
			continue
		}
		path := filepath.Join(m.dir, name)
		meta, err := parseMeta(path)
		if err != nil {
			continue
		}
		metas = append(metas, meta)
	}

	sort.Slice(metas, func(i, j int) bool {
		return metas[i].CreatedAt.After(metas[j].CreatedAt)
	})

	return metas, nil
}

func (m *Manager) LoadMessages(path string) ([]llm.Message, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read session file: %w", err)
	}

	jsonBlock := extractJSONCodeBlock(string(raw))
	if strings.TrimSpace(jsonBlock) == "" {
		return nil, errors.New("session reload data not found")
	}

	var payload reloadPayload
	if err := json.Unmarshal([]byte(jsonBlock), &payload); err != nil {
		return nil, fmt.Errorf("parse session reload data: %w", err)
	}

	messages := normalizeMessages(payload.Messages)
	if len(messages) == 0 {
		return nil, errors.New("session reload data is empty")
	}
	return messages, nil
}

func buildMarkdown(frontMatter map[string]string, messages []llm.Message) (string, error) {
	payloadData, err := json.MarshalIndent(reloadPayload{Messages: messages}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal reload payload: %w", err)
	}

	var builder strings.Builder
	builder.WriteString("---\n")
	writeFrontMatterField(&builder, "id", frontMatter["id"])
	writeFrontMatterField(&builder, "created_at", frontMatter["created_at"])
	writeFrontMatterField(&builder, "llm_provider", frontMatter["llm_provider"])
	writeFrontMatterField(&builder, "llm_model", frontMatter["llm_model"])
	writeFrontMatterField(&builder, "cloud_provider", frontMatter["cloud_provider"])
	writeFrontMatterField(&builder, "message_count", frontMatter["message_count"])
	writeFrontMatterField(&builder, "preview", frontMatter["preview"])
	builder.WriteString("---\n\n")

	builder.WriteString("# Night Watch Session\n\n")
	builder.WriteString("## Summary\n\n")
	builder.WriteString("- This is a condensed transcript for quick context reloading.\n")
	builder.WriteString("- Most recent turns are shown below.\n\n")

	builder.WriteString("## Transcript (Condensed)\n\n")
	start := 0
	if len(messages) > 12 {
		start = len(messages) - 12
	}
	for idx, msg := range messages[start:] {
		label := "Assistant"
		if msg.Role == "user" {
			label = "User"
		}
		builder.WriteString(fmt.Sprintf("%d. %s: %s\n", idx+1, label, summarizeText(msg.Content, 180)))
	}
	builder.WriteString("\n")

	builder.WriteString("## Reload Data\n\n")
	builder.WriteString("```json\n")
	builder.Write(payloadData)
	builder.WriteString("\n```\n")

	return builder.String(), nil
}

func writeFrontMatterField(builder *strings.Builder, key, value string) {
	builder.WriteString(key)
	builder.WriteString(": ")
	builder.WriteString(sanitizeFrontMatterValue(value))
	builder.WriteString("\n")
}

func sanitizeFrontMatterValue(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	return strings.Join(strings.Fields(value), " ")
}

func parseMeta(path string) (SessionMeta, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return SessionMeta{}, err
	}

	fm := parseFrontMatter(string(raw))
	id := strings.TrimSpace(fm["id"])
	if id == "" {
		id = deriveIDFromFilename(path)
	}

	createdAt := parseCreatedAt(strings.TrimSpace(fm["created_at"]), id)
	if createdAt.IsZero() {
		if info, statErr := os.Stat(path); statErr == nil {
			createdAt = info.ModTime().UTC()
		}
	}

	count := 0
	if rawCount := strings.TrimSpace(fm["message_count"]); rawCount != "" {
		if n, err := strconv.Atoi(rawCount); err == nil {
			count = n
		}
	}

	preview := strings.TrimSpace(fm["preview"])
	if preview == "" {
		preview = "session transcript"
	}

	return SessionMeta{
		ID:           id,
		Path:         path,
		CreatedAt:    createdAt,
		Preview:      preview,
		MessageCount: count,
	}, nil
}

func parseFrontMatter(raw string) map[string]string {
	out := map[string]string{}
	if !strings.HasPrefix(raw, "---\n") {
		return out
	}
	rest := strings.TrimPrefix(raw, "---\n")
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return out
	}
	body := rest[:end]
	for _, line := range strings.Split(body, "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if key == "" {
			continue
		}
		out[key] = value
	}
	return out
}

func deriveIDFromFilename(path string) string {
	base := filepath.Base(path)
	base = strings.TrimPrefix(base, "session-")
	base = strings.TrimSuffix(base, ".md")
	return strings.TrimSpace(base)
}

func parseCreatedAt(raw string, fallbackID string) time.Time {
	if raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			return t.UTC()
		}
	}
	if fallbackID != "" {
		if t, err := time.Parse("20060102-150405", fallbackID); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func extractJSONCodeBlock(raw string) string {
	start := strings.Index(raw, "```json")
	if start < 0 {
		return ""
	}
	rest := raw[start+len("```json"):]
	rest = strings.TrimPrefix(rest, "\n")
	end := strings.Index(rest, "\n```")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}

func normalizeMessages(messages []llm.Message) []llm.Message {
	var normalized []llm.Message
	for _, msg := range messages {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}
		if role != "user" && role != "assistant" {
			continue
		}
		normalized = append(normalized, llm.Message{
			Role:    role,
			Content: content,
		})
	}
	return normalized
}

func firstUserMessage(messages []llm.Message) string {
	for _, msg := range messages {
		if msg.Role == "user" {
			return msg.Content
		}
	}
	return ""
}

func summarizeText(value string, maxLen int) string {
	value = strings.TrimSpace(value)
	value = strings.Join(strings.Fields(value), " ")
	if maxLen <= 0 || len(value) <= maxLen {
		return value
	}
	if maxLen < 4 {
		return value[:maxLen]
	}
	return value[:maxLen-3] + "..."
}
