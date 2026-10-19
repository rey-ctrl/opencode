package tools

import (
	"context"
	"encoding/json"
)

type ToolInfo struct {
	Name        string
	Description string
	Parameters  map[string]any
	Required    []string
}

type toolResponseType string

type (
	sessionIDContextKey string
	messageIDContextKey string
)

const (
	ToolResponseTypeText  toolResponseType = "text"
	ToolResponseTypeImage toolResponseType = "image"

	SessionIDContextKey sessionIDContextKey = "session_id"
	MessageIDContextKey messageIDContextKey = "message_id"
)

type ToolResponse struct {
	Type     toolResponseType `json:"type"`
	Content  string           `json:"content"`
	Metadata string           `json:"metadata,omitempty"`
	IsError  bool             `json:"is_error"`
}

func NewTextResponse(content string) ToolResponse {
	return ToolResponse{
		Type:    ToolResponseTypeText,
		Content: content,
	}
}

func WithResponseMetadata(response ToolResponse, metadata any) ToolResponse {
	if metadata != nil {
		metadataBytes, err := json.Marshal(metadata)
		if err != nil {
			return response
		}
		response.Metadata = string(metadataBytes)
	}
	return response
}

func NewTextErrorResponse(content string) ToolResponse {
	return ToolResponse{
		Type:    ToolResponseTypeText,
		Content: content,
		IsError: true,
	}
}

type ToolCall struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Input string `json:"input"`
}

type BaseTool interface {
	Info() ToolInfo
	Run(ctx context.Context, params ToolCall) (ToolResponse, error)
}

// ToolCapability defines execution characteristics for concurrent scheduling.
type ToolCapability struct {
	IsReadOnly   bool // Can safely run in parallel with other read-only tools
	MutatesFiles bool // Writes or alters workspace files
	IsAtomic     bool // Must run completely serially (e.g. bash, subagent)
}

// ToolWithCapability is an optional interface tools can implement to declare capabilities.
type ToolWithCapability interface {
	BaseTool
	Capability() ToolCapability
}

// IsToolReadOnly returns true if the tool can be safely executed concurrently with other queries.
func IsToolReadOnly(tool BaseTool) bool {
	if tool == nil {
		return false
	}
	if tc, ok := tool.(ToolWithCapability); ok {
		return tc.Capability().IsReadOnly
	}
	switch tool.Info().Name {
	case "view", "grep", "glob", "ls", "diagnostics", "sourcegraph", "find_symbol", "recall":
		return true
	default:
		return false
	}
}

func GetContextValues(ctx context.Context) (string, string) {
	sessionID := ctx.Value(SessionIDContextKey)
	messageID := ctx.Value(MessageIDContextKey)
	if sessionID == nil {
		return "", ""
	}
	if messageID == nil {
		return sessionID.(string), ""
	}
	return sessionID.(string), messageID.(string)
}
