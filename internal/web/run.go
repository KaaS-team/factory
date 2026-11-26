package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	ui "github.com/KaaS-team/ui"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/redact"
)

// kindText is how each core event kind reads on a timeline. A kind not listed reads as its name.
var kindText = map[string]string{
	"dispatched": "Session started", "steered": "Message sent to the run", "created": "Ticket filed",
	"handed_off": "Handed off", "followup": "Follow-up queued", "canceled": "Canceled", "retried": "New attempt",
	"stalled": "Stalled, nudged to continue", "pushed": "Pushed", "gate": "Gate ran", "queued": "Entered the merge queue",
	"merged": "Merged", "needs_human": "Needs a person", "stage": "Moved to the next step", "warning": "Warning",
	"budget": "Budget check",
}

// kindStatus colours timeline and log entries.
var eventStatus = map[string]ui.Status{
	"merged": ui.StatusPass, "queued": ui.StatusPass, "pushed": ui.StatusInfo, "dispatched": ui.StatusInfo,
	"needs_human": ui.StatusFail, "canceled": ui.StatusFail, "stalled": ui.StatusWait, "retried": ui.StatusWait,
	"warning": ui.StatusWait, "budget": ui.StatusWait,
}

// customerKeys are the event data keys a customer may see. Anything else (session ids, hosts,
// usage, verdict detail) shows only to an operator.
var customerKeys = []string{"attempt", "branch", "pr", "node", "outcome", "message", "reason", "to", "title", "manual"}

// hidden reports whether an event kind carries usage, budget or cost; it shows only to an operator.
func hidden(kind string) bool {
	k := strings.ToLower(kind)
	return strings.Contains(k, "usage") || strings.Contains(k, "budget") || strings.Contains(k, "cost")
}

// scrub returns the event as a viewer may see it, and false when they may not see it at all.
func scrub(e api.Event, operator bool) (api.Event, bool) {
	if operator {
		return e, true
	}
	if hidden(e.Kind) {
		return e, false
	}
	var d map[string]any
	for _, k := range customerKeys {
		if v, ok := e.Data[k]; ok {
			if d == nil {
				d = map[string]any{}
			}
			if s, isStr := v.(string); isStr {
				v = redact.Strict(s)
			}
			d[k] = v
		}
	}
	e.Data = d
	return e, true
}

// detail is an event's data as one short line, keys sorted.
func detail(e api.Event) string {
	keys := make([]string, 0, len(e.Data))
	for k := range e.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, e.Data[k]))
	}
	return strings.Join(parts, " ")
}

// logLine is one event as a log line: the kind and its data.
func logLine(e api.Event) ui.LogLine {
	return ui.LogLine{Time: e.Time.UTC().Format("15:04:05"), Status: eventStatus[e.Kind], Text: strings.TrimSpace(e.Kind + " " + detail(e))}
}

// timelineEvent is one event as a timeline entry.
func timelineEvent(e api.Event) ui.TimelineEvent {
	t := kindText[e.Kind]
	if t == "" {
		t = e.Kind
	}
	body := ""
	for _, k := range []string{"message", "reason", "branch", "pr", "node"} {
		if v, ok := e.Data[k]; ok {
			body = fmt.Sprintf("%v", v)
			if k == "pr" {
				body = fmt.Sprintf("PR #%v", v)
			}
			break
		}
	}
	return ui.TimelineEvent{Time: e.Time.UTC().Format(time.DateTime), Title: t, Body: body, Status: eventStatus[e.Kind]}
}

type runView struct {
	Slot        api.Slot
	Chip        ui.Chip
	Wave        int
	Ready       bool // a ready ticket with no session yet
	Timeline    ui.Timeline
	Log         ui.Log
	HasHistory  bool
	HistoryErr  string
	CanRetry    bool
	CanCancel   bool
	CanSteer    bool
	Finished    bool // merged or canceled: no actions apply
	Steer       ui.Form
	Done        string
	ActionsNote string
}

// find returns the slot for id, or a slot built from the ready list, and whether either exists.
func find(b api.Board, id api.TicketID) (api.Slot, bool, bool) {
	for _, sl := range b.Slots {
		if sl.Ticket.ID == id {
			return sl, true, false
		}
	}
	for _, t := range b.Ready {
		if t.ID == id {
			return api.Slot{Ticket: t, Phase: api.PhaseReady}, true, true
		}
	}
	return api.Slot{}, false, false
}

// lookup finds the run p may read. A run p may not read is reported as missing (404), never 403.
func (s *server) lookup(w http.ResponseWriter, r *http.Request) (api.Slot, bool, bool) {
	b, err := s.ctl.Board(r.Context())
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, "The board could not be loaded", "The factory did not answer. Reload to try again.")
		return api.Slot{}, false, false
	}
	sl, ok, ready := find(b, api.TicketID(r.PathValue("id")))
	if !ok || !s.can(r.Context(), principal(r), contract.ActionRunRead, sl.Ticket.Repo) {
		s.fail(w, r, http.StatusNotFound, "Run not found", "It finished long ago, or you do not have access to it.")
		return api.Slot{}, false, false
	}
	return sl, true, ready
}

func (s *server) run(w http.ResponseWriter, r *http.Request) {
	sl, ok, ready := s.lookup(w, r)
	if !ok {
		return
	}
	p := principal(r)
	repo := sl.Ticket.Repo
	finished := sl.Phase == api.PhaseMerged || sl.Phase == api.PhaseCanceled
	v := runView{
		Slot: sl, Ready: ready, Finished: finished,
		Chip:      ui.Chip{Label: phaseLabel[sl.Phase], Status: statusOf(sl.Phase)},
		Done:      doneNote(r.URL.Query().Get("done")),
		CanRetry:  !finished && s.can(r.Context(), p, contract.ActionRunRetry, repo),
		CanCancel: !finished && !ready && s.can(r.Context(), p, contract.ActionRunCancel, repo),
		CanSteer:  !finished && !ready && sl.Session != nil && s.can(r.Context(), p, contract.ActionRunSteer, repo),
		Timeline:  ui.Timeline{Label: "Run timeline", Empty: "Nothing has happened on this run yet."},
		Log:       ui.Log{Title: "Events", Live: !finished, Empty: "Waiting for the first event."},
	}
	if b, err := s.ctl.Board(r.Context()); err == nil {
		v.Wave = waveOf(b)[sl.Ticket.ID]
	}
	if !s.opts.Operator {
		v.Slot.Usage, v.Slot.Budget = api.Usage{}, api.Budget{}
		if v.Slot.Session != nil {
			sess := *v.Slot.Session
			sess.Dispatcher, sess.URL = "", ""
			v.Slot.Session = &sess
		}
	}
	if !v.CanRetry && !v.CanCancel && !v.CanSteer && !finished {
		v.ActionsNote = "Your role can watch this run but not change it."
		if s.opts.Identity == nil && s.opts.Authorizer == nil {
			v.ActionsNote = "This dashboard is read-only. Start it with --allow-actions to change runs."
		}
	}
	v.Steer = ui.Form{
		ID: "steer", Action: "/runs/" + string(sl.Ticket.ID) + "/steer", CSRF: csrfToken(w, r),
		Fields: []ui.Field{{Name: "message", Label: "Message to the run", Type: "textarea", Required: true,
			Placeholder: "Tell the agent what to change", Help: "The agent reads it on its next step."}},
		Submit: ui.Button{Label: "Send", Type: "submit", Variant: "primary"},
	}
	code := http.StatusOK
	if h, ok := s.ctl.(api.Historian); ok {
		evs, err := h.History(r.Context(), sl.Ticket.ID)
		if err != nil {
			v.HistoryErr = "The run history could not be loaded. New events still appear below."
		}
		v.HasHistory = err == nil
		for _, e := range evs {
			e, ok := scrub(e, s.opts.Operator)
			if !ok {
				continue
			}
			v.Timeline.Events = append(v.Timeline.Events, timelineEvent(e))
			v.Log.Lines = append(v.Log.Lines, logLine(e))
		}
	}
	s.render(w, r, code, "run", page{Title: string(sl.Ticket.ID) + " " + sl.Ticket.Title, Nav: "board", Data: v})
}

// act runs one write on a run: retry, cancel or steer. It checks the action, calls the Control and
// sends the person back to the run page.
func (s *server) act(a contract.Action) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sl, ok, _ := s.lookup(w, r)
		if !ok {
			return
		}
		if !s.can(r.Context(), principal(r), a, sl.Ticket.Repo) {
			s.fail(w, r, http.StatusForbidden, "Not allowed", "Your role cannot do this. Ask an admin.")
			return
		}
		id := sl.Ticket.ID
		var err error
		done := ""
		switch a {
		case contract.ActionRunRetry:
			err, done = s.ctl.Retry(r.Context(), id), "retry"
		case contract.ActionRunCancel:
			err, done = s.ctl.Cancel(r.Context(), id), "cancel"
		case contract.ActionRunSteer:
			msg := strings.TrimSpace(r.PostFormValue("message"))
			if msg == "" {
				s.fail(w, r, http.StatusBadRequest, "The message is empty", "Write what the agent should do, then send it.")
				return
			}
			err, done = s.ctl.Steer(r.Context(), id, msg), "steer"
		}
		if err != nil {
			s.fail(w, r, http.StatusBadGateway, "Not done", "The factory did not accept it. Try again.")
			return
		}
		http.Redirect(w, r, "/runs/"+string(id)+"?done="+done, http.StatusSeeOther)
	}
}

// runEvents streams one run's events, scrubbed for the viewer.
func (s *server) runEvents(w http.ResponseWriter, r *http.Request) {
	sl, ok, _ := s.lookup(w, r)
	if !ok {
		return
	}
	s.stream(w, r, func(e api.Event) bool { return e.Ticket == sl.Ticket.ID })
}

// events streams every event the viewer may read, for the board and inbox to refresh on.
func (s *server) events(w http.ResponseWriter, r *http.Request) {
	repos := s.repoOf(r)
	p := principal(r)
	s.stream(w, r, func(e api.Event) bool {
		if e.Ticket == "" {
			return true
		}
		repo, known := repos[e.Ticket]
		if !known {
			repos = s.repoOf(r)
			repo = repos[e.Ticket]
		}
		return s.can(r.Context(), p, contract.ActionRunRead, repo)
	})
}

// stream writes Server-Sent Events. Each event is JSON with a rendered timeline entry and log line,
// so the page script only inserts text. It ends when the client goes or the Control closes the channel.
func (s *server) stream(w http.ResponseWriter, r *http.Request, keep func(api.Event) bool) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	ch, err := s.ctl.Events(r.Context())
	if err != nil {
		http.Error(w, "events unavailable", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = fmt.Fprint(w, ": ok\n\n")
	fl.Flush()
	beat := time.NewTicker(25 * time.Second)
	defer beat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-beat.C:
			_, _ = fmt.Fprint(w, ": keepalive\n\n")
			fl.Flush()
		case e, open := <-ch:
			if !open {
				return
			}
			e, ok := scrub(e, s.opts.Operator)
			if !ok || !keep(e) {
				continue
			}
			line, err := json.Marshal(liveEvent{Ticket: e.Ticket, Kind: e.Kind, Timeline: timelineEvent(e), Log: logLine(e)})
			if err != nil {
				continue
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", line)
			fl.Flush()
		}
	}
}

// liveEvent is one SSE message.
type liveEvent struct {
	Ticket   api.TicketID     `json:"ticket,omitempty"`
	Kind     string           `json:"kind"`
	Timeline ui.TimelineEvent `json:"timeline"`
	Log      ui.LogLine       `json:"log"`
}
