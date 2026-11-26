package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"

	"github.com/KaaS-team/ui"
	"github.com/KaaS-team/ui/term"
)

// theme is every style the screens draw with. Each colour comes from the KaaS design tokens in
// github.com/KaaS-team/ui/term, so the terminal matches the web dashboard and no colour is written here.
// A screen starts dark and switches when the terminal reports a light background.
type theme struct {
	dark bool

	text, muted, title, selected, warn, stalled lipgloss.Style
	// border is the colour of an unselected card; focus is the selected one.
	border, focus color.Color
}

func pick(c term.Color, dark bool) color.Color { return lipgloss.Color(c.Pick(dark)) }

func newTheme(dark bool) theme {
	t := term.Default
	return theme{
		dark:     dark,
		text:     lipgloss.NewStyle().Foreground(pick(t.Text, dark)),
		muted:    lipgloss.NewStyle().Foreground(pick(t.Muted, dark)),
		title:    lipgloss.NewStyle().Bold(true).Foreground(pick(t.Text, dark)),
		selected: lipgloss.NewStyle().Bold(true).Foreground(pick(t.Info, dark)),
		warn:     lipgloss.NewStyle().Bold(true).Foreground(pick(t.Fail, dark)),
		stalled:  lipgloss.NewStyle().Bold(true).Foreground(pick(t.Wait, dark)),
		border:   pick(t.Border, dark),
		focus:    pick(t.Focus, dark),
	}
}

// badge draws label as a status chip: the status colour on its tinted background, as the web chip does.
func (th theme) badge(s ui.Status, label string) string {
	fg, bg := term.Default.Status(s)
	return lipgloss.NewStyle().Bold(true).Padding(0, 1).
		Foreground(pick(fg, th.dark)).Background(pick(bg, th.dark)).Render(label)
}

// status is the status colour alone, for text that is not a chip.
func (th theme) status(s ui.Status) lipgloss.Style {
	fg, _ := term.Default.Status(s)
	return lipgloss.NewStyle().Foreground(pick(fg, th.dark))
}

// panel is a rounded box w cells wide with the border colour of focus or rest.
func (th theme) panel(w int, focused bool) lipgloss.Style {
	c := th.border
	if focused {
		c = th.focus
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c).Width(w).Padding(0, 1)
}
