// Package telemetry provides Prometheus compatible metrics, request records and cost estimates
// without third party dependencies.
package telemetry

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// DefaultBuckets are histogram bucket upper bounds in seconds, chosen for a proxy whose fast path
// takes microseconds and whose slow path takes seconds.
var DefaultBuckets = []float64{0.00005, 0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 120}

type seriesKey struct{ name, labels string }

type histogram struct {
	counts []uint64 // per bucket, not cumulative
	sum    float64
	n      uint64
}

// Metrics is a tiny registry of counters, gauges and histograms that renders the Prometheus text format.
type Metrics struct {
	mu    sync.Mutex
	kinds map[string]string
	help  map[string]string
	vals  map[seriesKey]float64
	hists map[seriesKey]*histogram
}

// NewMetrics creates an empty registry.
func NewMetrics() *Metrics {
	return &Metrics{
		kinds: map[string]string{}, help: map[string]string{},
		vals: map[seriesKey]float64{}, hists: map[seriesKey]*histogram{},
	}
}

// Labels builds a label string from key, value pairs: Labels("a","1","b","2") gives a="1",b="2".
func Labels(kv ...string) string {
	var b strings.Builder
	for i := 0; i+1 < len(kv); i += 2 {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(kv[i])
		b.WriteString(`="`)
		b.WriteString(strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(kv[i+1]))
		b.WriteByte('"')
	}
	return b.String()
}

// Describe registers a metric. kind is "counter", "gauge" or "histogram".
func (m *Metrics) Describe(name, kind, help string) {
	m.mu.Lock()
	m.kinds[name], m.help[name] = kind, help
	m.mu.Unlock()
}

// Add increases a counter or gauge series by delta (use a negative delta to decrease a gauge).
func (m *Metrics) Add(name, labels string, delta float64) {
	m.mu.Lock()
	m.vals[seriesKey{name, labels}] += delta
	m.mu.Unlock()
}

// Set overwrites a gauge series.
func (m *Metrics) Set(name, labels string, v float64) {
	m.mu.Lock()
	m.vals[seriesKey{name, labels}] = v
	m.mu.Unlock()
}

// Observe records one value (in seconds) in a histogram series.
func (m *Metrics) Observe(name, labels string, v float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := seriesKey{name, labels}
	h := m.hists[k]
	if h == nil {
		h = &histogram{counts: make([]uint64, len(DefaultBuckets)+1)}
		m.hists[k] = h
	}
	i := sort.SearchFloat64s(DefaultBuckets, v) // first bucket whose bound is >= v
	h.counts[i]++
	h.sum += v
	h.n++
}

func fmtFloat(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

func withLE(labels, le string) string {
	if labels == "" {
		return `le="` + le + `"`
	}
	return labels + `,le="` + le + `"`
}

func series(name, labels string) string {
	if labels == "" {
		return name
	}
	return name + "{" + labels + "}"
}

// WriteText renders every metric in the Prometheus text exposition format.
func (m *Metrics) WriteText(w io.Writer) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	names := map[string]bool{}
	for k := range m.vals {
		names[k.name] = true
	}
	for k := range m.hists {
		names[k.name] = true
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)

	for _, name := range sorted {
		kind := m.kinds[name]
		if kind == "" {
			kind = "counter"
		}
		if h := m.help[name]; h != "" {
			if _, err := fmt.Fprintf(w, "# HELP %s %s\n", name, h); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(w, "# TYPE %s %s\n", name, kind); err != nil {
			return err
		}
		if kind == "histogram" {
			var keys []seriesKey
			for k := range m.hists {
				if k.name == name {
					keys = append(keys, k)
				}
			}
			sort.Slice(keys, func(i, j int) bool { return keys[i].labels < keys[j].labels })
			for _, k := range keys {
				h := m.hists[k]
				var cum uint64
				for i, b := range DefaultBuckets {
					cum += h.counts[i]
					fmt.Fprintf(w, "%s %d\n", series(name+"_bucket", withLE(k.labels, fmtFloat(b))), cum)
				}
				cum += h.counts[len(DefaultBuckets)]
				fmt.Fprintf(w, "%s %d\n", series(name+"_bucket", withLE(k.labels, "+Inf")), cum)
				fmt.Fprintf(w, "%s %s\n", series(name+"_sum", k.labels), fmtFloat(h.sum))
				fmt.Fprintf(w, "%s %d\n", series(name+"_count", k.labels), h.n)
			}
			continue
		}
		var keys []seriesKey
		for k := range m.vals {
			if k.name == name {
				keys = append(keys, k)
			}
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i].labels < keys[j].labels })
		for _, k := range keys {
			if _, err := fmt.Fprintf(w, "%s %s\n", series(name, k.labels), fmtFloat(m.vals[k])); err != nil {
				return err
			}
		}
	}
	return nil
}
