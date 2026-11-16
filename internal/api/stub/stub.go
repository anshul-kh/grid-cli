// Package stub is an in-process implementation of every GRID_SPEC §5 CLI
// endpoint, with canned fixtures for the Redis/Go catalogue lesson
// (build-a-redis-core-in-go / 02-speak-resp), so the whole CLI is testable
// end-to-end before the real backend ships (TASK.md §9).
//
// Wire shape: every response — success and error — is wrapped in the §6
// envelope {success, statusCode, message, data}, with two confirmed
// exceptions (GRID_SPEC §5.4b): device-poll errors carry the poll state as a
// TOP-LEVEL `status` sibling of `message` with no `data`
// ({"success":false,"statusCode":428,"message":"Authorization pending",
// "status":"pending"}); submission errors for the named outcomes
// (attempt_expired, nonce_already_used, attempt_already_passed — and the
// §5.4 attestation failures, 400/422 invalid_envelope et al.) carry a
// TOP-LEVEL `code` sibling the same way. Error detail belongs in `errors`,
// never `data`. This is the canonical shape the internal/api client
// unwraps; the client also tolerates raw §5 bodies for robustness.
//
// The package is deliberately self-contained (it imports nothing from
// internal/api): internal/api imports it to implement the `stub://` base URL
// scheme, so fixtures are raw JSON strings mirroring §5 rather than shared
// types. The internal/api tests prove the client decodes these fixtures into
// the typed structs.
package stub

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// Fixed fixture identities. SessionKey is the test session key every stub
// response is signed/authorised with; it is only ever compared, never
// printed. It is the base64url encoding of 32 bytes, matching §5.1's session
// key shape.
var SessionKey = base64.RawURLEncoding.EncodeToString([]byte("stub-session-key-0123456789abcde"))

const (
	// DeviceCode is the device code returned by /cli/device/start.
	DeviceCode = "stub-device-code"
	// UserCode is the pairing code the user types into the browser
	// (GRID_SPEC §5.1 example shape).
	UserCode = "K7QP-3M2X"
	// VerificationURL is the page the user approves pairing on. This must
	// mirror what the real backend returns (GRID_SPEC §5.1:
	// <WEB_URL>/cli/link?code=<userCode>) — the CLI opens the server's string
	// verbatim, so a stub path that does not exist in grid-ghost sends anyone
	// tracing `grid login --api stub://` to a 404 and makes a working pairing
	// flow look broken. /device was that path; it is now only a redirect.
	VerificationURL = "http://localhost:3000/cli/link?code=" + UserCode
	// TestUserID identifies the seeded test user.
	TestUserID = "0d0a1f2e-5c4b-4a3d-9e8f-7a6b5c4d3e2f"

	// CourseSlug and LessonSlug select the Redis/Go catalogue lesson.
	CourseSlug = "build-a-redis-core-in-go"
	LessonSlug = "02-speak-resp"
	// LessonID is the lesson UUID the attempt fixture is bound to.
	LessonID = "f2908a1e-6b6c-4e4d-9f6e-8b0a0d9a3f21"
	// AttemptID and AttemptNonce identify the single-use attempt.
	AttemptID    = "7c3d1b2a-4e5f-4a6b-8c9d-0e1f2a3b4c5d"
	AttemptNonce = "stub-attempt-nonce-0001"
)

// checkSpecFixture is the *public* check-spec view (GRID_SPEC §5.2): ids,
// titles, hints, types, and the fields needed to perform a measurement. It
// deliberately contains no expected values (pattern, equals, sha256, value,
// must_include) — the server is the judge. version is an integer (GRID_SPEC
// §4's grammar example: `version: 1`, matching spec.CheckSpec.Version int) —
// not a string, so internal/spec.Parse can decode this fixture directly.
const checkSpecFixture = `{
  "version": 1,
  "workdir": ".",
  "timeout": "180s",
  "checks": [
    {
      "id": "resp-server-exists",
      "title": "The RESP server binary exists",
      "hint": "Build your server into the workspace root.",
      "type": "file.exists",
      "path": "server"
    },
    {
      "id": "resp-server-pings",
      "title": "RESP server answers PING",
      "hint": "Wire PING into your command loop.",
      "type": "cmd.stdout_matches",
      "run": ["./server"],
      "timeout": "5s"
    },
    {
      "id": "resp-server-handles-commands",
      "title": "Server handles the COMMAND listing",
      "hint": "Implement the COMMAND command.",
      "type": "cmd.exit_code",
      "run": ["./server", "COMMAND"],
      "timeout": "5s"
    }
  ]
}`

// attemptResponseTemplate is the GET /cli/lessons/:id/attempt data payload,
// with the course/lesson/attempt fixture values interpolated once at init.
const attemptResponseTemplate = `{
  "attempt": {"id": "%s", "nonce": "%s", "expiresAt": "2030-01-01T00:00:00Z"},
  "lesson": {"id": "%s", "title": "Speak RESP", "courseSlug": "%s", "lessonSlug": "%s", "difficulty": "intermediate", "estimatedMinutes": 30},
  "workspace": {"dirName": "grid-%s-%s", "files": [
    {"path": "README.md", "contents": "# Speak RESP\n\nMake your server speak the Redis Serialization Protocol.\n"},
    {"path": "go.mod", "contents": "module resp-server\n\ngo 1.27\n"}
  ]},
  "checkSpec": %s,
  "urls": {"lessonUrl": "http://localhost:3000/courses/%s/lessons/%s"}
}`

var attemptResponseFixture = fmt.Sprintf(
	attemptResponseTemplate,
	AttemptID, AttemptNonce, LessonID, CourseSlug, LessonSlug,
	CourseSlug, LessonSlug,
	checkSpecFixture,
	CourseSlug, LessonSlug,
)

// pollSuccessFixture is the 200 device-poll payload (GRID_SPEC §5.1).
var pollSuccessFixture = fmt.Sprintf(
	`{"sessionKey": "%s", "user": {"id": "%s", "username": "grid-tester", "displayName": "Grid Tester", "avatarUrl": "https://grid.dev/img/tester.png"}}`,
	SessionKey, TestUserID,
)

// PassedSubmissionFixture is the default 200 submission payload (GRID_SPEC
// §5.3): the server's verdict — everything in it is the server's judgement.
// It is the default response queue entry (see Server.responses) and exported
// so cmd-level tests can reuse it verbatim when building a response queue
// (e.g. SetSubmissionResponses([]string{FailedSubmissionFixture,
// PassedSubmissionFixture})).
const PassedSubmissionFixture = `{
  "submissionId": "c1a2b3c4-d5e6-4f70-8a9b-0c1d2e3f4a5b",
  "verdict": "PASSED",
  "checks": [
    {"checkId": "resp-server-exists", "title": "The RESP server binary exists", "passed": true, "message": "Found the server binary."},
    {"checkId": "resp-server-pings", "title": "RESP server answers PING", "passed": true, "message": "PING answered."},
    {"checkId": "resp-server-handles-commands", "title": "Server handles the COMMAND listing", "passed": true, "message": "COMMAND exited cleanly."}
  ],
  "xp": {"delta": 94, "total": 1284, "penaltyCapped": false},
  "stats": {"rank": "OPERATOR", "level": 3, "currentStreak": 4, "lessonsCompleted": 12},
  "unlocked": {"badges": [{"slug": "resp-speaker", "name": "RESP Speaker", "tier": "bronze", "iconKey": "resp-speaker", "description": "Spoke RESP for the first time."}], "achievements": []},
  "next": {"lessonId": "4b6c7d8e-9f0a-4b1c-2d3e-4f5a6b7c8d9e", "title": "Command dispatch"}
}`

// FailedSubmissionFixture is a canned FAILED-verdict payload: one check
// judged wrong, a small XP delta, no next lesson. Exported for tests driving
// the "submit, fix, submit again" retry loop (GRID_SYNC_CLAUDE.md 2026-11-12
// 01:05) via SetSubmissionResponses — the single most common path through
// the product, and the one grid submit's golden test must cover.
const FailedSubmissionFixture = `{
  "submissionId": "d2b3c4d5-e6f7-4081-9a0b-1c2d3e4f5a6c",
  "verdict": "FAILED",
  "checks": [
    {"checkId": "resp-server-exists", "title": "The RESP server binary exists", "passed": true, "message": "Found the server binary."},
    {"checkId": "resp-server-pings", "title": "RESP server answers PING", "passed": false, "message": "Expected +PONG\r\n, got nothing.", "hint": "Wire PING into your command loop."},
    {"checkId": "resp-server-handles-commands", "title": "Server handles the COMMAND listing", "passed": true, "message": "COMMAND exited cleanly."}
  ],
  "xp": {"delta": 5, "total": 1190, "penaltyCapped": false},
  "stats": {"rank": "OPERATOR", "level": 3, "currentStreak": 4, "lessonsCompleted": 12},
  "unlocked": {"badges": [], "achievements": []},
  "next": null
}`

// InvalidSubmissionFixture is the automatic INVALID response (GRID_SPEC §5.3
// addendum): served whenever the submitted results[] show a setup failure
// or are empty — the one piece of server-side judging the stub actually
// implements, because it is a structural rule about what was submitted, not
// a content-specific verdict (see Server.nextFixture).
const InvalidSubmissionFixture = `{
  "submissionId": "e3c4d5e6-f708-4192-ab1c-2d3e4f5a6b7d",
  "verdict": "INVALID",
  "checks": [
    {"checkId": "resp-server-exists", "title": "The RESP server binary exists", "passed": false, "message": "Setup failed — this check was not evaluated."},
    {"checkId": "resp-server-pings", "title": "RESP server answers PING", "passed": false, "message": "Setup failed — this check was not evaluated."},
    {"checkId": "resp-server-handles-commands", "title": "Server handles the COMMAND listing", "passed": false, "message": "Setup failed — this check was not evaluated."}
  ],
  "xp": {"delta": 0, "total": 1190, "penaltyCapped": false},
  "stats": {"rank": "OPERATOR", "level": 3, "currentStreak": 4, "lessonsCompleted": 12},
  "unlocked": {"badges": [], "achievements": []},
  "next": null
}`

// setupResultPrefix is the §5.3 synthetic checkId convention for reporting a
// setup[] step's outcome: `__setup__#<index>`, 0-based, declaration order.
const setupResultPrefix = "__setup__#"

// meFixture is the GET /cli/me payload (§5.4b): the confirmed FLAT shape —
// no nested user object, currentStreak/longestStreak (not "streak"), level
// alongside rank.
const meFixture = `{
  "id": "` + TestUserID + `",
  "username": "grid-tester",
  "displayName": "Grid Tester",
  "avatarUrl": "https://grid.dev/img/tester.png",
  "role": "USER",
  "xp": 1284,
  "level": 13,
  "rank": "OPERATOR",
  "currentStreak": 4,
  "longestStreak": 11
}`

// coursesFixture is the GET /cli/courses payload (§5.4b): the confirmed
// entry shape — progress is enrollment.progressPct plus lessonCount; there
// is no lessonsCompleted field.
const coursesFixture = `{
  "courses": [
    {"id": "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5e", "slug": "` + CourseSlug + `", "title": "Build a Redis core in Go", "difficulty": "INTERMEDIATE", "language": "go", "estimatedMinutes": 60, "iconKey": "redis", "accentColor": "#dc382d", "lessonCount": 8, "enrollment": {"status": "ACTIVE", "progressPct": 25, "lastLessonId": "` + LessonID + `", "enrolledAt": "2026-11-12T05:00:00.000Z"}}
  ]
}`

// sessionRevokedFixture is the DELETE /cli/session payload (§5.5).
const sessionRevokedFixture = `{"revoked": true}`

// Server is the in-process stub. It implements http.Handler, so tests can
// serve it with httptest.NewServer for real HTTP semantics (signature
// headers over the wire) and the api client serves it in-process for the
// stub:// scheme. All state is mutex-guarded; a Server is safe for concurrent
// use.
type Server struct {
	mu sync.Mutex
	// pollStates is the queued device-poll outcome sequence; it defaults to
	// pending → slow_down → success and repeats its last element when
	// exhausted — except that "success" is served exactly once (see
	// keyCollected).
	pollStates []string
	pollIdx    int
	// keyCollected records that the 200 poll success (the session key) has
	// been served. The real backend returns the key EXACTLY ONCE and answers
	// 410 expired to any later poll (confirmed flow, GRID_SYNC_CLAUDE.md
	// 05:25 + 08:20 READY): persist on first receipt.
	keyCollected bool
	// pollInterval is the `interval` the start response advertises.
	pollInterval int
	// forced maps a normalized path (e.g. "/cli/submissions") to a status
	// code the endpoint must return instead of its normal behaviour — used
	// to exercise 401/409/410/413 paths.
	forced map[string]int
	// usedNonces records every nonce consumed by an accepted submission —
	// test bookkeeping only; acceptance itself is gated on currentNonce.
	usedNonces map[string]bool
	// currentNonce is the nonce the next submission against the fixture
	// attempt must present (GRID_SPEC §5.4: single-use, rotating — the
	// attempt survives a non-passing submission, and the server hands back
	// the next value as nextNonce). Starts at the attempt fixture's nonce.
	currentNonce string
	// nonceSeq generates fresh rotated nonces deterministically.
	nonceSeq int
	// attemptPassed records that a PASSED verdict has closed the fixture
	// attempt: every later submission is 409 attempt_already_passed,
	// whatever nonce it presents.
	attemptPassed bool
	// responses is the queue of canned submission response bodies served in
	// order to accepted (non-forced, non-replay, non-setup-failure)
	// submissions, repeating the last entry once exhausted.
	// SetSubmissionResponses configures it; empty means "always
	// PassedSubmissionFixture" (see nextFixture).
	responses   []string
	responseIdx int
	// submissions counts accepted (non-forced, non-replay) submissions.
	submissions int
	// polls counts device-poll requests.
	polls int
	// workspaceDirNameOverride, when non-empty, replaces the attempt
	// fixture's workspace.dirName. Test knob: lets a test serve a malicious
	// or malformed dirName (e.g. "../escape") over real HTTP, so `grid run`
	// end-to-end — not just the exported workspace helpers in isolation —
	// is provably safe against a server-controlled escape attempt
	// (GRID_SPEC §5.2).
	workspaceDirNameOverride string
	// workspaceModeOverride, when non-empty, replaces the attempt fixture's
	// workspace.mode (default "folder" — see attemptResponseTemplate). Test
	// knob: SetWorkspaceMode.
	workspaceModeOverride string
	// notEnrolled maps a normalized path to a forced §5.2/§5.3/§6
	// not_enrolled detail — ForceNotEnrolled.
	notEnrolled map[string]notEnrolledDetail
	// devicePublicKey is the most recently registered device public key
	// (§5.1: device/start requires it) — test bookkeeping only, surfaced by
	// LastDevicePublicKey.
	devicePublicKey string
}

// notEnrolledDetail is a forced not_enrolled override (test knob) — see
// ForceNotEnrolled.
type notEnrolledDetail struct {
	courseSlug string
	enrollURL  string
}

// New returns a stub Server with the default poll sequence
// pending → slow_down → success and a 3-second poll interval.
func New() *Server {
	return &Server{
		pollStates:   []string{"pending", "slow_down", "success"},
		pollInterval: 3,
		forced:       map[string]int{},
		usedNonces:   map[string]bool{},
		currentNonce: AttemptNonce,
		notEnrolled:  map[string]notEnrolledDetail{},
	}
}

// SetPollInterval changes the `interval` the start response advertises
// (default 3 seconds). Test knob: 0 makes a login flow poll without
// waiting, which keeps command-level tests fast; real servers always
// advertise a positive interval (GRID_SPEC §5.1).
func (s *Server) SetPollInterval(seconds int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pollInterval = seconds
}

// SetWorkspaceDirName overrides the workspace.dirName the attempt fixture
// serves (test knob — see the Server.workspaceDirNameOverride field doc).
func (s *Server) SetWorkspaceDirName(dirName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workspaceDirNameOverride = dirName
}

// SetWorkspaceMode overrides the workspace.mode the attempt fixture serves
// (default "folder" — test knob, GRID_SPEC §4).
func (s *Server) SetWorkspaceMode(mode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workspaceModeOverride = mode
}

// ForceNotEnrolled makes the endpoint at path return the real §5.2/§5.3/§6
// 403 not_enrolled body — GRID_SPEC's "no auto-enroll" decision, confirmed
// 2026-11-13 against a live seeded backend — instead of its normal
// behaviour, so CLI tests can drive the not_enrolled path without a real
// backend. Pass empty courseSlug and enrollURL to clear it. Paths are
// normalized the same way ForceStatus's are.
func (s *Server) ForceNotEnrolled(path, courseSlug, enrollURL string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path = normalizePath(path)
	if courseSlug == "" && enrollURL == "" {
		delete(s.notEnrolled, path)
		return
	}
	s.notEnrolled[path] = notEnrolledDetail{courseSlug: courseSlug, enrollURL: enrollURL}
}

// SetPollStates replaces the device-poll outcome queue (values: "pending",
// "slow_down", "expired", "denied", "success").
func (s *Server) SetPollStates(states []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pollStates = append([]string(nil), states...)
	s.pollIdx = 0
}

// ForceStatus makes the endpoint at path return the given status on every
// call, with the standard machine code for that status (pass 0 to clear).
// Paths are normalized ("/api/v1" prefix optional).
func (s *Server) ForceStatus(path string, code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path = normalizePath(path)
	if code == 0 {
		delete(s.forced, path)
	} else {
		s.forced[path] = code
	}
}

// SubmissionCount returns how many submissions were accepted.
func (s *Server) SubmissionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.submissions
}

// SetSubmissionResponses replaces the queue of canned submission response
// bodies served to accepted submissions in order, repeating the last entry
// once exhausted. Test knob: e.g.
// SetSubmissionResponses([]string{FailedSubmissionFixture,
// PassedSubmissionFixture}) drives "submit, fix, submit again" without a
// fresh `grid run` — nonce rotation and retry plumbing, exercised end to end.
func (s *Server) SetSubmissionResponses(bodies []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.responses = append([]string(nil), bodies...)
	s.responseIdx = 0
}

// CurrentNonce returns the nonce the next submission against the fixture
// attempt must present.
func (s *Server) CurrentNonce() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentNonce
}

// AttemptPassed reports whether the fixture attempt has been closed by a
// PASSED verdict.
func (s *Server) AttemptPassed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attemptPassed
}

// PollCount returns how many device-poll requests were served.
func (s *Server) PollCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.polls
}

// LastDevicePublicKey returns the device public key most recently
// registered at device/start (test evidence for the §5.1 registration
// contract — the key that must sign every later envelope).
func (s *Server) LastDevicePublicKey() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.devicePublicKey
}

// NonceUsed reports whether a nonce has been consumed by an accepted
// submission.
func (s *Server) NonceUsed(nonce string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.usedNonces[nonce]
}

// ServeHTTP routes every GRID_SPEC §5 CLI endpoint.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := normalizePath(r.URL.Path)
	if detail, ok := s.notEnrolledOverride(path); ok {
		writeNotEnrolled(w, detail.courseSlug, detail.enrollURL)
		return
	}
	if code := s.forcedStatus(path); code != 0 {
		if code == http.StatusUnprocessableEntity {
			// §5.4's attestation failures: 422 with a top-level machine
			// code (§5.4b's second code-carrying exception, extended).
			writeSubmissionError(w, code, "invalid_device_signature",
				"The envelope's device signature did not verify.")
			return
		}
		// Error detail belongs in `errors`, not `data` (confirmed envelope,
		// §5.4b) — forced errors carry just the machine-code message.
		writeEnvelope(w, code, messageForStatus(code), nil)
		return
	}
	switch {
	case path == "/cli/device/start" && r.Method == http.MethodPost:
		s.handleDeviceStart(w, r)
	case path == "/cli/device/poll" && r.Method == http.MethodPost:
		s.handleDevicePoll(w, r)
	case strings.HasPrefix(path, "/cli/lessons/") && strings.HasSuffix(path, "/attempt") && r.Method == http.MethodGet:
		s.handleAttempt(w, r)
	case path == "/cli/submissions" && r.Method == http.MethodPost:
		s.handleSubmissions(w, r)
	case path == "/cli/me" && r.Method == http.MethodGet:
		s.requireAuth(w, r, func() { writeEnvelope(w, http.StatusOK, "ok", json.RawMessage(meFixture)) })
	case path == "/cli/session" && r.Method == http.MethodDelete:
		s.requireAuth(w, r, func() { writeEnvelope(w, http.StatusOK, "ok", json.RawMessage(sessionRevokedFixture)) })
	case path == "/cli/courses" && r.Method == http.MethodGet:
		s.requireAuth(w, r, func() { writeEnvelope(w, http.StatusOK, "ok", json.RawMessage(coursesFixture)) })
	default:
		writeEnvelope(w, http.StatusNotFound, "not_found", nil)
	}
}

func (s *Server) handleDeviceStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Hostname        string `json:"hostname"`
		OS              string `json:"os"`
		Arch            string `json:"arch"`
		CliVersion      string `json:"cliVersion"`
		DevicePublicKey string `json:"devicePublicKey"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeEnvelope(w, http.StatusBadRequest, "malformed_request", nil)
		return
	}
	// GRID_SPEC §5.1: devicePublicKey is required — base64 of the raw
	// 32-byte Ed25519 public key that must sign every submission envelope.
	if req.DevicePublicKey == "" {
		writeEnvelope(w, http.StatusBadRequest, "device_public_key_required", nil)
		return
	}
	s.mu.Lock()
	s.devicePublicKey = req.DevicePublicKey
	interval := s.pollInterval
	s.mu.Unlock()
	data := fmt.Sprintf(`{"deviceCode": %q, "userCode": %q, "verificationUrl": %q, "expiresIn": 600, "interval": %d}`,
		DeviceCode, UserCode, VerificationURL, interval)
	writeEnvelope(w, http.StatusOK, "ok", json.RawMessage(data))
}

func (s *Server) handleDevicePoll(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceCode string `json:"deviceCode"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeEnvelope(w, http.StatusBadRequest, "malformed_request", nil)
		return
	}
	if req.DeviceCode != DeviceCode {
		writeEnvelope(w, http.StatusBadRequest, "unknown_device_code", nil)
		return
	}
	s.mu.Lock()
	s.polls++
	state := "success"
	if s.pollIdx < len(s.pollStates) {
		state = s.pollStates[s.pollIdx]
	} else if len(s.pollStates) > 0 {
		state = s.pollStates[len(s.pollStates)-1]
	}
	s.pollIdx++
	// The session key is returned EXACTLY ONCE: a second poll after
	// collection is 410 expired, matching the real backend.
	alreadyCollected := s.keyCollected
	if state == "success" && !alreadyCollected {
		s.keyCollected = true
	}
	s.mu.Unlock()

	switch {
	case state == "success" && alreadyCollected:
		writePollError(w, http.StatusGone, "expired")
	case state == "pending":
		writePollError(w, http.StatusPreconditionRequired, "pending")
	case state == "slow_down":
		writePollError(w, http.StatusTooManyRequests, "slow_down")
	case state == "expired":
		writePollError(w, http.StatusGone, "expired")
	case state == "denied":
		writePollError(w, http.StatusForbidden, "denied")
	default: // success, first (and only) collection
		writeEnvelope(w, http.StatusOK, "ok", json.RawMessage(pollSuccessFixture))
	}
}

func (s *Server) handleAttempt(w http.ResponseWriter, r *http.Request) {
	s.requireAuth(w, r, func() {
		writeEnvelope(w, http.StatusOK, "ok", json.RawMessage(s.attemptResponse()))
	})
}

// attemptResponse returns the attempt fixture, patching in
// workspaceDirNameOverride/workspaceModeOverride when a test has set them.
func (s *Server) attemptResponse() string {
	s.mu.Lock()
	dirOverride := s.workspaceDirNameOverride
	modeOverride := s.workspaceModeOverride
	s.mu.Unlock()
	if dirOverride == "" && modeOverride == "" {
		return attemptResponseFixture
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal([]byte(attemptResponseFixture), &data); err != nil {
		return attemptResponseFixture
	}
	var ws map[string]json.RawMessage
	if err := json.Unmarshal(data["workspace"], &ws); err != nil {
		return attemptResponseFixture
	}
	if dirOverride != "" {
		dirNameJSON, err := json.Marshal(dirOverride)
		if err != nil {
			return attemptResponseFixture
		}
		ws["dirName"] = dirNameJSON
	}
	if modeOverride != "" {
		modeJSON, err := json.Marshal(modeOverride)
		if err != nil {
			return attemptResponseFixture
		}
		ws["mode"] = modeJSON
	}
	wsJSON, err := json.Marshal(ws)
	if err != nil {
		return attemptResponseFixture
	}
	data["workspace"] = wsJSON
	patched, err := json.Marshal(data)
	if err != nil {
		return attemptResponseFixture
	}
	return string(patched)
}

// submittedResult is the one field of results[] the stub actually inspects:
// enough to detect a setup failure (GRID_SPEC §5.3 addendum) without
// decoding the full evidence payload.
type submittedResult struct {
	CheckID string `json:"checkId"`
	Status  string `json:"status"`
}

func (s *Server) handleSubmissions(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeEnvelope(w, http.StatusInternalServerError, "unreadable_body", nil)
		return
	}
	// GRID_SPEC §5.4: the server recomputes HMAC-SHA256(sessionKey, raw body)
	// and rejects mismatches with 401.
	if r.Header.Get("Authorization") != "Bearer "+SessionKey {
		writeEnvelope(w, http.StatusUnauthorized, "unauthorized", nil)
		return
	}
	if !signatureValid(r.Header.Get("X-Grid-Signature"), body) {
		writeEnvelope(w, http.StatusUnauthorized, "invalid_signature", nil)
		return
	}
	var sub struct {
		AttemptID       string            `json:"attemptId"`
		AttemptNonce    string            `json:"attemptNonce"`
		DeviceSignature string            `json:"deviceSignature"`
		Results         []submittedResult `json:"results"`
	}
	// Cluster L seam #4: a body that is not an AttestationEnvelope at all —
	// the legacy evidence body, or one missing required envelope fields —
	// is 400 invalid_envelope (GRID_SPEC §5.3), never judged.
	if err := json.Unmarshal(body, &sub); err != nil || sub.AttemptNonce == "" || sub.DeviceSignature == "" {
		writeSubmissionError(w, http.StatusBadRequest, "invalid_envelope",
			"The submission body must be a device-signed AttestationEnvelope (GRID_SPEC §5.3).")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// GRID_SPEC §5.4: the attempt survives a non-passing submission but
	// closes on PASSED — every later submission against it is rejected
	// regardless of nonce.
	if s.attemptPassed {
		writeSubmissionError(w, http.StatusConflict, "attempt_already_passed",
			"This attempt already passed. Run `grid run` again to redo it.")
		return
	}
	// The nonce is single-use and rotates per submission (nextNonce); a
	// stale nonce is a genuine replay.
	if sub.AttemptNonce != s.currentNonce {
		writeSubmissionError(w, http.StatusConflict, "nonce_already_used",
			"That nonce has already been used.")
		return
	}

	fixture := s.nextFixture(sub.Results)
	var verdict struct {
		Verdict string `json:"verdict"`
	}
	_ = json.Unmarshal([]byte(fixture), &verdict)

	s.usedNonces[sub.AttemptNonce] = true
	s.submissions++
	if verdict.Verdict == "PASSED" {
		s.attemptPassed = true
		writeEnvelope(w, http.StatusOK, "ok", json.RawMessage(withNextNonce(fixture, nil)))
		return
	}
	s.nonceSeq++
	next := fmt.Sprintf("%s-r%d", AttemptNonce, s.nonceSeq)
	s.currentNonce = next
	writeEnvelope(w, http.StatusOK, "ok", json.RawMessage(withNextNonce(fixture, &next)))
}

// nextFixture picks the canned response body for an accepted submission.
// Caller holds s.mu.
//
// An automatic INVALID (GRID_SPEC §5.3 addendum) wins over the configured
// queue: an empty results[] ("no check results were submitted"), or any
// __setup__#<index> entry whose status isn't "ok" (a setup step the CLI
// itself could not execute), means the environment was never ready and
// nothing was meaningfully judged — the one piece of server-side judging
// the stub actually implements, because it is a structural rule about what
// was submitted, not a content-specific verdict. Otherwise it serves the
// next entry of the configured response queue (SetSubmissionResponses),
// repeating the last entry once exhausted, defaulting to
// PassedSubmissionFixture when no queue was configured.
func (s *Server) nextFixture(results []submittedResult) string {
	if len(results) == 0 {
		return InvalidSubmissionFixture
	}
	for _, res := range results {
		if strings.HasPrefix(res.CheckID, setupResultPrefix) && res.Status != "ok" {
			return InvalidSubmissionFixture
		}
	}
	if len(s.responses) == 0 {
		return PassedSubmissionFixture
	}
	idx := s.responseIdx
	if idx >= len(s.responses) {
		idx = len(s.responses) - 1
	}
	s.responseIdx++
	return s.responses[idx]
}

// withNextNonce patches a canned submission fixture's nextNonce field — the
// server-assigned nonce for the next retry, or null once the verdict closes
// the attempt (GRID_SPEC §5.4) — the same decode/mutate/re-encode pattern
// attemptResponse uses to patch workspace.dirName.
func withNextNonce(fixture string, nextNonce *string) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(fixture), &m); err != nil {
		return fixture
	}
	if nextNonce == nil {
		m["nextNonce"] = json.RawMessage("null")
	} else {
		b, err := json.Marshal(*nextNonce)
		if err != nil {
			return fixture
		}
		m["nextNonce"] = b
	}
	out, err := json.Marshal(m)
	if err != nil {
		return fixture
	}
	return string(out)
}

// requireAuth guards an authenticated endpoint (§5.1: all authenticated calls
// send Bearer <sessionKey>). A missing or wrong session key is a 401.
func (s *Server) requireAuth(w http.ResponseWriter, r *http.Request, ok func()) {
	if r.Header.Get("Authorization") != "Bearer "+SessionKey {
		writeEnvelope(w, http.StatusUnauthorized, "unauthorized", nil)
		return
	}
	ok()
}

// signatureValid recomputes the §5.4 signature over the exact received bytes
// and compares in constant time.
func signatureValid(header string, body []byte) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	got, err := hex.DecodeString(header[len(prefix):])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(SessionKey))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// --- helpers ---

func normalizePath(p string) string {
	p = strings.TrimPrefix(p, "/api/v1")
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

func (s *Server) forcedStatus(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.forced[normalizePath(path)]
}

func (s *Server) notEnrolledOverride(path string) (notEnrolledDetail, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.notEnrolled[path]
	return d, ok
}

// writeNotEnrolled writes the real backend's not_enrolled body (confirmed
// 2026-11-13 against a live seeded backend): a plain ApiError, no
// top-level `code` — the machine code and courseSlug/enrollUrl live in
// errors[0].
func writeNotEnrolled(w http.ResponseWriter, courseSlug, enrollURL string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(struct {
		Success    bool                    `json:"success"`
		StatusCode int                     `json:"statusCode"`
		Message    string                  `json:"message"`
		Errors     []notEnrolledErrorEntry `json:"errors"`
	}{
		Success:    false,
		StatusCode: http.StatusForbidden,
		Message:    "Enroll in this course to open its lessons.",
		Errors: []notEnrolledErrorEntry{
			{Code: "not_enrolled", CourseSlug: courseSlug, EnrollURL: enrollURL},
		},
	})
}

// notEnrolledErrorEntry is the wire shape of writeNotEnrolled's single
// errors[] entry.
type notEnrolledErrorEntry struct {
	Code       string `json:"code"`
	CourseSlug string `json:"courseSlug"`
	EnrollURL  string `json:"enrollUrl"`
}

func decodeBody(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}

// messageForStatus maps a status to the machine code the envelope carries.
func messageForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "bad_request"
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "denied"
	case http.StatusConflict:
		return "nonce_already_used"
	case http.StatusGone:
		return "attempt_expired"
	case http.StatusPreconditionRequired:
		return "pending"
	case http.StatusTooManyRequests:
		return "slow_down"
	case http.StatusRequestEntityTooLarge:
		return "payload_too_large"
	case http.StatusNotFound:
		return "not_found"
	default:
		if status >= 500 {
			return "server_error"
		}
		return fmt.Sprintf("http_%d", status)
	}
}

// pollMessages are the human messages the real backend sends for each
// device-poll state (§5.4b: `message` is human text; the machine state is the
// top-level `status` sibling).
var pollMessages = map[string]string{
	"pending":   "Authorization pending",
	"slow_down": "Polling too fast — wait for the advertised interval",
	"expired":   "Device code expired",
	"denied":    "Device pairing denied",
}

// writePollError writes the confirmed device-poll error envelope (GRID_SPEC
// §5.4b): the `status` string ("pending" | "slow_down" | "expired" | "denied")
// is a TOP-LEVEL sibling of `message`, and there is no `data` payload —
//
//	{"success": false, "statusCode": 428, "message": "Authorization pending",
//	 "status": "pending"}
func writePollError(w http.ResponseWriter, status int, state string) {
	w.Header().Set("Content-Type", "application/json")
	if status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", "1")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Success    bool   `json:"success"`
		StatusCode int    `json:"statusCode"`
		Message    string `json:"message"`
		Status     string `json:"status"`
	}{false, status, pollMessages[state], state})
}

// writeSubmissionError writes the §5.4b second code-carrying exception: a
// top-level `code` sibling of `message`, no `data` — the shape of the named
// submission outcomes (attempt_expired, nonce_already_used,
// attempt_already_passed) and the §5.4 attestation failures
// (invalid_envelope, invalid_device_signature, …).
func writeSubmissionError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Success    bool   `json:"success"`
		StatusCode int    `json:"statusCode"`
		Message    string `json:"message"`
		Code       string `json:"code"`
	}{false, status, message, code})
}

// writeEnvelope writes the canonical §6 envelope with the given status.
func writeEnvelope(w http.ResponseWriter, status int, message string, data json.RawMessage) {
	w.Header().Set("Content-Type", "application/json")
	if status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", "1")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Success    bool            `json:"success"`
		StatusCode int             `json:"statusCode"`
		Message    string          `json:"message"`
		Data       json.RawMessage `json:"data"`
	}{status < 400, status, message, data})
}
