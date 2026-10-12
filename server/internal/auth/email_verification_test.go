package auth

import (
	"bytes"
	"testing"
)

func TestVerificationCodesAreSixDigitsAndBoundToTheServerSecret(t *testing.T) {
	t.Parallel()

	code, err := newEmailVerificationCode(bytes.NewReader(make([]byte, 16)))
	if err != nil {
		t.Fatalf("newEmailVerificationCode() error = %v", err)
	}
	if code != "000000" {
		t.Fatalf("newEmailVerificationCode() = %q, want zero-padded six digits", code)
	}
	first := hashEmailVerificationCode([]byte("01234567890123456789012345678901"), code)
	second := hashEmailVerificationCode([]byte("different-secret-890123456789012"), code)
	if len(first) != 32 || bytes.Equal(first, second) {
		t.Fatalf("verification hashes are not secret-bound: %x / %x", first, second)
	}
	if !validEmailVerificationCode("012345") || validEmailVerificationCode("12345") ||
		validEmailVerificationCode("１２３４５６") {
		t.Fatal("verification-code format must be exactly six ASCII digits")
	}
}
