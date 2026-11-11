package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"agentops/internal/trace"
)

// ANSI palette indexes, not RGB: the terminal theme maps them, so they stay readable
// on both dark and light backgrounds. Meaning is also carried by glyphs.
var (
	green  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	red    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	dim    = lipgloss.NewStyle().Faint(true)
	bold   = lipgloss.NewStyle().Bold(true)
	hilite = lipgloss.NewStyle().Bold(true).Reverse(true)
	accent = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
)

var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// rightW is the text width of the right pane: the screen minus the three vertical
// borders, the left pane and a one-cell margin on each side.
func (m Model) rightW() int { return m.w - listW - 3 - 2 }

func (m Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m Model) render() string {
	switch {
	case m.w == 0:
		return ""
	case m.tooSmall():
		return lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center,
			fmt.Sprintf("терминал слишком мал (нужно %d×%d)", minW, minH))
	case m.screen == screenHelp:
		return m.compose("agentops · помощь", dim.Render("любая клавиша закрывает"), nil, helpLines())
	case m.screen == screenStep:
		r := m.selected()
		head := fmt.Sprintf("%s · шаг %d из %d", oneLine(r.ID), m.step+1, len(r.Steps))
		return m.compose(head, dim.Render("↑↓ pgup/pgdn прокрутка  esc назад  q назад"), nil,
			window(m.stepLines(m.w-2), m.vert, m.bodyH()))
	}
	return m.compose(m.summary(), dim.Render(m.footer()), m.leftLines(), m.rightLines())
}

func (m Model) summary() string {
	s := trace.Summarize(m.runs)
	if s.Total == 0 {
		return "agentops · прогонов нет"
	}
	parts := []string{"agentops", fmt.Sprintf("%d %s", s.Total, trace.Plural(s.Total, "прогон", "прогона", "прогонов")),
		fmt.Sprintf("✓ %d", s.Passed), fmt.Sprintf("✗ %d", s.Failed)}
	if s.Running > 0 {
		parts = append(parts, fmt.Sprintf("… %d", s.Running))
	}
	parts = append(parts, trace.FmtCost(s.CostUSD), "в среднем "+trace.FmtDuration(s.AvgDuration))
	return strings.Join(parts, " · ")
}

func (m Model) footer() string {
	if m.focus == focusSteps {
		return "↑↓ шаг  enter открыть  tab назад к списку  ? помощь  q выход"
	}
	return "↑↓ выбор  tab шаги  f только провалы  ? помощь  q выход"
}

// compose draws the frame. With a non-nil left it is two panes split at listW;
// otherwise right is a single full-width pane. Every output line is exactly m.w cells.
func (m Model) compose(header, footer string, left, right []string) string {
	inner := m.w - 2
	split := left != nil
	rule := func(l, mid, r string) string {
		if !split {
			return l + strings.Repeat("─", inner) + r
		}
		return l + strings.Repeat("─", listW) + mid + strings.Repeat("─", inner-listW-1) + r
	}
	flat := func(l, r string) string { return l + strings.Repeat("─", inner) + r }
	var b strings.Builder
	b.WriteString(flat("┌", "┐") + "\n")
	b.WriteString("│" + fit(" "+header, inner) + "│\n")
	b.WriteString(rule("├", "┬", "┤") + "\n")
	for i := 0; i < m.bodyH(); i++ {
		if split {
			b.WriteString("│" + fit(at(left, i), listW) + "│" + fit(at(right, i), inner-listW-1) + "│\n")
		} else {
			b.WriteString("│" + fit(at(right, i), inner) + "│\n")
		}
	}
	b.WriteString(rule("├", "┴", "┤") + "\n")
	b.WriteString("│" + fit(" "+footer, inner) + "│\n")
	b.WriteString(flat("└", "┘"))
	return b.String()
}

func at(lines []string, i int) string {
	if i < len(lines) {
		return lines[i]
	}
	return ""
}

// fit truncates to w cells with an ellipsis and pads with spaces; widths come from
// the terminal-aware measure so Cyrillic and glyphs keep the borders aligned.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) > w {
		s = ansi.Truncate(s, w, "…")
	}
	return s + strings.Repeat(" ", w-lipgloss.Width(s))
}

func padLeft(s string, w int) string {
	return strings.Repeat(" ", max(0, w-lipgloss.Width(s))) + s
}

func window(lines []string, off, rows int) []string {
	off = max(0, min(off, len(lines)))
	return lines[off:min(len(lines), off+rows)]
}

func midWidth(w int, lead, right string) int {
	return w - 2 - lipgloss.Width(lead) - 2 - lipgloss.Width(right) - 1
}

// row draws "<marker> <lead><glyph> <mid>  <right>" in w cells. A selected row is
// drawn plain and reversed: nested colours would break the highlight.
func row(w int, sel, reverse bool, marker, lead, glyph string, gst lipgloss.Style, mid, right string) string {
	midW := midWidth(w, lead, right)
	g := gst.Render(glyph)
	if sel {
		g = glyph
	}
	s := fit(marker+" "+lead+g+" "+fit(mid, midW)+" "+right, w)
	switch {
	case sel && reverse:
		return hilite.Render(s)
	case sel:
		return bold.Render(s)
	}
	return s
}

func (m Model) glyph(r trace.Run) (string, lipgloss.Style) {
	switch r.Status() {
	case trace.Passed:
		return "✓", green
	case trace.Failed:
		return "✗", red
	}
	return spinner[m.frame%len(spinner)], dim
}

func (m Model) leftLines() []string {
	cw := listW - 2
	title := "ПРОГОНЫ"
	if m.failedOnly {
		title += " · только провалы"
	}
	t := dim.Render(fit(" "+title, listW))
	if m.focus == focusList {
		t = accent.Render(fit(" "+title, listW))
	}
	lines := []string{t, ""}
	if len(m.visible) == 0 {
		msg := "прогонов нет — выполни команду run"
		if m.loadErr != nil {
			msg = "не удалось прочитать прогоны: " + oneLine(m.loadErr.Error())
		} else if m.failedOnly {
			msg = "провалов нет — нажми f, чтобы показать все"
		}
		for _, l := range strings.Split(ansi.Wrap(msg, cw, ""), "\n") {
			lines = append(lines, " "+l)
		}
		return lines
	}
	rows := max(1, m.bodyH()-paneHead)
	for i := m.listScroll; i < min(len(m.visible), m.listScroll+rows); i++ {
		r := m.runs[m.visible[i]]
		g, st := m.glyph(r)
		right := padLeft(trace.FmtDuration(r.Duration), 6) + " " + padLeft(trace.FmtCost(r.CostUSD), 7)
		if r.Status() == trace.Running {
			right = "идёт"
		}
		marker := " "
		if i == m.sel {
			marker = "▶"
		}
		lines = append(lines, " "+row(cw, i == m.sel, m.focus == focusList, marker, "", g, st, oneLine(r.ID), right))
	}
	return lines
}

func (m Model) rightLines() []string {
	r := m.selected()
	if r == nil {
		return nil
	}
	word := map[trace.Status]string{trace.Passed: "✓ успех", trace.Failed: "✗ провал", trace.Running: "… идёт"}[r.Status()]
	// Status right after the ID: truncation cuts the least important part last.
	parts := []string{oneLine(r.ID), word}
	for _, p := range []string{r.Model, r.Scenario, r.Task} {
		if p != "" {
			parts = append(parts, oneLine(p))
		}
	}
	title := fit(" "+strings.Join(parts, " · "), m.rightW()+2)
	if m.focus == focusSteps {
		title = accent.Render(title)
	} else {
		title = dim.Render(title)
	}
	out := []string{title, ""}
	if pm := m.pmBlock(); pm != nil {
		for _, l := range pm {
			out = append(out, " "+l)
		}
		out = append(out, "")
	}
	for _, l := range window(m.stepRowLines(m.rightW()), m.bodyScroll, m.stepRows()) {
		out = append(out, " "+l)
	}
	return out
}

// pmBlock is the pinned diagnosis of a failed run, cut with an ellipsis so that at
// least minStepRows rows are left for the steps. Nil for passed and running runs.
func (m Model) pmBlock() []string {
	r := m.selected()
	if r == nil || r.Status() != trace.Failed {
		return nil
	}
	w := m.rightW()
	pm := r.Postmortem
	if pm == nil {
		return []string{bold.Render("РАЗБОР"), dim.Render("разбора ещё нет — выполни команду analyze")}
	}
	title := "РАЗБОР"
	switch pm.Source {
	case "rule":
		title += " · правило"
	case "model":
		title += " · модель " + trace.FmtCost(pm.CostUSD)
	}
	lines := []string{bold.Render(title)}
	where := "Сбой не привязан к одному шагу"
	if pm.FailedStep >= 1 && pm.FailedStep <= len(r.Steps) {
		s := r.Steps[pm.FailedStep-1]
		where = fmt.Sprintf("Причина на шаге %d: %s %s", s.N, oneLine(s.Tool), oneLine(s.Summary))
	}
	for _, p := range []string{where, "Причина: " + pm.Cause, "Что поправить: " + pm.Fix} {
		lines = append(lines, strings.Split(ansi.Wrap(trace.Clean(p), w, ""), "\n")...)
	}
	if avail := max(1, m.bodyH()-paneHead-minStepRows-1); len(lines) > avail {
		lines = lines[:avail]
		lines[avail-1] = ansi.Truncate(lines[avail-1], w-1, "") + "…"
	}
	return lines
}

// pmHeight is the rows the pinned block takes in the right pane, with its blank separator.
func (m Model) pmHeight() int {
	if n := len(m.pmBlock()); n > 0 {
		return n + 1
	}
	return 0
}

func (m Model) stepRows() int { return max(1, m.bodyH()-paneHead-m.pmHeight()) }

func (m Model) stepCount() int {
	if r := m.selected(); r != nil {
		return len(r.Steps)
	}
	return 0
}

// stepRowLines draws one row per step; line i is step i, which click and
// scroll-into-view rely on.
func (m Model) stepRowLines(w int) []string {
	r := m.selected()
	if r == nil {
		return nil
	}
	toolW := 0
	for _, s := range r.Steps {
		toolW = max(toolW, min(lipgloss.Width(oneLine(s.Tool)), 10))
	}
	numW := len(fmt.Sprint(len(r.Steps)))
	var lines []string
	for i, s := range r.Steps {
		g, st := s.Glyph(), green
		switch g {
		case "✗":
			st = red
		case "⊘":
			st = dim
		}
		marker := " "
		if m.focus == focusSteps && i == m.step {
			marker = "▶"
		}
		summary := oneLine(s.Line)
		const tag, link = " · запрещено", " ← разбор"
		lead, right := padLeft(fmt.Sprint(s.N), numW)+" ", padLeft(trace.FmtDuration(s.Duration), 6)
		room := midWidth(w, lead, right) - toolW - 2
		tags := ""
		if s.Denied {
			tags = tag
		}
		if r.CauseStep(s) {
			tags += link
		}
		if lipgloss.Width(summary)+lipgloss.Width(tags) > room {
			tags = "" // the link wins when both do not fit
			if r.CauseStep(s) {
				tags = link
			}
		}
		if lipgloss.Width(summary)+lipgloss.Width(tags) > room {
			summary = ansi.Truncate(summary, max(0, room-lipgloss.Width(tags)-1), "") + "…"
		}
		summary += tags
		mid := fit(oneLine(s.Tool), toolW) + "  " + summary
		lines = append(lines, row(w, m.focus == focusSteps && i == m.step, true, marker,
			lead, g, st, mid, right))
	}
	return lines
}

func (m Model) stepLines(w int) []string {
	r := m.selected()
	if r == nil || m.step >= len(r.Steps) {
		return nil
	}
	s := r.Steps[m.step]
	status := "✓ успех"
	switch s.Glyph() {
	case "⊘":
		status = "⊘ запрещено"
	case "✗":
		status = "✗ ошибка"
	}
	lines := []string{"Инструмент: " + oneLine(s.Tool), "Статус: " + status, "Время: " + trace.FmtDuration(s.Duration), ""}
	section := func(title, body string) {
		lines = append(lines, bold.Render(title))
		if strings.TrimSpace(body) == "" {
			lines = append(lines, dim.Render("(пусто)"))
		} else {
			lines = append(lines, strings.Split(ansi.Wrap(trace.Clean(body), w-2, ""), "\n")...)
		}
	}
	section("ВХОД", s.Input)
	lines = append(lines, "")
	section("ВЫХОД", s.Output)
	for i, l := range lines {
		lines[i] = " " + l
	}
	return lines
}

func helpLines() []string {
	return []string{
		"",
		" " + bold.Render("Список прогонов"),
		"   ↑ k / ↓ j            выбрать прогон",
		"   tab, → l, enter      перейти к шагам",
		"   f                    только провалы / все",
		"   ?                    эта справка",
		"   q, ctrl+c            выход",
		"",
		" " + bold.Render("Шаги"),
		"   ↑ k / ↓ j            выбрать шаг",
		"   enter                открыть шаг на весь экран",
		"   tab, ← h, esc        назад к списку",
		"   ?                    эта справка",
		"   q                    выход",
		"",
		" " + bold.Render("Шаг на весь экран"),
		"   ↑ ↓, pgup, pgdn      прокрутка",
		"   esc, q               назад",
		"",
		" " + bold.Render("Мышь"),
		"   клик по прогону или шагу выбирает его, колесо прокручивает панель под курсором",
		"",
		" ✓ успех · ✗ ошибка вызова · ⊘ запрещено · ← разбор — шаг с причиной",
	}
}
