package conversation

import "testing"

func TestCanonicalDiscussionEmoji(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, input, want string }{
		{"emoji", "👍", "👍"},
		{"skin tone", "👍🏽", "👍🏽"},
		{"presentation alias", "❤", "❤️"},
		{"flag", "🇻🇳", "🇻🇳"},
		{"family", "👨‍👩‍👧‍👦", "👨‍👩‍👧‍👦"},
		{"text", "yes", ""},
		{"multiple", "👍👍", ""},
		{"standalone modifier", "🏽", ""},
		{"control", "👍\n", ""},
		{"empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := canonicalDiscussionEmoji(tc.input)
			if got != tc.want || (err == nil) != (tc.want != "") {
				t.Fatalf("canonical = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}
