// Package api implements the typed HTTP client for the GRID_SPEC §5
// CLI ↔ backend protocol: request/response types, the response envelope,
// retries with HMAC request signing, and typed errors.
//
// This file owns the wire types. They mirror GRID_SPEC §5 field-for-field;
// the in-process stub (internal/api/stub) serves fixtures in the same shapes,
// and internal/api tests prove the client decodes them.
package api

import (
	"encoding/json"
	"time"
)

// CheckStatus is the measurement status the CLI reports for a check
// (GRID_SPEC §5.3). The CLI measures, never judges: these values are
// exactly "ok" | "error" | "skipped" — never "pass"/"fail"; verdicts belong
// to the server.
type CheckStatus string

const (
	// CheckStatusOK means the measurement was taken successfully.
	CheckStatusOK CheckStatus = "ok"
	// CheckStatusError means the measurement could not be taken.
	CheckStatusError CheckStatus = "error"
	// CheckStatusSkipped means a depends_on check did not produce ok.
	CheckStatusSkipped CheckStatus = "skipped"
)

// Verdict is the server's judgement on a submission (GRID_SPEC §5.3).
// Only the server produces these values; the CLI only renders them.
type Verdict string

const (
	VerdictPassed  Verdict = "PASSED"
	VerdictFailed  Verdict = "FAILED"
	VerdictInvalid Verdict = "INVALID"
)

// Envelope is the response wrapper every API response uses
// ({success, statusCode, message, data} — GRID_SPEC §6 / TASK.md §6).
// The stub serves §5 payloads wrapped in this envelope and the client
// unwraps data; the client also tolerates raw §5 bodies (see Assumptions in
// .context/plan.md).
//
// Status is the first confirmed exception to the envelope (GRID_SPEC §5.4b):
// on POST /cli/device/poll errors the poll state — "pending" | "slow_down" |
// "expired" | "denied" — is a TOP-LEVEL sibling of message, not nested under
// data. The client reads it as the machine code for the typed error.
//
// Code is the second confirmed exception (GRID_SYNC_CLAUDE.md 2026-11-12
// 06:00): on POST /cli/submissions errors it disambiguates the three named
// outcomes that can share an HTTP status — "attempt_expired" (410),
// "nonce_already_used" | "attempt_already_passed" (both 409) — as a
// TOP-LEVEL sibling of message. Every other submission error (and every
// other endpoint entirely) carries no Code.
//
// Errors is the generic ApiError detail array (confirmed 2026-11-13 against
// a live seeded backend, GRID_SPEC §5.2/§5.3/§6 "not_enrolled"): a plain
// ApiError — anything that isn't one of the Status/Code-carrying exceptions
// above — has no top-level `code` at all; its machine code and any extra
// fields (courseSlug, enrollUrl) live in errors[0] instead.
type Envelope struct {
	Success    bool            `json:"success"`
	StatusCode int             `json:"statusCode"`
	Message    string          `json:"message"`
	Status     string          `json:"status,omitempty"`
	Code       string          `json:"code,omitempty"`
	Errors     []ErrorDetail   `json:"errors,omitempty"`
	Data       json.RawMessage `json:"data"`
}

// ErrorDetail is one entry of a plain ApiError's `errors` array
// (ApiErrorDetail in grid-backend/src/types/response.types.ts). Most errors
// carry none of these; not_enrolled is the first confirmed user of Code/
// CourseSlug/EnrollURL together.
type ErrorDetail struct {
	Field      string `json:"field,omitempty"`
	Message    string `json:"message,omitempty"`
	Code       string `json:"code,omitempty"`
	CourseSlug string `json:"courseSlug,omitempty"`
	EnrollURL  string `json:"enrollUrl,omitempty"`
}

// User is the paired account record inside the device-poll success payload
// {sessionKey, user} (GRID_SPEC §5.1). GET /cli/me is flat and does not
// nest this object — see MeResponse.
type User struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	AvatarURL   string `json:"avatarUrl"`
}

// DeviceStartRequest is the body of POST /cli/device/start (GRID_SPEC §5.1).
type DeviceStartRequest struct {
	Hostname   string `json:"hostname"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	CliVersion string `json:"cliVersion"`
	// DevicePublicKey is required (GRID_SPEC §5.1/A2): base64 of the raw
	// 32-byte Ed25519 device public key. The private half never leaves the
	// CLI's keychain/config dir; this half is stored on CliSession and
	// verifies every submission envelope's deviceSignature (§5.3).
	DevicePublicKey string `json:"devicePublicKey"`
}

// DeviceStartResponse is the 200 body of POST /cli/device/start.
type DeviceStartResponse struct {
	DeviceCode       string `json:"deviceCode"`
	UserCode         string `json:"userCode"`
	VerificationURL  string `json:"verificationUrl"`
	ExpiresInSeconds int    `json:"expiresIn"`
	IntervalSeconds  int    `json:"interval"`
}

// DevicePollRequest is the body of POST /cli/device/poll.
type DevicePollRequest struct {
	DeviceCode string `json:"deviceCode"`
}

// DevicePollResponse is the union of poll outcomes: the terminal states carry
// only Status ("pending", "slow_down", "expired", "denied"), while success
// carries the session key and the paired user. Terminal states surface as
// typed errors from Client.DevicePoll; this struct is only returned on
// success, where SessionKey and User are always set.
type DevicePollResponse struct {
	Status     string `json:"status,omitempty"`
	SessionKey string `json:"sessionKey,omitempty"`
	User       *User  `json:"user,omitempty"`
}

// Attempt is the single-use attempt issued by GET /cli/lessons/:id/attempt
// (GRID_SPEC §5.2). Nonce is single-use, but the attempt survives a
// non-passing submission: the nonce rotates per submission (SubmissionResponse
// .NextNonce) rather than closing the attempt. Replaying a spent nonce is a
// 409 nonce_already_used; resubmitting a closed (already-passed) attempt is a
// 409 attempt_already_passed (§5.4).
type Attempt struct {
	ID        string    `json:"id"`
	Nonce     string    `json:"nonce"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// Lesson identifies the lesson an attempt belongs to.
type Lesson struct {
	ID               string `json:"id"`
	Title            string `json:"title"`
	CourseSlug       string `json:"courseSlug"`
	LessonSlug       string `json:"lessonSlug"`
	Difficulty       string `json:"difficulty"`
	EstimatedMinutes int    `json:"estimatedMinutes"`
}

// WorkspaceFile is a starter file to scaffold into the workspace.
type WorkspaceFile struct {
	Path     string `json:"path"`
	Contents string `json:"contents"`
}

// WorkspaceBundle is a presigned, short-lived pointer to a seed-generated
// starter archive (GRID_SPEC §4/§5.2 "Bundle resolution"). SHA256 is the
// integrity authority the CLI verifies against — never Content-Length or
// the filename; Bytes is advisory only. `null` until P20 wires up bundle
// generation — a Workspace with Mode "bundle" and a nil Bundle means the
// CLI silently degrades to Mode "repo" behaviour.
type WorkspaceBundle struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// Workspace describes how to scaffold the lesson workspace. Mode is
// environment only, never an assertion (GRID_SPEC §4 "Workspace modes") —
// parse it with workspace.ParseMode, never compare the raw string.
type Workspace struct {
	Mode           string           `json:"mode"`
	DirName        string           `json:"dirName"`
	StarterRepoURL string           `json:"starterRepoUrl,omitempty"`
	StarterSubdir  string           `json:"starterSubdir,omitempty"`
	Files          []WorkspaceFile  `json:"files,omitempty"`
	Bundle         *WorkspaceBundle `json:"bundle,omitempty"`
}

// LessonURLs carries the human-facing URLs for the lesson.
type LessonURLs struct {
	LessonURL string `json:"lessonUrl"`
}

// AttemptResponse is the 200 body of GET /cli/lessons/:lessonId/attempt.
//
// CheckSpec is the *public* check-spec view (GRID_SPEC §5.2): ids, titles,
// hints, types, and the fields needed to perform a measurement — expected
// values (pattern, equals, sha256, value, must_include) are omitted by the
// server. It is kept raw here because full spec decoding belongs to
// internal/spec (pass 4); this package only transports it.
type AttemptResponse struct {
	Attempt   Attempt         `json:"attempt"`
	Lesson    Lesson          `json:"lesson"`
	Workspace Workspace       `json:"workspace"`
	CheckSpec json.RawMessage `json:"checkSpec"`
	// Extensions carries the attempt payload's `extensions: [{ name, version }]`
	// pins (GRID_SPEC §5.2/A3): the signed extension binaries the CLI must
	// ensure cached before measuring this spec's ext.* checks. Optional — a
	// spec with no ext.* checks sends none.
	Extensions []ExtPin   `json:"extensions,omitempty"`
	URLs       LessonURLs `json:"urls"`
}

// ExtPin is one extension pin (§5.2/A3): the extension's directory name and
// its exact cached version.
type ExtPin struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// The POST /cli/submissions body is the dual-signed AttestationEnvelope
// (GRID_SPEC §5.3/A4) — grid-extensions/pkg/attest.AttestationEnvelope is
// the canonical Go shape for every consumer; the legacy evidence body is
// gone, rejected by the server as 400 invalid_envelope. Each result's
// evidence shape is the §4 payload; `__setup__#N` synthetic ids ride
// `results[]` unchanged (§5.3).

// SubmissionCheck is the server's judgement for one check. Passed is the
// server's field name (it judges, the CLI does not — GRID_SPEC §1).
type SubmissionCheck struct {
	CheckID string `json:"checkId"`
	Title   string `json:"title"`
	Passed  bool   `json:"passed"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

// XP is the experience summary in a submission response.
type XP struct {
	Delta         int  `json:"delta"`
	Total         int  `json:"total"`
	PenaltyCapped bool `json:"penaltyCapped,omitempty"`
}

// Stats is the post-submission account snapshot.
type Stats struct {
	Rank             string `json:"rank"`
	Level            int    `json:"level"`
	CurrentStreak    int    `json:"currentStreak"`
	LessonsCompleted int    `json:"lessonsCompleted"`
}

// Badge is one newly-unlocked badge (GRID_SPEC §5.3's unlocked.badges[]
// entry). Confirmed against the live backend on 2026-11-12: an object, not
// a bare slug string — grid-backend/src/modules/submissions/
// submissions.types.ts::BadgeUnlockDto. GRID_SPEC's payload skeleton elides
// the entry shape ("badges: [...]"), which is easy to misread as a string
// slice; it is not.
type Badge struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Tier        string `json:"tier"`
	IconKey     string `json:"iconKey"`
	Description string `json:"description"`
}

// Achievement is one newly-unlocked (or newly-progressed) achievement
// (AchievementUnlockDto, same confirmation as Badge). Completed
// distinguishes "this just finished" from "progress moved but it isn't
// done yet" — both ride the same unlocked.achievements[] array.
type Achievement struct {
	Slug      string `json:"slug"`
	Progress  int    `json:"progress"`
	Target    int    `json:"target"`
	Completed bool   `json:"completed"`
}

// Unlocked lists badges/achievements newly awarded by this submission
// (not a running total — GRID_SYNC_CLAUDE.md 2026-11-12 06:00).
type Unlocked struct {
	Badges       []Badge       `json:"badges"`
	Achievements []Achievement `json:"achievements"`
}

// NextLesson is the follow-up lesson the server suggests, if any.
type NextLesson struct {
	LessonID string `json:"lessonId"`
	Title    string `json:"title"`
}

// SubmissionResponse is the 200 body of POST /cli/submissions: the server's
// verdict. Everything in here is the server's judgement — the CLI renders it,
// it never derives it locally.
//
// NextNonce is GRID_SPEC §5.4's rotating-nonce contract (GRID_SYNC_CLAUDE.md
// 2026-11-12 01:05): an attempt survives a non-passing submission, but its
// nonce is single-use, so the server hands back the value the CLI must use
// for the next retry. It is nil once the verdict is PASSED — the attempt is
// closed and a further submission against it is 409 attempt_already_passed.
type SubmissionResponse struct {
	SubmissionID string            `json:"submissionId"`
	Verdict      Verdict           `json:"verdict"`
	Checks       []SubmissionCheck `json:"checks"`
	XP           XP                `json:"xp"`
	Stats        Stats             `json:"stats"`
	Unlocked     Unlocked          `json:"unlocked"`
	Next         *NextLesson       `json:"next"`
	NextNonce    *string           `json:"nextNonce"`
}

// MeResponse is the 200 body of GET /cli/me (GRID_SPEC §5.4b, confirmed by
// the backend: GRID_SYNC_CLAUDE.md 05:40 ANSWER + the 08:20 live transcript).
// It is flat — no nested user object — and carries currentStreak/longestStreak
// (not "streak"), plus level alongside rank (level ticks every 100 XP; rank
// is an identity tier — GRID_SPEC §3).
type MeResponse struct {
	ID            string `json:"id"`
	Username      string `json:"username"`
	DisplayName   string `json:"displayName"`
	AvatarURL     string `json:"avatarUrl"`
	Role          string `json:"role"`
	XP            int    `json:"xp"`
	Level         int    `json:"level"`
	Rank          string `json:"rank"`
	CurrentStreak int    `json:"currentStreak"`
	LongestStreak int    `json:"longestStreak"`
}

// Course is one enrolled course in GET /cli/courses (GRID_SPEC §5.4b,
// confirmed by the 05:40 ANSWER). Progress is enrollment.progressPct plus
// lessonCount — there is no lessonsCompleted field.
type Course struct {
	ID               string     `json:"id"`
	Slug             string     `json:"slug"`
	Title            string     `json:"title"`
	Difficulty       string     `json:"difficulty"`
	Language         string     `json:"language"`
	EstimatedMinutes int        `json:"estimatedMinutes"`
	IconKey          string     `json:"iconKey"`
	AccentColor      string     `json:"accentColor"`
	LessonCount      int        `json:"lessonCount"`
	Enrollment       Enrollment `json:"enrollment"`
}

// Enrollment is the per-course enrollment record nested in a Course.
type Enrollment struct {
	Status       string    `json:"status"`
	ProgressPct  int       `json:"progressPct"`
	LastLessonID string    `json:"lastLessonId"`
	EnrolledAt   time.Time `json:"enrolledAt"`
}

// CoursesResponse is the 200 body of GET /cli/courses.
type CoursesResponse struct {
	Courses []Course `json:"courses"`
}
