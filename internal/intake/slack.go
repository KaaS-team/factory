package intake

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	maxBody       = 1 << 20
	slackMaxSkew  = 5 * time.Minute
	slackTitleMax = 120
)

// Replier posts a message in a Slack thread.
type Replier interface {
	Reply(ctx context.Context, channel, threadTS, text string) error
}

// Slack handles Slack Events API calls: an app_mention creates a ticket and the bot answers in the
// thread. Every request must carry a valid v0 signature made with Secret.
type Slack struct {
	Secret  string // signing secret, never logged
	Store   Store
	Replier Replier // nil means no reply
	Now     func() time.Time
}

// VerifySlack checks Slack's v0 signature: HMAC-SHA256 over "v0:<timestamp>:<body>", sent in
// X-Slack-Signature, with a timestamp no more than five minutes from now (replay guard).
func VerifySlack(secret string, h http.Header, body []byte, now time.Time) bool {
	ts, err := strconv.ParseInt(h.Get("X-Slack-Request-Timestamp"), 10, 64)
	if err != nil || secret == "" {
		return false
	}
	if d := now.Sub(time.Unix(ts, 0)); d > slackMaxSkew || d < -slackMaxSkew {
		return false
	}
	return hmacHexEqual(secret, []byte(fmt.Sprintf("v0:%d:%s", ts, body)), "v0=", h.Get("X-Slack-Signature"))
}

func hmacHexEqual(secret string, msg []byte, prefix, got string) bool {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(msg)
	want := prefix + hex.EncodeToString(m.Sum(nil))
	return hmac.Equal([]byte(want), []byte(got))
}

type slackEnvelope struct {
	Type      string `json:"type"`
	Challenge string `json:"challenge"`
	EventID   string `json:"event_id"`
	Event     struct {
		Type     string `json:"type"`
		Channel  string `json:"channel"`
		Text     string `json:"text"`
		TS       string `json:"ts"`
		ThreadTS string `json:"thread_ts"`
		BotID    string `json:"bot_id"`
	} `json:"event"`
}

var mention = regexp.MustCompile(`<@[A-Z0-9]+>`)

// ServeHTTP implements http.Handler.
func (s *Slack) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	if !VerifySlack(s.Secret, r.Header, body, now()) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var env slackEnvelope
	if json.Unmarshal(body, &env) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	switch env.Type {
	case "url_verification":
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"challenge": env.Challenge})
		return
	case "event_callback":
	default:
		w.WriteHeader(http.StatusOK)
		return
	}
	e := env.Event
	// Ignore other events and the bot's own messages so a reply never makes a ticket.
	if e.Type != "app_mention" || e.BotID != "" {
		w.WriteHeader(http.StatusOK)
		return
	}
	text := strings.TrimSpace(mention.ReplaceAllString(e.Text, ""))
	if text == "" {
		text = "Slack request"
	}
	title, _, _ := strings.Cut(text, "\n")
	if len(title) > slackTitleMax {
		title = title[:slackTitleMax]
	}
	c, err := s.Store.Create(r.Context(), NewTicket{
		Source: SourceSlack, Key: e.Channel + "/" + e.TS, Title: title, Body: text,
	})
	if err != nil {
		http.Error(w, "could not create ticket", http.StatusInternalServerError)
		return
	}
	// Slack retries an event it thinks failed; only the first delivery answers in the thread.
	if c.New && s.Replier != nil {
		thread := e.ThreadTS
		if thread == "" {
			thread = e.TS
		}
		_ = s.Replier.Reply(r.Context(), e.Channel, thread, fmt.Sprintf("Created %s: %s. I will update this thread as it moves.", c.ID, c.Title))
	}
	w.WriteHeader(http.StatusOK)
}

// Poster is a Replier that calls Slack's chat.postMessage with a bot token.
type Poster struct {
	BaseURL string // default https://slack.com
	Token   string // bot token, never logged
	Client  *http.Client
}

// Reply implements Replier.
func (p Poster) Reply(ctx context.Context, channel, threadTS, text string) error {
	base := p.BaseURL
	if base == "" {
		base = "https://slack.com"
	}
	b, _ := json.Marshal(map[string]string{"channel": channel, "thread_ts": threadTS, "text": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/chat.postMessage", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.Token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	c := p.Client
	if c == nil {
		c = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("slack: post failed")
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&out)
	if !out.OK {
		return fmt.Errorf("slack: post refused: %s", out.Error)
	}
	return nil
}
