package util

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestParseCalendarDate_DateOnly(t *testing.T) {
	d, err := ParseCalendarDate("2026-03-01")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if got := DateToPtr(d); got == nil || *got != "2026-03-01" {
		t.Fatalf("round-trip mismatch: got %v want 2026-03-01", got)
	}
}

func TestParseCalendarDate_RejectsTimestamps(t *testing.T) {
	cases := []string{
		"2026-03-01T00:00:00Z",
		"2026-02-28T16:00:00Z",
		"2026-03-01T05:00:00Z",
		"2026-03-01T00:00:00+08:00",
	}
	for _, s := range cases {
		t.Run(s, func(t *testing.T) {
			if _, err := ParseCalendarDate(s); err == nil {
				t.Fatalf("expected error for non-midnight instant %q, got nil", s)
			}
		})
	}
}

func TestParseCalendarDate_RejectsGarbage(t *testing.T) {
	for _, s := range []string{"", "not-a-date", "03/01/2026", "2026-13-40"} {
		t.Run(s, func(t *testing.T) {
			if _, err := ParseCalendarDate(s); err == nil {
				t.Fatalf("expected error for %q, got nil", s)
			}
		})
	}
}

func TestDateToPtr_NullIsNil(t *testing.T) {
	if got := DateToPtr(pgtype.Date{Valid: false}); got != nil {
		t.Fatalf("expected nil for invalid date, got %v", *got)
	}
}

// Guard against a localtime regression: DateToPtr must emit the stored calendar
// day regardless of the host process timezone.
func TestDateToPtr_StableAcrossTimezone(t *testing.T) {
	d := pgtype.Date{Time: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), Valid: true}
	if got := DateToPtr(d); got == nil || *got != "2026-03-01" {
		t.Fatalf("got %v want 2026-03-01", got)
	}
}

func TestTimestampWireHelpersUseUTCAndPreservePrecision(t *testing.T) {
	t.Parallel()
	timestamp := pgtype.Timestamptz{
		Time:  time.Date(2026, 8, 19, 23, 45, 12, 345678000, time.FixedZone("UTC+7", 7*60*60)),
		Valid: true,
	}
	want := "2026-08-19T16:45:12.345678Z"

	if got := TimestampToString(timestamp); got != want {
		t.Fatalf("TimestampToString() = %q, want %q", got, want)
	}
	got := TimestampToPtr(timestamp)
	if got == nil || *got != want {
		t.Fatalf("TimestampToPtr() = %v, want %q", got, want)
	}
}

func TestTimestampWireHelpersKeepInvalidValuesEmpty(t *testing.T) {
	t.Parallel()
	if got := TimestampToString(pgtype.Timestamptz{}); got != "" {
		t.Fatalf("TimestampToString(invalid) = %q, want empty", got)
	}
	if got := TimestampToPtr(pgtype.Timestamptz{}); got != nil {
		t.Fatalf("TimestampToPtr(invalid) = %v, want nil", got)
	}
}

// The three nullable-id helpers are what replaced the pgtype conversions. Each
// exists because a nullable uuid column has a state the plain type cannot
// carry, and each has to keep NULL distinct from the zero id — collapsing the
// two is the failure mode this whole seam is built to avoid.
func TestNullUUIDHelpersKeepNullDistinctFromZero(t *testing.T) {
	t.Parallel()

	id := uuid.MustParse("0198aa00-0000-7000-8000-00000000000a")
	set := uuid.NullUUID{UUID: id, Valid: true}
	null := uuid.NullUUID{}
	// The trap: present, and carrying the zero id. Not the same as absent.
	zero := uuid.NullUUID{UUID: uuid.Nil, Valid: true}

	t.Run("NullUUIDPtr", func(t *testing.T) {
		t.Parallel()
		if got := NullUUIDPtr(null); got != nil {
			t.Errorf("NULL -> %v, want nil", got)
		}
		got := NullUUIDPtr(set)
		if got == nil || *got != id {
			t.Fatalf("set -> %v, want %s", got, id)
		}
		// A pointer into the argument would let a caller mutate the row.
		*got = uuid.Nil
		if set.UUID != id {
			t.Error("NullUUIDPtr aliased its argument")
		}
		if got := NullUUIDPtr(zero); got == nil || *got != uuid.Nil {
			t.Errorf("a present zero id -> %v, want a pointer to the zero id", got)
		}
	})

	t.Run("NullUUIDToPtr", func(t *testing.T) {
		t.Parallel()
		if got := NullUUIDToPtr(null); got != nil {
			t.Errorf("NULL -> %v, want nil", got)
		}
		got := NullUUIDToPtr(set)
		if got == nil || *got != id.String() {
			t.Fatalf("set -> %v, want %s", got, id)
		}
	})

	t.Run("NullUUIDToString", func(t *testing.T) {
		t.Parallel()
		if got := NullUUIDToString(null); got != "" {
			t.Errorf("NULL -> %q, want the empty string", got)
		}
		if got := NullUUIDToString(set); got != id.String() {
			t.Errorf("set -> %q, want %s", got, id)
		}
		// Rendered, a present zero id is not empty — that is the distinction
		// callers rely on when they cannot use the pointer form.
		if got := NullUUIDToString(zero); got != uuid.Nil.String() {
			t.Errorf("present zero -> %q, want %s", got, uuid.Nil)
		}
	})
}

// NullUUIDFromPtr is the inbound direction: an optional id becomes a nullable
// column, and nil must become SQL NULL rather than the zero id.
func TestNullUUIDFromPtr(t *testing.T) {
	t.Parallel()

	if got := NullUUIDFromPtr(nil); got.Valid || got.UUID != uuid.Nil {
		t.Errorf("nil -> %+v, want an invalid zero value", got)
	}

	id := uuid.MustParse("0198aa00-0000-7000-8000-00000000000a")
	got := NullUUIDFromPtr(&id)
	if !got.Valid || got.UUID != id {
		t.Errorf("&id -> %+v, want %s valid", got, id)
	}

	// An explicit zero id is a value the caller chose; it is not absence.
	zero := uuid.Nil
	if got := NullUUIDFromPtr(&zero); !got.Valid {
		t.Error("&uuid.Nil -> NULL; a pointer to the zero id is still a value")
	}
}
