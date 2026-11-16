package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anshul-kh/grid-cli/internal/api/stub"
	"github.com/anshul-kh/grid-cli/internal/buildinfo"
	"github.com/anshul-kh/grid-extensions/pkg/attest"
)

// stubSessionKey exposes the stub's fixed test session key to tests in this
// package. It is compared, never printed.
func stubSessionKey() string { return stub.SessionKey }

// newStubTestServer serves a fresh stub over real HTTP and returns the
// server plus a client wired to it with the stub's session key.
func newStubTestServer(t *testing.T, opts ...Option) (*stub.Server, *httptest.Server, *Client) {
	t.Helper()
	st := stub.New()
	srv := httptest.NewServer(st)
	t.Cleanup(srv.Close)
	all := append([]Option{WithSessionKey(stub.SessionKey)}, opts...)
	c, err := New(srv.URL+"/api/v1", all...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return st, srv, c
}

// TestStubDeviceFlowFullSequence drives the complete §5.1 state machine:
// start → poll(pending) → poll(slow_down) → poll(success). device/start
// registers the §5.1 devicePublicKey — a missing one is a 400.
func TestStubDeviceFlowFullSequence(t *testing.T) {
	st, _, c := newStubTestServer(t)
	ctx := context.Background()

	devicePublicKey := attest.PublicKeyB64(attest.DeterministicTestKey().Public().(ed25519.PublicKey))
	start, err := c.DeviceStart(ctx, DeviceStartRequest{
		Hostname:        "test-host",
		OS:              "darwin",
		Arch:            "arm64",
		CliVersion:      buildinfo.Version,
		DevicePublicKey: devicePublicKey,
	})
	if err != nil {
		t.Fatalf("DeviceStart: %v", err)
	}
	if start.DeviceCode == "" || start.UserCode == "" || start.VerificationURL == "" {
		t.Errorf("device-start response incomplete: %+v", start)
	}
	if start.IntervalSeconds <= 0 {
		t.Errorf("interval = %d, want > 0", start.IntervalSeconds)
	}
	if got := st.LastDevicePublicKey(); got != devicePublicKey {
		t.Errorf("device/start registered devicePublicKey = %q, want the request's key", got)
	}

	if _, err := c.DevicePoll(ctx, start.DeviceCode); !errors.Is(err, ErrPending) {
		t.Fatalf("poll 1 error = %v, want ErrPending", err)
	}
	if _, err := c.DevicePoll(ctx, start.DeviceCode); !errors.Is(err, ErrSlowDown) {
		t.Fatalf("poll 2 error = %v, want ErrSlowDown", err)
	}
	resp, err := c.DevicePoll(ctx, start.DeviceCode)
	if err != nil {
		t.Fatalf("poll 3: %v", err)
	}
	if resp.SessionKey != stub.SessionKey {
		t.Error("poll 3 returned an unexpected session key (values never printed)")
	}
	if resp.User == nil || resp.User.Username != "grid-tester" || resp.User.DisplayName != "Grid Tester" {
		t.Errorf("poll 3 user not decoded correctly: %+v", resp.User)
	}
	if st.PollCount() != 3 {
		t.Errorf("PollCount = %d, want 3", st.PollCount())
	}
}

// TestStubDeviceStartRequiresPublicKey pins §5.1/A2: device/start without a
// devicePublicKey is rejected, not silently accepted.
func TestStubDeviceStartRequiresPublicKey(t *testing.T) {
	_, _, c := newStubTestServer(t)
	_, err := c.DeviceStart(context.Background(), DeviceStartRequest{
		Hostname:   "test-host",
		OS:         "darwin",
		Arch:       "arm64",
		CliVersion: buildinfo.Version,
	})
	if !errors.Is(err, ErrUnexpected) {
		t.Fatalf("DeviceStart without devicePublicKey: error = %v, want the 400 to surface as ErrUnexpected", err)
	}
}

// TestStubDeviceFlowTerminalStates covers the non-happy poll outcomes.
func TestStubDeviceFlowTerminalStates(t *testing.T) {
	for _, tc := range []struct {
		states []string
		want   error
	}{
		{[]string{"expired"}, ErrAttemptExpired},
		{[]string{"denied"}, ErrDenied},
		{[]string{"pending", "pending", "expired"}, ErrPending}, // 3rd state not reached
	} {
		st, _, c := newStubTestServer(t)
		st.SetPollStates(tc.states)
		_, err := c.DevicePoll(context.Background(), stub.DeviceCode)
		if !errors.Is(err, tc.want) {
			t.Errorf("states %v: error = %v, want %v", tc.states, err, tc.want)
		}
	}
}

// TestStubDevicePollReturnsSessionKeyExactlyOnce pins the confirmed flow
// (GRID_SYNC_CLAUDE.md 05:25 + 08:20 READY): the 200 success body carrying
// the session key is served EXACTLY ONCE; any later poll is 410 expired
// with no key in the body — persist on first receipt.
func TestStubDevicePollReturnsSessionKeyExactlyOnce(t *testing.T) {
	st, srv, c := newStubTestServer(t)
	st.SetPollStates([]string{"pending", "success"})
	ctx := context.Background()

	if _, err := c.DevicePoll(ctx, stub.DeviceCode); !errors.Is(err, ErrPending) {
		t.Fatalf("poll 1 = %v, want ErrPending", err)
	}
	if _, err := c.DevicePoll(ctx, stub.DeviceCode); err != nil {
		t.Fatalf("poll 2 (first and only success): %v", err)
	}
	// A second poll after collection is 410 expired.
	if _, err := c.DevicePoll(ctx, stub.DeviceCode); !errors.Is(err, ErrAttemptExpired) {
		t.Fatalf("poll 3 after collection = %v, want ErrAttemptExpired", err)
	}

	// Raw-wire check: the 410 body carries the top-level status and no
	// session key anywhere in the response.
	res, err := http.Post(srv.URL+"/api/v1/cli/device/poll", "application/json",
		bytes.NewReader([]byte(`{"deviceCode": "`+stub.DeviceCode+`"}`)))
	if err != nil {
		t.Fatalf("raw poll: %v", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("reading raw poll body: %v", err)
	}
	if res.StatusCode != http.StatusGone {
		t.Errorf("post-collection poll status = %d, want 410", res.StatusCode)
	}
	var env struct {
		Success    bool            `json:"success"`
		StatusCode int             `json:"statusCode"`
		Message    string          `json:"message"`
		Status     string          `json:"status"`
		Data       json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("410 body is not valid JSON: %v\n%s", err, body)
	}
	if env.Status != "expired" {
		t.Errorf("410 body status = %q, want %q (top-level sibling of message)", env.Status, "expired")
	}
	if bytes.Contains(body, []byte(stub.SessionKey)) {
		t.Error("410 body must not contain the session key — it is returned exactly once")
	}
}

// TestStubPollErrorEnvelopeMatchesConfirmedShape pins the confirmed
// device-poll error wire (GRID_SPEC §5.4b, 05:40 ANSWER): `status`
// ("pending"|"slow_down"|"expired"|"denied") is a TOP-LEVEL sibling of the
// human `message`, and there is no `data` payload.
func TestStubPollErrorEnvelopeMatchesConfirmedShape(t *testing.T) {
	st, srv, _ := newStubTestServer(t)
	st.SetPollStates([]string{"pending"})

	res, err := http.Post(srv.URL+"/api/v1/cli/device/poll", "application/json",
		bytes.NewReader([]byte(`{"deviceCode": "`+stub.DeviceCode+`"}`)))
	if err != nil {
		t.Fatalf("raw poll: %v", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("reading raw poll body: %v", err)
	}
	if res.StatusCode != http.StatusPreconditionRequired {
		t.Errorf("pending poll status = %d, want 428", res.StatusCode)
	}
	var env struct {
		Success    bool            `json:"success"`
		StatusCode int             `json:"statusCode"`
		Message    string          `json:"message"`
		Status     string          `json:"status"`
		Data       json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("428 body is not valid JSON: %v\n%s", err, body)
	}
	if env.Success || env.StatusCode != http.StatusPreconditionRequired {
		t.Errorf("428 envelope = {success:%v, statusCode:%d}, want the error envelope", env.Success, env.StatusCode)
	}
	if env.Status != "pending" {
		t.Errorf("top-level status = %q, want %q", env.Status, "pending")
	}
	if env.Message == "" || env.Message == env.Status {
		t.Errorf("message = %q, want the server's human message (not the machine state)", env.Message)
	}
	if len(env.Data) != 0 && string(env.Data) != "null" {
		t.Errorf("poll error must carry no data payload, got %s", env.Data)
	}
}

// TestStubSubmissionHappyPathAndReplay covers the §5.3/§5.4 submission flow:
// signed post accepted with the attempt fixture's starting nonce, a PASSED
// verdict closes the attempt (nextNonce nil), and replaying the same —now
// stale— nonce afterwards is rejected as attempt_already_passed rather than
// nonce_already_used (GRID_SYNC_CLAUDE.md 2026-11-12 01:05: the attempt is
// closed, not merely replayed).
func TestStubSubmissionHappyPathAndReplay(t *testing.T) {
	st, _, c := newStubTestServer(t)
	ctx := context.Background()

	resp, err := c.Submit(ctx, testEnvelope(stub.AttemptNonce))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if resp.Verdict != VerdictPassed {
		t.Errorf("verdict = %q, want PASSED", resp.Verdict)
	}
	if resp.XP.Delta != 94 || resp.XP.Total != 1284 || resp.XP.PenaltyCapped {
		t.Errorf("xp not decoded correctly: %+v", resp.XP)
	}
	if resp.Stats.Rank != "OPERATOR" || resp.Stats.Level != 3 || resp.Stats.CurrentStreak != 4 || resp.Stats.LessonsCompleted != 12 {
		t.Errorf("stats not decoded correctly: %+v", resp.Stats)
	}
	if len(resp.Checks) != 3 || !resp.Checks[0].Passed {
		t.Errorf("checks not decoded correctly: %+v", resp.Checks)
	}
	if resp.Next == nil || resp.Next.Title != "Command dispatch" {
		t.Errorf("next lesson not decoded correctly: %+v", resp.Next)
	}
	if resp.NextNonce != nil {
		t.Errorf("NextNonce = %q, want nil (PASSED closes the attempt)", *resp.NextNonce)
	}
	if got := st.SubmissionCount(); got != 1 {
		t.Errorf("SubmissionCount = %d, want 1", got)
	}
	if !st.AttemptPassed() {
		t.Error("stub should record the attempt as passed")
	}

	// Resubmitting the same nonce after PASSED ⇒ 409 attempt_already_passed,
	// not nonce_already_used — the attempt is closed, which is a distinct
	// claim from "that nonce was already spent".
	if _, err := c.Submit(ctx, testEnvelope(stub.AttemptNonce)); !errors.Is(err, ErrAttemptAlreadyPassed) {
		t.Fatalf("replay-after-passed error = %v, want ErrAttemptAlreadyPassed", err)
	}
	if got := st.SubmissionCount(); got != 1 {
		t.Errorf("SubmissionCount after replay = %d, want 1", got)
	}
	if !st.NonceUsed(stub.AttemptNonce) {
		t.Error("nonce should be recorded as used")
	}
}

// TestStubSubmissionNonceRotatesOnNonPassingVerdict covers the rotating-nonce
// contract (GRID_SYNC_CLAUDE.md 2026-11-12 01:05): a non-passing verdict
// keeps the attempt open, rotates the nonce, and returns the new value as
// nextNonce; the stale nonce is then rejected as nonce_already_used, and the
// retry with the fresh nonce succeeds without a new `grid run`.
func TestStubSubmissionNonceRotatesOnNonPassingVerdict(t *testing.T) {
	st, _, c := newStubTestServer(t)
	st.SetSubmissionResponses([]string{stub.FailedSubmissionFixture, stub.PassedSubmissionFixture})
	ctx := context.Background()

	resp, err := c.Submit(ctx, testEnvelope(stub.AttemptNonce))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if resp.Verdict != VerdictFailed {
		t.Errorf("verdict = %q, want FAILED", resp.Verdict)
	}
	if resp.NextNonce == nil || *resp.NextNonce == "" || *resp.NextNonce == stub.AttemptNonce {
		t.Fatalf("NextNonce = %v, want a fresh non-empty value", resp.NextNonce)
	}
	fresh := *resp.NextNonce
	if got := st.CurrentNonce(); got != fresh {
		t.Errorf("stub CurrentNonce = %q, want the rotated value %q", got, fresh)
	}

	// The stale (first) nonce is now spent.
	if _, err := c.Submit(ctx, testEnvelope(stub.AttemptNonce)); !errors.Is(err, ErrNonceAlreadyUsed) {
		t.Fatalf("stale-nonce resubmit error = %v, want ErrNonceAlreadyUsed", err)
	}

	// Retrying with the rotated nonce succeeds — no fresh `grid run` needed.
	resp2, err := c.Submit(ctx, testEnvelope(fresh))
	if err != nil {
		t.Fatalf("Submit with rotated nonce: %v", err)
	}
	if resp2.Verdict != VerdictPassed {
		t.Errorf("verdict = %q, want PASSED", resp2.Verdict)
	}
	if resp2.NextNonce != nil {
		t.Errorf("NextNonce = %q, want nil (PASSED closes the attempt)", *resp2.NextNonce)
	}
	if got := st.SubmissionCount(); got != 2 {
		t.Errorf("SubmissionCount = %d, want 2 (the stale-nonce resubmit must not count)", got)
	}
}

// TestStubSubmissionEmptyResultsOrSetupFailureIsInvalid pins the §5.3
// addendum: an empty results[] or a failed __setup__#<index> entry is an
// automatic INVALID verdict, never FAILED — "nothing was measured" is a
// different claim from "we measured, and it was wrong".
func TestStubSubmissionEmptyResultsOrSetupFailureIsInvalid(t *testing.T) {
	st, _, c := newStubTestServer(t)
	ctx := context.Background()

	sub := testEnvelope(stub.AttemptNonce)
	sub.Results = nil
	resp, err := c.Submit(ctx, sub)
	if err != nil {
		t.Fatalf("Submit with empty results: %v", err)
	}
	if resp.Verdict != VerdictInvalid {
		t.Errorf("verdict = %q, want INVALID for an empty results[]", resp.Verdict)
	}
	if resp.NextNonce == nil {
		t.Fatal("NextNonce = nil, want a rotated value (INVALID is non-passing, the attempt stays open)")
	}

	sub2 := testEnvelope(*resp.NextNonce)
	sub2.Results = []attest.Result{
		{CheckID: "__setup__#0", Status: "ok", DurationMs: 100, Evidence: map[string]any{"exitCode": 0}},
		{CheckID: "__setup__#1", Status: "error", DurationMs: 50, Evidence: map[string]any{"exitCode": -1}},
	}
	resp2, err := c.Submit(ctx, sub2)
	if err != nil {
		t.Fatalf("Submit with a failed setup step: %v", err)
	}
	if resp2.Verdict != VerdictInvalid {
		t.Errorf("verdict = %q, want INVALID for a non-ok setup step", resp2.Verdict)
	}
	if st.AttemptPassed() {
		t.Error("an INVALID verdict must never close the attempt")
	}
}

// TestStubForcedStatuses exercises the configurable 401/409/410/413 paths
// plus the §5.4 422 attestation failure (the stub's forced 422 is the
// invalid_device_signature fixture).
func TestStubForcedStatuses(t *testing.T) {
	st, _, c := newStubTestServer(t)
	ctx := context.Background()

	for _, tc := range []struct {
		status int
		want   error
	}{
		{401, ErrUnauthorized},
		{409, ErrNonceAlreadyUsed},
		{410, ErrAttemptExpired},
		{413, ErrTooLarge},
		{422, ErrAttestation},
	} {
		st.ForceStatus("/cli/submissions", tc.status)
		err := func() error {
			_, err := c.Submit(ctx, testEnvelope(stub.AttemptNonce))
			return err
		}()
		if !errors.Is(err, tc.want) {
			t.Errorf("forced %d: error = %v, want %v", tc.status, err, tc.want)
		}
		if tc.status == 422 {
			var se *StatusError
			if !errors.As(err, &se) || se.Code != "invalid_device_signature" {
				t.Errorf("forced 422: StatusError.Code = %v, want invalid_device_signature", err)
			}
		}
	}
	// Forced statuses do not consume the nonce or count as submissions.
	if st.SubmissionCount() != 0 || st.NonceUsed(stub.AttemptNonce) {
		t.Error("forced error statuses must not consume the nonce")
	}
	// Clearing the force restores the happy path with the attempt's starting
	// nonce — forced statuses never touched it.
	st.ForceStatus("/cli/submissions", 0)
	if _, err := c.Submit(ctx, testEnvelope(stub.AttemptNonce)); err != nil {
		t.Errorf("Submit after clearing forced status: %v", err)
	}
}

// TestStubRejectsBadSignature proves the §5.4 server-side check: a client
// signing with the wrong key gets 401.
func TestStubRejectsBadSignature(t *testing.T) {
	_, srv, _ := newStubTestServer(t)
	c, err := New(srv.URL+"/api/v1", WithSessionKey("a-wrong-session-key-00000000000"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Submit(context.Background(), testEnvelope("nonce-sig")); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Submit with wrong key = %v, want ErrUnauthorized", err)
	}
}

// TestStubRequiresAuth proves authenticated endpoints reject requests without
// the session key.
func TestStubRequiresAuth(t *testing.T) {
	_, srv, _ := newStubTestServer(t)
	c, err := New(srv.URL + "/api/v1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if _, err := c.Me(ctx); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("Me without auth = %v, want ErrUnauthorized", err)
	}
	if _, err := c.Courses(ctx); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("Courses without auth = %v, want ErrUnauthorized", err)
	}
	if _, err := c.Attempt(ctx, stub.LessonID); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("Attempt without auth = %v, want ErrUnauthorized", err)
	}
	if err := c.Logout(ctx); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("Logout without auth = %v, want ErrUnauthorized", err)
	}
}

// TestStubAllEndpoints drives every §5 endpoint end-to-end through the typed
// client, asserting the fixtures decode into the §5 structs.
func TestStubAllEndpoints(t *testing.T) {
	_, _, c := newStubTestServer(t)
	ctx := context.Background()

	me, err := c.Me(ctx)
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	// The confirmed flat /cli/me shape (GRID_SPEC §5.4b, 05:40 ANSWER): no
	// nested user object, currentStreak/longestStreak, role and level.
	if me.Username != "grid-tester" || me.DisplayName != "Grid Tester" || me.ID != stub.TestUserID {
		t.Errorf("me identity not decoded correctly: %+v", me)
	}
	if me.Role != "USER" || me.Rank != "OPERATOR" || me.XP != 1284 || me.Level != 13 {
		t.Errorf("me rank/xp/level not decoded correctly: %+v", me)
	}
	if me.CurrentStreak != 4 || me.LongestStreak != 11 {
		t.Errorf("me streaks not decoded correctly: %+v", me)
	}

	courses, err := c.Courses(ctx)
	if err != nil {
		t.Fatalf("Courses: %v", err)
	}
	if len(courses.Courses) != 1 {
		t.Fatalf("courses = %+v, want 1 course", courses.Courses)
	}
	// The confirmed course entry shape (05:40 ANSWER): progress is
	// enrollment.progressPct plus lessonCount — no lessonsCompleted.
	got := courses.Courses[0]
	if got.Slug != stub.CourseSlug || got.Title != "Build a Redis core in Go" ||
		got.Difficulty != "INTERMEDIATE" || got.Language != "go" ||
		got.EstimatedMinutes != 60 || got.LessonCount != 8 {
		t.Errorf("course fields not decoded correctly: %+v", got)
	}
	if got.Enrollment.Status != "ACTIVE" || got.Enrollment.ProgressPct != 25 ||
		got.Enrollment.LastLessonID != stub.LessonID || got.Enrollment.EnrolledAt.IsZero() {
		t.Errorf("enrollment not decoded correctly: %+v", got.Enrollment)
	}

	att, err := c.Attempt(ctx, stub.LessonID)
	if err != nil {
		t.Fatalf("Attempt: %v", err)
	}
	if att.Attempt.ID != stub.AttemptID || att.Attempt.Nonce != stub.AttemptNonce || att.Attempt.ExpiresAt.IsZero() {
		t.Errorf("attempt fixture not decoded correctly: %+v", att.Attempt)
	}
	if att.Lesson.CourseSlug != stub.CourseSlug || att.Lesson.LessonSlug != stub.LessonSlug || att.Lesson.Title != "Speak RESP" {
		t.Errorf("lesson fixture not decoded correctly: %+v", att.Lesson)
	}
	if want := "grid-" + stub.CourseSlug + "-" + stub.LessonSlug; att.Workspace.DirName != want {
		t.Errorf("dirName = %q, want %q", att.Workspace.DirName, want)
	}
	if len(att.Workspace.Files) != 2 || att.Workspace.Files[0].Path != "README.md" {
		t.Errorf("workspace files not decoded correctly: %+v", att.Workspace.Files)
	}
	if len(att.CheckSpec) == 0 {
		t.Error("public checkSpec payload missing from attempt response")
	}
	if att.URLs.LessonURL == "" {
		t.Error("lessonUrl missing from attempt response")
	}

	if err := c.Logout(ctx); err != nil {
		t.Errorf("Logout: %v", err)
	}
}
