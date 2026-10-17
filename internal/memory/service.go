package memory

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/opencode-ai/opencode/internal/logging"
)

// Memory represents a persistent piece of project knowledge, convention, or architectural decision.
type Memory struct {
	ID          string `json:"id"`
	ProjectPath string `json:"project_path"`
	Category    string `json:"category"`
	Key         string `json:"key"`
	Content     string `json:"content"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

// Service defines operations for reading, writing, and formatting project memories.
type Service interface {
	Remember(ctx context.Context, projectPath, category, key, content string) (Memory, error)
	Recall(ctx context.Context, projectPath, query, category string) ([]Memory, error)
	Get(ctx context.Context, projectPath, key string) (Memory, error)
	List(ctx context.Context, projectPath string) ([]Memory, error)
	Forget(ctx context.Context, projectPath, key string) error
	GetSummaryForPrompt(ctx context.Context, projectPath string, maxItems int) string
}

type memoryService struct {
	db *sql.DB
	mu sync.RWMutex
}

// NewService creates a new persistent project memory service backed by SQLite.
func NewService(db *sql.DB) Service {
	return &memoryService{
		db: db,
	}
}

// Remember stores or updates a persistent memory item for the given project.
func (s *memoryService) Remember(ctx context.Context, projectPath, category, key, content string) (Memory, error) {
	if s.db == nil {
		return Memory{}, fmt.Errorf("database connection is nil")
	}

	cleanPath := filepath.Clean(projectPath)
	key = strings.TrimSpace(key)
	if key == "" {
		return Memory{}, fmt.Errorf("memory key cannot be empty")
	}

	content = strings.TrimSpace(content)
	if content == "" {
		return Memory{}, fmt.Errorf("memory content cannot be empty")
	}

	category = strings.ToLower(strings.TrimSpace(category))
	if category == "" {
		category = "general"
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UnixMilli()

	// Check if this project key already exists
	var existingID string
	var createdAt int64
	err := s.db.QueryRowContext(ctx,
		"SELECT id, created_at FROM project_memories WHERE project_path = ? AND key = ?",
		cleanPath, key,
	).Scan(&existingID, &createdAt)

	if err == nil {
		// Update existing memory
		_, updateErr := s.db.ExecContext(ctx,
			"UPDATE project_memories SET category = ?, content = ?, updated_at = ? WHERE id = ?",
			category, content, now, existingID,
		)
		if updateErr != nil {
			return Memory{}, fmt.Errorf("failed to update memory: %w", updateErr)
		}

		return Memory{
			ID:          existingID,
			ProjectPath: cleanPath,
			Category:    category,
			Key:         key,
			Content:     content,
			CreatedAt:   createdAt,
			UpdatedAt:   now,
		}, nil
	} else if err != sql.ErrNoRows {
		return Memory{}, fmt.Errorf("failed to query existing memory: %w", err)
	}

	// Insert new memory
	newID := uuid.New().String()
	_, insertErr := s.db.ExecContext(ctx,
		`INSERT INTO project_memories (id, project_path, category, key, content, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		newID, cleanPath, category, key, content, now, now,
	)
	if insertErr != nil {
		return Memory{}, fmt.Errorf("failed to insert memory: %w", insertErr)
	}

	return Memory{
		ID:          newID,
		ProjectPath: cleanPath,
		Category:    category,
		Key:         key,
		Content:     content,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

// Recall searches project memories with optional query text and category filter.
func (s *memoryService) Recall(ctx context.Context, projectPath, query, category string) ([]Memory, error) {
	if s.db == nil {
		return nil, fmt.Errorf("database connection is nil")
	}

	cleanPath := filepath.Clean(projectPath)
	query = strings.TrimSpace(query)
	category = strings.ToLower(strings.TrimSpace(category))

	s.mu.RLock()
	defer s.mu.RUnlock()

	querySQL := "SELECT id, project_path, category, key, content, created_at, updated_at FROM project_memories WHERE project_path = ?"
	args := []any{cleanPath}

	if category != "" && category != "all" {
		querySQL += " AND category = ?"
		args = append(args, category)
	}

	if query != "" {
		querySQL += " AND (key LIKE ? OR content LIKE ?)"
		pattern := "%" + query + "%"
		args = append(args, pattern, pattern)
	}

	querySQL += " ORDER BY updated_at DESC"

	rows, err := s.db.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query memories: %w", err)
	}
	defer rows.Close()

	var memories []Memory
	for rows.Next() {
		var m Memory
		if scanErr := rows.Scan(&m.ID, &m.ProjectPath, &m.Category, &m.Key, &m.Content, &m.CreatedAt, &m.UpdatedAt); scanErr != nil {
			return nil, fmt.Errorf("failed to scan memory row: %w", scanErr)
		}
		memories = append(memories, m)
	}

	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, fmt.Errorf("error reading memory rows: %w", rowsErr)
	}

	return memories, nil
}

// Get retrieves a specific memory by key.
func (s *memoryService) Get(ctx context.Context, projectPath, key string) (Memory, error) {
	if s.db == nil {
		return Memory{}, fmt.Errorf("database connection is nil")
	}

	cleanPath := filepath.Clean(projectPath)
	s.mu.RLock()
	defer s.mu.RUnlock()

	var m Memory
	err := s.db.QueryRowContext(ctx,
		"SELECT id, project_path, category, key, content, created_at, updated_at FROM project_memories WHERE project_path = ? AND key = ?",
		cleanPath, strings.TrimSpace(key),
	).Scan(&m.ID, &m.ProjectPath, &m.Category, &m.Key, &m.Content, &m.CreatedAt, &m.UpdatedAt)

	if err != nil {
		if err == sql.ErrNoRows {
			return Memory{}, fmt.Errorf("memory not found: %s", key)
		}
		return Memory{}, fmt.Errorf("failed to get memory: %w", err)
	}

	return m, nil
}

// List returns all memories for a given project path.
func (s *memoryService) List(ctx context.Context, projectPath string) ([]Memory, error) {
	return s.Recall(ctx, projectPath, "", "")
}

// Forget deletes a persistent memory by key.
func (s *memoryService) Forget(ctx context.Context, projectPath, key string) error {
	if s.db == nil {
		return fmt.Errorf("database connection is nil")
	}

	cleanPath := filepath.Clean(projectPath)
	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.ExecContext(ctx,
		"DELETE FROM project_memories WHERE project_path = ? AND key = ?",
		cleanPath, strings.TrimSpace(key),
	)
	if err != nil {
		return fmt.Errorf("failed to delete memory: %w", err)
	}

	affected, err := res.RowsAffected()
	if err == nil && affected == 0 {
		return fmt.Errorf("memory not found: %s", key)
	}

	return nil
}

// GetSummaryForPrompt formats the top project memories into a concise markdown block for system prompt injection.
func (s *memoryService) GetSummaryForPrompt(ctx context.Context, projectPath string, maxItems int) string {
	if s.db == nil {
		return ""
	}

	if maxItems <= 0 {
		maxItems = 15
	}

	cleanPath := filepath.Clean(projectPath)
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.QueryContext(ctx,
		"SELECT category, key, content FROM project_memories WHERE project_path = ? ORDER BY updated_at DESC LIMIT ?",
		cleanPath, maxItems,
	)
	if err != nil {
		logging.Debug("Failed to query project memories for prompt", "error", err)
		return ""
	}
	defer rows.Close()

	var sb strings.Builder
	count := 0
	for rows.Next() {
		var category, key, content string
		if scanErr := rows.Scan(&category, &key, &content); scanErr != nil {
			continue
		}
		if count == 0 {
			sb.WriteString("<project_memories>\n")
			sb.WriteString("The following persistent project memories, conventions, and architectural decisions have been saved across sessions for this repository:\n")
		}
		sb.WriteString(fmt.Sprintf("- [%s] %s: %s\n", category, key, content))
		count++
	}

	if count > 0 {
		sb.WriteString("</project_memories>")
		return sb.String()
	}

	return ""
}
