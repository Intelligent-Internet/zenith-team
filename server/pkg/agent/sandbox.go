package agent

import (
	"maps"
	"slices"
)

// Sandbox is a macOS Seatbelt profile a provider launch runs under. The
// daemon builds it; the adapter only wraps the command line,
// so every process the provider starts (its shell, zen, zenith-team-mcp, MCP
// servers) inherits the same walls.
type Sandbox struct {
	// Profile is the SBPL source handed to sandbox-exec -p. It names paths
	// only through (param "NAME"), never as literals.
	Profile string
	// Params are the profile's parameter values, passed as -D NAME=VALUE.
	// A path travels as its own argv entry, so spaces and quotes in it never
	// need escaping inside the profile.
	Params map[string]string
}

// Command returns the command line that starts execPath with args inside
// the sandbox, or execPath and args unchanged when s is nil. sandbox-exec
// applies the profile and then replaces itself with the provider, so the
// pid, the process group and every signal stay the provider's.
func (s *Sandbox) Command(execPath string, args []string) (string, []string) {
	if s == nil {
		return execPath, args
	}
	wrapped := make([]string, 0, 2*len(s.Params)+4+len(args))
	for _, name := range slices.Sorted(maps.Keys(s.Params)) {
		wrapped = append(wrapped, "-D", name+"="+s.Params[name])
	}
	wrapped = append(wrapped, "-p", s.Profile, "--", execPath)
	wrapped = append(wrapped, args...)
	return "/usr/bin/sandbox-exec", wrapped
}
