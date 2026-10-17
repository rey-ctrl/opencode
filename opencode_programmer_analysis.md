# Analisis OpenCode: Peningkatan untuk Developer Experience

Analisis menyeluruh dari seluruh codebase — agent, tools, provider, TUI, session, shell, prompt.

---

## 🔍 Temuan Masalah Kritis (dari kode aktual)

### 1. Shell: Single Global Instance — Bottleneck Nyata
**File**: [`shell/shell.go#L43-L58`](file:///c:/Users/Adit/Documents/My%20Life/Project/Gawean/on_god/opencode/internal/llm/tools/shell/shell.go#L43-L58)

```go
var (
    shellInstance     *PersistentShell      // ← SATU instance global
    shellInstanceOnce sync.Once
)
```

**Masalah**: Semua tool bash dari SEMUA agent/subagent berbagi satu shell instance. Jika 3 subagent jalan paralel dan semuanya panggil bash, mereka antre satu per satu. State shell (env vars, cwd) juga bisa saling tercampur.

**Solusi**: Shell pool per session, bukan singleton global.

---

### 2. Tool Execution: Serial Total
**File**: [`agent.go#L350-L420`](file:///c:/Users/Adit/Documents/My%20Life/Project/Gawean/on_god/opencode/internal/llm/agent/agent.go#L350-L420)

Ketika AI memanggil 3 tools sekaligus (grep, glob, view), ketiganya dijalankan satu per satu. Untuk developer yang sering searching codebase, ini sangat lambat.

---

### 3. Banned Commands: Terlalu Agresif untuk Developer
**File**: [`bash.go#L41-L45`](file:///c:/Users/Adit/Documents/My%20Life/Project/Gawean/on_god/opencode/internal/llm/tools/bash.go#L41-L45)

```go
var bannedCommands = []string{
    "alias", "curl", "curlie", "wget", "axel", "aria2c",
    "nc", "telnet", "lynx", "w3m", "links", "httpie", "xh",
    ...
}
```

`curl` dan `wget` dibanned. Ini menyulitkan developer yang perlu test API, download dependencies, atau check endpoints. Bisa dikonfigurasi per-project saja.

---

### 4. Output Truncation: Potong di Tengah
**File**: [`bash.go#L329-L340`](file:///c:/Users/Adit/Documents/My%20Life/Project/Gawean/on_god/opencode/internal/llm/tools/bash.go#L329-L340)

```go
func truncateOutput(content string) string {
    // Potong half dari awal + half dari akhir
    start := content[:halfLength]
    end := content[len(content)-halfLength:]
}
```

Saat compile error panjang atau test output besar, AI hanya dapat potongan tengah dibuang. Output test/lint lebih baik diprioritaskan dari **bawah** (error terbaru), bukan dipotong simetris.

---

### 5. View Tool: Tidak Ada Symbol Navigation
**File**: [`view.go`](file:///c:/Users/Adit/Documents/My%20Life/Project/Gawean/on_god/opencode/internal/llm/tools/view.go)

Hanya bisa baca file per baris (offset/limit). Tidak ada kemampuan "jump to function definition" padahal LSP client sudah tersedia di tool.

---

### 6. Grep Tool: Tidak Support AST/Semantic Search
**File**: [`grep.go`](file:///c:/Users/Adit/Documents/My%20Life/Project/Gawean/on_god/opencode/internal/llm/tools/grep.go)

Pure regex text search. Tidak bisa cari "semua implementasi interface X" atau "semua caller fungsi Y" padahal info ini sudah ada di LSP.

---

### 7. Context Injection: Static, Tidak Adaptive
**File**: [`coder.go#L170-L189`](file:///c:/Users/Adit/Documents/My%20Life/Project/Gawean/on_god/opencode/internal/llm/prompt/coder.go#L170-L189)

Environment info di-inject sekali di awal session (working dir, git status, ls output). Tidak pernah di-refresh. Jika developer berpindah branch, menambah file, atau mengubah dependencies di tengah session, AI tetap punya info lama.

---

### 8. Agent Type: Hanya 2 Tier (Coder + Task)
**File**: [`config.go#L39-L44`](file:///c:/Users/Adit/Documents/My%20Life/Project/Gawean/on_god/opencode/internal/config/config.go#L39-L44)

```go
const (
    AgentCoder      AgentName = "coder"
    AgentSummarizer AgentName = "summarizer"
    AgentTask       AgentName = "task"
    AgentTitle      AgentName = "title"
)
```

Task agent hanya punya tools: glob, grep, ls, view, sourcegraph — tidak bisa bash atau edit. Tidak ada agen spesialis: "debugger agent", "reviewer agent", "tester agent" dengan tool set berbeda.

---

### 9. Subagent (AgentTool): Tidak Bisa Dapat Hasil Partial
**File**: [`agent-tool.go#L67-L71`](file:///c:/Users/Adit/Documents/My%20Life/Project/Gawean/on_god/opencode/internal/llm/agent/agent-tool.go#L67-L71)

Subagent blocking sampai selesai. Tidak ada streaming progress ke parent agent. Developer tidak bisa lihat subagent sedang mengerjakan apa.

---

### 10. TUI: Tidak Ada Diff Viewer
**File**: [`tui.go`](file:///c:/Users/Adit/Documents/My%20Life/Project/Gawean/on_god/opencode/internal/tui/tui.go)

Ketika AI mengedit file, kamu hanya dapat notifikasi teks. Tidak ada visual diff yang terintegrasi langsung di TUI.

---

## 💡 Semua Peningkatan yang Bisa Dilakukan

### TIER 1 — Impact Tinggi, Kompleksitas Sedang

#### A. Shell Pool per Session
**Lokasi**: `internal/llm/tools/shell/shell.go`

```go
// GANTI singleton dengan pool
type ShellPool struct {
    mu      sync.RWMutex
    shells  map[string]*PersistentShell // key: sessionID
}

var globalPool = &ShellPool{shells: make(map[string]*PersistentShell)}

func GetShellForSession(sessionID, workingDir string) *PersistentShell {
    globalPool.mu.RLock()
    if s, ok := globalPool.shells[sessionID]; ok && s.isAlive {
        globalPool.mu.RUnlock()
        return s
    }
    globalPool.mu.RUnlock()
    
    globalPool.mu.Lock()
    defer globalPool.mu.Unlock()
    s := newPersistentShell(workingDir)
    globalPool.shells[sessionID] = s
    return s
}
```

**Dampak**: Subagent paralel tidak saling blokir. State shell terisolasi per session.

---

#### B. Parallel Tool Execution
**Lokasi**: `internal/llm/agent/agent.go`

Ganti loop serial di `streamAndHandleEvents` dengan goroutine pool. Tools yang bersifat read-only (view, grep, glob, ls) boleh paralel sepenuhnya. Tools yang write (edit, write, patch) harus dikunci per file path.

```go
// Tambah metadata ke interface BaseTool
type ToolCapability struct {
    IsReadOnly   bool   // boleh paralel tanpa lock
    MutatesFiles bool   // butuh per-file mutex
    IsAtomic     bool   // harus serial total
}

type BaseTool interface {
    Info() ToolInfo
    Run(ctx context.Context, params ToolCall) (ToolResponse, error)
    Capability() ToolCapability  // ← tambah ini
}
```

---

#### C. Smart Output Truncation
**Lokasi**: `internal/llm/tools/bash.go`

```go
func truncateOutput(content string) string {
    if len(content) <= MaxOutputLength {
        return content
    }
    // Untuk developer: prioritaskan output TERBARU (errors, test results)
    // Potong dari awal, pertahankan akhir
    keepFromEnd := int(float64(MaxOutputLength) * 0.8)
    keepFromStart := MaxOutputLength - keepFromEnd
    
    start := content[:keepFromStart]
    end := content[len(content)-keepFromEnd:]
    
    linesDropped := countLines(content[keepFromStart : len(content)-keepFromEnd])
    return fmt.Sprintf("%s\n\n... [%d lines from middle omitted] ...\n\n%s", 
        start, linesDropped, end)
}
```

---

#### D. Configurable Command Allowlist
**Lokasi**: `internal/config/config.go` + `internal/llm/tools/bash.go`

```go
// Di Config struct, tambah:
type BashConfig struct {
    AllowedExtraCommands []string `json:"allowedExtraCommands"` // misal: ["curl", "wget"]
    BannedCommands       []string `json:"bannedCommands"`        // override default bans
}

// Di Config:
Bash BashConfig `json:"bash,omitempty"`
```

Dengan ini, tim bisa whitelist `curl` untuk project API testing tanpa modifikasi kode.

---

#### E. LSP-Powered Symbol Search Tool
**Lokasi**: `internal/llm/tools/` — file baru `symbol.go`

```go
// Tool baru: "find_symbol"
type FindSymbolParams struct {
    Symbol   string `json:"symbol"`    // nama fungsi/class/interface
    Kind     string `json:"kind"`      // "definition" | "references" | "implementation"
    FilePath string `json:"file_path"` // opsional, untuk scope
}

// Gunakan LSP workspace/symbol dan textDocument/references
// yang sudah tersedia via lspClients
```

Ini memungkinkan AI untuk query: "semua implementasi interface X", "semua caller fungsi Y" — tanpa grep teks biasa yang sering salah match.

---

#### F. Auto-Refresh Environment Context
**Lokasi**: `internal/llm/prompt/coder.go`

```go
// Saat ini: dicompute sekali di awal session
func CoderPrompt(provider models.ModelProvider) string {
    envInfo := getEnvironmentInfo() // ← static
    return fmt.Sprintf(...)
}

// Solusi: inject context dinamis ke setiap request
// Di agent.go processGeneration, sebelum kirim ke provider:
func (a *agent) buildDynamicSystemContext() string {
    return fmt.Sprintf(`
<current_state>
Git branch: %s
Last modified files: %s
Timestamp: %s
</current_state>`,
        getCurrentBranch(),
        getRecentlyModifiedFiles(),
        time.Now().Format(time.RFC3339),
    )
}
```

---

### TIER 2 — Specialist Agents

#### G. Tambah Agent Specialist dengan Tool Set Berbeda
**Lokasi**: `internal/config/config.go` + `internal/llm/agent/tools.go`

```go
// Config baru
const (
    AgentCoder      AgentName = "coder"
    AgentSummarizer AgentName = "summarizer"
    AgentTask       AgentName = "task"
    AgentTitle      AgentName = "title"
    
    // ← TAMBAH:
    AgentReviewer   AgentName = "reviewer"   // read-only, fokus code quality
    AgentTester     AgentName = "tester"     // bash (run tests), read files
    AgentDebugger   AgentName = "debugger"   // bash, view, diagnostics, no edit
    AgentArchitect  AgentName = "architect"  // hanya read + subagent dispatch
)

// Tool sets berbeda:
func ReviewerAgentTools(lspClients) []tools.BaseTool {
    return []tools.BaseTool{
        tools.NewGlobTool(),
        tools.NewGrepTool(),
        tools.NewViewTool(lspClients),
        tools.NewDiagnosticsTool(lspClients),
        tools.NewFindSymbolTool(lspClients), // tool baru
        // Tidak ada bash, edit, write
    }
}

func TesterAgentTools(permissions, lspClients) []tools.BaseTool {
    return []tools.BaseTool{
        tools.NewBashTool(permissions), // untuk run tests
        tools.NewViewTool(lspClients),
        tools.NewGrepTool(),
        tools.NewDiagnosticsTool(lspClients),
        // Tidak ada edit/write — hanya run & report
    }
}
```

---

#### H. Streaming Progress dari Subagent ke Parent
**Lokasi**: `internal/llm/agent/agent-tool.go`

```go
// SAAT INI: blocking, tidak ada feedback
done, err := agent.Run(ctx, session.ID, params.Prompt)
result := <-done

// MODIFIKASI: subscribe ke pubsub subagent untuk forward progress
sub := agent.Subscribe()
defer agent.Unsubscribe(sub)

done, err := agent.Run(ctx, session.ID, params.Prompt)

// Forward events ke parent via context/channel
for {
    select {
    case event := <-sub:
        if event.Type == AgentEventTypeResponse && event.Done {
            return formatResult(event), nil
        }
        // Log progress untuk parent agent
        logging.Info("Subagent progress", "session", session.ID, 
            "content_preview", event.Message.Content().String()[:min(100, ...)])
    case <-ctx.Done():
        return tools.ToolResponse{}, ctx.Err()
    }
}
```

---

#### I. Project Memory yang Persisten
**Lokasi**: Package baru `internal/memory/`

OpenCode sudah punya `OPENCODE.md` untuk manual memory, tapi AI tidak otomatis menulis/membaca dari database. Solusi:

```go
// internal/memory/memory.go
type Memory struct {
    ID        string
    ProjectID string   // hash dari working directory
    Key       string   // "preferred_test_command", "architecture_decision", dll
    Value     string
    CreatedAt int64
}

type Service interface {
    Store(ctx context.Context, projectID, key, value string) error
    Retrieve(ctx context.Context, projectID string, keys ...string) ([]Memory, error)
    Search(ctx context.Context, projectID, query string) ([]Memory, error)
}
```

Tambah tool `memory_write` dan `memory_read` untuk agent:

```go
// Tool: "memory_write" — simpan informasi ke DB
// Tool: "memory_read"  — query memory sebelumnya

// Di system prompt, auto-inject memory relevan:
func (a *agent) buildSystemWithMemory(ctx context.Context, sessionID string) string {
    memories, _ := a.memory.Retrieve(ctx, config.ProjectID())
    if len(memories) == 0 {
        return a.baseSystemPrompt
    }
    memStr := formatMemories(memories)
    return a.baseSystemPrompt + "\n\n<project_memory>\n" + memStr + "\n</project_memory>"
}
```

---

#### J. Tool Call Retry dengan Backoff
**Lokasi**: `internal/llm/agent/agent.go`

Saat ini, jika tool gagal (misal permission denied, file not found), error langsung dikembalikan ke AI. AI sering tidak tau harus retry atau tidak.

```go
func (a *agent) executeToolWithRetry(ctx context.Context, tool tools.BaseTool, call tools.ToolCall) (tools.ToolResponse, error) {
    const maxRetries = 2
    backoff := 100 * time.Millisecond
    
    for attempt := 0; attempt <= maxRetries; attempt++ {
        result, err := tool.Run(ctx, call)
        if err == nil && !result.IsError {
            return result, nil
        }
        
        // Jangan retry jika permission denied (butuh user action)
        if errors.Is(err, permission.ErrorPermissionDenied) {
            return result, err
        }
        
        if attempt < maxRetries {
            select {
            case <-ctx.Done():
                return tools.ToolResponse{}, ctx.Err()
            case <-time.After(backoff):
                backoff *= 2
            }
        }
    }
    return tool.Run(ctx, call) // final attempt
}
```

---

#### K. Git-Aware Context dalam System Prompt
**Lokasi**: `internal/llm/prompt/coder.go`

```go
func getGitContext(cwd string) string {
    // Inject info yang relevan untuk developer:
    // - Current branch
    // - Uncommitted changes summary  
    // - Recent commit messages (3-5 terakhir)
    // - Files yang sudah dimodifikasi di session ini
    
    shell := exec.Command("git", "-C", cwd, "log", "--oneline", "-5")
    // ...
    
    return fmt.Sprintf(`
<git_context>
Branch: %s
Staged: %s
Unstaged: %s  
Recent commits:
%s
</git_context>`, branch, staged, unstaged, recentCommits)
}
```

---

#### L. TUI: Inline Diff Viewer
**Lokasi**: `internal/tui/` — komponen baru

Ketika AI mengedit file, tampilkan diff langsung di TUI menggunakan library `diff` yang sudah ada di `internal/diff/`. Bisa implementasi sebagai panel samping atau popup saat tool edit dipanggil.

---

#### M. Multi-File Edit Coordinator
**Lokasi**: Tool baru `internal/llm/tools/multi_edit.go`

Saat ini AI mengedit file satu per satu. Untuk refactor besar (rename function yang dipakai di 20 file), AI harus panggil edit tool 20 kali secara serial.

```go
type MultiEditParams struct {
    Edits []struct {
        FilePath  string `json:"file_path"`
        OldString string `json:"old_string"`
        NewString string `json:"new_string"`
    } `json:"edits"`
    ValidateConsistency bool `json:"validate_consistency"` // cek tidak ada konflik
}
```

Batch edits dengan validasi konsistensi antar file — lebih aman untuk refactoring besar.

---

#### N. Test Runner Tool yang Aware
**Lokasi**: Tool baru `internal/llm/tools/test_runner.go`

```go
type TestRunnerParams struct {
    TestPattern  string `json:"test_pattern"`  // go test pattern, pytest mark, dll
    Timeout      int    `json:"timeout_ms"`
    ParseResults bool   `json:"parse_results"` // structured output vs raw
}

type TestResult struct {
    Passed  int
    Failed  int
    Skipped int
    Errors  []TestError
    Duration float64
}
```

Tool ini parse output test runner (go test, pytest, jest, cargo test) dan return structured result, bukan raw text. AI bisa langsung tau "3 test gagal di fungsi X" tanpa parse sendiri.

---

## 📊 Prioritas Implementasi

| # | Mod | File Utama | Effort | Impact Dev |
|---|-----|------------|--------|------------|
| 1 | Shell Pool per Session | `shell/shell.go` | S | 🔥🔥🔥 Fixes subagent parallelism |
| 2 | Smart Output Truncation | `bash.go` | XS | 🔥🔥🔥 AI dapat error context lebih baik |
| 3 | Parallel Tool Execution | `agent.go` | M | 🔥🔥🔥 Speed up semua tool calls |
| 4 | Configurable Banned Cmds | `config.go`+`bash.go` | XS | 🔥🔥 Unlock curl/wget per project |
| 5 | LSP Symbol Search Tool | `tools/symbol.go` | M | 🔥🔥🔥 Semantic code navigation |
| 6 | Git Context Injection | `prompt/coder.go` | S | 🔥🔥 Better git-aware responses |
| 7 | Specialist Agent Types | `config.go`+`tools.go` | M | 🔥🔥 Safer task delegation |
| 8 | Tool Retry with Backoff | `agent.go` | S | 🔥🔥 Fewer transient failures |
| 9 | Project Memory (DB) | `internal/memory/` | L | 🔥🔥🔥 No repeated re-discovery |
| 10 | Multi-File Edit | `tools/multi_edit.go` | M | 🔥🔥 Better refactoring |
| 11 | Test Runner Tool | `tools/test_runner.go` | M | 🔥🔥 Structured test feedback |
| 12 | Dynamic Env Refresh | `prompt/coder.go` | S | 🔥 Better long session accuracy |
| 13 | Subagent Progress Stream | `agent-tool.go` | M | 🔥 Better observability |
| 14 | TUI Inline Diff | `tui/` | L | 🔥 Better visual feedback |

---

## 🏗️ Quick Wins (Bisa dikerjakan < 2 jam)

1. **Smart truncation** — 10 baris perubahan di `bash.go`
2. **Configurable banned commands** — tambah field di `Config` struct + check di `bash.go`
3. **Git context** — tambah `exec.Command` calls di `prompt/coder.go`
4. **Tool retry** — wrapper sederhana di `agent.go`

## 🔨 High-Impact Mods (1-2 hari)

5. **Shell pool** — refactor singleton → map di `shell.go`
6. **Parallel tools** — goroutine pool + capability interface
7. **LSP symbol tool** — gunakan `lspClients` yang sudah ada
8. **Specialist agents** — tambah constants + tool sets

## 🚀 Transformational (1 minggu+)

9. **Project memory system** — package baru, schema DB baru, 2 tools baru
10. **Multi-file edit** — koordinasi + validasi + rollback
11. **Test runner** — parser per bahasa (Go, Python, JS, Rust)
