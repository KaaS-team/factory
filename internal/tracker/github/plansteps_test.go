package github_test

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/KaaS-team/factory/internal/api"
	gh "github.com/KaaS-team/factory/internal/tracker/github"
)

func issueJSON(id int, number int, labels ...string) string {
	var ls []string
	for _, l := range labels {
		ls = append(ls, fmt.Sprintf(`{"name":%q}`, l))
	}
	return fmt.Sprintf(`{"id":%d,"number":%d,"title":"t","labels":[%s]}`, id, number, strings.Join(ls, ","))
}

func serveJSON(f *fakeGitHub, route string, status int, body string) {
	f.routes[route] = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

// Decompose on GitHub: the child's database id, not its number, goes to the sub-issues endpoint of the parent.
func TestLinkSubSendsTheChildIDUnderTheParent(t *testing.T) {
	f := newFake(t)
	serveJSON(f, "GET /repos/o/r/issues/12", 200, issueJSON(9012, 12))
	serveJSON(f, "POST /repos/o/r/issues/7/sub_issues", 201, issueJSON(7, 7))
	if err := f.tracker(t).LinkSub(context.Background(), "7", "12"); err != nil {
		t.Fatal(err)
	}
	var posted string
	for _, r := range f.requests() {
		if r.Method == http.MethodPost {
			posted = r.Body
		}
	}
	if !strings.Contains(posted, `"sub_issue_id":9012`) {
		t.Errorf("POST body = %q", posted)
	}
}

func TestLinkSubToleratesAnAlreadyLinkedChildButNotOtherErrors(t *testing.T) {
	f := newFake(t)
	serveJSON(f, "GET /repos/o/r/issues/12", 200, issueJSON(9012, 12))
	serveJSON(f, "POST /repos/o/r/issues/7/sub_issues", 422, `{"message":"Validation Failed: sub issue is already a sub-issue of this issue"}`)
	if err := f.tracker(t).LinkSub(context.Background(), "7", "12"); err != nil {
		t.Errorf("already linked: %v", err)
	}
	serveJSON(f, "POST /repos/o/r/issues/7/sub_issues", 403, `{"message":"forbidden"}`)
	if err := f.tracker(t).LinkSub(context.Background(), "7", "12"); err == nil {
		t.Error("a 403 must surface")
	}
	if err := f.tracker(t).LinkSub(context.Background(), "x", "12"); err == nil {
		t.Error("a bad parent id must be refused")
	}
}

// SetPriority keeps exactly one P<n> label and touches no other label.
func TestSetPriorityReplacesOnlyPriorityLabels(t *testing.T) {
	f := newFake(t)
	serveJSON(f, "GET /repos/o/r/issues/5", 200, issueJSON(1, 5, "bug", "P3", "factory:ready"))
	serveJSON(f, "DELETE /repos/o/r/issues/5/labels/P3", 200, `[]`)
	serveJSON(f, "POST /repos/o/r/issues/5/labels", 200, `[]`)
	if err := f.tracker(t).SetPriority(context.Background(), "5", 1); err != nil {
		t.Fatal(err)
	}
	removed, added := labelChanges(t, f, 5)
	if !reflect.DeepEqual(removed, []string{"P3"}) || !reflect.DeepEqual(added, []string{"P1"}) {
		t.Errorf("removed %v added %v", removed, added)
	}
}

func TestSetPriorityNoChangeWritesNothingAndRefusesNegative(t *testing.T) {
	f := newFake(t)
	serveJSON(f, "GET /repos/o/r/issues/5", 200, issueJSON(1, 5, "P2"))
	if err := f.tracker(t).SetPriority(context.Background(), "5", 2); err != nil {
		t.Fatal(err)
	}
	removed, added := labelChanges(t, f, 5)
	if len(removed)+len(added) != 0 {
		t.Errorf("rewrote an unchanged label: %v %v", removed, added)
	}
	if err := f.tracker(t).SetPriority(context.Background(), "5", -1); err == nil {
		t.Error("a negative priority must be refused")
	}
}

// Pickup reads the label back: P2 is 2, no label is after every ranked issue.
func TestReadTicketPriorityFromLabel(t *testing.T) {
	f := newFake(t)
	serveJSON(f, "GET /repos/o/r/issues", 200, `[`+issueJSON(1, 1, "factory:ready", "P2")+`,`+issueJSON(2, 2, "factory:ready")+`]`)
	got, err := f.tracker(t).Ready(context.Background())
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %v", got, err)
	}
	if got[0].Priority != 2 || got[1].Priority != gh.UnrankedPriority {
		t.Errorf("priorities %d %d", got[0].Priority, got[1].Priority)
	}
	var _ api.Ticket = got[0]
}
