package cmd

import (
	"errors"
	"testing"
)

// TestExitCodeForMapsEveryTaskErrorKind pins the TASK.md §3 exit-code
// mapping Execute relies on — unit-testable on its own (no os.Exit, no
// cobra parse) now that Execute defers to this pure core (golang-cli
// review checklist item 2: os.Exit lives in main alone).
func TestExitCodeForMapsEveryTaskErrorKind(t *testing.T) {
	tests := []struct {
		name          string
		err           error
		wantCode      int
		wantShowUsage bool
	}{
		{
			name:          "a UsageError exits 5 and shows usage",
			err:           usageErrorf("bad arg"),
			wantCode:      5,
			wantShowUsage: true,
		},
		{
			name:          "cobra's own unknown-command error exits 5 and shows usage",
			err:           errors.New(`unknown command "bogus" for "grid"`),
			wantCode:      5,
			wantShowUsage: true,
		},
		{
			name:          "an ExitError carries its own code and never shows usage",
			err:           exitError(3, errors.New("not logged in")),
			wantCode:      3,
			wantShowUsage: false,
		},
		{
			name:          "exit code 130 (interrupted) survives the mapping unchanged",
			err:           exitError(130, errors.New("interrupted")),
			wantCode:      130,
			wantShowUsage: false,
		},
		{
			name:          "any other error defaults to exit 1 and never shows usage",
			err:           errors.New("something unexpected"),
			wantCode:      1,
			wantShowUsage: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, showUsage := exitCodeFor(tt.err)
			if code != tt.wantCode {
				t.Errorf("exitCodeFor(%v) code = %d, want %d", tt.err, code, tt.wantCode)
			}
			if showUsage != tt.wantShowUsage {
				t.Errorf("exitCodeFor(%v) showUsage = %v, want %v", tt.err, showUsage, tt.wantShowUsage)
			}
		})
	}
}
