//go:build integration

package execution

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	dbgen "boardly.dev/boardly/server/pkg/db/generated"
)

func TestPublicationWorkerPublishesAndUnpublishesDurably(t *testing.T) {
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
	outputID := seedApprovedPublicationOutput(t, ctx, pool, fixture)
	bus := publicationTestBus(t)
	var states []string
	if err := bus.Subscribe(TopicArtifact, func(_ context.Context, event events.Event) error {
		if event.Name != EventArtifactChanged {
			t.Fatalf("unexpected publication event %s", event.Name)
		}
		change, err := events.PayloadOf[ArtifactChanged](event)
		if err != nil || !slices.Equal(change.RecipientUserIDs, []uuid.UUID{fixture.UserA}) {
			t.Fatalf("publication audience = %+v, %v", change, err)
		}
		row, err := store.Queries().
			GetOutputPublicationByOutput(ctx, dbgen.GetOutputPublicationByOutputParams{CompanyID: fixture.CompanyA, OutputID: outputID})
		if err != nil {
			t.Fatalf("event preceded publication commit: %v", err)
		}
		states = append(states, row.Status)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	provider := &recordingPublicationProvider{key: publicationTestProviderKey()}
	catalog := recordingPublicationCatalog{provider: provider}
	service, err := NewPublicationService(
		PublicationServiceOptions{Store: store, Bus: bus, Providers: catalog, AllowPublic: true},
	)
	if err != nil {
		t.Fatalf("NewPublicationService() error = %v", err)
	}
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, SessionID: dbtest.NewUUID(t), AuthKind: auth.TokenKindBearer,
		CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	created, err := service.Create(
		ctx,
		principal,
		fixture.RunA,
		PublicationCreateInput{ConfirmPublicExposure: true},
	)
	if err != nil || !created.Created {
		t.Fatalf("Create() = %+v, %v", created, err)
	}
	worker, err := NewPublicationWorker(
		store,
		publicationMemoryStore{content: []byte("application archive")},
		catalog,
		PublicationWorkerOptions{Bus: bus},
	)
	if err != nil {
		t.Fatalf("NewPublicationWorker() error = %v", err)
	}
	worked, err := worker.ProcessOnce(ctx)
	if err != nil || !worked {
		t.Fatalf("ProcessOnce(publish) = %t, %v", worked, err)
	}
	row, err := store.Queries().GetOutputPublicationByOutput(
		ctx,
		dbgen.GetOutputPublicationByOutputParams{CompanyID: fixture.CompanyA, OutputID: outputID},
	)
	if err != nil {
		t.Fatalf("GetOutputPublicationByOutput() error = %v", err)
	}
	if row.Status != string(PublicationStatusPublished) || !row.PublicUrl.Valid ||
		row.PublicUrl.String != "https://published.boardly.test" || provider.publishCalls != 1 ||
		provider.publishedSource != "application archive" {
		t.Fatalf("published row/provider = %+v / %+v", row, provider)
	}

	unpublishing, err := service.Unpublish(
		ctx,
		principal,
		fixture.RunA,
		PublicationUnpublishInput{ExpectedRevision: row.Version},
	)
	if err != nil || unpublishing.Status != PublicationStatusUnpublishing {
		t.Fatalf("Unpublish() = %+v, %v", unpublishing, err)
	}
	worked, err = worker.ProcessOnce(ctx)
	if err != nil || !worked {
		t.Fatalf("ProcessOnce(unpublish) = %t, %v", worked, err)
	}
	row, err = store.Queries().GetOutputPublicationByOutput(
		ctx,
		dbgen.GetOutputPublicationByOutputParams{CompanyID: fixture.CompanyA, OutputID: outputID},
	)
	if err != nil || row.Status != string(PublicationStatusUnpublished) ||
		provider.deleteCalls != 1 || row.PublicUrl.Valid || row.ProviderResourcesMayExist {
		t.Fatalf("unpublished row/provider = %+v / %+v, %v", row, provider, err)
	}

	if _, err := service.Create(ctx, principal, fixture.RunA,
		PublicationCreateInput{ConfirmPublicExposure: true}); err != nil {
		t.Fatalf("Create(after unpublish) error = %v", err)
	}
	processCtx, cancelProcess := context.WithCancel(ctx)
	provider.cancelPublish = cancelProcess
	worked, err = worker.ProcessOnce(processCtx)
	if !worked || !errors.Is(err, context.Canceled) {
		t.Fatalf("ProcessOnce(cancelled publish) = %t, %v", worked, err)
	}
	row, err = store.Queries().GetOutputPublicationByOutput(
		ctx,
		dbgen.GetOutputPublicationByOutputParams{CompanyID: fixture.CompanyA, OutputID: outputID},
	)
	if err != nil || row.Status != string(PublicationStatusQueued) || row.LeaseID.Valid {
		t.Fatalf("cancelled publication row = %+v, %v", row, err)
	}
	for _, status := range []string{"queued", "uploading", "building", "deploying", "published", "unpublishing", "unpublished"} {
		if !slices.Contains(states, status) {
			t.Errorf("missing committed %s event: %v", status, states)
		}
	}
	if len(states) == 0 || states[len(states)-1] != "queued" {
		t.Fatalf("cancelled lease recovery event missing: %v", states)
	}
}

func TestPublicationWorkerRetriesThenPersistsTerminalFailure(t *testing.T) {
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
	outputID := seedApprovedPublicationOutput(t, ctx, pool, fixture)
	provider := &recordingPublicationProvider{
		key: publicationTestProviderKey(),
		publishErr: NewPublicationProviderFailure(
			PublicationFailureProviderUnavailable,
			true,
			errors.New("provider offline"),
		),
	}
	catalog := recordingPublicationCatalog{provider: provider}
	bus := publicationTestBus(t)
	var states []string
	if err := bus.Subscribe(TopicArtifact, func(ctx context.Context, event events.Event) error {
		if event.Name != EventArtifactChanged {
			return nil
		}
		row, err := store.Queries().
			GetOutputPublicationByOutput(ctx, dbgen.GetOutputPublicationByOutputParams{CompanyID: fixture.CompanyA, OutputID: outputID})
		if err != nil {
			t.Errorf("event before committed publication: %v", err)
			return err
		}
		states = append(states, row.Status)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	service, err := NewPublicationService(
		PublicationServiceOptions{Bus: bus, Store: store, Providers: catalog, AllowPublic: true},
	)
	if err != nil {
		t.Fatalf("NewPublicationService() error = %v", err)
	}
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, SessionID: dbtest.NewUUID(t), AuthKind: auth.TokenKindBearer,
		CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	if _, err := service.Create(ctx, principal, fixture.RunA,
		PublicationCreateInput{ConfirmPublicExposure: true}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	worker, err := NewPublicationWorker(
		store,
		publicationMemoryStore{content: []byte("application archive")},
		catalog,
		PublicationWorkerOptions{Bus: bus, MaxAttempts: 2},
	)
	if err != nil {
		t.Fatalf("NewPublicationWorker() error = %v", err)
	}
	if worked, processErr := worker.ProcessOnce(ctx); !worked ||
		!errors.Is(processErr, errPublicationProcessing) {
		t.Fatalf("ProcessOnce(first failure) = %t, %v", worked, processErr)
	}
	if len(states) < 2 || states[len(states)-1] != "queued" {
		t.Fatalf("retry hint missing: %v", states)
	}
	dbtest.MustExec(t, ctx, pool, `
		UPDATE output_publications SET next_attempt_at = clock_timestamp() - interval '1 second'
		WHERE output_id = $1`, outputID)
	if worked, processErr := worker.ProcessOnce(ctx); !worked ||
		!errors.Is(processErr, errPublicationProcessing) {
		t.Fatalf("ProcessOnce(terminal failure) = %t, %v", worked, processErr)
	}
	row, err := store.Queries().GetOutputPublicationByOutput(
		ctx,
		dbgen.GetOutputPublicationByOutputParams{CompanyID: fixture.CompanyA, OutputID: outputID},
	)
	if err != nil || row.Status != string(PublicationStatusFailed) || row.AttemptCount != 2 ||
		!row.FailureCode.Valid || row.FailureCode.String != string(PublicationFailureProviderUnavailable) ||
		row.LeaseID.Valid {
		t.Fatalf("failed publication row = %+v, %v", row, err)
	}

	if states[len(states)-1] != "failed" {
		t.Fatalf("failure hint missing: %v", states)
	}
	beforeExpiry := len(states)
	expiredLeaseID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE output_publications
		SET status = 'uploading', attempt_count = 2, lease_id = $2,
			lease_expires_at = clock_timestamp() - interval '1 second',
			failure_code = NULL, failure_detail = NULL
		WHERE output_id = $1`, outputID, expiredLeaseID)
	worked, processErr := worker.ProcessOnce(ctx)
	if processErr != nil || !worked {
		t.Fatalf("ProcessOnce(expired exhausted lease) = %t, %v", worked, processErr)
	}
	row, err = store.Queries().GetOutputPublicationByOutput(
		ctx,
		dbgen.GetOutputPublicationByOutputParams{CompanyID: fixture.CompanyA, OutputID: outputID},
	)
	if err != nil || row.Status != string(PublicationStatusFailed) || row.LeaseID.Valid ||
		!row.FailureCode.Valid {
		t.Fatalf("expired publication row = %+v, %v", row, err)
	}
	if len(states) != beforeExpiry+1 || states[len(states)-1] != "failed" {
		t.Fatalf("expired-lease hint missing: %v", states)
	}
}

func seedApprovedPublicationOutput(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture dbtest.Fixture,
) uuid.UUID {
	t.Helper()
	attachmentID := dbtest.NewUUID(t)
	assetID := dbtest.NewUUID(t)
	assetVersionID := dbtest.NewUUID(t)
	outputID := dbtest.NewUUID(t)
	outputItemID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO attachments (
			id, company_id, uploaded_by_user_id, request_key,
			filename, content_type, size_bytes, status
		) VALUES ($1, $2, $3, $4, 'application.zip', 'application/zip', 19, 'ready')`,
		attachmentID, fixture.CompanyA, fixture.UserA, dbtest.NewUUID(t))
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO assets (id, company_id, title, media_class, created_by_user_id)
		VALUES ($1, $2, 'Published application', 'file', $3)`,
		assetID, fixture.CompanyA, fixture.UserA)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO asset_versions (
			id, company_id, asset_id, version_number, attachment_id, source_run_id,
			filename, content_type, size_bytes, integrity_status
		) VALUES ($1, $2, $3, 1, $4, $5, 'application.zip', 'application/zip', 19, 'verified')`,
		assetVersionID, fixture.CompanyA, assetID, attachmentID, fixture.RunA)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO outputs (id, company_id, run_id, title, summary, presentation_fingerprint, owner_user_id, conversation_id, task_id, project_id, task_execution_generation, actor_agent_id) SELECT $1, $2, $3, 'Application', 'Ready to publish', $4, $5, origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id
 FROM runs origin JOIN conversations conversation ON conversation.company_id=origin.company_id AND conversation.id=origin.conversation_id WHERE origin.company_id=$2 AND origin.id=$3`,
		outputID, fixture.CompanyA, fixture.RunA,
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", fixture.UserA)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO output_items (id, company_id, output_id, ordinal, role, label, asset_version_id)
		VALUES ($1, $2, $3, 0, 'primary', 'Application source', $4)`,
		outputItemID, fixture.CompanyA, outputID, assetVersionID)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO deliverables (
			company_id, output_id, status, submitted_at, reviewed_by_user_id, reviewed_at
		) VALUES ($1, $2, 'approved', statement_timestamp(), $3, statement_timestamp())`,
		fixture.CompanyA, outputID, fixture.UserA)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE runs SET status = 'completed', updated_at = statement_timestamp() WHERE id = $1`,
		fixture.RunA)
	return outputID
}

type publicationMemoryStore struct {
	content []byte
}

func (s publicationMemoryStore) GetReader(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.content)), nil
}

type recordingPublicationProvider struct {
	key             PublicationProviderKey
	publishErr      error
	cancelPublish   context.CancelFunc
	publishCalls    int
	deleteCalls     int
	publishedSource string
}

func (p *recordingPublicationProvider) Key() PublicationProviderKey { return p.key }

func (p *recordingPublicationProvider) Publish(
	ctx context.Context,
	_ PublicationProviderRequest,
	source io.Reader,
	observe PublicationProviderObserver,
) (PublicationProviderResult, error) {
	p.publishCalls++
	content, err := io.ReadAll(source)
	if err != nil {
		return PublicationProviderResult{}, err
	}
	p.publishedSource = string(content)
	if p.cancelPublish != nil {
		p.cancelPublish()
		return PublicationProviderResult{}, ctx.Err()
	}
	if p.publishErr != nil {
		return PublicationProviderResult{}, p.publishErr
	}
	if err := observe(ctx, PublicationProviderProgress{
		Stage: PublicationProviderStageBuilding,
		PublicationProviderResources: PublicationProviderResources{
			SourceRef: "provider://sources/publication", SourceTemplate: "static_html",
		},
	}); err != nil {
		return PublicationProviderResult{}, err
	}
	if err := observe(ctx, PublicationProviderProgress{
		Stage: PublicationProviderStageDeploying,
		PublicationProviderResources: PublicationProviderResources{
			BuildRef:       "provider://builds/build-1",
			ArtifactRef:    "provider://artifacts/image",
			ArtifactDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
	}); err != nil {
		return PublicationProviderResult{}, err
	}
	return PublicationProviderResult{
		PublicationProviderResources: PublicationProviderResources{
			SourceRef: "provider://sources/publication", SourceTemplate: "static_html",
			BuildRef: "provider://builds/build-1", ArtifactRef: "provider://artifacts/image",
			ArtifactDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			ServiceRef:     "provider://services/application",
		},
		ServiceURI: "https://published.boardly.test",
	}, nil
}

func (p *recordingPublicationProvider) Delete(
	context.Context,
	PublicationProviderDeleteRequest,
) error {
	p.deleteCalls++
	return nil
}

type recordingPublicationCatalog struct {
	provider *recordingPublicationProvider
}

func (c recordingPublicationCatalog) Default() (PublicationProvider, bool) {
	return c.provider, c.provider != nil
}

func (c recordingPublicationCatalog) Resolve(key PublicationProviderKey) (PublicationProvider, bool) {
	return c.provider, c.provider != nil && key == c.provider.key
}

func publicationTestProviderKey() PublicationProviderKey {
	return PublicationProviderKey{
		Type:      PublicationProviderTypeCloudRun,
		BindingID: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
}

func waitForBlockedOutputMutation(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		var count int
		if err := pool.QueryRow(ctx, `
			SELECT count(*)
			FROM pg_stat_activity
			WHERE datname = current_database()
			  AND pid <> pg_backend_pid()
			  AND wait_event_type = 'Lock'
			  AND query LIKE '%LockCompanyForMembershipMutation%'`).Scan(&count); err != nil {
			t.Fatalf("count blocked Output mutations: %v", err)
		}
		if count > 0 {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for blocked Output mutation: %v", ctx.Err())
		case <-timeout.C:
			t.Fatal("Output mutation did not block on the Company lock")
		case <-ticker.C:
		}
	}
}
