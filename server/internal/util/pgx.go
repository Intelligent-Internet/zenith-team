package util

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/jackc/pgx/v5/pgtype"
)

func TextToPtr(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

func PtrToText(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *s, Valid: true}
}

func StrToText(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

func TimestampToString(t pgtype.Timestamptz) string {
	if !t.Valid {
		return ""
	}
	return t.Time.UTC().Format(time.RFC3339Nano)
}

func TimestampToPtr(t pgtype.Timestamptz) *string {
	if !t.Valid {
		return nil
	}
	s := t.Time.UTC().Format(time.RFC3339Nano)
	return &s
}

// TimestampToTime converts a nullable timestamptz read-back to the instant
// normalized to UTC, or nil when unset. TimestampToPtr is the wire-DTO
// sibling that renders RFC3339 text instead of the instant.
func TimestampToTime(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	utc := t.Time.UTC()
	return &utc
}

// DateToPtr formats a pgtype.Date as a date-only "YYYY-MM-DD" string, or nil
// when unset. Issue start_date/due_date are calendar days with no time-of-day
// or timezone, so they must never be rendered through an instant.
func DateToPtr(d pgtype.Date) *string {
	if !d.Valid {
		return nil
	}
	s := d.Time.Format(time.DateOnly)
	return &s
}

// ParseCalendarDate parses the canonical "YYYY-MM-DD" calendar-day wire value
// into a pgtype.Date carrying no time-of-day or timezone.
func ParseCalendarDate(s string) (pgtype.Date, error) {
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return pgtype.Date{Time: t, Valid: true}, nil
	}
	return pgtype.Date{}, fmt.Errorf("invalid date %q: expected YYYY-MM-DD", s)
}

// NullUUIDToPtr renders a nullable uuid column as the optional string the wire
// types take.
func NullUUIDToPtr(u uuid.NullUUID) *string {
	if !u.Valid {
		return nil
	}
	s := u.UUID.String()
	return &s
}

// NullUUIDFromPtr converts an optional id to the nullable form a uuid column
// takes; nil becomes SQL NULL.
func NullUUIDFromPtr(id *uuid.UUID) uuid.NullUUID {
	if id == nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: *id, Valid: true}
}

// NullUUIDToString renders a nullable uuid column, with NULL as the empty
// string. Callers that need to tell NULL from a value use NullUUIDToPtr.
func NullUUIDToString(u uuid.NullUUID) string {
	if !u.Valid {
		return ""
	}
	return u.UUID.String()
}

// NullUUIDPtr converts a nullable uuid column to an optional id.
func NullUUIDPtr(u uuid.NullUUID) *uuid.UUID {
	if !u.Valid {
		return nil
	}
	id := u.UUID
	return &id
}

// UUIDToValue flattens an optional id to the absent-is-uuid.Nil convention the
// event envelope uses, where a fact that is about no such row simply carries
// the zero id.
func UUIDToValue(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.Nil
	}
	return *id
}

// NullUUIDToValue is the same flattening for a nullable uuid column: SQL NULL
// becomes uuid.Nil.
func NullUUIDToValue(u uuid.NullUUID) uuid.UUID {
	if !u.Valid {
		return uuid.Nil
	}
	return u.UUID
}
