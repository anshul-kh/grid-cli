package cmd

import (
	"fmt"
	"runtime"

	"github.com/anshul-kh/grid-cli/internal/buildinfo"
	"github.com/anshul-kh/grid-cli/internal/ui"
	"github.com/spf13/cobra"
)

// versionInfo is the stable --json schema for grid version. Field order in
// the struct is the field order in the output; do not reorder.
type versionInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Date      string `json:"date"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	GoVersion string `json:"goVersion"`
}

func newVersionInfo() versionInfo {
	return versionInfo{
		Version:   buildinfo.Version,
		Commit:    buildinfo.Commit,
		Date:      buildinfo.Date,
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		GoVersion: runtime.Version(),
	}
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		info := newVersionInfo()
		if cfg.JSON {
			return ui.WriteJSON(info)
		}
		theme := Theme()
		fmt.Println(theme.Bold("grid " + buildinfo.Version))
		fmt.Println(ui.Indent(fmt.Sprintf("commit:  %s", buildinfo.Commit)))
		fmt.Println(ui.Indent(fmt.Sprintf("built:   %s", buildinfo.Date)))
		fmt.Println(ui.Indent(fmt.Sprintf("os/arch: %s/%s", runtime.GOOS, runtime.GOARCH)))
		fmt.Println(ui.Indent(fmt.Sprintf("go:      %s", runtime.Version())))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
