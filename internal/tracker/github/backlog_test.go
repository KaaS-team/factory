package github_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/KaaS-team/factory/internal/api"
)

func issueJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

// Pickup follows the rank: the ticket a rank label sits on carries it as Priority.
func TestRankLabelBecomesPriority(t *testing.T) {
	f := newFake(t)
	f.routes["GET /repos/o/r/issues/5"] = func(w http.ResponseWriter, _ *http.Request) {
		issueJSON(w, `{"number":5,"title":"x","labels":[{"name":"factory:ready"},{"name":"factory:rank:3"}]}`)
	}
	f.routes["GET /repos/o/r/issues/6"] = func(w http.ResponseWriter, _ *http.Request) {
		issueJSON(w, `{"number":6,"title":"y","labels":[{"name":"factory:rank:zero"},{"name":"factory:rank:-2"}]}`)
	}
	tr := f.tracker(t)
	if got, err := tr.Get(context.Background(), "5"); err != nil || got.Priority != 3 {
		t.Fatalf("priority = %d err=%v", got.Priority, err)
	}
	if got, _ := tr.Get(context.Background(), "6"); got.Priority != 0 {
		t.Fatalf("malformed rank labels gave priority %d", got.Priority)
	}
}

func TestSetRankSwapsOnlyTheRankLabel(t *testing.T) {
	f := newFake(t)
	var removed, added []string
	f.routes["GET /repos/o/r/issues/5"] = func(w http.ResponseWriter, _ *http.Request) {
		issueJSON(w, `{"number":5,"labels":[{"name":"bug"},{"name":"factory:ready"},{"name":"factory:rank:9"}]}`)
	}
	f.routes["DELETE /repos/o/r/issues/5/labels/factory:rank:9"] = func(w http.ResponseWriter, _ *http.Request) {
		removed = append(removed, "factory:rank:9")
		issueJSON(w, `[]`)
	}
	f.routes["POST /repos/o/r/issues/5/labels"] = func(w http.ResponseWriter, r *http.Request) {
		added = append(added, r.URL.Path)
		issueJSON(w, `[]`)
	}
	tr := f.tracker(t)
	if err := tr.SetRank(context.Background(), "5", 2); err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || len(added) != 1 {
		t.Fatalf("removed=%v added=%v", removed, added)
	}
	for _, r := range f.requests() {
		if r.Method == "POST" && !strings.Contains(r.Body, "factory:rank:2") {
			t.Fatalf("added body = %s", r.Body)
		}
	}
	if err := tr.SetRank(context.Background(), "5", 0); err == nil {
		t.Fatal("want an error for rank 0")
	}
}

// GitHub links a sub-issue by the child's database id, not its number.
func TestLinkSubSendsTheChildDatabaseID(t *testing.T) {
	f := newFake(t)
	f.routes["GET /repos/o/r/issues/8"] = func(w http.ResponseWriter, _ *http.Request) {
		issueJSON(w, `{"id":424242,"number":8,"title":"child"}`)
	}
	f.routes["POST /repos/o/r/issues/2/sub_issues"] = func(w http.ResponseWriter, _ *http.Request) {
		issueJSON(w, `{"id":424242,"number":8}`)
	}
	if err := f.tracker(t).LinkSub(context.Background(), api.TicketID("2"), api.TicketID("8")); err != nil {
		t.Fatal(err)
	}
	var body string
	for _, r := range f.requests() {
		if r.Method == "POST" {
			body = r.Body
		}
	}
	if !strings.Contains(body, `"sub_issue_id":424242`) {
		t.Fatalf("body = %q", body)
	}
	if err := f.tracker(t).LinkSub(context.Background(), "x", "8"); err == nil {
		t.Fatal("want an error for a bad parent id")
	}
}
