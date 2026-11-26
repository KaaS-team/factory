package intake

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Sentry handles Sentry issue alert webhooks. The request must carry Sentry-Hook-Signature, the
// hex HMAC-SHA256 of the body made with the integration's client secret. Tickets dedupe by
// Sentry issue id, so every later alert for the same issue returns the first ticket.
type Sentry struct {
	Secret string
	Store  Store
}

const sentryFrames = 5

type sentryPayload struct {
	Action string `json:"action"`
	Data   struct {
		Event struct {
			IssueID   string `json:"issue_id"`
			Title     string `json:"title"`
			Culprit   string `json:"culprit"`
			WebURL    string `json:"web_url"`
			IssueURL  string `json:"issue_url"`
			Level     string `json:"level"`
			Exception struct {
				Values []struct {
					Type       string `json:"type"`
					Value      string `json:"value"`
					Stacktrace struct {
						Frames []struct {
							Filename string `json:"filename"`
							Function string `json:"function"`
							Lineno   int    `json:"lineno"`
							InApp    bool   `json:"in_app"`
						} `json:"frames"`
					} `json:"stacktrace"`
				} `json:"values"`
			} `json:"exception"`
		} `json:"event"`
	} `json:"data"`
}

// ServeHTTP implements http.Handler.
func (s *Sentry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if s.Secret == "" || !hmacHexEqual(s.Secret, body, "", r.Header.Get("Sentry-Hook-Signature")) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var p sentryPayload
	if json.Unmarshal(body, &p) != nil || p.Data.Event.IssueID == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	e := p.Data.Event
	title := e.Title
	if title == "" {
		title = "Sentry issue " + e.IssueID
	}
	link := e.WebURL
	if link == "" {
		link = e.IssueURL
	}
	c, err := s.Store.Create(r.Context(), NewTicket{
		Source: SourceSentry, Key: e.IssueID, Title: "Fix: " + title, Body: sentryBody(p), URL: link,
	})
	if err != nil {
		http.Error(w, "could not create ticket", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ticket": c.ID, "created": c.New})
}

// sentryBody is a short summary: the error, where it was raised, the innermost in-app frames and
// the links. It is not the whole event.
func sentryBody(p sentryPayload) string {
	e := p.Data.Event
	var b strings.Builder
	fmt.Fprintf(&b, "Sentry issue %s", e.IssueID)
	if e.Level != "" {
		fmt.Fprintf(&b, " (%s)", e.Level)
	}
	b.WriteString("\n\n")
	if e.Culprit != "" {
		fmt.Fprintf(&b, "Culprit: %s\n", e.Culprit)
	}
	for _, v := range e.Exception.Values {
		fmt.Fprintf(&b, "Error: %s: %s\n", v.Type, v.Value)
		frames := v.Stacktrace.Frames
		var app []int
		for i, f := range frames {
			if f.InApp {
				app = append(app, i)
			}
		}
		if len(app) == 0 { // no in-app frames: fall back to every frame
			for i := range frames {
				app = append(app, i)
			}
		}
		if len(app) > sentryFrames {
			app = app[len(app)-sentryFrames:] // Sentry lists the innermost frame last
		}
		for i := len(app) - 1; i >= 0; i-- {
			f := frames[app[i]]
			fmt.Fprintf(&b, "  at %s (%s:%d)\n", f.Function, f.Filename, f.Lineno)
		}
	}
	if e.WebURL != "" {
		fmt.Fprintf(&b, "\nEvent: %s\n", e.WebURL)
	}
	if e.IssueURL != "" {
		fmt.Fprintf(&b, "Issue: %s\n", e.IssueURL)
	}
	return b.String()
}
