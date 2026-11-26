package runstate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoundTripAndAtomicity(t *testing.T) {
	s := Store{Root: t.TempDir()}
	st := State{ID: "r1", Kind: "stack", Epic: "#9"}
	if err := s.Create(st); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(st); err == nil {
		t.Fatal("second Create of the same run must fail")
	}
	st.MarkDone("a")
	st.MarkDone("a")
	if err := st.Put("plan", map[string]int{"n": 3}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(st); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load("r1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Steps) != 1 || !got.Done("a") {
		t.Fatalf("steps = %v", got.Steps)
	}
	var m map[string]int
	if ok, err := got.Get("plan", &m); err != nil || !ok || m["n"] != 3 {
		t.Fatalf("plan = %v %v %v", m, ok, err)
	}
	es, _ := os.ReadDir(filepath.Join(s.Root, "r1"))
	if len(es) != 1 {
		t.Fatalf("a save left stray files: %v", es)
	}
}

func TestMissingAndBadIDs(t *testing.T) {
	s := Store{Root: t.TempDir()}
	if _, err := s.Load("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	for _, id := range []string{"", "..", "../x", "a/b", ".hidden"} {
		if _, err := s.Load(id); err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("id %q should be rejected, got %v", id, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(s.Root, "bad"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(s.Root, "bad", "state.json"), []byte("{"), 0o644)
	if _, err := s.Load("bad"); err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Fatalf("err = %v", err)
	}
}
