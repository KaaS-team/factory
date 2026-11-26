package jira_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/tracker/jira"
)

// Data Center has REST v2 only: a startAt-paged search, text bodies and a Bearer token.
func TestDataCenter(t *testing.T) {
	var paths, auths []string
	var comment map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		auths = append(auths, r.Header.Get("Authorization"))
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		issue := func(k string) map[string]any {
			return map[string]any{"key": k, "fields": map[string]any{"summary": "S", "description": "plain", "status": map[string]any{"name": "Ready"}}}
		}
		switch {
		case r.URL.Path == "/rest/api/2/search":
			start := int(body["startAt"].(float64))
			rows := []any{issue("SHOP-1")}
			if start == 1 {
				rows = []any{issue("SHOP-2")}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"issues": rows, "total": 2})
		case strings.HasSuffix(r.URL.Path, "/comment") && r.Method == "POST":
			comment = body
			w.WriteHeader(201)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	tr, err := jira.New(jira.Config{BaseURL: srv.URL, Project: "SHOP", TokenEnv: "T", ReadyStatus: "Ready", DataCenter: true,
		Getenv: func(string) string { return "pat-token" }})
	if err != nil {
		t.Fatal(err)
	}
	got, err := tr.Ready(context.Background())
	if err != nil || len(got) != 2 || got[1].ID != "SHOP-2" || got[0].Body != "plain" {
		t.Fatalf("ready = %v, %v", got, err)
	}
	if err := tr.Comment(context.Background(), api.TicketID("SHOP-1"), "run passed"); err != nil {
		t.Fatal(err)
	}
	if _, ok := comment["body"].(string); !ok {
		t.Fatalf("comment body must be text on Data Center: %v", comment)
	}
	if paths[2] != "POST /rest/api/2/issue/SHOP-1/comment" || auths[0] != "Bearer pat-token" {
		t.Fatalf("paths %v auths %v", paths, auths)
	}
}
