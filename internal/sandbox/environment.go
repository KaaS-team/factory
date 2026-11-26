// Package sandbox holds what a per-task sandbox needs that is not Kubernetes: the
// .factory/environment.toml schema, the cache key of a set-up environment, and the redactor that
// keeps secret values out of logs.
package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// EnvironmentPath is where a repo declares its sandbox environment.
const EnvironmentPath = ".factory/environment.toml"

// EnvironmentVersion is the only schema version this package reads.
const EnvironmentVersion = 1

// Environment is .factory/environment.toml.
//
//	version = 1
//	image   = "ghcr.io/acme/base:1.4"
//	setup   = ["go mod download"]
//	caches  = ["/root/go/pkg/mod"]
//	secrets = ["NPM_TOKEN"]
//
// Setup commands run once per file hash and the result is reused. Secrets are names only: the
// values come from the secret store at run start.
type Environment struct {
	Version int      `toml:"version"`
	Image   string   `toml:"image"`   // base image
	Setup   []string `toml:"setup"`   // commands run once, in order, inside the base image
	Caches  []string `toml:"caches"`  // absolute paths kept in the snapshot
	Secrets []string `toml:"secrets"` // names injected as env at run start

	// Hash is the sha256 of the file bytes, set by Parse. It keys the snapshot.
	Hash string `toml:"-"`
}

var secretName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// Parse reads and checks an environment file.
func Parse(data []byte) (Environment, error) {
	var e Environment
	md, err := toml.Decode(string(data), &e)
	if err != nil {
		return Environment{}, fmt.Errorf("environment.toml: %w", err)
	}
	if u := md.Undecoded(); len(u) > 0 {
		return Environment{}, fmt.Errorf("environment.toml: unknown key %q", u[0].String())
	}
	if e.Version != EnvironmentVersion {
		return Environment{}, fmt.Errorf("environment.toml: version must be %d", EnvironmentVersion)
	}
	if strings.TrimSpace(e.Image) == "" {
		return Environment{}, errors.New("environment.toml: image is required")
	}
	for _, c := range e.Setup {
		if strings.TrimSpace(c) == "" {
			return Environment{}, errors.New("environment.toml: a setup command is empty")
		}
	}
	for _, c := range e.Caches {
		if !path.IsAbs(c) {
			return Environment{}, fmt.Errorf("environment.toml: cache %q is not an absolute path", c)
		}
	}
	for _, s := range e.Secrets {
		if !secretName.MatchString(s) || strings.HasPrefix(s, "ANTHROPIC_") {
			return Environment{}, fmt.Errorf("environment.toml: secret name %q is not allowed", s)
		}
	}
	sum := sha256.Sum256(data)
	e.Hash = hex.EncodeToString(sum[:])
	return e, nil
}

// Load reads EnvironmentPath under dir. A repo without the file gets ok false and no error.
func Load(dir string) (e Environment, ok bool, err error) {
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(EnvironmentPath)))
	if errors.Is(err, os.ErrNotExist) {
		return Environment{}, false, nil
	}
	if err != nil {
		return Environment{}, false, err
	}
	e, err = Parse(b)
	return e, err == nil, err
}

// Tag is the image tag of the set-up environment. The same file always gives the same tag.
func (e Environment) Tag() string { return "factory-env-" + e.Hash[:16] }
