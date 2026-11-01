// Package api implements the HTTP surface of the gateway.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/<your-user>/smartroute-core/internal/async"
	"github.com/<your-user>/smartroute-core/internal/cache"
	"github.com/<your-user>/smartroute-core/internal/config"
	"github.com/<your-user>/smartroute-core/internal/flight"
	"github.com/<your-user>/smartroute-core/internal/provider"
	"github.com/<your-user>/smartroute-core/internal/telemetry"
)

// Deps are the collaborators a Server needs.
type Deps struct {
	Config   config.Config
	Log      *slog.Logger
	Cache    *cache.Cache
	Provider provider.Provider
	Writes   *async.Queue // cache write back queue
	Metrics  *telemetry.Metrics
	Recorder telemetry.Recorder
	Prices   telemetry.Prices
	Ready    func(ctx context.Context) error // optional extra readiness check (for example the embedder)
}

// Server is the gateway HTTP handler set.
type Server struct {
	cfg      config.Config
	log      *slog.Logger
	cache    *cache.Cache
	provider provider.Provider
	writes   *async.Queue
	metrics  *telemetry.Metrics
	recorder telemetry.Recorder
	prices   telemetry.Prices
	ready    func(ctx context.Context) error
	flight   flight.Group
}

// New builds a Server and registers its metric descriptions.
func New(d Deps) *Server {
	s := &Server{
		cfg: d.Config, log: d.Log, cache: d.Cache, provider: d.Provider, writes: d.Writes,
		metrics: d.Metrics, recorder: d.Recorder, prices: d.Prices, ready: d.Ready,
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.metrics == nil {
		s.metrics = telemetry.NewMetrics()
	}
	if s.recorder == nil {
		s.recorder = telemetry.Nop{}
	}
	if s.prices == nil {
		s.prices = telemetry.DefaultPrices()
	}
	m := s.metrics
	m.Describe("smartroute_requests_total", "counter", "Chat completion requests by cache outcome.")
	m.Describe("smartroute_request_duration_seconds", "histogram", "End to end request latency by outcome.")
	m.Describe("smartroute_stage_duration_seconds", "histogram", "Time spent in each pipeline stage.")
	m.Describe("smartroute_inflight_requests", "gauge", "Requests currently being served.")
	m.Describe("smartroute_estimated_savings_usd_total", "counter", "Estimated provider spend avoided by cache hits (uses PRICES_JSON).")
	m.Describe("smartroute_cache_write_dropped_total", "counter", "Cache writes dropped because the write queue was full.")
	m.Describe("smartroute_guard_rejections_total", "counter", "Semantic hits rejected by the guard.")
	m.Describe("smartroute_embed_errors_total", "counter", "Embedding failures (request fell back to a miss).")
	m.Describe("smartroute_store_errors_total", "counter", "Cache store failures (request fell back to a miss).")
	m.Describe("smartroute_upstream_errors_total", "counter", "Upstream transport failures.")
	m.Describe("smartroute_l1_entries", "gauge", "Entries in the in process cache layer.")
	m.Describe("smartroute_write_queue_depth", "gauge", "Cache writes waiting in the write back queue.")
	return s
}

// Routes returns the gateway's http.Handler.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", s.handleChat)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /readyz", s.handleReady)
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	return s.middleware(mux)
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if s.cache != nil {
		if err := s.cache.Ready(ctx); err != nil {
			writeError(w, http.StatusServiceUnavailable, "not_ready", "cache store unavailable")
			return
		}
	}
	if s.ready != nil {
		if err := s.ready(ctx); err != nil {
			writeError(w, http.StatusServiceUnavailable, "not_ready", "dependency unavailable")
			return
		}
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte("ready"))
}

func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	if s.cache != nil {
		s.metrics.Set("smartroute_l1_entries", "", float64(s.cache.L1Len()))
	}
	if s.writes != nil {
		s.metrics.Set("smartroute_cache_write_dropped_total", "", float64(s.writes.Dropped()))
		s.metrics.Set("smartroute_write_queue_depth", "", float64(s.writes.Depth()))
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_ = s.metrics.WriteText(w)
}

func writeError(w http.ResponseWriter, status int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg, "type": typ}})
}
