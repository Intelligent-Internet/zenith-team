package conversation

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

// The revision bound has left this function: `binding:"revision"` reads
// through the optional[int64] wrapper now, and
// handler.TestRunQueueRejectsAnUnusableRevisionBeforeTheService covers both
// ends of the range at the boundary. What is left is the pair no tag can
// state — a nil UUID, which the `uuid` rule's hex shape accepts, and two items
// of one request naming the same queue item.
func TestNormalizeRunQueueOrderRequiresExactDistinctItems(t *testing.T) {
	t.Parallel()

	first := uuid.Must(uuid.NewV7())
	second := uuid.Must(uuid.NewV7())
	input := RunQueueOrderInput{Items: []RunQueueOrderItem{
		{ID: second, ExpectedRevision: 3},
		{ID: first, ExpectedRevision: 1},
	}}
	if got, err := normalizeRunQueueOrderInput(input); err != nil || len(got.Items) != 2 {
		t.Fatalf("normalizeRunQueueOrderInput() = %+v, %v", got, err)
	}

	for _, invalid := range []RunQueueOrderInput{
		{},
		{Items: []RunQueueOrderItem{{ID: uuid.Nil, ExpectedRevision: 1}}},
		{Items: []RunQueueOrderItem{
			{ID: first, ExpectedRevision: 1},
			{ID: first, ExpectedRevision: 1},
		}},
	} {
		if _, err := normalizeRunQueueOrderInput(invalid); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("normalizeRunQueueOrderInput(%+v) error = %v, want %v", invalid, err, ErrInvalidInput)
		}
	}
}
