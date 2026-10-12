package execution

import "testing"

func TestFrozenStudioEnabledReadsTheFrozenAgentBrief(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		context string
		want    bool
	}{
		{name: "studio", context: `{"agent":{"capabilities":{"enabledTools":["zenith-studio"]}}}`, want: true},
		{name: "no tools", context: `{"agent":{"capabilities":{"enabledTools":[]}}}`},
		{name: "no agent brief", context: `{"prompt":"hello"}`},
	} {
		if got := frozenStudioEnabled([]byte(tc.context)); got != tc.want {
			t.Errorf("%s: frozenStudioEnabled() = %v, want %v", tc.name, got, tc.want)
		}
	}
}
