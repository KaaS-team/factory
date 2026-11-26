package mcp_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/attention"
	factorymcp "github.com/KaaS-team/factory/internal/mcp"
)

type attnControl struct {
	api.Control
	items []attention.Item
	err   error
}

func (a attnControl) Open(context.Context) ([]attention.Item, error) { return a.items, a.err }

func TestAttentionTool(t *testing.T) {
	want := attention.Item{ID: "a1", Kind: attention.MergeApproval, Ticket: "T-1", Why: "needs approval"}
	cs := connect(t, attnControl{Control: newFake(), items: []attention.Item{want}}, factorymcp.Handlers{})
	r, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "attention"})
	if err != nil || r.IsError {
		t.Fatalf("err=%v r=%+v", err, r)
	}
	if got := text(t, r); !contains(got, "a1") || !contains(got, "needs approval") {
		t.Fatalf("output = %s", got)
	}

	cs = connect(t, attnControl{Control: newFake(), err: errors.New("dial tcp 10.0.0.5 /home/op/x.go:42")}, factorymcp.Handlers{})
	r, err = cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "attention"})
	if err != nil || !r.IsError {
		t.Fatalf("want tool error, err=%v r=%+v", err, r)
	}
	if m := operatorDetail.FindString(text(t, r)); m != "" {
		t.Fatalf("leaks %q", m)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
