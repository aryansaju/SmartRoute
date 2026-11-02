package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aryansaju/smartroute-core/internal/cache"
	"github.com/aryansaju/smartroute-core/internal/chat"
	"github.com/aryansaju/smartroute-core/internal/config"
	"github.com/aryansaju/smartroute-core/internal/embed"
	"github.com/aryansaju/smartroute-core/internal/mockllm"
	"github.com/aryansaju/smartroute-core/internal/provider"
	"github.com/aryansaju/smartroute-core/internal/telemetry"
)

// stubEmbedder returns registered vectors and falls back to the hash embedder for anything else.
type stubEmbedder struct {
	mu       sync.Mutex
	vecs     map[string][]float32
	fallback embed.Embedder
	err      error
}

func (s *stubEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	if v, ok := s.vecs[text]; ok {
		return v, nil
	}
	return s.fallback.Embed(ctx, text)
}

type env struct {
	mock    *mockllm.Server
	store   *cache.MemoryStore
	emb     *stubEmbedder
	srv     *Server
	h       http.Handler
	metrics *telemetry.Metrics
}

func newEnv(t *testing.T, mc mockllm.Config, mutate func(*config.Config)) *env {
	t.Helper()
	m := mockllm.New(mc)
	up := httptest.NewServer(m.Handler())
	t.Cleanup(up.Close)

	cfg, err := config.LoadFrom(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	cfg.SemanticThreshold = 0.9
	if mutate != nil {
		mutate(&cfg)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := cache.NewMemoryStore()
	emb := &stubEmbedder{vecs: map[string][]float32{}, fallback: embed.NewHash(32)}
	c := cache.New(cache.Config{
		SemanticEnabled: cfg.SemanticEnabled, Threshold: cfg.SemanticThreshold, TopK: 3, MaxPromptChars: 1000,
		Guard: cfg.GuardEnabled, TTL: time.Hour, EmbedTimeout: time.Second,
	}, 100, st, emb, log)
	metrics := telemetry.NewMetrics()
	srv := New(Deps{
		Config: cfg, Log: log, Cache: c, Provider: provider.NewOpenAI("mock", up.URL, "", 5*time.Second),
		Writes: nil, // nil means cache writes happen inline, which keeps the tests deterministic
		Metrics: metrics,
	})
	return &env{mock: m, store: st, emb: emb, srv: srv, h: srv.Routes(), metrics: metrics}
}

func body(user string, extra string) string {
	return fmt.Sprintf(`{"model":"mock-llm","messages":[{"role":"user","content":%q}]%s}`, user, extra)
}

func (e *env) post(b string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	e.h.ServeHTTP(rr, req)
	return rr
}

func jsonContent(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	var r struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &r); err != nil || len(r.Choices) != 1 {
		t.Fatalf("not a chat completion: %v\n%s", err, rr.Body.String())
	}
	return r.Choices[0].Message.Content
}

func streamContent(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	acc := chat.NewStreamAccumulator()
	for _, line := range strings.Split(rr.Body.String(), "\n") {
		acc.FeedLine([]byte(line))
	}
	c, ok := acc.Completion()
	if !ok {
		t.Fatalf("incomplete SSE stream:\n%s", rr.Body.String())
	}
	return c.Content
}

func want(t *testing.T, rr *httptest.ResponseRecorder, status int, cacheHeader string) {
	t.Helper()
	if rr.Code != status {
		t.Fatalf("status = %d, want %d\n%s", rr.Code, status, rr.Body.String())
	}
	if got := rr.Header().Get("X-SmartRoute-Cache"); got != cacheHeader {
		t.Fatalf("X-SmartRoute-Cache = %q, want %q", got, cacheHeader)
	}
}

func TestMissThenExactHit(t *testing.T) {
	e := newEnv(t, mockllm.Config{Words: 8}, nil)
	first := e.post(body("hello there", ""), nil)
	want(t, first, 200, "miss")
	if jsonContent(t, first) != mockllm.Answer("hello there", 8) {
		t.Fatal("miss must return the upstream answer unchanged")
	}
	if first.Header().Get("Server-Timing") == "" {
		t.Error("Server-Timing header should expose stage timings")
	}

	second := e.post(body("hello there", ""), nil)
	want(t, second, 200, "hit-l1")
	if jsonContent(t, second) != jsonContent(t, first) {
		t.Fatal("a hit must return the same answer")
	}
	if e.mock.Requests() != 1 {
		t.Fatalf("upstream called %d times, want 1", e.mock.Requests())
	}
}

func TestStreamMissIsPassedThroughAndSatisfiesLaterJSONRequest(t *testing.T) {
	e := newEnv(t, mockllm.Config{Words: 8}, nil)
	s := e.post(body("tell me a story", `,"stream":true`), nil)
	want(t, s, 200, "miss")
	if !strings.Contains(s.Body.String(), "data: [DONE]") {
		t.Fatal("stream must be forwarded to the end")
	}
	got := streamContent(t, s)
	if got != mockllm.Answer("tell me a story", 8) {
		t.Fatalf("streamed content = %q", got)
	}

	j := e.post(body("tell me a story", ""), nil)
	want(t, j, 200, "hit-l1")
	if jsonContent(t, j) != got {
		t.Fatal("a streamed answer must satisfy a non streaming request")
	}
	if e.mock.Requests() != 1 {
		t.Fatalf("upstream called %d times, want 1", e.mock.Requests())
	}
}

func TestCachedAnswerIsReplayedAsAStream(t *testing.T) {
	e := newEnv(t, mockllm.Config{Words: 8}, func(c *config.Config) { c.ReplayChunkChars = 5 })
	want(t, e.post(body("explain caching", ""), nil), 200, "miss")

	s := e.post(body("explain caching", `,"stream":true`), nil)
	want(t, s, 200, "hit-l1")
	if ct := s.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type = %q", ct)
	}
	if streamContent(t, s) != mockllm.Answer("explain caching", 8) {
		t.Fatal("replayed stream must carry the cached answer")
	}
	if strings.Count(s.Body.String(), "data: ") < 5 {
		t.Fatal("replay should be chunked, not one giant event")
	}
	if e.mock.Requests() != 1 {
		t.Fatalf("upstream called %d times, want 1", e.mock.Requests())
	}
}

func TestTenantsNeverShareCachedAnswers(t *testing.T) {
	e := newEnv(t, mockllm.Config{Words: 8}, nil)
	want(t, e.post(body("secret question", ""), map[string]string{"X-SmartRoute-Tenant": "acme"}), 200, "miss")
	want(t, e.post(body("secret question", ""), map[string]string{"X-SmartRoute-Tenant": "globex"}), 200, "miss")
	want(t, e.post(body("secret question", ""), map[string]string{"X-SmartRoute-Tenant": "acme"}), 200, "hit-l1")
	if e.mock.Requests() != 2 {
		t.Fatalf("upstream called %d times, want 2 (one per tenant)", e.mock.Requests())
	}
}

func TestSystemPromptAndParametersIsolateEntries(t *testing.T) {
	e := newEnv(t, mockllm.Config{Words: 8}, nil)
	withSystem := func(sys, user, extra string) string {
		return fmt.Sprintf(`{"model":"mock-llm","messages":[{"role":"system","content":%q},{"role":"user","content":%q}]%s}`, sys, user, extra)
	}
	want(t, e.post(withSystem("be brief", "hi", ""), nil), 200, "miss")
	want(t, e.post(withSystem("be verbose", "hi", ""), nil), 200, "miss")
	want(t, e.post(withSystem("be brief", "hi", `,"temperature":0.9`), nil), 200, "miss")
	want(t, e.post(withSystem("be brief", "hi", ""), nil), 200, "hit-l1")
}

func TestSemanticHitServesParaphrase(t *testing.T) {
	e := newEnv(t, mockllm.Config{Words: 8}, nil)
	e.emb.vecs["How do I reset my password?"] = []float32{1, 0, 0, 0}
	e.emb.vecs["How can I reset my password?"] = []float32{0.99, 0.141, 0, 0}

	first := e.post(body("How do I reset my password?", ""), nil)
	want(t, first, 200, "miss")
	second := e.post(body("How can I reset my password?", ""), nil)
	want(t, second, 200, "hit-semantic")
	if jsonContent(t, second) != jsonContent(t, first) {
		t.Fatal("semantic hit must return the cached answer")
	}
	if sim := second.Header().Get("X-SmartRoute-Similarity"); sim == "" {
		t.Error("semantic hits should report their similarity")
	}
	if e.mock.Requests() != 1 {
		t.Fatalf("upstream called %d times, want 1", e.mock.Requests())
	}
}

func TestGuardBlocksNearDuplicateWithDifferentEntity(t *testing.T) {
	e := newEnv(t, mockllm.Config{Words: 8}, nil)
	e.emb.vecs["What is the capital of France?"] = []float32{1, 0, 0, 0}
	e.emb.vecs["What is the capital of Germany?"] = []float32{0.99, 0.141, 0, 0}

	want(t, e.post(body("What is the capital of France?", ""), nil), 200, "miss")
	want(t, e.post(body("What is the capital of Germany?", ""), nil), 200, "miss")
	if e.mock.Requests() != 2 {
		t.Fatalf("upstream called %d times, want 2", e.mock.Requests())
	}
	var sb strings.Builder
	_ = e.metrics.WriteText(&sb)
	if !strings.Contains(sb.String(), "smartroute_guard_rejections_total 1") {
		t.Errorf("guard rejection should be counted:\n%s", sb.String())
	}
}

func TestMultiTurnConversationsUseExactMatchOnly(t *testing.T) {
	e := newEnv(t, mockllm.Config{Words: 8}, nil)
	multi := `{"model":"mock-llm","messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"user","content":"c"}]}`
	want(t, e.post(multi, nil), 200, "miss")
	want(t, e.post(multi, nil), 200, "hit-l1")
	if e.mock.Requests() != 1 {
		t.Fatalf("upstream called %d times", e.mock.Requests())
	}
}

func TestBypassSkipsReadAndWrite(t *testing.T) {
	e := newEnv(t, mockllm.Config{Words: 8}, nil)
	h := map[string]string{"X-SmartRoute-Cache": "bypass"}
	want(t, e.post(body("q", ""), h), 200, "bypass")
	want(t, e.post(body("q", ""), h), 200, "bypass")
	if e.mock.Requests() != 2 || e.store.Len() != 0 {
		t.Fatalf("bypass must not read or write the cache (upstream=%d stored=%d)", e.mock.Requests(), e.store.Len())
	}
}

func TestUncacheableRequestsAreProxiedButNotStored(t *testing.T) {
	e := newEnv(t, mockllm.Config{Words: 8}, nil)
	tools := body("q", `,"tools":[{"type":"function","function":{"name":"f"}}]`)
	want(t, e.post(tools, nil), 200, "skip")
	want(t, e.post(tools, nil), 200, "skip")
	if e.mock.Requests() != 2 || e.store.Len() != 0 {
		t.Fatalf("tool requests must not be cached (upstream=%d stored=%d)", e.mock.Requests(), e.store.Len())
	}
}

func TestUpstreamErrorsAreForwardedAndNeverCached(t *testing.T) {
	e := newEnv(t, mockllm.Config{Words: 8, ErrorRate: 1, ErrorCode: 503}, nil)
	want(t, e.post(body("q", ""), nil), 503, "miss")
	want(t, e.post(body("q", ""), nil), 503, "miss")
	if e.mock.Requests() != 2 || e.store.Len() != 0 {
		t.Fatalf("errors must not be cached (upstream=%d stored=%d)", e.mock.Requests(), e.store.Len())
	}
	st := e.post(body("q", `,"stream":true`), nil)
	if st.Code != 503 {
		t.Fatalf("streaming upstream error should surface its status, got %d", st.Code)
	}
}

func TestUnreachableUpstreamGivesBadGateway(t *testing.T) {
	e := newEnv(t, mockllm.Config{Words: 8}, nil)
	e.srv.provider = provider.NewOpenAI("dead", "http://127.0.0.1:1", "", time.Second)
	rr := e.post(body("q", ""), nil)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rr.Code)
	}
}

func TestEmbedderFailureDoesNotFailTheRequest(t *testing.T) {
	e := newEnv(t, mockllm.Config{Words: 8}, nil)
	e.emb.err = fmt.Errorf("sidecar down")
	want(t, e.post(body("hello", ""), nil), 200, "miss")
	want(t, e.post(body("hello", ""), nil), 200, "hit-l1") // exact matching still works
}

func TestConcurrentIdenticalRequestsShareOneUpstreamCall(t *testing.T) {
	e := newEnv(t, mockllm.Config{Words: 8, Latency: 200 * time.Millisecond}, nil)
	const n = 12
	var wg sync.WaitGroup
	codes := make([]int, n)
	outcomes := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rr := e.post(body("popular question", ""), nil)
			codes[i], outcomes[i] = rr.Code, rr.Header().Get("X-SmartRoute-Cache")
		}(i)
	}
	wg.Wait()
	for i := range codes {
		if codes[i] != 200 {
			t.Fatalf("request %d failed with %d", i, codes[i])
		}
	}
	if e.mock.Requests() != 1 {
		t.Fatalf("upstream called %d times, want exactly 1 (outcomes: %v)", e.mock.Requests(), outcomes)
	}
}

func TestAuth(t *testing.T) {
	e := newEnv(t, mockllm.Config{Words: 8}, func(c *config.Config) { c.GatewayAPIKey = "s3cret" })
	if rr := e.post(body("q", ""), nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("missing key: %d", rr.Code)
	}
	if rr := e.post(body("q", ""), map[string]string{"Authorization": "Bearer wrong"}); rr.Code != http.StatusUnauthorized {
		t.Fatalf("wrong key: %d", rr.Code)
	}
	if rr := e.post(body("q", ""), map[string]string{"Authorization": "Bearer s3cret"}); rr.Code != 200 {
		t.Fatalf("right key: %d", rr.Code)
	}
}

func TestInvalidRequests(t *testing.T) {
	e := newEnv(t, mockllm.Config{Words: 8}, func(c *config.Config) { c.MaxBodyBytes = 200 })
	for name, tc := range map[string]struct {
		body string
		hdr  map[string]string
		code int
	}{
		"bad json":       {`{`, nil, 400},
		"missing model":  {`{"messages":[{"role":"user","content":"x"}]}`, nil, 400},
		"bad tenant":     {body("q", ""), map[string]string{"X-SmartRoute-Tenant": "bad tenant!"}, 400},
		"body too large": {body(strings.Repeat("x", 500), ""), nil, 413},
	} {
		if rr := e.post(tc.body, tc.hdr); rr.Code != tc.code {
			t.Errorf("%s: status %d, want %d", name, rr.Code, tc.code)
		}
	}
}

func TestHealthReadinessAndMetrics(t *testing.T) {
	e := newEnv(t, mockllm.Config{Words: 8}, nil)
	for _, path := range []string{"/healthz", "/readyz"} {
		rr := httptest.NewRecorder()
		e.h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != 200 {
			t.Errorf("%s: %d", path, rr.Code)
		}
	}
	e.post(body("q", ""), nil)
	e.post(body("q", ""), nil)
	rr := httptest.NewRecorder()
	e.h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	out := rr.Body.String()
	for _, wantLine := range []string{
		`smartroute_requests_total{outcome="miss",stream="false"} 1`,
		`smartroute_requests_total{outcome="hit_l1",stream="false"} 1`,
		"smartroute_estimated_savings_usd_total",
		`smartroute_request_duration_seconds_count{outcome="miss"} 1`,
		`smartroute_stage_duration_seconds_count{stage="upstream"} 1`,
	} {
		if !strings.Contains(out, wantLine) {
			t.Errorf("metrics missing %q\n%s", wantLine, out)
		}
	}
}

func TestRequestIDIsEchoedAndGenerated(t *testing.T) {
	e := newEnv(t, mockllm.Config{Words: 8}, nil)
	if rr := e.post(body("q", ""), map[string]string{"X-Request-Id": "abc123"}); rr.Header().Get("X-Request-Id") != "abc123" {
		t.Error("a client supplied request id should be echoed")
	}
	if rr := e.post(body("q2", ""), nil); rr.Header().Get("X-Request-Id") == "" {
		t.Error("a request id should be generated")
	}
}
