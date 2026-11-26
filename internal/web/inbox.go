package web

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	ui "github.com/KaaS-team/ui"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/attention"
)

// resolver is the attention write side. The Control offers it when it also offers attention.Lister.
type resolver interface {
	Resolve(id string, a attention.Action, by string) error
}

// actionFor is the contract action each attention action needs. Approve, reject and dismiss are a
// person's decision; retry starts another attempt.
var actionFor = map[attention.Action]contract.Action{
	attention.Approve: contract.ActionApprove,
	attention.Reject:  contract.ActionApprove,
	attention.Dismiss: contract.ActionApprove,
	attention.Retry:   contract.ActionRunRetry,
}

// kindTitle names each attention kind for the inbox.
var kindTitle = map[attention.Kind]string{
	attention.PlanApproval:       "Plan to approve",
	attention.ExternalFailure:    "Failure the factory cannot fix",
	attention.BudgetStop:         "Stopped at its budget",
	attention.PolicyRefusal:      "Refused by policy",
	attention.ReviewDisagreement: "Reviewers disagree",
	attention.MergeApproval:      "Merge to approve",
	attention.MissingProof:       "Missing proof of work",
}

// kindStatus is the colour of each kind: a stop is a failure, a decision is a wait.
func kindStatus(k attention.Kind) ui.Status {
	switch k {
	case attention.ExternalFailure, attention.BudgetStop, attention.PolicyRefusal, attention.MissingProof:
		return ui.StatusFail
	default:
		return ui.StatusWait
	}
}

// actionLabel is the button text for each action.
var actionLabel = map[attention.Action]string{
	attention.Approve: "Approve", attention.Reject: "Reject", attention.Retry: "Retry", attention.Dismiss: "Dismiss",
}

type inboxItem struct {
	ID      string
	Ticket  string
	Created string
	Card    ui.Attention
}

type inboxView struct {
	Items       []inboxItem
	Unavailable bool   // the Control keeps no attention list
	Error       string // the list could not be read
	Done        string // what the last action did, shown once
}

// openItems returns the items p may see, and whether the Control keeps a list at all.
func (s *server) openItems(r *http.Request) (items []attention.Item, kept bool, err error) {
	l, ok := s.ctl.(attention.Lister)
	if !ok {
		return nil, false, nil
	}
	all, err := l.Open(r.Context())
	if err != nil {
		return nil, true, err
	}
	repos := s.repoOf(r)
	p := principal(r)
	for _, it := range all {
		if s.can(r.Context(), p, contract.ActionRunRead, repos[api.TicketID(it.Ticket)]) {
			items = append(items, it)
		}
	}
	return items, true, nil
}

// repoOf maps each ticket on the board to its repo. A ticket off the board maps to "", which the
// Authorizer checks at org scope.
func (s *server) repoOf(r *http.Request) map[api.TicketID]string {
	out := map[api.TicketID]string{}
	b, err := s.ctl.Board(r.Context())
	if err != nil {
		return out
	}
	for _, sl := range b.Slots {
		out[sl.Ticket.ID] = sl.Ticket.Repo
	}
	for _, t := range b.Ready {
		out[t.ID] = t.Repo
	}
	return out
}

// openCount is the nav badge: open items the caller may see. It is 0 when the list is missing or fails.
func (s *server) openCount(r *http.Request) int {
	if _, ok := r.Context().Value(principalKey{}).(contract.Principal); !ok {
		return 0
	}
	items, _, err := s.openItems(r)
	if err != nil {
		return 0
	}
	return len(items)
}

func (s *server) inbox(w http.ResponseWriter, r *http.Request) {
	items, kept, err := s.openItems(r)
	v := inboxView{Unavailable: !kept, Done: doneNote(r.URL.Query().Get("done"))}
	code := http.StatusOK
	if err != nil {
		v.Error = "The attention list could not be loaded. Reload to try again."
		code = http.StatusBadGateway
	}
	p := principal(r)
	repos := s.repoOf(r)
	for _, it := range items {
		c := ui.Attention{Status: kindStatus(it.Kind), Title: kindTitle[it.Kind] + " · " + it.Ticket, Body: it.Why}
		if c.Title == " · "+it.Ticket {
			c.Title = strings.ReplaceAll(string(it.Kind), "_", " ") + " · " + it.Ticket
		}
		for _, a := range it.Actions {
			b := ui.Button{Label: actionLabel[a], Type: "submit", Name: "action", Value: string(a)}
			if a == attention.Approve {
				b.Variant = "primary"
			}
			if !s.can(r.Context(), p, actionFor[a], repos[api.TicketID(it.Ticket)]) || !resolvable(s.ctl) {
				b.Disabled = true
			}
			c.Actions = append(c.Actions, b)
		}
		v.Items = append(v.Items, inboxItem{ID: it.ID, Ticket: it.Ticket, Created: it.Created.UTC().Format(time.DateTime), Card: c})
	}
	s.render(w, r, code, "inbox", page{Title: "Attention", Nav: "attention", Data: v})
}

func resolvable(ctl api.Control) bool {
	_, ok := ctl.(resolver)
	return ok
}

// resolve closes an attention item. It answers 404 for an item the caller may not see, so a
// guess does not learn that the item exists.
func (s *server) resolve(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("item")
	a := attention.Action(r.PostFormValue("action"))
	items, _, err := s.openItems(r)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, "The attention list could not be loaded", "Reload the page and try again.")
		return
	}
	i := slices.IndexFunc(items, func(it attention.Item) bool { return it.ID == id })
	if i < 0 {
		s.fail(w, r, http.StatusNotFound, "Not found", "That item is closed or does not exist.")
		return
	}
	need, known := actionFor[a]
	if !known || !slices.Contains(items[i].Actions, a) {
		s.fail(w, r, http.StatusBadRequest, "Not an action for this item", "Pick one of the buttons on the item.")
		return
	}
	if !s.can(r.Context(), principal(r), need, s.repoOf(r)[api.TicketID(items[i].Ticket)]) {
		s.fail(w, r, http.StatusForbidden, "Not allowed", "Your role cannot do this. Ask an admin.")
		return
	}
	res, ok := s.ctl.(resolver)
	if !ok {
		s.fail(w, r, http.StatusNotImplemented, "Actions are off", "This factory does not accept decisions from the web app.")
		return
	}
	if err := res.Resolve(id, a, principal(r).ID); err != nil {
		code, msg := http.StatusBadGateway, "The factory did not accept it. Try again."
		if errors.Is(err, attention.ErrNotFound) {
			code, msg = http.StatusNotFound, "That item is closed or does not exist."
		}
		s.fail(w, r, code, "Not done", msg)
		return
	}
	http.Redirect(w, r, "/attention?done="+string(a), http.StatusSeeOther)
}

// doneNote is the one-line confirmation after an action redirect. Unknown values show nothing.
func doneNote(v string) string {
	return map[string]string{
		"approve": "Approved.", "reject": "Rejected.", "dismiss": "Dismissed.", "retry": "Retry queued.",
		"cancel": "Cancel sent.", "steer": "Message sent to the run.",
	}[v]
}
