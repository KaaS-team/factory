// Package cidrift compares the staged workflow copies in ci/ with the live ones in .github/workflows.
// Cloud sessions cannot push .github/workflows/**, so they stage the change in ci/ and a person copies it over.
package cidrift

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Kind says how a staged workflow differs from the live one.
type Kind string

const (
	// Differs means both files exist and their bytes differ.
	Differs Kind = "differs"
	// NotLive means the staged file has no live counterpart.
	NotLive Kind = "not live"
)

// Drift is one staged workflow that the live tree does not match.
type Drift struct {
	Name string
	Kind Kind
}

func (d Drift) String() string {
	return fmt.Sprintf("ci/%s %s from .github/workflows/%s", d.Name, d.Kind, d.Name)
}

// Check returns each file in root/ci whose live copy under root/.github/workflows is missing or different,
// sorted by name. A repo with no ci/ directory has no drift. Live files with no staged copy are not drift.
func Check(root string) ([]Drift, error) {
	staged, err := os.ReadDir(filepath.Join(root, "ci"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Drift
	for _, e := range staged {
		n := e.Name()
		if e.IsDir() || (!strings.HasSuffix(n, ".yml") && !strings.HasSuffix(n, ".yaml")) {
			continue
		}
		want, err := os.ReadFile(filepath.Join(root, "ci", n))
		if err != nil {
			return nil, err
		}
		got, err := os.ReadFile(filepath.Join(root, ".github", "workflows", n))
		switch {
		case os.IsNotExist(err):
			out = append(out, Drift{n, NotLive})
		case err != nil:
			return nil, err
		case !bytes.Equal(want, got):
			out = append(out, Drift{n, Differs})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
