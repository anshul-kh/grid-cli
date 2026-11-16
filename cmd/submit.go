package cmd

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"

	"github.com/anshul-kh/grid-cli/internal/api"
	"github.com/anshul-kh/grid-cli/internal/auth"
	"github.com/anshul-kh/grid-cli/internal/buildinfo"
	"github.com/anshul-kh/grid-cli/internal/runner"
	"github.com/anshul-kh/grid-cli/internal/ui"
	"github.com/anshul-kh/grid-cli/internal/workspace"
	"github.com/anshul-kh/grid-extensions/pkg/attest"
	"github.com/spf13/cobra"
)

var submitCmd = &cobra.Command{
	Use:   "submit",
	Short: "Submit the workspace for a verdict",
	Long: `Run the lesson's checks, sign the evidence, and submit it to the
server for a verdict.

grid submit never judges locally: every check result it uploads is ok,
error, or skipped. The server applies the real check-spec and returns
PASSED, FAILED, or INVALID — this command only renders that answer.`,
	Args: cobra.NoArgs,
	RunE: runSubmit,
}

func init() {
	rootCmd.AddCommand(submitCmd)
}

// submitJSON is the stable --json schema for grid submit: the server's
// response object verbatim (embedded, so its fields land at the top level),
// plus the exit-code-relevant verdict already resolved (TASK.md pass 7
// item 4).
type submitJSON struct {
	api.SubmissionResponse
	ExitCode int `json:"exitCode"`
}

func runSubmit(cmd *cobra.Command, args []string) error {
	ls, err := loadSpecForMeasurement()
	if err != nil {
		return err
	}
	defer ls.cleanup()
	store, err := auth.DefaultStore()
	if err != nil {
		return err
	}
	creds, err := store.Load()
	if errors.Is(err, auth.ErrNotLoggedIn) {
		return exitError(3, fmt.Errorf("not logged in — run `grid login` first"))
	} else if err != nil {
		return err
	}
	client, err := newAPIClient(creds.SessionKey)
	if err != nil {
		return err
	}

	rn := buildRunner(ls)
	reg, err := buildRegistry(ls)
	if err != nil {
		return err
	}
	theme := Theme()

	mctx, cancelMeasure := measurementCtx()
	defer cancelMeasure()
	fmt.Fprintln(os.Stderr, "Running checks...")
	sp := attachSpinner(rn, theme, "")
	startedAt := time.Now().UTC()
	outcome := rn.Run(mctx, ls.cs, reg)
	finishedAt := time.Now().UTC()
	sp.Stop()
	if interrupted(mctx) {
		return exitError(130, fmt.Errorf("interrupted"))
	}
	if outcome.SetupErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", outcome.SetupErr)
	}

	// The device key (GRID_SPEC §5.1) signs the envelope; reusing the
	// keypair registered at device/start is what makes the server's
	// signature check pass.
	key, err := auth.LoadOrCreateDeviceKey()
	if err != nil {
		return exitError(1, fmt.Errorf("could not prepare the device key: %w", err))
	}

	env, err := buildEnvelope(ls.af, ls.dir, outcome, startedAt, finishedAt, key)
	if err != nil {
		return exitError(1, err)
	}

	ctx, cancel := commandCtx()
	defer cancel()
	fmt.Fprintln(os.Stderr, "Submitting...")
	resp, err := submitWithRetry(ctx, client, env, key)
	if err != nil {
		return submitError(err)
	}

	// GRID_SPEC §5.4: the attempt survives a non-passing submission by
	// rotating its nonce; persist it now so the next `grid submit` (no
	// fresh `grid run` needed) uses a nonce the server will still accept.
	// bookDir, not dir: for a mode: none attempt dir is a throwaway temp
	// cwd cleanup removes before this command returns, never where the
	// attempt's local records live.
	if resp.NextNonce != nil {
		if err := workspace.UpdateNonce(ls.bookDir, *resp.NextNonce); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not persist the rotated nonce: %s\n", err)
		}
	}

	// Record this verdict under .grid/ so `grid status` can show it later
	// without resubmitting anything (pass 8). Best-effort: a write failure
	// here must not turn a successful submission into a failed command.
	lastVerdict := workspace.LastVerdict{SubmittedAt: finishedAt, Verdict: resp.Verdict, Checks: resp.Checks, XP: resp.XP}
	if err := workspace.WriteLastVerdict(ls.bookDir, lastVerdict); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not record last-verdict results: %s\n", err)
	}

	exitCode := verdictExitCode(resp.Verdict)
	if cfg.JSON {
		if err := writeSubmitJSON(resp, exitCode); err != nil {
			return err
		}
	} else {
		renderSubmission(os.Stdout, theme, resp, env.Results)
	}
	if exitCode != 0 {
		return exitError(exitCode, fmt.Errorf("verdict: %s", resp.Verdict))
	}
	return nil
}

// buildEnvelope assembles and device-signs the AttestationEnvelope
// (GRID_SPEC §5.3/A4): attemptNonce is the attempt file's rotating nonce,
// cliBinaryHash is this binary's attest.SelfHash, and workspaceProof pairs
// the workspace's git HEAD with the salted tree hash over the
// workspace.Fingerprint walk. Pure given its inputs, so the golden test can
// pin the signature against a test key. startedAt is the measurement
// window's open end (A4's only timestamp, submittedAt, is finishedAt).
func buildEnvelope(af workspace.AttemptFile, dir string, outcome runner.Outcome, startedAt, finishedAt time.Time, key ed25519.PrivateKey) (*attest.AttestationEnvelope, error) {
	cliHash, err := attest.SelfHash()
	if err != nil {
		return nil, fmt.Errorf("hashing this CLI binary: %w", err)
	}
	tree, err := workspace.Fingerprint(dir)
	if err != nil {
		return nil, fmt.Errorf("fingerprinting the workspace: %w", err)
	}
	files := make([]attest.FileHash, len(tree.Files))
	for i, f := range tree.Files {
		files[i] = attest.FileHash{Path: f.Path, SHA256: f.SHA256}
	}
	env := &attest.AttestationEnvelope{
		AttemptID:    af.AttemptID,
		AttemptNonce: af.Nonce,
		SubmittedAt:  finishedAt.UTC().Format(time.RFC3339),
		ClientMetadata: attest.ClientMetadata{
			CLIVersion:    buildinfo.Version,
			CLIBinaryHash: cliHash,
			OS:            runtime.GOOS,
			Arch:          runtime.GOARCH,
		},
		WorkspaceProof: attest.WorkspaceProof{
			GitHeadSha:     workspace.GitHeadSHA(dir),
			SaltedTreeHash: attest.SaltedTreeHash(af.Nonce, files),
		},
		Results: buildResults(outcome),
	}
	sig, err := attest.SignEnvelope(key, env)
	if err != nil {
		return nil, fmt.Errorf("signing the submission envelope: %w", err)
	}
	env.DeviceSignature = sig
	return env, nil
}

// verdictExitCode maps the server's verdict to TASK.md §3's exit codes:
// 0 PASSED, 1 FAILED, 2 INVALID.
func verdictExitCode(v api.Verdict) int {
	switch v {
	case api.VerdictPassed:
		return 0
	case api.VerdictFailed:
		return 1
	default: // api.VerdictInvalid, or anything the client doesn't recognise
		return 2
	}
}

// submitWithRetry posts the device-signed envelope, and on a 413 (evidence
// over the server's caps) re-truncates every result's evidence at half the
// normal cap and retries exactly once (GRID_SPEC §5.4). A second 413 is
// terminal.
func submitWithRetry(ctx context.Context, client *api.Client, env *attest.AttestationEnvelope, key ed25519.PrivateKey) (api.SubmissionResponse, error) {
	resp, err := client.Submit(ctx, env)
	if err == nil || !errors.Is(err, api.ErrTooLarge) {
		return resp, err
	}
	retried, err := retruncateEnvelope(env, key)
	if err != nil {
		return api.SubmissionResponse{}, err
	}
	resp, err = client.Submit(ctx, retried)
	if errors.Is(err, api.ErrTooLarge) {
		return api.SubmissionResponse{}, fmt.Errorf("evidence is still too large after re-truncating at half the cap: %w", err)
	}
	return resp, err
}

// retruncateEnvelope re-caps every result's evidence at half the normal
// §5.4 limits (runner.Retruncate), leaving attemptId, attemptNonce and the
// workspace proof unchanged, and re-signs: the device signature covers the
// canonical results, so any change to them changes the signed bytes. The
// nonce is reused: a 413 is rejected before the server ever judges or
// consumes it.
func retruncateEnvelope(env *attest.AttestationEnvelope, key ed25519.PrivateKey) (*attest.AttestationEnvelope, error) {
	out := *env
	out.Results = make([]attest.Result, len(env.Results))
	for i, res := range env.Results {
		if res.Evidence != nil {
			res.Evidence = map[string]any(runner.Retruncate(runner.Evidence(res.Evidence), 0.5))
		}
		out.Results[i] = res
	}
	out.DeviceSignature = ""
	sig, err := attest.SignEnvelope(key, &out)
	if err != nil {
		return nil, fmt.Errorf("re-signing the re-truncated envelope: %w", err)
	}
	out.DeviceSignature = sig
	return &out, nil
}

// buildResults flattens an Outcome's setup and check results, in the order
// they were attempted, into the wire shape (GRID_SPEC §5.3).
func buildResults(outcome runner.Outcome) []attest.Result {
	results := make([]attest.Result, 0, len(outcome.SetupResults)+len(outcome.CheckResults))
	for _, res := range outcome.SetupResults {
		results = append(results, toCheckResult(res))
	}
	for _, res := range outcome.CheckResults {
		results = append(results, toCheckResult(res))
	}
	return results
}

// toCheckResult converts one local measurement into its wire shape. Status
// is carried through exactly as measured — ok, error, or skipped, never a
// judgement (GRID_SPEC §1). An ext result (§5.3/A4 result kinds) carries
// its telemetry/ext fields *instead of* evidence — the nil Evidence keeps
// the two kinds apart on the wire exactly as the spec requires.
func toCheckResult(res runner.Result) attest.Result {
	out := attest.Result{
		CheckID:    res.CheckID,
		Status:     string(res.Status),
		DurationMs: res.DurationMS,
	}
	if res.Ext != nil {
		out.Extension = res.Ext.Extension
		out.ExtBinaryHash = res.Ext.ExtBinaryHash
		out.Telemetry = res.Ext.Telemetry
		out.ExtSignature = res.Ext.ExtSignature
		return out
	}
	if res.Evidence != nil {
		out.Evidence = map[string]any(res.Evidence)
	}
	return out
}

// renderSubmission writes grid submit's full human output (TASK.md §7):
// the verdict banner, a rule-bounded ✔/✖/– check table, a "Failures"
// section expanding anything that isn't a ✔ (only when there is one), the
// XP line, streak, new badges, and the next-lesson command — through a
// single ui.Flow so the blank lines between them never double up or leave
// a trailing one (layout.go).
func renderSubmission(w io.Writer, theme *ui.Theme, resp api.SubmissionResponse, results []attest.Result) {
	width := ui.RenderWidth()
	rule := theme.Dim(ui.Rule(width))
	rows := ui.BuildVerdictRows(resp.Checks, results)

	flow := ui.NewFlow(w)
	flow.Line(renderBlock(func(bw io.Writer) { ui.RenderVerdictBanner(bw, theme, resp.Verdict) }))
	flow.Blank()
	flow.Line(rule)
	flow.Line(renderBlock(func(bw io.Writer) { ui.RenderVerdictTable(bw, theme, rows) }))
	flow.Line(rule)
	if hasAnyFailure(rows) {
		flow.Section(theme, "Failures", width, func(bw io.Writer) { ui.RenderFailureDetails(bw, theme, rows) })
	}
	flow.Blank()
	flow.Line(renderBlock(func(bw io.Writer) { ui.RenderXPLine(bw, theme, resp.XP, resp.Stats.Rank) }))
	flow.Line(renderBlock(func(bw io.Writer) { ui.RenderStreak(bw, theme, resp.Stats.CurrentStreak) }))
	if badges := renderBlock(func(bw io.Writer) { ui.RenderNewBadges(bw, theme, resp.Unlocked) }); badges != "" {
		flow.Line(badges)
	}
	if resp.Next != nil {
		flow.Blank()
		flow.Line(renderBlock(func(bw io.Writer) { ui.RenderNextLesson(bw, theme, resp.Next) }))
	}
}

// hasAnyFailure reports whether any row isn't a ✔ — the "Failures" section
// only appears when there is at least one failure to expand (TASK.md §7).
func hasAnyFailure(rows []ui.VerdictRow) bool {
	for _, row := range rows {
		if row.Icon() != "✔" {
			return true
		}
	}
	return false
}

func writeSubmitJSON(resp api.SubmissionResponse, exitCode int) error {
	return ui.WriteJSON(submitJSON{SubmissionResponse: resp, ExitCode: exitCode})
}

// submitError maps POST /cli/submissions failures to TASK.md §3 exit codes
// with the per-scenario messages GRID_SYNC_CLAUDE.md's rotating-nonce and
// code-field CONTRACTs call for: attempt_expired → 4 ("run grid run <id>
// again"); nonce_already_used / attempt_already_passed → 1 (the local
// attempt state is stale, or already closed — neither is a network/server
// problem); unauthorized → 3 ("run grid login"); not_enrolled → 1, via
// notEnrolledError (GRID_SPEC §5.2/§5.3/§6: enrollment is re-checked on
// every submit, not cached from `grid run` — the identical 403 shape can
// surface here too); 422 attestation failures → 2 with the server's
// machine code surfaced (GRID_SPEC §5.4/§5.5/A6 — the same class as a
// INVALID verdict); everything else the client already classifies → 4.
func submitError(err error) error {
	switch {
	case errors.Is(err, api.ErrAttemptExpired):
		return exitError(4, errors.New(api.UserHint(err)))
	case errors.Is(err, api.ErrNonceAlreadyUsed), errors.Is(err, api.ErrAttemptAlreadyPassed):
		return exitError(1, errors.New(api.UserHint(err)))
	case errors.Is(err, api.ErrAttestation):
		return exitError(2, fmt.Errorf("%s. %s", err, api.UserHint(err)))
	case errors.Is(err, api.ErrUnauthorized):
		return exitError(3, fmt.Errorf("session missing or expired — run `grid login` to log in again"))
	case errors.Is(err, api.ErrNotEnrolled):
		return notEnrolledError(err)
	case errors.Is(err, api.ErrDenied):
		return exitError(3, fmt.Errorf("this Grid account doesn't have access to that lesson — run `grid login` with an authorised account"))
	case errors.Is(err, api.ErrTooLarge):
		return exitError(4, fmt.Errorf("%s", err))
	default:
		return exitError(4, fmt.Errorf("could not submit: %s. %s", err, api.UserHint(err)))
	}
}
