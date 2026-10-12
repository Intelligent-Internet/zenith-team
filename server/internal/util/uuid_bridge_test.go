package util

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestParseGoUUIDMatchesTheCanonicalRFCContract(t *testing.T) {
	canonical := "0195c48e-7bb4-7f2a-9e21-101b9715febb"
	// Any RFC-4122 version is welcome: the DB generates v4
	// (gen_random_uuid) while clients may mint v7 for idempotent creates.
	v4 := "6ba7b810-9dad-41d1-80b4-00c04fd430c8"
	for _, accepted := range []string{canonical, strings.ToUpper(canonical), v4} {
		id, err := ParseGoUUID(accepted)
		if err != nil || !strings.EqualFold(id.String(), accepted) {
			t.Errorf("ParseGoUUID(%q) = %s, %v", accepted, id, err)
		}
	}
	for _, rejected := range []string{
		strings.ReplaceAll(canonical, "-", ""),
		"{" + canonical + "}",
		"urn:uuid:" + canonical,
		"0195c48e-7bb4-7f2a-1e21-101b9715febb",
		uuid.Nil.String(),
	} {
		if _, err := ParseGoUUID(rejected); err == nil {
			t.Errorf("ParseGoUUID(%q) error = nil", rejected)
		}
	}
}
