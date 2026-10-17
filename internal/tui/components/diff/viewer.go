package diff

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/opencode-ai/opencode/internal/diff"
	"github.com/opencode-ai/opencode/internal/tui/layout"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
)

// ViewMode represents the rendering mode of the diff viewer.
type ViewMode int

const (
	// ViewModeInline renders unified inline diff with red/green lines
	ViewModeInline ViewMode = iota
	// ViewModeSideBySide renders side-by-side diff with two columns
	ViewModeSideBySide
)

func (m ViewMode) String() string {
	switch m {
	case ViewModeInline:
		return "Inline"
	case ViewModeSideBySide:
		return "Side-by-Side"
	default:
		return "Inline"
	}
}

// DiffKeyMap defines keybindings for navigating the diff viewer.
type DiffKeyMap struct {
	ToggleMode key.Binding
	Down       key.Binding
	Up         key.Binding
	HalfDown   key.Binding
	HalfUp     key.Binding
	NextHunk   key.Binding
	PrevHunk   key.Binding
	Top        key.Binding
	Bottom     key.Binding
}

// DefaultDiffKeyMap returns standard keybindings for diff viewer.
func DefaultDiffKeyMap() DiffKeyMap {
	return DiffKeyMap{
		ToggleMode: key.NewBinding(
			key.WithKeys("t"),
			key.WithHelp("t", "toggle inline/side-by-side"),
		),
		Down: key.NewBinding(
			key.WithKeys("down", "j"),
			key.WithHelp("↓/j", "scroll down"),
		),
		Up: key.NewBinding(
			key.WithKeys("up", "k"),
			key.WithHelp("↑/k", "scroll up"),
		),
		HalfDown: key.NewBinding(
			key.WithKeys("d", "ctrl+d"),
			key.WithHelp("d", "half page down"),
		),
		HalfUp: key.NewBinding(
			key.WithKeys("u", "ctrl+u"),
			key.WithHelp("u", "half page up"),
		),
		NextHunk: key.NewBinding(
			key.WithKeys("n"),
			key.WithHelp("n", "next hunk"),
		),
		PrevHunk: key.NewBinding(
			key.WithKeys("p"),
			key.WithHelp("p", "prev hunk"),
		),
		Top: key.NewBinding(
			key.WithKeys("g", "home"),
			key.WithHelp("g", "top"),
		),
		Bottom: key.NewBinding(
			key.WithKeys("G", "end"),
			key.WithHelp("G", "bottom"),
		),
	}
}

// DiffViewer defines the interface for the diff viewer component.
type DiffViewer interface {
	tea.Model
	layout.Bindings
	SetDiff(diffText, fileName string)
	SetSize(width, height int)
	ToggleMode()
	GetMode() ViewMode
	SetMode(mode ViewMode)
	GetViewport() *viewport.Model
}

type diffViewer struct {
	diffText       string
	fileName       string
	mode           ViewMode
	viewport       viewport.Model
	keyMap         DiffKeyMap
	width          int
	height         int
	additions      int
	deletions      int
	hunkOffsets    []int // Line offset for each hunk
	currentHunkIdx int
	cache          map[ViewMode]string
}

// NewDiffViewer creates a new diff viewer component.
func NewDiffViewer() DiffViewer {
	vp := viewport.New(80, 20)
	return &diffViewer{
		mode:     ViewModeInline,
		viewport: vp,
		keyMap:   DefaultDiffKeyMap(),
		cache:    make(map[ViewMode]string),
	}
}

func (d *diffViewer) Init() tea.Cmd {
	return d.viewport.Init()
}

func (d *diffViewer) SetSize(width, height int) {
	d.width = width
	d.height = height

	// Reserve space for header (2 lines) and footer (1 line)
	headerHeight := 2
	footerHeight := 1
	vpHeight := max(3, height-headerHeight-footerHeight)
	vpWidth := max(10, width)

	d.viewport.Width = vpWidth
	d.viewport.Height = vpHeight

	// Invalidate cache on resize because diff formatting is width-dependent
	d.cache = make(map[ViewMode]string)
	d.updateContent()
}

func (d *diffViewer) SetDiff(diffText, fileName string) {
	d.diffText = diffText
	d.fileName = fileName
	d.cache = make(map[ViewMode]string)
	d.currentHunkIdx = 0

	// Count additions and deletions
	d.additions = 0
	d.deletions = 0
	lines := strings.Split(diffText, "\n")
	for _, l := range lines {
		if strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++") {
			d.additions++
		} else if strings.HasPrefix(l, "-") && !strings.HasPrefix(l, "---") {
			d.deletions++
		}
	}

	d.updateContent()
	d.viewport.GotoTop()
}

func (d *diffViewer) ToggleMode() {
	if d.mode == ViewModeInline {
		d.mode = ViewModeSideBySide
	} else {
		d.mode = ViewModeInline
	}
	d.updateContent()
}

func (d *diffViewer) GetMode() ViewMode {
	return d.mode
}

func (d *diffViewer) SetMode(mode ViewMode) {
	if d.mode != mode {
		d.mode = mode
		d.updateContent()
	}
}

func (d *diffViewer) GetViewport() *viewport.Model {
	return &d.viewport
}

func (d *diffViewer) updateContent() {
	if d.diffText == "" {
		d.viewport.SetContent("")
		d.hunkOffsets = nil
		return
	}

	// Check cache
	if cached, ok := d.cache[d.mode]; ok && cached != "" {
		d.viewport.SetContent(cached)
		return
	}

	vpWidth := max(10, d.viewport.Width)
	var formatted string
	var err error

	if d.mode == ViewModeSideBySide {
		formatted, err = diff.FormatDiff(d.diffText, diff.WithTotalWidth(vpWidth))
	} else {
		formatted, err = diff.FormatInlineDiff(d.diffText, diff.WithInlineTotalWidth(vpWidth), diff.WithShowHunkHeader(true))
	}

	if err != nil || formatted == "" {
		// Fallback to raw diff text if formatting fails
		formatted = d.diffText
	}

	// Calculate hunk line offsets in rendered output
	d.hunkOffsets = nil
	renderedLines := strings.Split(formatted, "\n")
	for idx, line := range renderedLines {
		if strings.Contains(line, "@@") {
			d.hunkOffsets = append(d.hunkOffsets, idx)
		}
	}

	d.cache[d.mode] = formatted
	d.viewport.SetContent(formatted)
}

func (d *diffViewer) nextHunk() {
	if len(d.hunkOffsets) == 0 {
		return
	}
	d.currentHunkIdx++
	if d.currentHunkIdx >= len(d.hunkOffsets) {
		d.currentHunkIdx = 0
	}
	d.viewport.SetYOffset(d.hunkOffsets[d.currentHunkIdx])
}

func (d *diffViewer) prevHunk() {
	if len(d.hunkOffsets) == 0 {
		return
	}
	d.currentHunkIdx--
	if d.currentHunkIdx < 0 {
		d.currentHunkIdx = len(d.hunkOffsets) - 1
	}
	d.viewport.SetYOffset(d.hunkOffsets[d.currentHunkIdx])
}

func (d *diffViewer) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, d.keyMap.ToggleMode):
			d.ToggleMode()
			return d, nil
		case key.Matches(msg, d.keyMap.NextHunk):
			d.nextHunk()
			return d, nil
		case key.Matches(msg, d.keyMap.PrevHunk):
			d.prevHunk()
			return d, nil
		case key.Matches(msg, d.keyMap.Top):
			d.viewport.GotoTop()
			return d, nil
		case key.Matches(msg, d.keyMap.Bottom):
			d.viewport.GotoBottom()
			return d, nil
		case key.Matches(msg, d.keyMap.HalfDown):
			d.viewport.HalfViewDown()
			return d, nil
		case key.Matches(msg, d.keyMap.HalfUp):
			d.viewport.HalfViewUp()
			return d, nil
		}
	}

	d.viewport, cmd = d.viewport.Update(msg)
	return d, cmd
}

func (d *diffViewer) renderHeader() string {
	t := theme.CurrentTheme()
	base := styles.BaseStyle()

	// File title
	fileStyle := base.Bold(true).Foreground(t.Primary())
	fileName := d.fileName
	if fileName == "" {
		fileName = "Diff"
	}
	fileBadge := fileStyle.Render(fmt.Sprintf("📄 %s", fileName))

	// Stats: +additions -deletions
	addStyle := base.Bold(true).Foreground(t.Success())
	delStyle := base.Bold(true).Foreground(t.Error())
	stats := fmt.Sprintf("%s %s", addStyle.Render(fmt.Sprintf("+%d", d.additions)), delStyle.Render(fmt.Sprintf("-%d", d.deletions)))

	// Mode badge
	modeStyle := base.Background(t.BackgroundDarker()).Foreground(t.Accent()).Padding(0, 1)
	modeBadge := modeStyle.Render(fmt.Sprintf("[t] %s", d.mode.String()))

	// Scroll position
	var scrollText string
	if d.viewport.AtTop() {
		scrollText = "TOP"
	} else if d.viewport.AtBottom() {
		scrollText = "BOT"
	} else {
		scrollText = fmt.Sprintf("%2.0f%%", d.viewport.ScrollPercent()*100)
	}
	scrollBadge := base.Foreground(t.TextMuted()).Render(fmt.Sprintf("[%s]", scrollText))

	leftPart := fmt.Sprintf("%s  %s", fileBadge, stats)
	rightPart := fmt.Sprintf("%s %s", modeBadge, scrollBadge)

	availWidth := max(0, d.width-lipgloss.Width(leftPart)-lipgloss.Width(rightPart)-2)
	spacer := base.Render(strings.Repeat(" ", availWidth))

	headerLine := lipgloss.JoinHorizontal(lipgloss.Center, leftPart, spacer, rightPart)
	divider := base.Foreground(t.BorderDim()).Render(strings.Repeat("─", max(0, d.width)))

	return headerLine + "\n" + divider
}

func (d *diffViewer) renderFooter() string {
	t := theme.CurrentTheme()
	base := styles.BaseStyle()

	hintStyle := base.Foreground(t.TextMuted())
	keyStyle := base.Bold(true).Foreground(t.Accent())

	hints := []string{
		keyStyle.Render("[t]") + hintStyle.Render(" toggle view"),
		keyStyle.Render("[n/p]") + hintStyle.Render(" hunk"),
		keyStyle.Render("[↑/↓/d/u]") + hintStyle.Render(" scroll"),
		keyStyle.Render("[g/G]") + hintStyle.Render(" top/bot"),
	}

	return hintStyle.Render(strings.Join(hints, "  "))
}

func (d *diffViewer) View() string {
	if d.diffText == "" {
		return ""
	}

	header := d.renderHeader()
	body := d.viewport.View()
	footer := d.renderFooter()

	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (d *diffViewer) BindingKeys() []key.Binding {
	return layout.KeyMapToSlice(d.keyMap)
}
