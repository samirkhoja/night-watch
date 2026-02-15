package agent

import "testing"

func TestParseSubAgentTasksFromTasksList(t *testing.T) {
	params := map[string]interface{}{
		"tasks": []interface{}{
			map[string]interface{}{
				"name":      "logs",
				"goal":      "Find error spikes in aws logs",
				"context":   "region us-east-1",
				"max_steps": 3,
			},
			map[string]interface{}{
				"task": "Inspect recent deploy commits",
			},
		},
	}

	tasks := parseSubAgentTasks(params, "")
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}
	if tasks[0].Name != "logs" || tasks[0].Goal == "" {
		t.Fatalf("expected first task name/goal to be parsed")
	}
	if tasks[0].MaxSteps != 3 {
		t.Fatalf("expected first task max steps = 3, got %d", tasks[0].MaxSteps)
	}
	if tasks[1].Goal != "Inspect recent deploy commits" {
		t.Fatalf("expected second task goal parsed from task field")
	}
}

func TestParseSubAgentTasksFallback(t *testing.T) {
	tasks := parseSubAgentTasks(nil, "Do repo research")
	if len(tasks) != 1 {
		t.Fatalf("expected fallback task")
	}
	if tasks[0].Goal != "Do repo research" {
		t.Fatalf("unexpected fallback goal: %q", tasks[0].Goal)
	}
}

func TestParseSubAgentTasksGoalShortcut(t *testing.T) {
	params := map[string]interface{}{
		"goal":      "Check cloud auth",
		"max_steps": 2,
	}
	tasks := parseSubAgentTasks(params, "")
	if len(tasks) != 1 {
		t.Fatalf("expected single task from goal shortcut")
	}
	if tasks[0].MaxSteps != 2 {
		t.Fatalf("expected max_steps to carry through")
	}
}
