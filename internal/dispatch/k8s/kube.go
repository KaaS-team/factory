package k8s

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Sandbox CRD of kubernetes-sigs/agent-sandbox (Apache-2.0).
const (
	crdGroupVersion = "agents.x-k8s.io/v1alpha1"
	crdResource     = "sandboxes"
)

// kube is a thin REST client for the few Kubernetes calls a run needs.
type kube struct {
	base, token, ns string
	hc              *http.Client
}

func (k *kube) do(ctx context.Context, method, path string, body any) (map[string]any, int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(k.base, "/")+path, rd)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+k.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := k.hc.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("k8s api: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out, resp.StatusCode, nil
}

func (k *kube) ok(ctx context.Context, method, path string, body any) (map[string]any, error) {
	out, code, err := k.do(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	if code/100 != 2 {
		return nil, fmt.Errorf("k8s api: %s %s: status %d", method, path, code)
	}
	return out, nil
}

// hasCRD reports whether the Sandbox CRD is installed.
func (k *kube) hasCRD(ctx context.Context) (bool, error) {
	_, code, err := k.do(ctx, http.MethodGet, "/apis/"+crdGroupVersion, nil)
	switch {
	case err != nil:
		return false, err
	case code == http.StatusOK:
		return true, nil
	case code == http.StatusNotFound:
		return false, nil
	}
	return false, fmt.Errorf("k8s api: discovery: status %d", code)
}

// collection is the REST path of the sandbox objects: Sandbox CRs, or plain Pods without the CRD.
func (k *kube) collection(crd bool) string {
	if crd {
		return "/apis/" + crdGroupVersion + "/namespaces/" + k.ns + "/" + crdResource
	}
	return "/api/v1/namespaces/" + k.ns + "/pods"
}

func (k *kube) netpols() string {
	return "/apis/networking.k8s.io/v1/namespaces/" + k.ns + "/networkpolicies"
}

func podSpec(image string) map[string]any {
	return map[string]any{
		"restartPolicy": "Never",
		"containers": []any{map[string]any{
			"name": "agent", "image": image,
			"securityContext": map[string]any{"allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []string{"ALL"}}},
		}},
	}
}

func objectBody(crd bool, name, image string, labels map[string]string) map[string]any {
	meta := map[string]any{"name": name, "labels": labels}
	if crd {
		return map[string]any{"apiVersion": crdGroupVersion, "kind": "Sandbox", "metadata": meta,
			"spec": map[string]any{"podTemplate": map[string]any{"metadata": map[string]any{"labels": labels}, "spec": podSpec(image)}}}
	}
	return map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": meta, "spec": podSpec(image)}
}

// netpolBody lets the sandbox reach only the egress proxy: everything else is dropped, so the
// proxy and its allowlist are the only way out.
func netpolBody(name string, labels map[string]string, proxyCIDR string, proxyPort int) map[string]any {
	return map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]any{"name": name},
		"spec": map[string]any{
			"podSelector": map[string]any{"matchLabels": labels},
			"policyTypes": []string{"Egress"},
			"egress": []any{map[string]any{
				"to":    []any{map[string]any{"ipBlock": map[string]any{"cidr": proxyCIDR}}},
				"ports": []any{map[string]any{"protocol": "TCP", "port": proxyPort}},
			}},
		},
	}
}

// ready reads a created object and says whether it can take commands.
func ready(crd bool, obj map[string]any) bool {
	st, _ := obj["status"].(map[string]any)
	if !crd {
		return st["phase"] == "Running"
	}
	conds, _ := st["conditions"].([]any)
	for _, c := range conds {
		m, _ := c.(map[string]any)
		if m["type"] == "Ready" && m["status"] == "True" {
			return true
		}
	}
	return false
}
