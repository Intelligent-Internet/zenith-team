package protocol

import "time"

// RuntimeUnusableReason says why a runtime the daemon found on a machine
// cannot run work there. It rides the daemon register payload and the
// user-plane Runtime projection: a runtime carrying one is stored offline
// regardless of the status the daemon announced, and the next registration
// without it clears it. The message is a human-facing sentence the daemon
// composed from the probe verdict — never a path.
type RuntimeUnusableReason struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// The closed unusable-reason vocabulary. The handler validates announced
// codes against RuntimeUnusableCodes exactly the way providers are checked,
// and PostgreSQL pins the same list.
const (
	// RuntimeUnusableCodeBelowMinimumVersion: the provider CLI parsed a
	// version below agent.MinVersions.
	RuntimeUnusableCodeBelowMinimumVersion = "below_minimum_version"
	// RuntimeUnusableCodeNotExecutable: the provider CLI was found but the
	// version probe could not execute it (permission denied, wrong format).
	RuntimeUnusableCodeNotExecutable = "not_executable"
)

// RuntimeUnusableCodes lists every accepted code.
var RuntimeUnusableCodes = []string{
	RuntimeUnusableCodeBelowMinimumVersion,
	RuntimeUnusableCodeNotExecutable,
}

// RuntimeUnusableMessageMaxLen bounds the message on the wire and on the
// row; the daemon composes messages well under it and the handler rejects
// longer ones.
const RuntimeUnusableMessageMaxLen = 256

// IsRuntimeUnusableCode reports whether code is an accepted unusable-reason
// code. Codes are exact and case-sensitive.
func IsRuntimeUnusableCode(code string) bool {
	for _, accepted := range RuntimeUnusableCodes {
		if code == accepted {
			return true
		}
	}
	return false
}

// RuntimeSignIn is the daemon's last look at whether the provider CLI on
// that machine is signed in. It rides the daemon register payload and the
// user-plane Runtime projection beside the version. A signed-out provider
// keeps its runtime online — runs on it fail with a provider_auth_or_access
// reason the person can read — but every roster and picker can now say so
// before a run is started. State is one of RuntimeSignInStates; Detail is a
// short human sentence the daemon composed ("Signed in with ChatGPT"), never
// a token or a path.
type RuntimeSignIn struct {
	State       string `json:"state"`
	Detail      string `json:"detail"`
	CheckedAt   string `json:"checkedAt"`
	BillingMode string `json:"billingMode,omitempty"`
}

// The closed sign-in vocabulary. unknown is what the daemon reports when the
// CLI's status command failed or could not be parsed; it is never inferred
// from a run failure.
const (
	RuntimeSignInSignedIn  = "signed_in"
	RuntimeSignInSignedOut = "signed_out"
	RuntimeSignInUnknown   = "unknown"
)

// RuntimeSignInStates lists every accepted state.
var RuntimeSignInStates = []string{
	RuntimeSignInSignedIn,
	RuntimeSignInSignedOut,
	RuntimeSignInUnknown,
}

// RuntimeSignInDetailMaxLen bounds the detail sentence on the wire and on
// the row.
const RuntimeSignInDetailMaxLen = 256

// IsRuntimeSignInState reports whether state is an accepted sign-in state.
func IsRuntimeSignInState(state string) bool {
	for _, accepted := range RuntimeSignInStates {
		if state == accepted {
			return true
		}
	}
	return false
}

// The closed credential-isolation vocabulary a runtime reports at
// registration and on every heartbeat. enforced: the
// daemon starts every provider process of this runtime in a macOS sandbox
// that cannot read or write a Zenith Team sign-in on the computer, and its
// probe proved it this round. not_enforced: anything else (Windows, Linux,
// a failed probe, or ZENITH_TEAM_DAEMON_CREDENTIAL_ISOLATION=not_enforced).
const (
	CredentialIsolationEnforced    = "enforced"
	CredentialIsolationNotEnforced = "not_enforced"
)

// IsCredentialIsolation reports whether value is an accepted report. Values
// are exact and case-sensitive.
func IsCredentialIsolation(value string) bool {
	return value == CredentialIsolationEnforced || value == CredentialIsolationNotEnforced
}

// ConnectorDiscoveryFreshWindow is how old a Runtime's Connector discovery
// may be for a Run claim to admit its apps. The server applies it; the daemon
// sizes its discovery keepalive to stay well inside it.
const ConnectorDiscoveryFreshWindow = 10 * time.Minute
