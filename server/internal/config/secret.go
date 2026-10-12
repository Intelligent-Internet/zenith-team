package config

import (
	"fmt"
	"io"
)

// SecretString keeps sensitive configuration values out of ordinary fmt and
// JSON output. Reveal must be used explicitly at the integration boundary.
type SecretString struct {
	value string
}

func newSecretString(value string) SecretString {
	return SecretString{value: value}
}

// Reveal returns the original value for the narrow consumer that needs it.
func (s SecretString) Reveal() string {
	return s.value
}

func (SecretString) String() string {
	return "[REDACTED]"
}

func (SecretString) GoString() string {
	return "config.SecretString([REDACTED])"
}

func (SecretString) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[REDACTED]")
}
