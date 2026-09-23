// Package probe runs concurrent HTTP health checks with bounded
// parallelism, per-request timeouts, and retry with exponential backoff.
package probe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// Target is a single endpoint to check.
type Target struct {
	Name         string `json:"name"`
	URL          string `json:"url"`
	ExpectStatus int    `json:"expect_status,omitempty"` // 0 means any 2xx
	TimeoutMS    int    `json:"timeout_ms,omitempty"`    // 0 means use Config.Timeout
}

// Result is the outcome of checking one Target.
type Result struct {
	Target     Target
	Up         bool
	StatusCode int           // 0 if no HTTP response was received
	Latency    time.Duration // time until response headers arrived
	Attempts   int           // 0 means the check never ran
	Err        string
}

// Config controls how checks are run.
type Config struct {
	Concurrency int           // maximum checks in flight at once
	Retries     int           // extra attempts after the first failure
	Backoff     time.Duration // wait before the first retry; doubles each retry
	Timeout     time.Duration // default per-request timeout
	Jitter      bool          // randomise backoff so failing targets don't retry in lockstep
	Client      *http.Client  // nil means http.DefaultClient

	rand func() float64 // returns [0,1); nil means math/rand/v2. Swappable in tests.
}

// maxBackoff caps exponential growth. Without it, Backoff << (attempt-1)
// reaches years after ~30 retries and then overflows int64 into a negative
// or zero wait, so retries either hang or fire with no delay at all.
const maxBackoff = 30 * time.Second

const notRun = "not run: cancelled"

func (c Config) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return http.DefaultClient
}

// Check probes one target, retrying on failure with exponential backoff.
// It returns as soon as ctx is cancelled, so an interrupt never has to wait
// out a backoff sleep.
func (c Config) Check(ctx context.Context, t Target) Result {
	var res Result
	for attempt := 1; ; attempt++ {
		res = c.once(ctx, t)
		res.Attempts = attempt
		if res.Up || attempt > c.Retries || ctx.Err() != nil {
			return res
		}
		timer := time.NewTimer(c.backoff(attempt))
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return res
		}
	}
}

// backoff returns the wait before retry number attempt (1-based): Backoff,
// doubled per attempt, capped at max(maxBackoff, Backoff), then optionally
// jittered. Doubling stops at the cap, so it can never overflow.
func (c Config) backoff(attempt int) time.Duration {
	if c.Backoff <= 0 {
		return 0
	}
	ceiling := max(maxBackoff, c.Backoff) // never shrink a large explicit backoff
	wait := c.Backoff
	for i := 1; i < attempt && wait < ceiling; i++ {
		wait *= 2
	}
	wait = min(wait, ceiling)
	if !c.Jitter {
		return wait
	}
	// "Equal jitter": keep half the wait as a floor so retries still back
	// off, and randomise the other half. When many targets fail together
	// (say, one upstream outage), this spreads their retries out instead of
	// every worker hammering the recovering service at the same instant.
	r := c.rand
	if r == nil {
		r = rand.Float64
	}
	half := wait / 2
	return half + time.Duration(r()*float64(wait-half))
}

// once performs a single GET with its own timeout derived from ctx.
func (c Config) once(ctx context.Context, t Target) Result {
	res := Result{Target: t}

	timeout := c.Timeout
	if t.TimeoutMS > 0 {
		timeout = time.Duration(t.TimeoutMS) * time.Millisecond
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, t.URL, nil)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	req.Header.Set("User-Agent", "pulsecheck/1")

	start := time.Now()
	resp, err := c.client().Do(req)
	res.Latency = time.Since(start)
	if err != nil {
		res.Err = describe(err)
		return res
	}
	// Drain a bounded amount of the body so the TCP connection can be
	// reused by the next check instead of being torn down.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()

	res.StatusCode = resp.StatusCode
	res.Up = statusOK(resp.StatusCode, t.ExpectStatus)
	if !res.Up {
		res.Err = fmt.Sprintf("unexpected status %d", resp.StatusCode)
	}
	return res
}

func statusOK(got, want int) bool {
	if want == 0 {
		return got >= 200 && got < 300
	}
	return got == want
}

func describe(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	}
	// *url.Error prefixes the method and URL, which the report already
	// shows; unwrap it so the detail column holds only the real cause.
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err.Error()
	}
	return err.Error()
}

// Run checks every target with a fixed pool of worker goroutines and
// returns results in the same order as targets. Targets that never start
// because ctx was cancelled are reported as down with Attempts == 0.
func Run(ctx context.Context, cfg Config, targets []Target) []Result {
	results := make([]Result, len(targets))
	for i, t := range targets {
		results[i] = Result{Target: t, Err: notRun}
	}
	if len(targets) == 0 {
		return results
	}

	workers := max(cfg.Concurrency, 1)
	workers = min(workers, len(targets))

	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				// Each index is sent on the channel exactly once, so no two
				// goroutines ever write the same element: no mutex needed.
				results[i] = cfg.Check(ctx, targets[i])
			}
		}()
	}

feed:
	for i := range targets {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	return results
}
