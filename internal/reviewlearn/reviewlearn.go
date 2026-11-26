// Package reviewlearn turns review findings that humans dismissed as wrong into rules. Dismissals
// are recorded in .factory/review-rules.yml. When the same kind of finding is dismissed often
// enough, the file gains a rule that suppresses it, or rewords it when the dismissals agree on a
// better wording. Each rule carries a confidence that grows with every further dismissal.
package reviewlearn

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultPath is where the rules live, relative to the repository root.
const DefaultPath = ".factory/review-rules.yml"

// DefaultThreshold is how many dismissals of one kind make a rule.
const DefaultThreshold = 3

// MinConfidence is the confidence a rule needs before it applies.
const MinConfidence = 0.5

// Actions a rule can take.
const (
	Suppress = "suppress"
	Reword   = "reword"
)

// Dismissal is one finding a human called wrong.
type Dismissal struct {
	Kind   string `yaml:"kind"`
	File   string `yaml:"file,omitempty"`
	Reason string `yaml:"reason,omitempty"`
	// Reword is the wording the human wanted instead. Empty means the finding should not exist.
	Reword string `yaml:"reword,omitempty"`
}

// Rule changes how findings of one kind are shown.
type Rule struct {
	Kind       string  `yaml:"kind"`
	Action     string  `yaml:"action"`
	Message    string  `yaml:"message,omitempty"` // the replacement text for Reword
	Confidence float64 `yaml:"confidence"`
	Dismissals int     `yaml:"dismissals"`
}

// File is the content of review-rules.yml. Rules can be edited by hand; distil only rewrites the
// rules it owns, which are the ones whose kind has dismissals.
type File struct {
	Rules      []Rule      `yaml:"rules"`
	Dismissals []Dismissal `yaml:"dismissals,omitempty"`
}

// Learner records dismissals and keeps the rules current.
type Learner struct {
	Path      string // the rules file; DefaultPath under Root when empty
	Root      string
	Threshold int // DefaultThreshold when zero
}

func (l Learner) path() string {
	if l.Path != "" {
		return l.Path
	}
	return filepath.Join(l.Root, DefaultPath)
}

// Load reads the file. A missing file is empty, not an error.
func (l Learner) Load() (File, error) {
	var f File
	b, err := os.ReadFile(l.path())
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := yaml.Unmarshal(b, &f); err != nil {
		return f, fmt.Errorf("review rules: %w", err)
	}
	return f, nil
}

// Record stores a dismissal, redistils the rules and writes the file.
func (l Learner) Record(d Dismissal) error {
	d.Kind = normKind(d.Kind)
	if d.Kind == "" {
		return errors.New("review rules: dismissal needs a kind")
	}
	f, err := l.Load()
	if err != nil {
		return err
	}
	f.Dismissals = append(f.Dismissals, d)
	f.Rules = distil(f, l.Threshold)
	b, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.path()), 0o755); err != nil {
		return err
	}
	return os.WriteFile(l.path(), b, 0o644)
}

// Rules returns the rules that are confident enough to apply.
func (l Learner) Rules() (Rules, error) {
	f, err := l.Load()
	if err != nil {
		return nil, err
	}
	r := Rules{}
	for _, x := range f.Rules {
		if x.Confidence >= MinConfidence {
			r[normKind(x.Kind)] = x
		}
	}
	return r, nil
}

// Rules maps a finding kind to its rule.
type Rules map[string]Rule

// Apply returns the rule for kind, if one applies.
func (r Rules) Apply(kind string) (Rule, bool) {
	x, ok := r[normKind(kind)]
	return x, ok
}

// distil rebuilds the rules from the dismissals. Hand-written rules for kinds without dismissals
// are kept as they are.
func distil(f File, threshold int) []Rule {
	if threshold <= 0 {
		threshold = DefaultThreshold
	}
	byKind := map[string][]Dismissal{}
	for _, d := range f.Dismissals {
		byKind[d.Kind] = append(byKind[d.Kind], d)
	}
	var out []Rule
	for _, r := range f.Rules {
		if _, learned := byKind[normKind(r.Kind)]; !learned {
			out = append(out, r)
		}
	}
	kinds := make([]string, 0, len(byKind))
	for k := range byKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		ds := byKind[k]
		if len(ds) < threshold {
			continue
		}
		r := Rule{Kind: k, Action: Suppress, Dismissals: len(ds), Confidence: float64(len(ds)) / float64(len(ds)+1)}
		if w := agreedWording(ds); w != "" {
			r.Action, r.Message = Reword, w
		}
		out = append(out, r)
	}
	return out
}

// agreedWording returns the reword text every dismissal shares, or "" when they differ.
func agreedWording(ds []Dismissal) string {
	w := strings.TrimSpace(ds[0].Reword)
	for _, d := range ds {
		if strings.TrimSpace(d.Reword) != w {
			return ""
		}
	}
	return w
}

func normKind(k string) string { return strings.ToLower(strings.TrimSpace(k)) }
