package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/KaaS-team/ui"
)

// CheckStatus is the outcome of one readiness check.
type CheckStatus string

// The outcomes. CheckUnknown means the check could not run; it does not fail readiness.
const (
	CheckPass    CheckStatus = "pass"
	CheckFail    CheckStatus = "fail"
	CheckUnknown CheckStatus = "unknown"
)

// ReadyCheck is one readiness result as the report screen shows it.
type ReadyCheck struct {
	Name   string
	Status CheckStatus
	Detail string
	// Fix says what applying fixes does for this check; empty means it needs a human.
	Fix string
}

// Readiness is the first-run report: the checks, and what the last fix repaired. The caller builds it
// from internal/firstrun; the TUI runs no check itself.
type Readiness struct {
	Dir    string
	Checks []ReadyCheck
	// Fixed names each check the last fix repaired, with what it did.
	Fixed []string
}

// OK is true when no check failed.
func (r Readiness) OK() bool {
	for _, c := range r.Checks {
		if c.Status == CheckFail {
			return false
		}
	}
	return true
}

func (r Readiness) fixable() int {
	n := 0
	for _, c := range r.Checks {
		if c.Status == CheckFail && c.Fix != "" {
			n++
		}
	}
	return n
}

// ReadyFuncs runs the checks for the report screen. Fix applies every fix in place and checks again.
type ReadyFuncs struct {
	Check func(ctx context.Context) (Readiness, error)
	Fix   func(ctx context.Context) (Readiness, error)
}

type reportState int

const (
	reportChecking reportState = iota
	reportShown
	reportConfirm
	reportFixing
)

type readyMsg struct {
	r   Readiness
	err error
}

// ReportModel is the first-run report screen: readiness checks, their fixes, and a key to apply them.
type ReportModel struct {
	ctx           context.Context
	fn            ReadyFuncs
	th            theme
	width, height int
	state         reportState
	ready         Readiness
	err           error
	scroll        int
}

// NewReport returns the first-run report screen. It runs fn.Check when it starts.
func NewReport(ctx context.Context, fn ReadyFuncs) *ReportModel {
	return &ReportModel{ctx: ctx, fn: fn, th: newTheme(true), width: defaultWidth, height: defaultHeight}
}

// Readiness returns the latest report, for the exit status once the program has quit.
func (m *ReportModel) Readiness() Readiness { return m.ready }

// Err returns the error of the latest check or fix.
func (m *ReportModel) Err() error { return m.err }

// Init starts the checks and asks the terminal for its background.
func (m *ReportModel) Init() tea.Cmd {
	return tea.Batch(m.run(m.fn.Check), tea.RequestBackgroundColor)
}

func (m *ReportModel) run(f func(context.Context) (Readiness, error)) tea.Cmd {
	return func() tea.Msg {
		r, err := f(m.ctx)
		return readyMsg{r, err}
	}
}

// Update applies one message.
func (m *ReportModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.BackgroundColorMsg:
		m.th = newTheme(msg.IsDark())
	case readyMsg:
		m.state, m.err = reportShown, msg.err
		if msg.err == nil {
			m.ready = msg.r
		}
	case tea.KeyPressMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m *ReportModel) key(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	if s == "ctrl+c" || s == "q" && m.state != reportConfirm {
		return m, tea.Quit
	}
	switch m.state {
	case reportConfirm:
		if s == "y" {
			m.state = reportFixing
			return m, m.run(m.fn.Fix)
		}
		m.state = reportShown
		return m, nil
	case reportChecking, reportFixing:
		return m, nil
	}
	switch s {
	case "esc":
		return m, tea.Quit
	case "j", "down":
		m.scroll++
	case "k", "up":
		m.scroll = max(m.scroll-1, 0)
	case "f":
		if m.ready.fixable() > 0 && m.fn.Fix != nil {
			m.state = reportConfirm
		}
	case "R":
		m.state = reportChecking
		return m, m.run(m.fn.Check)
	}
	return m, nil
}

// View renders the screen to exactly width x height cells.
func (m *ReportModel) View() tea.View {
	w, h := m.width, m.height
	if w <= 0 || h <= 0 {
		w, h = defaultWidth, defaultHeight
	}
	head := m.reportHeader(w)
	foot := m.reportFooter(w)
	bodyH := max(h-len(head)-len(foot), 1)
	body := m.reportBody(w)
	m.scroll = min(m.scroll, max(len(body)-bodyH, 0))
	body = body[m.scroll:]
	if len(body) > bodyH {
		body = append(body[:bodyH-1], m.th.muted.Render("more below: j"))
	}
	v := tea.NewView(strings.Join(append(append(head, fit(body, bodyH)...), foot...), "\n"))
	v.AltScreen = true
	return v
}

func (m *ReportModel) reportHeader(w int) []string {
	title := clip(m.th.muted.Render("factory  ")+m.th.selected.Render("first-run report"), w)
	var state string
	switch {
	case m.state == reportChecking:
		state = m.th.badge(ui.StatusInfo, "checking")
	case m.state == reportFixing:
		state = m.th.badge(ui.StatusInfo, "fixing")
	case m.err != nil:
		state = m.th.badge(ui.StatusFail, "could not check")
	case m.ready.OK():
		state = m.th.badge(ui.StatusPass, "ready for agents")
	default:
		state = m.th.badge(ui.StatusFail, "not ready")
	}
	dir := m.ready.Dir
	if dir == "" {
		dir = "this repo"
	}
	counts := m.th.muted.Render(m.counts())
	// A long path keeps its end, the part that names the repo.
	room := w - lipgloss.Width(state) - 1
	if n := len([]rune(dir)); n > room {
		dir = "…" + string([]rune(dir)[n-room+1:])
	}
	left := m.th.title.Render(dir)
	if m.counts() == "" || lipgloss.Width(left+"  "+counts+" "+state) <= w {
		return []string{title, spread(left+"  "+counts, state, w), ""}
	}
	return []string{title, spread(left, state, w), clip(counts, w), ""}
}

func (m *ReportModel) counts() string {
	pass, fail, unknown := 0, 0, 0
	for _, c := range m.ready.Checks {
		switch c.Status {
		case CheckPass:
			pass++
		case CheckFail:
			fail++
		default:
			unknown++
		}
	}
	if len(m.ready.Checks) == 0 {
		return ""
	}
	return fmt.Sprintf("%d of %d pass  %d failing  %d not checked", pass, len(m.ready.Checks), fail, unknown)
}

// nameWidth is the column the check names take; the longest name, "branch protection", fits.
const nameWidth = 19

func (m *ReportModel) reportBody(w int) []string {
	switch {
	case m.state == reportChecking && len(m.ready.Checks) == 0:
		return []string{m.th.muted.Render("Checking: builds, tests, AGENTS.md, gate script, CI, branch protection.")}
	case m.err != nil:
		return append([]string{m.th.warn.Render("The checks could not run.")},
			strings.Split(wrap(m.err.Error(), w), "\n")...)
	}
	inner := w - 4
	var rows []string
	for i, c := range m.ready.Checks {
		if i > 0 {
			rows = append(rows, "")
		}
		rows = append(rows, m.checkRow(c, inner)...)
	}
	out := strings.Split(m.th.panel(w, false).Render(m.th.muted.Render("Readiness")+"\n"+strings.Join(rows, "\n")), "\n")
	if len(m.ready.Fixed) > 0 {
		out = append(out, "", m.th.status(ui.StatusPass).Render("Fixed"))
		for _, f := range m.ready.Fixed {
			out = append(out, strings.Split(wrap("  "+f, w), "\n")...)
		}
	}
	return out
}

// wideRows is the narrowest row that keeps the name and detail columns side by side.
const wideRows = 68

// badgeWidth is the cells of the widest badge, "UNKNOWN" with its padding.
const badgeWidth = 9

func (m *ReportModel) checkRow(c ReadyCheck, w int) []string {
	st, label := ui.StatusWait, "UNKNOWN"
	switch c.Status {
	case CheckPass:
		st, label = ui.StatusPass, "PASS"
	case CheckFail:
		st, label = ui.StatusFail, "FAIL"
	}
	badge := m.th.badge(st, fmt.Sprintf("%-7s", label))
	detail := c.Detail
	if detail == "" && c.Status == CheckPass {
		detail = "ok"
	}
	var lines []string
	indent := badgeWidth + 1
	if w >= wideRows {
		indent += nameWidth + 1
		name := m.th.title.Render(fmt.Sprintf("%-*s", nameWidth, c.Name))
		lines = strings.Split(hang(badge+" "+name+" ", detail, indent, w), "\n")
	} else {
		// Narrow: the name follows the badge and the detail starts on the next line.
		lines = []string{badge + " " + m.th.title.Render(c.Name)}
		if c.Status != CheckPass {
			lines = append(lines, strings.Split(hang(strings.Repeat(" ", indent), detail, indent, w), "\n")...)
		}
	}
	if c.Status != CheckFail {
		return lines
	}
	pad := strings.Repeat(" ", indent)
	if c.Fix != "" {
		return append(lines, strings.Split(hang(pad, m.th.status(ui.StatusInfo).Render("fix: "+c.Fix), indent, w), "\n")...)
	}
	return append(lines, pad+m.th.muted.Render("no automatic fix; fix it by hand"))
}

func (m *ReportModel) reportFooter(w int) []string {
	if m.state == reportConfirm {
		return []string{"", clip(m.th.title.Render(fmt.Sprintf("Apply %s in place? y/n", plural(m.ready.fixable(), "fix"))), w)}
	}
	keys := "j/k scroll  R recheck  q quit"
	if n := m.ready.fixable(); n > 0 && m.fn.Fix != nil {
		keys = fmt.Sprintf("f apply %s  ", plural(n, "fix")) + keys
	}
	return []string{"", clip(m.th.muted.Render(keys), w)}
}
