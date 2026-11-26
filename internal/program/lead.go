package program

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// AskFunc sends a prompt to the model client and returns its reply text.
type AskFunc func(ctx context.Context, prompt string) (string, error)

// ModelLead is a Lead that asks a model through ask. It never touches provider credentials.
type ModelLead struct{ Ask AskFunc }

const leadPrompt = `You are the tech lead of a program of work. You own the epic below.
Workers have finished some tickets. Read their results and decide how the rest of the plan changes.

Epic:
%s

Results so far (JSON):
%s

Tasks still to run (JSON):
%s

Reply with one JSON object and nothing else:
{"add":[{"id":"...","title":"...","depends":["..."]}],"drop":["task id not yet run"],"note":"one short sentence"}
Use empty lists when the plan stands. Add a task only when a result shows work that is missing.`

// Review asks the model and parses its decision. A reply that is not one JSON object is an error.
func (m ModelLead) Review(ctx context.Context, epic string, results []Result, remaining []Task) (Decision, error) {
	if m.Ask == nil {
		return Decision{}, errors.New("program: no model client")
	}
	rb, _ := json.Marshal(results)
	tb, _ := json.Marshal(remaining)
	text, err := m.Ask(ctx, fmt.Sprintf(leadPrompt, epic, rb, tb))
	if err != nil {
		return Decision{}, err
	}
	return ParseDecision(text)
}

// ParseDecision reads the lead's reply. It accepts the object alone or inside a code fence.
func ParseDecision(text string) (Decision, error) {
	t := strings.TrimSpace(text)
	if i, j := strings.Index(t, "{"), strings.LastIndex(t, "}"); i >= 0 && j > i {
		t = t[i : j+1]
	}
	var d Decision
	dec := json.NewDecoder(strings.NewReader(t))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return Decision{}, fmt.Errorf("program: the lead's reply is not a decision: %w", err)
	}
	return d, nil
}
