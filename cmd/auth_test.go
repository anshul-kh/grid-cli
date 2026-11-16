package cmd

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/anshul-kh/grid-cli/internal/api"
	"github.com/anshul-kh/grid-cli/internal/api/stub"
	"github.com/anshul-kh/grid-cli/internal/auth"
	"github.com/anshul-kh/grid-cli/internal/config"
	"github.com/anshul-kh/grid-extensions/pkg/attest"
)

// isolateHome points every home-like env var at a temp directory so the
// commands read/write credentials under the test sandbox, never the
// developer's real config dir, and returns the resulting credentials path.
// The device key is sandboxed the same way: GRID_DEVICE_KEY_FILE forces the
// file store, so grid login/grid submit never touch a real OS keychain.
func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AppData", filepath.Join(home, "AppData"))
	t.Setenv(auth.EnvDeviceKeyFile, filepath.Join(home, "device-key.pem"))
	path, err := config.CredentialsPath()
	if err != nil {
		t.Fatalf("CredentialsPath: %v", err)
	}
	return path
}

// newStubCmdServer serves a fresh stub over real HTTP for command tests.
func newStubCmdServer(t *testing.T) (*stub.Server, *httptest.Server) {
	t.Helper()
	st := stub.New()
	srv := httptest.NewServer(st)
	t.Cleanup(srv.Close)
	return st, srv
}

// setCmdCfg installs the resolved config a running command would see.
func setCmdCfg(apiURL string, jsonMode bool) {
	cfg = config.Config{
		APIURL:  apiURL,
		JSON:    jsonMode,
		Timeout: 5 * time.Second,
	}
}

// captureOutput swaps os.Stdout and os.Stderr for pipes, runs f, and returns
// everything both streams received plus f's error. Captured output is what a
// real user sees, so these are the "grid login never prints the session key"
// assertions.
func captureOutput(t *testing.T, f func() error) (stdout, stderr string, err error) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	outR, outW, err0 := os.Pipe()
	errR, errW, err1 := os.Pipe()
	if err0 != nil || err1 != nil {
		t.Fatalf("pipe: %v / %v", err0, err1)
	}
	os.Stdout, os.Stderr = outW, errW
	func() {
		defer func() {
			if r := recover(); r != nil {
				os.Stdout, os.Stderr = oldOut, oldErr
				outW.Close()
				errW.Close()
				panic(r)
			}
		}()
		err = f()
		outW.Close()
		errW.Close()
	}()
	os.Stdout, os.Stderr = oldOut, oldErr
	outData, _ := io.ReadAll(outR)
	errData, _ := io.ReadAll(errR)
	return string(outData), string(errData), err
}

// exitCodeOf returns the TASK.md §3 code an error would exit with, or 0 for
// a nil error.
func exitCodeOf(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var ee *ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return 1
}

// TestLoginCommandEndToEnd drives grid login's RunE against the stub with a
// zero poll interval: the credentials file appears (0600 on unix), the
// output shows the code, URL, username, and rank, and no output channel ever
// contains the session key.
func TestLoginCommandEndToEnd(t *testing.T) {
	st, srv := newStubCmdServer(t)
	st.SetPollInterval(0)
	credsPath := isolateHome(t)
	setCmdCfg(srv.URL+"/api/v1", false)

	stdout, stderr, err := captureOutput(t, func() error {
		return runLogin(loginCmd, nil)
	})
	if err != nil {
		t.Fatalf("runLogin: %v", err)
	}
	if got := st.PollCount(); got != 3 {
		t.Errorf("PollCount = %d, want 3 (pending, slow_down, success)", got)
	}

	// §5.1/A2: login registered this machine's device public key at
	// device/start — the key that signs every submission envelope.
	key, err := auth.LoadOrCreateDeviceKey()
	if err != nil {
		t.Fatalf("LoadOrCreateDeviceKey after login: %v", err)
	}
	if got, want := st.LastDevicePublicKey(), attest.PublicKeyB64(key.Public().(ed25519.PublicKey)); got != want {
		t.Errorf("device/start registered %q, want the local keypair's public half", got)
	}

	// Output contract: the user code and URL are printed; the username and
	// rank are printed on success.
	for _, want := range []string{stub.UserCode, stub.VerificationURL, "grid-tester", "OPERATOR"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("login stdout missing %q:\n%s", want, stdout)
		}
	}
	// Hard invariant: the session key must never appear in any output.
	if strings.Contains(stdout+stderr, stub.SessionKey) {
		t.Error("login output contains the session key — it must never be printed")
	}

	// Credentials landed at the config path.
	fi, err := os.Stat(credsPath)
	if err != nil {
		t.Fatalf("credentials file missing after login: %v", err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("credentials mode = %o, want 600 (POSIX modes are not asserted on Windows)", fi.Mode().Perm())
	}
	loaded, err := auth.NewStore(credsPath).Load()
	if err != nil {
		t.Fatalf("Load after login: %v", err)
	}
	if loaded.User.Username != "grid-tester" {
		t.Errorf("stored user = %+v", loaded.User)
	}
	if loaded.SessionKey != stub.SessionKey {
		t.Error("stored session key mismatch (values not printed)")
	}
}

// TestLoginCommandJSON emits a machine object on stdout and never the
// session key.
func TestLoginCommandJSON(t *testing.T) {
	st, srv := newStubCmdServer(t)
	st.SetPollInterval(0)
	isolateHome(t)
	setCmdCfg(srv.URL+"/api/v1", true)

	stdout, stderr, err := captureOutput(t, func() error {
		return runLogin(loginCmd, nil)
	})
	if err != nil {
		t.Fatalf("runLogin --json: %v", err)
	}
	if strings.Contains(stdout+stderr, stub.SessionKey) {
		t.Error("login --json output contains the session key — it must never be printed")
	}
	var got loginJSON
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("login --json stdout is not valid JSON: %v\n%s", err, stdout)
	}
	if got.AlreadyLoggedIn || got.User == nil || got.User.Username != "grid-tester" || got.Rank != "OPERATOR" {
		t.Errorf("login --json = %+v, want user grid-tester with rank OPERATOR", got)
	}
}

// TestLoginAlreadyLoggedInIsIdempotent: a stored session short-circuits with
// exit 0 and a clear message.
func TestLoginAlreadyLoggedInIsIdempotent(t *testing.T) {
	_, srv := newStubCmdServer(t)
	credsPath := isolateHome(t)
	setCmdCfg(srv.URL+"/api/v1", false)
	if err := auth.NewStore(credsPath).Save(auth.Credentials{
		SessionKey: "stored-session-key",
		User:       stubTestUser(),
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	stdout, _, err := captureOutput(t, func() error {
		return runLogin(loginCmd, nil)
	})
	if code := exitCodeOf(t, err); code != 0 {
		t.Fatalf("already-logged-in login exit code = %d (err %v), want 0", code, err)
	}
	if !strings.Contains(stdout, "Already logged in as grid-tester") {
		t.Errorf("stdout missing already-logged-in message:\n%s", stdout)
	}
}

// TestLogoutRemovesCredentialsDespiteServerError: DELETE /cli/session
// failing with 500 still exits 0 and removes the local credentials file.
func TestLogoutRemovesCredentialsDespiteServerError(t *testing.T) {
	st, srv := newStubCmdServer(t)
	credsPath := isolateHome(t)
	st.ForceStatus("/cli/session", 500)
	if err := auth.NewStore(credsPath).Save(auth.Credentials{
		SessionKey: stub.SessionKey,
		User:       stubTestUser(),
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	setCmdCfg(srv.URL+"/api/v1", false)

	stdout, _, err := captureOutput(t, func() error {
		return runLogout(logoutCmd, nil)
	})
	if code := exitCodeOf(t, err); code != 0 {
		t.Fatalf("logout with server 500 exit code = %d (err %v), want 0", code, err)
	}
	if _, err := os.Stat(credsPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("credentials file still present after logout (stat err %v)", err)
	}
	if !strings.Contains(stdout, "Logged out locally") || !strings.Contains(stdout, "could not revoke") {
		t.Errorf("stdout must say the local logout succeeded but the server revoke failed:\n%s", stdout)
	}
}

// TestLogoutHappyPath: server revoke succeeds, credentials removed, exit 0.
func TestLogoutHappyPath(t *testing.T) {
	_, srv := newStubCmdServer(t)
	credsPath := isolateHome(t)
	if err := auth.NewStore(credsPath).Save(auth.Credentials{
		SessionKey: stub.SessionKey,
		User:       stubTestUser(),
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	setCmdCfg(srv.URL+"/api/v1", false)

	stdout, _, err := captureOutput(t, func() error {
		return runLogout(logoutCmd, nil)
	})
	if code := exitCodeOf(t, err); code != 0 {
		t.Fatalf("logout exit code = %d (err %v), want 0", code, err)
	}
	if !strings.Contains(stdout, "Logged out.") {
		t.Errorf("stdout = %q, want the logged-out confirmation", stdout)
	}
	if _, err := os.Stat(credsPath); !errors.Is(err, os.ErrNotExist) {
		t.Error("credentials file still present after logout")
	}
}

// TestLogoutWithoutCredentialsIsExit0: logging out while not logged in is a
// local no-op success.
func TestLogoutWithoutCredentialsIsExit0(t *testing.T) {
	_, srv := newStubCmdServer(t)
	isolateHome(t)
	setCmdCfg(srv.URL+"/api/v1", false)

	stdout, _, err := captureOutput(t, func() error {
		return runLogout(logoutCmd, nil)
	})
	if code := exitCodeOf(t, err); code != 0 {
		t.Fatalf("logout without credentials exit code = %d (err %v), want 0", code, err)
	}
	if !strings.Contains(stdout, "Not logged in") {
		t.Errorf("stdout = %q, want the not-logged-in note", stdout)
	}
}

// TestWhoamiExits3WithoutCredentials: no stored session → exit 3, auth
// required.
func TestWhoamiExits3WithoutCredentials(t *testing.T) {
	_, srv := newStubCmdServer(t)
	isolateHome(t)
	setCmdCfg(srv.URL+"/api/v1", false)

	_, _, err := captureOutput(t, func() error {
		return runWhoami(whoamiCmd, nil)
	})
	if code := exitCodeOf(t, err); code != 3 {
		t.Fatalf("whoami without credentials exit code = %d (err %v), want 3", code, err)
	}
}

// TestWhoamiExits3On401: stored credentials the server rejects with 401 map
// to exit 3 (auth required), with the grid login hint.
func TestWhoamiExits3On401(t *testing.T) {
	_, srv := newStubCmdServer(t)
	credsPath := isolateHome(t)
	// A session key the stub does not accept → /cli/me answers 401.
	if err := auth.NewStore(credsPath).Save(auth.Credentials{
		SessionKey: "a-session-key-the-server-rejects",
		User:       stubTestUser(),
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	setCmdCfg(srv.URL+"/api/v1", false)

	_, _, err := captureOutput(t, func() error {
		return runWhoami(whoamiCmd, nil)
	})
	if code := exitCodeOf(t, err); code != 3 {
		t.Fatalf("whoami on 401 exit code = %d (err %v), want 3", code, err)
	}
	// The error's message is what Execute prints to stderr; it must point
	// the user at grid login.
	if !strings.Contains(err.Error(), "grid login") {
		t.Errorf("whoami 401 error should suggest grid login, got: %s", err.Error())
	}
}

// TestWhoamiHappyPath: valid credentials render username, display name,
// rank, XP, and streak from the confirmed flat /cli/me shape; --json emits
// the raw /cli/me object with the flat keys.
func TestWhoamiHappyPath(t *testing.T) {
	_, srv := newStubCmdServer(t)
	credsPath := isolateHome(t)
	if err := auth.NewStore(credsPath).Save(auth.Credentials{
		SessionKey: stub.SessionKey,
		User:       stubTestUser(),
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	setCmdCfg(srv.URL+"/api/v1", false)

	stdout, _, err := captureOutput(t, func() error {
		return runWhoami(whoamiCmd, nil)
	})
	if err != nil {
		t.Fatalf("runWhoami: %v", err)
	}
	for _, want := range []string{"Grid Tester", "grid-tester", "OPERATOR", "1284 XP", "4-day streak"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("whoami stdout missing %q:\n%s", want, stdout)
		}
	}

	setCmdCfg(srv.URL+"/api/v1", true)
	stdout, _, err = captureOutput(t, func() error {
		return runWhoami(whoamiCmd, nil)
	})
	if err != nil {
		t.Fatalf("runWhoami --json: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
		t.Fatalf("whoami --json stdout is not valid JSON: %v\n%s", err, stdout)
	}
	// The confirmed flat /cli/me keys (GRID_SPEC §5.4b) — no nested `user`,
	// no `streak`.
	for _, key := range []string{"id", "username", "displayName", "rank", "xp", "currentStreak", "longestStreak"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("whoami --json missing flat key %q: %s", key, stdout)
		}
	}
	for _, stale := range []string{"user", "streak"} {
		if _, ok := raw[stale]; ok {
			t.Errorf("whoami --json must not carry the old fabricated key %q: %s", stale, stdout)
		}
	}
	if raw["username"] != "grid-tester" || raw["currentStreak"] != float64(4) {
		t.Errorf("whoami --json flat values not decoded correctly: %s", stdout)
	}
	if strings.Contains(stdout, stub.SessionKey) {
		t.Error("whoami --json output contains the session key")
	}
}

// stubTestUser mirrors the stub's paired user.
func stubTestUser() api.User {
	return api.User{
		ID:          "0d0a1f2e-5c4b-4a3d-9e8f-7a6b5c4d3e2f",
		Username:    "grid-tester",
		DisplayName: "Grid Tester",
		AvatarURL:   "https://grid.dev/img/tester.png",
	}
}
