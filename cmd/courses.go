package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/anshul-kh/grid-cli/internal/api"
	"github.com/anshul-kh/grid-cli/internal/auth"
	"github.com/anshul-kh/grid-cli/internal/ui"
	"github.com/spf13/cobra"
)

var coursesCmd = &cobra.Command{
	Use:   "courses",
	Short: "List your enrolled courses",
	Long: `List your enrolled courses with their progress, fetched from
the Grid server. Requires a prior ` + "`grid login`" + `.`,
	Args: cobra.NoArgs,
	RunE: runCourses,
}

func init() {
	rootCmd.AddCommand(coursesCmd)
}

func runCourses(cmd *cobra.Command, args []string) error {
	store, err := auth.DefaultStore()
	if err != nil {
		return err
	}
	creds, err := store.Load()
	if errors.Is(err, auth.ErrNotLoggedIn) {
		return exitError(3, fmt.Errorf("not logged in — run `grid login` first"))
	} else if err != nil {
		return err
	}

	client, err := newAPIClient(creds.SessionKey)
	if err != nil {
		return err
	}
	ctx, cancel := commandCtx()
	defer cancel()
	resp, err := client.Courses(ctx)
	if err != nil {
		return coursesError(err)
	}

	if cfg.JSON {
		// The raw /cli/courses response object (§5.5): { "courses": [...] }.
		return ui.WriteJSON(resp)
	}

	theme := Theme()
	if len(resp.Courses) == 0 {
		ui.RenderEmptyState(os.Stdout, theme, "Not enrolled in any courses yet.", ui.RenderWidth())
		return nil
	}
	ui.RenderCourses(os.Stdout, theme, toCourseRows(resp.Courses))
	return nil
}

// toCourseRows adapts the wire Course list to ui.CourseRow.
func toCourseRows(courses []api.Course) []ui.CourseRow {
	rows := make([]ui.CourseRow, len(courses))
	for i, c := range courses {
		rows[i] = ui.CourseRow{
			Title:            c.Title,
			Difficulty:       c.Difficulty,
			EnrollmentStatus: c.Enrollment.Status,
			ProgressPct:      c.Enrollment.ProgressPct,
			LessonCount:      c.LessonCount,
		}
	}
	return rows
}

// coursesError maps GET /cli/courses failures to TASK.md §3 exit codes:
// missing or invalid session → 3 (auth required) with the grid login hint;
// server and network failures → 4.
func coursesError(err error) error {
	switch {
	case errors.Is(err, api.ErrUnauthorized):
		return exitError(3, fmt.Errorf("session missing or expired — run `grid login` to log in again"))
	case errors.Is(err, api.ErrDenied):
		return exitError(3, fmt.Errorf("this Grid account doesn't have access — run `grid login` with an authorised account"))
	default:
		return exitError(4, fmt.Errorf("could not fetch your courses: %s. %s", err, api.UserHint(err)))
	}
}
