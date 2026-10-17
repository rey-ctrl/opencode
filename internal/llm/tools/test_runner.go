package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/tools/shell"
	"github.com/opencode-ai/opencode/internal/permission"
)

const (
	TestRunnerToolName = "test_runner"
	testRunnerDescription = `Runs test suites across various languages and returns structured test reports (pass/fail counts, duration, and exact failure details).

WHEN TO USE THIS TOOL:
- Running unit, integration, or regression tests during TDD or debugging.
- Automatically isolates failing tests, line numbers, and error messages instead of forcing you to sift through hundreds of raw log lines.
- Far cleaner and more structured than running tests through raw bash.

SUPPORTED FRAMEWORKS:
- "go": Runs 'go test -json' and parses Go test event streams.
- "python": Runs pytest with structured short tracebacks.
- "node": Runs npm test / vitest / jest.
- "rust": Runs cargo test.
- "auto" (default): Auto-detects based on project files (go.mod, package.json, pytest.ini/pyproject.toml, Cargo.toml).

PARAMETERS:
- framework: "auto" | "go" | "python" | "node" | "rust" (default: "auto")
- command: Optional custom command override (e.g. "go test -v ./internal/llm/tools")
- package: Package or folder path to test (e.g. "./internal/llm/tools", "tests/")
- test_filter: Pattern or substring to filter tests (e.g. "TestFindSymbol", "test_auth")
- timeout: Timeout in seconds (default: 120)`
)

type TestRunnerParams struct {
	Framework  string `json:"framework,omitempty"`
	Command    string `json:"command,omitempty"`
	Package    string `json:"package,omitempty"`
	TestFilter string `json:"test_filter,omitempty"`
	Timeout    int    `json:"timeout,omitempty"`
}

type TestFailureDetail struct {
	TestName     string `json:"test_name"`
	Package      string `json:"package,omitempty"`
	File         string `json:"file,omitempty"`
	Line         int    `json:"line,omitempty"`
	ErrorMessage string `json:"error_message"`
	Output       string `json:"output,omitempty"`
}

type TestReport struct {
	Framework string              `json:"framework"`
	Command   string              `json:"command"`
	Passed    int                 `json:"passed"`
	Failed    int                 `json:"failed"`
	Skipped   int                 `json:"skipped"`
	Duration  float64             `json:"duration_seconds"`
	Success   bool                `json:"success"`
	Failures  []TestFailureDetail `json:"failures,omitempty"`
	RawOutput string              `json:"raw_output,omitempty"`
}

type testRunnerTool struct {
	permissions permission.Service
}

func NewTestRunnerTool(permissions permission.Service) BaseTool {
	return &testRunnerTool{
		permissions: permissions,
	}
}

func (t *testRunnerTool) Info() ToolInfo {
	return ToolInfo{
		Name:        TestRunnerToolName,
		Description: testRunnerDescription,
		Parameters: map[string]any{
			"framework": map[string]any{
				"type":        "string",
				"description": "Test framework: 'auto' (default), 'go', 'python', 'node', 'rust'",
				"enum":        []string{"auto", "go", "python", "node", "rust"},
			},
			"command": map[string]any{
				"type":        "string",
				"description": "Custom command to run instead of the default test command",
			},
			"package": map[string]any{
				"type":        "string",
				"description": "Package or directory to target (e.g. './internal/llm/tools' or 'tests/')",
			},
			"test_filter": map[string]any{
				"type":        "string",
				"description": "Filter test names (maps to -run in go, -k in pytest, -t in jest)",
			},
			"timeout": map[string]any{
				"type":        "integer",
				"description": "Test execution timeout in seconds (default: 120)",
			},
		},
	}
}

func (t *testRunnerTool) Capability() ToolCapability {
	return ToolCapability{
		IsReadOnly:   false,
		MutatesFiles: false,
		IsAtomic:     true,
	}
}

func (t *testRunnerTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params TestRunnerParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return NewTextErrorResponse("invalid parameters for test_runner: " + err.Error()), nil
	}

	sessionID, messageID := GetContextValues(ctx)
	if sessionID == "" || messageID == "" {
		return ToolResponse{}, fmt.Errorf("session ID and message ID are required")
	}

	wd := config.WorkingDirectory()
	framework := strings.ToLower(strings.TrimSpace(params.Framework))
	if framework == "" || framework == "auto" {
		framework = detectFramework(wd)
	}

	cmdStr := buildTestCommand(framework, params)
	timeoutSec := params.Timeout
	if timeoutSec <= 0 {
		timeoutSec = 120
	}
	timeoutMs := timeoutSec * 1000

	// Permission request
	permitted := t.permissions.Request(
		permission.CreatePermissionRequest{
			SessionID:   sessionID,
			Path:        wd,
			ToolName:    TestRunnerToolName,
			Action:      "execute",
			Description: fmt.Sprintf("Run test suite: %s", cmdStr),
			Params: BashPermissionsParams{
				Command: cmdStr,
			},
		},
	)
	if !permitted {
		return ToolResponse{}, permission.ErrorPermissionDenied
	}

	s := shell.GetShellForSession(sessionID, wd)
	if s == nil {
		return NewTextErrorResponse("failed to initialize persistent shell for test execution"), nil
	}

	startTime := time.Now()
	stdout, stderr, exitCode, interrupted, err := s.Exec(ctx, cmdStr, timeoutMs)
	duration := time.Since(startTime).Seconds()
	if err != nil {
		return ToolResponse{}, fmt.Errorf("error executing test command: %w", err)
	}

	var report TestReport
	if framework == "go" && strings.Contains(cmdStr, "-json") {
		report = parseGoTestJson(stdout, stderr, exitCode, duration)
	} else {
		report = parseGenericTestOutput(framework, stdout, stderr, exitCode, duration)
	}
	report.Command = cmdStr
	report.Framework = framework

	if interrupted {
		report.Success = false
		report.Failures = append(report.Failures, TestFailureDetail{
			TestName:     "TestTimeoutOrInterrupted",
			ErrorMessage: fmt.Sprintf("Test execution timed out after %d seconds or was aborted", timeoutSec),
		})
	}

	textResponse := formatTestReport(report)
	return WithResponseMetadata(
		NewTextResponse(textResponse),
		report,
	), nil
}

func detectFramework(dir string) string {
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
		return "go"
	}
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err == nil {
		return "node"
	}
	for _, f := range []string{"pytest.ini", "pyproject.toml", "setup.py", "requirements.txt"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			return "python"
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "Cargo.toml")); err == nil {
		return "rust"
	}
	return "go" // default fallback
}

func buildTestCommand(framework string, params TestRunnerParams) string {
	if params.Command != "" {
		return params.Command
	}

	switch framework {
	case "go":
		cmd := "go test -json"
		if params.TestFilter != "" {
			cmd += fmt.Sprintf(" -run %s", shellQuoteParam(params.TestFilter))
		}
		if params.Package != "" {
			cmd += " " + params.Package
		} else {
			cmd += " ./..."
		}
		return cmd
	case "python":
		cmd := "pytest -v --tb=short"
		if params.TestFilter != "" {
			cmd += fmt.Sprintf(" -k %s", shellQuoteParam(params.TestFilter))
		}
		if params.Package != "" {
			cmd += " " + params.Package
		}
		return cmd
	case "node":
		cmd := "npm test --"
		if params.TestFilter != "" {
			cmd += fmt.Sprintf(" -t %s", shellQuoteParam(params.TestFilter))
		}
		if params.Package != "" {
			cmd += " " + params.Package
		}
		return cmd
	case "rust":
		cmd := "cargo test"
		if params.TestFilter != "" {
			cmd += " " + params.TestFilter
		}
		return cmd
	default:
		if params.Package != "" {
			return "go test -json " + params.Package
		}
		return "go test -json ./..."
	}
}

func shellQuoteParam(s string) string {
	if !strings.ContainsAny(s, " \t\n\r\"'") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

type goTestEvent struct {
	Time    time.Time `json:"Time"`
	Action  string    `json:"Action"`
	Package string    `json:"Package"`
	Test    string    `json:"Test"`
	Elapsed float64   `json:"Elapsed"`
	Output  string    `json:"Output"`
}

var goLocationRegex = regexp.MustCompile(`([a-zA-Z0-9_\-\./]+\.go):(\d+):`)

func parseGoTestJson(stdout, stderr string, exitCode int, duration float64) TestReport {
	report := TestReport{
		Duration:  duration,
		Success:   exitCode == 0,
		RawOutput: stdout,
	}

	testOutputs := make(map[string][]string)
	failedTests := make(map[string]bool)
	testPackages := make(map[string]string)
	packageErrors := make(map[string][]string)

	scanner := bufio.NewScanner(strings.NewReader(stdout))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "{") || !strings.HasSuffix(line, "}") {
			continue
		}

		var event goTestEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}

		key := event.Package + "/" + event.Test

		if event.Test != "" {
			testPackages[key] = event.Package
			if event.Output != "" {
				testOutputs[key] = append(testOutputs[key], event.Output)
			}

			switch event.Action {
			case "pass":
				report.Passed++
			case "skip":
				report.Skipped++
			case "fail":
				report.Failed++
				failedTests[key] = true
			}
		} else {
			// Package-level event
			if event.Action == "output" && event.Output != "" {
				packageErrors[event.Package] = append(packageErrors[event.Package], event.Output)
			}
			if event.Action == "fail" && len(failedTests) == 0 {
				// Package failed with no individual test failures (e.g. build error)
				report.Failed++
				errLines := strings.Join(packageErrors[event.Package], "")
				report.Failures = append(report.Failures, TestFailureDetail{
					TestName:     "BuildFailure:" + event.Package,
					Package:      event.Package,
					ErrorMessage: strings.TrimSpace(errLines),
				})
			}
		}
	}

	for key := range failedTests {
		parts := strings.Split(key, "/")
		pkg := testPackages[key]
		testName := parts[len(parts)-1]

		outputLines := testOutputs[key]
		fullOutput := strings.Join(outputLines, "")

		file := ""
		line := 0
		firstErrorMsg := ""

		for _, out := range outputLines {
			trimmed := strings.TrimSpace(out)
			if match := goLocationRegex.FindStringSubmatch(trimmed); len(match) > 2 {
				file = match[1]
				line, _ = strconv.Atoi(match[2])
				firstErrorMsg = trimmed
				break
			}
		}

		if firstErrorMsg == "" && len(outputLines) > 0 {
			for _, l := range outputLines {
				if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "=== RUN") && !strings.HasPrefix(t, "--- FAIL") {
					firstErrorMsg = t
					break
				}
			}
		}

		report.Failures = append(report.Failures, TestFailureDetail{
			TestName:     testName,
			Package:      pkg,
			File:         file,
			Line:         line,
			ErrorMessage: firstErrorMsg,
			Output:       truncateOutputLines(fullOutput, 30),
		})
	}

	if exitCode != 0 && report.Passed == 0 && report.Failed == 0 {
		report.Failed = 1
		report.Failures = append(report.Failures, TestFailureDetail{
			TestName:     "ExecutionError",
			ErrorMessage: strings.TrimSpace(stderr + "\n" + stdout),
		})
	}

	return report
}

var (
	pyFailedRegex  = regexp.MustCompile(`(\d+)\s+failed`)
	pyPassedRegex  = regexp.MustCompile(`(\d+)\s+passed`)
	pySkippedRegex = regexp.MustCompile(`(\d+)\s+skipped`)

	jestFailedRegex = regexp.MustCompile(`(\d+)\s+failed`)
	jestPassedRegex = regexp.MustCompile(`(\d+)\s+passed`)

	cargoTestRegex = regexp.MustCompile(`test result:\s+(ok|FAILED)\.\s+(\d+)\s+passed;\s+(\d+)\s+failed;\s+(\d+)\s+ignored`)
)

func parseGenericTestOutput(framework, stdout, stderr string, exitCode int, duration float64) TestReport {
	report := TestReport{
		Duration:  duration,
		Success:   exitCode == 0,
		RawOutput: stdout + "\n" + stderr,
	}

	combined := stdout + "\n" + stderr
	lines := strings.Split(combined, "\n")

	switch framework {
	case "python":
		for _, line := range lines {
			if strings.Contains(line, "passed") || strings.Contains(line, "failed") {
				if m := pyPassedRegex.FindStringSubmatch(line); len(m) > 1 {
					report.Passed, _ = strconv.Atoi(m[1])
				}
				if m := pyFailedRegex.FindStringSubmatch(line); len(m) > 1 {
					report.Failed, _ = strconv.Atoi(m[1])
				}
				if m := pySkippedRegex.FindStringSubmatch(line); len(m) > 1 {
					report.Skipped, _ = strconv.Atoi(m[1])
				}
			}
		}
	case "node":
		for _, line := range lines {
			if strings.Contains(line, "Tests:") {
				if m := jestPassedRegex.FindStringSubmatch(line); len(m) > 1 {
					report.Passed, _ = strconv.Atoi(m[1])
				}
				if m := jestFailedRegex.FindStringSubmatch(line); len(m) > 1 {
					report.Failed, _ = strconv.Atoi(m[1])
				}
			}
		}
	case "rust":
		for _, line := range lines {
			if match := cargoTestRegex.FindStringSubmatch(line); len(match) > 4 {
				report.Passed, _ = strconv.Atoi(match[2])
				report.Failed, _ = strconv.Atoi(match[3])
				report.Skipped, _ = strconv.Atoi(match[4])
			}
		}
	}

	if exitCode != 0 {
		report.Success = false
		if report.Failed == 0 {
			report.Failed = 1
		}
		// Extract tail lines as failure description
		tail := lines
		if len(tail) > 25 {
			tail = tail[len(tail)-25:]
		}
		report.Failures = append(report.Failures, TestFailureDetail{
			TestName:     "TestExecutionFailure",
			ErrorMessage: strings.TrimSpace(strings.Join(tail, "\n")),
		})
	} else {
		report.Success = true
	}

	return report
}

func truncateOutputLines(s string, maxLines int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s
	}
	kept := lines[len(lines)-maxLines:]
	return fmt.Sprintf("... [omitted %d earlier lines] ...\n%s", len(lines)-maxLines, strings.Join(kept, "\n"))
}

func formatTestReport(report TestReport) string {
	var sb strings.Builder

	statusBadge := "✅ PASSED"
	if !report.Success {
		statusBadge = "❌ FAILED"
	}

	sb.WriteString(fmt.Sprintf("### 🧪 Test Run Results: %s\n", statusBadge))
	sb.WriteString(fmt.Sprintf("- **Framework**: %s\n", strings.ToUpper(report.Framework)))
	sb.WriteString(fmt.Sprintf("- **Command**: `%s`\n", report.Command))
	sb.WriteString(fmt.Sprintf("- **Duration**: %.2fs\n", report.Duration))
	sb.WriteString(fmt.Sprintf("- **Summary**: **%d passed**, **%d failed**, **%d skipped**\n\n",
		report.Passed, report.Failed, report.Skipped))

	if len(report.Failures) > 0 {
		sb.WriteString(fmt.Sprintf("#### ❌ Failure Details (%d):\n", len(report.Failures)))
		for i, f := range report.Failures {
			sb.WriteString(fmt.Sprintf("\n**%d. %s**", i+1, f.TestName))
			if f.Package != "" {
				sb.WriteString(fmt.Sprintf(" *(%s)*", f.Package))
			}
			if f.File != "" && f.Line > 0 {
				sb.WriteString(fmt.Sprintf("\n  - **Location**: `%s:%d`", f.File, f.Line))
			}
			if f.ErrorMessage != "" {
				sb.WriteString(fmt.Sprintf("\n  - **Error**: `%s`", f.ErrorMessage))
			}
			if f.Output != "" {
				sb.WriteString(fmt.Sprintf("\n```\n%s\n```", strings.TrimSpace(f.Output)))
			}
		}
	} else if report.Success {
		sb.WriteString("All tests completed successfully without failures.")
	}

	return sb.String()
}
