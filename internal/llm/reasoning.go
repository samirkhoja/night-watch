package llm

func anthropicThinkingBudget(effort string) int {
	switch effort {
	case "low":
		return 256
	case "high":
		return 1536
	default:
		return 768
	}
}

func googleThinkingBudget(effort string) int {
	switch effort {
	case "low":
		return 256
	case "high":
		return 2048
	default:
		return 1024
	}
}
