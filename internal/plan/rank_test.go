package plan

import (
	"math"
	"strings"
	"testing"
)

func TestScoreFormulas(t *testing.T) {
	r, err := Score("Score: reach=8 impact=2 confidence=0.5 effort=4", RICE)
	if err != nil || math.Abs(r-2) > 1e-9 {
		t.Errorf("RICE = %v, %v; want 2", r, err)
	}
	w, err := Score("text\nScore: value=8 time=3 risk=1 size=4\n", WSJF)
	if err != nil || math.Abs(w-3) > 1e-9 {
		t.Errorf("WSJF = %v, %v; want 3", w, err)
	}
}

func TestScoreRefusesBadLines(t *testing.T) {
	for name, c := range map[string]struct {
		body string
		m    Method
		want string
	}{
		"no line":      {"nothing", RICE, "no Score"},
		"missing key":  {"Score: reach=1 impact=1 confidence=1", RICE, "effort="},
		"zero effort":  {"Score: reach=1 impact=1 confidence=1 effort=0", RICE, "above 0"},
		"zero size":    {"Score: value=1 time=1 risk=1 size=0", WSJF, "above 0"},
		"not a number": {"Score: reach=x impact=1 confidence=1 effort=1", RICE, "name=number"},
		"negative":     {"Score: reach=-1 impact=1 confidence=1 effort=1", RICE, "name=number"},
		"wrong method": {"Score: reach=1 impact=1 confidence=1 effort=1", WSJF, "value="},
	} {
		if _, err := Score(c.body, c.m); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to contain %q", name, err, c.want)
		}
	}
}

func TestRankOrdersByScoreThenGivenOrderThenUnscored(t *testing.T) {
	items := []Item{
		{"1", "Score: reach=1 impact=1 confidence=1 effort=1"}, // 1
		{"2", "no score"},
		{"3", "Score: reach=9 impact=1 confidence=1 effort=1"}, // 9
		{"4", "Score: reach=1 impact=1 confidence=1 effort=1"}, // 1, ties with 1
		{"5", "Score: reach=1 impact=1 confidence=1 effort=0"}, // unscored
	}
	var got []string
	for _, r := range Rank(items, RICE) {
		got = append(got, r.ID)
	}
	if want := "3 1 4 2 5"; strings.Join(got, " ") != want {
		t.Errorf("order = %v, want %s", got, want)
	}
	rk := Rank(items, RICE)
	if rk[0].Rank != 0 || rk[4].Rank != 4 || rk[3].Scored || rk[3].Why == "" {
		t.Errorf("ranks or reasons wrong: %+v", rk)
	}
}

func TestParseMethod(t *testing.T) {
	if m, err := ParseMethod(" WSJF "); err != nil || m != WSJF {
		t.Errorf("got %v %v", m, err)
	}
	if _, err := ParseMethod("moscow"); err == nil {
		t.Error("an unknown method must be refused")
	}
}
