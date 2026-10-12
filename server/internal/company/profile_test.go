package company

import (
	"errors"
	"testing"
)

func TestNormalizeCompanyContactEmailRequiresDottedDomain(t *testing.T) {
	for _, value := range []string{
		"hello@example",
		"hello@.example",
		"hello@example.",
	} {
		t.Run(value, func(t *testing.T) {
			if _, err := normalizeCompanyContactEmail(&value); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf(
					"normalizeCompanyContactEmail(%q) error = %v, want ErrInvalidInput",
					value,
					err,
				)
			}
		})
	}

	value := " HELLO@EXAMPLE.COM "
	normalized, err := normalizeCompanyContactEmail(&value)
	if err != nil {
		t.Fatalf("normalizeCompanyContactEmail(%q) error = %v", value, err)
	}
	if normalized == nil || *normalized != "hello@example.com" {
		t.Fatalf("normalizeCompanyContactEmail(%q) = %v, want hello@example.com", value, normalized)
	}
}
