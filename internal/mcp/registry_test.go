package mcp_test

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	factorymcp "github.com/KaaS-team/factory/internal/mcp"
)

// TestToolRegistry: the server lists exactly the registered tools, so a registration whose add
// func names a different tool, or adds none, fails here.
func TestToolRegistry(t *testing.T) {
	cs := connect(t, newFake(), factorymcp.Handlers{})
	var listed []string
	for tool, err := range cs.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		listed = append(listed, tool.Name)
	}
	sort.Strings(listed)
	if want := factorymcp.ToolNames(); !slices.Equal(listed, want) {
		t.Errorf("server lists %v, registry has %v", listed, want)
	}
	reg := slices.Clone(toolNames)
	sort.Strings(reg)
	if !slices.Equal(reg, factorymcp.ToolNames()) {
		t.Errorf("toolNames in this test is %v, registry has %v", reg, factorymcp.ToolNames())
	}
}

// TestOneFilePerTool: each tool registers from its own tool_<name>.go, so parallel work adds a
// file and never edits a shared table.
func TestOneFilePerTool(t *testing.T) {
	for _, n := range factorymcp.ToolNames() {
		src, err := os.ReadFile(filepath.Join(".", "tool_"+n+".go"))
		if err != nil {
			t.Errorf("tool %s: %v", n, err)
			continue
		}
		if !strings.Contains(string(src), `registerTool("`+n+`"`) {
			t.Errorf("tool_%s.go does not register %s", n, n)
		}
	}
}
