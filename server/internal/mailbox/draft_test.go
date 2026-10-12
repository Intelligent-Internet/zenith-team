package mailbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func TestCanonicalizeDraftInputsNormalizesAddressesAndKeepsDeliveriesPrivate(t *testing.T) {
	t.Parallel()

	canonical, digest, err := canonicalizeDraftInputs([]DraftInput{{
		Subject: "  Launch update  ",
		Text:    "Hello team\n",
		Deliveries: []DraftDeliveryInput{
			{To: []string{" Lead-A@Example.COM "}, CC: []string{}},
			{To: []string{"lead-b@example.com"}, CC: []string{"Owner@Example.com"}},
		},
	}})
	if err != nil {
		t.Fatalf("canonicalizeDraftInputs() error = %v", err)
	}
	if len(digest) != 32 || len(canonical) != 1 || len(canonical[0].Deliveries) != 2 {
		t.Fatalf("canonical draft = %#v, digest bytes = %d", canonical, len(digest))
	}
	if canonical[0].Subject != "Launch update" || canonical[0].Text != "Hello team\n" ||
		canonical[0].Deliveries[0].To[0] != "lead-a@example.com" ||
		canonical[0].Deliveries[1].CC[0] != "owner@example.com" {
		t.Fatalf("canonical draft = %#v", canonical[0])
	}
	if bytes.Equal(digest, make([]byte, 32)) {
		t.Fatal("canonical digest is all zeroes")
	}
}

func TestCanonicalizeDraftInputsRejectsUnsafeOrUnboundedDrafts(t *testing.T) {
	t.Parallel()

	validDeliveries := []DraftDeliveryInput{{To: []string{"lead@example.com"}}}
	tests := []struct {
		name   string
		drafts []DraftInput
		err    error
	}{
		{name: "empty group", drafts: nil, err: ErrAgentMailInvalidInput},
		{name: "subject newline", drafts: []DraftInput{{
			Subject: "Hello\r\nBcc: hidden@example.com", Text: "Body",
			Deliveries: validDeliveries,
		}}, err: ErrAgentMailInvalidInput},
		{name: "subject NUL", drafts: []DraftInput{{
			Subject: "Hello\x00team", Text: "Body", Deliveries: validDeliveries,
		}}, err: ErrAgentMailInvalidInput},
		{
			name:   "empty body",
			drafts: []DraftInput{{Subject: "Hello", Deliveries: validDeliveries}},
			err:    ErrAgentMailInvalidInput,
		},
		{name: "body NUL", drafts: []DraftInput{{
			Subject: "Hello", Text: "Body\x00tail", Deliveries: validDeliveries,
		}}, err: ErrAgentMailInvalidInput},
		{name: "invalid UTF-8 body", drafts: []DraftInput{{
			Subject: "Hello", Text: string([]byte{0xff}), Deliveries: validDeliveries,
		}}, err: ErrAgentMailInvalidInput},
		{name: "large body", drafts: []DraftInput{{
			Subject: "Hello", Text: strings.Repeat("x", maxDraftBodyBytes+1), Deliveries: validDeliveries,
		}}, err: ErrAgentMailInvalidInput},
		{name: "invalid recipient", drafts: []DraftInput{
			{
				Subject:    "Hello",
				Text:       "Body",
				Deliveries: []DraftDeliveryInput{{To: []string{"Display <lead@example.com>"}}},
			},
		}, err: ErrAgentMailInvalidRecipients},
		{name: "duplicate across to and cc", drafts: []DraftInput{{
			Subject: "Hello", Text: "Body", Deliveries: []DraftDeliveryInput{{
				To: []string{"lead@example.com"}, CC: []string{"LEAD@example.com"},
			}},
		}}, err: ErrAgentMailInvalidRecipients},
		{name: "recipient limit", drafts: []DraftInput{{
			Subject: "Hello", Text: "Body", Deliveries: recipientDeliveries(maxAgentMailRecipients + 1),
		}}, err: ErrAgentMailSendLimitExceeded},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := canonicalizeDraftInputs(test.drafts); !errors.Is(err, test.err) {
				t.Fatalf("canonicalizeDraftInputs() error = %v, want %v", err, test.err)
			}
		})
	}
}

func TestCanonicalizeDraftInputsCountsSubjectCharactersInsteadOfBytes(t *testing.T) {
	t.Parallel()

	draft := DraftInput{
		Subject: strings.Repeat("ế", maxDraftSubjectLength),
		Text:    "Body",
		Deliveries: []DraftDeliveryInput{{
			To: []string{"lead@example.com"},
		}},
	}
	if _, _, err := canonicalizeDraftInputs([]DraftInput{draft}); err != nil {
		t.Fatalf("canonicalizeDraftInputs(200 Unicode characters) error = %v", err)
	}
	draft.Subject += "ế"
	if _, _, err := canonicalizeDraftInputs([]DraftInput{draft}); !errors.Is(err, ErrAgentMailInvalidInput) {
		t.Fatalf("canonicalizeDraftInputs(201 Unicode characters) error = %v, want invalid input", err)
	}
}

func TestDraftFromRowsSerializesEmptyCCAsArray(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	companyID := uuid.Must(uuid.NewV7())
	draftID := uuid.Must(uuid.NewV7())
	draft := draftFromRows(generated.MailboxDraft{
		ID: draftID, CompanyID: companyID, GroupID: uuid.Must(uuid.NewV7()),
		Subject: "Hello", BodyText: "Body", FromName: "Boardly",
		FromAddress: "hello@example.com", Status: string(DraftStatusDraft),
		CreatedAt: timestamp(now), UpdatedAt: timestamp(now), Version: 1,
	}, []generated.MailboxDraftDelivery{{
		ID: uuid.Must(uuid.NewV7()), CompanyID: companyID, DraftID: draftID,
		ToAddresses: []string{"lead@example.com"}, CcAddresses: []string{},
		CreatedAt: timestamp(now),
	}})
	if draft.Deliveries[0].CC == nil {
		t.Fatal("draft delivery CC is nil, want an empty JSON array")
	}
	payload, err := json.Marshal(draft.Deliveries[0])
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if !bytes.Contains(payload, []byte(`"cc":[]`)) {
		t.Fatalf("draft delivery JSON = %s, want cc array", payload)
	}
}

func recipientDeliveries(count int) []DraftDeliveryInput {
	deliveries := make([]DraftDeliveryInput, count)
	for index := range deliveries {
		deliveries[index] = DraftDeliveryInput{To: []string{
			"lead-" + strings.Repeat("x", index%5) + string(rune('a'+index%26)) + "@example.com",
		}}
	}
	return deliveries
}
