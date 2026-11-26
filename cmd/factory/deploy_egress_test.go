package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestChartWorkerEgressExcludesInternal: the default public egress must carve RFC 1918, link-local
// and the cluster CIDRs out of 0.0.0.0/0, and the template must render those carve-outs.
func TestChartWorkerEgressExcludesInternal(t *testing.T) {
	dir := filepath.Join("..", "..", "deploy", "helm", "factory")
	raw, err := os.ReadFile(filepath.Join(dir, "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		NetworkPolicy struct {
			PublicEgress struct {
				Enabled     bool     `yaml:"enabled"`
				Except      []string `yaml:"except"`
				PodCIDR     *string  `yaml:"podCIDR"`
				ServiceCIDR *string  `yaml:"serviceCIDR"`
			} `yaml:"publicEgress"`
			WorkerEgress []map[string]any `yaml:"workerEgress"`
		} `yaml:"networkPolicy"`
	}
	if err := yaml.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	pe := v.NetworkPolicy.PublicEgress
	if pe.PodCIDR == nil || pe.ServiceCIDR == nil {
		t.Error("podCIDR and serviceCIDR must be values")
	}
	for _, want := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16"} {
		found := false
		for _, e := range pe.Except {
			found = found || e == want
		}
		if !found {
			t.Errorf("publicEgress.except lacks %s", want)
		}
	}
	for _, r := range v.NetworkPolicy.WorkerEgress {
		if strings.Contains(strings.ReplaceAll(toStr(r), " ", ""), "0.0.0.0/0") {
			t.Errorf("workerEgress default must not hold an open 0.0.0.0/0 rule: %v", r)
		}
	}
	tpl, err := os.ReadFile(filepath.Join(dir, "templates", "networkpolicy.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{".Values.networkPolicy.publicEgress", "except:", ".podCIDR", ".serviceCIDR"} {
		if !strings.Contains(string(tpl), want) {
			t.Errorf("networkpolicy.yaml does not render %s", want)
		}
	}
}

func toStr(m map[string]any) string {
	b, _ := yaml.Marshal(m)
	return string(b)
}
