package diff

import (
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testDiff = `--- a/file.go
+++ b/file.go
@@ -1,5 +1,5 @@
 package main
 
-func old() {}
+func new() {}
 
 func main() {}
`

func TestDiffViewer_NewAndSetDiff(t *testing.T) {
	dv := NewDiffViewer()
	require.NotNil(t, dv)
	assert.Equal(t, ViewModeInline, dv.GetMode())

	dv.SetSize(100, 30)
	dv.SetDiff(testDiff, "file.go")

	impl, ok := dv.(*diffViewer)
	require.True(t, ok)
	assert.Equal(t, 1, impl.additions)
	assert.Equal(t, 1, impl.deletions)
	assert.NotEmpty(t, impl.viewport.View())

	// View rendering
	rendered := dv.View()
	assert.Contains(t, rendered, "file.go")
	assert.Contains(t, rendered, "+1")
	assert.Contains(t, rendered, "-1")
	assert.Contains(t, rendered, "Inline")
}

func TestDiffViewer_ToggleMode(t *testing.T) {
	dv := NewDiffViewer()
	dv.SetSize(100, 30)
	dv.SetDiff(testDiff, "file.go")

	assert.Equal(t, ViewModeInline, dv.GetMode())

	// Toggle to Side-by-Side
	dv.ToggleMode()
	assert.Equal(t, ViewModeSideBySide, dv.GetMode())
	assert.Contains(t, dv.View(), "Side-by-Side")

	// Toggle back to Inline
	dv.ToggleMode()
	assert.Equal(t, ViewModeInline, dv.GetMode())
	assert.Contains(t, dv.View(), "Inline")
}

func TestDiffViewer_KeyHandling(t *testing.T) {
	dv := NewDiffViewer()
	dv.SetSize(100, 30)
	dv.SetDiff(testDiff, "file.go")

	// Press 't' to toggle
	tKey := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}}
	_, _ = dv.Update(tKey)
	assert.Equal(t, ViewModeSideBySide, dv.GetMode())

	// Press 't' again
	_, _ = dv.Update(tKey)
	assert.Equal(t, ViewModeInline, dv.GetMode())
}

func TestDiffViewer_HunkNavigation(t *testing.T) {
	multiHunkDiff := `--- a/multi.go
+++ b/multi.go
@@ -1,3 +1,3 @@
-line1
+line1_new
@@ -10,3 +10,3 @@
-line10
+line10_new
`
	dv := NewDiffViewer()
	dv.SetSize(100, 30)
	dv.SetDiff(multiHunkDiff, "multi.go")

	impl := dv.(*diffViewer)
	assert.GreaterOrEqual(t, len(impl.hunkOffsets), 2)

	// Navigate hunks with 'n'
	nKey := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}}
	_, _ = dv.Update(nKey)
	assert.Equal(t, 1, impl.currentHunkIdx)

	// Navigate prev hunk with 'p'
	pKey := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}}
	_, _ = dv.Update(pKey)
	assert.Equal(t, 0, impl.currentHunkIdx)
}

func TestDiffViewer_BindingKeys(t *testing.T) {
	dv := NewDiffViewer()
	bindings := dv.BindingKeys()
	assert.NotEmpty(t, bindings)

	hasToggle := false
	for _, b := range bindings {
		if key.Matches(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}}, b) {
			hasToggle = true
			break
		}
	}
	assert.True(t, hasToggle)
}
