package company

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
)

func TestAccountDeletionMembershipEventsUseSystemActor(t *testing.T) {
	t.Parallel()

	companyID := uuid.Must(uuid.NewV7())
	userID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()
	member := MemberResponse{
		CompanyID: companyID,
		UserID:    userID,
		Role:      auth.CompanyRoleOwner,
		Status:    MembershipStatusRevoked,
		CreatedAt: now,
		UpdatedAt: now,
		Revision:  2,
	}
	var pending []events.Event
	if err := stageMembershipEventsAs(
		&pending,
		events.SystemActor(),
		member,
		ActiveMemberResponse{},
		MembershipRemoved,
	); err != nil {
		t.Fatalf("stageMembershipEventsAs() error = %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("account deletion membership events = %d, want 2", len(pending))
	}
	if pending[0].Name != EventMemberRemoved || pending[0].Actor != events.SystemActor() {
		t.Fatalf("Company membership event = %+v", pending[0])
	}
	if pending[1].Name != EventAuthCompanyAccessRevoked ||
		pending[1].Actor != events.SystemActor() {
		t.Fatalf("personal access event = %+v", pending[1])
	}
	removed, err := events.PayloadOf[MemberRemovalReceipt](pending[0])
	if err != nil {
		t.Fatalf("decode member.removed: %v", err)
	}
	if removed.CompanyID != companyID || removed.UserID != userID || removed.Revision != 2 {
		t.Fatalf("member.removed payload = %+v", removed)
	}
	revoked, err := events.PayloadOf[AccessRevokedReceipt](pending[1])
	if err != nil {
		t.Fatalf("decode auth.company_access_revoked: %v", err)
	}
	if revoked.CompanyID != companyID || revoked.UserID != userID ||
		revoked.Reason != AccessReasonMembershipRemoved {
		t.Fatalf("auth.company_access_revoked payload = %+v", revoked)
	}
}
