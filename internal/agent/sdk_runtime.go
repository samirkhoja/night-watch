package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	agentsdk "github.com/samirkhoja/agent-sdk"
	"github.com/samirkhoja/night-watch/internal/ui"
)

const defaultSDKMaxIterations = 32

type sessionEventSink struct {
	session *Session
	prefix  string
	trackRun bool
}

func (s *sessionEventSink) HandleEvent(ctx context.Context, event agentsdk.RunEvent) error {
	_ = ctx
	if s == nil || s.session == nil {
		return nil
	}
	prefix := strings.TrimSpace(s.prefix)
	if prefix != "" {
		prefix += " "
	}
	switch event.Type {
	case agentsdk.EventRunStarted, agentsdk.EventRunResumed:
		if s.trackRun && strings.TrimSpace(event.RunID) != "" {
			s.session.setCurrentRunID(event.RunID)
		}
	case agentsdk.EventCompactionStarted:
		ui.Thinking(s.session.out, prefix+"compacting conversation to stay within model token budget")
	case agentsdk.EventToolProgress:
		if event.Progress != nil && strings.TrimSpace(event.Progress.Message) != "" {
			ui.Thinking(s.session.out, prefix+event.Progress.Message)
		}
	}
	return nil
}

type statusTool struct {
	out io.Writer
}

func (t statusTool) Name() string { return "status" }
func (t statusTool) Description() string {
	return "Emit a short status update for the user before or during work."
}
func (t statusTool) Parameters() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"message": map[string]any{"type": "string"},
		},
		"required": []string{"message"},
	}
}
func (t statusTool) Execute(ctx context.Context, args map[string]any) agentsdk.ToolResult {
	_ = ctx
	msg := strings.TrimSpace(fmt.Sprintf("%v", args["message"]))
	if msg == "" {
		msg = "working..."
	}
	ui.Status(t.out, msg)
	return agentsdk.Success(msg)
}

type runCommandTool struct {
	session *Session
}

func (t runCommandTool) Name() string { return "run_command" }
func (t runCommandTool) Description() string {
	return "Run a shell command for diagnostics or data collection. Include a short user-visible reason. Use cwd and paths inside workspace_root or runbook_root."
}
func (t runCommandTool) Parameters() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"reason":      map[string]any{"type": "string"},
			"command":     map[string]any{"type": "string"},
			"cwd":         map[string]any{"type": "string"},
			"timeout_sec": map[string]any{"type": "integer", "minimum": 1},
		},
		"required": []string{"reason", "command"},
	}
}
func (t runCommandTool) Execute(ctx context.Context, args map[string]any) agentsdk.ToolResult {
	if t.session == nil {
		return agentsdk.Failure("session is not configured", errors.New("session is not configured"))
	}
	result, err := t.session.executeCommandAction(ctx, agentAction{
		Type:       "run_command",
		Reason:     stringParam(args, "reason"),
		Command:    stringParam(args, "command"),
		Cwd:        stringParam(args, "cwd"),
		TimeoutSec: intParam(args, "timeout_sec", 0),
	})
	if err != nil {
		return agentsdk.Failure(err.Error(), err)
	}
	return agentsdk.Success(result)
}

type spawnSubAgentsTool struct {
	session       *Session
	allowChildren bool
	single        bool
}

func (t spawnSubAgentsTool) Name() string {
	if t.single {
		return "spawn_sub_agent"
	}
	return "spawn_sub_agents"
}
func (t spawnSubAgentsTool) Description() string {
	if t.single {
		return "Run a single delegated sub-agent task."
	}
	return "Run independent sub-agent tasks in parallel and aggregate findings."
}
func (t spawnSubAgentsTool) Parameters() map[string]any {
	if t.single {
		return map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"goal":      map[string]any{"type": "string"},
				"task":      map[string]any{"type": "string"},
				"context":   map[string]any{"type": "string"},
				"max_steps": map[string]any{"type": "integer", "minimum": 1},
			},
		}
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"max_parallel": map[string]any{"type": "integer", "minimum": 1},
			"tasks": map[string]any{
				"type":     "array",
				"minItems": 1,
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"name":      map[string]any{"type": "string"},
						"goal":      map[string]any{"type": "string"},
						"context":   map[string]any{"type": "string"},
						"max_steps": map[string]any{"type": "integer", "minimum": 1},
					},
					"required": []string{"goal"},
				},
			},
			"goal":        map[string]any{"type": "string"},
			"task":        map[string]any{"type": "string"},
			"context":     map[string]any{"type": "string"},
			"max_steps":   map[string]any{"type": "integer", "minimum": 1},
			"parallelism": map[string]any{"type": "integer", "minimum": 1},
		},
	}
}
func (t spawnSubAgentsTool) Execute(ctx context.Context, args map[string]any) agentsdk.ToolResult {
	if !t.allowChildren {
		err := errors.New("sub-agents cannot spawn sub-agents")
		return agentsdk.Failure(err.Error(), err)
	}
	if t.session == nil {
		err := errors.New("session is not configured")
		return agentsdk.Failure(err.Error(), err)
	}
	result, err := t.session.executeSDKSubAgents(ctx, args, t.single)
	if err != nil {
		return agentsdk.Failure(err.Error(), err)
	}
	return agentsdk.Success(result)
}

func (s *Session) ensureSDKAgent() error {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.sdkStore == nil {
		s.sdkStore = agentsdk.NewMemorySessionStore()
	}
	if s.sdkAgent != nil {
		return nil
	}
	if s.provider == nil {
		return errors.New("llm provider is not configured")
	}

	opts := []agentsdk.Option{
		agentsdk.WithConfig(s.agentSDKConfig(s.agentSystemPrompt(), s.maxSteps)),
		agentsdk.WithSessionStore(s.sdkStore),
		agentsdk.WithTools(
			statusTool{out: s.out},
			runCommandTool{session: s},
			spawnSubAgentsTool{session: s, allowChildren: true, single: true},
			spawnSubAgentsTool{session: s, allowChildren: true, single: false},
		),
	}
	if s.compactionProvider != nil {
		opts = append(opts, agentsdk.WithCompactionProvider(s.compactionProvider))
	}

	agent, err := agentsdk.New(s.provider, opts...)
	if err != nil {
		return err
	}
	s.sdkAgent = agent
	return nil
}

func (s *Session) invalidateSDKAgent() {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	s.sdkAgent = nil
}

func (s *Session) agentSDKConfig(systemPrompt string, maxSteps int) agentsdk.Config {
	maxIterations := defaultSDKMaxIterations
	if maxSteps > 0 {
		maxIterations = maxSteps
	}
	summaryModel := ""
	if s.compactionProvider != nil {
		summaryModel = strings.TrimSpace(s.compactionProvider.DefaultModel())
	}
	return agentsdk.Config{
		Name:          "night-watch",
		Model:         strings.TrimSpace(s.runtimeModel()),
		SystemPrompt:  systemPrompt,
		MaxIterations: maxIterations,
		LLMOptions: map[string]any{
			"reasoning_effort":  s.runtimeReasoningEffort(),
			"temperature":       1.0,
			"max_output_tokens": s.replyMaxTokens,
		},
		Compaction: agentsdk.CompactionConfig{
			Enabled:                true,
			MaxContextTokens:       s.inputTokenBudget(),
			TriggerPercent:         80,
			PreserveRecentMessages: 8,
			SummaryModel:           summaryModel,
			SummaryMaxOutputTokens: 600,
		},
	}
}

func (s *Session) executeSDKSubAgents(ctx context.Context, args map[string]any, single bool) (string, error) {
	fallback := ""
	if single {
		fallback = firstNonEmpty(stringParam(args, "goal"), stringParam(args, "task"))
	}
	tasks := parseSubAgentTasks(args, fallback)
	if len(tasks) == 0 {
		return "", fmt.Errorf("spawn_sub_agents requires at least one task")
	}

	maxParallel := 1
	if !single {
		maxParallel = intParam(args, "max_parallel", len(tasks))
		if alt := intParam(args, "parallelism", 0); alt > 0 {
			maxParallel = alt
		}
	}
	if maxParallel <= 0 {
		maxParallel = 1
	}
	if maxParallel > 4 {
		maxParallel = 4
	}
	if maxParallel > len(tasks) {
		maxParallel = len(tasks)
	}

	ui.Status(s.out, fmt.Sprintf("spawning %d sub-agent(s) (parallel=%d)", len(tasks), maxParallel))

	results := make([]subAgentResult, len(tasks))
	sem := make(chan struct{}, maxParallel)
	doneCh := make(chan subAgentResult, len(tasks))
	for i, task := range tasks {
		i := i
		task := task
		go func() {
			select {
			case <-ctx.Done():
				doneCh <- subAgentResult{Index: i, Name: task.Name, Goal: task.Goal, Err: ctx.Err()}
				return
			case sem <- struct{}{}:
			}
			defer func() { <-sem }()
			doneCh <- s.runSDKSubAgentTask(ctx, i, task)
		}()
	}
	for i := 0; i < len(tasks); i++ {
		result := <-doneCh
		results[result.Index] = result
	}
	return formatSubAgentResults(results), nil
}

func (s *Session) runSDKSubAgentTask(ctx context.Context, index int, task subAgentTask) subAgentResult {
	name := strings.TrimSpace(task.Name)
	if name == "" {
		name = fmt.Sprintf("sub-agent-%d", index+1)
	}
	maxSteps := task.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 4
	}
	if maxSteps > 6 {
		maxSteps = 6
	}
	if s.provider == nil {
		return subAgentResult{Index: index, Name: name, Goal: task.Goal, Err: errors.New("llm provider is not configured")}
	}

	ui.Status(s.out, fmt.Sprintf("[%s] started", name))

	opts := []agentsdk.Option{
		agentsdk.WithConfig(s.agentSDKConfig(s.subAgentSystemPrompt(), maxSteps)),
		agentsdk.WithSessionStore(s.sdkStore),
		agentsdk.WithTools(
			statusTool{out: s.out},
			runCommandTool{session: s},
		),
	}
	if s.compactionProvider != nil {
		opts = append(opts, agentsdk.WithCompactionProvider(s.compactionProvider))
	}
	childAgent, err := agentsdk.New(s.provider, opts...)
	if err != nil {
		return subAgentResult{Index: index, Name: name, Goal: task.Goal, Err: err}
	}

	prompt := buildSubAgentTaskPrompt(task, s.History())
	childSessionID := fmt.Sprintf("sub_%d_%d", time.Now().UTC().UnixNano(), index+1)
	handle, err := childAgent.Spawn(ctx, agentsdk.SpawnRequest{
		SessionID:       childSessionID,
		Input:           prompt,
		ParentSessionID: s.sessionID,
		ParentRunID:     s.currentRunIDValue(),
		EventSink:       &sessionEventSink{session: s, prefix: "[" + name + "]"},
	})
	if err != nil {
		return subAgentResult{Index: index, Name: name, Goal: task.Goal, Err: err}
	}
	resp, err := handle.Wait(ctx)
	if err != nil {
		return subAgentResult{Index: index, Name: name, Goal: task.Goal, Err: err}
	}
	ui.Status(s.out, fmt.Sprintf("[%s] completed", name))

	actionRuns := 0
	for _, msg := range resp.Messages {
		if msg.Role == agentsdk.RoleTool {
			actionRuns++
		}
	}
	return subAgentResult{
		Index:      index,
		Name:       name,
		Goal:       task.Goal,
		Reply:      strings.TrimSpace(resp.Output),
		Steps:      resp.Iterations,
		ActionRuns: actionRuns,
	}
}

func (s *Session) runtimeModel() string {
	if s == nil || s.cfg == nil {
		return ""
	}
	if s.provider != nil {
		return strings.TrimSpace(s.provider.DefaultModel())
	}
	return strings.TrimSpace(s.cfg.LLMModel)
}

func (s *Session) runtimeCompactionProvider() agentsdk.Provider {
	if s == nil {
		return nil
	}
	return s.compactionProvider
}

func (s *Session) runtimeCompactionModel() string {
	if s == nil || s.compactionProvider == nil {
		return ""
	}
	return strings.TrimSpace(s.compactionProvider.DefaultModel())
}

func (s *Session) runtimeReasoningEffort() string {
	if s == nil || s.cfg == nil {
		return "medium"
	}
	effort := strings.TrimSpace(s.cfg.ReasoningEffort)
	if effort == "" {
		return "medium"
	}
	return effort
}

func (s *Session) setCurrentRunID(runID string) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	s.currentRunID = strings.TrimSpace(runID)
}

func (s *Session) currentRunIDValue() string {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	return s.currentRunID
}

func sdkHistoryToMessages(messages []agentsdk.Message, maxMemory int) []agentsdk.Message {
	return normalizeHistory(messages, maxMemory)
}

func maxIntValue(values ...int) int {
	best := 0
	for _, value := range values {
		if value > best {
			best = value
		}
	}
	return best
}
