package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/memory"
)

const (
	RememberToolName = "remember"
)

type RememberTool struct {
	memory memory.Service
}

func NewRememberTool(mem memory.Service) *RememberTool {
	return &RememberTool{
		memory: mem,
	}
}

type RememberParams struct {
	Key      string `json:"key"`
	Content  string `json:"content"`
	Category string `json:"category,omitempty"`
}

func (t *RememberTool) Info() ToolInfo {
	return ToolInfo{
		Name:        RememberToolName,
		Description: "Save a persistent project convention, architectural decision, gotcha, or build instruction for the current repository across future sessions.",
		Parameters: map[string]any{
			"key": map[string]any{
				"type":        "string",
				"description": "Unique identifier or short tag for this memory (e.g. 'test_pattern', 'api_port', 'docker_build')",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "Detailed instruction, rule, or context to be remembered",
			},
			"category": map[string]any{
				"type":        "string",
				"description": "Category of memory: 'convention', 'architecture', 'build', 'gotcha', 'decision', or 'general' (default: 'general')",
				"enum": []string{
					"convention",
					"architecture",
					"build",
					"gotcha",
					"decision",
					"general",
				},
			},
		},
		Required: []string{"key", "content"},
	}
}

func (t *RememberTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	if t.memory == nil {
		return NewTextErrorResponse("memory service is not configured"), nil
	}

	var params RememberParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("error parsing parameters: %s", err)), nil
	}

	if params.Key == "" {
		return NewTextErrorResponse("key is required"), nil
	}
	if params.Content == "" {
		return NewTextErrorResponse("content is required"), nil
	}

	projectPath := config.WorkingDirectory()
	mem, err := t.memory.Remember(ctx, projectPath, params.Category, params.Key, params.Content)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("failed to save memory: %s", err)), nil
	}

	result := fmt.Sprintf("Successfully stored persistent project memory:\n- **Key**: `%s`\n- **Category**: `%s`\n- **Content**: %s",
		mem.Key, mem.Category, mem.Content)

	return WithResponseMetadata(NewTextResponse(result), mem), nil
}
