package checks

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"os"
	"time"

	"github.com/anshul-kh/grid-cli/internal/extensions"
	"github.com/anshul-kh/grid-cli/internal/runner"
	"github.com/anshul-kh/grid-cli/internal/spec"
	"github.com/anshul-kh/grid-extensions/pkg/attest"
	"github.com/anshul-kh/grid-extensions/pkg/extcommon"
)

// ipcTimeoutMargin is added on top of the check's timeout (or the spec's
// global default) to bound one ext IPC exchange: the margin covers process
// spawn and teardown, so the extension's own budget stays the check's.
const ipcTimeoutMargin = 10 * time.Second

// extChecker measures one ext.<name>.<action> check (§4/A1): it ensures the
// pinned official binary is cached (downloading and release-verifying it if
// not), dispatches the extcommon IPC request carrying the check's params
// and the active attemptNonce, and verifies the signed response before
// surfacing it as the check's A4 ext result (telemetry + extBinaryHash +
// extSignature, in place of builtin evidence). One instance per check —
// the registry builds it through the ext prefix factory, and its ExtResult
// is this check's alone.
type extChecker struct {
	mgr          *extensions.Manager
	name         string
	version      string
	action       string
	attemptID    string
	attemptNonce string

	// verifyPub is the ext-signature verification key; nil means the
	// embedded release key. Test seam only — official extensions sign with
	// the release private half.
	verifyPub ed25519.PublicKey

	result *runner.ExtResult
}

// NewExtFactory returns the registry prefix factory for the ext. namespace
// (§4/A1): it resolves the pinned version for the type's extension — a
// type naming an unpinned extension is a measurement that cannot even
// start, and says so — and builds the check's own extChecker.
func NewExtFactory(cs *spec.CheckSpec, mgr *extensions.Manager, attemptID, attemptNonce string) PrefixFactory {
	pins := make(map[string]string, len(cs.Extensions))
	for _, p := range cs.Extensions {
		pins[p.Name] = p.Version
	}
	return func(typeName string) (Checker, error) {
		name, action, ok := spec.ParseExtType(typeName)
		if !ok {
			return nil, spec.UnknownCheckTypeError(typeName)
		}
		version, pinned := pins[name]
		if !pinned {
			return nil, fmt.Errorf("type %q names extension %q, which the spec does not pin in its extensions block", typeName, name)
		}
		return &extChecker{
			mgr:          mgr,
			name:         name,
			version:      version,
			action:       action,
			attemptID:    attemptID,
			attemptNonce: attemptNonce,
		}, nil
	}
}

// extVerifyKey resolves the key an ext response's extSignature must verify
// against: the embedded release key by default.
func (e *extChecker) extVerifyKey() ed25519.PublicKey {
	if e.verifyPub != nil {
		return e.verifyPub
	}
	return extensions.ReleasePub()
}

// ExtResult hands the measured ext result to runner.MeasureCheck.
func (e *extChecker) ExtResult() *runner.ExtResult { return e.result }

func (e *extChecker) Measure(ctx context.Context, c spec.Check, env *runner.Env) (runner.Evidence, error) {
	path, _, err := e.mgr.Ensure(e.name, e.version)
	if err != nil {
		return nil, fmt.Errorf("ext.%s: %w", e.name, err)
	}

	timeout := c.Timeout.D()
	if timeout <= 0 {
		timeout = env.DefaultTimeout
	}
	resp, err := extensions.Call(ctx, path, &extcommon.Request{
		AttemptID:    e.attemptID,
		AttemptNonce: e.attemptNonce,
		CheckID:      c.ID,
		Action:       e.action,
		Params:       c.Params,
	}, timeout+ipcTimeoutMargin)
	if err != nil {
		return nil, fmt.Errorf("ext.%s: %w", e.name, err)
	}

	bin, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("ext.%s: hashing the cached binary: %w", e.name, err)
	}
	wantHash := attest.SHA256Prefix(bin)
	if resp.ExtBinaryHash != wantHash {
		return nil, fmt.Errorf("ext.%s: the response's extBinaryHash %q does not match the cached binary (%s) — refusing the telemetry", e.name, resp.ExtBinaryHash, wantHash)
	}
	if !attest.VerifyExtSignature(e.extVerifyKey(), e.attemptID, e.attemptNonce, c.ID, resp.ExtBinaryHash, resp.Telemetry, resp.ExtSignature) {
		return nil, fmt.Errorf("ext.%s: the response's extSignature did not verify — refusing the telemetry", e.name)
	}

	e.result = &runner.ExtResult{
		Extension:     e.name + "@" + e.version,
		ExtBinaryHash: resp.ExtBinaryHash,
		Telemetry:     resp.Telemetry,
		ExtSignature:  resp.ExtSignature,
	}
	// Ext results carry telemetry, not evidence (§5.3 result kinds) — a nil
	// Evidence keeps the two kinds apart on the wire exactly as the spec
	// requires.
	return nil, nil
}
