package ui

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestSpinnerSuppressedWhenNotATerminal is the plan's pinned criterion: a
// test proves spinner output is absent when stderr is not a TTY. enabled
// is the caller-supplied TTY check (StderrIsTerminal in production), so
// this never needs a real terminal to exercise the suppressed path.
func TestSpinnerSuppressedWhenNotATerminal(t *testing.T) {
	var buf bytes.Buffer
	sp := NewSpinner(&buf, NewTheme(false), false)
	sp.Start("measuring the first check")
	sp.SetTitle("measuring the second check")
	time.Sleep(3 * spinnerInterval)
	sp.Stop()
	if buf.Len() != 0 {
		t.Errorf("suppressed spinner wrote %d byte(s), want 0: %q", buf.Len(), buf.String())
	}
}

// TestSpinnerDrawsTheCurrentTitleWhenEnabled proves the complementary
// enabled path actually draws frames naming the current check.
func TestSpinnerDrawsTheCurrentTitleWhenEnabled(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	sp := NewSpinner(syncWriter{&buf, &mu}, NewTheme(false), true)
	sp.Start("measuring the first check")
	time.Sleep(3 * spinnerInterval)
	sp.SetTitle("measuring the second check")
	time.Sleep(3 * spinnerInterval)
	sp.Stop()

	mu.Lock()
	out := buf.String()
	mu.Unlock()
	if !strings.Contains(out, "measuring the first check") {
		t.Errorf("spinner output missing the first title:\n%q", out)
	}
	if !strings.Contains(out, "measuring the second check") {
		t.Errorf("spinner output missing the second title after SetTitle:\n%q", out)
	}
	if !strings.Contains(out, "\r") {
		t.Errorf("spinner output never redraws in place (no carriage return):\n%q", out)
	}
}

// TestSpinnerStopClearsTheLine proves Stop leaves the cursor's line blank
// (just carriage returns and spaces), not the last frame's text sitting on
// screen.
func TestSpinnerStopClearsTheLine(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	sp := NewSpinner(syncWriter{&buf, &mu}, NewTheme(false), true)
	sp.Start("a check title that is reasonably long")
	time.Sleep(2 * spinnerInterval)
	sp.Stop()

	mu.Lock()
	out := buf.String()
	mu.Unlock()
	last := out[strings.LastIndex(out, "\r"):]
	if strings.TrimSpace(strings.ReplaceAll(last, "\r", "")) != "" {
		t.Errorf("Stop did not clear the line, trailing content: %q", last)
	}
}

// TestSpinnerStopIsIdempotentAndSafeWithoutStart proves Stop never blocks
// or panics when called twice, or when Start was never called at all —
// the "stops cleanly on completion/error" contract callers defer on every
// exit path.
func TestSpinnerStopIsIdempotentAndSafeWithoutStart(t *testing.T) {
	var buf bytes.Buffer
	sp := NewSpinner(&buf, NewTheme(false), true)
	sp.Stop() // never started
	sp.Start("x")
	sp.Stop()
	sp.Stop() // already stopped
}

// TestSpinnerSetTitleFromMultipleGoroutinesIsRaceFree exercises the
// parallel_safe check-group scenario: Progress calls SetTitle from several
// goroutines concurrently. Run under `go test -race`.
func TestSpinnerSetTitleFromMultipleGoroutinesIsRaceFree(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	sp := NewSpinner(syncWriter{&buf, &mu}, NewTheme(false), true)
	sp.Start("start")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			sp.SetTitle(strings.Repeat("x", n+1))
		}(i)
	}
	wg.Wait()
	sp.Stop()
}

// syncWriter guards an underlying writer with a mutex so a test can safely
// read the buffer's contents while the spinner's own goroutine may still
// be writing to it (every write still happens before the asserted read,
// via the preceding Stop()/time.Sleep ordering — the mutex only satisfies
// the race detector's lack of visibility into that ordering).
type syncWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (s syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}
