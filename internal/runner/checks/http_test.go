package checks

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anshul-kh/grid-cli/internal/runner"
	"github.com/anshul-kh/grid-cli/internal/spec"
)

// TestHTTPRequestEvidenceShape proves { status, headers, body } and that
// the request actually carries the spec's method/headers/body through to
// the server.
func TestHTTPRequestEvidenceShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("server saw method %s, want POST", r.Method)
		}
		if got := r.Header.Get("X-Grid-Test"); got != "present" {
			t.Errorf("server saw X-Grid-Test=%q, want \"present\"", got)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != "ping" {
			t.Errorf("server saw body %q, want \"ping\"", body)
		}
		w.Header().Set("X-Reply", "pong")
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()

	ev, err := (httpRequestChecker{}).Measure(context.Background(), spec.Check{
		ID:      "c",
		Method:  "post",
		URL:     srv.URL,
		Headers: map[string]string{"X-Grid-Test": "present"},
		Body:    "ping",
	}, nil)
	if err != nil {
		t.Fatalf("Measure: unexpected error: %v", err)
	}
	if ev["status"] != http.StatusTeapot {
		t.Errorf("evidence[status] = %v, want %d", ev["status"], http.StatusTeapot)
	}
	if ev["body"] != "hello" {
		t.Errorf("evidence[body] = %v, want \"hello\"", ev["body"])
	}
	headers, ok := ev["headers"].(map[string]string)
	if !ok {
		t.Fatalf("evidence[headers] is %T, want map[string]string", ev["headers"])
	}
	if headers["X-Reply"] != "pong" {
		t.Errorf("evidence[headers][X-Reply] = %q, want \"pong\"", headers["X-Reply"])
	}
}

// TestHTTPRequestBodyCappedAt64KB proves the body is capped at 64 KB
// (MaxFieldBytes) with truncated:true, and that the checker does not read
// the response whole first (the server writes well past the cap).
func TestHTTPRequestBodyCappedAt64KB(t *testing.T) {
	huge := strings.Repeat("x", 10*(64<<10)) // 640 KB, far past the 64 KB cap
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(huge))
	}))
	defer srv.Close()

	ev, err := (httpRequestChecker{}).Measure(context.Background(), spec.Check{
		ID: "c", Method: "GET", URL: srv.URL,
	}, nil)
	if err != nil {
		t.Fatalf("Measure: unexpected error: %v", err)
	}
	body, _ := ev["body"].(string)
	if len(body) > runner.MaxFieldBytes {
		t.Fatalf("evidence[body] is %d bytes, want <= %d (the 64 KB cap)", len(body), runner.MaxFieldBytes)
	}
	if len(body) == 0 {
		t.Fatal("evidence[body] is empty, want the capped head of the response")
	}
	if ev["truncated"] != true {
		t.Errorf("evidence[truncated] = %v, want true for a 640 KB body", ev["truncated"])
	}
}

// TestHTTPRequestMissingURLIsError proves a malformed check (no method/url)
// is a measurement failure, not a zero-value evidence payload.
func TestHTTPRequestMissingURLIsError(t *testing.T) {
	if _, err := (httpRequestChecker{}).Measure(context.Background(), spec.Check{ID: "c", Method: "GET"}, nil); err == nil {
		t.Fatal("Measure with no URL: want error, got nil")
	}
}
