package protocol

// Provider vocabulary: zenith-team runs exactly two providers, and this list is
// the single authority the same way EntityType is for activity — server-side
// validation checks names against it, and agent.New constructs the matching
// runtime. Adding a provider starts here.
var SupportedProviders = []string{"claude", "codex"}

// IsSupportedProvider reports whether name is a supported provider. Names
// are exact and case-sensitive.
func IsSupportedProvider(name string) bool {
	for _, supported := range SupportedProviders {
		if name == supported {
			return true
		}
	}
	return false
}
