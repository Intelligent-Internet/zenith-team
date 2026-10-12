//go:build integration

package commerce_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/commerce"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

const commerceTermsVersion = "2026-08-20"

func TestCommerceTermsCatalogPinsOneDigestPerVersionAcrossReplicas(t *testing.T) {
	test := newCommerceTest(t)

	if err := test.service.EnsureTermsCatalog(test.ctx); err != nil {
		t.Fatalf("EnsureTermsCatalog(first replica) error = %v", err)
	}
	var firstCreatedAt time.Time
	if err := test.pool.QueryRow(test.ctx, `
		SELECT created_at
		FROM commerce_terms_catalog
		WHERE terms_version=$1`, commerceTermsVersion).Scan(&firstCreatedAt); err != nil {
		t.Fatalf("read first Commerce terms catalog registration: %v", err)
	}
	exactReplay := newCommerceService(
		t,
		test.store,
		test.provider,
		test.now,
		commerceTermsVersion,
	)
	if err := exactReplay.EnsureTermsCatalog(test.ctx); err != nil {
		t.Fatalf("EnsureTermsCatalog(exact replay) error = %v", err)
	}

	conflictingDigest := sha256.Sum256([]byte("conflicting Commerce Addendum"))
	conflictingReplica := newCommerceServiceWithDigest(
		t,
		test.store,
		test.provider,
		test.now,
		commerceTermsVersion,
		conflictingDigest,
	)
	if err := conflictingReplica.EnsureTermsCatalog(test.ctx); !errors.Is(
		err,
		commerce.ErrTermsConflict,
	) {
		t.Fatalf("EnsureTermsCatalog(conflicting replica) error = %v, want terms conflict", err)
	}
	if _, err := conflictingReplica.AcceptTerms(
		test.ctx,
		test.ownerA,
		commerce.AcceptTermsInput{TermsVersion: commerceTermsVersion, Accepted: true},
	); !errors.Is(err, commerce.ErrTermsConflict) {
		t.Fatalf("AcceptTerms(conflicting replica) error = %v, want terms conflict", err)
	}

	wantDigest := sha256.Sum256([]byte("Boardly Commerce Addendum " + commerceTermsVersion))
	var catalogCount int
	if err := test.pool.QueryRow(test.ctx, `
		SELECT count(*)
		FROM commerce_terms_catalog
		WHERE terms_version=$1`, commerceTermsVersion).Scan(&catalogCount); err != nil {
		t.Fatalf("count Commerce terms catalog: %v", err)
	}
	var storedDigest []byte
	var storedCreatedAt time.Time
	if err := test.pool.QueryRow(test.ctx, `
		SELECT terms_sha256, created_at
		FROM commerce_terms_catalog
		WHERE terms_version=$1`, commerceTermsVersion).Scan(
		&storedDigest,
		&storedCreatedAt,
	); err != nil {
		t.Fatalf("read Commerce terms catalog: %v", err)
	}
	if catalogCount != 1 || !bytes.Equal(storedDigest, wantDigest[:]) ||
		!storedCreatedAt.Equal(firstCreatedAt) {
		t.Fatalf(
			"Commerce terms catalog = count:%d digest:%x registered:%s, want registered:%s",
			catalogCount,
			storedDigest,
			storedCreatedAt,
			firstCreatedAt,
		)
	}
}

func TestCommerceOwnerAuthorityScopesSummaryAndCredentialCreation(t *testing.T) {
	test := newCommerceTest(t)

	summary, err := test.service.Summary(test.ctx, test.ownerA)
	if err != nil {
		t.Fatalf("Summary(owner) error = %v", err)
	}
	if summary.CompanyID != test.fixture.CompanyA || summary.Terms.Accepted || len(summary.Accounts) != 2 {
		t.Fatalf("Summary(owner) authority projection = %#v", summary)
	}

	if _, err := test.service.Summary(test.ctx, test.memberA); !errors.Is(err, commerce.ErrForbidden) {
		t.Fatalf("Summary(member) error = %v, want forbidden", err)
	}
	forgedCrossCompany := commercePrincipal(
		test.fixture.UserB,
		test.ownerB.SessionID,
		test.fixture.CompanyA,
		auth.CompanyRoleOwner,
		1,
	)
	if _, err := test.service.Summary(
		test.ctx,
		forgedCrossCompany,
	); !errors.Is(err, commerce.ErrAccessForbidden) {
		t.Fatalf("Summary(forged cross-company owner) error = %v, want forbidden", err)
	}

	mustAcceptCommerceTerms(t, test, test.ownerA)
	if _, err := test.service.CreateAccountSession(
		test.ctx,
		test.ownerA,
		commerce.EnvironmentSandbox,
	); err != nil {
		t.Fatalf("CreateAccountSession(owner) error = %v", err)
	}
	providerCalls := test.provider.credentialCalls()
	if providerCalls != 1 {
		t.Fatalf("owner credential calls = %d, want 1", providerCalls)
	}

	if _, err := test.service.CreateAccountSession(
		test.ctx,
		test.memberA,
		commerce.EnvironmentSandbox,
	); !errors.Is(err, commerce.ErrForbidden) {
		t.Fatalf("CreateAccountSession(member) error = %v, want forbidden", err)
	}
	if _, err := test.service.CreateAccountSession(
		test.ctx,
		forgedCrossCompany,
		commerce.EnvironmentSandbox,
	); !errors.Is(err, commerce.ErrAccessForbidden) {
		t.Fatalf("CreateAccountSession(forged cross-company owner) error = %v, want forbidden", err)
	}

	dbtest.MustExec(t, test.ctx, test.pool, `
		UPDATE company_members
		SET status='revoked', revoked_at=clock_timestamp(), revoked_by_user_id=$2,
			version=version+1, updated_at=clock_timestamp()
		WHERE company_id=$1 AND user_id=$2`, test.fixture.CompanyA, test.fixture.UserA)
	if _, err := test.service.CreateAccountSession(
		test.ctx,
		test.ownerA,
		commerce.EnvironmentSandbox,
	); !errors.Is(err, commerce.ErrAccessForbidden) {
		t.Fatalf("CreateAccountSession(revoked owner) error = %v, want forbidden", err)
	}
	if got := test.provider.credentialCalls(); got != providerCalls {
		t.Fatalf("unauthorized credential calls changed from %d to %d", providerCalls, got)
	}
}

func TestCommerceDashboardLinkRequiresReadyFullDashboardAccount(t *testing.T) {
	test := newCommerceTest(t)
	mustAcceptCommerceTerms(t, test, test.ownerA)
	if _, err := test.service.CreateDashboardLink(
		test.ctx,
		test.ownerA,
		commerce.EnvironmentSandbox,
	); !errors.Is(err, commerce.ErrProviderSetupRequired) {
		t.Fatalf("CreateDashboardLink(without account) error = %v, want setup required", err)
	}
	readyGatewayAccount(t, test)

	link, err := test.service.CreateDashboardLink(
		test.ctx,
		test.ownerA,
		commerce.EnvironmentSandbox,
	)
	if err != nil || !strings.HasPrefix(
		link.URL,
		"https://dashboard.stripe.com/test/acct_",
	) || !strings.HasSuffix(link.URL, "/dashboard") {
		t.Fatalf("CreateDashboardLink(owner) = %#v, %v", link, err)
	}
	if _, err := test.service.CreateDashboardLink(
		test.ctx,
		test.memberA,
		commerce.EnvironmentSandbox,
	); !errors.Is(err, commerce.ErrForbidden) {
		t.Fatalf("CreateDashboardLink(member) error = %v, want forbidden", err)
	}

	test.provider.mu.Lock()
	test.provider.dashboardURL = "https://example.com/test/acct_invalid/dashboard"
	test.provider.mu.Unlock()
	if _, err := test.service.CreateDashboardLink(
		test.ctx,
		test.ownerA,
		commerce.EnvironmentSandbox,
	); !errors.Is(err, commerce.ErrInvalidProviderState) {
		t.Fatalf("CreateDashboardLink(invalid provider URL) error = %v, want invalid state", err)
	}
	dbtest.MustExec(t, test.ctx, test.pool, `
		UPDATE commerce_accounts
		SET status='disconnected', disconnected_at=clock_timestamp(),
			version=version+1, updated_at=clock_timestamp()
		WHERE company_id=$1 AND environment='sandbox'`, test.fixture.CompanyA)
	if _, err := test.service.CreateDashboardLink(
		test.ctx,
		test.ownerA,
		commerce.EnvironmentSandbox,
	); !errors.Is(err, commerce.ErrProviderSetupRequired) {
		t.Fatalf("CreateDashboardLink(disconnected) error = %v, want setup required", err)
	}
}

func TestCommerceCredentialReleaseReauthorizesAfterProviderIO(t *testing.T) {
	tests := []struct {
		name   string
		revoke func(*testing.T, *commerceTest)
	}{
		{
			name: "membership revoked",
			revoke: func(t *testing.T, test *commerceTest) {
				t.Helper()
				dbtest.MustExec(t, test.ctx, test.pool, `
					UPDATE company_members
					SET status='revoked', revoked_at=clock_timestamp(), revoked_by_user_id=$2,
						version=version+1, updated_at=clock_timestamp()
					WHERE company_id=$1 AND user_id=$2`, test.fixture.CompanyA, test.fixture.UserA)
			},
		},
		{
			name: "originating session revoked",
			revoke: func(t *testing.T, test *commerceTest) {
				t.Helper()
				dbtest.MustExec(t, test.ctx, test.pool, `
					UPDATE auth_sessions
					SET revoked_at=clock_timestamp(), revocation_reason='test revocation'
					WHERE id=$1`, test.ownerA.SessionID)
			},
		},
		{
			name: "user disabled and deleted",
			revoke: func(t *testing.T, test *commerceTest) {
				t.Helper()
				dbtest.MustExec(t, test.ctx, test.pool, `
					UPDATE users
					SET disabled_at=clock_timestamp(), deleted_at=clock_timestamp(),
						version=version+1, updated_at=clock_timestamp()
					WHERE id=$1`, test.fixture.UserA)
			},
		},
		{
			name: "Company deletion starts",
			revoke: func(t *testing.T, test *commerceTest) {
				t.Helper()
				dbtest.MustExec(t, test.ctx, test.pool, `
					UPDATE companies
					SET deleting_at=clock_timestamp(), version=version+1,
						updated_at=clock_timestamp()
					WHERE id=$1`, test.fixture.CompanyA)
			},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			test := newCommerceTest(t)
			mustAcceptCommerceTerms(t, test, test.ownerA)

			if _, err := test.service.CreateAccountSession(
				test.ctx,
				test.ownerA,
				commerce.EnvironmentSandbox,
			); err != nil {
				t.Fatalf("CreateAccountSession(provision) error = %v", err)
			}

			providerStarted := make(chan struct{})
			providerContinue := make(chan struct{})
			test.provider.setCredentialBarrier(providerStarted, providerContinue)
			t.Cleanup(func() { test.provider.setCredentialBarrier(nil, nil) })

			result := make(chan error, 1)
			go func() {
				_, err := test.service.CreateAccountSession(
					test.ctx,
					test.ownerA,
					commerce.EnvironmentSandbox,
				)
				result <- err
			}()

			select {
			case <-providerStarted:
			case <-time.After(5 * time.Second):
				t.Fatal("CreateAccountSession() did not reach provider credential creation")
			}
			testCase.revoke(t, test)
			close(providerContinue)

			select {
			case err := <-result:
				if !errors.Is(err, commerce.ErrAccessForbidden) {
					t.Fatalf(
						"CreateAccountSession(revoked during provider I/O) error = %v, want forbidden",
						err,
					)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("CreateAccountSession() did not finish after provider release")
			}
		})
	}
}

func TestCommerceTermsAcceptanceIsExactImmutableAndIdempotent(t *testing.T) {
	test := newCommerceTest(t)

	first, err := test.service.AcceptTerms(test.ctx, test.ownerA, commerce.AcceptTermsInput{
		TermsVersion: commerceTermsVersion,
		Accepted:     true,
	})
	if err != nil {
		t.Fatalf("AcceptTerms(first) error = %v", err)
	}
	if !first.Created || first.Acceptance.TermsVersion != commerceTermsVersion {
		t.Fatalf("AcceptTerms(first) = %#v", first)
	}

	var storedActor uuid.UUID
	var storedAt time.Time
	if err := test.pool.QueryRow(test.ctx, `
		SELECT accepted_by_user_id, accepted_at
		FROM commerce_terms_acceptances
		WHERE company_id=$1 AND terms_version=$2`,
		test.fixture.CompanyA,
		commerceTermsVersion,
	).Scan(&storedActor, &storedAt); err != nil {
		t.Fatalf("read first Commerce terms receipt: %v", err)
	}
	if storedActor != test.fixture.UserA || !first.Acceptance.AcceptedAt.Equal(storedAt) {
		t.Fatalf(
			"first receipt actor/time = %s/%s, want %s/%s",
			storedActor,
			storedAt,
			test.fixture.UserA,
			first.Acceptance.AcceptedAt,
		)
	}

	dbtest.MustExec(t, test.ctx, test.pool, `
		UPDATE company_members
		SET role='owner', version=version+1, updated_at=clock_timestamp()
		WHERE company_id=$1 AND user_id=$2`, test.fixture.CompanyA, test.fixture.UserB)
	secondOwner := commercePrincipal(
		test.fixture.UserB,
		test.ownerB.SessionID,
		test.fixture.CompanyA,
		auth.CompanyRoleOwner,
		2,
	)
	replay, err := test.service.AcceptTerms(test.ctx, secondOwner, commerce.AcceptTermsInput{
		TermsVersion: commerceTermsVersion,
		Accepted:     true,
	})
	if err != nil {
		t.Fatalf("AcceptTerms(replay) error = %v", err)
	}
	if replay.Created || !replay.Acceptance.AcceptedAt.Equal(first.Acceptance.AcceptedAt) {
		t.Fatalf("AcceptTerms(replay) = %#v, first = %#v", replay, first)
	}

	var receiptCount int
	if err := test.pool.QueryRow(test.ctx, `
		SELECT count(*)
		FROM commerce_terms_acceptances
		WHERE company_id=$1 AND terms_version=$2`,
		test.fixture.CompanyA,
		commerceTermsVersion,
	).Scan(&receiptCount); err != nil {
		t.Fatalf("count Commerce terms receipts: %v", err)
	}
	if receiptCount != 1 {
		t.Fatalf("Commerce terms receipt count = %d, want 1", receiptCount)
	}
	if err := test.pool.QueryRow(test.ctx, `
		SELECT accepted_by_user_id, accepted_at
		FROM commerce_terms_acceptances
		WHERE company_id=$1 AND terms_version=$2`,
		test.fixture.CompanyA,
		commerceTermsVersion,
	).Scan(&storedActor, &storedAt); err != nil {
		t.Fatalf("read replayed Commerce terms receipt: %v", err)
	}
	if storedActor != test.fixture.UserA || !storedAt.Equal(first.Acceptance.AcceptedAt) {
		t.Fatalf("replayed receipt changed actor/time to %s/%s", storedActor, storedAt)
	}
}

func TestCommerceTermsVersionRotationRejectsStaleAcceptance(t *testing.T) {
	test := newCommerceTest(t)
	mustAcceptCommerceTerms(t, test, test.ownerA)

	const nextTermsVersion = "2026-09-01"
	nextService := newCommerceService(
		t,
		test.store,
		test.provider,
		test.now,
		nextTermsVersion,
	)
	summary, err := nextService.Summary(test.ctx, test.ownerA)
	if err != nil {
		t.Fatalf("Summary(next terms) error = %v", err)
	}
	if summary.Terms.Accepted || summary.Terms.CurrentVersion != nextTermsVersion {
		t.Fatalf("rotated terms state = %#v", summary.Terms)
	}
	if _, err := nextService.AcceptTerms(test.ctx, test.ownerA, commerce.AcceptTermsInput{
		TermsVersion: commerceTermsVersion,
		Accepted:     true,
	}); !errors.Is(err, commerce.ErrTermsOutdated) {
		t.Fatalf("AcceptTerms(stale version) error = %v, want terms outdated", err)
	}
	if _, err := nextService.AcceptTerms(test.ctx, test.ownerA, commerce.AcceptTermsInput{
		TermsVersion: nextTermsVersion,
		Accepted:     false,
	}); !errors.Is(err, commerce.ErrInvalidInput) {
		t.Fatalf("AcceptTerms(false) error = %v, want invalid input", err)
	}
	if _, err := nextService.CreateAccountSession(
		test.ctx,
		test.ownerA,
		commerce.EnvironmentSandbox,
	); !errors.Is(err, commerce.ErrTermsOutdated) {
		t.Fatalf("CreateAccountSession(stale acceptance) error = %v, want terms outdated", err)
	}
	if got := test.provider.createdAccountCount(); got != 0 {
		t.Fatalf("stale acceptance created %d provider accounts", got)
	}
}

func TestCommercePublicReadinessRequiresCurrentTermsAndProviderRead(t *testing.T) {
	test := newCommerceTest(t)
	mustAcceptCommerceTerms(t, test, test.ownerA)
	if _, err := test.service.CreateAccountSession(
		test.ctx,
		test.ownerA,
		commerce.EnvironmentSandbox,
	); err != nil {
		t.Fatalf("CreateAccountSession(provision) error = %v", err)
	}
	test.provider.setAllAccountsReady()

	summary, err := test.service.Summary(test.ctx, test.ownerA)
	if err != nil {
		t.Fatalf("Summary(ready) error = %v", err)
	}
	readyAccount := summaryAccount(t, summary, commerce.EnvironmentSandbox)
	if readyAccount.Status != commerce.StatusReady || readyAccount.SyncStatus != "current" {
		t.Fatalf("ready account = %#v", readyAccount)
	}

	test.provider.setRetrieveFailure(true)
	stale, err := test.service.Summary(test.ctx, test.ownerA)
	if err != nil {
		t.Fatalf("Summary(stale provider) error = %v", err)
	}
	staleAccount := summaryAccount(t, stale, commerce.EnvironmentSandbox)
	if staleAccount.Status == commerce.StatusReady || staleAccount.SyncStatus != "stale" {
		t.Fatalf("stale provider account = %#v", staleAccount)
	}
	test.provider.setRetrieveFailure(false)

	digest := sha256.Sum256([]byte("Boardly Commerce Addendum " + commerceTermsVersion))
	unconfiguredService, err := commerce.NewCommerceService(commerce.ServiceOptions{
		Store: test.store,
		Terms: commerce.TermsDocument{
			Version:     commerceTermsVersion,
			SHA256:      digest,
			DocumentURL: "https://boardly.test/legal/commerce/" + commerceTermsVersion,
			PrivacyURL:  "https://boardly.test/legal/privacy",
		},
		Environments:     map[commerce.Environment]commerce.EnvironmentOptions{},
		AllowedCountries: []string{"GB"},
		Now:              func() time.Time { return test.now },
	})
	if err != nil {
		t.Fatalf("commerce.NewCommerceService(unconfigured provider) error = %v", err)
	}
	unconfigured, err := unconfiguredService.Summary(test.ctx, test.ownerA)
	if err != nil {
		t.Fatalf("Summary(unconfigured provider) error = %v", err)
	}
	unconfiguredAccount := summaryAccount(t, unconfigured, commerce.EnvironmentSandbox)
	if unconfiguredAccount.Configured || unconfiguredAccount.Status == commerce.StatusReady ||
		unconfiguredAccount.SyncStatus != "stale" || unconfiguredAccount.SyncErrorCode == nil ||
		*unconfiguredAccount.SyncErrorCode != "provider_unavailable" {
		t.Fatalf("unconfigured provider account = %#v", unconfiguredAccount)
	}

	const nextTermsVersion = "2026-09-01"
	nextService := newCommerceService(t, test.store, test.provider, test.now, nextTermsVersion)
	rotated, err := nextService.Summary(test.ctx, test.ownerA)
	if err != nil {
		t.Fatalf("Summary(rotated terms) error = %v", err)
	}
	rotatedAccount := summaryAccount(t, rotated, commerce.EnvironmentSandbox)
	if rotated.Terms.Accepted || rotatedAccount.Status != commerce.StatusActionRequired ||
		!rotatedAccount.ActionRequired {
		t.Fatalf("rotated terms account = terms:%#v account:%#v", rotated.Terms, rotatedAccount)
	}
}

func TestCommerceAccountProvisioningIsConcurrentSafeAndCompanyScoped(t *testing.T) {
	test := newCommerceTest(t)
	mustAcceptCommerceTerms(t, test, test.ownerA)

	start := make(chan struct{})
	type result struct {
		session commerce.AccountSession
		err     error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			<-start
			session, err := test.service.CreateAccountSession(
				test.ctx,
				test.ownerA,
				commerce.EnvironmentSandbox,
			)
			results <- result{session: session, err: err}
		}()
	}
	close(start)
	successes := 0
	for range 2 {
		result := <-results
		if result.err != nil {
			if !errors.Is(result.err, commerce.ErrAccountRecovery) {
				t.Fatalf("concurrent CreateAccountSession() error = %v", result.err)
			}
			continue
		}
		successes++
		if result.session.PublishableKey != "pk_test_boardlycommerce123" ||
			result.session.ClientSecret == "" || !result.session.ExpiresAt.Equal(test.now.Add(time.Hour)) {
			t.Fatal("concurrent CreateAccountSession() returned an invalid public session envelope")
		}
	}
	if successes == 0 {
		t.Fatal("concurrent provisioning returned no successful AccountSession")
	}

	if got := test.provider.createdAccountCount(); got != 1 {
		t.Fatalf("provider account count = %d, want 1", got)
	}
	if got := test.provider.uniqueCreateIdempotencyKeys(); got != 1 {
		t.Fatalf("provider account idempotency keys = %d, want 1", got)
	}
	assertCommerceAccountRow(t, test, test.fixture.CompanyA, test.fixture.UserA)

	if _, err := test.service.CreateAccountSession(
		test.ctx,
		test.ownerA,
		commerce.EnvironmentSandbox,
	); err != nil {
		t.Fatalf("CreateAccountSession(retry) error = %v", err)
	}
	if got := test.provider.createdAccountCount(); got != 1 {
		t.Fatalf("retry provider account count = %d, want 1", got)
	}

	mustAcceptCommerceTerms(t, test, test.ownerB)
	if _, err := test.service.CreateAccountSession(
		test.ctx,
		test.ownerB,
		commerce.EnvironmentSandbox,
	); err != nil {
		t.Fatalf("CreateAccountSession(second Company) error = %v", err)
	}
	if got := test.provider.createdAccountCount(); got != 2 {
		t.Fatalf("provider account count across Companies = %d, want 2", got)
	}
	assertCommerceAccountRow(t, test, test.fixture.CompanyB, test.fixture.UserB)

	var accountRows int
	if err := test.pool.QueryRow(test.ctx, `
		SELECT count(*) FROM commerce_accounts
		WHERE environment='sandbox' AND company_id IN ($1, $2)`,
		test.fixture.CompanyA,
		test.fixture.CompanyB,
	).Scan(&accountRows); err != nil {
		t.Fatalf("count Company Commerce accounts: %v", err)
	}
	if accountRows != 2 {
		t.Fatalf("Company Commerce account rows = %d, want 2", accountRows)
	}
}

func TestCommerceProvisioningRecoversAmbiguousProviderCreateByOperationMetadata(t *testing.T) {
	test := newCommerceTest(t)
	mustAcceptCommerceTerms(t, test, test.ownerA)
	test.provider.failNextCreateAfterPersistingAccount()

	if _, err := test.service.CreateAccountSession(
		test.ctx,
		test.ownerA,
		commerce.EnvironmentSandbox,
	); !errors.Is(err, commerce.ErrProviderUnavailable) {
		t.Fatalf("CreateAccountSession(ambiguous create) error = %v, want provider unavailable", err)
	}
	if got := test.provider.createdAccountCount(); got != 1 {
		t.Fatalf("provider accounts after ambiguous create = %d, want 1", got)
	}
	if _, err := test.service.CreateAccountSession(
		test.ctx,
		test.ownerA,
		commerce.EnvironmentSandbox,
	); err != nil {
		t.Fatalf("CreateAccountSession(recover ambiguous create) error = %v", err)
	}
	if got := test.provider.createdAccountCount(); got != 1 {
		t.Fatalf("provider accounts after metadata recovery = %d, want 1", got)
	}
	if got := test.provider.accountLookupCalls(); got < 2 {
		t.Fatalf("provider metadata lookup calls = %d, want lookup before create and recovery", got)
	}

	var state string
	var providerAttemptCount int32
	var providerResourceID string
	if err := test.pool.QueryRow(test.ctx, `
		SELECT state, provider_attempt_count, provider_resource_id
		FROM commerce_operations
		WHERE company_id=$1 AND environment='sandbox'`, test.fixture.CompanyA).Scan(
		&state,
		&providerAttemptCount,
		&providerResourceID,
	); err != nil {
		t.Fatalf("read recovered Commerce operation: %v", err)
	}
	if state != "succeeded" || providerAttemptCount != 1 ||
		!strings.HasPrefix(providerResourceID, "acct_") {
		t.Fatalf(
			"recovered Commerce operation = state:%q provider attempts:%d resource:%q",
			state,
			providerAttemptCount,
			providerResourceID,
		)
	}
}

func TestCommerceProvisioningRotatesIdempotencyAfterDefinitiveSetupRejection(t *testing.T) {
	test := newCommerceTest(t)
	mustAcceptCommerceTerms(t, test, test.ownerA)
	test.provider.requireConnectSetupForNextCreate()

	if _, err := test.service.CreateAccountSession(
		test.ctx,
		test.ownerA,
		commerce.EnvironmentSandbox,
	); !errors.Is(err, commerce.ErrProviderSetupRequired) {
		t.Fatalf("CreateAccountSession(setup rejection) error = %v, want setup required", err)
	}
	firstKeys := test.provider.createKeys()
	if len(firstKeys) != 1 {
		t.Fatalf("provider create keys after rejection = %v, want one key", firstKeys)
	}

	var state string
	var storedKey string
	var providerAttemptCount int32
	var lastErrorCode string
	if err := test.pool.QueryRow(test.ctx, `
		SELECT state, idempotency_key, provider_attempt_count, last_error_code
		FROM commerce_operations
		WHERE company_id=$1 AND environment='sandbox'`, test.fixture.CompanyA).Scan(
		&state,
		&storedKey,
		&providerAttemptCount,
		&lastErrorCode,
	); err != nil {
		t.Fatalf("read rejected Commerce operation: %v", err)
	}
	if state != "pending" || storedKey == firstKeys[0] || providerAttemptCount != 1 ||
		lastErrorCode != "provider_setup_required" {
		t.Fatalf(
			"rejected Commerce operation = state:%q key:%q attempts:%d error:%q",
			state,
			storedKey,
			providerAttemptCount,
			lastErrorCode,
		)
	}

	if _, err := test.service.CreateAccountSession(
		test.ctx,
		test.ownerA,
		commerce.EnvironmentSandbox,
	); err != nil {
		t.Fatalf("CreateAccountSession(after setup correction) error = %v", err)
	}
	keys := test.provider.createKeys()
	if len(keys) != 2 || keys[0] == keys[1] || keys[1] != storedKey {
		t.Fatalf("provider create keys = %v, want distinct persisted retry key", keys)
	}
	if got := test.provider.createdAccountCount(); got != 1 {
		t.Fatalf("provider account count after setup correction = %d, want 1", got)
	}
}

func TestCommerceProvisioningNeverPostsAfterDurableRetryDeadline(t *testing.T) {
	test := newCommerceTest(t)
	mustAcceptCommerceTerms(t, test, test.ownerA)
	test.provider.failNextCreateWithoutPersistingAccount()

	if _, err := test.service.CreateAccountSession(
		test.ctx,
		test.ownerA,
		commerce.EnvironmentSandbox,
	); !errors.Is(err, commerce.ErrProviderUnavailable) {
		t.Fatalf("CreateAccountSession(ambiguous missing create) error = %v, want provider unavailable", err)
	}
	if got := test.provider.createAccountCalls(); got != 1 {
		t.Fatalf("initial provider create calls = %d, want 1", got)
	}
	ageCommerceOperationPastRetryDeadline(t, test)

	if _, err := test.service.CreateAccountSession(
		test.ctx,
		test.ownerA,
		commerce.EnvironmentSandbox,
	); !errors.Is(err, commerce.ErrAccountRecovery) {
		t.Fatalf("CreateAccountSession(expired retry horizon) error = %v, want account recovery", err)
	}
	if got := test.provider.createAccountCalls(); got != 1 {
		t.Fatalf("expired operation provider create calls = %d, want 1", got)
	}

	var state string
	var recoveryRequiredAt *time.Time
	if err := test.pool.QueryRow(test.ctx, `
		SELECT state, recovery_required_at
		FROM commerce_operations
		WHERE company_id=$1 AND environment='sandbox'`, test.fixture.CompanyA).Scan(
		&state,
		&recoveryRequiredAt,
	); err != nil {
		t.Fatalf("read expired Commerce operation: %v", err)
	}
	if state != "recovery_required" || recoveryRequiredAt == nil {
		t.Fatalf("expired Commerce operation = state:%q recovery:%v", state, recoveryRequiredAt)
	}
}

func TestCommerceProviderRetryUsesDatabaseClockAcrossApplicationSkew(t *testing.T) {
	t.Run("behind clock cannot post after durable deadline", func(t *testing.T) {
		test := newCommerceTest(t)
		mustAcceptCommerceTerms(t, test, test.ownerA)
		test.provider.failNextCreateWithoutPersistingAccount()

		if _, err := test.service.CreateAccountSession(
			test.ctx,
			test.ownerA,
			commerce.EnvironmentSandbox,
		); !errors.Is(err, commerce.ErrProviderUnavailable) {
			t.Fatalf("CreateAccountSession(initial ambiguous create) error = %v", err)
		}
		ageCommerceOperationPastRetryDeadline(t, test)
		behindClock := time.Date(1900, time.January, 1, 0, 0, 0, 0, time.UTC)
		behindService := newCommerceService(
			t,
			test.store,
			test.provider,
			behindClock,
			commerceTermsVersion,
		)

		if _, err := behindService.CreateAccountSession(
			test.ctx,
			test.ownerA,
			commerce.EnvironmentSandbox,
		); !errors.Is(err, commerce.ErrAccountRecovery) {
			t.Fatalf("CreateAccountSession(behind clock) error = %v, want account recovery", err)
		}
		if got := test.provider.createAccountCalls(); got != 1 {
			t.Fatalf("behind clock provider create calls = %d, want 1", got)
		}
	})

	t.Run("ahead clock cannot suppress retry inside durable deadline", func(t *testing.T) {
		test := newCommerceTest(t)
		mustAcceptCommerceTerms(t, test, test.ownerA)
		test.provider.failNextCreateWithoutPersistingAccount()

		if _, err := test.service.CreateAccountSession(
			test.ctx,
			test.ownerA,
			commerce.EnvironmentSandbox,
		); !errors.Is(err, commerce.ErrProviderUnavailable) {
			t.Fatalf("CreateAccountSession(initial ambiguous create) error = %v", err)
		}
		aheadClock := time.Date(2200, time.January, 1, 0, 0, 0, 0, time.UTC)
		test.provider.setNow(aheadClock)
		aheadService := newCommerceService(
			t,
			test.store,
			test.provider,
			aheadClock,
			commerceTermsVersion,
		)

		if _, err := aheadService.CreateAccountSession(
			test.ctx,
			test.ownerA,
			commerce.EnvironmentSandbox,
		); err != nil {
			t.Fatalf("CreateAccountSession(ahead clock) error = %v", err)
		}
		if got := test.provider.createAccountCalls(); got != 2 {
			t.Fatalf("ahead clock provider create calls = %d, want 2", got)
		}
		if got := test.provider.uniqueCreateIdempotencyKeys(); got != 1 {
			t.Fatalf("ahead clock idempotency keys = %d, want 1", got)
		}
	})
}

func TestCommerceProvisioningLeaseCannotBeStolenByApplicationClockSkew(t *testing.T) {
	test := newCommerceTest(t)
	mustAcceptCommerceTerms(t, test, test.ownerA)
	lookupStarted := make(chan struct{})
	lookupContinue := make(chan struct{})
	test.provider.setLookupBarrier(lookupStarted, lookupContinue)
	behindClock := time.Date(1900, time.January, 1, 0, 0, 0, 0, time.UTC)
	test.provider.setNow(behindClock)
	firstService := newCommerceService(
		t,
		test.store,
		test.provider,
		behindClock,
		commerceTermsVersion,
	)
	aheadService := newCommerceService(
		t,
		test.store,
		test.provider,
		time.Date(2200, time.January, 1, 0, 0, 0, 0, time.UTC),
		commerceTermsVersion,
	)

	firstResult := make(chan error, 1)
	go func() {
		_, err := firstService.CreateAccountSession(
			test.ctx,
			test.ownerA,
			commerce.EnvironmentSandbox,
		)
		firstResult <- err
	}()
	select {
	case <-lookupStarted:
	case <-test.ctx.Done():
		t.Fatalf("first provider lookup did not start: %v", test.ctx.Err())
	}

	if _, err := aheadService.CreateAccountSession(
		test.ctx,
		test.ownerA,
		commerce.EnvironmentSandbox,
	); !errors.Is(err, commerce.ErrAccountRecovery) {
		t.Fatalf("concurrent CreateAccountSession() error = %v, want account recovery", err)
	}
	var attemptCount int32
	var leaseLive bool
	if err := test.pool.QueryRow(test.ctx, `
		SELECT attempt_count, COALESCE(lease_expires_at > clock_timestamp(), false)
		FROM commerce_operations
		WHERE company_id=$1 AND environment='sandbox'`, test.fixture.CompanyA).Scan(
		&attemptCount,
		&leaseLive,
	); err != nil {
		t.Fatalf("read active Commerce operation lease: %v", err)
	}
	if attemptCount != 1 || !leaseLive {
		t.Fatalf("active Commerce operation lease = attempts:%d live:%t, want 1/true", attemptCount, leaseLive)
	}

	close(lookupContinue)
	select {
	case err := <-firstResult:
		if err != nil {
			t.Fatalf("first CreateAccountSession() error = %v", err)
		}
	case <-test.ctx.Done():
		t.Fatalf("first provisioning did not finish: %v", test.ctx.Err())
	}
	if got := test.provider.createdAccountCount(); got != 1 {
		t.Fatalf("provider account count = %d, want 1", got)
	}
}

func TestCommerceProviderAttemptReadsDatabaseClockAfterOperationLock(t *testing.T) {
	test := newCommerceTest(t)
	mustAcceptCommerceTerms(t, test, test.ownerA)
	test.provider.failNextCreateWithoutPersistingAccount()
	if _, err := test.service.CreateAccountSession(
		test.ctx,
		test.ownerA,
		commerce.EnvironmentSandbox,
	); !errors.Is(err, commerce.ErrProviderUnavailable) {
		t.Fatalf("CreateAccountSession(initial ambiguous create) error = %v", err)
	}
	dbtest.MustExec(t, test.ctx, test.pool, `
		WITH db_clock AS MATERIALIZED (
			SELECT clock_timestamp() AS now_at
		)
		UPDATE commerce_operations
		SET created_at=db_clock.now_at - interval '22 hours 59 minutes 58 seconds',
		    provider_retry_deadline_at=db_clock.now_at + interval '2 seconds',
		    version=commerce_operations.version + 1
		FROM db_clock
		WHERE company_id=$1 AND environment='sandbox'`, test.fixture.CompanyA)

	lookupStarted := make(chan struct{})
	lookupContinue := make(chan struct{})
	test.provider.setLookupBarrier(lookupStarted, lookupContinue)
	retryResult := make(chan error, 1)
	go func() {
		_, err := test.service.CreateAccountSession(
			test.ctx,
			test.ownerA,
			commerce.EnvironmentSandbox,
		)
		retryResult <- err
	}()
	select {
	case <-lookupStarted:
	case <-test.ctx.Done():
		t.Fatalf("provider recovery lookup did not start: %v", test.ctx.Err())
	}

	blocker, err := test.pool.Begin(test.ctx)
	if err != nil {
		t.Fatalf("begin operation lock blocker: %v", err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	var operationID uuid.UUID
	if err := blocker.QueryRow(test.ctx, `
		SELECT id
		FROM commerce_operations
		WHERE company_id=$1 AND environment='sandbox'
		FOR UPDATE`, test.fixture.CompanyA).Scan(&operationID); err != nil {
		t.Fatalf("lock Commerce operation before provider attempt: %v", err)
	}
	close(lookupContinue)

	deadlineTicker := time.NewTicker(10 * time.Millisecond)
	defer deadlineTicker.Stop()
	for {
		var deadlinePassed bool
		if err := blocker.QueryRow(test.ctx, `
			SELECT clock_timestamp() >= provider_retry_deadline_at
			FROM commerce_operations
			WHERE id=$1`, operationID).Scan(&deadlinePassed); err != nil {
			t.Fatalf("read Commerce provider retry deadline: %v", err)
		}
		if deadlinePassed {
			break
		}
		select {
		case <-deadlineTicker.C:
		case <-test.ctx.Done():
			t.Fatalf("Commerce provider retry deadline did not pass: %v", test.ctx.Err())
		}
	}
	if err := blocker.Commit(test.ctx); err != nil {
		t.Fatalf("release Commerce operation lock blocker: %v", err)
	}

	select {
	case err := <-retryResult:
		if !errors.Is(err, commerce.ErrAccountRecovery) {
			t.Fatalf("CreateAccountSession(post-lock expiry) error = %v, want account recovery", err)
		}
	case <-test.ctx.Done():
		t.Fatalf("provider retry did not finish: %v", test.ctx.Err())
	}
	if got := test.provider.createAccountCalls(); got != 1 {
		t.Fatalf("post-lock expiry provider create calls = %d, want 1", got)
	}
}

func TestCommerceProviderCallWindowPreventsPostAfterDurableDeadline(t *testing.T) {
	test := newCommerceTest(t)
	mustAcceptCommerceTerms(t, test, test.ownerA)
	test.provider.returnIncompleteLookupPageOnce()
	if _, err := test.service.CreateAccountSession(
		test.ctx,
		test.ownerA,
		commerce.EnvironmentSandbox,
	); !errors.Is(err, commerce.ErrAccountRecovery) {
		t.Fatalf("CreateAccountSession(initial recovery page) error = %v", err)
	}
	dbtest.MustExec(t, test.ctx, test.pool, `
		WITH db_clock AS MATERIALIZED (
			SELECT clock_timestamp() AS now_at
		)
		UPDATE commerce_operations
		SET created_at=db_clock.now_at - interval '22 hours 59 minutes 58 seconds',
		    provider_retry_deadline_at=db_clock.now_at + interval '2 seconds',
		    version=commerce_operations.version + 1
		FROM db_clock
		WHERE company_id=$1 AND environment='sandbox'`, test.fixture.CompanyA)

	createStarted := make(chan struct{})
	createContinue := make(chan struct{})
	test.provider.setCreateBarrier(createStarted, createContinue)
	service := newCommerceServiceWithProviderCallTimeout(
		t,
		test.store,
		test.provider,
		test.now,
		commerceTermsVersion,
		time.Second,
	)
	result := make(chan error, 1)
	go func() {
		_, err := service.CreateAccountSession(
			test.ctx,
			test.ownerA,
			commerce.EnvironmentSandbox,
		)
		result <- err
	}()
	select {
	case <-createStarted:
	case <-test.ctx.Done():
		t.Fatalf("reserved provider create did not reach its pre-POST fence: %v", test.ctx.Err())
	}
	waitForCommerceRetryDeadline(t, test)
	close(createContinue)

	select {
	case err := <-result:
		if !errors.Is(err, commerce.ErrProviderUnavailable) {
			t.Fatalf("CreateAccountSession(expired call window) error = %v", err)
		}
	case <-test.ctx.Done():
		t.Fatalf("expired provider call did not return: %v", test.ctx.Err())
	}
	if got := test.provider.createAccountCalls(); got != 0 {
		t.Fatalf("provider POST calls after durable retry deadline = %d, want 0", got)
	}
	var state string
	var providerAttemptCount int32
	var recoveryRequired bool
	if err := test.pool.QueryRow(test.ctx, `
		SELECT state, provider_attempt_count, recovery_required_at IS NOT NULL
		FROM commerce_operations
		WHERE company_id=$1 AND environment='sandbox'`, test.fixture.CompanyA).Scan(
		&state,
		&providerAttemptCount,
		&recoveryRequired,
	); err != nil {
		t.Fatalf("read expired provider call reservation: %v", err)
	}
	if state != "recovery_required" || providerAttemptCount != 1 || !recoveryRequired {
		t.Fatalf(
			"expired provider call reservation = state:%q attempts:%d recovery:%t",
			state,
			providerAttemptCount,
			recoveryRequired,
		)
	}
}

func TestCommerceProvisioningFinalizesConfirmedProviderEffectAfterRequestCancellation(t *testing.T) {
	test := newCommerceTest(t)
	mustAcceptCommerceTerms(t, test, test.ownerA)
	requestCtx, cancel := context.WithCancel(test.ctx)
	test.provider.cancelAfterNextCreate(cancel)

	if _, err := test.service.CreateAccountSession(
		requestCtx,
		test.ownerA,
		commerce.EnvironmentSandbox,
	); err == nil {
		t.Fatal("CreateAccountSession(cancelled after provider create) error = nil")
	}

	var operationState, providerAccountID string
	if err := test.pool.QueryRow(test.ctx, `
		SELECT operations.state, accounts.provider_account_id
		FROM commerce_operations AS operations
		JOIN commerce_accounts AS accounts
		  ON accounts.id=operations.commerce_account_id
		 AND accounts.company_id=operations.company_id
		 AND accounts.environment=operations.environment
		WHERE operations.company_id=$1 AND operations.environment='sandbox'`,
		test.fixture.CompanyA,
	).Scan(&operationState, &providerAccountID); err != nil {
		t.Fatalf("read cancelled Commerce provisioning finalization: %v", err)
	}
	if operationState != "succeeded" || !strings.HasPrefix(providerAccountID, "acct_") {
		t.Fatalf(
			"cancelled Commerce provisioning = state:%q provider:%q",
			operationState,
			providerAccountID,
		)
	}
}

type commerceTest struct {
	t                  *testing.T
	ctx                context.Context
	pool               *pgxpool.Pool
	store              *db.Store
	fixture            dbtest.Fixture
	service            *commerce.DefaultCommerceService
	provider           *fakeCommerceProvider
	ownerA             auth.UserPrincipal
	ownerB             auth.UserPrincipal
	memberA            auth.UserPrincipal
	now                time.Time
	manifestRequestKey uuid.UUID
}

func newCommerceTest(t *testing.T) *commerceTest {
	t.Helper()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("db.MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	sessionA := dbtest.NewUUID(t)
	sessionB := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO auth_sessions (id, user_id, token_prefix, token_hash, expires_at)
		VALUES
			($1, $2, 'commerce-a', $3, clock_timestamp() + interval '1 hour'),
			($4, $5, 'commerce-b', $6, clock_timestamp() + interval '1 hour')`,
		sessionA, fixture.UserA, []byte("commerce-session-a"),
		sessionB, fixture.UserB, []byte("commerce-session-b"))
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO company_members (company_id, user_id, role)
		VALUES ($1, $2, 'member')`, fixture.CompanyA, fixture.UserB)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("db.NewStore() error = %v", err)
	}
	now := time.Date(2026, 8, 20, 8, 30, 0, 0, time.UTC)
	provider := newFakeCommerceProvider(now)
	service := newCommerceService(t, store, provider, now, commerceTermsVersion)
	return &commerceTest{
		t: t, ctx: ctx, pool: pool, store: store, fixture: fixture,
		service: service, provider: provider,
		ownerA: commercePrincipal(
			fixture.UserA,
			sessionA,
			fixture.CompanyA,
			auth.CompanyRoleOwner,
			1,
		),
		ownerB: commercePrincipal(
			fixture.UserB,
			sessionB,
			fixture.CompanyB,
			auth.CompanyRoleOwner,
			1,
		),
		memberA: commercePrincipal(
			fixture.UserB,
			sessionB,
			fixture.CompanyA,
			auth.CompanyRoleMember,
			1,
		),
		now:                now,
		manifestRequestKey: dbtest.NewUUID(t),
	}
}

func newCommerceService(
	t *testing.T,
	store *db.Store,
	provider commerce.Provider,
	now time.Time,
	termsVersion string,
) *commerce.DefaultCommerceService {
	t.Helper()
	digest := sha256.Sum256([]byte("Boardly Commerce Addendum " + termsVersion))
	return newCommerceServiceWithOptions(t, store, provider, now, termsVersion, digest, 0)
}

func newCommerceServiceWithDigest(
	t *testing.T,
	store *db.Store,
	provider commerce.Provider,
	now time.Time,
	termsVersion string,
	digest [sha256.Size]byte,
) *commerce.DefaultCommerceService {
	t.Helper()
	return newCommerceServiceWithOptions(t, store, provider, now, termsVersion, digest, 0)
}

func newCommerceServiceWithProviderCallTimeout(
	t *testing.T,
	store *db.Store,
	provider commerce.Provider,
	now time.Time,
	termsVersion string,
	providerCallTimeout time.Duration,
) *commerce.DefaultCommerceService {
	t.Helper()
	digest := sha256.Sum256([]byte("Boardly Commerce Addendum " + termsVersion))
	return newCommerceServiceWithOptions(
		t,
		store,
		provider,
		now,
		termsVersion,
		digest,
		providerCallTimeout,
	)
}

func newCommerceServiceWithOptions(
	t *testing.T,
	store *db.Store,
	provider commerce.Provider,
	now time.Time,
	termsVersion string,
	digest [sha256.Size]byte,
	providerCallTimeout time.Duration,
) *commerce.DefaultCommerceService {
	t.Helper()
	service, err := commerce.NewCommerceService(commerce.ServiceOptions{
		Store:               store,
		AllowedCountries:    []string{"GB"},
		ProviderCallTimeout: providerCallTimeout,
		Terms: commerce.TermsDocument{
			Version:     termsVersion,
			SHA256:      digest,
			DocumentURL: "https://boardly.test/legal/commerce/" + termsVersion,
			PrivacyURL:  "https://boardly.test/legal/privacy",
		},
		Environments: map[commerce.Environment]commerce.EnvironmentOptions{
			commerce.EnvironmentSandbox: {
				Configured:     true,
				PublishableKey: "pk_test_boardlycommerce123",
				ReturnURL:      "https://boardly.test/connect/stripe/return",
				RefreshURL:     "https://boardly.test/connect/stripe/refresh",
				Provider:       provider,
			},
		},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("commerce.NewCommerceService() error = %v", err)
	}
	return service
}

func commercePrincipal(
	userID uuid.UUID,
	sessionID uuid.UUID,
	companyID uuid.UUID,
	role auth.CompanyRole,
	_ int64,
) auth.UserPrincipal {
	return auth.UserPrincipal{
		UserID: userID, SessionID: sessionID, AuthKind: auth.TokenKindSession,
		CompanyID: companyID, Role: role,
	}
}

func mustAcceptCommerceTerms(
	t *testing.T,
	test *commerceTest,
	principal auth.UserPrincipal,
) {
	t.Helper()
	result, err := test.service.AcceptTerms(test.ctx, principal, commerce.AcceptTermsInput{
		TermsVersion: commerceTermsVersion,
		Accepted:     true,
	})
	if err != nil {
		t.Fatalf("AcceptTerms() error = %v", err)
	}
	if result.Acceptance.TermsVersion != commerceTermsVersion {
		t.Fatalf(
			"AcceptTerms().TermsVersion = %q, want %q",
			result.Acceptance.TermsVersion,
			commerceTermsVersion,
		)
	}
}

func ageCommerceOperationPastRetryDeadline(t *testing.T, test *commerceTest) {
	t.Helper()
	dbtest.MustExec(t, test.ctx, test.pool, `
		WITH db_clock AS MATERIALIZED (
			SELECT clock_timestamp() AS now_at
		)
		UPDATE commerce_operations
		SET created_at=db_clock.now_at - interval '23 hours 1 minute',
		    provider_retry_deadline_at=db_clock.now_at - interval '1 minute',
		    version=commerce_operations.version + 1
		FROM db_clock
		WHERE company_id=$1 AND environment='sandbox'`, test.fixture.CompanyA)
}

func waitForCommerceRetryDeadline(t *testing.T, test *commerceTest) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var passed bool
		if err := test.pool.QueryRow(test.ctx, `
			SELECT clock_timestamp() >= provider_retry_deadline_at
			FROM commerce_operations
			WHERE company_id=$1 AND environment='sandbox'`, test.fixture.CompanyA).Scan(&passed); err != nil {
			t.Fatalf("read Commerce provider retry deadline: %v", err)
		}
		if passed {
			return
		}
		select {
		case <-ticker.C:
		case <-test.ctx.Done():
			t.Fatalf("Commerce provider retry deadline did not pass: %v", test.ctx.Err())
		}
	}
}

func assertCommerceAccountRow(
	t *testing.T,
	test *commerceTest,
	companyID uuid.UUID,
	createdBy uuid.UUID,
) {
	t.Helper()
	var environment, providerAccountID, status string
	var storedCreatedBy uuid.UUID
	var version, generation int64
	if err := test.pool.QueryRow(test.ctx, `
		SELECT environment, provider_account_id, status, created_by_user_id,
			version, reconciliation_generation
		FROM commerce_accounts
		WHERE company_id=$1 AND environment='sandbox'`, companyID).Scan(
		&environment,
		&providerAccountID,
		&status,
		&storedCreatedBy,
		&version,
		&generation,
	); err != nil {
		t.Fatalf("read Company Commerce account: %v", err)
	}
	if environment != "sandbox" || !strings.HasPrefix(providerAccountID, "acct_") ||
		status != string(commerce.StatusOnboarding) || storedCreatedBy != createdBy ||
		version < 2 || generation < 1 {
		t.Fatalf(
			"Company Commerce account = env:%q provider:%q status:%q creator:%s version:%d generation:%d",
			environment,
			providerAccountID,
			status,
			storedCreatedBy,
			version,
			generation,
		)
	}
}

func summaryAccount(
	t *testing.T,
	summary commerce.Summary,
	environment commerce.Environment,
) commerce.Account {
	t.Helper()
	for _, account := range summary.Accounts {
		if account.Environment == environment {
			return account
		}
	}
	t.Fatalf("Summary() has no %s account: %#v", environment, summary.Accounts)
	return commerce.Account{}
}

type fakeCommerceProvider struct {
	mu sync.Mutex

	now                      time.Time
	accounts                 map[string]commerce.ProviderAccount
	accountByIdempotencyKey  map[string]string
	createIdempotencyKeys    []string
	credentialCallCount      int
	credentialStarted        chan struct{}
	credentialContinue       <-chan struct{}
	retrieveFailure          bool
	dashboardURL             string
	failCreateAfterPersist   bool
	failCreateWithoutPersist bool
	connectSetupRequired     bool
	lookupCallCount          int
	incompleteLookupPage     bool
	lookupStarted            chan struct{}
	lookupContinue           <-chan struct{}
	createStarted            chan struct{}
	createContinue           <-chan struct{}
	cancelCreate             context.CancelFunc
}

func newFakeCommerceProvider(now time.Time) *fakeCommerceProvider {
	return &fakeCommerceProvider{
		now: now, accounts: make(map[string]commerce.ProviderAccount),
		accountByIdempotencyKey: make(map[string]string),
	}
}

func (p *fakeCommerceProvider) CreateAccount(
	ctx context.Context,
	input commerce.CreateAccountInput,
) (commerce.ProviderAccount, error) {
	if err := ctx.Err(); err != nil {
		return commerce.ProviderAccount{}, err
	}
	p.mu.Lock()
	started := p.createStarted
	continued := p.createContinue
	p.createStarted = nil
	p.createContinue = nil
	p.mu.Unlock()
	if started != nil {
		close(started)
	}
	if continued != nil {
		<-continued
	}
	if err := ctx.Err(); err != nil {
		return commerce.ProviderAccount{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.createIdempotencyKeys = append(p.createIdempotencyKeys, input.IdempotencyKey)
	if accountID, exists := p.accountByIdempotencyKey[input.IdempotencyKey]; exists {
		return p.accounts[accountID], nil
	}
	if p.connectSetupRequired {
		p.connectSetupRequired = false
		return commerce.ProviderAccount{}, commerce.ErrProviderSetupRequired
	}
	if p.failCreateWithoutPersist {
		p.failCreateWithoutPersist = false
		return commerce.ProviderAccount{}, errors.New("fake Stripe response lost before account observation")
	}
	commerceAccountID := strings.ReplaceAll(input.Metadata["boardly_commerce_account_id"], "-", "")
	if len(commerceAccountID) < 24 || input.ControllerTemplateVersion != 1 {
		return commerce.ProviderAccount{}, errors.New("invalid fake Commerce account request")
	}
	accountID := fmt.Sprintf("acct_%024d", len(p.accounts)+1)
	account := commerce.ProviderAccount{
		ID:                 accountID,
		Metadata:           cloneStringMap(input.Metadata),
		Country:            "GB",
		DefaultCurrency:    "gbp",
		CardPaymentsStatus: commerce.CapabilityStatusUnrequested,
		TransfersStatus:    commerce.CapabilityStatusUnrequested,
		Controller: commerce.ControllerSnapshot{
			LossesPayments:        "stripe",
			FeesPayer:             "account",
			RequirementCollection: "stripe",
			DashboardType:         "full",
		},
	}
	p.accounts[accountID] = account
	p.accountByIdempotencyKey[input.IdempotencyKey] = accountID
	if p.cancelCreate != nil {
		cancel := p.cancelCreate
		p.cancelCreate = nil
		cancel()
	}
	if p.failCreateAfterPersist {
		p.failCreateAfterPersist = false
		delete(p.accountByIdempotencyKey, input.IdempotencyKey)
		return commerce.ProviderAccount{}, errors.New("fake Stripe response lost after account creation")
	}
	return account, nil
}

func (p *fakeCommerceProvider) requireConnectSetupForNextCreate() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.connectSetupRequired = true
}

func (p *fakeCommerceProvider) createKeys() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.createIdempotencyKeys...)
}

func (p *fakeCommerceProvider) FindAccount(
	ctx context.Context,
	input commerce.FindAccountInput,
) (commerce.FindAccountResult, error) {
	if err := ctx.Err(); err != nil {
		return commerce.FindAccountResult{}, err
	}
	p.mu.Lock()
	p.lookupCallCount++
	started := p.lookupStarted
	continued := p.lookupContinue
	p.lookupStarted = nil
	p.lookupContinue = nil
	p.mu.Unlock()
	if started != nil {
		close(started)
	}
	if continued != nil {
		select {
		case <-continued:
		case <-ctx.Done():
			return commerce.FindAccountResult{}, ctx.Err()
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.incompleteLookupPage {
		p.incompleteLookupPage = false
		return commerce.FindAccountResult{
			HasMore: true, NextCursor: "acct_ScanCursor00000001",
		}, nil
	}
	var result commerce.FindAccountResult
	for _, account := range p.accounts {
		matches := true
		for key, value := range input.Metadata {
			if account.Metadata[key] != value {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		if result.Found {
			return commerce.FindAccountResult{}, errors.New("duplicate fake Commerce accounts")
		}
		result.Account = account
		result.Found = true
	}
	return result, nil
}

func (p *fakeCommerceProvider) RetrieveAccount(
	ctx context.Context,
	accountID string,
) (commerce.ProviderAccount, error) {
	if err := ctx.Err(); err != nil {
		return commerce.ProviderAccount{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.retrieveFailure {
		return commerce.ProviderAccount{}, errors.New("fake Commerce provider unavailable")
	}
	account, exists := p.accounts[accountID]
	if !exists {
		return commerce.ProviderAccount{}, errors.New("fake Commerce account not found")
	}
	return account, nil
}

func (p *fakeCommerceProvider) CreateAccountSession(
	ctx context.Context,
	input commerce.AccountSessionInput,
) (commerce.ProviderSession, error) {
	if err := ctx.Err(); err != nil {
		return commerce.ProviderSession{}, err
	}
	p.mu.Lock()
	if _, exists := p.accounts[input.AccountID]; !exists || input.IdempotencyKey == "" {
		p.mu.Unlock()
		return commerce.ProviderSession{}, errors.New("fake Commerce account not found")
	}
	p.credentialCallCount++
	callCount := p.credentialCallCount
	started := p.credentialStarted
	continued := p.credentialContinue
	p.mu.Unlock()
	if started != nil {
		close(started)
	}
	if continued != nil {
		select {
		case <-continued:
		case <-ctx.Done():
			return commerce.ProviderSession{}, ctx.Err()
		}
	}
	return commerce.ProviderSession{
		ClientSecret: fmt.Sprintf("account_session_secret_%d", callCount),
		ExpiresAt:    p.now.Add(time.Hour),
	}, nil
}

func (p *fakeCommerceProvider) CreateOnboardingLink(
	ctx context.Context,
	input commerce.OnboardingLinkInput,
) (commerce.ProviderLink, error) {
	if err := ctx.Err(); err != nil {
		return commerce.ProviderLink{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.accounts[input.AccountID]; !exists || input.IdempotencyKey == "" {
		return commerce.ProviderLink{}, errors.New("fake Commerce account not found")
	}
	p.credentialCallCount++
	return commerce.ProviderLink{
		URL:       fmt.Sprintf("https://connect.stripe.com/setup/s/%d", p.credentialCallCount),
		ExpiresAt: p.now.Add(time.Hour),
	}, nil
}

func (p *fakeCommerceProvider) CreateDashboardLink(
	input commerce.DashboardLinkInput,
) (commerce.ProviderDashboardLink, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	account, exists := p.accounts[input.AccountID]
	if !exists || account.Controller.DashboardType != "full" {
		return commerce.ProviderDashboardLink{}, errors.New("fake Commerce account not found")
	}
	if p.dashboardURL != "" {
		return commerce.ProviderDashboardLink{URL: p.dashboardURL}, nil
	}
	return commerce.ProviderDashboardLink{
		URL: "https://dashboard.stripe.com/test/" + input.AccountID + "/dashboard",
	}, nil
}

func (p *fakeCommerceProvider) createdAccountCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.accounts)
}

func (p *fakeCommerceProvider) uniqueCreateIdempotencyKeys() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	keys := make(map[string]struct{}, len(p.createIdempotencyKeys))
	for _, key := range p.createIdempotencyKeys {
		keys[key] = struct{}{}
	}
	return len(keys)
}

func (p *fakeCommerceProvider) createAccountCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.createIdempotencyKeys)
}

func (p *fakeCommerceProvider) accountLookupCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lookupCallCount
}

func (p *fakeCommerceProvider) returnIncompleteLookupPageOnce() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.incompleteLookupPage = true
}

func (p *fakeCommerceProvider) setCreateBarrier(started chan struct{}, continued <-chan struct{}) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.createStarted = started
	p.createContinue = continued
}

func (p *fakeCommerceProvider) credentialCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.credentialCallCount
}

func (p *fakeCommerceProvider) setCredentialBarrier(started chan struct{}, continued <-chan struct{}) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.credentialStarted = started
	p.credentialContinue = continued
}

func (p *fakeCommerceProvider) setLookupBarrier(started chan struct{}, continued <-chan struct{}) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lookupStarted = started
	p.lookupContinue = continued
}

func (p *fakeCommerceProvider) setNow(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.now = now
}

func (p *fakeCommerceProvider) setAllAccountsReady() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for accountID, account := range p.accounts {
		account.Country = "GB"
		account.DefaultCurrency = "gbp"
		account.DetailsSubmitted = true
		account.ChargesEnabled = true
		account.PayoutsEnabled = true
		account.CardPaymentsStatus = commerce.CapabilityStatusActive
		account.TransfersStatus = commerce.CapabilityStatusActive
		p.accounts[accountID] = account
	}
}

func (p *fakeCommerceProvider) setAllAccountsPayoutsDisabled() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for accountID, account := range p.accounts {
		account.PayoutsEnabled = false
		p.accounts[accountID] = account
	}
}

func (p *fakeCommerceProvider) setRetrieveFailure(enabled bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.retrieveFailure = enabled
}

func (p *fakeCommerceProvider) failNextCreateAfterPersistingAccount() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failCreateAfterPersist = true
}

func (p *fakeCommerceProvider) failNextCreateWithoutPersistingAccount() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failCreateWithoutPersist = true
}

func (p *fakeCommerceProvider) cancelAfterNextCreate(cancel context.CancelFunc) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cancelCreate = cancel
}

func cloneStringMap(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}
