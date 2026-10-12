package social

import "testing"

func TestDesktopReturnOnlyAcceptsLoopbackCallback(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		uri   string
		valid bool
	}{
		{"http://127.0.0.1:43210/social/callback", true},
		{"https://evil.example/social/callback", false},
		{"http://127.0.0.1.evil.example:43210/social/callback", false},
		{"http://127.0.0.1:43210/social/callback?next=evil", false},
		{"http://user@127.0.0.1:43210/social/callback", false},
		{"http://127.0.0.1:80/social/callback", false},
		{"http://127.0.0.1:43210/other", false},
	} {
		if got := validDesktopRedirect(tc.uri); got != tc.valid {
			t.Errorf("validDesktopRedirect(%q) = %v, want %v", tc.uri, got, tc.valid)
		}
	}
}
