package canvas

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBoardViewerCacheExpiresQuickly(t *testing.T) {
	t.Parallel()
	cache := boardViewerCache{entries: map[boardViewerKey]boardViewerEntry{}}
	key := boardViewerKey{company: uuid.New(), user: uuid.New(), board: uuid.New()}
	now := time.Now()
	viewer := BoardViewer{RecipientUserIDs: []uuid.UUID{key.user}}
	cache.put(key, viewer, now)
	if got, ok := cache.get(key, now.Add(2*time.Second)); !ok || len(got.RecipientUserIDs) != 1 {
		t.Fatalf("fresh entry = %+v, %v", got, ok)
	}
	if _, ok := cache.get(key, now.Add(4*time.Second)); ok {
		t.Fatal("presence authorization outlived its window")
	}
	other := key
	other.run = uuid.New()
	if _, ok := cache.get(other, now); ok {
		t.Fatal("a Run shared its member's cached viewer")
	}
}
