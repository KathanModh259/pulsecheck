package probe

import (
	"math"
	"slices"
	"time"
)

// Summary aggregates the results of a run.
type Summary struct {
	Total, Up, Down    int
	P50, P95, P99, Max time.Duration
}

// Summarize counts results and computes latency percentiles over targets
// that were up. Down targets are excluded: a timed-out request's "latency"
// is just the timeout value and would distort the distribution.
func Summarize(results []Result) Summary {
	s := Summary{Total: len(results)}
	var lat []time.Duration
	for _, r := range results {
		if r.Up {
			s.Up++
			lat = append(lat, r.Latency)
		} else {
			s.Down++
		}
	}
	slices.Sort(lat)
	s.P50 = Percentile(lat, 50)
	s.P95 = Percentile(lat, 95)
	s.P99 = Percentile(lat, 99)
	if len(lat) > 0 {
		s.Max = lat[len(lat)-1]
	}
	return s
}

// Percentile returns the nearest-rank percentile p (0-100] of an
// ascending slice, or 0 for an empty slice.
func Percentile(sorted []time.Duration, p float64) time.Duration {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	rank := int(math.Ceil(p / 100 * float64(n)))
	rank = min(max(rank, 1), n)
	return sorted[rank-1]
}
