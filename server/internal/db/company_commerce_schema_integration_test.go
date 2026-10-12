//go:build integration

package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

func TestCompanyCommerceSchemaEnforcesReceiptsAccountIdentityAndSafePersistence(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)

	termsDigest := make([]byte, 32)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO commerce_terms_catalog (terms_version, terms_sha256)
		VALUES ('2026-08-20', $1)`, termsDigest)
	assertStatementRejected(t, ctx, pool, "23505", `
		INSERT INTO commerce_terms_catalog (terms_version, terms_sha256)
		VALUES ('2026-08-20', $1)`, termsDigest)
	assertStatementRejected(t, ctx, pool, "23514", `
		INSERT INTO commerce_terms_catalog (terms_version, terms_sha256)
		VALUES ('2026-08-21', $1)`, make([]byte, 31))

	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO commerce_terms_acceptances (
			id, company_id, terms_version, terms_sha256, accepted_by_user_id
		) VALUES ($1, $2, '2026-08-20', $3, $4)`,
		dbtest.NewUUID(t), fixture.CompanyA, termsDigest, fixture.UserA)
	assertStatementRejected(t, ctx, pool, "23505", `
		INSERT INTO commerce_terms_acceptances (
			id, company_id, terms_version, terms_sha256, accepted_by_user_id
		) VALUES ($1, $2, '2026-08-20', $3, $4)`,
		dbtest.NewUUID(t), fixture.CompanyA, termsDigest, fixture.UserA)
	assertStatementRejected(t, ctx, pool, "23514", `
		INSERT INTO commerce_terms_acceptances (
			id, company_id, terms_version, terms_sha256, accepted_by_user_id
		) VALUES ($1, $2, '2026-08-21', $3, $4)`,
		dbtest.NewUUID(t), fixture.CompanyA, make([]byte, 31), fixture.UserA)
	wrongTermsDigest := make([]byte, 32)
	wrongTermsDigest[0] = 1
	assertStatementRejected(t, ctx, pool, "23503", `
		INSERT INTO commerce_terms_acceptances (
			id, company_id, terms_version, terms_sha256, accepted_by_user_id
		) VALUES ($1, $2, '2026-08-20', $3, $4)`,
		dbtest.NewUUID(t), fixture.CompanyB, wrongTermsDigest, fixture.UserB)

	accountID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO commerce_accounts (id, company_id, environment, created_by_user_id)
		VALUES ($1, $2, 'sandbox', $3)`, accountID, fixture.CompanyA, fixture.UserA)
	assertStatementRejected(t, ctx, pool, "23505", `
		INSERT INTO commerce_accounts (id, company_id, environment, created_by_user_id)
		VALUES ($1, $2, 'sandbox', $3)`,
		dbtest.NewUUID(t), fixture.CompanyA, fixture.UserA)
	assertStatementRejected(t, ctx, pool, "23514", `
		INSERT INTO commerce_accounts (id, company_id, environment, created_by_user_id)
		VALUES ($1, $2, 'production', $3)`,
		dbtest.NewUUID(t), fixture.CompanyB, fixture.UserB)

	operationID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO commerce_operations (
			id, company_id, commerce_account_id, environment, operation_type,
			metadata_schema_version, request_sha256, idempotency_key,
			provider_retry_deadline_at, created_by_user_id
		) VALUES (
			$1, $2, $3, 'sandbox', 'account_provisioning', 1, $4, $5,
			clock_timestamp() + interval '23 hours', $6
		)`, operationID, fixture.CompanyA, accountID, make([]byte, 32),
		"boardly:connect-account:v1:"+operationID.String(), fixture.UserA)
	assertStatementRejected(t, ctx, pool, "23505", `
		INSERT INTO commerce_operations (
			id, company_id, commerce_account_id, environment, operation_type,
			metadata_schema_version, request_sha256, idempotency_key,
			provider_retry_deadline_at, created_by_user_id
		) VALUES (
			$1, $2, $3, 'sandbox', 'account_provisioning', 1, $4, $5,
			clock_timestamp() + interval '23 hours', $6
		)`, dbtest.NewUUID(t), fixture.CompanyA, accountID, make([]byte, 32),
		"boardly:connect-account:v1:"+dbtest.NewUUID(t).String(), fixture.UserA)
	assertStatementRejected(t, ctx, pool, "23514", `
		INSERT INTO commerce_operations (
			id, company_id, commerce_account_id, environment, operation_type,
			metadata_schema_version, request_sha256, idempotency_key,
			provider_retry_deadline_at, created_by_user_id
		) VALUES (
			$1, $2, $3, 'live', 'account_provisioning', 1, $4, $5,
			clock_timestamp() + interval '23 hours', $6
		)`, dbtest.NewUUID(t), fixture.CompanyB, dbtest.NewUUID(t), make([]byte, 31),
		"boardly:connect-account:v1:"+dbtest.NewUUID(t).String(), fixture.UserB)

	dbtest.MustExec(t, ctx, pool, `
		UPDATE commerce_accounts
		SET provider_account_id='acct_schemaaccounta', status='onboarding',
			controller_losses_payments='stripe', controller_fees_payer='account',
			controller_requirement_collection='stripe', controller_dashboard_type='full',
			controller_template_version=1, reconciliation_generation=1,
			last_reconciled_at=clock_timestamp(), version=version+1
		WHERE id=$1`, accountID)
	assertCommerceOfferAndEntitlementAuthority(t, ctx, pool, fixture, accountID)
	assertCommerceSiteAuthorityIsUniqueOnlyWhenActive(t, ctx, pool, fixture, accountID)
	assertCommerceSiteSigningKeysAndChallenges(
		t,
		ctx,
		pool,
		fixture,
		accountID,
	)
	assertStatementRejected(t, ctx, pool, "23505", `
		INSERT INTO commerce_accounts (
			id, company_id, environment, provider_account_id, status,
			controller_losses_payments, controller_fees_payer,
			controller_requirement_collection, controller_dashboard_type,
			controller_template_version, reconciliation_generation,
			last_reconciled_at, created_by_user_id
		) VALUES (
			$1, $2, 'sandbox', 'acct_schemaaccounta', 'onboarding',
			'stripe', 'account', 'stripe', 'full', 1, 1,
			clock_timestamp(), $3
		)`,
		dbtest.NewUUID(t), fixture.CompanyB, fixture.UserB)

	assertCommerceProjectionVersionShape(t, ctx, pool)
	assertCommerceProjectionEventClocks(t, ctx, pool)
	assertCommerceEntityIDsAreDatabaseGenerated(t, ctx, pool)
	assertCommerceForeignKeys(t, ctx, pool)
	assertCommerceAuthorityForeignKeyColumns(t, ctx, pool)
	assertCommercePaymentSubscriptionIndex(t, ctx, pool)
	assertCommerceTablesPersistNoProviderSecretsOrRecipientFinancialData(t, ctx, pool)
}

func assertCommerceSiteSigningKeysAndChallenges(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture dbtest.Fixture,
	accountID uuid.UUID,
) {
	t.Helper()
	siteID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO commerce_sites (
			id, company_id, commerce_account_id, environment, site_alias, origin,
			verification_token_sha256, portal_return_path, created_by_user_id
		) VALUES (
			$1, $2, $3, 'sandbox', 'site-signed-bound', 'http://localhost:5173',
			$4, '/billing', $5
		)`, siteID, fixture.CompanyA, accountID, make([]byte, 32), fixture.UserA)
	fingerprint := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO commerce_site_signing_keys (
			company_id, site_id, environment, key_id, fingerprint, public_jwk
		) VALUES ($1, $2, 'sandbox', $3, $3, '{}')`,
		fixture.CompanyA, siteID, fingerprint)
	assertStatementRejected(t, ctx, pool, "23505", `
		INSERT INTO commerce_site_signing_keys (
			company_id, site_id, environment, key_id, fingerprint, public_jwk
		) VALUES ($1, $2, 'sandbox', $3, $3, '{}')`,
		fixture.CompanyA, siteID, "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB")
	operationID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO commerce_site_challenges (
			company_id, site_id, environment, action, operation_id,
			nonce_sha256, expires_at
		) VALUES ($1, $2, 'sandbox', 'checkout', $3, $4, clock_timestamp() + interval '1 minute')`,
		fixture.CompanyA, siteID, operationID, make([]byte, 32))
	assertStatementRejected(t, ctx, pool, "23505", `
		INSERT INTO commerce_site_challenges (
			company_id, site_id, environment, action, operation_id,
			nonce_sha256, expires_at
		) VALUES ($1, $2, 'sandbox', 'checkout', $3, $4, clock_timestamp() + interval '1 minute')`,
		fixture.CompanyA, siteID, operationID, append([]byte{1}, make([]byte, 31)...))
	assertStatementRejected(t, ctx, pool, "23514", `
		INSERT INTO commerce_site_challenges (
			company_id, site_id, environment, action, operation_id,
			nonce_sha256, expires_at
		) VALUES ($1, $2, 'sandbox', 'portal', $3, $4, clock_timestamp() + interval '1 minute')`,
		fixture.CompanyA, siteID, dbtest.NewUUID(t), make([]byte, 31))
}

func assertCommerceOfferAndEntitlementAuthority(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture dbtest.Fixture,
	accountID uuid.UUID,
) {
	t.Helper()
	siteID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO commerce_sites (
			id, company_id, commerce_account_id, environment, site_alias,
			origin, verification_token_sha256, portal_return_path, created_by_user_id
		) VALUES (
			$1, $2, $3, 'sandbox', 'entitlement-authority',
			'https://entitlements.example', $4, '/account', $5
		)`, siteID, fixture.CompanyA, accountID, make([]byte, 32), fixture.UserA)
	manifestID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO commerce_manifests (
			id, company_id, site_id, task_id, run_id, request_key,
			schema_version, canonical_json, digest
		) VALUES ($1, $2, $3, $4, $5, $6, 2, '{}', $7)`,
		manifestID, fixture.CompanyA, siteID, fixture.TaskA, fixture.RunA,
		dbtest.NewUUID(t), make([]byte, 32))
	assertStatementRejected(t, ctx, pool, "23514", `
		INSERT INTO commerce_offers (
			company_id, site_id, manifest_id, environment, alias, display_name,
			offer_type, entitlement_key, amount, currency,
			success_path, cancel_path
		) VALUES (
			$1, $2, $3, 'sandbox', 'missing-billing', 'Missing billing',
			'subscription', 'reports.pro', 1299, 'usd', '/ok', '/cancel'
		)`, fixture.CompanyA, siteID, manifestID)

	offerID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO commerce_offers (
			id, company_id, site_id, manifest_id, environment, alias, display_name,
			offer_type, plan_group_alias, entitlement_key, amount, currency,
			recurring_interval, success_path, cancel_path
		) VALUES (
			$1, $2, $3, $4, 'sandbox', 'pro-month', 'Pro monthly',
			'subscription', 'pro', 'reports.pro', 1299, 'usd',
			'month', '/ok', '/cancel'
		)`, offerID, fixture.CompanyA, siteID, manifestID)
	customerID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO commerce_customers (
			id, company_id, site_id, environment, subject_hmac
		) VALUES ($1, $2, $3, 'sandbox', $4)`,
		customerID, fixture.CompanyA, siteID, make([]byte, 32))
	subscriptionA, subscriptionB := dbtest.NewUUID(t), dbtest.NewUUID(t)
	for index, subscriptionID := range []uuid.UUID{subscriptionA, subscriptionB} {
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO commerce_subscriptions (
				id, company_id, site_id, customer_id, offer_id, environment,
				provider_subscription_id, status, provider_event_at
			) VALUES ($1, $2, $3, $4, $5, 'sandbox', $6, 'active', clock_timestamp())`,
			subscriptionID, fixture.CompanyA, siteID, customerID, offerID,
			[]string{"sub_authoritya1", "sub_authorityb2"}[index])
	}
	paymentA, paymentB := dbtest.NewUUID(t), dbtest.NewUUID(t)
	for _, payment := range []struct {
		id               uuid.UUID
		subscriptionID   uuid.UUID
		paymentIntentID  string
		invoicePaymentID string
		invoiceID        string
	}{
		{
			id: paymentA, subscriptionID: subscriptionA,
			paymentIntentID: "pi_authoritya1", invoicePaymentID: "inpay_authoritya1",
			invoiceID: "in_authoritya1",
		},
		{
			id: paymentB, subscriptionID: subscriptionB,
			paymentIntentID: "pi_authorityb2", invoicePaymentID: "inpay_authorityb2",
			invoiceID: "in_authorityb2",
		},
	} {
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO commerce_payments (
				id, company_id, site_id, customer_id, offer_id, subscription_id,
				environment, provider_payment_intent_id, provider_invoice_payment_id,
				provider_invoice_id, status, amount, currency, provider_event_at
			) VALUES (
				$1, $2, $3, $4, $5, $6, 'sandbox', $7, $8, $9, 'paid', 1299, 'usd',
				clock_timestamp()
			)`, payment.id, fixture.CompanyA, siteID, customerID, offerID,
			payment.subscriptionID, payment.paymentIntentID, payment.invoicePaymentID,
			payment.invoiceID)
	}
	assertStatementRejected(t, ctx, pool, "23503", `
		INSERT INTO commerce_entitlements (
			company_id, site_id, customer_id, offer_id, payment_id,
			subscription_id, grant_id, entitlement_key, source_type,
			status, provider_event_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $6, 'reports.wrong',
			'subscription', 'active', clock_timestamp()
		)`, fixture.CompanyA, siteID, customerID, offerID, paymentA, subscriptionA)
	assertStatementRejected(t, ctx, pool, "23503", `
		INSERT INTO commerce_entitlements (
			company_id, site_id, customer_id, offer_id, payment_id,
			subscription_id, grant_id, entitlement_key, source_type,
			status, provider_event_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $6, 'reports.pro',
			'subscription', 'active', clock_timestamp()
		)`, fixture.CompanyA, siteID, customerID, offerID, paymentB, subscriptionA)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO commerce_entitlements (
			company_id, site_id, customer_id, offer_id, payment_id,
			subscription_id, grant_id, entitlement_key, source_type,
			status, provider_event_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $6, 'reports.pro',
			'subscription', 'active', clock_timestamp()
		)`, fixture.CompanyA, siteID, customerID, offerID, paymentA, subscriptionA)
}

func assertCommerceSiteAuthorityIsUniqueOnlyWhenActive(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture dbtest.Fixture,
	accountA uuid.UUID,
) {
	t.Helper()
	accountB := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO commerce_accounts (id, company_id, environment, created_by_user_id)
		VALUES ($1, $2, 'sandbox', $3)`, accountB, fixture.CompanyB, fixture.UserB)
	siteA, siteB := dbtest.NewUUID(t), dbtest.NewUUID(t)
	for _, site := range []struct {
		id        uuid.UUID
		companyID uuid.UUID
		accountID uuid.UUID
		taskID    uuid.UUID
		runID     uuid.UUID
		alias     string
		userID    uuid.UUID
	}{
		{siteA, fixture.CompanyA, accountA, fixture.TaskA, fixture.RunA, "authority-a", fixture.UserA},
		{siteB, fixture.CompanyB, accountB, fixture.TaskB, fixture.RunB, "authority-b", fixture.UserB},
	} {
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO commerce_sites (
				id, company_id, commerce_account_id, environment, site_alias,
				origin, verification_token_sha256, portal_return_path, created_by_user_id
			) VALUES (
				$1, $2, $3, 'sandbox', $4,
				'https://shared.example', $5, '/account', $6
			)`, site.id, site.companyID, site.accountID, site.alias, make([]byte, 32), site.userID)
		manifestID := dbtest.NewUUID(t)
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO commerce_manifests (
				id, company_id, site_id, task_id, run_id, request_key,
				schema_version, canonical_json, digest, state
			) VALUES ($1, $2, $3, $4, $5, $6, 2, '{}', $7, 'active')`,
			manifestID, site.companyID, site.id, site.taskID, site.runID,
			dbtest.NewUUID(t), make([]byte, 32))
		dbtest.MustExec(t, ctx, pool, `
			UPDATE commerce_sites SET active_manifest_id=$1 WHERE id=$2`,
			manifestID, site.id)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE commerce_sites SET status='active' WHERE id=$1`, siteA)
	assertStatementRejected(t, ctx, pool, "23505", `
		UPDATE commerce_sites SET status='active' WHERE id=$1`, siteB)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE commerce_sites
		SET origin='http://localhost:5173'
		WHERE id=$1`, siteA)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE commerce_sites
		SET origin='http://localhost:5173'
		WHERE id=$1`, siteB)
	dbtest.MustExec(t, ctx, pool, `UPDATE commerce_sites SET status='active' WHERE id=$1`, siteB)
}

func assertCommerceProjectionEventClocks(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) {
	t.Helper()
	var requiredClocks int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM information_schema.columns
		WHERE table_schema='public'
		  AND table_name IN (
			'commerce_payments', 'commerce_subscriptions', 'commerce_entitlements'
		  )
		  AND column_name='provider_event_at'
		  AND is_nullable='NO'`).Scan(&requiredClocks); err != nil {
		t.Fatalf("inspect Commerce provider event clocks: %v", err)
	}
	if requiredClocks != 3 {
		t.Fatalf("Commerce provider event clocks = %d, want 3", requiredClocks)
	}
}

func assertCommerceProjectionVersionShape(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) {
	t.Helper()
	var mutableVersionColumns, immutableVersionColumns int
	if err := pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE table_name IN (
				'commerce_accounts', 'commerce_operations', 'commerce_sites',
				'commerce_site_signing_keys',
				'commerce_manifests', 'commerce_offers', 'commerce_customers',
				'commerce_gateway_operations', 'commerce_payments',
				'commerce_subscriptions', 'commerce_entitlements',
				'commerce_webhook_receipts'
			)),
			count(*) FILTER (
				WHERE table_name IN ('commerce_terms_catalog', 'commerce_terms_acceptances')
			)
		FROM information_schema.columns
		WHERE table_schema='public' AND column_name='version'
		  AND table_name LIKE 'commerce_%'`).Scan(
		&mutableVersionColumns,
		&immutableVersionColumns,
	); err != nil {
		t.Fatalf("inspect Commerce version columns: %v", err)
	}
	if mutableVersionColumns != 12 || immutableVersionColumns != 0 {
		t.Fatalf(
			"Commerce version columns = mutable:%d immutable:%d, want mutable:12 immutable:0",
			mutableVersionColumns,
			immutableVersionColumns,
		)
	}
}

func assertCommerceEntityIDsAreDatabaseGenerated(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT table_name, column_default
		FROM information_schema.columns
		WHERE table_schema='public'
		  AND column_name='id'
		  AND table_name = ANY($1::text[])
		ORDER BY table_name`, []string{
		"commerce_terms_acceptances",
		"commerce_accounts",
		"commerce_operations",
		"commerce_sites",
		"commerce_site_signing_keys",
		"commerce_site_challenges",
		"commerce_manifests",
		"commerce_offers",
		"commerce_customers",
		"commerce_gateway_operations",
		"commerce_payments",
		"commerce_subscriptions",
		"commerce_entitlements",
		"commerce_webhook_receipts",
	})
	if err != nil {
		t.Fatalf("inspect Commerce entity IDs: %v", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var table, columnDefault string
		if err := rows.Scan(&table, &columnDefault); err != nil {
			t.Fatalf("scan Commerce entity ID: %v", err)
		}
		count++
		if columnDefault != "gen_random_uuid()" {
			t.Errorf("%s.id default = %q, want gen_random_uuid()", table, columnDefault)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate Commerce entity IDs: %v", err)
	}
	if count != 14 {
		t.Fatalf("Commerce entity ID columns = %d, want 14", count)
	}
}

func assertCommerceForeignKeys(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) {
	t.Helper()
	want := map[string]string{
		"approvals_commerce_manifest_fkey":                "c",
		"commerce_terms_acceptances_company_fkey":         "c",
		"commerce_terms_acceptances_terms_fkey":           "a",
		"commerce_accounts_company_fkey":                  "c",
		"commerce_operations_account_fkey":                "c",
		"commerce_site_signing_keys_site_fkey":            "c",
		"commerce_site_challenges_site_fkey":              "c",
		"commerce_sites_account_fkey":                     "c",
		"commerce_sites_active_manifest_fkey":             "n",
		"commerce_manifests_site_fkey":                    "c",
		"commerce_offers_site_fkey":                       "c",
		"commerce_offers_manifest_fkey":                   "c",
		"commerce_customers_site_fkey":                    "c",
		"commerce_rate_limits_site_fkey":                  "c",
		"commerce_gateway_operations_site_fkey":           "c",
		"commerce_gateway_operations_customer_fkey":       "n",
		"commerce_gateway_operations_manifest_fkey":       "n",
		"commerce_gateway_operations_offer_fkey":          "n",
		"commerce_payments_customer_fkey":                 "c",
		"commerce_payments_offer_fkey":                    "a",
		"commerce_payments_subscription_fkey":             "n",
		"commerce_subscriptions_customer_fkey":            "c",
		"commerce_subscriptions_offer_fkey":               "a",
		"commerce_entitlements_customer_fkey":             "c",
		"commerce_entitlements_offer_fkey":                "a",
		"commerce_entitlements_payment_fkey":              "a",
		"commerce_entitlements_subscription_fkey":         "a",
		"commerce_entitlements_subscription_payment_fkey": "a",
		"commerce_webhook_account_leases_account_fkey":    "c",
		"commerce_webhook_receipts_account_fkey":          "c",
	}
	rows, err := pool.Query(ctx, `
		SELECT conname, confdeltype::text
		FROM pg_constraint
		WHERE contype='f'
		  AND (
			conrelid::regclass::text LIKE 'commerce_%'
			OR conname='approvals_commerce_manifest_fkey'
		  )
		ORDER BY conname`)
	if err != nil {
		t.Fatalf("inspect Commerce foreign keys: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, deleteAction string
		if err := rows.Scan(&name, &deleteAction); err != nil {
			t.Fatalf("scan Commerce foreign key: %v", err)
		}
		wantDeleteAction, ok := want[name]
		if !ok {
			t.Errorf("unexpected Commerce foreign key %q", name)
			continue
		}
		if deleteAction != wantDeleteAction {
			t.Errorf(
				"Commerce foreign key %s delete action = %q, want %q",
				name,
				deleteAction,
				wantDeleteAction,
			)
		}
		delete(want, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate Commerce foreign keys: %v", err)
	}
	for name := range want {
		t.Errorf("missing Commerce foreign key %q", name)
	}
}

func assertCommerceAuthorityForeignKeyColumns(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) {
	t.Helper()
	want := map[string][2]string{
		"approvals_commerce_manifest_fkey": {
			"company_id,commerce_manifest_id", "company_id,id",
		},
		"commerce_terms_acceptances_terms_fkey": {
			"terms_version,terms_sha256", "terms_version,terms_sha256",
		},
		"commerce_sites_account_fkey": {
			"company_id,commerce_account_id,environment", "company_id,id,environment",
		},
		"commerce_site_signing_keys_site_fkey": {
			"company_id,site_id,environment", "company_id,id,environment",
		},
		"commerce_site_challenges_site_fkey": {
			"company_id,site_id,environment", "company_id,id,environment",
		},
		"commerce_entitlements_offer_fkey": {
			"company_id,site_id,offer_id,source_type,entitlement_key",
			"company_id,site_id,id,offer_type,entitlement_key",
		},
		"commerce_entitlements_subscription_fkey": {
			"company_id,site_id,customer_id,subscription_id",
			"company_id,site_id,customer_id,id",
		},
		"commerce_entitlements_subscription_payment_fkey": {
			"company_id,site_id,customer_id,offer_id,subscription_id,payment_id",
			"company_id,site_id,customer_id,offer_id,subscription_id,id",
		},
		"commerce_payments_subscription_fkey": {
			"company_id,site_id,customer_id,subscription_id",
			"company_id,site_id,customer_id,id",
		},
		"commerce_webhook_account_leases_account_fkey": {
			"company_id,environment,provider_account_id",
			"company_id,environment,provider_account_id",
		},
		"commerce_webhook_receipts_account_fkey": {
			"company_id,environment,provider_account_id",
			"company_id,environment,provider_account_id",
		},
	}
	for name, columns := range want {
		var localColumns, referencedColumns string
		err := pool.QueryRow(ctx, `
			SELECT
				string_agg(local_attribute.attname, ',' ORDER BY keys.position),
				string_agg(referenced_attribute.attname, ',' ORDER BY keys.position)
			FROM pg_constraint AS constraint_row
			CROSS JOIN LATERAL unnest(
				constraint_row.conkey, constraint_row.confkey
			) WITH ORDINALITY AS keys(local_number, referenced_number, position)
			JOIN pg_attribute AS local_attribute
			  ON local_attribute.attrelid=constraint_row.conrelid
			 AND local_attribute.attnum=keys.local_number
			JOIN pg_attribute AS referenced_attribute
			  ON referenced_attribute.attrelid=constraint_row.confrelid
			 AND referenced_attribute.attnum=keys.referenced_number
			WHERE constraint_row.contype='f' AND constraint_row.conname=$1
			GROUP BY constraint_row.oid`, name).Scan(&localColumns, &referencedColumns)
		if err != nil {
			t.Fatalf("inspect Commerce foreign key %s columns: %v", name, err)
		}
		if localColumns != columns[0] || referencedColumns != columns[1] {
			t.Errorf(
				"Commerce foreign key %s columns = (%s)->(%s), want (%s)->(%s)",
				name,
				localColumns,
				referencedColumns,
				columns[0],
				columns[1],
			)
		}
	}
}

func assertCommercePaymentSubscriptionIndex(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) {
	t.Helper()
	var columns, predicate string
	err := pool.QueryRow(ctx, `
		SELECT
			string_agg(attribute.attname, ',' ORDER BY keys.position),
			pg_get_expr(index_row.indpred, index_row.indrelid)
		FROM pg_class AS index_class
		JOIN pg_index AS index_row ON index_row.indexrelid=index_class.oid
		JOIN pg_class AS table_class ON table_class.oid=index_row.indrelid
		CROSS JOIN LATERAL unnest(index_row.indkey)
			WITH ORDINALITY AS keys(attribute_number, position)
		JOIN pg_attribute AS attribute
		  ON attribute.attrelid=table_class.oid
		 AND attribute.attnum=keys.attribute_number
		WHERE index_class.relname='commerce_payments_subscription_idx'
		  AND keys.position <= index_row.indnkeyatts
		GROUP BY index_row.indpred, index_row.indrelid`).Scan(&columns, &predicate)
	if err != nil {
		t.Fatalf("inspect Commerce payment subscription index: %v", err)
	}
	if columns != "company_id,site_id,customer_id,subscription_id" ||
		predicate != "(subscription_id IS NOT NULL)" {
		t.Fatalf(
			"Commerce payment subscription index = %q WHERE %q",
			columns,
			predicate,
		)
	}
}

func assertCommerceTablesPersistNoProviderSecretsOrRecipientFinancialData(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) {
	t.Helper()
	for _, column := range []string{
		"secret",
		"secret_key",
		"api_key",
		"client_secret",
		"account_session_client_secret",
		"access_token",
		"refresh_token",
		"bank_account",
		"bank_account_id",
		"account_number",
		"routing_number",
		"external_account_id",
		"tax_id",
		"ssn",
		"date_of_birth",
		"dob",
		"kyc",
		"kyc_payload",
		"identity_document",
		"webhook_payload",
		"raw_payload",
	} {
		var count int
		if err := pool.QueryRow(ctx, `
			SELECT count(*)
			FROM information_schema.columns
			WHERE table_schema='public'
				  AND table_name IN (
					'commerce_accounts', 'commerce_operations',
					'commerce_terms_catalog', 'commerce_terms_acceptances',
					'commerce_sites', 'commerce_site_signing_keys',
					'commerce_site_challenges', 'commerce_manifests', 'commerce_offers',
					'commerce_customers', 'commerce_rate_limits',
					'commerce_gateway_operations',
					'commerce_payments', 'commerce_subscriptions',
					'commerce_entitlements', 'commerce_webhook_account_leases',
					'commerce_webhook_receipts'
			  )
			  AND column_name=$1`, column).Scan(&count); err != nil {
			t.Fatalf("inspect forbidden Commerce column %q: %v", column, err)
		}
		if count != 0 {
			t.Errorf("Commerce schema persists forbidden column %q", column)
		}
	}
}
