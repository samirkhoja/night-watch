package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/samirkhoja/night-watch/internal/llm"
	"github.com/samirkhoja/night-watch/internal/ui"
)

func (s *Session) executeSubAgentsAction(ctx context.Context, action agentAction) (string, error) {
	tasks := parseSubAgentTasks(action.Params, action.Message)
	if len(tasks) == 0 {
		return "", fmt.Errorf("spawn_sub_agents requires params.tasks with at least one task")
	}

	maxParallel := intParam(action.Params, "max_parallel", len(tasks))
	if maxParallel <= 0 {
		maxParallel = 1
	}
	if alt := intParam(action.Params, "parallelism", 0); alt > 0 {
		maxParallel = alt
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
				doneCh <- subAgentResult{
					Index: i,
					Name:  task.Name,
					Goal:  task.Goal,
					Err:   ctx.Err(),
				}
				return
			case sem <- struct{}{}:
			}
			defer func() { <-sem }()

			result := s.runSubAgentTask(ctx, i, task)
			doneCh <- result
		}()
	}

	for i := 0; i < len(tasks); i++ {
		result := <-doneCh
		results[result.Index] = result
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].Index < results[j].Index
	})

	return formatSubAgentResults(results), nil
}

func (s *Session) runSubAgentTask(ctx context.Context, index int, task subAgentTask) subAgentResult {
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

	ui.Status(s.out, fmt.Sprintf("[%s] started", name))

	prompt := buildSubAgentTaskPrompt(task, s.history)
	conversation := []llm.Message{
		{
			Role:    "user",
			Content: prompt,
		},
	}
	loopResult, err := s.runActionLoop(ctx, actionLoopConfig{
		SystemPrompt:        s.subAgentSystemPrompt(),
		InitialConversation: conversation,
		MaxSteps:            maxSteps,
		MaxTokens:           maxIntValue(600, s.replyMaxTokens/2),
		EmptyReply:          "Sub-agent completed with no additional notes.",
		ExecuteAction: func(ctx context.Context, action agentAction) (string, error) {
			return s.executeSubAgentAction(ctx, name, action)
		},
		Hooks: actionLoopHooks{
			OnComplete: func(reply string) {
				ui.Status(s.out, fmt.Sprintf("[%s] completed", name))
			},
			OnMaxSteps: func(state actionLoopState) {
				ui.Warn(s.out, fmt.Sprintf("[%s] reached max steps", name))
			},
		},
	})
	if err != nil {
		return subAgentResult{
			Index: index,
			Name:  name,
			Goal:  task.Goal,
			Steps: loopResult.Steps,
			Err:   err,
		}
	}

	lastReply := strings.TrimSpace(loopResult.Reply)
	if loopResult.ReachedMaxSteps && lastReply == "" {
		lastReply = "Sub-agent reached max steps before final answer."
	}

	return subAgentResult{
		Index:      index,
		Name:       name,
		Goal:       task.Goal,
		Reply:      lastReply,
		Steps:      loopResult.Steps,
		ActionRuns: loopResult.ActionRuns,
	}
}

func (s *Session) executeSubAgentAction(ctx context.Context, name string, action agentAction) (string, error) {
	actionType := strings.ToLower(strings.TrimSpace(action.Type))
	switch actionType {
	case "status":
		msg := strings.TrimSpace(action.Message)
		if msg == "" {
			msg = "working..."
		}
		ui.Status(s.out, fmt.Sprintf("[%s] %s", name, msg))
		return msg, nil
	case "run_command":
		return s.executeCommandAction(ctx, action)
	case "spawn_sub_agents", "spawn_sub_agent":
		return "", fmt.Errorf("sub-agents cannot spawn sub-agents")
	default:
		return "", fmt.Errorf("unsupported sub-agent action type: %s", action.Type)
	}
}

func parseSubAgentTasks(params map[string]interface{}, fallbackMessage string) []subAgentTask {
	var tasks []subAgentTask
	if params == nil {
		if msg := strings.TrimSpace(fallbackMessage); msg != "" {
			return []subAgentTask{
				{
					Name:     "sub-agent-1",
					Goal:     msg,
					MaxSteps: 4,
				},
			}
		}
		return tasks
	}

	if raw, ok := params["tasks"]; ok {
		if list, ok := raw.([]interface{}); ok {
			for i, item := range list {
				task := subAgentTask{
					Name:     fmt.Sprintf("sub-agent-%d", i+1),
					MaxSteps: 4,
				}
				switch typed := item.(type) {
				case string:
					task.Goal = strings.TrimSpace(typed)
				case map[string]interface{}:
					if name := firstNonEmpty(
						stringParam(typed, "name"),
						stringParam(typed, "id"),
						stringParam(typed, "task"),
					); name != "" {
						task.Name = name
					}
					task.Goal = firstNonEmpty(
						stringParam(typed, "goal"),
						stringParam(typed, "task"),
						stringParam(typed, "prompt"),
						stringParam(typed, "objective"),
						stringParam(typed, "query"),
					)
					task.Context = stringParam(typed, "context")
					task.MaxSteps = intParam(typed, "max_steps", 4)
				}
				task.Goal = strings.TrimSpace(task.Goal)
				if task.Goal == "" {
					continue
				}
				if task.MaxSteps <= 0 {
					task.MaxSteps = 4
				}
				if task.MaxSteps > 6 {
					task.MaxSteps = 6
				}
				tasks = append(tasks, task)
			}
		}
	}

	if len(tasks) == 0 {
		goal := firstNonEmpty(
			stringParam(params, "goal"),
			stringParam(params, "task"),
			stringParam(params, "prompt"),
			stringParam(params, "objective"),
			stringParam(params, "query"),
			strings.TrimSpace(fallbackMessage),
		)
		if goal != "" {
			tasks = append(tasks, subAgentTask{
				Name:     "sub-agent-1",
				Goal:     goal,
				Context:  stringParam(params, "context"),
				MaxSteps: intParam(params, "max_steps", 4),
			})
		}
	}

	return tasks
}

func buildSubAgentTaskPrompt(task subAgentTask, history []llm.Message) string {
	var builder strings.Builder
	builder.WriteString("Task objective:\n")
	builder.WriteString(task.Goal)
	builder.WriteString("\n")

	if ctx := strings.TrimSpace(task.Context); ctx != "" {
		builder.WriteString("\nTask context:\n")
		builder.WriteString(ctx)
		builder.WriteString("\n")
	}

	if len(history) > 0 {
		historyContext := summarizeHistoryForSubAgent(history, 900)
		if strings.TrimSpace(historyContext) != "" {
			builder.WriteString("\nParent conversation context:\n")
			builder.WriteString(historyContext)
			builder.WriteString("\n")
		}
	}

	builder.WriteString("\nReturn concise findings and actionable next steps.")
	return strings.TrimSpace(builder.String())
}

func formatSubAgentResults(results []subAgentResult) string {
	if len(results) == 0 {
		return "No sub-agent results."
	}

	var builder strings.Builder
	builder.WriteString("sub-agent results\n")
	builder.WriteString(strings.Repeat("-", 17))
	builder.WriteByte('\n')

	for _, result := range results {
		name := strings.TrimSpace(result.Name)
		if name == "" {
			name = fmt.Sprintf("sub-agent-%d", result.Index+1)
		}
		builder.WriteString(fmt.Sprintf("[%s]\n", name))
		if goal := strings.TrimSpace(result.Goal); goal != "" {
			builder.WriteString("goal: ")
			builder.WriteString(summarizeInline(goal, 140))
			builder.WriteByte('\n')
		}

		if result.Err != nil {
			builder.WriteString("status: failed\n")
			builder.WriteString("error: ")
			builder.WriteString(result.Err.Error())
			builder.WriteByte('\n')
			builder.WriteByte('\n')
			continue
		}

		builder.WriteString("status: complete\n")
		builder.WriteString(fmt.Sprintf("steps: %d, actions: %d\n", result.Steps, result.ActionRuns))
		reply := strings.TrimSpace(result.Reply)
		if reply == "" {
			reply = "No summary provided."
		}
		builder.WriteString("summary: ")
		builder.WriteString(summarizeInline(reply, 600))
		builder.WriteByte('\n')
		builder.WriteByte('\n')
	}

	return strings.TrimSpace(builder.String())
}
