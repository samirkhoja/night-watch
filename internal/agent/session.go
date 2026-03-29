package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	agentsdk "github.com/samirkhoja/agent-sdk"
	"github.com/samirkhoja/night-watch/internal/config"
	"github.com/samirkhoja/night-watch/internal/prompts"
	"github.com/samirkhoja/night-watch/internal/ui"
)

type Session struct {
	provider           agentsdk.Provider
	compactionProvider agentsdk.Provider
	cfg                *config.Config
	approval           *ApprovalManager
	out                io.Writer
	workspaceRoot      string
	runbookRoot        string
	history            []agentsdk.Message
	maxMemory          int
	modelMaxTokens     int
	replyMaxTokens     int
	maxSteps           int
	showUserInput      bool
	sdkAgent           *agentsdk.Agent
	sdkStore           agentsdk.SessionStore
	sessionID          string
	currentRunID       string
	runtimeMu          sync.Mutex
}

type agentAction struct {
	Type       string                 `json:"type"`
	Message    string                 `json:"message,omitempty"`
	Reason     string                 `json:"reason,omitempty"`
	Command    string                 `json:"command,omitempty"`
	Cwd        string                 `json:"cwd,omitempty"`
	TimeoutSec int                    `json:"timeout_sec,omitempty"`
	Params     map[string]interface{} `json:"params,omitempty"`
}

type subAgentTask struct {
	Name     string
	Goal     string
	Context  string
	MaxSteps int
}

type subAgentResult struct {
	Index      int
	Name       string
	Goal       string
	Reply      string
	Steps      int
	ActionRuns int
	Err        error
}

func NewSession(
	provider agentsdk.Provider,
	cfg *config.Config,
	approval *ApprovalManager,
	out io.Writer,
) *Session {
	modelMaxTokens := inferModelMaxTokens(cfg)
	replyMaxTokens := 2500
	if modelMaxTokens <= 0 {
		modelMaxTokens = 32000
	}
	if replyMaxTokens >= modelMaxTokens/2 {
		replyMaxTokens = maxIntValue(1024, modelMaxTokens/6)
	}
	workspaceRoot := "."
	if cwd, err := os.Getwd(); err == nil {
		workspaceRoot = cwd
	}
	return &Session{
		provider:       provider,
		cfg:            cfg,
		approval:       approval,
		out:            out,
		workspaceRoot:  resolveWorkspaceRoot(workspaceRoot),
		runbookRoot:    resolveWorkspaceRoot(workspaceRoot),
		maxMemory:      80,
		modelMaxTokens: modelMaxTokens,
		replyMaxTokens: replyMaxTokens,
		showUserInput:  true,
		sessionID:      newSessionID(),
	}
}

func (s *Session) Reset() {
	s.history = nil
	if s.sdkStore != nil {
		_ = s.sdkStore.Save(context.Background(), s.sessionID, &agentsdk.SessionState{})
	}
}

func (s *Session) SetShowUserInput(enabled bool) {
	s.showUserInput = enabled
}

func (s *Session) SetSessionStore(store agentsdk.SessionStore) {
	s.sdkStore = store
	s.invalidateSDKAgent()
}

func (s *Session) SetSessionID(id string) {
	id = strings.TrimSpace(id)
	if id == "" {
		id = newSessionID()
	}
	s.sessionID = id
}

func (s *Session) SessionID() string {
	if s == nil {
		return newSessionID()
	}
	if strings.TrimSpace(s.sessionID) == "" {
		s.sessionID = newSessionID()
	}
	return s.sessionID
}

func (s *Session) SetWorkspaceRoot(root string) {
	root = strings.TrimSpace(root)
	if root == "" {
		return
	}
	resolved := resolveWorkspaceRoot(root)
	s.workspaceRoot = resolved
	if strings.TrimSpace(s.runbookRoot) == "" {
		s.runbookRoot = resolved
	}
	s.invalidateSDKAgent()
}

func (s *Session) SetRunbookRoot(root string) {
	root = strings.TrimSpace(root)
	if root == "" {
		s.runbookRoot = s.workspaceRoot
		s.invalidateSDKAgent()
		return
	}
	s.runbookRoot = resolveWorkspaceRoot(root)
	s.invalidateSDKAgent()
}

func (s *Session) SetCompactionProvider(provider agentsdk.Provider) {
	s.compactionProvider = provider
	s.invalidateSDKAgent()
}

func (s *Session) SetHistory(messages []agentsdk.Message) {
	history := normalizeHistory(messages, s.maxMemory)
	s.history = history
	if s.sdkStore == nil {
		s.sdkStore = agentsdk.NewMemorySessionStore()
	}
	state := &agentsdk.SessionState{
		Messages: history,
	}
	_ = s.sdkStore.Save(context.Background(), s.sessionID, state)
}

func (s *Session) SetMaxSteps(maxSteps int) {
	if maxSteps < 0 {
		maxSteps = 0
	}
	s.maxSteps = maxSteps
	s.invalidateSDKAgent()
}

func (s *Session) LoadHistoryFromStore(ctx context.Context) (bool, error) {
	if s == nil || s.sdkStore == nil {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	state, err := s.sdkStore.Load(ctx, s.sessionID)
	if err != nil {
		return false, err
	}
	if state == nil || len(state.Messages) == 0 {
		return false, nil
	}
	s.history = sdkHistoryToMessages(state.Messages, s.maxMemory)
	return len(s.history) > 0, nil
}

func (s *Session) RestoreState(ctx context.Context, state *agentsdk.SessionState) error {
	if s == nil || state == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if s.sdkStore == nil {
		s.sdkStore = agentsdk.NewMemorySessionStore()
	}
	sessionID := s.SessionID()
	if err := s.sdkStore.Save(ctx, sessionID, state); err != nil {
		return err
	}
	s.history = sdkHistoryToMessages(state.Messages, s.maxMemory)
	return nil
}

func (s *Session) LoadState(ctx context.Context) (*agentsdk.SessionState, error) {
	if s == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if s.sdkStore == nil {
		if len(s.history) == 0 {
			return nil, nil
		}
		return &agentsdk.SessionState{Messages: s.history}, nil
	}
	state, err := s.sdkStore.Load(ctx, s.SessionID())
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, nil
	}
	if len(state.Messages) == 0 && state.ActiveRun == nil && len(state.Runs) == 0 {
		if len(s.history) == 0 {
			return nil, nil
		}
		return &agentsdk.SessionState{Messages: s.history}, nil
	}
	return state, nil
}

func (s *Session) ResumeActiveRun(ctx context.Context, showReply bool) (string, bool, error) {
	if s == nil || s.sdkStore == nil {
		return "", false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	sessionID := s.SessionID()
	state, err := s.sdkStore.Load(ctx, sessionID)
	if err != nil {
		return "", false, err
	}
	if state == nil || state.ActiveRun == nil {
		return "", false, nil
	}
	if err := s.ensureSDKAgent(); err != nil {
		return "", false, err
	}

	ui.Status(s.out, "Resuming interrupted run from the selected session.")
	ui.Thinking(s.out, "resuming interrupted run")
	resp, err := s.sdkAgent.RunWithEvents(ctx, agentsdk.Request{
		SessionID: sessionID,
	}, &sessionEventSink{session: s, trackRun: true})
	if err != nil {
		if errors.Is(err, agentsdk.ErrMaxIterations) {
			limit := s.maxSteps
			if limit <= 0 {
				limit = defaultSDKMaxIterations
			}
			ui.Warn(s.out, fmt.Sprintf("reached configured max steps (%d)", limit))
		}
		return "", true, err
	}

	reply := strings.TrimSpace(resp.Output)
	if reply == "" {
		reply = "I do not have a response yet. Please refine your request."
	}
	ui.Thinking(s.out, "complete")
	if showReply {
		ui.Assistant(s.out, reply)
	}
	s.history = s.History()
	return reply, true, nil
}

func (s *Session) History() []agentsdk.Message {
	if s.sdkStore == nil {
		out := make([]agentsdk.Message, len(s.history))
		copy(out, s.history)
		return out
	}
	state, err := s.sdkStore.Load(context.Background(), s.sessionID)
	if err != nil || state == nil {
		out := make([]agentsdk.Message, len(s.history))
		copy(out, s.history)
		return out
	}
	history := sdkHistoryToMessages(state.Messages, s.maxMemory)
	s.history = history
	out := make([]agentsdk.Message, len(history))
	copy(out, history)
	return out
}

func (s *Session) Ask(ctx context.Context, userInput string) (string, error) {
	userInput = strings.TrimSpace(userInput)
	if userInput == "" {
		return "", errors.New("prompt cannot be empty")
	}
	requiresOperationalTools := likelyNeedsTooling(userInput)
	requiresCorrelationDelegation := likelyNeedsCorrelationDelegation(userInput)
	if s.showUserInput {
		ui.User(s.out, userInput)
	}
	if err := s.ensureSDKAgent(); err != nil {
		return "", err
	}
	ui.Thinking(s.out, "thinking")
	reply, err := s.askWithSDKGuardrails(
		ctx,
		userInput,
		requiresOperationalTools,
		requiresCorrelationDelegation,
	)
	if err != nil {
		return "", err
	}
	ui.Thinking(s.out, "complete")
	ui.Assistant(s.out, reply)
	s.history = s.History()
	return reply, nil
}

type sdkRunStats struct {
	ActionRuns         int
	OperationalActions int
	CommandActions     int
	SubAgentActions    int
}

func (s *Session) askWithSDKGuardrails(
	ctx context.Context,
	userInput string,
	requiresOperationalTools bool,
	requiresCorrelationDelegation bool,
) (string, error) {
	currentInput := userInput
	reply := ""
	cumulative := sdkRunStats{}

	for attempt := 1; attempt <= 4; attempt++ {
		resp, err := s.sdkAgent.RunWithEvents(ctx, agentsdk.Request{
			SessionID: s.sessionID,
			Input:     currentInput,
		}, &sessionEventSink{session: s, trackRun: true})
		if err != nil {
			if errors.Is(err, agentsdk.ErrMaxIterations) {
				limit := s.maxSteps
				if limit <= 0 {
					limit = defaultSDKMaxIterations
				}
				ui.Warn(s.out, fmt.Sprintf("reached configured max steps (%d)", limit))
			}
			return "", err
		}

		reply = strings.TrimSpace(resp.Output)
		stats := summarizeSDKRun(resp.Messages)
		cumulative.ActionRuns += stats.ActionRuns
		cumulative.OperationalActions += stats.OperationalActions
		cumulative.CommandActions += stats.CommandActions
		cumulative.SubAgentActions += stats.SubAgentActions

		followUp, status := sdkGuardrailFollowUp(
			attempt,
			reply,
			cumulative,
			requiresOperationalTools,
			requiresCorrelationDelegation,
		)
		if strings.TrimSpace(followUp) == "" {
			break
		}
		if status != "" {
			ui.Thinking(s.out, status)
		}
		currentInput = followUp
	}

	reply = strings.TrimSpace(reply)
	if reply == "" {
		reply = "I do not have a response yet. Please refine your request."
	}
	return reply, nil
}

func summarizeSDKRun(messages []agentsdk.Message) sdkRunStats {
	stats := sdkRunStats{}
	toolNamesByID := make(map[string]string)
	for _, msg := range messages {
		if msg.Role == agentsdk.RoleAssistant {
			for _, call := range msg.ToolCalls {
				name := strings.ToLower(strings.TrimSpace(call.Name))
				if name == "" && call.Function != nil {
					name = strings.ToLower(strings.TrimSpace(call.Function.Name))
				}
				if name == "" || strings.TrimSpace(call.ID) == "" {
					continue
				}
				toolNamesByID[call.ID] = name
			}
			continue
		}
		if msg.Role != agentsdk.RoleTool {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(toolNamesByID[msg.ToolCallID]))
		if name == "" {
			continue
		}
		stats.ActionRuns++
		switch name {
		case "run_command":
			stats.OperationalActions++
			stats.CommandActions++
		case "spawn_sub_agent", "spawn_sub_agents":
			stats.OperationalActions++
			stats.SubAgentActions++
		}
	}
	return stats
}

func sdkGuardrailFollowUp(
	attempt int,
	reply string,
	stats sdkRunStats,
	requiresOperationalTools bool,
	requiresCorrelationDelegation bool,
) (string, string) {
	if looksLikeClarifyingQuestion(reply) {
		return "", ""
	}
	if requiresOperationalTools && stats.OperationalActions == 0 && attempt <= 1 {
		return "For this request, run at least one diagnostic tool call before finalizing " +
				"(run_command or spawn_sub_agents). Status-only updates are not sufficient.",
			"requesting concrete tool execution"
	}
	if requiresCorrelationDelegation && stats.CommandActions == 0 && attempt <= 2 {
		return "For correlation requests, gather concrete evidence first with run_command " +
				"(for example recent commits and provider/runtime error signals). Then continue.",
			"requesting correlation evidence collection commands"
	}
	if requiresCorrelationDelegation && stats.CommandActions > 0 && stats.SubAgentActions == 0 && attempt <= 3 {
		return "Now delegate correlation synthesis via spawn_sub_agent (or spawn_sub_agents). " +
				"Pass a focused goal and include evidence from gathered command outputs " +
				"so the sub-agent can identify likely commit/deploy suspects.",
			"requesting delegated correlation synthesis"
	}
	return "", ""
}

func (s *Session) executeCommandAction(ctx context.Context, action agentAction) (string, error) {
	if reason := strings.TrimSpace(action.Reason); reason != "" {
		ui.Reason(s.out, reason)
	}

	command := strings.TrimSpace(action.Command)
	if command == "" {
		return "", errors.New("run_command action requires command")
	}
	if isBlockedCommand(command) {
		return "blocked by safety policy", nil
	}

	cwd, err := s.resolveCommandCWD(action.Cwd)
	if err != nil {
		return "", err
	}
	if err := s.enforceCommandAnchoring(command, cwd); err != nil {
		return "", err
	}
	timeoutSec := action.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 20
	}

	if isAutoApprovedLowRiskCommand(command) {
		ui.Thinking(s.out, fmt.Sprintf("auto-approved low-risk command: %s", commandPolicyName(command)))
	} else {
		if s.approval == nil {
			return "", errors.New("approval manager is not configured")
		}
		if s.approval.AutoApproveEnabled() {
			ui.Warn(s.out, "dangerous flow: auto-approval enabled; running command without approval prompt")
		}
		approved, err := s.approval.Request(ctx, command, cwd, timeoutSec)
		if err != nil {
			return "", err
		}
		if !approved {
			ui.Warn(s.out, "command rejected by user policy")
			return "command rejected", nil
		}
	}

	ui.RunningCommand(s.out, command)
	result, err := runShellCommand(ctx, command, cwd, time.Duration(timeoutSec)*time.Second, s.commandEnvVars())
	if err != nil {
		return "", err
	}
	return formatCommandResult(result), nil
}

type commandResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Duration time.Duration
}

func runShellCommand(
	ctx context.Context,
	command string,
	cwd string,
	timeout time.Duration,
	envVars map[string]string,
) (commandResult, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "/bin/sh", "-lc", command)
	cmd.Dir = cwd
	if len(envVars) > 0 {
		envMap := map[string]string{}
		for _, item := range os.Environ() {
			parts := strings.SplitN(item, "=", 2)
			if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
				continue
			}
			value := ""
			if len(parts) == 2 {
				value = parts[1]
			}
			envMap[parts[0]] = value
		}
		for key, value := range envVars {
			envMap[key] = value
		}

		keys := make([]string, 0, len(envMap))
		for key := range envMap {
			keys = append(keys, key)
		}
		sort.Strings(keys)

		env := make([]string, 0, len(keys))
		for _, key := range keys {
			env = append(env, fmt.Sprintf("%s=%s", key, envMap[key]))
		}
		cmd.Env = env
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err := cmd.Run()
	duration := time.Since(start)

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return commandResult{}, err
		}
	}

	return commandResult{
		ExitCode: exitCode,
		Stdout:   trimOutput(stdout.String()),
		Stderr:   trimOutput(stderr.String()),
		Duration: duration,
	}, nil
}

func (s *Session) commandEnvVars() map[string]string {
	if s == nil || s.cfg == nil {
		return nil
	}
	cloud := strings.ToLower(strings.TrimSpace(s.cfg.CloudProvider))
	if cloud != "aws" {
		return nil
	}
	profile := strings.TrimSpace(s.cfg.AWSProfile)
	if profile == "" {
		return nil
	}
	return map[string]string{
		"AWS_PROFILE":         profile,
		"AWS_DEFAULT_PROFILE": profile,
	}
}

func (s *Session) agentSystemPrompt() string {
	return mergeSystemPrompt(prompts.AgentSystem, s.runtimeContextPrompt())
}

func (s *Session) subAgentSystemPrompt() string {
	return mergeSystemPrompt(prompts.SubAgentSystem, s.runtimeContextPrompt())
}

func mergeSystemPrompt(base, runtime string) string {
	base = strings.TrimSpace(base)
	runtime = strings.TrimSpace(runtime)
	if runtime == "" {
		return base
	}
	if base == "" {
		return runtime
	}
	return base + "\n\n" + runtime
}

func (s *Session) runtimeContextPrompt() string {
	if s == nil || s.cfg == nil {
		return ""
	}
	llmProvider := strings.TrimSpace(s.cfg.LLMProvider)
	if llmProvider == "" {
		llmProvider = "unknown"
	}
	llmModel := strings.TrimSpace(s.cfg.LLMModel)
	if llmModel == "" {
		llmModel = "unknown"
	}
	reasoning := strings.TrimSpace(s.cfg.ReasoningEffort)
	if reasoning == "" {
		reasoning = "medium"
	}
	cloud := strings.TrimSpace(s.cfg.CloudProvider)
	if cloud == "" {
		cloud = "unknown"
	}
	profile := strings.TrimSpace(s.cfg.AWSProfile)
	workspace := s.workspaceRoot
	if workspace == "" {
		workspace = "."
	}
	runbook := s.runbookRoot
	if runbook == "" {
		runbook = workspace
	}

	var builder strings.Builder
	builder.WriteString("<runtime_context>\n")
	builder.WriteString("Session defaults already configured by setup:\n")
	builder.WriteString("- llm_provider: ")
	builder.WriteString(llmProvider)
	builder.WriteByte('\n')
	builder.WriteString("- llm_model: ")
	builder.WriteString(llmModel)
	builder.WriteByte('\n')
	builder.WriteString("- reasoning_effort: ")
	builder.WriteString(reasoning)
	builder.WriteByte('\n')
	builder.WriteString("- cloud_provider: ")
	builder.WriteString(cloud)
	builder.WriteByte('\n')
	if profile != "" {
		builder.WriteString("- aws_profile: ")
		builder.WriteString(profile)
		builder.WriteByte('\n')
	}
	builder.WriteString("- workspace_root: ")
	builder.WriteString(workspace)
	builder.WriteByte('\n')
	builder.WriteString("Keep normal operations within workspace_root.\n")
	builder.WriteString("- runbook_root: ")
	builder.WriteString(runbook)
	builder.WriteByte('\n')
	builder.WriteString("Use runbook_root as the primary location when searching for incident runbooks.\n")
	builder.WriteString("Runbooks installed via `nwatch runbook install` are stored under runbook_root.\n")
	builder.WriteString("run_command paths and cwd may target workspace_root or runbook_root.\n")
	builder.WriteString("\n<runbook_context>\n")
	builder.WriteString("For incidents, use runbook guidance first before broad diagnostics.\n")
	builder.WriteString("Runbooks are not pre-scanned. Use run_command to locate runbook folders/files in runbook_root (markdown files and runbook directories), then follow the discovered guidance.\n")
	builder.WriteString("</runbook_context>\n")
	builder.WriteString("Use these defaults directly. Do not ask the user to restate them unless the user asks to override them or a required value is missing.\n")
	builder.WriteString("</runtime_context>")
	return builder.String()
}

func trimOutput(output string) string {
	output = strings.TrimSpace(output)
	if len(output) > 12000 {
		return output[:12000] + "\n...[truncated]"
	}
	return output
}

func formatCommandResult(result commandResult) string {
	return fmt.Sprintf(
		"command complete (exit=%d, duration=%s)\nstdout:\n%s\nstderr:\n%s",
		result.ExitCode,
		result.Duration.Truncate(time.Millisecond).String(),
		emptyIfBlank(result.Stdout),
		emptyIfBlank(result.Stderr),
	)
}

func emptyIfBlank(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(empty)"
	}
	return value
}

func resolveWorkspaceRoot(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		root = "."
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return filepath.Clean(root)
}

func (s *Session) resolveCommandCWD(path string) (string, error) {
	if workspaceCWD, err := s.resolvePathInWorkspace(path); err == nil {
		return workspaceCWD, nil
	}
	return s.resolvePathInRunbookRoot(path)
}

func (s *Session) resolvePathInRunbookRoot(path string) (string, error) {
	root := strings.TrimSpace(s.runbookRoot)
	if root == "" {
		root = s.workspaceRoot
	}
	root = resolveWorkspaceRoot(root)

	path = strings.TrimSpace(path)
	if path == "" || path == "." {
		return root, nil
	}

	var resolved string
	if filepath.IsAbs(path) {
		resolved = filepath.Clean(path)
	} else {
		resolved = filepath.Clean(filepath.Join(root, path))
	}
	if !isPathWithinWorkspace(root, resolved) {
		return "", fmt.Errorf("path %q is outside runbook root %q", resolved, root)
	}
	return resolved, nil
}

func (s *Session) enforceCommandAnchoring(command string, cwd string) error {
	command = strings.TrimSpace(command)
	if command == "" {
		return errors.New("command is required")
	}

	cwd = resolveWorkspaceRoot(cwd)
	allowedRoots := s.commandAllowedRoots()
	if !isPathWithinAnyRoot(cwd, allowedRoots) {
		return fmt.Errorf("cwd %q is outside allowed roots: %s", cwd, strings.Join(allowedRoots, ", "))
	}

	tokens := tokenizeShellCommand(command)
	if len(tokens) == 0 {
		return errors.New("command has no executable tokens")
	}

	// Validate every path-like token (including --flag=/path values), not just cwd.
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token == "" || isShellOperatorToken(token) {
			continue
		}

		if inlineValue, ok := splitInlineOptionValue(token); ok {
			if err := enforcePathTokenWithinRoots(inlineValue, cwd, allowedRoots); err != nil {
				return err
			}
			continue
		}

		if err := enforcePathTokenWithinRoots(token, cwd, allowedRoots); err != nil {
			return err
		}
	}
	return nil
}

func (s *Session) commandAllowedRoots() []string {
	roots := make([]string, 0, 2)
	seen := map[string]struct{}{}
	addRoot := func(root string) {
		root = resolveWorkspaceRoot(root)
		if root == "" {
			return
		}
		if _, exists := seen[root]; exists {
			return
		}
		seen[root] = struct{}{}
		roots = append(roots, root)
	}

	addRoot(s.workspaceRoot)
	runbookRoot := strings.TrimSpace(s.runbookRoot)
	if runbookRoot == "" {
		runbookRoot = s.workspaceRoot
	}
	addRoot(runbookRoot)
	if len(roots) == 0 {
		addRoot(".")
	}
	return roots
}

func enforcePathTokenWithinRoots(token string, cwd string, roots []string) error {
	resolvedPath, hasPath, err := resolvePathTokenForAnchoring(token, cwd)
	if err != nil {
		return err
	}
	if !hasPath {
		return nil
	}
	// Some provider/resource identifiers look like absolute paths (for example /aws/... log groups).
	// Skip anchoring checks for those non-filesystem literals to avoid false positives.
	if filepath.IsAbs(resolvedPath) && !isLikelyFilesystemAbsolutePath(resolvedPath, roots) {
		return nil
	}
	if !isPathWithinAnyRoot(resolvedPath, roots) {
		return fmt.Errorf("path %q is outside allowed roots: %s", resolvedPath, strings.Join(roots, ", "))
	}
	return nil
}

func resolvePathTokenForAnchoring(token string, cwd string) (string, bool, error) {
	token = strings.TrimSpace(token)
	if token == "" || isShellOperatorToken(token) {
		return "", false, nil
	}
	if strings.Contains(token, "://") {
		return "", false, nil
	}
	if !isLikelyPathToken(token) {
		return "", false, nil
	}
	// Dynamic shell expansion is not safely analyzable here, so reject path-like dynamic tokens.
	if strings.Contains(token, "$") || strings.Contains(token, "`") {
		return "", true, fmt.Errorf("dynamic path token %q is not allowed when anchoring commands", token)
	}

	if strings.HasPrefix(token, "~") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", true, fmt.Errorf("cannot resolve home directory for token %q: %w", token, err)
		}
		if token == "~" {
			return filepath.Clean(homeDir), true, nil
		}
		if strings.HasPrefix(token, "~/") {
			return filepath.Clean(filepath.Join(homeDir, strings.TrimPrefix(token, "~/"))), true, nil
		}
		return "", true, fmt.Errorf("unsupported tilde path token %q", token)
	}

	if filepath.IsAbs(token) {
		return filepath.Clean(token), true, nil
	}
	return filepath.Clean(filepath.Join(cwd, token)), true, nil
}

func isLikelyPathToken(token string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	if token == "." || token == ".." {
		return true
	}
	if filepath.IsAbs(token) {
		return true
	}
	if strings.HasPrefix(token, "~/") || token == "~" {
		return true
	}
	if strings.HasPrefix(token, "./") || strings.HasPrefix(token, "../") {
		return true
	}
	if strings.Contains(token, "/") {
		return true
	}
	if len(token) >= 3 && token[1] == ':' && (token[2] == '\\' || token[2] == '/') {
		return true
	}
	return false
}

func isPathWithinAnyRoot(target string, roots []string) bool {
	for _, root := range roots {
		if isPathWithinWorkspace(root, target) {
			return true
		}
	}
	return false
}

func isLikelyFilesystemAbsolutePath(path string, allowedRoots []string) bool {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return false
	}
	if isPathWithinAnyRoot(path, allowedRoots) {
		return true
	}

	trimmed := strings.TrimPrefix(path, string(os.PathSeparator))
	if trimmed == "" {
		return true
	}
	firstSegment := trimmed
	if idx := strings.IndexRune(trimmed, rune(os.PathSeparator)); idx >= 0 {
		firstSegment = trimmed[:idx]
	}
	probeRoot := string(os.PathSeparator) + firstSegment
	_, err := os.Stat(probeRoot)
	return err == nil
}

func splitInlineOptionValue(token string) (string, bool) {
	if !strings.HasPrefix(token, "-") {
		return "", false
	}
	idx := strings.Index(token, "=")
	if idx <= 0 || idx == len(token)-1 {
		return "", false
	}
	return strings.TrimSpace(token[idx+1:]), true
}

func tokenizeShellCommand(command string) []string {
	var tokens []string
	var current strings.Builder
	inSingle := false
	inDouble := false
	escaped := false

	flush := func() {
		if current.Len() == 0 {
			return
		}
		tokens = append(tokens, current.String())
		current.Reset()
	}

	for i := 0; i < len(command); i++ {
		ch := command[i]
		if escaped {
			current.WriteByte(ch)
			escaped = false
			continue
		}
		if inSingle {
			if ch == '\'' {
				inSingle = false
			} else {
				current.WriteByte(ch)
			}
			continue
		}
		if inDouble {
			switch ch {
			case '"':
				inDouble = false
			case '\\':
				if i+1 < len(command) {
					i++
					current.WriteByte(command[i])
				}
			default:
				current.WriteByte(ch)
			}
			continue
		}

		switch ch {
		case '\\':
			escaped = true
		case '\'':
			inSingle = true
		case '"':
			inDouble = true
		case ' ', '\t', '\n', '\r':
			flush()
		default:
			// Keep shell operators as standalone tokens so callers can reason about pipelines/redirection.
			if operator, size, ok := readShellOperator(command[i:]); ok {
				flush()
				tokens = append(tokens, operator)
				i += size - 1
				continue
			}
			current.WriteByte(ch)
		}
	}

	flush()
	return tokens
}

func readShellOperator(remaining string) (string, int, bool) {
	switch {
	case strings.HasPrefix(remaining, "&&"):
		return "&&", 2, true
	case strings.HasPrefix(remaining, "||"):
		return "||", 2, true
	case strings.HasPrefix(remaining, ">>"):
		return ">>", 2, true
	case strings.HasPrefix(remaining, "<<"):
		return "<<", 2, true
	case strings.HasPrefix(remaining, "&>"):
		return "&>", 2, true
	}
	if remaining == "" {
		return "", 0, false
	}
	switch remaining[0] {
	case '|', '&', ';', '(', ')', '<', '>':
		return string(remaining[0]), 1, true
	default:
		return "", 0, false
	}
}

func isShellOperatorToken(token string) bool {
	switch strings.TrimSpace(token) {
	case "|", "||", "&", "&&", ";", "(", ")", "<", "<<", ">", ">>", "&>":
		return true
	default:
		return false
	}
}

var lowRiskAutoApproveCommands = map[string]struct{}{
	"ls":     {},
	"pwd":    {},
	"whoami": {},
	"date":   {},
	"which":  {},
}

func isAutoApprovedLowRiskCommand(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	if hasShellControlOperators(command) {
		return false
	}
	name := commandPolicyName(command)
	if name == "" {
		return false
	}
	_, ok := lowRiskAutoApproveCommands[name]
	return ok
}

func hasShellControlOperators(command string) bool {
	for _, marker := range []string{
		"\n",
		"\r",
		";",
		"|",
		"&",
		">",
		"<",
		"`",
		"$(",
	} {
		if strings.Contains(command, marker) {
			return true
		}
	}
	return false
}

func (s *Session) resolvePathInWorkspace(path string) (string, error) {
	root := resolveWorkspaceRoot(s.workspaceRoot)
	path = strings.TrimSpace(path)
	if path == "" || path == "." {
		return root, nil
	}

	var resolved string
	if filepath.IsAbs(path) {
		resolved = filepath.Clean(path)
	} else {
		resolved = filepath.Clean(filepath.Join(root, path))
	}
	if !isPathWithinWorkspace(root, resolved) {
		return "", fmt.Errorf("path %q is outside workspace root %q", resolved, root)
	}
	return resolved, nil
}

func isPathWithinWorkspace(root, target string) bool {
	root = resolveWorkspaceRoot(root)
	target = resolveWorkspaceRoot(target)
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	rel = strings.TrimSpace(rel)
	if rel == "." || rel == "" {
		return true
	}
	if rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

var blockedCommandPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\brm\s+-rf\s+/`),
	regexp.MustCompile(`(?i)\bgit\s+reset\s+--hard\b`),
	regexp.MustCompile(`(?i)\bshutdown\b`),
	regexp.MustCompile(`(?i)\breboot\b`),
}

func isBlockedCommand(command string) bool {
	for _, pattern := range blockedCommandPatterns {
		if pattern.MatchString(command) {
			return true
		}
	}
	return false
}

func stringParam(params map[string]interface{}, key string) string {
	if params == nil {
		return ""
	}
	value, ok := params[key]
	if !ok {
		return ""
	}
	return strings.TrimSpace(fmt.Sprintf("%v", value))
}

func intParam(params map[string]interface{}, key string, fallback int) int {
	if params == nil {
		return fallback
	}
	value, ok := params[key]
	if !ok {
		return fallback
	}
	switch typed := value.(type) {
	case float64:
		if typed > 0 {
			return int(typed)
		}
	case int:
		if typed > 0 {
			return typed
		}
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func likelyNeedsTooling(input string) bool {
	normalized := strings.ToLower(strings.TrimSpace(input))
	if normalized == "" {
		return false
	}
	keywords := []string{
		"log", "logs", "error", "errors", "exception", "exceptions", "incident",
		"failure", "failures", "outage", "root cause", "correlat", "commit",
		"cloudwatch", "sentry", "aws", "gcp", "deploy", "deployment",
	}
	for _, token := range keywords {
		if strings.Contains(normalized, token) {
			return true
		}
	}
	return false
}

func likelyNeedsCorrelationDelegation(input string) bool {
	normalized := strings.ToLower(strings.TrimSpace(input))
	if normalized == "" {
		return false
	}
	keywords := []string{
		"correlat", "commit", "suspect commit", "blame", "deploy", "deployment",
		"regression", "which change", "which commit", "root cause",
	}
	for _, token := range keywords {
		if strings.Contains(normalized, token) {
			return true
		}
	}
	return false
}

func looksLikeClarifyingQuestion(reply string) bool {
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return false
	}
	if !strings.Contains(reply, "?") {
		return false
	}
	normalized := strings.ToLower(reply)
	questionHints := []string{
		"which ", "what ", "where ", "when ", "how ", "can you", "could you",
		"please provide", "provide", "specify", "confirm",
	}
	for _, hint := range questionHints {
		if strings.Contains(normalized, hint) {
			return true
		}
	}
	return false
}

func summarizeInline(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.Join(strings.Fields(value), " ")
	if maxRunes <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	if maxRunes <= 3 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-3]) + "..."
}

func newSessionID() string {
	return fmt.Sprintf("session_%d", time.Now().UTC().UnixNano())
}

func normalizeHistory(messages []agentsdk.Message, maxMemory int) []agentsdk.Message {
	var normalized []agentsdk.Message
	for _, msg := range messages {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}
		if role != "user" && role != "assistant" {
			continue
		}
		normalized = append(normalized, agentsdk.Message{
			Role:    role,
			Content: content,
		})
	}
	if maxMemory > 0 && len(normalized) > maxMemory {
		normalized = normalized[len(normalized)-maxMemory:]
	}
	return normalized
}
