//go:build integration

package execution

import (
	"context"
	"errors"
	"testing"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	dbgen "boardly.dev/boardly/server/pkg/db/generated"
)

func TestPublicationSubdomainRoutesWithoutRebuildingAndCleansUpBeforeUnpublish(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	outputID := seedApprovedPublicationOutput(t, ctx, pool, fixture)
	provider := &recordingPublicationProvider{key: publicationTestProviderKey()}
	catalog := recordingPublicationCatalog{provider: provider}
	routes := &recordingPublicationRoutes{}
	service, err := NewPublicationService(
		PublicationServiceOptions{
			Bus:         publicationTestBus(t),
			Store:       store,
			Providers:   catalog,
			Routes:      routes,
			AllowPublic: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := NewPublicationWorker(
		store,
		publicationMemoryStore{content: []byte("application archive")},
		catalog,
		PublicationWorkerOptions{Bus: publicationTestBus(t), Routes: routes, MaxAttempts: 2},
	)
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.UserPrincipal{
		UserID:    fixture.UserA,
		SessionID: dbtest.NewUUID(t),
		AuthKind:  auth.TokenKindBearer,
		CompanyID: fixture.CompanyA,
		Role:      auth.CompanyRoleOwner,
	}
	if _, err := service.Create(
		ctx,
		principal,
		fixture.RunA,
		PublicationCreateInput{ConfirmPublicExposure: true},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	read := func() dbgen.OutputPublication {
		t.Helper()
		row, err := store.Queries().
			GetOutputPublicationByOutput(ctx, dbgen.GetOutputPublicationByOutputParams{CompanyID: fixture.CompanyA, OutputID: outputID})
		if err != nil {
			t.Fatal(err)
		}
		return row
	}
	input := PublicationSubdomainInput{Subdomain: " My-App ", ExpectedRevision: read().Version}
	queued, err := service.SetSubdomain(ctx, principal, fixture.RunA, input)
	if err != nil || queued.Status != PublicationStatusRouting {
		t.Fatalf("SetSubdomain() = %+v, %v", queued, err)
	}
	if _, err := service.SetSubdomain(
		ctx,
		principal,
		fixture.RunA,
		input,
	); !errors.Is(
		err,
		ErrPublicationConflict,
	) {
		t.Fatalf("stale claim error = %v", err)
	}
	routes.putErr = errors.New("KV unavailable")
	failRouting := func() {
		t.Helper()
		for range 2 {
			if _, err := worker.ProcessOnce(ctx); err == nil {
				t.Fatal("routing outage was ignored")
			}
			dbtest.MustExec(
				t,
				ctx,
				pool,
				`UPDATE output_publications SET next_attempt_at = clock_timestamp() WHERE output_id = $1`,
				outputID,
			)
		}
	}
	failRouting()

	if row := read(); row.Status != "failed" || !row.RoutePending || provider.publishCalls != 1 {
		t.Fatalf("failed routing row = %+v, builds = %d", row, provider.publishCalls)
	}
	retried, err := service.Create(
		ctx,
		principal,
		fixture.RunA,
		PublicationCreateInput{ConfirmPublicExposure: true},
	)
	if err != nil || !retried.Created || !read().RoutePending {
		t.Fatalf("publication retry lost pending routing: %+v, %v", retried, err)
	}
	failRouting()
	routes.putErr = nil
	if _, err := service.SetSubdomain(
		ctx,
		principal,
		fixture.RunA,
		PublicationSubdomainInput{Subdomain: "my-app", ExpectedRevision: read().Version},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	view, err := service.Fetch(ctx, principal, fixture.RunA)
	if err != nil || view.Publication.CustomURL == nil ||
		*view.Publication.CustomURL != "https://my-app.apps.example.com" ||
		routes.hostname != "my-app.apps.example.com" ||
		routes.origin != "https://published.boardly.test" ||
		provider.publishCalls != 1 {
		t.Fatalf(
			"routed publication = %+v, routes = %+v, builds = %d, error = %v",
			view.Publication,
			routes,
			provider.publishCalls,
			err,
		)
	}
	if _, err := service.SetSubdomain(
		ctx,
		principal,
		fixture.RunA,
		PublicationSubdomainInput{Subdomain: "different", ExpectedRevision: read().Version},
	); !errors.Is(
		err,
		ErrPublicationConflict,
	) {
		t.Fatalf("rename error = %v", err)
	}
	if _, err := service.Unpublish(
		ctx,
		principal,
		fixture.RunA,
		PublicationUnpublishInput{ExpectedRevision: read().Version},
	); err != nil {
		t.Fatal(err)
	}
	routes.deleteErr = errors.New("KV offline")
	if _, err := worker.ProcessOnce(ctx); err == nil || provider.deleteCalls != 0 {
		t.Fatalf("cleanup failure = %v, provider deletes = %d", err, provider.deleteCalls)
	}
	routes.deleteErr = nil
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE output_publications SET next_attempt_at = clock_timestamp() WHERE output_id = $1`,
		outputID,
	)
	if _, err := worker.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if row := read(); row.Status != "unpublished" || row.CustomHostname.String != "my-app.apps.example.com" ||
		provider.deleteCalls != 1 {
		t.Fatalf("unpublished row = %+v", row)
	}
	if _, err := service.Create(
		ctx,
		principal,
		fixture.RunA,
		PublicationCreateInput{ConfirmPublicExposure: true},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if row := read(); row.Status != "routing" {
		t.Fatalf("republish status = %q", row.Status)
	}
	if _, err := worker.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if routes.putCalls != 6 || provider.publishCalls != 2 {
		t.Fatalf("route puts = %d, builds = %d", routes.putCalls, provider.publishCalls)
	}
}

func TestPublicationSubdomainEnforcesOwnershipAndGlobalNameUniqueness(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	seedApprovedPublicationOutput(t, ctx, pool, fixture)
	second := fixture
	second.CompanyA, second.UserA, second.RunA = fixture.CompanyB, fixture.UserB, fixture.RunB
	seedApprovedPublicationOutput(t, ctx, pool, second)
	provider := &recordingPublicationProvider{key: publicationTestProviderKey()}
	catalog := recordingPublicationCatalog{provider: provider}
	routes := &recordingPublicationRoutes{}
	service, err := NewPublicationService(
		PublicationServiceOptions{
			Bus:         publicationTestBus(t),
			Store:       store,
			Providers:   catalog,
			Routes:      routes,
			AllowPublic: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := NewPublicationWorker(
		store,
		publicationMemoryStore{content: []byte("application archive")},
		catalog,
		PublicationWorkerOptions{Bus: publicationTestBus(t), Routes: routes},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, owner := range []dbtest.Fixture{fixture, second} {
		principal := auth.UserPrincipal{
			UserID:    owner.UserA,
			SessionID: dbtest.NewUUID(t),
			AuthKind:  auth.TokenKindBearer,
			CompanyID: owner.CompanyA,
			Role:      auth.CompanyRoleOwner,
		}
		if _, err := service.Create(
			ctx,
			principal,
			owner.RunA,
			PublicationCreateInput{ConfirmPublicExposure: true},
		); err != nil {
			t.Fatal(err)
		}
		if _, err := worker.ProcessOnce(ctx); err != nil {
			t.Fatal(err)
		}
		view, err := service.Fetch(ctx, principal, owner.RunA)
		if err != nil {
			t.Fatal(err)
		}
		_, err = service.SetSubdomain(
			ctx,
			principal,
			owner.RunA,
			PublicationSubdomainInput{Subdomain: "same-name", ExpectedRevision: view.Publication.Revision},
		)
		if owner.CompanyA == fixture.CompanyA {
			if err != nil {
				t.Fatal(err)
			}
			if _, err := worker.ProcessOnce(ctx); err != nil {
				t.Fatal(err)
			}
		} else if !errors.Is(err, ErrPublicationSubdomainTaken) {
			t.Fatalf("duplicate name error = %v", err)
		}
	}
	principal := auth.UserPrincipal{
		UserID:    fixture.UserB,
		SessionID: dbtest.NewUUID(t),
		AuthKind:  auth.TokenKindBearer,
		CompanyID: fixture.CompanyA,
		Role:      auth.CompanyRoleOwner,
	}
	if _, err := service.SetSubdomain(
		ctx,
		principal,
		fixture.RunA,
		PublicationSubdomainInput{Subdomain: "intruder", ExpectedRevision: 1},
	); !errors.Is(
		err,
		ErrPublicationForbidden,
	) {
		t.Fatalf("cross-company claim error = %v", err)
	}
}

type recordingPublicationRoutes struct {
	hostname, origin string
	putCalls         int
	deleteErr        error
	putErr           error
}

func (*recordingPublicationRoutes) BindingID() string {
	return "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
}
func (*recordingPublicationRoutes) BaseDomain() string { return "apps.example.com" }
func (r *recordingPublicationRoutes) Put(_ context.Context, hostname, origin string) error {
	r.putCalls++
	r.hostname, r.origin = hostname, origin
	return r.putErr
}
func (r *recordingPublicationRoutes) Delete(context.Context, string) error { return r.deleteErr }
