// Command gateway runs SmartRoute Core: an OpenAI compatible proxy with an exact and semantic response cache.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/<your-user>/smartroute-core/internal/api"
	"github.com/<your-user>/smartroute-core/internal/async"
	"github.com/<your-user>/smartroute-core/internal/cache"
	"github.com/<your-user>/smartroute-core/internal/config"
	"github.com/<your-user>/smartroute-core/internal/embed"
	"github.com/<your-user>/smartroute-core/internal/provider"
	"github.com/<your-user>/smartroute-core/internal/store"
	"github.com/<your-user>/smartroute-core/internal/telemetry"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func newLogger(level string) *slog.Logger {
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(level)); err != nil {
		lv = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lv}))
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := newLogger(cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Storage: PostgreSQL with pgvector in production, memory for quick local experiments.
	var (
		st   cache.Store
		rec  telemetry.Recorder = telemetry.Nop{}
		logQ *async.Queue
	)
	if cfg.DatabaseURL != "" {
		pg, err := store.Open(ctx, cfg.DatabaseURL, cfg.PGMaxConns, cfg.HNSWEfSearch, log)
		if err != nil {
			return err
		}
		if err := pg.Migrate(ctx); err != nil {
			return err
		}
		st = pg
		logQ = async.NewQueue(cfg.WriteQueueSize, 2)
		rec = store.NewRecorder(pg.Pool(), logQ, log)
		go pg.RunJanitor(ctx, 5*time.Minute)
	} else {
		log.Warn("DATABASE_URL is not set: using the in memory store (development only, nothing is persisted)")
		st = cache.NewMemoryStore()
	}

	// Embeddings: the Python sidecar in production, a fake hash embedder for quick local experiments.
	var (
		emb     embed.Embedder
		readyFn func(context.Context) error
	)
	if cfg.EmbedderURL != "" {
		h := embed.NewHTTP(cfg.EmbedderURL, cfg.EmbedTimeout)
		emb, readyFn = h, h.Ping
	} else {
		log.Warn("EMBEDDER_URL is not set: using the fake hash embedder (word overlap only, not semantic)")
		emb = embed.NewHash(384)
	}

	prices, err := telemetry.ParsePrices(cfg.PricesJSON)
	if err != nil {
		return err
	}
	prov := provider.NewOpenAI("upstream", cfg.UpstreamBaseURL, cfg.UpstreamAPIKey, cfg.UpstreamHeaderTimeout)
	c := cache.New(cache.Config{
		SemanticEnabled: cfg.SemanticEnabled,
		Threshold:       cfg.SemanticThreshold,
		TopK:            cfg.SemanticTopK,
		MaxPromptChars:  cfg.SemanticMaxChars,
		Guard:           cfg.GuardEnabled,
		TTL:             cfg.CacheTTL,
		EmbedTimeout:    cfg.EmbedTimeout,
	}, cfg.L1Size, st, emb, log)
	writes := async.NewQueue(cfg.WriteQueueSize, cfg.WriteWorkers)

	srv := api.New(api.Deps{
		Config: cfg, Log: log, Cache: c, Provider: prov, Writes: writes,
		Metrics: telemetry.NewMetrics(), Recorder: rec, Prices: prices, Ready: readyFn,
	})
	httpSrv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout: streamed responses can legitimately run for minutes.
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("gateway listening", "addr", cfg.ListenAddr, "upstream", cfg.UpstreamBaseURL,
			"semantic", cfg.SemanticEnabled, "threshold", cfg.SemanticThreshold, "guard", cfg.GuardEnabled)
		errCh <- httpSrv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Warn("graceful shutdown incomplete", "err", err)
	}
	writes.Close() // finish pending cache writes before the store goes away
	if logQ != nil {
		logQ.Close()
	}
	st.Close()
	return nil
}
