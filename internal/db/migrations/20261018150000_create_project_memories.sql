-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS project_memories (
    id TEXT PRIMARY KEY,
    project_path TEXT NOT NULL,
    category TEXT NOT NULL DEFAULT 'general',
    key TEXT NOT NULL COLLATE NOCASE,
    content TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_project_memories_project ON project_memories (project_path, category);
CREATE UNIQUE INDEX IF NOT EXISTS idx_project_memories_project_key ON project_memories (project_path, key);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS project_memories;
-- +goose StatementEnd
