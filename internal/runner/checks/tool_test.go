package checks

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/anshul-kh/grid-cli/internal/runner"
	"github.com/anshul-kh/grid-cli/internal/spec"
)

// writeFakeTool drops an executable script named name on a temp PATH
// directory that prints output and returns it. It is a shell script on
// unix and a .bat on Windows — either way, found purely via PATH lookup
// (exec.LookPath), never a hardcoded location.
func writeFakeTool(t *testing.T, name, printTo, output string, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	var path, body string
	if runtime.GOOS == "windows" {
		path = filepath.Join(dir, name+".bat")
		// Never redirect stdout to itself (`1>&1`): cmd.exe answers with
		// "The handle could not be duplicated during redirection of handle 1."
		redirect := ""
		if printTo == "stderr" {
			redirect = " 1>&2"
		}
		body = "@echo off\r\necho " + output + redirect + "\r\nexit /b " + strconv.Itoa(exitCode) + "\r\n"
	} else {
		path = filepath.Join(dir, name)
		redirect := ""
		if printTo == "stderr" {
			redirect = " 1>&2"
		}
		body = "#!/bin/sh\necho '" + output + "'" + redirect + "\nexit " + strconv.Itoa(exitCode) + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake tool: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return path
}

// TestToolInstalledFound proves { found, path, versionOutput } for a tool
// that exists on PATH, resolved via exec.LookPath (grep-checked separately
// across the package — see TestToolInstalledNeverShellsOutToWhich).
func TestToolInstalledFound(t *testing.T) {
	writeFakeTool(t, "grid-fake-tool", "stdout", "grid-fake-tool version 9.9.9", 0)
	env := runner.NewEnv(t.TempDir(), nil)

	ev, err := (toolInstalledChecker{}).Measure(context.Background(), spec.Check{ID: "c", Name: "grid-fake-tool"}, env)
	if err != nil {
		t.Fatalf("Measure: unexpected error: %v", err)
	}
	if ev["found"] != true {
		t.Fatalf("evidence[found] = %v, want true", ev["found"])
	}
	if p, _ := ev["path"].(string); p == "" {
		t.Error("evidence[path] is empty, want the resolved absolute path")
	}
	if out, _ := ev["versionOutput"].(string); out != "grid-fake-tool version 9.9.9" {
		t.Errorf("evidence[versionOutput] = %q, want \"grid-fake-tool version 9.9.9\"", out)
	}
}

// TestToolInstalledNotFound proves a missing tool is ok, found:false — the
// common case a tool.installed check exists to measure, not an error.
func TestToolInstalledNotFound(t *testing.T) {
	env := runner.NewEnv(t.TempDir(), nil)
	ev, err := (toolInstalledChecker{}).Measure(context.Background(), spec.Check{
		ID: "c", Name: "grid-cli-definitely-not-a-real-tool-xyz",
	}, env)
	if err != nil {
		t.Fatalf("Measure: unexpected error: %v", err)
	}
	if ev["found"] != false {
		t.Errorf("evidence[found] = %v, want false", ev["found"])
	}
}

// TestToolInstalledDefaultVersionArgs proves the default version_args is
// ["--version"] when the check does not set its own (plan Assumption 9).
func TestToolInstalledDefaultVersionArgs(t *testing.T) {
	writeFakeTool(t, "grid-fake-tool-2", "stdout", "ok", 0)
	env := runner.NewEnv(t.TempDir(), nil)
	ev, err := (toolInstalledChecker{}).Measure(context.Background(), spec.Check{ID: "c", Name: "grid-fake-tool-2"}, env)
	if err != nil {
		t.Fatalf("Measure: unexpected error: %v", err)
	}
	if ev["versionOutput"] != "ok" {
		t.Fatalf("evidence[versionOutput] = %v; the fake tool only prints \"ok\" when invoked with --version, proving the default version_args was used", ev["versionOutput"])
	}
}

// TestToolInstalledCustomVersionArgs proves version_args overrides the
// default.
func TestToolInstalledCustomVersionArgs(t *testing.T) {
	dir := t.TempDir()
	var path string
	if runtime.GOOS == "windows" {
		path = filepath.Join(dir, "grid-fake-tool-3.bat")
		if err := os.WriteFile(path, []byte("@echo off\r\nif \"%1\"==\"version\" (echo custom-ok) else (echo wrong-args)\r\n"), 0o755); err != nil {
			t.Fatalf("write: %v", err)
		}
	} else {
		path = filepath.Join(dir, "grid-fake-tool-3")
		if err := os.WriteFile(path, []byte("#!/bin/sh\nif [ \"$1\" = \"version\" ]; then echo custom-ok; else echo wrong-args; fi\n"), 0o755); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	env := runner.NewEnv(t.TempDir(), nil)
	ev, err := (toolInstalledChecker{}).Measure(context.Background(), spec.Check{
		ID: "c", Name: "grid-fake-tool-3", VersionArgs: []string{"version"},
	}, env)
	if err != nil {
		t.Fatalf("Measure: unexpected error: %v", err)
	}
	if ev["versionOutput"] != "custom-ok" {
		t.Errorf("evidence[versionOutput] = %v, want \"custom-ok\" (version_args: [\"version\"] honoured)", ev["versionOutput"])
	}
}

// TestToolInstalledNeverShellsOutToWhich greps the package source for
// `which`/`where` invocations — tool.installed must resolve purely via
// exec.LookPath.
func TestToolInstalledNeverShellsOutToWhich(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		// Only the production sources need the guarantee; this very test
		// file necessarily mentions the literals to check for them.
		if e.IsDir() || filepath.Ext(e.Name()) != ".go" || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		content, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		src := string(content)
		if strings.Contains(src, `"which"`) || strings.Contains(src, `"where"`) {
			t.Errorf("%s shells out to which/where — tool.installed must use exec.LookPath only", e.Name())
		}
	}
}
