package handler

import (
	"encoding/base64"
	"encoding/binary"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
)

// A feed cursor is the cross-endpoint pagination contract
// (docs/api/conventions.md): opaque to clients, and on the wire an unpadded
// base64url encoding of one versioned, fixed-width record — a version byte,
// the row's created_at as 8 big-endian bytes of Unix microseconds, and the
// row's id. Every paginated feed (activity, runs, approvals, questions,
// Inbox) speaks the format through this one codec so the wire shape cannot
// fork per endpoint; each feed keeps its own sentinel error and maps a decode
// refusal onto it at the call site.
//
// General feeds use v1 UUID records. Run feeds use v3 UUID records so old
// numeric v2 cursors and cursors from unrelated feeds fail closed.

const (
	feedCursorSize = 25
	feedCursorV1   = byte(1)
	runCursorSize  = 25
	runCursorV3    = byte(3)
	// maxEncodedFeedCursorLength bounds the input before any base64 work; a
	// valid cursor encodes to 34 characters.
	maxEncodedFeedCursorLength = 64
)

// encodeFeedCursor renders a feed position as the opaque wire cursor. Both
// inputs come from a database row and are not re-validated here.
func encodeFeedCursor(createdAt time.Time, id uuid.UUID) string {
	encoded := make([]byte, feedCursorSize)
	encoded[0] = feedCursorV1
	binary.BigEndian.PutUint64(encoded[1:9], uint64(createdAt.UnixMicro()))
	copy(encoded[9:], id[:])
	return base64.RawURLEncoding.EncodeToString(encoded)
}

// decodeFeedCursor parses the wire form back into a feed position. ok is false
// for anything but a well-formed v1 record carrying a positive microsecond
// timestamp and a real RFC-4122 UUID.
func decodeFeedCursor(raw string) (createdAt time.Time, id uuid.UUID, ok bool) {
	if raw == "" || len(raw) > maxEncodedFeedCursorLength {
		return time.Time{}, uuid.Nil, false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil || len(decoded) != feedCursorSize || decoded[0] != feedCursorV1 {
		return time.Time{}, uuid.Nil, false
	}
	micros := binary.BigEndian.Uint64(decoded[1:9])
	if micros == 0 || micros > ^uint64(0)>>1 {
		return time.Time{}, uuid.Nil, false
	}
	copy(id[:], decoded[9:])
	if !util.ValidUUID(id) {
		return time.Time{}, uuid.Nil, false
	}
	return time.UnixMicro(int64(micros)).UTC(), id, true
}

// encodeRunCursor renders a run feed position as the v3 wire cursor. Both
// inputs come from a database row and are not re-validated here.
func encodeRunCursor(createdAt time.Time, id uuid.UUID) string {
	encoded := make([]byte, runCursorSize)
	encoded[0] = runCursorV3
	binary.BigEndian.PutUint64(encoded[1:9], uint64(createdAt.UnixMicro()))
	copy(encoded[9:], id[:])
	return base64.RawURLEncoding.EncodeToString(encoded)
}

// decodeRunCursor accepts only v3 UUID positions; numeric cursors are expired.
func decodeRunCursor(raw string) (createdAt time.Time, id uuid.UUID, ok bool) {
	if raw == "" || len(raw) > maxEncodedFeedCursorLength {
		return time.Time{}, uuid.Nil, false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil || len(decoded) != runCursorSize || decoded[0] != runCursorV3 {
		return time.Time{}, uuid.Nil, false
	}
	micros := binary.BigEndian.Uint64(decoded[1:9])
	if micros == 0 || micros > ^uint64(0)>>1 {
		return time.Time{}, uuid.Nil, false
	}
	copy(id[:], decoded[9:])
	if !util.ValidUUID(id) {
		return time.Time{}, uuid.Nil, false
	}
	return time.UnixMicro(int64(micros)).UTC(), id, true
}
