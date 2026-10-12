package agent

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// The rename input rule: trimmed, at most 100 runes, and blank means clear.
// Runes, not bytes — a name made of multi-byte characters is measured the
// way a person counts it.
func TestNormalizeRuntimeRenameInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		wantValid bool
		wantValue string
		wantErr   error
	}{
		{name: "trimmed", input: "  Office Mac  ", wantValid: true, wantValue: "Office Mac"},
		{name: "empty clears", input: ""},
		{name: "whitespace clears", input: " \t\n "},
		{
			name: "exactly 100 runes", input: strings.Repeat("a", 100),
			wantValid: true, wantValue: strings.Repeat("a", 100),
		},
		{name: "101 runes", input: strings.Repeat("a", 101), wantErr: ErrRuntimeInvalidInput},
		{
			name: "100 multi-byte runes", input: strings.Repeat("é", 100),
			wantValid: true, wantValue: strings.Repeat("é", 100),
		},
		{name: "101 multi-byte runes", input: strings.Repeat("é", 101), wantErr: ErrRuntimeInvalidInput},
		{
			name: "padding does not count", input: "  " + strings.Repeat("a", 100) + "  ",
			wantValid: true, wantValue: strings.Repeat("a", 100),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := normalizeRuntimeRenameInput(RuntimeRenameInput{CustomName: test.input})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf(
					"normalizeRuntimeRenameInput(%q) error = %v, want %v",
					test.input,
					err,
					test.wantErr,
				)
			}
			if err != nil {
				return
			}
			if got.Valid != test.wantValid || got.String != test.wantValue {
				t.Fatalf("normalizeRuntimeRenameInput(%q) = %+v, want valid=%t %q",
					test.input, got, test.wantValid, test.wantValue)
			}
		})
	}
}

// The ownership rule (ADR 0014): the registrant alone, whatever anyone's
// Company role, and a runtime without a stored registrant belongs to nobody.
func TestRegisteredBy(t *testing.T) {
	t.Parallel()
	actor := uuid.Must(uuid.NewV7())
	someoneElse := uuid.Must(uuid.NewV7())

	tests := []struct {
		name  string
		owner uuid.NullUUID
		want  bool
	}{
		{name: "the registrant", owner: uuid.NullUUID{UUID: actor, Valid: true}, want: true},
		{name: "another member's machine", owner: uuid.NullUUID{UUID: someoneElse, Valid: true}},
		{name: "an unowned machine belongs to nobody", owner: uuid.NullUUID{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := registeredBy(test.owner, actor); got != test.want {
				t.Fatalf("registeredBy(owner valid=%t) = %t, want %t", test.owner.Valid, got, test.want)
			}
		})
	}
}
