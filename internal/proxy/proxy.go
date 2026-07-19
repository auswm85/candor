package proxy

import (
	"bufio"
	"bytes"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

// Proxy is a transparent reverse proxy. The first path segment selects the
// upstream provider (e.g. POST /anthropic/v1/messages forwards to
// https://api.anthropic.com/v1/messages), and token usage is tapped from each
// response — using a provider-appropriate extractor — and handed to the recorder.
type Proxy struct {
	upstreams    map[string]string // provider name -> upstream base URL
	recorder     *Recorder
	client       *http.Client
	maxBodyBytes int64 // cap on the proxied request body (0 = unlimited)
}

// hopByHop headers are connection-specific and must not be forwarded by a proxy.
var hopByHop = map[string]bool{
	"connection": true, "keep-alive": true, "proxy-authenticate": true,
	"proxy-authorization": true, "te": true, "trailer": true,
	"transfer-encoding": true, "upgrade": true,
}

func isHopByHop(k string) bool { return hopByHop[strings.ToLower(k)] }

func NewProxy(upstreams map[string]string, recorder *Recorder, maxBodyBytes int64) *Proxy {
	return &Proxy{
		upstreams:    upstreams,
		recorder:     recorder,
		maxBodyBytes: maxBodyBytes,
		// No Client.Timeout — it would cut off long streamed responses. A hung
		// upstream is bounded instead by connect/TLS/response-header timeouts
		// (which don't limit the streaming body) plus the request context, which
		// cancels when the client disconnects.
		client: &http.Client{
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 60 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
				MaxIdleConns:          100,
				IdleConnTimeout:       90 * time.Second,
			},
		},
	}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// Liveness probe used by `tt run` / `tt doctor` to decide whether to route a
	// child harness through the proxy. Answered before provider routing.
	if req.URL.Path == "/healthz" {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
		return
	}

	provider, rest := splitProvider(req.URL.Path)
	upstream := p.upstreams[provider]
	if upstream == "" {
		http.Error(w, "unknown provider prefix; expected /<provider>/... where provider is one of the configured upstreams", http.StatusNotFound)
		return
	}
	ext := extractorFor(provider)

	// Cap the request body. Read one byte past the limit so we can tell a body
	// that's exactly at the limit from one that was truncated. (Only the request
	// is capped — the response is streamed to the client untouched.)
	reqReader := io.Reader(req.Body)
	if p.maxBodyBytes > 0 {
		reqReader = io.LimitReader(req.Body, p.maxBodyBytes+1)
	}
	body, err := io.ReadAll(reqReader)
	if err != nil {
		http.Error(w, "read request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if p.maxBodyBytes > 0 && int64(len(body)) > p.maxBodyBytes {
		log.Printf("proxy: %s request body exceeds %d bytes", provider, p.maxBodyBytes)
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
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
		if strings.EqualFold(k, "Content-Length") || isHopByHop(k) {
			continue
		}
		for _, v := range vs {
			outReq.Header.Add(k, v)
		}
	}
	outReq.ContentLength = int64(len(body))

	resp, err := p.client.Do(outReq)
	if err != nil {
		// Log the detail (incl. upstream) locally; keep the client's error generic.
		log.Printf("proxy: %s upstream request failed: %v", provider, err)
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	for k, vs := range resp.Header {
		if isHopByHop(k) {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	// Usage tapping is best-effort and must never break the proxied response: a
	// bug in an extractor should cost us a metric, not the user's request. Every
	// tap point below is isolated so forwarding continues regardless.
	var usage *Usage
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		acc := ext.NewStream()
		streamThrough(w, resp.Body, acc) // forwards even if the tap panics mid-stream
		recovering("stream usage", func() { usage = acc.Usage() })
	} else {
		respBody, _ := io.ReadAll(resp.Body)
		_, _ = w.Write(respBody)
		recovering("non-streaming extract", func() { usage = ext.NonStreaming(respBody) })
	}

	if usage != nil && p.recorder != nil {
		recovering("record usage", func() {
			if err := p.recorder.Record(provider, *usage); err != nil {
				log.Printf("proxy: record %s usage: %v", provider, err)
			}
		})
	}
}

// recovering runs fn, turning any panic into a log line so a tapping bug can
// never propagate into (and abort) the proxied request.
func recovering(what string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("proxy: recovered panic in %s: %v", what, r)
		}
	}()
	fn()
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
// each line to the accumulator so usage can be parsed as it streams. Forwarding
// is the priority: the client always gets the bytes first, and if the tap ever
// panics it's disabled for the rest of the stream rather than aborting it.
func streamThrough(w http.ResponseWriter, body io.Reader, acc StreamAccumulator) {
	flusher, _ := w.(http.Flusher)
	br := bufio.NewReader(body)
	tapAlive := true
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			_, _ = w.Write(line) // forward first, unconditionally
			if flusher != nil {
				flusher.Flush()
			}
			if tapAlive {
				tapAlive = safeTap(acc, line)
			}
		}
		if err != nil {
			break
		}
	}
}

// safeTap feeds one line to the accumulator, returning false (so the caller
// stops tapping) if it panics — forwarding is never affected.
func safeTap(acc StreamAccumulator, line []byte) (alive bool) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("proxy: usage tap panicked, disabling for this stream: %v", r)
			alive = false
		}
	}()
	acc.Line(line)
	return true
}
