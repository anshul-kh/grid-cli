package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/anshul-kh/grid-cli/internal/upgrade"
)

// resetUpgradeFlags restores grid upgrade's package-level flag state
// between tests (matches run_test.go's resetRunFlags).
func resetUpgradeFlags(t *testing.T) {
	t.Helper()
	upgradeCheck = false
	t.Cleanup(func() { upgradeCheck = false })
}

// stubUpgradeRun swaps upgradeRun for a test double returning (res, err),
// so cmd-level tests can assert flag wiring and output formatting without
// hitting GitHub — internal/upgrade's own tests cover the real check/
// download/verify/replace logic against an httptest stand-in.
func stubUpgradeRun(t *testing.T, res upgrade.Result, err error) {
	t.Helper()
	old := upgradeRun
	upgradeRun = func(ctx context.Context, opts upgrade.Options, dryRun bool) (upgrade.Result, error) {
		return res, err
	}
	t.Cleanup(func() { upgradeRun = old })
}

// TestUpgradeJSONOutputShape proves --json emits exactly the plan's fixed
// schema: { currentVersion, latestVersion, upgraded, assetUrl }.
func TestUpgradeJSONOutputShape(t *testing.T) {
	resetUpgradeFlags(t)
	stubUpgradeRun(t, upgrade.Result{
		CurrentVersion: "1.0.0",
		LatestVersion:  "1.1.0",
		Upgraded:       true,
		AssetURL:       "https://example.invalid/grid-cli_linux_amd64.tar.gz",
	}, nil)
	setCmdCfg(unreachableAPIURL, true)

	stdout, _, err := captureOutput(t, func() error {
		return runUpgrade(upgradeCmd, nil)
	})
	if err != nil {
		t.Fatalf("runUpgrade --json: %v", err)
	}
	var got upgradeJSON
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("upgrade --json stdout is not valid JSON: %v\n%s", err, stdout)
	}
	if got.CurrentVersion != "1.0.0" || got.LatestVersion != "1.1.0" || !got.Upgraded ||
		got.AssetURL != "https://example.invalid/grid-cli_linux_amd64.tar.gz" {
		t.Errorf("upgrade --json = %+v", got)
	}
}

// TestUpgradePackageManagedPrintsHintAndExitsZero proves the
// package-manager detection path refuses to self-replace and says so
// (plan's explicit passing criterion), instead of silently doing nothing.
func TestUpgradePackageManagedPrintsHintAndExitsZero(t *testing.T) {
	resetUpgradeFlags(t)
	stubUpgradeRun(t, upgrade.Result{CurrentVersion: "1.0.0", LatestVersion: "1.0.0", PackageManaged: "Homebrew"}, nil)
	setCmdCfg(unreachableAPIURL, false)

	stdout, _, err := captureOutput(t, func() error {
		return runUpgrade(upgradeCmd, nil)
	})
	if err != nil {
		t.Fatalf("runUpgrade (package-managed): %v", err)
	}
	if !strings.Contains(stdout, "Homebrew") || !strings.Contains(stdout, "package manager") {
		t.Errorf("stdout = %q, want a clear package-manager hint", stdout)
	}
}

// TestUpgradeCheckFlagReportsWithoutClaimingUpgraded proves --check renders
// the available version without ever saying it was installed.
func TestUpgradeCheckFlagReportsWithoutClaimingUpgraded(t *testing.T) {
	resetUpgradeFlags(t)
	upgradeCheck = true
	stubUpgradeRun(t, upgrade.Result{CurrentVersion: "1.0.0", LatestVersion: "1.1.0", AssetURL: "https://example.invalid/x"}, nil)
	setCmdCfg(unreachableAPIURL, false)

	stdout, _, err := captureOutput(t, func() error {
		return runUpgrade(upgradeCmd, nil)
	})
	if err != nil {
		t.Fatalf("runUpgrade --check: %v", err)
	}
	if !strings.Contains(stdout, "1.1.0") {
		t.Errorf("stdout should report the available version:\n%s", stdout)
	}
	if strings.Contains(stdout, "Upgraded") {
		t.Errorf("--check must never claim an upgrade happened:\n%s", stdout)
	}
}

// TestUpgradeInstallFailureExitsOne proves a verification/install failure
// (checksum mismatch, bad archive, ...) maps to exit 1, not 4.
func TestUpgradeInstallFailureExitsOne(t *testing.T) {
	resetUpgradeFlags(t)
	stubUpgradeRun(t, upgrade.Result{}, fmt.Errorf("checksum mismatch: %w", upgrade.ErrInstallFailed))
	setCmdCfg(unreachableAPIURL, false)

	_, _, err := captureOutput(t, func() error {
		return runUpgrade(upgradeCmd, nil)
	})
	if code := exitCodeOf(t, err); code != 1 {
		t.Fatalf("upgrade install failure: exit code = %d (err %v), want 1", code, err)
	}
}

// TestUpgradeNetworkFailureExitsFour proves a plain fetch failure (reaching
// GitHub at all) maps to exit 4.
func TestUpgradeNetworkFailureExitsFour(t *testing.T) {
	resetUpgradeFlags(t)
	stubUpgradeRun(t, upgrade.Result{}, fmt.Errorf("dial tcp: connection refused"))
	setCmdCfg(unreachableAPIURL, false)

	_, _, err := captureOutput(t, func() error {
		return runUpgrade(upgradeCmd, nil)
	})
	if code := exitCodeOf(t, err); code != 4 {
		t.Fatalf("upgrade network failure: exit code = %d (err %v), want 4", code, err)
	}
}
