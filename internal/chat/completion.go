package chat

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

// ErrNotCacheable is returned when an upstream answer should not be stored.
var ErrNotCacheable = errors.New("response is not cacheable")

// Completion is a provider independent record of one assistant answer.
// Storing this instead of raw bytes lets a streamed answer satisfy a non streaming request and the reverse.
type Completion struct {
	Model            string `json:"model"`
	Content          string `json:"content"`
	FinishReason     string `json:"finish_reason"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
}

func cacheableFinish(reason string) bool { return reason == "stop" || reason == "length" }

// EstimateTokens is a rough count used only when the provider reports no usage (about 4 characters per token).
func EstimateTokens(s string) int { return (len(s) + 3) / 4 }

// ParseCompletion extracts a Completion from a non streaming upstream response body.
func ParseCompletion(body []byte) (Completion, error) {
	var r struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content   *string         `json:"content"`
				ToolCalls json.RawMessage `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return Completion{}, ErrNotCacheable
	}
	if len(r.Choices) != 1 {
		return Completion{}, ErrNotCacheable
	}
	ch := r.Choices[0]
	if ch.Message.Content == nil || *ch.Message.Content == "" || present(ch.Message.ToolCalls) || !cacheableFinish(ch.FinishReason) {
		return Completion{}, ErrNotCacheable
	}
	c := Completion{
		Model:            r.Model,
		Content:          *ch.Message.Content,
		FinishReason:     ch.FinishReason,
		PromptTokens:     r.Usage.PromptTokens,
		CompletionTokens: r.Usage.CompletionTokens,
	}
	if c.CompletionTokens == 0 {
		c.CompletionTokens = EstimateTokens(c.Content)
	}
	return c, nil
}

// StreamAccumulator rebuilds a Completion from the raw lines of an upstream SSE stream.
type StreamAccumulator struct {
	model            string
	content          strings.Builder
	finish           string
	promptTokens     int
	completionTokens int
	haveUsage        bool
	toolCalls        bool
	done             bool
	malformed        bool
}

// NewStreamAccumulator returns an empty accumulator.
func NewStreamAccumulator() *StreamAccumulator { return &StreamAccumulator{} }

// FeedLine consumes one raw SSE line (a trailing newline is fine).
func (a *StreamAccumulator) FeedLine(line []byte) {
	s := bytes.TrimSpace(line)
	if !bytes.HasPrefix(s, []byte("data:")) {
		return
	}
	payload := bytes.TrimSpace(s[len("data:"):])
	if len(payload) == 0 {
		return
	}
	if string(payload) == "[DONE]" {
		a.done = true
		return
	}
	var ch struct {
		Model   string          `json:"model"`
		Error   json.RawMessage `json:"error"`
		Choices []struct {
			Delta struct {
				Content   *string         `json:"content"`
				ToolCalls json.RawMessage `json:"tool_calls"`
			} `json:"delta"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(payload, &ch); err != nil || present(ch.Error) || len(ch.Choices) > 1 {
		a.malformed = true
		return
	}
	if ch.Model != "" {
		a.model = ch.Model
	}
	if ch.Usage != nil {
		a.promptTokens, a.completionTokens, a.haveUsage = ch.Usage.PromptTokens, ch.Usage.CompletionTokens, true
	}
	if len(ch.Choices) == 1 {
		c := ch.Choices[0]
		if c.Delta.Content != nil {
			a.content.WriteString(*c.Delta.Content)
		}
		if present(c.Delta.ToolCalls) {
			a.toolCalls = true
		}
		if c.FinishReason != nil && *c.FinishReason != "" {
			a.finish = *c.FinishReason
		}
	}
}

// Done reports whether the terminating [DONE] marker was seen.
func (a *StreamAccumulator) Done() bool { return a.done }

// Completion returns the rebuilt answer, or false if the stream was incomplete or not cacheable.
func (a *StreamAccumulator) Completion() (Completion, bool) {
	content := a.content.String()
	if !a.done || a.malformed || a.toolCalls || content == "" || !cacheableFinish(a.finish) {
		return Completion{}, false
	}
	prompt, comp := a.promptTokens, a.completionTokens
	if !a.haveUsage || comp == 0 {
		prompt, comp = 0, EstimateTokens(content)
	}
	return Completion{Model: a.model, Content: content, FinishReason: a.finish, PromptTokens: prompt, CompletionTokens: comp}, true
}

// ---- replaying a cached completion to a client -------------------------------------------------

type usageJSON struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type responseMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseChoice struct {
	Index        int             `json:"index"`
	Message      responseMessage `json:"message"`
	FinishReason string          `json:"finish_reason"`
}

type responseJSON struct {
	ID      string           `json:"id"`
	Object  string           `json:"object"`
	Created int64            `json:"created"`
	Model   string           `json:"model"`
	Choices []responseChoice `json:"choices"`
	Usage   usageJSON        `json:"usage"`
}

// JSON renders the completion as a non streaming chat.completion response.
func (c Completion) JSON(id string, created int64) ([]byte, error) {
	return json.Marshal(responseJSON{
		ID: id, Object: "chat.completion", Created: created, Model: c.Model,
		Choices: []responseChoice{{Index: 0, Message: responseMessage{Role: "assistant", Content: c.Content}, FinishReason: c.FinishReason}},
		Usage:   usageJSON{PromptTokens: c.PromptTokens, CompletionTokens: c.CompletionTokens, TotalTokens: c.PromptTokens + c.CompletionTokens},
	})
}

type streamDelta struct {
	Role    string  `json:"role,omitempty"`
	Content *string `json:"content,omitempty"`
}

type streamChoice struct {
	Index        int         `json:"index"`
	Delta        streamDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

type streamChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []streamChoice `json:"choices"`
	Usage   *usageJSON     `json:"usage,omitempty"`
}

func splitRunes(s string, n int) []string {
	if n < 1 {
		n = 1
	}
	r := []rune(s)
	var out []string
	for i := 0; i < len(r); i += n {
		end := i + n
		if end > len(r) {
			end = len(r)
		}
		out = append(out, string(r[i:end]))
	}
	return out
}

// StreamFrames renders the completion as ready to send SSE frames, ending with the [DONE] marker.
func (c Completion) StreamFrames(id string, created int64, chunkChars int, includeUsage bool) [][]byte {
	if chunkChars < 1 {
		chunkChars = 24
	}
	var frames [][]byte
	add := func(ch streamChunk) {
		b, _ := json.Marshal(ch)
		frame := append([]byte("data: "), b...)
		frame = append(frame, '\n', '\n')
		frames = append(frames, frame)
	}
	base := func() streamChunk {
		return streamChunk{ID: id, Object: "chat.completion.chunk", Created: created, Model: c.Model}
	}

	empty := ""
	first := base()
	first.Choices = []streamChoice{{Index: 0, Delta: streamDelta{Role: "assistant", Content: &empty}}}
	add(first)

	for _, piece := range splitRunes(c.Content, chunkChars) {
		p := piece
		ch := base()
		ch.Choices = []streamChoice{{Index: 0, Delta: streamDelta{Content: &p}}}
		add(ch)
	}

	reason := c.FinishReason
	if reason == "" {
		reason = "stop"
	}
	last := base()
	last.Choices = []streamChoice{{Index: 0, Delta: streamDelta{}, FinishReason: &reason}}
	add(last)

	if includeUsage {
		u := base()
		u.Choices = []streamChoice{}
		u.Usage = &usageJSON{PromptTokens: c.PromptTokens, CompletionTokens: c.CompletionTokens, TotalTokens: c.PromptTokens + c.CompletionTokens}
		add(u)
	}
	frames = append(frames, []byte("data: [DONE]\n\n"))
	return frames
}
