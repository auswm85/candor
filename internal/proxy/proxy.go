package proxy

import (
	"bufio"
	"bytes"
	"io"
	"log"
	"net/http"
	"strings"
)

// Proxy is a transparent reverse proxy. The first path segment selects the
// upstream provider (e.g. POST /anthropic/v1/messages forwards to
// https://api.anthropic.com/v1/messages), and token usage is tapped from each
// response — using a provider-appropriate extractor — and handed to the recorder.
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
	ext := extractorFor(provider)

	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "read request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	body = ext.PrepareRequestBody(body)

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
		acc := ext.NewStream()
		streamThrough(w, resp.Body, acc)
		usage = acc.Usage()
	} else {
		respBody, _ := io.ReadAll(resp.Body)
		_, _ = w.Write(respBody)
		usage = ext.NonStreaming(respBody)
	}

	if usage != nil && p.recorder != nil {
		if err := p.recorder.Record(provider, *usage); err != nil {
			log.Printf("proxy: record %s usage: %v", provider, err)
		}
	}
}

// splitProvider separates "/anthropic/v1/messages" into ("anthropic", "/v1/messages").
func splitProvider(path string) (provider, rest string) {
	trimmed := strings.TrimPrefix(path, "/")
	if i := strings.IndexByte(trimmed, '/'); i >= 0 {
		return trimmed[:i], "/" + trimmed[i+1:]
	}
	return trimmed, "/"
}

// streamThrough forwards an SSE body to the client byte-for-byte while feeding
// each line to the accumulator so usage can be parsed as it streams.
func streamThrough(w http.ResponseWriter, body io.Reader, acc StreamAccumulator) {
	flusher, _ := w.(http.Flusher)
	br := bufio.NewReader(body)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			_, _ = w.Write(line)
			if flusher != nil {
				flusher.Flush()
			}
			acc.Line(line)
		}
		if err != nil {
			break
		}
	}
}
