//go:build integration

package execution

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	artifactdomain "github.com/Intelligent-Internet/zenith-team/server/internal/artifact"

	dbgen "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestPublicationCreateFencesConcurrentOwnerDemotion(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	seedApprovedPublicationOutput(t, ctx, pool, fixture)
	provider := integrationPublicationProvider{key: publicationTestProviderKey()}
	service, err := NewPublicationService(PublicationServiceOptions{
		Bus:   publicationTestBus(t),
		Store: store, Providers: integrationPublicationCatalog{provider: provider}, AllowPublic: true,
	})
	if err != nil {
		t.Fatalf("NewPublicationService() error = %v", err)
	}
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, SessionID: dbtest.NewUUID(t), AuthKind: auth.TokenKindBearer,
		CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin Company lock holder: %v", err)
	}
	defer func() { _ = holder.Rollback(context.Background()) }()
	if _, err := holder.Exec(
		ctx, `SELECT id FROM companies WHERE id = $1 FOR UPDATE`, fixture.CompanyA,
	); err != nil {
		t.Fatalf("hold Company lock: %v", err)
	}
	result := make(chan error, 1)
	go func() {
		_, createErr := service.Create(
			ctx,
			principal,
			fixture.RunA,
			PublicationCreateInput{ConfirmPublicExposure: true},
		)
		result <- createErr
	}()
	waitForBlockedOutputMutation(t, ctx, pool)
	if _, err := holder.Exec(ctx, `
		UPDATE company_members
		SET role = 'member', updated_at = clock_timestamp(), version = version + 1
		WHERE company_id = $1 AND user_id = $2`, fixture.CompanyA, fixture.UserA); err != nil {
		t.Fatalf("demote publication owner: %v", err)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatalf("commit owner demotion: %v", err)
	}
	select {
	case createErr := <-result:
		if !errors.Is(createErr, ErrPublicationForbidden) {
			t.Fatalf("Create() error = %v, want ErrPublicationForbidden", createErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Create() did not resume after Company lock release")
	}
	var publicationCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM output_publications`).Scan(&publicationCount); err != nil {
		t.Fatalf("count Output publications: %v", err)
	}
	if publicationCount != 0 {
		t.Fatalf("Output publication count after demotion race = %d, want 0", publicationCount)
	}
}

func TestPublicationServiceQueuesApprovedPrimaryZIPAndRevalidatesOwner(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	provider := integrationPublicationProvider{key: PublicationProviderKey{
		Type:      PublicationProviderTypeCloudRun,
		BindingID: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}
	bus := publicationTestBus(t)
	hints := 0
	if err := bus.Subscribe(
		artifactdomain.EventChanged,
		func(context.Context, events.Event) error { hints++; return nil },
	); err != nil {
		t.Fatal(err)
	}
	service, err := NewPublicationService(PublicationServiceOptions{
		Bus:   bus,
		Store: store, Providers: integrationPublicationCatalog{provider: provider}, AllowPublic: true,
	})
	if err != nil {
		t.Fatalf("NewPublicationService() error = %v", err)
	}

	attachmentID := dbtest.NewUUID(t)
	assetID := dbtest.NewUUID(t)
	assetVersionID := dbtest.NewUUID(t)
	outputID := dbtest.NewUUID(t)
	outputItemID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO attachments (
			id, company_id, uploaded_by_user_id, request_key,
			filename, content_type, size_bytes, status
		) VALUES ($1, $2, $3, $4, 'application.zip', 'application/zip', 4, 'ready')`,
		attachmentID, fixture.CompanyA, fixture.UserA, dbtest.NewUUID(t),
	)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO assets (id, company_id, title, media_class, created_by_user_id)
		VALUES ($1, $2, 'Published application', 'file', $3)`,
		assetID, fixture.CompanyA, fixture.UserA,
	)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO asset_versions (
			id, company_id, asset_id, version_number, attachment_id, source_run_id,
			filename, content_type, size_bytes, integrity_status
		) VALUES ($1, $2, $3, 1, $4, $5, 'application.zip', 'application/zip', 4, 'verified')`,
		assetVersionID, fixture.CompanyA, assetID, attachmentID, fixture.RunA,
	)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO outputs (id, company_id, run_id, title, summary, presentation_fingerprint, owner_user_id, conversation_id, task_id, project_id, task_execution_generation, actor_agent_id) SELECT $1, $2, $3, 'Application', 'Ready to publish', $4, $5, origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id
 FROM runs origin JOIN conversations conversation ON conversation.company_id=origin.company_id AND conversation.id=origin.conversation_id WHERE origin.company_id=$2 AND origin.id=$3`,
		outputID, fixture.CompanyA, fixture.RunA,
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", fixture.UserA,
	)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO output_items (id, company_id, output_id, ordinal, role, label, asset_version_id)
		VALUES ($1, $2, $3, 0, 'primary', 'Application source', $4)`,
		outputItemID, fixture.CompanyA, outputID, assetVersionID,
	)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO deliverables (
			company_id, output_id, status, submitted_at, reviewed_by_user_id, reviewed_at
		) VALUES ($1, $2, 'approved', statement_timestamp(), $3, statement_timestamp())`,
		fixture.CompanyA, outputID, fixture.UserA,
	)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE runs SET status = 'completed', updated_at = statement_timestamp() WHERE id = $1`,
		fixture.RunA,
	)

	principal := auth.UserPrincipal{
		UserID: fixture.UserA, SessionID: dbtest.NewUUID(t), AuthKind: auth.TokenKindBearer,
		CompanyID: fixture.CompanyA,
		// The locked membership is authoritative even if a token still carries
		// the caller's earlier role.
		Role: auth.CompanyRoleMember,
	}
	created, err := service.Create(ctx, principal, fixture.RunA,
		PublicationCreateInput{ConfirmPublicExposure: true})
	if err != nil || !created.Created || created.Publication.OutputID != outputID ||
		created.Publication.Status != PublicationStatusQueued || created.Publication.Revision != 1 {
		t.Fatalf("Create() = %+v, %v", created, err)
	}
	view, err := service.Fetch(ctx, principal, fixture.RunA)
	if err != nil || view.Publication == nil || view.Publication.ID != created.Publication.ID ||
		view.CanPublish || view.CanUnpublish {
		t.Fatalf("Fetch() = %+v, %v", view, err)
	}
	replay, err := service.Create(ctx, principal, fixture.RunA,
		PublicationCreateInput{ConfirmPublicExposure: true})
	if err != nil || replay.Created || replay.Publication.ID != created.Publication.ID {
		t.Fatalf("Create(replay) = %+v, %v", replay, err)
	}

	dbtest.MustExec(t, ctx, pool, `
		UPDATE company_members SET role = 'member', version = version + 1
		WHERE company_id = $1 AND user_id = $2`, fixture.CompanyA, fixture.UserA)
	if _, err := service.Create(ctx, principal, fixture.RunA,
		PublicationCreateInput{ConfirmPublicExposure: true}); !errors.Is(err, ErrPublicationForbidden) {
		t.Fatalf("Create(after owner demotion) error = %v, want ErrPublicationForbidden", err)
	}
	if hints != 1 {
		t.Fatalf("publication replay or rejected write emitted a hint: %d", hints)
	}
}

type integrationPublicationProvider struct {
	key PublicationProviderKey
}

func (p integrationPublicationProvider) Key() PublicationProviderKey { return p.key }

func (integrationPublicationProvider) Publish(
	context.Context, PublicationProviderRequest, io.Reader, PublicationProviderObserver,
) (PublicationProviderResult, error) {
	return PublicationProviderResult{}, nil
}

func (integrationPublicationProvider) Delete(
	context.Context, PublicationProviderDeleteRequest,
) error {
	return nil
}

type integrationPublicationCatalog struct {
	provider   integrationPublicationProvider
	additional []integrationPublicationProvider
}

func (c integrationPublicationCatalog) Default() (PublicationProvider, bool) {
	return c.provider, c.provider.key != (PublicationProviderKey{})
}

func (c integrationPublicationCatalog) Resolve(key PublicationProviderKey) (PublicationProvider, bool) {
	if key == c.provider.key {
		return c.provider, true
	}
	for _, provider := range c.additional {
		if provider.key == key {
			return provider, true
		}
	}
	return nil, false
}

func TestPublicationRetryPreservesExistingProviderResources(t *testing.T) {
	for _, test := range []struct {
		name         string
		keepOriginal bool
		failureCode  PublicationFailureCode
	}{
		{name: "original provider available", keepOriginal: true, failureCode: PublicationFailureProviderUnavailable},
		{name: "original provider missing", failureCode: PublicationFailureProviderUnavailable},
		{name: "deployment failed with original provider available", keepOriginal: true, failureCode: PublicationFailureDeployFailed},
		{name: "deployment failed with original provider missing", failureCode: PublicationFailureDeployFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			pool, _ := dbtest.StartPostgres(t)
			ctx := t.Context()
			if err := db.MigrateUp(ctx, pool); err != nil {
				t.Fatal(err)
			}
			fixture := dbtest.SeedFixture(t, ctx, pool)
			outputID := seedApprovedPublicationOutput(t, ctx, pool, fixture)
			store, err := db.NewStore(pool)
			if err != nil {
				t.Fatal(err)
			}
			original := integrationPublicationProvider{key: publicationTestProviderKey()}
			catalog := integrationPublicationCatalog{provider: original}
			service, err := NewPublicationService(
				PublicationServiceOptions{
					Bus:         publicationTestBus(t),
					Store:       store,
					Providers:   catalog,
					AllowPublic: true,
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			principal := auth.UserPrincipal{
				UserID:    fixture.UserA,
				CompanyID: fixture.CompanyA,
				Role:      auth.CompanyRoleOwner,
			}
			created, err := service.Create(
				ctx,
				principal,
				fixture.RunA,
				PublicationCreateInput{ConfirmPublicExposure: true},
			)
			if err != nil {
				t.Fatal(err)
			}
			dbtest.MustExec(t, ctx, pool, `UPDATE output_publications SET status = 'failed',
                failure_code = $2, failure_detail = 'Provider failed', provider_resources_may_exist = true,
                source_ref = 'provider://source', build_ref = 'provider://build', service_ref = 'provider://service',
                public_url = 'https://existing.example.com', attempt_count = 3 WHERE id = $1`, created.Publication.ID, string(test.failureCode))
			next := original
			next.key.BindingID = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			catalog = integrationPublicationCatalog{provider: next}
			if test.keepOriginal {
				catalog.additional = []integrationPublicationProvider{original}
			}
			service, err = NewPublicationService(
				PublicationServiceOptions{
					Bus:         publicationTestBus(t),
					Store:       store,
					Providers:   catalog,
					AllowPublic: true,
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			view, err := service.Fetch(ctx, principal, fixture.RunA)
			if err != nil || view.CanPublish != test.keepOriginal {
				t.Fatalf("Fetch() = %+v, %v", view, err)
			}
			retried, err := service.Create(
				ctx,
				principal,
				fixture.RunA,
				PublicationCreateInput{ConfirmPublicExposure: true},
			)
			if !test.keepOriginal {
				if !errors.Is(err, ErrPublicationDisabled) {
					t.Fatalf("retry without original provider error = %v", err)
				}
				return
			}
			if err != nil || !retried.Created || retried.Publication.ID != created.Publication.ID {
				t.Fatalf("Create(retry) = %+v, %v", retried, err)
			}
			row, err := store.Queries().
				GetOutputPublicationByOutput(ctx, dbgen.GetOutputPublicationByOutputParams{CompanyID: fixture.CompanyA, OutputID: outputID})
			if err != nil || row.Status != "queued" || row.ProviderBindingID != original.key.BindingID ||
				!row.ProviderResourcesMayExist ||
				row.SourceRef.String != "provider://source" ||
				row.BuildRef.String != "provider://build" ||
				row.ServiceRef.String != "provider://service" ||
				row.PublicUrl.String != "https://existing.example.com" ||
				row.AttemptCount != 0 {
				t.Fatalf("retry lost resource ownership: %+v, %v", row, err)
			}
			dbtest.MustExec(
				t,
				ctx,
				pool,
				`UPDATE output_publications SET status = 'failed', failure_code = $2, failure_detail = 'Provider failed' WHERE id = $1`,
				row.ID,
				string(test.failureCode),
			)
			if _, err := store.Queries().RequeueOutputPublication(ctx, dbgen.RequeueOutputPublicationParams{
				PublicationID:     row.ID,
				CompanyID:         row.CompanyID,
				OutputID:          row.OutputID,
				OutputItemID:      row.OutputItemID,
				RequestedByUserID: fixture.UserA,
				ProviderType:      string(next.key.Type),
				ProviderBindingID: next.key.BindingID,
			}); err == nil {
				t.Fatal("query allowed rebinding existing resources")
			}
		})
	}
}
