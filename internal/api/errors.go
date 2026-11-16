package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Sentinel errors. Commands classify failures with errors.Is against these
// (TASK.md §3 exit codes) and print the matching user hint. They never carry
// the session key, a signature, or any secret material.
var (
	// ErrAttemptExpired maps HTTP 410 attempt_expired (GRID_SPEC §5.4).
	ErrAttemptExpired = errors.New("attempt expired")
	// ErrNonceAlreadyUsed maps HTTP 409 nonce_already_used (GRID_SPEC §5.4,
	// GRID_SYNC_CLAUDE.md 2026-11-12 01:05): the submitted nonce was already
	// spent — a genuine replay, or a local attempt.json that fell behind the
	// server's rotated nonce.
	ErrNonceAlreadyUsed = errors.New("nonce already used")
	// ErrAttemptAlreadyPassed maps HTTP 409 attempt_already_passed: the
	// attempt is closed by a prior PASSED verdict. Redoing the lesson needs a
	// fresh `grid run`.
	ErrAttemptAlreadyPassed = errors.New("attempt already passed")
	// ErrUnauthorized maps HTTP 401 (missing, invalid, or expired session;
	// also a signature mismatch per §5.4).
	ErrUnauthorized = errors.New("unauthorized")
	// ErrDenied maps HTTP 403 (device pairing denied, access denied).
	ErrDenied = errors.New("access denied")
	// ErrNotEnrolled maps HTTP 403 code=not_enrolled (GRID_SPEC §5.2/§5.3/
	// §6, "no auto-enroll"): the account is authenticated but not enrolled
	// in the lesson's course. Distinct from ErrDenied — every other 403
	// (device pairing denied, insufficient access tier) — because this one
	// comes with a courseSlug/enrollUrl the caller can act on immediately,
	// and the CLI never enrolls on the user's behalf.
	ErrNotEnrolled = errors.New("not enrolled")
	// ErrSlowDown maps HTTP 429 slow_down.
	ErrSlowDown = errors.New("slow down")
	// ErrPending maps HTTP 428 pending (device pairing still awaiting the
	// user).
	ErrPending = errors.New("pending")
	// ErrTooLarge maps HTTP 413: the evidence payload exceeded the server's
	// caps. The submit path re-truncates at half cap and retries once
	// (GRID_SPEC §5.4); the truncation policy lives in internal/runner.
	ErrTooLarge = errors.New("evidence payload too large")
	// ErrAttestation maps HTTP 422 (GRID_SPEC §5.4/§5.5/A5/A6): the server
	// rejected the submission envelope itself. StatusError.Code carries the
	// machine code — invalid_envelope, invalid_device_signature,
	// invalid_client_binary, invalid_ext_signature, causality_violation.
	ErrAttestation = errors.New("attestation rejected")
	// ErrServer maps HTTP 5xx.
	ErrServer = errors.New("server error")
	// ErrNetwork maps transport-level failures (connection refused, DNS,
	// timeouts, truncated responses).
	ErrNetwork = errors.New("network error")
	// ErrUnexpected maps any status the client has no specific handling for
	// (404, 400, ...) so callers never see an unclassified error.
	ErrUnexpected = errors.New("unexpected server response")
)

// Kind discriminates StatusError values.
type Kind int

const (
	KindUnknown Kind = iota
	KindAttemptExpired
	KindNonceAlreadyUsed
	KindAttemptAlreadyPassed
	KindUnauthorized
	KindDenied
	KindNotEnrolled
	KindSlowDown
	KindPending
	KindTooLarge
	KindAttestation
	KindServer
	KindNetwork
	KindUnexpected
)

// String returns a short human name for the kind.
func (k Kind) String() string {
	switch k {
	case KindAttemptExpired:
		return "attempt expired"
	case KindNonceAlreadyUsed:
		return "nonce already used"
	case KindAttemptAlreadyPassed:
		return "attempt already passed"
	case KindUnauthorized:
		return "unauthorized"
	case KindDenied:
		return "access denied"
	case KindNotEnrolled:
		return "not enrolled"
	case KindSlowDown:
		return "rate limited"
	case KindPending:
		return "pending"
	case KindTooLarge:
		return "payload too large"
	case KindAttestation:
		return "attestation rejected"
	case KindServer:
		return "server error"
	case KindNetwork:
		return "network error"
	default:
		return "unexpected response"
	}
}

// StatusError is a classified API failure: every HTTP status the server can
// return maps to one Kind and one sentinel, so commands can switch on
// errors.Is and print an actionable hint.
type StatusError struct {
	// Kind classifies the failure.
	Kind Kind
	// StatusCode is the HTTP status, or 0 for transport-level failures.
	StatusCode int
	// Code is the server's machine code — the top-level `status` sibling on
	// device-poll errors ("pending", "slow_down", "expired", "denied") or the
	// top-level `code` sibling on submission errors ("attempt_expired",
	// "nonce_already_used", "attempt_already_passed" — GRID_SPEC §5.4b's
	// second code-carrying exception) — or a synthesised default.
	Code string
	// Message is the server's human-readable message, if any.
	Message string
	// Data is the unwrapped error payload from the envelope's `data`, if any
	// (the confirmed wire puts error detail in `errors`, so this is usually
	// nil against the real backend; the legacy stub shape used it).
	Data json.RawMessage
	// RetryAfter is the parsed Retry-After header on 429 responses.
	RetryAfter time.Duration
	// CourseSlug and EnrollURL are populated only for the not_enrolled
	// detail (GRID_SPEC §5.2/§5.3/§6): a 403 the user can resolve
	// themselves by enrolling, so callers print EnrollURL rather than the
	// generic "access denied" hint. Empty for every other error.
	CourseSlug string
	EnrollURL  string

	sentinel error
}

// Error implements error. It deliberately renders only classification data —
// never the session key, a signature, or environment values.
func (e *StatusError) Error() string {
	msg := fmt.Sprintf("grid api: %s", e.Kind)
	if e.StatusCode > 0 {
		msg += fmt.Sprintf(" (HTTP %d)", e.StatusCode)
	}
	if e.Code != "" {
		msg += ": " + e.Code
	}
	if e.Message != "" && e.Message != e.Code {
		msg += ": " + e.Message
	}
	return msg
}

// Unwrap returns the sentinel error for the kind so errors.Is works.
func (e *StatusError) Unwrap() error { return e.sentinel }

// statusError builds the typed error for an HTTP status. code is the server's
// machine code — the top-level `status` sibling on device-poll errors, the
// top-level `code` sibling on submission errors (GRID_SPEC §5.4b), errors[0]
// .code on a plain ApiError (not_enrolled), or the envelope message on the
// legacy shape — and overrides the per-status default when non-empty.
// message is the server's human text, kept for rendering. CourseSlug/
// EnrollURL are not set here — the caller (Client.attempt) populates them
// separately from the same errors[0] entry, since every other call site that
// builds a StatusError has nothing to put there.
//
// 409 is shared by two distinct submission outcomes that only the `code`
// sibling can tell apart (GRID_SYNC_CLAUDE.md 2026-11-12 06:00):
// nonce_already_used and attempt_already_passed. code == "attempt_already_
// passed" selects the latter; every other 409 (including no code at all,
// e.g. a test forcing the bare status) defaults to nonce_already_used, the
// more common replay case. Similarly, 403 is shared by not_enrolled (code ==
// "not_enrolled") and every other forbidden outcome (ErrDenied).
func statusError(statusCode int, code, message string, data json.RawMessage, retryAfter time.Duration) *StatusError {
	kind, sentinel, defCode := KindUnexpected, ErrUnexpected, ""
	switch {
	case statusCode == 401:
		kind, sentinel, defCode = KindUnauthorized, ErrUnauthorized, "unauthorized"
	case statusCode == 403 && code == "not_enrolled":
		kind, sentinel, defCode = KindNotEnrolled, ErrNotEnrolled, "not_enrolled"
	case statusCode == 403:
		kind, sentinel, defCode = KindDenied, ErrDenied, "forbidden"
	case statusCode == 409 && code == "attempt_already_passed":
		kind, sentinel, defCode = KindAttemptAlreadyPassed, ErrAttemptAlreadyPassed, "attempt_already_passed"
	case statusCode == 409:
		kind, sentinel, defCode = KindNonceAlreadyUsed, ErrNonceAlreadyUsed, "nonce_already_used"
	case statusCode == 410:
		kind, sentinel, defCode = KindAttemptExpired, ErrAttemptExpired, "attempt_expired"
	case statusCode == 413:
		kind, sentinel, defCode = KindTooLarge, ErrTooLarge, "payload_too_large"
	case statusCode == 422:
		// §5.4 attestation failures; the code sibling (or its absence)
		// rides through verbatim — the CLI surfaces it (§5.5/A6: exit 2).
		kind, sentinel, defCode = KindAttestation, ErrAttestation, "invalid_envelope"
	case statusCode == 428:
		kind, sentinel, defCode = KindPending, ErrPending, "pending"
	case statusCode == 429:
		kind, sentinel, defCode = KindSlowDown, ErrSlowDown, "slow_down"
	case statusCode >= 500:
		kind, sentinel, defCode = KindServer, ErrServer, "server_error"
	}
	if code == "" {
		code = defCode
	}
	return &StatusError{
		Kind:       kind,
		StatusCode: statusCode,
		Code:       code,
		Message:    message,
		Data:       data,
		RetryAfter: retryAfter,
		sentinel:   sentinel,
	}
}

// networkError builds the transport-level typed error.
func networkError(statusCode int, cause error) *StatusError {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	return &StatusError{Kind: KindNetwork, StatusCode: statusCode, Code: "network_error", Message: msg, sentinel: ErrNetwork}
}

// UserHint returns the actionable next step for a typed API error ("what to
// type next" — TASK.md §7). Empty for non-API errors.
func UserHint(err error) string {
	switch {
	case errors.Is(err, ErrAttemptExpired):
		return "This attempt expired. Run `grid run <id>` again."
	case errors.Is(err, ErrNonceAlreadyUsed):
		return "Your local attempt file is stale — run `grid run` again."
	case errors.Is(err, ErrAttemptAlreadyPassed):
		return "You already passed this — run `grid run <id>` to redo it."
	case errors.Is(err, ErrUnauthorized):
		return "Run `grid login`."
	case errors.Is(err, ErrDenied):
		return "This Grid account doesn't have access to that."
	case errors.Is(err, ErrNotEnrolled):
		return "Enroll in the course first, then try again."
	case errors.Is(err, ErrSlowDown):
		return "The server asked us to slow down. Wait a moment and try again."
	case errors.Is(err, ErrPending):
		return "Still pending — keep waiting."
	case errors.Is(err, ErrTooLarge):
		return "The evidence payload was too large for the server."
	case errors.Is(err, ErrAttestation):
		return "The server rejected this submission's attestation. Update the CLI and try again."
	case errors.Is(err, ErrServer):
		return "The Grid server hit an error. Try again in a moment."
	case errors.Is(err, ErrNetwork):
		return "Could not reach the Grid server. Check your connection and the --api URL."
	default:
		return ""
	}
}
