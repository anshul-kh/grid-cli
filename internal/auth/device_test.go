package auth

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anshul-kh/grid-cli/internal/api"
	"github.com/anshul-kh/grid-cli/internal/api/stub"
	"github.com/anshul-kh/grid-extensions/pkg/attest"
)

// sleeper records the poll waits instead of sleeping, so tests observe the
// interval behaviour (including the slow_down doubling) instantly.
type sleeper struct {
	mu    sync.Mutex
	waits []time.Duration
}

func (s *sleeper) sleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	s.waits = append(s.waits, d)
	s.mu.Unlock()
	return nil
}

func (s *sleeper) recorded() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.waits...)
}

// newStubFlow wires a Flow to a fresh stub served over real HTTP and returns
// everything a test needs to drive it. The flow registers a device public
// key (§5.1: the stub's device/start requires one).
func newStubFlow(t *testing.T, opts ...FlowOption) (*stub.Server, *Flow, *bytes.Buffer, *bytes.Buffer, *sleeper) {
	t.Helper()
	st := stub.New()
	srv := httptest.NewServer(st)
	t.Cleanup(srv.Close)
	client, err := api.New(srv.URL + "/api/v1")
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	sl := &sleeper{}
	devicePublicKey := attest.PublicKeyB64(attest.DeterministicTestKey().Public().(ed25519.PublicKey))
	all := append([]FlowOption{WithSleeper(sl.sleep), WithDevicePublicKey(devicePublicKey)}, opts...)
	flow := NewFlow(client, out, errOut, all...)
	return st, flow, out, errOut, sl
}

// stubStart mirrors what the stub's /cli/device/start returns, so tests can
// drive Poll directly with a controlled interval/expiry.
func stubStart(intervalSeconds, expiresInSeconds int) api.DeviceStartResponse {
	return api.DeviceStartResponse{
		DeviceCode:       stub.DeviceCode,
		UserCode:         stub.UserCode,
		VerificationURL:  stub.VerificationURL,
		ExpiresInSeconds: expiresInSeconds,
		IntervalSeconds:  intervalSeconds,
	}
}

// TestLoginPollsPendingSlowDownSuccess proves the full §5.1 sequence through
// the stub — start, prompt, pending, slow_down (interval doubled), success —
// and that the pairing output never leaks the session key.
func TestLoginPollsPendingSlowDownSuccess(t *testing.T) {
	var mu sync.Mutex
	var opened []string
	st, flow, out, errOut, sl := newStubFlow(t, WithBrowserOpener(func(ctx context.Context, url string) error {
		mu.Lock()
		opened = append(opened, url)
		mu.Unlock()
		return nil
	}))

	creds, err := flow.Login(context.Background())
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	// The stub's default queue is pending → slow_down → success with a
	// 3-second interval: the flow must have waited interval, then the
	// doubled interval after slow_down.
	if got, want := sl.recorded(), []time.Duration{3 * time.Second, 6 * time.Second}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("poll waits = %v, want %v (interval must double after slow_down)", got, want)
	}
	if got := st.PollCount(); got != 3 {
		t.Errorf("PollCount = %d, want 3", got)
	}
	if creds.User.Username != "grid-tester" || creds.User.DisplayName != "Grid Tester" {
		t.Errorf("paired user not decoded: %+v", creds.User)
	}
	if creds.SessionKey != stub.SessionKey {
		t.Error("session key mismatch after pairing (values not printed)")
	}
	if creds.CreatedAt.IsZero() {
		t.Error("CreatedAt not set on fresh credentials")
	}

	// The prompt must show the user code and the verification URL.
	combined := out.String() + errOut.String()
	if !strings.Contains(combined, stub.UserCode) {
		t.Errorf("pairing output does not contain the user code:\n%s", out.String())
	}
	if !strings.Contains(combined, stub.VerificationURL) {
		t.Errorf("pairing output does not contain the verification URL:\n%s", out.String())
	}
	// Hard invariant: the session key never appears in any output channel.
	if strings.Contains(combined, stub.SessionKey) {
		t.Error("pairing output contains the session key — it must never be printed")
	}

	mu.Lock()
	if len(opened) != 1 || opened[0] != stub.VerificationURL {
		t.Errorf("browser opener calls = %v, want [%s]", opened, stub.VerificationURL)
	}
	mu.Unlock()
}

// TestPollExpiredIsTerminal: a 410 expired response ends the flow with
// api.ErrAttemptExpired and no further waits.
func TestPollExpiredIsTerminal(t *testing.T) {
	st, flow, _, _, sl := newStubFlow(t)
	st.SetPollStates([]string{"expired"})

	_, err := flow.Poll(context.Background(), stubStart(3, 600))
	if !errors.Is(err, api.ErrAttemptExpired) {
		t.Fatalf("Poll error = %v, want api.ErrAttemptExpired", err)
	}
	if got := st.PollCount(); got != 1 {
		t.Errorf("PollCount = %d, want 1", got)
	}
	if waits := sl.recorded(); len(waits) != 0 {
		t.Errorf("waits after terminal expired = %v, want none", waits)
	}
}

// TestPollDeniedIsTerminal: a 403 denied response ends the flow with
// api.ErrDenied.
func TestPollDeniedIsTerminal(t *testing.T) {
	st, flow, _, _, _ := newStubFlow(t)
	st.SetPollStates([]string{"denied"})

	_, err := flow.Poll(context.Background(), stubStart(3, 600))
	if !errors.Is(err, api.ErrDenied) {
		t.Fatalf("Poll error = %v, want api.ErrDenied", err)
	}
	if got := st.PollCount(); got != 1 {
		t.Errorf("PollCount = %d, want 1", got)
	}
}

// TestPollStopsWhenWindowElapses: pending forever inside a window too short
// for another wait ends the flow with ErrLoginExpired (the "poll never
// succeeds within expiresIn" path) instead of polling forever.
func TestPollStopsWhenWindowElapses(t *testing.T) {
	st, flow, _, _, sl := newStubFlow(t)
	st.SetPollStates([]string{"pending"})

	// Interval 5s inside a 4s window: the first poll is pending, but the next
	// wait would outlive the window.
	_, err := flow.Poll(context.Background(), stubStart(5, 4))
	if !errors.Is(err, ErrLoginExpired) {
		t.Fatalf("Poll error = %v, want ErrLoginExpired", err)
	}
	if got := st.PollCount(); got != 1 {
		t.Errorf("PollCount = %d, want 1", got)
	}
	if waits := sl.recorded(); len(waits) != 0 {
		t.Errorf("waits = %v, want none (the wait would have exceeded the window)", waits)
	}
}

// TestPollWindowElapsesDuringWaits: the deadline is also re-checked as
// waits elapse, with an injected clock, so a pending server cannot keep the
// flow alive past expiresIn.
func TestPollWindowElapsesDuringWaits(t *testing.T) {
	st, flow, _, _, sl := newStubFlow(t)
	st.SetPollStates([]string{"pending"})

	// The clock advances 2s per call: after two 2s waits the top-of-loop
	// deadline check trips and the flow stops with ErrLoginExpired.
	call := 0
	flow.now = func() time.Time {
		base := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
		call++
		return base.Add(time.Duration(call) * 2 * time.Second)
	}
	_, err := flow.Poll(context.Background(), stubStart(2, 10))
	if !errors.Is(err, ErrLoginExpired) {
		t.Fatalf("Poll error = %v, want ErrLoginExpired", err)
	}
	if got := st.PollCount(); got != 2 {
		t.Errorf("PollCount = %d, want 2", got)
	}
	if got := sl.recorded(); len(got) != 2 || got[0] != 2*time.Second || got[1] != 2*time.Second {
		t.Errorf("waits = %v, want [2s 2s]", got)
	}
}

// TestBrowserOpenFailureNeverFailsLogin: when no browser can be opened the
// login still succeeds and the fallback message points at the printed URL.
func TestBrowserOpenFailureNeverFailsLogin(t *testing.T) {
	_, flow, out, errOut, _ := newStubFlow(t, WithBrowserOpener(func(ctx context.Context, url string) error {
		return errors.New("no browser available")
	}))

	creds, err := flow.Login(context.Background())
	if err != nil {
		t.Fatalf("Login with failing browser open: %v (must never fail the command)", err)
	}
	if creds.User.Username != "grid-tester" {
		t.Errorf("paired user = %+v", creds.User)
	}
	if !strings.Contains(errOut.String(), "Could not open a browser") {
		t.Errorf("stderr missing browser fallback message:\n%s", errOut.String())
	}
	if !strings.Contains(out.String(), stub.VerificationURL) {
		t.Errorf("stdout missing the verification URL fallback:\n%s", out.String())
	}
}

// TestPollSlowDownDoublesOncePerState: two consecutive slow_down responses
// double the interval each time (3s → 6s → 12s) before success.
func TestPollSlowDownDoublesOncePerState(t *testing.T) {
	st, flow, _, _, sl := newStubFlow(t)
	st.SetPollStates([]string{"pending", "slow_down", "slow_down", "success"})

	if _, err := flow.Poll(context.Background(), stubStart(3, 600)); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	got := sl.recorded()
	want := []time.Duration{3 * time.Second, 6 * time.Second, 12 * time.Second}
	if len(got) != len(want) {
		t.Fatalf("waits = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("waits = %v, want %v", got, want)
		}
	}
	if got := st.PollCount(); got != 4 {
		t.Errorf("PollCount = %d, want 4", got)
	}
}
