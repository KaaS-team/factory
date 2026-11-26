package mcp_test

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	factorymcp "github.com/KaaS-team/factory/internal/mcp"
)

// The three tools pass their input to the CLI as arguments and text as stdin, and refuse a bad epic or method.
func TestBacklogToolsBuildTheCommandLine(t *testing.T) {
	var gotArgs []string
	var gotIn string
	factorymcp.BacklogRun = func(_ context.Context, stdin string, args ...string) (string, error) {
		gotIn, gotArgs = stdin, args
		return "ok", nil
	}
	t.Cleanup(func() { factorymcp.BacklogRun = nil })
	cs := connect(t, newFake(), factorymcp.Handlers{})
	call := func(name string, args map[string]any) (*sdk.CallToolResult, error) {
		return cs.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: args})
	}
	if _, err := call("spec", map[string]any{"text": "blocks", "approve": true}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(gotArgs, " ") != "spec --from - --approve" || gotIn != "blocks" {
		t.Fatalf("spec: args=%v in=%q", gotArgs, gotIn)
	}
	if _, err := call("decompose", map[string]any{"epic": "12"}); err != nil || strings.Join(gotArgs, " ") != "decompose 12" {
		t.Fatalf("decompose: args=%v err=%v", gotArgs, err)
	}
	if _, err := call("prioritize", map[string]any{"method": "wsjf", "approve": true}); err != nil || strings.Join(gotArgs, " ") != "prioritize --method wsjf --approve" {
		t.Fatalf("prioritize: args=%v err=%v", gotArgs, err)
	}
	for name, in := range map[string]map[string]any{
		"decompose":  {"epic": "--approve"},
		"prioritize": {"method": "moscow"},
	} {
		if r, err := call(name, in); err == nil && (r == nil || !r.IsError) {
			t.Fatalf("%s %v: want a refusal", name, in)
		}
	}
}
