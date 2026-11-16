package extensions

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/anshul-kh/grid-extensions/pkg/attest"
	"github.com/anshul-kh/grid-extensions/pkg/extcommon"
)

// ipcHelperMarker gates TestMain's helper branch: only a self-exec
// invocation carrying this marker runs an extcommon harness — the
// services_test.go self-exec pattern, so these tests need no real
// extension binary and no sh/cmd dependency.
const ipcHelperMarker = "__grid_ext_ipc_helper__"

// ipcHelperKeyEnv carries the base64-of-PEM test key into the helper, so
// the helper's signed responses verify with the key the test holds.
const ipcHelperKeyEnv = "GRID_EXT_IPC_HELPER_KEY_B64"

// ipcOrphanPidEnv points the orphan-spawning helper at its pid file.
const ipcOrphanPidEnv = "GRID_EXT_IPC_ORPHAN_PIDFILE"

func TestMain(m *testing.M) {
	for i, a := range os.Args {
		if a != ipcHelperMarker || i+1 >= len(os.Args) {
			continue
		}
		os.Exit(runIPCHelper(os.Args[i+1]))
	}
	os.Exit(m.Run())
}

// runIPCHelper is the spawned process: a real extcommon harness main —
// dispatch, self-hash, signed telemetry — over a tiny action set.
func runIPCHelper(mode string) int {
	pem, err := base64.StdEncoding.DecodeString(os.Getenv(ipcHelperKeyEnv))
	if err != nil {
		os.Stderr.WriteString("helper: bad key env: " + err.Error() + "\n")
		return 2
	}
	signer, err := extcommon.NewSigner(pem)
	if err != nil {
		os.Stderr.WriteString("helper: bad signer: " + err.Error() + "\n")
		return 2
	}
	if mode == "orphan" {
		if err := spawnIgnoredOrphan(); err != nil {
			os.Stderr.WriteString("helper: spawn orphan: " + err.Error() + "\n")
			return 2
		}
	}
	// The harness runs until stdin hits EOF. "hang" (and the post-orphan
	// case) never answers: the caller's deadline must be what ends this
	// process — and its whole process group.
	actions := map[string]extcommon.Handler{
		"echo": func(ctx context.Context, req *extcommon.Request) (map[string]any, error) {
			return map[string]any{
				"echoed": req.Params["x"],
				"nonce":  req.AttemptNonce,
			}, nil
		},
		"boom": func(ctx context.Context, req *extcommon.Request) (map[string]any, error) {
			return nil, errors.New("action exploded")
		},
		"hang": func(ctx context.Context, req *extcommon.Request) (map[string]any, error) {
			time.Sleep(time.Hour) // outlives any test deadline
			return nil, nil
		},
	}
	if err := extcommon.Run(signer, actions, extcommon.Options{}); err != nil {
		os.Stderr.WriteString("helper: harness: " + err.Error() + "\n")
		return 1
	}
	return 0
}

// helperKeyEnv installs the test key for the spawned helper and returns
// the argv suffix that turns a re-exec of this test binary into an
// extcommon harness subprocess.
func helperKeyEnv(t *testing.T, key ed25519.PrivateKey) []string {
	t.Helper()
	pem, err := attest.EncodePrivateKeyPEM(key)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(ipcHelperKeyEnv, base64.StdEncoding.EncodeToString(pem))
	return []string{"-test.run=TestMain", ipcHelperMarker}
}

func callHelper(t *testing.T, key ed25519.PrivateKey, mode, action string, timeout time.Duration) (*extcommon.Response, error) {
	t.Helper()
	req := &extcommon.Request{
		AttemptID:    "att_test",
		AttemptNonce: "nonce-abc123",
		CheckID:      "load_1k_rps",
		Action:       action,
		Params:       map[string]any{"x": "hello"},
	}
	return Call(context.Background(), os.Args[0], req, timeout, append(helperKeyEnv(t, key), mode)...)
}

func TestIPCRoundTripSignedResponse(t *testing.T) {
	key := attest.DeterministicTestKey()
	resp, err := callHelper(t, key, "echo", "echo", 10*time.Second)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if resp.CheckID != "load_1k_rps" {
		t.Errorf("resp.CheckID = %q, want the request's checkId echoed", resp.CheckID)
	}
	if got := resp.Telemetry["nonce"]; got != "nonce-abc123" {
		t.Errorf("telemetry.nonce = %v, want the attemptNonce plumbed through", got)
	}

	// The signed response must verify on the CLI side over the A4 ext
	// layout, and the self-hash rides in the sha256:… form.
	pub := key.Public().(ed25519.PublicKey)
	if !attest.VerifyExtSignature(pub, "att_test", "nonce-abc123", "load_1k_rps", resp.ExtBinaryHash, resp.Telemetry, resp.ExtSignature) {
		t.Error("VerifyExtSignature rejected the helper's signed response")
	}
	if !strings.HasPrefix(resp.ExtBinaryHash, "sha256:") {
		t.Errorf("ExtBinaryHash = %q, want the sha256:… self-hash form", resp.ExtBinaryHash)
	}
}

func TestIPCErrorActionSurfacesMessage(t *testing.T) {
	key := attest.DeterministicTestKey()
	_, err := callHelper(t, key, "echo", "boom", 10*time.Second)
	if err == nil || !strings.Contains(err.Error(), "action exploded") {
		t.Fatalf("Call error = %v, want the extension's action error surfaced", err)
	}
}

func TestIPCUnknownAction(t *testing.T) {
	key := attest.DeterministicTestKey()
	_, err := callHelper(t, key, "echo", "no_such_action", 10*time.Second)
	if err == nil || !strings.Contains(err.Error(), "unknown action") {
		t.Fatalf("Call error = %v, want the harness's unknown-action error", err)
	}
}

func TestIPCTimesOutHonouringDeadline(t *testing.T) {
	key := attest.DeterministicTestKey()
	start := time.Now()
	_, err := callHelper(t, key, "echo", "hang", 300*time.Millisecond)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("Call error = %v, want ErrTimeout", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Call took %s, want the 300ms deadline honoured", elapsed)
	}
}
