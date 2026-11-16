package cmd

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"runtime"

	"github.com/anshul-kh/grid-cli/internal/buildinfo"
	"github.com/anshul-kh/grid-cli/internal/ui"
	"github.com/anshul-kh/grid-cli/internal/upgrade"
	"github.com/spf13/cobra"
)

var upgradeCheck bool

var upgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Self-update grid to the latest release",
	Long: `Check GitHub releases for a newer grid version, verify its
published checksum, and atomically replace the running executable.

grid upgrade refuses to self-replace an install it detects was placed by a
package manager (Homebrew, or a root-owned /usr/local/bin) — use that
instead. --check only reports what's available, without downloading or
replacing anything.`,
	Args: cobra.NoArgs,
	RunE: runUpgrade,
}

func init() {
	upgradeCmd.Flags().BoolVar(&upgradeCheck, "check", false, "report the latest version without installing it")
	rootCmd.AddCommand(upgradeCmd)
}

// upgradeRun performs the actual check/upgrade; overridable in tests so
// cmd-level tests can assert flag wiring and output formatting without
// hitting GitHub (internal/upgrade's own tests cover the real logic end to
// end against an httptest stand-in for GitHub's release API).
var upgradeRun = upgrade.Run

// upgradeJSON is the stable --json schema for grid upgrade.
type upgradeJSON struct {
	CurrentVersion string `json:"currentVersion"`
	LatestVersion  string `json:"latestVersion"`
	Upgraded       bool   `json:"upgraded"`
	AssetURL       string `json:"assetUrl"`
}

func runUpgrade(cmd *cobra.Command, args []string) error {
	execPath, err := os.Executable()
	if err != nil {
		return exitError(1, fmt.Errorf("could not resolve the running executable's path: %w", err))
	}

	ctx, cancel := measurementCtx()
	defer cancel()
	res, err := upgradeRun(ctx, upgrade.Options{
		HTTPClient:     http.DefaultClient,
		RepoURL:        buildinfo.RepoURL,
		CurrentVersion: buildinfo.Version,
		OS:             runtime.GOOS,
		Arch:           runtime.GOARCH,
		ExecPath:       execPath,
	}, upgradeCheck)
	if interrupted(ctx) {
		return exitError(130, fmt.Errorf("interrupted"))
	}
	if err != nil {
		if errors.Is(err, upgrade.ErrInstallFailed) {
			return exitError(1, fmt.Errorf("could not upgrade grid: %w", err))
		}
		return exitError(4, fmt.Errorf("could not reach GitHub releases: %w", err))
	}

	if cfg.JSON {
		return ui.WriteJSON(upgradeJSON{
			CurrentVersion: res.CurrentVersion,
			LatestVersion:  res.LatestVersion,
			Upgraded:       res.Upgraded,
			AssetURL:       res.AssetURL,
		})
	}

	theme := Theme()
	switch {
	case res.PackageManaged != "":
		fmt.Printf("grid was installed via %s — use your package manager to upgrade instead of `grid upgrade`.\n", res.PackageManaged)
	case res.AssetURL == "":
		fmt.Printf("Already up to date (%s).\n", theme.Bold(res.CurrentVersion))
	case upgradeCheck:
		fmt.Printf("%s -> %s available: %s\n", res.CurrentVersion, theme.Bold(res.LatestVersion), res.AssetURL)
	case res.Upgraded:
		fmt.Printf("Upgraded %s -> %s.\n", res.CurrentVersion, theme.Bold(res.LatestVersion))
	default:
		fmt.Printf("%s -> %s available but not installed.\n", res.CurrentVersion, res.LatestVersion)
	}
	return nil
}
