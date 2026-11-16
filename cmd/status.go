package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/anshul-kh/grid-cli/internal/ui"
	"github.com/anshul-kh/grid-cli/internal/workspace"
	"github.com/spf13/cobra"
)

var statusRefresh bool

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the current lesson workspace's local state",
	Long: `Show the current lesson workspace's local state: the attempt's
expiry countdown, grid check's last local measurement, and grid submit's
last server verdict.

grid status never talks to the network — everything it shows comes from
.grid/attempt.json and the local records grid check/grid submit leave
behind. --refresh re-reads that local state against the current clock;
there is no server endpoint to refresh an attempt against (GRID_SPEC §5.5
lists grid status as local).`,
	Args: cobra.NoArgs,
	RunE: runStatus,
}

func init() {
	statusCmd.Flags().BoolVar(&statusRefresh, "refresh", false, "re-check the attempt's expiry against the current clock")
	rootCmd.AddCommand(statusCmd)
}

// statusLastRunJSON is grid status --json's lastRun object.
type statusLastRunJSON struct {
	RanAt    time.Time `json:"ranAt"`
	Measured int       `json:"measured"`
	Total    int       `json:"total"`
}

// statusLastVerdictJSON is grid status --json's lastVerdict object.
type statusLastVerdictJSON struct {
	SubmittedAt time.Time `json:"submittedAt"`
	Verdict     string    `json:"verdict"`
}

// statusJSON is the stable --json schema for grid status.
type statusJSON struct {
	LessonID         string                 `json:"lessonId"`
	Title            string                 `json:"title"`
	Difficulty       string                 `json:"difficulty"`
	Mode             string                 `json:"mode"`
	DirPath          string                 `json:"dirPath,omitempty"`
	AttemptID        string                 `json:"attemptId"`
	ExpiresAt        time.Time              `json:"expiresAt"`
	Expired          bool                   `json:"expired"`
	ExpiresInSeconds int64                  `json:"expiresInSeconds"`
	LastRun          *statusLastRunJSON     `json:"lastRun"`
	LastVerdict      *statusLastVerdictJSON `json:"lastVerdict"`
}

func runStatus(cmd *cobra.Command, args []string) error {
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
	// bookDir is af.DirPath always: the scaffolded workspace for every mode
	// except none, or the state pseudo-directory for none — the records
	// below were written against whichever one grid run/check/submit used.
	bookDir := af.DirPath

	lastRun, err := workspace.LoadLastRun(bookDir)
	haveLastRun := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return exitError(1, fmt.Errorf("could not read .grid/last-run.json: %w", err))
	}

	lastVerdict, err := workspace.LoadLastVerdict(bookDir)
	haveLastVerdict := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return exitError(1, fmt.Errorf("could not read .grid/last-verdict.json: %w", err))
	}

	// mode: none has no project directory to show (af.DirPath is the CLI's
	// internal state pseudo-directory, not something the learner should
	// see or cd into).
	mode := workspace.ParseMode(af.Mode)
	displayDir := af.DirPath
	if mode == workspace.ModeNone {
		displayDir = ""
	}

	now := time.Now().UTC()
	expired := !af.ExpiresAt.IsZero() && now.After(af.ExpiresAt)

	if cfg.JSON {
		out := statusJSON{
			LessonID:         af.Lesson.ID,
			Title:            af.Lesson.Title,
			Difficulty:       af.Lesson.Difficulty,
			Mode:             string(mode),
			DirPath:          displayDir,
			AttemptID:        af.AttemptID,
			ExpiresAt:        af.ExpiresAt,
			Expired:          expired,
			ExpiresInSeconds: int64(af.ExpiresAt.Sub(now).Seconds()),
		}
		if haveLastRun {
			out.LastRun = &statusLastRunJSON{RanAt: lastRun.RanAt, Measured: lastRun.Measured, Total: lastRun.Total}
		}
		if haveLastVerdict {
			out.LastVerdict = &statusLastVerdictJSON{SubmittedAt: lastVerdict.SubmittedAt, Verdict: string(lastVerdict.Verdict)}
		}
		return ui.WriteJSON(out)
	}

	theme := Theme()
	flow := ui.NewFlow(os.Stdout)
	flow.Line(theme.Bold(af.Lesson.Title))
	location := displayDir
	if location == "" {
		location = "no project directory — this lesson checks your machine"
	}
	flow.Line(fmt.Sprintf("%s  ·  ~%d min  ·  %s", theme.Cyan(af.Lesson.Difficulty), af.Lesson.EstimatedMinutes, location))
	flow.Blank()
	flow.Line(formatExpiry(theme, af.ExpiresAt, now))

	flow.Blank()
	if haveLastRun {
		flow.Line(fmt.Sprintf("last check: %s", formatAgo(lastRun.RanAt, now)))
		rows := make([]ui.MeasurementRow, len(lastRun.Checks))
		for i, c := range lastRun.Checks {
			rows[i] = ui.MeasurementRow{Title: c.Title, Status: c.Status, DurationMS: c.DurationMS}
		}
		flow.Line(renderBlock(func(bw io.Writer) { ui.RenderMeasurements(bw, theme, rows) }))
		flow.Blank()
		flow.Line(renderBlock(func(bw io.Writer) { ui.MeasurementFooter(bw, theme, lastRun.Measured, lastRun.Total) }))
	} else {
		flow.Line(theme.Dim("last check: none — run `grid check`"))
	}

	flow.Blank()
	if haveLastVerdict {
		flow.Line(fmt.Sprintf("last submit: %s", formatAgo(lastVerdict.SubmittedAt, now)))
		flow.Line(renderBlock(func(bw io.Writer) { ui.RenderVerdictBanner(bw, theme, lastVerdict.Verdict) }))
		rows := make([]ui.VerdictRow, len(lastVerdict.Checks))
		for i, c := range lastVerdict.Checks {
			rows[i] = ui.VerdictRow{Title: c.Title, Passed: c.Passed, Message: c.Message, Hint: c.Hint}
		}
		flow.Line(renderBlock(func(bw io.Writer) { ui.RenderVerdictTable(bw, theme, rows) }))
	} else {
		flow.Line(theme.Dim("last submit: none — run `grid submit`"))
	}
	return nil
}

// formatExpiry renders the attempt's expiry relative to now: "attempt
// expires in 42m" while still valid, "attempt expired 3h ago" once past
// ExpiresAt.
func formatExpiry(theme *ui.Theme, expiresAt, now time.Time) string {
	if expiresAt.IsZero() {
		return theme.Dim("no expiry recorded")
	}
	if now.After(expiresAt) {
		return theme.Red("attempt expired " + formatRoughDuration(now.Sub(expiresAt)) + " ago")
	}
	return theme.Green("attempt expires in " + formatRoughDuration(expiresAt.Sub(now)))
}

// formatAgo renders t relative to now as "3h ago" / "just now".
func formatAgo(t, now time.Time) string {
	d := now.Sub(t)
	if d < time.Second {
		return "just now"
	}
	return formatRoughDuration(d) + " ago"
}

// formatRoughDuration renders d at whichever of days/hours/minutes/seconds
// is coarsest and non-zero (e.g. "2d", "3h", "42m", "9s") — grid status's
// countdown only needs a rough magnitude, not sub-second precision.
func formatRoughDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	default:
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
}
