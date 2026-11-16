package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/anshul-kh/grid-cli/internal/auth"
	"github.com/anshul-kh/grid-cli/internal/ui"
	"github.com/anshul-kh/grid-cli/internal/workspace"
	"github.com/spf13/cobra"
)

var openPrint bool

var openCmd = &cobra.Command{
	Use:   "open",
	Short: "Open the current lesson in a browser",
	Long: `Open the lesson URL recorded in .grid/attempt.json in your
browser.

grid open never fails when a browser cannot be launched — it always
prints the URL too, as the fallback. --print skips launching a browser
entirely and only prints the URL.`,
	Args: cobra.NoArgs,
	RunE: runOpen,
}

func init() {
	openCmd.Flags().BoolVar(&openPrint, "print", false, "print the lesson URL instead of opening a browser")
	rootCmd.AddCommand(openCmd)
}

// openBrowser is the browser-launch hook, overridable in tests so they can
// simulate a browser command that is missing or fails without actually
// shelling out. Defaults to the same best-effort opener `grid login` uses
// (TASK.md §5: open / xdg-open / rundll32).
var openBrowser = auth.OpenBrowser

// openJSON is the stable --json schema for grid open.
type openJSON struct {
	URL    string `json:"url"`
	Opened bool   `json:"opened"`
}

func runOpen(cmd *cobra.Command, args []string) error {
	cwd, err := workingDir()
	if err != nil {
		return err
	}
	// FindAttempt, not LoadAttemptFile: a workspace.mode: none attempt
	// (GRID_SPEC §4) never scaffolded a local .grid, so it's found in the
	// CLI's own state directory instead, from any cwd.
	af, err := workspace.FindAttempt(cwd)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return noOpenAttemptError()
		}
		return exitError(5, fmt.Errorf("could not read the open attempt: %w", err))
	}
	if af.LessonURL == "" {
		return exitError(4, fmt.Errorf("this lesson has no URL recorded — run `grid run %s` again to refresh it", af.Lesson.ID))
	}

	opened := false
	if !openPrint {
		if err := openBrowser(context.Background(), af.LessonURL); err == nil {
			opened = true
		} else if cfg.Verbose {
			fmt.Fprintf(os.Stderr, "could not open a browser automatically: %s\n", err)
		}
	}

	if cfg.JSON {
		return ui.WriteJSON(openJSON{URL: af.LessonURL, Opened: opened})
	}
	fmt.Println(af.LessonURL)
	if !opened && !openPrint {
		theme := Theme()
		fmt.Println(theme.Dim("Could not open a browser automatically — open the URL above."))
	}
	return nil
}
