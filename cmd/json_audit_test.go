package cmd

import (
	"encoding/json"
	"testing"

	"github.com/anshul-kh/grid-cli/internal/api/stub"
	"github.com/anshul-kh/grid-cli/internal/upgrade"
)

// TestEveryCommandSupportsJSON is the plan's pinned audit criterion: every
// command in cmd/ accepts --json and emits valid JSON on stdout. Each
// entry does the minimal setup that command's own dedicated *JSONOutputShape
// test already uses (reused here, not reimplemented) and only asserts the
// json.Valid baseline — field-for-field shape assertions stay in each
// command's own test, this one just proves none of the eleven commands
// was missed.
func TestEveryCommandSupportsJSON(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T) string // returns captured stdout
	}{
		{
			name: "login",
			run: func(t *testing.T) string {
				credsPath := isolateHome(t)
				loginAsStubUser(t, credsPath)
				setCmdCfg(unreachableAPIURL, true) // already-logged-in path never dials out
				stdout, _, err := captureOutput(t, func() error { return runLogin(loginCmd, nil) })
				if err != nil {
					t.Fatalf("runLogin --json: %v", err)
				}
				return stdout
			},
		},
		{
			name: "logout",
			run: func(t *testing.T) string {
				isolateHome(t) // deliberately never logged in: the no-network fast path
				setCmdCfg(unreachableAPIURL, true)
				stdout, _, err := captureOutput(t, func() error { return runLogout(logoutCmd, nil) })
				if err != nil {
					t.Fatalf("runLogout --json: %v", err)
				}
				return stdout
			},
		},
		{
			name: "whoami",
			run: func(t *testing.T) string {
				_, srv := newStubCmdServer(t)
				credsPath := isolateHome(t)
				loginAsStubUser(t, credsPath)
				setCmdCfg(srv.URL+"/api/v1", true)
				stdout, _, err := captureOutput(t, func() error { return runWhoami(whoamiCmd, nil) })
				if err != nil {
					t.Fatalf("runWhoami --json: %v", err)
				}
				return stdout
			},
		},
		{
			name: "courses",
			run: func(t *testing.T) string {
				_, srv := newStubCmdServer(t)
				credsPath := isolateHome(t)
				loginAsStubUser(t, credsPath)
				setCmdCfg(srv.URL+"/api/v1", true)
				stdout, _, err := captureOutput(t, func() error { return runCourses(coursesCmd, nil) })
				if err != nil {
					t.Fatalf("runCourses --json: %v", err)
				}
				return stdout
			},
		},
		{
			name: "run",
			run: func(t *testing.T) string {
				resetRunFlags(t)
				_, srv := newStubCmdServer(t)
				credsPath := isolateHome(t)
				loginAsStubUser(t, credsPath)
				setCmdCfg(srv.URL+"/api/v1", true)
				cfg.Dir = t.TempDir()
				stdout, _, err := captureOutput(t, func() error { return runRun(runCmd, []string{stub.LessonID}) })
				if err != nil {
					t.Fatalf("runRun --json: %v", err)
				}
				return stdout
			},
		},
		{
			name: "check",
			run: func(t *testing.T) string {
				isolateHome(t) // grid check needs no auth at all
				setCmdCfg(unreachableAPIURL, true)
				dir := t.TempDir()
				cfg.Dir = dir
				writeTestAttempt(t, dir, stub.AttemptNonce, simpleCheckSpecFixture)
				stdout, _, err := captureOutput(t, func() error { return runCheck(checkCmd, nil) })
				if err != nil {
					t.Fatalf("runCheck --json: %v", err)
				}
				return stdout
			},
		},
		{
			name: "submit",
			run: func(t *testing.T) string {
				credsPath := isolateHome(t)
				loginAsStubUser(t, credsPath)
				_, srv := newStubCmdServer(t)
				setCmdCfg(srv.URL+"/api/v1", true)
				dir := t.TempDir()
				cfg.Dir = dir
				writeTestAttempt(t, dir, stub.AttemptNonce, simpleCheckSpecFixture)
				stdout, _, err := captureOutput(t, func() error { return runSubmit(submitCmd, nil) })
				if err != nil {
					t.Fatalf("runSubmit --json: %v", err)
				}
				return stdout
			},
		},
		{
			name: "status",
			run: func(t *testing.T) string {
				isolateHome(t) // grid status never touches the network
				setCmdCfg(unreachableAPIURL, true)
				dir := t.TempDir()
				cfg.Dir = dir
				writeTestAttempt(t, dir, stub.AttemptNonce, simpleCheckSpecFixture)
				stdout, _, err := captureOutput(t, func() error { return runStatus(statusCmd, nil) })
				if err != nil {
					t.Fatalf("runStatus --json: %v", err)
				}
				return stdout
			},
		},
		{
			name: "open",
			run: func(t *testing.T) string {
				resetOpenFlags(t)
				openPrint = true // never shells out to a real browser opener
				isolateHome(t)
				setCmdCfg(unreachableAPIURL, true)
				dir := t.TempDir()
				cfg.Dir = dir
				writeTestAttemptWithURL(t, dir, testLessonURL)
				stdout, _, err := captureOutput(t, func() error { return runOpen(openCmd, nil) })
				if err != nil {
					t.Fatalf("runOpen --json: %v", err)
				}
				return stdout
			},
		},
		{
			name: "upgrade",
			run: func(t *testing.T) string {
				resetUpgradeFlags(t)
				stubUpgradeRun(t, upgrade.Result{CurrentVersion: "1.0.0", LatestVersion: "1.0.0"}, nil)
				setCmdCfg(unreachableAPIURL, true)
				stdout, _, err := captureOutput(t, func() error { return runUpgrade(upgradeCmd, nil) })
				if err != nil {
					t.Fatalf("runUpgrade --json: %v", err)
				}
				return stdout
			},
		},
		{
			name: "version",
			run: func(t *testing.T) string {
				setCmdCfg(unreachableAPIURL, true)
				stdout, _, err := captureOutput(t, func() error { return versionCmd.RunE(versionCmd, nil) })
				if err != nil {
					t.Fatalf("version --json: %v", err)
				}
				return stdout
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout := tt.run(t)
			if !json.Valid([]byte(stdout)) {
				t.Fatalf("grid %s --json stdout is not valid JSON:\n%s", tt.name, stdout)
			}
			var v any
			if err := json.Unmarshal([]byte(stdout), &v); err != nil {
				t.Fatalf("grid %s --json: %v", tt.name, err)
			}
			if _, ok := v.(map[string]any); !ok {
				t.Errorf("grid %s --json top level = %T, want a JSON object", tt.name, v)
			}
		})
	}
}
