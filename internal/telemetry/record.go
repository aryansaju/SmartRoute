package telemetry

import (
	"encoding/json"
	"fmt"
)

// RequestRecord is one row of request level telemetry.
type RequestRecord struct {
	RequestID        string
	Tenant           string
	Model            string
	Outcome          string // hit_l1, hit_exact, hit_semantic, miss, coalesced, bypass, skip, disabled, error
	Similarity       float64
	Status           int
	Stream           bool
	PromptTokens     int
	CompletionTokens int
	EstCostSavedUSD  float64
	TotalMS          float64
	StagesMS         map[string]float64
}

// Recorder persists request records. Implementations must not block the caller.
type Recorder interface {
	Record(rec RequestRecord)
}

// Nop discards records.
type Nop struct{}

// Record implements Recorder.
func (Nop) Record(RequestRecord) {}

// Price is a cost in US dollars per one million tokens.
type Price struct {
	InputPer1M  float64 `json:"input_per_1m"`
	OutputPer1M float64 `json:"output_per_1m"`
}

// Prices maps a model name to its price. Real provider prices change often, so the gateway ships only a
// placeholder for its own mock model and expects real prices through the PRICES_JSON setting.
type Prices map[string]Price

// DefaultPrices contains only the mock model, so dashboards show something in a demo.
func DefaultPrices() Prices {
	return Prices{"mock-llm": {InputPer1M: 0.15, OutputPer1M: 0.60}}
}

// ParsePrices reads {"model": {"input_per_1m": 1.0, "output_per_1m": 2.0}} and merges it over the defaults.
func ParsePrices(raw string) (Prices, error) {
	p := DefaultPrices()
	if raw == "" {
		return p, nil
	}
	var extra map[string]Price
	if err := json.Unmarshal([]byte(raw), &extra); err != nil {
		return nil, fmt.Errorf("PRICES_JSON: %w", err)
	}
	for k, v := range extra {
		p[k] = v
	}
	return p, nil
}

// Cost returns the estimated dollar cost of a request, or 0 for an unknown model.
func (p Prices) Cost(model string, promptTokens, completionTokens int) float64 {
	pr, ok := p[model]
	if !ok {
		return 0
	}
	return float64(promptTokens)/1e6*pr.InputPer1M + float64(completionTokens)/1e6*pr.OutputPer1M
}
