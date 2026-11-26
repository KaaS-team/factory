package main

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/KaaS-team/factory/internal/mcp"
)

// builtIn are the commands cobra adds itself; they are not registered.
var builtIn = []string{"completion", "help"}

// TestCommandRegistry: the root holds exactly the registered commands, each registered under the
// name its build func returns, each from its own cmd_<name>.go.
func TestCommandRegistry(t *testing.T) {
	var got []string
	for _, c := range newRoot().Commands() {
		if !slices.Contains(builtIn, c.Name()) {
			got = append(got, c.Name())
		}
	}
	if want := commandNames(); !slices.Equal(got, want) {
		t.Errorf("root has %v, registry has %v", got, want)
	}
	for _, n := range commandNames() {
		if b := commands[n].build().Name(); b != n {
			t.Errorf("registered as %q, builds %q", n, b)
		}
		file := "cmd_" + strings.ReplaceAll(n, "-", "_") + ".go"
		src, err := os.ReadFile(file)
		if err != nil {
			t.Errorf("command %s: %v", n, err)
			continue
		}
		if !strings.Contains(string(src), `register("`+n+`"`) {
			t.Errorf("%s does not register %s", file, n)
		}
	}
}

// TestCLIMCPParity: every registered command has an MCP tool, or says with noTool why it has none.
// An exemption for a command that has a tool is stale and fails, so a closed gap drops its entry.
func TestCLIMCPParity(t *testing.T) {
	have := map[string]bool{}
	for _, n := range mcp.ToolNames() {
		have[n] = true
	}
	for _, n := range commandNames() {
		c := commands[n]
		switch {
		case c.noTool != "" && have[c.tool]:
			t.Errorf("%s is exempt (%s) but MCP tool %s exists; drop the noTool", n, c.noTool, c.tool)
		case c.noTool == "" && !have[c.tool]:
			t.Errorf("%s has no MCP tool %s; add internal/mcp/tool_%s.go or register it with noTool(reason)", n, c.tool, c.tool)
		case c.noTool != "" && len(strings.Fields(strings.TrimPrefix(c.noTool, "gap:"))) < 3:
			t.Errorf("%s: noTool reason %q is too short to say why", n, c.noTool)
		}
	}
}
