package agent

import (
	"fmt"
	"strings"

	agentsdk "github.com/samirkhoja/agent-sdk"
)

func parseSubAgentTasks(params map[string]interface{}, fallbackMessage string) []subAgentTask {
	var tasks []subAgentTask
	if params == nil {
		if msg := strings.TrimSpace(fallbackMessage); msg != "" {
			return []subAgentTask{{Name: "sub-agent-1", Goal: msg, MaxSteps: 4}}
		}
		return tasks
	}

	if raw, ok := params["tasks"]; ok {
		if list, ok := raw.([]interface{}); ok {
			for i, item := range list {
				task := subAgentTask{Name: fmt.Sprintf("sub-agent-%d", i+1), MaxSteps: 4}
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

func buildSubAgentTaskPrompt(task subAgentTask, history []agentsdk.Message) string {
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
