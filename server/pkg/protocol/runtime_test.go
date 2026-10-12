package protocol

import "testing"

// The unusable-reason code vocabulary is closed and exact: the handler
// rejects anything else at the boundary and PostgreSQL pins the same list.
func TestIsRuntimeUnusableCodeIsExact(t *testing.T) {
	t.Parallel()
	for _, code := range RuntimeUnusableCodes {
		if !IsRuntimeUnusableCode(code) {
			t.Fatalf("IsRuntimeUnusableCode(%q) = false, want the listed code accepted", code)
		}
	}
	if RuntimeUnusableCodeBelowMinimumVersion != "below_minimum_version" ||
		RuntimeUnusableCodeNotExecutable != "not_executable" {
		t.Fatalf("code constants = %q, %q, want the wire spellings",
			RuntimeUnusableCodeBelowMinimumVersion, RuntimeUnusableCodeNotExecutable)
	}
	for _, rejected := range []string{"", "BELOW_MINIMUM_VERSION", " not_executable", "not_executable ", "broken"} {
		if IsRuntimeUnusableCode(rejected) {
			t.Fatalf("IsRuntimeUnusableCode(%q) = true, want rejected", rejected)
		}
	}
	if RuntimeUnusableMessageMaxLen != 256 {
		t.Fatalf("RuntimeUnusableMessageMaxLen = %d, want 256", RuntimeUnusableMessageMaxLen)
	}
}

// The sign-in vocabulary is closed and exact for the same reason.
func TestIsRuntimeSignInStateIsExact(t *testing.T) {
	t.Parallel()
	for _, state := range RuntimeSignInStates {
		if !IsRuntimeSignInState(state) {
			t.Fatalf("IsRuntimeSignInState(%q) = false, want the listed state accepted", state)
		}
	}
	if RuntimeSignInSignedIn != "signed_in" || RuntimeSignInSignedOut != "signed_out" ||
		RuntimeSignInUnknown != "unknown" {
		t.Fatal("sign-in state constants do not match the wire spellings")
	}
	for _, rejected := range []string{"", "SIGNED_IN", "signed-in", " unknown", "expired"} {
		if IsRuntimeSignInState(rejected) {
			t.Fatalf("IsRuntimeSignInState(%q) = true, want rejected", rejected)
		}
	}
}
