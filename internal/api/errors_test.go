package api

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

// TestTypedErrorsMapDistinctStatuses proves every handled status maps to a
// distinct Go error value with a distinct actionable hint.
func TestTypedErrorsMapDistinctStatuses(t *testing.T) {
	cases := []struct {
		status   int
		sentinel error
	}{
		{401, ErrUnauthorized},
		{403, ErrDenied},
		{409, ErrNonceAlreadyUsed},
		{410, ErrAttemptExpired},
		{413, ErrTooLarge},
		{428, ErrPending},
		{429, ErrSlowDown},
		{500, ErrServer},
		{503, ErrServer},
	}
	errs := make([]error, 0, len(cases))
	for _, tc := range cases {
		errs = append(errs, statusError(tc.status, "machine-code", "", nil, 0))
	}
	for i, tc := range cases {
		if !errors.Is(errs[i], tc.sentinel) {
			t.Errorf("status %d: errors.Is(%v, sentinel) = false", tc.status, errs[i])
		}
		for j, other := range cases {
			if i == j || tc.sentinel == other.sentinel {
				continue
			}
			if errors.Is(errs[i], other.sentinel) {
				t.Errorf("status %d and %d must map to distinct Go error values", tc.status, other.status)
			}
		}
	}
}

func TestTypedErrorFieldsAndHints(t *testing.T) {
	var err error = statusError(410, "attempt_expired", "Attempt expired", nil, 0)
	se, ok := err.(*StatusError)
	if !ok {
		t.Fatalf("statusError returned %T, want *StatusError", err)
	}
	if se.Kind != KindAttemptExpired || se.StatusCode != 410 || se.Code != "attempt_expired" {
		t.Errorf("StatusError fields = %+v", se)
	}
	if se.Message != "Attempt expired" {
		t.Errorf("Message = %q, want the server's human message", se.Message)
	}
	if want := "This attempt expired. Run `grid run <id>` again."; UserHint(err) != want {
		t.Errorf("UserHint = %q, want %q", UserHint(err), want)
	}
	// The top-level poll `status` sibling overrides the default machine code.
	err = statusError(428, "pending", "Authorization pending", nil, 0)
	if se := err.(*StatusError); se.Code != "pending" || se.Message != "Authorization pending" {
		t.Errorf("poll error fields = %+v, want code pending with the human message", se)
	}
	// Default machine code when the code is empty: 409 defaults to
	// nonce_already_used, the more common replay case.
	err = statusError(409, "", "", nil, 0)
	if se := err.(*StatusError); se.Code != "nonce_already_used" || se.Kind != KindNonceAlreadyUsed {
		t.Errorf("default code/kind for 409 = %q/%v", se.Code, se.Kind)
	}
	// The `code` sibling disambiguates the second named 409 outcome
	// (GRID_SYNC_CLAUDE.md 2026-11-12 06:00).
	err = statusError(409, "attempt_already_passed", "Attempt already passed", nil, 0)
	if se := err.(*StatusError); se.Kind != KindAttemptAlreadyPassed || !errors.Is(se, ErrAttemptAlreadyPassed) {
		t.Errorf("409 with code=attempt_already_passed = %+v, want KindAttemptAlreadyPassed", se)
	}
	// Retry-After is carried on 429 errors.
	err = statusError(429, "slow_down", "", nil, 2*time.Second)
	if se := err.(*StatusError); se.RetryAfter != 2*time.Second {
		t.Errorf("RetryAfter = %v, want 2s", se.RetryAfter)
	}
}

// TestStatusErrorNotEnrolledIsDistinctFromDenied proves the GRID_SPEC
// §5.2/§5.3/§6 split: a bare 403 is the generic ErrDenied, but a 403 with
// code=="not_enrolled" (the shape of a plain ApiError's errors[0].code) maps
// to the distinct ErrNotEnrolled instead — never both at once.
func TestStatusErrorNotEnrolledIsDistinctFromDenied(t *testing.T) {
	denied := statusError(403, "", "Forbidden", nil, 0)
	if denied.Kind != KindDenied || !errors.Is(denied, ErrDenied) {
		t.Errorf("bare 403 = %+v, want KindDenied/ErrDenied", denied)
	}
	if errors.Is(denied, ErrNotEnrolled) {
		t.Error("a bare 403 must not also match ErrNotEnrolled")
	}

	notEnrolled := statusError(403, "not_enrolled", "Enroll in this course to open its lessons.", nil, 0)
	if notEnrolled.Kind != KindNotEnrolled || !errors.Is(notEnrolled, ErrNotEnrolled) {
		t.Errorf("403 code=not_enrolled = %+v, want KindNotEnrolled/ErrNotEnrolled", notEnrolled)
	}
	if errors.Is(notEnrolled, ErrDenied) {
		t.Error("a not_enrolled 403 must not also match the generic ErrDenied")
	}
}

func TestEveryTypedErrorHasDistinctHint(t *testing.T) {
	sentinels := []error{
		ErrAttemptExpired, ErrNonceAlreadyUsed, ErrAttemptAlreadyPassed, ErrUnauthorized, ErrDenied,
		ErrNotEnrolled, ErrSlowDown, ErrPending, ErrTooLarge, ErrServer, ErrNetwork,
	}
	hints := map[string]string{}
	for _, s := range sentinels {
		hint := UserHint(s)
		if hint == "" {
			t.Errorf("%v: hint is empty — every typed error needs an actionable hint", s)
		}
		if prev, dup := hints[hint]; dup {
			t.Errorf("%v and %v share the same hint", prev, s)
		}
		hints[hint] = s.Error()
	}
}

func TestUserHintForNonAPIError(t *testing.T) {
	if UserHint(errors.New("something else")) != "" {
		t.Error("UserHint should be empty for non-API errors")
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	if got := parseRetryAfter("", now); got != 0 {
		t.Errorf("empty header = %v, want 0", got)
	}
	if got := parseRetryAfter("2", now); got != 2*time.Second {
		t.Errorf("delay-seconds = %v, want 2s", got)
	}
	if got := parseRetryAfter("0", now); got != 0 {
		t.Errorf("zero = %v, want 0", got)
	}
	if got := parseRetryAfter("-3", now); got != 0 {
		t.Errorf("negative = %v, want 0", got)
	}
	if got := parseRetryAfter("nonsense", now); got != 0 {
		t.Errorf("nonsense = %v, want 0", got)
	}
	if got := parseRetryAfter(now.Add(time.Minute).UTC().Format(http.TimeFormat), now); got != time.Minute {
		t.Errorf("HTTP-date = %v, want 1m", got)
	}
	// A date in the past means "retry now".
	if got := parseRetryAfter(now.Add(-time.Hour).UTC().Format(http.TimeFormat), now); got != 0 {
		t.Errorf("past HTTP-date = %v, want 0", got)
	}
}
