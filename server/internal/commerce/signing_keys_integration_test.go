//go:build integration

package commerce_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/commerce"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

func TestRunScopedLocalSigningKeyLifecycleRotatesActiveSiteAndAllowsExactReplay(
	t *testing.T,
) {
	test := newCommerceTest(t)
	readyGatewayAccount(t, test)
	service := newGatewayIntegrationService(t, test, newCatalogGateway())
	activateGatewayManifestRun(t, test)
	raw := []byte(strings.Replace(
		string(gatewayManifestJSON(1299)),
		"https://store.example",
		"http://localhost:3000",
		1,
	))
	manifest, err := service.SubmitManifest(
		test.ctx,
		test.ownerA,
		gatewayManifestContext(test, test.fixture.RunA, test.manifestRequestKey),
		commerce.SubmitManifestInput{
			Environment: commerce.EnvironmentSandbox,
			RawManifest: raw,
		},
	)
	if err != nil {
		t.Fatalf("SubmitManifest() error = %v", err)
	}
	first := sitePublicJWKForScalar(t, 1)
	firstRegistration, err := service.RegisterLocalSigningKey(
		test.ctx,
		test.memberA,
		localSigningKeyContext(test),
		localSigningKeyRequest(test, first),
	)
	if err != nil {
		t.Fatalf("RegisterLocalSigningKey(draft member) error = %v", err)
	}
	if firstRegistration.State != commerce.SigningKeyStatePending {
		t.Fatalf("draft key state = %q, want pending", firstRegistration.State)
	}
	if _, err := service.ApproveAndPublishManifest(
		test.ctx,
		test.ownerA,
		commerce.ApproveManifestInput{ManifestID: manifest.ManifestID, Digest: manifest.Digest},
	); err != nil {
		t.Fatalf("ApproveAndPublishManifest() error = %v", err)
	}

	second := sitePublicJWKForScalar(t, 2)
	secondRegistration, err := service.RegisterLocalSigningKey(
		test.ctx,
		test.memberA,
		localSigningKeyContext(test),
		localSigningKeyRequest(test, second),
	)
	if err != nil || secondRegistration.KeyID != second.KeyID ||
		secondRegistration.State != commerce.SigningKeyStateActive {
		t.Fatalf("RegisterLocalSigningKey(active rotation) = %#v, %v", secondRegistration, err)
	}
	replay, err := service.RegisterLocalSigningKey(
		test.ctx,
		test.memberA,
		localSigningKeyContext(test),
		localSigningKeyRequest(test, second),
	)
	if err != nil || replay.KeyID != second.KeyID || replay.State != commerce.SigningKeyStateActive {
		t.Fatalf("RegisterLocalSigningKey(replay) = %#v, %v", replay, err)
	}
}

func TestLocalSigningKeyRegistrationRequiresTaskScope(t *testing.T) {
	test := newCommerceTest(t)
	service := newGatewayIntegrationService(t, test, newCatalogGateway())
	trusted := localSigningKeyContext(test)
	trusted.TaskID = uuid.Nil
	if _, err := service.RegisterLocalSigningKey(
		test.ctx,
		test.ownerA,
		trusted,
		localSigningKeyRequest(test, sitePublicJWKForScalar(t, 9)),
	); !errors.Is(err, commerce.ErrInvalidInput) {
		t.Fatalf("RegisterLocalSigningKey(unscoped) error = %v, want invalid input", err)
	}
}

func TestLocalSigningKeyRegistrationRequiresExactManifestTaskScope(t *testing.T) {
	test := newCommerceTest(t)
	readyGatewayAccount(t, test)
	service := newGatewayIntegrationService(t, test, newCatalogGateway())
	activateGatewayManifestRun(t, test)
	raw := []byte(strings.Replace(
		string(gatewayManifestJSON(1299)),
		"https://store.example",
		"http://localhost:3000",
		1,
	))
	if _, err := service.SubmitManifest(
		test.ctx,
		test.ownerA,
		gatewayManifestContext(test, test.fixture.RunA, test.manifestRequestKey),
		commerce.SubmitManifestInput{
			Environment: commerce.EnvironmentSandbox,
			RawManifest: raw,
		},
	); err != nil {
		t.Fatalf("SubmitManifest() error = %v", err)
	}

	trusted := localSigningKeyContext(test)
	trusted.TaskID = uuid.Must(uuid.NewV7())
	if _, err := service.RegisterLocalSigningKey(
		test.ctx,
		test.ownerA,
		trusted,
		localSigningKeyRequest(test, sitePublicJWKForScalar(t, 8)),
	); !errors.Is(err, commerce.ErrSiteNotFound) {
		t.Fatalf("RegisterLocalSigningKey(wrong Task) error = %v, want Site not found", err)
	}

	trusted.TaskID = test.fixture.TaskA
	registration, err := service.RegisterLocalSigningKey(
		test.ctx,
		test.ownerA,
		trusted,
		localSigningKeyRequest(test, sitePublicJWKForScalar(t, 8)),
	)
	if err != nil || registration.State != commerce.SigningKeyStatePending {
		t.Fatalf("RegisterLocalSigningKey(exact scope) = %#v, %v", registration, err)
	}
}

func TestLocalSigningKeyRegistrationRecoversFailedManifest(t *testing.T) {
	test := newCommerceTest(t)
	readyGatewayAccount(t, test)
	gateway := newCatalogGateway()
	service := newGatewayIntegrationService(t, test, gateway)
	activateGatewayManifestRun(t, test)
	raw := []byte(strings.Replace(
		string(gatewayManifestJSON(1299)),
		"https://store.example",
		"http://localhost:3000",
		1,
	))
	manifest, err := service.SubmitManifest(
		test.ctx,
		test.ownerA,
		gatewayManifestContext(test, test.fixture.RunA, test.manifestRequestKey),
		commerce.SubmitManifestInput{
			Environment: commerce.EnvironmentSandbox,
			RawManifest: raw,
		},
	)
	if err != nil {
		t.Fatalf("SubmitManifest() error = %v", err)
	}
	if _, err := service.ApproveAndPublishManifest(
		test.ctx,
		test.ownerA,
		commerce.ApproveManifestInput{
			ManifestID: manifest.ManifestID,
			Digest:     manifest.Digest,
		},
	); !errors.Is(err, commerce.ErrSiteSigningKeyRequired) {
		t.Fatalf("ApproveAndPublishManifest(without key) error = %v, want signing key required", err)
	}
	var manifestState string
	if err := test.pool.QueryRow(
		test.ctx,
		`SELECT state FROM commerce_manifests WHERE id=$1`,
		manifest.ManifestID,
	).Scan(&manifestState); err != nil {
		t.Fatalf("read manifest after missing-key publish: %v", err)
	}
	if manifestState != "draft" {
		t.Fatalf("manifest state after missing-key publish = %q, want draft", manifestState)
	}
	if gateway.callCount() != 0 {
		t.Fatalf("missing-key publish made %d provider calls, want 0", gateway.callCount())
	}
	var offerCount, operationCount int
	if err := test.pool.QueryRow(test.ctx, `
		SELECT
			(SELECT count(*) FROM commerce_offers WHERE manifest_id=$1),
			(SELECT count(*) FROM commerce_gateway_operations WHERE manifest_id=$1)`,
		manifest.ManifestID,
	).Scan(&offerCount, &operationCount); err != nil {
		t.Fatalf("read missing-key publish effects: %v", err)
	}
	if offerCount != 0 || operationCount != 0 {
		t.Fatalf("missing-key publish effects = offers:%d operations:%d, want 0/0", offerCount, operationCount)
	}

	dbtest.MustExec(t, test.ctx, test.pool, `
		UPDATE commerce_manifests
		SET state='apply_failed', apply_error_code='site_verification_failed',
			approved_by_user_id=$2, approval_session_id=$3,
			approved_at=clock_timestamp(),
			updated_at=clock_timestamp(), version=version+1
		WHERE id=$1`, manifest.ManifestID, test.ownerA.UserID, test.ownerA.SessionID)
	dbtest.MustExec(t, test.ctx, test.pool, `
		INSERT INTO commerce_offers (
			company_id, site_id, manifest_id, environment, alias, display_name,
			offer_type, entitlement_key, amount, currency, automatic_tax,
			success_path, cancel_path
		)
		SELECT manifests.company_id, manifests.site_id, manifests.id, sites.environment,
			'report', 'Premium report',
			'one_time', 'report.access', 1299, 'gbp', true,
			'/billing/processing', '/pricing'
		FROM commerce_manifests AS manifests
		JOIN commerce_sites AS sites ON sites.id=manifests.site_id
		WHERE manifests.id=$1`, manifest.ManifestID)
	dbtest.MustExec(t, test.ctx, test.pool, `
		INSERT INTO commerce_gateway_operations (
			company_id, site_id, manifest_id, environment, operation_type,
			operation_id, request_sha256, idempotency_key, state, attempt_count,
			provider_attempt_count, last_error_code, version
		)
		SELECT manifests.company_id, manifests.site_id, manifests.id, sites.environment,
			'catalog_publish', manifests.id, manifests.digest,
			'boardly:catalog-publish:v1:' || manifests.id::text,
			'pending', 6, 0, 'site_verification_failed', 13
		FROM commerce_manifests AS manifests
		JOIN commerce_sites AS sites ON sites.id=manifests.site_id
		WHERE manifests.id=$1`, manifest.ManifestID)
	var legacyOperationID uuid.UUID
	if err := test.pool.QueryRow(test.ctx, `
		SELECT id
		FROM commerce_gateway_operations
		WHERE manifest_id=$1 AND operation_type='catalog_publish'`,
		manifest.ManifestID,
	).Scan(&legacyOperationID); err != nil {
		t.Fatalf("read legacy catalog operation: %v", err)
	}

	registration, err := service.RegisterLocalSigningKey(
		test.ctx,
		test.memberA,
		localSigningKeyContext(test),
		localSigningKeyRequest(test, sitePublicJWKForScalar(t, 3)),
	)
	if err != nil {
		t.Fatalf("RegisterLocalSigningKey(after publish failure) error = %v", err)
	}
	if registration.State != commerce.SigningKeyStatePending {
		t.Fatalf("recovery key state = %q, want pending", registration.State)
	}
	status, err := service.ApproveAndPublishManifest(
		test.ctx,
		test.ownerA,
		commerce.ApproveManifestInput{
			ManifestID: manifest.ManifestID,
			Digest:     manifest.Digest,
		},
	)
	if err != nil || status.State != "active" {
		t.Fatalf("ApproveAndPublishManifest(recovered) = %#v, %v", status, err)
	}
	if gateway.callCount() != 3 {
		t.Fatalf("recovered catalog provider calls = %d, want 3", gateway.callCount())
	}
	var operationID uuid.UUID
	var operationState, publishedManifestState, siteState, signingKeyState string
	var attempts, providerAttempts int32
	var operationErrorCleared, manifestErrorCleared bool
	if err := test.pool.QueryRow(test.ctx, `
		SELECT operations.id, operations.state, operations.attempt_count,
			operations.provider_attempt_count, operations.last_error_code IS NULL,
			manifests.state, manifests.apply_error_code IS NULL,
			sites.status, signing_keys.state
		FROM commerce_gateway_operations AS operations
		JOIN commerce_manifests AS manifests ON manifests.id=operations.manifest_id
		JOIN commerce_sites AS sites ON sites.id=manifests.site_id
		JOIN commerce_site_signing_keys AS signing_keys ON signing_keys.site_id=sites.id
		WHERE operations.manifest_id=$1
		  AND operations.operation_type='catalog_publish'`,
		manifest.ManifestID,
	).Scan(
		&operationID,
		&operationState,
		&attempts,
		&providerAttempts,
		&operationErrorCleared,
		&publishedManifestState,
		&manifestErrorCleared,
		&siteState,
		&signingKeyState,
	); err != nil {
		t.Fatalf("read recovered catalog state: %v", err)
	}
	if operationID != legacyOperationID || operationState != "succeeded" || attempts != 7 ||
		providerAttempts != 1 || !operationErrorCleared || publishedManifestState != "active" ||
		!manifestErrorCleared || siteState != "active" || signingKeyState != "active" {
		t.Fatalf(
			"recovered catalog state = operation:%s/%s attempts:%d/%d errors:%t/%t manifest:%s site:%s key:%s",
			operationID,
			operationState,
			attempts,
			providerAttempts,
			operationErrorCleared,
			manifestErrorCleared,
			publishedManifestState,
			siteState,
			signingKeyState,
		)
	}
	if err := test.pool.QueryRow(test.ctx, `
		SELECT count(*) FROM commerce_gateway_operations WHERE manifest_id=$1`,
		manifest.ManifestID,
	).Scan(&operationCount); err != nil {
		t.Fatalf("count recovered catalog operations: %v", err)
	}
	if operationCount != 1 {
		t.Fatalf("recovered catalog operation count = %d, want 1", operationCount)
	}
}

func TestCommerceSiteChallengeIsConsumedExactlyOnceConcurrently(t *testing.T) {
	test := newCommerceTest(t)
	readyGatewayAccount(t, test)
	service := newGatewayIntegrationService(t, test, newCatalogGateway())
	publishGatewayManifest(t, test, service)
	operationID := dbtest.NewUUID(t)
	request := service.signedRequest(
		"checkout",
		operationID,
		"/api/v1/site-commerce/checkout-sessions",
		true,
	)
	input := commerce.CheckoutRequest{OfferAlias: "report", OperationID: operationID}
	start := make(chan struct{})
	results := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for range 2 {
		go func() {
			ready.Done()
			<-start
			_, err := service.DefaultCommerceService.CreateCheckout(test.ctx, request, input)
			results <- err
		}()
	}
	ready.Wait()
	close(start)
	var succeeded, rejected int
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, commerce.ErrUnauthenticated):
			rejected++
		default:
			t.Fatalf("concurrent Checkout error = %v", err)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("concurrent challenge results = success:%d rejected:%d", succeeded, rejected)
	}
}

func TestCommercePortalRejectsStaleSiteAuthentication(t *testing.T) {
	test := newCommerceTest(t)
	readyGatewayAccount(t, test)
	service := newGatewayIntegrationService(t, test, newCatalogGateway())
	publishGatewayManifest(t, test, service)
	operationID := dbtest.NewUUID(t)
	request := service.signedRequestAt(
		"portal",
		operationID,
		"/api/v1/site-commerce/portal-sessions",
		true,
		test.now.Add(-5*time.Minute-time.Second),
	)
	_, err := service.DefaultCommerceService.CreatePortal(
		test.ctx,
		request,
		commerce.PortalRequest{OperationID: operationID},
	)
	if !errors.Is(err, commerce.ErrUnauthenticated) {
		t.Fatalf("CreatePortal(stale authentication) error = %v, want unauthenticated", err)
	}
}

func TestSiteChallengeRateLimitProtectsSiteBucketAndPrunesStaleScopes(t *testing.T) {
	test := newCommerceTest(t)
	readyGatewayAccount(t, test)
	service := newGatewayIntegrationService(t, test, newCatalogGateway())
	manifest := publishGatewayManifest(t, test, service)
	staleScope := make([]byte, 32)
	staleScope[0] = 7
	dbtest.MustExec(t, test.ctx, test.pool, `
		INSERT INTO commerce_rate_limits (
			company_id, site_id, subject_hmac, action, window_started_at,
			request_count, created_at, updated_at
		) VALUES ($1, $2, $3, 'challenge_ip',
			clock_timestamp() - interval '10 minutes', 1,
			clock_timestamp() - interval '10 minutes',
			clock_timestamp() - interval '10 minutes')`,
		test.fixture.CompanyA,
		manifest.SiteID,
		staleScope,
	)
	for range 60 {
		if _, err := service.CreateSiteChallenge(test.ctx, commerce.SiteChallengeRequest{
			SiteID: manifest.SiteID, Action: commerce.SiteChallengeActionCheckout,
			OperationID: dbtest.NewUUID(t), ClientAddress: "203.0.113.42",
		}); err != nil {
			t.Fatalf("CreateSiteChallenge() error = %v", err)
		}
	}
	if _, err := service.CreateSiteChallenge(test.ctx, commerce.SiteChallengeRequest{
		SiteID: manifest.SiteID, Action: commerce.SiteChallengeActionCheckout,
		OperationID: dbtest.NewUUID(t), ClientAddress: "203.0.113.42",
	}); !errors.Is(err, commerce.ErrRateLimited) {
		t.Fatalf("CreateSiteChallenge(over IP limit) error = %v, want rate limited", err)
	}
	var siteCount int
	if err := test.pool.QueryRow(test.ctx, `
		SELECT request_count
		FROM commerce_rate_limits
		WHERE site_id=$1 AND action='challenge_site'`,
		manifest.SiteID,
	).Scan(&siteCount); err != nil {
		t.Fatalf("read Site challenge rate limit: %v", err)
	}
	if siteCount != 60 {
		t.Fatalf("Site challenge rate count = %d, want 60", siteCount)
	}
	dbtest.MustExec(t, test.ctx, test.pool, `
		UPDATE commerce_rate_limits
		SET request_count=300, window_started_at=clock_timestamp(),
			updated_at=clock_timestamp()
		WHERE site_id=$1 AND action='challenge_site'`, manifest.SiteID)
	var rowsBefore int
	if err := test.pool.QueryRow(test.ctx, `
		SELECT count(*) FROM commerce_rate_limits WHERE site_id=$1`,
		manifest.SiteID,
	).Scan(&rowsBefore); err != nil {
		t.Fatalf("count rate limits before saturated Site request: %v", err)
	}
	if _, err := service.CreateSiteChallenge(test.ctx, commerce.SiteChallengeRequest{
		SiteID: manifest.SiteID, Action: commerce.SiteChallengeActionCheckout,
		OperationID: dbtest.NewUUID(t), ClientAddress: "203.0.113.99",
	}); !errors.Is(err, commerce.ErrRateLimited) {
		t.Fatalf("CreateSiteChallenge(saturated Site) error = %v, want rate limited", err)
	}
	var rowsAfter int
	if err := test.pool.QueryRow(test.ctx, `
		SELECT count(*) FROM commerce_rate_limits WHERE site_id=$1`,
		manifest.SiteID,
	).Scan(&rowsAfter); err != nil {
		t.Fatalf("count rate limits after saturated Site request: %v", err)
	}
	if rowsAfter != rowsBefore {
		t.Fatalf("saturated Site persisted a new IP scope: rows %d -> %d", rowsBefore, rowsAfter)
	}
	var staleRows int
	if err := test.pool.QueryRow(test.ctx, `
		SELECT count(*)
		FROM commerce_rate_limits
		WHERE site_id=$1 AND subject_hmac=$2`,
		manifest.SiteID,
		staleScope,
	).Scan(&staleRows); err != nil {
		t.Fatalf("count stale Commerce rate limits: %v", err)
	}
	if staleRows != 0 {
		t.Fatalf("stale Commerce rate-limit rows = %d, want 0", staleRows)
	}
}

func TestGatewayRateLimitRollsBackNewCustomerScopeWhenSiteIsSaturated(t *testing.T) {
	test := newCommerceTest(t)
	readyGatewayAccount(t, test)
	service := newGatewayIntegrationService(t, test, newCatalogGateway())
	manifest := publishGatewayManifest(t, test, service)
	firstOperationID := dbtest.NewUUID(t)
	firstRequest := service.signedRequest(
		"checkout",
		firstOperationID,
		"/api/v1/site-commerce/checkout-sessions",
		true,
	)
	if _, err := service.DefaultCommerceService.CreateCheckout(
		test.ctx,
		firstRequest,
		commerce.CheckoutRequest{OfferAlias: "report", OperationID: firstOperationID},
	); err != nil {
		t.Fatalf("CreateCheckout(first customer) error = %v", err)
	}
	dbtest.MustExec(t, test.ctx, test.pool, `
		UPDATE commerce_rate_limits
		SET request_count=1000, window_started_at=clock_timestamp(),
			updated_at=clock_timestamp()
		WHERE site_id=$1 AND action='site_checkout'`, manifest.SiteID)
	var customerScopesBefore int
	if err := test.pool.QueryRow(test.ctx, `
		SELECT count(*)
		FROM commerce_rate_limits
		WHERE site_id=$1 AND action='checkout'`,
		manifest.SiteID,
	).Scan(&customerScopesBefore); err != nil {
		t.Fatalf("count customer scopes before saturated request: %v", err)
	}
	secondOperationID := dbtest.NewUUID(t)
	secondRequest := service.signedRequestForSubjectAt(
		"checkout",
		secondOperationID,
		"/api/v1/site-commerce/checkout-sessions",
		true,
		"second-customer",
		test.now,
	)
	if _, err := service.DefaultCommerceService.CreateCheckout(
		test.ctx,
		secondRequest,
		commerce.CheckoutRequest{OfferAlias: "report", OperationID: secondOperationID},
	); !errors.Is(err, commerce.ErrRateLimited) {
		t.Fatalf("CreateCheckout(saturated Site) error = %v, want rate limited", err)
	}
	var customerScopesAfter int
	if err := test.pool.QueryRow(test.ctx, `
		SELECT count(*)
		FROM commerce_rate_limits
		WHERE site_id=$1 AND action='checkout'`,
		manifest.SiteID,
	).Scan(&customerScopesAfter); err != nil {
		t.Fatalf("count customer scopes after saturated request: %v", err)
	}
	if customerScopesAfter != customerScopesBefore {
		t.Fatalf(
			"saturated Site persisted a new customer scope: %d -> %d",
			customerScopesBefore,
			customerScopesAfter,
		)
	}
}

func publishGatewayManifest(
	t *testing.T,
	test *commerceTest,
	service *gatewayIntegrationService,
) commerce.ManifestSubmission {
	t.Helper()
	manifest := submitGatewayManifest(t, test, service, 1299)
	if _, err := service.ApproveAndPublishManifest(
		test.ctx,
		test.ownerA,
		commerce.ApproveManifestInput{ManifestID: manifest.ManifestID, Digest: manifest.Digest},
	); err != nil {
		t.Fatalf("ApproveAndPublishManifest() error = %v", err)
	}
	return manifest
}

func localSigningKeyRequest(
	_ *commerceTest,
	jwk commerce.SitePublicJWK,
) commerce.LocalSigningKeyRequest {
	return commerce.LocalSigningKeyRequest{
		SiteAlias: "task-store",
		Origin:    "http://localhost:3000",
		PublicKey: commerce.SitePublicKey{
			KeyType: jwk.KeyType,
			Curve:   jwk.Curve,
			X:       jwk.X,
			Y:       jwk.Y,
		},
		Fingerprint: jwk.KeyID,
	}
}

func localSigningKeyContext(test *commerceTest) commerce.TrustedLocalSigningKeyContext {
	return commerce.TrustedLocalSigningKeyContext{
		CompanyID: test.fixture.CompanyA,
		TaskID:    test.fixture.TaskA,
		RunID:     test.fixture.RunA,
	}
}

func sitePublicJWKForScalar(t *testing.T, scalar byte) commerce.SitePublicJWK {
	t.Helper()
	raw := make([]byte, 32)
	raw[len(raw)-1] = scalar
	key, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), raw)
	if err != nil {
		t.Fatalf("ParseRawPrivateKey() error = %v", err)
	}
	encoded, err := key.PublicKey.Bytes()
	if err != nil {
		t.Fatalf("PublicKey.Bytes() error = %v", err)
	}
	jwk, err := commerce.NewSitePublicJWK(commerce.SitePublicKey{
		KeyType: "EC",
		Curve:   "P-256",
		X:       base64.RawURLEncoding.EncodeToString(encoded[1:33]),
		Y:       base64.RawURLEncoding.EncodeToString(encoded[33:]),
	})
	if err != nil {
		t.Fatalf("NewSitePublicJWK() error = %v", err)
	}
	return jwk
}
