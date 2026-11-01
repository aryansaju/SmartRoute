package chat

import (
	"errors"
	"strings"
	"testing"
)

func mustParse(t *testing.T, body string) *Request {
	t.Helper()
	r, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return r
}

func TestParseValidation(t *testing.T) {
	for name, body := range map[string]string{
		"bad json":      `{`,
		"no model":      `{"messages":[{"role":"user","content":"hi"}]}`,
		"empty model":   `{"model":"  ","messages":[{"role":"user","content":"hi"}]}`,
		"no messages":   `{"model":"m"}`,
		"empty message": `{"model":"m","messages":[]}`,
	} {
		if _, err := Parse([]byte(body)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}
}

func TestCacheable(t *testing.T) {
	cases := []struct {
		name, body string
		want       bool
	}{
		{"plain", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, true},
		{"n=1", `{"model":"m","n":1,"messages":[{"role":"user","content":"hi"}]}`, true},
		{"n=2", `{"model":"m","n":2,"messages":[{"role":"user","content":"hi"}]}`, false},
		{"tools", `{"model":"m","tools":[{"type":"function"}],"messages":[{"role":"user","content":"hi"}]}`, false},
		{"null tools", `{"model":"m","tools":null,"messages":[{"role":"user","content":"hi"}]}`, true},
		{"tool message", `{"model":"m","messages":[{"role":"tool","content":"x"}]}`, false},
		{"image part", `{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"x"}}]}]}`, false},
		{"text parts", `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`, true},
	}
	for _, c := range cases {
		got, _ := mustParse(t, c.body).Cacheable()
		if got != c.want {
			t.Errorf("%s: cacheable = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestNamespaceIsolation(t *testing.T) {
	base := `{"model":"m","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hi"}]}`
	r := mustParse(t, base)
	ns := r.Namespace("tenantA")
	if ns != mustParse(t, base).Namespace("tenantA") {
		t.Fatal("namespace must be deterministic")
	}
	differs := map[string]string{
		"tenant":       mustParse(t, base).Namespace("tenantB"),
		"model":        mustParse(t, strings.Replace(base, `"model":"m"`, `"model":"other"`, 1)).Namespace("tenantA"),
		"system":       mustParse(t, strings.Replace(base, "be brief", "be verbose", 1)).Namespace("tenantA"),
		"temperature":  mustParse(t, strings.Replace(base, `"model":"m"`, `"model":"m","temperature":0.7`, 1)).Namespace("tenantA"),
		"max_tokens":   mustParse(t, strings.Replace(base, `"model":"m"`, `"model":"m","max_tokens":10`, 1)).Namespace("tenantA"),
		"stop":         mustParse(t, strings.Replace(base, `"model":"m"`, `"model":"m","stop":["x"]`, 1)).Namespace("tenantA"),
		"seed":         mustParse(t, strings.Replace(base, `"model":"m"`, `"model":"m","seed":7`, 1)).Namespace("tenantA"),
		"response fmt": mustParse(t, strings.Replace(base, `"model":"m"`, `"model":"m","response_format":{"type":"json_object"}`, 1)).Namespace("tenantA"),
	}
	for what, other := range differs {
		if other == ns {
			t.Errorf("changing %s must change the namespace", what)
		}
	}
}

func TestStreamFlagDoesNotChangeKeys(t *testing.T) {
	a := mustParse(t, `{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	b := mustParse(t, `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	nsA, nsB := a.Namespace("t"), b.Namespace("t")
	if nsA != nsB || a.ExactKey(nsA) != b.ExactKey(nsB) {
		t.Fatal("stream and non stream requests must share cache entries")
	}
}

func TestExactKeyDependsOnConversation(t *testing.T) {
	a := mustParse(t, `{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	b := mustParse(t, `{"model":"m","messages":[{"role":"user","content":"hello"}]}`)
	c := mustParse(t, `{"model":"m","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"},{"role":"user","content":"hi"}]}`)
	ns := a.Namespace("t")
	if a.ExactKey(ns) == b.ExactKey(ns) || a.ExactKey(ns) == c.ExactKey(ns) {
		t.Fatal("different conversations must have different exact keys")
	}
}

func TestSemanticPromptOnlyForSingleTurn(t *testing.T) {
	single := mustParse(t, `{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"  What is Go?  "}]}`)
	if p, ok := single.SemanticPrompt(); !ok || p != "What is Go?" {
		t.Errorf("single turn: got %q, %v", p, ok)
	}
	multi := mustParse(t, `{"model":"m","messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"user","content":"c"}]}`)
	if _, ok := multi.SemanticPrompt(); ok {
		t.Error("multi turn conversations must not be semantic eligible")
	}
	empty := mustParse(t, `{"model":"m","messages":[{"role":"user","content":"   "}]}`)
	if _, ok := empty.SemanticPrompt(); ok {
		t.Error("blank prompts must not be semantic eligible")
	}
}

func TestCompletionRoundTrip(t *testing.T) {
	want := Completion{Model: "m", Content: "Hello there, friend. こんにちは", FinishReason: "stop", PromptTokens: 5, CompletionTokens: 7}

	body, err := want.JSON("id1", 1)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseCompletion(body)
	if err != nil || got != want {
		t.Fatalf("json round trip: %+v, %v", got, err)
	}

	acc := NewStreamAccumulator()
	for _, f := range want.StreamFrames("id1", 1, 5, true) {
		for _, line := range strings.Split(string(f), "\n") {
			acc.FeedLine([]byte(line))
		}
	}
	sgot, ok := acc.Completion()
	if !ok || sgot != want {
		t.Fatalf("stream round trip: %+v, %v", sgot, ok)
	}
}

func TestStreamAccumulatorRejectsIncompleteAndUnsafeStreams(t *testing.T) {
	frames := Completion{Model: "m", Content: "abc", FinishReason: "stop"}.StreamFrames("i", 1, 2, false)

	acc := NewStreamAccumulator() // missing [DONE]
	for _, f := range frames[:len(frames)-1] {
		acc.FeedLine(f)
	}
	if _, ok := acc.Completion(); ok {
		t.Error("a stream without [DONE] must not be cacheable")
	}

	acc = NewStreamAccumulator()
	for _, f := range frames {
		acc.FeedLine(f)
	}
	acc.FeedLine([]byte(`data: {"error":{"message":"boom"}}`))
	if _, ok := acc.Completion(); ok {
		t.Error("a stream containing an error chunk must not be cacheable")
	}

	acc = NewStreamAccumulator()
	acc.FeedLine([]byte(`data: {"choices":[{"delta":{"tool_calls":[{"id":"1"}]},"finish_reason":null}]}`))
	acc.FeedLine([]byte(`data: {"choices":[{"delta":{"content":"x"},"finish_reason":"stop"}]}`))
	acc.FeedLine([]byte("data: [DONE]"))
	if _, ok := acc.Completion(); ok {
		t.Error("tool call streams must not be cacheable")
	}
}

func TestParseCompletionRejectsUncacheableResponses(t *testing.T) {
	for name, body := range map[string]string{
		"tool calls":  `{"choices":[{"message":{"content":null,"tool_calls":[{"id":"1"}]},"finish_reason":"tool_calls"}]}`,
		"two choices": `{"choices":[{"message":{"content":"a"},"finish_reason":"stop"},{"message":{"content":"b"},"finish_reason":"stop"}]}`,
		"filtered":    `{"choices":[{"message":{"content":"a"},"finish_reason":"content_filter"}]}`,
		"empty":       `{"choices":[{"message":{"content":""},"finish_reason":"stop"}]}`,
		"garbage":     `nope`,
	} {
		if _, err := ParseCompletion([]byte(body)); !errors.Is(err, ErrNotCacheable) {
			t.Errorf("%s: want ErrNotCacheable, got %v", name, err)
		}
	}
}
