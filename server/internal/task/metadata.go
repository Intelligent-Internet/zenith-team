package task

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

// DateChange distinguishes omission from clearing a calendar date with null.
type DateChange struct {
	Present bool
	Value   *string
}

// MaxLabelRunes bounds one Task label. The Workflow catalog sizes its keys so
// the workflow:<key> label a launch attaches always fits.
const MaxLabelRunes = 48

func normalizeLabels(labels []string) ([]string, error) {
	if len(labels) > 20 {
		return nil, ErrInvalidInput
	}
	result := make([]string, 0, len(labels))
	seen := make(map[string]bool, len(labels))
	for _, label := range labels {
		label = protocol.TrimContractWhitespace(label)
		if label == "" || !utf8.ValidString(label) || utf8.RuneCountInString(label) > MaxLabelRunes ||
			strings.ContainsFunc(label, unicode.IsControl) {
			return nil, ErrInvalidInput
		}
		key := strings.ToLower(label)
		if !seen[key] {
			result = append(result, label)
			seen[key] = true
		}
	}
	return result, nil
}

func validTaskDates(start, due *string) bool {
	for _, value := range []*string{start, due} {
		if value == nil {
			continue
		}
		parsed, err := time.Parse(time.DateOnly, *value)
		if err != nil || parsed.Year() < 1 || parsed.Format(time.DateOnly) != *value {
			return false
		}
	}
	return start == nil || due == nil || *start <= *due
}

func sameDate(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func dateToPG(value *string) pgtype.Date {
	if value == nil {
		return pgtype.Date{}
	}
	// Inputs and the merged update target have passed calendar-date validation.
	parsed, _ := time.Parse(time.DateOnly, *value)
	return pgtype.Date{Time: parsed, Valid: true}
}

func dateFromPG(value pgtype.Date) *string {
	if !value.Valid {
		return nil
	}
	date := value.Time.Format(time.DateOnly)
	return &date
}
