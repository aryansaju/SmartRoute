// Package mockllm is a deterministic, OpenAI compatible fake LLM used for benchmarks and tests.
//
// Real providers are slow, noisy and cost money, which makes gateway overhead impossible to measure.
// The mock answers with a fixed latency and counts every request, so tests can prove the gateway did not
// call upstream and benchmarks can subtract a known constant.
package mockllm

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/<your-user>/smartroute-core/internal/chat"
)

// Config controls the mock's behaviour.
type Config struct {
	Latency    time.Duration // delay before the first byte
	Words      int           // words in each answer
	WordDelay  time.Duration // delay between streamed words
	ErrorRate  float64       // fraction of requests answered with ErrorCode
	ErrorCode  int
	ModelName  string // model name echoed in responses
	FailAfterN int64  // when above zero, every request after the first N fails with ErrorCode
}

// Server is the mock upstream.
type Server struct {
	cfg      Config
	requests atomic.Int64
	counter  atomic.Int64
}

// New returns a mock with sensible defaults filled in.
func New(cfg Config) *Server {
	if cfg.Words < 1 {
		cfg.Words = 24
	}
	if cfg.ErrorCode == 0 {
		cfg.ErrorCode = http.StatusInternalServerError
	}
	if cfg.ModelName == "" {
		cfg.ModelName = "mock-llm"
	}
	return &Server{cfg: cfg}
}

// Requests returns how many chat completion requests have arrived.
func (s *Server) Requests() int64 { return s.requests.Load() }

// Reset zeroes the request counter.
func (s *Server) Reset() { s.requests.Store(0); s.counter.Store(0) }

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", s.handleChat)
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int64{"requests": s.Requests()})
	})
	mux.HandleFunc("POST /reset", func(w http.ResponseWriter, _ *http.Request) {
		s.Reset()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	return mux
}

var vocabulary = strings.Fields(`alpha river signal garden window planet quiet engine bridge copper meadow harbor
lantern pattern compass thunder velvet orchard canvas ember falcon glacier horizon island jungle kernel
ladder marble nectar oracle pebble quartz ripple summit timber umbrella valley whisper yonder zephyr`)

// Answer returns the deterministic answer for a prompt. Exposed so tests can predict responses.
func Answer(prompt string, words int) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(prompt))
	seed := h.Sum64()
	out := make([]string, 0, words+4)
	out = append(out, "Mock", "answer:")
	for i := 0; i < words; i++ {
		seed ^= seed << 13
		seed ^= seed >> 7
		seed ^= seed << 17
		out = append(out, vocabulary[seed%uint64(len(vocabulary))])
	}
	return strings.Join(out, " ")
}

func lastUser(req *chat.Request) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			t, _ := chat.TextOf(req.Messages[i].Content)
			return t
		}
	}
	return ""
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	n := s.requests.Add(1)
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, `{"error":{"message":"read error"}}`, http.StatusBadRequest)
		return
	}
	req, perr := chat.Parse(body)
	if perr != nil {
		http.Error(w, `{"error":{"message":"bad request"}}`, http.StatusBadRequest)
		return
	}

	select {
	case <-time.After(s.cfg.Latency):
	case <-r.Context().Done():
		return
	}

	failing := s.cfg.FailAfterN > 0 && n > s.cfg.FailAfterN
	if !failing && s.cfg.ErrorRate > 0 {
		failing = float64(s.counter.Add(1)%1000)/1000 < s.cfg.ErrorRate
	}
	if failing {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(s.cfg.ErrorCode)
		_, _ = w.Write([]byte(`{"error":{"message":"mock upstream failure","type":"mock_error"}}`))
		return
	}

	prompt := lastUser(req)
	content := Answer(prompt, s.cfg.Words)
	promptTokens := 0
	for _, m := range req.Messages {
		t, _ := chat.TextOf(m.Content)
		promptTokens += chat.EstimateTokens(t)
	}
	completionTokens := s.cfg.Words + 2
	id := fmt.Sprintf("chatcmpl-mock-%d", n)
	created := time.Now().Unix()

	if !req.Stream {
		comp := chat.Completion{Model: s.cfg.ModelName, Content: content, FinishReason: "stop", PromptTokens: promptTokens, CompletionTokens: completionTokens}
		b, _ := comp.JSON(id, created)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	comp := chat.Completion{Model: s.cfg.ModelName, Content: content, FinishReason: "stop", PromptTokens: promptTokens, CompletionTokens: completionTokens}
	// One chunk per word, so the stream has realistic pacing when WordDelay is set.
	frames := comp.StreamFrames(id, created, 1<<30, req.WantsUsage())
	words := strings.SplitAfter(content, " ")
	if s.cfg.WordDelay > 0 && len(words) > 1 {
		frames = wordFrames(comp, id, created, words, req.WantsUsage())
	}
	for _, f := range frames {
		if _, err := w.Write(f); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
		if s.cfg.WordDelay > 0 {
			select {
			case <-time.After(s.cfg.WordDelay):
			case <-r.Context().Done():
				return
			}
		}
	}
}

// wordFrames builds one SSE frame per word by reusing the chunk renderer on single word completions.
func wordFrames(c chat.Completion, id string, created int64, words []string, usage bool) [][]byte {
	all := c.StreamFrames(id, created, 1<<30, usage)
	// frames layout: role chunk, content chunk, finish chunk, [usage chunk], [DONE]
	if len(all) < 4 {
		return all
	}
	out := [][]byte{all[0]}
	for _, w := range words {
		if w == "" {
			continue
		}
		piece := chat.Completion{Model: c.Model, Content: w, FinishReason: "stop"}
		pf := piece.StreamFrames(id, created, 1<<30, false)
		out = append(out, pf[1]) // the single content chunk
	}
	return append(out, all[2:]...)
}
