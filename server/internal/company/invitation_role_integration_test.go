//go:build integration

package company

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
)

func TestInvitationGrantsTheInvitedRole(t *testing.T) {
	s, pool, owner, recipient, sender := invitationFixture(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, owner, InvitationCreateInput{
		Emails: []string{"invitee@example.com"}, Role: auth.CompanyRoleOwner,
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("inviting as owner = %v, want ErrInvalidInput", err)
	}
	if _, err := s.Create(ctx, owner, InvitationCreateInput{
		Emails: []string{"invitee@example.com"},
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("inviting with no role = %v, want ErrInvalidInput", err)
	}

	results, err := s.Create(ctx, owner, InvitationCreateInput{
		Emails: []string{"invitee@example.com"}, Role: auth.CompanyRoleAdmin,
	})
	if err != nil || len(results) != 1 || results[0].Invitation == nil ||
		results[0].Invitation.Role != auth.CompanyRoleAdmin {
		t.Fatalf("Create() = %+v, %v", results, err)
	}
	// A second invitation to the same address keeps the first role.
	repeat, err := s.Create(ctx, owner, InvitationCreateInput{
		Emails: []string{"invitee@example.com"}, Role: auth.CompanyRoleMember,
	})
	if err != nil || repeat[0].Outcome != "alreadyInvited" || repeat[0].Invitation.Role != auth.CompanyRoleAdmin {
		t.Fatalf("repeat Create() = %+v, %v", repeat, err)
	}
	if err = s.Deliver(ctx); err != nil {
		t.Fatal(err)
	}
	message := sender.messages[len(sender.messages)-1]
	token := strings.TrimPrefix(message.URL, "https://app.example.com/invite/")

	preview, err := s.Preview(ctx, token)
	if err != nil || preview.Role != auth.CompanyRoleAdmin {
		t.Fatalf("Preview() = %+v, %v", preview, err)
	}
	accepted, err := s.Accept(ctx, recipient, token)
	if err != nil || accepted.Outcome != "joined" || accepted.Company.Role != auth.CompanyRoleAdmin {
		t.Fatalf("Accept() = %+v, %v", accepted, err)
	}
	if len(accepted.Company.Permissions) != len(auth.PermissionsOf(auth.CompanyRoleAdmin)) {
		t.Fatalf("accepted permissions = %v, want the admin set", accepted.Company.Permissions)
	}
	var role string
	if err := pool.QueryRow(ctx,
		`SELECT role FROM company_members WHERE company_id=$1 AND user_id=$2 AND status='active'`,
		owner.CompanyID, recipient.UserID,
	).Scan(&role); err != nil || role != "admin" {
		t.Fatalf("stored role = %q, %v", role, err)
	}

	// The new admin may invite members, and the invitation.changed frame
	// names every member who may see the invitation surface: both the owner
	// and the admin, never the wider Company.
	var frames []InvitationChanged
	if err := s.bus.Subscribe(TopicInvitation, func(_ context.Context, event events.Event) error {
		if event.Name != EventInvitationChanged {
			return nil
		}
		payload, err := events.PayloadOf[InvitationChanged](event)
		if err != nil {
			return err
		}
		frames = append(frames, payload)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	recipient.CompanyID = owner.CompanyID
	recipient.Role = auth.CompanyRoleAdmin
	if _, err := s.Create(ctx, recipient, InvitationCreateInput{
		Emails: []string{"another@example.com"}, Role: auth.CompanyRoleMember,
	}); err != nil {
		t.Fatalf("admin inviting a member = %v", err)
	}
	if len(frames) != 1 || frames[0].Email != "another@example.com" || frames[0].Role != auth.CompanyRoleMember {
		t.Fatalf("invitation.changed frames = %+v, want one for the new invitation", frames)
	}
	recipients := map[uuid.UUID]bool{}
	for _, id := range frames[0].RecipientUserIDs {
		recipients[id] = true
	}
	if len(recipients) != 2 || !recipients[owner.UserID] || !recipients[recipient.UserID] {
		t.Fatalf("invitation.changed recipients = %v, want the owner and the admin", frames[0].RecipientUserIDs)
	}
}

func TestShareLinkGrantsItsRoleAndResetKeepsIt(t *testing.T) {
	s, pool, owner, recipient, _ := invitationFixture(t)
	ctx := context.Background()

	// Before any link exists the response still speaks the contract: the
	// role a first link would grant, never an empty string.
	none, err := s.GetShareLink(ctx, owner)
	if err != nil || none.URL != nil || none.Role != auth.CompanyRoleMember || none.Revision != 0 {
		t.Fatalf("GetShareLink() before any link = %+v, %v", none, err)
	}
	if _, err := s.CreateShareLink(ctx, owner, auth.CompanyRoleOwner); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("owner link = %v, want ErrInvalidInput", err)
	}
	link, err := s.CreateShareLink(ctx, owner, auth.CompanyRoleAdmin)
	if err != nil || link.URL == nil || link.Role != auth.CompanyRoleAdmin {
		t.Fatalf("CreateShareLink() = %+v, %v", link, err)
	}
	// Creating again while the link is live returns the same link and does
	// not silently change its role.
	same, err := s.CreateShareLink(ctx, owner, auth.CompanyRoleMember)
	if err != nil || *same.URL != *link.URL || same.Role != auth.CompanyRoleAdmin {
		t.Fatalf("second CreateShareLink() = %+v, %v", same, err)
	}
	// A reset without a role keeps the current one; with a role it changes.
	kept, err := s.ResetShareLink(ctx, owner, link.Revision, "")
	if err != nil || kept.Role != auth.CompanyRoleAdmin || *kept.URL == *link.URL {
		t.Fatalf("ResetShareLink(keep) = %+v, %v", kept, err)
	}
	changed, err := s.ResetShareLink(ctx, owner, kept.Revision, auth.CompanyRoleMember)
	if err != nil || changed.Role != auth.CompanyRoleMember {
		t.Fatalf("ResetShareLink(member) = %+v, %v", changed, err)
	}
	adminLink, err := s.ResetShareLink(ctx, owner, changed.Revision, auth.CompanyRoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(*adminLink.URL, "https://app.example.com/invite/")
	preview, err := s.Preview(ctx, token)
	if err != nil || preview.Type != "link" || preview.Role != auth.CompanyRoleAdmin {
		t.Fatalf("Preview() = %+v, %v", preview, err)
	}
	joined, err := s.Accept(ctx, recipient, token)
	if err != nil || joined.Company.Role != auth.CompanyRoleAdmin {
		t.Fatalf("Accept() = %+v, %v", joined, err)
	}
	var role string
	if err := pool.QueryRow(ctx,
		`SELECT role FROM company_members WHERE company_id=$1 AND user_id=$2 AND status='active'`,
		owner.CompanyID, recipient.UserID,
	).Scan(&role); err != nil || role != "admin" {
		t.Fatalf("stored role = %q, %v", role, err)
	}
}
