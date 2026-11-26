package main

import (
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/plan"
	"github.com/KaaS-team/factory/internal/tui"
)

func init() {
	register("plan-review", func() *cobra.Command {
		return tui.NewReviewCommand(tui.ReviewDeps{Load: loadPlanReview, Approve: approvePRD})
	}, noTool("an interactive terminal screen; the plan and approve_plan tools do the same review"))
}

// loadPlanReview drafts the epic and tickets from a PRD, as `plan --from-prd` does, and lays them out
// in wave order with the plan's problems.
func loadPlanReview(path string) (tui.PlanReview, error) {
	md, err := os.ReadFile(path)
	if err != nil {
		return tui.PlanReview{}, err
	}
	prd, err := plan.FromPRD(string(md))
	if err != nil {
		return tui.PlanReview{}, err
	}
	var p plan.Plan
	for _, t := range prd.Tickets {
		pt, err := plan.NewTicket(t.Key, t.Title, t.Body)
		if err != nil {
			return tui.PlanReview{}, err
		}
		p.Tickets = append(p.Tickets, pt)
	}
	return planReviewView(prd.Title, p), nil
}

// planReviewView orders the tickets by wave. A plan whose waves cannot be computed keeps its own order
// in one wave; its problems say why, and they block approval.
func planReviewView(epic string, p plan.Plan) tui.PlanReview {
	r := tui.PlanReview{Epic: epic, Problems: p.Problems()}
	waves, err := p.Waves()
	if err != nil || len(r.Problems) > 0 {
		waves = [][]string{nil}
		for _, t := range p.Tickets {
			waves[0] = append(waves[0], t.ID)
		}
	}
	by := map[string]plan.Ticket{}
	for _, t := range p.Tickets {
		by[t.ID] = t
	}
	for i, wave := range waves {
		for _, id := range wave {
			t := by[id]
			r.Tickets = append(r.Tickets, tui.PlanTicket{
				ID: t.ID, Title: t.Title, Wave: i, Files: t.Files, Depends: t.Depends,
				Why:        strings.Join(strings.Fields(sectionText(t.Body, "Why")), " "),
				Acceptance: acceptanceText(t.Body),
			})
		}
	}
	return r
}

func sectionText(b plan.Body, name string) string {
	s, ok := b.Section(name)
	if !ok {
		return ""
	}
	return s.Text()
}

// acceptanceText returns the "AC-n: text" lines of the Acceptance section.
func acceptanceText(b plan.Body) []string {
	var out []string
	for _, l := range strings.Split(sectionText(b, "Acceptance"), "\n") {
		if l = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "-")); strings.HasPrefix(l, "AC-") {
			out = append(out, l)
		}
	}
	return out
}

// approvePRD files the reviewed plan through `factory plan --from-prd <path> --approve`, so approval
// runs the same checks and filing as the plan command.
func approvePRD(cmd *cobra.Command, path string) error {
	c := commands["plan"].build()
	c.SetArgs([]string{"--from-prd", path, "--approve"})
	c.SetOut(cmd.OutOrStdout())
	c.SetErr(cmd.ErrOrStderr())
	return c.ExecuteContext(cmd.Context())
}
