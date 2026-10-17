package agent

import (
	"context"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTruncateForDisplay(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxLen   int
		expected string
	}{
		{
			name:     "short string untouched",
			input:    "hello",
			maxLen:   10,
			expected: "hello",
		},
		{
			name:     "exact length untouched",
			input:    "hello world",
			maxLen:   11,
			expected: "hello world",
		},
		{
			name:     "long string truncated with ellipsis",
			input:    "this is a very long text that needs truncation",
			maxLen:   15,
			expected: "this is a very ...",
		},
		{
			name:     "empty string",
			input:    "",
			maxLen:   5,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateForDisplay(tt.input, tt.maxLen)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestNewAgentTool_BrokerOption(t *testing.T) {
	// Without broker
	toolNoBroker := NewAgentTool(nil, nil, nil)
	require.NotNil(t, toolNoBroker)
	atNoBroker, ok := toolNoBroker.(*agentTool)
	require.True(t, ok)
	assert.Nil(t, atNoBroker.parentBroker)

	// With broker
	broker := pubsub.NewBroker[AgentEvent]()
	toolWithBroker := NewAgentTool(nil, nil, nil, broker)
	require.NotNil(t, toolWithBroker)
	atWithBroker, ok := toolWithBroker.(*agentTool)
	require.True(t, ok)
	assert.Equal(t, broker, atWithBroker.parentBroker)
}

func TestForwardSubagentProgress(t *testing.T) {
	broker := pubsub.NewBroker[AgentEvent]()
	sub := broker.Subscribe(context.Background())

	tool := &agentTool{
		parentBroker: broker,
	}

	msgChan := make(chan pubsub.Event[message.Message], 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	subagentSessionID := "subagent-session-123"
	parentSessionID := "parent-session-456"

	go tool.forwardSubagentProgress(ctx, msgChan, subagentSessionID, parentSessionID)

	// 1. Message from a DIFFERENT session should be ignored
	msgOtherSession := message.Message{
		SessionID: "other-session",
		Role:      message.Assistant,
	}
	msgChan <- pubsub.Event[message.Message]{
		Type:    pubsub.CreatedEvent,
		Payload: msgOtherSession,
	}

	// 2. Message from subagent session with tool calls
	msgWithTool := message.Message{
		SessionID: subagentSessionID,
		Role:      message.Assistant,
		Parts: []message.ContentPart{
			message.ToolCall{
				ID:    "tc-1",
				Name:  "grep",
				Input: `{"pattern": "func main"}`,
			},
		},
	}
	msgChan <- pubsub.Event[message.Message]{
		Type:    pubsub.CreatedEvent,
		Payload: msgWithTool,
	}

	// Read event from broker
	select {
	case event := <-sub:
		assert.Equal(t, AgentEventTypeSubagentProgress, event.Payload.Type)
		require.NotNil(t, event.Payload.SubagentProgress)
		assert.Equal(t, "running_tool", event.Payload.SubagentProgress.Status)
		assert.Equal(t, "grep", event.Payload.SubagentProgress.ToolName)
		assert.Equal(t, `{"pattern": "func main"}`, event.Payload.SubagentProgress.ToolInput)
		assert.Equal(t, subagentSessionID, event.Payload.SubagentProgress.SubagentSessionID)
		assert.Equal(t, parentSessionID, event.Payload.SubagentProgress.ParentSessionID)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for tool call progress event")
	}

	// 3. Message from subagent session with text delta (no tool calls)
	msgWithText := message.Message{
		SessionID: subagentSessionID,
		Role:      message.Assistant,
		Parts: []message.ContentPart{
			message.TextContent{
				Text: "Found 3 matching files.",
			},
		},
	}
	msgChan <- pubsub.Event[message.Message]{
		Type:    pubsub.CreatedEvent,
		Payload: msgWithText,
	}

	select {
	case event := <-sub:
		assert.Equal(t, AgentEventTypeSubagentProgress, event.Payload.Type)
		require.NotNil(t, event.Payload.SubagentProgress)
		assert.Equal(t, "streaming", event.Payload.SubagentProgress.Status)
		assert.Equal(t, "Found 3 matching files.", event.Payload.SubagentProgress.TextDelta)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for streaming progress event")
	}
}

func TestAgentTool_ResolveAgentSpecialists(t *testing.T) {
	tool := NewAgentToolWithPermissions(nil, nil, nil, nil, nil, nil)
	at, ok := tool.(*agentTool)
	require.True(t, ok)

	// Test "reviewer"
	name, tools := at.resolveAgent("reviewer")
	assert.Equal(t, config.AgentReviewer, name)
	assert.NotEmpty(t, tools)

	// Test "tester"
	name, tools = at.resolveAgent("tester")
	assert.Equal(t, config.AgentTester, name)
	assert.NotEmpty(t, tools)

	// Test "debugger"
	name, tools = at.resolveAgent("debugger")
	assert.Equal(t, config.AgentDebugger, name)
	assert.NotEmpty(t, tools)

	// Test default / empty / unknown
	name, tools = at.resolveAgent("")
	assert.Equal(t, config.AgentTask, name)
	assert.NotEmpty(t, tools)

	name, tools = at.resolveAgent("unknown_agent")
	assert.Equal(t, config.AgentTask, name)
	assert.NotEmpty(t, tools)
}

