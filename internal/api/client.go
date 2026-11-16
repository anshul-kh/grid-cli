package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/anshul-kh/grid-cli/internal/api/stub"
	"github.com/anshul-kh/grid-cli/internal/buildinfo"
	"github.com/anshul-kh/grid-extensions/pkg/attest"
)

const (
	// DefaultTimeout bounds a single request when the caller's context has
	// no deadline (TASK.md §6).
	DefaultTimeout = 30 * time.Second
	// DefaultMaxAttempts: 3 attempts total on retryable failures
	// (5xx, 429, transport errors) with exponential backoff (TASK.md §6).
	DefaultMaxAttempts = 3
	// DefaultBackoff is the first retry delay; it doubles per attempt with
	// up to 25% jitter.
	DefaultBackoff = 500 * time.Millisecond

	// EnvStub, when set to "1", wires the client to the in-process stub
	// server regardless of the configured base URL — equivalent to
	// --api stub:// (TASK.md §9).
	EnvStub = "GRID_STUB"
	// StubScheme is the base-URL scheme that selects the in-process stub.
	StubScheme = "stub"

	// HeaderSignature carries the request signature on submissions
	// (GRID_SPEC §5.4).
	HeaderSignature = "X-Grid-Signature"
	// HeaderCLIVersion reports the CLI semver on every request (§5.1).
	HeaderCLIVersion = "X-Grid-Cli-Version"
)

// Client is the typed GRID_SPEC §5 HTTP client: headers on every request,
// retries with backoff, HMAC request signing on submissions, envelope
// unwrapping, and typed errors.
type Client struct {
	baseURL     string // normalized, no trailing slash
	http        *http.Client
	sessionKey  string
	maxAttempts int
	backoff     time.Duration
	timeout     time.Duration
	sleep       func(context.Context, time.Duration) error
}

// Option configures a Client.
type Option func(*Client)

// WithSessionKey sets the bearer session key used for Authorization and
// request signing.
func WithSessionKey(key string) Option {
	return func(c *Client) { c.sessionKey = key }
}

// WithHTTPClient overrides the underlying http.Client (tests inject
// transports and timeouts).
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.http = hc }
}

// WithMaxAttempts sets the total attempt count (>= 1).
func WithMaxAttempts(n int) Option {
	return func(c *Client) {
		if n < 1 {
			n = 1
		}
		c.maxAttempts = n
	}
}

// WithBackoff sets the first retry delay; it doubles per attempt.
func WithBackoff(d time.Duration) Option {
	return func(c *Client) { c.backoff = d }
}

// WithTimeout bounds a single request when the caller's context has no
// deadline. 0 disables the client-side timeout.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.timeout = d }
}

// WithSleeper replaces the retry delay function (tests inject a recorder
// instead of real sleeping).
func WithSleeper(fn func(context.Context, time.Duration) error) Option {
	return func(c *Client) { c.sleep = fn }
}

// New builds a Client for the given base URL (e.g.
// http://localhost:4000/api/v1). When the URL uses the stub scheme —
// "stub://" — or GRID_STUB=1 is set, every request is served by an
// in-process stub server instead of the network (TASK.md §9), which is how
// the whole CLI runs end-to-end before the real backend ships.
func New(baseURL string, opts ...Option) (*Client, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("api: empty base URL")
	}
	base := baseURL
	useStub := strings.HasPrefix(base, StubScheme+"://")
	if !useStub {
		// Only normalise the trailing slash for real URLs; "stub://" must
		// keep its scheme slashes.
		base = strings.TrimSuffix(base, "/")
		if base == "" {
			return nil, fmt.Errorf("api: empty base URL")
		}
	}
	c := &Client{
		baseURL:     base,
		http:        &http.Client{},
		maxAttempts: DefaultMaxAttempts,
		backoff:     DefaultBackoff,
		timeout:     DefaultTimeout,
	}
	if useStub || os.Getenv(EnvStub) == "1" {
		c.http.Transport = stubTransport{handler: stub.New()}
	} else {
		u, err := url.Parse(base)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("api: invalid base URL %q", baseURL)
		}
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// request is one API call. It is built here so the retry loop replays the
// exact same bytes (marshal once, sign once, post repeatedly).
type request struct {
	method string
	path   string
	body   []byte
	// sign adds X-Grid-Signature = HMAC-SHA256(sessionKey, body) — only for
	// POST /cli/submissions (GRID_SPEC §5.4).
	sign bool
	// idempotent marks the request safe to replay. Non-idempotent requests
	// (submissions) are never re-sent once the server has responded — even
	// if the response body is cut off mid-flight — because the server may
	// already have processed it.
	idempotent bool
	// noRetry disables retries entirely; the device poll uses it because
	// 428/429 are poll states the caller must observe and time itself.
	noRetry bool
}

// attemptResult is the outcome of a single attempt.
type attemptResult struct {
	data  json.RawMessage
	err   error
	retry bool
	// delay overrides the computed backoff before the next attempt (the
	// Retry-After header on 429).
	delay time.Duration
}

// do executes req with retries. Retries happen only on 5xx, 429 (honouring
// Retry-After), and transport errors — never after a definitive 2xx or 4xx
// (TASK.md §6: never retry a submission after a 2xx or a 409).
func (c *Client) do(ctx context.Context, req request) (json.RawMessage, error) {
	if c.timeout > 0 {
		if _, ok := ctx.Deadline(); !ok {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, c.timeout)
			defer cancel()
		}
	}
	var lastErr error
	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		res := c.attempt(ctx, req)
		if res.err == nil {
			return res.data, nil
		}
		lastErr = res.err
		if !res.retry || req.noRetry || attempt == c.maxAttempts {
			return nil, res.err
		}
		delay := res.delay
		if delay <= 0 {
			delay = backoffFor(c.backoff, attempt)
		}
		if err := c.wait(ctx, delay); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

// attempt performs one request/response round trip and classifies the
// outcome.
func (c *Client) attempt(ctx context.Context, req request) attemptResult {
	resp, err := c.send(ctx, req)
	if err != nil {
		// Transport-level failure with no response at all: safe to retry.
		return attemptResult{err: networkError(0, err), retry: true}
	}
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		// The server responded but the body was cut off mid-flight. A
		// non-idempotent request may already have been processed — never
		// replay it (the "never retry a submission after a 2xx" rule,
		// extended to the moment the server first answers).
		if !req.idempotent {
			return attemptResult{err: networkError(resp.StatusCode, readErr), retry: false}
		}
		return attemptResult{err: networkError(resp.StatusCode, readErr), retry: true}
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		// A malformed 2xx body is terminal, not retryable: the server may
		// already have processed a non-idempotent request.
		data, err := unwrapEnvelope(body)
		if err != nil {
			return attemptResult{err: fmt.Errorf("decoding response body: %w", err)}
		}
		return attemptResult{data: data}
	}
	retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	code, message, detail, data := errorPayload(body)
	ra := time.Duration(0)
	if resp.StatusCode == 429 {
		ra = retryAfter
	}
	se := statusError(resp.StatusCode, code, message, data, ra)
	se.CourseSlug, se.EnrollURL = detail.CourseSlug, detail.EnrollURL
	switch {
	case resp.StatusCode == 429:
		// Rate limited: retry honouring Retry-After.
		return attemptResult{err: se, retry: true, delay: retryAfter}
	case resp.StatusCode >= 500:
		return attemptResult{err: se, retry: true}
	default:
		return attemptResult{err: se}
	}
}

// send performs the raw HTTP exchange, setting the §5.1 headers on every
// request and the §5.4 signature when the request is signed.
func (c *Client) send(ctx context.Context, req request) (*http.Response, error) {
	var body io.Reader
	if req.body != nil {
		body = bytes.NewReader(req.body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, req.method, c.baseURL+req.path, body)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", buildinfo.UserAgent())
	httpReq.Header.Set(HeaderCLIVersion, buildinfo.Version)
	if c.sessionKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.sessionKey)
	}
	if req.sign && c.sessionKey != "" {
		httpReq.Header.Set(HeaderSignature, signBody(c.sessionKey, req.body))
	}
	return c.http.Do(httpReq)
}

// signBody computes X-Grid-Signature: sha256=<hex> over the exact request
// bytes (GRID_SPEC §5.4). The caller marshals once and posts the same buffer
// that was signed — that pairing is what the server verifies.
func signBody(sessionKey string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(sessionKey))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// wait sleeps for d, honouring context cancellation.
func (c *Client) wait(ctx context.Context, d time.Duration) error {
	if c.sleep != nil {
		return c.sleep(ctx, d)
	}
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// backoffFor returns the delay before the given attempt (1-based): base *
// 2^(attempt-1), capped at 30s, plus up to 25% jitter.
func backoffFor(base time.Duration, attempt int) time.Duration {
	if base <= 0 {
		return 0
	}
	d := base
	for i := 1; i < attempt && d < 30*time.Second; i++ {
		d *= 2
	}
	d += time.Duration(rand.Int64N(int64(d/4) + 1))
	return d
}

// parseRetryAfter parses the Retry-After header: either delay-seconds or an
// HTTP-date (RFC 7231).
func parseRetryAfter(v string, now time.Time) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// envelopeProbe sniffs whether a body is a §6 envelope ({success, statusCode,
// message, data}) or a raw §5 payload. The stub always wraps; the real
// backend may not — the client tolerates both (plan Assumption 3). It also
// captures the two confirmed top-level exceptions, both siblings of
// `message` rather than nested in `data` (GRID_SPEC §5.4b): `status` on
// device-poll errors, and `code` on submission errors.
func envelopeProbe(body []byte) (*Envelope, bool) {
	var env struct {
		Success    *bool           `json:"success"`
		StatusCode *int            `json:"statusCode"`
		Message    string          `json:"message"`
		Status     string          `json:"status"`
		Code       string          `json:"code"`
		Errors     []ErrorDetail   `json:"errors"`
		Data       json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, false
	}
	if env.Success == nil || env.StatusCode == nil {
		return nil, false
	}
	return &Envelope{
		Success:    *env.Success,
		StatusCode: *env.StatusCode,
		Message:    env.Message,
		Status:     env.Status,
		Code:       env.Code,
		Errors:     env.Errors,
		Data:       env.Data,
	}, true
}

// unwrapEnvelope extracts the payload from a 2xx body, tolerating both the
// wrapped and raw shapes.
func unwrapEnvelope(body []byte) (json.RawMessage, error) {
	if env, ok := envelopeProbe(body); ok {
		if env.Data == nil {
			return json.RawMessage("null"), nil
		}
		return env.Data, nil
	}
	return json.RawMessage(body), nil
}

// errorPayload extracts the machine code, human message, the first error
// detail entry (if any), and data from an error body. It tolerates the
// confirmed wire shapes:
//   - the real backend's envelope (GRID_SPEC §5.4b): `message` is human text
//     ("Authorization pending"); the machine state is a top-level sibling —
//     `status` ("pending" · "slow_down" · "expired" · "denied") on
//     device-poll errors, or `code` ("attempt_expired" ·
//     "nonce_already_used" · "attempt_already_passed") on submission
//     errors;
//   - a plain ApiError (e.g. not_enrolled, GRID_SPEC §5.2/§5.3/§6): no
//     top-level `code` at all — the machine code and extra fields
//     (courseSlug, enrollUrl) live in errors[0] instead, confirmed
//     2026-11-13 against a live seeded backend;
//   - the stub's legacy envelope: machine code in `message`, detail in `data`;
//   - raw bodies (e.g. {"status":"pending"}).
func errorPayload(body []byte) (code, message string, detail ErrorDetail, data json.RawMessage) {
	if env, ok := envelopeProbe(body); ok {
		code = env.Code
		if code == "" {
			code = env.Status
		}
		if len(env.Errors) > 0 {
			detail = env.Errors[0]
			if code == "" {
				code = detail.Code
			}
		}
		if code == "" {
			// Legacy shape: the envelope message carried the machine code.
			code = env.Message
		}
		return code, env.Message, detail, env.Data
	}
	var raw struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &raw); err == nil && raw.Status != "" {
		return raw.Status, "", ErrorDetail{}, json.RawMessage(body)
	}
	return "", "", ErrorDetail{}, json.RawMessage(body)
}

// stubTransport routes requests into an in-process stub handler, so
// --api stub:// (or GRID_STUB=1) runs the whole CLI without a network.
type stubTransport struct {
	handler http.Handler
}

// RoundTrip implements http.RoundTripper by replaying the request through
// the stub handler and packaging the recorded response.
func (t stubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		req.Body.Close()
		body = b
	}
	target := "http://grid-stub.internal" + req.URL.Path
	if req.URL.RawQuery != "" {
		target += "?" + req.URL.RawQuery
	}
	hreq := httptest.NewRequest(req.Method, target, bytes.NewReader(body))
	hreq.Header = req.Header.Clone()
	rec := httptest.NewRecorder()
	t.handler.ServeHTTP(rec, hreq)
	return rec.Result(), nil
}

// DeviceStart initiates device pairing (POST /cli/device/start, §5.1).
func (c *Client) DeviceStart(ctx context.Context, r DeviceStartRequest) (DeviceStartResponse, error) {
	body, err := json.Marshal(r)
	if err != nil {
		return DeviceStartResponse{}, err
	}
	data, err := c.do(ctx, request{method: http.MethodPost, path: "/cli/device/start", body: body})
	if err != nil {
		return DeviceStartResponse{}, err
	}
	var out DeviceStartResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return DeviceStartResponse{}, fmt.Errorf("decoding device-start response: %w", err)
	}
	return out, nil
}

// DevicePoll polls pairing approval (POST /cli/device/poll, §5.1). Poll
// states are surfaced, not retried: pending (428) and slow_down (429) are
// timed by the caller (grid login), so the client never auto-retries them.
func (c *Client) DevicePoll(ctx context.Context, deviceCode string) (DevicePollResponse, error) {
	body, err := json.Marshal(DevicePollRequest{DeviceCode: deviceCode})
	if err != nil {
		return DevicePollResponse{}, err
	}
	data, err := c.do(ctx, request{method: http.MethodPost, path: "/cli/device/poll", body: body, noRetry: true})
	if err != nil {
		return DevicePollResponse{}, err
	}
	var out DevicePollResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return DevicePollResponse{}, fmt.Errorf("decoding device-poll response: %w", err)
	}
	return out, nil
}

// Attempt fetches the lesson attempt (GET /cli/lessons/:lessonId/attempt,
// §5.2).
func (c *Client) Attempt(ctx context.Context, lessonID string) (AttemptResponse, error) {
	data, err := c.do(ctx, request{method: http.MethodGet, path: "/cli/lessons/" + url.PathEscape(lessonID) + "/attempt", idempotent: true})
	if err != nil {
		return AttemptResponse{}, err
	}
	var out AttemptResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return AttemptResponse{}, fmt.Errorf("decoding attempt response: %w", err)
	}
	return out, nil
}

// Submit posts the device-signed AttestationEnvelope (POST /cli/submissions,
// §5.3/A4). The envelope is marshalled exactly once — into its §5.3
// canonical-JSON form, the same byte family the deviceSignature covers —
// and those exact bytes are HMAC-signed and posted. A 413 surfaces as
// ErrTooLarge so the caller can re-truncate at half cap and retry once; a
// 422 attestation failure surfaces as ErrAttestation carrying the server's
// machine code (§5.4).
func (c *Client) Submit(ctx context.Context, env *attest.AttestationEnvelope) (SubmissionResponse, error) {
	// GRID_SPEC §5.4 hard invariant: marshal once, sign those exact bytes,
	// post that exact buffer. Canonical bytes also guarantee the server's
	// re-canonicalization of the parsed body matches what was signed.
	body, err := attest.Canonicalize(env)
	if err != nil {
		return SubmissionResponse{}, fmt.Errorf("canonicalizing the submission envelope: %w", err)
	}
	data, err := c.do(ctx, request{method: http.MethodPost, path: "/cli/submissions", body: body, sign: true})
	if err != nil {
		return SubmissionResponse{}, err
	}
	var out SubmissionResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return SubmissionResponse{}, fmt.Errorf("decoding submission response: %w", err)
	}
	return out, nil
}

// Me fetches the current account (GET /cli/me, §5.5).
func (c *Client) Me(ctx context.Context) (MeResponse, error) {
	data, err := c.do(ctx, request{method: http.MethodGet, path: "/cli/me", idempotent: true})
	if err != nil {
		return MeResponse{}, err
	}
	var out MeResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return MeResponse{}, fmt.Errorf("decoding me response: %w", err)
	}
	return out, nil
}

// Logout revokes the server session (DELETE /cli/session, §5.5).
func (c *Client) Logout(ctx context.Context) error {
	_, err := c.do(ctx, request{method: http.MethodDelete, path: "/cli/session", idempotent: true})
	return err
}

// Courses fetches enrolled courses (GET /cli/courses, §5.5).
func (c *Client) Courses(ctx context.Context) (CoursesResponse, error) {
	data, err := c.do(ctx, request{method: http.MethodGet, path: "/cli/courses", idempotent: true})
	if err != nil {
		return CoursesResponse{}, err
	}
	var out CoursesResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return CoursesResponse{}, fmt.Errorf("decoding courses response: %w", err)
	}
	return out, nil
}
