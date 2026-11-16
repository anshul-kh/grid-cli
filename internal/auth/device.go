// Package auth: device.go drives the GRID_SPEC §5.1 device-code login:
// start pairing, print the user code large plus the verification URL, open
// the browser best-effort, then poll at the server's interval — doubling on
// slow_down — until the code is approved, expires, or is denied.
package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/anshul-kh/grid-cli/internal/api"
	"github.com/anshul-kh/grid-cli/internal/buildinfo"
)

// ErrLoginExpired is returned when the pairing window (expiresIn) elapsed
// without the code being approved. Distinct from api.ErrAttemptExpired (the
// server's 410) so callers can give the same actionable message for both.
var ErrLoginExpired = errors.New("login window expired")

// Flow runs the §5.1 device-code login against an api.Client. Every side
// effect is injectable for tests: where the prompt is written, how the
// browser is opened, how waiting happens, and the clock.
type Flow struct {
	client *api.Client
	// out receives the human pairing prompt (stdout in grid login, or
	// stderr when --json keeps stdout machine-only).
	out io.Writer
	// errOut receives diagnostics and progress (stderr).
	errOut io.Writer
	// devicePublicKey is the base64 raw 32-byte Ed25519 public key
	// registered at device/start (GRID_SPEC §5.1); the private half stays
	// in LoadOrCreateDeviceKey's store and signs every submission envelope.
	devicePublicKey string
	bold            func(string) string
	open            func(ctx context.Context, url string) error
	sleep           func(context.Context, time.Duration) error
	now             func() time.Time
	verbose         bool
}

// FlowOption configures a Flow.
type FlowOption func(*Flow)

// WithBrowserOpener overrides how the verification URL is opened. The
// default shells out per TASK.md §5 (open / xdg-open / rundll32); the
// returned error is never fatal — the URL is always printed as the fallback.
func WithBrowserOpener(fn func(ctx context.Context, url string) error) FlowOption {
	return func(f *Flow) { f.open = fn }
}

// WithBold overrides the emphasis used for the user code (cmd passes the
// colour theme; the default is plain, so captured output is ANSI-free).
func WithBold(fn func(string) string) FlowOption {
	return func(f *Flow) {
		if fn != nil {
			f.bold = fn
		}
	}
}

// WithSleeper overrides waiting between polls (tests record the intervals
// instead of sleeping).
func WithSleeper(fn func(context.Context, time.Duration) error) FlowOption {
	return func(f *Flow) { f.sleep = fn }
}

// WithClock overrides the clock used for the expiresIn budget.
func WithClock(fn func() time.Time) FlowOption {
	return func(f *Flow) {
		if fn != nil {
			f.now = fn
		}
	}
}

// WithDevicePublicKey sets the §5.1 devicePublicKey sent to device/start —
// base64 of the raw 32-byte Ed25519 public key (attest.PublicKeyB64 of the
// LoadOrCreateDeviceKey keypair).
func WithDevicePublicKey(b64 string) FlowOption {
	return func(f *Flow) { f.devicePublicKey = b64 }
}

// WithVerbose logs each poll transition to errOut.
func WithVerbose(v bool) FlowOption {
	return func(f *Flow) { f.verbose = v }
}

// NewFlow builds a Flow that writes the pairing prompt to out and progress
// to errOut.
func NewFlow(client *api.Client, out, errOut io.Writer, opts ...FlowOption) *Flow {
	f := &Flow{
		client: client,
		out:    out,
		errOut: errOut,
		bold:   func(s string) string { return s },
		open: func(ctx context.Context, url string) error {
			return OpenBrowser(ctx, url)
		},
		sleep: func(ctx context.Context, d time.Duration) error {
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
		},
		now: time.Now,
	}
	for _, o := range opts {
		o(f)
	}
	return f
}

// Login runs the complete flow: start pairing, print the prompt, open the
// browser (best-effort), poll to a terminal state, and return the fresh
// credentials. Terminal failures — expired, denied, network — return typed
// errors the caller maps to exit codes; only success returns credentials.
func (f *Flow) Login(ctx context.Context) (Credentials, error) {
	start, err := f.Start(ctx)
	if err != nil {
		return Credentials{}, err
	}
	f.prompt(ctx, start)
	return f.Poll(ctx, start)
}

// Start initiates device pairing (POST /cli/device/start, §5.1) identifying
// this machine and CLI build.
func (f *Flow) Start(ctx context.Context) (api.DeviceStartResponse, error) {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}
	return f.client.DeviceStart(ctx, api.DeviceStartRequest{
		Hostname:        hostname,
		OS:              runtime.GOOS,
		Arch:            runtime.GOARCH,
		CliVersion:      buildinfo.Version,
		DevicePublicKey: f.devicePublicKey,
	})
}

// Poll runs the §5.1 poll loop: POST /cli/device/poll {deviceCode} at
// `interval` seconds. 428 pending keeps polling at the current interval; 429
// slow_down doubles the interval and keeps polling; 410 expired and 403
// denied are terminal, as is any other API error (network, server). The
// loop also stops when the pairing window (expiresIn) elapses, surfacing
// ErrLoginExpired so the caller can tell the user to start over.
func (f *Flow) Poll(ctx context.Context, start api.DeviceStartResponse) (Credentials, error) {
	interval := time.Duration(start.IntervalSeconds) * time.Second
	if interval < 0 {
		interval = 0
	}
	deadline := f.now().Add(time.Duration(start.ExpiresInSeconds) * time.Second)
	for {
		if !f.now().Before(deadline) {
			return Credentials{}, errLoginExpired()
		}
		resp, err := f.client.DevicePoll(ctx, start.DeviceCode)
		if err == nil {
			if resp.SessionKey == "" || resp.User == nil {
				return Credentials{}, fmt.Errorf("pairing succeeded but the server response was missing the session or user")
			}
			return Credentials{
				SessionKey: resp.SessionKey,
				User:       *resp.User,
				CreatedAt:  f.now(),
			}, nil
		}
		switch {
		case errors.Is(err, api.ErrPending):
			// Keep polling at the current interval.
		case errors.Is(err, api.ErrSlowDown):
			// GRID_SPEC §5.1: double the interval, keep polling.
			interval *= 2
		default:
			// expired (410), denied (403), and any transport/server failure
			// are terminal for this pairing attempt.
			return Credentials{}, err
		}
		if f.verbose {
			fmt.Fprintf(f.errOut, "grid login: poll returned %v; waiting %s\n", err, interval)
		}
		if interval <= 0 {
			continue
		}
		if f.now().Add(interval).After(deadline) {
			return Credentials{}, errLoginExpired()
		}
		if err := f.sleep(ctx, interval); err != nil {
			return Credentials{}, err
		}
	}
}

// prompt prints the pairing code large and unmissable plus the verification
// URL, and tries to open the browser. A browser failure never fails the
// login: the URL is the fallback and is always printed above.
func (f *Flow) prompt(ctx context.Context, start api.DeviceStartResponse) {
	fmt.Fprintf(f.out, `
Log in to Grid

    Your pairing code:

        %s

    Enter the code at:

        %s

`, f.bold(start.UserCode), start.VerificationURL)
	if err := f.open(ctx, start.VerificationURL); err != nil {
		fmt.Fprintf(f.errOut, "Could not open a browser automatically — open the URL above and enter the code there.\n")
	}
	fmt.Fprintf(f.errOut, "Waiting for approval... (Ctrl-C to cancel)\n")
}

// errLoginExpired wraps ErrLoginExpired with the actionable message.
func errLoginExpired() error {
	return fmt.Errorf("%w: the pairing code was not approved in time — run `grid login` for a fresh code", ErrLoginExpired)
}

// OpenBrowser opens url in the user's browser, best-effort per TASK.md §5:
// `open` on darwin, `xdg-open` elsewhere on unix,
// `rundll32 url.dll,FileProtocolHandler` on windows. argv slices only, via
// exec.CommandContext. Callers treat any error as non-fatal and rely on the
// printed URL instead.
func OpenBrowser(ctx context.Context, url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "open", url)
	case "windows":
		cmd = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.CommandContext(ctx, "xdg-open", url)
	}
	return cmd.Start()
}
