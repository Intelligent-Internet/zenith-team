package handler

import (
	"encoding/base64"
	"encoding/binary"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestFeedCursorRoundTripsCreatedAtAndID(t *testing.T) {
	t.Parallel()

	wantCreatedAt := time.Date(2026, time.August, 19, 10, 11, 12, 345678000, time.UTC)
	wantID := uuid.MustParse("0198b3c4-b0d5-7e62-8f19-3ac75d09e4b2")
	encoded := encodeFeedCursor(wantCreatedAt, wantID)

	gotCreatedAt, gotID, ok := decodeFeedCursor(encoded)
	if !ok || gotID != wantID || !gotCreatedAt.Equal(wantCreatedAt) {
		t.Fatalf(
			"decodeFeedCursor(encode) = (%v, %v, %t), want (%v, %v, true)",
			gotCreatedAt, gotID, ok, wantCreatedAt, wantID,
		)
	}
}

func TestFeedCursorRejectsMalformedInput(t *testing.T) {
	t.Parallel()

	valid := encodeFeedCursor(
		time.Date(2026, time.August, 14, 10, 30, 0, 123456000, time.UTC),
		uuid.MustParse("6ba7b810-9dad-11d1-80b4-00c04fd430c8"),
	)
	record := func(mutate func(record []byte)) string {
		decoded, err := base64.RawURLEncoding.DecodeString(valid)
		if err != nil {
			t.Fatalf("decode the valid fixture: %v", err)
		}
		mutate(decoded)
		return base64.RawURLEncoding.EncodeToString(decoded)
	}

	cases := map[string]string{
		"empty":          "",
		"not base64":     "!!!",
		"truncated":      "AAAA",
		"oversized":      string(make([]byte, 80)),
		"padded":         valid + "=",
		"wrong version":  record(func(record []byte) { record[0] = 2 }),
		"zero timestamp": record(func(record []byte) { binary.BigEndian.PutUint64(record[1:9], 0) }),
		"nil uuid": record(func(record []byte) {
			copy(record[9:], make([]byte, 16))
		}),
		"non-rfc4122 uuid": record(func(record []byte) {
			// Clear the variant bits: a value that is bytes but not a UUID.
			record[17] = 0x00
		}),
	}
	for name, raw := range cases {
		if _, _, ok := decodeFeedCursor(raw); ok {
			t.Errorf("decodeFeedCursor(%s %q) accepted invalid input", name, raw)
		}
	}
}

// The run feed keys on a bigserial, so it speaks the v2 record. The two
// records share a version namespace precisely so neither decoder can be handed
// the other's cursor and produce a plausible-looking position.
func TestRunCursorRoundTripsAndRejectsForeignRecords(t *testing.T) {
	t.Parallel()

	wantCreatedAt := time.Date(2026, time.August, 19, 10, 11, 12, 345678000, time.UTC)
	encoded := encodeRunCursor(wantCreatedAt, uuid.MustParse("00000000-0000-4000-8000-000000004711"))

	gotCreatedAt, gotID, ok := decodeRunCursor(encoded)
	if !ok || gotID != uuid.MustParse("00000000-0000-4000-8000-000000004711") ||
		!gotCreatedAt.Equal(wantCreatedAt) {
		t.Fatalf(
			"decodeRunCursor(encode) = (%v, %d, %t), want (%v, 4711, true)",
			gotCreatedAt, gotID, ok, wantCreatedAt,
		)
	}

	uuidCursor := encodeFeedCursor(wantCreatedAt, uuid.MustParse("0198b3c4-b0d5-7e62-8f19-3ac75d09e4b2"))
	if _, _, ok := decodeRunCursor(uuidCursor); ok {
		t.Error("decodeRunCursor accepted a uuid-keyed feed's cursor")
	}
	if _, _, ok := decodeFeedCursor(encoded); ok {
		t.Error("decodeFeedCursor accepted a run cursor")
	}
	// runs.id starts at 1, so a record naming zero is not a row that exists.
	if _, _, ok := decodeRunCursor(encodeRunCursor(wantCreatedAt, uuid.Nil)); ok {
		t.Error("decodeRunCursor accepted a run id below the sequence floor")
	}
}
