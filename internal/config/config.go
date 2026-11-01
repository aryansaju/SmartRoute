// Package config loads gateway settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds every tunable of the gateway. All fields have safe defaults.
type Config struct {
	ListenAddr      string
	DatabaseURL     string // empty means: use the in memory store (dev and tests only)
	EmbedderURL     string // empty means: use the fake hash embedder (dev and tests only)
	UpstreamBaseURL string // OpenAI compatible base URL, without the /v1 suffix
	UpstreamAPIKey  string
	GatewayAPIKey   string // optional shared key clients must present as a Bearer token

	CacheEnabled      bool
	SemanticEnabled   bool
	SemanticThreshold float64
	SemanticTopK      int
	SemanticMaxChars  int
	GuardEnabled      bool
	CacheTTL          time.Duration
	L1Size            int

	EmbedTimeout          time.Duration
	RequestTimeout        time.Duration
	StreamTimeout         time.Duration
	UpstreamHeaderTimeout time.Duration
	MaxBodyBytes          int64
	MaxUpstreamBytes      int64

	PGMaxConns       int
	HNSWEfSearch     int
	WriteQueueSize   int
	WriteWorkers     int
	ReplayChunkChars int
	LogLevel         string
	PricesJSON       string
}

type loader struct {
	lookup func(string) (string, bool)
	errs   []string
}

func (l *loader) raw(key string) (string, bool) {
	v, ok := l.lookup(key)
	if !ok || strings.TrimSpace(v) == "" {
		return "", false
	}
	return strings.TrimSpace(v), true
}

func (l *loader) str(key, def string) string {
	if v, ok := l.raw(key); ok {
		return v
	}
	return def
}

func (l *loader) boolean(key string, def bool) bool {
	v, ok := l.raw(key)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		l.errs = append(l.errs, fmt.Sprintf("%s: %v", key, err))
		return def
	}
	return b
}

func (l *loader) integer(key string, def int) int {
	v, ok := l.raw(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		l.errs = append(l.errs, fmt.Sprintf("%s: %v", key, err))
		return def
	}
	return n
}

func (l *loader) int64v(key string, def int64) int64 {
	v, ok := l.raw(key)
	if !ok {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		l.errs = append(l.errs, fmt.Sprintf("%s: %v", key, err))
		return def
	}
	return n
}

func (l *loader) float(key string, def float64) float64 {
	v, ok := l.raw(key)
	if !ok {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		l.errs = append(l.errs, fmt.Sprintf("%s: %v", key, err))
		return def
	}
	return f
}

func (l *loader) duration(key string, def time.Duration) time.Duration {
	v, ok := l.raw(key)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		l.errs = append(l.errs, fmt.Sprintf("%s: %v", key, err))
		return def
	}
	return d
}

// Load reads the process environment.
func Load() (Config, error) { return LoadFrom(os.LookupEnv) }

// LoadFrom reads settings through lookup, which makes configuration easy to test.
func LoadFrom(lookup func(string) (string, bool)) (Config, error) {
	l := &loader{lookup: lookup}
	c := Config{
		ListenAddr:      l.str("LISTEN_ADDR", ":8080"),
		DatabaseURL:     l.str("DATABASE_URL", ""),
		EmbedderURL:     l.str("EMBEDDER_URL", ""),
		UpstreamBaseURL: strings.TrimRight(l.str("UPSTREAM_BASE_URL", "http://localhost:9000"), "/"),
		UpstreamAPIKey:  l.str("UPSTREAM_API_KEY", ""),
		GatewayAPIKey:   l.str("GATEWAY_API_KEY", ""),

		CacheEnabled:      l.boolean("CACHE_ENABLED", true),
		SemanticEnabled:   l.boolean("SEMANTIC_ENABLED", true),
		SemanticThreshold: l.float("SEMANTIC_THRESHOLD", 0.92),
		SemanticTopK:      l.integer("SEMANTIC_TOP_K", 3),
		SemanticMaxChars:  l.integer("SEMANTIC_MAX_CHARS", 1000),
		GuardEnabled:      l.boolean("GUARD_ENABLED", true),
		CacheTTL:          l.duration("CACHE_TTL", 24*time.Hour),
		L1Size:            l.integer("L1_SIZE", 10000),

		EmbedTimeout:          l.duration("EMBED_TIMEOUT", time.Second),
		RequestTimeout:        l.duration("REQUEST_TIMEOUT", 120*time.Second),
		StreamTimeout:         l.duration("STREAM_TIMEOUT", 5*time.Minute),
		UpstreamHeaderTimeout: l.duration("UPSTREAM_HEADER_TIMEOUT", 120*time.Second),
		MaxBodyBytes:          l.int64v("MAX_BODY_BYTES", 1<<20),
		MaxUpstreamBytes:      l.int64v("MAX_UPSTREAM_BYTES", 8<<20),

		PGMaxConns:       l.integer("PG_MAX_CONNS", 20),
		HNSWEfSearch:     l.integer("HNSW_EF_SEARCH", 40),
		WriteQueueSize:   l.integer("WRITE_QUEUE_SIZE", 1024),
		WriteWorkers:     l.integer("WRITE_WORKERS", 4),
		ReplayChunkChars: l.integer("REPLAY_CHUNK_CHARS", 24),
		LogLevel:         l.str("LOG_LEVEL", "info"),
		PricesJSON:       l.str("PRICES_JSON", ""),
	}
	if len(l.errs) > 0 {
		return c, errors.New(strings.Join(l.errs, "; "))
	}
	if c.SemanticThreshold <= 0 || c.SemanticThreshold > 1 {
		return c, fmt.Errorf("SEMANTIC_THRESHOLD must be in (0, 1], got %v", c.SemanticThreshold)
	}
	if c.SemanticTopK < 1 {
		return c, errors.New("SEMANTIC_TOP_K must be at least 1")
	}
	return c, nil
}
