package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/samirkhoja/night-watch/internal/llm"
)

type actionLoopConfig struct {
	SystemPrompt        string
	InitialConversation []llm.Message
	MaxSteps            int
	MaxTokens           int
	EmptyReply          string
	ExecuteAction       func(ctx context.Context, action agentAction) (string, error)
	Hooks               actionLoopHooks
}

type actionLoopHooks struct {
	BeforeStep     func(step int, conversation []llm.Message) ([]llm.Message, error)
	BeforeGenerate func(step int)
	OnReasoning    func(reason string)
	BeforeActions  func(step int, actions []agentAction)
	BeforeAction   func(step int, index int, total int, action agentAction)
	BeforeFinalize func(
		step int,
		reply string,
		plan assistantPlan,
		state *actionLoopState,
		conversation *[]llm.Message,
		modelResp llm.GenerateResponse,
	) (continueLoop bool, replyOverride string)
	OnComplete func(reply string)
	OnMaxSteps func(state actionLoopState)
}

type actionLoopState struct {
	StepsCompleted     int
	ActionRuns         int
	OperationalActions int
	CommandActions     int
	SubAgentActions    int
	LastReply          string
}

type actionLoopResult struct {
	Reply           string
	Conversation    []llm.Message
	Steps           int
	ActionRuns      int
	ReachedMaxSteps bool
}

func (s *Session) runActionLoop(ctx context.Context, cfg actionLoopConfig) (actionLoopResult, error) {
	if s == nil {
		return actionLoopResult{}, errors.New("session is nil")
	}
	if s.client == nil {
		return actionLoopResult{}, errors.New("llm client is not configured")
	}
	if cfg.ExecuteAction == nil {
		return actionLoopResult{}, errors.New("execute action callback is required")
	}

	maxTokens := cfg.MaxTokens
	if maxTokens <= 0 {
		maxTokens = s.replyMaxTokens
	}
	if maxTokens <= 0 {
		maxTokens = 1024
	}

	emptyReply := strings.TrimSpace(cfg.EmptyReply)
	if emptyReply == "" {
		emptyReply = "I do not have a response yet. Please refine your request."
	}

	state := actionLoopState{}
	conversation := append([]llm.Message{}, cfg.InitialConversation...)

	for step := 1; ; step++ {
		// The hard ceiling is optional. When reached, return the best reply assembled so far.
		if cfg.MaxSteps > 0 && step > cfg.MaxSteps {
			if cfg.Hooks.OnMaxSteps != nil {
				cfg.Hooks.OnMaxSteps(state)
			}
			return actionLoopResult{
				Reply:           strings.TrimSpace(state.LastReply),
				Conversation:    conversation,
				Steps:           state.StepsCompleted,
				ActionRuns:      state.ActionRuns,
				ReachedMaxSteps: true,
			}, nil
		}
		state.StepsCompleted = step

		if cfg.Hooks.BeforeStep != nil {
			nextConversation, err := cfg.Hooks.BeforeStep(step, conversation)
			if err != nil {
				return actionLoopResult{
					Reply:        strings.TrimSpace(state.LastReply),
					Conversation: conversation,
					Steps:        state.StepsCompleted,
					ActionRuns:   state.ActionRuns,
				}, err
			}
			conversation = nextConversation
		}

		if cfg.Hooks.BeforeGenerate != nil {
			cfg.Hooks.BeforeGenerate(step)
		}

		modelResp, err := s.client.Generate(ctx, llm.GenerateRequest{
			System:      cfg.SystemPrompt,
			Messages:    conversation,
			Temperature: 1,
			MaxTokens:   maxTokens,
		})
		if err != nil {
			return actionLoopResult{
				Reply:        strings.TrimSpace(state.LastReply),
				Conversation: conversation,
				Steps:        state.StepsCompleted,
				ActionRuns:   state.ActionRuns,
			}, err
		}

		for _, reason := range modelResp.Reasoning {
			trimmed := strings.TrimSpace(reason)
			if trimmed == "" {
				continue
			}
			if cfg.Hooks.OnReasoning != nil {
				cfg.Hooks.OnReasoning(trimmed)
			}
		}

		plan := planFromGenerateResponse(modelResp)
		reply := strings.TrimSpace(plan.Reply)
		state.LastReply = reply

		// "status"-only tool calls are treated as non-operational; finish if we already have a reply.
		finalTurn := len(plan.Actions) == 0 || (reply != "" && onlyStatusActions(plan.Actions))
		if finalTurn {
			if reply == "" {
				reply = emptyReply
			}
			state.LastReply = reply
			if cfg.Hooks.BeforeFinalize != nil {
				continueLoop, replyOverride := cfg.Hooks.BeforeFinalize(
					step,
					reply,
					plan,
					&state,
					&conversation,
					modelResp,
				)
				if continueLoop {
					continue
				}
				if trimmed := strings.TrimSpace(replyOverride); trimmed != "" {
					reply = trimmed
					state.LastReply = reply
				}
			}
			if cfg.Hooks.OnComplete != nil {
				cfg.Hooks.OnComplete(reply)
			}
			return actionLoopResult{
				Reply:        reply,
				Conversation: conversation,
				Steps:        state.StepsCompleted,
				ActionRuns:   state.ActionRuns,
			}, nil
		}

		if cfg.Hooks.BeforeActions != nil {
			cfg.Hooks.BeforeActions(step, plan.Actions)
		}

		results := make([]string, 0, len(plan.Actions))
		for i, action := range plan.Actions {
			if cfg.Hooks.BeforeAction != nil {
				cfg.Hooks.BeforeAction(step, i, len(plan.Actions), action)
			}
			if isOperationalAction(action.Type) {
				state.OperationalActions++
			}
			switch strings.ToLower(strings.TrimSpace(action.Type)) {
			case "run_command":
				state.CommandActions++
			case "spawn_sub_agents", "spawn_sub_agent":
				state.SubAgentActions++
			}
			state.ActionRuns++

			actionResult, actionErr := cfg.ExecuteAction(ctx, action)
			if actionErr != nil {
				actionResult = fmt.Sprintf("action_error: %v", actionErr)
			}
			results = append(results, fmt.Sprintf("action[%d] %s", i+1, actionResult))
		}

		conversation = appendToolResultsPrompt(conversation, modelResp, results)
	}
}

func appendAssistantFollowUp(conversation []llm.Message, modelResp llm.GenerateResponse, userMessage string) []llm.Message {
	conversation = append(conversation, llm.Message{
		Role:    "assistant",
		Content: formatAssistantTurn(modelResp),
	})
	conversation = append(conversation, llm.Message{
		Role:    "user",
		Content: userMessage,
	})
	return conversation
}

func appendToolResultsPrompt(conversation []llm.Message, modelResp llm.GenerateResponse, results []string) []llm.Message {
	return appendAssistantFollowUp(
		conversation,
		modelResp,
		"Tool/action results:\n"+strings.Join(results, "\n")+
			"\n\nNow continue. Call additional tools if needed, or provide your final answer.",
	)
}
