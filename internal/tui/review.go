package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/KaaS-team/ui"
)

// PlanTicket is one planned ticket as the review screen shows it.
type PlanTicket struct {
	ID, Title string
	// Why is the ticket's problem statement.
	Why string
	// Acceptance holds the "AC-n: text" lines.
	Acceptance []string
	Files      []string
	Depends    []string
	// Wave is the 0-based wave the ticket runs in; tickets in one wave run in parallel.
	Wave int
}

// PlanReview is what the plan review screen shows: an epic, its tickets in wave order, and the
// problems that block approval. The caller builds it from internal/plan; the TUI reads no plan itself.
type PlanReview struct {
	Epic     string
	Tickets  []PlanTicket
	Problems []string
}

// Verdict is what the reviewer decided.
type Verdict int

// The verdicts. VerdictNone means the reviewer quit without deciding.
const (
	VerdictNone Verdict = iota
	VerdictApprove
	VerdictRevise
)

// Decision is the result of a review. Note is the revision request and is set only for VerdictRevise.
type Decision struct {
	Verdict Verdict
	Note    string
}

type reviewMode int

const (
	reviewBrowse reviewMode = iota
	reviewConfirm
	reviewNote
)

// twoColumns is the narrowest width that shows the ticket list beside the detail pane.
const twoColumns = 96

// ReviewModel is the plan review screen: browse the tickets, then approve or ask for a revision.
type ReviewModel struct {
	review        PlanReview
	th            theme
	width, height int
	cursor        int
	scroll        int // first detail line shown
	mode          reviewMode
	input         textinput.Model
	status        string
	decision      Decision
}

// NewReview returns the plan review screen for r.
func NewReview(r PlanReview) *ReviewModel {
	in := textinput.New()
	in.Prompt = "revise> "
	in.Placeholder = "what should change"
	return &ReviewModel{review: r, th: newTheme(true), width: defaultWidth, height: defaultHeight, input: in}
}

// Decision returns what the reviewer decided once the program has quit.
func (m *ReviewModel) Decision() Decision { return m.decision }

// Init asks the terminal for its background so the theme matches it.
func (m *ReviewModel) Init() tea.Cmd { return tea.RequestBackgroundColor }

// Update applies one message.
func (m *ReviewModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.BackgroundColorMsg:
		m.th = newTheme(msg.IsDark())
	case tea.KeyPressMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m *ReviewModel) key(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case reviewConfirm:
		m.mode = reviewBrowse
		if k.String() == "y" {
			m.decision = Decision{Verdict: VerdictApprove}
			return m, tea.Quit
		}
		m.status = "approval cancelled"
		return m, nil
	case reviewNote:
		switch k.String() {
		case "esc":
			m.mode, m.status = reviewBrowse, "revision cancelled"
			m.input.Blur()
			return m, nil
		case "enter":
			note := strings.TrimSpace(m.input.Value())
			if note == "" {
				m.status = "say what should change, or esc to go back"
				return m, nil
			}
			m.decision = Decision{Verdict: VerdictRevise, Note: note}
			return m, tea.Quit
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(k)
		return m, cmd
	}
	m.status = ""
	switch k.String() {
	case "q", "ctrl+c", "esc":
		return m, tea.Quit
	case "j", "down":
		m.move(1)
	case "k", "up":
		m.move(-1)
	case "g", "home":
		m.move(-len(m.review.Tickets))
	case "G", "end":
		m.move(len(m.review.Tickets))
	case "pgdown", "space":
		m.scroll += max(m.height/2, 1)
	case "pgup":
		m.scroll = max(m.scroll-max(m.height/2, 1), 0)
	case "a":
		if n := len(m.review.Problems); n > 0 {
			m.status = fmt.Sprintf("cannot approve: fix the %s first", plural(n, "problem"))
			return m, nil
		}
		m.mode = reviewConfirm
	case "r":
		m.mode = reviewNote
		m.input.Reset()
		return m, m.input.Focus()
	}
	return m, nil
}

func (m *ReviewModel) move(d int) {
	m.cursor = min(max(m.cursor+d, 0), max(len(m.review.Tickets)-1, 0))
	m.scroll = 0
}

// View renders the screen to exactly width x height cells.
func (m *ReviewModel) View() tea.View {
	w, h := m.width, m.height
	if w <= 0 || h <= 0 {
		w, h = defaultWidth, defaultHeight
	}
	head := m.reviewHeader(w)
	foot := m.reviewFooter(w)
	bodyH := max(h-len(head)-len(foot), 1)
	v := tea.NewView(strings.Join(append(append(head, fit(m.reviewBody(w, bodyH), bodyH)...), foot...), "\n"))
	v.AltScreen = true
	return v
}

func (m *ReviewModel) reviewHeader(w int) []string {
	r := m.review
	waves := 0
	criteria := 0
	for _, t := range r.Tickets {
		waves = max(waves, t.Wave+1)
		criteria += len(t.Acceptance)
	}
	state := m.th.badge(ui.StatusPass, "ready to approve")
	if n := len(r.Problems); n > 0 {
		state = m.th.badge(ui.StatusFail, plural(n, "problem"))
	}
	counts := m.th.muted.Render(fmt.Sprintf("%s  %s  %s",
		plural(len(r.Tickets), "ticket"), plural(waves, "wave"), plural(criteria, "criterion")))
	out := []string{clip(m.th.muted.Render("factory  ")+m.th.selected.Render("plan review"), w)}
	left := m.th.title.Render(r.Epic)
	if lipgloss.Width(left+"  "+counts+" "+state) <= w {
		out = append(out, spread(left+"  "+counts, state, w))
	} else {
		out = append(out, spread(left, state, w), clip(counts, w))
	}
	if len(r.Problems) > 0 {
		box := m.th.status(ui.StatusFail).Render("Problems")
		for _, p := range r.Problems {
			box += "\n" + wrap("- "+p, w-4)
		}
		out = append(out, strings.Split(m.th.panel(w, false).BorderForeground(m.th.status(ui.StatusFail).GetForeground()).Render(box), "\n")...)
	}
	return append(out, "")
}

func (m *ReviewModel) reviewFooter(w int) []string {
	switch m.mode {
	case reviewConfirm:
		return []string{"", clip(m.th.title.Render(fmt.Sprintf("Approve and file %s? y/n", plural(len(m.review.Tickets), "ticket"))), w)}
	case reviewNote:
		return []string{clip(m.th.muted.Render("Enter sends the note, esc goes back"), w), clip(m.input.View(), w)}
	}
	keys := "j/k ticket  pgup/pgdn scroll  a approve  r revise  q quit"
	return []string{clip(m.th.warn.Render(m.status), w), clip(m.th.muted.Render(keys), w)}
}

func (m *ReviewModel) reviewBody(w, h int) []string {
	if len(m.review.Tickets) == 0 {
		return []string{m.th.muted.Render("The plan has no tickets. Press r to ask for a revision.")}
	}
	if w < twoColumns {
		list := m.compactList(w)
		// The list takes at most a third of the height on a narrow screen; the rest is detail.
		listH := min(len(list), max(h/3, 3))
		list = window(list, m.cursor, listH)
		detail := m.detail(w)
		return append(append(list, m.th.muted.Render(strings.Repeat("─", w))), m.scrolled(detail, h-listH-1)...)
	}
	lw := min(48, w*2/5)
	dw := w - lw - 1
	list := window(m.ticketList(lw), m.listLine(), h)
	detail := m.th.panel(dw, true).Render(strings.Join(m.scrolled(m.detail(dw-4), h-2), "\n"))
	return strings.Split(lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.NewStyle().Width(lw+1).Render(strings.Join(list, "\n")), detail), "\n")
}

// ticketList is the left column: tickets grouped under their wave.
func (m *ReviewModel) ticketList(w int) []string {
	var out []string
	wave := -1
	for i, t := range m.review.Tickets {
		if t.Wave != wave {
			wave = t.Wave
			if len(out) > 0 {
				out = append(out, "")
			}
			out = append(out, m.th.muted.Render(fmt.Sprintf("Wave %d", wave+1)))
		}
		out = append(out, m.ticketRow(i, "", w))
	}
	return out
}

// compactList is the narrow list: one row per ticket with its wave in front, no headings.
func (m *ReviewModel) compactList(w int) []string {
	out := make([]string, len(m.review.Tickets))
	for i, t := range m.review.Tickets {
		out[i] = m.ticketRow(i, fmt.Sprintf("W%d ", t.Wave+1), w)
	}
	return out
}

func (m *ReviewModel) ticketRow(i int, prefix string, w int) string {
	t := m.review.Tickets[i]
	ac := fmt.Sprintf("%d AC", len(t.Acceptance))
	name := prefix + t.ID + "  " + t.Title
	room := w - len(ac) - 2
	if i == m.cursor {
		return spread(m.th.selected.Render(ellipsis("> "+name, room)), m.th.selected.Render(ac), w)
	}
	return spread(m.th.text.Render(ellipsis("  "+name, room)), m.th.muted.Render(ac), w)
}

// ellipsis cuts plain text s to w cells, ending in "…" when it cut.
func ellipsis(s string, w int) string { return ansi.Truncate(s, max(w, 1), "…") }

// listLine is the line of the selected ticket in ticketList.
func (m *ReviewModel) listLine() int {
	line, wave := 0, -1
	for i, t := range m.review.Tickets {
		if t.Wave != wave {
			if wave >= 0 {
				line++
			}
			wave = t.Wave
			line++
		}
		if i == m.cursor {
			return line
		}
		line++
	}
	return 0
}

// detail is the selected ticket: problem, acceptance criteria, files and dependencies.
func (m *ReviewModel) detail(w int) []string {
	t := m.review.Tickets[m.cursor]
	deps := "none"
	if len(t.Depends) > 0 {
		deps = strings.Join(t.Depends, ", ")
	}
	out := []string{
		m.th.title.Render(strings.TrimRight(wrap(t.ID+"  "+t.Title, w), " ")),
		m.th.muted.Render(fmt.Sprintf("Wave %d  depends on %s", t.Wave+1, deps)),
		"",
		m.th.selected.Render("Problem"),
	}
	why := t.Why
	if why == "" {
		why = "No problem statement."
	}
	out = append(out, strings.Split(wrap(why, w), "\n")...)
	out = append(out, "", m.th.selected.Render("Acceptance criteria"))
	if len(t.Acceptance) == 0 {
		out = append(out, m.th.warn.Render("None. The ticket cannot be checked."))
	}
	for _, a := range t.Acceptance {
		id, text, ok := strings.Cut(a, ":")
		if !ok {
			id, text = "", a
		}
		out = append(out, strings.Split(hang(m.th.muted.Render(fmt.Sprintf("%-6s", id)), strings.TrimSpace(text), 6, w), "\n")...)
	}
	out = append(out, "", m.th.selected.Render("Files"))
	if len(t.Files) == 0 {
		out = append(out, m.th.warn.Render("None listed."))
	}
	for _, f := range t.Files {
		out = append(out, clip("  "+f, w))
	}
	return out
}

// scrolled returns h lines of lines from the scroll offset, clamping the offset.
func (m *ReviewModel) scrolled(lines []string, h int) []string {
	m.scroll = min(m.scroll, max(len(lines)-h, 0))
	lines = lines[m.scroll:]
	if len(lines) > h {
		lines = append(lines[:h-1], m.th.muted.Render("more below: pgdn"))
	}
	return lines
}

// window returns h lines of lines that keep line sel in view.
func window(lines []string, sel, h int) []string {
	if len(lines) <= h {
		return lines
	}
	start := min(max(sel-h+1, 0), len(lines)-h)
	return lines[start : start+h]
}

// fit pads or cuts lines to exactly h.
func fit(lines []string, h int) []string {
	for len(lines) < h {
		lines = append(lines, "")
	}
	return lines[:h]
}

// spread puts left and right on one line w cells wide, right-aligned, cutting left when both do not fit.
func spread(left, right string, w int) string {
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return clip(left, max(w-lipgloss.Width(right)-1, 0)) + " " + right
	}
	return left + strings.Repeat(" ", gap) + right
}

// wrap breaks s into lines of at most w cells.
func wrap(s string, w int) string { return lipgloss.NewStyle().Width(max(w, 1)).Render(s) }

// hang writes label then text wrapped to w, with the wrapped lines indented by indent cells.
func hang(label, text string, indent, w int) string {
	lines := strings.Split(wrap(text, w-indent), "\n")
	pad := strings.Repeat(" ", indent)
	for i := range lines {
		lines[i] = pad + strings.TrimRight(lines[i], " ")
	}
	lines[0] = label + strings.TrimPrefix(lines[0], pad)
	return strings.Join(lines, "\n")
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	switch {
	case word == "criterion":
		return fmt.Sprintf("%d criteria", n)
	case strings.HasSuffix(word, "x"):
		return fmt.Sprintf("%d %ses", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}
