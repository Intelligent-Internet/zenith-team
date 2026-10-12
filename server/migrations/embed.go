package migrations

import (
	"embed"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Files contains the complete Boardly schema migration set.
//
//go:embed *.sql
var Files embed.FS

// LatestVersion returns the highest migration version in the embedded set.
// Readiness derives its expected schema version from this so adding a
// migration can never silently desynchronize the readiness fence.
func LatestVersion() (int64, error) {
	entries, err := Files.ReadDir(".")
	if err != nil {
		return 0, fmt.Errorf("read embedded migrations: %w", err)
	}
	var latest int64
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		prefix, _, ok := strings.Cut(name, "_")
		if !ok {
			return 0, fmt.Errorf("embedded migration %q has no version prefix", name)
		}
		version, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil || version <= 0 {
			return 0, fmt.Errorf("embedded migration %q has an invalid version prefix", name)
		}
		if version > latest {
			latest = version
		}
	}
	if latest == 0 {
		return 0, errors.New("embedded migration set is empty")
	}
	return latest, nil
}
