package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anshul-kh/grid-cli/internal/buildinfo"
	"github.com/anshul-kh/grid-extensions/pkg/attest"
)

// reqLog records what an httptest server saw, race-safely.
type reqLog struct {
	mu         sync.Mutex
	hits       int
	lastBody   []byte
	lastHeader http.Header
}

func (l *reqLog) record(r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.hits++
	l.lastBody = body
	l.lastHeader = r.Header.Clone()
}

func (l *reqLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.hits
}

func (l *reqLog) body() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]byte(nil), l.lastBody...)
}

func (l *reqLog) header() http.Header {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastHeader.Clone()
}

// respond writes a full §6 envelope response body with the given status.
func respond(w http.ResponseWriter, status int, message string, data string) {
	w.Header().Set("Content-Type", "application/json")
	if status == http.StatusTooManyRequests && w.Header().Get("Retry-After") == "" {
		w.Header().Set("Retry-After", "1")
	}
	w.WriteHeader(status)
	enc := struct {
		Success    bool            `json:"success"`
		StatusCode int             `json:"statusCode"`
		Message    string          `json:"message"`
		Data       json.RawMessage `json:"data"`
	}{status < 400, status, message, json.RawMessage(data)}
	_ = json.NewEncoder(w).Encode(enc)
}

// respondWithCode writes a §5.4b submission-error envelope: a top-level
// `code` sibling of `message` (GRID_SYNC_CLAUDE.md 2026-11-12 06:00), no
// `data` — the shape of the three named outcomes attempt_expired /
// nonce_already_used / attempt_already_passed.
func respondWithCode(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := struct {
		Success    bool   `json:"success"`
		StatusCode int    `json:"statusCode"`
		Message    string `json:"message"`
		Code       string `json:"code"`
	}{false, status, message, code}
	_ = json.NewEncoder(w).Encode(enc)
}

const testMeData = `{"user": {"id": "u1", "username": "grid-tester", "displayName": "Grid Tester", "avatarUrl": ""}, "rank": "OPERATOR", "xp": 1284, "streak": 4}`

const testSubmissionData = `{"submissionId": "sub-1", "verdict": "PASSED", "checks": [{"checkId": "c1", "title": "t1", "passed": true, "message": "m"}], "xp": {"delta": 94, "total": 1284}, "stats": {"rank": "OPERATOR", "level": 3, "currentStreak": 4, "lessonsCompleted": 12}, "unlocked": {"badges": [], "achievements": []}, "next": {"lessonId": "n1", "title": "Next lesson"}}`

// testSessionKey is a throwaway key for client-side tests against local
// httptest servers. It is never printed in failure messages.
const testSessionKey = "unit-test-session-key-000000000000"

// testEnvelope builds a valid A4 AttestationEnvelope with the given
// attemptNonce, device-signed with the deterministic test key (the stub
// rejects a body without a deviceSignature as 400 invalid_envelope).
func testEnvelope(nonce string) *attest.AttestationEnvelope {
	env := &attest.AttestationEnvelope{
		AttemptID:    "attempt-1",
		AttemptNonce: nonce,
		SubmittedAt:  "2026-11-15T18:30:00Z",
		ClientMetadata: attest.ClientMetadata{
			CLIVersion:    buildinfo.Version,
			CLIBinaryHash: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			OS:            "darwin",
			Arch:          "arm64",
		},
		WorkspaceProof: attest.WorkspaceProof{
			GitHeadSha:     "9f8e7d6c5b4a3f2e1d0c9b8a7f6e5d4c3b2a1f0e",
			SaltedTreeHash: "sha256:7c3a1f2e4d5c6b7a8901fedcba9876543210fedcba9876543210fedcba987654",
		},
		Results: []attest.Result{
			{CheckID: "resp-server-exists", Status: "ok", DurationMs: 10, Evidence: map[string]any{"exists": true}},
			{CheckID: "resp-server-pings", Status: "ok", DurationMs: 20, Evidence: map[string]any{"exitCode": 0, "stdout": "PONG"}},
			{CheckID: "resp-server-handles-commands", Status: "skipped"},
		},
	}
	sig, err := attest.SignEnvelope(attest.DeterministicTestKey(), env)
	if err != nil {
		panic("testEnvelope: signing the fixture envelope: " + err.Error())
	}
	env.DeviceSignature = sig
	return env
}

func newStubbedClient(srv *httptest.Server, opts ...Option) *Client {
	c, err := New(srv.URL+"/api/v1", opts...)
	if err != nil {
		panic(err)
	}
	return c
}

func TestHeadersOnEveryRequest(t *testing.T) {
	log := &reqLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.record(r)
		respond(w, http.StatusOK, "ok", testMeData)
	}))
	defer srv.Close()
	c := newStubbedClient(srv, WithSessionKey(testSessionKey))

	if _, err := c.Me(context.Background()); err != nil {
		t.Fatalf("Me: %v", err)
	}
	h := log.header()
	if got := h.Get("Authorization"); got != "Bearer "+testSessionKey {
		t.Errorf("Authorization header mismatch (never print key values)")
	}
	if got, want := h.Get("User-Agent"), buildinfo.UserAgent(); got != want {
		t.Errorf("User-Agent = %q, want %q", got, want)
	}
	if got, want := h.Get(HeaderCLIVersion), buildinfo.Version; got != want {
		t.Errorf("%s = %q, want %q", HeaderCLIVersion, got, want)
	}
	if got := h.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
}

// TestSignatureIsHMACOfExactPostedBytes is the §5.4 hard invariant: the
// client marshals once, signs those exact bytes, and posts that exact buffer.
// The server recomputes the HMAC over the bytes it received; any drift
// between signed and posted bytes breaks the check.
func TestSignatureIsHMACOfExactPostedBytes(t *testing.T) {
	log := &reqLog{}
	sigOK := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		log.mu.Lock()
		log.hits++
		log.lastBody = body
		log.lastHeader = r.Header.Clone()
		log.mu.Unlock()
		mac := hmac.New(sha256.New, []byte(testSessionKey))
		mac.Write(body)
		sigOK = r.Header.Get(HeaderSignature) == "sha256="+hex.EncodeToString(mac.Sum(nil))
		respond(w, http.StatusOK, "ok", testSubmissionData)
	}))
	defer srv.Close()
	c := newStubbedClient(srv, WithSessionKey(testSessionKey))

	sub := testEnvelope("nonce-1")
	resp, err := c.Submit(context.Background(), sub)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if !sigOK {
		t.Fatal("server-recomputed HMAC over the received bytes does not match the signature header (signature and posted bytes must be the same buffer)")
	}
	// Marshal-once proof: the posted bytes are byte-identical to the single
	// canonical serialization of the same envelope (no re-marshal drift
	// between signing and sending).
	once, err := attest.Canonicalize(sub)
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	if !bytes.Equal(once, log.body()) {
		t.Error("posted body is not the exact buffer that was marshalled once (bytes differ)")
	}
	if resp.Verdict != VerdictPassed {
		t.Errorf("verdict = %q, want PASSED", resp.Verdict)
	}
	if resp.XP.Delta != 94 || resp.Stats.Rank != "OPERATOR" || resp.Next == nil {
		t.Errorf("submission response fields not decoded correctly: %+v", resp)
	}
}

// TestSubmitNotRetriedAfter409 proves the replay rule: a 409 is terminal —
// exactly one request hits the server. The top-level `code` sibling
// (GRID_SYNC_CLAUDE.md 2026-11-12 06:00) disambiguates nonce_already_used
// from attempt_already_passed; this exercises both.
func TestSubmitNotRetriedAfter409(t *testing.T) {
	for _, tc := range []struct {
		code string
		want error
		hint string
	}{
		{"nonce_already_used", ErrNonceAlreadyUsed, "Your local attempt file is stale — run `grid run` again."},
		{"attempt_already_passed", ErrAttemptAlreadyPassed, "You already passed this — run `grid run <id>` to redo it."},
	} {
		t.Run(tc.code, func(t *testing.T) {
			log := &reqLog{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				log.record(r)
				respondWithCode(w, http.StatusConflict, tc.code, "conflict")
			}))
			defer srv.Close()
			c := newStubbedClient(srv, WithSessionKey(testSessionKey), WithBackoff(time.Millisecond))

			_, err := c.Submit(context.Background(), testEnvelope("nonce-1"))
			if !errors.Is(err, tc.want) {
				t.Fatalf("Submit error = %v, want %v", err, tc.want)
			}
			if got := UserHint(err); got != tc.hint {
				t.Errorf("UserHint = %q, want %q", got, tc.hint)
			}
			if log.count() != 1 {
				t.Errorf("submission was retried after 409: handler hits = %d, want 1", log.count())
			}
		})
	}
}

// TestSubmitNotRetriedAfter2xx: once the server answers 2xx, the submission
// is never re-sent, even if the body is cut off mid-flight.
func TestSubmitNotRetriedAfter2xx(t *testing.T) {
	log := &reqLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.record(r)
		// Announce a Content-Length larger than what we send, then close:
		// the client sees 200 headers but a truncated body.
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("server does not support hijacking")
			return
		}
		conn, buf, _ := hj.Hijack()
		defer conn.Close()
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 500\r\n\r\n{\"success\":true,")
		_ = buf.Flush()
	}))
	defer srv.Close()
	c := newStubbedClient(srv, WithSessionKey(testSessionKey), WithBackoff(time.Millisecond))

	_, err := c.Submit(context.Background(), testEnvelope("nonce-1"))
	if !errors.Is(err, ErrNetwork) {
		t.Fatalf("Submit error = %v, want ErrNetwork (truncated response on a non-idempotent request)", err)
	}
	if log.count() != 1 {
		t.Errorf("non-idempotent request was replayed after the server responded: hits = %d, want 1", log.count())
	}

	// The same flaky response IS retried for an idempotent request.
	log2 := &reqLog{}
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log2.record(r)
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("server does not support hijacking")
			return
		}
		conn, buf, _ := hj.Hijack()
		defer conn.Close()
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 500\r\n\r\n{\"success\":true,")
		_ = buf.Flush()
	}))
	defer srv2.Close()
	c2 := newStubbedClient(srv2, WithSessionKey(testSessionKey), WithBackoff(time.Millisecond), WithMaxAttempts(3))
	_, err = c2.do(context.Background(), request{method: http.MethodGet, path: "/cli/me", idempotent: true})
	if !errors.Is(err, ErrNetwork) {
		t.Fatalf("idempotent GET error = %v, want ErrNetwork", err)
	}
	if log2.count() != 3 {
		t.Errorf("idempotent request should be retried to the attempt budget: hits = %d, want 3", log2.count())
	}
}

func TestRetriesOn5xxThenSucceeds(t *testing.T) {
	log := &reqLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.record(r)
		if log.count() == 1 {
			respond(w, http.StatusInternalServerError, "server_error", "")
			return
		}
		respond(w, http.StatusOK, "ok", testMeData)
	}))
	defer srv.Close()
	c := newStubbedClient(srv, WithSessionKey(testSessionKey), WithBackoff(time.Millisecond))

	if _, err := c.Me(context.Background()); err != nil {
		t.Fatalf("Me: %v", err)
	}
	if log.count() != 2 {
		t.Errorf("hits = %d, want 2 (one 500, then success)", log.count())
	}
}

func TestRetryBudgetIsThreeAttemptsOn5xx(t *testing.T) {
	log := &reqLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.record(r)
		respond(w, http.StatusInternalServerError, "server_error", "")
	}))
	defer srv.Close()
	var sleeps []time.Duration
	c := newStubbedClient(srv, WithSessionKey(testSessionKey), WithBackoff(time.Millisecond),
		WithSleeper(func(_ context.Context, d time.Duration) error {
			sleeps = append(sleeps, d)
			return nil
		}))

	_, err := c.Me(context.Background())
	if !errors.Is(err, ErrServer) {
		t.Fatalf("Me error = %v, want ErrServer", err)
	}
	if log.count() != 3 {
		t.Errorf("hits = %d, want 3 attempts on persistent 5xx", log.count())
	}
	if len(sleeps) != 2 {
		t.Errorf("slept %d times, want 2 (between the 3 attempts)", len(sleeps))
	}
}

func TestRetryHonoursRetryAfterHeader(t *testing.T) {
	log := &reqLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.record(r)
		if log.count() == 1 {
			w.Header().Set("Retry-After", "1")
			respond(w, http.StatusTooManyRequests, "slow_down", `{"status": "slow_down"}`)
			return
		}
		respond(w, http.StatusOK, "ok", testMeData)
	}))
	defer srv.Close()
	var sleeps []time.Duration
	c := newStubbedClient(srv, WithSessionKey(testSessionKey), WithBackoff(time.Millisecond),
		WithSleeper(func(_ context.Context, d time.Duration) error {
			sleeps = append(sleeps, d)
			return nil
		}))

	if _, err := c.Me(context.Background()); err != nil {
		t.Fatalf("Me: %v", err)
	}
	if log.count() != 2 {
		t.Errorf("hits = %d, want 2 (one 429, then success)", log.count())
	}
	if len(sleeps) != 1 || sleeps[0] < time.Second {
		t.Errorf("retry did not honour Retry-After: sleeps = %v, want a single delay >= 1s", sleeps)
	}
}

func TestRetriesOnNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	base := srv.URL
	srv.Close() // nothing is listening anymore

	sleeps := 0
	c, err := New(base, WithSessionKey(testSessionKey), WithBackoff(time.Millisecond),
		WithSleeper(func(_ context.Context, _ time.Duration) error {
			sleeps++
			return nil
		}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = c.Me(context.Background())
	if !errors.Is(err, ErrNetwork) {
		t.Fatalf("Me error = %v, want ErrNetwork", err)
	}
	if sleeps != 2 {
		t.Errorf("slept %d times, want 2 (3 attempts on connection-refused)", sleeps)
	}
}

func TestBackoffIsExponentialWithJitter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusInternalServerError, "server_error", "")
	}))
	defer srv.Close()
	var sleeps []time.Duration
	c := newStubbedClient(srv, WithSessionKey(testSessionKey), WithBackoff(100*time.Millisecond),
		WithSleeper(func(_ context.Context, d time.Duration) error {
			sleeps = append(sleeps, d)
			return nil
		}))

	_, err := c.Me(context.Background())
	if !errors.Is(err, ErrServer) {
		t.Fatalf("Me error = %v, want ErrServer", err)
	}
	if len(sleeps) != 2 {
		t.Fatalf("slept %d times, want 2", len(sleeps))
	}
	// base=100ms: first backoff ∈ [100ms, 125ms), second ∈ [200ms, 250ms).
	if sleeps[0] < 100*time.Millisecond || sleeps[0] >= 125*time.Millisecond {
		t.Errorf("first backoff = %v, want within [100ms, 125ms)", sleeps[0])
	}
	if sleeps[1] < 200*time.Millisecond || sleeps[1] >= 250*time.Millisecond {
		t.Errorf("second backoff = %v, want within [200ms, 250ms)", sleeps[1])
	}
}

// TestDevicePollSurfacesStatesWithoutAutoRetry: the poll states are the
// caller's to time (grid login), so 428/429 must surface immediately as
// typed errors with exactly one request sent.
func TestDevicePollSurfacesStatesWithoutAutoRetry(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		want   error
	}{
		{"pending", http.StatusPreconditionRequired, ErrPending},
		{"slow_down", http.StatusTooManyRequests, ErrSlowDown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := &reqLog{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				log.record(r)
				if tc.status == http.StatusTooManyRequests {
					w.Header().Set("Retry-After", "1")
				}
				respond(w, tc.status, tc.name, `{"status": "`+tc.name+`"}`)
			}))
			defer srv.Close()
			c := newStubbedClient(srv, WithBackoff(time.Millisecond))

			_, err := c.DevicePoll(context.Background(), "device-1")
			if !errors.Is(err, tc.want) {
				t.Fatalf("DevicePoll error = %v, want %v", err, tc.want)
			}
			if log.count() != 1 {
				t.Errorf("poll was auto-retried by the client: hits = %d, want 1", log.count())
			}
		})
	}
}

func TestEnvelopeUnwrappingToleratesRawAndWrapped(t *testing.T) {
	const rawBody = `{"deviceCode": "dc-1", "userCode": "UC-1", "verificationUrl": "http://x/device", "expiresIn": 600, "interval": 3}`
	for _, tc := range []struct {
		name string
		body string
	}{
		{"wrapped", `{"success": true, "statusCode": 200, "message": "ok", "data": ` + rawBody + `}`},
		{"raw", rawBody},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			c := newStubbedClient(srv)

			got, err := c.DeviceStart(context.Background(), DeviceStartRequest{Hostname: "h", OS: "darwin", Arch: "arm64", CliVersion: "dev"})
			if err != nil {
				t.Fatalf("DeviceStart: %v", err)
			}
			if got.DeviceCode != "dc-1" || got.UserCode != "UC-1" || got.ExpiresInSeconds != 600 || got.IntervalSeconds != 3 {
				t.Errorf("device-start response not decoded correctly: %+v", got)
			}
		})
	}
}

// TestAttemptSurfacesNotEnrolledDetail decodes the real backend's confirmed
// not_enrolled wire shape (2026-11-13, against a live seeded backend: a
// plain ApiError, no top-level `code` — errors[0] carries code/courseSlug/
// enrollUrl) and proves Client.Attempt surfaces ErrNotEnrolled with those
// fields intact, not just a generic ErrDenied.
func TestAttemptSurfacesNotEnrolledDetail(t *testing.T) {
	const body = `{"success":false,"statusCode":403,"message":"Enroll in this course to open its lessons.","errors":[{"code":"not_enrolled","courseSlug":"claude-code-end-to-end","enrollUrl":"http://localhost:3000/courses/claude-code-end-to-end"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()
	c := newStubbedClient(srv)

	_, err := c.Attempt(context.Background(), "f2908a1e-6b6c-4e4d-9f6e-8b0a0d9a3f21")
	if !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("Attempt on a not_enrolled 403: err = %v, want ErrNotEnrolled", err)
	}
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("err = %T, want *StatusError", err)
	}
	if se.CourseSlug != "claude-code-end-to-end" {
		t.Errorf("CourseSlug = %q, want claude-code-end-to-end", se.CourseSlug)
	}
	if se.EnrollURL != "http://localhost:3000/courses/claude-code-end-to-end" {
		t.Errorf("EnrollURL = %q, want the server's enrollUrl verbatim", se.EnrollURL)
	}
	if errors.Is(err, ErrDenied) {
		t.Error("not_enrolled must not also match the generic ErrDenied")
	}
}

func TestUnexpectedStatusMapsToErrUnexpected(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusBadRequest, http.StatusTeapot} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			respond(w, status, "whatever", "")
		}))
		c := newStubbedClient(srv, WithBackoff(time.Millisecond))
		_, err := c.Me(context.Background())
		if !errors.Is(err, ErrUnexpected) {
			t.Errorf("status %d: error = %v, want ErrUnexpected", status, err)
		}
		// Each server needs its own client; close after use.
		srv.Close()
	}
}

func TestCheckStatusValuesAreMeasurementOnly(t *testing.T) {
	if CheckStatusOK != "ok" || CheckStatusError != "error" || CheckStatusSkipped != "skipped" {
		t.Errorf("check status constants must be exactly ok|error|skipped")
	}
	for _, forbidden := range []string{"pass", "fail", "passed", "failed"} {
		if string(CheckStatusOK) == forbidden || string(CheckStatusError) == forbidden || string(CheckStatusSkipped) == forbidden {
			t.Errorf("check status must never be %q", forbidden)
		}
	}
}

func TestStubSchemeWiresInProcessStub(t *testing.T) {
	c, err := New("stub://", WithSessionKey(stubSessionKey()))
	if err != nil {
		t.Fatalf("New(stub://): %v", err)
	}
	me, err := c.Me(context.Background())
	if err != nil {
		t.Fatalf("Me against in-process stub: %v", err)
	}
	if me.Username != "grid-tester" {
		t.Errorf("username = %q, want grid-tester", me.Username)
	}
}

func TestGridStubEnvWiresInProcessStub(t *testing.T) {
	t.Setenv(EnvStub, "1")
	// The base URL points at a closed port; the stub transport must win.
	c, err := New("http://127.0.0.1:1/api/v1", WithSessionKey(stubSessionKey()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Me(context.Background()); err != nil {
		t.Fatalf("Me with %s=1: %v (stub transport should have handled it)", EnvStub, err)
	}
}

func TestNewRejectsInvalidBaseURL(t *testing.T) {
	for _, bad := range []string{"", "not a url", "localhost:4000/api"} {
		if _, err := New(bad); err == nil {
			t.Errorf("New(%q) should have failed", bad)
		}
	}
	if _, err := New("stub://"); err != nil {
		t.Errorf("New(stub://) should succeed, got %v", err)
	}
	// Tolerate trailing slashes.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/") {
			respond(w, http.StatusNotFound, "not_found", "")
			return
		}
		respond(w, http.StatusOK, "ok", testMeData)
	}))
	defer srv.Close()
	if _, err := New(srv.URL + "/api/v1/"); err != nil {
		t.Errorf("New with trailing slash: %v", err)
	}
}
