package brand_test

import (
	"strings"
	"testing"

	"github.com/KaaS-team/factory/internal/brand"
)

// #210: the footer and the trailer name no model and no dollar figure, however they are configured.
func TestBrandLinesLeakNothing(t *testing.T) {
	forbidden := []string{"claude", "opus", "sonnet", "haiku", "fable", "gpt", "gemini", "token", "$", "usd", "cost", "spent", "sk-ant"}
	lines := []string{brand.DefaultFooter, brand.Trailer, brand.Footer(""), brand.WithTrailer("msg")}
	for _, bad := range []string{
		"Built by kaas.team with claude-opus-5-5",
		"Shipped with Sonnet 5 at kaas.team",
		"kaas.team run cost $3.20",
		"kaas.team spent 4 USD",
		"kaas.team 12,000 tokens",
		"kaas.team\nsecond line",
		"no link here",
	} {
		lines = append(lines, brand.Footer(bad), brand.AppendFooter("body", bad))
	}
	for _, l := range lines {
		low := strings.ToLower(l)
		for _, f := range forbidden {
			if strings.Contains(low, f) {
				t.Errorf("%q carries %q", l, f)
			}
		}
		if !strings.Contains(l, "kaas.team") && l != "msg\n\n"+brand.Trailer && !strings.HasPrefix(l, "msg") {
			t.Errorf("%q lacks the kaas.team link", l)
		}
	}
}

func TestFooterKeepsCleanCustomLine(t *testing.T) {
	const custom = "Delivered by [Acme](https://acme.example) on kaas.team"
	if got := brand.Footer(custom); got != custom {
		t.Fatalf("got %q", got)
	}
	if got := brand.AppendFooter("body\n", ""); got != "body\n\n---\n"+brand.DefaultFooter {
		t.Fatalf("got %q", got)
	}
	once := brand.AppendFooter("body", "")
	if brand.AppendFooter(once, "") != once {
		t.Fatal("footer added twice")
	}
}

func TestWithTrailer(t *testing.T) {
	if got, want := brand.WithTrailer("fix: a"), "fix: a\n\n"+brand.Trailer; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if got, want := brand.WithTrailer("fix: a\n\nSigned-off-by: A <a@b>"), "fix: a\n\nSigned-off-by: A <a@b>\n"+brand.Trailer; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	once := brand.WithTrailer("x")
	if brand.WithTrailer(once) != once {
		t.Fatal("trailer added twice")
	}
}
