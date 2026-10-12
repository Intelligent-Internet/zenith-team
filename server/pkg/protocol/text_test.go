package protocol

import "testing"

func TestTrimContractWhitespaceCoversUnicodeWhiteSpaceAndBOM(t *testing.T) {
	if got := TrimContractWhitespace("\ufeff\u0085\u2000 Boardly \u3000"); got != "Boardly" {
		t.Fatalf("TrimContractWhitespace() = %q, want Boardly", got)
	}
	if got := TrimContractWhitespace("\u200bBoardly\u200b"); got != "\u200bBoardly\u200b" {
		t.Fatalf("TrimContractWhitespace() removed non-whitespace zero-width characters: %q", got)
	}
}
