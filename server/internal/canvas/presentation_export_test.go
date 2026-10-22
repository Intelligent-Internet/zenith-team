package canvas

import (
	"strings"
	"testing"
)

func TestRenderPresentationHTML(t *testing.T) {
	markdown := "# Retained review\n\n| Metric | Value |\n| --- | --- |\n| Revenue | 123 |\n\n" +
		"Assumption: growth remains 4%.\n\n![Private **diagram**](https://private.example/image.png)\n\n" +
		"<script>alert('unsafe')</script>\n\n<img src=\"https://private.example/raw.png\">\n\n" +
		"[Unsafe](javascript:alert%281%29)\n\n![&lt;script&gt;](https://private.example/second.png)\n"
	result, err := RenderPresentationHTML(markdown)
	if err != nil {
		t.Fatal(err)
	}
	html := string(result)
	for _, want := range []string{
		"<!doctype html>", "<h1>Retained review</h1>", "<table>", "<td>123</td>",
		"Assumption: growth remains 4%.", "[Image: Private diagram]", "&lt;script&gt;",
		"default-src 'none'", "</main></body></html>",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("export missing %q", want)
		}
	}
	for _, forbidden := range []string{"<script", "<img", "private.example", "javascript:", "alert('unsafe')"} {
		if strings.Contains(html, forbidden) {
			t.Errorf("export contains active or remote content %q", forbidden)
		}
	}
	second, err := RenderPresentationHTML(markdown)
	if err != nil || string(second) != html {
		t.Fatal("rendering the same retained content must be deterministic")
	}
}
