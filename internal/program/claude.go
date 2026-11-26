package program

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/KaaS-team/factory/internal/dispatch/local"
)

// leadBudgetUSD caps one lead review.
const leadBudgetUSD = 2.0

// ClaudeAsk asks the operator's local claude login, read-only, and returns the final text. It runs
// in dir. Provider keys in the environment are dropped: the session runs under the login.
func ClaudeAsk(bin, dir string) AskFunc {
	return func(ctx context.Context, prompt string) (string, error) {
		argv, err := local.ClaudeArgv(local.ClaudeInput{Stage: "plan", Issue: "program", MaxBudgetUSD: leadBudgetUSD, Prompt: prompt})
		if err != nil {
			return "", err
		}
		for i := 0; i+1 < len(argv); i++ {
			switch argv[i] {
			case "--tools":
				argv[i+1] = "Read,Grep,Glob"
			case "--permission-mode":
				argv[i+1] = "plan"
			}
		}
		cmd := exec.CommandContext(ctx, bin, argv[1:]...)
		cmd.Dir = dir
		cmd.Env = scrubEnv(os.Environ(), local.ClaudePreset.Env)
		cmd.WaitDelay = 2 * time.Second
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		text, found := FinalText(out)
		if err != nil && !found {
			return "", fmt.Errorf("running %s: %w: %s", bin, err, strings.TrimSpace(stderr.String()))
		}
		if !found {
			return "", errors.New("the agent printed no final text")
		}
		return text, nil
	}
}

var providerPrefixes = []string{"ANTHROPIC_", "OPENAI_", "AZURE_OPENAI_", "GEMINI_", "MISTRAL_", "COHERE_"}

// scrubEnv returns parent without provider keys, with extra set on top.
func scrubEnv(parent []string, extra map[string]string) []string {
	var out []string
	for _, kv := range parent {
		name, _, _ := strings.Cut(kv, "=")
		up := strings.ToUpper(name)
		drop := up == "GOOGLE_API_KEY" || up == "CURSOR_API_KEY"
		for _, p := range providerPrefixes {
			drop = drop || strings.HasPrefix(up, p)
		}
		if _, over := extra[name]; !drop && !over {
			out = append(out, kv)
		}
	}
	for k, v := range extra {
		out = append(out, k+"="+v)
	}
	return out
}

// FinalText reads stream-json and returns the result line's text, else the last assistant text.
func FinalText(out []byte) (string, bool) {
	var last string
	var found bool
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		var l struct {
			Type    string `json:"type"`
			Result  string `json:"result"`
			Message struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		switch l.Type {
		case "result":
			if l.Result != "" {
				return l.Result, true
			}
		case "assistant":
			var b strings.Builder
			for _, c := range l.Message.Content {
				if c.Type == "text" {
					b.WriteString(c.Text)
				}
			}
			if b.Len() > 0 {
				last, found = b.String(), true
			}
		}
	}
	return last, found
}
