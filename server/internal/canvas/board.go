package canvas

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

type BoardService interface {
	Snapshot(context.Context, auth.UserPrincipal, BoardScope, int64) (BoardSnapshot, error)
	Hydrate(context.Context, auth.UserPrincipal, BoardScope, BoardHydrateInput) (BoardHydration, error)
	Mutate(context.Context, auth.UserPrincipal, BoardScope, BoardBatch) (BoardMutation, error)
	Text(context.Context, auth.UserPrincipal, BoardScope, BoardTextInput) (BoardTextResult, error)
	Context(context.Context, auth.UserPrincipal, BoardScope, BoardContextInput) (BoardContext, error)
	AgentView(
		context.Context,
		auth.UserPrincipal,
		BoardScope,
		BoardAgentViewInput,
		BoardFocus,
	) (BoardAgentView, error)
	Compose(
		context.Context,
		auth.UserPrincipal,
		BoardScope,
		BoardComposeInput,
		BoardFocus,
	) (BoardComposeResult, error)
	Rename(context.Context, auth.UserPrincipal, BoardScope, string) (Board, error)
	Viewer(context.Context, auth.UserPrincipal, BoardScope) (BoardViewer, error)
	PublicSnapshot(context.Context, uuid.UUID) (BoardSnapshot, error)
	PublicHydrate(context.Context, uuid.UUID, BoardHydrateInput) (BoardHydration, error)
	Sweep(context.Context, time.Time) (BoardSweep, error)
}

var _ BoardService = (*DefaultBoardService)(nil)

type DefaultBoardService struct {
	store   *db.Store
	bus     *events.Bus
	viewers boardViewerCache
}

func NewBoardService(store *db.Store, bus *events.Bus) *DefaultBoardService {
	return &DefaultBoardService{
		store:   store,
		bus:     bus,
		viewers: boardViewerCache{entries: map[boardViewerKey]boardViewerEntry{}},
	}
}

// boardViewerCache keeps presence authorization for a few seconds. A browser
// heartbeats several times a second while its pointer moves; presence grants no
// authority, so a revocation reaching it a moment later is harmless, while
// every scene, text and file request still authorizes in its own transaction.
type boardViewerCache struct {
	mu      sync.Mutex
	entries map[boardViewerKey]boardViewerEntry
}
type boardViewerKey struct {
	company, user, board, run uuid.UUID
}
type boardViewerEntry struct {
	viewer  BoardViewer
	expires time.Time
}

func (c *boardViewerCache) get(key boardViewerKey, now time.Time) (BoardViewer, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || now.After(entry.expires) {
		return BoardViewer{}, false
	}
	return entry.viewer, true
}

func (c *boardViewerCache) put(key boardViewerKey, viewer BoardViewer, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= 4096 {
		for stale, entry := range c.entries {
			if now.After(entry.expires) {
				delete(c.entries, stale)
			}
		}
		if len(c.entries) >= 4096 {
			clear(c.entries)
		}
	}
	c.entries[key] = boardViewerEntry{viewer: viewer, expires: now.Add(3 * time.Second)}
}

// Company/member lifecycle locks protect every write. Cached principal roles
// never override the membership locked in this transaction.
func boardAuthorize(ctx context.Context, q *generated.Queries, p auth.UserPrincipal) error {
	if _, err := q.LockCompanyForMembershipRead(ctx, p.CompanyID); err != nil {
		return boardError(err)
	}
	member, err := q.GetCompanyMemberForShare(
		ctx,
		generated.GetCompanyMemberForShareParams{CompanyID: p.CompanyID, UserID: p.UserID},
	)
	if err != nil {
		return boardError(err)
	}
	if member.Status != "active" {
		return ErrBoardNotFound
	}
	return nil
}

// boardAuthorizeRead checks active membership in the read's snapshot. Reads
// take no lifecycle locks, so they never queue behind or block a writer.
func boardAuthorizeRead(ctx context.Context, q *generated.Queries, p auth.UserPrincipal) error {
	_, err := q.GetActiveCompanyMembership(
		ctx,
		generated.GetActiveCompanyMembershipParams{CompanyID: p.CompanyID, UserID: p.UserID},
	)
	return boardError(err)
}

// boardRead is the transaction every Board read runs in: one consistent
// snapshot of the scene, its notes and its sequence, without row locks.
var boardRead = pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}

func boardLock(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	id uuid.UUID,
	write bool,
) (boardRecord, error) {
	b, _, err := boardScopeLock(ctx, q, p, BoardScope{BoardID: id}, write)
	return b, err
}

func boardError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrBoardNotFound
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "23505", "40001", "40P01":
			return ErrBoardConflict
		case "23503", "23514", "22003":
			return ErrBoardInvalid
		}
	}
	return err
}

func boardDTO(row boardRecord, viewer BoardViewer) Board {
	return Board{
		ID:        row.ID,
		CompanyID: row.CompanyID,
		Title:     row.Title,
		Sequence:  row.HeadSeq,
		CreatedAt: row.CreatedAt.Time,
		UpdatedAt: row.UpdatedAt.Time,
		CanEdit:   viewer.CanEdit,
		CanShare:  viewer.CanShare,
	}
}

func boardHash(input any) ([]byte, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, ErrBoardInvalid
	}
	sum := sha256.Sum256(raw)
	return sum[:], nil
}

func (s *DefaultBoardService) Viewer(ctx context.Context, p auth.UserPrincipal, scope BoardScope) (BoardViewer, error) {
	key := boardViewerKey{company: p.CompanyID, user: p.UserID, board: scope.BoardID, run: scope.RunID}
	if cached, ok := s.viewers.get(key, time.Now()); ok {
		return cached, nil
	}
	var viewer BoardViewer
	err := s.store.WithinTx(ctx, boardRead, func(q *generated.Queries) error {
		_, v, err := boardScopeLock(ctx, q, p, scope, false)
		if err != nil {
			return err
		}
		viewer, err = boardViewerDetails(ctx, q, p, scope, v, nil)
		return err
	})
	if err != nil {
		return viewer, boardError(err)
	}
	s.viewers.put(key, viewer, time.Now())
	return viewer, nil
}

// boardViewerDetails completes a viewer for presence: the Board's audience,
// read unless the caller already has it, and the actor's name.
func boardViewerDetails(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	scope BoardScope,
	viewer BoardViewer,
	audience []uuid.UUID,
) (BoardViewer, error) {
	if audience == nil {
		var err error
		audience, err = q.ListBoardAudience(
			ctx,
			generated.ListBoardAudienceParams{CompanyID: p.CompanyID, OutputID: scope.BoardID},
		)
		if err != nil {
			return viewer, err
		}
	}
	viewer.RecipientUserIDs = audience
	if viewer.RunID != nil {
		agent, err := q.GetAgentForCompany(
			ctx,
			generated.GetAgentForCompanyParams{CompanyID: p.CompanyID, AgentID: viewer.Actor.ID},
		)
		viewer.Actor.DisplayName = agent.Name
		return viewer, err
	}
	user, err := q.GetUserByID(ctx, p.UserID)
	viewer.Actor.DisplayName = user.DisplayName
	return viewer, err
}

// runViewer completes a Run's viewer inside the transaction of a call it
// made; keepRunViewer then keeps it once that call commits, so moving the
// Agent's cursor right after needs no transaction of its own. A person's
// calls keep nothing: their browser's heartbeat reads its own viewer.
func runViewer(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	scope BoardScope,
	viewer BoardViewer,
	audience []uuid.UUID,
) (*BoardViewer, error) {
	if scope.RunID == uuid.Nil {
		return nil, nil
	}
	full, err := boardViewerDetails(ctx, q, p, scope, viewer, audience)
	return &full, err
}

func (s *DefaultBoardService) keepRunViewer(p auth.UserPrincipal, scope BoardScope, viewer *BoardViewer) {
	if viewer != nil {
		key := boardViewerKey{company: p.CompanyID, user: p.UserID, board: scope.BoardID, run: scope.RunID}
		s.viewers.put(key, *viewer, time.Now())
	}
}

// boardChangeEvent is called while the mutation transaction is still open.
func boardChangeEvent(p auth.UserPrincipal, id uuid.UUID, seq int64) events.Event {
	return events.Event{
		EntityType: TopicBoard,
		Name:       EventBoardChanged,
		CompanyID:  p.CompanyID,
		Actor:      events.UserActor(p.UserID.String()),
		CreatedAt:  time.Now().UTC(),
		Payload:    BoardChanged{BoardID: id, Sequence: seq},
	}
}

func (s *DefaultBoardService) publishEvent(ctx context.Context, event events.Event) {
	if s.bus != nil {
		publishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		s.bus.Publish(publishCtx, TopicBoard, event)
	}
}

func boardAdvance(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	id uuid.UUID,
	changes boardChanges,
) (int64, error) {
	seq, err := q.AdvanceBoardHead(ctx, generated.AdvanceBoardHeadParams{CompanyID: p.CompanyID, ID: id})
	if err != nil {
		return 0, err
	}
	raw, err := json.Marshal(changes)
	if err != nil {
		return 0, err
	}
	if err = q.InsertBoardChange(
		ctx,
		generated.InsertBoardChangeParams{CompanyID: p.CompanyID, BoardID: id, Seq: seq, Changes: raw},
	); err != nil {
		return 0, err
	}
	return seq, q.CompactBoardChanges(
		ctx,
		generated.CompactBoardChangesParams{
			BoardID:   id,
			HardFloor: seq - boardChangeFeedCap,
			Floor:     seq - boardChangeFeed,
			Cutoff:    pgtype.Timestamptz{Time: time.Now().Add(-boardChangeFeedWindow), Valid: true},
		},
	)
}
