package cmd

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/anshul-kh/grid-cli/internal/api"
	"github.com/anshul-kh/grid-cli/internal/runner"
	"github.com/anshul-kh/grid-cli/internal/ui"
	"github.com/anshul-kh/grid-cli/internal/workspace"
	"github.com/spf13/cobra"
)

var checkCmd = &cobra.Command{
	Use:   "check",
	Short: "Measure the workspace locally, without submitting",
	Long: `Run the lesson's checks locally and print what was measured.

grid check never submits anything to the server, and it never judges the
result — it can only report whether each measurement was taken
successfully (ok, error, or skipped). Run ` + "`grid submit`" + ` to get your
verdict.`,
	Args: cobra.NoArgs,
	RunE: runCheck,
}

func init() {
	rootCmd.AddCommand(checkCmd)
}

// checkResultJSON is one row of grid check's --json output.
type checkResultJSON struct {
	CheckID    string `json:"checkId"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	DurationMS int64  `json:"durationMs"`
	// Observed is what the checker measured, e.g. "exists=false". Added so a
	// script can tell "the measurement ran" (status) apart from "this is what
	// it saw" (observed) — `status: "ok"` alone says nothing about whether
	// the learner's work is correct. Omitted when there is nothing to report.
	Observed string `json:"observed,omitempty"`
}

// checkJSON is the stable --json schema for grid check.
type checkJSON struct {
	Results  []checkResultJSON `json:"results"`
	Measured int               `json:"measured"`
	Total    int               `json:"total"`
}

func runCheck(cmd *cobra.Command, args []string) error {
	ls, err := loadSpecForMeasurement()
	if err != nil {
		return err
	}
	defer ls.cleanup()

	rn := buildRunner(ls)
	reg, err := buildRegistry(ls)
	if err != nil {
		return err
	}
	theme := Theme()

	ctx, cancel := measurementCtx()
	defer cancel()
	fmt.Fprintln(os.Stderr, "Running checks...")
	sp := attachSpinner(rn, theme, "")
	defer sp.Stop()
	outcome := rn.Run(ctx, ls.cs, reg)
	sp.Stop()

	if outcome.SetupErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", outcome.SetupErr)
	}

	rows, anyError := measurementRows(outcome, checkTitles(ls.cs))
	measured := countMeasuredOK(outcome)
	total := len(ls.cs.Checks)

	// Record this run under .grid/ so `grid status` can show it later
	// without re-running anything (pass 8). Best-effort: a write failure
	// here must not turn a successful measurement into a failed command.
	// bookDir, not dir: for a mode: none attempt dir is a throwaway temp
	// cwd that cleanup removes before this command returns, never where
	// the attempt's local records live.
	if err := workspace.WriteLastRun(ls.bookDir, newLastRun(rows, measured, total)); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not record last-run results: %s\n", err)
	}

	if cfg.JSON {
		out := checkJSON{Measured: measured, Total: total}
		for _, row := range rows {
			out.Results = append(out.Results, checkResultJSON{
				CheckID:    row.CheckID,
				Title:      row.Title,
				Status:     string(row.Status),
				DurationMS: row.DurationMS,
				Observed:   row.Observed,
			})
		}
		if err := ui.WriteJSON(out); err != nil {
			return err
		}
	} else {
		width := ui.RenderWidth()
		rule := theme.Dim(ui.Rule(width))
		uiRows := toUIMeasurementRows(rows)

		flow := ui.NewFlow(os.Stdout)
		flow.Line(rule)
		flow.Line(renderBlock(func(bw io.Writer) { ui.RenderMeasurements(bw, theme, uiRows) }))
		flow.Line(rule)
		flow.Blank()
		flow.Line(renderBlock(func(bw io.Writer) { ui.MeasurementFooter(bw, theme, measured, total) }))
	}

	if interrupted(ctx) {
		return exitError(130, fmt.Errorf("interrupted"))
	}
	if anyError {
		return exitError(1, fmt.Errorf("one or more measurements could not be taken"))
	}
	return nil
}

// measurementRow is check.go's own row shape (carries CheckID, which
// ui.MeasurementRow deliberately does not need to know about).
type measurementRow struct {
	CheckID    string
	Title      string
	Status     api.CheckStatus
	DurationMS int64
	// Observed summarises what the checker actually measured, so the table
	// shows facts ("exists=false") rather than only that a measurement
	// happened. See ui.SummariseEvidence for why this matters.
	Observed string
}

// measurementRows flattens an Outcome's setup and check results into
// display rows, in the order they were attempted, and reports whether any
// of them is status "error" (grid check's only non-zero-exit condition,
// TASK.md pass 7 item 3).
func measurementRows(outcome runner.Outcome, titles map[string]string) (rows []measurementRow, anyError bool) {
	for i, res := range outcome.SetupResults {
		rows = append(rows, measurementRow{
			CheckID:    res.CheckID,
			Title:      fmt.Sprintf("setup step %d", i+1),
			Status:     api.CheckStatus(res.Status),
			DurationMS: res.DurationMS,
			Observed:   ui.SummariseEvidence(res.Evidence),
		})
		if res.Status == runner.StatusError {
			anyError = true
		}
	}
	for _, res := range outcome.CheckResults {
		title := titles[res.CheckID]
		if title == "" {
			title = res.CheckID
		}
		rows = append(rows, measurementRow{
			CheckID:    res.CheckID,
			Title:      title,
			Status:     api.CheckStatus(res.Status),
			DurationMS: res.DurationMS,
			Observed:   ui.SummariseEvidence(res.Evidence),
		})
		if res.Status == runner.StatusError {
			anyError = true
		}
	}
	return rows, anyError
}

// toUIMeasurementRows adapts measurementRow to ui.MeasurementRow.
func toUIMeasurementRows(rows []measurementRow) []ui.MeasurementRow {
	out := make([]ui.MeasurementRow, len(rows))
	for i, r := range rows {
		out[i] = ui.MeasurementRow{Title: r.Title, Status: r.Status, DurationMS: r.DurationMS, Observed: r.Observed}
	}
	return out
}

// countMeasuredOK counts the declared checks (not setup steps) that were
// successfully measured — the "measured N/M" footer's N.
func countMeasuredOK(outcome runner.Outcome) int {
	n := 0
	for _, res := range outcome.CheckResults {
		if res.Status == runner.StatusOK {
			n++
		}
	}
	return n
}

// newLastRun builds the .grid/last-run.json record `grid status` reads
// back (pass 8) from this run's display rows.
func newLastRun(rows []measurementRow, measured, total int) workspace.LastRun {
	checks := make([]workspace.LastRunCheck, len(rows))
	for i, row := range rows {
		checks[i] = workspace.LastRunCheck{
			CheckID:    row.CheckID,
			Title:      row.Title,
			Status:     row.Status,
			DurationMS: row.DurationMS,
		}
	}
	return workspace.LastRun{
		RanAt:    time.Now().UTC(),
		Measured: measured,
		Total:    total,
		Checks:   checks,
	}
}
