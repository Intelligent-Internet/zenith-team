package commerce

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func TestManifestVerificationDocumentIsExactAndProviderFree(t *testing.T) {
	t.Parallel()
	document := ManifestDocument{Manifest: Manifest{
		Site: ManifestSite{Alias: "acme-store"},
	}}
	for index := range document.Digest {
		document.Digest[index] = byte(index)
	}
	raw, err := verificationDocument(document)
	if err != nil {
		t.Fatalf("verificationDocument() error = %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode verification document: %v", err)
	}
	if len(decoded) != 3 || decoded["schemaVersion"] != float64(2) ||
		decoded["siteAlias"] != "acme-store" ||
		decoded["manifestDigest"] != "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f" {
		t.Fatalf("verification document = %s", raw)
	}
	for _, forbidden := range []string{"price_", "prod_", "acct_", "sk_", "rk_"} {
		if containsString(string(raw), forbidden) {
			t.Fatalf("verification document contains %q: %s", forbidden, raw)
		}
	}
}

func TestManifestProductGroupsShareOnlySubscriptionProducts(t *testing.T) {
	t.Parallel()
	offers := []ManifestOffer{
		{Alias: "report", Type: OfferTypeOneTime, DisplayName: "Report"},
		{Alias: "pro-month", Type: OfferTypeSubscription, PlanGroup: "pro", DisplayName: "Pro"},
		{Alias: "pro-year", Type: OfferTypeSubscription, PlanGroup: "pro", DisplayName: "Pro"},
	}
	groups := manifestProductGroups(offers)
	if len(groups) != 2 || groups[0].key != "one_time:report" ||
		groups[1].key != "subscription:pro" || len(groups[1].offerAliases) != 2 {
		t.Fatalf("manifestProductGroups() = %#v", groups)
	}
}

func TestCommerceSiteMatchesManifestBindingRejectsDrift(t *testing.T) {
	t.Parallel()
	companyID := uuid.MustParse("01991d2f-cd80-7901-9f96-b6b88ca64001")
	accountID := uuid.MustParse("01991d2f-cd80-7901-9f96-b6b88ca64002")
	manifestSite := ManifestSite{
		Alias: "store", Origin: "https://store.example", PortalReturnPath: "/billing",
	}
	site := generated.CommerceSite{
		ID:        uuid.MustParse("01991d2f-cd80-7901-9f96-b6b88ca64003"),
		CompanyID: companyID, CommerceAccountID: accountID,
		Environment: string(EnvironmentSandbox), SiteAlias: "store",
		Origin: "https://store.example", PortalReturnPath: "/billing",
	}
	if !commerceSiteMatchesManifestBinding(
		site,
		companyID,
		accountID,
		EnvironmentSandbox,
		manifestSite,
	) {
		t.Fatal("matching Site was rejected")
	}
	for _, mutate := range []func(*generated.CommerceSite){
		func(value *generated.CommerceSite) { value.SiteAlias = "other" },
		func(value *generated.CommerceSite) { value.Origin = "https://other.example" },
		func(value *generated.CommerceSite) { value.PortalReturnPath = "/account" },
	} {
		drifted := site
		mutate(&drifted)
		if commerceSiteMatchesManifestBinding(
			drifted,
			companyID,
			accountID,
			EnvironmentSandbox,
			manifestSite,
		) {
			t.Fatal("drifted Site binding was accepted")
		}
	}
}

func TestApplyCatalogReplaysPersistedPortalWithoutProviderWrite(t *testing.T) {
	t.Parallel()
	service := &DefaultCommerceService{}
	portal, err := service.applyCatalog(context.Background(), nil, catalogPublishState{
		operation: generated.CommerceGatewayOperation{
			ProviderResourceID: pgtype.Text{String: "bpc_PersistedPortal123", Valid: true},
		},
	})
	if err != nil || portal.ID != "bpc_PersistedPortal123" {
		t.Fatalf("applyCatalog(replay) = %+v, %v", portal, err)
	}
}

func TestVerifyCatalogSiteAuthorityUsesExactHTTPSProof(t *testing.T) {
	t.Parallel()
	authority := &recordingSiteAuthority{}
	service := &DefaultCommerceService{siteAuthority: authority}
	digest := make([]byte, 32)
	state := catalogPublishState{
		manifest: generated.CommerceManifest{Digest: digest},
		site: generated.CommerceSite{
			ID:          uuid.MustParse("01991d2f-cd80-7901-9f96-b6b88ca64004"),
			Environment: string(EnvironmentLive), Origin: "https://store.example",
		},
		document: ManifestDocument{Manifest: Manifest{Site: ManifestSite{
			Alias: "store", Origin: "https://store.example",
		}}},
	}
	verified, err := service.verifyCatalogSiteAuthority(t.Context(), state)
	if err != nil || len(verified.SigningKeys) != 1 || authority.calls != 1 ||
		authority.input.Origin != "https://store.example" || authority.input.SiteAlias != "store" ||
		authority.input.ManifestDigest != "0000000000000000000000000000000000000000000000000000000000000000" {
		t.Fatalf("verifyCatalogSiteAuthority() = %+v, %v / authority=%+v", verified, err, authority)
	}
}

func TestIsSiteAuthorityConflictOnlyAcceptsOriginIndex(t *testing.T) {
	t.Parallel()
	tests := []struct {
		err        error
		isConflict bool
	}{
		{
			err:        &pgconn.PgError{Code: "23505", ConstraintName: "commerce_sites_active_origin_key"},
			isConflict: true,
		},
		{err: &pgconn.PgError{Code: "23505", ConstraintName: "commerce_manifests_active_site_key"}},
		{err: &pgconn.PgError{Code: "23514", ConstraintName: "commerce_sites_active_origin_key"}},
	}
	for _, test := range tests {
		if got := isSiteAuthorityConflict(test.err); got != test.isConflict {
			t.Fatalf("isSiteAuthorityConflict(%v) = %t, want %t", test.err, got, test.isConflict)
		}
	}
}

func TestIsPostgresDeadlockOnlyAcceptsDeadlockSQLState(t *testing.T) {
	t.Parallel()
	if !isPostgresDeadlock(&pgconn.PgError{Code: "40P01"}) ||
		isPostgresDeadlock(&pgconn.PgError{Code: "40001"}) {
		t.Fatal("isPostgresDeadlock() accepted the wrong SQLSTATE")
	}
}

func containsString(value, fragment string) bool {
	return strings.Contains(value, fragment)
}

type recordingSiteAuthority struct {
	calls int
	input SiteVerificationInput
}

func (authority *recordingSiteAuthority) VerifySite(
	_ context.Context,
	input SiteVerificationInput,
) (VerifiedSite, error) {
	authority.calls++
	authority.input = input
	return VerifiedSite{SigningKeys: []SitePublicJWK{{KeyID: "test"}}}, nil
}
