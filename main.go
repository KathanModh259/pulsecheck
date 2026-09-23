// Command pulsecheck checks many HTTP endpoints concurrently and exits
// non-zero if any are down, so it can gate a CI job or a cron alert.
//
// Author: Kathan Modh
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/KathanModh259/pulsecheck/internal/probe"
)

// version is set at build time: go build -ldflags "-X main.version=v1.0.0"
var version = "dev"

// Exit codes are part of the CLI contract; scripts depend on them.
const (
	exitOK    = 0 // every target is up
	exitDown  = 1 // at least one target is down
	exitUsage = 2 // bad flags, bad config, or an output error
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("pulsecheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("f", "", "JSON file of targets (see examples/targets.json)")
	conc := fs.Int("c", 8, "maximum concurrent checks")
	retries := fs.Int("retries", 2, "retries per target after a failure")
	backoff := fs.Duration("backoff", 200*time.Millisecond, "wait before the first retry; doubles each retry")
	timeout := fs.Duration("timeout", 5*time.Second, "default per-request timeout")
	jitter := fs.Bool("jitter", true, "randomise retry waits so failing targets don't retry in lockstep")
	format := fs.String("o", "text", "output format: text or json")
	showVersion := fs.Bool("version", false, "print version and exit")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: pulsecheck [flags] [URL ...]\n\nFlags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if *showVersion {
		fmt.Fprintln(stdout, "pulsecheck", version)
		return exitOK
	}
	if *format != "text" && *format != "json" {
		fmt.Fprintf(stderr, "pulsecheck: unknown output format %q (want text or json)\n", *format)
		return exitUsage
	}
	if *conc < 1 || *retries < 0 || *backoff < 0 || *timeout <= 0 {
		fmt.Fprintln(stderr, "pulsecheck: need -c >= 1, -retries >= 0, -backoff >= 0, -timeout > 0")
		return exitUsage
	}

	targets, err := collectTargets(*file, fs.Args())
	if err != nil {
		fmt.Fprintf(stderr, "pulsecheck: %v\n", err)
		return exitUsage
	}
	if len(targets) == 0 {
		fmt.Fprintln(stderr, "pulsecheck: no targets: pass one or more URLs, or -f FILE")
		fs.Usage()
		return exitUsage
	}

	// Ctrl-C or SIGTERM cancels the context; in-flight requests abort and
	// pending retries stop immediately instead of sleeping out their backoff.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := probe.Config{
		Concurrency: *conc,
		Retries:     *retries,
		Backoff:     *backoff,
		Timeout:     *timeout,
		Jitter:      *jitter,
	}
	results := probe.Run(ctx, cfg, targets)
	sum := probe.Summarize(results)

	if *format == "json" {
		err = writeJSON(stdout, results, sum)
	} else {
		err = writeText(stdout, results, sum)
	}
	if err != nil {
		fmt.Fprintf(stderr, "pulsecheck: write output: %v\n", err)
		return exitUsage
	}
	if sum.Down > 0 {
		return exitDown
	}
	return exitOK
}

func collectTargets(file string, urls []string) ([]probe.Target, error) {
	var targets []probe.Target
	if file != "" {
		f, err := os.Open(file)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		ts, err := probe.LoadTargets(f)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		targets = append(targets, ts...)
	}
	for _, u := range urls {
		t, err := probe.FromURL(u)
		if err != nil {
			return nil, err
		}
		targets = append(targets, t)
	}
	return targets, nil
}

func writeText(w io.Writer, results []probe.Result, s probe.Summary) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSTATE\tCODE\tLATENCY\tTRIES\tDETAIL")
	for _, r := range results {
		state := "UP"
		if !r.Up {
			state = "DOWN"
		}
		code := "-"
		if r.StatusCode != 0 {
			code = strconv.Itoa(r.StatusCode)
		}
		latency := "-"
		if r.Attempts > 0 {
			latency = fmtDur(r.Latency)
		}
		detail := r.Err
		if detail == "" {
			detail = "ok"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\n",
			r.Target.Name, state, code, latency, r.Attempts, detail)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if s.Up == 0 {
		// No healthy targets means no latency sample; don't print "0s".
		_, err := fmt.Fprintf(w, "\n%d/%d up\n", s.Up, s.Total)
		return err
	}
	_, err := fmt.Fprintf(w, "\n%d/%d up  |  p50 %s  p95 %s  p99 %s  max %s\n",
		s.Up, s.Total, fmtDur(s.P50), fmtDur(s.P95), fmtDur(s.P99), fmtDur(s.Max))
	return err
}

func fmtDur(d time.Duration) string {
	if d >= time.Millisecond {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(time.Microsecond).String()
}

type jsonResult struct {
	Name       string  `json:"name"`
	URL        string  `json:"url"`
	Up         bool    `json:"up"`
	StatusCode int     `json:"status_code,omitempty"`
	LatencyMS  float64 `json:"latency_ms"`
	Attempts   int     `json:"attempts"`
	Error      string  `json:"error,omitempty"`
}

type jsonSummary struct {
	Total int     `json:"total"`
	Up    int     `json:"up"`
	Down  int     `json:"down"`
	P50MS float64 `json:"p50_ms"`
	P95MS float64 `json:"p95_ms"`
	P99MS float64 `json:"p99_ms"`
	MaxMS float64 `json:"max_ms"`
}

type jsonReport struct {
	Results []jsonResult `json:"results"`
	Summary jsonSummary  `json:"summary"`
}

func writeJSON(w io.Writer, results []probe.Result, s probe.Summary) error {
	rep := jsonReport{
		Results: make([]jsonResult, 0, len(results)),
		Summary: jsonSummary{
			Total: s.Total, Up: s.Up, Down: s.Down,
			P50MS: toMS(s.P50), P95MS: toMS(s.P95), P99MS: toMS(s.P99), MaxMS: toMS(s.Max),
		},
	}
	for _, r := range results {
		rep.Results = append(rep.Results, jsonResult{
			Name:       r.Target.Name,
			URL:        r.Target.URL,
			Up:         r.Up,
			StatusCode: r.StatusCode,
			LatencyMS:  toMS(r.Latency),
			Attempts:   r.Attempts,
			Error:      r.Err,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}

func toMS(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
