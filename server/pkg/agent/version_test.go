package agent

import (
	"errors"
	"strings"
	"testing"
)

// A provider CLI prints a banner, not a bare version: `codex --version` says
// "codex-cli 0.153.4" and `claude --version` says "2.1.258 (Claude Code)".
// The verdict a person reads — on the roster, in the agent's unavailability
// reason and under `zen daemon status` — is built from Detected, so it
// must carry the version the gate actually compared, never the banner. With
// the banner it read "Codex CLI codex-cli 0.90.0 is below the minimum
// 0.100.0".
func TestCheckMinVersionReportsTheParsedVersionNotTheCLIBanner(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		raw      string
		want     string
	}{
		{name: "codex banner", provider: "codex", raw: "codex-cli 0.90.0", want: "0.90.0"},
		{name: "claude banner", provider: "claude", raw: "1.9.3 (Claude Code)", want: "1.9.3"},
		{name: "leading v", provider: "codex", raw: "v0.99.9", want: "0.99.9"},
		{name: "bare version", provider: "codex", raw: "0.12.0", want: "0.12.0"},
		{name: "trailing prerelease", provider: "codex", raw: "codex-cli 0.90.0-beta.2", want: "0.90.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckMinVersion(tc.provider, tc.raw)
			var tooOld *BelowMinimumError
			if !errors.As(err, &tooOld) {
				t.Fatalf("CheckMinVersion(%q, %q) = %v, want a BelowMinimumError", tc.provider, tc.raw, err)
			}
			if tooOld.Detected != tc.want {
				t.Fatalf("Detected = %q, want the parsed version %q (never the CLI banner)", tooOld.Detected, tc.want)
			}
			// The sentence a person reads must name the version once.
			if message := tooOld.Error(); strings.Contains(message, "codex-cli") ||
				strings.Contains(message, "Claude Code") {
				t.Fatalf("Error() = %q, want no banner text", message)
			}
		})
	}
}

// A version at or above the minimum is not an error at all, banner included.
func TestCheckMinVersionAcceptsASupportedBanner(t *testing.T) {
	for _, raw := range []string{"codex-cli 0.153.4", "2.1.258 (Claude Code)", "v2.0.0"} {
		provider := "codex"
		if strings.Contains(raw, "Claude") || strings.HasPrefix(raw, "v2") {
			provider = "claude"
		}
		if err := CheckMinVersion(provider, raw); err != nil {
			t.Fatalf("CheckMinVersion(%q, %q) = %v, want nil", provider, raw, err)
		}
	}
}

// An unreadable version stays a plain error, never a BelowMinimumError: only
// a confirmed too-old verdict may take a runtime offline.
func TestCheckMinVersionKeepsAnUnreadableVersionDistinct(t *testing.T) {
	err := CheckMinVersion("codex", "codex-cli (unknown build)")
	if err == nil {
		t.Fatal("CheckMinVersion() = nil, want an error for an unparseable version")
	}
	var tooOld *BelowMinimumError
	if errors.As(err, &tooOld) {
		t.Fatalf("CheckMinVersion() = %T, want a plain error, not a confirmed too-old verdict", err)
	}
}

// The daemon ranks several installed copies of one CLI by Compare, so it
// must follow SemVer precedence, prereleases included: a ChatGPT app build
// "0.148.0-alpha.15" is older than the 0.148.0 release.
func TestVersionCompareFollowsSemVerPrecedence(t *testing.T) {
	ascending := [][]string{
		{"codex-cli 0.148.0-alpha.15", "codex-cli 0.148.0", "codex-cli 0.160.1"},
		{"0.151.0-alpha.7.2", "0.151.0-alpha.15"},
		// The SemVer specification's own example chain.
		{
			"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta",
			"1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0",
		},
		{"0.9.9", "0.10.0"},
		{"2.1.9", "2.1.258 (Claude Code)"},
		{"0.0.0", "0.1.0"},
		{"1.0.0-alpha.100", "1.0.0-alpha.99999999999999999999"},
	}
	for _, chain := range ascending {
		for i := 1; i < len(chain); i++ {
			older, newer := mustParseVersion(t, chain[i-1]), mustParseVersion(t, chain[i])
			if got := older.Compare(newer); got != -1 {
				t.Errorf("%q.Compare(%q) = %d, want -1", chain[i-1], chain[i], got)
			}
			if got := newer.Compare(older); got != 1 {
				t.Errorf("%q.Compare(%q) = %d, want 1", chain[i], chain[i-1], got)
			}
		}
	}
	for _, pair := range [][2]string{
		{"codex-cli 0.160.1", "0.160.1"},
		{"0.160.1+build.5", "0.160.1"},
		{"v2.0.0", "2.0.0"},
	} {
		if got := mustParseVersion(t, pair[0]).Compare(mustParseVersion(t, pair[1])); got != 0 {
			t.Errorf("%q.Compare(%q) = %d, want 0", pair[0], pair[1], got)
		}
	}
}

func TestParseVersionReadsTheCodexBanner(t *testing.T) {
	got := mustParseVersion(t, "codex-cli 0.148.0-alpha.15")
	want := Version{Major: 0, Minor: 148, Patch: 0, Prerelease: "alpha.15"}
	if got != want {
		t.Fatalf("ParseVersion() = %+v, want %+v", got, want)
	}
	if !got.IsPrerelease() || got.String() != "0.148.0" {
		t.Fatalf("IsPrerelease() = %v, String() = %q; want true, \"0.148.0\"", got.IsPrerelease(), got.String())
	}
	if release := mustParseVersion(t, "2.1.258 (Claude Code)"); release.IsPrerelease() {
		t.Fatalf("ParseVersion(claude banner) = %+v, want a release", release)
	}
}

func TestParseVersionRejectsAnUnreadableBanner(t *testing.T) {
	for _, raw := range []string{"", "codex-cli (unknown build)", "codex-cli 99999999999999999999.0.0"} {
		if got, err := ParseVersion(raw); err == nil {
			t.Errorf("ParseVersion(%q) = %+v, want an error", raw, got)
		}
	}
}

// The minimum-version gate compares major.minor.patch only: a prerelease of
// the floor version passes it, as it did before prereleases were parsed.
func TestCheckMinVersionIgnoresAPrereleaseAtTheFloor(t *testing.T) {
	if err := CheckMinVersion("codex", "codex-cli 0.100.0-alpha.1"); err != nil {
		t.Fatalf("CheckMinVersion() = %v, want nil for a prerelease of the floor", err)
	}
	ok, err := VersionAtLeast("0.150.0-alpha.1", "0.150.0")
	if err != nil || !ok {
		t.Fatalf("VersionAtLeast() = %v, %v; want true", ok, err)
	}
}

func mustParseVersion(t *testing.T, raw string) Version {
	t.Helper()
	v, err := ParseVersion(raw)
	if err != nil {
		t.Fatalf("ParseVersion(%q): %v", raw, err)
	}
	return v
}
