package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// command is one top-level CLI command. Each lives in cmd_<name>.go and adds itself from init(), so
// a new command is a new file and never an edit to a shared table.
type command struct {
	name  string
	build func() *cobra.Command
	// tool is the MCP tool that does the same job; empty means the name with "-" turned into "_".
	tool string
	// noTool, when set, says why the command has no MCP tool. The parity test reads it.
	noTool string
}

// commandOpt sets an optional field of a registration.
type commandOpt func(*command)

// withTool names the MCP tool that matches the command when its name differs.
func withTool(name string) commandOpt { return func(c *command) { c.tool = name } }

// noTool exempts the command from the CLI and MCP parity test. A reason starting "gap:" is work
// still owed; any other reason says why the command should never be a tool.
func noTool(reason string) commandOpt { return func(c *command) { c.noTool = reason } }

var (
	commands = map[string]command{}
	// rootHooks run on every new root after the commands are added. The ee build appends the
	// policy check that wraps the Control.
	rootHooks []func(*cobra.Command)
)

// register adds a top-level command. build runs once per newRoot, so it must return a fresh
// command. A second registration of a name panics at start-up.
func register(name string, build func() *cobra.Command, opts ...commandOpt) {
	if _, dup := commands[name]; dup {
		panic(fmt.Sprintf("factory: command %q registered twice", name))
	}
	c := command{name: name, build: build}
	for _, o := range opts {
		o(&c)
	}
	if c.tool == "" {
		c.tool = strings.ReplaceAll(name, "-", "_")
	}
	commands[name] = c
}

// commandNames returns the registered names in sorted order.
func commandNames() []string {
	names := make([]string, 0, len(commands))
	for n := range commands {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// addRegistered builds every registered command onto root, in name order.
func addRegistered(root *cobra.Command) {
	for _, n := range commandNames() {
		root.AddCommand(commands[n].build())
	}
	for _, h := range rootHooks {
		h(root)
	}
}
