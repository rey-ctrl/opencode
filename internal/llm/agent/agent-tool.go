package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/llm/tools/shell"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/memory"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/session"
)

type agentTool struct {
	sessions      session.Service
	messages      message.Service
	lspClients    map[string]*lsp.Client
	permissions   permission.Service
	memoryService memory.Service

	// parentBroker allows forwarding subagent progress events to the parent agent's TUI
	parentBroker *pubsub.Broker[AgentEvent]
}

const (
	AgentToolName = "agent"

	// Maximum length for tool input display in progress events
	maxToolInputDisplayLen = 120
)

type AgentParams struct {
	Prompt    string `json:"prompt"`
	AgentType string `json:"agent_type,omitempty"`
}

func (b *agentTool) Info() tools.ToolInfo {
	return tools.ToolInfo{
		Name: AgentToolName,
		Description: "Launch a subordinate specialized agent to autonomously perform a focused task and report back with findings.\n\n" +
			"AGENT TYPES (agent_type):\n" +
			"- \"task\" (default): General exploration, codebase search, and symbol lookup (Glob, Grep, LS, View, FindSymbol).\n" +
			"- \"reviewer\": Code review, security analysis, and architectural inspection (read-only tools + diagnostics + recall memory).\n" +
			"- \"tester\": Test execution, verification, and regression runs (bash + test_runner + diagnostics + inspection tools).\n" +
			"- \"debugger\": Root-cause diagnostics, symbol inspection, and test reproduction (bash + test_runner + diagnostics + symbols).\n\n" +
			"Usage notes:\n" +
			"1. Choose the appropriate agent_type for your objective (e.g. use 'reviewer' for code reviews, 'tester' to verify tests, 'debugger' for investigating failures).\n" +
			"2. Launch multiple agents concurrently whenever possible to maximize performance.\n" +
			"3. Each agent invocation is autonomous and returns a single summary report back to you.\n" +
			"4. Provide detailed, unambiguous instructions in prompt so the subagent can complete the task independently.",
		Parameters: map[string]any{
			"prompt": map[string]any{
				"type":        "string",
				"description": "The task for the agent to perform",
			},
			"agent_type": map[string]any{
				"type":        "string",
				"enum":        []string{"task", "reviewer", "tester", "debugger"},
				"description": "Specialist agent role: 'task' (general search), 'reviewer' (code review), 'tester' (run tests/bash), 'debugger' (root-cause diagnostics). Default is 'task'.",
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

func (b *agentTool) resolveAgent(agentType string) (config.AgentName, []tools.BaseTool) {
	switch strings.ToLower(strings.TrimSpace(agentType)) {
	case "reviewer":
		return config.AgentReviewer, ReviewerAgentTools(b.lspClients, b.memoryService)
	case "tester":
		return config.AgentTester, TesterAgentTools(b.permissions, b.lspClients, b.memoryService)
	case "debugger":
		return config.AgentDebugger, DebuggerAgentTools(b.permissions, b.lspClients, b.memoryService)
	default:
		return config.AgentTask, TaskAgentTools(b.lspClients)
	}
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

	agentName, agentTools := b.resolveAgent(params.AgentType)
	agent, err := NewAgent(agentName, b.sessions, b.messages, agentTools)
	if err != nil {
		return tools.ToolResponse{}, fmt.Errorf("error creating agent: %s", err)
	}

	sessionTitle := fmt.Sprintf("Agent (%s)", agentName)
	session, err := b.sessions.CreateTaskSession(ctx, call.ID, sessionID, sessionTitle)
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

// NewAgentToolWithPermissions creates an agentTool with permissions and memory service wired,
// enabling specialist agents (tester, debugger, reviewer) to access appropriate tool sets.
func NewAgentToolWithPermissions(
	Sessions session.Service,
	Messages message.Service,
	LspClients map[string]*lsp.Client,
	Broker *pubsub.Broker[AgentEvent],
	Permissions permission.Service,
	MemoryService ...memory.Service,
) tools.BaseTool {
	var mem memory.Service
	if len(MemoryService) > 0 {
		mem = MemoryService[0]
	}
	return &agentTool{
		sessions:      Sessions,
		messages:      Messages,
		lspClients:    LspClients,
		permissions:   Permissions,
		memoryService: mem,
		parentBroker:  Broker,
	}
}
