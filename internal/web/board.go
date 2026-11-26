package web

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	ui "github.com/KaaS-team/ui"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/plan"
)

// column is one board column: a name and the phases it holds, in board order.
type column struct {
	Key    string
	Title  string
	Phases []api.Phase
	Empty  string
}

// columns is the board's one phase-to-column table. TestEveryPhaseHasOneColumn holds it to
// every api.Phase.
var columns = []column{
	{Key: "ready", Title: "Ready", Phases: []api.Phase{api.PhaseReady}, Empty: "Nothing is waiting to start."},
	{Key: "running", Title: "Running", Phases: []api.Phase{api.PhaseClaimed, api.PhaseRunning, api.PhaseRetryQueue}, Empty: "No run is in progress."},
	{Key: "needs", Title: "Needs you", Phases: []api.Phase{api.PhaseNeedsHuman, api.PhaseStalled}, Empty: "Nothing needs a person."},
	{Key: "queue", Title: "Merge queue", Phases: []api.Phase{api.PhaseDone, api.PhaseQueued}, Empty: "The merge queue is empty."},
	{Key: "done", Title: "Done", Phases: []api.Phase{api.PhaseMerged, api.PhaseCanceled}, Empty: "Nothing has landed yet."},
}

// statusOf is the colour a phase carries on a chip.
func statusOf(p api.Phase) ui.Status {
	switch p {
	case api.PhaseRunning, api.PhaseClaimed:
		return ui.StatusInfo
	case api.PhaseNeedsHuman, api.PhaseStalled:
		return ui.StatusFail
	case api.PhaseRetryQueue, api.PhaseDone, api.PhaseQueued:
		return ui.StatusWait
	case api.PhaseMerged:
		return ui.StatusPass
	default:
		return ""
	}
}

// phaseLabel is a phase in words.
var phaseLabel = map[api.Phase]string{
	api.PhaseReady: "Ready", api.PhaseClaimed: "Starting", api.PhaseRunning: "Running",
	api.PhaseStalled: "Stalled", api.PhaseRetryQueue: "Retrying", api.PhaseDone: "PR open",
	api.PhaseQueued: "In merge queue", api.PhaseMerged: "Merged", api.PhaseNeedsHuman: "Needs you",
	api.PhaseCanceled: "Canceled",
}

// card is one run on the board.
type card struct {
	ID       api.TicketID
	Title    string
	Repo     string
	Chip     ui.Chip
	Wave     int // 1-based
	Attempt  int
	Since    string // last push, or empty
	Sub      string // repo, attempt and last push on one line
	Priority int
	QueuePos int // 1-based place in the merge queue, 0 when not queued
	Usage    api.Usage
}

type boardColumn struct {
	column
	Cards []card
}

type boardView struct {
	Stats   []ui.Stat
	Columns []boardColumn
	Waves   int
	Total   int
	Error   string
}

// waveOf numbers each ticket on the board by plan wave, using plan.Waves. A blocker that is not on
// the board is done and drops out. On a cycle every ticket is wave 1.
func waveOf(b api.Board) map[api.TicketID]int {
	var tickets []api.Ticket
	for _, sl := range b.Slots {
		tickets = append(tickets, sl.Ticket)
	}
	tickets = append(tickets, b.Ready...)
	on := map[api.TicketID]bool{}
	for _, t := range tickets {
		on[t.ID] = true
	}
	var p plan.Plan
	for _, t := range tickets {
		pt := plan.Ticket{ID: string(t.ID), Title: t.Title}
		for _, d := range t.BlockedBy {
			if on[d] {
				pt.Depends = append(pt.Depends, string(d))
			}
		}
		p.Tickets = append(p.Tickets, pt)
	}
	out := map[api.TicketID]int{}
	waves, err := p.Waves()
	if err != nil {
		for _, t := range tickets {
			out[t.ID] = 1
		}
		return out
	}
	for i, w := range waves {
		for _, id := range w {
			out[api.TicketID(id)] = i + 1
		}
	}
	return out
}

// visible keeps the slots and ready tickets p may read.
func (s *server) visible(r *http.Request, b api.Board) api.Board {
	p := principal(r)
	out := api.Board{FiresLeft: b.FiresLeft}
	seen := map[api.TicketID]bool{}
	for _, sl := range b.Slots {
		if s.can(r.Context(), p, contract.ActionRunRead, sl.Ticket.Repo) {
			out.Slots = append(out.Slots, sl)
			seen[sl.Ticket.ID] = true
		}
	}
	for _, t := range b.Ready {
		if s.can(r.Context(), p, contract.ActionRunRead, t.Repo) {
			out.Ready = append(out.Ready, t)
			seen[t.ID] = true
		}
	}
	for _, id := range b.MergeQueue {
		if seen[id] {
			out.MergeQueue = append(out.MergeQueue, id)
		}
	}
	return out
}

// buildBoard lays the board out in columns.
func buildBoard(b api.Board, operator bool) boardView {
	wave := waveOf(b)
	qpos := map[api.TicketID]int{}
	for i, id := range b.MergeQueue {
		qpos[id] = i + 1
	}
	byPhase := map[api.Phase][]card{}
	for _, sl := range b.Slots {
		c := card{
			ID: sl.Ticket.ID, Title: sl.Ticket.Title, Repo: sl.Ticket.Repo, Wave: wave[sl.Ticket.ID],
			Chip: ui.Chip{Label: phaseLabel[sl.Phase], Status: statusOf(sl.Phase)}, Priority: sl.Ticket.Priority,
			QueuePos: qpos[sl.Ticket.ID],
		}
		if sl.Session != nil {
			c.Attempt = sl.Session.Attempt
		}
		if !sl.LastPush.IsZero() {
			c.Since = sl.LastPush.UTC().Format("Jan 2 15:04") + " UTC"
		}
		if operator {
			c.Usage = sl.Usage
		}
		c.Sub = subline(c)
		byPhase[sl.Phase] = append(byPhase[sl.Phase], c)
	}
	for _, t := range b.Ready {
		byPhase[api.PhaseReady] = append(byPhase[api.PhaseReady], card{
			ID: t.ID, Title: t.Title, Repo: t.Repo, Wave: wave[t.ID], Priority: t.Priority,
			Chip: ui.Chip{Label: "P" + strconv.Itoa(t.Priority)}, Sub: t.Repo,
		})
	}
	v := boardView{Total: len(b.Slots) + len(b.Ready)}
	for _, n := range wave {
		v.Waves = max(v.Waves, n)
	}
	for _, col := range columns {
		bc := boardColumn{column: col}
		for _, ph := range col.Phases {
			bc.Cards = append(bc.Cards, byPhase[ph]...)
		}
		if col.Key == "queue" {
			slices.SortStableFunc(bc.Cards, func(a, b card) int { return queueRank(a) - queueRank(b) })
		}
		v.Columns = append(v.Columns, bc)
	}
	count := func(key string) int {
		for _, c := range v.Columns {
			if c.Key == key {
				return len(c.Cards)
			}
		}
		return 0
	}
	needs := ui.Stat{Label: "Needs you", Value: strconv.Itoa(count("needs"))}
	if count("needs") > 0 {
		needs.Status = ui.StatusFail
	}
	v.Stats = []ui.Stat{
		{Label: "Running", Value: strconv.Itoa(count("running"))},
		needs,
		{Label: "Merge queue", Value: strconv.Itoa(count("queue"))},
		{Label: "Ready", Value: strconv.Itoa(count("ready"))},
		{Label: "Starts left this hour", Value: strconv.Itoa(b.FiresLeft)},
	}
	return v
}

// subline joins a card's repo, attempt and last push.
func subline(c card) string {
	var parts []string
	if c.Repo != "" {
		parts = append(parts, c.Repo)
	}
	if c.Attempt > 0 {
		parts = append(parts, "attempt "+strconv.Itoa(c.Attempt))
	}
	if c.Since != "" {
		parts = append(parts, "pushed "+c.Since)
	}
	for i, p := range parts { // a part wraps whole, never "attempt" on one line and "1" on the next
		parts[i] = strings.ReplaceAll(p, " ", "\u00a0")
	}
	return strings.Join(parts, " · ")
}

// queueRank puts queued cards in queue order, then the rest.
func queueRank(c card) int {
	if c.QueuePos == 0 {
		return 1 << 20
	}
	return c.QueuePos
}

func (s *server) board(w http.ResponseWriter, r *http.Request) {
	b, err := s.ctl.Board(r.Context())
	if err != nil {
		s.render(w, r, http.StatusBadGateway, "board", page{Title: "Board", Nav: "board", Data: boardView{Error: "The board could not be loaded. It refreshes on its own when the factory answers."}})
		return
	}
	s.render(w, r, http.StatusOK, "board", page{Title: "Board", Nav: "board", Data: buildBoard(s.visible(r, b), s.opts.Operator)})
}

// legacyTicket keeps old /ticket/<id> links working.
func (s *server) legacyTicket(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/runs/"+r.PathValue("id"), http.StatusMovedPermanently)
}
