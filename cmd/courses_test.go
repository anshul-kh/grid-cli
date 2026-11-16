package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/anshul-kh/grid-cli/internal/api"
	"github.com/anshul-kh/grid-cli/internal/api/stub"
	"github.com/anshul-kh/grid-cli/internal/auth"
)

// TestCoursesExits3WithoutCredentials: no stored session → exit 3, auth
// required (plan's "Exit 3 when unauthenticated").
func TestCoursesExits3WithoutCredentials(t *testing.T) {
	_, srv := newStubCmdServer(t)
	isolateHome(t)
	setCmdCfg(srv.URL+"/api/v1", false)

	_, _, err := captureOutput(t, func() error {
		return runCourses(coursesCmd, nil)
	})
	if code := exitCodeOf(t, err); code != 3 {
		t.Fatalf("courses without credentials: exit code = %d (err %v), want 3", code, err)
	}
}

// TestCoursesExits4OnServerError: a 500 from the server maps to exit 4
// (plan's "4 on server error"), not an auth or usage error.
func TestCoursesExits4OnServerError(t *testing.T) {
	st, srv := newStubCmdServer(t)
	credsPath := isolateHome(t)
	if err := auth.NewStore(credsPath).Save(auth.Credentials{SessionKey: stub.SessionKey, User: stubTestUser()}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	st.ForceStatus("/cli/courses", 500)
	setCmdCfg(srv.URL+"/api/v1", false)

	_, _, err := captureOutput(t, func() error {
		return runCourses(coursesCmd, nil)
	})
	if code := exitCodeOf(t, err); code != 4 {
		t.Fatalf("courses on server error: exit code = %d (err %v), want 4", code, err)
	}
}

// TestCoursesHappyPathRendersTableAndJSON proves grid courses renders the
// stub's enrolled course with its progress, and that --json emits the raw
// {"courses":[...]} response object (GRID_SPEC §5.4b's confirmed shape).
func TestCoursesHappyPathRendersTableAndJSON(t *testing.T) {
	_, srv := newStubCmdServer(t)
	credsPath := isolateHome(t)
	if err := auth.NewStore(credsPath).Save(auth.Credentials{SessionKey: stub.SessionKey, User: stubTestUser()}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	setCmdCfg(srv.URL+"/api/v1", false)

	stdout, _, err := captureOutput(t, func() error {
		return runCourses(coursesCmd, nil)
	})
	if err != nil {
		t.Fatalf("runCourses: %v", err)
	}
	for _, want := range []string{"Build a Redis core in Go", "INTERMEDIATE", "25%", "8 lessons"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("courses stdout missing %q:\n%s", want, stdout)
		}
	}

	setCmdCfg(srv.URL+"/api/v1", true)
	stdout, _, err = captureOutput(t, func() error {
		return runCourses(coursesCmd, nil)
	})
	if err != nil {
		t.Fatalf("runCourses --json: %v", err)
	}
	var got api.CoursesResponse
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("courses --json stdout is not valid JSON: %v\n%s", err, stdout)
	}
	if len(got.Courses) != 1 || got.Courses[0].Slug != stub.CourseSlug {
		t.Errorf("courses --json = %+v", got)
	}
	if got.Courses[0].Enrollment.ProgressPct != 25 {
		t.Errorf("courses --json progressPct = %d, want 25", got.Courses[0].Enrollment.ProgressPct)
	}
}
