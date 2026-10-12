package mailbox

import (
	"errors"
	"testing"
)

func TestParseLocalPart(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
		err   error
	}{
		{name: "normalizes case and whitespace", input: "  Product.Team  ", want: "product.team"},
		{name: "allows separators", input: "sales-emea_2", want: "sales-emea_2"},
		{name: "minimum length", input: "abc", want: "abc"},
		{name: "too short", input: "ab", err: ErrInvalidLocalPart},
		{name: "leading separator", input: "-sales", err: ErrInvalidLocalPart},
		{name: "trailing separator", input: "sales_", err: ErrInvalidLocalPart},
		{name: "adjacent separators", input: "sales..team", err: ErrInvalidLocalPart},
		{name: "unicode", input: "sáles", err: ErrInvalidLocalPart},
		{name: "space", input: "sales team", err: ErrInvalidLocalPart},
		{name: "reserved postmaster", input: "POSTMASTER", err: ErrReservedLocalPart},
		{name: "reserved mailer daemon", input: "mailer-daemon", err: ErrReservedLocalPart},
		{name: "reserved no reply alias", input: "no-reply", err: ErrReservedLocalPart},
		{
			name:  "too long",
			input: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			err:   ErrInvalidLocalPart,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseLocalPart(test.input)
			if !errors.Is(err, test.err) {
				t.Fatalf("ParseLocalPart(%q) error = %v, want %v", test.input, err, test.err)
			}
			if got != test.want {
				t.Fatalf("ParseLocalPart(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestSanitizeHTMLRemovesActiveContentAndRemoteImages(t *testing.T) {
	t.Parallel()

	input := `<p onclick="steal()">Hello <strong>team</strong></p>` +
		`<script>steal()</script><img src="https://tracker.test/pixel">` +
		`<a href="javascript:steal()">bad</a><a href="https://boardly.test/help">help</a>`
	want := `<p>Hello <strong>team</strong></p><a>bad</a>` +
		`<a href="https://boardly.test/help">help</a>`
	if got := sanitizeHTML(input); got != want {
		t.Fatalf("sanitizeHTML() = %q, want %q", got, want)
	}
}

func TestValidMetadataTextRejectsPostgresUnsupportedNUL(t *testing.T) {
	t.Parallel()

	if validMetadataText("provider\x00metadata", 100, false) {
		t.Fatal("validMetadataText() accepted an embedded NUL")
	}
}
