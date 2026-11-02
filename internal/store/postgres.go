// Package store holds the PostgreSQL implementation of the cache store and the request log.
package store

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aryansaju/smartroute-core/internal/async"
	"github.com/aryansaju/smartroute-core/internal/cache"
	"github.com/aryansaju/smartroute-core/internal/telemetry"
)

//go:embed schema.sql
var schemaSQL string

// Postgres implements cache.Store on PostgreSQL with the pgvector extension.
type Postgres struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

// Open connects and tunes the pool. ef_search trades recall for speed on every HNSW query.
func Open(ctx context.Context, url string, maxConns, efSearch int, log *slog.Logger) (*Postgres, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	if maxConns > 0 {
		cfg.MaxConns = int32(maxConns)
	}
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		// Best effort: these settings only exist once the extension is loaded and (iterative scan) on pgvector 0.8+.
		_, _ = c.Exec(ctx, "SET hnsw.ef_search = "+strconv.Itoa(efSearch))
		_, _ = c.Exec(ctx, "SET hnsw.iterative_scan = relaxed_order")
		return nil
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &Postgres{pool: pool, log: log}, nil
}

// Migrate applies the embedded schema. It is safe to run on every start.
func (p *Postgres) Migrate(ctx context.Context) error {
	for _, stmt := range strings.Split(schemaSQL, "\n--;;\n") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := p.pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}

// Pool exposes the connection pool (used by the request log recorder).
func (p *Postgres) Pool() *pgxpool.Pool { return p.pool }

// Ping implements cache.Store.
func (p *Postgres) Ping(ctx context.Context) error { return p.pool.Ping(ctx) }

// Close implements cache.Store.
func (p *Postgres) Close() { p.pool.Close() }

func vecLiteral(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(x), 'f', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// GetExact implements cache.Store.
func (p *Postgres) GetExact(ctx context.Context, namespace, exactKey string) (*cache.Entry, error) {
	e := &cache.Entry{Namespace: namespace, ExactKey: exactKey}
	err := p.pool.QueryRow(ctx, `
		SELECT prompt_text, model, content, finish_reason, prompt_tokens, completion_tokens, created_at, expires_at
		FROM cache_entries
		WHERE namespace = $1 AND exact_key = $2 AND expires_at > now()`, namespace, exactKey).
		Scan(&e.PromptText, &e.Completion.Model, &e.Completion.Content, &e.Completion.FinishReason,
			&e.Completion.PromptTokens, &e.Completion.CompletionTokens, &e.CreatedAt, &e.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return e, nil
}

// SearchSemantic implements cache.Store. The namespace filter runs together with the HNSW scan; with
// hnsw.iterative_scan enabled (pgvector 0.8+) the scan continues until enough filtered rows are found.
func (p *Postgres) SearchSemantic(ctx context.Context, namespace string, emb []float32, k int) ([]cache.Match, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT exact_key, prompt_text, model, content, finish_reason, prompt_tokens, completion_tokens,
		       created_at, expires_at, 1 - (embedding <=> $2::vector) AS similarity
		FROM cache_entries
		WHERE namespace = $1 AND embedding IS NOT NULL AND expires_at > now()
		ORDER BY embedding <=> $2::vector
		LIMIT $3`, namespace, vecLiteral(emb), k)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []cache.Match
	for rows.Next() {
		e := &cache.Entry{Namespace: namespace}
		var sim float64
		if err := rows.Scan(&e.ExactKey, &e.PromptText, &e.Completion.Model, &e.Completion.Content, &e.Completion.FinishReason,
			&e.Completion.PromptTokens, &e.Completion.CompletionTokens, &e.CreatedAt, &e.ExpiresAt, &sim); err != nil {
			return nil, err
		}
		out = append(out, cache.Match{Entry: e, Similarity: sim})
	}
	return out, rows.Err()
}

// Put implements cache.Store (upsert).
func (p *Postgres) Put(ctx context.Context, e *cache.Entry) error {
	var emb any // stays nil, so a NULL is stored, for entries without an embedding
	if len(e.Embedding) > 0 {
		emb = vecLiteral(e.Embedding)
	}
	_, err := p.pool.Exec(ctx, `
		INSERT INTO cache_entries (namespace, exact_key, prompt_text, embedding, model, content, finish_reason,
		                           prompt_tokens, completion_tokens, created_at, expires_at)
		VALUES ($1, $2, $3, $4::vector, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (namespace, exact_key) DO UPDATE SET
		    prompt_text = EXCLUDED.prompt_text, embedding = EXCLUDED.embedding, model = EXCLUDED.model,
		    content = EXCLUDED.content, finish_reason = EXCLUDED.finish_reason,
		    prompt_tokens = EXCLUDED.prompt_tokens, completion_tokens = EXCLUDED.completion_tokens,
		    created_at = EXCLUDED.created_at, expires_at = EXCLUDED.expires_at`,
		e.Namespace, e.ExactKey, e.PromptText, emb, e.Completion.Model, e.Completion.Content, e.Completion.FinishReason,
		e.Completion.PromptTokens, e.Completion.CompletionTokens, e.CreatedAt, e.ExpiresAt)
	return err
}

// RunJanitor deletes expired entries until ctx is cancelled.
func (p *Postgres) RunJanitor(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tag, err := p.pool.Exec(ctx, "DELETE FROM cache_entries WHERE expires_at < now()")
			if err != nil {
				p.log.Warn("janitor failed", "err", err)
			} else if n := tag.RowsAffected(); n > 0 {
				p.log.Info("expired cache entries removed", "count", n)
			}
		}
	}
}

// Recorder writes request records to the request_log table through a bounded queue.
type Recorder struct {
	pool *pgxpool.Pool
	q    *async.Queue
	log  *slog.Logger
}

// NewRecorder creates a recorder that owns nothing: the caller closes the queue and the pool.
func NewRecorder(pool *pgxpool.Pool, q *async.Queue, log *slog.Logger) *Recorder {
	return &Recorder{pool: pool, q: q, log: log}
}

// Record implements telemetry.Recorder without blocking.
func (r *Recorder) Record(rec telemetry.RequestRecord) {
	r.q.Submit(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		stages, _ := json.Marshal(rec.StagesMS)
		_, err := r.pool.Exec(ctx, `
			INSERT INTO request_log (request_id, tenant, model, outcome, similarity, status, stream, prompt_tokens,
			                         completion_tokens, est_cost_saved_usd, total_ms, stages)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::jsonb)`,
			rec.RequestID, rec.Tenant, rec.Model, rec.Outcome, rec.Similarity, rec.Status, rec.Stream,
			rec.PromptTokens, rec.CompletionTokens, rec.EstCostSavedUSD, rec.TotalMS, string(stages))
		if err != nil {
			r.log.Warn("request log insert failed", "err", err)
		}
	})
}

var _ cache.Store = (*Postgres)(nil)
var _ telemetry.Recorder = (*Recorder)(nil)
