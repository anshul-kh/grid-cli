// Package workspace — this file (lastrun.go) owns `grid check`'s and
// `grid submit`'s local result records: `.grid/last-run.json` and
// `.grid/last-verdict.json`. `grid status` reads these back so it can show
// what was last measured/judged without re-running or re-submitting
// anything (pass 8, TASK.md §5.5: grid status is local-only).
package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/anshul-kh/grid-cli/internal/api"
)

// LastRunCheck is one check's locally-measured result, as `grid check` last
// saw it (ok | error | skipped — never a verdict, GRID_SPEC §1).
type LastRunCheck struct {
	CheckID    string          `json:"checkId"`
	Title      string          `json:"title"`
	Status     api.CheckStatus `json:"status"`
	DurationMS int64           `json:"durationMs"`
}

// LastRun is the on-disk shape of .grid/last-run.json: `grid check`'s most
// recent local measurement.
type LastRun struct {
	RanAt    time.Time      `json:"ranAt"`
	Measured int            `json:"measured"`
	Total    int            `json:"total"`
	Checks   []LastRunCheck `json:"checks"`
}

// LastVerdict is the on-disk shape of .grid/last-verdict.json: `grid
// submit`'s most recent server verdict. It never carries a session key or
// signature — only what the server already returns to the user.
type LastVerdict struct {
	SubmittedAt time.Time             `json:"submittedAt"`
	Verdict     api.Verdict           `json:"verdict"`
	Checks      []api.SubmissionCheck `json:"checks"`
	XP          api.XP                `json:"xp"`
}

func lastRunPath(dir string) string     { return filepath.Join(dir, gridDir, "last-run.json") }
func lastVerdictPath(dir string) string { return filepath.Join(dir, gridDir, "last-verdict.json") }

// WriteLastRun writes .grid/last-run.json under dir, creating the .grid
// directory if needed.
func WriteLastRun(dir string, lr LastRun) error {
	return writeJSONFile(lastRunPath(dir), lr)
}

// LoadLastRun reads back .grid/last-run.json. A missing file reports
// os.ErrNotExist (via errors.Is) — `grid status` treats that as "grid check
// has never run here", not a failure.
func LoadLastRun(dir string) (LastRun, error) {
	var lr LastRun
	err := readJSONFile(lastRunPath(dir), &lr)
	return lr, err
}

// WriteLastVerdict writes .grid/last-verdict.json under dir, creating the
// .grid directory if needed.
func WriteLastVerdict(dir string, lv LastVerdict) error {
	return writeJSONFile(lastVerdictPath(dir), lv)
}

// LoadLastVerdict reads back .grid/last-verdict.json. A missing file
// reports os.ErrNotExist — `grid status` treats that as "grid submit has
// never run here", not a failure.
func LoadLastVerdict(dir string) (LastVerdict, error) {
	var lv LastVerdict
	err := readJSONFile(lastVerdictPath(dir), &lv)
	return lv, err
}

// writeJSONFile marshals v as indented JSON and writes it to path, creating
// its parent directory if needed — the shared write path for attempt.json,
// last-run.json, and last-verdict.json.
func writeJSONFile(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding %s: %w", filepath.Base(path), err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// readJSONFile reads back path and unmarshals it into v.
func readJSONFile(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s is corrupt: %w", path, err)
	}
	return nil
}
