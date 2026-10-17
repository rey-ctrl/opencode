package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTestRunnerTool_InfoAndCapability(t *testing.T) {
	tool := NewTestRunnerTool(&mockPermissionService{allow: true})
	info := tool.Info()

	assert.Equal(t, TestRunnerToolName, info.Name)
	assert.NotEmpty(t, info.Description)
	assert.Contains(t, info.Parameters, "framework")
	assert.Contains(t, info.Parameters, "test_filter")

	tc, ok := tool.(ToolWithCapability)
	require.True(t, ok)
	assert.False(t, tc.Capability().IsReadOnly)
	assert.False(t, tc.Capability().MutatesFiles)
	assert.True(t, tc.Capability().IsAtomic)
}

func TestTestRunnerTool_ParseGoTestJson(t *testing.T) {
	sampleJsonOutput := `
{"Time":"2026-10-18T00:00:00Z","Action":"run","Package":"github.com/foo/bar","Test":"TestSuccess"}
{"Time":"2026-10-18T00:00:00Z","Action":"pass","Package":"github.com/foo/bar","Test":"TestSuccess","Elapsed":0.01}
{"Time":"2026-10-18T00:00:00Z","Action":"run","Package":"github.com/foo/bar","Test":"TestFailure"}
{"Time":"2026-10-18T00:00:00Z","Action":"output","Package":"github.com/foo/bar","Test":"TestFailure","Output":"=== RUN   TestFailure\n"}
{"Time":"2026-10-18T00:00:00Z","Action":"output","Package":"github.com/foo/bar","Test":"TestFailure","Output":"    bar_test.go:42: expected 10, got 20\n"}
{"Time":"2026-10-18T00:00:00Z","Action":"output","Package":"github.com/foo/bar","Test":"TestFailure","Output":"--- FAIL: TestFailure (0.02s)\n"}
{"Time":"2026-10-18T00:00:00Z","Action":"fail","Package":"github.com/foo/bar","Test":"TestFailure","Elapsed":0.02}
{"Time":"2026-10-18T00:00:00Z","Action":"run","Package":"github.com/foo/bar","Test":"TestSkipped"}
{"Time":"2026-10-18T00:00:00Z","Action":"skip","Package":"github.com/foo/bar","Test":"TestSkipped","Elapsed":0.0}
`

	report := parseGoTestJson(sampleJsonOutput, "", 1, 1.25)

	assert.False(t, report.Success)
	assert.Equal(t, 1, report.Passed)
	assert.Equal(t, 1, report.Failed)
	assert.Equal(t, 1, report.Skipped)
	assert.Equal(t, 1.25, report.Duration)

	require.Len(t, report.Failures, 1)
	failure := report.Failures[0]
	assert.Equal(t, "TestFailure", failure.TestName)
	assert.Equal(t, "github.com/foo/bar", failure.Package)
	assert.Equal(t, "bar_test.go", failure.File)
	assert.Equal(t, 42, failure.Line)
	assert.Contains(t, failure.ErrorMessage, "expected 10, got 20")
}

func TestTestRunnerTool_ParsePythonGeneric(t *testing.T) {
	pytestOutput := `
============================= test session starts =============================
collected 10 items

tests/test_api.py ....F....                                             [100%]

================================== FAILURES ===================================
_________________________________ test_login __________________________________
    def test_login():
>       assert response.status_code == 200
E       assert 401 == 200
tests/test_api.py:35: AssertionError
=========================== 1 failed, 9 passed in 0.45s ============================
`
	report := parseGenericTestOutput("python", pytestOutput, "", 1, 0.45)
	assert.False(t, report.Success)
	assert.Equal(t, 9, report.Passed)
	assert.Equal(t, 1, report.Failed)
}

func TestTestRunnerTool_BuildTestCommand(t *testing.T) {
	goCmd := buildTestCommand("go", TestRunnerParams{
		Package:    "./internal/llm/tools",
		TestFilter: "TestFindSymbol",
	})
	assert.Contains(t, goCmd, "go test -json")
	assert.Contains(t, goCmd, "-run TestFindSymbol")
	assert.Contains(t, goCmd, "./internal/llm/tools")

	pyCmd := buildTestCommand("python", TestRunnerParams{
		TestFilter: "auth",
	})
	assert.Contains(t, pyCmd, "pytest -v --tb=short")
	assert.Contains(t, pyCmd, "-k auth")
}
