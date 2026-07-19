package proxy

import (
	"bytes"
	"encoding/json"
)

// Extractor pulls token usage out of a provider's responses. Different provider
// APIs (OpenAI-compatible vs Anthropic Messages) have different response and
// streaming shapes, so each provides its own.
type Extractor interface {
	// PrepareRequestBody may rewrite the outgoing request body (e.g. to ask the
	// provider to include usage in a streamed response). Returns it unchanged if
	// nothing is needed.
	PrepareRequestBody(body []byte) []byte
	// NonStreaming parses usage from a complete JSON response body.
	NonStreaming(body []byte) *Usage
	// NewStream returns a stateful accumulator fed each SSE line of a stream.
	NewStream() StreamAccumulator
}

// StreamAccumulator collects usage across the lines of one SSE response.
type StreamAccumulator interface {
	Line(line []byte)
	Usage() *Usage
}

// extractorFor picks the protocol by provider name. Anthropic uses the Messages
// API; OpenRouter is OpenAI-compatible but also needs usage accounting turned on
// to return cost; everything else is plain OpenAI-compatible.
func extractorFor(provider string) Extractor {
	switch provider {
	case "anthropic":
		return anthropicExtractor{}
	case "openrouter":
		return openRouterExtractor{}
	default:
		return openAIExtractor{}
	}
}

// asBool coerces a decoded JSON value to bool, tolerating a string "true" (some
// clients send `"stream": "true"`) so a streaming request isn't misread as
// non-streaming and its usage silently dropped.
func asBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true"
	}
	return false
}

func sseData(line []byte) ([]byte, bool) {
	if !bytes.HasPrefix(line, []byte("data:")) {
		return nil, false
	}
	payload := bytes.TrimSpace(line[len("data:"):])
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return nil, false
	}
	return payload, true
}

// --- OpenAI-compatible (OpenAI, OpenRouter, most others) ---

type openAIExtractor struct{}

type openAIUsage struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	PromptTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	Cost float64 `json:"cost"` // OpenRouter includes a USD cost; OpenAI does not
}

type openAIResponse struct {
	Model string       `json:"model"`
	Usage *openAIUsage `json:"usage"`
}

func openAIToUsage(model string, u *openAIUsage) Usage {
	cached := u.PromptTokensDetails.CachedTokens
	input := u.PromptTokens - cached // OpenAI's prompt_tokens includes cached
	if input < 0 {
		input = 0
	}
	return Usage{
		Model:             model,
		InputTokens:       input,
		CachedInputTokens: cached,
		OutputTokens:      u.CompletionTokens,
		CostUSD:           u.Cost,
	}
}

func (openAIExtractor) NonStreaming(body []byte) *Usage {
	var r openAIResponse
	if err := json.Unmarshal(body, &r); err != nil || r.Usage == nil {
		return nil
	}
	u := openAIToUsage(r.Model, r.Usage)
	return &u
}

func (openAIExtractor) NewStream() StreamAccumulator { return &openAIStream{} }

// PrepareRequestBody injects stream_options.include_usage=true into a streaming
// request so OpenAI emits a final usage chunk (it omits it otherwise).
func (openAIExtractor) PrepareRequestBody(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return body // not a JSON object (e.g. "null") — nothing to inject
	}
	if !asBool(m["stream"]) {
		return body
	}
	opts, _ := m["stream_options"].(map[string]any)
	if opts == nil {
		opts = map[string]any{}
	}
	opts["include_usage"] = true
	m["stream_options"] = opts
	if nb, err := json.Marshal(m); err == nil {
		return nb
	}
	return body
}

type openAIStream struct{ usage *Usage }

func (s *openAIStream) Line(line []byte) {
	payload, ok := sseData(line)
	if !ok {
		return
	}
	var r openAIResponse
	if err := json.Unmarshal(payload, &r); err == nil && r.Usage != nil {
		u := openAIToUsage(r.Model, r.Usage)
		s.usage = &u
	}
}

func (s *openAIStream) Usage() *Usage { return s.usage }

// --- OpenRouter (OpenAI-compatible + usage accounting) ---

// openRouterExtractor parses responses exactly like OpenAI, but asks OpenRouter
// to include its `cost` in the response (usage accounting is off by default), so
// we record the real provider cost rather than pricing tokens ourselves.
type openRouterExtractor struct{ openAIExtractor }

func (openRouterExtractor) PrepareRequestBody(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return body // not a JSON object (e.g. "null") — nothing to inject
	}
	// {"usage": {"include": true}} → OpenRouter returns usage.cost.
	usage, _ := m["usage"].(map[string]any)
	if usage == nil {
		usage = map[string]any{}
	}
	usage["include"] = true
	m["usage"] = usage
	// Streaming responses also need a final usage chunk.
	if asBool(m["stream"]) {
		opts, _ := m["stream_options"].(map[string]any)
		if opts == nil {
			opts = map[string]any{}
		}
		opts["include_usage"] = true
		m["stream_options"] = opts
	}
	if nb, err := json.Marshal(m); err == nil {
		return nb
	}
	return body
}

// --- Anthropic Messages API (Claude Code, OpenCode-with-Claude) ---

type anthropicExtractor struct{}

type anthropicUsage struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	CacheReadTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_input_tokens"`
}

func anthropicToUsage(model string, u anthropicUsage) Usage {
	return Usage{
		Model:             model,
		InputTokens:       u.InputTokens, // Anthropic's input_tokens excludes cache
		CachedInputTokens: u.CacheReadTokens,
		CacheWriteTokens:  u.CacheCreationTokens,
		OutputTokens:      u.OutputTokens,
	}
}

// PrepareRequestBody deliberately returns the body untouched. Anthropic includes
// usage in responses without any request flag, and — critically — Claude Code
// subscription (OAuth) traffic must stay byte-for-byte first-party: mutating the
// body would change the prompt-cache key and risk the request being classified as
// a non-first-party harness. Do NOT inject anything here. (See proxy_test.go →
// TestProxy_AnthropicRequestBodyForwardedVerbatim.)
func (anthropicExtractor) PrepareRequestBody(body []byte) []byte { return body }

func (anthropicExtractor) NonStreaming(body []byte) *Usage {
	var r struct {
		Model string          `json:"model"`
		Usage *anthropicUsage `json:"usage"`
	}
	if err := json.Unmarshal(body, &r); err != nil || r.Usage == nil {
		return nil
	}
	u := anthropicToUsage(r.Model, *r.Usage)
	return &u
}

func (anthropicExtractor) NewStream() StreamAccumulator { return &anthropicStream{} }

// anthropicStream accumulates usage across streaming events: message_start
// carries the model plus input/cache tokens, and the final message_delta carries
// the cumulative output_tokens.
type anthropicStream struct {
	seen  bool
	model string
	usage anthropicUsage
}

func (s *anthropicStream) Line(line []byte) {
	payload, ok := sseData(line)
	if !ok {
		return
	}
	var ev struct {
		Type    string `json:"type"`
		Message struct {
			Model string         `json:"model"`
			Usage anthropicUsage `json:"usage"`
		} `json:"message"`
		Usage anthropicUsage `json:"usage"`
	}
	if err := json.Unmarshal(payload, &ev); err != nil {
		return
	}
	switch ev.Type {
	case "message_start":
		s.seen = true
		s.model = ev.Message.Model
		s.usage.InputTokens = ev.Message.Usage.InputTokens
		s.usage.CacheReadTokens = ev.Message.Usage.CacheReadTokens
		s.usage.CacheCreationTokens = ev.Message.Usage.CacheCreationTokens
		s.usage.OutputTokens = ev.Message.Usage.OutputTokens
	case "message_delta":
		if ev.Usage.OutputTokens > 0 {
			s.usage.OutputTokens = ev.Usage.OutputTokens
		}
	}
}

func (s *anthropicStream) Usage() *Usage {
	if !s.seen {
		return nil
	}
	u := anthropicToUsage(s.model, s.usage)
	return &u
}
