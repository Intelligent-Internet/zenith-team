package company

import "testing"

func TestProjectFileLimits(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, filename, contentType string
		size                        int64
		logo, want                  bool
	}{
		{"logo", "logo.png", "image/png", 5 << 20, true, true},
		{"oversized logo", "logo.png", "image/png", 5<<20 + 1, true, false},
		{"SVG logo", "logo.svg", "image/svg+xml", 100, true, false},
		{"PDF", "brief.pdf", "application/pdf", 20 << 20, false, true},
		{"oversized document", "brief.pdf", "application/pdf", 20<<20 + 1, false, false},
		{"empty document", "brief.txt", "text/plain", 0, false, false},
		{"wrong MIME", "brief.pdf", "text/html", 100, false, false},
		{"unsupported file", "archive.zip", "application/zip", 100, false, false},
		{"Markdown", "brief.md", "application/octet-stream", 100, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := validProjectFile(
				test.filename,
				test.contentType,
				test.size,
				test.logo,
			); got != test.want {
				t.Fatalf("validProjectFile() = %v, want %v", got, test.want)
			}
		})
	}
}
