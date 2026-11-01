package cache

import (
	"context"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/<your-user>/smartroute-core/internal/chat"
	"github.com/<your-user>/smartroute-core/internal/embed"
)

// Kind says which layer produced a hit.
type Kind string

// Hit kinds.
const (
	KindL1       Kind = "l1"
	KindExact    Kind = "exact"
	KindSemantic Kind = "semantic"
)

// Config controls cache behaviour.
type Config struct {
	SemanticEnabled bool
	Threshold       float64
	TopK            int
	MaxPromptChars  int
	Guard           bool
	TTL             time.Duration
	EmbedTimeout    time.Duration
}

// Stages records how long each step of a lookup took.
type Stages map[string]time.Duration

// Add accumulates d under name.
func (s Stages) Add(name string, d time.Duration) { s[name] += d }

// Hit is a successful lookup.
type Hit struct {
	Kind          Kind
	Completion    chat.Completion
	Similarity    float64
	MatchedPrompt string
}

// Result is everything a lookup learned, hit or miss. Embedding is reused for the write back so a miss
// costs one embedding call, not two.
type Result struct {
	Hit            *Hit
	Embedding      []float32
	Stages         Stages
	BestSimilarity float64
	GuardRejected  int
	EmbedErr       error
	StoreErr       error
}

// Cache combines an in process LRU, a persistent Store and an Embedder.
type Cache struct {
	cfg   Config
	l1    *LRU
	store Store
	emb   embed.Embedder
	log   *slog.Logger
}

// New builds a Cache. l1Size below 1 disables the in process layer.
func New(cfg Config, l1Size int, store Store, emb embed.Embedder, log *slog.Logger) *Cache {
	if log == nil {
		log = slog.Default()
	}
	if cfg.TopK < 1 {
		cfg.TopK = 1
	}
	return &Cache{cfg: cfg, l1: NewLRU(l1Size), store: store, emb: emb, log: log}
}

// Lookup tries L1, then the exact store, then (if eligible) semantic search. Failures of the store or the
// embedder never fail the request: the lookup degrades to a miss and the error is reported in the Result.
func (c *Cache) Lookup(ctx context.Context, key Key, prompt string, semanticOK bool) *Result {
	res := &Result{Stages: Stages{}}

	t := time.Now()
	if comp, ok := c.l1.Get(key.Exact); ok {
		res.Stages.Add("l1", time.Since(t))
		res.Hit = &Hit{Kind: KindL1, Completion: comp, Similarity: 1}
		return res
	}
	res.Stages.Add("l1", time.Since(t))

	t = time.Now()
	e, err := c.store.GetExact(ctx, key.Namespace, key.Exact)
	res.Stages.Add("exact", time.Since(t))
	if err != nil {
		c.log.Warn("exact lookup failed", "err", err)
		res.StoreErr = err
	} else if e != nil {
		c.l1.Add(key.Exact, e.Completion, time.Until(e.ExpiresAt))
		res.Hit = &Hit{Kind: KindExact, Completion: e.Completion, Similarity: 1, MatchedPrompt: e.PromptText}
		return res
	}

	if !c.cfg.SemanticEnabled || !semanticOK || prompt == "" || utf8.RuneCountInString(prompt) > c.cfg.MaxPromptChars {
		return res
	}

	t = time.Now()
	ectx, cancel := context.WithTimeout(ctx, c.cfg.EmbedTimeout)
	emb, err := c.emb.Embed(ectx, prompt)
	cancel()
	res.Stages.Add("embed", time.Since(t))
	if err != nil {
		c.log.Warn("embedding failed, treating as miss", "err", err)
		res.EmbedErr = err
		return res
	}
	res.Embedding = emb

	t = time.Now()
	matches, err := c.store.SearchSemantic(ctx, key.Namespace, emb, c.cfg.TopK)
	res.Stages.Add("vector", time.Since(t))
	if err != nil {
		c.log.Warn("semantic search failed", "err", err)
		res.StoreErr = err
		return res
	}
	for _, m := range matches {
		if m.Similarity > res.BestSimilarity {
			res.BestSimilarity = m.Similarity
		}
		if m.Similarity < c.cfg.Threshold {
			break // matches are sorted, nothing further can qualify
		}
		if c.cfg.Guard {
			tg := time.Now()
			ok, _ := GuardAllows(prompt, m.Entry.PromptText)
			res.Stages.Add("guard", time.Since(tg))
			if !ok {
				res.GuardRejected++
				continue
			}
		}
		c.l1.Add(key.Exact, m.Entry.Completion, time.Until(m.Entry.ExpiresAt))
		res.Hit = &Hit{Kind: KindSemantic, Completion: m.Entry.Completion, Similarity: m.Similarity, MatchedPrompt: m.Entry.PromptText}
		return res
	}
	return res
}

// Put stores an answer. prompt and emb may be empty for entries that should only be reachable by exact match.
func (c *Cache) Put(ctx context.Context, key Key, prompt string, emb []float32, comp chat.Completion) error {
	now := time.Now()
	c.l1.Add(key.Exact, comp, c.cfg.TTL)
	return c.store.Put(ctx, &Entry{
		Namespace: key.Namespace, ExactKey: key.Exact, PromptText: prompt, Embedding: emb,
		Completion: comp, CreatedAt: now, ExpiresAt: now.Add(c.cfg.TTL),
	})
}

// Ready checks the persistent store.
func (c *Cache) Ready(ctx context.Context) error { return c.store.Ping(ctx) }

// L1Len exposes the size of the in process layer for metrics.
func (c *Cache) L1Len() int { return c.l1.Len() }

// PutL1 stores an answer in the in process layer only. It is cheap enough to call on the request path,
// so an immediate repeat of a request hits even before the asynchronous store write finishes.
func (c *Cache) PutL1(key Key, comp chat.Completion) { c.l1.Add(key.Exact, comp, c.cfg.TTL) }
