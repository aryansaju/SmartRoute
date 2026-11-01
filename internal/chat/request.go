// Package chat models the subset of the OpenAI chat completions API the gateway understands.
//
// The gateway forwards the original request bytes untouched. This package only extracts what is needed
// to decide whether a request can be cached and under which key.
package chat

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrInvalid is wrapped by every parse failure so callers can answer with HTTP 400.
var ErrInvalid = errors.New("invalid chat completion request")

// Message is one chat message. Content is kept raw because it may be a string or a list of parts.
type Message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// StreamOptions mirrors the stream_options request field.
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// Request is a parsed chat completions request.
type Request struct {
	Model               string          `json:"model"`
	Messages            []Message       `json:"messages"`
	Stream              bool            `json:"stream"`
	StreamOptions       *StreamOptions  `json:"stream_options"`
	Temperature         *float64        `json:"temperature"`
	TopP                *float64        `json:"top_p"`
	MaxTokens           *int            `json:"max_tokens"`
	MaxCompletionTokens *int            `json:"max_completion_tokens"`
	N                   *int            `json:"n"`
	Seed                *int64          `json:"seed"`
	Stop                json.RawMessage `json:"stop"`
	ResponseFormat      json.RawMessage `json:"response_format"`
	Tools               json.RawMessage `json:"tools"`
	Functions           json.RawMessage `json:"functions"`
}

// Parse decodes and validates a request body.
func Parse(body []byte) (*Request, error) {
	var r Request
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if strings.TrimSpace(r.Model) == "" {
		return nil, fmt.Errorf("%w: model is required", ErrInvalid)
	}
	if len(r.Messages) == 0 {
		return nil, fmt.Errorf("%w: messages is required", ErrInvalid)
	}
	return &r, nil
}

// TextOf returns the plain text of a message content. ok is false when the content has non text parts
// (images, audio) or cannot be understood.
func TextOf(raw json.RawMessage) (text string, ok bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, true
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", false
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Type != "text" {
			return "", false
		}
		b.WriteString(p.Text)
	}
	return b.String(), true
}

func present(raw json.RawMessage) bool {
	t := strings.TrimSpace(string(raw))
	return t != "" && t != "null"
}

// Cacheable reports whether the response to this request may be cached. The reason is a short code for logs.
func (r *Request) Cacheable() (ok bool, reason string) {
	if r.N != nil && *r.N > 1 {
		return false, "n_greater_than_one"
	}
	if present(r.Tools) || present(r.Functions) {
		return false, "tools"
	}
	for _, m := range r.Messages {
		if m.Role == "tool" || m.Role == "function" {
			return false, "tool_message"
		}
		if _, ok := TextOf(m.Content); !ok {
			return false, "non_text_content"
		}
	}
	return true, ""
}

func isSystemRole(role string) bool { return role == "system" || role == "developer" }

// SystemText joins all system and developer messages.
func (r *Request) SystemText() string {
	var parts []string
	for _, m := range r.Messages {
		if isSystemRole(m.Role) {
			t, _ := TextOf(m.Content)
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n")
}

func fmtFloat(p *float64) string {
	if p == nil {
		return "default"
	}
	return strconv.FormatFloat(*p, 'g', -1, 64)
}

func fmtInt(p *int) string {
	if p == nil {
		return "default"
	}
	return strconv.Itoa(*p)
}

func compactRaw(raw json.RawMessage) string {
	if !present(raw) {
		return "none"
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}

// ParamsKey is a canonical string of every generation parameter that changes the answer.
func (r *Request) ParamsKey() string {
	seed := "default"
	if r.Seed != nil {
		seed = strconv.FormatInt(*r.Seed, 10)
	}
	return strings.Join([]string{
		"t=" + fmtFloat(r.Temperature),
		"p=" + fmtFloat(r.TopP),
		"mt=" + fmtInt(r.MaxTokens),
		"mct=" + fmtInt(r.MaxCompletionTokens),
		"seed=" + seed,
		"stop=" + compactRaw(r.Stop),
		"rf=" + compactRaw(r.ResponseFormat),
	}, "|")
}

func hashParts(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0x1f})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Namespace identifies everything a cached answer is valid for: tenant, model, system prompt and parameters.
// Two requests may only share a cache entry when their namespaces are identical. This is the isolation boundary
// that stops one customer's answer from being served to another.
func (r *Request) Namespace(tenant string) string {
	return hashParts("ns", tenant, r.Model, r.SystemText(), r.ParamsKey())
}

// conversation is the non system part of the chat, flattened to text.
func (r *Request) conversation() string {
	var parts []string
	for _, m := range r.Messages {
		if isSystemRole(m.Role) {
			continue
		}
		t, _ := TextOf(m.Content)
		parts = append(parts, m.Role+":"+t)
	}
	return strings.Join(parts, "\x1e")
}

// ExactKey is the key for exact match lookups inside a namespace.
func (r *Request) ExactKey(namespace string) string {
	return hashParts("exact", namespace, r.conversation())
}

// SemanticPrompt returns the user text to embed. It is only offered for single turn requests
// (optional system message plus exactly one user message). Multi turn chats depend on their history,
// which an embedding of the last message cannot capture, so they use exact matching only.
func (r *Request) SemanticPrompt() (string, bool) {
	var users []Message
	for _, m := range r.Messages {
		if isSystemRole(m.Role) {
			continue
		}
		users = append(users, m)
	}
	if len(users) != 1 || users[0].Role != "user" {
		return "", false
	}
	t, ok := TextOf(users[0].Content)
	if !ok || strings.TrimSpace(t) == "" {
		return "", false
	}
	return strings.TrimSpace(t), true
}

// WantsUsage reports whether the client asked for token usage in a streamed response.
func (r *Request) WantsUsage() bool { return r.StreamOptions != nil && r.StreamOptions.IncludeUsage }
