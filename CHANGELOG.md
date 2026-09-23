# Changelog

All notable changes to pulsecheck are documented here.
This project follows [Semantic Versioning](https://semver.org/).

## [v0.1.0] - 2026-09-23

First release.

### Added
- Concurrent HTTP health checks through a bounded worker pool (`-c`).
- Per-request timeouts via `context`, set globally (`-timeout`) or per target (`timeout_ms`).
- Retries with exponential backoff (`-retries`, `-backoff`), capped at 30s.
- Jitter on retry waits (`-jitter`, on by default) so failing targets don't retry in lockstep.
- Clean cancellation on `Ctrl-C` / `SIGTERM`, including during backoff sleeps.
- Latency percentiles (p50 / p95 / p99 / max) over healthy targets.
- Text and JSON output (`-o json`).
- Stable exit codes: `0` all up, `1` any down, `2` usage or config error.
- JSON targets file with strict validation (`-f`).
- Non-root distroless container image, compatible with OpenShift's restricted security model.

### Fixed
- Exponential backoff could overflow `int64` with many retries, producing waits of years, negative waits, or zero waits. Growth now stops at a ceiling.
