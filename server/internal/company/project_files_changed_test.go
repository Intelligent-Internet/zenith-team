package company

import (
	"slices"
	"testing"

	"github.com/google/uuid"
)

// A files replace retires every active row and revives the kept ones, so only
// the files it added or dropped change who sees a message holding them.
func TestChangedProjectFilesIsWhatTheReplaceAddedOrDropped(t *testing.T) {
	t.Parallel()

	kept, dropped, added := uuid.New(), uuid.New(), uuid.New()
	got := changedProjectFiles([]uuid.UUID{kept, dropped}, []uuid.UUID{kept, added})
	if want := []uuid.UUID{added, dropped}; !slices.Equal(got, want) {
		t.Fatalf("changedProjectFiles = %v, want %v", got, want)
	}
	if got := changedProjectFiles([]uuid.UUID{kept}, []uuid.UUID{kept}); len(got) != 0 {
		t.Fatalf("an unchanged selection = %v, want none", got)
	}
}
