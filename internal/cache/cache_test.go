package cache

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/<your-user>/smartroute-core/internal/chat"
)

type stubEmbedder struct {
	vecs  map[string][]float32
	err   error
	calls int
}

func (s *stubEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	v, ok := s.vecs[text]
	if !ok {
		return nil, errors.New("no vector registered for " + text)
	}
	return v, nil
}

type failingStore struct{ *MemoryStore }

func (failingStore) GetExact(context.Context, string, string) (*Entry, error) {
	return nil, errors.New("database down")
}

func (failingStore) SearchSemantic(context.Context, string, []float32, int) ([]Match, error) {
	return nil, errors.New("database down")
}

var (
	vecA = []float32{1, 0, 0, 0}
	vecB = []float32{0.99, 0.141, 0, 0} // cosine with vecA is about 0.99
	vecC = []float32{0, 1, 0, 0}        // orthogonal to vecA
	comp = chat.Completion{Model: "m", Content: "answer", FinishReason: "stop", PromptTokens: 3, CompletionTokens: 2}
)

func testCache(store Store, emb *stubEmbedder) *Cache {
	cfg := Config{SemanticEnabled: true, Threshold: 0.9, TopK: 3, MaxPromptChars: 100, Guard: true, TTL: time.Hour, EmbedTimeout: time.Second}
	return New(cfg, 16, store, emb, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestExactHitL1ThenStore(t *testing.T) {
	store := NewMemoryStore()
	c := testCache(store, &stubEmbedder{})
	key := Key{Namespace: "ns", Exact: "k1"}
	if err := c.Put(context.Background(), key, "", nil, comp); err != nil {
		t.Fatal(err)
	}
	if r := c.Lookup(context.Background(), key, "", false); r.Hit == nil || r.Hit.Kind != KindL1 {
		t.Fatalf("want L1 hit, got %+v", r.Hit)
	}
	cold := testCache(store, &stubEmbedder{}) // new process: empty L1, shared database
	r := cold.Lookup(context.Background(), key, "", false)
	if r.Hit == nil || r.Hit.Kind != KindExact || r.Hit.Completion != comp {
		t.Fatalf("want exact hit from store, got %+v", r.Hit)
	}
	if r := cold.Lookup(context.Background(), key, "", false); r.Hit == nil || r.Hit.Kind != KindL1 {
		t.Fatal("an exact hit must populate L1")
	}
}

func TestSemanticHitAndThreshold(t *testing.T) {
	emb := &stubEmbedder{vecs: map[string][]float32{"how do i reset my password": vecA, "how can i reset my password": vecB, "totally different": vecC}}
	c := testCache(NewMemoryStore(), emb)
	ns := "ns"
	_ = c.Put(context.Background(), Key{ns, "e1"}, "how do i reset my password", vecA, comp)

	r := c.Lookup(context.Background(), Key{ns, "e2"}, "how can i reset my password", true)
	if r.Hit == nil || r.Hit.Kind != KindSemantic || r.Hit.Similarity < 0.98 {
		t.Fatalf("want semantic hit, got %+v", r.Hit)
	}
	if len(r.Embedding) == 0 {
		t.Error("the lookup must return the embedding so the write back can reuse it")
	}

	r = c.Lookup(context.Background(), Key{ns, "e3"}, "totally different", true)
	if r.Hit != nil {
		t.Fatalf("orthogonal prompt must miss, got %+v", r.Hit)
	}
	if r.BestSimilarity > 0.1 {
		t.Errorf("best similarity should be reported even on a miss, got %v", r.BestSimilarity)
	}
}

func TestSemanticNeverCrossesNamespaces(t *testing.T) {
	emb := &stubEmbedder{vecs: map[string][]float32{"p one": vecA, "p two": vecB}}
	c := testCache(NewMemoryStore(), emb)
	_ = c.Put(context.Background(), Key{"tenantA", "e1"}, "p one", vecA, comp)
	if r := c.Lookup(context.Background(), Key{"tenantB", "e2"}, "p two", true); r.Hit != nil {
		t.Fatal("tenant B must never receive tenant A's cached answer")
	}
	if r := c.Lookup(context.Background(), Key{"tenantB", "e1"}, "p one", false); r.Hit != nil {
		t.Fatal("exact keys must be scoped to the namespace as well")
	}
}

func TestGuardRejectsNearDuplicatesWithDifferentEntities(t *testing.T) {
	emb := &stubEmbedder{vecs: map[string][]float32{"What is the capital of France?": vecA, "What is the capital of Germany?": vecB}}
	c := testCache(NewMemoryStore(), emb)
	_ = c.Put(context.Background(), Key{"ns", "e1"}, "What is the capital of France?", vecA, comp)
	r := c.Lookup(context.Background(), Key{"ns", "e2"}, "What is the capital of Germany?", true)
	if r.Hit != nil || r.GuardRejected != 1 {
		t.Fatalf("guard should reject: hit=%+v rejected=%d", r.Hit, r.GuardRejected)
	}

	c.cfg.Guard = false // without the guard the same pair is a (wrong) hit, which is the point of having it
	if r := c.Lookup(context.Background(), Key{"ns", "e2"}, "What is the capital of Germany?", true); r.Hit == nil {
		t.Fatal("expected a hit when the guard is disabled")
	}
}

func TestEmbedderFailureDegradesToMiss(t *testing.T) {
	emb := &stubEmbedder{err: errors.New("sidecar down")}
	c := testCache(NewMemoryStore(), emb)
	r := c.Lookup(context.Background(), Key{"ns", "e1"}, "anything", true)
	if r.Hit != nil || r.EmbedErr == nil {
		t.Fatalf("want a miss with EmbedErr, got %+v / %v", r.Hit, r.EmbedErr)
	}
}

func TestStoreFailureDegradesToMiss(t *testing.T) {
	emb := &stubEmbedder{vecs: map[string][]float32{"q": vecA}}
	c := testCache(failingStore{NewMemoryStore()}, emb)
	r := c.Lookup(context.Background(), Key{"ns", "e1"}, "q", true)
	if r.Hit != nil || r.StoreErr == nil {
		t.Fatalf("want a miss with StoreErr, got %+v / %v", r.Hit, r.StoreErr)
	}
}

func TestSemanticSkippedWhenNotEligible(t *testing.T) {
	emb := &stubEmbedder{vecs: map[string][]float32{"short": vecA}}
	c := testCache(NewMemoryStore(), emb)
	c.Lookup(context.Background(), Key{"ns", "a"}, "short", false) // multi turn
	c.Lookup(context.Background(), Key{"ns", "b"}, strings.Repeat("x", 101), true)
	c.Lookup(context.Background(), Key{"ns", "c"}, "", true)
	if emb.calls != 0 {
		t.Fatalf("embedder was called %d times for ineligible prompts", emb.calls)
	}
	c.cfg.SemanticEnabled = false
	c.Lookup(context.Background(), Key{"ns", "d"}, "short", true)
	if emb.calls != 0 {
		t.Fatal("embedder must not be called when semantic caching is disabled")
	}
}

func TestExpiredEntriesAreNotServed(t *testing.T) {
	store := NewMemoryStore()
	now := time.Now()
	store.now = func() time.Time { return now }
	c := testCache(store, &stubEmbedder{})
	c.cfg.TTL = time.Minute
	c.l1 = NewLRU(0) // isolate the store
	_ = c.Put(context.Background(), Key{"ns", "k"}, "", nil, comp)
	if r := c.Lookup(context.Background(), Key{"ns", "k"}, "", false); r.Hit == nil {
		t.Fatal("entry should be live")
	}
	now = now.Add(2 * time.Minute)
	if r := c.Lookup(context.Background(), Key{"ns", "k"}, "", false); r.Hit != nil {
		t.Fatal("entry should have expired")
	}
}

func TestLRUEvictionAndExpiry(t *testing.T) {
	l := NewLRU(2)
	now := time.Now()
	l.now = func() time.Time { return now }
	l.Add("a", comp, time.Hour)
	l.Add("b", comp, time.Hour)
	l.Get("a") // a is now most recently used
	l.Add("c", comp, time.Hour)
	if _, ok := l.Get("b"); ok {
		t.Error("b should have been evicted")
	}
	if _, ok := l.Get("a"); !ok {
		t.Error("a should have survived")
	}
	now = now.Add(2 * time.Hour)
	if _, ok := l.Get("a"); ok {
		t.Error("a should have expired")
	}
	if NewLRU(0).Len() != 0 {
		t.Error("disabled LRU must stay empty")
	}
}
