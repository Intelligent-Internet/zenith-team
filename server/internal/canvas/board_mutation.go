package canvas

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

var boardColorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}([0-9a-fA-F]{2})?$`)

// boardCommandTypes and boardItemTypes are what a batch's commands take.
var (
	boardCommandTypes = []string{
		"create_item", "geometry", "text", "content", "delete_item", "restore_item",
		"create_edge", "update_edge", "delete_edge", "restore_edge",
	}
	boardItemTypes = []string{"sticky", "text", "shape", "stroke", "image", "file", "frame"}
)

func normalizeBoardBatch(input BoardBatch) error {
	// Large selections move, restyle or delete in one atomic batch.
	if input.RequestKey == uuid.Nil || len(input.Commands) == 0 || len(input.Commands) > boardMaxCommands {
		return boardInvalid("a batch has a request key and 1 to %d commands", boardMaxCommands)
	}
	size, err := json.Marshal(input)
	if err != nil || len(size) > boardMaxBatchBytes {
		return ErrBoardLimit
	}
	drafts := map[string]bool{}
	for i, c := range input.Commands {
		// A refusal names the command, so its sender can correct it.
		if err := normalizeBoardCommand(c, drafts); err != nil {
			var invalid BoardInvalidError
			if errors.As(err, &invalid) {
				return boardInvalid("commands[%d] (%s): %s", i, c.Type, invalid.Reason)
			}
			return err
		}
	}
	return nil
}

func normalizeBoardCommand(c BoardCommand, drafts map[string]bool) error {
	if !slices.Contains(boardCommandTypes, c.Type) {
		return boardInvalid("type is one of %s", strings.Join(boardCommandTypes, ", "))
	}
	if strings.HasPrefix(c.Type, "create_") {
		if c.DraftID == "" || len(c.DraftID) > 100 || drafts[c.DraftID] {
			return boardInvalid("draftId is required, at most 100 bytes and unique in the batch")
		}
		drafts[c.DraftID] = true
	} else if c.ID == nil || *c.ID == uuid.Nil || c.ExpectedRevision < 1 {
		return boardInvalid("id and expectedRevision are required")
	}
	if c.Type == "text" && c.ExpectedTextRevision < 1 {
		return boardInvalid("expectedTextRevision is required")
	}
	if len(c.Text) > boardMaxNoteText {
		return ErrBoardLimit
	}
	if c.Type == "create_item" {
		if !slices.Contains(boardItemTypes, c.ItemType) {
			return boardInvalid("itemType is one of %s", strings.Join(boardItemTypes, ", "))
		}
		if c.Geometry == nil {
			return boardInvalid("geometry is required")
		}
		if c.ItemType != "sticky" && c.ItemType != "text" && c.Text != "" {
			return boardInvalid("only a sticky or text item has text; a shape or frame shows its title")
		}
		placesFile := c.ItemType == "image" || c.ItemType == "file"
		if placesFile != (c.Properties != nil && c.Properties.AttachmentID != nil) {
			return boardInvalid("an image or file item, and only one, has properties.attachmentId")
		}
		if c.ItemType == "stroke" && (len(c.Points) == 0 || len(c.Points) > boardMaxStrokePoints) {
			return ErrBoardLimit
		}
		if c.ItemType != "stroke" && len(c.Points) > 0 {
			return boardInvalid("only a stroke has points")
		}
	}
	if c.Type == "geometry" && c.Geometry == nil {
		return boardInvalid("geometry is required")
	}
	if c.Type == "content" && c.Properties == nil {
		return boardInvalid("properties are required")
	}
	if c.Geometry != nil {
		if err := normalizeBoardGeometry(*c.Geometry); err != nil {
			return err
		}
	}
	if c.Properties != nil {
		if err := normalizeBoardProperties(*c.Properties); err != nil {
			return err
		}
	}
	for _, pt := range c.Points {
		if !boardCoordinate(pt.X) || !boardCoordinate(pt.Y) || math.IsNaN(pt.Pressure) ||
			pt.Pressure < 0 ||
			pt.Pressure > 1 {
			return boardInvalid("points lie within ±1000000 with a pressure from 0 to 1")
		}
	}
	if utf8.RuneCountInString(c.Label) > 500 || utf8.RuneCountInString(c.Relation) > 100 {
		return boardInvalid("a label is at most 500 characters and a relation 100")
	}
	return normalizeBoardEdgeNotation(c)
}

// boardStrokePoints stores ink at a tenth of a Board unit and pressure at a
// hundredth: finer digits are invisible and only grow every stroke row.
func boardStrokePoints(points []BoardPoint) []BoardPoint {
	round := func(v, scale float64) float64 {
		// Adding zero turns a rounded -0 into 0.
		return math.Round(v*scale)/scale + 0
	}
	stored := make([]BoardPoint, len(points))
	for i, point := range points {
		stored[i] = BoardPoint{
			X:        round(point.X, 10),
			Y:        round(point.Y, 10),
			Pressure: round(point.Pressure, 100),
		}
	}
	return stored
}

func boardCoordinate(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= -1000000 && v <= 1000000
}

func normalizeBoardGeometry(g BoardGeometry) error {
	if !boardCoordinate(g.X) || !boardCoordinate(g.Y) || !boardCoordinate(g.Width) || !boardCoordinate(g.Height) ||
		g.Width < 0.1 ||
		g.Height < 0.1 ||
		math.IsNaN(g.Rotation) ||
		math.IsInf(g.Rotation, 0) ||
		g.Rotation < -360 ||
		g.Rotation > 360 {
		return boardInvalid(
			"geometry lies within ±1000000, at least 0.1 wide and high, rotated at most 360 degrees",
		)
	}
	return nil
}

// boardSwatchHexes stores a swatch's name as its value: an Agent reads
// colors by name (color=blue, fill=pink) and writes them back the same way.
func boardSwatchHexes(input BoardBatch) {
	for _, c := range input.Commands {
		p := c.Properties
		if p == nil {
			continue
		}
		// A hex value stays as sent.
		if hex := boardComposeInk(p.Color, ""); hex != "" && !boardColorPattern.MatchString(p.Color) {
			p.Color = hex
		}
		if hex := boardComposeFill(p.Fill, ""); hex != "" && !boardColorPattern.MatchString(p.Fill) {
			p.Fill = hex
		}
	}
}

func normalizeBoardProperties(p BoardProperties) error {
	// A title counts characters, as the editor and an Agent's request do.
	if utf8.RuneCountInString(p.Title) > 1000 || len(p.Caption) > 2000 || len(p.Alt) > 2000 || len(p.Role) > 100 {
		return boardInvalid("a title is at most 1000 characters, a caption or alt 2000 bytes and a role 100")
	}
	for _, color := range []struct {
		field, value string
		swatches     map[string]string
	}{{"color", p.Color, boardInkNames}, {"fill", p.Fill, boardFillNames}} {
		if color.value != "" && !boardColorPattern.MatchString(color.value) {
			return boardInvalid(
				"%s is #rrggbb, #rrggbbaa or a swatch: %s",
				color.field, strings.Join(slices.Sorted(maps.Values(color.swatches)), ", "),
			)
		}
	}
	if math.IsNaN(p.StrokeWidth) || p.StrokeWidth < 0 || p.StrokeWidth > 100 {
		return boardInvalid("strokeWidth is 0 to 100")
	}
	if err := normalizeBoardShape(p); err != nil {
		return err
	}
	if p.Brush != "" && !slices.Contains([]string{"pen", "highlighter"}, p.Brush) {
		return boardInvalid("brush is pen or highlighter")
	}
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > 8192 {
		return ErrBoardLimit
	}
	return nil
}

func (s *DefaultBoardService) Mutate(
	ctx context.Context,
	p auth.UserPrincipal,
	scope BoardScope,
	input BoardBatch,
) (BoardMutation, error) {
	id := scope.BoardID
	boardSwatchHexes(input)
	if err := normalizeBoardBatch(input); err != nil {
		return BoardMutation{}, err
	}
	var result BoardMutation
	var pending events.Event
	var run *BoardViewer
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		b, viewer, err := boardScopeLock(ctx, q, p, scope, true)
		if err != nil {
			return err
		}
		result, err = boardApplyBatch(ctx, q, p, b, input, viewer)
		pending = boardChangeEvent(p, id, result.Sequence)
		pending.Actor = boardEventActor(p, viewer)
		if err != nil {
			return err
		}
		if err = stampBoardAudience(ctx, q, &pending); err != nil {
			return err
		}
		run, err = runViewer(ctx, q, p, scope, viewer, boardAudienceOf(pending))
		return err
	})
	if err == nil {
		s.keepRunViewer(p, scope, run)
	}
	// A replay changed nothing, so nobody needs to refetch.
	if err == nil && !result.Replayed {
		s.publishEvent(ctx, pending)
	}
	return result, boardError(err)
}

func boardApplyBatch(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	b boardRecord,
	input BoardBatch,
	viewer BoardViewer,
) (BoardMutation, error) {
	hash, err := boardHash(input)
	if err != nil {
		return BoardMutation{}, err
	}
	if result, found, err := boardReceiptReplay(ctx, q, p, b, input.RequestKey, hash, viewer); found || err != nil {
		return result, err
	}
	return boardApplyCommands(ctx, q, p, b, input, hash, viewer)
}

// boardReceiptReplay answers a request key this actor already used: the
// original batch replayed when the request is the same, a reuse error when
// it is not. found is false for a new key.
func boardReceiptReplay(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	b boardRecord,
	key uuid.UUID,
	hash []byte,
	viewer BoardViewer,
) (BoardMutation, bool, error) {
	receipt, err := q.GetBoardReceipt(
		ctx,
		generated.GetBoardReceiptParams{
			BoardID:     b.ID,
			ActorUserID: p.UserID,
			ActorRunID:  boardReceiptRun(viewer),
			RequestKey:  key,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return BoardMutation{}, false, nil
	}
	if err != nil {
		return BoardMutation{}, false, err
	}
	if !bytes.Equal(hash, receipt.RequestHash) {
		return BoardMutation{}, true, ErrBoardRequestKeyReused
	}
	result, err := boardReplayBatch(ctx, q, p, b, receipt)
	return result, true, err
}

// boardApplyCommands applies a new batch and records its receipt under hash,
// which identifies the request: the batch itself, or the intent it was
// compiled from.
func boardApplyCommands(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	b boardRecord,
	input BoardBatch,
	hash []byte,
	viewer BoardViewer,
) (BoardMutation, error) {
	result := BoardMutation{
		IDs:     map[string]uuid.UUID{},
		Items:   []BoardItem{},
		Edges:   []BoardEdge{},
		Removed: []uuid.UUID{},
	}
	changes := boardChanges{Items: []uuid.UUID{}, Edges: []uuid.UUID{}, Removed: []uuid.UUID{}}
	limits := &boardLimits{board: b.ID}
	for _, command := range input.Commands {
		if err := boardApplyCommand(ctx, q, p, b.ID, command, result.IDs, &changes, limits); err != nil {
			return result, err
		}
	}
	if err := boardStampChanges(ctx, q, p, b.ID, changes, result.IDs, viewer); err != nil {
		return result, err
	}
	seq, err := boardAdvance(ctx, q, p, b.ID, changes)
	if err != nil {
		return result, err
	}
	b.HeadSeq = seq
	snapshot, err := boardSnapshotInTx(ctx, q, p, b, seq-1)
	if err != nil {
		return result, err
	}
	result.Sequence = seq
	result.Items = snapshot.Items
	result.Edges = snapshot.Edges
	result.Removed = snapshot.Removed
	var ids []byte
	if len(result.IDs) > 0 {
		if ids, err = json.Marshal(result.IDs); err != nil {
			return result, err
		}
	}
	err = q.InsertBoardReceipt(
		ctx,
		generated.InsertBoardReceiptParams{
			CompanyID:   p.CompanyID,
			BoardID:     b.ID,
			ActorUserID: p.UserID,
			ActorRunID:  boardReceiptRun(viewer),
			RequestKey:  input.RequestKey,
			RequestHash: hash,
			Seq:         seq,
			Ids:         ids,
		},
	)
	return result, err
}

// boardReceiptRun scopes a receipt to the acting Run; member calls use the nil
// Run so the receipt key stays a plain primary key.
func boardReceiptRun(viewer BoardViewer) uuid.UUID {
	if viewer.RunID != nil {
		return *viewer.RunID
	}
	return uuid.Nil
}

// boardReplayBatch answers a retried batch from its receipt: the original
// sequence and created IDs, with the objects that batch touched in their
// current state. An object a later commit changed is left out, so a client
// never takes a collaborator's revision as its own undo guard. Once the change
// feed no longer holds that sequence the answer carries no objects and the
// client catches up from a snapshot.
func boardReplayBatch(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	b boardRecord,
	receipt generated.GetBoardReceiptRow,
) (BoardMutation, error) {
	result := BoardMutation{
		Sequence: receipt.Seq,
		IDs:      map[string]uuid.UUID{},
		Items:    []BoardItem{},
		Edges:    []BoardEdge{},
		Removed:  []uuid.UUID{},
		Replayed: true,
	}
	if receipt.Ids != nil {
		if err := json.Unmarshal(receipt.Ids, &result.IDs); err != nil {
			return result, err
		}
	}
	raw, err := q.GetBoardChange(ctx, generated.GetBoardChangeParams{BoardID: b.ID, Seq: receipt.Seq})
	if errors.Is(err, pgx.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	var change boardChanges
	if err = json.Unmarshal(raw, &change); err != nil {
		return result, err
	}
	selected := map[uuid.UUID]bool{}
	for _, id := range slices.Concat(change.Items, change.Edges, change.Removed) {
		selected[id] = true
	}
	later, err := q.ListBoardChanges(ctx, generated.ListBoardChangesParams{BoardID: b.ID, Seq: receipt.Seq})
	if err != nil {
		return result, err
	}
	// The feed read is bounded; past that bound every object may have moved on.
	if len(later) > boardMaxDelta {
		return result, nil
	}
	for _, row := range later {
		var next boardChanges
		if err = json.Unmarshal(row.Changes, &next); err != nil {
			return result, err
		}
		for _, id := range slices.Concat(next.Items, next.Edges, next.Removed) {
			delete(selected, id)
		}
	}
	if len(selected) == 0 {
		return result, nil
	}
	result.Items, result.Edges, result.Removed, err = boardObjectsInTx(ctx, q, p, b, selected)
	return result, err
}

// boardLimits counts live items and connectors once per batch instead of once
// per created object, and tracks the batch's own changes against the limits.
type boardLimits struct {
	board        uuid.UUID
	items, edges int64
	loaded       bool
}

func (l *boardLimits) reserve(ctx context.Context, q *generated.Queries, items, edges int64) error {
	if !l.loaded {
		var err error
		if l.items, err = q.CountBoardItems(ctx, l.board); err != nil {
			return err
		}
		if l.edges, err = q.CountBoardEdges(ctx, l.board); err != nil {
			return err
		}
		l.loaded = true
	}
	if l.items+items > boardMaxItems || l.edges+edges > boardMaxEdges {
		return ErrBoardLimit
	}
	l.items, l.edges = l.items+items, l.edges+edges
	return nil
}

// release accounts for objects the batch removed after the counts loaded.
func (l *boardLimits) release(items, edges int64) {
	if l.loaded {
		l.items, l.edges = l.items-items, l.edges-edges
	}
}

func boardReference(ref string, ids map[string]uuid.UUID) (uuid.UUID, error) {
	if id, ok := ids[ref]; ok {
		return id, nil
	}
	id, err := uuid.Parse(ref)
	if err != nil {
		return uuid.Nil, ErrBoardInvalid
	}
	return id, nil
}

func boardParent(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	board, id uuid.UUID,
	parent *uuid.UUID,
) error {
	seen := map[uuid.UUID]bool{id: true}
	next := parent
	for next != nil {
		if seen[*next] {
			return ErrBoardInvalid
		}
		seen[*next] = true
		if len(seen) > 64 {
			return ErrBoardLimit
		}
		row, err := boardLiveItem(ctx, q, p, board, *next)
		if err != nil {
			return err
		}
		if row.Type != "frame" {
			return ErrBoardInvalid
		}
		next = nil
		if row.ParentFrameID.Valid {
			next = &row.ParentFrameID.UUID
		}
	}
	return nil
}

func boardApplyCommand(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	board uuid.UUID,
	c BoardCommand,
	ids map[string]uuid.UUID,
	changes *boardChanges,
	limits *boardLimits,
) error {
	if c.Type == "create_edge" || c.Type == "update_edge" || c.Type == "delete_edge" || c.Type == "restore_edge" {
		return boardApplyEdge(ctx, q, p, board, c, ids, changes, limits)
	}
	if c.Type == "create_item" {
		return boardCreateItem(ctx, q, p, board, c, ids, changes, limits)
	}
	id := *c.ID
	item, err := q.GetBoardItem(ctx, generated.GetBoardItemParams{CompanyID: p.CompanyID, BoardID: board, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrBoardItemNotFound
	}
	if err != nil {
		return err
	}
	if item.DeletedAt.Valid && c.Type != "restore_item" {
		return ErrBoardItemNotFound
	}
	if !item.DeletedAt.Valid && c.Type == "restore_item" {
		return ErrBoardConflict
	}
	var properties BoardProperties
	if err = json.Unmarshal(item.Properties, &properties); err != nil {
		return err
	}
	switch c.Type {
	case "text":
		if item.Type != "sticky" && item.Type != "text" {
			return ErrBoardInvalid
		}
		if item.LifecycleRevision != c.ExpectedRevision {
			return ErrBoardConflict
		}
		note, e := q.GetBoardNote(
			ctx,
			generated.GetBoardNoteParams{CompanyID: p.CompanyID, BoardID: board, ItemID: id},
		)
		if e != nil {
			return e
		}
		if note.TextRevision != c.ExpectedTextRevision {
			return ErrBoardConflict
		}
		state, e := replaceBoardNote(id, note.State, c.Text)
		if e != nil {
			return boardNoteError(e)
		}
		// The batch stamps the author on every item it touched.
		_, err = q.UpdateBoardNote(
			ctx,
			generated.UpdateBoardNoteParams{
				CompanyID: p.CompanyID,
				BoardID:   board,
				ItemID:    id,
				State:     state,
				PlainText: c.Text,
			},
		)
	case "geometry":
		if item.GeometryRevision != c.ExpectedRevision {
			return ErrBoardConflict
		}
		g := *c.Geometry
		// A lock prevents moving and resizing. Re-parenting or restacking in
		// place, as undoing a frame deletion does, is not movement.
		if properties.Locked && (g.X != item.X || g.Y != item.Y || g.Width != item.Width ||
			g.Height != item.Height || g.Rotation != item.Rotation) {
			return ErrBoardLocked
		}
		if c.ParentRef != "" {
			parent, err := boardReference(c.ParentRef, ids)
			if err != nil {
				return err
			}
			g.ParentFrameID = &parent
		}
		if err = boardParent(ctx, q, p, board, id, g.ParentFrameID); err != nil {
			return err
		}
		_, err = q.MoveBoardItem(
			ctx,
			generated.MoveBoardItemParams{
				CompanyID:        p.CompanyID,
				BoardID:          board,
				ID:               id,
				X:                g.X,
				Y:                g.Y,
				Width:            g.Width,
				Height:           g.Height,
				Rotation:         g.Rotation,
				StackOrder:       g.StackOrder,
				ParentFrameID:    nullable(g.ParentFrameID),
				ReadingOrder:     g.ReadingOrder,
				UpdatedBy:        p.UserID,
				GeometryRevision: c.ExpectedRevision,
			},
		)
	case "content":
		if item.ContentRevision != c.ExpectedRevision {
			return ErrBoardConflict
		}
		// Properties replace the previous set; a file placement keeps a file.
		if err = boardFileAllowed(ctx, q, p, board, item.Type, c.Properties.AttachmentID); err != nil {
			return err
		}
		raw, e := json.Marshal(c.Properties)
		if e != nil {
			return e
		}
		_, err = q.UpdateBoardItemContent(
			ctx,
			generated.UpdateBoardItemContentParams{
				CompanyID:       p.CompanyID,
				BoardID:         board,
				ID:              id,
				Properties:      raw,
				UpdatedBy:       p.UserID,
				ContentRevision: c.ExpectedRevision,
			},
		)
	case "delete_item", "restore_item":
		if c.ExpectedGeometryRevision > 0 && item.GeometryRevision != c.ExpectedGeometryRevision {
			return ErrBoardConflict
		}
		if c.ExpectedTextRevision > 0 {
			note, e := q.GetBoardNote(
				ctx,
				generated.GetBoardNoteParams{CompanyID: p.CompanyID, BoardID: board, ItemID: id},
			)
			if e != nil {
				return e
			}
			if note.TextRevision != c.ExpectedTextRevision {
				return ErrBoardConflict
			}
		}
		if item.ContentRevision != c.ExpectedRevision {
			return ErrBoardConflict
		}
		deleted := pgtype.Timestamptz{}
		if c.Type == "delete_item" {
			deleted = pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
		}
		if c.Type == "restore_item" {
			if e := limits.reserve(ctx, q, 1, 0); e != nil {
				return e
			}
			if item.ParentFrameID.Valid {
				if e := boardParent(ctx, q, p, board, id, &item.ParentFrameID.UUID); e != nil {
					return e
				}
			}
		}
		_, err = q.SetBoardItemDeleted(
			ctx,
			generated.SetBoardItemDeletedParams{
				CompanyID:       p.CompanyID,
				BoardID:         board,
				ID:              id,
				DeletedAt:       deleted,
				UpdatedBy:       p.UserID,
				ContentRevision: c.ExpectedRevision,
			},
		)
		if err != nil {
			return err
		}
		if c.Type == "delete_item" {
			removed, e := q.DeleteBoardItemEdges(
				ctx,
				generated.DeleteBoardItemEdgesParams{
					CompanyID: p.CompanyID,
					BoardID:   board,
					SourceID:  id,
					UpdatedBy: p.UserID,
				},
			)
			if e != nil {
				return e
			}
			changes.Removed = append(changes.Removed, removed...)
			limits.release(1, int64(len(removed)))
			// Only a frame has children to leave behind.
			if item.Type == "frame" {
				children, e := q.DetachBoardChildren(
					ctx,
					generated.DetachBoardChildrenParams{
						CompanyID:     p.CompanyID,
						BoardID:       board,
						ParentFrameID: nullable(&id),
						UpdatedBy:     p.UserID,
					},
				)
				if e != nil {
					return e
				}
				changes.Items = append(changes.Items, children...)
			}
			changes.Removed = append(changes.Removed, id)
		}
	}
	if err != nil {
		return err
	}
	changes.Items = append(changes.Items, id)
	return nil
}

func boardCreateItem(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	board uuid.UUID,
	c BoardCommand,
	ids map[string]uuid.UUID,
	changes *boardChanges,
	limits *boardLimits,
) error {
	if err := limits.reserve(ctx, q, 1, 0); err != nil {
		return err
	}
	var err error
	g := *c.Geometry
	if c.ParentRef != "" {
		parent, err := boardReference(c.ParentRef, ids)
		if err != nil {
			return err
		}
		g.ParentFrameID = &parent
	}
	if err = boardParent(ctx, q, p, board, uuid.Nil, g.ParentFrameID); err != nil {
		return err
	}
	properties := BoardProperties{}
	if c.Properties != nil {
		properties = *c.Properties
	}
	if err = boardFileAllowed(ctx, q, p, board, c.ItemType, properties.AttachmentID); err != nil {
		return err
	}
	raw, err := json.Marshal(properties)
	if err != nil {
		return err
	}
	item, err := q.CreateBoardItem(
		ctx,
		generated.CreateBoardItemParams{
			CompanyID:     p.CompanyID,
			BoardID:       board,
			Type:          c.ItemType,
			X:             g.X,
			Y:             g.Y,
			Width:         g.Width,
			Height:        g.Height,
			Rotation:      g.Rotation,
			StackOrder:    g.StackOrder,
			ParentFrameID: nullable(g.ParentFrameID),
			ReadingOrder:  g.ReadingOrder,
			Properties:    raw,
			CreatedBy:     p.UserID,
		},
	)
	if err != nil {
		return err
	}
	if c.ItemType == "sticky" || c.ItemType == "text" {
		state, err := newBoardNote(item.ID, c.Text)
		if err != nil {
			return boardNoteError(err)
		}
		if err = q.CreateBoardNote(
			ctx,
			generated.CreateBoardNoteParams{
				CompanyID: p.CompanyID,
				BoardID:   board,
				ItemID:    item.ID,
				State:     state,
				PlainText: c.Text,
			},
		); err != nil {
			return err
		}
	}
	if c.ItemType == "stroke" {
		points, err := json.Marshal(boardStrokePoints(c.Points))
		if err != nil {
			return err
		}
		if err = q.CreateBoardStroke(
			ctx,
			generated.CreateBoardStrokeParams{
				CompanyID: p.CompanyID,
				BoardID:   board,
				ItemID:    item.ID,
				Points:    points,
			},
		); err != nil {
			return err
		}
	}
	ids[c.DraftID] = item.ID
	changes.Items = append(changes.Items, item.ID)
	return nil
}

func boardApplyEdge(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	board uuid.UUID,
	c BoardCommand,
	ids map[string]uuid.UUID,
	changes *boardChanges,
	limits *boardLimits,
) error {
	if c.Type == "create_edge" {
		if err := limits.reserve(ctx, q, 0, 1); err != nil {
			return err
		}
		source, err := boardReference(c.SourceRef, ids)
		if err != nil {
			return err
		}
		target, err := boardReference(c.TargetRef, ids)
		if err != nil {
			return err
		}
		style, err := boardDefaultEdgeStyle().with(c).check(source == target)
		if err != nil {
			return err
		}
		for _, id := range []uuid.UUID{source, target} {
			if _, err := boardLiveItem(ctx, q, p, board, id); err != nil {
				return err
			}
		}
		edge, err := q.CreateBoardEdge(
			ctx,
			generated.CreateBoardEdgeParams{
				CompanyID:   p.CompanyID,
				BoardID:     board,
				SourceID:    source,
				TargetID:    target,
				Label:       c.Label,
				Relation:    c.Relation,
				CreatedBy:   p.UserID,
				Line:        string(style.Line),
				StartMarker: string(style.Start),
				EndMarker:   string(style.End),
				Route:       string(style.Route),
				AnchorY:     boardAnchorParam(style.AnchorY),
				SourceLabel: style.SourceLabel,
				TargetLabel: style.TargetLabel,
			},
		)
		if err != nil {
			return err
		}
		ids[c.DraftID] = edge.ID
		changes.Edges = append(changes.Edges, edge.ID)
		return nil
	}
	id := *c.ID
	edge, err := q.GetBoardEdge(ctx, generated.GetBoardEdgeParams{CompanyID: p.CompanyID, BoardID: board, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrBoardItemNotFound
	}
	if err != nil {
		return err
	}
	if edge.DeletedAt.Valid && c.Type != "restore_edge" {
		return ErrBoardItemNotFound
	}
	if edge.Revision != c.ExpectedRevision {
		return ErrBoardConflict
	}
	if !edge.DeletedAt.Valid && c.Type == "restore_edge" {
		return ErrBoardConflict
	}
	if c.Type == "update_edge" {
		style, e := boardEdgeStyleOf(edge).with(c).check(edge.SourceID == edge.TargetID)
		if e != nil {
			return e
		}
		_, err = q.UpdateBoardEdge(
			ctx,
			generated.UpdateBoardEdgeParams{
				CompanyID:   p.CompanyID,
				BoardID:     board,
				ID:          id,
				Label:       c.Label,
				Relation:    c.Relation,
				UpdatedBy:   p.UserID,
				Revision:    c.ExpectedRevision,
				Line:        string(style.Line),
				StartMarker: string(style.Start),
				EndMarker:   string(style.End),
				Route:       string(style.Route),
				AnchorY:     boardAnchorParam(style.AnchorY),
				SourceLabel: style.SourceLabel,
				TargetLabel: style.TargetLabel,
			},
		)
	} else {
		deleted := pgtype.Timestamptz{}
		if c.Type == "delete_edge" {
			deleted = pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
			changes.Removed = append(changes.Removed, id)
			limits.release(0, 1)
		} else {
			for _, target := range []uuid.UUID{edge.SourceID, edge.TargetID} {
				if _, e := boardLiveItem(ctx, q, p, board, target); e != nil {
					return e
				}
			}
			if e := limits.reserve(ctx, q, 0, 1); e != nil {
				return e
			}
		}
		_, err = q.SetBoardEdgeDeleted(
			ctx,
			generated.SetBoardEdgeDeletedParams{
				CompanyID: p.CompanyID,
				BoardID:   board,
				ID:        id,
				DeletedAt: deleted,
				UpdatedBy: p.UserID,
				Revision:  c.ExpectedRevision,
			},
		)
	}
	if err != nil {
		return err
	}
	changes.Edges = append(changes.Edges, id)
	return nil
}

func (s *DefaultBoardService) Text(
	ctx context.Context,
	p auth.UserPrincipal,
	scope BoardScope,
	input BoardTextInput,
) (BoardTextResult, error) {
	id := scope.BoardID
	result := BoardTextResult{}
	var pending events.Event
	var replayed bool
	if input.LifecycleRevision < 1 || input.RequestKey == uuid.Nil || input.ItemID == uuid.Nil ||
		len(input.Update) == 0 {
		return result, ErrBoardInvalid
	}
	if len(input.Update) > boardMaxTextUpdate || len(input.StateVector) > boardMaxStateVector {
		return result, ErrBoardLimit
	}
	hash, err := boardHash(input)
	if err != nil {
		return result, err
	}
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) (txErr error) {
		board, viewer, err := boardScopeLock(ctx, q, p, scope, true)
		defer func() {
			if txErr == nil {
				pending = boardChangeEvent(p, id, result.Sequence)
				pending.Actor = boardEventActor(p, viewer)
				txErr = stampBoardAudience(ctx, q, &pending)
			}
		}()
		if err != nil {
			return err
		}
		receipt, err := q.GetBoardReceipt(
			ctx,
			generated.GetBoardReceiptParams{
				BoardID:     id,
				ActorUserID: p.UserID,
				ActorRunID:  boardReceiptRun(viewer),
				RequestKey:  input.RequestKey,
			},
		)
		if err == nil {
			if !bytes.Equal(hash, receipt.RequestHash) {
				return ErrBoardRequestKeyReused
			}
			replayed = true
			// The same request names the same note; answer from its current state.
			result.ItemID = input.ItemID
			note, err := q.GetBoardNote(
				ctx,
				generated.GetBoardNoteParams{CompanyID: p.CompanyID, BoardID: id, ItemID: input.ItemID},
			)
			if err != nil {
				return err
			}
			update, _, err := wiki.BlockStateDelta(note.State, input.StateVector)
			if err != nil {
				return ErrBoardInvalid
			}
			result.Sequence = board.HeadSeq
			result.TextRevision = note.TextRevision
			result.Text = note.PlainText
			result.Update = update
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		item, err := boardLiveItem(ctx, q, p, id, input.ItemID)
		if err != nil {
			return err
		}
		if item.LifecycleRevision != input.LifecycleRevision {
			return ErrBoardConflict
		}
		if item.Type != "sticky" && item.Type != "text" {
			return ErrBoardInvalid
		}
		note, err := q.GetBoardNote(
			ctx,
			generated.GetBoardNoteParams{CompanyID: p.CompanyID, BoardID: id, ItemID: input.ItemID},
		)
		if err != nil {
			return err
		}
		state, text, err := mergeBoardNote(input.ItemID, note.State, input.Update)
		if err != nil {
			return boardNoteError(err)
		}
		revision, err := q.UpdateBoardNote(
			ctx,
			generated.UpdateBoardNoteParams{
				CompanyID: p.CompanyID,
				BoardID:   id,
				ItemID:    input.ItemID,
				State:     state,
				PlainText: text,
			},
		)
		if err != nil {
			return err
		}
		agent, run := boardAttribution(viewer)
		if err = q.StampBoardItemsAttribution(
			ctx,
			generated.StampBoardItemsAttributionParams{
				CompanyID:  p.CompanyID,
				BoardID:    id,
				Ids:        []uuid.UUID{input.ItemID},
				CreatedIds: []uuid.UUID{},
				UpdatedBy:  p.UserID,
				AgentID:    agent,
				RunID:      run,
			},
		); err != nil {
			return err
		}
		seq, err := boardAdvance(ctx, q, p, id, boardChanges{Items: []uuid.UUID{input.ItemID}})
		if err != nil {
			return err
		}
		update, _, err := wiki.BlockStateDelta(state, input.StateVector)
		if err != nil {
			return ErrBoardInvalid
		}
		result = BoardTextResult{
			Sequence:     seq,
			ItemID:       input.ItemID,
			TextRevision: revision,
			Text:         text,
			Update:       update,
		}
		// Receipts retain the acknowledgement only. The current accepted note
		// produces retry deltas; keystrokes never append another full CRDT copy.
		return q.InsertBoardReceipt(
			ctx,
			generated.InsertBoardReceiptParams{
				CompanyID:   p.CompanyID,
				BoardID:     id,
				ActorUserID: p.UserID,
				ActorRunID:  boardReceiptRun(viewer),
				RequestKey:  input.RequestKey,
				RequestHash: hash,
				Seq:         seq,
			},
		)
	})
	if err == nil && !replayed {
		s.publishEvent(ctx, pending)
	}
	return result, boardError(err)
}

// boardLiveItem reads an item a command names; one the Board no longer has is
// the command's problem, not the Board's.
func boardLiveItem(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	board, id uuid.UUID,
) (generated.BoardItem, error) {
	item, err := q.GetBoardItem(ctx, generated.GetBoardItemParams{CompanyID: p.CompanyID, BoardID: board, ID: id})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && item.DeletedAt.Valid) {
		return item, ErrBoardItemNotFound
	}
	return item, err
}

// boardNoteError keeps a note size limit a limit; anything else the note
// codec refuses is an invalid edit.
func boardNoteError(err error) error {
	if errors.Is(err, ErrBoardLimit) {
		return ErrBoardLimit
	}
	return ErrBoardInvalid
}
