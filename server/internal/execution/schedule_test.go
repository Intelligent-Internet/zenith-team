package execution

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestSupersedeScheduleRunInTxRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	validID := func() uuid.UUID {
		id, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("uuid.NewV7() error = %v", err)
		}
		return id
	}
	input := SupersedeScheduleRunInput{
		RunID: uuid.MustParse("00000000-0000-4000-8000-000000000001"), FiringID: validID(), ScheduleID: validID(),
		CompanyID: validID(), ConversationID: validID(),
	}

	if err := SupersedeScheduleRunInTx(context.Background(), nil, nil, input); err == nil {
		t.Fatal("SupersedeScheduleRunInTx(nil queries) error = nil, want invalid input")
	}
	input.FiringID = input.ScheduleID
	if err := SupersedeScheduleRunInTx(context.Background(), nil, nil, input); err == nil {
		t.Fatal("SupersedeScheduleRunInTx(duplicate IDs) error = nil, want invalid input")
	}
}

func TestInvalidateScheduleRunInTxRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	if err := InvalidateScheduleRunInTx(
		context.Background(), nil, nil, ScheduleRunCancellationInput{},
	); err == nil {
		t.Fatal("InvalidateScheduleRunInTx(invalid) error = nil")
	}
}
