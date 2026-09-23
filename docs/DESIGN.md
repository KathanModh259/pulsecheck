# Design notes

This document explains *why* pulsecheck is built the way it is. Each section names the decision, the alternative that was rejected, and the reason.

## 1. A fixed worker pool, not a goroutine per target

`probe.Run` starts `min(Concurrency, len(targets))` worker goroutines that pull target indices from an unbuffered channel.

The simpler alternative is `go check(t)` for every target. Goroutines are cheap, so that works for 10 targets — but with 10,000 it opens 10,000 simultaneous TCP connections. That can exhaust file descriptors on the machine running the check and looks like an attack to the service being checked. A worker pool puts a hard ceiling on in-flight requests, and `TestRunRespectsConcurrency` proves the ceiling holds by counting concurrent requests on the server side.

The channel is unbuffered on purpose: the feeder blocks until a worker is free, which is exactly the backpressure we want.

## 2. Results written by index, without a mutex

Workers write `results[i] = ...` into a slice that was allocated up front. This is safe with no lock because **each index is sent on the channel exactly once**, so no two goroutines ever write the same element. Go's race detector checks this: the whole suite runs under `go test -race`.

Two happens-before edges make the reads safe too: the slice is filled with placeholders *before* the workers start (the `go` statement orders it), and it is read only *after* `wg.Wait()` returns.

A side benefit: results come back in input order for free, with no sorting.

## 3. `context` for timeouts and cancellation

Every request gets its own `context.WithTimeout` derived from the run's parent context. That gives two independent ways to stop a request:

- the **per-request deadline** fires, reported as `timeout`; or
- the **parent is cancelled** by `Ctrl-C` / `SIGTERM` via `signal.NotifyContext`, reported as `cancelled`.

The alternative, `http.Client.Timeout`, covers only the first case, and only for a single client-wide value — it can't give each target its own `timeout_ms`.

## 4. Backoff that doesn't ignore cancellation

Retries wait `Backoff × 2^(attempt−1)`. The obvious implementation, `time.Sleep(wait)`, has a bug: if the user presses `Ctrl-C` during a 30-second backoff, the program waits 30 seconds before noticing. pulsecheck instead `select`s on a timer and `ctx.Done()`, so cancellation wins immediately. `TestCheckCancelDuringBackoff` sets a 10-second backoff, cancels after 50ms, and fails if the check takes longer than 2 seconds.

The timer is explicitly stopped on the cancel path so it doesn't linger until it fires.

## 5. Draining the response body

After reading the status code, pulsecheck copies up to 1 MiB of the body into `io.Discard` before closing it. If you close an unread body, Go's HTTP transport can't reuse the TCP connection, so the next check against the same host pays a fresh TCP (and TLS) handshake. The 1 MiB cap stops a misbehaving endpoint that streams forever from hanging the check.

## 6. Latency means "time to response headers"

Latency is measured from just before `client.Do` to when it returns, which is when response headers arrive — not when the body finishes downloading. For a health check, headers are the meaningful signal.

Percentiles are computed only over targets that were **up**. A timed-out request's "latency" is simply the timeout value; including it would make p99 report your timeout setting rather than your service's behaviour. When nothing is up, the summary prints no latency at all rather than a misleading `0s`.

Percentiles use the nearest-rank method: `rank = ceil(p/100 × n)`, clamped to `[1, n]`. It's simple, exact on small samples, and always returns a value that was actually observed.

## 7. Errors that say only what's new

When a connection fails, Go returns a `*url.Error` whose message begins `Get "http://…":` — the method and URL. The report already shows the target on the same row, so pulsecheck unwraps the error with `errors.As` and shows only the underlying cause, such as `connection refused`.

## 8. `run()` returns an exit code

`main` is a single line: `os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))`. All real logic lives in `run`, which takes its arguments and writers as parameters and returns a code. That makes the entire CLI — flag parsing, output, exit codes — testable in-process, without building a binary or spawning a subprocess. `os.Exit` is kept out of `run` because it skips deferred calls and would kill the test runner.

## 9. Strict configuration

The JSON decoder uses `DisallowUnknownFields`. A misspelled key like `timout_ms` is an error, not a silent no-op. In a monitoring tool, a config that quietly does less than you think is worse than one that refuses to load.

## 10. No dependencies, non-root container

pulsecheck uses only the standard library, so there is no supply chain to audit and `go install` needs nothing else. The container is a static `CGO_ENABLED=0` binary on a distroless base, running as non-root and writing no files — which is what OpenShift's default restricted security policy requires, since it runs containers under an arbitrary UID.
