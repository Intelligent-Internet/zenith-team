package daemon

import "testing"

func TestSignInBillingMode(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ provider, input, want string }{
		{"claude", `{"loggedIn":true,"authMethod":"claude.ai","subscriptionType":"max"}`, "subscription"},
		{"claude", `{"loggedIn":true,"authMethod":"api_key"}`, "api"},
		{"claude", `{"loggedIn":true,"authMethod":"future"}`, "unknown"},
		{"claude", `{"loggedIn":false}`, "unknown"},
		{"codex", "Logged in using ChatGPT", "subscription"},
		{"codex", "Logged in using an API key", "api"},
		{"codex", "Not logged in", "unknown"},
	} {
		t.Run(test.provider+"/"+test.input, func(t *testing.T) {
			t.Parallel()
			got := parseCodexSignIn(test.input, nil)
			if test.provider == "claude" {
				got = parseClaudeSignIn([]byte(test.input), nil)
			}
			if got.BillingMode != test.want {
				t.Fatalf("billing mode = %q, want %q", got.BillingMode, test.want)
			}
		})
	}
}
