package program

import (
	"strings"
	"testing"
)

func TestScrubEnvDropsProviderKeys(t *testing.T) {
	got := scrubEnv([]string{"PATH=/bin", "ANTHROPIC_API_KEY=x", "openai_api_key=y", "GOOGLE_API_KEY=z", "HOME=/h"}, map[string]string{"X": "1"})
	joined := strings.Join(got, " ")
	for _, bad := range []string{"ANTHROPIC", "openai", "GOOGLE"} {
		if strings.Contains(joined, bad) {
			t.Errorf("%s survived: %v", bad, got)
		}
	}
	if !strings.Contains(joined, "PATH=/bin") || !strings.Contains(joined, "X=1") {
		t.Errorf("lost entries: %v", got)
	}
}

func TestFinalText(t *testing.T) {
	out := []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"draft"}]}}` + "\n" + `{"type":"result","result":"final"}` + "\n")
	if got, ok := FinalText(out); !ok || got != "final" {
		t.Fatalf("got %q %v", got, ok)
	}
	if _, ok := FinalText([]byte("junk")); ok {
		t.Fatal("junk has no text")
	}
}
