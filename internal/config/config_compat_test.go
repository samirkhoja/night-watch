package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadIgnoresLegacyCommandPoliciesField(t *testing.T) {
	baseDir := t.TempDir()
	workDir := filepath.Join(baseDir, "work")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}

	customPath := filepath.Join(baseDir, "legacy.json")
	legacy := `{
  "llm_provider": "openai",
  "llm_model": "gpt-4o-mini",
  "reasoning_effort": "high",
  "cloud_provider": "aws",
  "aws_profile": "prod",
  "slack_enabled": true,
  "command_policies": {
    "cmd::ls": "allow"
  }
}`
	if err := os.WriteFile(customPath, []byte(legacy), 0o600); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	t.Setenv("NIGHTWATCH_CONFIG_DIR", filepath.Join(baseDir, "cfg"))

	manager, err := NewManager(Options{
		CustomConfigPath: customPath,
		WorkingDir:       workDir,
	})
	if err != nil {
		t.Fatalf("NewManager returned error: %v", err)
	}

	cfg, err := manager.Load(context.Background())
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.LLMProvider != "openai" || cfg.ReasoningEffort != "high" || cfg.AWSProfile != "prod" || !cfg.SlackEnabled {
		t.Fatalf("unexpected config values loaded from legacy file: %+v", cfg)
	}
}

func TestSaveDoesNotWriteCommandPoliciesField(t *testing.T) {
	baseDir := t.TempDir()
	workDir := filepath.Join(baseDir, "work")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	t.Setenv("NIGHTWATCH_CONFIG_DIR", filepath.Join(baseDir, "cfg"))

	manager, err := NewManager(Options{WorkingDir: workDir})
	if err != nil {
		t.Fatalf("NewManager returned error: %v", err)
	}

	cfg := DefaultConfig()
	cfg.SetupComplete = true
	cfg.LLMProvider = "google"
	cfg.SlackEnabled = true
	if err := manager.Save(context.Background(), cfg); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(manager.ConfigDir(), configFileName))
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	if strings.Contains(string(raw), "command_policies") {
		t.Fatalf("saved config unexpectedly contains legacy command_policies field")
	}
	if !strings.Contains(string(raw), "\"slack_enabled\": true") {
		t.Fatalf("saved config missing slack_enabled field when enabled")
	}
}
