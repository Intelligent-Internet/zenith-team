package commerce

import (
	"bytes"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func TestAccountProvisioningIntentUsesMinimalImmutableProviderMetadata(t *testing.T) {
	t.Parallel()
	intent := newAccountProvisioningIntent(
		uuid.MustParse("0198c09b-4600-7000-8000-000000000011"),
		uuid.MustParse("0198c09b-4600-7000-8000-000000000012"),
		uuid.MustParse("0198c09b-4600-7000-8000-000000000013"),
		EnvironmentSandbox,
		uuid.MustParse("0198c09b-4600-7000-8000-000000000014"),
		time.Date(2026, time.August, 20, 8, 0, 0, 0, time.UTC),
	)
	metadata := intent.metadata()
	if len(metadata) != 5 || metadata["boardly_metadata_schema"] != "commerce_account_v1" ||
		metadata["boardly_operation_type"] != "account_provisioning" ||
		metadata["boardly_operation_id"] != intent.operationID.String() ||
		metadata["boardly_commerce_account_id"] != intent.accountID.String() ||
		len(metadata["boardly_request_sha256"]) != 64 {
		t.Fatalf("account provisioning metadata = %#v", metadata)
	}
	if _, exists := metadata["boardly_company_id"]; exists {
		t.Fatalf("account provisioning metadata leaked Company identity: %#v", metadata)
	}
	if _, exists := metadata["boardly_environment"]; exists {
		t.Fatalf("account provisioning metadata duplicated API mode: %#v", metadata)
	}

	operation := provisioningOperationRow(intent)
	recovered := accountProvisioningIntentFromOperation(operation)
	if recovered.operationID != intent.operationID ||
		!bytes.Equal(recovered.requestSHA[:], intent.requestSHA[:]) {
		t.Fatalf("recovered provisioning intent = %#v", recovered)
	}
}

func TestAccountProvisioningIntentAcceptsOnlyCanonicalSetupRetryIdempotencyKeys(t *testing.T) {
	t.Parallel()

	intent := newAccountProvisioningRequest(
		uuid.MustParse("0198c09b-4600-7000-8000-000000000011"),
		uuid.MustParse("0198c09b-4600-7000-8000-000000000012"),
		uuid.MustParse("0198c09b-4600-7000-8000-000000000013"),
		EnvironmentSandbox,
		uuid.MustParse("0198c09b-4600-7000-8000-000000000014"),
	)
	tests := []struct {
		name                 string
		key                  string
		providerAttemptCount int32
		want                 bool
	}{
		{name: "initial key", key: intent.idempotencyKey(), want: true},
		{
			name: "current setup retry",
			key:  intent.setupRetryIdempotencyKey(2), providerAttemptCount: 2, want: true,
		},
		{
			name: "earlier setup retry",
			key:  intent.setupRetryIdempotencyKey(1), providerAttemptCount: 2, want: true,
		},
		{
			name: "future setup retry",
			key:  intent.setupRetryIdempotencyKey(3), providerAttemptCount: 2,
		},
		{
			name: "noncanonical setup retry",
			key:  intent.idempotencyKey() + ":setup-retry:01", providerAttemptCount: 1,
		},
		{
			name: "unknown suffix",
			key:  intent.idempotencyKey() + ":retry:1", providerAttemptCount: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := intent.validIdempotencyKey(test.key, test.providerAttemptCount); got != test.want {
				t.Fatalf(
					"validIdempotencyKey(%q, %d) = %t, want %t",
					test.key,
					test.providerAttemptCount,
					got,
					test.want,
				)
			}
		})
	}
}

func TestExactProviderProjectionMatchRejectsProviderIDOnlyResolution(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.August, 20, 8, 0, 0, 0, time.UTC)
	account := validProviderAccount()
	projection := projectionParams(account, map[string]struct{}{"GB": {}})
	actual := validCommerceAccountRow(now)
	before := actual
	before.ProviderAccountID = pgtype.Text{}
	before.Status = string(StatusProvisioning)
	before.CardPaymentsStatus = pgtype.Text{}
	before.TransfersStatus = pgtype.Text{}
	before.ControllerLossesPayments = pgtype.Text{}
	before.ControllerFeesPayer = pgtype.Text{}
	before.ControllerRequirementCollection = pgtype.Text{}
	before.ControllerDashboardType = pgtype.Text{}
	before.ControllerTemplateVersion = pgtype.Int4{}
	before.ReconciliationGeneration = 0
	before.LastReconciledAt = pgtype.Timestamptz{}
	before.Version = 1

	if !exactProviderProjectionMatches(actual, before, account, projection) {
		t.Fatalf("exactProviderProjectionMatches(valid) = false; actual = %#v", actual)
	}
	actual.Status = string(StatusReady)
	if exactProviderProjectionMatches(actual, before, account, projection) {
		t.Fatal("exactProviderProjectionMatches() accepted provider ID with stale projection")
	}
}

func provisioningOperationRow(intent accountProvisioningIntent) generated.CommerceOperation {
	return generated.CommerceOperation{
		ID:                    intent.operationID,
		CompanyID:             intent.companyID,
		CommerceAccountID:     intent.accountID,
		Environment:           string(intent.environment),
		OperationType:         accountProvisioningOperationType,
		MetadataSchemaVersion: accountMetadataSchemaVersion,
		State:                 operationStatePending,
		RequestSha256:         append([]byte(nil), intent.requestSHA[:]...),
		IdempotencyKey:        intent.idempotencyKey(),
		ProviderRetryDeadlineAt: pgtype.Timestamptz{
			Time: intent.createdAt.Add(accountProviderRetryHorizon), Valid: true,
		},
		CreatedByUserID: intent.createdBy,
		CreatedAt:       pgtype.Timestamptz{Time: intent.createdAt, Valid: true},
		UpdatedAt:       pgtype.Timestamptz{Time: intent.createdAt, Valid: true},
		Version:         1,
	}
}
