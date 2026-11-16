package workspace

import "testing"

func TestParseMode(t *testing.T) {
	cases := []struct {
		in   string
		want Mode
	}{
		{"none", ModeNone},
		{"folder", ModeFolder},
		{"repo", ModeRepo},
		{"bundle", ModeBundle},
		{"", ModeFolder},      // GRID_SPEC §4: absent workspace: block defaults to folder.
		{"bogus", ModeFolder}, // unknown server value never escalates privilege.
		{"NONE", ModeFolder},  // case-sensitive: the spec's literals are lowercase only.
	}
	for _, c := range cases {
		if got := ParseMode(c.in); got != c.want {
			t.Errorf("ParseMode(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestModeCreatesDir(t *testing.T) {
	cases := []struct {
		mode Mode
		want bool
	}{
		{ModeNone, false},
		{ModeFolder, true},
		{ModeRepo, true},
		{ModeBundle, true},
	}
	for _, c := range cases {
		if got := c.mode.CreatesDir(); got != c.want {
			t.Errorf("%q.CreatesDir() = %v, want %v", c.mode, got, c.want)
		}
	}
}

func TestModeNeedsGit(t *testing.T) {
	cases := []struct {
		mode Mode
		want bool
	}{
		{ModeNone, false},
		{ModeFolder, false},
		{ModeRepo, true},
		{ModeBundle, true},
	}
	for _, c := range cases {
		if got := c.mode.NeedsGit(); got != c.want {
			t.Errorf("%q.NeedsGit() = %v, want %v", c.mode, got, c.want)
		}
	}
}
