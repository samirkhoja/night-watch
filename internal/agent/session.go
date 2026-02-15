package agent

import (
	"bytes"
	"context"
	"encoding/json"
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
	"time"

	"github.com/samirkhoja/night-watch/internal/config"
	"github.com/samirkhoja/night-watch/internal/llm"
	"github.com/samirkhoja/night-watch/internal/prompts"
	"github.com/samirkhoja/night-watch/internal/ui"
)

type Session struct {
	client           llm.Client
	compactionClient llm.Client
	cfg              *config.Config
	approval         *ApprovalManager
	out              io.Writer
	workspaceRoot    string
	runbookRoot      string
	history          []llm.Message
	maxMemory        int
	modelMaxTokens   int
	replyMaxTokens   int
	maxSteps         int
	showUserInput    bool
}

type assistantPlan struct {
	Reply   string        `json:"reply"`
	Actions []agentAction `json:"actions"`
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
	client llm.Client,
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
		client:         client,
		cfg:            cfg,
		approval:       approval,
		out:            out,
		workspaceRoot:  resolveWorkspaceRoot(workspaceRoot),
		runbookRoot:    resolveWorkspaceRoot(workspaceRoot),
		maxMemory:      80,
		modelMaxTokens: modelMaxTokens,
		replyMaxTokens: replyMaxTokens,
		showUserInput:  true,
	}
}

func (s *Session) Reset() {
	s.history = nil
}

func (s *Session) SetShowUserInput(enabled bool) {
	s.showUserInput = enabled
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
}

func (s *Session) SetRunbookRoot(root string) {
	root = strings.TrimSpace(root)
	if root == "" {
		s.runbookRoot = s.workspaceRoot
		return
	}
	s.runbookRoot = resolveWorkspaceRoot(root)
}

func (s *Session) SetCompactionClient(client llm.Client) {
	s.compactionClient = client
}

func (s *Session) SetHistory(messages []llm.Message) {
	history := normalizeHistory(messages, s.maxMemory)
	compacted, _ := s.compactMessagesForBudget(history)
	s.history = compacted
}

func (s *Session) SetMaxSteps(maxSteps int) {
	if maxSteps < 0 {
		maxSteps = 0
	}
	s.maxSteps = maxSteps
}

func (s *Session) History() []llm.Message {
	out := make([]llm.Message, len(s.history))
	copy(out, s.history)
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
	conversation := append([]llm.Message{}, s.history...)
	conversation = append(conversation, llm.Message{
		Role:    "user",
		Content: userInput,
	})

	loopResult, err := s.runActionLoop(ctx, actionLoopConfig{
		SystemPrompt:        s.agentSystemPrompt(),
		InitialConversation: conversation,
		MaxSteps:            s.maxSteps,
		MaxTokens:           s.replyMaxTokens,
		EmptyReply:          "I do not have a response yet. Please refine your request.",
		ExecuteAction:       s.executeAction,
		Hooks: actionLoopHooks{
			BeforeStep: func(step int, messages []llm.Message) ([]llm.Message, error) {
				compacted, didCompact := s.compactMessagesForBudgetWithContext(ctx, messages)
				if didCompact {
					ui.Status(s.out, "compacting conversation to stay within model token budget")
				}
				return compacted, nil
			},
			BeforeGenerate: func(step int) {
				ui.Thinking(s.out, fmt.Sprintf("thinking (step %d)", step))
			},
			OnReasoning: func(reason string) {
				ui.Reasoning(s.out, reason)
			},
			BeforeFinalize: func(
				step int,
				reply string,
				plan assistantPlan,
				state *actionLoopState,
				messages *[]llm.Message,
				modelResp llm.GenerateResponse,
			) (bool, string) {
				if requiresOperationalTools &&
					state.OperationalActions == 0 &&
					step <= 3 &&
					!looksLikeClarifyingQuestion(reply) {
					ui.Thinking(s.out, "requesting concrete tool execution")
					*messages = appendAssistantFollowUp(
						*messages,
						modelResp,
						"For this request, run at least one diagnostic tool call before finalizing "+
							"(run_command or spawn_sub_agents). "+
							"Status-only updates are not sufficient.",
					)
					return true, ""
				}
				if requiresCorrelationDelegation &&
					state.CommandActions == 0 &&
					step <= 4 &&
					!looksLikeClarifyingQuestion(reply) {
					ui.Thinking(s.out, "requesting correlation evidence collection commands")
					*messages = appendAssistantFollowUp(
						*messages,
						modelResp,
						"For correlation requests, gather concrete evidence first with run_command "+
							"(for example recent commits and provider/runtime error signals). "+
							"Then continue.",
					)
					return true, ""
				}
				if requiresCorrelationDelegation &&
					state.CommandActions > 0 &&
					state.SubAgentActions == 0 &&
					step <= 5 &&
					!looksLikeClarifyingQuestion(reply) {
					ui.Thinking(s.out, "requesting delegated correlation synthesis")
					*messages = appendAssistantFollowUp(
						*messages,
						modelResp,
						"Now delegate correlation synthesis via spawn_sub_agent (or spawn_sub_agents). "+
							"Pass a focused goal and include evidence from gathered command outputs "+
							"so the sub-agent can identify likely commit/deploy suspects.",
					)
					return true, ""
				}
				return false, ""
			},
			BeforeActions: func(step int, actions []agentAction) {
				ui.Thinking(s.out, fmt.Sprintf("executing %d action(s)", len(actions)))
			},
			BeforeAction: func(step int, index int, total int, action agentAction) {
				ui.Thinking(s.out, fmt.Sprintf("action %d/%d: %s", index+1, total, actionLabel(action)))
			},
		},
	})
	if err != nil {
		return "", err
	}

	reply := strings.TrimSpace(loopResult.Reply)
	if reply == "" {
		reply = "I do not have a response yet. Please refine your request."
	}
	if s.maxSteps > 0 && loopResult.ReachedMaxSteps {
		ui.Warn(s.out, fmt.Sprintf("reached configured max steps (%d)", s.maxSteps))
	}
	ui.Thinking(s.out, "complete")
	ui.Assistant(s.out, reply)
	s.appendHistory(userInput, reply)
	return reply, nil
}

func (s *Session) executeAction(ctx context.Context, action agentAction) (string, error) {
	switch strings.ToLower(strings.TrimSpace(action.Type)) {
	case "status":
		msg := strings.TrimSpace(action.Message)
		if msg == "" {
			msg = "working..."
		}
		ui.Status(s.out, msg)
		return msg, nil
	case "run_command":
		return s.executeCommandAction(ctx, action)
	case "spawn_sub_agents", "spawn_sub_agent":
		return s.executeSubAgentsAction(ctx, action)
	default:
		return "", fmt.Errorf("unsupported action type: %s", action.Type)
	}
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

func (s *Session) appendHistory(userInput, assistantReply string) {
	s.history = append(s.history, llm.Message{
		Role:    "user",
		Content: userInput,
	})
	s.history = append(s.history, llm.Message{
		Role:    "assistant",
		Content: assistantReply,
	})
	s.history = normalizeHistory(s.history, s.maxMemory)
	compacted, didCompact := s.compactMessagesForBudget(s.history)
	s.history = compacted
	if didCompact {
		ui.Status(s.out, "compacted session history for future turns")
	}
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

func planFromGenerateResponse(response llm.GenerateResponse) assistantPlan {
	plan := assistantPlan{
		Reply:   strings.TrimSpace(response.Reply),
		Actions: make([]agentAction, 0, len(response.ToolCalls)),
	}

	for _, call := range response.ToolCalls {
		actionType := strings.ToLower(strings.TrimSpace(call.Name))
		args := cloneParams(call.Arguments)
		action := agentAction{
			Type: actionType,
		}

		switch actionType {
		case "status":
			action.Message = stringParam(args, "message")
		case "run_command":
			action.Reason = firstNonEmpty(
				stringParam(args, "reason"),
				stringParam(args, "why"),
				stringParam(args, "rationale"),
			)
			action.Command = stringParam(args, "command")
			action.Cwd = stringParam(args, "cwd")
			action.TimeoutSec = intParam(args, "timeout_sec", 0)
		case "spawn_sub_agents", "spawn_sub_agent":
			action.Params = args
		default:
			action.Params = args
		}

		plan.Actions = append(plan.Actions, action)
	}

	return plan
}

func formatAssistantTurn(response llm.GenerateResponse) string {
	var builder strings.Builder

	reply := strings.TrimSpace(response.Reply)
	if reply != "" {
		builder.WriteString(reply)
	}

	if len(response.ToolCalls) > 0 {
		if builder.Len() > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString("Tool calls:\n")
		for i, call := range response.ToolCalls {
			builder.WriteString(fmt.Sprintf("%d. %s", i+1, strings.TrimSpace(call.Name)))
			if len(call.Arguments) > 0 {
				args, err := json.Marshal(call.Arguments)
				if err == nil {
					builder.WriteString(" ")
					builder.WriteString(string(args))
				}
			}
			builder.WriteByte('\n')
		}
	}

	formatted := strings.TrimSpace(builder.String())
	if formatted == "" {
		return "(no assistant output)"
	}
	return formatted
}

func cloneParams(params map[string]interface{}) map[string]interface{} {
	if len(params) == 0 {
		return nil
	}
	out := make(map[string]interface{}, len(params))
	for key, value := range params {
		out[key] = value
	}
	return out
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

func onlyStatusActions(actions []agentAction) bool {
	if len(actions) == 0 {
		return true
	}
	for _, action := range actions {
		if strings.ToLower(strings.TrimSpace(action.Type)) != "status" {
			return false
		}
	}
	return true
}

func actionLabel(action agentAction) string {
	switch strings.ToLower(strings.TrimSpace(action.Type)) {
	case "status":
		if msg := strings.TrimSpace(action.Message); msg != "" {
			return msg
		}
		return "status update"
	case "run_command":
		if cmd := strings.TrimSpace(action.Command); cmd != "" {
			return summarizeInline(cmd, 56)
		}
		return "run command"
	case "spawn_sub_agents", "spawn_sub_agent":
		return "spawn sub-agents"
	default:
		if action.Type == "" {
			return "action"
		}
		return action.Type
	}
}

func isOperationalAction(actionType string) bool {
	switch strings.ToLower(strings.TrimSpace(actionType)) {
	case "run_command", "spawn_sub_agents", "spawn_sub_agent":
		return true
	default:
		return false
	}
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

func normalizeHistory(messages []llm.Message, maxMemory int) []llm.Message {
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
	if maxMemory > 0 && len(normalized) > maxMemory {
		normalized = normalized[len(normalized)-maxMemory:]
	}
	return normalized
}
