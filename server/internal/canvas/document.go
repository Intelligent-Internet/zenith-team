package canvas

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type DocumentReadInput struct {
	VersionID   uuid.UUID
	Since       int64
	Projection  bool
	StateVector []byte
	BlockID     string
	Preview     bool
}

func (s *DefaultPageService) ReadPageDocument(
	ctx context.Context, p auth.UserPrincipal, page uuid.UUID, input DocumentReadInput,
) (Document, error) {
	return s.readDocument(ctx, p, documentTarget{PageID: page}, input)
}

func (s *DefaultPageService) SyncPageDocument(
	ctx context.Context, p auth.UserPrincipal, page uuid.UUID, input DocumentSyncInput,
) (Document, error) {
	return s.syncDocument(ctx, p, documentTarget{PageID: page}, input)
}

func (s *DefaultPageService) ReservePageBlockIDs(
	ctx context.Context, p auth.UserPrincipal, page uuid.UUID, count int32,
) ([]uuid.UUID, error) {
	return s.reserveDocumentBlockIDs(ctx, p, documentTarget{PageID: page}, count)
}

// Document returns the live structured source, not the immutable review version.
func (s *DefaultPageService) Document(
	ctx context.Context,
	p auth.UserPrincipal,
	project, version uuid.UUID,
	since int64,
) (Document, error) {
	return s.ReadDocument(ctx, p, project, DocumentReadInput{VersionID: version, Since: since})
}

func (s *DefaultPageService) ReadDocument(
	ctx context.Context, p auth.UserPrincipal, project uuid.UUID, input DocumentReadInput,
) (Document, error) {
	return s.readDocument(ctx, p, documentTarget{ProjectID: project, VersionID: input.VersionID}, input)
}

func (s *DefaultPageService) readDocument(
	ctx context.Context,
	p auth.UserPrincipal,
	target documentTarget,
	input DocumentReadInput,
) (Document, error) {
	var result Document
	var err error
	for range 4 {
		result = Document{}
		err = s.store.WithinTx(
			ctx,
			pgx.TxOptions{IsoLevel: pgx.RepeatableRead},
			func(q *generated.Queries) error {
				page, canEdit, err := target.resolve(ctx, q, p, false)
				if err != nil {
					return err
				}
				// Canvas references read Company Wiki publication. Editing the Wiki
				// draft always uses the Wiki document endpoints, from any entry point.
				if page.ArtifactType == "wiki" {
					revision, err := q.GetWikiCurrentRevision(
						ctx,
						generated.GetWikiCurrentRevisionParams{
							CompanyID: p.CompanyID,
							PageID:    page.ID,
						},
					)
					if err != nil {
						return lookup(err)
					}
					result = Document{
						PageID:            page.ID,
						Sequence:          revision.Number,
						StructureSequence: revision.Number,
						SchemaVersion:     1,
					}
					var published wiki.BlockNode
					if len(revision.BlockContent) > 2 {
						if err = json.Unmarshal(revision.BlockContent, &published); err != nil {
							return err
						}
					}
					result.SchemaVersion = wiki.UIDocumentSchema(published)
					if input.Since == result.Sequence {
						return nil
					}
					result.Content = revision.BlockContent
					if len(result.Content) == 0 || string(result.Content) == "null" ||
						string(result.Content) == "{}" {
						result.Content, err = json.Marshal(
							wiki.BlocksFromMarkdown(revision.Body),
						)
						if err != nil {
							return err
						}
					}
					result.Markdown = revision.Body
					result.Content, err = documentProjection(
						result.Content,
						input.BlockID,
						input.Preview,
					)
					if input.Preview {
						result.Markdown = ""
					}
					return err
				}
				head, err := q.GetWikiBlockDraftHead(ctx, generated.GetWikiBlockDraftHeadParams{
					CompanyID: p.CompanyID, PageID: page.ID,
				})
				if err == nil && head.SchemaVersion >= 1 {
					result = Document{
						PageID:            page.ID,
						Sequence:          head.HeadSeq,
						StructureSequence: head.StructureSeq,
						SchemaVersion:     int(head.SchemaVersion),
						CanEdit: canEdit &&
							s.authoring.AllowsSchema(int(head.SchemaVersion)),
					}
					if input.Since == head.HeadSeq {
						return nil
					}
					if input.Projection {
						projection, e := q.GetWikiBlockProjection(
							ctx,
							generated.GetWikiBlockProjectionParams{
								CompanyID: p.CompanyID,
								PageID:    page.ID,
								HeadSeq:   head.HeadSeq,
							},
						)
						result.Content, result.Markdown = projection.Content, projection.Markdown
						if e != nil {
							return e
						}
						result.Content, e = documentProjection(
							result.Content,
							input.BlockID,
							input.Preview,
						)
						if input.Preview {
							result.Markdown = ""
						}
						return e
					}
				} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
				draft, err := q.GetCanvasDraft(
					ctx,
					generated.GetCanvasDraftParams{
						CompanyID: p.CompanyID,
						PageID:    page.ID,
					},
				)
				if err != nil {
					return err
				}
				result = Document{
					PageID:            draft.PageID,
					Sequence:          draft.HeadSeq,
					StructureSequence: draft.StructureSeq,
					SchemaVersion:     int(draft.SchemaVersion),
					CanEdit: canEdit &&
						s.authoring.AllowsSchema(int(draft.SchemaVersion)),
				}
				if input.Since != draft.HeadSeq {
					if !input.Projection {
						result.State, result.StateVector, err = wiki.BlockStateDelta(
							draft.State,
							input.StateVector,
						)
						if err != nil {
							return err
						}
					}
					result.Markdown = draft.Markdown
					result.Content = draft.Content
					if input.Projection {
						result.Content, err = documentProjection(
							result.Content,
							input.BlockID,
							input.Preview,
						)
						if err != nil {
							return err
						}
						if input.Preview {
							result.Markdown = ""
						}
					}
				}
				return nil
			},
		)
		err = mapWrite(err)
		if !errors.Is(err, ErrConflict) {
			result.IntelligentUIAuthoringEnabled = !s.authoring.Disabled
			return result, err
		}
		// Retry a serialization failure against a fresh snapshot.
	}
	return Document{}, err
}

// documentProjection selects addressed content without loading the editing CRDT.
func documentProjection(content []byte, blockID string, preview bool) (json.RawMessage, error) {
	if blockID == "" && !preview {
		return content, nil
	}
	var root wiki.BlockNode
	if err := json.Unmarshal(content, &root); err != nil {
		return nil, err
	}
	var find func(wiki.BlockNode) []wiki.BlockNode
	find = func(node wiki.BlockNode) []wiki.BlockNode {
		for i, child := range node.Content {
			if child.Attrs["blockId"] == blockID {
				if child.Type == "heading" {
					end := i + 1
					level, _ := child.Attrs["level"].(float64)
					for end < len(node.Content) {
						next := node.Content[end]
						nextLevel, _ := next.Attrs["level"].(float64)
						if next.Type == "heading" && nextLevel <= level {
							break
						}
						end++
					}
					return node.Content[i:end]
				}
				return []wiki.BlockNode{child}
			}
			if result := find(child); result != nil {
				return result
			}
		}
		return nil
	}
	if blockID != "" {
		root.Content = find(root)
	}
	if preview && blockID == "" && len(root.Content) > 8 {
		root.Content = root.Content[:8]
	}
	return json.Marshal(root)
}

func (s *DefaultPageService) SyncDocument(
	ctx context.Context,
	p auth.UserPrincipal,
	project, version uuid.UUID,
	input DocumentSyncInput,
) (Document, error) {
	return s.syncDocument(ctx, p, documentTarget{ProjectID: project, VersionID: version}, input)
}

func (s *DefaultPageService) syncDocument(
	ctx context.Context,
	p auth.UserPrincipal,
	target documentTarget,
	input DocumentSyncInput,
) (Document, error) {
	if len(input.Update) == 0 || len(input.Update) > 8<<20 {
		return Document{}, ErrLimit
	}
	for range 4 {
		saved, err := s.readDocument(ctx, p, target, DocumentReadInput{Since: -1})
		if err != nil {
			return Document{}, err
		}
		if err := s.authoring.Check(saved.SchemaVersion, wiki.BlockNode{}); err != nil {
			return Document{}, err
		}
		if !saved.CanEdit {
			return Document{}, ErrForbidden
		}
		if err := wiki.CheckUIDocumentWrite(
			input.SchemaVersion,
			saved.SchemaVersion,
			wiki.BlockNode{},
		); err != nil {
			return Document{}, err
		}
		state, root, markdown, structural, err := mergeBlockEdit(saved, input.Update, input.BaseSequence)
		if err != nil {
			return Document{}, mapWrite(err)
		}
		if err := s.authoring.Check(saved.SchemaVersion, root); err != nil {
			return Document{}, err
		}
		if err := wiki.CheckUIDocumentWrite(input.SchemaVersion, saved.SchemaVersion, root); err != nil {
			return Document{}, err
		}
		if bytes.Equal(state, saved.State) {
			return saved, nil
		}
		content, err := json.Marshal(root)
		if err != nil {
			return Document{}, err
		}
		changed := false
		var pending events.Event
		err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
			page, _, err := target.resolve(ctx, q, p, true)
			if err != nil {
				return err
			}
			if _, err = q.LockCanvasWikiPage(
				ctx,
				generated.LockCanvasWikiPageParams{CompanyID: p.CompanyID, ID: page.ID},
			); err != nil {
				return lookup(err)
			}
			ids := wiki.BlockIDs(root)
			count, err := q.ValidateWikiBlockIDs(
				ctx,
				generated.ValidateWikiBlockIDsParams{
					CompanyID: p.CompanyID,
					PageID:    saved.PageID,
					Ids:       ids,
				},
			)
			if err != nil {
				return err
			}
			if count != int64(len(ids)) {
				return ErrInvalid
			}
			if err = validateDocumentReferences(ctx, q, p, page.ProjectID, root); err != nil {
				return err
			}
			rows, err := q.UpdateWikiBlockDraft(
				ctx,
				generated.UpdateWikiBlockDraftParams{
					SchemaVersion: int32(wiki.UIDocumentSchema(root)),
					CompanyID:     p.CompanyID,
					PageID:        saved.PageID,
					State:         state,
					Content:       content,
					Markdown:      markdown,
					HeadSeq:       saved.Sequence,
					Structural:    structural,
				},
			)
			changed = rows == 1
			if changed {
				if err = wiki.RecordDocumentChangeInTx(
					ctx,
					q,
					wiki.DocumentChangeInput{
						CompanyID:  p.CompanyID,
						PageID:     page.ID,
						ActorType:  "user",
						ActorID:    p.UserID,
						ChangeType: "edit",
						Sequence:   saved.Sequence + 1,
						Before:     saved.State,
						State:      state,
					},
				); err != nil {
					return err
				}
				if err = reconcilePageComments(ctx, q, pageCommentEdit{
					companyID: p.CompanyID, pageID: saved.PageID, before: saved.State, after: state,
				}); err != nil {
					return err
				}
				if err = forgetHumanChanges(ctx, q, p, saved.PageID, root); err != nil {
					return err
				}
				event, eventErr := target.changed(ctx, q, p, saved.Sequence+1, saved.PageID)
				if eventErr != nil {
					return eventErr
				}
				if event != nil {
					pending = *event
				}
			}
			return err
		})
		if err != nil {
			return Document{}, mapWrite(err)
		}
		if changed {
			if pending.Name != "" {
				s.bus.Publish(ctx, Topic, pending)
			}
			structureSequence := saved.StructureSequence
			if structural {
				structureSequence = saved.Sequence + 1
			}
			return Document{
				IntelligentUIAuthoringEnabled: !s.authoring.Disabled,
				PageID:                        saved.PageID,
				Sequence:                      saved.Sequence + 1,
				StructureSequence:             structureSequence,
				SchemaVersion:                 max(saved.SchemaVersion, wiki.UIDocumentSchema(root)),
				State:                         state,
				Content:                       content,
				Markdown:                      markdown,
				CanEdit:                       true,
			}, nil
		}
	}
	return Document{}, ErrConflict
}

func mergeBlockEdit(saved Document, update []byte, baseSequence int64) ([]byte, wiki.BlockNode, string, bool, error) {
	state, root, markdown, structural, err := wiki.MergeDraftChanges(wiki.Document{
		State:             saved.State,
		Content:           saved.Content,
		Sequence:          saved.Sequence,
		StructureSequence: saved.StructureSequence,
	}, update, baseSequence)
	return state, root, markdown, structural, mapWrite(err)
}

// ReserveBlockIDs allocates a bounded pool for local editing, splits and paste.
func (s *DefaultPageService) ReserveBlockIDs(
	ctx context.Context,
	p auth.UserPrincipal,
	project, version uuid.UUID,
	count int32,
) ([]uuid.UUID, error) {
	return s.reserveDocumentBlockIDs(ctx, p, documentTarget{ProjectID: project, VersionID: version}, count)
}

func (s *DefaultPageService) reserveDocumentBlockIDs(
	ctx context.Context,
	p auth.UserPrincipal,
	target documentTarget,
	count int32,
) ([]uuid.UUID, error) {
	if count < 1 || count > 256 {
		return nil, ErrInvalid
	}
	var ids []uuid.UUID
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		metadata, _, err := target.resolve(ctx, q, p, true)
		if err != nil {
			return err
		}
		page, err := q.LockCanvasWikiPage(
			ctx,
			generated.LockCanvasWikiPageParams{CompanyID: p.CompanyID, ID: metadata.ID},
		)
		if err != nil {
			return lookup(err)
		}
		if page.Status != "active" {
			return ErrNotFound
		}
		if _, err = q.LockWikiDraft(
			ctx,
			generated.LockWikiDraftParams{CompanyID: p.CompanyID, PageID: metadata.ID},
		); err != nil {
			return err
		}
		// The content validator limits live blocks. Unused reservations must not
		// consume a lifetime editing quota; historical block identities stay valid.
		ids, err = q.ReserveWikiBlockIDs(
			ctx,
			generated.ReserveWikiBlockIDsParams{CompanyID: p.CompanyID, PageID: metadata.ID, Count: count},
		)
		return err
	})
	return ids, mapWrite(err)
}

func validateBlockReferences(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	project uuid.UUID,
	root wiki.BlockNode,
) error {
	seen := make(map[uuid.UUID]generated.GetCanvasReferenceRow)
	var walk func(wiki.BlockNode) error
	walk = func(root wiki.BlockNode) error {
		if root.Type == "taskReference" {
			if raw, _ := root.Attrs["taskId"].(string); raw != "" {
				id, err := uuid.Parse(raw)
				if err != nil {
					return ErrInvalid
				}
				if _, err = q.GetCanvasTask(
					ctx,
					generated.GetCanvasTaskParams{
						CompanyID: p.CompanyID,
						ProjectID: project,
						ID:        id,
					},
				); err != nil {
					return lookup(err)
				}
			}
		}
		for _, id := range sourceVersionIDs(root) {
			if err := canVersion(ctx, q, p, project, id); err != nil {
				return err
			}
		}
		if raw, ok := root.Attrs["versionId"].(string); ok && raw != "" {
			id, err := uuid.Parse(raw)
			if err != nil {
				return ErrInvalid
			}
			if err = canReference(
				ctx,
				q,
				p,
				project,
				id,
				root.Type == "artifactReference" && root.Attrs["format"] == "live",
			); err != nil {
				return err
			}
			ref, found := seen[id]
			if !found {
				ref, err = q.GetCanvasReference(
					ctx,
					generated.GetCanvasReferenceParams{
						CompanyID: p.CompanyID,
						ProjectID: project,
						ID:        id,
					},
				)
				if err != nil {
					return err
				}
				seen[id] = ref
			}
			if root.Type == "artifactReference" && root.Attrs["format"] == "live" {
				source, err := q.GetCanvasVersion(
					ctx,
					generated.GetCanvasVersionParams{
						CompanyID: p.CompanyID,
						ProjectID: project,
						ID:        id,
					},
				)
				if err != nil {
					return lookup(err)
				}
				if !source.WikiPageID.Valid || source.AccessOutputID.Valid {
					return ErrInvalid
				}
			}
			if artifact, ok := root.Attrs["artifactId"].(string); ok && artifact != "" &&
				artifact != ref.ArtifactID.String() {
				return ErrInvalid
			}
		}
		for _, child := range root.Content {
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(root)
}

func (s *DefaultPageService) VersionBlocks(
	ctx context.Context,
	p auth.UserPrincipal,
	project, version uuid.UUID,
) (json.RawMessage, error) {
	var content json.RawMessage
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := authorize(ctx, q, p, project, false); err != nil {
			return err
		}
		if err := canVersion(ctx, q, p, project, version); err != nil {
			return err
		}
		value, err := q.GetCanvasBlockBody(
			ctx,
			generated.GetCanvasBlockBodyParams{CompanyID: p.CompanyID, ProjectID: project, ID: version},
		)
		content = value
		return lookup(err)
	})
	return content, err
}
