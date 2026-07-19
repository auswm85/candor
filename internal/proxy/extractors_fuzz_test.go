package proxy

import "testing"

// FuzzExtractors feeds arbitrary bytes through every extractor's request-prep,
// non-streaming, and streaming paths. Extractors parse provider-controlled
// response bodies, so they must never panic on malformed input — the proxy's
// fail-open guarantee depends on it. Runs the seed corpus in normal `go test`;
// run with `-fuzz=FuzzExtractors` to explore.
func FuzzExtractors(f *testing.F) {
	seeds := []string{
		``,
		`{`,
		`{}`,
		`null`,
		`{"stream":true}`,
		`{"stream":"true","stream_options":5}`,
		`{"model":"gpt-4o","usage":{"prompt_tokens":10,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":3}}}`,
		`{"model":"x","usage":{"cost":0.01}}`,
		`data: {"usage":{"prompt_tokens":1}}`,
		`data: [DONE]`,
		`{"type":"message_start","message":{"model":"claude","usage":{"input_tokens":1,"cache_read_input_tokens":2}}}`,
		`{"type":"message_delta","usage":{"output_tokens":9}}`,
		`{"usage":{"input_tokens":-1,"output_tokens":99999999999999}}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		for _, provider := range []string{"openai", "openrouter", "anthropic"} {
			ext := extractorFor(provider)
			_ = ext.PrepareRequestBody(data)
			_ = ext.NonStreaming(data)
			acc := ext.NewStream()
			acc.Line(data)
			_ = acc.Usage()
		}
	})
}
