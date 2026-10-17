package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencode-ai/opencode/internal/history"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockHistoryService struct {
	history.Service
}

func (m *mockHistoryService) Create(ctx context.Context, sessionID, path, content string) (history.File, error) {
	return history.File{Path: path, Content: content}, nil
}

func (m *mockHistoryService) CreateVersion(ctx context.Context, sessionID, path, content string) (history.File, error) {
	return history.File{Path: path, Content: content}, nil
}

func (m *mockHistoryService) GetByPathAndSession(ctx context.Context, path, sessionID string) (history.File, error) {
	return history.File{Path: path, Content: "mock"}, nil
}

type mockPermissionService struct {
	permission.Service
	allow bool
}

func (m *mockPermissionService) Request(req permission.CreatePermissionRequest) bool {
	return m.allow
}

func TestMultiEditTool_InfoAndCapability(t *testing.T) {
	tool := NewMultiEditTool(nil, &mockPermissionService{allow: true}, &mockHistoryService{})
	info := tool.Info()

	assert.Equal(t, MultiEditToolName, info.Name)
	assert.NotEmpty(t, info.Description)
	assert.Contains(t, info.Required, "edits")

	tc, ok := tool.(ToolWithCapability)
	require.True(t, ok)
	assert.False(t, tc.Capability().IsReadOnly)
	assert.True(t, tc.Capability().MutatesFiles)
	assert.True(t, tc.Capability().IsAtomic)
}

func TestMultiEditTool_SuccessfulMultiFileAtomicEdit(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "multi_edit_test")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	fileA := filepath.Join(tempDir, "a.go")
	fileB := filepath.Join(tempDir, "b.go")

	err = os.WriteFile(fileA, []byte("package a\n\nfunc OldFoo() string {\n\treturn \"foo\"\n}\n"), 0644)
	require.NoError(t, err)
	err = os.WriteFile(fileB, []byte("package b\n\nimport \"a\"\n\nfunc Bar() {\n\ta.OldFoo()\n}\n"), 0644)
	require.NoError(t, err)

	tool := NewMultiEditTool(nil, &mockPermissionService{allow: true}, &mockHistoryService{})

	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	ctx = context.WithValue(ctx, MessageIDContextKey, "test-msg")

	payload := `{
		"description": "Rename OldFoo to NewFoo across package a and b",
		"edits": [
			{
				"file_path": "` + filepath.ToSlash(fileA) + `",
				"old_string": "func OldFoo()",
				"new_string": "func NewFoo()"
			},
			{
				"file_path": "` + filepath.ToSlash(fileB) + `",
				"old_string": "a.OldFoo()",
				"new_string": "a.NewFoo()"
			}
		]
	}`

	resp, err := tool.Run(ctx, ToolCall{
		ID:    "call-1",
		Name:  MultiEditToolName,
		Input: payload,
	})
	require.NoError(t, err)
	assert.False(t, resp.IsError)
	assert.Contains(t, resp.Content, "Successfully applied 2 edit(s) across 2 file(s)")

	// Verify file content on disk
	contentA, err := os.ReadFile(fileA)
	require.NoError(t, err)
	assert.Contains(t, string(contentA), "func NewFoo()")
	assert.NotContains(t, string(contentA), "func OldFoo()")

	contentB, err := os.ReadFile(fileB)
	require.NoError(t, err)
	assert.Contains(t, string(contentB), "a.NewFoo()")
	assert.NotContains(t, string(contentB), "a.OldFoo()")
}

func TestMultiEditTool_AtomicRollbackOnMissingOldString(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "multi_edit_rollback_test")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	fileA := filepath.Join(tempDir, "a.go")
	fileB := filepath.Join(tempDir, "b.go")

	originalContentA := "package a\n\nvar Value = 10\n"
	originalContentB := "package b\n\nvar Other = 20\n"

	err = os.WriteFile(fileA, []byte(originalContentA), 0644)
	require.NoError(t, err)
	err = os.WriteFile(fileB, []byte(originalContentB), 0644)
	require.NoError(t, err)

	tool := NewMultiEditTool(nil, &mockPermissionService{allow: true}, &mockHistoryService{})

	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	ctx = context.WithValue(ctx, MessageIDContextKey, "test-msg")

	// Edit 1 on fileA would match, but Edit 2 on fileB will fail to match
	payload := `{
		"edits": [
			{
				"file_path": "` + filepath.ToSlash(fileA) + `",
				"old_string": "Value = 10",
				"new_string": "Value = 999"
			},
			{
				"file_path": "` + filepath.ToSlash(fileB) + `",
				"old_string": "THIS_STRING_DOES_NOT_EXIST",
				"new_string": "Other = 888"
			}
		]
	}`

	resp, err := tool.Run(ctx, ToolCall{
		ID:    "call-2",
		Name:  MultiEditToolName,
		Input: payload,
	})
	require.NoError(t, err)
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "old_string not found")
	assert.Contains(t, resp.Content, "Multi-edit transaction aborted with no changes")

	// Verify fileA was NOT modified despite being the first edit!
	contentA, err := os.ReadFile(fileA)
	require.NoError(t, err)
	assert.Equal(t, originalContentA, string(contentA), "fileA must remain untouched when transaction aborts")

	contentB, err := os.ReadFile(fileB)
	require.NoError(t, err)
	assert.Equal(t, originalContentB, string(contentB), "fileB must remain untouched")
}

func TestMultiEditTool_MultipleMatchesAmbiguityAbort(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "multi_edit_ambiguous_test")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	fileA := filepath.Join(tempDir, "a.go")
	err = os.WriteFile(fileA, []byte("func x() {}\nfunc x() {}\n"), 0644)
	require.NoError(t, err)

	tool := NewMultiEditTool(nil, &mockPermissionService{allow: true}, &mockHistoryService{})
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	ctx = context.WithValue(ctx, MessageIDContextKey, "test-msg")

	payload := `{
		"edits": [
			{
				"file_path": "` + filepath.ToSlash(fileA) + `",
				"old_string": "func x() {}",
				"new_string": "func y() {}"
			}
		]
	}`

	resp, err := tool.Run(ctx, ToolCall{
		ID:    "call-3",
		Name:  MultiEditToolName,
		Input: payload,
	})
	require.NoError(t, err)
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "appears multiple times")
}
