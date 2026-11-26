// Package ledger writes the run ledger, the one line per finished run that dx and the analytics API
// read. The line is contract.RunLedgerEntry, pinned by docs/contracts/run-ledger.v1.schema.json.
// Every number the analytics API and spend.json show is derived from these entries, nowhere else.
package ledger

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/KaaS-team/factory/ee/contract"
)

// Entry is one ledger line.
type Entry = contract.RunLedgerEntry

// Dir is where the ledger lives, relative to the repository root.
const Dir = ".factory/ledger"

// RunsFile and SpendFile are the file names inside Dir.
const (
	RunsFile  = "runs.jsonl"
	SpendFile = "spend.json"
)

var (
	statuses = []string{"open", "merged", "failed", "cancelled", "abandoned"}
	sources  = []string{"github", "linear", "jira", "slack", "sentry", "api", "prd"}
	classes  = []string{"retryable", "repo_setup", "external", "agent", "policy", "budget"}
	verdicts = []string{"pass", "fail", "unverified", "skipped"}
)

// Validate reports the first way e breaks the schema's required fields and enums. The schema file
// stays the source of truth; a test checks every written entry against it.
func Validate(e Entry) error {
	switch {
	case e.Schema != contract.RunLedgerSchema:
		return fmt.Errorf("ledger: schema must be %q", contract.RunLedgerSchema)
	case e.RunID == "":
		return errors.New("ledger: run_id is required")
	case e.Repo == "":
		return errors.New("ledger: repo is required")
	case !slices.Contains(sources, e.Ticket.Source) || e.Ticket.ID == "":
		return errors.New("ledger: ticket needs a known source and an id")
	case e.Agent.Harness == "":
		return errors.New("ledger: agent.harness is required")
	case e.StartedAt.IsZero():
		return errors.New("ledger: started_at is required")
	case !slices.Contains(statuses, e.Status):
		return fmt.Errorf("ledger: unknown status %q", e.Status)
	case e.FailureClass != "" && !slices.Contains(classes, e.FailureClass):
		return fmt.Errorf("ledger: unknown failure_class %q", e.FailureClass)
	case e.QA != nil && e.QA.Verdict != "" && !slices.Contains(verdicts, e.QA.Verdict):
		return fmt.Errorf("ledger: unknown qa verdict %q", e.QA.Verdict)
	case e.CostUSD != nil && *e.CostUSD < 0:
		return errors.New("ledger: cost_usd must not be negative")
	}
	return nil
}

var appendMu sync.Mutex

// Append writes e as one line of root/.factory/ledger/runs.jsonl and refreshes spend.json from the
// whole file. It refuses an entry that fails Validate. A run that ends twice writes two lines;
// Entries keeps the last per run id.
func Append(root string, e Entry) error {
	if e.Schema == "" {
		e.Schema = contract.RunLedgerSchema
	}
	if err := Validate(e); err != nil {
		return err
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	appendMu.Lock()
	defer appendMu.Unlock()
	dir := filepath.Join(root, Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, RunsFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	all, err := Entries(root)
	if err != nil {
		return err
	}
	return writeSpend(dir, Summarize(all))
}

// Entries reads root/.factory/ledger/runs.jsonl. A missing file is an empty ledger. A line that is
// not a valid entry is an error naming its line number, because dx reads the same file. When a
// run id appears twice the later line wins and keeps the first line's place.
func Entries(root string) ([]Entry, error) {
	data, err := os.ReadFile(filepath.Join(root, Dir, RunsFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Entry
	at := map[string]int{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(nil, 1<<20)
	for n := 1; sc.Scan(); n++ {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("ledger: line %d: %w", n, err)
		}
		if err := Validate(e); err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		if i, ok := at[e.RunID]; ok {
			out[i] = e
			continue
		}
		at[e.RunID] = len(out)
		out = append(out, e)
	}
	return out, sc.Err()
}

// Duration is how long the run took, and false while it is open.
func Duration(e Entry) (time.Duration, bool) {
	if e.EndedAt == nil {
		return 0, false
	}
	return e.EndedAt.Sub(e.StartedAt), true
}
