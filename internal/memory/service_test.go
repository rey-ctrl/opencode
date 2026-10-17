package memory

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

func setupTestDB(t *testing.T) *sql.DB {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory sqlite db: %v", err)
	}

	createTableSQL := `
	CREATE TABLE project_memories (
		id TEXT PRIMARY KEY,
		project_path TEXT NOT NULL,
		category TEXT NOT NULL DEFAULT 'general',
		key TEXT NOT NULL COLLATE NOCASE,
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

func TestMemoryService_RememberAndGet(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	svc := NewService(db)
	ctx := context.Background()
	project := "/home/user/project"

	mem, err := svc.Remember(ctx, project, "convention", "test_style", "Use table-driven tests")
	if err != nil {
		t.Fatalf("failed to remember: %v", err)
	}

	if mem.Key != "test_style" || mem.Category != "convention" || mem.Content != "Use table-driven tests" {
		t.Errorf("unexpected memory: %+v", mem)
	}

	// Retrieve by key
	retrieved, err := svc.Get(ctx, project, "test_style")
	if err != nil {
		t.Fatalf("failed to get: %v", err)
	}
	if retrieved.ID != mem.ID || retrieved.Content != mem.Content {
		t.Errorf("mismatched retrieved memory: %+v vs %+v", retrieved, mem)
	}
}

func TestMemoryService_RememberUpsert(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	svc := NewService(db)
	ctx := context.Background()
	project := "/home/user/project"

	// First insert
	first, err := svc.Remember(ctx, project, "convention", "lint_rules", "Use golangci-lint")
	if err != nil {
		t.Fatalf("first remember failed: %v", err)
	}

	// Update existing key with new content & category
	second, err := svc.Remember(ctx, project, "build", "lint_rules", "Use golangci-lint run --fast")
	if err != nil {
		t.Fatalf("second remember failed: %v", err)
	}

	if second.ID != first.ID {
		t.Errorf("expected same ID on upsert, got %s vs %s", second.ID, first.ID)
	}
	if second.Category != "build" || second.Content != "Use golangci-lint run --fast" {
		t.Errorf("expected updated content and category, got %+v", second)
	}

	// Total count should still be 1
	all, err := svc.List(ctx, project)
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 item, got %d", len(all))
	}
}

func TestMemoryService_RecallWithFilters(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	svc := NewService(db)
	ctx := context.Background()
	project := "/home/user/project"

	svc.Remember(ctx, project, "convention", "naming", "Use PascalCase for exported types")
	svc.Remember(ctx, project, "architecture", "db_layer", "Use SQLite repository pattern")
	svc.Remember(ctx, project, "gotcha", "windows_path", "Use filepath.Clean for cross-platform paths")

	// Filter by category
	convs, err := svc.Recall(ctx, project, "", "convention")
	if err != nil {
		t.Fatalf("recall category failed: %v", err)
	}
	if len(convs) != 1 || convs[0].Key != "naming" {
		t.Errorf("expected only 'naming' convention, got %+v", convs)
	}

	// Filter by query text
	matches, err := svc.Recall(ctx, project, "filepath", "")
	if err != nil {
		t.Fatalf("recall query failed: %v", err)
	}
	if len(matches) != 1 || matches[0].Key != "windows_path" {
		t.Errorf("expected 'windows_path' query match, got %+v", matches)
	}

	// Case-insensitive query and category test
	ciMatches, err := svc.Recall(ctx, project, "FILEPATH", "GOTCHA")
	if err != nil {
		t.Fatalf("case-insensitive recall failed: %v", err)
	}
	if len(ciMatches) != 1 || ciMatches[0].Key != "windows_path" {
		t.Errorf("expected case-insensitive match for 'FILEPATH' in 'GOTCHA', got %+v", ciMatches)
	}

	// Case-insensitive Get
	ciGet, err := svc.Get(ctx, project, "WINDOWS_PATH")
	if err != nil {
		t.Fatalf("case-insensitive Get failed: %v", err)
	}
	if ciGet.Key != "windows_path" {
		t.Errorf("expected 'windows_path', got %s", ciGet.Key)
	}
}

func TestMemoryService_Forget(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	svc := NewService(db)
	ctx := context.Background()
	project := "/home/user/project"

	svc.Remember(ctx, project, "general", "temp_note", "To be forgotten")

	err := svc.Forget(ctx, project, "temp_note")
	if err != nil {
		t.Fatalf("forget failed: %v", err)
	}

	// Should not be found
	_, err = svc.Get(ctx, project, "temp_note")
	if err == nil {
		t.Errorf("expected error after forget, got nil")
	}
}

func TestMemoryService_GetSummaryForPrompt(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	svc := NewService(db)
	ctx := context.Background()
	project := "/home/user/project"

	// When empty
	summary := svc.GetSummaryForPrompt(ctx, project, 10)
	if summary != "" {
		t.Errorf("expected empty summary when no memories exist, got %s", summary)
	}

	// Add memories
	svc.Remember(ctx, project, "convention", "err_handling", "Wrap errors with %w")
	svc.Remember(ctx, project, "architecture", "service_wiring", "Inject services via App struct")

	summary = svc.GetSummaryForPrompt(ctx, project, 10)
	if !strings.Contains(summary, "<project_memories>") {
		t.Errorf("expected <project_memories> block, got:\n%s", summary)
	}
	if !strings.Contains(summary, "err_handling") || !strings.Contains(summary, "service_wiring") {
		t.Errorf("expected memory keys in summary, got:\n%s", summary)
	}
}
