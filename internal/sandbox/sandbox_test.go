package sandbox

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

const goodEnv = `version = 1
image = "ghcr.io/acme/base:1"
setup = ["go mod download"]
caches = ["/root/go/pkg/mod"]
secrets = ["NPM_TOKEN"]
`

func TestParse(t *testing.T) {
	e, err := Parse([]byte(goodEnv))
	if err != nil {
		t.Fatal(err)
	}
	if e.Image != "ghcr.io/acme/base:1" || len(e.Setup) != 1 || e.Secrets[0] != "NPM_TOKEN" {
		t.Fatalf("got %+v", e)
	}
	e2, _ := Parse([]byte(goodEnv + "\n# note\n"))
	if e.Hash == e2.Hash || e.Tag() == e2.Tag() {
		t.Fatal("a changed file must change the hash and the tag")
	}
	again, _ := Parse([]byte(goodEnv))
	if again.Tag() != e.Tag() {
		t.Fatal("the same file must give the same tag")
	}
}

func TestParseRejects(t *testing.T) {
	for name, in := range map[string]string{
		"version":  "version = 2\nimage = \"x\"\n",
		"image":    "version = 1\n",
		"unknown":  "version = 1\nimage = \"x\"\nbogus = 1\n",
		"cache":    "version = 1\nimage = \"x\"\ncaches = [\"rel/path\"]\n",
		"secret":   "version = 1\nimage = \"x\"\nsecrets = [\"lower\"]\n",
		"provider": "version = 1\nimage = \"x\"\nsecrets = [\"ANTHROPIC_API_KEY\"]\n",
		"empty":    "version = 1\nimage = \"x\"\nsetup = [\" \"]\n",
	} {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	if _, ok, err := Load(dir); ok || err != nil {
		t.Fatalf("missing file: ok=%v err=%v", ok, err)
	}
	writeFile(t, filepath.Join(dir, EnvironmentPath), goodEnv)
	if e, ok, err := Load(dir); !ok || err != nil || e.Hash == "" {
		t.Fatalf("Load: %+v %v %v", e, ok, err)
	}
}

func TestRedactorSplitAcrossWrites(t *testing.T) {
	var out bytes.Buffer
	r := NewRedactor(&out, []string{"s3cr3t-value", "", "short"})
	for _, p := range []string{"token=s3cr", "3t-value and sho", "rt\nnext s3cr3t-val", "ue"} {
		if _, err := r.Write([]byte(p)); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Contains(got, "s3cr3t") || strings.Contains(got, "short") {
		t.Fatalf("secret leaked: %q", got)
	}
	if want := "token=[redacted] and [redacted]\nnext [redacted]"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRedactorLongLine(t *testing.T) {
	var out bytes.Buffer
	r := NewRedactor(&out, []string{"SECRETVALUE"})
	line := strings.Repeat("a", flushAfter-5) + "SECRETVALUE" + strings.Repeat("b", 100)
	for i := 0; i < len(line); i += 7 {
		_, _ = r.Write([]byte(line[i:min(i+7, len(line))]))
	}
	_ = r.Close()
	if strings.Contains(out.String(), "SECRETVALUE") || !strings.Contains(out.String(), redacted) {
		t.Fatal("secret on a long line was not redacted")
	}
}

func TestCache(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	c, err := OpenCache(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get("h"); ok {
		t.Fatal("empty cache hit")
	}
	if err := c.Put("h", "img:1"); err != nil {
		t.Fatal(err)
	}
	c2, err := OpenCache(p)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := c2.Get("h"); !ok || v != "img:1" {
		t.Fatalf("reopened cache: %q %v", v, ok)
	}
}
