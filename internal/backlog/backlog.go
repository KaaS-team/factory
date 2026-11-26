// Package backlog turns a goal into issues (spec), splits an epic into sub-issues (decompose) and
// ranks the ready issues by RICE or WSJF (prioritize). It is pure over the Port; the tracker adapter
// and the CLI and MCP commands live elsewhere.
package backlog

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/KaaS-team/factory/internal/api"
)

// Port is what the three steps need from a tracker. The GitHub Tracker implements it.
type Port interface {
	// Find returns the id of an open issue with exactly this title.
	Find(ctx context.Context, title string) (api.TicketID, bool, error)
	// Create files one issue and returns its id.
	Create(ctx context.Context, t api.Ticket) (api.TicketID, error)
	Get(ctx context.Context, id api.TicketID) (api.Ticket, error)
	Ready(ctx context.Context) ([]api.Ticket, error)
	// LinkSub makes child a sub-issue of parent.
	LinkSub(ctx context.Context, parent, child api.TicketID) error
	// SetRank stores the pickup rank of an issue; rank 1 runs first.
	SetRank(ctx context.Context, id api.TicketID, rank int) error
}

// Draft is one issue to file.
type Draft struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// Filed is the outcome of filing one draft.
type Filed struct {
	ID      api.TicketID `json:"id"`
	Title   string       `json:"title"`
	Existed bool         `json:"existed"`
}

// Sections are the headings every spec issue body carries, in order.
var Sections = []string{"Problem", "Acceptance", "Out of scope"}

// ErrNoDrafter is returned when a goal is given and no model step is wired.
var ErrNoDrafter = errors.New("drafting issues from a goal needs the agent runner, which is not wired yet; pass --from <file>")

// Drafter is the model step: it turns a goal into text holding "--- issue: <title>" blocks.
type Drafter func(ctx context.Context, goal string) (string, error)

const issueMark = "--- issue:"

// Parse splits text into drafts. Each opens with a line "--- issue: <title>" and must hold the
// Problem, Acceptance and Out of scope sections, each with content.
func Parse(text string) ([]Draft, error) {
	var out []Draft
	var problems []string
	blocks := strings.Split("\n"+text, "\n"+issueMark)[1:]
	for _, blk := range blocks {
		head, body, _ := strings.Cut(blk, "\n")
		d := Draft{Title: strings.TrimSpace(head), Body: strings.TrimSpace(body) + "\n"}
		problems = append(problems, check(d)...)
		out = append(out, d)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no %q blocks found", issueMark+" <title>")
	}
	if len(problems) > 0 {
		return nil, errors.New(strings.Join(problems, "; "))
	}
	return out, nil
}

// check lists what is wrong with one draft.
func check(d Draft) []string {
	var p []string
	if d.Title == "" {
		p = append(p, "an issue has no title")
	}
	sec := sections(d.Body)
	for _, s := range Sections {
		if strings.TrimSpace(sec[strings.ToLower(s)]) == "" {
			p = append(p, fmt.Sprintf("%q: section %q is missing or empty", d.Title, s))
		}
	}
	return p
}

// sections maps a lowercased "## Heading" to the text under it.
func sections(body string) map[string]string {
	out := map[string]string{}
	cur := ""
	for _, l := range strings.Split(body, "\n") {
		if h, ok := strings.CutPrefix(l, "## "); ok {
			cur = strings.ToLower(strings.TrimSpace(h))
			continue
		}
		if cur != "" {
			out[cur] += l + "\n"
		}
	}
	return out
}

// Spec drafts issues from a goal. With file false nothing is written and Filed is nil.
func Spec(ctx context.Context, p Port, draft Drafter, goal string, file bool) ([]Draft, []Filed, error) {
	if draft == nil {
		return nil, nil, ErrNoDrafter
	}
	text, err := draft(ctx, goal)
	if err != nil {
		return nil, nil, err
	}
	return FromText(ctx, p, text, file)
}

// FromText parses text and, with file set, files each draft.
func FromText(ctx context.Context, p Port, text string, file bool) ([]Draft, []Filed, error) {
	ds, err := Parse(text)
	if err != nil {
		return nil, nil, err
	}
	if !file {
		return ds, nil, nil
	}
	fl, err := fileAll(ctx, p, ds, "")
	return ds, fl, err
}

// Decompose files the child issues in text under epic and links each as a sub-issue. A title the
// tracker already has is reused and linked again, so a rerun adds nothing.
func Decompose(ctx context.Context, p Port, epic api.TicketID, text string, file bool) ([]Draft, []Filed, error) {
	ds, err := Parse(text)
	if err != nil {
		return nil, nil, err
	}
	if !file {
		return ds, nil, nil
	}
	if _, err := p.Get(ctx, epic); err != nil {
		return ds, nil, err
	}
	fl, err := fileAll(ctx, p, ds, epic)
	return ds, fl, err
}

func fileAll(ctx context.Context, p Port, ds []Draft, epic api.TicketID) ([]Filed, error) {
	var out []Filed
	for _, d := range ds {
		id, ok, err := p.Find(ctx, d.Title)
		if err != nil {
			return out, err
		}
		if !ok {
			body := d.Body
			if epic != "" {
				body += "\nPart of #" + string(epic) + "\n"
			}
			if id, err = p.Create(ctx, api.Ticket{Title: d.Title, Body: body}); err != nil {
				return out, fmt.Errorf("filing %q: %w", d.Title, err)
			}
		}
		if epic != "" {
			if err := p.LinkSub(ctx, epic, id); err != nil {
				return out, fmt.Errorf("linking #%s under #%s: %w", id, epic, err)
			}
		}
		out = append(out, Filed{ID: id, Title: d.Title, Existed: ok})
	}
	return out, nil
}

// Method is a ranking method.
type Method string

// The ranking methods.
const (
	RICE Method = "rice"
	WSJF Method = "wsjf"
)

// ParseMethod reads a configured method name.
func ParseMethod(s string) (Method, error) {
	switch m := Method(strings.ToLower(strings.TrimSpace(s))); m {
	case RICE, WSJF:
		return m, nil
	}
	return "", fmt.Errorf("prioritize method %q: want rice or wsjf", s)
}

// Ranked is one issue with its score and rank. Scored is false when the body lacks a field.
type Ranked struct {
	ID     api.TicketID `json:"id"`
	Title  string       `json:"title"`
	Score  float64      `json:"score"`
	Scored bool         `json:"scored"`
	Rank   int          `json:"rank"`
}

// Score reads the method's fields from "Name: number" lines in body. RICE is
// reach*impact*confidence/effort; WSJF is (value+urgency+risk)/size.
func Score(m Method, body string) (float64, bool) {
	f := fields(body)
	get := func(names ...string) (v []float64, ok bool) {
		for _, n := range names {
			x, found := f[n]
			if !found {
				return nil, false
			}
			v = append(v, x)
		}
		return v, true
	}
	switch m {
	case RICE:
		v, ok := get("reach", "impact", "confidence", "effort")
		if !ok || v[3] <= 0 {
			return 0, false
		}
		return v[0] * v[1] * v[2] / v[3], true
	case WSJF:
		v, ok := get("value", "urgency", "risk", "size")
		if !ok || v[3] <= 0 {
			return 0, false
		}
		return (v[0] + v[1] + v[2]) / v[3], true
	}
	return 0, false
}

func fields(body string) map[string]float64 {
	out := map[string]float64{}
	for _, l := range strings.Split(body, "\n") {
		k, v, ok := strings.Cut(strings.TrimLeft(l, "-* \t"), ":")
		if !ok {
			continue
		}
		if x, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && x >= 0 {
			out[strings.ToLower(strings.TrimSpace(k))] = x
		}
	}
	return out
}

// Prioritize ranks the ready issues by score, highest first, with unscored issues last in the
// order Rank already uses (oldest first). With apply set it stores ranks 1..n on the tracker, which
// is what pickup reads.
func Prioritize(ctx context.Context, p Port, m Method, apply bool) ([]Ranked, error) {
	ready, err := p.Ready(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Ranked, len(ready))
	for i, t := range ready {
		s, ok := Score(m, t.Body)
		out[i] = Ranked{ID: t.ID, Title: t.Title, Score: s, Scored: ok}
	}
	created := map[api.TicketID]int{}
	for i, t := range ready {
		created[t.ID] = i
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Scored != b.Scored {
			return a.Scored
		}
		if a.Scored && a.Score != b.Score {
			return a.Score > b.Score
		}
		return created[a.ID] < created[b.ID]
	})
	for i := range out {
		out[i].Rank = i + 1
		if apply {
			if err := p.SetRank(ctx, out[i].ID, out[i].Rank); err != nil {
				return out, fmt.Errorf("ranking #%s: %w", out[i].ID, err)
			}
		}
	}
	return out, nil
}
