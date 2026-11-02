package api

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aryansaju/smartroute-core/internal/cache"
	"github.com/aryansaju/smartroute-core/internal/chat"
	"github.com/aryansaju/smartroute-core/internal/telemetry"
)

var (
	tenantRe    = regexp.MustCompile(`^[A-Za-z0-9_.:@]{1,64}$`)
	errTooLarge = errors.New("upstream response too large")
)

// writeBack is what a miss needs to remember so the answer can be cached afterwards.
type writeBack struct {
	key    cache.Key
	prompt string
	emb    []float32
}

// call bundles everything known about one request.
type call struct {
	id     string
	tenant string
	start  time.Time
	body   []byte
	req    *chat.Request
	stages cache.Stages
	wb     *writeBack // nil when the request must not be cached
}

type upstreamResult struct {
	Status      int
	ContentType string
	Body        []byte
	Completion  *chat.Completion
}

func (s *Server) authorized(r *http.Request) bool {
	if s.cfg.GatewayAPIKey == "" {
		return true
	}
	h := r.Header.Get("Authorization")
	tok := strings.TrimPrefix(h, "Bearer ")
	if tok == h {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(tok), []byte(s.cfg.GatewayAPIKey)) == 1
}

func serverTiming(st cache.Stages) string {
	names := make([]string, 0, len(st))
	for n := range st {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, fmt.Sprintf("%s;dur=%.3f", n, float64(st[n])/float64(time.Millisecond)))
	}
	return strings.Join(parts, ", ")
}

// handleChat is POST /v1/chat/completions.
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	c := &call{id: RequestID(r.Context()), start: time.Now(), stages: cache.Stages{}}

	if !s.authorized(r) {
		writeError(w, http.StatusUnauthorized, "invalid_api_key", "missing or invalid API key")
		return
	}
	c.tenant = r.Header.Get("X-SmartRoute-Tenant")
	if c.tenant == "" {
		c.tenant = "default"
	}
	if !tenantRe.MatchString(c.tenant) {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid X-SmartRoute-Tenant header")
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.cfg.MaxBodyBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request_error", "could not read request body")
		return
	}
	req, err := chat.Parse(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	c.body, c.req = body, req

	outcome := "miss"
	cacheable, _ := req.Cacheable()
	switch {
	case !s.cfg.CacheEnabled || s.cache == nil:
		outcome = "disabled"
	case strings.EqualFold(r.Header.Get("X-SmartRoute-Cache"), "bypass"):
		outcome = "bypass"
	case !cacheable:
		outcome = "skip"
	default:
		t := time.Now()
		ns := req.Namespace(c.tenant)
		key := cache.Key{Namespace: ns, Exact: req.ExactKey(ns)}
		prompt, semanticOK := req.SemanticPrompt()
		c.stages.Add("hash", time.Since(t))

		res := s.cache.Lookup(r.Context(), key, prompt, semanticOK)
		for name, d := range res.Stages {
			c.stages.Add(name, d)
		}
		if res.GuardRejected > 0 {
			s.metrics.Add("smartroute_guard_rejections_total", "", float64(res.GuardRejected))
		}
		if res.EmbedErr != nil {
			s.metrics.Add("smartroute_embed_errors_total", "", 1)
		}
		if res.StoreErr != nil {
			s.metrics.Add("smartroute_store_errors_total", "", 1)
		}
		if res.Hit != nil {
			s.serveHit(w, c, res.Hit)
			return
		}
		c.wb = &writeBack{key: key, prompt: prompt, emb: res.Embedding}
	}

	if req.Stream {
		s.proxyStream(w, r, c, outcome)
		return
	}
	s.proxyJSON(w, r, c, outcome)
}

func (s *Server) serveHit(w http.ResponseWriter, c *call, hit *cache.Hit) {
	outcome := "hit_" + string(hit.Kind)
	h := w.Header()
	h.Set("X-SmartRoute-Cache", "hit-"+string(hit.Kind))
	if hit.Kind == cache.KindSemantic {
		h.Set("X-SmartRoute-Similarity", strconv.FormatFloat(hit.Similarity, 'f', 4, 64))
	}
	id := "chatcmpl-sr-" + c.id
	created := time.Now().Unix()
	t := time.Now()

	if c.req.Stream {
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, f := range hit.Completion.StreamFrames(id, created, s.cfg.ReplayChunkChars, c.req.WantsUsage()) {
			if _, err := w.Write(f); err != nil {
				break
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	} else {
		b, err := hit.Completion.JSON(id, created)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "server_error", "could not render cached response")
			return
		}
		h.Set("Content-Type", "application/json")
		h.Set("Server-Timing", serverTiming(c.stages))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(b)
	}
	c.stages.Add("respond", time.Since(t))

	saved := s.prices.Cost(c.req.Model, hit.Completion.PromptTokens, hit.Completion.CompletionTokens)
	comp := hit.Completion
	s.finish(c, outcome, http.StatusOK, hit.Similarity, &comp, saved)
}

func (s *Server) proxyJSON(w http.ResponseWriter, r *http.Request, c *call, outcome string) {
	run := func(parent context.Context) (any, error) {
		ctx, cancel := context.WithTimeout(parent, s.cfg.RequestTimeout)
		defer cancel()
		return s.fetchJSON(ctx, c)
	}

	t := time.Now()
	var (
		v      any
		err    error
		leader = true
	)
	if c.wb != nil {
		// Identical requests that arrive while one is in flight share a single upstream call.
		// The leader's upstream call is detached from its client so a disconnect does not fail the followers.
		v, leader, err = s.flight.Do(c.wb.key.Exact, func() (any, error) {
			return run(context.WithoutCancel(r.Context()))
		})
	} else {
		v, err = run(r.Context())
	}
	c.stages.Add("upstream", time.Since(t))
	if err != nil {
		s.upstreamFailure(w, c, err)
		return
	}
	res := v.(*upstreamResult)
	if !leader {
		outcome = "coalesced"
	}

	h := w.Header()
	if res.ContentType != "" {
		h.Set("Content-Type", res.ContentType)
	} else {
		h.Set("Content-Type", "application/json")
	}
	h.Set("X-SmartRoute-Cache", outcome)
	h.Set("Server-Timing", serverTiming(c.stages))
	w.WriteHeader(res.Status)
	_, _ = w.Write(res.Body)
	s.finish(c, outcome, res.Status, 0, res.Completion, 0)
}

func (s *Server) fetchJSON(ctx context.Context, c *call) (*upstreamResult, error) {
	resp, err := s.provider.Do(ctx, c.body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, s.cfg.MaxUpstreamBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > s.cfg.MaxUpstreamBytes {
		return nil, errTooLarge
	}
	res := &upstreamResult{Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: data}
	if resp.StatusCode == http.StatusOK {
		if comp, perr := chat.ParseCompletion(data); perr == nil {
			res.Completion = &comp
			s.writeBack(c, comp)
		}
	}
	return res, nil
}

func (s *Server) proxyStream(w http.ResponseWriter, r *http.Request, c *call, outcome string) {
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.StreamTimeout)
	defer cancel()

	t := time.Now()
	resp, err := s.provider.Do(ctx, c.body)
	if err != nil {
		s.upstreamFailure(w, c, err)
		return
	}
	defer resp.Body.Close()
	c.stages.Add("upstream_headers", time.Since(t))

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if ct := resp.Header.Get("Content-Type"); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.Header().Set("X-SmartRoute-Cache", outcome)
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(data)
		s.finish(c, outcome, resp.StatusCode, 0, nil, 0)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	h.Set("X-SmartRoute-Cache", outcome)
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}

	acc := chat.NewStreamAccumulator()
	reader := bufio.NewReaderSize(resp.Body, 64*1024)
	first, clientGone := true, false
	for {
		line, rerr := reader.ReadBytes('\n')
		if len(line) > 0 {
			if first {
				c.stages.Add("first_byte", time.Since(t))
				first = false
			}
			if _, werr := w.Write(line); werr != nil {
				clientGone = true
				break
			}
			if flusher != nil && len(bytes.TrimSpace(line)) == 0 {
				flusher.Flush() // a blank line ends one SSE event
			}
			acc.FeedLine(line)
		}
		if rerr != nil {
			break
		}
	}
	c.stages.Add("upstream_total", time.Since(t))

	var comp *chat.Completion
	if !clientGone && acc.Done() {
		if cc, ok := acc.Completion(); ok {
			comp = &cc
			s.writeBack(c, cc)
		}
	}
	s.finish(c, outcome, http.StatusOK, 0, comp, 0)
}

// writeBack caches an answer. The in process layer is updated immediately; the store write is asynchronous
// and dropped (and counted) if the queue is full.
func (s *Server) writeBack(c *call, comp chat.Completion) {
	wb := c.wb
	if wb == nil || s.cache == nil {
		return
	}
	s.cache.PutL1(wb.key, comp)
	put := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.cache.Put(ctx, wb.key, wb.prompt, wb.emb, comp); err != nil {
			s.log.Warn("cache write failed", "err", err)
			s.metrics.Add("smartroute_store_errors_total", "", 1)
		}
	}
	if s.writes == nil {
		put()
		return
	}
	s.writes.Submit(put)
}

func (s *Server) upstreamFailure(w http.ResponseWriter, c *call, err error) {
	if errors.Is(err, context.Canceled) {
		s.finish(c, "error", 499, 0, nil, 0) // the client went away; nobody is listening for a response
		return
	}
	s.metrics.Add("smartroute_upstream_errors_total", "", 1)
	status, msg := http.StatusBadGateway, "upstream request failed"
	if errors.Is(err, context.DeadlineExceeded) {
		status, msg = http.StatusGatewayTimeout, "upstream timed out"
	}
	s.log.Warn("upstream failure", "request_id", c.id, "err", err)
	writeError(w, status, "upstream_error", msg)
	s.finish(c, "error", status, 0, nil, 0)
}

func (s *Server) finish(c *call, outcome string, status int, similarity float64, comp *chat.Completion, saved float64) {
	total := time.Since(c.start)
	stream := strconv.FormatBool(c.req.Stream)
	s.metrics.Add("smartroute_requests_total", telemetry.Labels("outcome", outcome, "stream", stream), 1)
	s.metrics.Observe("smartroute_request_duration_seconds", telemetry.Labels("outcome", outcome), total.Seconds())

	stagesMS := make(map[string]float64, len(c.stages))
	for name, d := range c.stages {
		s.metrics.Observe("smartroute_stage_duration_seconds", telemetry.Labels("stage", name), d.Seconds())
		stagesMS[name] = float64(d) / float64(time.Millisecond)
	}
	if saved > 0 {
		s.metrics.Add("smartroute_estimated_savings_usd_total", "", saved)
	}

	rec := telemetry.RequestRecord{
		RequestID: c.id, Tenant: c.tenant, Model: c.req.Model, Outcome: outcome, Similarity: similarity,
		Status: status, Stream: c.req.Stream, EstCostSavedUSD: saved,
		TotalMS: float64(total) / float64(time.Millisecond), StagesMS: stagesMS,
	}
	if comp != nil {
		rec.PromptTokens, rec.CompletionTokens = comp.PromptTokens, comp.CompletionTokens
	}
	s.recorder.Record(rec)
	s.log.Debug("request", "request_id", c.id, "tenant", c.tenant, "outcome", outcome, "status", status, "total_ms", rec.TotalMS)
}
