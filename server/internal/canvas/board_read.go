package canvas

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/util"
	"boardly.dev/boardly/server/internal/wiki"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func (s *DefaultBoardService) Snapshot(
	ctx context.Context,
	p auth.UserPrincipal,
	scope BoardScope,
	after int64,
) (BoardSnapshot, error) {
	var result BoardSnapshot
	err := s.store.WithinTx(ctx, boardRead, func(q *generated.Queries) error {
		b, viewer, err := boardScopeLock(ctx, q, p, scope, false)
		if err != nil {
			return err
		}
		result, err = boardSnapshotInTx(ctx, q, p, b, after)
		result.Board.CanEdit = viewer.CanEdit
		result.Board.CanShare = viewer.CanShare
		result.Board.CanManage = viewer.CanShare && result.Board.CanManage
		return err
	})
	return result, boardError(err)
}

// The Board row lock excludes writers for the entire snapshot, including note
// hydration and capture. Subscription hints can race this read; head sequence
// catch-up always observes the next committed transaction.
func boardSnapshotInTx(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	b generated.Board,
	after int64,
) (BoardSnapshot, error) {
	result := BoardSnapshot{
		Board:    boardDTO(b, p),
		Sequence: b.HeadSeq,
		Full:     after == 0 || after > b.HeadSeq,
		Items:    []BoardItem{},
		Edges:    []BoardEdge{},
		Removed:  []uuid.UUID{},
	}
	favorite, err := q.BoardFavorite(ctx, generated.BoardFavoriteParams{BoardID: b.ID, UserID: p.UserID})
	if err != nil {
		return result, err
	}
	result.Board.Favorite = favorite
	selected := map[uuid.UUID]bool{}
	if !result.Full && after < b.HeadSeq {
		rows, err := q.ListBoardChanges(ctx, generated.ListBoardChangesParams{BoardID: b.ID, Seq: after})
		if err != nil {
			return result, err
		}
		result.Full = len(rows) == 0 || len(rows) > boardMaxDelta || rows[0].Seq != after+1 ||
			rows[len(rows)-1].Seq != b.HeadSeq
		if !result.Full {
			for _, row := range rows {
				var change boardChanges
				if err = json.Unmarshal(row.Changes, &change); err != nil {
					return result, err
				}
				for _, id := range append(append(change.Items, change.Edges...), change.Removed...) {
					selected[id] = true
				}
			}
		}
	}
	if len(selected) > boardMaxItems {
		result.Full = true
	}
	if !result.Full && len(selected) == 0 {
		return result, nil
	}
	if result.Full {
		selected = nil
	}
	result.Items, result.Edges, result.Removed, err = boardObjectsInTx(ctx, q, p, b, selected)
	return result, err
}

// boardObjectsInTx loads the current state of the selected items and
// connectors, or of the whole live scene when selected is nil. A selected
// object that was deleted, or that retention already purged, is reported as
// removed.
func boardObjectsInTx(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	b generated.Board,
	selected map[uuid.UUID]bool,
) ([]BoardItem, []BoardEdge, []uuid.UUID, error) {
	items, edges, removed := []BoardItem{}, []BoardEdge{}, []uuid.UUID{}
	full := selected == nil
	ids := make([]uuid.UUID, 0, len(selected))
	for id := range selected {
		ids = append(ids, id)
	}
	rows, err := q.ListBoardItems(
		ctx,
		generated.ListBoardItemsParams{
			CompanyID:      p.CompanyID,
			BoardID:        b.ID,
			IncludeDeleted: !full,
			ItemIds:        ids,
		},
	)
	if err != nil {
		return items, edges, removed, err
	}
	for _, row := range rows {
		if !full && !selected[row.ID] {
			continue
		}
		if row.DeletedAt.Valid {
			removed = append(removed, row.ID)
			continue
		}
		item, err := boardItemDTO(row)
		if err != nil {
			return items, edges, removed, err
		}
		items = append(items, item)
	}
	edgeRows, err := q.ListBoardEdges(
		ctx,
		generated.ListBoardEdgesParams{
			CompanyID:      p.CompanyID,
			BoardID:        b.ID,
			IncludeDeleted: !full,
			ItemIds:        ids,
		},
	)
	if err != nil {
		return items, edges, removed, err
	}
	found := make(map[uuid.UUID]bool, len(rows)+len(edgeRows))
	for _, row := range rows {
		found[row.ID] = true
	}
	for _, row := range edgeRows {
		found[row.ID] = true
		if !full && !selected[row.ID] {
			continue
		}
		if row.DeletedAt.Valid {
			removed = append(removed, row.ID)
			continue
		}
		edges = append(edges, boardEdgeDTO(row))
	}
	// A delta can name a tombstone that retention already purged.
	for id := range selected {
		if !found[id] {
			removed = append(removed, id)
		}
	}
	return items, edges, removed, nil
}

func boardItemDTO(row generated.ListBoardItemsRow) (BoardItem, error) {
	item := BoardItem{
		ID:   row.ID,
		Type: row.Type,
		BoardGeometry: BoardGeometry{
			X:            row.X,
			Y:            row.Y,
			Width:        row.Width,
			Height:       row.Height,
			Rotation:     row.Rotation,
			StackOrder:   row.StackOrder,
			ReadingOrder: row.ReadingOrder,
		},
		GeometryRevision:  row.GeometryRevision,
		ContentRevision:   row.ContentRevision,
		TextRevision:      row.TextRevision,
		LifecycleRevision: row.LifecycleRevision,
		Text:              row.Text,
		TextTruncated:     row.TextTruncated,
		CreatedBy:         boardActor(row.CreatedBy, row.CreatedByAgentID, row.CreatorName),
		UpdatedBy:         boardActor(row.UpdatedBy, row.UpdatedByAgentID, row.UpdaterName),

		CreatedByRunID: util.NullUUIDPtr(
			row.CreatedByRunID,
		),
		UpdatedByRunID: util.NullUUIDPtr(row.UpdatedByRunID),
		Deleted:        row.DeletedAt.Valid,
	}
	if row.ParentFrameID.Valid {
		item.ParentFrameID = &row.ParentFrameID.UUID
	}
	if row.AttachmentID.Valid && row.FileFilename.Valid {
		item.File = &BoardFile{
			AttachmentID: row.AttachmentID.UUID,
			Filename:     row.FileFilename.String,
			ContentType:  row.FileContentType.String,
			SizeBytes:    row.FileSizeBytes.Int64,
			HasPreview:   row.FileHasPreview,
		}
		if row.FileWidth.Valid {
			item.File.Width, item.File.Height = &row.FileWidth.Int32, &row.FileHeight.Int32
		}
	}
	err := json.Unmarshal(row.Properties, &item.Properties)
	return item, err
}

func boardEdgeDTO(row generated.ListBoardEdgesRow) BoardEdge {
	return BoardEdge{
		ID:          row.ID,
		SourceID:    row.SourceID,
		TargetID:    row.TargetID,
		Label:       row.Label,
		Relation:    row.Relation,
		Line:        BoardLineType(row.Line),
		StartMarker: BoardMarkerType(row.StartMarker),
		EndMarker:   BoardMarkerType(row.EndMarker),
		Route:       BoardRouteType(row.Route),
		AnchorY:     boardAnchorValue(row.AnchorY),
		SourceLabel: row.SourceLabel,
		TargetLabel: row.TargetLabel,
		Revision:    row.Revision,
		CreatedBy:   boardActor(row.CreatedBy, row.CreatedByAgentID, row.CreatorName),
		UpdatedBy:   boardActor(row.UpdatedBy, row.UpdatedByAgentID, row.UpdaterName),

		CreatedByRunID: util.NullUUIDPtr(
			row.CreatedByRunID,
		),
		UpdatedByRunID: util.NullUUIDPtr(row.UpdatedByRunID),
		Deleted:        row.DeletedAt.Valid,
	}
}

func (s *DefaultBoardService) Hydrate(
	ctx context.Context,
	p auth.UserPrincipal,
	scope BoardScope,
	input BoardHydrateInput,
) (BoardHydration, error) {
	result := BoardHydration{Items: []BoardHydratedItem{}, Missing: []uuid.UUID{}}
	err := s.store.WithinTx(ctx, boardRead, func(q *generated.Queries) error {
		board, _, err := boardScopeLock(ctx, q, p, scope, false)
		if err != nil {
			return err
		}
		// A Run edits text through semantic commands, never CRDT updates.
		var hydrateErr error
		result, hydrateErr = boardHydrateInTx(ctx, q, board, input, scope.RunID == uuid.Nil)
		return hydrateErr
	})
	return result, boardError(err)
}

func (s *DefaultBoardService) Context(
	ctx context.Context,
	p auth.UserPrincipal,
	scope BoardScope,
	input BoardContextInput,
) (BoardContext, error) {
	id := scope.BoardID
	result := BoardContext{
		Items:      []BoardItem{},
		Edges:      []BoardEdge{},
		OmittedIDs: []uuid.UUID{},
	}
	if input.MaxItems == 0 {
		input.MaxItems = 200
	}
	if input.MaxBytes == 0 {
		input.MaxBytes = 128 << 10
	}
	invalidLimits := input.MaxItems < 1 || input.MaxItems > boardMaxContextItems || input.MaxBytes < 1024 ||
		input.MaxBytes > 1<<20
	invalidContinuation := input.VisualOffset < 0 || (input.ExpectedSequence != nil && *input.ExpectedSequence < 0)
	if invalidLimits || invalidContinuation || len(input.ItemIDs) > boardMaxContextItems {
		return result, ErrBoardInvalid
	}
	var run *BoardViewer
	err := s.store.WithinTx(ctx, boardRead, func(q *generated.Queries) error {
		board, viewer, err := boardScopeLock(ctx, q, p, scope, false)
		if err != nil {
			return err
		}
		if run, err = runViewer(ctx, q, p, scope, viewer, nil); err != nil {
			return err
		}
		snapshot, err := boardSnapshotInTx(ctx, q, p, board, 0)
		if err != nil {
			return err
		}
		result.Board = snapshot.Board
		result.Board.CanEdit = viewer.CanEdit
		result.Board.CanShare = viewer.CanShare
		result.Board.CanManage = viewer.CanShare && result.Board.CanManage
		result.Sequence = snapshot.Sequence
		wanted := map[uuid.UUID]bool{}
		for _, itemID := range input.ItemIDs {
			wanted[itemID] = true
		}
		included := map[uuid.UUID]bool{}
		omitted := map[uuid.UUID]bool{}
		// Budget from text sizes, then load only the included notes' text.
		sizes, err := q.ListBoardNoteTextSizes(
			ctx,
			generated.ListBoardNoteTextSizesParams{CompanyID: p.CompanyID, BoardID: id},
		)
		if err != nil {
			return err
		}
		textBytes := make(map[uuid.UUID]int, len(sizes))
		for _, size := range sizes {
			textBytes[size.ItemID] = int(size.TextBytes)
		}
		// JSON and Markdown repeat text; reserve room for both and context metadata.
		used := len(board.Title) + len(board.Purpose) + 1024
		var notes []uuid.UUID
		for _, item := range snapshot.Items {
			if len(wanted) > 0 && !wanted[item.ID] {
				continue
			}
			item.Text, item.TextTruncated = "", false
			raw, err := json.Marshal(item)
			if err != nil {
				return err
			}
			cost := len(raw) + 2*textBytes[item.ID] + 800
			if len(result.Items) >= input.MaxItems || used+cost > input.MaxBytes {
				result.Truncated = true
				omitted[item.ID] = true
				continue
			}
			used += cost
			result.Items = append(result.Items, item)
			included[item.ID] = true
			if item.Type == "sticky" || item.Type == "text" {
				notes = append(notes, item.ID)
			}
		}
		if len(notes) > 0 {
			texts, err := q.ListBoardNoteTexts(
				ctx,
				generated.ListBoardNoteTextsParams{CompanyID: p.CompanyID, BoardID: id, ItemIds: notes},
			)
			if err != nil {
				return err
			}
			full := make(map[uuid.UUID]string, len(texts))
			for _, text := range texts {
				full[text.ItemID] = text.PlainText
			}
			for i := range result.Items {
				result.Items[i].Text = full[result.Items[i].ID]
			}
		}
		for itemID := range wanted {
			if !included[itemID] {
				omitted[itemID] = true
			}
		}
		for _, edge := range snapshot.Edges {
			if !included[edge.SourceID] && !included[edge.TargetID] {
				continue
			}
			raw, err := json.Marshal(edge)
			if err != nil {
				return err
			}
			if used+2*len(raw)+100 > input.MaxBytes {
				result.Truncated = true
				omitted[edge.ID] = true
				continue
			}
			used += 2*len(raw) + 100
			result.Edges = append(result.Edges, edge)
		}
		// Estimates count raw bytes; escaping and repeated headers can exceed
		// them, so trim the last items until the real answer fits instead of
		// refusing the whole read.
		for {
			raw, err := boardContextFinish(&result, board, included, omitted, used, input.MaxBytes)
			if err != nil {
				return err
			}
			if len(raw) <= input.MaxBytes {
				return nil
			}
			if len(result.Items) == 0 {
				return ErrBoardLimit
			}
			drop := max(1, len(result.Items)*(len(raw)-input.MaxBytes)/len(raw)+1)
			for _, item := range result.Items[len(result.Items)-min(drop, len(result.Items)):] {
				delete(included, item.ID)
				omitted[item.ID] = true
			}
			result.Items = result.Items[:len(result.Items)-min(drop, len(result.Items))]
			result.Edges = slices.DeleteFunc(result.Edges, func(edge BoardEdge) bool {
				drop := !included[edge.SourceID] && !included[edge.TargetID]
				if drop {
					omitted[edge.ID] = true
				}
				return drop
			})
			result.Truncated = true
		}
	})
	if err == nil {
		s.keepRunViewer(p, scope, run)
	}
	return result, boardError(err)
}

// boardContextFinish derives the omitted references and the Markdown from the
// included items and edges, and returns the answer's encoded size.
func boardContextFinish(
	result *BoardContext,
	board generated.Board,
	included, omitted map[uuid.UUID]bool,
	used, maxBytes int,
) ([]byte, error) {
	references := maps.Clone(omitted)
	for _, edge := range result.Edges {
		// A connector to an object outside the context names that object.
		for _, end := range []uuid.UUID{edge.SourceID, edge.TargetID} {
			if !included[end] {
				references[end] = true
			}
		}
	}
	for _, item := range result.Items {
		if item.ParentFrameID != nil && !included[*item.ParentFrameID] {
			references[*item.ParentFrameID] = true
		}
	}
	result.OmittedCount = len(references)
	omittedIDs := make([]uuid.UUID, 0, len(references))
	for omittedID := range references {
		omittedIDs = append(omittedIDs, omittedID)
	}
	slices.SortFunc(omittedIDs, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	result.OmittedIDsTruncated = false
	omissionLimit := min(128, max(0, (maxBytes-used-1024)/160))
	if len(omittedIDs) > omissionLimit {
		result.OmittedIDsTruncated = true
		omittedIDs = omittedIDs[:omissionLimit]
	}
	result.OmittedIDs = omittedIDs
	var md strings.Builder
	fmt.Fprintf(
		&md,
		"# %s\n\n%s\n\nBoard %s, committed sequence %d.\n",
		board.Title,
		board.Purpose,
		board.ID,
		board.HeadSeq,
	)
	for _, item := range result.Items {
		fmt.Fprintf(
			&md,
			"\n## %s %s\n\nID: %s; geometry revision %d; content revision %d; text revision %d.\nCreated by %s (%s); updated by %s (%s).\n[Open item](/whiteboards/%s?companyId=%s&itemId=%s)\n\n%s\n",
			item.Type,
			item.Properties.Title,
			item.ID,
			item.GeometryRevision,
			item.ContentRevision,
			item.TextRevision,
			item.CreatedBy.DisplayName,
			item.CreatedBy.ID,
			item.UpdatedBy.DisplayName,
			item.UpdatedBy.ID,
			board.ID,
			board.CompanyID,
			item.ID,
			item.Text,
		)
		if item.ParentFrameID != nil {
			fmt.Fprintf(&md, "Parent frame: %s\n", *item.ParentFrameID)
		}
		if item.Properties.Shape != "" {
			fmt.Fprintf(&md, "Shape: %s %q\n", item.Properties.Shape, item.Properties.Title)
		}
		if item.Properties.Head != "" {
			fmt.Fprintf(&md, "Head: %s\n", item.Properties.Head)
		}
		for i, section := range item.Properties.Sections {
			fmt.Fprintf(&md, "Section %d: %q\n", i+1, section)
		}
		if item.File != nil {
			fmt.Fprintf(
				&md,
				"File: %s (%s, %d bytes, attachment %s)\n",
				item.File.Filename,
				item.File.ContentType,
				item.File.SizeBytes,
				item.File.AttachmentID,
			)
		}
	}
	for _, edge := range result.Edges {
		style := edge.notation()
		fmt.Fprintf(
			&md,
			"\nExplicit relationship %s: %s → %s; label %q; relation %q; %s %s line, %s to %s; ends %q, %q; revision %d.\n",
			edge.ID,
			edge.SourceID,
			edge.TargetID,
			edge.Label,
			edge.Relation,
			style.Route,
			style.Line,
			style.Start,
			style.End,
			style.SourceLabel,
			style.TargetLabel,
			edge.Revision,
		)
	}
	if len(result.OmittedIDs) > 0 {
		fmt.Fprintf(&md, "\nOmitted item/relationship targets: %v.\n", result.OmittedIDs)
	}
	if result.OmittedIDsTruncated {
		fmt.Fprintf(&md, "\n%d omitted IDs in total; the ID list is bounded.\n", result.OmittedCount)
	}
	if result.Truncated {
		md.WriteString("\nContext is truncated by the requested limits.\n")
	}
	md.WriteString("\nPosition and color do not imply factual relationships.\n")
	result.Markdown = md.String()
	return json.Marshal(result)
}

// boardHydrateInTx loads note text and ink for the requested items. An ID that
// no longer names a live item is reported in Missing rather than failing the
// batch. withState includes CRDT note state for clients that edit.
func boardHydrateInTx(
	ctx context.Context,
	q *generated.Queries,
	board generated.Board,
	input BoardHydrateInput,
	withState bool,
) (BoardHydration, error) {
	result := BoardHydration{Items: []BoardHydratedItem{}, Missing: []uuid.UUID{}}
	result.Sequence = board.HeadSeq
	if len(input.StateVectors) > len(input.ItemIDs) {
		return result, ErrBoardInvalid
	}
	vectors := make(map[uuid.UUID][]byte, len(input.StateVectors))
	for key, vector := range input.StateVectors {
		itemID, err := uuid.Parse(key)
		if err != nil {
			return result, ErrBoardInvalid
		}
		vectors[itemID] = vector
	}
	rows, err := q.ListBoardHydration(
		ctx,
		generated.ListBoardHydrationParams{
			CompanyID: board.CompanyID,
			BoardID:   board.ID,
			ItemIds:   input.ItemIDs,
		},
	)
	if err != nil {
		return result, err
	}
	byID := make(map[uuid.UUID]generated.ListBoardHydrationRow, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	// Bound aggregate response even when all notes independently meet their limit.
	size := 0
	seen := map[uuid.UUID]bool{}
	for _, itemID := range input.ItemIDs {
		if seen[itemID] {
			continue
		}
		seen[itemID] = true
		item, ok := byID[itemID]
		if !ok || item.DeletedAt.Valid {
			result.Missing = append(result.Missing, itemID)
			continue
		}
		hydrated := BoardHydratedItem{ID: itemID, LifecycleRevision: item.LifecycleRevision}
		switch item.Type {
		case "sticky", "text":
			hydrated.Text = item.PlainText.String
			hydrated.TextRevision = item.TextRevision.Int64
			if withState {
				hydrated.State = item.State
			}
			if vector := vectors[itemID]; withState && len(vector) > 0 {
				update, _, err := wiki.BlockStateDelta(item.State, vector)
				if err != nil {
					return result, ErrBoardInvalid
				}
				hydrated.State, hydrated.Delta = update, true
			}
			size += len(hydrated.State) + len(hydrated.Text)
		case "stroke":
			if err = json.Unmarshal(item.Points, &hydrated.Points); err != nil {
				return result, err
			}
			size += len(item.Points)
		}
		if size > boardMaxHydration {
			return result, ErrBoardLimit
		}
		result.Items = append(result.Items, hydrated)
	}
	return result, nil
}
