package upgrade

import (
	"strconv"
	"strings"
)

// semver is a parsed MAJOR.MINOR.PATCH version. grid upgrade's comparisons
// are numeric-only — a release tag never needs pre-release precedence
// rules, just "is this a bigger release than what's installed".
type semver struct {
	major, minor, patch int
	ok                  bool
}

// parseSemver parses a version string, tolerating a leading "v" and an
// optional "-<prerelease>"/"+<build>" suffix (e.g. "v1.2.3-beta.1"). ok is
// false for anything that isn't at least MAJOR.MINOR.PATCH numeric —
// notably the local dev build's "dev" placeholder.
func parseSemver(s string) semver {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.SplitN(s, ".", 3)
	if len(parts) != 3 {
		return semver{}
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return semver{}
		}
		nums[i] = n
	}
	return semver{major: nums[0], minor: nums[1], patch: nums[2], ok: true}
}

// newerThan reports whether latest is a valid, strictly greater semver than
// current. An invalid latest (a malformed tag) is never newer. An invalid
// current (e.g. "dev" for a local build) treats any valid latest as newer,
// so `grid upgrade` is useful on development builds too.
func newerThan(latest, current string) bool {
	l := parseSemver(latest)
	if !l.ok {
		return false
	}
	c := parseSemver(current)
	if !c.ok {
		return true
	}
	if l.major != c.major {
		return l.major > c.major
	}
	if l.minor != c.minor {
		return l.minor > c.minor
	}
	return l.patch > c.patch
}
