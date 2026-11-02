// Package cache implements the two level (exact and semantic) response cache.
package cache

import (
	"context"
	"math"
	"time"

	"github.com/aryansaju/smartroute-core/internal/chat"
)

// Key addresses a cache entry. Namespace is the isolation boundary, Exact identifies the full request in it.
type Key struct {
	Namespace string
	Exact     string
}

// Entry is one cached answer together with what is needed to find it again.
type Entry struct {
	Namespace  string
	ExactKey   string
	PromptText string    // the embedded prompt (empty for multi turn entries)
	Embedding  []float32 // nil when the entry is only reachable by exact match
	Completion chat.Completion
	CreatedAt  time.Time
	ExpiresAt  time.Time
}

// Match is a semantic search candidate.
type Match struct {
	Entry      *Entry
	Similarity float64 // cosine similarity in [-1, 1]
}

// Store is the persistent layer. Implementations: MemoryStore (this package) and store.Postgres.
type Store interface {
	// GetExact returns (nil, nil) on a miss.
	GetExact(ctx context.Context, namespace, exactKey string) (*Entry, error)
	// SearchSemantic returns up to k live entries of the namespace, most similar first.
	SearchSemantic(ctx context.Context, namespace string, emb []float32, k int) ([]Match, error)
	Put(ctx context.Context, e *Entry) error
	Ping(ctx context.Context) error
	Close()
}

// Cosine returns the cosine similarity of two vectors, or 0 if they cannot be compared.
func Cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
