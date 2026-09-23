package probe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func statusServer(t *testing.T, code int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCheckHealthy(t *testing.T) {
	srv := statusServer(t, http.StatusOK)
	res := Config{Timeout: time.Second}.Check(context.Background(), Target{Name: "ok", URL: srv.URL})

	if !res.Up || res.StatusCode != 200 || res.Attempts != 1 || res.Err != "" {
		t.Fatalf("got %+v, want up on first attempt", res)
	}
}

func TestCheckUnexpectedStatus(t *testing.T) {
	srv := statusServer(t, http.StatusServiceUnavailable)
	res := Config{Timeout: time.Second}.Check(context.Background(), Target{URL: srv.URL})

	if res.Up || res.StatusCode != 503 || !strings.Contains(res.Err, "503") {
		t.Fatalf("got %+v, want down with status 503", res)
	}
}

func TestCheckExpectStatus(t *testing.T) {
	srv := statusServer(t, http.StatusNoContent)
	cfg := Config{Timeout: time.Second}

	if res := cfg.Check(context.Background(), Target{URL: srv.URL, ExpectStatus: 204}); !res.Up {
		t.Fatalf("expect_status 204 vs 204: got %+v, want up", res)
	}
	if res := cfg.Check(context.Background(), Target{URL: srv.URL, ExpectStatus: 200}); res.Up {
		t.Fatalf("expect_status 200 vs 204: got %+v, want down", res)
	}
}

func TestCheckTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done(): // client gave up; stop promptly
		}
	}))
	t.Cleanup(srv.Close)

	start := time.Now()
	res := Config{}.Check(context.Background(), Target{URL: srv.URL, TimeoutMS: 50})

	if res.Up || res.Err != "timeout" {
		t.Fatalf("got %+v, want timeout", res)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("check took %v; per-target timeout was not honoured", elapsed)
	}
}

func TestCheckConnectionRefusedDetail(t *testing.T) {
	srv := statusServer(t, http.StatusOK)
	addr := srv.URL
	srv.Close() // nothing listening now

	res := Config{Timeout: time.Second}.Check(context.Background(), Target{URL: addr})
	if res.Up || !strings.Contains(res.Err, "connection refused") {
		t.Fatalf("got %+v, want connection refused", res)
	}
	if strings.Contains(res.Err, "Get ") {
		t.Fatalf("detail %q repeats the request line; want only the cause", res.Err)
	}
}

func TestCheckRetriesThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	cfg := Config{Timeout: time.Second, Retries: 3, Backoff: time.Millisecond}
	res := cfg.Check(context.Background(), Target{URL: srv.URL})

	if !res.Up || res.Attempts != 3 {
		t.Fatalf("got %+v, want up on attempt 3", res)
	}
}

func TestCheckGivesUpAfterRetries(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	cfg := Config{Timeout: time.Second, Retries: 2, Backoff: time.Millisecond}
	res := cfg.Check(context.Background(), Target{URL: srv.URL})

	if res.Up || res.Attempts != 3 || calls.Load() != 3 {
		t.Fatalf("got %+v after %d calls, want down after exactly 3", res, calls.Load())
	}
}

func TestCheckCancelDuringBackoff(t *testing.T) {
	srv := statusServer(t, http.StatusInternalServerError)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)

	start := time.Now()
	cfg := Config{Timeout: time.Second, Retries: 5, Backoff: 10 * time.Second}
	res := cfg.Check(ctx, Target{URL: srv.URL})

	if res.Up {
		t.Fatalf("got %+v, want down", res)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("cancel took %v; backoff sleep ignored the context", elapsed)
	}
}

// TestRunRespectsConcurrency proves the worker pool never has more than
// Concurrency requests in flight, even with many targets.
func TestRunRespectsConcurrency(t *testing.T) {
	const limit = 4
	var inFlight, peak atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond) // hold the slot so requests overlap
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	targets := make([]Target, 20)
	for i := range targets {
		targets[i] = Target{URL: srv.URL}
	}
	results := Run(context.Background(), Config{Concurrency: limit, Timeout: time.Second}, targets)

	for i, r := range results {
		if !r.Up {
			t.Fatalf("target %d: got %+v, want up", i, r)
		}
	}
	if got := peak.Load(); got > limit {
		t.Fatalf("peak concurrency %d exceeded limit %d", got, limit)
	}
	if got := peak.Load(); got < 2 {
		t.Fatalf("peak concurrency %d; checks did not run in parallel", got)
	}
}

func TestRunPreservesOrder(t *testing.T) {
	ok := statusServer(t, http.StatusOK)
	bad := statusServer(t, http.StatusBadGateway)
	targets := []Target{
		{Name: "a", URL: ok.URL},
		{Name: "b", URL: bad.URL},
		{Name: "c", URL: ok.URL},
	}
	results := Run(context.Background(), Config{Concurrency: 3, Timeout: time.Second}, targets)

	want := []struct {
		name string
		up   bool
	}{{"a", true}, {"b", false}, {"c", true}}
	for i, w := range want {
		if results[i].Target.Name != w.name || results[i].Up != w.up {
			t.Fatalf("result %d: got %s up=%v, want %s up=%v",
				i, results[i].Target.Name, results[i].Up, w.name, w.up)
		}
	}
}

func TestRunCancelledContext(t *testing.T) {
	srv := statusServer(t, http.StatusOK)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	targets := []Target{{URL: srv.URL}, {URL: srv.URL}, {URL: srv.URL}}
	for i, r := range Run(ctx, Config{Concurrency: 2, Timeout: time.Second}, targets) {
		if r.Up {
			t.Fatalf("target %d: got up on a cancelled context", i)
		}
	}
}

func TestRunEmpty(t *testing.T) {
	if got := Run(context.Background(), Config{}, nil); len(got) != 0 {
		t.Fatalf("got %d results for no targets", len(got))
	}
}

func TestBackoffDoublesWithoutJitter(t *testing.T) {
	cfg := Config{Backoff: 100 * time.Millisecond}
	for attempt, want := range map[int]time.Duration{
		1: 100 * time.Millisecond,
		2: 200 * time.Millisecond,
		3: 400 * time.Millisecond,
	} {
		if got := cfg.backoff(attempt); got != want {
			t.Errorf("backoff(%d) = %v, want %v", attempt, got, want)
		}
	}
}

// Regression test: the old Backoff<<(attempt-1) formula produced a 17-year
// wait at attempt 30, a negative wait at 35, and zero at 64.
func TestBackoffNeverOverflows(t *testing.T) {
	cfg := Config{Backoff: time.Second}
	for _, attempt := range []int{30, 35, 64, 1000} {
		if got := cfg.backoff(attempt); got != maxBackoff {
			t.Errorf("backoff(%d) = %v, want the %v ceiling", attempt, got, maxBackoff)
		}
	}
}

func TestBackoffKeepsLargeExplicitValue(t *testing.T) {
	cfg := Config{Backoff: time.Minute}
	if got := cfg.backoff(5); got != time.Minute {
		t.Fatalf("backoff = %v; a user-set 1m backoff must not be cut to %v", got, maxBackoff)
	}
}

func TestBackoffZero(t *testing.T) {
	if got := (Config{Jitter: true}).backoff(3); got != 0 {
		t.Fatalf("backoff with Backoff=0 = %v, want 0", got)
	}
}

func TestBackoffJitterBounds(t *testing.T) {
	base := Config{Backoff: 100 * time.Millisecond, Jitter: true} // attempt 3 => 400ms before jitter
	cases := map[float64]time.Duration{
		0:   200 * time.Millisecond, // floor is half the wait
		0.5: 300 * time.Millisecond,
	}
	for r, want := range cases {
		cfg := base
		cfg.rand = func() float64 { return r }
		if got := cfg.backoff(3); got != want {
			t.Errorf("rand=%v: backoff = %v, want %v", r, got, want)
		}
	}
}

func TestBackoffJitterRealRandomStaysInRange(t *testing.T) {
	cfg := Config{Backoff: 100 * time.Millisecond, Jitter: true}
	lo, hi := 200*time.Millisecond, 400*time.Millisecond
	seen := map[time.Duration]bool{}
	for i := 0; i < 1000; i++ {
		got := cfg.backoff(3)
		if got < lo || got >= hi {
			t.Fatalf("backoff = %v, want within [%v, %v)", got, lo, hi)
		}
		seen[got] = true
	}
	if len(seen) < 2 {
		t.Fatal("jitter produced the same wait every time")
	}
}
