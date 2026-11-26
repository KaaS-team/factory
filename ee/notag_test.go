// SPDX-License-Identifier: LicenseRef-KaaS-EE

package ee_test

import (
	"os/exec"
	"strings"
	"testing"
)

// untagged are the ee packages that build without the tag, each with its reason.
var untagged = map[string]string{
	"/ee/contract": "the Apache-2.0 contracts the core and other repositories import; types and interfaces, no features",
}

// TestNoEEFilesWithoutTag: a build without -tags ee compiles none of the enterprise packages
// except the untagged ones, and with the tag it compiles all three features.
func TestNoEEFilesWithoutTag(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go is not on PATH")
	}
	list := func(args ...string) string {
		cmd := exec.Command(goBin, append([]string{"list", "-e", "-f", "{{.ImportPath}} {{len .GoFiles}}"}, append(args, "./...")...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go list: %v\n%s", err, out)
		}
		return string(out)
	}
	for _, line := range strings.Split(strings.TrimSpace(list()), "\n") {
		if _, ok := untagged[strings.TrimPrefix(strings.Fields(line)[0], "github.com/KaaS-team/factory")]; ok {
			continue
		}
		if strings.Contains(line, "/ee/") && !strings.HasSuffix(line, " 0") {
			t.Errorf("compiled without the ee tag: %s", line)
		}
	}
	withTag := list("-tags", "ee")
	for _, pkg := range []string{"ee/policy", "ee/scim", "ee/sso"} {
		ok := false
		for _, line := range strings.Split(withTag, "\n") {
			if strings.Contains(line, "/"+pkg+" ") && !strings.HasSuffix(line, " 0") {
				ok = true
			}
		}
		if !ok {
			t.Errorf("%s has no files with -tags ee", pkg)
		}
	}
}
