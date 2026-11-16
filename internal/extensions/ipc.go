package extensions

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/anshul-kh/grid-cli/internal/runner"
	"github.com/anshul-kh/grid-extensions/pkg/extcommon"
)

// ErrTimeout is the typed error for an extension subprocess that outlived
// its budget — the same contract runner.ErrTimeout gives cmd measurements
// (status "error", never a verdict).
var ErrTimeout = errors.New("extension timed out")

// Call runs one framed extcommon request/response exchange against the
// binary at binPath (ARCH §3.3 / pkg/extcommon's frozen contract):
// spawn the signed binary, write the length-prefixed request, read the
// length-prefixed response, then kill the whole process group either way —
// the extension is a one-shot subprocess, never a daemon. timeout is the
// caller's check timeout plus its margin; expiry kills the group and
// returns ErrTimeout. args carries extra spawn argv (unused for official
// binaries; the test helpers need it to re-exec themselves).
//
// AttemptNonce rides the framed request (extcommon.Request): the extension
// injects it as the X-Grid-Attempt-Nonce header on its probes (ARCH §3.3/
// §4.1) and it is load-bearing in the extSignature — header injection is
// the extension's job, never the CLI's.
func Call(ctx context.Context, binPath string, req *extcommon.Request, timeout time.Duration, args ...string) (*extcommon.Response, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if _, err := exec.LookPath(binPath); err != nil {
		return nil, fmt.Errorf("extension binary %s is not executable: %w", binPath, err)
	}

	cmd := exec.CommandContext(ctx, binPath, args...)
	runner.SetProcessGroup(cmd)
	// On expiry (or caller cancellation), kill the whole process group, not
	// just the direct child; WaitDelay bounds the wait if a killed group's
	// orphan holds the pipes open — the same discipline as runner's Env.Run.
	cmd.Cancel = func() error { return runner.KillGroup(cmd.Process.Pid) }
	cmd.WaitDelay = 3 * time.Second

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("opening the extension's stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("opening the extension's stdout: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting %s: %w", binPath, err)
	}

	requestBody, err := json.Marshal(req)
	if err == nil {
		err = extcommon.WriteFrame(stdin, requestBody)
	}
	if err != nil {
		// The request never went out whole: nothing can come back — stop
		// the group now rather than waiting for the timeout.
		stdin.Close()
		_ = runner.KillGroup(cmd.Process.Pid)
		_ = cmd.Wait()
		return nil, fmt.Errorf("writing the extension request: %w", err)
	}
	stdin.Close() // EOF: a well-behaved one-shot extension exits after replying

	responseBody, readErr := extcommon.ReadFrame(bufio.NewReaderSize(stdout, 64<<10))
	if readErr != nil {
		_ = runner.KillGroup(cmd.Process.Pid)
		_ = cmd.Wait()
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%w after %s: %s (stderr: %s)", ErrTimeout, timeout, binPath, truncateStderr(stderr.String()))
		}
		return nil, fmt.Errorf("reading the extension response: %w (stderr: %s)", readErr, truncateStderr(stderr.String()))
	}

	var resp extcommon.Response
	if err := json.Unmarshal(responseBody, &resp); err != nil {
		_ = runner.KillGroup(cmd.Process.Pid)
		_ = cmd.Wait()
		return nil, fmt.Errorf("decoding the extension response: %w", err)
	}

	// The exchange is over either way: kill the group and reap, so no
	// extension (or anything it spawned) outlives the call.
	_ = runner.KillGroup(cmd.Process.Pid)
	if err := cmd.Wait(); err != nil {
		// A nonzero/signal exit after a valid response is the extension's
		// own shutdown behaviour, not a failed measurement — the framed
		// response is authoritative.
		_ = err
	}

	if resp.Error != "" {
		return nil, fmt.Errorf("grid extension: %s", resp.Error)
	}
	return &resp, nil
}

// truncateStderr keeps a failed call's diagnostics readable: the extension
// binary is release-hardened (-s -w) and normally silent, so anything it
// did print on stderr is worth showing — head-capped, not the whole dump.
func truncateStderr(s string) string {
	const max = 4 << 10
	if len(s) <= max {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(s[:max]) + "…"
}
