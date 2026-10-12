package util

import (
	"errors"
	"strings"

	"github.com/google/uuid"
)

// UUID parsing at the request boundary. There is no bridge here any more:
// sqlc emits google/uuid types and pgx encodes them directly, so a uuid is one
// type from the JSON body to the query parameter.

// ErrInvalidUUID reports an id that is not a canonical RFC-4122 UUID.
var ErrInvalidUUID = errors.New("invalid UUID")

// ParseGoUUID parses a canonical 36-character RFC-4122 UUID of any version
// into a google/uuid id (the pgtype counterpart is ParseUUID in pgx.go).
// Entity IDs are DB-generated (gen_random_uuid, v4), while clients mint v7
// idempotency keys; both versions are welcome and only shape is validated.
func ParseGoUUID(raw string) (uuid.UUID, error) {
	if len(raw) != 36 {
		return uuid.Nil, ErrInvalidUUID
	}
	id, err := uuid.Parse(raw)
	if err != nil || !strings.EqualFold(raw, id.String()) || !ValidUUID(id) {
		return uuid.Nil, ErrInvalidUUID
	}
	return id, nil
}

// ValidUUID reports whether id is a non-nil RFC-4122 UUID of any version.
func ValidUUID(id uuid.UUID) bool {
	return id != uuid.Nil && id.Variant() == uuid.RFC4122
}
