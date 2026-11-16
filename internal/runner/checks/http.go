package checks

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/anshul-kh/grid-cli/internal/runner"
	"github.com/anshul-kh/grid-cli/internal/spec"
)

// httpRequestChecker implements http.request (§4): evidence is
// { status, headers, body } (body capped 64 KB). The `expect` block
// (status/body_matches/jsonpath/equals/contains) is judge-side and absent
// from the public spec (content.publicview.ts keeps only method/url/
// headers/body) — the checker performs the request and uploads the raw
// response, nothing more.
type httpRequestChecker struct{}

func (httpRequestChecker) Measure(ctx context.Context, c spec.Check, _ *runner.Env) (runner.Evidence, error) {
	if c.Method == "" || c.URL == "" {
		return nil, fmt.Errorf("http.request: check %q is missing method or url", c.ID)
	}

	var body io.Reader
	if c.Body != "" {
		body = strings.NewReader(c.Body)
	}
	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(c.Method), c.URL, body)
	if err != nil {
		return nil, fmt.Errorf("http.request: building request for %q: %w", c.URL, err)
	}
	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http.request: %w", err)
	}
	defer resp.Body.Close()

	// Read one byte past the field cap so BuildEvidence's own truncation
	// detection (len(capped) < len(read)) fires correctly, while still
	// bounding memory — never read an unbounded response whole.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, runner.MaxFieldBytes+1))
	if err != nil {
		return nil, fmt.Errorf("http.request: reading response body from %q: %w", c.URL, err)
	}

	headers := make(map[string]string, len(resp.Header))
	for k := range resp.Header {
		headers[k] = resp.Header.Get(k)
	}

	return runner.BuildEvidence(map[string]any{
		"status":  resp.StatusCode,
		"headers": headers,
		"body":    string(runner.NormalizeOutput(raw)),
	}), nil
}
