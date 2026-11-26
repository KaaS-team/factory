package main

import (
	"os/exec"
	"strings"
	"testing"
)

// TestEEMountedOnlyWithTag: the default build lists no ee symbols or commands; the ee build lists them.
func TestEEMountedOnlyWithTag(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go is not on PATH")
	}
	deps := func(tags string) string {
		out, err := exec.Command(goBin, "list", "-tags", tags, "-deps", "-f", "{{.ImportPath}}", ".").CombinedOutput()
		if err != nil {
			t.Fatalf("go list: %v\n%s", err, out)
		}
		return string(out)
	}
	// ee/contract is the Apache-2.0 contract package the core may import; any other ee import is a leak.
	for _, line := range strings.Fields(deps("")) {
		if strings.Contains(line, "/ee/") && !strings.HasSuffix(line, "/ee/contract") {
			t.Errorf("default build imports %s", line)
		}
	}
	for _, p := range []string{"/ee/policy", "/ee/scim"} {
		if !strings.Contains(deps("ee"), p) {
			t.Errorf("-tags ee does not import %s", p)
		}
	}
}
