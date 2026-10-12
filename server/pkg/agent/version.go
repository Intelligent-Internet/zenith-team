package agent

import (
	"cmp"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// MinVersions defines the minimum required CLI version for each agent type.
// Versions below these will be rejected during daemon registration.
var MinVersions = map[string]string{
	"claude": "2.0.0",
	"codex":  "0.100.0", // app-server --listen stdio:// added in 0.100.0
}

// Version is a parsed semantic version. Prerelease holds the dot-separated
// identifiers after the "-" ("alpha.15" in "0.148.0-alpha.15"); build
// metadata after a "+" is dropped, as SemVer says it never affects order.
type Version struct {
	Major, Minor, Patch int
	Prerelease          string
}

// versionRe matches version strings like "2.1.100", "v2.0.0",
// "2.1.100 (Claude Code)" or "codex-cli 0.148.0-alpha.15" — it extracts the
// first three numeric components and an optional prerelease.
var versionRe = regexp.MustCompile(`v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?`)

// ParseVersion extracts the first semantic version from a CLI banner.
func ParseVersion(raw string) (Version, error) {
	m := versionRe.FindStringSubmatch(raw)
	if m == nil {
		return Version{}, fmt.Errorf("cannot parse version %q", raw)
	}
	var parts [3]int
	for i := range parts {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return Version{}, fmt.Errorf("cannot parse version %q: %w", raw, err)
		}
		parts[i] = n
	}
	return Version{Major: parts[0], Minor: parts[1], Patch: parts[2], Prerelease: m[4]}, nil
}

// String renders major.minor.patch. A provider CLI prints a banner rather
// than a bare version ("codex-cli 0.153.4", "2.1.258 (Claude Code)"), so this
// is what a person should be shown when the gate reports a verdict about it.
func (v Version) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// IsPrerelease reports whether v is an alpha, beta or other pre-release
// build rather than a release.
func (v Version) IsPrerelease() bool {
	return v.Prerelease != ""
}

// lessThan compares major.minor.patch only. The minimum-version gate uses it,
// so a prerelease of the floor version still passes the floor.
func (v Version) lessThan(other Version) bool {
	if v.Major != other.Major {
		return v.Major < other.Major
	}
	if v.Minor != other.Minor {
		return v.Minor < other.Minor
	}
	return v.Patch < other.Patch
}

// Compare orders v against o by SemVer precedence: -1 when v is older, 0
// when they are equal, +1 when v is newer. A prerelease comes before the
// release it leads to, so 0.148.0-alpha.15 < 0.148.0 < 0.160.1.
func (v Version) Compare(o Version) int {
	if c := cmp.Compare(v.Major, o.Major); c != 0 {
		return c
	}
	if c := cmp.Compare(v.Minor, o.Minor); c != 0 {
		return c
	}
	if c := cmp.Compare(v.Patch, o.Patch); c != 0 {
		return c
	}
	return comparePrerelease(v.Prerelease, o.Prerelease)
}

// comparePrerelease orders two prerelease strings. No prerelease outranks
// any prerelease; otherwise identifiers are compared left to right, and when
// one list runs out first, the shorter list is older.
func comparePrerelease(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		if c := comparePrereleaseIdentifier(as[i], bs[i]); c != 0 {
			return c
		}
	}
	return cmp.Compare(len(as), len(bs))
}

// comparePrereleaseIdentifier orders one prerelease identifier pair: numbers
// by value, words alphabetically, and a number before a word. Numbers are
// compared by digit count first, so one too long for an int still orders
// right.
func comparePrereleaseIdentifier(a, b string) int {
	aNumeric, bNumeric := isNumericIdentifier(a), isNumericIdentifier(b)
	switch {
	case aNumeric && bNumeric:
		a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
		if c := cmp.Compare(len(a), len(b)); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	case aNumeric:
		return -1
	case bNumeric:
		return 1
	}
	return strings.Compare(a, b)
}

func isNumericIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// VersionAtLeast reports whether detected parses as a semantic version at
// or above minimum. Callers outside the registry (the codex sandbox gate)
// share the registry's version grammar through this instead of keeping a
// second regex.
func VersionAtLeast(detected, minimum string) (bool, error) {
	got, err := ParseVersion(detected)
	if err != nil {
		return false, err
	}
	floor, err := ParseVersion(minimum)
	if err != nil {
		return false, err
	}
	return !got.lessThan(floor), nil
}

// BelowMinimumError reports a version that parsed successfully and is below
// the configured minimum. It is a distinct type so callers can tell a
// CONFIRMED too-old verdict apart from "could not parse the version": only
// the former is evidence strong enough to act on (taking a runtime offline),
// while an unreadable version must be treated like any other failed
// detection and leave working runtimes alone.
type BelowMinimumError struct {
	AgentType string
	Detected  string
	Minimum   string
}

func (e *BelowMinimumError) Error() string {
	return fmt.Sprintf(
		"%s version %s is below minimum required %s — please upgrade",
		e.AgentType,
		e.Detected,
		e.Minimum,
	)
}

// CheckMinVersion validates that detectedVersion meets the minimum for
// agentType. Returns nil if the version is acceptable or no minimum is
// defined, a *BelowMinimumError when the version parsed and is confirmed too
// old, and a plain error when the version could not be parsed at all.
func CheckMinVersion(agentType, detectedVersion string) error {
	minRaw, ok := MinVersions[agentType]
	if !ok {
		return nil
	}
	minimum, err := ParseVersion(minRaw)
	if err != nil {
		return fmt.Errorf("invalid minimum version %q for %s: %w", minRaw, agentType, err)
	}
	detected, err := ParseVersion(detectedVersion)
	if err != nil {
		return fmt.Errorf("cannot parse detected %s version %q: %w", agentType, detectedVersion, err)
	}
	if detected.lessThan(minimum) {
		// Detected is the version the comparison used, not the banner it was
		// read from: this value reaches the roster, the agent's unavailability
		// reason and `zen daemon status`, where "Codex CLI codex-cli
		// 0.90.0" reads as a mistake.
		return &BelowMinimumError{AgentType: agentType, Detected: detected.String(), Minimum: minRaw}
	}
	return nil
}
