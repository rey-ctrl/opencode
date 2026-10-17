package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/lsp/protocol"
)

const (
	FindSymbolToolName = "find_symbol"
	findSymbolDescription = `Find symbols (functions, classes, interfaces, methods, structs, variables) across the codebase using Language Server Protocol (LSP).

WHEN TO USE THIS TOOL:
- Use when searching for symbol definitions, callers/references, or implementations with semantic accuracy.
- Far more reliable than text grep because it understands AST, scope, and types.
- Perfect for exploring architecture, finding all callers of a function, or listing symbols in a file.

MODES (kind):
- "workspace" (default): Searches for matching symbols by name across the entire workspace.
- "document": Lists all symbols declared in a specific file (file outline).
- "references": Finds all references/callers of a symbol at file_path, line, character.
- "definition": Jumps to the exact definition of a symbol at file_path, line, character.
- "implementation": Finds all implementations of an interface or method.

PARAMETERS:
- query: Symbol name to search for (e.g. "GetShellForSession", "BaseTool", "Config").
- kind: "workspace" | "document" | "references" | "definition" | "implementation" (defaults to "workspace").
- file_path: Path to target file (required for document, references, definition, implementation).
- line: 1-indexed line number in file_path (required for references, definition, implementation).
- character: 1-indexed column number in file_path (defaults to 1).`
)

type FindSymbolParams struct {
	Query     string `json:"query"`
	Kind      string `json:"kind"`
	FilePath  string `json:"file_path"`
	Line      int    `json:"line"`
	Character int    `json:"character"`
}

type symbolTool struct {
	lspClients map[string]*lsp.Client
}

func NewFindSymbolTool(lspClients map[string]*lsp.Client) BaseTool {
	return &symbolTool{
		lspClients: lspClients,
	}
}

func (s *symbolTool) Info() ToolInfo {
	return ToolInfo{
		Name:        FindSymbolToolName,
		Description: findSymbolDescription,
		Parameters: map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Symbol name to query across workspace (e.g. function or type name)",
			},
			"kind": map[string]any{
				"type":        "string",
				"description": "Search mode: 'workspace' (default), 'document', 'references', 'definition', 'implementation'",
				"enum":        []string{"workspace", "document", "references", "definition", "implementation"},
			},
			"file_path": map[string]any{
				"type":        "string",
				"description": "Target file path (required for 'document', 'references', 'definition', 'implementation')",
			},
			"line": map[string]any{
				"type":        "integer",
				"description": "1-indexed line number for position-based lookup (references, definition, implementation)",
			},
			"character": map[string]any{
				"type":        "integer",
				"description": "1-indexed column number for position-based lookup (defaults to 1)",
			},
		},
	}
}

func (s *symbolTool) Capability() ToolCapability {
	return ToolCapability{
		IsReadOnly: true,
	}
}

func (s *symbolTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params FindSymbolParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("error parsing parameters: %s", err)), nil
	}

	if len(s.lspClients) == 0 {
		return NewTextErrorResponse("No LSP clients are currently active for this workspace. For text-based searching, please use grep or glob tools."), nil
	}

	kind := strings.ToLower(strings.TrimSpace(params.Kind))
	if kind == "" {
		if params.FilePath != "" && params.Query == "" {
			kind = "document"
		} else {
			kind = "workspace"
		}
	}

	switch kind {
	case "workspace":
		return s.runWorkspaceSymbol(ctx, params.Query)
	case "document":
		return s.runDocumentSymbol(ctx, params.FilePath)
	case "references":
		return s.runReferences(ctx, params)
	case "definition":
		return s.runDefinition(ctx, params)
	case "implementation":
		return s.runImplementation(ctx, params)
	default:
		return NewTextErrorResponse(fmt.Sprintf("unsupported search kind: '%s'", kind)), nil
	}
}

func (s *symbolTool) runWorkspaceSymbol(ctx context.Context, query string) (ToolResponse, error) {
	type matchResult struct {
		Name      string
		Kind      string
		FilePath  string
		Line      int
		Character int
	}

	var results []matchResult
	cwd := config.WorkingDirectory()

	for _, client := range s.lspClients {
		if client == nil {
			continue
		}
		rawRes, err := client.Symbol(ctx, protocol.WorkspaceSymbolParams{Query: query})
		if err != nil {
			continue
		}
		items, err := rawRes.Results()
		if err != nil {
			continue
		}

		for _, item := range items {
			loc := item.GetLocation()
			path := loc.URI.Path()
			if rel, err := filepath.Rel(cwd, path); err == nil && !strings.HasPrefix(rel, "..") {
				path = rel
			}

			kindStr := "symbol"
			if ws, ok := item.(*protocol.WorkspaceSymbol); ok {
				kindStr = formatSymbolKind(ws.Kind)
			} else if si, ok := item.(*protocol.SymbolInformation); ok {
				kindStr = formatSymbolKind(si.Kind)
			}

			results = append(results, matchResult{
				Name:      item.GetName(),
				Kind:      kindStr,
				FilePath:  path,
				Line:      int(loc.Range.Start.Line) + 1,
				Character: int(loc.Range.Start.Character) + 1,
			})
		}
	}

	if len(results) == 0 {
		return NewTextResponse(fmt.Sprintf("No symbols found matching query '%s' in workspace.", query)), nil
	}

	// Sort results by FilePath then Line
	sort.Slice(results, func(i, j int) bool {
		if results[i].FilePath != results[j].FilePath {
			return results[i].FilePath < results[j].FilePath
		}
		return results[i].Line < results[j].Line
	})

	const maxResults = 100
	truncated := false
	if len(results) > maxResults {
		results = results[:maxResults]
		truncated = true
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d symbol(s) for query '%s':\n\n", len(results), query))
	for _, r := range results {
		sb.WriteString(fmt.Sprintf("- [%s] %s (%s:%d:%d)\n", r.Kind, r.Name, r.FilePath, r.Line, r.Character))
	}
	if truncated {
		sb.WriteString(fmt.Sprintf("\n... [truncated to %d results] ...\n", maxResults))
	}

	return NewTextResponse(sb.String()), nil
}

func (s *symbolTool) runDocumentSymbol(ctx context.Context, targetPath string) (ToolResponse, error) {
	if targetPath == "" {
		return NewTextErrorResponse("file_path is required for 'document' symbol mode"), nil
	}

	absPath := targetPath
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(config.WorkingDirectory(), targetPath)
	}

	uri := protocol.URIFromPath(absPath)
	type docSymbolEntry struct {
		Name      string
		Kind      string
		StartLine int
		EndLine   int
	}

	var symbols []docSymbolEntry

	for _, client := range s.lspClients {
		if client == nil {
			continue
		}
		rawRes, err := client.DocumentSymbol(ctx, protocol.DocumentSymbolParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: uri},
		})
		if err != nil {
			continue
		}
		items, err := rawRes.Results()
		if err != nil {
			continue
		}

		for _, item := range items {
			kindStr := "symbol"
			if ds, ok := item.(*protocol.DocumentSymbol); ok {
				kindStr = formatSymbolKind(ds.Kind)
			} else if si, ok := item.(*protocol.SymbolInformation); ok {
				kindStr = formatSymbolKind(si.Kind)
			}

			rng := item.GetRange()
			symbols = append(symbols, docSymbolEntry{
				Name:      item.GetName(),
				Kind:      kindStr,
				StartLine: int(rng.Start.Line) + 1,
				EndLine:   int(rng.End.Line) + 1,
			})
		}
	}

	if len(symbols) == 0 {
		return NewTextResponse(fmt.Sprintf("No symbols found in %s.", targetPath)), nil
	}

	sort.Slice(symbols, func(i, j int) bool {
		return symbols[i].StartLine < symbols[j].StartLine
	})

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Symbols in %s (%d found):\n\n", targetPath, len(symbols)))
	for _, sym := range symbols {
		if sym.StartLine == sym.EndLine {
			sb.WriteString(fmt.Sprintf("- Line %d: [%s] %s\n", sym.StartLine, sym.Kind, sym.Name))
		} else {
			sb.WriteString(fmt.Sprintf("- Lines %d-%d: [%s] %s\n", sym.StartLine, sym.EndLine, sym.Kind, sym.Name))
		}
	}

	return NewTextResponse(sb.String()), nil
}

func (s *symbolTool) runReferences(ctx context.Context, params FindSymbolParams) (ToolResponse, error) {
	if params.FilePath == "" || params.Line <= 0 {
		return NewTextErrorResponse("file_path and line (1-indexed) are required for 'references' mode"), nil
	}

	absPath := params.FilePath
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(config.WorkingDirectory(), params.FilePath)
	}

	uri := protocol.URIFromPath(absPath)
	col := params.Character - 1
	if col < 0 {
		col = 0
	}

	var references []protocol.Location
	cwd := config.WorkingDirectory()

	for _, client := range s.lspClients {
		if client == nil {
			continue
		}
		locs, err := client.References(ctx, protocol.ReferenceParams{
			TextDocumentPositionParams: protocol.TextDocumentPositionParams{
				TextDocument: protocol.TextDocumentIdentifier{URI: uri},
				Position: protocol.Position{
					Line:      uint32(params.Line - 1),
					Character: uint32(col),
				},
			},
			Context: protocol.ReferenceContext{
				IncludeDeclaration: true,
			},
		})
		if err == nil && len(locs) > 0 {
			references = append(references, locs...)
		}
	}

	if len(references) == 0 {
		return NewTextResponse(fmt.Sprintf("No references found at %s:%d:%d", params.FilePath, params.Line, params.Character)), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d reference(s) for symbol at %s:%d:\n\n", len(references), params.FilePath, params.Line))
	for _, loc := range references {
		path := loc.URI.Path()
		if rel, err := filepath.Rel(cwd, path); err == nil && !strings.HasPrefix(rel, "..") {
			path = rel
		}
		sb.WriteString(fmt.Sprintf("- %s:%d:%d\n", path, loc.Range.Start.Line+1, loc.Range.Start.Character+1))
	}

	return NewTextResponse(sb.String()), nil
}

func (s *symbolTool) runDefinition(ctx context.Context, params FindSymbolParams) (ToolResponse, error) {
	if params.FilePath == "" || params.Line <= 0 {
		return NewTextErrorResponse("file_path and line (1-indexed) are required for 'definition' mode"), nil
	}

	absPath := params.FilePath
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(config.WorkingDirectory(), params.FilePath)
	}

	uri := protocol.URIFromPath(absPath)
	col := params.Character - 1
	if col < 0 {
		col = 0
	}

	cwd := config.WorkingDirectory()
	for _, client := range s.lspClients {
		if client == nil {
			continue
		}
		defResult, err := client.Definition(ctx, protocol.DefinitionParams{
			TextDocumentPositionParams: protocol.TextDocumentPositionParams{
				TextDocument: protocol.TextDocumentIdentifier{URI: uri},
				Position: protocol.Position{
					Line:      uint32(params.Line - 1),
					Character: uint32(col),
				},
			},
		})
		if err != nil || defResult.Value == nil {
			continue
		}

		switch v := defResult.Value.(type) {
		case protocol.Location:
			path := v.URI.Path()
			if rel, err := filepath.Rel(cwd, path); err == nil && !strings.HasPrefix(rel, "..") {
				path = rel
			}
			return NewTextResponse(fmt.Sprintf("Definition: %s:%d:%d", path, v.Range.Start.Line+1, v.Range.Start.Character+1)), nil
		case []protocol.Location:
			if len(v) > 0 {
				var sb strings.Builder
				sb.WriteString(fmt.Sprintf("Found %d definition location(s):\n", len(v)))
				for _, loc := range v {
					path := loc.URI.Path()
					if rel, err := filepath.Rel(cwd, path); err == nil && !strings.HasPrefix(rel, "..") {
						path = rel
					}
					sb.WriteString(fmt.Sprintf("- %s:%d:%d\n", path, loc.Range.Start.Line+1, loc.Range.Start.Character+1))
				}
				return NewTextResponse(sb.String()), nil
			}
		}
	}

	return NewTextResponse(fmt.Sprintf("Definition not found for symbol at %s:%d:%d", params.FilePath, params.Line, params.Character)), nil
}

func (s *symbolTool) runImplementation(ctx context.Context, params FindSymbolParams) (ToolResponse, error) {
	if params.FilePath == "" || params.Line <= 0 {
		return NewTextErrorResponse("file_path and line (1-indexed) are required for 'implementation' mode"), nil
	}

	absPath := params.FilePath
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(config.WorkingDirectory(), params.FilePath)
	}

	uri := protocol.URIFromPath(absPath)
	col := params.Character - 1
	if col < 0 {
		col = 0
	}

	cwd := config.WorkingDirectory()
	for _, client := range s.lspClients {
		if client == nil {
			continue
		}
		implResult, err := client.Implementation(ctx, protocol.ImplementationParams{
			TextDocumentPositionParams: protocol.TextDocumentPositionParams{
				TextDocument: protocol.TextDocumentIdentifier{URI: uri},
				Position: protocol.Position{
					Line:      uint32(params.Line - 1),
					Character: uint32(col),
				},
			},
		})
		if err != nil || implResult.Value == nil {
			continue
		}

		switch v := implResult.Value.(type) {
		case protocol.Location:
			path := v.URI.Path()
			if rel, err := filepath.Rel(cwd, path); err == nil && !strings.HasPrefix(rel, "..") {
				path = rel
			}
			return NewTextResponse(fmt.Sprintf("Implementation: %s:%d:%d", path, v.Range.Start.Line+1, v.Range.Start.Character+1)), nil
		case []protocol.Location:
			if len(v) > 0 {
				var sb strings.Builder
				sb.WriteString(fmt.Sprintf("Found %d implementation location(s):\n", len(v)))
				for _, loc := range v {
					path := loc.URI.Path()
					if rel, err := filepath.Rel(cwd, path); err == nil && !strings.HasPrefix(rel, "..") {
						path = rel
					}
					sb.WriteString(fmt.Sprintf("- %s:%d:%d\n", path, loc.Range.Start.Line+1, loc.Range.Start.Character+1))
				}
				return NewTextResponse(sb.String()), nil
			}
		}
	}

	return NewTextResponse(fmt.Sprintf("No implementations found for symbol at %s:%d:%d", params.FilePath, params.Line, params.Character)), nil
}

func formatSymbolKind(kind protocol.SymbolKind) string {
	switch kind {
	case protocol.File:
		return "file"
	case protocol.Module:
		return "module"
	case protocol.Namespace:
		return "namespace"
	case protocol.Package:
		return "package"
	case protocol.Class:
		return "class"
	case protocol.Method:
		return "method"
	case protocol.Property:
		return "property"
	case protocol.Field:
		return "field"
	case protocol.Constructor:
		return "constructor"
	case protocol.Enum:
		return "enum"
	case protocol.Interface:
		return "interface"
	case protocol.Function:
		return "function"
	case protocol.Variable:
		return "variable"
	case protocol.Constant:
		return "constant"
	case protocol.String:
		return "string"
	case protocol.Number:
		return "number"
	case protocol.Boolean:
		return "boolean"
	case protocol.Array:
		return "array"
	case protocol.Object:
		return "object"
	case protocol.Key:
		return "key"
	case protocol.Null:
		return "null"
	case protocol.EnumMember:
		return "enum_member"
	case protocol.Struct:
		return "struct"
	case protocol.Event:
		return "event"
	case protocol.Operator:
		return "operator"
	case protocol.TypeParameter:
		return "type_param"
	default:
		return fmt.Sprintf("kind_%d", kind)
	}
}
