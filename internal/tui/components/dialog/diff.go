package dialog

import (
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	diffComponent "github.com/opencode-ai/opencode/internal/tui/components/diff"
	"github.com/opencode-ai/opencode/internal/tui/layout"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
	"github.com/opencode-ai/opencode/internal/tui/util"
)

// CloseDiffDialogMsg is sent when the diff viewer dialog is closed
type CloseDiffDialogMsg struct{}

// OpenDiffDialogMsg is sent to request opening the diff dialog with given diff text
type OpenDiffDialogMsg struct {
	DiffText string
	FileName string
}

// DiffDialog interface for the interactive diff viewer dialog
type DiffDialog interface {
	tea.Model
	layout.Bindings
	SetDiff(diffText, fileName string)
	SetSize(width, height int)
	IsOpen() bool
	SetOpen(open bool)
}

type diffDialogCmp struct {
	viewer diffComponent.DiffViewer
	width  int
	height int
	isOpen bool
}

type diffDialogKeyMap struct {
	Close key.Binding
}

var diffDialogKeys = diffDialogKeyMap{
	Close: key.NewBinding(
		key.WithKeys("esc", "q"),
		key.WithHelp("esc/q", "close diff viewer"),
	),
}

// NewDiffDialogCmp creates a new interactive diff viewer dialog
func NewDiffDialogCmp() DiffDialog {
	return &diffDialogCmp{
		viewer: diffComponent.NewDiffViewer(),
	}
}

func (d *diffDialogCmp) Init() tea.Cmd {
	return d.viewer.Init()
}

func (d *diffDialogCmp) SetDiff(diffText, fileName string) {
	d.viewer.SetDiff(diffText, fileName)
}

func (d *diffDialogCmp) SetSize(width, height int) {
	d.width = width
	d.height = height

	// Inner viewer gets dialog dimensions minus borders and padding
	dialogWidth := max(30, width-8)
	dialogHeight := max(10, height-6)

	// Subtract 2 for border on width, 4 for borders and title
	d.viewer.SetSize(dialogWidth-4, dialogHeight-3)
}

func (d *diffDialogCmp) IsOpen() bool {
	return d.isOpen
}

func (d *diffDialogCmp) SetOpen(open bool) {
	d.isOpen = open
}

func (d *diffDialogCmp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		d.SetSize(msg.Width, msg.Height)
	case tea.KeyMsg:
		if key.Matches(msg, diffDialogKeys.Close) {
			d.isOpen = false
			return d, util.CmdHandler(CloseDiffDialogMsg{})
		}
	}

	updatedViewer, cmd := d.viewer.Update(msg)
	d.viewer = updatedViewer.(diffComponent.DiffViewer)
	return d, cmd
}

func (d *diffDialogCmp) View() string {
	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()

	dialogWidth := max(30, d.width-8)
	dialogHeight := max(10, d.height-6)

	titleStyle := baseStyle.
		Bold(true).
		Foreground(t.Primary()).
		Padding(0, 1)

	closeHint := baseStyle.
		Foreground(t.TextMuted()).
		Render("[esc/q] close")

	titleBar := lipgloss.JoinHorizontal(
		lipgloss.Center,
		titleStyle.Render("🔍 Diff Viewer"),
		baseStyle.Render(stringsRepeat(" ", max(0, dialogWidth-lipgloss.Width("🔍 Diff Viewer")-lipgloss.Width(closeHint)-6))),
		closeHint,
	)

	divider := baseStyle.Foreground(t.BorderDim()).Render(stringsRepeat("─", max(0, dialogWidth-4)))

	viewerView := d.viewer.View()
	if viewerView == "" {
		emptyStyle := baseStyle.
			Foreground(t.TextMuted()).
			Italic(true).
			Padding(2, 4)
		viewerView = emptyStyle.Render("No changes detected / empty diff")
	}

	content := lipgloss.JoinVertical(
		lipgloss.Left,
		titleBar,
		divider,
		viewerView,
	)

	return baseStyle.
		Padding(0, 1).
		Border(lipgloss.RoundedBorder()).
		BorderBackground(t.Background()).
		BorderForeground(t.Primary()).
		Width(dialogWidth).
		Height(dialogHeight).
		Render(content)
}

func (d *diffDialogCmp) BindingKeys() []key.Binding {
	bindings := layout.KeyMapToSlice(diffDialogKeys)
	bindings = append(bindings, d.viewer.BindingKeys()...)
	return bindings
}

func stringsRepeat(s string, count int) string {
	if count <= 0 {
		return ""
	}
	res := make([]byte, 0, len(s)*count)
	for i := 0; i < count; i++ {
		res = append(res, s...)
	}
	return string(res)
}
