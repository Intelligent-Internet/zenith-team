package conversation

import (
	"testing"

	"github.com/google/uuid"
)

func TestStudioOrigin(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ input, want string }{
		{"HTTPS://EXAMPLE.COM:443/", "https://example.com"},
		{"http://localhost:8000/", "http://localhost:8000"},
		{"http://127.0.0.1:80", "http://127.0.0.1"},
		{"http://[::1]:80/", "http://[::1]"},
		{"http://example.com", ""},
		{"https://u:p@example.com", ""},
		{"https://example.com/path", ""},
		{"https://example.com?x", ""},
		{"https://example.com#x", ""},
		{"https://%65xample.com", ""},
		{"https://example.com/../", ""},
		{"http://127.1", ""},
	} {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			got, err := canonicalStudioOrigin(tt.input)
			if got != tt.want || (err != nil) != (tt.want == "") {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestStudioCanonicalReceiptHash(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ description, want string }{
		{"Unicode 漢 <>&\u2028\u2029", "8c28e15b7cd311a62d1ee7fd1a04482efbd680fb87bdb15018c2539ce9be1d4c"},
		{`literal \u2028 and \u2029`, "b0a44f7d23dfec6a561dfc601ba237eab42bf48ab1dad0a21e619ed97ec3341b"},
	} {
		got, err := studioRequestHash(
			StudioReceiptRequest{
				Namespace:     uuid.MustParse("11111111-1111-1111-1111-111111111111"),
				ReservationID: uuid.MustParse("22222222-2222-2222-2222-222222222222"),
				Title:         "Hash",
				Description:   tt.description,
			},
		)
		if err != nil || got != tt.want {
			t.Fatalf("hash for %q = %s, %v; want %s", tt.description, got, err, tt.want)
		}
	}
}
