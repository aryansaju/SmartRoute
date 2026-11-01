package embed

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func cosine(a, b []float32) float64 {
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return dot
}

func TestHashEmbedderIsDeterministicAndNormalised(t *testing.T) {
	h := NewHash(64)
	a, _ := h.Embed(context.Background(), "How do I reset my password?")
	b, _ := h.Embed(context.Background(), "How do I reset my password?")
	var norm float64
	for i := range a {
		if a[i] != b[i] {
			t.Fatal("embedding must be deterministic")
		}
		norm += float64(a[i]) * float64(a[i])
	}
	if math.Abs(norm-1) > 1e-5 {
		t.Fatalf("vector is not unit length: %v", norm)
	}
	zero, _ := h.Embed(context.Background(), "   ")
	for _, x := range zero {
		if x != 0 {
			t.Fatal("empty text should give the zero vector")
		}
	}
}

func TestHashEmbedderSimilarityFollowsWordOverlap(t *testing.T) {
	h := NewHash(384)
	base, _ := h.Embed(context.Background(), "how do i reset my password")
	near, _ := h.Embed(context.Background(), "how can i reset my password")
	far, _ := h.Embed(context.Background(), "best pizza toppings in naples")
	if cosine(base, near) <= cosine(base, far) {
		t.Fatal("overlapping prompts must be closer than unrelated ones")
	}
}

func TestHTTPEmbedder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/embed":
			_, _ = w.Write([]byte(`{"embedding":[0.5,0.5,0.5,0.5],"dim":4}`))
		case "/health":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	e := NewHTTP(srv.URL, time.Second)
	v, err := e.Embed(context.Background(), "x")
	if err != nil || len(v) != 4 {
		t.Fatalf("embed: %v %v", v, err)
	}
	if err := e.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestHTTPEmbedderErrors(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", 500) }))
	defer bad.Close()
	if _, err := NewHTTP(bad.URL, time.Second).Embed(context.Background(), "x"); err == nil {
		t.Error("a 500 must be an error")
	}
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"embedding":[]}`)) }))
	defer empty.Close()
	if _, err := NewHTTP(empty.URL, time.Second).Embed(context.Background(), "x"); err == nil {
		t.Error("an empty vector must be an error")
	}
	if _, err := NewHTTP("http://127.0.0.1:1", 200*time.Millisecond).Embed(context.Background(), "x"); err == nil {
		t.Error("an unreachable sidecar must be an error")
	}
}
