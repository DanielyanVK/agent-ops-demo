// Package tui is a read-only terminal viewer of a runs directory.
package tui

import (
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"agentops/internal/trace"
)

const (
	minW, minH = 80, 16
	// Screen rows around the panes: top border, header, separator, separator, footer, bottom border.
	chromeRows = 6
	// Rows inside a pane above its scrollable content: title and a blank line.
	paneHead = 2
	listW    = 32
	// Body row of the first screen line inside the panes (after top border, header, separator).
	bodyTop = 3
	// Rows kept for steps when the pinned diagnosis is long.
	minStepRows = 3
)

type focus int

const (
	focusList focus = iota
	focusSteps
)

type screen int

const (
	screenMain screen = iota
	screenStep
	screenHelp
)

type Model struct {
	dir  string
	runs []trace.Run

	visible []int // indexes into runs that pass the filter
	sel     int   // index into visible
	selID   string
	step    int

	focus      focus
	screen     screen
	failedOnly bool
	loadErr    error
	spinning   bool // a spinner tick is in flight; it is armed only while a run is running

	w, h                         int
	listScroll, bodyScroll, vert int // vert: scroll of the full-screen step view
	frame                        int
}

type reloadMsg struct {
	runs []trace.Run
	err  error
}
type spinMsg struct{}

func New(dir string) Model {
	runs, err := load(dir)
	m := Model{dir: dir, loadErr: err}
	m.applyRuns(runs)
	m.spinning = m.hasRunning()
	return m
}

// load reports a missing directory as an error (trace.LoadDir treats it as empty),
// so a mistyped --runs is visible instead of looking like "no runs yet".
func load(dir string) ([]trace.Run, error) {
	if _, err := os.Stat(dir); err != nil {
		return nil, err
	}
	runs, _, err := trace.LoadDir(dir)
	return runs, err
}

func (m Model) hasRunning() bool {
	for _, r := range m.runs {
		if r.Status() == trace.Running {
			return true
		}
	}
	return false
}

func (m Model) Init() tea.Cmd {
	if m.spinning {
		return tea.Batch(m.reloadCmd(), spinCmd())
	}
	return m.reloadCmd()
}

func (m Model) reloadCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg {
		runs, err := load(m.dir)
		return reloadMsg{runs, err}
	})
}

func spinCmd() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return spinMsg{} })
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.clamp()
	case reloadMsg:
		m.loadErr = msg.err
		if msg.err == nil {
			m.applyRuns(msg.runs)
		}
		if m.hasRunning() && !m.spinning {
			m.spinning = true
			return m, tea.Batch(m.reloadCmd(), spinCmd())
		}
		return m, m.reloadCmd()
	case spinMsg:
		if !m.hasRunning() {
			m.spinning = false
			return m, nil
		}
		m.frame++
		return m, spinCmd()
	case tea.KeyPressMsg:
		return m.key(msg.String())
	case tea.MouseClickMsg:
		if msg.Button == tea.MouseLeft {
			m.click(msg.X, msg.Y)
		}
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			m.wheel(msg.X, -3)
		case tea.MouseWheelDown:
			m.wheel(msg.X, 3)
		}
	}
	return m, nil
}

func (m Model) tooSmall() bool { return m.w < minW || m.h < minH }

func (m Model) bodyH() int { return m.h - chromeRows }

func (m Model) key(k string) (tea.Model, tea.Cmd) {
	if k == "ctrl+c" {
		return m, tea.Quit
	}
	if m.tooSmall() {
		if k == "q" {
			return m, tea.Quit
		}
		return m, nil
	}
	switch m.screen {
	case screenHelp:
		m.screen = screenMain
		return m, nil
	case screenStep:
		switch k {
		case "esc", "q":
			m.screen = screenMain
		case "up", "k":
			m.vert--
		case "down", "j":
			m.vert++
		case "pgup":
			m.vert -= m.bodyH()
		case "pgdown":
			m.vert += m.bodyH()
		}
		m.clamp()
		return m, nil
	}

	if k == "q" {
		return m, tea.Quit
	}
	if k == "?" {
		m.screen = screenHelp
		return m, nil
	}
	if m.focus == focusList {
		switch k {
		case "up", "k":
			m.moveRun(-1)
		case "down", "j":
			m.moveRun(1)
		case "tab", "right", "l", "enter":
			if m.selected() != nil {
				m.focus = focusSteps
			}
		case "f":
			m.failedOnly = !m.failedOnly
			m.rebuild()
		}
		return m, nil
	}
	switch k {
	case "up", "k":
		m.moveStep(-1)
	case "down", "j":
		m.moveStep(1)
	case "enter":
		if r := m.selected(); r != nil && len(r.Steps) > 0 {
			m.screen, m.vert = screenStep, 0
		}
	case "tab", "left", "h", "esc":
		m.focus = focusList
	}
	return m, nil
}

func (m *Model) selected() *trace.Run {
	if m.sel < 0 || m.sel >= len(m.visible) {
		return nil
	}
	return &m.runs[m.visible[m.sel]]
}

func (m *Model) moveRun(d int) { m.selectRun(m.sel + d) }

func (m *Model) selectRun(i int) {
	if i < 0 || i >= len(m.visible) || i == m.sel {
		return
	}
	m.sel, m.step, m.bodyScroll = i, 0, 0
	m.selID = m.runs[m.visible[i]].ID
	m.clamp()
}

func (m *Model) moveStep(d int) {
	r := m.selected()
	if r == nil {
		return
	}
	m.step = max(0, min(len(r.Steps)-1, m.step+d))
	m.clamp()
}

// rebuild recomputes the filtered list and keeps the selection by run ID; when the
// selected run is gone it stays at the same position, so the cursor does not jump.
func (m *Model) rebuild() {
	m.visible = m.visible[:0:0]
	for i, r := range m.runs {
		if !m.failedOnly || r.Status() == trace.Failed {
			m.visible = append(m.visible, i)
		}
	}
	idx := -1
	for i, ri := range m.visible {
		if m.runs[ri].ID == m.selID {
			idx = i
		}
	}
	if idx < 0 {
		idx = max(0, min(m.sel, len(m.visible)-1))
		m.step, m.bodyScroll = 0, 0
		m.screen = screenMain
	}
	m.sel = idx
	m.selID = ""
	if r := m.selected(); r != nil {
		m.selID = r.ID
	} else {
		m.focus = focusList
		m.screen = screenMain
	}
	m.clamp()
}

func (m *Model) applyRuns(runs []trace.Run) {
	m.runs = runs
	m.rebuild()
}

// clamp keeps every index and scroll offset inside its range and the selection on screen.
func (m *Model) clamp() {
	if r := m.selected(); r != nil {
		m.step = max(0, min(m.step, len(r.Steps)-1))
	} else {
		m.step = 0
	}
	m.listScroll = scrollTo(m.listScroll, m.sel, max(1, m.bodyH()-paneHead), len(m.visible))
	n, rows := m.stepCount(), m.stepRows()
	m.bodyScroll = max(0, min(m.bodyScroll, n-rows))
	if m.focus == focusSteps {
		m.bodyScroll = scrollTo(m.bodyScroll, m.step, rows, n)
	}
	m.vert = max(0, min(m.vert, len(m.stepLines(max(1, m.w-2)))-m.bodyH()))
}

// scrollTo moves off the least needed to make row i visible in a window of rows lines.
func scrollTo(off, i, rows, total int) int {
	if i < off {
		off = i
	}
	if i >= off+rows {
		off = i - rows + 1
	}
	return max(0, min(off, total-rows))
}

func (m *Model) click(x, y int) {
	if m.tooSmall() || m.screen != screenMain || y < bodyTop+paneHead || y >= bodyTop+m.bodyH() {
		return
	}
	row := y - bodyTop - paneHead
	switch {
	case x >= 1 && x <= listW:
		if i := m.listScroll + row; i < len(m.visible) {
			m.selectRun(i)
		}
	case x > listW+1:
		r := m.selected()
		if i := m.bodyScroll + row - m.pmHeight(); r != nil && row >= m.pmHeight() && i < len(r.Steps) {
			m.step, m.focus = i, focusSteps
			m.clamp()
		}
	}
}

func (m *Model) wheel(x, d int) {
	if m.tooSmall() || m.screen == screenHelp {
		return
	}
	switch {
	case m.screen == screenStep:
		m.vert += d
	case x <= listW:
		m.listScroll += d
	default:
		m.bodyScroll += d
	}
	m.clampScrollOnly()
}

// clampScrollOnly is clamp without pulling the selection back into view: the wheel
// scrolls the view, not the selection.
func (m *Model) clampScrollOnly() {
	m.listScroll = max(0, min(m.listScroll, len(m.visible)-max(1, m.bodyH()-paneHead)))
	m.bodyScroll = max(0, min(m.bodyScroll, m.stepCount()-m.stepRows()))
	m.vert = max(0, min(m.vert, len(m.stepLines(max(1, m.w-2)))-m.bodyH()))
}

// oneLine is trace.Clean for text drawn in a single cell row.
func oneLine(s string) string { return strings.ReplaceAll(trace.Clean(s), "\n", " ") }
