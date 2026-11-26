//go:build ee

// SPDX-License-Identifier: LicenseRef-KaaS-EE

package policy

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/KaaS-team/factory/ee/contract"
)

// RepoFile is where a repo's own layer sits, under the repo root.
var RepoFile = filepath.Join(".factory", contract.PolicyFileName)

// Environment variables that name the layers. The key is a public key, not a secret.
const (
	EnvOrg  = "FACTORY_POLICY_ORG"
	EnvTeam = "FACTORY_POLICY_TEAM"
	EnvKey  = "FACTORY_POLICY_KEY"
)

// Paths names the layer files. Org and Team files are read with Key when it is set; the repo file
// never is, because the repo is what the signature protects the org from.
type Paths struct {
	Org, Team, Repo string
	Key             string // base64 ed25519 public key; empty reads the files unsigned
}

// FromEnv fills empty fields from the environment and the repo file default.
func (p Paths) FromEnv() Paths {
	fill := func(dst *string, env string) {
		if *dst == "" {
			*dst = os.Getenv(env)
		}
	}
	fill(&p.Org, EnvOrg)
	fill(&p.Team, EnvTeam)
	fill(&p.Key, EnvKey)
	if p.Repo == "" {
		if _, err := os.Stat(RepoFile); err == nil {
			p.Repo = RepoFile
		}
	}
	return p
}

func (p Paths) sources() ([]Source, error) {
	var key ed25519.PublicKey
	if p.Key != "" {
		k, err := ParsePublicKey(p.Key)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", contract.ErrPolicyInvalid, err)
		}
		key = k
	}
	return []Source{
		{Layer: contract.LayerOrg, Path: p.Org, Key: key},
		{Layer: contract.LayerTeam, Path: p.Team, Key: key},
		{Layer: contract.LayerRepo, Path: p.Repo},
	}, nil
}

// Resolve loads and compiles the layers. It returns nothing and no error when no layer is named,
// which means no policy.
func Resolve(p Paths) (*Effective, error) {
	if p.Org == "" && p.Team == "" && p.Repo == "" {
		return nil, nil
	}
	src, err := p.sources()
	if err != nil {
		return nil, err
	}
	e, _, err := Load(src...)
	return e, err
}

// Violation is one problem in a policy file, with the reason code it carries.
type Violation struct {
	Code    contract.ReasonCode
	Message string
}

func (v Violation) String() string { return string(v.Code) + ": " + v.Message }

// CheckFile validates the file at path as its own layer. A team or repo file is also compiled
// under the layers above it (from p), so a widening shows up. Without those it is checked alone,
// and a lower layer's widening cannot be seen.
func CheckFile(path string, p Paths) []Violation {
	data, err := os.ReadFile(path)
	if err != nil {
		return []Violation{{contract.ReasonPolicyInvalid, err.Error()}}
	}
	f, err := Decode(data)
	if err != nil {
		return violations(err)
	}
	if err := Validate(f); err != nil {
		return violations(err)
	}
	var above []contract.PolicyFile
	if f.Layer != contract.LayerOrg {
		src, err := p.sources()
		if err != nil {
			return violations(err)
		}
		for _, s := range src {
			if s.Layer == f.Layer {
				break
			}
			if s.Path == "" {
				if s.Layer == contract.LayerOrg {
					return []Violation{{contract.ReasonPolicyInvalid, "a " + string(f.Layer) + " file needs the org file above it: pass --org or set " + EnvOrg}}
				}
				continue
			}
			_, files, err := Load(s)
			if err != nil && len(files) == 0 {
				return violations(err)
			}
			above = append(above, files...)
		}
	}
	if _, err := (Compiler{}).Compile(append(above, f)...); err != nil {
		return violations(err)
	}
	return nil
}

// violations turns a joined error into one Violation per problem, each with the code of the
// sentinel it wraps.
func violations(err error) []Violation {
	code := contract.ReasonPolicyInvalid
	switch {
	case errors.Is(err, contract.ErrPolicyWidening):
		code = contract.ReasonPolicyWidening
	case errors.Is(err, contract.ErrPolicyLayerOrder):
		code = contract.ReasonPolicyLayerOrder
	}
	var out []Violation
	for _, line := range strings.Split(err.Error(), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, string(code)+": "))
		if line != "" {
			out = append(out, Violation{code, line})
		}
	}
	return out
}

// ExplainResult is the answer to `factory policy explain`.
type ExplainResult struct {
	Allow  bool
	Reason contract.ReasonCode
	Layer  contract.Layer // empty when no layer decided
}

// Explain compiles the layers and asks one question. The check is a contract.Check name; an
// empty one means an action.
func Explain(p Paths, check, value string) (ExplainResult, error) {
	e, err := Resolve(p)
	if err != nil {
		return ExplainResult{}, err
	}
	if e == nil {
		return ExplainResult{}, fmt.Errorf("%w: no policy is configured; pass --org or set %s", contract.ErrPolicyInvalid, EnvOrg)
	}
	if check == "" {
		check = string(contract.CheckAction)
	}
	x := e.Explain(contract.PolicyRequest{Check: contract.Check(check), Value: value})
	return ExplainResult{x.Decision.Allow, x.Decision.Reason, x.Layer}, nil
}
