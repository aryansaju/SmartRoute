// Package provider talks to upstream LLM APIs.
package provider

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"strings"
	"time"
)

// Provider sends a chat completions request body upstream and returns the raw HTTP response.
// The caller owns the response body and must close it. Keeping the interface this small lets the routing
// and failover layer (a later milestone) wrap several providers behind the same shape.
type Provider interface {
	Name() string
	Do(ctx context.Context, body []byte) (*http.Response, error)
}

// OpenAI speaks the OpenAI chat completions protocol over plain HTTP. It works with OpenAI itself and with any
// compatible server, including the bundled mock upstream.
type OpenAI struct {
	name    string
	baseURL string
	apiKey  string
	client  *http.Client
}

// NewOpenAI creates an adapter. baseURL has no /v1 suffix (for example https://api.openai.com).
// headerTimeout bounds the wait for response headers; streaming bodies are bounded by the request context instead.
func NewOpenAI(name, baseURL, apiKey string, headerTimeout time.Duration) *OpenAI {
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          1024,
		MaxIdleConnsPerHost:   512,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: headerTimeout,
	}
	return &OpenAI{name: name, baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, client: &http.Client{Transport: tr}}
}

// Name implements Provider.
func (p *OpenAI) Name() string { return p.name }

// Do implements Provider. The gateway's own upstream key replaces whatever credentials the client sent.
func (p *OpenAI) Do(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	return p.client.Do(req)
}
