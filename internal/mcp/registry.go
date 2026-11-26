package mcp

import (
	"fmt"
	"sort"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/KaaS-team/factory/internal/api"
)

// tool is one MCP tool. Each lives in tool_<name>.go and adds itself from init(), so a new tool is
// a new file and never an edit to a shared table.
type tool struct {
	name string
	// write marks a tool that changes the factory or files work; read-only mode leaves it out.
	write bool
	add   func(s *sdk.Server, ctl api.Control, h Handlers)
}

var tools = map[string]tool{}

// registerTool adds a tool. add must call sdk.AddTool with exactly this name; TestToolRegistry
// checks that. A second registration of a name panics at start-up.
func registerTool(name string, write bool, add func(s *sdk.Server, ctl api.Control, h Handlers)) {
	if _, dup := tools[name]; dup {
		panic(fmt.Sprintf("mcp: tool %q registered twice", name))
	}
	tools[name] = tool{name: name, write: write, add: add}
}

// ToolNames returns the name of every registered tool, sorted. The CLI parity test reads it.
func ToolNames() []string {
	names := make([]string, 0, len(tools))
	for n := range tools {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// addRegisteredTools adds every registered tool in name order, skipping write tools when
// h.ReadOnly is set, so a call to one is refused as unknown.
func addRegisteredTools(s *sdk.Server, ctl api.Control, h Handlers) {
	for _, n := range ToolNames() {
		if t := tools[n]; !h.ReadOnly || !t.write {
			t.add(s, ctl, h)
		}
	}
}
