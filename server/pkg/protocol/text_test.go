package protocol

import "testing"

func TestTrimContractWhitespaceCoversUnicodeWhiteSpaceAndBOM(t *testing.T) {
	if got := TrimContractWhitespace("\ufeff\u0085\u2000 Zenith Team \u3000"); got != "Zenith Team" {
		t.Fatalf("TrimContractWhitespace() = %q, want Zenith Team", got)
	}
	if got := TrimContractWhitespace("\u200bZenithTeam\u200b"); got != "\u200bZenithTeam\u200b" {
		t.Fatalf("TrimContractWhitespace() removed non-whitespace zero-width characters: %q", got)
	}
}
