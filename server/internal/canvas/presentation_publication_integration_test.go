//go:build integration

package canvas

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestPresentationPublicationLifecycle(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, _ := db.NewStore(pool)
	bus, _ := events.NewBus(events.BusOptions{})
	pages := NewPageService(store, bus)
	service := NewPresentationService(store, bus, nil)
	principal := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	page, err := pages.CreatePage(
		ctx,
		principal,
		f.ProjectA,
		PageCreateInput{RequestKey: uuid.New(), Title: "Publication", Markdown: "Retained public content"},
	)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := service.PreviewPresentation(ctx, principal, f.ProjectA, page.PageID, page.Sequence)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := service.CapturePresentation(
		ctx,
		principal,
		f.ProjectA,
		PresentationInput{
			PageID:      page.PageID,
			Sequence:    page.Sequence,
			Fingerprint: preview.Fingerprint,
			RequestKey:  uuid.New(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	input := PublicationInput{RequestKey: uuid.New(), ConfirmPublicExposure: true}
	if _, err := service.PublishPresentation(
		ctx,
		principal,
		f.ProjectA,
		saved.ID,
		PublicationInput{RequestKey: uuid.New()},
	); !errors.Is(
		err,
		ErrInvalid,
	) {
		t.Fatalf("unconfirmed: %v", err)
	}
	publication, err := service.PublishPresentation(ctx, principal, f.ProjectA, saved.ID, input)
	if err != nil || publication.ID == nil || publication.PublicToken == nil {
		t.Fatalf("publish: %+v %v", publication, err)
	}
	replay, err := service.PublishPresentation(ctx, principal, f.ProjectA, saved.ID, input)
	if err != nil || *replay.ID != *publication.ID {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	if _, err := service.PublishPresentation(
		ctx,
		principal,
		f.ProjectA,
		saved.ID,
		PublicationInput{RequestKey: uuid.New(), ConfirmPublicExposure: true},
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("second active: %v", err)
	}
	html, err := service.PublicPresentationHTML(ctx, *publication.PublicToken)
	if err != nil || !strings.Contains(html, "Retained public content") {
		t.Fatalf("public HTML: %q %v", html, err)
	}
	// A revoked membership cannot use a stale principal to publish or manage.
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE company_members SET role='member' WHERE company_id=$1 AND user_id=$2`,
		f.CompanyA,
		f.UserA,
	)
	if _, err := service.Publication(ctx, principal, f.ProjectA, saved.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("stale role: %v", err)
	}
	retained, err := service.PublicPresentationHTML(ctx, *publication.PublicToken)
	if err != nil || retained != html {
		t.Fatalf("public recipient depends on publisher role: %v", err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE company_members SET role='owner' WHERE company_id=$1 AND user_id=$2`,
		f.CompanyA,
		f.UserA,
	)
	// Archive hides the public copy, while revocation remains available.
	dbtest.MustExec(t, ctx, pool, `UPDATE projects SET archived_at=now() WHERE id=$1`, f.ProjectA)
	if _, err := service.PublicPresentationHTML(ctx, *publication.PublicToken); !errors.Is(err, ErrNotFound) {
		t.Fatalf("archived public read: %v", err)
	}
	if err := service.RevokePublication(ctx, principal, f.ProjectA, saved.ID, *publication.ID); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE projects SET archived_at=NULL WHERE id=$1`, f.ProjectA)
	if _, err := service.PublicPresentationHTML(ctx, *publication.PublicToken); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked public read: %v", err)
	}
	replay, err = service.PublishPresentation(ctx, principal, f.ProjectA, saved.ID, input)
	if err != nil || !replay.Revoked || replay.PublicToken != nil {
		t.Fatalf("revoked receipt: %+v %v", replay, err)
	}
	next, err := service.PublishPresentation(
		ctx,
		principal,
		f.ProjectA,
		saved.ID,
		PublicationInput{RequestKey: uuid.New(), ConfirmPublicExposure: true},
	)
	if err != nil || next.PublicToken == nil || *next.PublicToken == *publication.PublicToken {
		t.Fatalf("republish: %+v %v", next, err)
	}
	if err := service.RevokePublication(ctx, principal, f.ProjectA, saved.ID, *publication.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.PublicPresentationHTML(ctx, *next.PublicToken); err != nil {
		t.Fatalf("delayed revoke affected new publication: %v", err)
	}
	wrongCompany := principal
	wrongCompany.CompanyID = f.CompanyB
	if err := service.RevokePublication(ctx, wrongCompany, f.ProjectA, saved.ID, *next.ID); err == nil {
		t.Fatal("cross-company revoke allowed")
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE companies SET deleting_at=now() WHERE id=$1`, f.CompanyA)
	if _, err := service.PublicPresentationHTML(ctx, *next.PublicToken); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting company: %v", err)
	}
}
