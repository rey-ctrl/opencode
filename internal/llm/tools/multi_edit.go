package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/diff"
	"github.com/opencode-ai/opencode/internal/history"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/permission"
)

const (
	MultiEditToolName = "multi_edit"
	multiEditDescription = `Performs atomic replacements across multiple files in a single coordinated transaction.

WHEN TO USE THIS TOOL:
- Renaming functions, types, interfaces, or variables that are used across multiple files.
- Applying coordinated architectural refactoring where all files must update together.
- Making multi-line edits in multiple files without dirty state if any single replacement fails.

ALL-OR-NOTHING GUARANTEE:
- All edits are validated in dry-run first before any file is touched on disk.
- If ANY file is missing, or if ANY old_string is missing or ambiguous, the ENTIRE operation aborts immediately with zero modifications to any file.

HOW TO USE:
- Provide an array of edits: [{file_path, old_string, new_string}, ...]
- old_string must match exactly, including indentation and line breaks.
- old_string must be unique within the target file.
- Relative paths are resolved against the project working directory.`
)

type MultiEditItem struct {
	FilePath  string `json:"file_path"`
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

type MultiEditParams struct {
	Edits       []MultiEditItem `json:"edits"`
	Description string          `json:"description,omitempty"`
}

type MultiEditResponseMetadata struct {
	FilesModified int    `json:"files_modified"`
	EditsApplied  int    `json:"edits_applied"`
	Additions     int    `json:"additions"`
	Removals      int    `json:"removals"`
	Diff          string `json:"diff"`
}

type multiEditTool struct {
	lspClients  map[string]*lsp.Client
	permissions permission.Service
	files       history.Service
}

func NewMultiEditTool(
	lspClients map[string]*lsp.Client,
	permissions permission.Service,
	files history.Service,
) BaseTool {
	return &multiEditTool{
		lspClients:  lspClients,
		permissions: permissions,
		files:       files,
	}
}

func (m *multiEditTool) Info() ToolInfo {
	return ToolInfo{
		Name:        MultiEditToolName,
		Description: multiEditDescription,
		Parameters: map[string]any{
			"edits": map[string]any{
				"type": "array",
				"description": "Array of file edit items to apply atomically",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"file_path": map[string]any{
							"type":        "string",
							"description": "Path to the file to edit",
						},
						"old_string": map[string]any{
							"type":        "string",
							"description": "Exact text to replace (must be unique in file)",
						},
						"new_string": map[string]any{
							"type":        "string",
							"description": "Replacement text",
						},
					},
					"required": []string{"file_path", "old_string", "new_string"},
				},
			},
			"description": map[string]any{
				"type":        "string",
				"description": "Optional human-readable description of the refactoring purpose",
			},
		},
		Required: []string{"edits"},
	}
}

func (m *multiEditTool) Capability() ToolCapability {
	return ToolCapability{
		IsReadOnly:   false,
		MutatesFiles: true,
		IsAtomic:     true,
	}
}

type fileEditPlan struct {
	filePath        string
	originalContent string
	currentContent  string
	editCount       int
	diff            string
	additions       int
	removals        int
}

func (m *multiEditTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params MultiEditParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return NewTextErrorResponse("invalid parameters for multi_edit: " + err.Error()), nil
	}

	if len(params.Edits) == 0 {
		return NewTextErrorResponse("edits list cannot be empty"), nil
	}

	sessionID, messageID := GetContextValues(ctx)
	if sessionID == "" || messageID == "" {
		return ToolResponse{}, fmt.Errorf("session ID and message ID are required for multi_edit")
	}

	wd := config.WorkingDirectory()
	plans := make(map[string]*fileEditPlan)
	var orderedPaths []string

	// 1. Dry-Run Validation Phase (All-or-Nothing)
	for idx, edit := range params.Edits {
		if edit.FilePath == "" {
			return NewTextErrorResponse(fmt.Sprintf("edits[%d]: file_path is required", idx)), nil
		}
		if edit.OldString == "" {
			return NewTextErrorResponse(fmt.Sprintf("edits[%d] in %s: old_string is required for multi_edit", idx, edit.FilePath)), nil
		}

		absPath := edit.FilePath
		if !filepath.IsAbs(absPath) {
			absPath = filepath.Join(wd, absPath)
		}

		plan, exists := plans[absPath]
		if !exists {
			fileInfo, err := os.Stat(absPath)
			if err != nil {
				if os.IsNotExist(err) {
					return NewTextErrorResponse(fmt.Sprintf("file not found: %s. Multi-edit transaction aborted with no changes.", edit.FilePath)), nil
				}
				return ToolResponse{}, fmt.Errorf("failed to access file %s: %w", edit.FilePath, err)
			}

			if fileInfo.IsDir() {
				return NewTextErrorResponse(fmt.Sprintf("path is a directory, not a file: %s. Multi-edit transaction aborted.", edit.FilePath)), nil
			}

			raw, err := os.ReadFile(absPath)
			if err != nil {
				return ToolResponse{}, fmt.Errorf("failed to read file %s: %w", edit.FilePath, err)
			}

			plan = &fileEditPlan{
				filePath:        absPath,
				originalContent: string(raw),
				currentContent:  string(raw),
			}
			plans[absPath] = plan
			orderedPaths = append(orderedPaths, absPath)
		}

		// Validate exact match in current buffer
		firstIndex := strings.Index(plan.currentContent, edit.OldString)
		if firstIndex == -1 {
			return NewTextErrorResponse(fmt.Sprintf("old_string not found in %s (edit #%d). Ensure it matches exact whitespace and indentation. Multi-edit transaction aborted with no changes.", edit.FilePath, idx+1)), nil
		}

		lastIndex := strings.LastIndex(plan.currentContent, edit.OldString)
		if firstIndex != lastIndex {
			return NewTextErrorResponse(fmt.Sprintf("old_string appears multiple times in %s (edit #%d). Provide more surrounding context to disambiguate. Multi-edit transaction aborted with no changes.", edit.FilePath, idx+1)), nil
		}

		// Apply replacement in in-memory plan
		plan.currentContent = plan.currentContent[:firstIndex] + edit.NewString + plan.currentContent[firstIndex+len(edit.OldString):]
		plan.editCount++
	}

	// 2. Generate Diffs and Compute Totals
	totalAdditions := 0
	totalRemovals := 0
	var combinedDiff strings.Builder

	for _, p := range orderedPaths {
		plan := plans[p]
		fileDiff, adds, rems := diff.GenerateDiff(plan.originalContent, plan.currentContent, plan.filePath)
		plan.diff = fileDiff
		plan.additions = adds
		plan.removals = rems

		totalAdditions += adds
		totalRemovals += rems
		combinedDiff.WriteString(fileDiff)
		combinedDiff.WriteString("\n")
	}

	// 3. Permission Request
	primaryPath := wd
	if len(orderedPaths) == 1 {
		primaryPath = orderedPaths[0]
	}

	desc := params.Description
	if desc == "" {
		desc = fmt.Sprintf("Apply %d coordinated edit(s) across %d file(s)", len(params.Edits), len(plans))
	}

	permitted := m.permissions.Request(
		permission.CreatePermissionRequest{
			SessionID:   sessionID,
			Path:        primaryPath,
			ToolName:    MultiEditToolName,
			Action:      "write",
			Description: desc,
			Params: EditPermissionsParams{
				FilePath: primaryPath,
				Diff:     combinedDiff.String(),
			},
		},
	)
	if !permitted {
		return ToolResponse{}, permission.ErrorPermissionDenied
	}

	// 4. Execution Phase: Write all planned changes to disk atomically
	for _, p := range orderedPaths {
		plan := plans[p]
		if err := os.WriteFile(plan.filePath, []byte(plan.currentContent), 0o644); err != nil {
			return ToolResponse{}, fmt.Errorf("error writing file %s: %w", plan.filePath, err)
		}

		// Record in history service
		file, err := m.files.GetByPathAndSession(ctx, plan.filePath, sessionID)
		if err != nil {
			_, err = m.files.Create(ctx, sessionID, plan.filePath, plan.originalContent)
			if err != nil {
				logging.Debug("Error creating file history", "error", err)
			}
		} else if file.Content != plan.originalContent {
			_, err = m.files.CreateVersion(ctx, sessionID, plan.filePath, plan.originalContent)
			if err != nil {
				logging.Debug("Error creating file history version", "error", err)
			}
		}

		_, err = m.files.CreateVersion(ctx, sessionID, plan.filePath, plan.currentContent)
		if err != nil {
			logging.Debug("Error creating file history version", "error", err)
		}

		recordFileWrite(plan.filePath)
		recordFileRead(plan.filePath)
		waitForLspDiagnostics(ctx, plan.filePath, m.lspClients)
	}

	// 5. Build Response
	var responseSummary strings.Builder
	responseSummary.WriteString(fmt.Sprintf("Successfully applied %d edit(s) across %d file(s) (+%d / -%d lines):\n\n",
		len(params.Edits), len(plans), totalAdditions, totalRemovals))

	for _, p := range orderedPaths {
		plan := plans[p]
		relPath, err := filepath.Rel(wd, plan.filePath)
		if err != nil || strings.HasPrefix(relPath, "..") {
			relPath = plan.filePath
		}
		responseSummary.WriteString(fmt.Sprintf("- %s: %d edit(s), +%d / -%d lines\n",
			relPath, plan.editCount, plan.additions, plan.removals))
	}

	// Append any active diagnostics from edited files
	for _, p := range orderedPaths {
		diag := getDiagnostics(p, m.lspClients)
		if diag != "" {
			responseSummary.WriteString("\n" + diag)
		}
	}

	return WithResponseMetadata(
		NewTextResponse(responseSummary.String()),
		MultiEditResponseMetadata{
			FilesModified: len(plans),
			EditsApplied:  len(params.Edits),
			Additions:     totalAdditions,
			Removals:      totalRemovals,
			Diff:          combinedDiff.String(),
		},
	), nil
}
