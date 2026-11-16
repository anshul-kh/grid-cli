package checks

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/anshul-kh/grid-cli/internal/extensions"
	"github.com/anshul-kh/grid-cli/internal/runner"
	"github.com/anshul-kh/grid-cli/internal/spec"
	"github.com/anshul-kh/grid-extensions/pkg/attest"
)

// extSpec builds a minimal spec pinning loadtest@version with one ext check.
func extSpec(version string) *spec.CheckSpec {
	return &spec.CheckSpec{
		Version: 1,
		Extensions: []spec.ExtensionPin{
			{Name: "loadtest", Version: version},
			{Name: "other", Version: "1.0.0"},
		},
	}
}

// TestNewExtFactoryResolvesPins proves the ext. prefix factory resolves
// name/version/action per type and refuses a type naming an unpinned
// extension (§4/A1: the name must match a top-level pin).
func TestNewExtFactoryResolvesPins(t *testing.T) {
	f := NewExtFactory(extSpec("0.1.0"), nil, "att", "nonce")

	c, err := f("ext.loadtest.http_benchmark")
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	ext, ok := c.(*extChecker)
	if !ok {
		t.Fatalf("factory returned %T, want *extChecker", c)
	}
	if ext.name != "loadtest" || ext.version != "0.1.0" || ext.action != "http_benchmark" {
		t.Errorf("resolved %s@%s action %s, want loadtest@0.1.0/http_benchmark", ext.name, ext.version, ext.action)
	}
	if ext.attemptNonce != "nonce" || ext.attemptID != "att" {
		t.Errorf("attempt identity not carried: %q/%q", ext.attemptID, ext.attemptNonce)
	}

	if _, err := f("ext.unpinned.action"); err == nil {
		t.Error("unpinned extension: want an error, got nil")
	}
	if _, err := f("file.exists"); err == nil {
		t.Error("builtin type through the ext factory: want an error, got nil")
	}
}

// TestExtCheckerEnsureFailureIsMeasurementError proves a manager failure
// (here: a mirror binary whose signature is garbage) surfaces as a
// measurement error naming the extension — never as evidence of anything.
func TestExtCheckerEnsureFailureIsMeasurementError(t *testing.T) {
	mirror := t.TempDir()
	dir := filepath.Join(mirror, "grid-ext-loadtest", "0.1.0", runtime.GOOS+"-"+runtime.GOARCH)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, extensions.BinaryName("loadtest")), []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, extensions.BinaryName("loadtest")+".sig"), []byte("not a signature"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(extensions.EnvMirror, mirror)
	mgr := extensions.NewManagerWithCache(t.TempDir())
	f := NewExtFactory(extSpec("0.1.0"), mgr, "att", "nonce")
	c, err := f("ext.loadtest.http_benchmark")
	if err != nil {
		t.Fatal(err)
	}
	env := runner.NewEnv(t.TempDir(), nil)
	_, err = c.Measure(context.Background(), spec.Check{ID: "x", Type: "ext.loadtest.http_benchmark"}, env)
	if err == nil || !errors.Is(err, extensions.ErrBadSignature) {
		t.Fatalf("Measure error = %v, want the ErrBadSignature chain", err)
	}
}

// fakeExtChecker is the minimal ExtResultSource: it exists to prove
// runner.MeasureCheck picks the ext result up and leaves evidence empty —
// the §5.3 result-kind split — without any subprocess.
type fakeExtChecker struct{ res *runner.ExtResult }

func (f fakeExtChecker) Measure(context.Context, spec.Check, *runner.Env) (runner.Evidence, error) {
	return nil, nil
}
func (f fakeExtChecker) ExtResult() *runner.ExtResult { return f.res }

type fakeRegistry struct{ c Checker }

func (r fakeRegistry) Lookup(string) (Checker, error) { return r.c, nil }

func TestMeasureCheckCarriesExtResult(t *testing.T) {
	want := &runner.ExtResult{
		Extension:     "loadtest@0.1.0",
		ExtBinaryHash: "sha256:abc",
		Telemetry:     map[string]any{"actualRps": 1000.0},
		ExtSignature:  "base64:…",
	}
	env := runner.NewEnv(t.TempDir(), nil)
	res := runner.New(env).MeasureCheck(context.Background(),
		spec.Check{ID: "load", Type: "ext.loadtest.http_benchmark"}, fakeRegistry{c: fakeExtChecker{res: want}})
	if res.Status != runner.StatusOK {
		t.Fatalf("status = %s, want ok", res.Status)
	}
	if res.Evidence != nil {
		t.Errorf("evidence = %v, want nil (ext results carry telemetry, not evidence)", res.Evidence)
	}
	if res.Ext != want {
		t.Errorf("res.Ext = %+v, want the checker's result", res.Ext)
	}
}

// TestExtCheckerAgainstRealDistBinary is the live half: the real signed
// grid-ext-loadtest from grid-extensions' dist/ (Cluster M artifacts)
// drives an actual IPC benchmark against a local test server through the
// whole manager → verify → spawn → verify-signature path, with the
// embedded release key checking the response. Skipped where dist/ was not
// built.
func TestExtCheckerAgainstRealDistBinary(t *testing.T) {
	distRoot, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "grid-extensions", "dist"))
	if err != nil {
		t.Fatal(err)
	}
	binRel := filepath.Join("grid-ext-loadtest", "0.1.0", runtime.GOOS+"-"+runtime.GOARCH, extensions.BinaryName("loadtest"))
	if _, err := os.Stat(filepath.Join(distRoot, binRel)); err != nil {
		t.Skipf("no signed dist binary (run make build && make sign in grid-extensions): %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Setenv(extensions.EnvMirror, distRoot)
	mgr := extensions.NewManagerWithCache(t.TempDir())
	f := NewExtFactory(extSpec("0.1.0"), mgr, "att_live", "nonce-live-1234")
	env := runner.NewEnv(t.TempDir(), nil)
	env.DefaultTimeout = 30 * time.Second
	reg := factoryRegistry{f: f, types: map[string]bool{"ext.loadtest.http_benchmark": true}}
	res := runner.New(env).MeasureCheck(context.Background(), spec.Check{
		ID:      "load",
		Type:    "ext.loadtest.http_benchmark",
		Timeout: spec.Duration(30 * time.Second), // measured: ~1s benchmark + margin, well inside
		Params: map[string]any{
			"targetUrl":       srv.URL,
			"targetRps":       20,
			"durationSeconds": 1,
		},
	}, reg)
	if res.Status != runner.StatusOK {
		t.Fatalf("status = %s err = %v", res.Status, res.Err)
	}
	if res.Evidence != nil {
		t.Errorf("evidence = %v, want nil", res.Evidence)
	}
	if res.Ext == nil {
		t.Fatal("res.Ext = nil, want the ext result")
	}
	if res.Ext.Extension != "loadtest@0.1.0" {
		t.Errorf("Extension = %q, want loadtest@0.1.0", res.Ext.Extension)
	}
	if res.Ext.ExtBinaryHash != attest.SHA256Prefix(mustRead(t, filepath.Join(distRoot, binRel))) {
		t.Errorf("ExtBinaryHash = %q, want the hash of the dist binary that ran", res.Ext.ExtBinaryHash)
	}
	if !attest.VerifyExtSignature(extensions.ReleasePub(), "att_live", "nonce-live-1234", "load", res.Ext.ExtBinaryHash, res.Ext.Telemetry, res.Ext.ExtSignature) {
		t.Error("the real binary's signed telemetry failed VerifyExtSignature under the embedded release key")
	}
	if _, ok := res.Ext.Telemetry["p99Ms"]; !ok {
		t.Errorf("telemetry = %v, want the ARCH §3.3 percentile keys", res.Ext.Telemetry)
	}
}

// smallRegistry adapts a PrefixFactory to runner.CheckLookup for one type.
type factoryRegistry struct {
	f     PrefixFactory
	types map[string]bool
}

func (r factoryRegistry) Lookup(typeName string) (Checker, error) {
	if !r.types[typeName] {
		return nil, spec.UnknownCheckTypeError(typeName)
	}
	return r.f(typeName)
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
