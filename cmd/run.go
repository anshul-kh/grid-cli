package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/anshul-kh/grid-cli/internal/api"
	"github.com/anshul-kh/grid-cli/internal/auth"
	"github.com/anshul-kh/grid-cli/internal/buildinfo"
	"github.com/anshul-kh/grid-cli/internal/extensions"
	"github.com/anshul-kh/grid-cli/internal/spec"
	"github.com/anshul-kh/grid-cli/internal/ui"
	"github.com/anshul-kh/grid-cli/internal/workspace"
	"github.com/spf13/cobra"
)

var (
	runHere  bool
	runForce bool
)

var runCmd = &cobra.Command{
	Use:   "run <lessonId>",
	Short: "Fetch a lesson attempt and scaffold its workspace",
	Long: `Fetch a lesson's attempt and public check-spec, then scaffold exactly
what the lesson declared — nothing, a plain directory, a directory with
git initialised, or (soon) a directory with a starter bundle unpacked into
it — and write .grid/attempt.json so grid check and grid submit can pick
it up.`,
	Args: cobra.ExactArgs(1),
	RunE: runRun,
}

func init() {
	runCmd.Flags().BoolVar(&runHere, "here", false, "scaffold into the current directory instead of a new one")
	runCmd.Flags().BoolVar(&runForce, "force", false, "overwrite a non-empty workspace directory")
	rootCmd.AddCommand(runCmd)
}

// runTaskJSON is one entry of the --json task list.
type runTaskJSON struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// runJSON is the stable --json schema for grid run.
type runJSON struct {
	LessonID         string        `json:"lessonId"`
	Title            string        `json:"title"`
	Difficulty       string        `json:"difficulty"`
	EstimatedMinutes int           `json:"estimatedMinutes"`
	Mode             string        `json:"mode"`
	DirPath          string        `json:"dirPath,omitempty"`
	Tasks            []runTaskJSON `json:"tasks"`
	LessonURL        string        `json:"lessonUrl"`
}

func runRun(cmd *cobra.Command, args []string) error {
	lessonID := args[0]
	if !workspace.IsValidLessonID(lessonID) {
		return usageErrorf("lesson id %q is not a valid UUID", lessonID)
	}

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

	stderr := os.Stderr
	fmt.Fprintf(stderr, "Fetching lesson %s...\n", lessonID)
	att, err := client.Attempt(ctx, lessonID)
	if err != nil {
		return runFetchError(err)
	}

	cs, err := spec.Parse(att.CheckSpec)
	if err != nil {
		return exitError(4, fmt.Errorf("could not parse the lesson's check spec: %w", err))
	}

	// A3: ensure the attempt's declared extensions are cached (downloaded
	// and release-verified) before anything measures — and before any
	// workspace is scaffolded, so a bad mirror or tampered mirror binary
	// fails the run before it creates anything.
	if err := ensureExtensions(stderr, att, cs); err != nil {
		return err
	}

	mode := workspace.ParseMode(att.Workspace.Mode)

	// mode: none scaffolds nothing in the invocation directory at all — no
	// folder, no git. .grid/attempt.json lives in the CLI's own state
	// directory instead (workspace.StateDir), keyed by lesson id, so
	// `grid check`/`grid status`/`grid submit`/`grid open` can still find it
	// (workspace.FindAttempt) from anywhere.
	if mode == workspace.ModeNone {
		return runNone(stderr, att, cs)
	}

	dirName := att.Workspace.DirName
	if dirName == "" {
		dirName = workspace.DefaultDirName(att.Lesson.CourseSlug, att.Lesson.LessonSlug)
	}
	base, err := workingDir()
	if err != nil {
		return err
	}
	dir, err := workspace.ResolveDir(base, dirName, runHere)
	if err != nil {
		// dirName came from the server (GRID_SPEC §5.2 workspace.dirName);
		// a rejected one is bad server data, not a usage mistake — exit 4.
		return exitError(4, fmt.Errorf("the lesson server returned an invalid workspace directory name: %w", err))
	}
	if err := workspace.EnsureWritable(dir, runForce); err != nil {
		return exitError(1, err)
	}
	// Guarded by mode.CreatesDir(), not just "we got this far": mode: none
	// already returned via runNone above, but the condition is spelled out
	// here too so this is the one place in the file that ever creates a
	// directory, and it is self-evidently never reached for a mode this
	// CLI must create nothing for.
	if mode.CreatesDir() {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return exitError(1, fmt.Errorf("creating workspace directory %s: %w", dir, err))
		}
	}

	// mode: repo and mode: bundle both need a git repo. Bundle download
	// itself is P20's job — until it lands, a bundle-mode lesson gets the
	// same directory-plus-git treatment as repo, with an honest note below
	// that no starter bundle was actually fetched.
	if mode.NeedsGit() {
		if err := workspace.InitRepo(dir); err != nil {
			if errors.Is(err, workspace.ErrGitUnavailable) {
				fmt.Fprintf(stderr, "Warning: %s — continuing without a git repo.\n", err)
			} else {
				return exitError(4, fmt.Errorf("could not initialise a git repo: %w", err))
			}
		}
	}

	if att.Workspace.StarterRepoURL != "" {
		fmt.Fprintf(stderr, "Cloning starter repo %s...\n", att.Workspace.StarterRepoURL)
		if err := workspace.CloneStarter(ctx, dir, att.Workspace.StarterRepoURL, att.Workspace.StarterSubdir); err != nil {
			if errors.Is(err, workspace.ErrGitUnavailable) {
				fmt.Fprintf(stderr, "Warning: %s — continuing with the starter files only.\n", err)
			} else {
				return exitError(4, fmt.Errorf("could not clone the starter repo: %w", err))
			}
		}
	}

	if err := workspace.WriteFiles(dir, att.Workspace.Files); err != nil {
		return exitError(1, fmt.Errorf("writing starter files: %w", err))
	}
	fmt.Fprintf(stderr, "Wrote %d starter file(s) to %s.\n", len(att.Workspace.Files), dir)

	af := workspace.NewAttemptFile(att, mode, dir, buildinfo.Version, time.Now().UTC())
	if err := workspace.WriteAttemptFile(dir, af); err != nil {
		return exitError(1, fmt.Errorf("writing .grid/attempt.json: %w", err))
	}

	tasks := taskList(cs)

	if cfg.JSON {
		// --json keeps stdout machine-only (TestRunJSONStdoutIsMachineOnly),
		// so the task list's only human-visible home is stderr progress —
		// printed here, once, rather than also repeated in a stdout render
		// nothing in this mode ever produces.
		fmt.Fprintln(stderr, "Tasks:")
		for i, t := range tasks {
			fmt.Fprintf(stderr, "  %d. %s\n", i+1, t.Title)
		}
		out := runJSON{
			LessonID:         att.Lesson.ID,
			Title:            att.Lesson.Title,
			Difficulty:       att.Lesson.Difficulty,
			EstimatedMinutes: att.Lesson.EstimatedMinutes,
			Mode:             string(mode),
			DirPath:          dir,
			Tasks:            tasks,
			LessonURL:        att.URLs.LessonURL,
		}
		return ui.WriteJSON(out)
	}

	// Human mode renders the full numbered task list once, below, as part
	// of the final stdout output — echoing it on stderr first would just
	// be the same list printed twice to a terminal that merges both
	// streams (pass 9: the duplicate print pass 7's verifier flagged).
	fmt.Fprintf(stderr, "Found %d task(s).\n", len(tasks))

	theme := Theme()
	width := ui.RenderWidth()
	rel, err := filepath.Rel(base, dir)
	if err != nil || rel == "." {
		rel = dir
	}

	flow := ui.NewFlow(os.Stdout)
	flow.Line(theme.Bold(att.Lesson.Title))
	flow.Line(fmt.Sprintf("%s  ·  ~%d min", theme.Cyan(att.Lesson.Difficulty), att.Lesson.EstimatedMinutes))
	flow.Line(theme.Dim(workspaceSummary(mode, rel)))
	flow.Section(theme, "Tasks", width, func(w io.Writer) {
		for i, t := range tasks {
			fmt.Fprintf(w, "%d. %s\n", i+1, t.Title)
		}
	})
	flow.Section(theme, "Next steps", width, func(w io.Writer) {
		if !runHere {
			fmt.Fprintf(w, "cd %s\n", rel)
		}
		fmt.Fprintln(w, "grid check")
		fmt.Fprintln(w, "grid submit")
	})
	if att.URLs.LessonURL != "" {
		flow.Blank()
		flow.Line(theme.Dim(att.URLs.LessonURL))
	}
	return nil
}

// extensionPins merges the attempt payload's pins (§5.2/A3 — the pins the
// CLI must ensure cached) with the public spec's own top-level extensions
// block (§4/A1), deduped: either source alone is authoritative, so the
// union covers a server that ships only one of them.
func extensionPins(att api.AttemptResponse, cs *spec.CheckSpec) []spec.ExtensionPin {
	seen := make(map[string]string)
	var order []string
	add := func(name, version string) {
		if _, ok := seen[name]; !ok {
			order = append(order, name)
		}
		seen[name] = version
	}
	for _, p := range att.Extensions {
		add(p.Name, p.Version)
	}
	for _, p := range cs.Extensions {
		add(p.Name, p.Version)
	}
	pins := make([]spec.ExtensionPin, 0, len(order))
	for _, name := range order {
		pins = append(pins, spec.ExtensionPin{Name: name, Version: seen[name]})
	}
	return pins
}

// ensureExtensions brings every pinned extension into the local cache
// before the run continues (§5.2/A3): downloading from the mirror and
// release-verifying each miss, printing exactly what was fetched or was
// already cached. A failure — unreachable mirror, tampered or unsigned
// binary — aborts the run: the spec's ext checks cannot be measured
// honestly without the official binary. Exit 4: fetching the lesson's
// tooling is a network/server-class failure, like the attempt fetch itself.
func ensureExtensions(stderr io.Writer, att api.AttemptResponse, cs *spec.CheckSpec) error {
	pins := extensionPins(att, cs)
	if len(pins) == 0 {
		return nil
	}
	mgr, err := extensions.NewManager()
	if err != nil {
		return exitError(1, err)
	}
	for _, pin := range pins {
		fmt.Fprintf(stderr, "Ensuring extension %s@%s...\n", pin.Name, pin.Version)
		_, downloaded, err := mgr.Ensure(pin.Name, pin.Version)
		if err != nil {
			if errors.Is(err, extensions.ErrBadSignature) {
				return exitError(4, fmt.Errorf("extension %s@%s failed release signature verification and was deleted, never run — the mirror at %s may be tampered: %w", pin.Name, pin.Version, os.Getenv(extensions.EnvMirror), err))
			}
			return exitError(4, fmt.Errorf("could not fetch extension %s@%s: %w", pin.Name, pin.Version, err))
		}
		if downloaded {
			fmt.Fprintf(stderr, "Downloaded and signature-verified %s@%s.\n", pin.Name, pin.Version)
		} else {
			fmt.Fprintf(stderr, "Already cached: %s@%s.\n", pin.Name, pin.Version)
		}
	}
	return nil
}

// runNone handles workspace.mode: none — GRID_SPEC §4's "checks run in a
// temp cwd" mode: no directory, no git, nothing written anywhere in the
// invocation directory. The attempt lives in the CLI's own state directory
// (workspace.StateDir), keyed by lesson id, so grid check/grid status/grid
// submit/grid open can still find it from any cwd (workspace.FindAttempt).
func runNone(stderr io.Writer, att api.AttemptResponse, cs *spec.CheckSpec) error {
	dir, err := workspace.StateDir(att.Lesson.ID)
	if err != nil {
		return exitError(1, fmt.Errorf("resolving attempt state directory: %w", err))
	}
	af := workspace.NewAttemptFile(att, workspace.ModeNone, dir, buildinfo.Version, time.Now().UTC())
	if err := workspace.WriteAttemptFile(dir, af); err != nil {
		return exitError(1, fmt.Errorf("writing attempt state: %w", err))
	}

	tasks := taskList(cs)
	fmt.Fprintf(stderr, "Found %d task(s).\n", len(tasks))

	if cfg.JSON {
		fmt.Fprintln(stderr, "Tasks:")
		for i, t := range tasks {
			fmt.Fprintf(stderr, "  %d. %s\n", i+1, t.Title)
		}
		out := runJSON{
			LessonID:         att.Lesson.ID,
			Title:            att.Lesson.Title,
			Difficulty:       att.Lesson.Difficulty,
			EstimatedMinutes: att.Lesson.EstimatedMinutes,
			Mode:             string(workspace.ModeNone),
			Tasks:            tasks,
			LessonURL:        att.URLs.LessonURL,
		}
		return ui.WriteJSON(out)
	}

	theme := Theme()
	width := ui.RenderWidth()
	flow := ui.NewFlow(os.Stdout)
	flow.Line(theme.Bold(att.Lesson.Title))
	flow.Line(fmt.Sprintf("%s  ·  ~%d min", theme.Cyan(att.Lesson.Difficulty), att.Lesson.EstimatedMinutes))
	flow.Line(theme.Dim(workspaceSummary(workspace.ModeNone, "")))
	flow.Section(theme, "Tasks", width, func(w io.Writer) {
		for i, t := range tasks {
			fmt.Fprintf(w, "%d. %s\n", i+1, t.Title)
		}
	})
	flow.Section(theme, "Next steps", width, func(w io.Writer) {
		fmt.Fprintln(w, "grid check")
		fmt.Fprintln(w, "grid submit")
	})
	if att.URLs.LessonURL != "" {
		flow.Blank()
		flow.Line(theme.Dim(att.URLs.LessonURL))
	}
	return nil
}

// taskList flattens a parsed check-spec's checks into grid run's numbered
// task list, falling back to the check id when a check has no title —
// shared by every mode's render.
func taskList(cs *spec.CheckSpec) []runTaskJSON {
	tasks := make([]runTaskJSON, len(cs.Checks))
	for i, c := range cs.Checks {
		title := c.Title
		if title == "" {
			title = c.ID
		}
		tasks[i] = runTaskJSON{ID: c.ID, Title: title}
	}
	return tasks
}

// workspaceSummary renders a one-line, honest account of what `grid run`
// actually created for this mode (GRID_SPEC §4 "Workspace modes") — stated
// plainly rather than left for the learner to infer from which files
// happen to exist.
func workspaceSummary(mode workspace.Mode, rel string) string {
	switch mode {
	case workspace.ModeNone:
		return "This lesson checks your machine, not a project — nothing was created."
	case workspace.ModeRepo:
		return fmt.Sprintf("Created %s (a git repo).", rel)
	case workspace.ModeBundle:
		return fmt.Sprintf("Created %s (a git repo) — no starter bundle declared yet.", rel)
	default: // workspace.ModeFolder
		return fmt.Sprintf("Created %s.", rel)
	}
}

// workingDir resolves what "the current directory" means for this
// invocation: the --dir global flag when set, otherwise the process's
// actual working directory — the same resolution every command uses
// (TASK.md §3: --dir overrides it for the whole CLI).
func workingDir() (string, error) {
	base := cfg.Dir
	if base == "" {
		base = "."
	}
	abs, err := filepath.Abs(base)
	if err != nil {
		return "", fmt.Errorf("resolving working directory: %w", err)
	}
	return abs, nil
}

// runFetchError maps GET /cli/lessons/:id/attempt failures to TASK.md §3
// exit codes: missing/expired session → 3 with the grid login hint; an
// unknown lesson id → 4 with a clear message; everything else the client
// already classifies (server/network) → 4.
func runFetchError(err error) error {
	switch {
	case errors.Is(err, api.ErrUnauthorized):
		return exitError(3, fmt.Errorf("session missing or expired — run `grid login` to log in again"))
	case errors.Is(err, api.ErrNotEnrolled):
		return notEnrolledError(err)
	case errors.Is(err, api.ErrDenied):
		return exitError(3, fmt.Errorf("this Grid account doesn't have access to that lesson — run `grid login` with an authorised account"))
	default:
		var statusErr *api.StatusError
		if errors.As(err, &statusErr) && statusErr.StatusCode == 404 {
			return exitError(4, fmt.Errorf("lesson not found — check the lesson id and try again"))
		}
		return exitError(4, fmt.Errorf("could not fetch the lesson attempt: %s. %s", err, api.UserHint(err)))
	}
}
