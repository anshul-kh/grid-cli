package upgrade

import "testing"

func TestSelectAsset(t *testing.T) {
	assets := []Asset{
		{Name: "grid-cli_linux_amd64.tar.gz"},
		{Name: "grid-cli_darwin_arm64.tar.gz"},
		{Name: "grid-cli_windows_amd64.zip"},
		{Name: "checksums.txt"},
	}
	if a, ok := selectAsset(assets, "linux", "amd64"); !ok || a.Name != "grid-cli_linux_amd64.tar.gz" {
		t.Errorf("selectAsset(linux, amd64) = %+v, %v", a, ok)
	}
	if a, ok := selectAsset(assets, "windows", "amd64"); !ok || a.Name != "grid-cli_windows_amd64.zip" {
		t.Errorf("selectAsset(windows, amd64) = %+v, %v", a, ok)
	}
	if a, ok := selectAsset(assets, "darwin", "arm64"); !ok || a.Name != "grid-cli_darwin_arm64.tar.gz" {
		t.Errorf("selectAsset(darwin, arm64) = %+v, %v", a, ok)
	}
	if _, ok := selectAsset(assets, "linux", "arm64"); ok {
		t.Error("selectAsset(linux, arm64) should not match anything published")
	}
	if _, ok := selectAsset(assets, "windows", "amd64_shouldnotmatch"); ok {
		t.Error("selectAsset must not substring-match an arch that isn't an exact token")
	}
}

func TestFindChecksumsAsset(t *testing.T) {
	assets := []Asset{{Name: "grid-cli_linux_amd64.tar.gz"}, {Name: "CHECKSUMS.txt"}}
	a, ok := findChecksumsAsset(assets)
	if !ok || a.Name != "CHECKSUMS.txt" {
		t.Errorf("findChecksumsAsset = %+v, %v", a, ok)
	}
	if _, ok := findChecksumsAsset(assets[:1]); ok {
		t.Error("findChecksumsAsset found a checksums file that isn't there")
	}
}

func TestParseChecksums(t *testing.T) {
	data := []byte("ABC123  grid-cli_linux_amd64.tar.gz\ndef456  grid-cli_darwin_arm64.tar.gz\n\n")
	sums := parseChecksums(data)
	if sums["grid-cli_linux_amd64.tar.gz"] != "abc123" {
		t.Errorf("parseChecksums lowercases the digest: got %+v", sums)
	}
	if sums["grid-cli_darwin_arm64.tar.gz"] != "def456" {
		t.Errorf("parseChecksums = %+v", sums)
	}
	if len(sums) != 2 {
		t.Errorf("parseChecksums returned %d entries, want 2: %+v", len(sums), sums)
	}
}
