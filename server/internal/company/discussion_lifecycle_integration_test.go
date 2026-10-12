//go:build integration

package company

import (
	"context"
	"testing"

	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

func TestRemoveMemberRevokesDiscussionGrantInSameCommit(t *testing.T) {
	invites, pool, owner, recipient, sender := invitationFixture(t)
	_, token := createInvitationToken(t, invites, owner, sender)
	ctx := context.Background()
	if _, err := invites.Accept(ctx, recipient, token); err != nil {
		t.Fatal(err)
	}
	id := dbtest.SeedDiscussionChannel(t, ctx, pool, owner.CompanyID, recipient.UserID, nil)
	var revision int64
	if err := pool.QueryRow(ctx, `SELECT version FROM company_members WHERE company_id=$1 AND user_id=$2`, owner.CompanyID, recipient.UserID).
		Scan(&revision); err != nil {
		t.Fatal(err)
	}
	service := &DefaultCompanyService{store: invites.store, bus: invites.bus}
	if err := service.RemoveMember(
		ctx,
		owner,
		MembershipMutation{UserID: recipient.UserID, ExpectedRevision: revision},
	); err != nil {
		t.Fatal(err)
	}
	var revoked, archived bool
	if err := pool.QueryRow(ctx, `SELECT m.status='revoked',c.archived_at IS NOT NULL FROM conversation_members m JOIN conversations c ON c.id=m.conversation_id WHERE c.id=$1`, id).
		Scan(&revoked, &archived); err != nil {
		t.Fatal(err)
	}
	if !revoked || !archived {
		t.Fatalf("revoke/empty archive=%v/%v", revoked, archived)
	}
}

func TestGeneralChannelFollowsCompanyReactivation(t *testing.T) {
	invites, pool, owner, recipient, sender := invitationFixture(t)
	ctx := context.Background()
	_, token := createInvitationToken(t, invites, owner, sender)
	if _, err := invites.Accept(ctx, recipient, token); err != nil {
		t.Fatal(err)
	}
	var generalID string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM conversations WHERE company_id=$1 AND is_general`, owner.CompanyID).
		Scan(&generalID); err != nil {
		t.Fatal(err)
	}
	var revision int64
	if err := pool.QueryRow(ctx, `SELECT version FROM company_members WHERE company_id=$1 AND user_id=$2`, owner.CompanyID, recipient.UserID).
		Scan(&revision); err != nil {
		t.Fatal(err)
	}
	service := &DefaultCompanyService{store: invites.store, bus: invites.bus}
	if err := service.RemoveMember(
		ctx,
		owner,
		MembershipMutation{UserID: recipient.UserID, ExpectedRevision: revision},
	); err != nil {
		t.Fatal(err)
	}
	var status string
	var archived bool
	if err := pool.QueryRow(ctx, `SELECT m.status,c.archived_at IS NOT NULL FROM conversation_members m JOIN conversations c ON c.id=m.conversation_id WHERE c.id=$1 AND m.user_id=$2`, generalID, recipient.UserID).
		Scan(&status, &archived); err != nil {
		t.Fatal(err)
	}
	if status != "revoked" || archived {
		t.Fatalf("removed status=%s archived=%v", status, archived)
	}
	_, token = createInvitationToken(t, invites, owner, sender)
	if _, err := invites.Accept(ctx, recipient, token); err != nil {
		t.Fatal(err)
	}
	var generation int64
	if err := pool.QueryRow(ctx, `SELECT status,generation FROM conversation_members WHERE conversation_id=$1 AND user_id=$2`, generalID, recipient.UserID).
		Scan(&status, &generation); err != nil {
		t.Fatal(err)
	}
	if status != "active" || generation != 2 {
		t.Fatalf("rejoined status=%s generation=%d", status, generation)
	}
}
