//go:build ee

// SPDX-License-Identifier: LicenseRef-KaaS-EE

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/ee/authz"
	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/ee/policy"
	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/config"
	"github.com/KaaS-team/factory/internal/mcp"
	"github.com/KaaS-team/factory/internal/policygate"
)

func init() {
	mcp.PolicyRun = func(ctx context.Context, in mcp.PolicyInput) (string, error) {
		return runPolicyMCP(ctx, in)
	}
	rootHooks = append(rootHooks, enforcePolicy)
}

// enforcePolicy loads the layered kaas-policy.toml files when FACTORY_POLICY_ORG or a repo file
// names one, and sets the policy the core checks. A policy that does not load stops the command,
// so a broken file never means no policy.
func enforcePolicy(*cobra.Command) {
	next := newControl
	newControl = func(cfg config.Config) (api.Control, error) {
		e, err := policy.Resolve(policy.Paths{}.FromEnv())
		if err != nil {
			return nil, fmt.Errorf("kaas-policy.toml: %w", err)
		}
		if e != nil {
			policygate.Configure(e)
		}
		return next(cfg)
	}
}

// runPolicy does the work of `policy check` and `policy explain`, for the CLI and the MCP tool.
// violated is true when the file has violations or the action is denied.
func runPolicy(_ context.Context, in mcp.PolicyInput) (out string, violated bool, err error) {
	paths := policy.Paths{Org: in.Org, Team: in.Team, Repo: in.Repo}.FromEnv()
	switch in.Action {
	case "check":
		path := in.Path
		if path == "" {
			path = policy.RepoFile
		}
		vs := policy.CheckFile(path, paths)
		if len(vs) == 0 {
			return "ok " + path + "\n", false, nil
		}
		var b strings.Builder
		for _, v := range vs {
			fmt.Fprintf(&b, "violation %s\n", v)
		}
		return b.String(), true, nil
	case "explain":
		if in.Value == "" {
			return "", false, errors.New("explain needs a value to ask about")
		}
		r, err := policy.Explain(paths, in.Check, in.Value)
		if err != nil {
			return "", false, err
		}
		verdict, layer := "deny", string(r.Layer)
		if r.Allow {
			verdict = "allow"
		}
		if layer == "" {
			layer = "none (no layer set it)"
		}
		return fmt.Sprintf("%s %s\ndecided by layer: %s\n", verdict, r.Reason, layer), !r.Allow, nil
	}
	return "", false, fmt.Errorf("unknown policy action %q: use check or explain", in.Action)
}

// errPolicyMCP is all a remote caller learns about a refusal or failure.
var errPolicyMCP = errors.New("policy: request refused or failed")

// runPolicyMCP serves the MCP tool. The caller must be authenticated and hold the policy admin
// role (contract.ActionPolicyEdit) from the ee Authorizer. Client-supplied layer paths are ignored,
// the file is limited to the repo's .factory directory, and every failure is one generic error, so
// a remote caller cannot read other files or see parser detail.
func runPolicyMCP(ctx context.Context, in mcp.PolicyInput) (string, error) {
	p, ok := mcp.PrincipalFrom(ctx)
	if !ok || !authz.New().Can(ctx, p, contract.ActionPolicyEdit, contract.Resource{Org: p.Org}).Allow {
		return "", errPolicyMCP
	}
	in.Org, in.Team, in.Repo = "", "", ""
	if in.Action == "check" {
		root, err := os.Getwd()
		if err != nil {
			return "", errPolicyMCP
		}
		if in.Path, err = confinePolicyPath(root, in.Path); err != nil {
			return "", errPolicyMCP
		}
	}
	out, _, err := runPolicy(ctx, in)
	if err != nil {
		return "", errPolicyMCP
	}
	if in.Action == "check" {
		// Violation messages quote the file and the parser; only the reason codes go back.
		var codes []string
		for _, l := range strings.Split(out, "\n") {
			if rest, ok := strings.CutPrefix(l, "violation "); ok {
				code, _, _ := strings.Cut(rest, ":")
				codes = append(codes, "violation "+code)
			}
		}
		if len(codes) == 0 {
			return "ok\n", nil
		}
		return strings.Join(codes, "\n") + "\n", nil
	}
	return out, nil
}

// confinePolicyPath returns the file to check under root/.factory. An empty path means the repo
// file. The cleaned path must stay inside .factory, and no part of it may be a symlink.
func confinePolicyPath(root, path string) (string, error) {
	if path == "" {
		path = policy.RepoFile
	}
	if filepath.IsAbs(path) {
		return "", errPolicyMCP
	}
	dir := filepath.Join(root, ".factory")
	full := filepath.Join(root, filepath.Clean(path))
	if rel, err := filepath.Rel(dir, full); err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errPolicyMCP
	}
	cur := root
	for _, part := range strings.Split(strings.TrimPrefix(full, root+string(filepath.Separator)), string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if err != nil || fi.Mode()&os.ModeSymlink != 0 {
			return "", errPolicyMCP
		}
	}
	return full, nil
}

func policyLayerFlags(cmd *cobra.Command, in *mcp.PolicyInput) {
	cmd.Flags().StringVar(&in.Org, "org", "", "org layer file (default $"+policy.EnvOrg+")")
	cmd.Flags().StringVar(&in.Team, "team", "", "team layer file (default $"+policy.EnvTeam+")")
}

func policyCheckCmd() *cobra.Command {
	in := mcp.PolicyInput{Action: "check"}
	cmd := &cobra.Command{
		Use:   "check [path]",
		Short: "Validate a kaas-policy.toml and print its violations",
		Long: "Validate a kaas-policy.toml (default .factory/kaas-policy.toml). A team or repo file is also\n" +
			"checked against the layers above it, so a file that widens the org policy fails.\n" +
			"Exits non-zero when there are violations.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				in.Path = args[0]
			}
			return emitPolicy(cmd, in)
		},
	}
	policyLayerFlags(cmd, &in)
	return cmd
}

func policyExplainCmd() *cobra.Command {
	in := mcp.PolicyInput{Action: "explain"}
	cmd := &cobra.Command{
		Use:   "explain <value>",
		Short: "Show whether policy allows an action, model, harness, MCP server, host or risk class, and which layer decided",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			in.Value = args[0]
			return emitPolicy(cmd, in)
		},
	}
	policyLayerFlags(cmd, &in)
	cmd.Flags().StringVar(&in.Repo, "repo", "", "repo layer file (default .factory/kaas-policy.toml)")
	cmd.Flags().StringVar(&in.Check, "check", "action", "what the value is: "+strings.Join([]string{
		string(contract.CheckAction), string(contract.CheckModel), string(contract.CheckHarness),
		string(contract.CheckMCPServer), string(contract.CheckEgress), string(contract.CheckAutoMerge)}, ", "))
	return cmd
}

func emitPolicy(cmd *cobra.Command, in mcp.PolicyInput) error {
	out, violated, err := runPolicy(cmd.Context(), in)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprint(cmd.OutOrStdout(), out)
	if violated {
		cmd.SilenceUsage = true
		return fmt.Errorf("policy: %s", in.Action+" found a problem")
	}
	return nil
}
