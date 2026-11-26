package plan

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Method is how Rank scores a ticket.
type Method string

// The two ranking methods the config may name.
const (
	RICE Method = "rice"
	WSJF Method = "wsjf"
)

// ParseMethod accepts "rice" or "wsjf" in any case.
func ParseMethod(s string) (Method, error) {
	switch m := Method(strings.ToLower(strings.TrimSpace(s))); m {
	case RICE, WSJF:
		return m, nil
	}
	return "", fmt.Errorf("unknown ranking method %q: use rice or wsjf", s)
}

// A scored issue carries one line in its body:
//
//	Score: reach=8 impact=2 confidence=0.8 effort=2        (RICE)
//	Score: value=8 time=3 risk=2 size=5                    (WSJF)
//
// RICE is reach*impact*confidence/effort. WSJF is (value+time+risk)/size. A higher score runs first.
var scoreLine = regexp.MustCompile(`(?im)^Score:[ \t]*(.*)$`)

// Item is an issue to rank: its id and its body.
type Item struct {
	ID   string
	Body string
}

// Ranked is one issue with its score and position; Rank 0 runs first. Scored is false for an issue
// with no usable Score line, which sorts after every scored issue.
type Ranked struct {
	ID     string
	Score  float64
	Scored bool
	Rank   int
	Why    string // why an issue is unscored, empty otherwise
}

// Score reads the Score line of body for method.
func Score(body string, m Method) (float64, error) {
	line := scoreLine.FindStringSubmatch(body)
	if line == nil {
		return 0, fmt.Errorf("no Score: line")
	}
	kv := map[string]float64{}
	for _, f := range strings.Fields(line[1]) {
		k, v, ok := strings.Cut(f, "=")
		x, err := strconv.ParseFloat(v, 64)
		if !ok || err != nil || x < 0 || math.IsNaN(x) || math.IsInf(x, 0) {
			return 0, fmt.Errorf("Score: %q is not name=number", f)
		}
		kv[strings.ToLower(k)] = x
	}
	need := map[Method][]string{RICE: {"reach", "impact", "confidence", "effort"}, WSJF: {"value", "time", "risk", "size"}}[m]
	if need == nil {
		return 0, fmt.Errorf("unknown ranking method %q", m)
	}
	for _, k := range need {
		if _, ok := kv[k]; !ok {
			return 0, fmt.Errorf("Score: line lacks %s=", k)
		}
	}
	if m == RICE {
		if kv["effort"] == 0 {
			return 0, fmt.Errorf("Score: effort must be above 0")
		}
		return kv["reach"] * kv["impact"] * kv["confidence"] / kv["effort"], nil
	}
	if kv["size"] == 0 {
		return 0, fmt.Errorf("Score: size must be above 0")
	}
	return (kv["value"] + kv["time"] + kv["risk"]) / kv["size"], nil
}

// Rank orders items by score, highest first; equal scores keep the order given, and unscored items
// come last in the order given. The result holds every item once.
func Rank(items []Item, m Method) []Ranked {
	out := make([]Ranked, len(items))
	for i, it := range items {
		s, err := Score(it.Body, m)
		out[i] = Ranked{ID: it.ID, Score: s, Scored: err == nil}
		if err != nil {
			out[i].Why = err.Error()
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Scored != b.Scored {
			return a.Scored
		}
		return a.Scored && a.Score > b.Score
	})
	for i := range out {
		out[i].Rank = i
	}
	return out
}
