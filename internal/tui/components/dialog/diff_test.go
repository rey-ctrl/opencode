package dialog

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
)

func TestDiffDialog_Basic(t *testing.T) {
	dlg := NewDiffDialogCmp()
	assert.NotNil(t, dlg)
	assert.False(t, dlg.IsOpen())

	dlg.SetOpen(true)
	assert.True(t, dlg.IsOpen())

	sampleDiff := `--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
+import "fmt"
 func main() {}
`
	dlg.SetDiff(sampleDiff, "main.go")
	dlg.SetSize(100, 30)

	view := dlg.View()
	assert.Contains(t, view, "Diff Viewer")
	assert.Contains(t, view, "main.go")

	// Test Esc key closes
	_, cmd := dlg.Update(tea.KeyMsg{Type: tea.KeyEsc})
	assert.False(t, dlg.IsOpen())
	assert.NotNil(t, cmd)

	msg := cmd()
	_, isClose := msg.(CloseDiffDialogMsg)
	assert.True(t, isClose)
}

func TestDiffDialog_EmptyDiff(t *testing.T) {
	dlg := NewDiffDialogCmp()
	dlg.SetSize(80, 24)
	dlg.SetDiff("", "empty.txt")

	view := dlg.View()
	assert.Contains(t, view, "No changes detected")
}
