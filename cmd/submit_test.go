package cmd

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anshul-kh/grid-cli/internal/api"
	"github.com/anshul-kh/grid-cli/internal/api/stub"
	"github.com/anshul-kh/grid-cli/internal/auth"
	"github.com/anshul-kh/grid-cli/internal/buildinfo"
	"github.com/anshul-kh/grid-cli/internal/runner"
	"github.com/anshul-kh/grid-cli/internal/workspace"
	"github.com/anshul-kh/grid-extensions/pkg/attest"
)

// loginAsStubUserForSubmit wires a session the stub accepts, isolated under
// a sandboxed home directory.
func loginAsStubUserForSubmit(t *testing.T) {
	t.Helper()
	credsPath := isolateHome(t)
	if err := auth.NewStore(credsPath).Save(auth.Credentials{
		SessionKey: stub.SessionKey,
		User:       stubTestUser(),
	}); err != nil {
		t.Fatalf("Save credentials: %v", err)
	}
}

// TestSubmitPassedExitsZero drives the default stub happy path (PASSED)
// through the real grid submit command.
func TestSubmitPassedExitsZero(t *testing.T) {
	loginAsStubUserForSubmit(t)
	_, srv := newStubCmdServer(t)
	setCmdCfg(srv.URL+"/api/v1", false)
	dir := t.TempDir()
	cfg.Dir = dir
	writeTestAttempt(t, dir, stub.AttemptNonce, simpleCheckSpecFixture)

	stdout, stderr, err := captureOutput(t, func() error {
		return runSubmit(submitCmd, nil)
	})
	if err != nil {
		t.Fatalf("runSubmit: %v", err)
	}
	if !strings.Contains(stdout, "PASSED") {
		t.Errorf("stdout missing the PASSED verdict:\n%s", stdout)
	}
	if !strings.Contains(stdout, "+94 XP") || !strings.Contains(stdout, "1,284 total") {
		t.Errorf("stdout missing the XP line:\n%s", stdout)
	}
	if !strings.Contains(stdout, "grid run 4b6c7d8e-9f0a-4b1c-2d3e-4f5a6b7c8d9e") {
		t.Errorf("stdout missing the next-lesson command:\n%s", stdout)
	}
	// Progress goes to stderr only; stdout stays the rendered result.
	if strings.Contains(stdout, "Running checks") || strings.Contains(stdout, "Submitting") {
		t.Errorf("progress output leaked into stdout:\n%s", stdout)
	}
	if !strings.Contains(stderr, "Running checks") || !strings.Contains(stderr, "Submitting") {
		t.Errorf("stderr missing progress output:\n%s", stderr)
	}
}

// TestSubmitFailedExitsOne drives a FAILED verdict and proves the failure
// detail (message + hint) is rendered, and the check table marks it ✖.
func TestSubmitFailedExitsOne(t *testing.T) {
	loginAsStubUserForSubmit(t)
	st, srv := newStubCmdServer(t)
	st.SetSubmissionResponses([]string{stub.FailedSubmissionFixture})
	setCmdCfg(srv.URL+"/api/v1", false)
	dir := t.TempDir()
	cfg.Dir = dir
	writeTestAttempt(t, dir, stub.AttemptNonce, simpleCheckSpecFixture)

	stdout, _, err := captureOutput(t, func() error {
		return runSubmit(submitCmd, nil)
	})
	if code := exitCodeOf(t, err); code != 1 {
		t.Fatalf("submit with a FAILED verdict: exit code = %d (err %v), want 1", code, err)
	}
	if !strings.Contains(stdout, "FAILED") {
		t.Errorf("stdout missing the FAILED verdict:\n%s", stdout)
	}
	if !strings.Contains(stdout, "✖") {
		t.Errorf("stdout missing the ✖ icon for the failed check:\n%s", stdout)
	}
	if !strings.Contains(stdout, "Expected +PONG") {
		t.Errorf("stdout missing the server's failure message:\n%s", stdout)
	}
	if !strings.Contains(stdout, "hint:") || !strings.Contains(stdout, "Wire PING into your command loop.") {
		t.Errorf("stdout missing the server's hint:\n%s", stdout)
	}
}

// TestSubmitSetupFailureExitsTwoInvalid proves a local setup failure still
// goes all the way to the server (never a local short-circuit): the stub's
// automatic INVALID rule fires, and every check renders "–" (not
// evaluated), never ✖ — "nothing was measured" is a different claim from
// "we measured, and it was wrong".
func TestSubmitSetupFailureExitsTwoInvalid(t *testing.T) {
	loginAsStubUserForSubmit(t)
	_, srv := newStubCmdServer(t)
	setCmdCfg(srv.URL+"/api/v1", false)
	dir := t.TempDir()
	cfg.Dir = dir
	writeTestAttempt(t, dir, stub.AttemptNonce, simpleCheckSpecWithFailingSetupFixture)

	stdout, stderr, err := captureOutput(t, func() error {
		return runSubmit(submitCmd, nil)
	})
	if code := exitCodeOf(t, err); code != 2 {
		t.Fatalf("submit with a setup failure: exit code = %d (err %v), want 2", code, err)
	}
	if !strings.Contains(stdout, "INVALID") {
		t.Errorf("stdout missing the INVALID verdict:\n%s", stdout)
	}
	if strings.Contains(stdout, "✖") {
		t.Errorf("stdout must not mark any check ✖ (failed) when nothing was evaluated:\n%s", stdout)
	}
	if !strings.Contains(stdout, "–") {
		t.Errorf("stdout missing the – (not evaluated) icon:\n%s", stdout)
	}
	if !strings.Contains(stderr, "setup failed") {
		t.Errorf("stderr should warn about the setup failure:\n%s", stderr)
	}
}

// TestSubmitRetryAfterFailureWithoutFreshRun is the golden test the plan
// calls "the single most common path through the product": submit, get a
// non-passing verdict, submit again — with NO `grid run` in between — and
// have the retry succeed. This exercises the rotating-nonce contract
// end to end: the first response's nextNonce must be persisted to
// .grid/attempt.json and picked up by the second invocation of the exact
// same command.
func TestSubmitRetryAfterFailureWithoutFreshRun(t *testing.T) {
	loginAsStubUserForSubmit(t)
	st, srv := newStubCmdServer(t)
	st.SetSubmissionResponses([]string{stub.FailedSubmissionFixture, stub.PassedSubmissionFixture})
	setCmdCfg(srv.URL+"/api/v1", false)
	dir := t.TempDir()
	cfg.Dir = dir
	writeTestAttempt(t, dir, stub.AttemptNonce, simpleCheckSpecFixture)

	stdout1, _, err := captureOutput(t, func() error {
		return runSubmit(submitCmd, nil)
	})
	if code := exitCodeOf(t, err); code != 1 {
		t.Fatalf("first submit: exit code = %d (err %v), want 1 (FAILED)", code, err)
	}
	if !strings.Contains(stdout1, "FAILED") {
		t.Errorf("first submit stdout missing FAILED:\n%s", stdout1)
	}

	// No grid run in between — just submit again, exactly like a learner
	// fixing their code and retrying.
	stdout2, _, err := captureOutput(t, func() error {
		return runSubmit(submitCmd, nil)
	})
	if err != nil {
		t.Fatalf("second submit (retry without a fresh grid run): %v", err)
	}
	if !strings.Contains(stdout2, "PASSED") {
		t.Errorf("second submit stdout missing PASSED:\n%s", stdout2)
	}
	if got := st.SubmissionCount(); got != 2 {
		t.Errorf("SubmissionCount = %d, want 2 (both submits accepted)", got)
	}
}

// TestSubmit409NonceAlreadyUsedExitsOneWithoutRetry proves the 409
// nonce_already_used path: exit 1, the stale-attempt-file hint, and no
// client-side auto-retry (the client only retries 5xx/429, never 4xx).
func TestSubmit409NonceAlreadyUsedExitsOneWithoutRetry(t *testing.T) {
	loginAsStubUserForSubmit(t)
	st, srv := newStubCmdServer(t)
	st.ForceStatus("/cli/submissions", http.StatusConflict)
	setCmdCfg(srv.URL+"/api/v1", false)
	dir := t.TempDir()
	cfg.Dir = dir
	writeTestAttempt(t, dir, stub.AttemptNonce, simpleCheckSpecFixture)

	_, _, err := captureOutput(t, func() error {
		return runSubmit(submitCmd, nil)
	})
	if code := exitCodeOf(t, err); code != 1 {
		t.Fatalf("submit on forced 409: exit code = %d (err %v), want 1", code, err)
	}
	if !strings.Contains(err.Error(), "stale") {
		t.Errorf("error should explain the local attempt file is stale, got: %s", err.Error())
	}
	if got := st.SubmissionCount(); got != 0 {
		t.Errorf("a 409 must never be counted as an accepted submission, got %d", got)
	}
}

// TestSubmit401ExitsThree proves an expired/invalid session maps to exit 3
// with the `grid login` hint.
func TestSubmit401ExitsThree(t *testing.T) {
	credsPath := isolateHome(t)
	if err := auth.NewStore(credsPath).Save(auth.Credentials{
		SessionKey: "a-session-key-the-server-rejects",
		User:       stubTestUser(),
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	_, srv := newStubCmdServer(t)
	setCmdCfg(srv.URL+"/api/v1", false)
	dir := t.TempDir()
	cfg.Dir = dir
	writeTestAttempt(t, dir, stub.AttemptNonce, simpleCheckSpecFixture)

	_, _, err := captureOutput(t, func() error {
		return runSubmit(submitCmd, nil)
	})
	if code := exitCodeOf(t, err); code != 3 {
		t.Fatalf("submit with a rejected session: exit code = %d (err %v), want 3", code, err)
	}
	if !strings.Contains(err.Error(), "grid login") {
		t.Errorf("error should suggest `grid login`, got: %s", err.Error())
	}
}

// TestSubmitRunsInATempCwdForNoneModeAndPersistsBookkeeping proves
// GRID_SPEC §4's "checks run in a temp cwd" for mode: none end to end
// through grid submit specifically: the rotated nonce and the last-verdict
// record both land against the CLI's state directory (af.DirPath), not the
// throwaway exec-root temp dir cleanup deletes before this command
// returns — so a later `grid submit` retry and `grid status` both still
// work with no fresh `grid run`.
func TestSubmitRunsInATempCwdForNoneModeAndPersistsBookkeeping(t *testing.T) {
	loginAsStubUserForSubmit(t)
	st, srv := newStubCmdServer(t)
	st.SetSubmissionResponses([]string{stub.FailedSubmissionFixture, stub.PassedSubmissionFixture})
	setCmdCfg(srv.URL+"/api/v1", false)
	cfg.Dir = t.TempDir() // an unrelated cwd — none-mode never needs a real one
	af := writeTestNoneAttempt(t, stub.AttemptNonce, noneModeCheckSpecFixture)

	stdout1, _, err := captureOutput(t, func() error {
		return runSubmit(submitCmd, nil)
	})
	if code := exitCodeOf(t, err); code != 1 {
		t.Fatalf("first submit (mode: none): exit code = %d (err %v), want 1 (FAILED)", code, err)
	}
	if !strings.Contains(stdout1, "FAILED") {
		t.Errorf("first submit stdout missing FAILED:\n%s", stdout1)
	}

	lastVerdict, err := workspace.LoadLastVerdict(af.DirPath)
	if err != nil {
		t.Fatalf("LoadLastVerdict against the state dir: %v", err)
	}
	if lastVerdict.Verdict != api.VerdictFailed {
		t.Errorf("last-verdict.json Verdict = %q, want FAILED", lastVerdict.Verdict)
	}

	// No fresh `grid run` — the rotated nonce must have been persisted
	// against the state dir for this retry to be accepted at all.
	stdout2, _, err := captureOutput(t, func() error {
		return runSubmit(submitCmd, nil)
	})
	if err != nil {
		t.Fatalf("second submit (retry without a fresh grid run): %v", err)
	}
	if !strings.Contains(stdout2, "PASSED") {
		t.Errorf("second submit stdout missing PASSED:\n%s", stdout2)
	}
}

// TestSubmitExits1OnNotEnrolled proves enrollment is re-checked on every
// submit, not cached from `grid run` (GRID_SPEC §5.2/§5.3/§6): the
// identical not_enrolled 403 shape maps to exit 1 here too, with the
// enrollUrl printed to stderr verbatim.
func TestSubmitExits1OnNotEnrolled(t *testing.T) {
	loginAsStubUserForSubmit(t)
	st, srv := newStubCmdServer(t)
	const wantURL = "http://localhost:3000/courses/build-a-redis-core-in-go"
	st.ForceNotEnrolled("/cli/submissions", stub.CourseSlug, wantURL)
	setCmdCfg(srv.URL+"/api/v1", false)
	dir := t.TempDir()
	cfg.Dir = dir
	writeTestAttempt(t, dir, stub.AttemptNonce, simpleCheckSpecFixture)

	_, stderr, err := captureOutput(t, func() error {
		return runSubmit(submitCmd, nil)
	})
	if code := exitCodeOf(t, err); code != 1 {
		t.Fatalf("submit on not_enrolled: exit code = %d (err %v), want 1", code, err)
	}
	if !strings.Contains(stderr, wantURL) {
		t.Errorf("stderr should contain the enrollUrl verbatim, got: %s", stderr)
	}
}

// TestSubmitMissingAttemptFileExitsUsageError mirrors grid check's exit-5
// contract for grid submit.
func TestSubmitMissingAttemptFileExitsUsageError(t *testing.T) {
	loginAsStubUserForSubmit(t)
	_, srv := newStubCmdServer(t)
	setCmdCfg(srv.URL+"/api/v1", false)
	cfg.Dir = t.TempDir()

	_, _, err := captureOutput(t, func() error {
		return runSubmit(submitCmd, nil)
	})
	if code := exitCodeOf(t, err); code != 5 {
		t.Fatalf("submit without attempt.json: exit code = %d (err %v), want 5", code, err)
	}
}

// --- 413 retry: unit-tested directly against submitWithRetry/
// retruncateEnvelope, the exact functions runSubmit calls, with a raw
// httptest handler the test fully controls (constructing >64KB of real
// evidence through an actual exec measurement would be slow and
// platform-fragile; this proves the identical code path precisely). ---

// jsonEnvelope writes the §6 envelope shape the real client unwraps.
func jsonEnvelope(w http.ResponseWriter, status int, data string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `{"success":`+strconv.FormatBool(status < 400)+`,"statusCode":`+strconv.Itoa(status)+`,"message":"m","data":`+data+`}`)
}

// bigEvidenceEnvelope builds an envelope whose single result carries a
// >64KB evidence field — the 413 trigger — signed with the test device key.
func bigEvidenceEnvelope(t *testing.T, key ed25519.PrivateKey) *attest.AttestationEnvelope {
	t.Helper()
	env := &attest.AttestationEnvelope{
		AttemptID:    "attempt-1",
		AttemptNonce: "nonce-1",
		SubmittedAt:  "2026-11-15T18:30:00Z",
		ClientMetadata: attest.ClientMetadata{
			CLIVersion:    "test",
			CLIBinaryHash: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			OS:            "linux",
			Arch:          "amd64",
		},
		WorkspaceProof: attest.WorkspaceProof{
			GitHeadSha:     "0000000000000000000000000000000000000000",
			SaltedTreeHash: "sha256:7c3a1f2e4d5c6b7a8901fedcba9876543210fedcba9876543210fedcba987654",
		},
		Results: []attest.Result{
			{CheckID: "c1", Status: "ok", DurationMs: 1, Evidence: map[string]any{"stdout": strings.Repeat("x", 80000)}},
		},
	}
	sig, err := attest.SignEnvelope(key, env)
	if err != nil {
		t.Fatalf("SignEnvelope: %v", err)
	}
	env.DeviceSignature = sig
	return env
}

func TestSubmitWithRetryRetriesOnceOn413WithSmallerSecondPayload(t *testing.T) {
	var mu sync.Mutex
	var hits int
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		hits++
		n := hits
		bodies = append(bodies, body)
		mu.Unlock()
		if n == 1 {
			jsonEnvelope(w, http.StatusRequestEntityTooLarge, "null")
			return
		}
		jsonEnvelope(w, http.StatusOK, stub.PassedSubmissionFixture)
	}))
	defer srv.Close()

	client, err := api.New(srv.URL+"/api/v1", api.WithSessionKey("test-session-key"))
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	key := attest.DeterministicTestKey()
	env := bigEvidenceEnvelope(t, key)

	resp, err := submitWithRetry(context.Background(), client, env, key)
	if err != nil {
		t.Fatalf("submitWithRetry: %v", err)
	}
	if resp.Verdict != api.VerdictPassed {
		t.Errorf("verdict = %q, want PASSED", resp.Verdict)
	}

	mu.Lock()
	defer mu.Unlock()
	if hits != 2 {
		t.Fatalf("handler hit-count = %d, want exactly 2 (one 413, one retry)", hits)
	}
	if len(bodies[1]) >= len(bodies[0]) {
		t.Errorf("second post was %d bytes, first was %d bytes — want the retry smaller (half-cap re-truncation)", len(bodies[1]), len(bodies[0]))
	}
	// The retruncated envelope is re-signed: the device signature over the
	// second body must verify against the test key (GRID_SPEC §5.3 — the
	// signature covers the canonical results that were just changed).
	var resent attest.AttestationEnvelope
	if err := json.Unmarshal(bodies[1], &resent); err != nil {
		t.Fatalf("decoding the retried body: %v", err)
	}
	if !attest.VerifyEnvelopeSignature(key.Public().(ed25519.PublicKey), &resent) {
		t.Error("the re-truncated envelope's deviceSignature does not verify after re-signing")
	}
}

// TestSubmitWithRetrySecondFailureIsTerminal proves a second 413 (even
// after re-truncation) does not retry a third time.
func TestSubmitWithRetrySecondFailureIsTerminal(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		hits++
		jsonEnvelope(w, http.StatusRequestEntityTooLarge, "null")
	}))
	defer srv.Close()
	client, err := api.New(srv.URL+"/api/v1", api.WithSessionKey("test-session-key"))
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	key := attest.DeterministicTestKey()
	_, err = submitWithRetry(context.Background(), client, bigEvidenceEnvelope(t, key), key)
	if !errors.Is(err, api.ErrTooLarge) {
		t.Fatalf("submitWithRetry error = %v, want ErrTooLarge", err)
	}
	if hits != 2 {
		t.Errorf("handler hit-count = %d, want exactly 2 (no third attempt)", hits)
	}
}

// TestSubmit422AttestationExitsTwo proves GRID_SPEC §5.5/A6: a 422
// attestation failure maps to exit 2 with the server's machine code
// surfaced in the error.
func TestSubmit422AttestationExitsTwo(t *testing.T) {
	loginAsStubUserForSubmit(t)
	st, srv := newStubCmdServer(t)
	st.ForceStatus("/cli/submissions", http.StatusUnprocessableEntity)
	setCmdCfg(srv.URL+"/api/v1", false)
	dir := t.TempDir()
	cfg.Dir = dir
	writeTestAttempt(t, dir, stub.AttemptNonce, simpleCheckSpecFixture)

	_, _, err := captureOutput(t, func() error {
		return runSubmit(submitCmd, nil)
	})
	if code := exitCodeOf(t, err); code != 2 {
		t.Fatalf("submit on a forced 422: exit code = %d (err %v), want 2", code, err)
	}
	if !strings.Contains(err.Error(), "invalid_device_signature") {
		t.Errorf("error should surface the server's attestation code, got: %s", err.Error())
	}
	if got := st.SubmissionCount(); got != 0 {
		t.Errorf("a 422 must never count as an accepted submission, got %d", got)
	}
}

// TestBuildEnvelopeSignsA4Layout is the plan's golden test (criterion 2):
// a built envelope's deviceSignature verifies with the test device key
// over the A4 canonical layout — the unsigned envelope bytes — and stops
// verifying the moment any covered field changes.
func TestBuildEnvelopeSignsA4Layout(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("seed workspace file: %v", err)
	}
	af := workspace.AttemptFile{AttemptID: stub.AttemptID, Nonce: stub.AttemptNonce}
	outcome := runner.Outcome{
		CheckResults: []runner.Result{
			{CheckID: "resp-server-exists", Status: runner.StatusOK, DurationMS: 12, Evidence: runner.Evidence{"exists": true}},
		},
	}
	key := attest.DeterministicTestKey()
	startedAt := time.Date(2026, 11, 15, 18, 30, 0, 0, time.UTC)
	finishedAt := startedAt.Add(3 * time.Minute)

	env, err := buildEnvelope(af, dir, outcome, startedAt, finishedAt, key)
	if err != nil {
		t.Fatalf("buildEnvelope: %v", err)
	}
	pub := key.Public().(ed25519.PublicKey)
	if !attest.VerifyEnvelopeSignature(pub, env) {
		t.Fatal("the built envelope's deviceSignature does not verify against the signing key")
	}
	// The signature covers exactly the A4 unsigned bytes.
	unsigned, err := attest.MarshalUnsigned(env)
	if err != nil {
		t.Fatalf("MarshalUnsigned: %v", err)
	}
	sig, ok := strings.CutPrefix(env.DeviceSignature, "base64:")
	if !ok {
		t.Fatalf("deviceSignature = %q, want the base64: prefix", env.DeviceSignature)
	}
	rawSig, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		t.Fatalf("decoding deviceSignature: %v", err)
	}
	if !attest.Verify(pub, unsigned, rawSig) {
		t.Error("attest.Verify rejects the signature over the unsigned envelope bytes")
	}

	// A4 wiring: attemptNonce is the attempt's rotating nonce, and the
	// saltedTreeHash is the A4 digest over the Fingerprint walk.
	if env.AttemptNonce != stub.AttemptNonce {
		t.Errorf("attemptNonce = %q, want the attempt file's nonce", env.AttemptNonce)
	}
	tree, err := workspace.Fingerprint(dir)
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	files := make([]attest.FileHash, len(tree.Files))
	for i, f := range tree.Files {
		files[i] = attest.FileHash{Path: f.Path, SHA256: f.SHA256}
	}
	if want := attest.SaltedTreeHash(stub.AttemptNonce, files); env.WorkspaceProof.SaltedTreeHash != want {
		t.Errorf("saltedTreeHash = %q, want the A4 digest over the walk %q", env.WorkspaceProof.SaltedTreeHash, want)
	}
	if len(env.WorkspaceProof.GitHeadSha) != 40 {
		t.Errorf("gitHeadSha = %q, want 40 hex (zeros when the workspace has no commits)", env.WorkspaceProof.GitHeadSha)
	}
	if env.ClientMetadata.CLIVersion != buildinfo.Version || env.ClientMetadata.CLIBinaryHash == "" {
		t.Errorf("clientMetadata = %+v, want this build's version and SelfHash", env.ClientMetadata)
	}

	// Tamper with a covered field and the signature must stop verifying.
	env.Results[0].Status = "skipped"
	if attest.VerifyEnvelopeSignature(pub, env) {
		t.Error("the signature still verifies after mutating a covered field")
	}
}

// TestSigningReproducesAttestVectors proves the CLI's envelope/signing code
// reproduces the frozen grid-extensions fixtures byte-for-byte: the device
// signature over the canonical unsigned envelope and the ext signature
// over the A4 telemetry layout both regenerate exactly, and verify against
// the vector's public key.
//
// The vectors live in testdata/ so a standalone checkout (CI) still runs the
// test. When the grid-extensions repo is checked out side-by-side, the two
// copies must be byte-identical — that is the drift guard that keeps the
// vendored fixture honest.
func TestSigningReproducesAttestVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "attest-vectors.json"))
	if err != nil {
		t.Fatalf("reading ../testdata/attest-vectors.json: %v", err)
	}
	sibling := filepath.Join("..", "..", "grid-extensions", "fixtures", "attest-vectors.json")
	if sib, err := os.ReadFile(sibling); err == nil && !bytes.Equal(sib, raw) {
		t.Fatalf("../testdata/attest-vectors.json has drifted from %s; run `make vectors` in grid-extensions and copy the fixture over", sibling)
	}
	var vectors struct {
		Signatures []struct {
			Name            string          `json:"name"`
			Kind            string          `json:"kind"`
			PublicKey       string          `json:"publicKey"`
			Envelope        json.RawMessage `json:"envelope"`
			AttemptID       string          `json:"attemptId"`
			AttemptNonce    string          `json:"attemptNonce"`
			CheckID         string          `json:"checkId"`
			ExtBinaryHash   string          `json:"extBinaryHash"`
			Telemetry       map[string]any  `json:"telemetry"`
			Signature       string          `json:"signature"`
			SigningInputHex string          `json:"signingInputHex"`
		} `json:"signatures"`
	}
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatalf("parsing the vectors: %v", err)
	}
	if len(vectors.Signatures) < 2 {
		t.Fatalf("vectors carry %d signature cases, want ≥ 2", len(vectors.Signatures))
	}
	key := attest.DeterministicTestKey()
	for _, v := range vectors.Signatures {
		wantInput, err := hex.DecodeString(v.SigningInputHex)
		if err != nil {
			t.Fatalf("%s: decoding signingInputHex: %v", v.Name, err)
		}
		pub, err := attest.PublicKeyFromB64(v.PublicKey)
		if err != nil {
			t.Fatalf("%s: vector public key: %v", v.Name, err)
		}
		switch v.Kind {
		case "device":
			var env attest.AttestationEnvelope
			if err := json.Unmarshal(v.Envelope, &env); err != nil {
				t.Fatalf("%s: decoding the fixture envelope: %v", v.Name, err)
			}
			gotInput, err := attest.MarshalUnsigned(&env)
			if err != nil {
				t.Fatalf("%s: MarshalUnsigned: %v", v.Name, err)
			}
			if !bytes.Equal(gotInput, wantInput) {
				t.Errorf("%s: canonical signing input drifted from the fixture", v.Name)
			}
			gotSig, err := attest.SignEnvelope(key, &env)
			if err != nil {
				t.Fatalf("%s: SignEnvelope: %v", v.Name, err)
			}
			if gotSig != v.Signature {
				t.Errorf("%s: reproduced deviceSignature = %q, want the fixture's %q", v.Name, gotSig, v.Signature)
			}
			if !attest.VerifyEnvelopeSignature(pub, &env) {
				t.Errorf("%s: the fixture's deviceSignature does not verify", v.Name)
			}
		case "ext":
			gotInput, err := attest.ExtSigningInput(v.AttemptID, v.AttemptNonce, v.CheckID, v.ExtBinaryHash, v.Telemetry)
			if err != nil {
				t.Fatalf("%s: ExtSigningInput: %v", v.Name, err)
			}
			if !bytes.Equal(gotInput, wantInput) {
				t.Errorf("%s: ext signing input drifted from the fixture", v.Name)
			}
			gotSig, err := attest.SignExt(key, v.AttemptID, v.AttemptNonce, v.CheckID, v.ExtBinaryHash, v.Telemetry)
			if err != nil {
				t.Fatalf("%s: SignExt: %v", v.Name, err)
			}
			if gotSig != v.Signature {
				t.Errorf("%s: reproduced extSignature = %q, want the fixture's %q", v.Name, gotSig, v.Signature)
			}
			if !attest.VerifyExtSignature(pub, v.AttemptID, v.AttemptNonce, v.CheckID, v.ExtBinaryHash, v.Telemetry, v.Signature) {
				t.Errorf("%s: the fixture's extSignature does not verify", v.Name)
			}
		}
	}
}

// TestSubmitJSONOutputShape proves --json emits the server's response
// object verbatim plus the resolved exitCode field.
func TestSubmitJSONOutputShape(t *testing.T) {
	loginAsStubUserForSubmit(t)
	_, srv := newStubCmdServer(t)
	setCmdCfg(srv.URL+"/api/v1", true)
	dir := t.TempDir()
	cfg.Dir = dir
	writeTestAttempt(t, dir, stub.AttemptNonce, simpleCheckSpecFixture)

	stdout, _, err := captureOutput(t, func() error {
		return runSubmit(submitCmd, nil)
	})
	if err != nil {
		t.Fatalf("runSubmit --json: %v", err)
	}
	var out submitJSON
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("submit --json stdout is not valid JSON: %v\n%s", err, stdout)
	}
	if out.Verdict != api.VerdictPassed || out.ExitCode != 0 {
		t.Errorf("submit --json = %+v, want verdict PASSED and exitCode 0", out)
	}
	if strings.Contains(stdout, stub.SessionKey) {
		t.Error("submit --json output contains the session key")
	}
}
