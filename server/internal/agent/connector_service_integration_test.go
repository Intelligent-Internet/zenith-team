//go:build integration

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	provideragent "github.com/Intelligent-Internet/zenith-team/server/pkg/agent"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestConnectorDiscoveryPersistsDatabaseIDsAndInvalidatesAfterCommit(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	invalidator := &connectorRunInvalidatorRecorder{}
	bus := mustAgentTestBus(t)
	var changes []events.Event
	if err := bus.Subscribe(TopicRuntime, func(_ context.Context, event events.Event) error {
		changes = append(changes, event)
		// Reading on the pool proves the event follows the commit.
		_, err := store.Queries().GetConnectorDiscoveryGenerationForUpdate(ctx, fixture.runtimeID)
		if err != nil {
			t.Errorf("event preceded committed discovery: %v", err)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	service, err := NewConnectorService(ConnectorServiceOptions{
		Store:          store,
		Bus:            bus,
		RunInvalidator: invalidator,
	})
	if err != nil {
		t.Fatal(err)
	}

	item := ConnectorObservationInput{
		Source: ConnectorSourceCodexApp, NativeID: "github", Name: "GitHub",
		Status: ConnectorStatusAvailable, Action: ConnectorActionInstallAndAuth,
	}
	canonical, err := normalizeConnectorObservation(ConnectorProviderCodex, item)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := connectorManifestDigest([]ConnectorObservationInput{canonical})
	if err != nil {
		t.Fatal(err)
	}
	input := ConnectorDiscoveryInput{
		CompanyID: fixture.companyID, RuntimeID: fixture.runtimeID,
		DaemonID: "daemon-" + fixture.companyID.String(), Generation: uuid.New(),
		ManifestDigest: digest, ObservedAt: time.Now().UTC(),
		Items: []ConnectorObservationInput{item},
	}
	result, err := service.PublishDiscovery(ctx, fixture.principal, input)
	if err != nil || !result.Applied {
		t.Fatalf("PublishDiscovery() = %+v, %v", result, err)
	}
	if len(invalidator.changes) != 1 || invalidator.changes[0].RuntimeID == nil ||
		*invalidator.changes[0].RuntimeID != fixture.runtimeID ||
		invalidator.changes[0].Provider != ConnectorProviderCodex ||
		invalidator.changes[0].Type != ConnectorRunChangeDiscovery {
		t.Fatalf("Run invalidations = %+v", invalidator.changes)
	}

	var connectorID uuid.UUID
	var desiredEnabled bool
	if err := pool.QueryRow(
		ctx, `
		SELECT catalog.id, policies.desired_enabled
		FROM connector_catalog_items AS catalog
		JOIN connector_policies AS policies ON policies.connector_id = catalog.id
		WHERE catalog.company_id = $1 AND catalog.native_id = 'github'`,
		fixture.companyID,
	).Scan(&connectorID, &desiredEnabled); err != nil {
		t.Fatalf("read persisted Connector: %v", err)
	}
	if connectorID == uuid.Nil || connectorID.Version() != 4 || desiredEnabled {
		t.Fatalf("persisted Connector = %s desired=%v", connectorID, desiredEnabled)
	}

	replay, err := service.PublishDiscovery(ctx, fixture.principal, input)
	if err != nil || replay.Applied || len(invalidator.changes) != 1 {
		t.Fatalf("PublishDiscovery(replay) = %+v, %v; invalidations=%+v", replay, err, invalidator.changes)
	}
	stale := input
	stale.Generation = uuid.New()
	stale.ObservedAt = input.ObservedAt.Add(-time.Second)
	if _, err := service.PublishDiscovery(ctx, fixture.principal, stale); !errors.Is(
		err,
		ErrConnectorDiscoveryConflict,
	) {
		t.Fatalf("PublishDiscovery(stale) error = %v", err)
	}
	page, err := service.List(ctx, fixture.principal, ConnectorListInput{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(page.Connectors) != 1 || page.Connectors[0].ID != connectorID ||
		page.Connectors[0].NativeID != "github" || page.Connectors[0].EffectiveEnabled {
		t.Fatalf("List() = %+v", page)
	}
	if len(changes) != 1 || changes[0].Name != EventRuntimeCapabilitiesChanged {
		t.Fatalf("discovery events (including replay/conflict) = %+v", changes)
	}
	change, err := events.PayloadOf[CapabilitiesChanged](changes[0])
	if err != nil || change.UserID != fixture.principal.UserID || change.RuntimeID != fixture.runtimeID ||
		change.Catalog != "connector" {
		t.Fatalf("discovery routing = %+v, %v", change, err)
	}
	_, err = service.ReaffirmDiscovery(ctx, fixture.principal, ConnectorDiscoveryReaffirmInput{
		CompanyID:      input.CompanyID,
		RuntimeID:      input.RuntimeID,
		DaemonID:       input.DaemonID,
		Generation:     input.Generation,
		ManifestDigest: input.ManifestDigest,
		ObservedAt:     input.ObservedAt.Add(time.Second),
	})
	if err != nil || len(changes) != 1 {
		t.Fatalf("unchanged keepalive emitted a catalog event: %+v, %v", changes, err)
	}
	unchanged := input
	unchanged.Generation = uuid.New()
	unchanged.ObservedAt = input.ObservedAt.Add(2 * time.Second)
	if _, err := service.PublishDiscovery(ctx, fixture.principal, unchanged); err != nil || len(changes) != 1 {
		t.Fatalf("same catalog in a new generation emitted a hint: %d, %v", len(changes), err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE connector_discovery_generations SET observed_at=now()-interval '1 day' WHERE runtime_id=$1`,
		input.RuntimeID,
	)
	_, err = service.ReaffirmDiscovery(ctx, fixture.principal, ConnectorDiscoveryReaffirmInput{
		CompanyID:      input.CompanyID,
		RuntimeID:      input.RuntimeID,
		DaemonID:       input.DaemonID,
		Generation:     unchanged.Generation,
		ManifestDigest: unchanged.ManifestDigest,
		ObservedAt:     unchanged.ObservedAt.Add(time.Second),
	})
	if err != nil || len(changes) != 2 {
		t.Fatalf("stale discovery recovery hint missing: %d, %v", len(changes), err)
	}
}

func TestConnectorCatalogSourceFiltersPagesAndCounts(t *testing.T) {
	t.Parallel()
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	service, err := NewConnectorService(ConnectorServiceOptions{Bus: mustAgentTestBus(t), Store: store})
	if err != nil {
		t.Fatal(err)
	}
	items := []ConnectorObservationInput{
		{
			Source:   ConnectorSourceCodexApp,
			NativeID: "app",
			Name:     "A App",
			Category: connectorStringPointer("Productivity"),
			Status:   ConnectorStatusConnected,
			Action:   ConnectorActionNone,
		},
		{
			Source:   ConnectorSourceCodexMCP,
			NativeID: "b-ready",
			Name:     "B Ready",
			Category: connectorStringPointer("Developer tools"),
			Status:   ConnectorStatusConnected,
			Action:   ConnectorActionManageInProvider,
		},
		{
			Source:   ConnectorSourceCodexMCP,
			NativeID: "c-auth",
			Name:     "C Sign in",
			Category: connectorStringPointer("Developer tools"),
			Status:   ConnectorStatusNeedsAuth,
			Action:   ConnectorActionManageInProvider,
		},
		{
			Source:   ConnectorSourceCodexMCP,
			NativeID: "d-error",
			Name:     "D Error",
			Category: connectorStringPointer("Productivity"),
			Status:   ConnectorStatusError,
			Action:   ConnectorActionManageInProvider,
		},
		{
			Source:   ConnectorSourceCodexMCP,
			NativeID: "e-absent",
			Name:     "E Absent",
			Category: connectorStringPointer("Developer tools"),
			Status:   ConnectorStatusConnected,
			Action:   ConnectorActionManageInProvider,
		},
	}
	observedAt := time.Now().UTC()
	for generation, count := range []int{len(items), len(items) - 1} {
		canonical := make([]ConnectorObservationInput, count)
		for index, item := range items[:count] {
			canonical[index], err = normalizeConnectorObservation(ConnectorProviderCodex, item)
			if err != nil {
				t.Fatal(err)
			}
		}
		digest, err := connectorManifestDigest(canonical)
		if err != nil {
			t.Fatal(err)
		}
		_, err = service.PublishDiscovery(ctx, fixture.principal, ConnectorDiscoveryInput{
			CompanyID: fixture.companyID, RuntimeID: fixture.runtimeID,
			DaemonID: "daemon-" + fixture.companyID.String(), Generation: uuid.New(),
			ManifestDigest: digest, ObservedAt: observedAt.Add(time.Duration(generation) * time.Second),
			Items: items[:count],
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	input := ConnectorListInput{RuntimeID: &fixture.runtimeID, Source: ConnectorSourceCodexMCP, Limit: 1}
	for index, want := range items[1:] {
		page, err := service.List(ctx, fixture.principal, input)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Connectors) != 1 || page.Connectors[0].NativeID != want.NativeID || page.TotalCount != 4 ||
			page.ConnectedCount != 1 {
			t.Fatalf("MCP page %d = %+v, want %s, total 4, connected 1", index, page, want.NativeID)
		}
		wantStatus := want.Status
		if want.NativeID == "e-absent" {
			wantStatus = ConnectorStatusUnavailable
		}
		if page.Connectors[0].Source != ConnectorSourceCodexMCP || page.Connectors[0].Status != wantStatus {
			t.Fatalf("MCP item = %+v, want source codex_mcp, status %s", page.Connectors[0], wantStatus)
		}
		counts := make(map[string]int64)
		for _, category := range page.CategoryCounts {
			counts[category.Category] = category.Count
		}
		if len(counts) != 1 || counts["Other"] != 4 {
			t.Fatalf("MCP category counts = %v, want Other 4", counts)
		}
		if index == 3 {
			if page.NextCursor != nil {
				t.Fatalf("last MCP page next cursor = %v, want nil", page.NextCursor)
			}
			break
		}
		if page.NextCursor == nil {
			t.Fatal("MCP page has no next cursor")
		}
		input.Cursor, err = ParseConnectorCursor(*page.NextCursor)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, filter := range []ConnectorListInput{
		{Source: ConnectorSourceCodexMCP, Status: ConnectorStatusConnected},
		{Source: ConnectorSourceCodexMCP, Query: "Sign in"},
		{Source: ConnectorSourceCodexMCP, Category: "Other", Query: "Error"},
	} {
		page, err := service.List(ctx, fixture.principal, filter)
		if err != nil || len(page.Connectors) != 1 || page.TotalCount != 1 || page.ConnectedCount != 1 {
			t.Fatalf("List(%+v) = %+v, %v; want one row, total 1, connected 1", filter, page, err)
		}
	}
	page, err := service.List(ctx, fixture.principal, ConnectorListInput{})
	if err != nil || len(page.Connectors) != 5 || page.TotalCount != 5 || page.ConnectedCount != 2 {
		t.Fatalf("unfiltered List() = %+v, %v; want 5 rows, total 5, connected 2", page, err)
	}
	other := seedAgentCompany(t, ctx, pool, "owner")
	_, err = service.List(
		ctx,
		fixture.principal,
		ConnectorListInput{RuntimeID: &other.runtimeID, Source: ConnectorSourceCodexMCP},
	)
	if !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("List(other runtime, MCP) error = %v, want ErrRuntimeNotFound", err)
	}
}

func TestConnectorDiscoveryRechecksMembershipAfterCompanyWait(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	service, err := NewConnectorService(ConnectorServiceOptions{Bus: mustAgentTestBus(t), Store: store})
	if err != nil {
		t.Fatal(err)
	}

	item := ConnectorObservationInput{
		Source: ConnectorSourceCodexApp, NativeID: "github", Name: "GitHub",
		Status: ConnectorStatusAvailable, Action: ConnectorActionInstallAndAuth,
	}
	canonical, err := normalizeConnectorObservation(ConnectorProviderCodex, item)
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest, err := connectorManifestDigest([]ConnectorObservationInput{canonical})
	if err != nil {
		t.Fatal(err)
	}
	input := ConnectorDiscoveryInput{
		CompanyID: fixture.companyID, RuntimeID: fixture.runtimeID,
		DaemonID: "daemon-" + fixture.companyID.String(), Generation: uuid.New(),
		ManifestDigest: manifestDigest, ObservedAt: time.Now().UTC(),
		Items: []ConnectorObservationInput{item},
	}

	holder, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatalf("begin Connector discovery membership race: %v", err)
	}
	defer func() { _ = holder.Rollback(context.Background()) }()
	if _, err := holder.Exec(
		ctx,
		`SELECT id FROM companies WHERE id = $1 FOR UPDATE`,
		fixture.companyID,
	); err != nil {
		t.Fatalf("lock Company before Connector discovery membership race: %v", err)
	}
	if _, err := holder.Exec(
		ctx, `
		UPDATE company_members
		SET status = 'revoked', revoked_at = clock_timestamp(),
			revoked_by_user_id = user_id, updated_at = clock_timestamp(),
			version = version + 1
		WHERE company_id = $1 AND user_id = $2`,
		fixture.companyID,
		fixture.userID,
	); err != nil {
		t.Fatalf("revoke Connector discovery publisher during race: %v", err)
	}

	result := make(chan error, 1)
	go func() {
		_, publishErr := service.PublishDiscovery(ctx, fixture.principal, input)
		result <- publishErr
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM pg_stat_activity
				WHERE pid <> pg_backend_pid()
				  AND datname = current_database()
				  AND state = 'active'
				  AND wait_event_type = 'Lock'
				  AND query LIKE '%LockCompanyForMembershipMutation%'
			)`).Scan(&waiting); err != nil {
			t.Fatalf("observe blocked Connector discovery publication: %v", err)
		}
		if waiting {
			break
		}
		select {
		case publishErr := <-result:
			t.Fatalf("PublishDiscovery() returned before Company mutation committed: %v", publishErr)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("PublishDiscovery() did not reach the held Company lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatalf("commit Connector discovery membership race: %v", err)
	}
	select {
	case publishErr := <-result:
		if !errors.Is(publishErr, ErrRuntimeNotFound) {
			t.Fatalf(
				"PublishDiscovery() after membership revocation error = %v, want ErrRuntimeNotFound",
				publishErr,
			)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("PublishDiscovery() did not resume after membership revocation")
	}

	var generationCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM connector_discovery_generations
		WHERE runtime_id = $1`, fixture.runtimeID).Scan(&generationCount); err != nil {
		t.Fatalf("count Connector generations after rejected stale principal: %v", err)
	}
	if generationCount != 0 {
		t.Fatalf("Connector generations after rejected stale principal = %d, want 0", generationCount)
	}
}

func TestConnectorCatalogRejectsRuntimeWithIneligibleOwner(t *testing.T) {
	tests := []struct {
		name     string
		mutation string
	}{
		{
			name: "revoked membership",
			mutation: `UPDATE company_members
				SET status = 'revoked', revoked_at = clock_timestamp(),
				    revoked_by_user_id = user_id
				WHERE company_id = $1 AND user_id = $2`,
		},
		{
			name: "disabled account",
			mutation: `UPDATE users
				SET disabled_at = clock_timestamp()
				WHERE id = $2
				  AND EXISTS (
				      SELECT 1 FROM company_members
				      WHERE company_id = $1 AND user_id = $2
				  )`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pool, store := startAgentPostgres(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			fixture := seedAgentCompany(t, ctx, pool, "owner")
			service, err := NewConnectorService(
				ConnectorServiceOptions{Bus: mustAgentTestBus(t), Store: store},
			)
			if err != nil {
				t.Fatal(err)
			}

			runtimeOwnerID := uuid.Must(uuid.NewV7())
			mustAgentExec(
				t, ctx, pool,
				`INSERT INTO users (id, email, display_name) VALUES ($1, $2, 'Runtime Owner')`,
				runtimeOwnerID,
				runtimeOwnerID.String()+"@zenith-team.test",
			)
			mustAgentExec(t, ctx, pool, `
				INSERT INTO company_members (company_id, user_id, role)
				VALUES ($1, $2, 'member')`, fixture.companyID, runtimeOwnerID)
			mustAgentExec(
				t, ctx, pool,
				`UPDATE runtimes SET owner_user_id = $2 WHERE id = $1`,
				fixture.runtimeID,
				runtimeOwnerID,
			)
			mustAgentExec(t, ctx, pool, test.mutation, fixture.companyID, runtimeOwnerID)

			_, err = service.List(ctx, fixture.principal, ConnectorListInput{})
			if !errors.Is(err, ErrConnectorProviderUnavailable) {
				t.Fatalf("List() error = %v, want ErrConnectorProviderUnavailable", err)
			}
		})
	}
}

// The Company owner's administrative view of another member's registration
// is not a discovery context: the catalog selects the runtime only once its
// registrant has shared it with the Company (ADR 0007).
func TestConnectorCatalogSelectsAnotherMembersRuntimeOnlyWhenShared(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	service, err := NewConnectorService(ConnectorServiceOptions{Bus: mustAgentTestBus(t), Store: store})
	if err != nil {
		t.Fatal(err)
	}

	registrantID := uuid.Must(uuid.NewV7())
	mustAgentExec(
		t, ctx, pool,
		`INSERT INTO users (id, email, display_name) VALUES ($1, $2, 'Registrant')`,
		registrantID,
		registrantID.String()+"@zenith-team.test",
	)
	mustAgentExec(t, ctx, pool, `
		INSERT INTO company_members (company_id, user_id, role)
		VALUES ($1, $2, 'member')`, fixture.companyID, registrantID)
	mustAgentExec(
		t, ctx, pool,
		`UPDATE runtimes SET owner_user_id = $2 WHERE id = $1`,
		fixture.runtimeID,
		registrantID,
	)

	// Left to the default, another member's machine is simply not a
	// candidate; named explicitly it is not found, exactly like on the
	// runtime routes (ADR 0014).
	explicit := ConnectorListInput{RuntimeID: &fixture.runtimeID}
	for name, test := range map[string]struct {
		input ConnectorListInput
		want  error
	}{
		"default":  {ConnectorListInput{}, ErrConnectorProviderUnavailable},
		"explicit": {explicit, ErrRuntimeNotFound},
	} {
		if _, err := service.List(ctx, fixture.principal, test.input); !errors.Is(err, test.want) {
			t.Fatalf("List(%s) on another member's runtime error = %v, want %v", name, err, test.want)
		}
	}

	// The machine comes back to the caller: their own registration is the
	// only discovery context there is (ADR 0014).
	mustAgentExec(
		t, ctx, pool,
		`UPDATE runtimes SET owner_user_id = $2 WHERE id = $1`,
		fixture.runtimeID,
		fixture.userID,
	)
	result, err := service.List(ctx, fixture.principal, explicit)
	if err != nil {
		t.Fatalf("List() on the caller's own runtime error = %v", err)
	}
	if result.Runtime.ID != fixture.runtimeID {
		t.Fatalf("List() selected runtime %s, want the caller's %s", result.Runtime.ID, fixture.runtimeID)
	}
}

func TestConnectorCatalogRefreshIgnoresObservationFromIneligibleRuntimeOwner(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	service, err := NewConnectorService(ConnectorServiceOptions{Bus: mustAgentTestBus(t), Store: store})
	if err != nil {
		t.Fatal(err)
	}

	tools := []provideragent.ConnectorTool{{Name: "search", Enabled: true, ReadOnly: true}}
	contractDigest, err := provideragent.ConnectorToolContractDigest(tools)
	if err != nil {
		t.Fatal(err)
	}
	item := ConnectorObservationInput{
		Source: ConnectorSourceCodexApp, NativeID: "github", Name: "Eligible projection",
		Status: ConnectorStatusConnected, Action: ConnectorActionNone, CanExecute: true,
		ContractDigest: contractDigest, Tools: tools,
	}
	canonical, err := normalizeConnectorObservation(ConnectorProviderCodex, item)
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest, err := connectorManifestDigest([]ConnectorObservationInput{canonical})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := service.PublishDiscovery(ctx, fixture.principal, ConnectorDiscoveryInput{
		CompanyID: fixture.companyID, RuntimeID: fixture.runtimeID,
		DaemonID: "daemon-" + fixture.companyID.String(), Generation: uuid.New(),
		ManifestDigest: manifestDigest, ObservedAt: time.Now().UTC(),
		Items: []ConnectorObservationInput{item},
	}); err != nil || !result.Applied {
		t.Fatalf("PublishDiscovery() = %+v, %v", result, err)
	}

	var connectorID uuid.UUID
	if err := pool.QueryRow(
		ctx, `
		SELECT id FROM connector_catalog_items
		WHERE company_id = $1 AND native_id = 'github'`, fixture.companyID,
	).Scan(&connectorID); err != nil {
		t.Fatalf("read Connector catalog id: %v", err)
	}
	runtimeOwnerID := uuid.Must(uuid.NewV7())
	ineligibleRuntimeID := uuid.Must(uuid.NewV7())
	ineligibleAgentID := uuid.Must(uuid.NewV7())
	ineligibleGeneration := uuid.Must(uuid.NewV7())
	mustAgentExec(
		t, ctx, pool,
		`INSERT INTO users (id, email, display_name) VALUES ($1, $2, 'Revoked Runtime Owner')`,
		runtimeOwnerID,
		runtimeOwnerID.String()+"@zenith-team.test",
	)
	mustAgentExec(t, ctx, pool, `
		INSERT INTO company_members (company_id, user_id, role)
		VALUES ($1, $2, 'member')`, fixture.companyID, runtimeOwnerID)
	mustAgentExec(t, ctx, pool, `
		INSERT INTO runtimes (
			id, company_id, daemon_id, name, provider, status, last_seen_at, owner_user_id
		) VALUES (
			$1, $2, $3, 'Revoked Owner Runtime', 'codex', 'online',
			clock_timestamp() + interval '1 second', $4
		)`, ineligibleRuntimeID, fixture.companyID,
		"ineligible-daemon-"+fixture.companyID.String(), runtimeOwnerID)
	mustAgentExec(t, ctx, pool, `
		INSERT INTO agents (id, company_id, name, title, provider, reports_to_agent_id)
		VALUES ($1, $2, 'Revoked Runtime Agent', 'Agent', 'codex', $3)`,
		ineligibleAgentID, fixture.companyID, fixture.rootAgentID)
	mustAgentExec(t, ctx, pool, `
		INSERT INTO agent_runtime_bindings (company_id, agent_id, user_id, runtime_id, provider)
		VALUES ($1, $2, $3, $4, 'codex')`,
		fixture.companyID, ineligibleAgentID, runtimeOwnerID, ineligibleRuntimeID)
	mustAgentExec(t, ctx, pool, `
		INSERT INTO connector_discovery_generations (
			runtime_id, company_id, provider, generation, manifest_digest, observed_at
		) VALUES ($1, $2, 'codex', $3, $4, clock_timestamp() + interval '1 second')`,
		ineligibleRuntimeID, fixture.companyID, ineligibleGeneration, manifestDigest)
	mustAgentExec(t, ctx, pool, `
		INSERT INTO connector_runtime_observations (
			runtime_id, connector_id, company_id, provider, generation, name,
			status, action, can_execute, contract_digest, tool_namespace, observed_at
		) VALUES (
			$1, $2, $3, 'codex', $4, 'Ineligible projection',
			'connected', 'none', true, $5,
			'[{"name":"search","enabled":true,"readOnly":true}]'::jsonb,
			clock_timestamp() + interval '1 second'
		)`, ineligibleRuntimeID, connectorID, fixture.companyID,
		ineligibleGeneration, contractDigest)
	mustAgentExec(t, ctx, pool, `
		UPDATE company_members
		SET status = 'revoked', revoked_at = clock_timestamp(), revoked_by_user_id = user_id
		WHERE company_id = $1 AND user_id = $2`, fixture.companyID, runtimeOwnerID)

	if _, err := store.Queries().RefreshCompanyConnectorCatalogFromObservations(
		ctx,
		generated.RefreshCompanyConnectorCatalogFromObservationsParams{
			CompanyID: fixture.companyID,
			Provider:  string(ConnectorProviderCodex),
		},
	); err != nil {
		t.Fatalf("RefreshCompanyConnectorCatalogFromObservations() error = %v", err)
	}
	var name string
	if err := pool.QueryRow(
		ctx,
		`SELECT name FROM connector_catalog_items WHERE id = $1`, connectorID,
	).Scan(&name); err != nil {
		t.Fatalf("read refreshed Connector catalog: %v", err)
	}
	if name != item.Name {
		t.Fatalf("refreshed Connector name = %q, want %q", name, item.Name)
	}
}

func TestConnectorPolicyChangesPublishCommittedReadiness(t *testing.T) {
	t.Parallel()
	pool, store := startAgentPostgres(t)
	ctx := t.Context()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	bus := mustAgentTestBus(t)
	service, err := NewConnectorService(ConnectorServiceOptions{Bus: bus, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	var changes []events.Event
	wantReview := false
	if err := bus.Subscribe("connector.policy_changed", func(ctx context.Context, event events.Event) error {
		changes = append(changes, event)
		if event.CompanyID != fixture.companyID || event.EntityType != "connector" {
			t.Errorf("policy event = %+v, want the fixture Company Connector", event)
		}
		raw, err := json.Marshal(event.Payload)
		if err != nil || string(raw) != "{}" {
			t.Errorf("policy hint = %s, %v, want an opaque payload", raw, err)
		}
		// A separate pool read must see the policy before clients refetch it.
		var enabled, review bool
		err = pool.QueryRow(ctx, `
			SELECT policies.desired_enabled, policies.review_required
			FROM connector_policies AS policies
			JOIN connector_catalog_items AS catalog ON catalog.id = policies.connector_id
			WHERE catalog.company_id = $1 AND catalog.native_id = $2`,
			fixture.companyID, testGmailID).Scan(&enabled, &review)
		if err != nil || !enabled || review != wantReview {
			t.Errorf("committed policy = enabled %v, review %v, %v; want true, %v",
				enabled, review, err, wantReview)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	daemonID := "daemon-" + fixture.companyID.String()
	item := executableTestConnector(testGmailID, "Gmail")
	publishTestConnectors(t, ctx, service, fixture.principal, fixture.runtimeID, daemonID,
		[]ConnectorObservationInput{item})
	if len(changes) != 1 {
		t.Fatalf("initial policy events = %d, want 1", len(changes))
	}
	publishTestConnectors(t, ctx, service, fixture.principal, fixture.runtimeID, daemonID,
		[]ConnectorObservationInput{item})
	if len(changes) != 1 {
		t.Fatalf("unchanged discovery policy events = %d, want 1", len(changes))
	}
	wantReview = true
	item = executableTestConnectorWithTools(testGmailID, "Gmail", []provideragent.ConnectorTool{
		{Name: "search", Enabled: true, ReadOnly: false},
	})
	publishTestConnectors(t, ctx, service, fixture.principal, fixture.runtimeID, daemonID,
		[]ConnectorObservationInput{item})
	if len(changes) != 2 {
		t.Fatalf("contract drift policy events = %d, want 2", len(changes))
	}
	readiness := readTestReadiness(t, ctx, service, fixture.principal, fixture.rootAgentID, "gmail")
	if readiness.Apps[0].State != ConnectorAppStateReviewRequired || readiness.Apps[0].Connector == nil {
		t.Fatalf("readiness before review = %+v, want review_required", readiness.Apps[0])
	}
	connectorID := readiness.Apps[0].Connector.ID
	member := fixture.principal
	member.Role = auth.CompanyRoleMember
	if err := service.Connect(ctx, member, connectorID); !errors.Is(err, ErrForbidden) || len(changes) != 2 {
		t.Fatalf("forbidden review = %v, events %d; want ErrForbidden and 2", err, len(changes))
	}
	mustAgentExec(t, ctx, pool,
		`UPDATE runtimes SET last_seen_at = clock_timestamp() WHERE id = $1`, fixture.runtimeID)
	wantReview = false
	if err := service.Connect(ctx, fixture.principal, connectorID); err != nil {
		t.Fatal(err)
	}
	if len(changes) != 3 {
		t.Fatalf("accepted review policy events = %d, want 3", len(changes))
	}
	readiness = readTestReadiness(t, ctx, service, fixture.principal, fixture.rootAgentID, "gmail")
	if readiness.Apps[0].State != ConnectorAppStateReady {
		t.Fatalf("readiness after review = %+v, want ready", readiness.Apps[0])
	}
	if err := service.Connect(
		ctx,
		fixture.principal,
		connectorID,
	); !errors.Is(
		err,
		ErrConnectorActionUnavailable,
	) ||
		len(changes) != 3 {
		t.Fatalf("repeated review = %v, events %d; want unavailable and 3", err, len(changes))
	}
}

func TestConnectorDiscoveryKeepsEnabledIntentPendingContractReview(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	service, err := NewConnectorService(ConnectorServiceOptions{Bus: mustAgentTestBus(t), Store: store})
	if err != nil {
		t.Fatal(err)
	}

	observedAt := time.Now().UTC()
	tools := []provideragent.ConnectorTool{{Name: "search", Enabled: true, ReadOnly: true}}
	contractDigest, err := provideragent.ConnectorToolContractDigest(tools)
	if err != nil {
		t.Fatal(err)
	}
	item := ConnectorObservationInput{
		Source: ConnectorSourceCodexApp, NativeID: "github", Name: "GitHub",
		Status: ConnectorStatusConnected, Action: ConnectorActionNone, CanExecute: true,
		ContractDigest: contractDigest, Tools: tools,
	}
	canonical, err := normalizeConnectorObservation(ConnectorProviderCodex, item)
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest, err := connectorManifestDigest([]ConnectorObservationInput{canonical})
	if err != nil {
		t.Fatal(err)
	}
	input := ConnectorDiscoveryInput{
		CompanyID: fixture.companyID, RuntimeID: fixture.runtimeID,
		DaemonID:       "daemon-" + fixture.companyID.String(),
		Generation:     uuid.MustParse("00000000-0000-4000-8000-000000000101"),
		ManifestDigest: manifestDigest, ObservedAt: observedAt,
		Items: []ConnectorObservationInput{item},
	}
	if result, err := service.PublishDiscovery(ctx, fixture.principal, input); err != nil || !result.Applied {
		t.Fatalf("PublishDiscovery(initial) = %+v, %v", result, err)
	}

	var connectorID uuid.UUID
	var desiredEnabled, reviewRequired bool
	var policyVersion int64
	var policyUpdatedAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT policies.connector_id, policies.desired_enabled, policies.review_required,
			policies.version, policies.updated_at
		FROM connector_policies AS policies
		JOIN connector_catalog_items AS catalog ON catalog.id = policies.connector_id
		WHERE catalog.company_id = $1
		  AND catalog.native_id = 'github'
		`, fixture.companyID).Scan(
		&connectorID,
		&desiredEnabled,
		&reviewRequired,
		&policyVersion,
		&policyUpdatedAt,
	); err != nil {
		t.Fatalf("read initially enabled Connector policy: %v", err)
	}
	if !desiredEnabled || reviewRequired {
		t.Fatalf(
			"initial Connector policy = enabled %v reviewRequired %v; want true, false",
			desiredEnabled,
			reviewRequired,
		)
	}
	unchanged := input
	unchanged.Generation = uuid.MustParse("00000000-0000-4000-8000-000000000102")
	unchanged.ObservedAt = observedAt.Add(time.Millisecond)
	if result, err := service.PublishDiscovery(ctx, fixture.principal, unchanged); err != nil || !result.Applied {
		t.Fatalf("PublishDiscovery(unchanged contract) = %+v, %v", result, err)
	}
	var unchangedVersion int64
	var unchangedUpdatedAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT version, updated_at
		FROM connector_policies
		WHERE connector_id = $1`, connectorID).Scan(&unchangedVersion, &unchangedUpdatedAt); err != nil {
		t.Fatalf("read Connector policy after unchanged discovery: %v", err)
	}
	if unchangedVersion != policyVersion || !unchangedUpdatedAt.Equal(policyUpdatedAt) {
		t.Fatalf(
			"unchanged discovery moved policy revision/timestamp from %d/%s to %d/%s",
			policyVersion,
			policyUpdatedAt,
			unchangedVersion,
			unchangedUpdatedAt,
		)
	}

	changedTools := []provideragent.ConnectorTool{{Name: "search", Enabled: true, ReadOnly: false}}
	changedDigest, err := provideragent.ConnectorToolContractDigest(changedTools)
	if err != nil {
		t.Fatal(err)
	}
	changedItem := item
	changedItem.ContractDigest = changedDigest
	changedItem.Tools = changedTools
	canonical, err = normalizeConnectorObservation(ConnectorProviderCodex, changedItem)
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest, err = connectorManifestDigest([]ConnectorObservationInput{canonical})
	if err != nil {
		t.Fatal(err)
	}
	input.Generation = uuid.MustParse("00000000-0000-4000-8000-000000000103")
	input.ManifestDigest = manifestDigest
	input.ObservedAt = observedAt.Add(time.Second)
	input.Items = []ConnectorObservationInput{changedItem}
	if result, err := service.PublishDiscovery(ctx, fixture.principal, input); err != nil || !result.Applied {
		t.Fatalf("PublishDiscovery(changed contract) = %+v, %v", result, err)
	}

	if err := pool.QueryRow(ctx, `
		SELECT desired_enabled, review_required
		FROM connector_policies
		WHERE connector_id = $1`, connectorID).Scan(&desiredEnabled, &reviewRequired); err != nil {
		t.Fatalf("read reconciled Connector policy: %v", err)
	}
	if !desiredEnabled || !reviewRequired {
		t.Fatalf(
			"reconciled Connector policy = enabled %v reviewRequired %v; want true, true",
			desiredEnabled,
			reviewRequired,
		)
	}
	page, err := service.List(ctx, fixture.principal, ConnectorListInput{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(page.Connectors) != 1 || page.Connectors[0].ID != connectorID ||
		!page.Connectors[0].DesiredEnabled || !page.Connectors[0].ReviewRequired ||
		page.Connectors[0].EffectiveEnabled {
		t.Fatalf("List() after contract drift = %+v", page.Connectors)
	}

	mustAgentExec(t, ctx, pool, `
		WITH inserted AS (
			INSERT INTO connector_catalog_items (
				company_id, provider, source, native_id, name, status, action,
				can_execute, observed_at
			)
			SELECT $1, 'codex', 'codex_app', 'cap-' || value::text,
				'Cap Connector ' || value::text, 'unavailable', 'none', false,
				clock_timestamp()
			FROM generate_series(1, 127) AS series(value)
			RETURNING id, company_id
		)
		INSERT INTO connector_policies (
			connector_id, company_id, desired_enabled,
			reviewed_contract_digest, review_required
		)
		SELECT id, company_id, true, NULL, false
		FROM inserted`, fixture.companyID)
	mustAgentExec(t, ctx, pool, `
		UPDATE runtimes SET last_seen_at = clock_timestamp() WHERE id = $1`, fixture.runtimeID)
	if err := service.Connect(ctx, fixture.principal, connectorID); err != nil {
		t.Fatalf("Connect() re-review at desired Connector cap error = %v", err)
	}
	var desiredCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM connector_policies
		WHERE company_id = $1 AND desired_enabled`, fixture.companyID).Scan(&desiredCount); err != nil {
		t.Fatalf("count desired Connectors after re-review: %v", err)
	}
	if desiredCount != maxDesiredExecutableConnectors {
		t.Fatalf(
			"desired Connector count after re-review = %d, want %d",
			desiredCount,
			maxDesiredExecutableConnectors,
		)
	}
	if err := pool.QueryRow(ctx, `
		SELECT desired_enabled, review_required
		FROM connector_policies
		WHERE connector_id = $1`, connectorID).Scan(&desiredEnabled, &reviewRequired); err != nil {
		t.Fatalf("read Connector policy after re-review: %v", err)
	}
	if !desiredEnabled || reviewRequired {
		t.Fatalf(
			"Connector policy after re-review = enabled %v reviewRequired %v; want true, false",
			desiredEnabled,
			reviewRequired,
		)
	}

	var reviewedVersion int64
	var reviewedDigest string
	if err := pool.QueryRow(ctx, `
		SELECT version, reviewed_contract_digest
		FROM connector_policies
		WHERE connector_id = $1`, connectorID).Scan(&reviewedVersion, &reviewedDigest); err != nil {
		t.Fatalf("read reviewed Connector policy before disappearance: %v", err)
	}
	emptyDigest, err := connectorManifestDigest([]ConnectorObservationInput{})
	if err != nil {
		t.Fatal(err)
	}
	input.Generation = uuid.MustParse("00000000-0000-4000-8000-000000000104")
	input.ManifestDigest = emptyDigest
	input.ObservedAt = observedAt.Add(2 * time.Second)
	input.Items = []ConnectorObservationInput{}
	if result, err := service.PublishDiscovery(ctx, fixture.principal, input); err != nil || !result.Applied {
		t.Fatalf("PublishDiscovery(Connector removed) = %+v, %v", result, err)
	}
	var removedVersion int64
	var removedDigest string
	if err := pool.QueryRow(ctx, `
		SELECT desired_enabled, reviewed_contract_digest, review_required, version
		FROM connector_policies
		WHERE connector_id = $1`, connectorID).Scan(
		&desiredEnabled,
		&removedDigest,
		&reviewRequired,
		&removedVersion,
	); err != nil {
		t.Fatalf("read Connector policy after disappearance: %v", err)
	}
	if !desiredEnabled || !reviewRequired || removedDigest != reviewedDigest ||
		removedVersion != reviewedVersion+1 {
		t.Fatalf(
			"removed Connector policy = enabled %v digest %q reviewRequired %v version %d; "+
				"want true, %q, true, %d",
			desiredEnabled,
			removedDigest,
			reviewRequired,
			removedVersion,
			reviewedDigest,
			reviewedVersion+1,
		)
	}
	page, err = service.List(ctx, fixture.principal, ConnectorListInput{Query: "GitHub", Limit: 10})
	if err != nil {
		t.Fatalf("List() after Connector disappearance error = %v", err)
	}
	if len(page.Connectors) != 1 || page.Connectors[0].Status != ConnectorStatusUnavailable ||
		!page.Connectors[0].DesiredEnabled || !page.Connectors[0].ReviewRequired ||
		page.Connectors[0].EffectiveEnabled {
		t.Fatalf("List() after Connector disappearance = %+v", page.Connectors)
	}
}

func TestConnectorDiscoveryRanksOnlyExecutableConnectedItemsForDefaultEnablement(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	service, err := NewConnectorService(ConnectorServiceOptions{Bus: mustAgentTestBus(t), Store: store})
	if err != nil {
		t.Fatal(err)
	}

	tools := []provideragent.ConnectorTool{{Name: "search", Enabled: true, ReadOnly: true}}
	contractDigest, err := provideragent.ConnectorToolContractDigest(tools)
	if err != nil {
		t.Fatal(err)
	}
	items := []ConnectorObservationInput{{
		Source: ConnectorSourceCodexApp, NativeID: "executable", Name: "Z Executable",
		Status: ConnectorStatusConnected, Action: ConnectorActionNone, CanExecute: true,
		ContractDigest: contractDigest, Tools: tools,
	}}
	for index := range maxDesiredExecutableConnectors {
		items = append(items, ConnectorObservationInput{
			Source: ConnectorSourceCodexApp, NativeID: fmt.Sprintf("unavailable-%03d", index),
			Name: fmt.Sprintf("A Unavailable %03d", index), Status: ConnectorStatusUnavailable,
			Action: ConnectorActionNone,
		})
	}
	normalizedItems := make([]ConnectorObservationInput, len(items))
	for index, item := range items {
		normalizedItems[index], err = normalizeConnectorObservation(ConnectorProviderCodex, item)
		if err != nil {
			t.Fatalf("normalize Connector observation %d: %v", index, err)
		}
	}
	manifestDigest, err := connectorManifestDigest(normalizedItems)
	if err != nil {
		t.Fatal(err)
	}
	observedAt := time.Now().UTC()
	result, err := service.PublishDiscovery(ctx, fixture.principal, ConnectorDiscoveryInput{
		CompanyID: fixture.companyID, RuntimeID: fixture.runtimeID,
		DaemonID:       "daemon-" + fixture.companyID.String(),
		Generation:     uuid.MustParse("00000000-0000-4000-8000-000000000201"),
		ManifestDigest: manifestDigest, ObservedAt: observedAt, Items: items,
	})
	if err != nil || !result.Applied {
		t.Fatalf("PublishDiscovery() = %+v, %v", result, err)
	}
	mustAgentExec(t, ctx, pool, `
		UPDATE runtimes SET last_seen_at = clock_timestamp() WHERE id = $1`, fixture.runtimeID)

	page, err := service.List(ctx, fixture.principal, ConnectorListInput{
		Query: "Z Executable", Limit: 10,
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(page.Connectors) != 1 || !page.Connectors[0].DesiredEnabled ||
		!page.Connectors[0].EffectiveEnabled {
		t.Fatalf("executable Connector after unavailable catalog rows = %+v", page.Connectors)
	}

	mustAgentExec(t, ctx, pool, `
		UPDATE connector_policies
		SET desired_enabled = true
		WHERE connector_id IN (
			SELECT catalog.id
			FROM connector_catalog_items AS catalog
			WHERE catalog.company_id = $1
			  AND catalog.native_id LIKE 'unavailable-%'
			ORDER BY catalog.native_id
			LIMIT 127
		)`, fixture.companyID)
	replacement := ConnectorObservationInput{
		Source: ConnectorSourceCodexApp, NativeID: "replacement", Name: "Replacement",
		Status: ConnectorStatusConnected, Action: ConnectorActionNone, CanExecute: true,
		ContractDigest: contractDigest, Tools: tools,
	}
	normalizedReplacement, err := normalizeConnectorObservation(ConnectorProviderCodex, replacement)
	if err != nil {
		t.Fatal(err)
	}
	replacementDigest, err := connectorManifestDigest([]ConnectorObservationInput{normalizedReplacement})
	if err != nil {
		t.Fatal(err)
	}
	result, err = service.PublishDiscovery(ctx, fixture.principal, ConnectorDiscoveryInput{
		CompanyID: fixture.companyID, RuntimeID: fixture.runtimeID,
		DaemonID:       "daemon-" + fixture.companyID.String(),
		Generation:     uuid.MustParse("00000000-0000-4000-8000-000000000202"),
		ManifestDigest: replacementDigest, ObservedAt: observedAt.Add(time.Second),
		Items: []ConnectorObservationInput{replacement},
	})
	if err != nil || !result.Applied {
		t.Fatalf("PublishDiscovery(replacement at desired cap) = %+v, %v", result, err)
	}
	var replacementDesired bool
	var desiredCount int
	if err := pool.QueryRow(
		ctx, `
		SELECT policies.desired_enabled,
			(SELECT count(*) FROM connector_policies WHERE company_id = $1 AND desired_enabled)
		FROM connector_policies AS policies
		JOIN connector_catalog_items AS catalog ON catalog.id = policies.connector_id
		WHERE catalog.company_id = $1 AND catalog.native_id = 'replacement'`,
		fixture.companyID,
	).Scan(&replacementDesired, &desiredCount); err != nil {
		t.Fatalf("read replacement Connector policy: %v", err)
	}
	if replacementDesired || desiredCount != maxDesiredExecutableConnectors {
		t.Fatalf(
			"replacement desired = %v, total desired = %d; want false, %d",
			replacementDesired,
			desiredCount,
			maxDesiredExecutableConnectors,
		)
	}
}

func TestConnectorReviewUsesTargetProviderRuntimeAndProviderScopedCap(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	service, err := NewConnectorService(ConnectorServiceOptions{Bus: mustAgentTestBus(t), Store: store})
	if err != nil {
		t.Fatal(err)
	}

	claudeRuntimeID := uuid.Must(uuid.NewV7())
	claudeAgentID := uuid.Must(uuid.NewV7())
	connectorID := uuid.Must(uuid.NewV7())
	generation := uuid.Must(uuid.NewV7())
	currentDigest := strings.Repeat("a", 64)
	reviewedDigest := strings.Repeat("b", 64)
	manifestDigest := strings.Repeat("c", 64)
	mustAgentExec(t, ctx, pool, `
		UPDATE runtimes
		SET last_seen_at = clock_timestamp() + interval '1 second'
		WHERE id = $1`, fixture.runtimeID)
	mustAgentExec(t, ctx, pool, `
		INSERT INTO runtimes (
			id, company_id, daemon_id, name, provider, status, last_seen_at, owner_user_id
		) VALUES ($1, $2, $3, 'Claude Runtime', 'claude', 'online', clock_timestamp(), $4)`,
		claudeRuntimeID, fixture.companyID, "claude-daemon-"+fixture.companyID.String(), fixture.userID)
	mustAgentExec(t, ctx, pool, `
		INSERT INTO agents (id, company_id, name, title, provider, reports_to_agent_id)
		VALUES ($1, $2, 'Claude Agent', 'Claude Agent', 'claude', $3)`,
		claudeAgentID, fixture.companyID, fixture.rootAgentID)
	mustAgentExec(t, ctx, pool, `
		INSERT INTO agent_runtime_bindings (company_id, agent_id, user_id, runtime_id, provider)
		VALUES ($1, $2, $3, $4, 'claude')`,
		fixture.companyID, claudeAgentID, fixture.userID, claudeRuntimeID)
	mustAgentExec(t, ctx, pool, `
		INSERT INTO connector_catalog_items (
			id, company_id, provider, source, native_id, name, status, action,
			can_execute, contract_digest, tool_namespace, observed_at
		) VALUES (
			$1, $2, 'claude', 'claude_mcp', 'claude-search', 'Claude Search',
			'connected', 'none', true, $3,
			'[{"name":"search","enabled":true,"readOnly":true}]'::jsonb,
			clock_timestamp()
		)`, connectorID, fixture.companyID, currentDigest)
	mustAgentExec(t, ctx, pool, `
		INSERT INTO connector_policies (
			connector_id, company_id, desired_enabled,
			reviewed_contract_digest, review_required
		) VALUES ($1, $2, true, $3, true)`, connectorID, fixture.companyID, reviewedDigest)
	mustAgentExec(t, ctx, pool, `
		INSERT INTO connector_discovery_generations (
			runtime_id, company_id, provider, generation, manifest_digest, observed_at
		) VALUES ($1, $2, 'claude', $3, $4, clock_timestamp())`,
		claudeRuntimeID, fixture.companyID, generation, manifestDigest)
	mustAgentExec(t, ctx, pool, `
		INSERT INTO connector_runtime_observations (
			runtime_id, connector_id, company_id, provider, generation, name,
			status, action, can_execute, contract_digest, tool_namespace, observed_at
		) VALUES (
			$1, $2, $3, 'claude', $4, 'Claude Search', 'connected', 'none', true, $5,
			'[{"name":"search","enabled":true,"readOnly":true}]'::jsonb,
			clock_timestamp()
		)`, claudeRuntimeID, connectorID, fixture.companyID, generation, currentDigest)
	mustAgentExec(t, ctx, pool, `
		WITH inserted AS (
			INSERT INTO connector_catalog_items (
				company_id, provider, source, native_id, name, status, action,
				can_execute, contract_digest, tool_namespace, observed_at
			)
			SELECT $1, 'codex', 'codex_app', 'codex-cap-' || value::text,
				'Codex Cap ' || value::text, 'connected', 'none', true, $2,
				'[{"name":"search","enabled":true,"readOnly":true}]'::jsonb,
				clock_timestamp()
			FROM generate_series(1, 128) AS series(value)
			RETURNING id, company_id
		)
		INSERT INTO connector_policies (
			connector_id, company_id, desired_enabled,
			reviewed_contract_digest, review_required
		)
		SELECT id, company_id, true, $2, false
		FROM inserted`, fixture.companyID, currentDigest)

	if err := service.Connect(ctx, fixture.principal, connectorID); err != nil {
		t.Fatalf("Connect() with fresher other-provider Runtime and full other-provider cap = %v", err)
	}
	var desiredEnabled, reviewRequired bool
	var gotReviewedDigest *string
	if err := pool.QueryRow(ctx, `
		SELECT desired_enabled, reviewed_contract_digest, review_required
		FROM connector_policies
		WHERE connector_id = $1`, connectorID).
		Scan(&desiredEnabled, &gotReviewedDigest, &reviewRequired); err != nil {
		t.Fatalf("read reviewed Claude Connector policy: %v", err)
	}
	if !desiredEnabled || gotReviewedDigest == nil || *gotReviewedDigest != currentDigest || reviewRequired {
		t.Fatalf(
			"reviewed Claude Connector = enabled %v digest %v reviewRequired %v",
			desiredEnabled,
			gotReviewedDigest,
			reviewRequired,
		)
	}
}

func TestConnectorConnectRechecksOwnerAfterCompanyWait(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	service, err := NewConnectorService(ConnectorServiceOptions{Bus: mustAgentTestBus(t), Store: store})
	if err != nil {
		t.Fatal(err)
	}

	connectorID := uuid.Must(uuid.NewV7())
	generation := uuid.Must(uuid.NewV7())
	currentDigest := strings.Repeat("a", 64)
	reviewedDigest := strings.Repeat("b", 64)
	manifestDigest := strings.Repeat("c", 64)
	mustAgentExec(t, ctx, pool, `
		INSERT INTO connector_catalog_items (
			id, company_id, provider, source, native_id, name, status, action,
			can_execute, contract_digest, tool_namespace, observed_at
		) VALUES (
			$1, $2, 'codex', 'codex_app', 'github', 'GitHub', 'connected', 'none',
			true, $3, '[{"name":"search","enabled":true,"readOnly":true}]'::jsonb,
			clock_timestamp()
		)`, connectorID, fixture.companyID, currentDigest)
	mustAgentExec(t, ctx, pool, `
		INSERT INTO connector_policies (
			connector_id, company_id, desired_enabled,
			reviewed_contract_digest, review_required
		) VALUES ($1, $2, false, $3, true)`,
		connectorID, fixture.companyID, reviewedDigest)
	mustAgentExec(t, ctx, pool, `
		INSERT INTO connector_discovery_generations (
			runtime_id, company_id, provider, generation, manifest_digest, observed_at
		) VALUES ($1, $2, 'codex', $3, $4, clock_timestamp())`,
		fixture.runtimeID, fixture.companyID, generation, manifestDigest)
	mustAgentExec(t, ctx, pool, `
		INSERT INTO connector_runtime_observations (
			runtime_id, connector_id, company_id, provider, generation, name,
			status, action, can_execute, contract_digest, tool_namespace, observed_at
		) VALUES (
			$1, $2, $3, 'codex', $4, 'GitHub', 'connected', 'none', true, $5,
			'[{"name":"search","enabled":true,"readOnly":true}]'::jsonb,
			clock_timestamp()
		)`, fixture.runtimeID, connectorID, fixture.companyID, generation, currentDigest)

	holder, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatalf("begin Connector membership race: %v", err)
	}
	defer func() { _ = holder.Rollback(context.Background()) }()
	if _, err := holder.Exec(
		ctx,
		`SELECT id FROM companies WHERE id = $1 FOR UPDATE`,
		fixture.companyID,
	); err != nil {
		t.Fatalf("lock Company before Connector membership race: %v", err)
	}
	if _, err := holder.Exec(
		ctx, `
		UPDATE company_members
		SET status = 'revoked', revoked_at = clock_timestamp(),
			revoked_by_user_id = user_id, updated_at = clock_timestamp(),
			version = version + 1
		WHERE company_id = $1 AND user_id = $2`,
		fixture.companyID,
		fixture.userID,
	); err != nil {
		t.Fatalf("revoke Connector owner during race: %v", err)
	}

	result := make(chan error, 1)
	go func() {
		result <- service.Connect(ctx, fixture.principal, connectorID)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM pg_stat_activity
				WHERE pid <> pg_backend_pid()
				  AND datname = current_database()
				  AND state = 'active'
				  AND wait_event_type = 'Lock'
				  AND (
					  query LIKE '%LockCompanyForMembershipMutation%'
					  OR query LIKE '%GetCompanyConnectorForOwner%'
				  )
			)`).Scan(&waiting); err != nil {
			t.Fatalf("observe blocked Connector mutation: %v", err)
		}
		if waiting {
			break
		}
		select {
		case connectErr := <-result:
			t.Fatalf("Connect() returned before Company mutation committed: %v", connectErr)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("Connect() did not reach the held Company lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatalf("commit Connector membership race: %v", err)
	}
	select {
	case connectErr := <-result:
		if !errors.Is(connectErr, ErrForbidden) {
			t.Fatalf("Connect() after membership revocation error = %v, want ErrForbidden", connectErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Connect() did not resume after membership revocation")
	}

	var desiredEnabled, reviewRequired bool
	if err := pool.QueryRow(ctx, `
		SELECT desired_enabled, review_required
		FROM connector_policies
		WHERE connector_id = $1`, connectorID).Scan(&desiredEnabled, &reviewRequired); err != nil {
		t.Fatalf("read Connector policy after rejected stale principal: %v", err)
	}
	if desiredEnabled || !reviewRequired {
		t.Fatalf(
			"Connector policy after rejected stale principal = enabled %v reviewRequired %v",
			desiredEnabled,
			reviewRequired,
		)
	}
}

func TestFreezeRunConnectorSnapshotCarriesReviewedRuntimeContractIntoContext(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	service, err := NewConnectorService(ConnectorServiceOptions{Bus: mustAgentTestBus(t), Store: store})
	if err != nil {
		t.Fatal(err)
	}

	tools := []provideragent.ConnectorTool{{Name: "search", Enabled: true, ReadOnly: true}}
	contractDigest, err := provideragent.ConnectorToolContractDigest(tools)
	if err != nil {
		t.Fatal(err)
	}
	item := ConnectorObservationInput{
		Source: ConnectorSourceCodexApp, NativeID: "github", Name: "GitHub",
		Status: ConnectorStatusConnected, Action: ConnectorActionNone, CanExecute: true,
		ContractDigest: contractDigest, Tools: tools,
	}
	canonical, err := normalizeConnectorObservation(ConnectorProviderCodex, item)
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest, err := connectorManifestDigest([]ConnectorObservationInput{canonical})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := service.PublishDiscovery(ctx, fixture.principal, ConnectorDiscoveryInput{
		CompanyID: fixture.companyID, RuntimeID: fixture.runtimeID,
		DaemonID: "daemon-" + fixture.companyID.String(), Generation: uuid.New(),
		ManifestDigest: manifestDigest, ObservedAt: time.Now().UTC(),
		Items: []ConnectorObservationInput{item},
	}); err != nil || !result.Applied {
		t.Fatalf("PublishDiscovery() = %+v, %v", result, err)
	}

	conversationID := uuid.New()
	mustAgentExec(t, ctx, pool, `
		INSERT INTO conversations (
			id, company_id, kind, agent_id, created_by_user_id, title
		) VALUES ($1, $2, 'direct', $3, $4, 'Connector run')`,
		conversationID, fixture.companyID, fixture.rootAgentID, fixture.userID)
	var runID uuid.UUID
	if err := pool.QueryRow(
		ctx, `
		INSERT INTO runs (
			company_id, agent_id, conversation_id, runtime_id, status, context
		, run_type) VALUES ($1, $2, $3, $4, 'dispatched', '{"prompt":"inspect"}'::jsonb, (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $3 ))
		RETURNING id`,
		fixture.companyID, fixture.rootAgentID, conversationID, fixture.runtimeID,
	).Scan(&runID); err != nil {
		t.Fatalf("insert Run: %v", err)
	}

	var frozen generated.Run
	err = store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		var freezeErr error
		frozen, freezeErr = FreezeRunConnectorSnapshotInTx(ctx, q, runID, fixture.runtimeID)
		return freezeErr
	})
	if err != nil {
		t.Fatalf("FreezeRunConnectorSnapshotInTx() error = %v", err)
	}
	var snapshot struct {
		SchemaVersion int `json:"schemaVersion"`
		Connectors    []struct {
			ID             uuid.UUID                     `json:"id"`
			PolicyRevision int64                         `json:"policyRevision"`
			NativeID       string                        `json:"nativeId"`
			ContractDigest string                        `json:"contractDigest"`
			Tools          []provideragent.ConnectorTool `json:"tools"`
		} `json:"connectors"`
	}
	// The daemon reads the frozen contract, tools included, from the context.
	var runContext struct {
		Prompt            string          `json:"prompt"`
		ConnectorSnapshot json.RawMessage `json:"connectorSnapshot"`
	}
	if err := json.Unmarshal(frozen.Context, &runContext); err != nil {
		t.Fatalf("decode Run context: %v", err)
	}
	if err := json.Unmarshal(runContext.ConnectorSnapshot, &snapshot); err != nil {
		t.Fatalf("decode Connector snapshot: %v", err)
	}
	if runContext.Prompt != "inspect" || snapshot.SchemaVersion != ConnectorSchemaVersion ||
		len(snapshot.Connectors) != 1 || snapshot.Connectors[0].NativeID != "github" ||
		snapshot.Connectors[0].ContractDigest != contractDigest ||
		len(snapshot.Connectors[0].Tools) != 1 || snapshot.Connectors[0].Tools[0].Name != "search" {
		t.Fatalf("Run context = %+v; frozen Connector snapshot = %+v", runContext, snapshot)
	}
	// SQL reads the same contract from typed rows.
	var frozenConnectorID uuid.UUID
	var frozenRevision int64
	var frozenNativeID, frozenDigest string
	if err := pool.QueryRow(ctx, `
		SELECT connector_id, policy_revision, native_id, contract_digest
		FROM run_connector_snapshots WHERE run_id = $1`, runID,
	).Scan(&frozenConnectorID, &frozenRevision, &frozenNativeID, &frozenDigest); err != nil {
		t.Fatalf("read frozen Connector rows: %v", err)
	}
	if !frozen.ConnectorSnapshotFrozenAt.Valid || frozenConnectorID != snapshot.Connectors[0].ID ||
		frozenRevision != snapshot.Connectors[0].PolicyRevision ||
		frozenNativeID != "github" || frozenDigest != contractDigest {
		t.Fatalf(
			"frozen rows = %s %d %s %s, marker %v",
			frozenConnectorID,
			frozenRevision,
			frozenNativeID,
			frozenDigest,
			frozen.ConnectorSnapshotFrozenAt,
		)
	}

	// Message tags freeze the sender's placement in addition to the contract.
	mustAgentExec(
		t,
		ctx,
		pool,
		`UPDATE runs SET connector_selection_mode='exact', execution_authorized_by_user_id=$2,
 context=context || jsonb_build_object('messageConnectorSelection',true,'messageConnectorRuntimeId',$3::uuid) WHERE id=$1`,
		runID,
		fixture.userID,
		fixture.runtimeID,
	)
	mustAgentExec(
		t,
		ctx,
		pool,
		`INSERT INTO run_connector_selections(run_id,connector_id,policy_revision,source,native_id,contract_digest) VALUES($1,$2,$3,'codex_app','github',$4)`,
		runID,
		frozenConnectorID,
		frozenRevision,
		contractDigest,
	)
	freezeMessage := func() error {
		return store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
			_, err := FreezeRunConnectorSnapshotInTx(ctx, q, runID, fixture.runtimeID)
			return err
		})
	}
	if err := freezeMessage(); err != nil {
		t.Fatalf("message claim = %v", err)
	}
	mustAgentExec(
		t,
		ctx,
		pool,
		`UPDATE agents SET settings=jsonb_build_object('capabilities',jsonb_build_object('disabledMcpServers',jsonb_build_array($2::text))) WHERE id=$1`,
		fixture.rootAgentID,
		frozenConnectorID.String(),
	)
	if err := freezeMessage(); !errors.Is(err, ErrConnectorSnapshotUnavailable) {
		t.Fatalf("disabled Agent message claim = %v", err)
	}
	mustAgentExec(t, ctx, pool, `UPDATE agents SET settings='{}' WHERE id=$1`, fixture.rootAgentID)
	mustAgentExec(
		t,
		ctx,
		pool,
		`UPDATE runs SET context=jsonb_set(context,'{messageConnectorRuntimeId}',to_jsonb($2::text)) WHERE id=$1`,
		runID,
		uuid.New().String(),
	)
	if err := freezeMessage(); !errors.Is(err, ErrConnectorSnapshotUnavailable) {
		t.Fatalf("changed placement message claim = %v", err)
	}

	var rejectedRunID uuid.UUID
	if err := pool.QueryRow(
		ctx, `
		INSERT INTO runs (
			company_id, agent_id, conversation_id, runtime_id, status, context
		, run_type) VALUES ($1, $2, $3, $4, 'dispatched', '{"prompt":"reject"}'::jsonb, (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $3 ))
		RETURNING id`,
		fixture.companyID, fixture.rootAgentID, conversationID, fixture.runtimeID,
	).Scan(&rejectedRunID); err != nil {
		t.Fatalf("insert Run for ineligible owner: %v", err)
	}
	mustAgentExec(t, ctx, pool,
		`UPDATE users SET disabled_at = clock_timestamp() WHERE id = $1`, fixture.userID)
	err = store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		_, freezeErr := FreezeRunConnectorSnapshotInTx(ctx, q, rejectedRunID, fixture.runtimeID)
		return freezeErr
	})
	if !errors.Is(err, ErrConnectorSnapshotUnavailable) {
		t.Fatalf(
			"FreezeRunConnectorSnapshotInTx(ineligible owner) error = %v, want %v",
			err,
			ErrConnectorSnapshotUnavailable,
		)
	}

	connectorID := snapshot.Connectors[0].ID
	mustAgentExec(t, ctx, pool, `
		UPDATE connector_policies
		SET desired_enabled = false, version = version + 1
		WHERE connector_id = $1`, connectorID)
	invalidated, err := ListRunIDsWithInvalidConnectorSnapshots(
		ctx,
		store.Queries(),
		ConnectorRunChange{
			CompanyID: fixture.companyID, ConnectorID: &connectorID,
			Provider: ConnectorProviderCodex, Type: ConnectorRunChangePolicy,
		},
	)
	if err != nil {
		t.Fatalf("ListRunIDsWithInvalidConnectorSnapshots() error = %v", err)
	}
	if len(invalidated) != 1 || invalidated[0] != runID {
		t.Fatalf("invalidated Run IDs = %v, want [%d]", invalidated, runID)
	}
}

func TestFreezeRunConnectorSnapshotRejectsPartialExactSelection(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	service, err := NewConnectorService(ConnectorServiceOptions{Bus: mustAgentTestBus(t), Store: store})
	if err != nil {
		t.Fatal(err)
	}

	tools := []provideragent.ConnectorTool{{Name: "search", Enabled: true, ReadOnly: true}}
	contractDigest, err := provideragent.ConnectorToolContractDigest(tools)
	if err != nil {
		t.Fatal(err)
	}
	item := ConnectorObservationInput{
		Source: ConnectorSourceCodexApp, NativeID: "github", Name: "GitHub",
		Status: ConnectorStatusConnected, Action: ConnectorActionNone, CanExecute: true,
		ContractDigest: contractDigest, Tools: tools,
	}
	canonical, err := normalizeConnectorObservation(ConnectorProviderCodex, item)
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest, err := connectorManifestDigest([]ConnectorObservationInput{canonical})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := service.PublishDiscovery(ctx, fixture.principal, ConnectorDiscoveryInput{
		CompanyID: fixture.companyID, RuntimeID: fixture.runtimeID,
		DaemonID: "daemon-" + fixture.companyID.String(), Generation: uuid.New(),
		ManifestDigest: manifestDigest, ObservedAt: time.Now().UTC(),
		Items: []ConnectorObservationInput{item},
	}); err != nil || !result.Applied {
		t.Fatalf("PublishDiscovery() = %+v, %v", result, err)
	}

	var connectorID uuid.UUID
	var policyRevision int64
	if err := pool.QueryRow(
		ctx, `
		SELECT catalog.id, policies.version
		FROM connector_catalog_items AS catalog
		JOIN connector_policies AS policies
		  ON policies.connector_id = catalog.id
		 AND policies.company_id = catalog.company_id
		WHERE catalog.company_id = $1 AND catalog.native_id = 'github'`,
		fixture.companyID,
	).Scan(&connectorID, &policyRevision); err != nil {
		t.Fatalf("read reviewed Connector: %v", err)
	}
	conversationID := uuid.New()
	mustAgentExec(t, ctx, pool, `
		INSERT INTO conversations (
			id, company_id, kind, agent_id, created_by_user_id, title
		) VALUES ($1, $2, 'direct', $3, $4, 'Exact Connector run')`,
		conversationID, fixture.companyID, fixture.rootAgentID, fixture.userID)
	var runID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO runs (
			company_id, agent_id, conversation_id, runtime_id, status,
			context, connector_selection_mode
		, run_type) VALUES ($1, $2, $3, $4, 'dispatched', '{"prompt":"inspect"}'::jsonb, 'exact', (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $3 ))
		RETURNING id`, fixture.companyID, fixture.rootAgentID, conversationID,
		fixture.runtimeID).Scan(&runID); err != nil {
		t.Fatalf("insert exact-selection Run: %v", err)
	}
	// One reviewed connector and one that no longer exists: all or nothing.
	mustAgentExec(t, ctx, pool, `
		INSERT INTO run_connector_selections (run_id, connector_id, policy_revision, source, native_id, contract_digest)
		VALUES ($1, $2, $3, 'codex_app', 'github', $4), ($1, $5, 1, 'codex_app', 'removed', $4)`,
		runID, connectorID, policyRevision, contractDigest, uuid.New())

	err = store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		_, freezeErr := FreezeRunConnectorSnapshotInTx(ctx, q, runID, fixture.runtimeID)
		return freezeErr
	})
	if !errors.Is(err, ErrConnectorSnapshotUnavailable) {
		t.Fatalf("FreezeRunConnectorSnapshotInTx() error = %v, want %v",
			err, ErrConnectorSnapshotUnavailable)
	}
	var runContext []byte
	var frozenRows int
	var frozenAt *time.Time
	if err := pool.QueryRow(
		ctx, `
		SELECT context, connector_snapshot_frozen_at,
			(SELECT count(*) FROM run_connector_snapshots WHERE run_id = runs.id)
		FROM runs WHERE id = $1`, runID,
	).Scan(&runContext, &frozenAt, &frozenRows); err != nil {
		t.Fatalf("read rejected Run snapshot: %v", err)
	}
	var contextDocument map[string]json.RawMessage
	if err := json.Unmarshal(runContext, &contextDocument); err != nil {
		t.Fatalf("decode rejected Run context: %v", err)
	}
	if _, exists := contextDocument["connectorSnapshot"]; exists {
		t.Fatalf("rejected Run context contains a partial Connector snapshot: %s", runContext)
	}
	if frozenRows != 0 || frozenAt != nil {
		t.Fatalf("rejected Run froze %d rows, marker %v", frozenRows, frozenAt)
	}
	invalidated, err := ListRunIDsWithInvalidConnectorSnapshots(
		ctx,
		store.Queries(),
		ConnectorRunChange{
			CompanyID: fixture.companyID,
			RuntimeID: &fixture.runtimeID,
			Provider:  ConnectorProviderCodex,
			Type:      ConnectorRunChangeDiscovery,
		},
	)
	if err != nil {
		t.Fatalf("ListRunIDsWithInvalidConnectorSnapshots() error = %v", err)
	}
	if len(invalidated) != 1 || invalidated[0] != runID {
		t.Fatalf("invalidated exact-selection Run IDs = %v, want [%d]", invalidated, runID)
	}
}

type connectorRunInvalidatorRecorder struct {
	changes []ConnectorRunChange
}

func (r *connectorRunInvalidatorRecorder) InvalidateConnectorRuns(
	_ context.Context,
	change ConnectorRunChange,
) {
	r.changes = append(r.changes, change)
}

// A reaffirmation renews exactly one timestamp, the generation's observedAt,
// which is all freshness is read from; catalog, observation and policy rows
// and their revisions stay untouched. Only the current generation and digest
// may be reaffirmed, and an observation that is not newer changes nothing.
func TestReaffirmDiscoveryRenewsFreshnessOnly(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	invalidator := &connectorRunInvalidatorRecorder{}
	service, err := NewConnectorService(
		ConnectorServiceOptions{Bus: mustAgentTestBus(t), Store: store, RunInvalidator: invalidator},
	)
	if err != nil {
		t.Fatal(err)
	}
	daemonID := "daemon-" + fixture.companyID.String()
	published := publishTestConnectors(t, ctx, service, fixture.principal, fixture.runtimeID, daemonID,
		[]ConnectorObservationInput{executableTestConnector(testGmailID, "Gmail")})
	invalidator.changes = nil

	type rowState struct {
		generationObservedAt, generationUpdatedAt                   time.Time
		catalogVersion                                              int64
		catalogUpdatedAt, observationObservedAt, observationUpdated time.Time
		policyVersion                                               int64
		policyUpdatedAt                                             time.Time
	}
	read := func() rowState {
		t.Helper()
		var state rowState
		if err := pool.QueryRow(ctx, `
			SELECT generations.observed_at, generations.updated_at,
				catalog.version, catalog.updated_at,
				observations.observed_at, observations.updated_at,
				policies.version, policies.updated_at
			FROM connector_discovery_generations AS generations
			JOIN connector_runtime_observations AS observations ON observations.runtime_id = generations.runtime_id
			JOIN connector_catalog_items AS catalog ON catalog.id = observations.connector_id
			JOIN connector_policies AS policies ON policies.connector_id = catalog.id
			WHERE generations.runtime_id = $1`, fixture.runtimeID).Scan(
			&state.generationObservedAt, &state.generationUpdatedAt,
			&state.catalogVersion, &state.catalogUpdatedAt,
			&state.observationObservedAt, &state.observationUpdated,
			&state.policyVersion, &state.policyUpdatedAt,
		); err != nil {
			t.Fatalf("read discovery rows: %v", err)
		}
		return state
	}
	before := read()
	reaffirm := func(change func(*ConnectorDiscoveryReaffirmInput)) (ConnectorDiscoveryResult, error) {
		input := ConnectorDiscoveryReaffirmInput{
			CompanyID: fixture.companyID, RuntimeID: fixture.runtimeID, DaemonID: daemonID,
			Generation: published.Generation, ManifestDigest: published.ManifestDigest,
			ObservedAt: published.ObservedAt.Add(time.Second),
		}
		if change != nil {
			change(&input)
		}
		return service.ReaffirmDiscovery(ctx, fixture.principal, input)
	}

	result, err := reaffirm(nil)
	if err != nil || !result.Applied {
		t.Fatalf("ReaffirmDiscovery() = %+v, %v", result, err)
	}
	after := read()
	if !after.generationObservedAt.Equal(published.ObservedAt.Add(time.Second).Truncate(time.Microsecond)) ||
		!after.generationUpdatedAt.After(before.generationUpdatedAt) {
		t.Fatalf("generation after reaffirmation = %+v, before %+v", after, before)
	}
	if after.catalogVersion != before.catalogVersion || !after.catalogUpdatedAt.Equal(before.catalogUpdatedAt) ||
		!after.observationObservedAt.Equal(before.observationObservedAt) ||
		!after.observationUpdated.Equal(before.observationUpdated) ||
		after.policyVersion != before.policyVersion || !after.policyUpdatedAt.Equal(before.policyUpdatedAt) {
		t.Fatalf("reaffirmation rewrote more than freshness: before %+v after %+v", before, after)
	}
	if len(invalidator.changes) != 0 {
		t.Fatalf("an unchanged reaffirmation invalidated Runs: %+v", invalidator.changes)
	}

	for name, observedAt := range map[string]time.Time{
		"replayed":            published.ObservedAt.Add(time.Second),
		"newer below storage": after.generationObservedAt.Add(500 * time.Nanosecond),
		"older":               published.ObservedAt,
	} {
		result, err := reaffirm(func(input *ConnectorDiscoveryReaffirmInput) { input.ObservedAt = observedAt })
		if err != nil || result.Applied {
			t.Fatalf("ReaffirmDiscovery(%s) = %+v, %v; want applied false", name, result, err)
		}
	}
	if !read().generationObservedAt.Equal(after.generationObservedAt) {
		t.Fatal("an unapplied reaffirmation moved freshness")
	}
	if _, err := reaffirm(func(input *ConnectorDiscoveryReaffirmInput) {
		input.ObservedAt = time.Now().Add(10 * time.Minute)
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ReaffirmDiscovery(future) error = %v, want ErrInvalidInput", err)
	}
	for name, change := range map[string]func(*ConnectorDiscoveryReaffirmInput){
		"another generation": func(input *ConnectorDiscoveryReaffirmInput) { input.Generation = uuid.New() },
		"another digest": func(input *ConnectorDiscoveryReaffirmInput) {
			input.ManifestDigest = strings.Repeat("a", 64)
		},
	} {
		if _, err := reaffirm(change); !errors.Is(err, ErrConnectorDiscoveryConflict) {
			t.Fatalf("ReaffirmDiscovery(%s) error = %v, want ErrConnectorDiscoveryConflict", name, err)
		}
	}
	if _, err := reaffirm(func(input *ConnectorDiscoveryReaffirmInput) {
		input.DaemonID = "another-daemon"
	}); !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("ReaffirmDiscovery(foreign daemon) error = %v, want ErrRuntimeNotFound", err)
	}
	mustAgentExec(t, ctx, pool,
		`DELETE FROM connector_discovery_generations WHERE runtime_id = $1`, fixture.runtimeID)
	if _, err := reaffirm(func(input *ConnectorDiscoveryReaffirmInput) {
		input.ObservedAt = time.Now().UTC()
	}); !errors.Is(err, ErrConnectorDiscoveryConflict) {
		t.Fatalf("ReaffirmDiscovery(no generation) error = %v, want ErrConnectorDiscoveryConflict", err)
	}
}

// An Agent a member binds to their Runtime after its last publication gets
// the default Connector policy from the next reaffirmation (ADR 0014), and
// only a reaffirmation that changed policy invalidates Runs.
func TestReaffirmDiscoveryReconcilesAgentsBoundSinceThePublish(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	invalidator := &connectorRunInvalidatorRecorder{}
	bus := mustAgentTestBus(t)
	var policyChanges int
	if err := bus.Subscribe("connector.policy_changed", func(context.Context, events.Event) error {
		policyChanges++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	service, err := NewConnectorService(
		ConnectorServiceOptions{Bus: bus, Store: store, RunInvalidator: invalidator},
	)
	if err != nil {
		t.Fatal(err)
	}
	secondRuntimeID := uuid.Must(uuid.NewV7())
	secondDaemonID := "second-" + fixture.companyID.String()
	mustAgentExec(t, ctx, pool, `
		INSERT INTO runtimes (id, company_id, daemon_id, name, provider, status, owner_user_id)
		VALUES ($1, $2, $3, 'Second Runtime', 'codex', 'online', $4)`,
		secondRuntimeID, fixture.companyID, secondDaemonID, fixture.userID)
	published := publishTestConnectors(t, ctx, service, fixture.principal, secondRuntimeID, secondDaemonID,
		[]ConnectorObservationInput{executableTestConnector(testGmailID, "Gmail")})
	desired := func() bool {
		t.Helper()
		var enabled bool
		if err := pool.QueryRow(ctx, `
			SELECT policies.desired_enabled
			FROM connector_policies AS policies
			JOIN connector_catalog_items AS catalog ON catalog.id = policies.connector_id
			WHERE catalog.company_id = $1 AND catalog.native_id = $2`,
			fixture.companyID, testGmailID).Scan(&enabled); err != nil {
			t.Fatalf("read Gmail policy: %v", err)
		}
		return enabled
	}
	if desired() {
		t.Fatal("a Runtime without Agents enabled Gmail at publication")
	}
	if policyChanges != 0 {
		t.Fatalf("policy events without bound Agents = %d, want 0", policyChanges)
	}

	dbtest.SeedBoundAgent(t, ctx, pool, uuid.Must(uuid.NewV7()), fixture.companyID, "Assistant", "codex",
		&fixture.rootAgentID, fixture.userID, secondRuntimeID)
	invalidator.changes = nil
	reaffirm := func(observedAt time.Time) {
		t.Helper()
		result, err := service.ReaffirmDiscovery(ctx, fixture.principal, ConnectorDiscoveryReaffirmInput{
			CompanyID:      fixture.companyID,
			RuntimeID:      secondRuntimeID,
			DaemonID:       secondDaemonID,
			Generation:     published.Generation,
			ManifestDigest: published.ManifestDigest,
			ObservedAt:     observedAt,
		})
		if err != nil || !result.Applied {
			t.Fatalf("ReaffirmDiscovery() = %+v, %v", result, err)
		}
	}
	reaffirm(published.ObservedAt.Add(time.Second))
	if !desired() {
		t.Fatal("reaffirmation did not reconcile policy for the newly bound Agent")
	}
	if policyChanges != 1 {
		t.Fatalf("policy events after binding = %d, want 1", policyChanges)
	}
	if len(invalidator.changes) != 1 || invalidator.changes[0].RuntimeID == nil ||
		*invalidator.changes[0].RuntimeID != secondRuntimeID ||
		invalidator.changes[0].Type != ConnectorRunChangeDiscovery {
		t.Fatalf("invalidations after reconciling reaffirmation = %+v", invalidator.changes)
	}
	reaffirm(published.ObservedAt.Add(2 * time.Second))
	if policyChanges != 1 {
		t.Fatalf("unchanged reaffirmation policy events = %d, want 1", policyChanges)
	}
	if len(invalidator.changes) != 1 {
		t.Fatalf("a reaffirmation that changed no policy invalidated Runs: %+v", invalidator.changes)
	}
}

// A reaffirmation re-observed nothing new, so in a Company where two Runtimes
// of one provider both host Agents it may enable a Connector for a newly bound
// Agent but never revises a policy the other Runtime's publication settled.
// Otherwise each keepalive would flip the shared policy between the two
// Runtimes' views, bump its revision and cancel every Run that froze it.
func TestReaffirmDiscoveryNeverRevisesPoliciesAcrossRuntimes(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	invalidator := &connectorRunInvalidatorRecorder{}
	service, err := NewConnectorService(
		ConnectorServiceOptions{Bus: mustAgentTestBus(t), Store: store, RunInvalidator: invalidator},
	)
	if err != nil {
		t.Fatal(err)
	}
	firstDaemonID := "daemon-" + fixture.companyID.String()
	secondRuntimeID := uuid.Must(uuid.NewV7())
	secondDaemonID := "second-" + fixture.companyID.String()
	mustAgentExec(t, ctx, pool, `
		INSERT INTO runtimes (id, company_id, daemon_id, name, provider, status, owner_user_id)
		VALUES ($1, $2, $3, 'Second Runtime', 'codex', 'online', $4)`,
		secondRuntimeID, fixture.companyID, secondDaemonID, fixture.userID)
	dbtest.SeedBoundAgent(t, ctx, pool, uuid.Must(uuid.NewV7()), fixture.companyID, "Assistant", "codex",
		&fixture.rootAgentID, fixture.userID, secondRuntimeID)

	// Gmail is connected on the first Runtime and awaits authentication on the
	// second, whose later publication marks the shared policy for review.
	first := publishTestConnectors(t, ctx, service, fixture.principal, fixture.runtimeID, firstDaemonID,
		[]ConnectorObservationInput{executableTestConnector(testGmailID, "Gmail")})
	second := publishTestConnectors(t, ctx, service, fixture.principal, secondRuntimeID, secondDaemonID,
		[]ConnectorObservationInput{{
			Source: ConnectorSourceCodexApp, NativeID: testGmailID, Name: "Gmail",
			Status: ConnectorStatusNeedsAuth, Action: ConnectorActionAuthenticate,
		}})
	type policyState struct {
		desired, review bool
		version         int64
	}
	policy := func() policyState {
		t.Helper()
		var state policyState
		if err := pool.QueryRow(ctx, `
			SELECT policies.desired_enabled, policies.review_required, policies.version
			FROM connector_policies AS policies
			JOIN connector_catalog_items AS catalog ON catalog.id = policies.connector_id
			WHERE catalog.company_id = $1 AND catalog.native_id = $2`,
			fixture.companyID,
			testGmailID,
		).Scan(&state.desired, &state.review, &state.version); err != nil {
			t.Fatalf("read Gmail policy: %v", err)
		}
		return state
	}
	settled := policy()
	if !settled.desired {
		t.Fatalf("Gmail policy after both publications = %+v, want enabled", settled)
	}
	invalidator.changes = nil

	for round := 1; round <= 3; round++ {
		for _, reaffirmation := range []ConnectorDiscoveryReaffirmInput{
			{
				CompanyID: fixture.companyID, RuntimeID: fixture.runtimeID, DaemonID: firstDaemonID,
				Generation: first.Generation, ManifestDigest: first.ManifestDigest,
			},
			{
				CompanyID: fixture.companyID, RuntimeID: secondRuntimeID, DaemonID: secondDaemonID,
				Generation: second.Generation, ManifestDigest: second.ManifestDigest,
			},
		} {
			reaffirmation.ObservedAt = second.ObservedAt.Add(time.Duration(round) * time.Second)
			result, err := service.ReaffirmDiscovery(ctx, fixture.principal, reaffirmation)
			if err != nil || !result.Applied {
				t.Fatalf(
					"round %d: ReaffirmDiscovery(%s) = %+v, %v",
					round,
					reaffirmation.RuntimeID,
					result,
					err,
				)
			}
			if got := policy(); got != settled {
				t.Fatalf("round %d: reaffirming %s revised the Gmail policy %+v -> %+v",
					round, reaffirmation.RuntimeID, settled, got)
			}
		}
	}
	if len(invalidator.changes) != 0 {
		t.Fatalf("reaffirmations that settled nothing invalidated Runs: %+v", invalidator.changes)
	}
}
