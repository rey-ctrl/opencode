package tools

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/memory"
)

func setupMemoryTestDB(t *testing.T) *sql.DB {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory sqlite db: %v", err)
	}

	createTableSQL := `
	CREATE TABLE project_memories (
		id TEXT PRIMARY KEY,
		project_path TEXT NOT NULL,
		category TEXT NOT NULL DEFAULT 'general',
		key TEXT NOT NULL,
		content TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	);
	CREATE INDEX idx_project_memories_project ON project_memories (project_path, category);
	CREATE UNIQUE INDEX idx_project_memories_project_key ON project_memories (project_path, key);
	`
	if _, err := db.Exec(createTableSQL); err != nil {
		t.Fatalf("failed to create test schema: %v", err)
	}

	return db
}

func TestRememberTool_InfoAndExecution(t *testing.T) {
	db := setupMemoryTestDB(t)
	defer db.Close()

	memSvc := memory.NewService(db)
	tool := NewRememberTool(memSvc)

	info := tool.Info()
	if info.Name != "remember" {
		t.Errorf("expected tool name 'remember', got %s", info.Name)
	}

	project := config.WorkingDirectory()
	ctx := context.Background()
	call := ToolCall{
		Input: `{"key": "test_rule", "category": "convention", "content": "Always write unit tests"}`,
	}

	resp, err := tool.Run(ctx, call)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.IsError {
		t.Fatalf("expected success, got error: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "test_rule") || !strings.Contains(resp.Content, "convention") {
		t.Errorf("expected key and category in response, got: %s", resp.Content)
	}

	// Verify persistence via recall
	recalled, err := memSvc.Get(ctx, project, "test_rule")
	if err != nil {
		t.Fatalf("failed to recall saved memory: %v", err)
	}
	if recalled.Content != "Always write unit tests" {
		t.Errorf("expected content match, got %s", recalled.Content)
	}
}

func TestRememberTool_MissingFields(t *testing.T) {
	db := setupMemoryTestDB(t)
	defer db.Close()

	memSvc := memory.NewService(db)
	tool := NewRememberTool(memSvc)

	ctx := context.Background()

	// Missing key
	resp, _ := tool.Run(ctx, ToolCall{Input: `{"content": "Just content"}`})
	if !resp.IsError {
		t.Errorf("expected error response for missing key, got %+v", resp)
	}

	// Missing content
	resp, _ = tool.Run(ctx, ToolCall{Input: `{"key": "some_key"}`})
	if !resp.IsError {
		t.Errorf("expected error response for missing content, got %+v", resp)
	}
}

func TestRecallTool_InfoAndSearch(t *testing.T) {
	db := setupMemoryTestDB(t)
	defer db.Close()

	memSvc := memory.NewService(db)
	tool := NewRecallTool(memSvc)

	info := tool.Info()
	if info.Name != "recall" {
		t.Errorf("expected tool name 'recall', got %s", info.Name)
	}
	if !tool.IsReadOnly() {
		t.Errorf("expected RecallTool to be marked ReadOnly")
	}

	project := config.WorkingDirectory()
	ctx := context.Background()
	memSvc.Remember(ctx, project, "architecture", "microservices", "Use gRPC for inter-service RPC")
	memSvc.Remember(ctx, project, "convention", "lint_rules", "No unused imports")

	// Search all
	resp, err := tool.Run(ctx, ToolCall{Input: `{}`})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.IsError {
		t.Fatalf("expected success, got error: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "microservices") || !strings.Contains(resp.Content, "lint_rules") {
		t.Errorf("expected both memories, got: %s", resp.Content)
	}

	// Search by category
	catResp, err := tool.Run(ctx, ToolCall{Input: `{"category": "architecture"}`})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(catResp.Content, "microservices") || strings.Contains(catResp.Content, "lint_rules") {
		t.Errorf("expected only architecture memory, got: %s", catResp.Content)
	}

	// Search non-existent
	emptyResp, err := tool.Run(ctx, ToolCall{Input: `{"query": "non_existent_topic"}`})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(emptyResp.Content, "No project memories found") {
		t.Errorf("expected not found message, got: %s", emptyResp.Content)
	}
}
