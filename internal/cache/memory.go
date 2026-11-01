package cache

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemoryStore keeps everything in process memory. Semantic search is a linear scan.
// It exists for tests and for running the gateway without Postgres; it is not meant for production.
type MemoryStore struct {
	mu      sync.RWMutex
	entries map[string]*Entry
	now     func() time.Time
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{entries: make(map[string]*Entry), now: time.Now}
}

func mkey(namespace, exact string) string { return namespace + "\x00" + exact }

func (m *MemoryStore) live(e *Entry) bool { return e.ExpiresAt.After(m.now()) }

// GetExact implements Store.
func (m *MemoryStore) GetExact(_ context.Context, namespace, exactKey string) (*Entry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e := m.entries[mkey(namespace, exactKey)]
	if e == nil || !m.live(e) {
		return nil, nil
	}
	cp := *e
	return &cp, nil
}

// SearchSemantic implements Store.
func (m *MemoryStore) SearchSemantic(_ context.Context, namespace string, emb []float32, k int) ([]Match, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Match
	for _, e := range m.entries {
		if e.Namespace != namespace || len(e.Embedding) == 0 || !m.live(e) {
			continue
		}
		cp := *e
		out = append(out, Match{Entry: &cp, Similarity: Cosine(emb, e.Embedding)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Similarity > out[j].Similarity })
	if k > 0 && len(out) > k {
		out = out[:k]
	}
	return out, nil
}

// Put implements Store.
func (m *MemoryStore) Put(_ context.Context, e *Entry) error {
	cp := *e
	m.mu.Lock()
	m.entries[mkey(e.Namespace, e.ExactKey)] = &cp
	m.mu.Unlock()
	return nil
}

// Ping implements Store.
func (m *MemoryStore) Ping(context.Context) error { return nil }

// Close implements Store.
func (m *MemoryStore) Close() {}

// Len returns the number of stored entries, including expired ones not yet overwritten.
func (m *MemoryStore) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.entries)
}
