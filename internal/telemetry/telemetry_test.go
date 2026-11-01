package telemetry

import (
	"math"
	"strings"
	"testing"
)

func TestMetricsText(t *testing.T) {
	m := NewMetrics()
	m.Describe("req_total", "counter", "Requests.")
	m.Describe("lat_seconds", "histogram", "Latency.")
	m.Add("req_total", Labels("outcome", "miss"), 1)
	m.Add("req_total", Labels("outcome", "miss"), 1)
	m.Add("req_total", Labels("outcome", `a"b`), 1)
	m.Observe("lat_seconds", Labels("outcome", "miss"), 0.0004)
	m.Observe("lat_seconds", Labels("outcome", "miss"), 100)
	m.Set("depth", "", 3)

	var sb strings.Builder
	if err := m.WriteText(&sb); err != nil {
		t.Fatal(err)
	}
	out := sb.String()
	for _, want := range []string{
		"# TYPE req_total counter",
		`req_total{outcome="miss"} 2`,
		`req_total{outcome="a\"b"} 1`,
		"# TYPE lat_seconds histogram",
		`lat_seconds_bucket{outcome="miss",le="0.0005"} 1`,
		`lat_seconds_bucket{outcome="miss",le="+Inf"} 2`,
		`lat_seconds_count{outcome="miss"} 2`,
		"depth 3",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics output is missing %q\n%s", want, out)
		}
	}
}

func TestHistogramBucketsAreCumulative(t *testing.T) {
	m := NewMetrics()
	m.Describe("h", "histogram", "")
	for _, v := range []float64{0.00001, 0.0003, 0.2, 500} {
		m.Observe("h", "", v)
	}
	var sb strings.Builder
	_ = m.WriteText(&sb)
	for _, want := range []string{`h_bucket{le="5e-05"} 1`, `h_bucket{le="0.0005"} 2`, `h_bucket{le="0.25"} 3`, `h_bucket{le="+Inf"} 4`, "h_count 4"} {
		if !strings.Contains(sb.String(), want) {
			t.Errorf("missing %q in\n%s", want, sb.String())
		}
	}
}

func TestPrices(t *testing.T) {
	p, err := ParsePrices(`{"gpt-test":{"input_per_1m":2,"output_per_1m":10}}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Cost("gpt-test", 1_000_000, 500_000); math.Abs(got-7) > 1e-9 {
		t.Errorf("cost = %v, want 7", got)
	}
	if p.Cost("unknown-model", 1000, 1000) != 0 {
		t.Error("unknown models cost 0 rather than a guess")
	}
	if _, ok := p["mock-llm"]; !ok {
		t.Error("defaults must survive merging")
	}
	if _, err := ParsePrices(`{bad`); err == nil {
		t.Error("invalid JSON must be an error")
	}
}
