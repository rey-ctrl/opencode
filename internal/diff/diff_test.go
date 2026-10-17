package diff

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleDiff = `--- a/example.go
+++ b/example.go
@@ -1,5 +1,5 @@
 package main
 
-func oldName() string {
+func newName() string {
 	return "hello"
 }
`

func TestParseUnifiedDiff(t *testing.T) {
	result, err := ParseUnifiedDiff(sampleDiff)
	require.NoError(t, err)
	assert.Equal(t, "example.go", result.OldFile)
	assert.Equal(t, "example.go", result.NewFile)
	require.Len(t, result.Hunks, 1)

	hunk := result.Hunks[0]
	assert.True(t, strings.HasPrefix(hunk.Header, "@@"))
	assert.NotEmpty(t, hunk.Lines)
}

func TestFormatInlineDiff(t *testing.T) {
	output, err := FormatInlineDiff(sampleDiff, WithInlineTotalWidth(100), WithShowHunkHeader(true))
	require.NoError(t, err)
	assert.NotEmpty(t, output)

	// Verify both added and removed markers are present in the output
	assert.Contains(t, output, "-")
	assert.Contains(t, output, "+")
	assert.Contains(t, output, "@@")
}

func TestFormatDiff_SideBySide(t *testing.T) {
	output, err := FormatDiff(sampleDiff, WithTotalWidth(120))
	require.NoError(t, err)
	assert.NotEmpty(t, output)
}
