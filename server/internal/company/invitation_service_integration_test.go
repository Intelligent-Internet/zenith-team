//go:build integration

package company

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

type invitationTestSender struct {
	messages []InvitationEmail
	fail     bool
}

func (s *invitationTestSender) SendInvitation(_ context.Context, message InvitationEmail, _ string) error {
	if s.fail {
		return errors.New("provider unavailable")
	}
	s.messages = append(s.messages, message)
	return nil
}

func invitationFixture(
	t *testing.T,
) (*DefaultInvitationService, *pgxpool.Pool, auth.UserPrincipal, auth.UserPrincipal, *invitationTestSender) {
	t.Helper()
	pool, _ := dbtest.StartPostgres(t)
	ctx := context.Background()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	var recipient uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (email,display_name,email_verified_at) VALUES ('invitee@example.com','Invitee',now()) RETURNING id`).
		Scan(&recipient); err != nil {
		t.Fatal(err)
	}
	sender := &invitationTestSender{}
	s, err := NewInvitationService(
		InvitationOptions{
			Store:                  store,
			Bus:                    companyTestBus(t),
			Sender:                 sender,
			WebAppOrigin:           "https://app.example.com",
			EncryptionKey:          []byte(strings.Repeat("k", 32)),
			ShareLinkEncryptionKey: []byte(strings.Repeat("s", 32)),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return s, pool, auth.UserPrincipal{
			UserID:    fixture.UserA,
			CompanyID: fixture.CompanyA,
			Role:      auth.CompanyRoleOwner,
		}, auth.UserPrincipal{
			UserID: recipient,
		}, sender
}

func createInvitationToken(
	t *testing.T,
	s *DefaultInvitationService,
	owner auth.UserPrincipal,
	sender *invitationTestSender,
) (InvitationResponse, string) {
	t.Helper()
	ctx := context.Background()
	results, err := s.Create(
		ctx,
		owner,
		InvitationCreateInput{Emails: []string{" Invitee@Example.com "}, Role: auth.CompanyRoleMember},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Outcome != "queued" {
		t.Fatalf("results = %+v", results)
	}
	if err = s.Deliver(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sender.messages) == 0 {
		t.Fatal("invitation not sent")
	}
	message := sender.messages[len(sender.messages)-1]
	return *results[0].Invitation, strings.TrimPrefix(message.URL, "https://app.example.com/invite/")
}

func TestInvitationAcceptMatchesAccountAndIsIdempotent(t *testing.T) {
	s, pool, owner, recipient, sender := invitationFixture(t)
	ctx := context.Background()
	invitation, token := createInvitationToken(t, s, owner, sender)
	if _, err := s.Accept(ctx, owner, token); !errors.Is(err, ErrInvitationAccountMismatch) {
		t.Fatalf("mismatch = %v", err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := s.Accept(ctx, recipient, token)
			if err == nil && result.Company.ID != owner.CompanyID {
				err = errors.New("wrong company")
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM company_members WHERE company_id=$1 AND user_id=$2 AND status='active'`, owner.CompanyID, recipient.UserID).
		Scan(&count); err != nil ||
		count != 1 {
		t.Fatalf("members = %d, %v", count, err)
	}
	rows, err := s.List(ctx, owner)
	if err != nil || len(rows) != 1 || rows[0].Status != "accepted" {
		t.Fatalf("list = %+v, %v", rows, err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE company_members SET status='revoked' WHERE company_id=$1 AND user_id=$2`,
		owner.CompanyID,
		recipient.UserID,
	)
	if _, err = s.Accept(ctx, recipient, token); !errors.Is(err, ErrInvitationInactive) {
		t.Fatalf("old invitation regrant = %v", err)
	}
	var audit int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM activity_events WHERE entity_id=$1 AND action='invitation.accepted'`, invitation.ID.String()).
		Scan(&audit); err != nil ||
		audit != 1 {
		t.Fatalf("audit = %d, %v", audit, err)
	}
}

func TestInvitationResendRotatesAndCancelPreventsJoin(t *testing.T) {
	s, pool, owner, recipient, sender := invitationFixture(t)
	ctx := context.Background()
	invitation, token := createInvitationToken(t, s, owner, sender)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE company_invitations SET updated_at=now()-interval '2 minutes' WHERE id=$1`,
		invitation.ID,
	)
	resent, err := s.Resend(
		ctx,
		owner,
		InvitationMutation{ID: invitation.ID, ExpectedRevision: invitation.Revision},
	)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := s.Preview(ctx, token)
	if err != nil || preview.State != "replaced" {
		t.Fatalf("old token = %+v, %v", preview, err)
	}
	if err = s.Deliver(ctx); err != nil {
		t.Fatal(err)
	}
	token = strings.TrimPrefix(sender.messages[len(sender.messages)-1].URL, "https://app.example.com/invite/")
	if _, err = s.Cancel(
		ctx,
		owner,
		InvitationMutation{ID: invitation.ID, ExpectedRevision: resent.Revision},
	); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Accept(ctx, recipient, token); !errors.Is(err, ErrInvitationInactive) {
		t.Fatalf("cancelled accept = %v", err)
	}
}

func TestInvitationExpiryDuplicateAndDeliveryRecovery(t *testing.T) {
	s, pool, owner, _, sender := invitationFixture(t)
	ctx := context.Background()
	sender.fail = true
	results, err := s.Create(
		ctx,
		owner,
		InvitationCreateInput{
			Emails: []string{"invitee@example.com", "INVITEE@example.com"},
			Role:   auth.CompanyRoleMember,
		},
	)
	if err != nil || len(results) != 1 {
		t.Fatalf("create = %+v, %v", results, err)
	}
	if err = s.Deliver(ctx); err != nil {
		t.Fatal(err)
	}
	duplicate, err := s.Create(
		ctx,
		owner,
		InvitationCreateInput{Emails: []string{"invitee@example.com"}, Role: auth.CompanyRoleMember},
	)
	if err != nil || duplicate[0].Outcome != "alreadyInvited" {
		t.Fatalf("duplicate = %+v, %v", duplicate, err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE company_invitation_tokens SET next_attempt_at=now()-interval '1 second'`)
	sender.fail = false
	if err = s.Deliver(ctx); err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(sender.messages[0].URL, "https://app.example.com/invite/")
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE company_invitations SET expires_at=now()-interval '1 second' WHERE id=$1`,
		results[0].Invitation.ID,
	)
	preview, err := s.Preview(ctx, token)
	if err != nil || preview.State != "expired" {
		t.Fatalf("expired = %+v, %v", preview, err)
	}
	if err = s.RequestRenewal(ctx, token); err != nil {
		t.Fatal(err)
	}
	reinvite, err := s.Create(
		ctx,
		owner,
		InvitationCreateInput{Emails: []string{"invitee@example.com"}, Role: auth.CompanyRoleMember},
	)
	if err != nil || reinvite[0].Outcome != "queued" {
		t.Fatalf("reinvite = %+v, %v", reinvite, err)
	}
}

func TestInvitationRejectsUnverifiedAndRevokedOwner(t *testing.T) {
	s, pool, owner, recipient, sender := invitationFixture(t)
	ctx := context.Background()
	_, token := createInvitationToken(t, s, owner, sender)
	dbtest.MustExec(t, ctx, pool, `UPDATE users SET email_verified_at=NULL WHERE id=$1`, recipient.UserID)
	if _, err := s.Accept(ctx, recipient, token); !errors.Is(err, ErrInvitationAccountMismatch) {
		t.Fatalf("unverified = %v", err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE company_members SET status='revoked' WHERE company_id=$1 AND user_id=$2`,
		owner.CompanyID,
		owner.UserID,
	)
	if _, err := s.Create(
		ctx,
		owner,
		InvitationCreateInput{Emails: []string{"other@example.com"}, Role: auth.CompanyRoleMember},
	); !errors.Is(
		err,
		ErrNotFound,
	) {
		t.Fatalf("revoked owner = %v", err)
	}
}

func TestInvitationDeliveryExpiresAndErasesSecretWithoutAnotherJob(t *testing.T) {
	s, pool, owner, _, sender := invitationFixture(t)
	ctx := context.Background()
	results, err := s.Create(
		ctx,
		owner,
		InvitationCreateInput{Emails: []string{"invitee@example.com"}, Role: auth.CompanyRoleMember},
	)
	if err != nil {
		t.Fatal(err)
	}
	id := results[0].Invitation.ID
	var encrypted []byte
	if err := pool.QueryRow(ctx, `SELECT encrypted_token FROM company_invitation_tokens WHERE invitation_id=$1`, id).
		Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if len(encrypted) <= 43 || strings.Contains(string(encrypted), "https://") {
		t.Fatal("queue secret is not encrypted")
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE company_invitation_tokens SET created_at=now()-interval '24 hours' WHERE invitation_id=$1`,
		id,
	)
	if err := s.Deliver(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT delivery_status, encrypted_token FROM company_invitation_tokens WHERE invitation_id=$1`, id).
		Scan(&status, &encrypted); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || encrypted != nil || len(sender.messages) != 0 {
		t.Fatalf(
			"expired delivery = %s, secret bytes %d, messages %d",
			status,
			len(encrypted),
			len(sender.messages),
		)
	}
}

func TestInvitationReactivationDoesNotRestoreOwnerRole(t *testing.T) {
	s, pool, owner, recipient, sender := invitationFixture(t)
	ctx := context.Background()
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members (company_id,user_id,role,status) VALUES ($1,$2,'owner','revoked')`,
		owner.CompanyID,
		recipient.UserID,
	)
	_, token := createInvitationToken(t, s, owner, sender)
	joined, err := s.Accept(ctx, recipient, token)
	if err != nil {
		t.Fatal(err)
	}
	if joined.Company.Role != auth.CompanyRoleMember {
		t.Fatalf("reactivated role = %s", joined.Company.Role)
	}
}

func TestInvitationAcceptAndCancelSerialize(t *testing.T) {
	s, pool, owner, recipient, sender := invitationFixture(t)
	ctx := context.Background()
	invitation, token := createInvitationToken(t, s, owner, sender)
	start := make(chan struct{})
	acceptDone := make(chan error, 1)
	cancelDone := make(chan error, 1)
	go func() { <-start; _, err := s.Accept(ctx, recipient, token); acceptDone <- err }()
	go func() {
		<-start
		_, err := s.Cancel(
			ctx,
			owner,
			InvitationMutation{ID: invitation.ID, ExpectedRevision: invitation.Revision},
		)
		cancelDone <- err
	}()
	close(start)
	accepted, cancelled := <-acceptDone, <-cancelDone
	if accepted == nil && cancelled == nil {
		t.Fatal("both conflicting transitions committed")
	}
	if accepted != nil && !errors.Is(accepted, ErrInvitationInactive) {
		t.Fatal(accepted)
	}
	if cancelled != nil && !errors.Is(cancelled, ErrInvitationConflict) {
		t.Fatal(cancelled)
	}
	var status string
	var members int
	if err := pool.QueryRow(ctx, `SELECT status, (SELECT count(*) FROM company_members WHERE company_id=$2 AND user_id=$3 AND status='active') FROM company_invitations WHERE id=$1`, invitation.ID, owner.CompanyID, recipient.UserID).
		Scan(&status, &members); err != nil {
		t.Fatal(err)
	}
	if (status == "accepted" && members != 1) || (status == "revoked" && members != 0) {
		t.Fatalf("status=%s members=%d", status, members)
	}
}
