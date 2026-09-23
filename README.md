# pulsecheck

[![ci](https://github.com/KathanModh259/pulsecheck/actions/workflows/ci.yml/badge.svg)](https://github.com/KathanModh259/pulsecheck/actions/workflows/ci.yml)

A fast, dependency-free command-line tool that checks many HTTP endpoints **concurrently** and exits non-zero if any are down — so it can gate a CI job, a deploy, or a cron alert.

```text
$ pulsecheck https://api.example.com/healthz https://api.example.com/missing http://10.0.0.9:8080
NAME                          STATE  CODE  LATENCY  TRIES  DETAIL
api.example.com/healthz       UP     200   48ms     1      ok
api.example.com/missing       DOWN   404   51ms     3      unexpected status 404
10.0.0.9:8080                 DOWN   -     2ms      3      dial tcp 10.0.0.9:8080: connect: connection refused

1/3 up  |  p50 48ms  p95 48ms  p99 48ms  max 48ms
$ echo $?
1
```

## Why

Most outages start as a quiet failure nobody is watching. pulsecheck grew out of work on keeping a flaky service reliable, where the fix began with knowing — quickly and scriptably — which endpoints were actually healthy. pulsecheck is built to be dropped into any pipeline — one static binary, no runtime, no dependencies, a stable exit-code contract.

## Features

- **Bounded concurrency** — a fixed worker pool (`-c`) checks targets in parallel without ever opening more than `c` connections at once.
- **Per-request timeouts** — every request carries its own deadline via `context`, configurable globally or per target.
- **Retries with exponential backoff and jitter** — transient failures are retried (`-retries`, `-backoff`); the wait doubles each attempt up to a 30s ceiling, and jitter (`-jitter`, on by default) spreads retries out so failing targets don't all retry at once.
- **Clean cancellation** — `Ctrl-C` / `SIGTERM` aborts in-flight requests and skips pending backoff sleeps immediately.
- **Latency percentiles** — p50 / p95 / p99 / max over healthy targets.
- **Text or JSON output** — human-readable tables, or `-o json` for machines.
- **CI-friendly exit codes** — `0` all up, `1` any down, `2` usage or config error.
- **Zero dependencies** — standard library only.

## Install

```bash
go install github.com/KathanModh259/pulsecheck@latest
```

Or build from source:

```bash
git clone https://github.com/KathanModh259/pulsecheck && cd pulsecheck
make build        # produces ./bin/pulsecheck
```

## Usage

```text
pulsecheck [flags] [URL ...]

  -f FILE         JSON file of targets (see examples/targets.json)
  -c N            maximum concurrent checks (default 8)
  -retries N      retries per target after a failure (default 2)
  -backoff D      wait before the first retry; doubles each retry (default 200ms)
  -timeout D      default per-request timeout (default 5s)
  -jitter         randomise retry waits (default true; use -jitter=false to disable)
  -o FORMAT       text or json (default text)
  -version        print version and exit
```

Pass URLs directly, a targets file, or both:

```bash
pulsecheck https://go.dev https://www.redhat.com
pulsecheck -f examples/targets.json -c 16 -o json
```

### Targets file

```json
[
  { "name": "red-hat", "url": "https://www.redhat.com" },
  { "name": "go-dev", "url": "https://go.dev", "timeout_ms": 3000 },
  { "name": "no-content", "url": "https://httpbin.org/status/204", "expect_status": 204 }
]
```

| Field           | Required | Meaning                                              |
|-----------------|----------|------------------------------------------------------|
| `url`           | yes      | `http` or `https` URL to `GET`                       |
| `name`          | no       | Display name; defaults to host + path                |
| `expect_status` | no       | Exact status that counts as up; default is any `2xx` |
| `timeout_ms`    | no       | Overrides `-timeout` for this target                 |

Unknown fields are rejected, so a typo like `timout_ms` fails loudly instead of being silently ignored.

### Exit codes

| Code | Meaning                                   |
|------|-------------------------------------------|
| `0`  | every target is up                        |
| `1`  | at least one target is down               |
| `2`  | bad flags, bad config, or an output error |

### In CI

```yaml
- name: Smoke-test the deployment
  run: pulsecheck -retries 5 -backoff 1s https://staging.example.com/healthz
```

The step fails if the service isn't healthy after the retries.

## Container

A multi-stage build produces a small distroless image that runs as non-root and writes no files, so it works under OpenShift's restricted security model (arbitrary UIDs):

```bash
docker build -t pulsecheck .
docker run --rm pulsecheck https://go.dev
```

## Development

```bash
make check    # gofmt check + go vet + tests with the race detector
make test     # tests only (with -race)
```

The test suite uses `net/http/httptest` to simulate healthy, failing, slow, and flapping servers, and includes a test that **proves the worker pool never exceeds its concurrency limit**. See [docs/DESIGN.md](docs/DESIGN.md) for how and why it works the way it does.

## License

MIT © 2026 Kathan Modh — see [LICENSE](LICENSE).
