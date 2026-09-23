package probe

import (
	"strings"
	"testing"
	"time"
)

func TestLoadTargets(t *testing.T) {
	in := `[
		{"name": "api", "url": "https://example.com/health", "expect_status": 204},
		{"url": "  http://localhost:8080  ", "timeout_ms": 1500}
	]`
	ts, err := LoadTargets(strings.NewReader(in))
	if err != nil {
		t.Fatalf("LoadTargets: %v", err)
	}
	if len(ts) != 2 {
		t.Fatalf("got %d targets, want 2", len(ts))
	}
	if ts[0].Name != "api" || ts[0].ExpectStatus != 204 {
		t.Errorf("target 0 = %+v", ts[0])
	}
	if ts[1].Name != "localhost:8080" || ts[1].URL != "http://localhost:8080" || ts[1].TimeoutMS != 1500 {
		t.Errorf("target 1 = %+v; want host as default name and trimmed URL", ts[1])
	}
}

func TestLoadTargetsRejectsBadInput(t *testing.T) {
	cases := map[string]string{
		"not json":        `{`,
		"unknown field":   `[{"url": "https://a.com", "timout_ms": 5}]`,
		"missing url":     `[{"name": "x"}]`,
		"bad scheme":      `[{"url": "ftp://a.com"}]`,
		"no host":         `[{"url": "https://"}]`,
		"bad status":      `[{"url": "https://a.com", "expect_status": 42}]`,
		"negative timout": `[{"url": "https://a.com", "timeout_ms": -1}]`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadTargets(strings.NewReader(in)); err == nil {
				t.Fatalf("LoadTargets(%s) succeeded, want error", in)
			}
		})
	}
}

func TestFromURL(t *testing.T) {
	tg, err := FromURL("https://go.dev")
	if err != nil || tg.Name != "go.dev" {
		t.Fatalf("FromURL = %+v, %v", tg, err)
	}
	if _, err := FromURL("not a url"); err == nil {
		t.Fatal("FromURL accepted a URL without a scheme")
	}
	// Two checks on the same host must get distinct default names.
	a, _ := FromURL("http://svc.local:8080/")
	b, _ := FromURL("http://svc.local:8080/healthz")
	if a.Name == b.Name || b.Name != "svc.local:8080/healthz" {
		t.Fatalf("default names %q and %q are not distinguishable", a.Name, b.Name)
	}
}

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func TestPercentile(t *testing.T) {
	sorted := []time.Duration{ms(10), ms(20), ms(30), ms(40), ms(50), ms(60), ms(70), ms(80), ms(90), ms(100)}
	cases := []struct {
		p    float64
		want time.Duration
	}{
		{50, ms(50)},  // ceil(0.50*10) = rank 5
		{95, ms(100)}, // ceil(0.95*10) = rank 10
		{10, ms(10)},  // ceil(0.10*10) = rank 1
		{1, ms(10)},   // rank clamps to 1
		{100, ms(100)},
	}
	for _, c := range cases {
		if got := Percentile(sorted, c.p); got != c.want {
			t.Errorf("Percentile(p%v) = %v, want %v", c.p, got, c.want)
		}
	}
	if got := Percentile(nil, 50); got != 0 {
		t.Errorf("Percentile(empty) = %v, want 0", got)
	}
}

func TestSummarize(t *testing.T) {
	results := []Result{
		{Up: true, Latency: ms(30)},
		{Up: false, Latency: ms(5000)}, // a timeout must not skew percentiles
		{Up: true, Latency: ms(10)},
		{Up: true, Latency: ms(20)},
	}
	s := Summarize(results)
	if s.Total != 4 || s.Up != 3 || s.Down != 1 {
		t.Fatalf("counts = %+v", s)
	}
	if s.P50 != ms(20) || s.Max != ms(30) {
		t.Fatalf("P50 = %v, Max = %v; want 20ms, 30ms (down target excluded)", s.P50, s.Max)
	}
}
