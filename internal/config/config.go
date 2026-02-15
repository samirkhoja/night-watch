package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	defaultConfigDirName       = "night-watch"
	configFileName             = "config.json"
	envFileName                = ".env"
	projectConfigDirName       = ".nightwatch"
	projectConfigFileName      = "settings.json"
	projectConfigLocalFileName = "settings.local.json"
	customConfigEnvName        = "NIGHTWATCH_CONFIG_FILE"
)

type Config struct {
	SetupComplete   bool   `json:"setup_complete"`
	LLMProvider     string `json:"llm_provider"`
	LLMModel        string `json:"llm_model"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	CloudProvider   string `json:"cloud_provider"`
	AWSProfile      string `json:"aws_profile,omitempty"`
	SlackEnabled    bool   `json:"slack_enabled,omitempty"`
}

type Options struct {
	CustomConfigPath string
	WorkingDir       string
}

type partialConfig struct {
	SetupComplete   *bool   `json:"setup_complete,omitempty"`
	LLMProvider     *string `json:"llm_provider,omitempty"`
	LLMModel        *string `json:"llm_model,omitempty"`
	ReasoningEffort *string `json:"reasoning_effort,omitempty"`
	CloudProvider   *string `json:"cloud_provider,omitempty"`
	AWSProfile      *string `json:"aws_profile,omitempty"`
	SlackEnabled    *bool   `json:"slack_enabled,omitempty"`
}

func DefaultConfig() Config {
	return Config{
		SetupComplete:   false,
		LLMProvider:     "openai",
		LLMModel:        "gpt-5.2",
		ReasoningEffort: "medium",
		CloudProvider:   "aws",
		AWSProfile:      "default",
		SlackEnabled:    false,
	}
}

type Manager struct {
	configDir            string
	configPath           string
	envPath              string
	projectConfigPath    string
	projectLocalPath     string
	customConfigPath     string
	customConfigRequired bool
}

func NewManager(options Options) (*Manager, error) {
	baseDir := strings.TrimSpace(os.Getenv("NIGHTWATCH_CONFIG_DIR"))
	if baseDir == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve home dir: %w", err)
		}
		baseDir = filepath.Join(homeDir, ".config", defaultConfigDirName)
	}
	baseDir = filepath.Clean(baseDir)

	workingDir := strings.TrimSpace(options.WorkingDir)
	if workingDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve working dir: %w", err)
		}
		workingDir = cwd
	}
	workingDir = filepath.Clean(workingDir)

	customConfigPath := strings.TrimSpace(options.CustomConfigPath)
	if customConfigPath == "" {
		customConfigPath = strings.TrimSpace(os.Getenv(customConfigEnvName))
	}
	customConfigRequired := customConfigPath != ""
	if customConfigRequired {
		customConfigPath = resolvePath(workingDir, customConfigPath)
	}

	projectConfigPath, projectLocalPath := discoverProjectConfigFiles(workingDir)

	m := &Manager{
		configDir:            baseDir,
		configPath:           filepath.Join(baseDir, configFileName),
		envPath:              filepath.Join(baseDir, envFileName),
		projectConfigPath:    projectConfigPath,
		projectLocalPath:     projectLocalPath,
		customConfigPath:     customConfigPath,
		customConfigRequired: customConfigRequired,
	}
	if err := m.ensureFiles(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) ConfigDir() string {
	return m.configDir
}

func (m *Manager) ensureFiles() error {
	if err := os.MkdirAll(m.configDir, 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	if _, err := os.Stat(m.configPath); errors.Is(err, os.ErrNotExist) {
		cfg := DefaultConfig()
		if err := m.Save(context.Background(), cfg); err != nil {
			return err
		}
	} else if err != nil {
		return fmt.Errorf("stat config file: %w", err)
	}

	if _, err := os.Stat(m.envPath); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(m.envPath, []byte{}, 0o600); err != nil {
			return fmt.Errorf("create env file: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("stat env file: %w", err)
	}

	return nil
}

func (m *Manager) Load(ctx context.Context) (Config, error) {
	select {
	case <-ctx.Done():
		return Config{}, ctx.Err()
	default:
	}

	cfg := DefaultConfig()
	layers := []struct {
		path     string
		required bool
		label    string
	}{
		{path: m.configPath, required: true, label: "user config"},
		{path: m.projectConfigPath, required: false, label: "project settings"},
		{path: m.projectLocalPath, required: false, label: "project local settings"},
		{path: m.customConfigPath, required: m.customConfigRequired, label: "custom settings"},
	}

	for _, layer := range layers {
		if strings.TrimSpace(layer.path) == "" {
			continue
		}
		partial, err := loadPartialConfig(layer.path, layer.required)
		if err != nil {
			return Config{}, fmt.Errorf("load %s: %w", layer.label, err)
		}
		applyPartialConfig(&cfg, partial)
	}

	return cfg, nil
}

func (m *Manager) Save(ctx context.Context, cfg Config) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	cfg.ReasoningEffort = NormalizeReasoningEffort(cfg.ReasoningEffort)

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(m.configPath, data, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

func (m *Manager) LoadEnv() (map[string]string, error) {
	raw, err := os.ReadFile(m.envPath)
	if err != nil {
		return nil, fmt.Errorf("read env file: %w", err)
	}
	env := parseDotEnv(string(raw))
	return env, nil
}

func (m *Manager) SetEnvValue(key, value string) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("env key cannot be empty")
	}

	env, err := m.LoadEnv()
	if err != nil {
		return err
	}
	env[key] = value

	var keys []string
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var builder strings.Builder
	for _, k := range keys {
		builder.WriteString(k)
		builder.WriteByte('=')
		builder.WriteString(env[k])
		builder.WriteByte('\n')
	}

	if err := os.WriteFile(m.envPath, []byte(builder.String()), 0o600); err != nil {
		return fmt.Errorf("write env file: %w", err)
	}
	return nil
}

func (m *Manager) GetEnvValue(key string) (string, error) {
	if val := strings.TrimSpace(os.Getenv(key)); val != "" {
		return val, nil
	}
	env, err := m.LoadEnv()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(env[key]), nil
}

func parseDotEnv(raw string) map[string]string {
	env := map[string]string{}
	lines := strings.Split(raw, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eqIdx := strings.Index(line, "=")
		if eqIdx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eqIdx])
		value := strings.TrimSpace(line[eqIdx+1:])
		env[key] = strings.Trim(value, `"'`)
	}
	return env
}

func discoverProjectConfigFiles(startDir string) (string, string) {
	dir := filepath.Clean(strings.TrimSpace(startDir))
	if dir == "" {
		return "", ""
	}

	for {
		projectDir := filepath.Join(dir, projectConfigDirName)
		info, err := os.Stat(projectDir)
		if err == nil && info.IsDir() {
			return filepath.Join(projectDir, projectConfigFileName), filepath.Join(projectDir, projectConfigLocalFileName)
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", ""
}

func resolvePath(baseDir string, pathValue string) string {
	pathValue = strings.TrimSpace(pathValue)
	if pathValue == "" {
		return ""
	}
	if filepath.IsAbs(pathValue) {
		return filepath.Clean(pathValue)
	}
	return filepath.Clean(filepath.Join(baseDir, pathValue))
}

func loadPartialConfig(path string, required bool) (partialConfig, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if required {
			return partialConfig{}, fmt.Errorf("file not found: %s", path)
		}
		return partialConfig{}, nil
	}
	if err != nil {
		return partialConfig{}, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return partialConfig{}, nil
	}

	var cfg partialConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return partialConfig{}, err
	}
	return cfg, nil
}

func applyPartialConfig(target *Config, partial partialConfig) {
	if partial.SetupComplete != nil {
		target.SetupComplete = *partial.SetupComplete
	}
	if partial.LLMProvider != nil {
		if value := strings.TrimSpace(*partial.LLMProvider); value != "" {
			target.LLMProvider = value
		}
	}
	if partial.LLMModel != nil {
		if value := strings.TrimSpace(*partial.LLMModel); value != "" {
			target.LLMModel = value
		}
	}
	if partial.ReasoningEffort != nil {
		target.ReasoningEffort = NormalizeReasoningEffort(*partial.ReasoningEffort)
	}
	if partial.CloudProvider != nil {
		if value := strings.TrimSpace(*partial.CloudProvider); value != "" {
			target.CloudProvider = value
		}
	}
	if partial.AWSProfile != nil {
		target.AWSProfile = strings.TrimSpace(*partial.AWSProfile)
	}
	if partial.SlackEnabled != nil {
		target.SlackEnabled = *partial.SlackEnabled
	}
}

func NormalizeReasoningEffort(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "low":
		return "low"
	case "high":
		return "high"
	default:
		return "medium"
	}
}
