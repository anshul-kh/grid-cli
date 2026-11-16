package ui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// SummariseEvidence renders a one-line, human-readable summary of what a
// checker actually observed — "exists=false", "exit=1", "found=true go1.25".
//
// This exists because `grid check` used to print a green "ok" next to a
// check's title and nothing else, which read as "this check passed". It does
// not mean that: `ok` means the measurement was *taken*, and a measurement of
// "the directory is absent" is a perfectly successful measurement. A learner
// staring at a green "ok" beside "logs/ directory exists" reasonably concluded
// the directory existed when it did not.
//
// The CLI still never judges (GRID_SPEC §1 principle 1) — it cannot, because
// the public spec withholds both the expected values and `negate`, so
// `exists=false` may be exactly what a check wants. Showing the observation
// instead of a verdict is the honest middle: the learner can see the facts and
// draw their own conclusion, and the server remains the only judge.
//
// Returns "" when there is nothing worth showing, so callers can omit the
// column entirely rather than printing an empty bracket.
func SummariseEvidence(evidence map[string]any) string {
	if len(evidence) == 0 {
		return ""
	}

	// Ordered by how much the field tells a learner at a glance. The first
	// match wins, so a checker that reports several of these shows the most
	// decisive one rather than an unreadable dump of all of them.
	for _, key := range []string{"found", "exists", "tracked", "set", "matches", "exitCode", "status", "count"} {
		raw, ok := evidence[key]
		if !ok {
			continue
		}

		label := key
		if key == "exitCode" {
			label = "exit"
		}

		summary := fmt.Sprintf("%s=%s", label, formatEvidenceValue(raw))

		// A version string next to found=true turns "is it installed?" into
		// "which one did it find?", which is the actual question when a
		// version-matching check fails.
		if extra := firstString(evidence, "versionOutput", "version"); extra != "" && (key == "found" || key == "exists") {
			summary += " " + truncateOneLine(extra, 24)
		}
		return summary
	}

	// Composite evidence (dir.tree, git.has_commit) has no single decisive
	// field — a count is the useful summary.
	for _, key := range []string{"entries", "commits"} {
		if list, ok := evidence[key].([]any); ok {
			return fmt.Sprintf("%s=%d", key, len(list))
		}
	}

	return ""
}

func formatEvidenceValue(raw any) string {
	switch v := raw.(type) {
	case bool:
		return strconv.FormatBool(v)
	case string:
		return truncateOneLine(v, 24)
	case float64:
		// JSON round-trips every number as float64; render whole numbers
		// (exit codes, HTTP statuses) without a misleading ".000000".
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'g', -1, 64)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	default:
		return truncateOneLine(fmt.Sprintf("%v", v), 24)
	}
}

func firstString(evidence map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := evidence[k].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// truncateOneLine collapses a multi-line value to its first line and caps it,
// so a command that printed 200 lines cannot wreck the table.
func truncateOneLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return Truncate(s, max)
}

// EvidenceKeys returns the evidence field names, sorted — used by tests and
// --verbose output where the whole payload is worth showing.
func EvidenceKeys(evidence map[string]any) []string {
	keys := make([]string, 0, len(evidence))
	for k := range evidence {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
