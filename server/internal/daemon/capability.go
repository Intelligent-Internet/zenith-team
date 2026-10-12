package daemon

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// A local capability is a 32-byte random secret the daemon hands to exactly
// one local caller (a Run's CLI environment, a Task's Commerce bootstrap) and
// checks on every request to its loopback plane. It authorizes nothing on
// the server: the daemon exchanges it for its own session plus the Run proof
// on the caller's behalf, so the secret is worthless outside this machine
// and dies with the daemon process.

func newLocalCapability() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate local capability: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
