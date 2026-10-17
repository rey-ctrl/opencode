package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/memory"
)

type RecallTool struct {
	memory memory.Service
}

func NewRecallTool(mem memory.Service) *RecallTool {
	return &RecallTool{
		memory: mem,
	}
}

// IsReadOnly marks RecallTool as eligible for parallel concurrent execution.
func (t *RecallTool) IsReadOnly() bool {
	return true
}

type RecallParams struct {
	Query    string `json:"query,omitempty"`
	Category string `json:"category,omitempty"`
}

func (t *RecallTool) Info() ToolInfo {
	return ToolInfo{
		Name:        "recall",
		Description: "Search and retrieve persistent project memories, conventions, build instructions, and architecture decisions stored across sessions for this repository.",
		Parameters: map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Optional search query to match against memory keys or content. Omit to list recent memories.",
			},
			"category": map[string]any{
				"type":        "string",
				"description": "Optional category filter: 'convention', 'architecture', 'build', 'gotcha', 'decision', or 'all'",
			},
		},
	}
}

func (t *RecallTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	if t.memory == nil {
		return NewTextErrorResponse("memory service is not configured"), nil
	}

	var params RecallParams
	if call.Input != "" {
		if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
			return NewTextErrorResponse(fmt.Sprintf("error parsing parameters: %s", err)), nil
		}
	}

	projectPath := config.WorkingDirectory()
	memories, err := t.memory.Recall(ctx, projectPath, params.Query, params.Category)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("failed to recall memories: %s", err)), nil
	}

	if len(memories) == 0 {
		msg := "No persistent project memories found."
		if params.Query != "" || params.Category != "" {
			msg = fmt.Sprintf("No project memories found matching query='%s' category='%s'.", params.Query, params.Category)
		}
		return NewTextResponse(msg), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d project memory item(s):\n\n", len(memories)))
	for _, m := range memories {
		updatedTime := time.UnixMilli(m.UpdatedAt).Format("2006-01-02 15:04")
		sb.WriteString(fmt.Sprintf("- **[%s] %s** (updated %s):\n  %s\n",
			strings.ToUpper(m.Category), m.Key, updatedTime, m.Content))
	}

	return WithResponseMetadata(NewTextResponse(sb.String()), memories), nil
}
