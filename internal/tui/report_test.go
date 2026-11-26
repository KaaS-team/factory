package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func mixedReadiness() Readiness {
	return Readiness{Dir: "acme/api", Checks: []ReadyCheck{
		{Name: "builds", Status: CheckPass},
		{Name: "tests run", Status: CheckFail, Detail: "`go test ./...` failed: FAIL github.com/acme/api/store 0.41s"},
		{Name: "AGENTS.md", Status: CheckFail, Detail: "no AGENTS.md; agents start with no rules for this repo", Fix: "write an AGENTS.md skeleton"},
		{Name: "gate script", Status: CheckPass},
		{Name: "CI exists", Status: CheckFail, Detail: "no CI config; nothing checks a pull request", Fix: "add .github/workflows/ci.yml that runs gate.sh"},
		{Name: "branch protection", Status: CheckUnknown, Detail: "needs the GitHub repo and a token in the config to check it"},
	}}
}

func fixedReadiness() Readiness {
	r := mixedReadiness()
	r.Checks[2] = ReadyCheck{Name: "AGENTS.md", Status: CheckPass}
	r.Checks[4] = ReadyCheck{Name: "CI exists", Status: CheckPass}
	r.Fixed = []string{"AGENTS.md: write an AGENTS.md skeleton", "CI exists: add .github/workflows/ci.yml that runs gate.sh"}
	return r
}

func passingReadiness() Readiness {
	r := fixedReadiness()
	r.Checks[1] = ReadyCheck{Name: "tests run", Status: CheckPass}
	r.Fixed = nil
	return r
}

type fakeReady struct{ checks, fixes int }

func (f *fakeReady) funcs() ReadyFuncs {
	return ReadyFuncs{
		Check: func(context.Context) (Readiness, error) { f.checks++; return mixedReadiness(), nil },
		Fix:   func(context.Context) (Readiness, error) { f.fixes++; return fixedReadiness(), nil },
	}
}

// reportRun feeds a model's command back into it until it settles, as the program would.
func reportRun(m *ReportModel, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			reportRun(m, c)
		}
	case readyMsg:
		_, next := m.Update(msg)
		reportRun(m, next)
	}
}

func reportAt(fn ReadyFuncs, w, h int) *ReportModel {
	m := NewReport(context.Background(), fn)
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	reportRun(m, m.Init())
	return m
}

func reportPress(m *ReportModel, keys ...string) {
	for _, k := range keys {
		_, cmd := m.Update(keyMsg(k))
		reportRun(m, cmd)
	}
}

func readyFuncs(r Readiness) ReadyFuncs {
	return ReadyFuncs{Check: func(context.Context) (Readiness, error) { return r, nil }}
}

func TestGoldenReport(t *testing.T) {
	f := &fakeReady{}
	cases := []struct {
		name string
		fn   ReadyFuncs
		w, h int
		keys []string
	}{
		{"report_100x30", f.funcs(), 100, 30, nil},
		{"report_60x30", f.funcs(), 60, 30, nil},
		{"report_confirm_100x30", f.funcs(), 100, 30, []string{"f"}},
		{"report_fixed_100x30", f.funcs(), 100, 30, []string{"f", "y"}},
		{"report_ready_100x30", readyFuncs(passingReadiness()), 100, 30, nil},
		{"report_error_100x30", ReadyFuncs{Check: func(context.Context) (Readiness, error) {
			return Readiness{}, errors.New("open go.mod: permission denied")
		}}, 100, 30, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := reportAt(tc.fn, tc.w, tc.h)
			reportPress(m, tc.keys...)
			got := strip(m.View().Content)
			checkSize(t, got, tc.w, tc.h)
			goldenCheck(t, "testdata/"+tc.name+".golden", got)
		})
	}
}

// Before the first check returns, the screen says what it is checking.
func TestGoldenReportChecking(t *testing.T) {
	m := NewReport(context.Background(), (&fakeReady{}).funcs())
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	got := strip(m.View().Content)
	checkSize(t, got, 100, 30)
	goldenCheck(t, "testdata/report_checking_100x30.golden", got)
}

func TestReportFixNeedsConfirm(t *testing.T) {
	f := &fakeReady{}
	m := reportAt(f.funcs(), 100, 30)
	reportPress(m, "f", "n")
	if f.fixes != 0 {
		t.Fatal("n applied the fixes")
	}
	reportPress(m, "f", "y")
	if f.fixes != 1 {
		t.Fatalf("fixes = %d, want 1", f.fixes)
	}
	if got := m.Readiness(); len(got.Fixed) != 2 {
		t.Fatalf("fixed = %v", got.Fixed)
	}
}

// With nothing fixable, f does nothing and the footer does not offer it.
func TestReportNoFixOffered(t *testing.T) {
	fixes := 0
	fn := readyFuncs(passingReadiness())
	fn.Fix = func(context.Context) (Readiness, error) { fixes++; return passingReadiness(), nil }
	m := reportAt(fn, 100, 30)
	reportPress(m, "f", "y")
	if fixes != 0 {
		t.Fatal("f fixed a passing repo")
	}
	if strings.Contains(strip(m.View().Content), "f apply") {
		t.Fatal("footer offers a fix")
	}
}

func TestReportRecheck(t *testing.T) {
	f := &fakeReady{}
	m := reportAt(f.funcs(), 100, 30)
	reportPress(m, "R")
	if f.checks != 2 {
		t.Fatalf("checks = %d, want 2", f.checks)
	}
}

func TestReportOK(t *testing.T) {
	if mixedReadiness().OK() {
		t.Fatal("a report with failures is OK")
	}
	if !passingReadiness().OK() {
		t.Fatal("a passing report is not OK")
	}
	r := passingReadiness()
	r.Checks[5].Status = CheckUnknown
	if !r.OK() {
		t.Fatal("an unknown check failed readiness")
	}
}

func TestReportQuit(t *testing.T) {
	m := reportAt((&fakeReady{}).funcs(), 100, 30)
	_, cmd := m.Update(keyMsg("q"))
	if cmd == nil || cmd() != tea.Quit() {
		t.Fatal("q did not quit")
	}
}

// A long path keeps its end and the counts move to their own line, so neither is cut off.
func TestReportLongDir(t *testing.T) {
	r := mixedReadiness()
	r.Dir = "/tmp/" + strings.Repeat("deep/", 30) + "acme-api"
	m := reportAt(readyFuncs(r), 100, 30)
	got := strip(m.View().Content)
	checkSize(t, got, 100, 30)
	lines := strings.Split(got, "\n")
	if !strings.Contains(lines[1], "…") || !strings.Contains(lines[1], "deep/acme-api") || !strings.Contains(lines[1], "not ready") {
		t.Fatalf("header line = %q", lines[1])
	}
	if !strings.Contains(lines[2], "2 of 6 pass") {
		t.Fatalf("counts line = %q", lines[2])
	}
}
