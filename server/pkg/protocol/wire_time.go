package protocol

import (
	"errors"
	"time"
)

// Wire timestamps are strings, never bare time.Time: pgx scans timestamptz
// in the server's local location, and encoding/json would serialize that
// local offset onto the wire. Formatting explicitly in UTC keeps every
// encoded contract independent of the host time zone.

// FormatWireTime renders one canonical UTC RFC 3339 wire timestamp with
// sub-second precision, byte-identical to what encoding/json emits for a
// UTC time.Time.
func FormatWireTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

// FormatOptionalWireTime renders a nullable wire timestamp.
func FormatOptionalWireTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := FormatWireTime(*value)
	return &formatted
}

// ParseWireTime accepts exactly the RFC 3339 shapes encoding/json accepted
// for time.Time fields and normalizes the result to UTC.
func ParseWireTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, errors.New("wire timestamp is required")
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}
