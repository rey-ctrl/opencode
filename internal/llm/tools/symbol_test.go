package tools

import (
	"context"
	"testing"

	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindSymbolTool_InfoAndCapability(t *testing.T) {
	tool := NewFindSymbolTool(nil)
	info := tool.Info()

	assert.Equal(t, FindSymbolToolName, info.Name)
	assert.NotEmpty(t, info.Description)
	assert.Contains(t, info.Parameters, "query")
	assert.Contains(t, info.Parameters, "kind")
	assert.Contains(t, info.Parameters, "file_path")
	assert.Contains(t, info.Parameters, "line")

	tc, ok := tool.(ToolWithCapability)
	require.True(t, ok)
	assert.True(t, tc.Capability().IsReadOnly)
	assert.True(t, IsToolReadOnly(tool))
}

func TestFindSymbolTool_NoLSPClients(t *testing.T) {
	tool := NewFindSymbolTool(make(map[string]*lsp.Client))
	resp, err := tool.Run(context.Background(), ToolCall{
		ID:    "test-1",
		Name:  FindSymbolToolName,
		Input: `{"query": "Test"}`,
	})
	require.NoError(t, err)
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "No LSP clients")
}

func TestFindSymbolTool_InvalidJSON(t *testing.T) {
	tool := NewFindSymbolTool(map[string]*lsp.Client{"go": nil})
	resp, err := tool.Run(context.Background(), ToolCall{
		ID:    "test-2",
		Name:  FindSymbolToolName,
		Input: `invalid-json`,
	})
	require.NoError(t, err)
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "error parsing parameters")
}
