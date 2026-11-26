// Package runstate keeps one run's state on disk, one directory per run, so a run that
// crashed resumes where it stopped. Every write is atomic: a crash leaves the old state or
// the new one, never half of either.
package runstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
)

// ErrNotFound means no run has that id.
var ErrNotFound = errors.New("runstate: no such run")

var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// State is what a run needs to continue.
type State struct {
	ID    string   `json:"id"`
	Kind  string   `json:"kind"`  // stack or program
	Epic  string   `json:"epic"`  // the ticket the run serves
	Steps []string `json:"steps"` // names of steps already done, in order
	// Heads maps a branch to the commit the run last saw at its tip.
	Heads map[string]string `json:"heads,omitempty"`
	// Data holds the run's own documents, such as the plan, by name.
	Data map[string]json.RawMessage `json:"data,omitempty"`
}

// Done reports whether the step is recorded as done.
func (s State) Done(step string) bool { return slices.Contains(s.Steps, step) }

// MarkDone records a step as done. It is idempotent.
func (s *State) MarkDone(step string) {
	if !s.Done(step) {
		s.Steps = append(s.Steps, step)
	}
}

// Put stores a document under name.
func (s *State) Put(name string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if s.Data == nil {
		s.Data = map[string]json.RawMessage{}
	}
	s.Data[name] = b
	return nil
}

// Get reads the document stored under name into v. It reports false when there is none.
func (s State) Get(name string, v any) (bool, error) {
	b, ok := s.Data[name]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(b, v)
}

// Store is a directory holding one subdirectory per run.
type Store struct{ Root string }

func (s Store) dir(id string) (string, error) {
	if !idRe.MatchString(id) || id == "." || id == ".." {
		return "", fmt.Errorf("runstate: %q is not a valid run id", id)
	}
	return filepath.Join(s.Root, id), nil
}

// Create writes the first state of a new run. It fails when the run exists.
func (s Store) Create(st State) error {
	d, err := s.dir(st.ID)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(d, "state.json")); err == nil {
		return fmt.Errorf("runstate: run %s already exists", st.ID)
	}
	return s.Save(st)
}

// Save replaces the run's state atomically.
func (s Store) Save(st State) error {
	d, err := s.dir(st.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(d, "state-*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(d, "state.json")); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// Load reads a run's state.
func (s Store) Load(id string) (State, error) {
	d, err := s.dir(id)
	if err != nil {
		return State{}, err
	}
	b, err := os.ReadFile(filepath.Join(d, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return State{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return State{}, err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return State{}, fmt.Errorf("runstate: run %s has a damaged state file: %w", id, err)
	}
	return st, nil
}
