package ask

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/ledger"
)

func entry(id, status string) ledger.Entry {
	f := 1.25
	e := ledger.Entry{Schema: "kaas.run/v1", RunID: "r-" + id, Repo: "o/r", StartedAt: time.Unix(0, 0), Status: status, CostUSD: &f}
	e.Ticket.Source, e.Ticket.ID = "github", id
	e.Agent.Harness, e.Agent.Model = "claude", "secret-model-name"
	return e
}

func TestAskCitesTheBestLines(t *testing.T) {
	a := Ask("why was the migration failed", Sources{
		Ticket:     "7",
		Ledger:     []ledger.Entry{entry("7", "failed"), entry("8", "failed")},
		Transcript: []string{"started", "migration step failed: column exists", "unrelated chatter"},
	})
	if !a.Found || len(a.Cites) != 2 {
		t.Fatalf("answer = %+v", a)
	}
	if a.Cites[0].Source != "transcript:2" || !strings.Contains(a.Cites[0].Text, "column exists") {
		t.Errorf("best cite = %+v", a.Cites[0])
	}
	if a.Cites[1].Source != "ledger" || strings.Contains(a.Cites[1].Text, "r-8") {
		t.Errorf("ledger cite = %+v (another ticket's run must not appear)", a.Cites[1])
	}
}

func TestAskNeverLeaksOperatorDetail(t *testing.T) {
	a := Ask("what model and cost and token", Sources{
		Ticket: "7",
		Ledger: []ledger.Entry{entry("7", "merged")},
		Events: []api.Event{{Kind: "usage", Data: map[string]any{"model": "secret-model-name", "cost": 1.25, "tokens": 9}}},
	})
	for _, c := range a.Cites {
		for _, bad := range []string{"secret-model-name", "1.25"} {
			if strings.Contains(c.Text, bad) {
				t.Errorf("cite %+v leaks %q", c, bad)
			}
		}
	}
}

func TestAskRedactsSecrets(t *testing.T) {
	a := Ask("token", Sources{Ticket: "7", Transcript: []string{"token ghp_abcdefghijklmnopqrstuvwxyz0123456789 used"}})
	if len(a.Cites) == 1 && strings.Contains(a.Cites[0].Text, "ghp_abcdefghijklmnopqrstuvwxyz0123456789") {
		t.Fatalf("secret quoted: %+v", a.Cites[0])
	}
}

func TestAskNoMatchSaysSo(t *testing.T) {
	a := Ask("zebra", Sources{Ticket: "7", Transcript: []string{"hello"}})
	if a.Found || !strings.Contains(Text(a), "No record") {
		t.Fatalf("answer = %+v", a)
	}
	if Ask("", Sources{}).Found {
		t.Fatal("empty question must find nothing")
	}
}

func TestAskCapsCites(t *testing.T) {
	var tr []string
	for range 20 {
		tr = append(tr, "build failed")
	}
	if n := len(Ask("build failed", Sources{Ticket: "7", Transcript: tr}).Cites); n != MaxCites {
		t.Fatalf("cites = %d", n)
	}
}

// TestAskIsReadOnly: asking leaves the directory it reads from byte for byte as it was.
func TestAskIsReadOnly(t *testing.T) {
	root := t.TempDir()
	if err := ledger.Append(root, entry("7", "failed")); err != nil {
		t.Fatal(err)
	}
	snap := func() string {
		var b strings.Builder
		_ = filepath.Walk(root, func(p string, i os.FileInfo, _ error) error {
			if !i.IsDir() {
				d, _ := os.ReadFile(p)
				b.WriteString(p + string(d) + i.ModTime().String())
			}
			return nil
		})
		return b.String()
	}
	before := snap()
	all, err := ledger.Entries(root)
	if err != nil {
		t.Fatal(err)
	}
	Ask("status failed", Sources{Ticket: "7", Ledger: all})
	if snap() != before {
		t.Fatal("ask changed the ledger directory")
	}
}
