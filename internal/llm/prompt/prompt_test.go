package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetContextFromPaths(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	_, err := config.Load(tmpDir, false)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	cfg := config.Get()
	cfg.WorkingDir = tmpDir
	cfg.ContextPaths = []string{
		"file.txt",
		"directory/",
	}
	testFiles := []string{
		"file.txt",
		"directory/file_a.txt",
		"directory/file_b.txt",
		"directory/file_c.txt",
	}

	createTestFiles(t, tmpDir, testFiles)

	context := getContextFromPaths()
	assert.Contains(t, context, fmt.Sprintf("# From:%s\nfile.txt: test content", filepath.Join(tmpDir, "file.txt")))
	assert.Contains(t, context, fmt.Sprintf("# From:%s\ndirectory/file_a.txt: test content", filepath.Join(tmpDir, "directory", "file_a.txt")))
	assert.Contains(t, context, fmt.Sprintf("# From:%s\ndirectory/file_b.txt: test content", filepath.Join(tmpDir, "directory", "file_b.txt")))
	assert.Contains(t, context, fmt.Sprintf("# From:%s\ndirectory/file_c.txt: test content", filepath.Join(tmpDir, "directory", "file_c.txt")))
}

func createTestFiles(t *testing.T, tmpDir string, testFiles []string) {
	t.Helper()
	for _, path := range testFiles {
		fullPath := filepath.Join(tmpDir, path)
		if path[len(path)-1] == '/' {
			err := os.MkdirAll(fullPath, 0755)
			require.NoError(t, err)
		} else {
			dir := filepath.Dir(fullPath)
			err := os.MkdirAll(dir, 0755)
			require.NoError(t, err)
			err = os.WriteFile(fullPath, []byte(path+": test content"), 0644)
			require.NoError(t, err)
		}
	}
}

func TestPrompt_PersistentProjectMemories(t *testing.T) {
	SetMemoryProvider(func(projectPath string) string {
		return "<project_memories>\n- [convention] test_style: Use table tests\n</project_memories>"
	})
	defer SetMemoryProvider(nil)

	p := GetAgentPrompt(config.AgentCoder, "")
	assert.Contains(t, p, "# Persistent Project Memories")
	assert.Contains(t, p, "test_style: Use table tests")
}

