package execution

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func TestRunTokenHashAcceptsOnlyMintedWireShape(t *testing.T) {
	t.Parallel()

	raw := strings.Repeat("ab", 32)
	token := "brt_" + raw
	got, err := runTokenHash(token)
	if err != nil {
		t.Fatalf("runTokenHash(valid) error = %v", err)
	}
	want := sha256.Sum256([]byte(token))
	if hex.EncodeToString(got) != hex.EncodeToString(want[:]) {
		t.Fatalf("runTokenHash(valid) = %x, want %x", got, want)
	}

	for _, invalid := range []string{
		"", "brt_", "bearer_" + raw, "brt_" + strings.Repeat("a", 63),
		"brt_" + strings.Repeat("a", 65), "brt_" + strings.Repeat("A", 64),
		"brt_" + strings.Repeat("g", 64), " brt_" + raw,
	} {
		if _, err := runTokenHash(invalid); !errors.Is(err, ErrRunProofInvalid) {
			t.Errorf("runTokenHash(%q) error = %v, want ErrRunProofInvalid", invalid, err)
		}
	}
}
