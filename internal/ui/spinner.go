// Package ui — this file (spinner.go) owns the progress spinner `grid
// check`/`grid submit` show on stderr while measuring checks: a rotating
// frame plus the current check's title (TASK.md §7: "Spinners while
// running checks, with the current check title."). It is suppressed
// entirely when stderr is not a terminal (TASK.md §7: "Suppressed when not
// a TTY") — piped/redirected output, or a CI runner, never receives
// spinner frames, only the static progress lines the cmd layer already
// prints. Colour goes through Theme, so --no-color/NO_COLOR disable it the
// same way as every other renderer here, never forcing ANSI on.
package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// spinnerFrames is the rotating glyph sequence — plain ASCII so it renders
// identically whether or not colour is enabled.
var spinnerFrames = []string{"|", "/", "-", "\\"}

// spinnerInterval is how often the frame advances.
const spinnerInterval = 120 * time.Millisecond

// StderrIsTerminal reports whether os.Stderr is attached to a character
// device. It mirrors theme.go's stdoutIsTerminal, but against stderr —
// the stream the spinner's own TTY gate cares about (TASK.md §7).
func StderrIsTerminal() bool {
	fi, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// Spinner is a single-line progress indicator on an io.Writer (always
// os.Stderr in production). Every method is a no-op when enabled is
// false, so a caller never needs an "if isTTY" branch of its own — it
// just calls Start/SetTitle/Stop unconditionally.
type Spinner struct {
	out     io.Writer
	theme   *Theme
	enabled bool

	mu    sync.Mutex
	title string

	stop chan struct{}
	done chan struct{}
	// lastLen is only ever touched by the run() goroutine (one writer, one
	// reader within the same goroutine), so it needs no lock of its own.
	lastLen int
}

// NewSpinner returns a Spinner writing to out. enabled should be
// StderrIsTerminal() in production; tests pass it explicitly so both the
// enabled and suppressed paths are exercised without a real terminal.
func NewSpinner(out io.Writer, theme *Theme, enabled bool) *Spinner {
	return &Spinner{out: out, theme: theme, enabled: enabled}
}

// Start begins animating with the given initial title. Calling Start
// again before Stop replaces any still-running animation's goroutine with
// a fresh one. Always safe to call, even when disabled — it just never
// starts the drawing goroutine then.
func (s *Spinner) Start(title string) {
	s.SetTitle(title)
	if !s.enabled {
		return
	}
	s.stop = make(chan struct{})
	s.done = make(chan struct{})
	go s.run()
}

// SetTitle updates the title the next frame renders — the "current check
// title" TASK.md §7 asks for. Safe to call from multiple goroutines (a
// parallel_safe check group reports progress concurrently), enabled or
// not: callers do not need to gate this on TTY-ness themselves.
func (s *Spinner) SetTitle(title string) {
	s.mu.Lock()
	s.title = title
	s.mu.Unlock()
}

func (s *Spinner) currentTitle() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.title
}

// Stop ends the animation and clears its line. It stops cleanly whether
// the run it was tracking completed or errored — callers defer it
// unconditionally, right after Start, so every exit path clears the line.
// A no-op when disabled or when Start was never (successfully) called.
func (s *Spinner) Stop() {
	if !s.enabled || s.stop == nil {
		return
	}
	close(s.stop)
	<-s.done
	fmt.Fprint(s.out, "\r"+strings.Repeat(" ", s.lastLen)+"\r")
	s.stop, s.done, s.lastLen = nil, nil, 0
}

func (s *Spinner) run() {
	defer close(s.done)
	ticker := time.NewTicker(spinnerInterval)
	defer ticker.Stop()
	frame := 0
	s.draw(frame)
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			frame = (frame + 1) % len(spinnerFrames)
			s.draw(frame)
		}
	}
}

// draw renders one frame. It tracks the previous frame's plain-text length
// (lastLen) so a title that shrinks between frames still gets its leftover
// tail blanked out, not just overwritten up to the new, shorter length.
func (s *Spinner) draw(frame int) {
	plain := spinnerFrames[frame] + " " + s.currentTitle()
	pad := 0
	if s.lastLen > len(plain) {
		pad = s.lastLen - len(plain)
	}
	s.lastLen = len(plain)
	fmt.Fprint(s.out, "\r"+s.theme.Dim(plain)+strings.Repeat(" ", pad))
}
