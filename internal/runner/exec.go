package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// DefaultCaptureCap is the per-stream capture cap: 64 KB, matching the §5.4
// per-field evidence cap so a captured stream can always be uploaded whole.
const DefaultCaptureCap = MaxFieldBytes

// ErrTimeout is the typed error for a command whose own timeout expired.
// Callers map it to status "error" — never a verdict (§4 cross-platform
// rule 6: a timeout is reported as error, not fail).
var ErrTimeout = errors.New("command timed out")

// CmdSpec describes one command execution (GRID_SPEC §4 "Running things").
// Run is always an argv slice; Shell is the discouraged escape hatch that
// routes the joined command through a shell.
type CmdSpec struct {
	Run     []string
	Shell   bool
	Dir     string // working directory for the command
	Stdin   []byte
	Env     map[string]string // check-level env; overrides spec env; both override parent
	Timeout time.Duration     // per-command timeout; 0 = no extra deadline
}

// CmdResult is a finished command's measurement: raw (not yet normalised)
// captured output, head-capped to the capture cap.
type CmdResult struct {
	ExitCode   int
	Stdout     []byte
	Stderr     []byte
	DurationMS int64
	// StdoutTruncated/StderrTruncated report that the stream exceeded the
	// capture cap (the head was kept).
	StdoutTruncated bool
	StderrTruncated bool
}

// shellArgs builds the shell argv for the discouraged `shell: true` escape
// hatch (§4 rule 1): cmd /c on Windows, sh -c elsewhere. The joined Run
// elements become the shell's command string — the CLI still never builds a
// shell string from *user input*, only from the spec's own argv slice.
func shellArgs(command string) []string {
	if runtime.GOOS == "windows" {
		return []string{"cmd", "/c", command}
	}
	return []string{"sh", "-c", command}
}

// taskkillArgs builds the Windows process-tree kill argv (taskkill /T /F).
// Declared here (portably) so its exact shape is testable on every OS; the
// Windows killGroup runs it.
func taskkillArgs(pid int) []string {
	return []string{"taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)}
}

// Run executes one command per the spec: argv slices only, Dir from the
// caller, env = parent + spec env + check env, stdout/stderr captured into
// head-biased capped buffers, and the whole process group killed on timeout
// (syscall.Setpgid + negative-PID kill on unix, taskkill /T /F on Windows)
// so a child that ignores SIGTERM — or spawns grandchildren that do — cannot
// outlive its budget. On timeout it returns the partial result plus
// ErrTimeout (wrapped). A command that cannot start (not found) returns the
// exec error with ExitCode -1.
func (e *Env) Run(ctx context.Context, s CmdSpec) (*CmdResult, error) {
	if len(s.Run) == 0 {
		return nil, errors.New("exec: empty run (spec §4: run must be a non-empty argv array)")
	}
	argv := s.Run
	if s.Shell {
		e.warnf("spec uses shell: true (discouraged, GRID_SPEC §4 rule 1): running %q via %s -c",
			strings.Join(s.Run, " "), shellArgs("")[0])
		argv = shellArgs(strings.Join(s.Run, " "))
	}

	// The per-command timeout must be derived BEFORE exec.CommandContext is
	// called: CommandContext captures the ctx value it is given at that call
	// and uses it internally to decide when to invoke Cancel. Deriving the
	// timeout afterward and only rebinding the local ctx variable would leave
	// the running Cmd watching the original (un-timed-out) context forever —
	// s.Timeout would be silently never enforced.
	if s.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = s.Dir
	cmd.Env = e.Environ(s.Env)
	if len(s.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(s.Stdin)
	}
	setSysProcAttr(cmd) // process-group leader on unix; new process group on Windows

	stdout := newCappedBuffer(e.CaptureCap)
	stderr := newCappedBuffer(e.CaptureCap)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// On context cancellation (timeout or whole-run budget), kill the whole
	// process group instead of just the direct child; WaitDelay bounds the
	// wait if a killed group's orphan holds the pipes open.
	cmd.Cancel = func() error { return killGroup(cmd.Process.Pid) }
	cmd.WaitDelay = 3 * time.Second

	start := time.Now()
	runErr := cmd.Run()
	res := &CmdResult{
		ExitCode:        -1,
		Stdout:          stdout.Bytes(),
		Stderr:          stderr.Bytes(),
		DurationMS:      time.Since(start).Milliseconds(),
		StdoutTruncated: stdout.Truncated(),
		StderrTruncated: stderr.Truncated(),
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			res.ExitCode = exitErr.ExitCode() // -1 when killed by signal
		}
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return res, fmt.Errorf("%w after %s: %s", ErrTimeout, timeoutLabel(ctx, s), strings.Join(argv, " "))
		case errors.Is(ctx.Err(), context.Canceled):
			return res, fmt.Errorf("command cancelled: %s", strings.Join(argv, " "))
		}
		return res, runErr
	}
	res.ExitCode = 0
	return res, nil
}

// timeoutLabel names which budget expired, if it is knowable.
func timeoutLabel(ctx context.Context, s CmdSpec) string {
	if s.Timeout > 0 {
		return s.Timeout.String()
	}
	if dl, ok := ctx.Deadline(); ok {
		return time.Until(dl).Round(time.Millisecond).String()
	}
	return "the run budget"
}

// cappedBuffer is a head-biased capped capture buffer: it keeps at most cap
// bytes (the head) and counts what it dropped, so a command emitting 10 MB of
// stdout costs at most cap bytes of memory (§4 "never unbounded memory")
// and the evidence layer's head-biased truncation matches what was kept.
type cappedBuffer struct {
	cap     int
	buf     []byte
	dropped int64
}

func newCappedBuffer(cap int) *cappedBuffer {
	if cap <= 0 {
		cap = DefaultCaptureCap
	}
	return &cappedBuffer{cap: cap}
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	room := b.cap - len(b.buf)
	if room > 0 {
		n := min(room, len(p))
		b.buf = append(b.buf, p[:n]...)
		b.dropped += int64(len(p) - n)
	} else {
		b.dropped += int64(len(p))
	}
	return len(p), nil
}

// Bytes returns the captured head.
func (b *cappedBuffer) Bytes() []byte { return b.buf }

// Truncated reports whether any bytes were dropped past the cap.
func (b *cappedBuffer) Truncated() bool { return b.dropped > 0 }

var _ io.Writer = (*cappedBuffer)(nil)
