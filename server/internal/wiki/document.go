package wiki

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/redact"
)

// Document is the canonical saved editing state. Published readers receive the
// immutable projection on PageResponse instead, never this working draft.
type Document struct {
	IntelligentUIAuthoringEnabled bool            `json:"intelligentUIAuthoringEnabled"`
	PageID                        uuid.UUID       `json:"pageId"`
	State                         []byte          `json:"state"`
	StateVector                   []byte          `json:"stateVector,omitempty"`
	Content                       json.RawMessage `json:"content,omitempty"`
	Markdown                      string          `json:"markdown"`
	Sequence                      int64           `json:"sequence"`
	StructureSequence             int64           `json:"structureSequence"`
	SchemaVersion                 int32           `json:"schemaVersion"`
	CanEdit                       bool            `json:"canEdit"`
	Title                         string          `json:"title"`
	Summary                       string          `json:"summary"`
	CollectionID                  *uuid.UUID      `json:"collectionId"`
}

type DocumentReadInput struct {
	Since       int64
	StateVector []byte
	Projection  bool
}
type SyncDocumentInput struct {
	SchemaVersion int
	Update        []byte
	BaseSequence  int64
}
type DraftMetadataInput struct {
	ExpectedSequence int64
	Title, Summary   string
	CollectionID     *uuid.UUID
}
type (
	PublishPageInput     struct{ ExpectedRevision, ExpectedSequence int64 }
	RestoreRevisionInput struct {
		ExpectedRevision, ExpectedSequence int64
		SchemaVersion                      int
	}
)

func revisionContent(content []byte, markdown string) json.RawMessage {
	if len(content) > 2 {
		return content
	}
	root := BlocksFromMarkdown(markdown)
	data, _ := json.Marshal(root)
	return data
}

func (s *DefaultWikiService) documentResponse(d generated.WikiCanvasDraft) Document {
	return Document{
		IntelligentUIAuthoringEnabled: !s.authoring.Disabled,
		PageID:                        d.PageID, State: d.State, Content: d.Content, Markdown: d.Markdown,
		Sequence: d.HeadSeq, StructureSequence: d.StructureSeq, SchemaVersion: d.SchemaVersion,
		CanEdit: true, Title: d.Title, Summary: d.Summary, CollectionID: uuidPointer(d.CollectionID),
	}
}

// Wiki lock order, shared by every Wiki write and the lifecycle writers it
// must fence (Company mutation, membership revoke and role change, account
// deletion):
//
//	Company -> acting membership -> [research writes] -> [collections] ->
//	page -> draft
//
// Company and membership are taken FOR SHARE here. That is a complete
// lifecycle fence: every writer of those rows updates or deletes the exact
// Company or membership row held here, so it conflicts directly. Revoke, role
// change, invitation accept and Company deletion also take the Company row
// FOR UPDATE first; account deletion revokes the membership row itself. A Wiki
// write either commits before the change or sees the revoked or demoted
// membership, because authorizePublisher reads it under the share lock. The Wiki never needs the exclusive Company lock: it
// would make every page create, archive or collection change wait for all
// in-flight autosaves Company-wide and stall them in turn.
//
// The set-level Wiki invariants are protected below the Company, not by it:
// collection names by the (company_id, lower(name)) unique index, revision
// numbers and archive status by the page row lock, and a collection's
// existence while pages are filed into it by the shared/exclusive collections
// advisory lock (writers that file a page take lockFilingWiki, only
// DeleteCollection takes the exclusive side).
func authorizePublisher(ctx context.Context, q *generated.Queries, p auth.UserPrincipal) error {
	if _, err := q.LockCompanyForMembershipRead(ctx, p.CompanyID); errors.Is(err, pgx.ErrNoRows) {
		return ErrPageNotFound
	} else if err != nil {
		return fmt.Errorf("lock Wiki Company: %w", err)
	}
	member, err := q.GetCompanyMemberForShare(
		ctx,
		generated.GetCompanyMemberForShareParams{CompanyID: p.CompanyID, UserID: p.UserID},
	)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && member.Status != "active" {
		return ErrPageNotFound
	}
	if err != nil {
		return fmt.Errorf("lock Wiki actor membership: %w", err)
	}
	if !auth.CompanyRole(member.Role).Can(auth.PermissionWikiPublish) {
		return ErrPageForbidden
	}
	return nil
}

// lockEditableWiki fences the actor and locks the page for a draft edit that
// leaves the page's collection alone (autosave, block ids, the first open).
func lockEditableWiki(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	id uuid.UUID,
) error {
	if err := authorizePublisher(ctx, q, p); err != nil {
		return err
	}
	_, err := lockActivePage(ctx, q, p.CompanyID, id)
	return err
}

// lockFilingWiki is lockEditableWiki for a write that can file the page into a
// collection (draft metadata, publish, restore): it takes the shared
// collections lock between the membership fence and the page lock, the order
// DeleteCollection's exclusive lock relies on.
func lockFilingWiki(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	id uuid.UUID,
) (generated.GetWikiPageForUpdateRow, error) {
	if err := authorizePublisher(ctx, q, p); err != nil {
		return generated.GetWikiPageForUpdateRow{}, err
	}
	if err := q.LockWikiCollectionsShared(ctx, p.CompanyID); err != nil {
		return generated.GetWikiPageForUpdateRow{}, fmt.Errorf("lock Wiki collections: %w", err)
	}
	return lockActivePage(ctx, q, p.CompanyID, id)
}

func lockActivePage(
	ctx context.Context,
	q *generated.Queries,
	companyID, id uuid.UUID,
) (generated.GetWikiPageForUpdateRow, error) {
	row, err := q.GetWikiPageForUpdate(
		ctx,
		generated.GetWikiPageForUpdateParams{CompanyID: companyID, PageID: id},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return row, ErrPageNotFound
	}
	if err != nil {
		return row, err
	}
	if row.Status != "active" {
		return row, ErrPageArchived
	}
	return row, nil
}

// readUpgradedDraft is ReadDocument's lock-free path: a read-only snapshot that
// applies authorizePublisher's and lockEditableWiki's rules without taking the
// Company, member, page or draft locks, so a reader never queues behind an
// autosave. ok is false when no draft exists yet or it still needs the
// legacy-Markdown upgrade; only the locked path may create or upgrade it.
func (s *DefaultWikiService) readUpgradedDraft(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
) (draft generated.WikiCanvasDraft, ok bool, err error) {
	err = s.store.WithinTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(q *generated.Queries) error {
			member, err := q.GetActiveCompanyMembership(
				ctx,
				generated.GetActiveCompanyMembershipParams{CompanyID: p.CompanyID, UserID: p.UserID},
			)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrPageNotFound
			}
			if err != nil {
				return err
			}
			if !auth.CompanyRole(member.Role).Can(auth.PermissionWikiPublish) {
				return ErrPageForbidden
			}
			page, err := q.GetEditableWikiPage(
				ctx,
				generated.GetEditableWikiPageParams{CompanyID: p.CompanyID, PageID: id},
			)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrPageNotFound
			}
			if err != nil {
				return err
			}
			if page.Status != "active" {
				return ErrPageArchived
			}
			draft, err = q.GetCanvasDraft(
				ctx,
				generated.GetCanvasDraftParams{CompanyID: p.CompanyID, PageID: id},
			)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			ok = err == nil && draft.SchemaVersion >= 1
			return err
		},
	)
	return draft, ok, err
}

func (s *DefaultWikiService) ReadDocument(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
	input DocumentReadInput,
) (Document, error) {
	draft, ok, err := s.readUpgradedDraft(ctx, p, id)
	if err != nil {
		return Document{}, err
	}
	if !ok {
		err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
			if err := lockEditableWiki(ctx, q, p, id); err != nil {
				return err
			}
			draft, err = BlockDraftInTx(ctx, q, p.CompanyID, id)
			return err
		})
		if err != nil {
			return Document{}, err
		}
	}
	result := s.documentResponse(draft)
	result.CanEdit = result.CanEdit && s.authoring.AllowsSchema(int(draft.SchemaVersion))
	if input.Since == draft.HeadSeq {
		result.State = nil
		result.Content = nil
		result.Markdown = ""
		return result, nil
	}
	if input.Projection {
		result.State = nil
		return result, nil
	}
	result.State, result.StateVector, err = BlockStateDelta(draft.State, input.StateVector)
	return result, err
}

// MergeDraftChanges is shared by both document surfaces. Text deltas can merge
// concurrently; structural edits cannot overwrite a structure they did not read.
func MergeDraftChanges(saved Document, update []byte, baseSequence int64) ([]byte, BlockNode, string, bool, error) {
	state, root, markdown, xmlChanged, err := MergeBlockChanges(saved.State, update)
	if err != nil {
		return nil, root, "", false, err
	}
	if bytes.Equal(state, saved.State) {
		return state, root, markdown, false, nil
	}
	var current BlockNode
	if err = json.Unmarshal(saved.Content, &current); err != nil {
		return nil, root, "", false, err
	}
	structural := xmlChanged || BlockStructureChanged(current, root)
	if baseSequence > saved.Sequence || baseSequence < saved.StructureSequence ||
		(structural && baseSequence != saved.Sequence) {
		return nil, root, "", false, ErrPageConflict
	}
	return state, root, markdown, structural, nil
}

func (s *DefaultWikiService) SyncDocument(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
	input SyncDocumentInput,
) (Document, error) {
	if len(input.Update) == 0 || len(input.Update) > 8<<20 || input.BaseSequence < 0 {
		return Document{}, ErrPageInvalidInput
	}
	var result Document
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if err := lockEditableWiki(ctx, q, p, id); err != nil {
			return err
		}
		draft, err := BlockDraftInTx(ctx, q, p.CompanyID, id)
		if err != nil {
			return err
		}
		result = s.documentResponse(draft)
		result.CanEdit = result.CanEdit && s.authoring.AllowsSchema(int(draft.SchemaVersion))
		if err := CheckUIDocumentWrite(input.SchemaVersion, int(draft.SchemaVersion), BlockNode{}); err != nil {
			return err
		}
		state, root, markdown, structural, err := MergeDraftChanges(result, input.Update, input.BaseSequence)
		if err != nil {
			return err
		}
		if err := s.authoring.Check(int(draft.SchemaVersion), root); err != nil {
			return err
		}
		if err := CheckUIDocumentWrite(input.SchemaVersion, int(draft.SchemaVersion), root); err != nil {
			return err
		}
		if bytes.Equal(state, draft.State) {
			return nil
		}
		ids := BlockIDs(root)
		count, err := q.ValidateWikiBlockIDs(
			ctx,
			generated.ValidateWikiBlockIDsParams{CompanyID: p.CompanyID, PageID: id, Ids: ids},
		)
		if err != nil {
			return err
		}
		if count != int64(len(ids)) {
			return ErrPageInvalidInput
		}
		if err = ValidateStandaloneBlocks(root); err != nil {
			return err
		}
		content, err := json.Marshal(root)
		if err != nil {
			return err
		}
		rows, err := q.UpdateWikiBlockDraft(
			ctx,
			generated.UpdateWikiBlockDraftParams{
				SchemaVersion: int32(UIDocumentSchema(root)),
				CompanyID:     p.CompanyID,
				PageID:        id,
				State:         state,
				Content:       content,
				Markdown:      strings.TrimRight(markdown, "\n"),
				HeadSeq:       draft.HeadSeq,
				Structural:    structural,
			},
		)
		if err != nil {
			return err
		}
		if rows != 1 {
			return ErrPageConflict
		}
		if err = RecordDocumentChangeInTx(
			ctx,
			q,
			DocumentChangeInput{
				CompanyID:  p.CompanyID,
				PageID:     id,
				ActorType:  "user",
				ActorID:    p.UserID,
				ChangeType: "edit",
				Sequence:   draft.HeadSeq + 1,
				Before:     draft.State,
				State:      state,
			},
		); err != nil {
			return err
		}
		result.State, result.Content, result.Markdown = state, content, strings.TrimRight(markdown, "\n")
		result.SchemaVersion = max(draft.SchemaVersion, int32(UIDocumentSchema(root)))
		result.Sequence++
		if structural {
			result.StructureSequence = result.Sequence
		}
		return nil
	})
	return result, err
}

// Project-backed blocks resolve through Project permissions and cannot be
// copied into Company knowledge as captured source content. Wiki links and
// Artifact citations carry only references and resolve per reader.
// ValidateStandaloneBlocks excludes references whose authority requires a Project.
func ValidateStandaloneBlocks(root BlockNode) error {
	if slices.Contains(
		[]string{
			"artifactReference",
			"taskReference",
			"taskCollection",
			"outputCollection",
			"workflowReference",
			"fileBlock",
			"audioBlock",
			"videoBlock",
		},
		root.Type,
	) {
		return ErrPageInvalidInput
	}
	for _, child := range root.Content {
		if err := ValidateStandaloneBlocks(child); err != nil {
			return err
		}
	}
	return nil
}

func (s *DefaultWikiService) ReserveBlockIDs(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
	count int32,
) ([]uuid.UUID, error) {
	if count < 1 || count > 256 {
		return nil, ErrPageInvalidInput
	}
	var ids []uuid.UUID
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if err := lockEditableWiki(ctx, q, p, id); err != nil {
			return err
		}
		var err error
		ids, err = q.ReserveWikiBlockIDs(
			ctx,
			generated.ReserveWikiBlockIDsParams{CompanyID: p.CompanyID, PageID: id, Count: count},
		)
		return err
	})
	return ids, err
}

func (s *DefaultWikiService) UpdateDraftMetadata(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
	input DraftMetadataInput,
) (Document, error) {
	title, _, summary, err := normalizePageInput(input.Title, "", input.Summary)
	if err != nil {
		return Document{}, err
	}
	var result Document
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := lockFilingWiki(ctx, q, p, id); err != nil {
			return err
		}
		draft, err := BlockDraftInTx(ctx, q, p.CompanyID, id)
		if err != nil {
			return err
		}
		if draft.HeadSeq != input.ExpectedSequence {
			return ErrPageConflict
		}
		if err = checkCollection(ctx, q, p.CompanyID, input.CollectionID); err != nil {
			return err
		}
		if draft.Title == title && draft.Summary == summary &&
			draft.CollectionID == nullableUUID(input.CollectionID) {
			result = s.documentResponse(draft)
			result.CanEdit = result.CanEdit && s.authoring.AllowsSchema(int(draft.SchemaVersion))
			return nil
		}
		draft, err = q.UpdateWikiDraftMetadata(
			ctx,
			generated.UpdateWikiDraftMetadataParams{
				CompanyID:    p.CompanyID,
				PageID:       id,
				Title:        title,
				Summary:      summary,
				CollectionID: nullableUUID(input.CollectionID),
				HeadSeq:      input.ExpectedSequence,
			},
		)
		if err != nil {
			return err
		}
		result = s.documentResponse(draft)
		result.CanEdit = result.CanEdit && s.authoring.AllowsSchema(int(draft.SchemaVersion))
		return nil
	})
	return result, err
}

func (s *DefaultWikiService) PublishPage(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
	input PublishPageInput,
) (PageResponse, error) {
	if input.ExpectedRevision < 0 || input.ExpectedSequence < 0 {
		return PageResponse{}, ErrPageInvalidInput
	}
	var response PageResponse
	created := false
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		var err error
		response, created, err = publishDraftInTx(ctx, q, p, id, input)
		return err
	})
	if err != nil {
		return PageResponse{}, fmt.Errorf("publish Wiki draft: %w", mapWriteError(err))
	}
	name := EventPageUpdated
	if created {
		name = EventPageCreated
	}
	s.publishPageEvent(ctx, p, name, response)
	return response, nil
}

// publishDraftInTx publishes the page's draft as its next revision and
// rebuilds the links its body names. created reports the first publication.
func publishDraftInTx(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	id uuid.UUID,
	input PublishPageInput,
) (response PageResponse, created bool, err error) {
	page, err := lockFilingWiki(ctx, q, p, id)
	if err != nil {
		return response, false, err
	}
	if page.Version != input.ExpectedRevision {
		return response, false, ErrPageConflict
	}
	draft, err := BlockDraftInTx(ctx, q, p.CompanyID, id)
	if err != nil {
		return response, false, err
	}
	if draft.HeadSeq != input.ExpectedSequence {
		return response, false, ErrPageConflict
	}
	// The draft's Markdown is validated here; the published body is rebuilt
	// from the redacted blocks below.
	title, _, summary, err := normalizePageInput(draft.Title, draft.Markdown, draft.Summary)
	if err != nil {
		return response, created, err
	}
	var root BlockNode
	if err = json.Unmarshal(draft.Content, &root); err != nil {
		return response, created, err
	}
	if err = ValidateInlinePublication(root); err != nil {
		return response, false, err
	}
	if err = ValidateStandaloneBlocks(root); err != nil {
		return response, created, err
	}
	redactBlockContent(&root)
	body := strings.TrimRight(BlocksMarkdown(root), "\n")
	publishedContent, err := json.Marshal(root)
	if err != nil {
		return response, created, err
	}
	if err = checkCollection(ctx, q, p.CompanyID, uuidPointer(draft.CollectionID)); err != nil {
		return response, created, err
	}
	var carried []artifactRef
	previous, err := q.GetWikiCurrentRevision(
		ctx,
		generated.GetWikiCurrentRevisionParams{CompanyID: p.CompanyID, PageID: id},
	)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		created = true
	case err != nil:
		return response, created, err
	default:
		carried = parseArtifactRefs(previous.Body)
	}
	if draft.RestoredRevisionID.Valid {
		restored, err := q.GetWikiRevisionForMember(
			ctx,
			generated.GetWikiRevisionForMemberParams{
				CompanyID:   p.CompanyID,
				PageID:      id,
				ActorUserID: p.UserID,
				RevisionID:  draft.RestoredRevisionID.UUID,
			},
		)
		if err != nil {
			return response, created, err
		}
		carried = append(carried, parseArtifactRefs(restored.Body)...)
	}
	if err = replaceArtifactRefs(ctx, q, p, id, body, carried); err != nil {
		return response, created, err
	}
	number, err := q.AllocateWikiRevisionNumber(
		ctx,
		generated.AllocateWikiRevisionNumberParams{CompanyID: p.CompanyID, ID: id},
	)
	if err != nil {
		return response, created, err
	}
	revision, err := q.CreateWikiRevision(
		ctx,
		generated.CreateWikiRevisionParams{
			CompanyID:    p.CompanyID,
			PageID:       id,
			Number:       number,
			Title:        title,
			Body:         body,
			Summary:      summary,
			CollectionID: draft.CollectionID,
			AuthorUserID: p.UserID,
		},
	)
	if err != nil {
		return response, created, err
	}
	if err = q.SetWikiRevisionBlocks(
		ctx,
		generated.SetWikiRevisionBlocksParams{
			CompanyID:    p.CompanyID,
			ID:           revision.ID,
			BlockContent: publishedContent,
			Body:         body,
		},
	); err != nil {
		return response, created, err
	}
	if err = q.SetWikiRevisionPublished(
		ctx,
		generated.SetWikiRevisionPublishedParams{CompanyID: p.CompanyID, PageID: id, ID: revision.ID},
	); err != nil {
		return response, created, err
	}
	if _, err = q.SetWikiPageCurrentRevision(
		ctx,
		generated.SetWikiPageCurrentRevisionParams{
			CompanyID:       p.CompanyID,
			PageID:          id,
			ExpectedVersion: input.ExpectedRevision,
			Title:           title,
			Summary:         summary,
			CollectionID:    draft.CollectionID,
			RevisionID:      revision.ID,
		},
	); err != nil {
		return response, created, err
	}
	if err = q.RecordCanvasCheckpoint(
		ctx,
		generated.RecordCanvasCheckpointParams{
			CompanyID:  p.CompanyID,
			PageID:     id,
			RevisionID: revision.ID,
			ThroughSeq: draft.HeadSeq,
		},
	); err != nil {
		return response, created, err
	}
	if err = q.ClearWikiDraftRestore(
		ctx,
		generated.ClearWikiDraftRestoreParams{CompanyID: p.CompanyID, PageID: id},
	); err != nil {
		return response, created, err
	}
	if err = replaceLinks(ctx, q, p.CompanyID, id, body); err != nil {
		return response, created, err
	}
	response, err = readPage(ctx, q, p, id)
	return response, created, err
}

func (s *DefaultWikiService) RestoreRevision(
	ctx context.Context,
	p auth.UserPrincipal,
	id, revisionID uuid.UUID,
	input RestoreRevisionInput,
) (Document, error) {
	if id == uuid.Nil || revisionID == uuid.Nil || input.ExpectedRevision < 0 || input.ExpectedSequence < 0 {
		return Document{}, ErrPageInvalidInput
	}
	var result Document
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		page, err := lockFilingWiki(ctx, q, p, id)
		if err != nil {
			return err
		}
		if page.Version != input.ExpectedRevision {
			return ErrPageConflict
		}
		draft, err := BlockDraftInTx(ctx, q, p.CompanyID, id)
		if err != nil {
			return err
		}
		if draft.HeadSeq != input.ExpectedSequence {
			return ErrPageConflict
		}
		if err := CheckUIDocumentWrite(input.SchemaVersion, int(draft.SchemaVersion), BlockNode{}); err != nil {
			return err
		}
		revision, err := q.GetWikiRevisionForMember(
			ctx,
			generated.GetWikiRevisionForMemberParams{
				CompanyID:   p.CompanyID,
				PageID:      id,
				RevisionID:  revisionID,
				ActorUserID: p.UserID,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPageNotFound
		}
		if err != nil {
			return err
		}
		var root BlockNode
		if len(revision.BlockContent) > 2 {
			err = json.Unmarshal(revision.BlockContent, &root)
		} else {
			root = BlocksFromMarkdown(revision.Body)
			err = AllocateBlockTreeInTx(ctx, q, p.CompanyID, id, &root)
		}
		if err != nil {
			return err
		}
		if err := s.authoring.Check(int(draft.SchemaVersion), root); err != nil {
			return err
		}
		if err := CheckUIDocumentWrite(input.SchemaVersion, int(draft.SchemaVersion), root); err != nil {
			return err
		}
		if err = ValidateStandaloneBlocks(root); err != nil {
			return err
		}
		state, root, body, err := ReplaceDocumentContent(draft.State, root)
		if err != nil {
			return err
		}
		content, err := json.Marshal(root)
		if err != nil {
			return err
		}
		restored, err := q.RestoreWikiBlockDraft(
			ctx,
			generated.RestoreWikiBlockDraftParams{
				SchemaVersion:      int32(UIDocumentSchema(root)),
				CompanyID:          p.CompanyID,
				PageID:             id,
				State:              state,
				Content:            content,
				Markdown:           strings.TrimRight(body, "\n"),
				Title:              revision.Title,
				Summary:            revision.Summary,
				CollectionID:       revision.CollectionID,
				HeadSeq:            input.ExpectedSequence,
				RestoredRevisionID: uuid.NullUUID{UUID: revisionID, Valid: true},
			},
		)
		if err != nil {
			return err
		}
		if err = RecordDocumentChangeInTx(
			ctx,
			q,
			DocumentChangeInput{
				CompanyID:  p.CompanyID,
				PageID:     id,
				ActorType:  "user",
				ActorID:    p.UserID,
				ChangeType: "restore",
				Sequence:   restored.HeadSeq,
				Before:     draft.State,
				State:      state,
			},
		); err != nil {
			return err
		}
		result = s.documentResponse(restored)
		return nil
	})
	return result, err
}

// Published native content and its Markdown projection carry the same sanitized
// text. Redacting only Markdown would leave secrets visible in the block view.
func redactBlockContent(root *BlockNode) {
	root.Text = redact.Credentials(root.Text)
	for key, value := range root.Attrs {
		if text, ok := value.(string); ok {
			root.Attrs[key] = redact.Credentials(text)
		}
	}
	for i := range root.Marks {
		for key, value := range root.Marks[i].Attrs {
			if text, ok := value.(string); ok {
				root.Marks[i].Attrs[key] = redact.Credentials(text)
			}
		}
	}
	for i := range root.Content {
		redactBlockContent(&root.Content[i])
	}
}
