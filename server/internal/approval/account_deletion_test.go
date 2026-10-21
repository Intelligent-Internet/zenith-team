package approval

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestAbandonAccountContinuationsInTxRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	companyID := uuid.Must(uuid.NewV7())
	userID := uuid.Must(uuid.NewV7())
	var pending []events.Event
	validQueries := generated.New(nil)
	tests := []struct {
		name      string
		queries   *generated.Queries
		pending   *[]events.Event
		companyID uuid.UUID
		userID    uuid.UUID
		reason    string
	}{
		{
			name:      "nil queries",
			pending:   &pending,
			companyID: companyID,
			userID:    userID,
			reason:    "account_deleted",
		},
		{
			name:      "nil event batch",
			queries:   validQueries,
			companyID: companyID,
			userID:    userID,
			reason:    "account_deleted",
		},
		{
			name:    "zero Company id",
			queries: validQueries,
			pending: &pending,
			userID:  userID,
			reason:  "account_deleted",
		},
		{
			name:      "zero User id",
			queries:   validQueries,
			pending:   &pending,
			companyID: companyID,
			reason:    "account_deleted",
		},
		{
			name:      "empty reason",
			queries:   validQueries,
			pending:   &pending,
			companyID: companyID,
			userID:    userID,
		},
		{
			name:      "padded reason",
			queries:   validQueries,
			pending:   &pending,
			companyID: companyID,
			userID:    userID,
			reason:    " account_deleted",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := AbandonAccountContinuationsInTx(
				context.Background(),
				test.queries,
				test.pending,
				test.companyID,
				test.userID,
				test.reason,
			)
			if !errors.Is(err, ErrApprovalInvalidInput) {
				t.Fatalf(
					"AbandonAccountContinuationsInTx() error = %v, want ErrApprovalInvalidInput",
					err,
				)
			}
		})
	}
}
