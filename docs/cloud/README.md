# Cloud sessions: contracts and seams

Shared ground for the parallel cloud sessions that build enterprise features. Add files; do not
edit shared tables.

## Where the contracts live

- `ee/contract`: Principal, Identity, Authorizer, Policy and PolicyCompiler, Audit, Budget, reason
  codes and sentinel errors. Apache-2.0, no build tag, no `internal/` imports, so the core and
  other repositories import it as `github.com/KaaS-team/factory/ee/contract`. The doc comments
  are the specification.
- `ee/contract/testdata/conformance.json`: language-neutral cases for every interface. An
  implementation conforms when it passes all of them. The Go runner is
  `ee/contract/conformance_test.go`; the reference implementation it runs against is
  `ee/contract/reference_test.go` (test code only, copy it as a starting point).
- `ee/contract/testdata/kaas-policy.example.toml`: every `kaas-policy.toml` key, decoded by
  `contract.PolicyFile`.
- `docs/contracts/run-ledger.v1.schema.json`: the run ledger line. `contract.RunLedgerEntry`
  round-trips it, and a test keeps the two field sets equal.

To change a contract, change the doc comment, the type and conformance.json in one PR. A reason
code never changes meaning; add a new one.

## Register a CLI command

Add `cmd/factory/cmd_<name>.go` (dashes become underscores):

```go
func init() {
	register("widget", widgetCmd)
}
```

`register` panics on a duplicate name. If the command has an MCP tool with a different name, pass
`withTool("tool_name")`. If it has none, pass `noTool("reason")`; start the reason with `gap:` when
a tool is owed. Enterprise commands start the file with `//go:build ee`.

## Register an MCP tool

Add `internal/mcp/tool_<name>.go`:

```go
func init() {
	registerTool("widget", false, func(s *sdk.Server, ctl api.Control, h Handlers) {
		sdk.AddTool(s, &sdk.Tool{Name: "widget", Description: "..."}, handler)
	})
}
```

The second argument marks a write tool, which `--read-only` leaves out. The handler reaches the
core only through `api.Control`, and errors go through `safe` so no operator detail leaks.

## Tests that hold the seams

- `TestCommandRegistry` and `TestOneFilePerTool`: one file per command and per tool, each
  registered under the name it builds.
- `TestCLIMCPParity` (`cmd/factory/registry_test.go`): every command has an MCP tool or a
  `noTool` reason. Adding the tool for an exempt command fails until its `noTool` is removed.
- `TestToolRegistry`: the MCP server lists exactly the registered tools.

## Ownership tonight

| Ticket | Packages |
|---|---|
| E1 | `ee/sso`, `ee/scim`, `ee/identity`, `ee/authz` |
| E2 | `ee/policy` |
| E3 | `ee/audit`, `ee/budget`, `internal/audit`, `internal/budget`, `internal/meter` |
| F1 | e2e tests, status and board |
| F3 | `internal/worker`, `deploy/` |
| F4 | `internal/dispatch/k8s`, `internal/sandbox`, `internal/egress` |
| F5 | `internal/intake`, `internal/tracker/jira` |
| F6 | `internal/triage`, `internal/firstrun` |
| F7 | `internal/manager`, `internal/ciclass` |
| F8 | `internal/attention`, `internal/proof`, `internal/risk` |
| F9 | `internal/stack`, `internal/runstate`, `internal/program` |
| F10 | `internal/mergeq` |
| F11 | `internal/review` |
| F12 | `internal/ledger`, `internal/meter` |
| F13 | `internal/mcp` auth |
| F14 | `internal/dashboard`, `internal/web` |
| F15 | `internal/admin` |
| F16 | `internal/tui` |

`ee/contract`, `cmd/factory/registry.go` and `internal/mcp/registry.go` belong to W0. E3 and F12
both list `internal/meter`, so each adds new files there rather than editing the other's.
