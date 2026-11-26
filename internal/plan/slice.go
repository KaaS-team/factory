package plan

import (
	"fmt"
	"slices"
	"strings"
)

// A unit is what one PR delivers: about 30 minutes of agent work. Tickets that form a straight
// dependency chain (each waits only for the one before, and nothing else waits for the ones inside)
// become one unit, an epic with ordered tasks, instead of separate PRs that wait on each other.
const (
	// MaxUnitTasks caps the tasks in one unit; a longer chain is split at a task boundary.
	MaxUnitTasks = 3
	// MaxChain is the longest dependency chain the planner accepts without a warning.
	MaxChain = 2
)

// Unit is one PR's worth of work: one or more tasks run in order.
type Unit struct {
	ID      string
	Title   string
	Tasks   []Ticket
	Depends []string // ids of the units this one waits for
}

// Sliced is a plan cut into units, with the dependency chain before and after.
type Sliced struct {
	Units []Unit
	// Plan holds one ticket per unit; Waves and Markdown work on it as on any plan.
	Plan Plan
	// ChainBefore and ChainAfter are the longest dependency chains, counted in tickets and in units.
	ChainBefore, ChainAfter int
}

// Slice cuts a valid plan into units of at most maxTasks tasks (MaxUnitTasks when it is below 1).
// A chain longer than that is split at a task boundary, never inside a task.
func Slice(p Plan, maxTasks int) (Sliced, error) {
	if maxTasks < 1 {
		maxTasks = MaxUnitTasks
	}
	if pr := p.Problems(); len(pr) > 0 {
		return Sliced{}, fmt.Errorf("cannot slice a plan with problems: %s", strings.Join(pr, "; "))
	}
	by := p.byID()
	dependents := map[string][]string{}
	for _, t := range p.Tickets {
		for _, d := range t.Depends {
			dependents[d] = append(dependents[d], t.ID)
		}
	}
	// next is the one ticket that continues t's chain: its only dependent, which waits for nothing else.
	next := func(id string) (string, bool) {
		if ds := dependents[id]; len(ds) == 1 && len(by[ds[0]].Depends) == 1 {
			return ds[0], true
		}
		return "", false
	}
	inChain := map[string]bool{}
	for _, t := range p.Tickets {
		if n, ok := next(t.ID); ok {
			inChain[n] = true
		}
	}
	unitOf := map[string]string{}
	var units []Unit
	for _, t := range p.Tickets {
		if inChain[t.ID] {
			continue // not a chain head
		}
		var chain []Ticket
		for id, ok := t.ID, true; ok; id, ok = next(id) {
			chain = append(chain, by[id])
		}
		parts := chunkTickets(chain, maxTasks)
		for i, tasks := range parts {
			u := Unit{ID: tasks[0].ID, Title: unitTitle(tasks, i, len(parts)), Tasks: tasks}
			for _, k := range tasks {
				unitOf[k.ID] = u.ID
			}
			if i > 0 {
				u.Depends = append(u.Depends, units[len(units)-1].ID)
			}
			units = append(units, u)
		}
	}
	// A head's outside dependencies point at a chain tail, which now sits in some unit.
	for i := range units {
		head := units[i].Tasks[0]
		if len(units[i].Depends) > 0 {
			continue // a later part waits only for the part before it
		}
		seen := map[string]bool{}
		for _, d := range head.Depends {
			if u := unitOf[d]; !seen[u] {
				seen[u] = true
				units[i].Depends = append(units[i].Depends, u)
			}
		}
	}
	out := Sliced{Units: units, ChainBefore: p.LongestChain()}
	for _, u := range units {
		t, err := u.Ticket()
		if err != nil {
			return Sliced{}, err
		}
		out.Plan.Tickets = append(out.Plan.Tickets, t)
	}
	out.ChainAfter = out.Plan.LongestChain()
	return out, nil
}

func chunkTickets(s []Ticket, n int) [][]Ticket {
	var out [][]Ticket
	for len(s) > n {
		out, s = append(out, s[:n]), s[n:]
	}
	return append(out, s)
}

func unitTitle(tasks []Ticket, part, parts int) string {
	t := tasks[0].Title
	if len(tasks) > 1 {
		more := "tasks"
		if len(tasks) == 2 {
			more = "task"
		}
		t = fmt.Sprintf("%s and %d more %s", t, len(tasks)-1, more)
	}
	if parts > 1 && !strings.Contains(t, " (part ") {
		t = fmt.Sprintf("%s (part %d of %d)", t, part+1, parts)
	}
	return t
}

// Ticket renders the unit as one planned ticket: the tasks' acceptance criteria renumbered in order,
// their files merged, and the tasks listed in the order they run.
func (u Unit) Ticket() (Ticket, error) {
	if len(u.Tasks) == 1 && slices.Equal(u.Depends, u.Tasks[0].Depends) {
		return u.Tasks[0], nil // a lone task keeps its body, so a plan that needs no slicing is unchanged
	}
	var b strings.Builder
	b.WriteString(Marker + "\n")
	if len(u.Depends) == 0 {
		b.WriteString("Deps: none\n")
	} else {
		b.WriteString("Deps: " + strings.Join(u.Depends, ", ") + "\n")
	}
	b.WriteString("## Why\n\n")
	for _, t := range u.Tasks {
		fmt.Fprintf(&b, "%s: %s\n", t.ID, oneLine(t.Body.section("Why")))
	}
	b.WriteString("\n## Tasks\n\nRun in this order, in one branch, as one PR.\n\n")
	for i, t := range u.Tasks {
		fmt.Fprintf(&b, "%d. %s: %s\n", i+1, t.ID, t.Title)
	}
	b.WriteString("\n## Acceptance\n\n")
	n := 0
	for _, t := range u.Tasks {
		for _, l := range t.Body.acceptanceLines() {
			_, text, _ := strings.Cut(l, ":")
			n++
			fmt.Fprintf(&b, "- AC-%d: %s\n", n, strings.TrimSpace(text))
		}
	}
	b.WriteString("\n## Files\n\n")
	seen := map[string]bool{}
	for _, t := range u.Tasks {
		for _, f := range t.Files {
			if !seen[f] {
				seen[f] = true
				fmt.Fprintf(&b, "- `%s`\n", f)
			}
		}
	}
	return NewTicket(u.ID, u.Title, b.String())
}

// LongestChain is the number of tickets on the longest dependency path; 0 for an empty plan.
// It assumes no cycle, which Problems rules out.
func (p Plan) LongestChain() int {
	by := p.byID()
	memo := map[string]int{}
	var depth func(id string) int
	depth = func(id string) int {
		if v, ok := memo[id]; ok {
			return v
		}
		memo[id] = 1 // guards a cycle
		best := 0
		for _, d := range by[id].Depends {
			if _, ok := by[d]; ok {
				best = max(best, depth(d))
			}
		}
		memo[id] = best + 1
		return best + 1
	}
	longest := 0
	for _, t := range p.Tickets {
		longest = max(longest, depth(t.ID))
	}
	return longest
}

// ChainWarning is the planner's warning for a plan whose longest chain is above MaxChain, or "".
func ChainWarning(longest int) string {
	if longest <= MaxChain {
		return ""
	}
	return fmt.Sprintf("the longest dependency chain is %d, above %d: only one unit of that chain can run until the one before it merges", longest, MaxChain)
}
