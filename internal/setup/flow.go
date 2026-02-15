package setup

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/samirkhoja/night-watch/internal/config"
	"github.com/samirkhoja/night-watch/internal/ui"
)

var providerChoices = []string{"openai", "anthropic", "google"}
var reasoningChoices = []string{"low", "medium", "high"}
var cloudChoices = []string{"aws", "gcp", "sentry"}

const slackWebhookEnvName = "SLACK_WEBHOOK_URL"

var providerCheckTimeout = 15 * time.Second

var lookupBinary = exec.LookPath
var runExternalCommand = func(ctx context.Context, name string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()), err
}

func runProviderCLICommand(ctx context.Context, name string, args ...string) (string, string, error) {
	timeout := providerCheckTimeout
	if timeout <= 0 {
		return runExternalCommand(ctx, name, args...)
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stdout, stderr, err := runExternalCommand(runCtx, name, args...)
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return stdout, stderr, fmt.Errorf("%s command timed out after %s", name, timeout)
	}
	return stdout, stderr, err
}

type DisplayContext struct {
	WorkspaceRoot string
	RunbookRoot   string
}

func Run(ctx context.Context, cfgManager *config.Manager, in io.Reader, out io.Writer, display DisplayContext) error {
	reader := bufio.NewReader(in)

	cfg, err := cfgManager.Load(ctx)
	if err != nil {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(cfg.CloudProvider), "multi") {
		ui.Warn(out, "cloud_provider=multi is no longer supported; defaulting to aws for setup")
		cfg.CloudProvider = "aws"
	}

	ui.Section(out, "setup")
	ui.Status(out, "Choose your preferred LLM provider and cloud provider.")

	llmProvider, err := selectOption(reader, out, "LLM provider", providerChoices, cfg.LLMProvider)
	if err != nil {
		return err
	}
	cfg.LLMProvider = llmProvider

	defaultModel := defaultModelForProvider(llmProvider)
	modelPrompt := fmt.Sprintf("Model for %s", llmProvider)
	model, err := promptWithDefault(reader, out, modelPrompt, firstNonEmpty(cfg.LLMModel, defaultModel))
	if err != nil {
		return err
	}
	cfg.LLMModel = model

	reasoningEffort, err := selectOption(
		reader,
		out,
		"Reasoning effort",
		reasoningChoices,
		config.NormalizeReasoningEffort(cfg.ReasoningEffort),
	)
	if err != nil {
		return err
	}
	cfg.ReasoningEffort = reasoningEffort

	cloudProvider, err := selectOption(reader, out, "Cloud provider", cloudChoices, cfg.CloudProvider)
	if err != nil {
		return err
	}
	cfg.CloudProvider = cloudProvider

	if cloudProvider == "aws" {
		awsProfile, err := selectAWSProfile(reader, out, cfg.AWSProfile)
		if err != nil {
			return err
		}
		cfg.AWSProfile = awsProfile
	}

	if err := ensureProviderKey(reader, out, cfgManager, llmProvider); err != nil {
		return err
	}

	if err := checkCloudCredentials(ctx, reader, out, cloudProvider, cfg.AWSProfile); err != nil {
		return err
	}

	slackEnabled, err := selectYesNo(reader, out, "Enable Slack notifications", cfg.SlackEnabled)
	if err != nil {
		return err
	}
	cfg.SlackEnabled = slackEnabled
	if slackEnabled {
		if err := ensureSlackWebhook(reader, out, cfgManager); err != nil {
			return err
		}
	}

	cfg.SetupComplete = true
	if err := cfgManager.Save(ctx, cfg); err != nil {
		return err
	}

	ui.Success(out, "Setup complete.")
	workspaceRoot := strings.TrimSpace(display.WorkspaceRoot)
	if workspaceRoot != "" {
		if abs, err := filepath.Abs(workspaceRoot); err == nil {
			workspaceRoot = abs
		}
	}
	runbookRoot := strings.TrimSpace(display.RunbookRoot)
	if runbookRoot != "" {
		if abs, err := filepath.Abs(runbookRoot); err == nil {
			runbookRoot = abs
		}
	}
	ui.ConfigStatus(
		out,
		cfg.LLMProvider,
		cfg.LLMModel,
		cfg.ReasoningEffort,
		cfg.CloudProvider,
		cfg.AWSProfile,
		cfg.SlackEnabled,
		workspaceRoot,
		runbookRoot,
	)
	return nil
}

func selectOption(
	reader *bufio.Reader,
	out io.Writer,
	label string,
	options []string,
	current string,
) (string, error) {
	fmt.Fprintf(out, "\n%s:\n", label)
	defaultIndex := -1
	for i, option := range options {
		if option == current {
			defaultIndex = i + 1
		}
		marker := " "
		if option == current {
			marker = "*"
		}
		fmt.Fprintf(out, "  %d) [%s] %s\n", i+1, marker, option)
	}

	defaultValue := ""
	if defaultIndex > 0 {
		defaultValue = strconv.Itoa(defaultIndex)
	}
	choiceRaw, err := promptWithDefault(reader, out, "Select number", defaultValue)
	if err != nil {
		return "", err
	}

	choiceIndex, err := strconv.Atoi(strings.TrimSpace(choiceRaw))
	if err != nil || choiceIndex < 1 || choiceIndex > len(options) {
		return "", fmt.Errorf("invalid %s choice", label)
	}
	return options[choiceIndex-1], nil
}

func promptWithDefault(reader *bufio.Reader, out io.Writer, label, defaultValue string) (string, error) {
	if defaultValue != "" {
		fmt.Fprintf(out, "%s [%s]: ", label, defaultValue)
	} else {
		fmt.Fprintf(out, "%s: ", label)
	}
	raw, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultValue, nil
	}
	return raw, nil
}

func selectYesNo(reader *bufio.Reader, out io.Writer, label string, current bool) (bool, error) {
	defaultValue := "n"
	if current {
		defaultValue = "y"
	}
	raw, err := promptWithDefault(reader, out, label+" (y/n)", defaultValue)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return false, fmt.Errorf("invalid choice for %s", label)
	}
}

func ensureProviderKey(
	reader *bufio.Reader,
	out io.Writer,
	cfgManager *config.Manager,
	provider string,
) error {
	keyName := apiKeyEnvName(provider)
	if keyName == "" {
		return nil
	}

	val, err := cfgManager.GetEnvValue(keyName)
	if err != nil {
		return err
	}
	if strings.TrimSpace(val) != "" {
		ui.Success(out, fmt.Sprintf("%s is configured.", keyName))
		return nil
	}

	ui.Warn(out, fmt.Sprintf("%s is missing.", keyName))
	input, err := promptWithDefault(reader, out, fmt.Sprintf("Enter %s", keyName), "")
	if err != nil {
		return err
	}
	input = strings.TrimSpace(input)
	if input == "" {
		return fmt.Errorf("%s is required for %s provider", keyName, provider)
	}
	if err := cfgManager.SetEnvValue(keyName, input); err != nil {
		return err
	}
	ui.Success(out, fmt.Sprintf("Saved %s to %s.", keyName, cfgManager.ConfigDir()))
	return nil
}

func ensureSlackWebhook(reader *bufio.Reader, out io.Writer, cfgManager *config.Manager) error {
	val, err := cfgManager.GetEnvValue(slackWebhookEnvName)
	if err != nil {
		return err
	}
	if strings.TrimSpace(val) != "" {
		ui.Success(out, fmt.Sprintf("%s is configured.", slackWebhookEnvName))
		return nil
	}

	ui.Warn(out, fmt.Sprintf("%s is missing.", slackWebhookEnvName))
	input, err := promptWithDefault(reader, out, fmt.Sprintf("Enter %s", slackWebhookEnvName), "")
	if err != nil {
		return err
	}
	input = strings.TrimSpace(input)
	if input == "" {
		return fmt.Errorf("%s is required when Slack notifications are enabled", slackWebhookEnvName)
	}
	if err := validateSlackWebhookURL(input); err != nil {
		return err
	}
	if err := cfgManager.SetEnvValue(slackWebhookEnvName, input); err != nil {
		return err
	}
	ui.Success(out, fmt.Sprintf("Saved %s to %s.", slackWebhookEnvName, cfgManager.ConfigDir()))
	return nil
}

func validateSlackWebhookURL(raw string) error {
	u, err := url.ParseRequestURI(strings.TrimSpace(raw))
	if err != nil {
		return errors.New("invalid Slack webhook URL")
	}
	switch strings.ToLower(strings.TrimSpace(u.Scheme)) {
	case "http", "https":
		return nil
	default:
		return errors.New("invalid Slack webhook URL scheme")
	}
}

func apiKeyEnvName(provider string) string {
	switch provider {
	case "openai":
		return "OPENAI_API_KEY"
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "google":
		return "GOOGLE_API_KEY"
	default:
		return ""
	}
}

func defaultModelForProvider(provider string) string {
	switch provider {
	case "openai":
		return "gpt-5.2"
	case "anthropic":
		return "claude-opus-4-5"
	case "google":
		return "gemini-3.0-pro"
	default:
		return "gpt-5.2"
	}
}

func selectAWSProfile(reader *bufio.Reader, out io.Writer, current string) (string, error) {
	current = strings.TrimSpace(current)
	if current == "" {
		current = "default"
	}

	profiles := discoverAWSProfiles()
	if !containsString(profiles, current) {
		profiles = append([]string{current}, profiles...)
	}

	fmt.Fprintln(out, "\nAWS profile:")
	for i, profile := range profiles {
		marker := " "
		if profile == current {
			marker = "*"
		}
		fmt.Fprintf(out, "  %d) [%s] %s\n", i+1, marker, profile)
	}

	defaultValue := current
	for i, profile := range profiles {
		if profile == current {
			defaultValue = strconv.Itoa(i + 1)
			break
		}
	}

	raw, err := promptWithDefault(reader, out, "Select number or enter profile", defaultValue)
	if err != nil {
		return "", err
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return current, nil
	}

	if index, err := strconv.Atoi(raw); err == nil {
		if index < 1 || index > len(profiles) {
			return "", fmt.Errorf("invalid AWS profile choice")
		}
		return profiles[index-1], nil
	}
	return raw, nil
}

func discoverAWSProfiles() []string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return []string{"default"}
	}

	paths := []string{
		filepath.Join(homeDir, ".aws", "credentials"),
		filepath.Join(homeDir, ".aws", "config"),
	}
	seen := map[string]struct{}{
		"default": {},
	}

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, profile := range parseAWSProfileNamesFromINI(string(data)) {
			trimmed := strings.TrimSpace(profile)
			if trimmed == "" {
				continue
			}
			seen[trimmed] = struct{}{}
		}
	}

	profiles := make([]string, 0, len(seen))
	for profile := range seen {
		profiles = append(profiles, profile)
	}
	sort.Strings(profiles)
	if containsString(profiles, "default") && profiles[0] != "default" {
		profiles = append([]string{"default"}, removeString(profiles, "default")...)
	}
	return profiles
}

func parseAWSProfileNamesFromINI(raw string) []string {
	lines := strings.Split(raw, "\n")
	seen := map[string]struct{}{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "[") || !strings.HasSuffix(line, "]") {
			continue
		}
		section := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
		if section == "" {
			continue
		}
		if strings.HasPrefix(section, "profile ") {
			section = strings.TrimSpace(strings.TrimPrefix(section, "profile "))
		}
		if section == "" {
			continue
		}
		seen[section] = struct{}{}
	}

	out := make([]string, 0, len(seen))
	for profile := range seen {
		out = append(out, profile)
	}
	sort.Strings(out)
	return out
}

func checkCloudCredentials(
	ctx context.Context,
	reader *bufio.Reader,
	out io.Writer,
	cloudProvider,
	awsProfile string,
) error {
	ui.Section(out, "cloud credential check")
	switch cloudProvider {
	case "aws":
		selected := strings.TrimSpace(awsProfile)
		if selected == "" {
			selected = "default"
		}
		summary, err := verifyAWSCLI(ctx, selected)
		if err != nil {
			ui.Warn(out, err.Error())
			return errors.New("configure AWS CLI auth and rerun setup")
		}
		ui.Success(out, "AWS CLI is configured and authenticated.")
		ui.Status(out, summary)
		confirmed, err := selectYesNo(reader, out, "Use this AWS environment", true)
		if err != nil {
			return err
		}
		if !confirmed {
			return errors.New("setup canceled: configure the correct AWS environment and rerun setup")
		}
	case "gcp":
		summary, err := verifyGCPCLI(ctx)
		if err != nil {
			ui.Warn(out, err.Error())
			return errors.New("configure gcloud auth and rerun setup")
		}
		ui.Success(out, "gcloud CLI is configured and authenticated.")
		ui.Status(out, summary)
		confirmed, err := selectYesNo(reader, out, "Use this GCP environment", true)
		if err != nil {
			return err
		}
		if !confirmed {
			return errors.New("setup canceled: configure the correct GCP environment and rerun setup")
		}
	case "sentry":
		summary, err := verifySentryCLI(ctx)
		if err != nil {
			ui.Warn(out, err.Error())
			return errors.New("configure sentry-cli auth and rerun setup")
		}
		ui.Success(out, "sentry-cli is configured and authenticated.")
		ui.Status(out, summary)
		confirmed, err := selectYesNo(reader, out, "Use this Sentry environment", true)
		if err != nil {
			return err
		}
		if !confirmed {
			return errors.New("setup canceled: configure the correct Sentry environment and rerun setup")
		}
	case "multi":
		return errors.New("cloud_provider=multi is not supported. choose one provider: aws, gcp, or sentry")
	default:
		return errors.New("unknown cloud provider selected")
	}
	return nil
}

func verifyAWSCLI(ctx context.Context, profile string) (string, error) {
	if _, err := lookupBinary("aws"); err != nil {
		return "", errors.New("AWS CLI not found. Install it and run `aws configure` or `aws sso login`")
	}

	args := []string{"--no-cli-pager"}
	if strings.TrimSpace(profile) != "" {
		args = append(args, "--profile", strings.TrimSpace(profile))
	}
	args = append(args, "sts", "get-caller-identity", "--output", "json")
	stdout, stderr, err := runProviderCLICommand(ctx, "aws", args...)
	if err != nil {
		return "", fmt.Errorf("AWS CLI auth check failed (%s). Run `aws configure` or `aws sso login`", firstNonEmpty(stderr, stdout, err.Error()))
	}

	var identity struct {
		Account string `json:"Account"`
		Arn     string `json:"Arn"`
	}
	if parseErr := json.Unmarshal([]byte(stdout), &identity); parseErr != nil {
		return "", fmt.Errorf("AWS CLI auth check returned unexpected output: %v", parseErr)
	}
	account := strings.TrimSpace(identity.Account)
	if account == "" {
		account = "unknown-account"
	}
	arn := strings.TrimSpace(identity.Arn)
	if arn == "" {
		arn = "unknown-principal"
	}
	return fmt.Sprintf("AWS profile %q -> account %s, principal %s", profile, account, arn), nil
}

func verifyGCPCLI(ctx context.Context) (string, error) {
	if _, err := lookupBinary("gcloud"); err != nil {
		return "", errors.New("gcloud CLI not found. Install it and run `gcloud auth login`")
	}

	accountOut, accountErrOut, err := runProviderCLICommand(
		ctx,
		"gcloud",
		"auth",
		"list",
		"--filter=status:ACTIVE",
		"--format=value(account)",
	)
	if err != nil {
		return "", fmt.Errorf("gcloud auth check failed (%s). Run `gcloud auth login`", firstNonEmpty(accountErrOut, accountOut, err.Error()))
	}
	account := firstNonBlankLine(accountOut)
	if account == "" {
		return "", errors.New("gcloud has no active account. Run `gcloud auth login`")
	}

	projectOut, _, _ := runProviderCLICommand(ctx, "gcloud", "config", "get-value", "project", "--quiet")
	project := strings.TrimSpace(projectOut)
	if project == "" {
		project = "(unset)"
	}
	return fmt.Sprintf("GCP account %q, project %q", account, project), nil
}

func verifySentryCLI(ctx context.Context) (string, error) {
	if _, err := lookupBinary("sentry-cli"); err != nil {
		return "", errors.New("sentry-cli not found. Install it and run `sentry-cli login`")
	}

	stdout, stderr, err := runProviderCLICommand(ctx, "sentry-cli", "info")
	if err != nil {
		return "", fmt.Errorf("sentry-cli auth check failed (%s). Run `sentry-cli login`", firstNonEmpty(stderr, stdout, err.Error()))
	}
	if strings.TrimSpace(stdout) == "" {
		return "sentry-cli authenticated.", nil
	}

	org := extractSentryInfoValue(stdout, "Default Organization:")
	urlValue := extractSentryInfoValue(stdout, "Sentry Server:")
	if strings.TrimSpace(urlValue) == "" {
		urlValue = extractSentryInfoValue(stdout, "URL:")
	}
	if strings.TrimSpace(org) == "" {
		org = "unknown-org"
	}
	if strings.TrimSpace(urlValue) == "" {
		urlValue = "default"
	}
	return fmt.Sprintf("Sentry org %q, server %q", org, urlValue), nil
}

func extractSentryInfoValue(raw string, prefix string) string {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func firstNonBlankLine(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func removeString(values []string, target string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == target {
			continue
		}
		out = append(out, value)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
