package chat

import (
	"encoding/json"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/stretchr/testify/assert"
)

func TestToolName(t *testing.T) {
	assert.Equal(t, "MultiEdit", toolName(tools.MultiEditToolName))
	assert.Equal(t, "TestRunner", toolName(tools.TestRunnerToolName))
	assert.Equal(t, "Symbol", toolName(tools.FindSymbolToolName))
	assert.Equal(t, "Remember", toolName(tools.RememberToolName))
	assert.Equal(t, "Recall", toolName(tools.RecallToolName))
	assert.Equal(t, "Diagnostics", toolName(tools.DiagnosticsToolName))
	assert.Equal(t, "Edit", toolName(tools.EditToolName))
	assert.Equal(t, "Bash", toolName(tools.BashToolName))
}

func TestGetToolAction(t *testing.T) {
	assert.Equal(t, "Planning multi-file edits...", getToolAction(tools.MultiEditToolName))
	assert.Equal(t, "Running test suite...", getToolAction(tools.TestRunnerToolName))
	assert.Equal(t, "Locating symbol...", getToolAction(tools.FindSymbolToolName))
	assert.Equal(t, "Storing project memory...", getToolAction(tools.RememberToolName))
	assert.Equal(t, "Recalling memories...", getToolAction(tools.RecallToolName))
	assert.Equal(t, "Checking diagnostics...", getToolAction(tools.DiagnosticsToolName))
}

func TestRenderToolParams(t *testing.T) {
	// MultiEdit
	multiEditCall := message.ToolCall{
		Name:  tools.MultiEditToolName,
		Input: `{"description":"Refactor authentication handlers"}`,
	}
	rendered := renderToolParams(80, multiEditCall)
	assert.Contains(t, rendered, "Refactor authentication handlers")

	// TestRunner
	testRunnerCall := message.ToolCall{
		Name:  tools.TestRunnerToolName,
		Input: `{"framework":"go","test_filter":"TestAuth"}`,
	}
	rendered = renderToolParams(80, testRunnerCall)
	assert.Contains(t, rendered, "TestAuth")
	assert.Contains(t, rendered, "framework=go")

	// Remember
	rememberCall := message.ToolCall{
		Name:  tools.RememberToolName,
		Input: `{"key":"db_port","category":"architecture"}`,
	}
	rendered = renderToolParams(80, rememberCall)
	assert.Contains(t, rendered, "db_port")
	assert.Contains(t, rendered, "cat=architecture")
}

func TestRenderToolResponse_TestRunner(t *testing.T) {
	report := tools.TestReport{
		Framework: "go",
		Passed:    10,
		Failed:    0,
		Duration:  0.45,
		Success:   true,
	}
	metaBytes, _ := json.Marshal(report)

	call := message.ToolCall{Name: tools.TestRunnerToolName}
	resp := message.ToolResult{
		Metadata: string(metaBytes),
		Content:  "PASS",
	}

	output := renderToolResponse(call, resp, 80)
	assert.Contains(t, output, "PASS")
	assert.Contains(t, output, "10 passed")
}

func TestRenderToolResponse_MultiEdit(t *testing.T) {
	meta := tools.MultiEditResponseMetadata{
		FilesModified: 2,
		EditsApplied:  4,
		Additions:     12,
		Removals:      3,
		Diff: `--- a/file1.go
+++ b/file1.go
@@ -1,3 +1,3 @@
-old
+new
`,
	}
	metaBytes, _ := json.Marshal(meta)

	call := message.ToolCall{Name: tools.MultiEditToolName}
	resp := message.ToolResult{
		Metadata: string(metaBytes),
		Content:  "Applied 4 edits",
	}

	output := renderToolResponse(call, resp, 80)
	assert.Contains(t, output, "2 file(s) modified")
}
