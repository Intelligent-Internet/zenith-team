package commerce

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func TestCustomerSubjectHMACIsSiteScopedAndDoesNotContainSubject(t *testing.T) {
	t.Parallel()
	key := bytes.Repeat([]byte{0x42}, 32)
	siteA := uuid.MustParse("0198c09b-4600-7000-8000-000000000011")
	siteB := uuid.MustParse("0198c09b-4600-7000-8000-000000000012")
	left := customerSubjectHMAC(key, siteA, "site-user-123")
	replay := customerSubjectHMAC(key, siteA, "site-user-123")
	right := customerSubjectHMAC(key, siteB, "site-user-123")
	if len(left) != 32 || !bytes.Equal(left, replay) || bytes.Equal(left, right) ||
		bytes.Contains(left, []byte("site-user-123")) {
		t.Fatalf("customerSubjectHMAC() left=%x replay=%x right=%x", left, replay, right)
	}
}

func TestGatewayOperationDigestBindsCustomerAndOffer(t *testing.T) {
	t.Parallel()
	siteID := uuid.MustParse("0198c09b-4600-7000-8000-000000000011")
	customerID := uuid.MustParse("0198c09b-4600-7000-8000-000000000012")
	offerID := uuid.MustParse("0198c09b-4600-7000-8000-000000000013")
	base := gatewayOperationDigest("checkout", siteID, customerID, offerID)
	if base == gatewayOperationDigest("portal", siteID, customerID, offerID) ||
		base == gatewayOperationDigest("checkout", siteID, customerID, uuid.Nil) ||
		base == gatewayOperationDigest("checkout", siteID, uuid.Nil, offerID) {
		t.Fatal("gateway operation digest is not bound to its exact authority")
	}
}

func TestGatewayOperationDispositionNeverBlindlyRepostsOldProviderWrites(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.August, 21, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		row     generated.CommerceGatewayOperation
		want    gatewayOperationAction
		wantErr error
	}{
		{
			name: "new operation can write",
			row: generated.CommerceGatewayOperation{
				State: "pending", CreatedAt: pgtype.Timestamptz{
					Time: now.Add(-time.Minute), Valid: true,
				},
			},
			want: gatewayOperationWrite,
		},
		{
			name: "recent ambiguous write can reuse Stripe idempotency",
			row: generated.CommerceGatewayOperation{
				State: "pending", ProviderAttemptCount: 1, CreatedAt: pgtype.Timestamptz{
					Time: now.Add(-time.Hour), Valid: true,
				},
			},
			want: gatewayOperationWrite,
		},
		{
			name: "old ambiguous write requires reconciliation",
			row: generated.CommerceGatewayOperation{
				State: "recovery_required", ProviderAttemptCount: 1,
				CreatedAt: pgtype.Timestamptz{
					Time: now.Add(-maximumProviderIdempotencyRetryAge), Valid: true,
				},
			},
			want: gatewayOperationRecover,
		},
		{
			name: "legacy recent recovery state can reuse Stripe idempotency",
			row: generated.CommerceGatewayOperation{
				State: "recovery_required", ProviderAttemptCount: 8,
				CreatedAt: pgtype.Timestamptz{
					Time: now.Add(-time.Minute), Valid: true,
				},
			},
			want: gatewayOperationWrite,
		},
		{
			name: "last provider attempt can still write",
			row: generated.CommerceGatewayOperation{
				State: "pending", ProviderAttemptCount: 63, CreatedAt: pgtype.Timestamptz{
					Time: now.Add(-time.Minute), Valid: true,
				},
			},
			want: gatewayOperationWrite,
		},
		{
			name: "provider attempt ceiling requires reconciliation",
			row: generated.CommerceGatewayOperation{
				State: "pending", ProviderAttemptCount: 64, CreatedAt: pgtype.Timestamptz{
					Time: now.Add(-time.Minute), Valid: true,
				},
			},
			want: gatewayOperationRecover,
		},
		{
			name: "completed operation replays persisted provider resource",
			row: generated.CommerceGatewayOperation{
				State: "succeeded", ProviderResourceID: pgtype.Text{
					String: "cs_test_Persisted123", Valid: true,
				},
				CreatedAt: pgtype.Timestamptz{
					Time: now.Add(-48 * time.Hour), Valid: true,
				},
			},
			want: gatewayOperationReplay,
		},
		{
			name: "completed operation without provider resource is corrupt",
			row: generated.CommerceGatewayOperation{
				State: "succeeded", CreatedAt: pgtype.Timestamptz{
					Time: now.Add(-time.Minute), Valid: true,
				},
			},
			wantErr: ErrInvalidStore,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := gatewayOperationDisposition(test.row, now)
			if got != test.want || !errors.Is(err, test.wantErr) {
				t.Fatalf(
					"gatewayOperationDisposition() = %q, %v; want %q, %v",
					got,
					err,
					test.want,
					test.wantErr,
				)
			}
		})
	}
}
