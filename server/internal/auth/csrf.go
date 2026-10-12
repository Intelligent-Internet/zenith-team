package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
)

// CSRFProtector mints and validates the double-submit CSRF token. *CSRF is
// the implementation; the HTTP layer holds the interface so the transport
// packages depend on the contract this package owns rather than declaring
// their own.
type CSRFProtector interface {
	Token(authenticationCookie string) string
	Validate(authenticationCookie, token string) bool
}

var _ CSRFProtector = (*CSRF)(nil)

type CSRF struct {
	secret []byte
}

func NewCSRF(secret []byte) (*CSRF, error) {
	if len(secret) < 32 {
		return nil, errors.New("CSRF secret must contain at least 32 bytes")
	}
	return &CSRF{secret: append([]byte(nil), secret...)}, nil
}

func (c *CSRF) Token(authenticationCookie string) string {
	if authenticationCookie == "" {
		return ""
	}
	mac := hmac.New(sha256.New, c.secret)
	_, _ = mac.Write([]byte(authenticationCookie))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (c *CSRF) Validate(authenticationCookie, token string) bool {
	if authenticationCookie == "" || token == "" {
		return false
	}
	expected := c.Token(authenticationCookie)
	return hmac.Equal([]byte(expected), []byte(token))
}
