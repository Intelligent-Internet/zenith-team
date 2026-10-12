package auth

import "testing"

func TestCSRFTokenIsBoundToAuthenticationCookie(t *testing.T) {
	t.Parallel()

	protector, err := NewCSRF([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatalf("NewCSRF() error = %v", err)
	}
	token := protector.Token("access-cookie-a")
	if token == "" {
		t.Fatal("Token() returned empty token")
	}
	if !protector.Validate("access-cookie-a", token) {
		t.Fatal("Validate() = false for matching authentication cookie")
	}
	if protector.Validate("access-cookie-b", token) {
		t.Fatal("Validate() = true for a different authentication cookie")
	}
	if protector.Validate("access-cookie-a", token+"tampered") {
		t.Fatal("Validate() = true for a modified token")
	}
}
