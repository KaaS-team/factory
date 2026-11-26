//go:build ee

// SPDX-License-Identifier: LicenseRef-KaaS-EE

package admin

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/factory/ee/policy"
)

// layerView is one policy layer on the policy page.
type layerView struct {
	Layer    string
	Path     string
	Text     string
	Signed   bool // signed layers are changed in their config repo, never here
	Summary  [][2]string
	Problems []policy.Violation
	ReadErr  bool
}

type policyData struct {
	Layers  []layerView
	CanEdit bool
	None    bool
}

// layerPath returns the file of layer l, and whether a signing key protects it.
func (c *Console) layerPath(l contract.Layer) (path string, signed bool) {
	p := c.cfg.Policy
	switch l {
	case contract.LayerOrg:
		return p.Org, p.Key != ""
	case contract.LayerTeam:
		return p.Team, p.Key != ""
	case contract.LayerRepo:
		return p.Repo, false
	}
	return "", false
}

var layers = []contract.Layer{contract.LayerOrg, contract.LayerTeam, contract.LayerRepo}

func (c *Console) policyLayers(edited contract.Layer, text string, problems []policy.Violation) []layerView {
	var out []layerView
	for _, l := range layers {
		path, signed := c.layerPath(l)
		if path == "" {
			continue
		}
		v := layerView{Layer: string(l), Path: path, Signed: signed}
		if l == edited {
			v.Text, v.Problems = text, problems
		} else if data, err := os.ReadFile(path); err != nil {
			v.ReadErr = true
		} else {
			v.Text = string(data)
		}
		if f, err := policy.Decode([]byte(v.Text)); err == nil {
			v.Summary = summary(f)
		}
		out = append(out, v)
	}
	return out
}

// summary lists what a layer sets. "inherits" means the layer leaves the list to the one above.
func summary(f contract.PolicyFile) [][2]string {
	list := func(set bool, vs []string) string {
		switch {
		case !set:
			return "inherits"
		case len(vs) == 0:
			return "none allowed"
		}
		return strings.Join(vs, ", ")
	}
	str := func(vs any) []string {
		var out []string
		switch v := vs.(type) {
		case []contract.RiskClass:
			for _, x := range v {
				out = append(out, string(x))
			}
		case []contract.Action:
			for _, x := range v {
				out = append(out, string(x))
			}
		}
		return out
	}
	return [][2]string{
		{"Models", list(f.Models.Allowed != nil, f.Models.Allowed)},
		{"Harnesses", list(f.Harness.Allowed != nil, f.Harness.Allowed)},
		{"MCP servers", list(f.MCP.AllowedServers != nil, f.MCP.AllowedServers)},
		{"Egress hosts", list(f.Egress.AllowedHosts != nil, f.Egress.AllowedHosts)},
		{"Auto-merge risk", list(f.Merge.AutoMergeRisk != nil, str(f.Merge.AutoMergeRisk))},
		{"Human approval", list(f.Approval.RequireHuman != nil, str(f.Approval.RequireHuman))},
	}
}

func (c *Console) canEdit(r *http.Request, p contract.Principal, a contract.Action) bool {
	return c.cfg.Authorizer.Can(r.Context(), p, a, contract.Resource{Org: c.cfg.Org}).Allow
}

func (c *Console) policyView(w http.ResponseWriter, r *http.Request, p contract.Principal) {
	d := policyData{Layers: c.policyLayers("", "", nil), CanEdit: c.canEdit(r, p, contract.ActionPolicyEdit)}
	d.None = len(d.Layers) == 0
	c.page.render(w, http.StatusOK, "policy", "Policy", notice(r), d)
}

func (c *Console) policySave(w http.ResponseWriter, r *http.Request, p contract.Principal) {
	l := contract.Layer(r.PostFormValue("layer"))
	text := strings.ReplaceAll(r.PostFormValue("text"), "\r\n", "\n")
	path, signed := c.layerPath(l)
	switch {
	case path == "":
		c.page.error(w, http.StatusNotFound, "No such policy layer", "Only the configured org, team and repo files can be edited.")
		return
	case signed:
		c.page.error(w, http.StatusConflict, "This layer is signed", "Change it in its config repo and sign it there. The console never holds the signing key.")
		return
	}
	if problems := c.save(r, p, contract.ActionPolicyEdit, l, path, []byte(text), map[string]string{"layer": string(l)}); problems != nil {
		d := policyData{Layers: c.policyLayers(l, text, problems), CanEdit: true}
		c.page.render(w, http.StatusUnprocessableEntity, "policy", "Policy", "", d)
		return
	}
	done(w, r, "/admin/policy", "policy-saved")
}

// save runs the `factory policy check` validation on data as layer l, plus a compile of every
// configured layer with data in place, so a narrowed org file cannot leave a team file widening.
// Only a clean result is written, atomically over path; a refused or failed save is recorded
// and returns its problems.
func (c *Console) save(r *http.Request, p contract.Principal, a contract.Action, l contract.Layer, path string, data []byte, ev map[string]string) []policy.Violation {
	tmp, err := tempNear(path, data)
	if err != nil {
		return c.refused(r, p, a, ev, []policy.Violation{{Code: contract.ReasonPolicyInvalid, Message: "the file could not be staged for checking"}})
	}
	defer func() { _ = os.Remove(tmp) }()
	problems := policy.CheckFile(tmp, c.cfg.Policy)
	if problems == nil {
		paths := c.cfg.Policy
		switch l {
		case contract.LayerOrg:
			paths.Org = tmp
		case contract.LayerTeam:
			paths.Team = tmp
		case contract.LayerRepo:
			paths.Repo = tmp
		}
		if _, err := policy.Resolve(paths); err != nil {
			problems = []policy.Violation{{Code: codeOf(err), Message: err.Error()}}
		}
	}
	for i := range problems {
		problems[i].Message = strings.ReplaceAll(problems[i].Message, tmp, filepath.Base(path))
	}
	if problems != nil {
		return c.refused(r, p, a, ev, problems)
	}
	if err := c.change(r.Context(), p, a, ev, func() error { return os.Rename(tmp, path) }); err != nil {
		return []policy.Violation{{Code: contract.ReasonPolicyInvalid, Message: "the checked file could not be saved; nothing changed"}}
	}
	return nil
}

// refused records a save the check turned down, with the first problem's code.
func (c *Console) refused(r *http.Request, p contract.Principal, a contract.Action, ev map[string]string, problems []policy.Violation) []policy.Violation {
	_ = c.record(r.Context(), p, a, contract.OutcomeDeny, problems[0].Code, ev)
	return problems
}

// codeOf is the reason code of a policy error, from the sentinel it wraps.
func codeOf(err error) contract.ReasonCode {
	switch {
	case errors.Is(err, contract.ErrPolicyWidening):
		return contract.ReasonPolicyWidening
	case errors.Is(err, contract.ErrPolicyLayerOrder):
		return contract.ReasonPolicyLayerOrder
	}
	return contract.ReasonPolicyInvalid
}

// tempNear writes data to a new file beside path, so the final rename stays on one filesystem.
func tempNear(path string, data []byte) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(path), ".kaas-policy-*.toml")
	if err != nil {
		return "", err
	}
	_, werr := f.Write(data)
	if err := errors.Join(werr, f.Close()); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// writeAtomic replaces path with data through a rename.
func writeAtomic(path string, data []byte) error {
	tmp, err := tempNear(path, data)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
