package handler

import (
	"encoding/json"
	"testing"
)

func TestCompanyProfileAvatarInput(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, raw                 string
		present, valid, wantError bool
	}{
		{name: "unchanged"},
		{name: "remove", raw: "null", present: true},
		{name: "select", raw: `"b11bc2b8-0bad-4ddb-9f46-b3b3c48dd797"`, present: true, valid: true},
		{name: "invalid id", raw: `"not-an-id"`, wantError: true},
		{name: "wrong type", raw: "42", wantError: true},
		{name: "zero id", raw: `"00000000-0000-0000-0000-000000000000"`, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := updateCompanyProfileRequest{
				ExpectedRevision: optional[int64]{set: true, value: 1},
				Name:             json.RawMessage(`"Company"`), ContactEmail: json.RawMessage("null"),
				PhoneCountryISO2: json.RawMessage("null"), PhoneNumber: json.RawMessage("null"),
				AvatarAttachmentID: json.RawMessage(test.raw),
			}
			input, err := request.domainInput()
			if (err != nil) != test.wantError {
				t.Fatalf("domainInput() error = %v, want error %v", err, test.wantError)
			}
			if err != nil {
				return
			}
			if (input.AvatarAttachmentID != nil) != test.present {
				t.Fatalf("avatar presence = %v, want %v", input.AvatarAttachmentID, test.present)
			}
			if input.AvatarAttachmentID != nil && input.AvatarAttachmentID.Valid != test.valid {
				t.Fatalf("avatar valid = %v, want %v", input.AvatarAttachmentID.Valid, test.valid)
			}
		})
	}
}
