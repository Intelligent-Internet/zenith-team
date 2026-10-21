package conversation

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

func TestNewConversationServiceRejectsMissingStore(t *testing.T) {
	t.Parallel()

	if _, err := NewConversationService(ServiceOptions{
		Bus: newTestBus(t), TaskMessageRuns: testTaskMessageRuns(),
	}); err == nil {
		t.Fatal("NewConversationService(nil store) error = nil")
	}
	if _, err := NewConversationService(ServiceOptions{
		Store: &db.Store{}, TaskMessageRuns: testTaskMessageRuns(),
	}); err == nil {
		t.Fatal("NewConversationService(nil bus) error = nil")
	}
	if _, err := NewConversationService(ServiceOptions{
		Store: &db.Store{}, Bus: newTestBus(t),
	}); err == nil {
		t.Fatal("NewConversationService(nil Task Message Run preparer) error = nil")
	}
}

func TestConversationWritesUseReadCommittedOptions(t *testing.T) {
	t.Parallel()

	if conversationWriteTxOptions.IsoLevel != pgx.ReadCommitted ||
		conversationWriteTxOptions.AccessMode != pgx.ReadWrite {
		t.Fatalf("Conversation write transaction options = %+v", conversationWriteTxOptions)
	}
}

// A request rejected at the boundary must fail before any database or bus
// interaction.
func TestSendRejectsInvalidInputBeforeTheStore(t *testing.T) {
	t.Parallel()

	service := &DefaultConversationService{bus: newTestBus(t)}
	principal := testUserPrincipal()
	target := MessageTarget{Kind: TargetConversation, ID: uuid.Must(uuid.NewV7())}
	input := SendInput{RequestKey: uuid.Must(uuid.NewV7()), Content: "Please inspect this."}

	if _, err := service.Send(
		context.Background(),
		principal,
		MessageTarget{},
		input,
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Send(invalid target) error = %v, want %v", err, ErrNotFound)
	}
	if _, err := service.Send(context.Background(), principal, target, SendInput{
		RequestKey: input.RequestKey, Content: "   ",
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Send(blank content) error = %v, want %v", err, ErrInvalidInput)
	}
}

func TestConversationReadsAndMutationsRejectInvalidBoundaryInput(t *testing.T) {
	t.Parallel()

	service := &DefaultConversationService{}
	principal := testUserPrincipal()
	conversationID := uuid.Must(uuid.NewV7())
	if _, err := service.UpdateState(
		context.Background(),
		principal,
		conversationID,
		UpdateUserStateInput{ExpectedRevision: 1},
	); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("UpdateState(empty mutation) error = %v, want %v", err, ErrInvalidInput)
	}
}

// The predicate that tells a lost response from a lost race. Both reach the
// CAS as "the revision you named is not the one stored", and only these three
// facts together separate them — so each is moved on its own below.
func TestIsReplayOfUserStateMutation(t *testing.T) {
	t.Parallel()

	moment := time.Unix(1_700_000_000, 0).UTC()

	for name, testCase := range map[string]struct {
		current          UserState
		expectedRevision int64
		targetPinned     bool
		targetArchived   bool
		want             bool
	}{
		// The client asked to pin, the write landed, the response was lost.
		// The row is one ahead and already holds what the retry asks for.
		"exact replay": {
			current:          UserState{PinnedAt: &moment, Revision: 4},
			targetPinned:     true,
			expectedRevision: 3,
			want:             true,
		},
		"exact replay of both flags": {
			current:          UserState{PinnedAt: &moment, ArchivedAt: &moment, Revision: 8},
			targetPinned:     true,
			targetArchived:   true,
			expectedRevision: 7,
			want:             true,
		},
		"exact replay of a clear": {
			current:          UserState{Revision: 4},
			expectedRevision: 3,
			want:             true,
		},
		// One ahead, but the row does not hold what the retry asks for, so
		// whatever advanced it was not this request.
		"one ahead with the wrong pin state": {
			current:          UserState{Revision: 4},
			targetPinned:     true,
			expectedRevision: 3,
		},
		"one ahead with the wrong archive state": {
			current:          UserState{PinnedAt: &moment, Revision: 4},
			targetPinned:     true,
			targetArchived:   true,
			expectedRevision: 3,
		},
		// Genuine conflicts: someone else wrote in between, or the caller
		// names a revision the row has already passed.
		"two ahead": {
			current:          UserState{PinnedAt: &moment, Revision: 5},
			targetPinned:     true,
			expectedRevision: 3,
		},
		"behind": {
			current:          UserState{PinnedAt: &moment, Revision: 2},
			targetPinned:     true,
			expectedRevision: 3,
		},
		// The ceiling a JSON client can send. Without the guard the +1 wraps
		// negative and this reads as a replay of a row it never saw.
		"at the JSON-safe ceiling": {
			current:          UserState{PinnedAt: &moment, Revision: math.MinInt64},
			targetPinned:     true,
			expectedRevision: protocol.MaxJSONSafeInteger,
		},
		"past the JSON-safe ceiling": {
			current:          UserState{PinnedAt: &moment, Revision: math.MinInt64},
			targetPinned:     true,
			expectedRevision: math.MaxInt64,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := isReplayOfUserStateMutation(
				testCase.current, testCase.expectedRevision,
				testCase.targetPinned, testCase.targetArchived,
			)
			if got != testCase.want {
				t.Fatalf(
					"isReplayOfUserStateMutation(stored %d, expected %d) = %t, want %t",
					testCase.current.Revision, testCase.expectedRevision, got, testCase.want,
				)
			}
		})
	}
}

func newTestBus(t *testing.T) *events.Bus {
	t.Helper()
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("events.NewBus() error = %v", err)
	}
	return bus
}

func testUserPrincipal() auth.UserPrincipal {
	return auth.UserPrincipal{
		UserID:    uuid.Must(uuid.NewV7()),
		SessionID: uuid.Must(uuid.NewV7()),
		AuthKind:  auth.TokenKindBearer,
	}
}
