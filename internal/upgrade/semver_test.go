package upgrade

import "testing"

func TestNewerThan(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"v1.2.0", "1.1.9", true},
		{"1.1.9", "v1.2.0", false},
		{"2.0.0", "1.9.9", true},
		{"1.0.0", "1.0.0", false},
		{"1.0.1", "1.0.0", true},
		{"v1.0.1-rc.1", "1.0.0", true},
		{"garbage", "1.0.0", false},
		{"1.0.0", "dev", true},
		{"garbage", "dev", false},
	}
	for _, c := range cases {
		if got := newerThan(c.latest, c.current); got != c.want {
			t.Errorf("newerThan(%q, %q) = %v, want %v", c.latest, c.current, got, c.want)
		}
	}
}

func TestParseSemver(t *testing.T) {
	v := parseSemver("v1.2.3-beta.1")
	if !v.ok || v.major != 1 || v.minor != 2 || v.patch != 3 {
		t.Errorf("parseSemver(v1.2.3-beta.1) = %+v", v)
	}
	if parseSemver("not-a-version").ok {
		t.Error("parseSemver(not-a-version).ok = true, want false")
	}
	if parseSemver("1.2").ok {
		t.Error("parseSemver(1.2).ok = true, want false (needs three components)")
	}
}
