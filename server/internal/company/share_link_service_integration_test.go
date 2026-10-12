//go:build integration

package company

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

func TestShareLinkLifecycleAndRemovedMemberCannotRejoin(t *testing.T) {
	t.Parallel()
	s, _, owner, recipient, sender := invitationFixture(t)
	ctx := context.Background()
	initial, err := s.GetShareLink(ctx, owner)
	if err != nil || initial.URL != nil {
		t.Fatalf("initial=%+v %v", initial, err)
	}
	link, err := s.CreateShareLink(ctx, owner, auth.CompanyRoleMember)
	if err != nil || link.URL == nil {
		t.Fatalf("create=%+v %v", link, err)
	}
	again, err := s.CreateShareLink(ctx, owner, auth.CompanyRoleMember)
	if err != nil || *again.URL != *link.URL {
		t.Fatalf("duplicate create=%+v %v", again, err)
	}
	token := strings.TrimPrefix(*link.URL, "https://app.example.com/invite/")
	preview, err := s.Preview(ctx, token)
	if err != nil || preview.Type != "link" || preview.State != "pending" {
		t.Fatalf("preview=%+v %v", preview, err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.Accept(ctx, recipient, token); results <- err }()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	recipient.CompanyID = owner.CompanyID
	recipient.Role = auth.CompanyRoleMember
	if _, err := s.CreateShareLink(ctx, recipient, auth.CompanyRoleMember); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member created link: %v", err)
	}
	svc := &DefaultCompanyService{store: s.store, bus: s.bus}
	if err := svc.RemoveMember(
		ctx,
		owner,
		MembershipMutation{UserID: recipient.UserID, ExpectedRevision: 1},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Accept(ctx, recipient, token); !errors.Is(err, ErrShareLinkMembershipRemoved) {
		t.Fatalf("removed rejoined: %v", err)
	}
	_, freshToken := createInvitationToken(t, s, owner, sender)
	if _, err := s.Accept(ctx, recipient, freshToken); err != nil {
		t.Fatalf("fresh owner invitation: %v", err)
	}
	if _, err := s.ResetShareLink(ctx, owner, link.Revision+1, ""); !errors.Is(err, ErrInvitationConflict) {
		t.Fatalf("stale reset: %v", err)
	}
	reset, err := s.ResetShareLink(ctx, owner, link.Revision, "")
	if err != nil || reset.URL == nil || *reset.URL == *link.URL {
		t.Fatalf("reset=%+v %v", reset, err)
	}
	if _, err := s.Accept(ctx, recipient, token); !errors.Is(err, ErrInvitationNotFound) {
		t.Fatalf("old token accepted: %v", err)
	}
	token = strings.TrimPrefix(*reset.URL, "https://app.example.com/invite/")
	if _, err := s.DisableShareLink(ctx, owner, reset.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Accept(ctx, recipient, token); !errors.Is(err, ErrInvitationInactive) {
		t.Fatalf("disabled token accepted: %v", err)
	}
}

func TestShareLinkExpiryAndVoluntaryLeave(t *testing.T) {
	t.Parallel()
	s, pool, owner, recipient, _ := invitationFixture(t)
	// Copy links work even if invitation email is not configured.
	s.sender = nil
	ctx := context.Background()
	link, err := s.CreateShareLink(ctx, owner, auth.CompanyRoleMember)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(*link.URL, "https://app.example.com/invite/")
	if _, err = s.Accept(ctx, recipient, token); err != nil {
		t.Fatal(err)
	}
	recipient.CompanyID = owner.CompanyID
	recipient.Role = auth.CompanyRoleMember
	svc := &DefaultCompanyService{store: s.store, bus: s.bus}
	if err = svc.Leave(ctx, recipient, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Accept(ctx, recipient, token); err != nil {
		t.Fatalf("voluntary rejoin: %v", err)
	}
	var generalGeneration int64
	if err := pool.QueryRow(ctx, `SELECT m.generation FROM conversation_members m JOIN conversations c ON c.id=m.conversation_id WHERE c.company_id=$1 AND c.is_general AND m.user_id=$2 AND m.status='active'`, owner.CompanyID, recipient.UserID).
		Scan(&generalGeneration); err != nil {
		t.Fatal(err)
	}
	if generalGeneration != 2 {
		t.Fatalf("General generation after share-link rejoin=%d", generalGeneration)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE company_share_links SET expires_at=now()-interval '1 second' WHERE company_id=$1`,
		owner.CompanyID,
	)
	if _, err = s.Accept(ctx, recipient, token); !errors.Is(err, ErrInvitationInactive) {
		t.Fatalf("expired join: %v", err)
	}
	preview, err := s.Preview(ctx, token)
	if err != nil || preview.State != "expired" {
		t.Fatalf("expired preview=%+v %v", preview, err)
	}
}

func TestShareLinkAllowsDifferentVerifiedAccounts(t *testing.T) {
	t.Parallel()
	s, pool, owner, recipient, _ := invitationFixture(t)
	ctx := context.Background()
	link, err := s.CreateShareLink(ctx, owner, auth.CompanyRoleMember)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(*link.URL, "https://app.example.com/invite/")
	dbtest.MustExec(t, ctx, pool, `UPDATE users SET email_verified_at=NULL WHERE id=$1`, recipient.UserID)
	if _, err = s.Accept(ctx, recipient, token); !errors.Is(err, ErrShareLinkVerificationRequired) {
		t.Fatalf("unverified join=%v", err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE users SET email_verified_at=now() WHERE id=$1`, recipient.UserID)
	var another uuid.UUID
	if err = pool.QueryRow(ctx, `INSERT INTO users (email,display_name,email_verified_at) VALUES ('another@example.com','Another',now()) RETURNING id`).
		Scan(&another); err != nil {
		t.Fatal(err)
	}
	for _, person := range []auth.UserPrincipal{recipient, {UserID: another}} {
		joined, err := s.Accept(ctx, person, token)
		if err != nil || joined.Company.ID != owner.CompanyID || joined.Company.Role != auth.CompanyRoleMember {
			t.Fatalf("join=%+v %v", joined, err)
		}
	}
}
