package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KaaS-team/factory/internal/dispatch/broker"
)

func TestLoadPoolsAndTokenFile(t *testing.T) {
	dir := t.TempDir()
	tok, _ := broker.NewToken()
	pf := filepath.Join(dir, "pools")
	if err := os.WriteFile(pf, []byte("# pools\ndefault "+broker.HashToken(tok)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := loadPools(pf)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := p.Authenticate(tok); err != nil || got != "default" {
		t.Fatalf("authenticate: %q %v", got, err)
	}
	if err := os.WriteFile(pf, []byte("default "+tok[:10]+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPools(pf); err == nil {
		t.Fatal("a short hash was accepted")
	}
	tf := filepath.Join(dir, "tok")
	if err := os.WriteFile(tf, []byte(tok+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(tf, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readSecretFile(tf); err == nil {
		t.Fatal("a world-readable token file was accepted")
	}
	if err := os.Chmod(tf, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := readSecretFile(tf); err != nil || got != tok {
		t.Fatalf("read: %q %v", got, err)
	}
}

// TestDeployMatchesCLI keeps the image and chart tied to the real command: every flag the chart
// passes exists, workers take no inbound traffic, and the image runs as non-root. When helm is
// installed it also renders the chart.
func TestDeployMatchesCLI(t *testing.T) {
	read := func(p string) string {
		b, err := os.ReadFile(filepath.Join("..", "..", p))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	root := newWorker()
	for sub, flags := range map[string][]string{
		"run":   {"plane", "token-file", "drain", "label", "cap"},
		"plane": {"listen", "pools", "ttl"},
	} {
		c, _, err := root.Find([]string{sub})
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range flags {
			if c.Flags().Lookup(f) == nil {
				t.Errorf("worker %s has no --%s", sub, f)
			}
			tpl := read("deploy/helm/factory/templates/workers.yaml")
			if sub == "plane" {
				tpl = read("deploy/helm/factory/templates/plane.yaml")
			}
			if !strings.Contains(tpl, "--"+f) {
				t.Errorf("chart never passes --%s to worker %s", f, sub)
			}
		}
	}
	if !strings.Contains(read("deploy/helm/factory/templates/networkpolicy.yaml"), "ingress: []") {
		t.Error("worker NetworkPolicy must allow no ingress")
	}
	if df := read("Dockerfile"); !strings.Contains(df, "USER 65532") || !strings.Contains(df, "nonroot") {
		t.Error("image must run as a non-root user")
	}
	if p, err := exec.LookPath("helm"); err == nil {
		if out, err := exec.Command(p, "template", "rel", filepath.Join("..", "..", "deploy", "helm", "factory")).CombinedOutput(); err != nil {
			t.Fatalf("helm template: %v\n%s", err, out)
		}
	}
}
