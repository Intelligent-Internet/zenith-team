package execution

import "testing"

func TestNormalizePublicationSubdomain(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		input string
		want  string
	}{
		{" My-App ", "my-app"},
		{"app42", "app42"},
		{"a", ""},
		{"api", ""},
		{"www", ""},
		{"xn--test", ""},
		{"a.b", ""},
		{"-app", ""},
		{"app-", ""},
		{"https://app", ""},
		{"app/path", ""},
	} {
		t.Run(test.input, func(t *testing.T) {
			t.Parallel()
			got, err := normalizePublicationSubdomain(test.input)
			if got != test.want || (err != nil) != (test.want == "") {
				t.Fatalf(
					"normalizePublicationSubdomain(%q) = %q, %v; want %q",
					test.input,
					got,
					err,
					test.want,
				)
			}
		})
	}
}
