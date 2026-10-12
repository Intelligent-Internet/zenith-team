package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/company"
)

func TestInvitationRoutingReachesOnlyTheInvitingRoles(t *testing.T) {
	t.Parallel()
	companyID, owner, admin := uuid.New(), uuid.New(), uuid.New()
	for _, testCase := range []struct {
		name    string
		payload any
	}{
		{
			name: company.EventInvitationChanged,
			payload: company.InvitationChanged{
				InvitationResponse: company.InvitationResponse{Email: "invitee@example.com"},
				RecipientUserIDs:   []uuid.UUID{owner, admin},
			},
		},
		{
			name:    company.EventShareLinkChanged,
			payload: company.ShareLinkChanged{Revision: 3, RecipientUserIDs: []uuid.UUID{owner, admin}},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			bus, recorder := routedBus(t)
			publish(t, bus, companyID, company.TopicInvitation, testCase.name, testCase.payload)
			want := []string{
				"company_user/" + companyID.String() + "/" + owner.String(),
				"company_user/" + companyID.String() + "/" + admin.String(),
			}
			if len(recorder.sent) != 2 || recorder.sent[0] != want[0] || recorder.sent[1] != want[1] {
				t.Fatalf("rooms = %v, want %v", recorder.sent, want)
			}
			// The recipient list is a routing fact and never reaches the wire.
			encoded, err := json.Marshal(recorder.frames[0])
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), owner.String()) {
				t.Fatalf("frame leaks its recipient list: %s", encoded)
			}
		})
	}
}

func TestInvitationRoutingWithNoRecipientsReachesNobody(t *testing.T) {
	t.Parallel()
	bus, recorder := routedBus(t)
	publish(
		t,
		bus,
		uuid.New(),
		company.TopicInvitation,
		company.EventInvitationChanged,
		company.InvitationChanged{},
	)
	if len(recorder.sent) != 0 {
		t.Fatalf("rooms = %v, want none", recorder.sent)
	}
}
