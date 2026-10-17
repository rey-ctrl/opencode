package tools

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateOutput_UTF8Safety(t *testing.T) {
	// Build a string that exceeds MaxOutputLength with multi-byte unicode characters (emojis, CJK, etc.)
	var sb strings.Builder
	for i := 0; i < 2000; i++ {
		sb.WriteString("Line 🚀 测试 日本語 ")
		sb.WriteString("error details and stacktrace info\n")
	}
	content := sb.String()

	if len(content) <= MaxOutputLength {
		t.Fatalf("expected test content to exceed MaxOutputLength")
	}

	truncated := truncateOutput(content)

	// Must be valid UTF-8 throughout
	if !utf8.ValidString(truncated) {
		t.Errorf("truncated output contains invalid UTF-8 sequences")
	}

	// Must contain the omission message
	if !strings.Contains(truncated, "lines from middle omitted") {
		t.Errorf("expected omission message in output")
	}

	// Output must be shorter than original
	if len(truncated) >= len(content) {
		t.Errorf("truncated output should be shorter than original")
	}
}
