//go:build integration

package commerce_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/approval"
	"boardly.dev/boardly/server/internal/commerce"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

func TestCommerceManifestPublishIsDraftOnlyAndAtomicallySupersedesCatalog(t *testing.T) {
	test := newCommerceTest(t)
	readyGatewayAccount(t, test)

	gateway := newCatalogGateway()
	service := newGatewayIntegrationService(t, test, gateway)
	first := submitGatewayManifest(t, test, service, 1299)
	if gateway.callCount() != 0 {
		t.Fatalf("draft submission made %d provider calls, want 0", gateway.callCount())
	}
	if _, err := service.ApproveAndPublishManifest(
		test.ctx,
		test.ownerA,
		commerce.ApproveManifestInput{ManifestID: first.ManifestID, Digest: first.Digest},
	); err != nil {
		t.Fatalf("ApproveAndPublishManifest(first) error = %v", err)
	}
	if gateway.callCount() != 3 {
		t.Fatalf("first catalog publish provider calls = %d, want 3", gateway.callCount())
	}

	replacementRunID := replaceGatewayManifestRun(t, test)
	second := submitGatewayManifestForRun(
		t,
		test,
		service,
		replacementRunID,
		dbtest.NewUUID(t),
		1599,
	)
	if gateway.callCount() != 3 {
		t.Fatalf("second draft submission made provider calls: %d", gateway.callCount())
	}
	if _, err := service.ApproveAndPublishManifest(
		test.ctx,
		test.ownerA,
		commerce.ApproveManifestInput{ManifestID: second.ManifestID, Digest: second.Digest},
	); err != nil {
		t.Fatalf("ApproveAndPublishManifest(second) error = %v", err)
	}
	if gateway.callCount() != 6 {
		t.Fatalf("second catalog publish provider calls = %d, want 6", gateway.callCount())
	}

	rows, err := test.pool.Query(test.ctx, `
		SELECT manifests.id, manifests.state, offers.status, offers.amount
		FROM commerce_manifests AS manifests
		JOIN commerce_offers AS offers ON offers.manifest_id=manifests.id
		WHERE manifests.site_id=$1
		ORDER BY manifests.created_at, manifests.id`, first.SiteID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type catalogRow struct {
		manifestID uuid.UUID
		state      string
		offerState string
		amount     int64
	}
	var catalog []catalogRow
	for rows.Next() {
		var row catalogRow
		if err := rows.Scan(&row.manifestID, &row.state, &row.offerState, &row.amount); err != nil {
			t.Fatal(err)
		}
		catalog = append(catalog, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 2 || catalog[0].manifestID != first.ManifestID ||
		catalog[0].state != "superseded" || catalog[0].offerState != "disabled" ||
		catalog[0].amount != 1299 || catalog[1].manifestID != second.ManifestID ||
		catalog[1].state != "active" || catalog[1].offerState != "active" ||
		catalog[1].amount != 1599 {
		t.Fatalf("published catalog = %#v", catalog)
	}
	var activeManifestID uuid.UUID
	if err := test.pool.QueryRow(test.ctx, `
		SELECT active_manifest_id FROM commerce_sites WHERE id=$1`, first.SiteID).Scan(
		&activeManifestID,
	); err != nil {
		t.Fatal(err)
	}
	if activeManifestID != second.ManifestID {
		t.Fatalf("active manifest = %s, want %s", activeManifestID, second.ManifestID)
	}
}

func TestCommerceManifestPublishResolvesItsApprovalForAutomaticContinuation(t *testing.T) {
	test := newCommerceTest(t)
	readyGatewayAccount(t, test)
	service := newGatewayIntegrationService(t, test, newCatalogGateway())

	manifest := submitGatewayManifest(t, test, service, 1299)
	if manifest.ApprovalID == uuid.Nil {
		t.Fatal("SubmitManifest() approval ID is nil")
	}
	var kind, status, continuationStatus string
	var subjectID, taskID uuid.UUID
	var originRunID uuid.UUID
	if err := test.pool.QueryRow(test.ctx, `
		SELECT type, commerce_manifest_id, status, task_id, origin_run_id,
			COALESCE(continuation_status, '')
		FROM approvals
		WHERE company_id=$1 AND id=$2`, test.fixture.CompanyA, manifest.ApprovalID).Scan(
		&kind,
		&subjectID,
		&status,
		&taskID,
		&originRunID,
		&continuationStatus,
	); err != nil {
		t.Fatalf("read Commerce manifest approval: %v", err)
	}
	if kind != "commerce_manifest_publish" || subjectID != manifest.ManifestID ||
		status != "pending" || taskID != test.fixture.TaskA ||
		originRunID != test.fixture.RunA || continuationStatus != "" {
		t.Fatalf(
			"draft Commerce approval = kind:%q subject:%s status:%q task:%s run:%d continuation:%q",
			kind,
			subjectID,
			status,
			taskID,
			originRunID,
			continuationStatus,
		)
	}

	if _, err := service.ApproveAndPublishManifest(
		test.ctx,
		test.ownerA,
		commerce.ApproveManifestInput{ManifestID: manifest.ManifestID, Digest: manifest.Digest},
	); err != nil {
		t.Fatalf("ApproveAndPublishManifest() error = %v", err)
	}
	var executionStatus string
	var decidedBy uuid.UUID
	if err := test.pool.QueryRow(test.ctx, `
		SELECT status, execution_status, continuation_status, decided_by_user_id
		FROM approvals
		WHERE company_id=$1 AND id=$2`, test.fixture.CompanyA, manifest.ApprovalID).Scan(
		&status,
		&executionStatus,
		&continuationStatus,
		&decidedBy,
	); err != nil {
		t.Fatalf("read resolved Commerce manifest approval: %v", err)
	}
	if status != "approved" || executionStatus != "executed" ||
		continuationStatus != "pending" || decidedBy != test.ownerA.UserID {
		t.Fatalf(
			"published Commerce approval = status:%q execution:%q continuation:%q decidedBy:%s",
			status,
			executionStatus,
			continuationStatus,
			decidedBy,
		)
	}

	dbtest.MustExec(t, test.ctx, test.pool, `
		UPDATE runs
		SET status='completed', updated_at=clock_timestamp()
		WHERE company_id=$1 AND id=$2`, test.fixture.CompanyA, test.fixture.RunA)
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("events.NewBus() error = %v", err)
	}
	worker, err := approval.NewLifecycleWorker(test.store, bus, nil)
	if err != nil {
		t.Fatalf("approval.NewLifecycleWorker() error = %v", err)
	}
	if err := worker.Reconcile(test.ctx); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var continuationRunID uuid.UUID
	if err := test.pool.QueryRow(test.ctx, `
		SELECT continuation_status, continuation_run_id
		FROM approvals
		WHERE company_id=$1 AND id=$2`, test.fixture.CompanyA, manifest.ApprovalID).Scan(
		&continuationStatus,
		&continuationRunID,
	); err != nil {
		t.Fatalf("read delivered Commerce continuation: %v", err)
	}
	if continuationStatus != "delivered" || continuationRunID == test.fixture.RunA || continuationRunID == uuid.Nil {
		t.Fatalf(
			"Commerce continuation = status:%q run:%d",
			continuationStatus,
			continuationRunID,
		)
	}
	var continuationTaskID, continuationConversationID, continuationAgentID uuid.UUID
	var continuationContext []byte
	if err := test.pool.QueryRow(test.ctx, `
		SELECT task_id, conversation_id, agent_id, context
		FROM runs
		WHERE company_id=$1 AND id=$2`, test.fixture.CompanyA, continuationRunID).Scan(
		&continuationTaskID,
		&continuationConversationID,
		&continuationAgentID,
		&continuationContext,
	); err != nil {
		t.Fatalf("read Commerce continuation Run: %v", err)
	}
	var continuation struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(continuationContext, &continuation); err != nil {
		t.Fatalf("decode Commerce continuation context: %v", err)
	}
	if continuationTaskID != test.fixture.TaskA ||
		continuationConversationID != test.fixture.ConversationA ||
		continuationAgentID != test.fixture.AgentA ||
		!strings.Contains(continuation.Prompt, manifest.ManifestID.String()) ||
		!strings.Contains(continuation.Prompt, manifest.Digest) {
		t.Fatalf(
			"Commerce continuation Run = task:%s conversation:%s agent:%s context:%s",
			continuationTaskID,
			continuationConversationID,
			continuationAgentID,
			continuationContext,
		)
	}
}

func TestCommerceManifestSubmissionRejectsAnUnclaimedRun(t *testing.T) {
	test := newCommerceTest(t)
	readyGatewayAccount(t, test)
	service := newGatewayIntegrationService(t, test, newCatalogGateway())

	_, err := service.SubmitManifest(
		test.ctx,
		test.ownerA,
		gatewayManifestContext(test, test.fixture.RunA, test.manifestRequestKey),
		commerce.SubmitManifestInput{
			Environment: commerce.EnvironmentSandbox,
			RawManifest: gatewayManifestJSON(1299),
		},
	)
	if !errors.Is(err, commerce.ErrAccessForbidden) {
		t.Fatalf("SubmitManifest(unclaimed Run) error = %v, want forbidden", err)
	}
}

func TestCommerceManifestSubmissionIsIdempotentPerRun(t *testing.T) {
	test := newCommerceTest(t)
	readyGatewayAccount(t, test)
	service := newGatewayIntegrationService(t, test, newCatalogGateway())
	activateGatewayManifestRun(t, test)

	input := commerce.SubmitManifestInput{
		Environment: commerce.EnvironmentSandbox,
		RawManifest: gatewayManifestJSON(1299),
	}
	contextA := gatewayManifestContext(test, test.fixture.RunA, test.manifestRequestKey)
	var requested []events.Event
	if err := service.bus.Subscribe(approval.Topic, func(_ context.Context, event events.Event) error {
		if event.Name == approval.EventApprovalRequested {
			requested = append(requested, event)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	first, err := service.SubmitManifest(test.ctx, test.ownerA, contextA, input)
	if err != nil {
		t.Fatalf("SubmitManifest(first Run) error = %v", err)
	}
	// IA-FR4: the Run raises the payment setup Approval for its member
	// (D31), so its frame and Activity name the Agent, the member and the
	// Run.
	raisedBy := events.RunActor(
		test.fixture.AgentA.String(),
		test.fixture.RunA.String(),
		test.ownerA.UserID.String(),
	)
	if len(requested) != 1 || requested[0].Actor != raisedBy {
		t.Fatalf("approval.requested frames = %+v, want one by %+v", requested, raisedBy)
	}
	replay, err := service.SubmitManifest(test.ctx, test.ownerA, contextA, input)
	if err != nil {
		t.Fatalf("SubmitManifest(first Run replay) error = %v", err)
	}
	if replay.ManifestID != first.ManifestID || replay.Digest != first.Digest {
		t.Fatalf("same-Run replay = %#v, want manifest %s", replay, first.ManifestID)
	}
	differentRequest := contextA
	differentRequest.RequestKey = dbtest.NewUUID(t)
	if _, err := service.SubmitManifest(
		test.ctx,
		test.ownerA,
		differentRequest,
		input,
	); !errors.Is(err, commerce.ErrManifestConflict) {
		t.Fatalf("SubmitManifest(same digest, different request) error = %v, want conflict", err)
	}

	replacementRequestKey := dbtest.NewUUID(t)
	replacementRunID := replaceGatewayManifestRun(t, test)
	replacementContext := gatewayManifestContext(test, replacementRunID, replacementRequestKey)
	replacement, err := service.SubmitManifest(
		test.ctx,
		test.ownerA,
		replacementContext,
		input,
	)
	if err != nil {
		t.Fatalf("SubmitManifest(replacement Run) error = %v", err)
	}
	if replacement.ManifestID == first.ManifestID || replacement.Digest != first.Digest {
		t.Fatalf("replacement Run submission = %#v, first = %#v", replacement, first)
	}
	replacementReplay, err := service.SubmitManifest(
		test.ctx,
		test.ownerA,
		replacementContext,
		input,
	)
	if err != nil {
		t.Fatalf("SubmitManifest(replacement Run replay) error = %v", err)
	}
	if replacementReplay.ManifestID != replacement.ManifestID {
		t.Fatalf(
			"replacement Run replay manifest = %s, want %s",
			replacementReplay.ManifestID,
			replacement.ManifestID,
		)
	}
}

func TestCommerceManifestHistoryKeysetPaginationReadsBeyondOneHundred(t *testing.T) {
	test := newCommerceTest(t)
	readyGatewayAccount(t, test)
	service := newGatewayIntegrationService(t, test, newCatalogGateway())
	manifest := submitGatewayManifest(t, test, service, 1299)
	dbtest.MustExec(t, test.ctx, test.pool, `
		INSERT INTO commerce_manifests (
			company_id, site_id, task_id, run_id, request_key,
			schema_version, canonical_json, digest, state, created_at, updated_at
		)
		SELECT source.company_id, source.site_id, source.task_id,
			gen_random_uuid(), gen_random_uuid(), source.schema_version,
			source.canonical_json, source.digest, 'draft',
			source.created_at - generated.value * interval '1 second',
			source.created_at - generated.value * interval '1 second'
		FROM commerce_manifests AS source
		CROSS JOIN generate_series(1, 101) AS generated(value)
		WHERE source.id=$1`, manifest.ManifestID)

	seen := make(map[uuid.UUID]struct{}, 102)
	options := commerce.ManifestListOptions{Limit: 50}
	pageCount := 0
	for {
		page, err := service.ListManifests(test.ctx, test.ownerA, options)
		if err != nil {
			t.Fatalf("ListManifests(page %d) error = %v", pageCount+1, err)
		}
		pageCount++
		if len(page.Items) == 0 || len(page.Items) > 50 {
			t.Fatalf("manifest page %d size = %d", pageCount, len(page.Items))
		}
		for _, review := range page.Items {
			if _, duplicate := seen[review.ManifestID]; duplicate {
				t.Fatalf("manifest %s appeared on more than one page", review.ManifestID)
			}
			seen[review.ManifestID] = struct{}{}
		}
		if page.Next == nil {
			break
		}
		options.Before = page.Next
	}
	if pageCount != 3 || len(seen) != 102 {
		t.Fatalf("manifest pagination = %d pages / %d rows, want 3 / 102", pageCount, len(seen))
	}
}

func TestCommerceManifestSubmissionRejectsAStaleTaskExecutionClaim(t *testing.T) {
	test := newCommerceTest(t)
	readyGatewayAccount(t, test)
	service := newGatewayIntegrationService(t, test, newCatalogGateway())
	activateGatewayManifestRun(t, test)
	dbtest.MustExec(t, test.ctx, test.pool, `
		UPDATE tasks
		SET execution_generation=execution_generation+1,
			updated_at=clock_timestamp(), version=version+1
		WHERE id=$1 AND company_id=$2`, test.fixture.TaskA, test.fixture.CompanyA)

	_, err := service.SubmitManifest(
		test.ctx,
		test.ownerA,
		gatewayManifestContext(test, test.fixture.RunA, test.manifestRequestKey),
		commerce.SubmitManifestInput{
			Environment: commerce.EnvironmentSandbox,
			RawManifest: gatewayManifestJSON(1299),
		},
	)
	if !errors.Is(err, commerce.ErrAccessForbidden) {
		t.Fatalf("SubmitManifest(stale Task execution) error = %v, want forbidden", err)
	}
}

func TestCommerceApprovalUsesAuthenticationRootAcrossRefreshRotation(t *testing.T) {
	test := newCommerceTest(t)
	readyGatewayAccount(t, test)
	service := newGatewayIntegrationService(t, test, newCatalogGateway())
	manifest := submitGatewayManifest(t, test, service, 1299)

	dbtest.MustExec(t, test.ctx, test.pool, `
		UPDATE auth_sessions
		SET created_at=clock_timestamp() - interval '1 hour'
		WHERE id=$1`, test.ownerA.SessionID)
	rotatedSessionID := dbtest.NewUUID(t)
	dbtest.MustExec(t, test.ctx, test.pool, `
		INSERT INTO auth_sessions (
			id, user_id, token_prefix, token_hash, expires_at,
			rotated_from_session_id, created_at
		) VALUES (
			$1, $2, 'commerce-rotated', $3,
			clock_timestamp() + interval '1 hour', $4, clock_timestamp()
		)`, rotatedSessionID, test.fixture.UserA, []byte("commerce-session-rotated"), test.ownerA.SessionID)
	rotatedPrincipal := test.ownerA
	rotatedPrincipal.SessionID = rotatedSessionID

	_, err := service.ApproveAndPublishManifest(
		test.ctx,
		rotatedPrincipal,
		commerce.ApproveManifestInput{ManifestID: manifest.ManifestID, Digest: manifest.Digest},
	)
	if !errors.Is(err, commerce.ErrApprovalExpired) {
		t.Fatalf("ApproveAndPublishManifest(rotated old login) error = %v, want reauthentication", err)
	}
}

func TestCommerceCheckoutReplayRetrievesPersistedSessionAndOldAmbiguityStops(t *testing.T) {
	test := newCommerceTest(t)
	readyGatewayAccount(t, test)
	gateway := newCatalogGateway()
	service := newGatewayIntegrationService(t, test, gateway)
	manifest := submitGatewayManifest(t, test, service, 1299)
	if _, err := service.ApproveAndPublishManifest(
		test.ctx,
		test.ownerA,
		commerce.ApproveManifestInput{ManifestID: manifest.ManifestID, Digest: manifest.Digest},
	); err != nil {
		t.Fatalf("ApproveAndPublishManifest() error = %v", err)
	}

	operationID := uuid.MustParse("01991d2f-cd80-7901-9f96-b6b88ca60101")
	first, err := service.CreateCheckout(test.ctx, "site-access-token", commerce.CheckoutRequest{
		OfferAlias: "report", OperationID: operationID,
	})
	if err != nil || first.URL == "" {
		t.Fatalf("CreateCheckout(first) = %+v, %v", first, err)
	}
	replay, err := service.CreateCheckout(test.ctx, "site-access-token", commerce.CheckoutRequest{
		OfferAlias: "report", OperationID: operationID,
	})
	if err != nil || replay.URL != first.URL {
		t.Fatalf("CreateCheckout(replay) = %+v, %v; want URL %q", replay, err, first.URL)
	}
	checkoutCalls, recoveryCalls := gateway.checkoutCallCounts()
	if checkoutCalls != 1 || recoveryCalls != 1 {
		t.Fatalf("Checkout provider calls = create:%d recover:%d, want 1/1", checkoutCalls, recoveryCalls)
	}

	staleOperationID := uuid.MustParse("01991d2f-cd80-7901-9f96-b6b88ca60102")
	customerID := gateway.customerID()
	var companyID, siteID, customerUUID, offerID uuid.UUID
	var environment string
	if err := test.pool.QueryRow(test.ctx, `
		SELECT offers.company_id, offers.site_id, customers.id, offers.id,
			offers.environment
		FROM commerce_offers AS offers
		JOIN commerce_customers AS customers ON customers.site_id=offers.site_id
		WHERE offers.manifest_id=$1 AND offers.alias='report'
		  AND customers.provider_customer_id=$2`, manifest.ManifestID, customerID).Scan(
		&companyID,
		&siteID,
		&customerUUID,
		&offerID,
		&environment,
	); err != nil {
		t.Fatal(err)
	}
	digest := gatewayOperationDigestForTest("checkout", siteID, customerUUID, offerID)
	dbtest.MustExec(
		t, test.ctx, test.pool, `
		INSERT INTO commerce_gateway_operations (
			id, company_id, site_id, customer_id, manifest_id, offer_id,
			environment, operation_type, operation_id, request_sha256,
			idempotency_key, state, provider_attempt_count, created_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, 'checkout', $8, $9,
			'boardly:checkout:v1:' || ($8::uuid)::text, 'recovery_required', 1,
			$10
		)`,
		uuid.MustParse("01991d2f-cd80-7901-9f96-b6b88ca60103"),
		companyID,
		siteID,
		customerUUID,
		manifest.ManifestID,
		offerID,
		environment,
		staleOperationID,
		digest,
		test.now.Add(-24*time.Hour),
	)
	if _, err := service.CreateCheckout(test.ctx, "site-access-token", commerce.CheckoutRequest{
		OfferAlias: "report", OperationID: staleOperationID,
	}); !errors.Is(err, commerce.ErrGatewayRecovery) {
		t.Fatalf("CreateCheckout(stale ambiguity) error = %v, want recovery", err)
	}
	checkoutCallsAfter, recoveryCallsAfter := gateway.checkoutCallCounts()
	if checkoutCallsAfter != checkoutCalls || recoveryCallsAfter != recoveryCalls {
		t.Fatalf(
			"stale operation reached Stripe: create %d->%d recover %d->%d",
			checkoutCalls,
			checkoutCallsAfter,
			recoveryCalls,
			recoveryCallsAfter,
		)
	}
}

func TestCommerceCheckoutRejectsUnavailableAccountBeforeCustomerOrProviderWrites(t *testing.T) {
	tests := []struct {
		name           string
		status         commerce.Status
		chargesEnabled bool
	}{
		{name: "restricted", status: commerce.StatusRestricted, chargesEnabled: true},
		{name: "disconnected", status: commerce.StatusDisconnected, chargesEnabled: true},
		{name: "charges disabled", status: commerce.StatusReady, chargesEnabled: false},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			test := newCommerceTest(t)
			readyGatewayAccount(t, test)
			gateway := newCatalogGateway()
			service := newGatewayIntegrationService(t, test, gateway)
			manifest := submitGatewayManifest(t, test, service, 1299)
			if _, err := service.ApproveAndPublishManifest(
				test.ctx,
				test.ownerA,
				commerce.ApproveManifestInput{
					ManifestID: manifest.ManifestID,
					Digest:     manifest.Digest,
				},
			); err != nil {
				t.Fatalf("ApproveAndPublishManifest() error = %v", err)
			}
			providerCalls := gateway.callCount()
			dbtest.MustExec(t, test.ctx, test.pool, `
				UPDATE commerce_accounts
				SET status=$2, charges_enabled=$3, updated_at=clock_timestamp(),
					version=version+1
				WHERE company_id=$1 AND environment='sandbox'`,
				test.fixture.CompanyA, string(testCase.status), testCase.chargesEnabled)

			_, err := service.CreateCheckout(
				test.ctx,
				"site-access-token",
				commerce.CheckoutRequest{
					OfferAlias: "report", OperationID: dbtest.NewUUID(t),
				},
			)
			if !errors.Is(err, commerce.ErrProviderSetupRequired) {
				t.Fatalf("CreateCheckout(unavailable account) error = %v, want setup required", err)
			}
			if got := gateway.callCount(); got != providerCalls {
				t.Fatalf(
					"unavailable Checkout provider calls = %d, want unchanged %d",
					got,
					providerCalls,
				)
			}
			var customers, rateLimits int
			if err := test.pool.QueryRow(test.ctx, `
				SELECT
					(SELECT count(*) FROM commerce_customers),
					(SELECT count(*) FROM commerce_rate_limits
					 WHERE action NOT LIKE 'challenge_%')`).Scan(
				&customers,
				&rateLimits,
			); err != nil {
				t.Fatalf("count unavailable Checkout writes: %v", err)
			}
			if customers != 0 || rateLimits != 0 {
				t.Fatalf(
					"unavailable Checkout writes = customers:%d rate limits:%d, want 0/0",
					customers,
					rateLimits,
				)
			}
		})
	}
}

func TestCommerceCheckoutRechecksCurrentProviderAccountBeforeCustomerOrProviderWrite(t *testing.T) {
	test := newCommerceTest(t)
	readyGatewayAccount(t, test)
	gateway := newCatalogGateway()
	service := newGatewayIntegrationService(t, test, gateway)
	manifest := submitGatewayManifest(t, test, service, 1299)
	if _, err := service.ApproveAndPublishManifest(
		test.ctx,
		test.ownerA,
		commerce.ApproveManifestInput{
			ManifestID: manifest.ManifestID,
			Digest:     manifest.Digest,
		},
	); err != nil {
		t.Fatalf("ApproveAndPublishManifest() error = %v", err)
	}
	providerCalls := gateway.callCount()
	before := commerceGatewayWriteCounts(t, test)
	test.provider.setAllAccountsPayoutsDisabled()

	_, err := service.CreateCheckout(
		test.ctx,
		"site-access-token",
		commerce.CheckoutRequest{
			OfferAlias: "report", OperationID: dbtest.NewUUID(t),
		},
	)
	if !errors.Is(err, commerce.ErrProviderSetupRequired) {
		t.Fatalf("CreateCheckout(stale ready projection) error = %v, want setup required", err)
	}
	if got := gateway.callCount(); got != providerCalls {
		t.Fatalf("stale Checkout provider writes = %d, want unchanged %d", got, providerCalls)
	}
	after := commerceGatewayWriteCounts(t, test)
	if after.customers != before.customers ||
		after.rateLimits != before.rateLimits+2 ||
		after.operations != before.operations {
		t.Fatalf(
			"stale Checkout writes = %+v, want customers/operations unchanged and two rate limits after %+v",
			after,
			before,
		)
	}
}

func TestCommercePortalRequiresExistingProviderCustomer(t *testing.T) {
	test := newCommerceTest(t)
	readyGatewayAccount(t, test)
	gateway := newCatalogGateway()
	service := newGatewayIntegrationService(t, test, gateway)
	manifest := submitGatewayManifest(t, test, service, 1299)
	if _, err := service.ApproveAndPublishManifest(
		test.ctx,
		test.ownerA,
		commerce.ApproveManifestInput{
			ManifestID: manifest.ManifestID,
			Digest:     manifest.Digest,
		},
	); err != nil {
		t.Fatalf("ApproveAndPublishManifest() error = %v", err)
	}
	providerCalls := gateway.callCount()
	before := commerceGatewayWriteCounts(t, test)

	_, err := service.CreatePortal(
		test.ctx,
		"site-access-token",
		commerce.PortalRequest{OperationID: dbtest.NewUUID(t)},
	)
	if !errors.Is(err, commerce.ErrCustomerNotFound) {
		t.Fatalf("CreatePortal(new subject) error = %v, want customer not found", err)
	}
	if got := gateway.callCount(); got != providerCalls {
		t.Fatalf("new-subject Portal provider calls = %d, want unchanged %d", got, providerCalls)
	}
	if after := commerceGatewayWriteCounts(t, test); after != before {
		t.Fatalf("new-subject Portal writes = %+v, want unchanged %+v", after, before)
	}
}

func TestCommercePublicGatewayRejectsCompanyAfterDeletionStarts(t *testing.T) {
	test := newCommerceTest(t)
	readyGatewayAccount(t, test)
	gateway := newCatalogGateway()
	service := newGatewayIntegrationService(t, test, gateway)
	manifest := submitGatewayManifest(t, test, service, 1299)
	if _, err := service.ApproveAndPublishManifest(
		test.ctx,
		test.ownerA,
		commerce.ApproveManifestInput{
			ManifestID: manifest.ManifestID,
			Digest:     manifest.Digest,
		},
	); err != nil {
		t.Fatalf("ApproveAndPublishManifest() error = %v", err)
	}
	if _, err := service.CreateCheckout(
		test.ctx,
		"site-access-token",
		commerce.CheckoutRequest{
			OfferAlias: "report", OperationID: dbtest.NewUUID(t),
		},
	); err != nil {
		t.Fatalf("CreateCheckout(before deletion) error = %v", err)
	}
	providerCalls := gateway.callCount()
	before := commerceGatewayWriteCounts(t, test)
	dbtest.MustExec(t, test.ctx, test.pool, `
		UPDATE companies
		SET deleting_at=clock_timestamp(), version=version+1,
			updated_at=clock_timestamp()
		WHERE id=$1`, test.fixture.CompanyA)

	if _, err := service.CreateCheckout(
		test.ctx,
		"site-access-token",
		commerce.CheckoutRequest{
			OfferAlias: "report", OperationID: dbtest.NewUUID(t),
		},
	); !errors.Is(err, commerce.ErrUnauthenticated) {
		t.Fatalf("CreateCheckout(deleting Company) error = %v, want unauthenticated", err)
	}
	if _, err := service.CreatePortal(
		test.ctx,
		"site-access-token",
		commerce.PortalRequest{OperationID: dbtest.NewUUID(t)},
	); !errors.Is(err, commerce.ErrUnauthenticated) {
		t.Fatalf("CreatePortal(deleting Company) error = %v, want unauthenticated", err)
	}
	if _, err := service.VerifyEntitlements(
		test.ctx,
		"site-access-token",
	); !errors.Is(err, commerce.ErrUnauthenticated) {
		t.Fatalf("VerifyEntitlements(deleting Company) error = %v, want unauthenticated", err)
	}
	if got := gateway.callCount(); got != providerCalls {
		t.Fatalf("deleting Company provider calls = %d, want unchanged %d", got, providerCalls)
	}
	if after := commerceGatewayWriteCounts(t, test); after != before {
		t.Fatalf("deleting Company writes = %+v, want unchanged %+v", after, before)
	}
}

type gatewayWriteCounts struct {
	customers  int
	rateLimits int
	operations int
}

func commerceGatewayWriteCounts(t *testing.T, test *commerceTest) gatewayWriteCounts {
	t.Helper()
	var counts gatewayWriteCounts
	if err := test.pool.QueryRow(test.ctx, `
		SELECT
			(SELECT count(*) FROM commerce_customers),
			(SELECT count(*) FROM commerce_rate_limits
			 WHERE action NOT LIKE 'challenge_%'),
			(SELECT count(*) FROM commerce_gateway_operations)`).Scan(
		&counts.customers,
		&counts.rateLimits,
		&counts.operations,
	); err != nil {
		t.Fatalf("count Commerce gateway writes: %v", err)
	}
	return counts
}

func TestCommerceRestrictedOrDrainingAccountKeepsPortalForExistingCustomer(t *testing.T) {
	for _, status := range []commerce.Status{
		commerce.StatusRestricted,
		commerce.StatusDraining,
	} {
		t.Run(string(status), func(t *testing.T) {
			test := newCommerceTest(t)
			readyGatewayAccount(t, test)
			gateway := newCatalogGateway()
			service := newGatewayIntegrationService(t, test, gateway)
			manifest := submitGatewayManifest(t, test, service, 1299)
			if _, err := service.ApproveAndPublishManifest(
				test.ctx,
				test.ownerA,
				commerce.ApproveManifestInput{
					ManifestID: manifest.ManifestID,
					Digest:     manifest.Digest,
				},
			); err != nil {
				t.Fatalf("ApproveAndPublishManifest() error = %v", err)
			}
			if _, err := service.CreateCheckout(
				test.ctx,
				"site-access-token",
				commerce.CheckoutRequest{
					OfferAlias: "report", OperationID: dbtest.NewUUID(t),
				},
			); err != nil {
				t.Fatalf("CreateCheckout() error = %v", err)
			}
			providerCalls := gateway.callCount()
			dbtest.MustExec(
				t, test.ctx, test.pool, `
				UPDATE commerce_accounts
				SET status=$2, charges_enabled=false,
					updated_at=clock_timestamp(), version=version+1
				WHERE company_id=$1 AND environment='sandbox'`,
				test.fixture.CompanyA,
				string(status),
			)

			portal, err := service.CreatePortal(
				test.ctx,
				"site-access-token",
				commerce.PortalRequest{OperationID: dbtest.NewUUID(t)},
			)
			if err != nil || portal.URL == "" {
				t.Fatalf("CreatePortal(%s) = %+v, %v", status, portal, err)
			}
			if got := gateway.callCount(); got != providerCalls+1 {
				t.Fatalf(
					"%s Portal provider calls = %d, want %d",
					status,
					got,
					providerCalls+1,
				)
			}
		})
	}
}

func TestCommerceDisconnectedAccountBlocksPortalButKeepsLocalEntitlementVerification(t *testing.T) {
	test := newCommerceTest(t)
	readyGatewayAccount(t, test)
	gateway := newCatalogGateway()
	service := newGatewayIntegrationService(t, test, gateway)
	manifest := submitGatewayManifest(t, test, service, 1299)
	if _, err := service.ApproveAndPublishManifest(
		test.ctx,
		test.ownerA,
		commerce.ApproveManifestInput{ManifestID: manifest.ManifestID, Digest: manifest.Digest},
	); err != nil {
		t.Fatalf("ApproveAndPublishManifest() error = %v", err)
	}
	providerCalls := gateway.callCount()
	dbtest.MustExec(t, test.ctx, test.pool, `
		UPDATE commerce_accounts
		SET status='disconnected', updated_at=clock_timestamp(), version=version+1
		WHERE company_id=$1 AND environment='sandbox'`, test.fixture.CompanyA)

	if _, err := service.CreatePortal(
		test.ctx,
		"site-access-token",
		commerce.PortalRequest{OperationID: dbtest.NewUUID(t)},
	); !errors.Is(err, commerce.ErrProviderSetupRequired) {
		t.Fatalf("CreatePortal(disconnected) error = %v, want setup required", err)
	}
	if got := gateway.callCount(); got != providerCalls {
		t.Fatalf("disconnected Portal provider calls = %d, want unchanged %d", got, providerCalls)
	}
	var customers int
	if err := test.pool.QueryRow(test.ctx, `SELECT count(*) FROM commerce_customers`).Scan(
		&customers,
	); err != nil {
		t.Fatalf("count disconnected Portal customers: %v", err)
	}
	if customers != 0 {
		t.Fatalf("disconnected Portal created %d customers, want 0", customers)
	}

	verification, err := service.VerifyEntitlements(test.ctx, "site-access-token")
	if err != nil {
		t.Fatalf("VerifyEntitlements(disconnected) error = %v", err)
	}
	if verification.Assertion == "" || len(verification.Entitlements) != 0 {
		t.Fatalf("VerifyEntitlements(disconnected) = %#v", verification)
	}
}

func TestCommerceCheckoutRateLimitStopsProviderWritesPerSiteCustomer(t *testing.T) {
	test := newCommerceTest(t)
	readyGatewayAccount(t, test)
	gateway := newCatalogGateway()
	service := newGatewayIntegrationService(t, test, gateway)
	manifest := submitGatewayManifest(t, test, service, 1299)
	if _, err := service.ApproveAndPublishManifest(
		test.ctx,
		test.ownerA,
		commerce.ApproveManifestInput{ManifestID: manifest.ManifestID, Digest: manifest.Digest},
	); err != nil {
		t.Fatalf("ApproveAndPublishManifest() error = %v", err)
	}

	for index := range 10 {
		operationID := uuid.MustParse(fmt.Sprintf(
			"01991d2f-cd80-7901-9f96-b6b88ca61%03d",
			index,
		))
		if _, err := service.CreateCheckout(
			test.ctx,
			"site-access-token",
			commerce.CheckoutRequest{OfferAlias: "report", OperationID: operationID},
		); err != nil {
			t.Fatalf("CreateCheckout(%d) error = %v", index, err)
		}
	}
	blockedID := uuid.MustParse("01991d2f-cd80-7901-9f96-b6b88ca61999")
	if _, err := service.CreateCheckout(
		test.ctx,
		"site-access-token",
		commerce.CheckoutRequest{OfferAlias: "report", OperationID: blockedID},
	); !errors.Is(err, commerce.ErrRateLimited) {
		t.Fatalf("CreateCheckout(over limit) error = %v, want rate limited", err)
	}
	checkoutCalls, _ := gateway.checkoutCallCounts()
	if checkoutCalls != 10 {
		t.Fatalf("Checkout provider calls = %d, want 10", checkoutCalls)
	}
}

func TestCommerceCatalogNeverRepostsAfterProviderIdempotencyWindow(t *testing.T) {
	test := newCommerceTest(t)
	readyGatewayAccount(t, test)
	gateway := &failingCatalogGateway{catalogGateway: newCatalogGateway()}
	service := newGatewayIntegrationService(t, test, gateway)
	manifest := submitGatewayManifest(t, test, service, 1299)
	if _, err := service.ApproveAndPublishManifest(
		test.ctx,
		test.ownerA,
		commerce.ApproveManifestInput{ManifestID: manifest.ManifestID, Digest: manifest.Digest},
	); !errors.Is(err, commerce.ErrProviderUnavailable) {
		t.Fatalf("ApproveAndPublishManifest(first ambiguity) error = %v", err)
	}
	if calls := gateway.callCount(); calls != 1 {
		t.Fatalf("first catalog provider calls = %d, want 1", calls)
	}
	dbtest.MustExec(t, test.ctx, test.pool, `
		UPDATE commerce_gateway_operations
		SET created_at=$2
		WHERE manifest_id=$1 AND operation_type='catalog_publish'`,
		manifest.ManifestID, test.now.Add(-24*time.Hour))
	if _, err := service.ApproveAndPublishManifest(
		test.ctx,
		test.ownerA,
		commerce.ApproveManifestInput{ManifestID: manifest.ManifestID, Digest: manifest.Digest},
	); !errors.Is(err, commerce.ErrGatewayRecovery) {
		t.Fatalf("ApproveAndPublishManifest(stale ambiguity) error = %v, want recovery", err)
	}
	if calls := gateway.callCount(); calls != 1 {
		t.Fatalf("stale catalog operation reached Stripe: calls = %d, want 1", calls)
	}
}

func TestCommerceCatalogRechecksReadyAccountAfterProviderIO(t *testing.T) {
	test := newCommerceTest(t)
	readyGatewayAccount(t, test)
	gateway := newBlockingCatalogGateway()
	service := newGatewayIntegrationService(t, test, gateway)
	manifest := submitGatewayManifest(t, test, service, 1299)
	t.Cleanup(func() { gateway.releaseOnce.Do(func() { close(gateway.release) }) })

	result := make(chan error, 1)
	go func() {
		_, err := service.ApproveAndPublishManifest(
			test.ctx,
			test.ownerA,
			commerce.ApproveManifestInput{
				ManifestID: manifest.ManifestID,
				Digest:     manifest.Digest,
			},
		)
		result <- err
	}()
	select {
	case <-gateway.started:
	case <-time.After(5 * time.Second):
		t.Fatal("catalog provider call did not start")
	}
	dbtest.MustExec(t, test.ctx, test.pool, `
		UPDATE commerce_accounts
		SET status='restricted', charges_enabled=false,
			updated_at=clock_timestamp(), version=version+1
		WHERE company_id=$1 AND environment='sandbox'`, test.fixture.CompanyA)
	gateway.releaseOnce.Do(func() { close(gateway.release) })
	select {
	case err := <-result:
		if !errors.Is(err, commerce.ErrProviderSetupRequired) {
			t.Fatalf("ApproveAndPublishManifest(account changed) error = %v, want setup required", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("catalog publication did not finish after provider release")
	}

	var manifestState string
	var siteHasActiveManifest bool
	if err := test.pool.QueryRow(test.ctx, `
		SELECT manifests.state, sites.active_manifest_id IS NOT NULL
		FROM commerce_manifests AS manifests
		JOIN commerce_sites AS sites ON sites.id=manifests.site_id
		WHERE manifests.id=$1`, manifest.ManifestID).Scan(
		&manifestState,
		&siteHasActiveManifest,
	); err != nil {
		t.Fatalf("read deferred Commerce catalog: %v", err)
	}
	if manifestState != "publishing" || siteHasActiveManifest {
		t.Fatalf(
			"deferred Commerce catalog = state:%q active:%t, want publishing/false",
			manifestState,
			siteHasActiveManifest,
		)
	}
	providerCalls := gateway.callCount()
	dbtest.MustExec(t, test.ctx, test.pool, `
		UPDATE commerce_accounts
		SET status='ready', charges_enabled=true,
			updated_at=clock_timestamp(), version=version+1
		WHERE company_id=$1 AND environment='sandbox'`, test.fixture.CompanyA)
	retried, err := service.ApproveAndPublishManifest(
		test.ctx,
		test.ownerA,
		commerce.ApproveManifestInput{
			ManifestID: manifest.ManifestID,
			Digest:     manifest.Digest,
		},
	)
	if err != nil || retried.State != "active" {
		t.Fatalf("ApproveAndPublishManifest(retry) = %#v, %v", retried, err)
	}
	if got := gateway.callCount(); got != providerCalls {
		t.Fatalf("catalog retry provider calls = %d, want unchanged %d", got, providerCalls)
	}
}

func TestCommerceCatalogVerifiesSiteBeforeRecordingProviderAttempt(t *testing.T) {
	test := newCommerceTest(t)
	readyGatewayAccount(t, test)
	gateway := newCatalogGateway()
	authority := &callbackSiteAuthority{
		verify: func(commerce.SiteVerificationInput) (commerce.VerifiedSite, error) {
			return commerce.VerifiedSite{}, errors.New("site proof rejected")
		},
	}
	service := newGatewayIntegrationServiceWithAuthority(t, test, gateway, authority)
	manifest := submitGatewayManifest(t, test, service, 1299)

	_, err := service.ApproveAndPublishManifest(
		test.ctx,
		test.ownerA,
		commerce.ApproveManifestInput{ManifestID: manifest.ManifestID, Digest: manifest.Digest},
	)
	if !errors.Is(err, commerce.ErrSiteAuthority) {
		t.Fatalf("ApproveAndPublishManifest() error = %v, want Site authority", err)
	}
	if gateway.callCount() != 0 {
		t.Fatalf("site verification failure made %d provider calls, want 0", gateway.callCount())
	}
	var operationState, manifestState string
	var providerAttempts int32
	if err := test.pool.QueryRow(
		test.ctx, `
		SELECT operations.state, operations.provider_attempt_count, manifests.state
		FROM commerce_gateway_operations AS operations
		JOIN commerce_manifests AS manifests ON manifests.id=operations.manifest_id
		WHERE operations.manifest_id=$1 AND operations.operation_type='catalog_publish'`,
		manifest.ManifestID,
	).Scan(&operationState, &providerAttempts, &manifestState); err != nil {
		t.Fatal(err)
	}
	if operationState != "pending" || providerAttempts != 0 || manifestState != "apply_failed" {
		t.Fatalf(
			"verification failure state = operation:%s attempts:%d manifest:%s",
			operationState,
			providerAttempts,
			manifestState,
		)
	}
}

func TestCommerceCatalogLeaseExpiryDuringVerificationStopsProviderCall(t *testing.T) {
	test := newCommerceTest(t)
	readyGatewayAccount(t, test)
	gateway := newCatalogGateway()
	authority := &callbackSiteAuthority{
		verify: func(input commerce.SiteVerificationInput) (commerce.VerifiedSite, error) {
			dbtest.MustExec(t, test.ctx, test.pool, `
				UPDATE commerce_gateway_operations
				SET lease_expires_at=clock_timestamp() - interval '1 second'
				WHERE operation_type='catalog_publish'
				  AND manifest_id=(
					SELECT id FROM commerce_manifests
					WHERE encode(digest, 'hex')=$1
				  )`, input.ManifestDigest)
			return commerce.VerifiedSite{
				SigningKeys: []commerce.SitePublicJWK{testSitePublicJWK()},
			}, nil
		},
	}
	service := newGatewayIntegrationServiceWithAuthority(t, test, gateway, authority)
	manifest := submitGatewayManifest(t, test, service, 1299)

	_, err := service.ApproveAndPublishManifest(
		test.ctx,
		test.ownerA,
		commerce.ApproveManifestInput{ManifestID: manifest.ManifestID, Digest: manifest.Digest},
	)
	if !errors.Is(err, commerce.ErrGatewayBusy) {
		t.Fatalf("ApproveAndPublishManifest() error = %v, want gateway busy", err)
	}
	if gateway.callCount() != 0 {
		t.Fatalf("expired verification lease made %d provider calls, want 0", gateway.callCount())
	}
	var providerAttempts int32
	if err := test.pool.QueryRow(test.ctx, `
		SELECT provider_attempt_count
		FROM commerce_gateway_operations
		WHERE manifest_id=$1 AND operation_type='catalog_publish'`, manifest.ManifestID).Scan(
		&providerAttempts,
	); err != nil {
		t.Fatal(err)
	}
	if providerAttempts != 0 {
		t.Fatalf("provider attempts = %d, want 0", providerAttempts)
	}
}

func TestCommerceStaleCatalogPublisherRollsBackFailureTransition(t *testing.T) {
	test := newCommerceTest(t)
	readyGatewayAccount(t, test)
	gateway := newBlockingFailingCatalogGateway()
	service := newGatewayIntegrationService(t, test, gateway)
	manifest := submitGatewayManifest(t, test, service, 1299)

	result := make(chan error, 1)
	go func() {
		_, err := service.ApproveAndPublishManifest(
			test.ctx,
			test.ownerA,
			commerce.ApproveManifestInput{ManifestID: manifest.ManifestID, Digest: manifest.Digest},
		)
		result <- err
	}()
	select {
	case <-gateway.started:
	case <-time.After(5 * time.Second):
		t.Fatal("catalog provider call did not start")
	}

	var operationVersion, manifestVersion int64
	var leaseID uuid.UUID
	if err := test.pool.QueryRow(
		test.ctx, `
		SELECT operations.version, operations.lease_id, manifests.version
		FROM commerce_gateway_operations AS operations
		JOIN commerce_manifests AS manifests ON manifests.id=operations.manifest_id
		WHERE operations.manifest_id=$1 AND operations.operation_type='catalog_publish'`,
		manifest.ManifestID,
	).Scan(&operationVersion, &leaseID, &manifestVersion); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, test.ctx, test.pool, `
		UPDATE commerce_manifests
		SET version=version+1, updated_at=clock_timestamp()
		WHERE id=$1 AND version=$2`, manifest.ManifestID, manifestVersion)
	close(gateway.release)
	select {
	case err := <-result:
		if !errors.Is(err, commerce.ErrProviderUnavailable) {
			t.Fatalf("ApproveAndPublishManifest() error = %v, want provider unavailable", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("catalog publisher did not return")
	}

	var operationState, manifestState string
	var storedOperationVersion int64
	var storedLeaseID uuid.UUID
	if err := test.pool.QueryRow(
		test.ctx, `
		SELECT operations.state, operations.version, operations.lease_id, manifests.state
		FROM commerce_gateway_operations AS operations
		JOIN commerce_manifests AS manifests ON manifests.id=operations.manifest_id
		WHERE operations.manifest_id=$1 AND operations.operation_type='catalog_publish'`,
		manifest.ManifestID,
	).Scan(&operationState, &storedOperationVersion, &storedLeaseID, &manifestState); err != nil {
		t.Fatal(err)
	}
	if operationState != "leased" || storedOperationVersion != operationVersion ||
		storedLeaseID != leaseID || manifestState != "publishing" {
		t.Fatalf(
			"stale failure state = operation:%s version:%d lease:%s manifest:%s",
			operationState,
			storedOperationVersion,
			storedLeaseID,
			manifestState,
		)
	}
}

func TestCommerceKillSwitchBlocksCheckoutButKeepsPortalAndEntitlements(t *testing.T) {
	test := newCommerceTest(t)
	readyGatewayAccount(t, test)
	gateway := newCatalogGateway()
	enabled := newGatewayIntegrationService(t, test, gateway)
	manifest := submitGatewayManifest(t, test, enabled, 1299)
	if _, err := enabled.ApproveAndPublishManifest(
		test.ctx,
		test.ownerA,
		commerce.ApproveManifestInput{ManifestID: manifest.ManifestID, Digest: manifest.Digest},
	); err != nil {
		t.Fatalf("ApproveAndPublishManifest() error = %v", err)
	}
	if _, err := enabled.CreateCheckout(
		test.ctx,
		"site-access-token",
		commerce.CheckoutRequest{
			OfferAlias:  "report",
			OperationID: uuid.MustParse("01991d2f-cd80-7901-9f96-b6b88ca60201"),
		},
	); err != nil {
		t.Fatalf("CreateCheckout(enabled) error = %v", err)
	}

	disabled := newGatewayIntegrationServiceWithFlag(t, test, gateway, false)
	providerCalls := gateway.callCount()
	if _, err := disabled.CreateCheckout(
		test.ctx,
		"site-access-token",
		commerce.CheckoutRequest{
			OfferAlias:  "report",
			OperationID: uuid.MustParse("01991d2f-cd80-7901-9f96-b6b88ca60202"),
		},
	); !errors.Is(err, commerce.ErrCatalogDisabled) {
		t.Fatalf("CreateCheckout(disabled) error = %v, want catalog disabled", err)
	}
	if gateway.callCount() != providerCalls {
		t.Fatal("disabled Checkout reached the provider")
	}
	portal, err := disabled.CreatePortal(
		test.ctx,
		"site-access-token",
		commerce.PortalRequest{
			OperationID: uuid.MustParse("01991d2f-cd80-7901-9f96-b6b88ca60203"),
		},
	)
	if err != nil || portal.URL == "" {
		t.Fatalf("CreatePortal(disabled) = %+v, %v", portal, err)
	}
	if gateway.portalCallCount() != 1 {
		t.Fatalf("Portal provider calls = %d, want 1", gateway.portalCallCount())
	}
	entitlements, err := disabled.VerifyEntitlements(test.ctx, "site-access-token")
	if err != nil || entitlements.Entitlements == nil || len(entitlements.Entitlements) != 0 {
		t.Fatalf("VerifyEntitlements(disabled) = %+v, %v", entitlements, err)
	}
}

func readyGatewayAccount(t *testing.T, test *commerceTest) {
	t.Helper()
	mustAcceptCommerceTerms(t, test, test.ownerA)
	if _, err := test.service.CreateAccountSession(
		test.ctx,
		test.ownerA,
		commerce.EnvironmentSandbox,
	); err != nil {
		t.Fatalf("CreateAccountSession() error = %v", err)
	}
	test.provider.setAllAccountsReady()
	dbtest.MustExec(t, test.ctx, test.pool, `
		UPDATE commerce_accounts
		SET status='ready', details_submitted=true, charges_enabled=true,
			payouts_enabled=true, country='GB', default_currency='gbp',
			card_payments_status='active', transfers_status='active',
			version=version+1, updated_at=clock_timestamp()
		WHERE company_id=$1 AND environment='sandbox'`, test.fixture.CompanyA)
}

func submitGatewayManifest(
	t *testing.T,
	test *commerceTest,
	service *gatewayIntegrationService,
	amount int64,
) commerce.ManifestSubmission {
	t.Helper()
	activateGatewayManifestRun(t, test)
	return submitGatewayManifestForRun(
		t,
		test,
		service,
		test.fixture.RunA,
		test.manifestRequestKey,
		amount,
	)
}

func submitGatewayManifestForRun(
	t *testing.T,
	test *commerceTest,
	service *gatewayIntegrationService,
	runID uuid.UUID,
	requestKey uuid.UUID,
	amount int64,
) commerce.ManifestSubmission {
	t.Helper()
	raw := gatewayManifestJSON(amount)
	result, err := service.SubmitManifest(
		test.ctx,
		test.ownerA,
		gatewayManifestContext(test, runID, requestKey),
		commerce.SubmitManifestInput{
			Environment: commerce.EnvironmentSandbox,
			RawManifest: raw,
		},
	)
	if err != nil {
		t.Fatalf("SubmitManifest(%d) error = %v", amount, err)
	}
	if result.State != "draft" || len(result.Digest) != sha256.Size*2 ||
		result.ApprovalID == uuid.Nil ||
		result.VerificationPath != "/.well-known/boardly-site-verification.json" {
		t.Fatalf("SubmitManifest(%d) = %#v", amount, result)
	}
	return result
}

func replaceGatewayManifestRun(t *testing.T, test *commerceTest) uuid.UUID {
	t.Helper()
	dbtest.MustExec(t, test.ctx, test.pool, `
		UPDATE runs
		SET status='failed', updated_at=clock_timestamp()
		WHERE id=$1 AND company_id=$2`, test.fixture.RunA, test.fixture.CompanyA)
	var replacementRunID uuid.UUID
	if err := test.pool.QueryRow(test.ctx, `
		INSERT INTO runs (
			company_id, agent_id, task_id, conversation_id, runtime_id,
			status, task_execution_generation, run_type, revision, context
		)
		SELECT company_id, agent_id, task_id, conversation_id, runtime_id,
			'running', task_execution_generation, run_type, revision, context
		FROM runs
		WHERE id=$1 AND company_id=$2
		RETURNING id`, test.fixture.RunA, test.fixture.CompanyA).Scan(&replacementRunID); err != nil {
		t.Fatalf("insert replacement Commerce manifest Run: %v", err)
	}
	return replacementRunID
}

func activateGatewayManifestRun(t *testing.T, test *commerceTest) {
	t.Helper()
	dbtest.MustExec(t, test.ctx, test.pool, `
		UPDATE tasks
		SET status='in_progress', updated_at=clock_timestamp(), version=version+1
		WHERE id=$1 AND company_id=$2`, test.fixture.TaskA, test.fixture.CompanyA)
	dbtest.MustExec(
		t, test.ctx, test.pool, `
		UPDATE runs
		SET status='running',
			execution_authorized_by_user_id=(
				SELECT assignee_user_id FROM tasks WHERE id=$2 AND company_id=$3
			),
			task_execution_generation=(
				SELECT execution_generation FROM tasks WHERE id=$2 AND company_id=$3
			), updated_at=clock_timestamp()
		WHERE id=$1 AND company_id=$3`,
		test.fixture.RunA,
		test.fixture.TaskA,
		test.fixture.CompanyA,
	)
}

func gatewayManifestContext(
	test *commerceTest,
	runID uuid.UUID,
	requestKey uuid.UUID,
) commerce.TrustedManifestContext {
	return commerce.TrustedManifestContext{
		CompanyID:      test.fixture.CompanyA,
		ConversationID: test.fixture.ConversationA,
		TaskID:         test.fixture.TaskA,
		AgentID:        test.fixture.AgentA,
		RunID:          runID,
		RequestKey:     requestKey,
	}
}

func gatewayManifestJSON(amount int64) []byte {
	return []byte(`{
		"schemaVersion":2,
		"site":{
			"alias":"task-store",
			"origin":"https://store.example",
			"portalReturnPath":"/account"
		},
		"offers":[{
			"alias":"report","displayName":"Premium report","type":"one_time",
			"entitlement":"report.access","amount":` + strconv.FormatInt(amount, 10) + `,
			"currency":"gbp","automaticTax":true,
			"successPath":"/billing/processing","cancelPath":"/pricing"
		}]
	}`)
}

func newGatewayIntegrationService(
	t *testing.T,
	test *commerceTest,
	gateway commerce.GatewayProvider,
) *gatewayIntegrationService {
	t.Helper()
	return newGatewayIntegrationServiceWithAuthority(t, test, gateway, &approvedSiteAuthority{})
}

func newGatewayIntegrationServiceWithAuthority(
	t *testing.T,
	test *commerceTest,
	gateway commerce.GatewayProvider,
	authority commerce.SiteAuthority,
) *gatewayIntegrationService {
	t.Helper()
	return newGatewayIntegrationServiceWithOptions(t, test, gateway, authority, true)
}

func newGatewayIntegrationServiceWithFlag(
	t *testing.T,
	test *commerceTest,
	gateway commerce.GatewayProvider,
	catalogCheckoutEnabled bool,
) *gatewayIntegrationService {
	t.Helper()
	return newGatewayIntegrationServiceWithOptions(
		t,
		test,
		gateway,
		&approvedSiteAuthority{},
		catalogCheckoutEnabled,
	)
}

func newGatewayIntegrationServiceWithOptions(
	t *testing.T,
	test *commerceTest,
	gateway commerce.GatewayProvider,
	authority commerce.SiteAuthority,
	catalogCheckoutEnabled bool,
) *gatewayIntegrationService {
	t.Helper()
	digest := sha256.Sum256([]byte("Boardly Commerce Addendum " + commerceTermsVersion))
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("events.NewBus() error = %v", err)
	}
	service, err := commerce.NewCommerceService(commerce.ServiceOptions{
		Store: test.store, Bus: bus, AllowedCountries: []string{"GB"},
		CatalogCheckoutEnabled: catalogCheckoutEnabled,
		SiteAuthority:          authority, CustomerHMACKey: make([]byte, 32),
		Terms: commerce.TermsDocument{
			Version: commerceTermsVersion, SHA256: digest,
			DocumentURL: "https://boardly.test/legal/commerce/" + commerceTermsVersion,
			PrivacyURL:  "https://boardly.test/legal/privacy",
		},
		Environments: map[commerce.Environment]commerce.EnvironmentOptions{
			commerce.EnvironmentSandbox: {
				Configured: true, PublishableKey: "pk_test_boardlycommerce123",
				ReturnURL:  "https://boardly.test/connect/stripe/return",
				RefreshURL: "https://boardly.test/connect/stripe/refresh",
				Provider:   test.provider, Gateway: gateway,
			},
		},
		Now: func() time.Time { return test.now },
	})
	if err != nil {
		t.Fatalf("commerce.NewCommerceService(gateway) error = %v", err)
	}
	return &gatewayIntegrationService{DefaultCommerceService: service, test: test, bus: bus}
}

type gatewayIntegrationService struct {
	*commerce.DefaultCommerceService
	test *commerceTest
	bus  *events.Bus
}

func (service *gatewayIntegrationService) CreateCheckout(
	ctx context.Context,
	_ string,
	input commerce.CheckoutRequest,
) (commerce.HostedRedirect, error) {
	request := service.signedRequest(
		"checkout",
		input.OperationID,
		"/api/v1/site-commerce/checkout-sessions",
		true,
	)
	return service.DefaultCommerceService.CreateCheckout(ctx, request, input)
}

func (service *gatewayIntegrationService) CreatePortal(
	ctx context.Context,
	_ string,
	input commerce.PortalRequest,
) (commerce.HostedRedirect, error) {
	request := service.signedRequest(
		"portal",
		input.OperationID,
		"/api/v1/site-commerce/portal-sessions",
		true,
	)
	return service.DefaultCommerceService.CreatePortal(ctx, request, input)
}

func (service *gatewayIntegrationService) VerifyEntitlements(
	ctx context.Context,
	_ string,
) (commerce.EntitlementVerification, error) {
	request := service.signedRequest(
		"entitlements",
		dbtest.NewUUID(service.test.t),
		"/api/v1/site-commerce/entitlements/verify",
		false,
	)
	return service.DefaultCommerceService.VerifyEntitlements(ctx, request)
}

func (service *gatewayIntegrationService) signedRequest(
	action string,
	operationID uuid.UUID,
	path string,
	requireNonce bool,
) commerce.SiteSignedRequest {
	return service.signedRequestAt(
		action,
		operationID,
		path,
		requireNonce,
		service.test.now.UTC(),
	)
}

func (service *gatewayIntegrationService) signedRequestAt(
	action string,
	operationID uuid.UUID,
	path string,
	requireNonce bool,
	authenticatedAt time.Time,
) commerce.SiteSignedRequest {
	return service.signedRequestForSubjectAt(
		action,
		operationID,
		path,
		requireNonce,
		"customer-subject",
		authenticatedAt,
	)
}

func (service *gatewayIntegrationService) signedRequestForSubjectAt(
	action string,
	operationID uuid.UUID,
	path string,
	requireNonce bool,
	subject string,
	authenticatedAt time.Time,
) commerce.SiteSignedRequest {
	service.test.t.Helper()
	var siteID uuid.UUID
	var environment, origin string
	if err := service.test.pool.QueryRow(
		service.test.ctx, `
		SELECT id, environment, origin
		FROM commerce_sites
		WHERE company_id=$1 AND site_alias='task-store'`,
		service.test.fixture.CompanyA,
	).Scan(&siteID, &environment, &origin); err != nil {
		service.test.t.Fatalf("load Site signing authority: %v", err)
	}
	nonce := ""
	if requireNonce {
		challenge, err := service.CreateSiteChallenge(
			service.test.ctx,
			commerce.SiteChallengeRequest{
				SiteID: siteID, Action: commerce.SiteChallengeAction(action),
				OperationID: operationID, ClientAddress: "198.51.100.42",
			},
		)
		if err == nil {
			nonce = challenge.Nonce
		} else {
			nonce = strings.Repeat("A", 43)
		}
	}
	bodyDigest := sha256.Sum256([]byte("{}"))
	now := service.test.now.UTC()
	claims := jwt.MapClaims{
		"schemaVersion":         1,
		"iss":                   siteID.String(),
		"aud":                   []string{"boardly-site-commerce"},
		"sub":                   subject,
		"iat":                   now.Unix(),
		"nbf":                   now.Unix(),
		"exp":                   now.Add(30 * time.Second).Unix(),
		"jti":                   dbtest.NewUUID(service.test.t).String(),
		"environment":           environment,
		"origin":                origin,
		"sessionId":             "site-session-1",
		"authenticatedAt":       authenticatedAt.UTC().Format(time.RFC3339),
		"authenticationMethods": []string{"password"},
		"action":                action,
		"method":                http.MethodPost,
		"path":                  path,
		"operationId":           operationID.String(),
		"bodySha256":            hex.EncodeToString(bodyDigest[:]),
	}
	if requireNonce {
		claims["nonce"] = nonce
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	jwk := testSitePublicJWK()
	token.Header["typ"] = "boardly-site-request+jwt"
	token.Header["kid"] = jwk.KeyID
	signed, err := token.SignedString(testSitePrivateKey())
	if err != nil {
		service.test.t.Fatalf("sign Site request: %v", err)
	}
	return commerce.SiteSignedRequest{
		Token: signed, Method: http.MethodPost, Path: path, BodySHA256: bodyDigest,
	}
}

type approvedSiteAuthority struct{}

type callbackSiteAuthority struct {
	verify func(commerce.SiteVerificationInput) (commerce.VerifiedSite, error)
}

func (authority *callbackSiteAuthority) VerifySite(
	_ context.Context,
	input commerce.SiteVerificationInput,
) (commerce.VerifiedSite, error) {
	return authority.verify(input)
}

func (*approvedSiteAuthority) VerifySite(
	_ context.Context,
	input commerce.SiteVerificationInput,
) (commerce.VerifiedSite, error) {
	if input.Origin != "https://store.example" || input.SiteAlias != "task-store" ||
		len(input.ManifestDigest) != sha256.Size*2 {
		return commerce.VerifiedSite{}, errors.New("unexpected Site authority")
	}
	return commerce.VerifiedSite{
		SigningKeys: []commerce.SitePublicJWK{testSitePublicJWK()},
	}, nil
}

func testSitePublicJWK() commerce.SitePublicJWK {
	encoded, err := testSitePrivateKey().PublicKey.Bytes()
	if err != nil {
		panic(err)
	}
	coordinateBytes := (len(encoded) - 1) / 2
	jwk, err := commerce.NewSitePublicJWK(commerce.SitePublicKey{
		KeyType: "EC",
		Curve:   "P-256",
		X:       base64.RawURLEncoding.EncodeToString(encoded[1 : 1+coordinateBytes]),
		Y:       base64.RawURLEncoding.EncodeToString(encoded[1+coordinateBytes:]),
	})
	if err != nil {
		panic(err)
	}
	return jwk
}

func testSitePrivateKey() *ecdsa.PrivateKey {
	raw := make([]byte, 32)
	raw[len(raw)-1] = 1
	key, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), raw)
	if err != nil {
		panic(err)
	}
	return key
}

type catalogGateway struct {
	mu                    sync.Mutex
	calls                 int
	checkoutCalls         int
	checkoutRecoveryCalls int
	portalCalls           int
	providerCustomerID    string
	checkoutSessions      map[string]commerce.GatewayHostedSession
}

type failingCatalogGateway struct {
	*catalogGateway
}

type blockingFailingCatalogGateway struct {
	*catalogGateway
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type blockingCatalogGateway struct {
	*catalogGateway
	started     chan struct{}
	release     chan struct{}
	startedOnce sync.Once
	releaseOnce sync.Once
}

func newBlockingCatalogGateway() *blockingCatalogGateway {
	return &blockingCatalogGateway{
		catalogGateway: newCatalogGateway(),
		started:        make(chan struct{}),
		release:        make(chan struct{}),
	}
}

func (gateway *blockingCatalogGateway) CreateProduct(
	ctx context.Context,
	input commerce.CatalogProductInput,
) (commerce.CatalogProduct, error) {
	gateway.startedOnce.Do(func() { close(gateway.started) })
	select {
	case <-gateway.release:
	case <-ctx.Done():
		return commerce.CatalogProduct{}, ctx.Err()
	}
	return gateway.catalogGateway.CreateProduct(ctx, input)
}

func newBlockingFailingCatalogGateway() *blockingFailingCatalogGateway {
	return &blockingFailingCatalogGateway{
		catalogGateway: newCatalogGateway(),
		started:        make(chan struct{}),
		release:        make(chan struct{}),
	}
}

func (gateway *blockingFailingCatalogGateway) CreateProduct(
	_ context.Context,
	_ commerce.CatalogProductInput,
) (commerce.CatalogProduct, error) {
	gateway.recordCall()
	gateway.once.Do(func() { close(gateway.started) })
	<-gateway.release
	return commerce.CatalogProduct{}, errors.New("ambiguous provider response")
}

func (gateway *failingCatalogGateway) CreateProduct(
	_ context.Context,
	_ commerce.CatalogProductInput,
) (commerce.CatalogProduct, error) {
	gateway.recordCall()
	return commerce.CatalogProduct{}, errors.New("ambiguous provider response")
}

func newCatalogGateway() *catalogGateway {
	return &catalogGateway{checkoutSessions: make(map[string]commerce.GatewayHostedSession)}
}

func (gateway *catalogGateway) CreateProduct(
	_ context.Context,
	input commerce.CatalogProductInput,
) (commerce.CatalogProduct, error) {
	gateway.recordCall()
	return commerce.CatalogProduct{ID: providerResourceID("prod_", input.IdempotencyKey)}, nil
}

func (gateway *catalogGateway) CreatePrice(
	_ context.Context,
	input commerce.CatalogPriceInput,
) (commerce.CatalogPrice, error) {
	gateway.recordCall()
	return commerce.CatalogPrice{ID: providerResourceID("price_", input.IdempotencyKey)}, nil
}

func (gateway *catalogGateway) CreatePortalConfiguration(
	_ context.Context,
	input commerce.CatalogPortalInput,
) (commerce.CatalogPortal, error) {
	gateway.recordCall()
	return commerce.CatalogPortal{ID: providerResourceID("bpc_", input.IdempotencyKey)}, nil
}

func (gateway *catalogGateway) CreateCustomer(
	_ context.Context,
	input commerce.GatewayCustomerInput,
) (commerce.GatewayCustomer, error) {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	gateway.calls++
	gateway.providerCustomerID = providerResourceID("cus_", input.IdempotencyKey)
	return commerce.GatewayCustomer{ID: gateway.providerCustomerID}, nil
}

func (gateway *catalogGateway) CreateCheckoutSession(
	_ context.Context,
	input commerce.GatewayCheckoutInput,
) (commerce.GatewayHostedSession, error) {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	gateway.calls++
	gateway.checkoutCalls++
	id := providerResourceID("cs_", input.IdempotencyKey)
	session := commerce.GatewayHostedSession{
		ID: id, URL: "https://checkout.stripe.com/c/pay/" + id,
	}
	gateway.checkoutSessions[id] = session
	return session, nil
}

func (gateway *catalogGateway) CreatePortalSession(
	_ context.Context,
	input commerce.GatewayPortalInput,
) (commerce.GatewayHostedSession, error) {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if input.CustomerID != gateway.providerCustomerID {
		return commerce.GatewayHostedSession{}, errors.New("customer mismatch")
	}
	gateway.calls++
	gateway.portalCalls++
	id := providerResourceID("bps_", input.IdempotencyKey)
	return commerce.GatewayHostedSession{
		ID: id, URL: "https://billing.stripe.com/p/session/" + id,
	}, nil
}

func (gateway *catalogGateway) RetrieveCheckoutSession(
	_ context.Context,
	input commerce.GatewayCheckoutRecoveryInput,
) (commerce.GatewayHostedSession, error) {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	gateway.calls++
	gateway.checkoutRecoveryCalls++
	if input.CustomerID != gateway.providerCustomerID {
		return commerce.GatewayHostedSession{}, errors.New("customer mismatch")
	}
	session, ok := gateway.checkoutSessions[input.SessionID]
	if !ok {
		return commerce.GatewayHostedSession{}, errors.New("session not found")
	}
	return session, nil
}

func (gateway *catalogGateway) recordCall() {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	gateway.calls++
}

func (gateway *catalogGateway) callCount() int {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	return gateway.calls
}

func (gateway *catalogGateway) checkoutCallCounts() (int, int) {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	return gateway.checkoutCalls, gateway.checkoutRecoveryCalls
}

func (gateway *catalogGateway) customerID() string {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	return gateway.providerCustomerID
}

func (gateway *catalogGateway) portalCallCount() int {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	return gateway.portalCalls
}

func providerResourceID(prefix, idempotencyKey string) string {
	digest := sha256.Sum256([]byte(idempotencyKey))
	return prefix + hex.EncodeToString(digest[:8])
}

func gatewayOperationDigestForTest(
	operationType string,
	siteID uuid.UUID,
	customerID uuid.UUID,
	offerID uuid.UUID,
) []byte {
	digest := sha256.New()
	_, _ = digest.Write([]byte("boardly-commerce-gateway-v1\x00"))
	_, _ = digest.Write([]byte(operationType))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(siteID[:])
	_, _ = digest.Write(customerID[:])
	_, _ = digest.Write(offerID[:])
	return digest.Sum(nil)
}
