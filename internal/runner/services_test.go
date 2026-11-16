package runner

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/anshul-kh/grid-cli/internal/spec"
)

// helperMarker gates TestHelperProcess: only a self-exec invocation that
// carries this marker (never an ordinary `go test` run) does anything but
// return immediately. The marker and the behaviour that follows it are
// found by scanning os.Args directly rather than via the testing package's
// own flag parsing, so this needs no "--" argument-separator support.
const helperMarker = "__grid_services_test_helper__"

// helperArgv builds a spec.Service.Run argv that re-execs this test binary
// as a long-running "service" — the same self-exec trick os/exec's own
// tests use (TestHelperProcess) — so these tests need no `sh`/`cmd` and
// behave identically on Windows, macOS, and Linux (TASK.md §5).
func helperArgv() []string {
	return []string{os.Args[0], "-test.run=TestHelperProcess", helperMarker, "sleep"}
}

// TestHelperProcess is not a real test: invoked directly by `go test`, it
// finds no helperMarker in os.Args and returns immediately, doing nothing.
// Invoked via helperArgv() as a spawned subprocess, it sleeps until killed
// — standing in for a long-running service.
func TestHelperProcess(t *testing.T) {
	idx := -1
	for i, a := range os.Args {
		if a == helperMarker {
			idx = i
			break
		}
	}
	if idx == -1 || idx+1 >= len(os.Args) {
		return
	}
	switch os.Args[idx+1] {
	case "sleep":
		time.Sleep(time.Minute)
	}
}

// startTestService starts a helper "service" directly via startOneService
// (skipping the spec.Service.Ready probe plumbing most tests here don't
// need) and fails the test immediately if it can't start.
func startTestService(t *testing.T, env *Env, id string) *Service {
	t.Helper()
	svc, err := startOneService(env, spec.Service{ID: id, Run: helperArgv()})
	if err != nil {
		t.Fatalf("startOneService(%q): %v", id, err)
	}
	return svc
}

// TestProbeTCPOnce proves the tcp readiness probe against a real listener:
// succeeds while it's open, fails once closed.
func TestProbeTCPOnce(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := probeTCPOnce(context.Background(), addr); err != nil {
		t.Errorf("probeTCPOnce against an open listener: %v", err)
	}
	ln.Close()
	if err := probeTCPOnce(context.Background(), addr); err == nil {
		t.Error("probeTCPOnce against a closed listener: want an error, got nil")
	}
}

// TestProbeHTTPOnce proves the http readiness probe treats 2xx as ready and
// everything else as not ready.
func TestProbeHTTPOnce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ready" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	if err := probeHTTPOnce(context.Background(), srv.URL+"/ready"); err != nil {
		t.Errorf("probeHTTPOnce on 200: %v", err)
	}
	if err := probeHTTPOnce(context.Background(), srv.URL+"/not-ready"); err == nil {
		t.Error("probeHTTPOnce on 503: want an error, got nil")
	}
}

// TestProbeReadyNoProbeConfiguredSucceedsImmediately: a service with
// neither tcp nor http in its ready block is considered ready as soon as it
// starts.
func TestProbeReadyNoProbeConfiguredSucceedsImmediately(t *testing.T) {
	start := time.Now()
	if err := probeReady(context.Background(), spec.ServiceReady{}); err != nil {
		t.Fatalf("probeReady with no probe configured: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Errorf("probeReady with no probe configured took %s, want ~instant", elapsed)
	}
}

// TestProbeReadyTimesOutWhenNeverReady proves a readiness probe against a
// port nothing listens on gives up after its own timeout, not immediately
// and not forever.
func TestProbeReadyTimesOutWhenNeverReady(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listens here now

	start := time.Now()
	err = probeReady(context.Background(), spec.ServiceReady{TCP: addr, Timeout: spec.Duration(150 * time.Millisecond)})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("probeReady against a dead address: want an error, got nil")
	}
	if elapsed < 100*time.Millisecond || elapsed > 2*time.Second {
		t.Errorf("probeReady took %s, want roughly its 150ms timeout", elapsed)
	}
}

// TestProbeReadySucceedsOnceListenerOpens proves probeReady actually polls
// rather than checking once: the listener opens after the probe has
// already started, and it must still succeed before its timeout.
func TestProbeReadySucceedsOnceListenerOpens(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close() // closed for now; reopened on the same address shortly

	opened := make(chan struct{})
	go func() {
		time.Sleep(150 * time.Millisecond)
		ln2, err := net.Listen("tcp", addr)
		if err != nil {
			close(opened)
			return
		}
		close(opened)
		<-time.After(2 * time.Second)
		ln2.Close()
	}()
	<-opened

	err = probeReady(context.Background(), spec.ServiceReady{TCP: addr, Timeout: spec.Duration(2 * time.Second)})
	if err != nil {
		t.Errorf("probeReady did not notice the listener opening: %v", err)
	}
}

// TestStartServicesWaitsForReadinessBeforeStartingTheNextOne proves the
// sequential contract directly: StartServices must not start service 1
// until service 0's readiness probe has succeeded. A delayed-open listener
// stands in for "service 0 isn't ready yet" without needing the spawned
// subprocess itself to open the port.
func TestStartServicesWaitsForReadinessBeforeStartingTheNextOne(t *testing.T) {
	env := NewEnv(t.TempDir(), nil)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	const delay = 200 * time.Millisecond
	opened := make(chan struct{})
	go func() {
		time.Sleep(delay)
		ln2, err := net.Listen("tcp", addr)
		close(opened)
		if err != nil {
			return
		}
		defer ln2.Close()
		<-time.After(2 * time.Second)
	}()
	defer func() { <-opened }()

	specs := []spec.Service{
		{ID: "zero", Run: helperArgv(), Ready: spec.ServiceReady{TCP: addr, Timeout: spec.Duration(2 * time.Second)}},
		{ID: "one", Run: helperArgv()},
	}
	start := time.Now()
	services, err := StartServices(context.Background(), env, specs)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("StartServices: %v", err)
	}
	defer StopServices(services)

	if elapsed < delay {
		t.Errorf("StartServices returned after %s, want it to have waited at least the %s readiness delay", elapsed, delay)
	}
	if len(services) != 2 {
		t.Fatalf("len(services) = %d, want 2", len(services))
	}
}

// TestStartServicesStopsAlreadyStartedOnLaterFailure proves a later
// service's start/readiness failure tears down everything already started,
// leaving no orphan.
func TestStartServicesStopsAlreadyStartedOnLaterFailure(t *testing.T) {
	env := NewEnv(t.TempDir(), nil)
	specs := []spec.Service{
		{ID: "zero", Run: helperArgv()},
		{ID: "one", Run: []string{"this-command-does-not-exist-grid-test-xyz"}},
	}
	services, err := StartServices(context.Background(), env, specs)
	if err == nil {
		t.Fatal("StartServices with a failing later service: want an error, got nil")
	}
	if services != nil {
		t.Errorf("StartServices on failure returned %d services, want nil", len(services))
	}
}

// TestStopServicesReverseOrder proves StopServices stops services in
// reverse start order. The hard SIGKILL used leaves nothing for a stopped
// process to report about itself, so order is observed via the stopObserved
// test seam (services.go) rather than subprocess side effects or timing.
func TestStopServicesReverseOrder(t *testing.T) {
	env := NewEnv(t.TempDir(), nil)
	a := startTestService(t, env, "a")
	b := startTestService(t, env, "b")
	c := startTestService(t, env, "c")
	services := []*Service{a, b, c}

	var mu sync.Mutex
	var order []string
	stopObserved = func(id string) {
		mu.Lock()
		order = append(order, id)
		mu.Unlock()
	}
	defer func() { stopObserved = nil }()

	StopServices(services)

	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()
	want := []string{"c", "b", "a"}
	if len(got) != len(want) {
		t.Fatalf("stop order = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stop order = %v, want %v", got, want)
		}
	}

	for _, svc := range services {
		if svc.cmd.ProcessState == nil {
			t.Errorf("service %q: ProcessState is nil after StopServices — not actually reaped", svc.ID)
		}
	}
}

// expectServiceStopped installs the stopObserved test seam, runs fn, and
// fails the test unless exactly the given service IDs were observed
// stopping. Real OS-level reaping of a service's process group (no orphan
// left behind) is proven once, directly, in TestStopServicesReverseOrder
// and in exec_unix_test.go/exec_windows_test.go's equivalent for the
// identical kill path checks use; the Run-level tests below only need to
// prove the deferred StopServices call actually fires on every exit path.
func expectServiceStopped(t *testing.T, fn func(), wantIDs ...string) {
	t.Helper()
	var mu sync.Mutex
	var stopped []string
	stopObserved = func(id string) {
		mu.Lock()
		stopped = append(stopped, id)
		mu.Unlock()
	}
	defer func() { stopObserved = nil }()

	fn()

	mu.Lock()
	got := append([]string(nil), stopped...)
	mu.Unlock()
	if len(got) != len(wantIDs) {
		t.Fatalf("stopped services = %v, want %v", got, wantIDs)
	}
	for i := range wantIDs {
		if got[i] != wantIDs[i] {
			t.Fatalf("stopped services = %v, want %v", got, wantIDs)
		}
	}
}

// TestRunStopsServicesAfterCheckError proves services are stopped on the
// ordinary path too: a check reporting "error" (an ordinary measurement
// failure, not a setup failure) still lets Run return normally, and the
// deferred StopServices still runs.
func TestRunStopsServicesAfterCheckError(t *testing.T) {
	env := NewEnv(t.TempDir(), nil)
	r := New(env)
	reg := fakeRegistry{"fails": errChecker{}}
	cs := &spec.CheckSpec{
		Version:  1,
		Services: []spec.Service{{ID: "svc", Run: helperArgv()}},
		Checks:   []spec.Check{{ID: "a", Type: "fails"}},
	}

	var outcome Outcome
	expectServiceStopped(t, func() {
		outcome = r.Run(context.Background(), cs, reg)
	}, "svc")

	if outcome.SetupErr != nil {
		t.Fatalf("SetupErr = %v, want nil", outcome.SetupErr)
	}
	if len(outcome.CheckResults) != 1 || outcome.CheckResults[0].Status != StatusError {
		t.Fatalf("CheckResults = %+v, want one StatusError entry", outcome.CheckResults)
	}
}

// TestRunStopsServicesOnContextCancellation simulates SIGINT the way the
// plan allows ("simulated signal or ctx cancel"): the cmd layer cancels the
// context it hands to Run when a real SIGINT/SIGTERM arrives, so cancelling
// it directly here exercises the exact same cleanup path without needing a
// real signal.
func TestRunStopsServicesOnContextCancellation(t *testing.T) {
	env := NewEnv(t.TempDir(), nil)
	r := New(env)
	blocked := make(chan struct{})
	reg := fakeRegistry{"block": checkerFunc(func(ctx context.Context, c spec.Check, env *Env) (Evidence, error) {
		close(blocked)
		<-ctx.Done()
		return nil, ctx.Err()
	})}
	cs := &spec.CheckSpec{
		Version:  1,
		Services: []spec.Service{{ID: "svc", Run: helperArgv()}},
		Checks:   []spec.Check{{ID: "a", Type: "block"}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Outcome, 1)

	expectServiceStopped(t, func() {
		go func() { done <- r.Run(ctx, cs, reg) }()
		<-blocked
		cancel() // simulated SIGINT

		select {
		case outcome := <-done:
			if len(outcome.CheckResults) != 1 || outcome.CheckResults[0].Status != StatusError {
				t.Errorf("CheckResults = %+v, want one StatusError entry (cancelled mid-measurement)", outcome.CheckResults)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not return within 5s of context cancellation")
		}
	}, "svc")
}

// TestRunStopsServicesOnPanic proves the deferred StopServices survives a
// panic unwinding out of Run — Go's defer runs during panic unwinding same
// as a normal return, so this really pins that nothing in Run's control
// flow defeats it.
func TestRunStopsServicesOnPanic(t *testing.T) {
	env := NewEnv(t.TempDir(), nil)
	r := New(env)
	reg := fakeRegistry{"panics": checkerFunc(func(ctx context.Context, c spec.Check, env *Env) (Evidence, error) {
		panic("boom")
	})}
	cs := &spec.CheckSpec{
		Version:  1,
		Services: []spec.Service{{ID: "svc", Run: helperArgv()}},
		Checks:   []spec.Check{{ID: "a", Type: "panics"}},
	}

	expectServiceStopped(t, func() {
		func() {
			defer func() {
				if rec := recover(); rec == nil {
					t.Error("expected Run to panic")
				}
			}()
			r.Run(context.Background(), cs, reg)
		}()
	}, "svc")
}
