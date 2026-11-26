package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/KaaS-team/ui"
	"github.com/KaaS-team/ui/term"
)

// sgr is the truecolour escape for a #rrggbb token: 38 for foreground, 48 for background.
func sgr(layer int, hex string) string {
	v, _ := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	return fmt.Sprintf("%d;2;%d;%d;%d", layer, v>>16, v>>8&0xff, v&0xff)
}

// A status chip draws the status token pair of the shared theme, in the mode the terminal is in.
func TestBadgeUsesStatusTokens(t *testing.T) {
	for _, dark := range []bool{true, false} {
		got := newTheme(dark).badge(ui.StatusFail, "FAIL")
		fg, bg := term.Default.Status(ui.StatusFail)
		for _, want := range []string{sgr(38, fg.Pick(dark)), sgr(48, bg.Pick(dark))} {
			if !strings.Contains(got, want) {
				t.Errorf("dark=%v: badge %q lacks %s", dark, got, want)
			}
		}
	}
}

// Every screen follows the terminal background: a light report draws the light tokens.
func TestScreensSwitchToLight(t *testing.T) {
	light := tea.BackgroundColorMsg{Color: lipglossWhite{}}
	text := sgr(38, term.Default.Text.Light)
	board, _ := newModel(t, fixedBoard())
	review := reviewAt(fixedReview(), 100, 30)
	report := reportAt((&fakeReady{}).funcs(), 100, 30)
	for name, m := range map[string]tea.Model{"board": board, "review": review, "report": report} {
		m.Update(light)
		if v := m.View().Content; !strings.Contains(v, text) && !strings.Contains(v, sgr(38, term.Default.Muted.Light)) {
			t.Errorf("%s draws no light token after a light background", name)
		}
	}
	if review.th.dark || report.th.dark || board.th.dark {
		t.Error("a screen kept the dark theme")
	}
}

type lipglossWhite struct{}

func (lipglossWhite) RGBA() (r, g, b, a uint32) { return 0xffff, 0xffff, 0xffff, 0xffff }

// rawColour is a colour written in this package instead of read from the ui tokens.
var rawColour = regexp.MustCompile(`lipgloss\.(Color|ANSIColor)\(\s*("|[0-9])|#[0-9a-fA-F]{6}\b|\bcolor\.RGBA\{`)

// No colour is written in internal/tui: every one comes from github.com/KaaS-team/ui/term.
func TestNoRawColours(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if rawColour.MatchString(line) {
				t.Errorf("%s:%d writes a raw colour; use a token from ui/term: %s", f, i+1, strings.TrimSpace(line))
			}
		}
	}
}
