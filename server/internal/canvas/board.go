package canvas

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

type BoardService interface {
	List(context.Context, auth.UserPrincipal, BoardListInput) (BoardList, error)
	Create(context.Context, auth.UserPrincipal, CreateBoardInput) (Board, error)
	Update(context.Context, auth.UserPrincipal, uuid.UUID, UpdateBoardInput) (Board, error)
	Claim(context.Context, auth.UserPrincipal, uuid.UUID, int64) (Board, error)
	Favorite(context.Context, auth.UserPrincipal, uuid.UUID, bool) error
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
	Viewer(context.Context, auth.UserPrincipal, BoardScope) (BoardViewer, error)
	Access(context.Context, auth.UserPrincipal, uuid.UUID) (BoardAccess, error)
	SetAccess(context.Context, auth.UserPrincipal, uuid.UUID, BoardAccessInput) error
	SetVisibility(context.Context, auth.UserPrincipal, uuid.UUID, BoardVisibilityInput) error
	PublicSnapshot(context.Context, uuid.UUID) (BoardSnapshot, error)
	PublicHydrate(context.Context, uuid.UUID, BoardHydrateInput) (BoardHydration, error)
	ListForRun(context.Context, auth.UserPrincipal, uuid.UUID, int32) (BoardList, error)
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
func boardAuthorize(ctx context.Context, q *generated.Queries, p auth.UserPrincipal, permission auth.Permission) error {
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
	if !auth.CompanyRole(member.Role).Can(permission) {
		return ErrBoardForbidden
	}
	return nil
}

// boardAuthorizeRead checks active membership and the Company permission in
// the read's snapshot. Reads take no lifecycle locks, so they never queue
// behind or block a writer.
func boardAuthorizeRead(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	permission auth.Permission,
) error {
	member, err := q.GetActiveCompanyMembership(
		ctx,
		generated.GetActiveCompanyMembershipParams{CompanyID: p.CompanyID, UserID: p.UserID},
	)
	if err != nil {
		return boardError(err)
	}
	if !auth.CompanyRole(member.Role).Can(permission) {
		return ErrBoardForbidden
	}
	return nil
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
) (generated.Board, error) {
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

func boardDTO(row generated.Board, p auth.UserPrincipal) Board {
	b := Board{
		Visibility: row.Visibility,
		CanShare:   row.CreatedBy == p.UserID && p.Can(auth.PermissionWhiteboardsEdit),
		ID:         row.ID,
		CompanyID:  row.CompanyID,
		Title:      row.Title,
		Purpose:    row.Purpose,
		Sequence:   row.HeadSeq,
		Revision:   row.Revision,
		CreatedBy:  row.CreatedBy,
		CreatedAt:  row.CreatedAt.Time,
		UpdatedAt:  row.UpdatedAt.Time,
		CanEdit:    p.Can(auth.PermissionWhiteboardsEdit) && !row.ArchivedAt.Valid,
		CanManage:  p.Can(auth.PermissionWhiteboardsManage),
	}
	if row.ArchivedAt.Valid {
		b.ArchivedAt = &row.ArchivedAt.Time
	}
	return b
}

func boardHash(input any) ([]byte, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, ErrBoardInvalid
	}
	sum := sha256.Sum256(raw)
	return sum[:], nil
}

func (s *DefaultBoardService) List(ctx context.Context, p auth.UserPrincipal, input BoardListInput) (BoardList, error) {
	result := BoardList{Boards: []Board{}}
	if input.Limit == 0 {
		input.Limit = 50
	}
	if input.Limit < 1 || input.Limit > 100 || input.Offset < 0 || len(input.Search) > 200 {
		return result, ErrBoardInvalid
	}
	if input.Unowned {
		return s.listUnowned(ctx, p, input)
	}
	err := s.store.WithinTx(ctx, boardRead, func(q *generated.Queries) error {
		if err := boardAuthorizeRead(ctx, q, p, auth.PermissionWhiteboardsRead); err != nil {
			return err
		}
		search := strings.TrimSpace(input.Search)
		rows, err := q.ListBoards(
			ctx,
			generated.ListBoardsParams{
				CompanyID:   p.CompanyID,
				ActorUserID: p.UserID,
				Search:      boardLikePattern(search),
				NoteQuery:   boardNoteQuery(search),
				Archived:    input.Archived,
				Favorites:   input.Favorites,
				RowLimit:    input.Limit + 1,
				RowOffset:   input.Offset,
			},
		)
		if err != nil {
			return err
		}
		result.HasMore = len(rows) > int(input.Limit)
		if result.HasMore {
			rows = rows[:input.Limit]
		}
		for _, row := range rows {
			b := boardDTO(
				generated.Board{
					ID:         row.ID,
					CompanyID:  row.CompanyID,
					Title:      row.Title,
					Purpose:    row.Purpose,
					HeadSeq:    row.HeadSeq,
					Revision:   row.Revision,
					CreatedBy:  row.CreatedBy,
					CreatedAt:  row.CreatedAt,
					UpdatedAt:  row.UpdatedAt,
					ArchivedAt: row.ArchivedAt,
					Visibility: row.Visibility,
				},
				p,
			)
			b.Favorite = row.Favorite
			b.CanEdit = b.CanEdit && row.Access != "read"
			b.CanShare = p.Can(auth.PermissionWhiteboardsEdit) && row.Access == "manage"
			b.CanManage = b.CanManage && b.CanShare
			result.Boards = append(result.Boards, b)
		}
		return nil
	})
	return result, boardError(err)
}

func (s *DefaultBoardService) Create(ctx context.Context, p auth.UserPrincipal, input CreateBoardInput) (Board, error) {
	input.Title = strings.TrimSpace(input.Title)
	input.Purpose = strings.TrimSpace(input.Purpose)
	if input.RequestKey == uuid.Nil || input.Title == "" || utf8.RuneCountInString(input.Title) > boardMaxTitle ||
		utf8.RuneCountInString(input.Purpose) > boardMaxPurpose {
		return Board{}, ErrBoardInvalid
	}
	hash, err := boardHash(input)
	if err != nil {
		return Board{}, err
	}
	var result Board
	var pending events.Event
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) (txErr error) {
		defer func() {
			if txErr == nil {
				pending = boardChangeEvent(p, result.ID, result.Sequence)
				txErr = stampBoardAudience(ctx, q, &pending)
			}
		}()
		// Serializes only Board creation; scene edits lock their own Board.
		if _, err := q.LockCompanyForMembershipMutation(ctx, p.CompanyID); err != nil {
			return boardError(err)
		}
		if err := boardAuthorize(ctx, q, p, auth.PermissionWhiteboardsCreate); err != nil {
			return err
		}
		receipt, err := q.GetBoardCreateReceipt(
			ctx,
			generated.GetBoardCreateReceiptParams{
				CompanyID:   p.CompanyID,
				ActorUserID: p.UserID,
				RequestKey:  input.RequestKey,
			},
		)
		if err == nil {
			if !bytes.Equal(hash, receipt.RequestHash) {
				return ErrBoardRequestKeyReused
			}
			row, err := q.ReadBoard(
				ctx,
				generated.ReadBoardParams{CompanyID: p.CompanyID, ID: receipt.BoardID},
			)
			result = boardDTO(row, p)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		row, err := q.CreateBoard(
			ctx,
			generated.CreateBoardParams{
				CompanyID: p.CompanyID,
				Title:     input.Title,
				Purpose:   input.Purpose,
				CreatedBy: p.UserID,
			},
		)
		if err != nil {
			return err
		}
		result = boardDTO(row, p)
		return q.InsertBoardCreateReceipt(
			ctx,
			generated.InsertBoardCreateReceiptParams{
				CompanyID:   p.CompanyID,
				ActorUserID: p.UserID,
				RequestKey:  input.RequestKey,
				BoardID:     row.ID,
				RequestHash: hash,
			},
		)
	})
	if err == nil {
		s.publishEvent(ctx, pending)
	}
	return result, boardError(err)
}

func (s *DefaultBoardService) Update(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
	input UpdateBoardInput,
) (Board, error) {
	if input.ExpectedRevision < 1 {
		return Board{}, ErrBoardInvalid
	}
	var result Board
	var pending events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) (txErr error) {
		defer func() {
			if txErr == nil {
				pending = boardChangeEvent(p, id, result.Sequence)
				txErr = stampBoardAudience(ctx, q, &pending)
			}
		}()
		permission := auth.PermissionWhiteboardsEdit
		if input.Archived != nil {
			permission = auth.PermissionWhiteboardsManage
		}
		if err := boardAuthorize(ctx, q, p, permission); err != nil {
			return err
		}
		row, err := q.LockBoard(ctx, generated.LockBoardParams{CompanyID: p.CompanyID, ID: id})
		if err != nil {
			return err
		}
		access, err := q.GetBoardMemberAccess(
			ctx,
			generated.GetBoardMemberAccessParams{CompanyID: p.CompanyID, BoardID: id, UserID: p.UserID},
		)
		if err != nil {
			return err
		}
		if access != "manage" {
			return ErrBoardForbidden
		}
		if row.Revision != input.ExpectedRevision {
			return ErrBoardConflict
		}
		// An archived Board's details stay as they were unless it is recovered
		// in the same request.
		if row.ArchivedAt.Valid && (input.Archived == nil || *input.Archived) &&
			(input.Title != nil || input.Purpose != nil) {
			return ErrBoardArchived
		}
		if input.Title != nil {
			row.Title = strings.TrimSpace(*input.Title)
		}
		if input.Purpose != nil {
			row.Purpose = strings.TrimSpace(*input.Purpose)
		}
		if row.Title == "" || utf8.RuneCountInString(row.Title) > boardMaxTitle ||
			utf8.RuneCountInString(row.Purpose) > boardMaxPurpose {
			return ErrBoardInvalid
		}
		if input.Archived != nil {
			row.ArchivedAt = pgtype.Timestamptz{}
			if *input.Archived {
				row.ArchivedAt = pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
			}
		}
		row, err = q.UpdateBoard(
			ctx,
			generated.UpdateBoardParams{
				CompanyID:  p.CompanyID,
				ID:         id,
				Title:      row.Title,
				Purpose:    row.Purpose,
				ArchivedAt: row.ArchivedAt,
				Revision:   input.ExpectedRevision,
			},
		)
		if err != nil {
			return err
		}
		seq, err := boardAdvance(ctx, q, p, id, boardChanges{})
		if err != nil {
			return err
		}
		row.HeadSeq = seq
		result = boardDTO(row, p)
		result.CanShare = true
		return nil
	})
	if err == nil {
		s.publishEvent(ctx, pending)
	}
	return result, boardError(err)
}

func (s *DefaultBoardService) Favorite(ctx context.Context, p auth.UserPrincipal, id uuid.UUID, favorite bool) error {
	return boardError(s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		// A favorite is a per-member write, so it holds the membership lock.
		if err := boardAuthorize(ctx, q, p, auth.PermissionWhiteboardsRead); err != nil {
			return err
		}
		if _, err := boardLock(ctx, q, p, id, false); err != nil {
			return err
		}
		if favorite {
			return q.SetBoardFavorite(
				ctx,
				generated.SetBoardFavoriteParams{CompanyID: p.CompanyID, BoardID: id, UserID: p.UserID},
			)
		}
		return q.DeleteBoardFavorite(ctx, generated.DeleteBoardFavoriteParams{BoardID: id, UserID: p.UserID})
	}))
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
			generated.ListBoardAudienceParams{
				CompanyID:  p.CompanyID,
				BoardID:    scope.BoardID,
				ActorRoles: auth.RolesWith(auth.PermissionWhiteboardsRead),
			},
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

// listUnowned shows Company Whiteboard managers the Boards they may claim.
func (s *DefaultBoardService) listUnowned(ctx context.Context, p auth.UserPrincipal, input BoardListInput) (BoardList, error) {
	result := BoardList{Boards: []Board{}}
	err := s.store.WithinTx(ctx, boardRead, func(q *generated.Queries) error {
		if err := boardAuthorizeRead(ctx, q, p, auth.PermissionWhiteboardsManage); err != nil {
			return err
		}
		rows, err := q.ListUnownedBoards(
			ctx,
			generated.ListUnownedBoardsParams{CompanyID: p.CompanyID, RowLimit: input.Limit + 1, RowOffset: input.Offset},
		)
		if err != nil {
			return err
		}
		result.HasMore = len(rows) > int(input.Limit)
		if result.HasMore {
			rows = rows[:input.Limit]
		}
		for _, row := range rows {
			b := boardDTO(row, p)
			b.CanEdit, b.CanShare = false, false
			result.Boards = append(result.Boards, b)
		}
		return nil
	})
	return result, boardError(err)
}

// boardLikePattern makes a search match its characters literally in ILIKE.
func boardLikePattern(search string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(search)
}

// boardNoteQuery turns a search into a full-text query over note words: every
// word must appear, the last as a prefix while it is still being typed.
// Characters other than letters and digits only separate words.
func boardNoteQuery(search string) string {
	words := strings.FieldsFunc(strings.ToLower(search), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.Is(unicode.Mn, r)
	})
	for i, word := range words {
		words[i] = word + ":*"
	}
	return strings.Join(words, " & ")
}
