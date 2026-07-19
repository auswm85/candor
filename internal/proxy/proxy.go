package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
)

// Proxy is a transparent reverse proxy. The first path segment selects the
// upstream provider (e.g. POST /openai/v1/chat/completions forwards to
// https://api.openai.com/v1/chat/completions), and token usage is tapped from
// each response and handed to the recorder.
type Proxy struct {
	upstreams map[string]string // provider name -> upstream base URL
	recorder  *Recorder
	client    *http.Client
}

func NewProxy(upstreams map[string]string, recorder *Recorder) *Proxy {
	return &Proxy{
		upstreams: upstreams,
		recorder:  recorder,
		client:    &http.Client{}, // no timeout: streamed responses can be long
	}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	provider, rest := splitProvider(req.URL.Path)
	upstream := p.upstreams[provider]
	if upstream == "" {
		http.Error(w, "unknown provider prefix; expected /<provider>/... where provider is one of the configured upstreams", http.StatusNotFound)
		return
	}

	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "read request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	// For streaming chat requests, make sure the provider includes a final usage
	// chunk (OpenAI omits it unless asked).
	body = ensureStreamUsage(body)

	target := strings.TrimSuffix(upstream, "/") + rest
	if req.URL.RawQuery != "" {
		target += "?" + req.URL.RawQuery
	}
	outReq, err := http.NewRequestWithContext(req.Context(), req.Method, target, bytes.NewReader(body))
	if err != nil {
		http.Error(w, "build upstream request: "+err.Error(), http.StatusBadGateway)
		return
	}
	for k, vs := range req.Header {
		if strings.EqualFold(k, "Content-Length") {
			continue
		}
		for _, v := range vs {
			outReq.Header.Add(k, v)
		}
	}
	outReq.ContentLength = int64(len(body))

	resp, err := p.client.Do(outReq)
	if err != nil {
		http.Error(w, "upstream request failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	var usage *Usage
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		usage = streamThrough(w, resp.Body)
	} else {
		respBody, _ := io.ReadAll(resp.Body)
		_, _ = w.Write(respBody)
		usage = parseJSONUsage(respBody)
	}

	if usage != nil && p.recorder != nil {
		if err := p.recorder.Record(provider, *usage); err != nil {
			log.Printf("proxy: record %s usage: %v", provider, err)
		}
	}
}

// splitProvider separates "/openai/v1/chat/completions" into ("openai", "/v1/chat/completions").
func splitProvider(path string) (provider, rest string) {
	trimmed := strings.TrimPrefix(path, "/")
	if i := strings.IndexByte(trimmed, '/'); i >= 0 {
		return trimmed[:i], "/" + trimmed[i+1:]
	}
	return trimmed, "/"
}

// --- usage extraction (OpenAI-compatible) ---

type openaiUsage struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	PromptTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	Cost float64 `json:"cost"` // OpenRouter includes a USD cost; OpenAI does not
}

type openaiResponse struct {
	Model string       `json:"model"`
	Usage *openaiUsage `json:"usage"`
}

func toUsage(model string, u *openaiUsage) Usage {
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

func parseJSONUsage(body []byte) *Usage {
	var r openaiResponse
	if err := json.Unmarshal(body, &r); err != nil || r.Usage == nil {
		return nil
	}
	u := toUsage(r.Model, r.Usage)
	return &u
}

// streamThrough forwards an SSE body to the client byte-for-byte while parsing
// each `data:` line for the usage chunk (which arrives last).
func streamThrough(w http.ResponseWriter, body io.Reader) *Usage {
	flusher, _ := w.(http.Flusher)
	br := bufio.NewReader(body)
	var usage *Usage
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			_, _ = w.Write(line)
			if flusher != nil {
				flusher.Flush()
			}
			if bytes.HasPrefix(line, []byte("data:")) {
				payload := bytes.TrimSpace(line[len("data:"):])
				if len(payload) > 0 && !bytes.Equal(payload, []byte("[DONE]")) {
					if u := parseJSONUsage(payload); u != nil {
						usage = u
					}
				}
			}
		}
		if err != nil {
			break
		}
	}
	return usage
}

// ensureStreamUsage injects stream_options.include_usage=true into a streaming
// chat request so the provider emits a final usage chunk. Non-streaming or
// non-JSON bodies are returned unchanged.
func ensureStreamUsage(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	streaming, _ := m["stream"].(bool)
	if !streaming {
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
