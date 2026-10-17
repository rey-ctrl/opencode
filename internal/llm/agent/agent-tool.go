package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/llm/tools/shell"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/session"
)

type agentTool struct {
	sessions   session.Service
	messages   message.Service
	lspClients map[string]*lsp.Client

	// parentBroker allows forwarding subagent progress events to the parent agent's TUI
	parentBroker *pubsub.Broker[AgentEvent]
}

const (
	AgentToolName = "agent"

	// Maximum length for tool input display in progress events
	maxToolInputDisplayLen = 120
)

type AgentParams struct {
	Prompt string `json:"prompt"`
}

func (b *agentTool) Info() tools.ToolInfo {
	return tools.ToolInfo{
		Name:        AgentToolName,
		Description: "Launch a new agent that has access to the following tools: GlobTool, GrepTool, LS, View. When you are searching for a keyword or file and are not confident that you will find the right match on the first try, use the Agent tool to perform the search for you. For example:\n\n- If you are searching for a keyword like \"config\" or \"logger\", or for questions like \"which file does X?\", the Agent tool is strongly recommended\n- If you want to read a specific file path, use the View or GlobTool tool instead of the Agent tool, to find the match more quickly\n- If you are searching for a specific class definition like \"class Foo\", use the GlobTool tool instead, to find the match more quickly\n\nUsage notes:\n1. Launch multiple agents concurrently whenever possible, to maximize performance; to do that, use a single message with multiple tool uses\n2. When the agent is done, it will return a single message back to you. The result returned by the agent is not visible to the user. To show the user the result, you should send a text message back to the user with a concise summary of the result.\n3. Each agent invocation is stateless. You will not be able to send additional messages to the agent, nor will the agent be able to communicate with you outside of its final report. Therefore, your prompt should contain a highly detailed task description for the agent to perform autonomously and you should specify exactly what information the agent should return back to you in its final and only message to you.\n4. The agent's outputs should generally be trusted\n5. IMPORTANT: The agent can not use Bash, Replace, Edit, so can not modify files. If you want to use these tools, use them directly instead of going through the agent.",
		Parameters: map[string]any{
			"prompt": map[string]any{
				"type":        "string",
				"description": "The task for the agent to perform",
			},
		},
		Required: []string{"prompt"},
	}
}

// truncateForDisplay truncates a string for display in progress events.
func truncateForDisplay(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func (b *agentTool) Run(ctx context.Context, call tools.ToolCall) (tools.ToolResponse, error) {
	var params AgentParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return tools.NewTextErrorResponse(fmt.Sprintf("error parsing parameters: %s", err)), nil
	}
	if params.Prompt == "" {
		return tools.NewTextErrorResponse("prompt is required"), nil
	}

	sessionID, messageID := tools.GetContextValues(ctx)
	if sessionID == "" || messageID == "" {
		return tools.ToolResponse{}, fmt.Errorf("session_id and message_id are required")
	}

	agent, err := NewAgent(config.AgentTask, b.sessions, b.messages, TaskAgentTools(b.lspClients))
	if err != nil {
		return tools.ToolResponse{}, fmt.Errorf("error creating agent: %s", err)
	}

	session, err := b.sessions.CreateTaskSession(ctx, call.ID, sessionID, "New Agent Session")
	if err != nil {
		return tools.ToolResponse{}, fmt.Errorf("error creating session: %s", err)
	}
	defer shell.CloseShellForSession(session.ID)

	// Subscribe to subagent's message stream for progress events
	progressCtx, progressCancel := context.WithCancel(ctx)
	defer progressCancel()

	if b.parentBroker != nil {
		// Publish initial "subagent started" progress
		b.parentBroker.Publish(pubsub.CreatedEvent, AgentEvent{
			Type: AgentEventTypeSubagentProgress,
			SubagentProgress: &SubagentProgressInfo{
				SubagentSessionID: session.ID,
				ParentSessionID:   sessionID,
				Status:            "started",
			},
		})

		// Subscribe to subagent's messages to forward tool activity
		msgSub := b.messages.Subscribe(progressCtx)
		go b.forwardSubagentProgress(progressCtx, msgSub, session.ID, sessionID)
	}

	done, err := agent.Run(ctx, session.ID, params.Prompt)
	if err != nil {
		return tools.ToolResponse{}, fmt.Errorf("error generating agent: %s", err)
	}
	result := <-done

	// Cancel progress subscription
	progressCancel()

	if result.Error != nil {
		// Publish error progress
		if b.parentBroker != nil {
			b.parentBroker.Publish(pubsub.UpdatedEvent, AgentEvent{
				Type: AgentEventTypeSubagentProgress,
				SubagentProgress: &SubagentProgressInfo{
					SubagentSessionID: session.ID,
					ParentSessionID:   sessionID,
					Status:            "error",
				},
			})
		}
		return tools.ToolResponse{}, fmt.Errorf("error generating agent: %s", result.Error)
	}

	// Publish completion progress
	if b.parentBroker != nil {
		b.parentBroker.Publish(pubsub.UpdatedEvent, AgentEvent{
			Type: AgentEventTypeSubagentProgress,
			SubagentProgress: &SubagentProgressInfo{
				SubagentSessionID: session.ID,
				ParentSessionID:   sessionID,
				Status:            "completed",
			},
		})
	}

	response := result.Message
	if response.Role != message.Assistant {
		return tools.NewTextErrorResponse("no response"), nil
	}

	updatedSession, err := b.sessions.Get(ctx, session.ID)
	if err != nil {
		return tools.ToolResponse{}, fmt.Errorf("error getting session: %s", err)
	}
	parentSession, err := b.sessions.Get(ctx, sessionID)
	if err != nil {
		return tools.ToolResponse{}, fmt.Errorf("error getting parent session: %s", err)
	}

	parentSession.Cost += updatedSession.Cost

	_, err = b.sessions.Save(ctx, parentSession)
	if err != nil {
		return tools.ToolResponse{}, fmt.Errorf("error saving parent session: %s", err)
	}
	return tools.NewTextResponse(response.Content().String()), nil
}

// forwardSubagentProgress listens for message events from the subagent session
// and publishes corresponding SubagentProgress events to the parent broker.
func (b *agentTool) forwardSubagentProgress(
	ctx context.Context,
	msgChan <-chan pubsub.Event[message.Message],
	subagentSessionID, parentSessionID string,
) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-msgChan:
			if !ok {
				return
			}
			// Only forward messages belonging to our subagent session
			if event.Payload.SessionID != subagentSessionID {
				continue
			}

			// Forward tool calls as progress
			if event.Payload.Role == message.Assistant {
				for _, tc := range event.Payload.ToolCalls() {
					b.parentBroker.Publish(pubsub.UpdatedEvent, AgentEvent{
						Type: AgentEventTypeSubagentProgress,
						SubagentProgress: &SubagentProgressInfo{
							SubagentSessionID: subagentSessionID,
							ParentSessionID:   parentSessionID,
							ToolName:          tc.Name,
							ToolInput:         truncateForDisplay(tc.Input, maxToolInputDisplayLen),
							Status:            "running_tool",
						},
					})
				}

				// Forward text deltas
				textContent := event.Payload.Content().String()
				if textContent != "" && len(event.Payload.ToolCalls()) == 0 {
					b.parentBroker.Publish(pubsub.UpdatedEvent, AgentEvent{
						Type: AgentEventTypeSubagentProgress,
						SubagentProgress: &SubagentProgressInfo{
							SubagentSessionID: subagentSessionID,
							ParentSessionID:   parentSessionID,
							TextDelta:         truncateForDisplay(textContent, 200),
							Status:            "streaming",
						},
					})
				}
			}
		}
	}
}

func NewAgentTool(
	Sessions session.Service,
	Messages message.Service,
	LspClients map[string]*lsp.Client,
	parentBroker ...*pubsub.Broker[AgentEvent],
) tools.BaseTool {
	var broker *pubsub.Broker[AgentEvent]
	if len(parentBroker) > 0 && parentBroker[0] != nil {
		broker = parentBroker[0]
	} else {
		logging.Debug("NewAgentTool: no parent broker provided, subagent progress streaming disabled")
	}
	return &agentTool{
		sessions:     Sessions,
		messages:     Messages,
		lspClients:   LspClients,
		parentBroker: broker,
	}
}
