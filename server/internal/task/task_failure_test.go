package task

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestNormalizeFailureInputTrimsAndBoundsTheBusinessReason(t *testing.T) {
	t.Parallel()

	requestKey := uuid.Must(uuid.NewV7())
	tests := []struct {
		name    string
		input   FailureInput
		want    string
		wantErr error
	}{
		{
			name: "trimmed", input: FailureInput{
				RequestKey: requestKey, Reason: "\u00a0Supplier rejected the order\u3000",
			},
			want: "Supplier rejected the order",
		},
		{
			name: "maximum", input: FailureInput{
				RequestKey: requestKey, Reason: strings.Repeat("界", 2000),
			},
			want: strings.Repeat("界", 2000),
		},
		{
			name: "missing key", input: FailureInput{Reason: "blocked"}, wantErr: ErrInvalidInput,
		},
		{
			name: "blank", input: FailureInput{
				RequestKey: requestKey, Reason: "\u00a0\u3000",
			}, wantErr: ErrInvalidInput,
		},
		{
			name: "too long", input: FailureInput{
				RequestKey: requestKey, Reason: strings.Repeat("x", 2001),
			}, wantErr: ErrInvalidInput,
		},
		{
			name: "invalid UTF-8", input: FailureInput{
				RequestKey: requestKey, Reason: string([]byte{0xff}),
			}, wantErr: ErrInvalidInput,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := normalizeFailureInput(test.input)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("normalizeFailureInput() error = %v, want %v", err, test.wantErr)
			}
			if err == nil && got.Reason != test.want {
				t.Fatalf("normalizeFailureInput().Reason = %q, want %q", got.Reason, test.want)
			}
		})
	}
}
