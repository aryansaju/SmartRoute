// Package embed turns prompts into vectors for the semantic cache.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode"
)

// Embedder converts text to a vector. Implementations must return L2 normalised vectors of a fixed size.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// HTTP calls the Python embedding sidecar (see sidecar/embedder).
type HTTP struct {
	url    string
	client *http.Client
}

// NewHTTP builds a client for the sidecar at baseURL, for example http://embedder:8001.
func NewHTTP(baseURL string, timeout time.Duration) *HTTP {
	tr := &http.Transport{
		MaxIdleConns:        256,
		MaxIdleConnsPerHost: 128,
		IdleConnTimeout:     90 * time.Second,
	}
	return &HTTP{url: strings.TrimRight(baseURL, "/"), client: &http.Client{Transport: tr, Timeout: timeout}}
}

// Embed implements Embedder.
func (h *HTTP) Embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url+"/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedder request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("embedder returned status %d", resp.StatusCode)
	}
	var out struct {
		Embedding []float32 `json:"embedding"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("embedder response: %w", err)
	}
	if len(out.Embedding) == 0 {
		return nil, errors.New("embedder returned an empty vector")
	}
	return out.Embedding, nil
}

// Ping checks that the sidecar is reachable and healthy.
func (h *HTTP) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.url+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("embedder health status %d", resp.StatusCode)
	}
	return nil
}

// Hash is a deterministic offline embedder built from hashed word unigrams and bigrams.
// It captures word overlap, not meaning. Use it for development and tests only, never for benchmarks of
// semantic quality.
type Hash struct{ dim int }

// NewHash returns a Hash embedder producing vectors of the given size (default 384).
func NewHash(dim int) *Hash {
	if dim < 8 {
		dim = 384
	}
	return &Hash{dim: dim}
}

// Embed implements Embedder.
func (h *Hash) Embed(_ context.Context, text string) ([]float32, error) {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	v := make([]float64, h.dim)
	add := func(feature string) {
		f := fnv.New32a()
		_, _ = f.Write([]byte(feature))
		sum := f.Sum32()
		sign := 1.0
		if sum&0x80000000 != 0 {
			sign = -1.0
		}
		v[int(sum%uint32(h.dim))] += sign
	}
	for i, w := range words {
		add(w)
		if i+1 < len(words) {
			add(w + "_" + words[i+1])
		}
	}
	var norm float64
	for _, x := range v {
		norm += x * x
	}
	out := make([]float32, h.dim)
	if norm == 0 {
		return out, nil
	}
	norm = math.Sqrt(norm)
	for i, x := range v {
		out[i] = float32(x / norm)
	}
	return out, nil
}
