//go:build integration

package conversation

import (
	"testing"

	"github.com/google/uuid"
)

func TestDiscussionEmptyWindowRefreshPreservesCatchup(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		seed bool
	}{
		{name: "initial empty history"},
		{name: "empty newer page", seed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newDiscussionFixture(t)
			dm, err := f.conversations.CreateHumanDirect(
				f.ctx,
				f.a,
				CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID}},
			)
			if err != nil {
				t.Fatal(err)
			}
			id := dm.Conversation.ID
			if tc.seed {
				if _, err := f.discussion.Send(f.ctx, f.a, id, textSend("already read")); err != nil {
					t.Fatal(err)
				}
			}
			empty, err := f.discussion.ListMessages(f.ctx, f.b, id, DiscussionPageOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if tc.seed {
				empty, err = f.discussion.ListMessages(
					f.ctx,
					f.b,
					id,
					DiscussionPageOptions{Cursor: empty.NewerCursor},
				)
				if err != nil {
					t.Fatal(err)
				}
			}
			if len(empty.Items) != 0 {
				t.Fatalf("initial item count = %d, want 0", len(empty.Items))
			}
			sent, err := f.discussion.Send(f.ctx, f.a, id, textSend("arrived after the empty page"))
			if err != nil {
				t.Fatal(err)
			}
			// Refresh must keep the loaded window empty without consuming the
			// unseen suffix, even when recovery refreshes it more than once.
			for range 2 {
				empty, err = f.discussion.ListMessages(
					f.ctx,
					f.b,
					id,
					DiscussionPageOptions{Cursor: empty.RefreshCursor},
				)
				if err != nil {
					t.Fatal(err)
				}
				if len(empty.Items) != 0 || empty.HeadSeq != sent.HeadSeq {
					t.Fatalf("refreshed window = %+v, want empty with head %d", empty, sent.HeadSeq)
				}
			}
			newer, err := f.discussion.ListMessages(
				f.ctx,
				f.b,
				id,
				DiscussionPageOptions{Cursor: empty.NewerCursor},
			)
			if err != nil {
				t.Fatal(err)
			}
			if len(newer.Items) != 1 || newer.Items[0].ID != sent.Message.ID {
				t.Fatalf("catch-up items = %+v, want only message %s", newer.Items, sent.Message.ID)
			}
		})
	}
}
