package canvas

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func freezeBlockReferences(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	project uuid.UUID,
	root *wiki.BlockNode,
) error {
	_, err := freezePageReferences(ctx, q, pageReferenceSnapshot{principal: p, project: project, root: root})
	return err
}

type pageReferenceSnapshot struct {
	principal auth.UserPrincipal
	project   uuid.UUID
	root      *wiki.BlockNode
	documents map[string]wiki.BlockNode
}

// A saved version retains the acknowledged content of each live Page it embeds.
// The collaborative draft keeps following the live Page; only its snapshot pins.
func freezePageReferences(
	ctx context.Context,
	q *generated.Queries,
	in pageReferenceSnapshot,
) (map[uuid.UUID]generated.ArtifactSnapshot, error) {
	p, project, root := in.principal, in.project, in.root
	pinned := map[uuid.UUID]generated.ArtifactSnapshot{}
	published := map[uuid.UUID]generated.ArtifactSnapshot{}
	snapshots := map[uuid.UUID]generated.ArtifactSnapshot{}
	active := map[uuid.UUID]bool{}

	var walk func(*wiki.BlockNode, int) error
	walk = func(node *wiki.BlockNode, depth int) error {
		if node.Type == "artifactReference" {
			if id, err := uuid.Parse(fmt.Sprint(node.Attrs["versionId"])); err == nil {
				if err = canReference(
					ctx,
					q,
					p,
					project,
					id,
					node.Attrs["format"] == "live",
				); err != nil {
					return err
				}
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
				if label, _ := node.Attrs["label"].(string); label == "" {
					title, err := q.GetOverviewArtifactTitle(
						ctx,
						generated.GetOverviewArtifactTitleParams{
							CompanyID: p.CompanyID,
							ProjectID: project,
							ID:        source.ArtifactID,
						},
					)
					if err != nil {
						return err
					}
					node.Attrs["label"] = title
				}
				if node.Attrs["format"] == "live" {
					if !source.WikiPageID.Valid || source.AccessOutputID.Valid {
						return ErrInvalid
					}
					page := source.WikiPageID.UUID
					if active[page] || depth >= 8 || len(snapshots) >= 256 {
						return ErrLimit
					}
					metadata, err := q.GetCanvasWikiPage(
						ctx,
						generated.GetCanvasWikiPageParams{CompanyID: p.CompanyID, ID: page},
					)
					if err != nil {
						return lookup(err)
					}
					if metadata.ArtifactType == "wiki" {
						// Company knowledge embeds its published revision, even when
						// the capturing member can also edit the unpublished Wiki draft.
						current, err := q.GetWikiCurrentRevision(
							ctx,
							generated.GetWikiCurrentRevisionParams{
								CompanyID: p.CompanyID,
								PageID:    page,
							},
						)
						if err != nil {
							return lookup(err)
						}
						if current.ID == source.WikiRevisionID.UUID {
							snapshots[id] = generated.ArtifactSnapshot{
								ID:             source.ID,
								WikiRevisionID: source.WikiRevisionID,
							}
							node.Attrs["format"] = "pinned"
							return nil
						}
						version, ok := published[source.ArtifactID]
						if !ok {
							version, err = q.GetArtifactSnapshot(
								ctx,
								generated.GetArtifactSnapshotParams{
									CompanyID: p.CompanyID,
									ID:        current.ID,
								},
							)
							if err != nil {
								return err
							}
							published[source.ArtifactID] = version
						}
						snapshots[id] = version
						node.Attrs["versionId"] = version.ID.String()
						node.Attrs["format"] = "pinned"
						return nil
					}
					version, ok := pinned[page]
					if !ok {
						if _, err = q.LockCanvasArtifact(
							ctx,
							generated.LockCanvasArtifactParams{
								CompanyID: p.CompanyID,
								ProjectID: project,
								ID:        source.ArtifactID,
							},
						); err != nil {
							return lookup(err)
						}
						draft, err := wiki.BlockDraftInTx(ctx, q, p.CompanyID, page)
						if err != nil {
							return err
						}
						var content wiki.BlockNode
						raw := draft.Content
						if captured, ok := in.documents[presentationDocumentKey(id.String(), true)]; ok {
							// Reuse the reviewed projection, including captured work and
							// workflow state, without mutating the Presentation's copy.
							raw, err = json.Marshal(captured)
							if err != nil {
								return err
							}
						}
						if err = json.Unmarshal(raw, &content); err != nil {
							return err
						}
						active[page] = true
						if err = walk(&content, depth+1); err != nil {
							return err
						}
						delete(active, page)
						raw, err = json.Marshal(content)
						if err != nil {
							return err
						}
						revision, err := wiki.CheckpointCanvasPageInTx(
							ctx,
							q,
							wiki.CanvasCheckpointInput{
								Principal: p,
								PageID:    page,
								Body:      wiki.BlocksMarkdown(content),
								Content:   raw,
								Sequence:  draft.HeadSeq,
							},
						)
						if err != nil {
							return err
						}
						saved, err := q.GetArtifactSnapshot(
							ctx,
							generated.GetArtifactSnapshotParams{
								CompanyID: p.CompanyID,
								ID:        revision,
							},
						)
						if err != nil {
							return err
						}
						version = saved
						pinned[page] = version
					}
					snapshots[id] = version
					node.Attrs["versionId"] = version.ID.String()
					node.Attrs["format"] = "pinned"
				}
			}
		}
		for i := range node.Content {
			if err := walk(&node.Content[i], depth); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root, 0); err != nil {
		return nil, err
	}
	return snapshots, nil
}

func frozenPageContent(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	project uuid.UUID,
	root wiki.BlockNode,
) (wiki.BlockNode, error) {
	raw, err := json.Marshal(root)
	if err != nil {
		return wiki.BlockNode{}, err
	}
	var frozen wiki.BlockNode
	if err = json.Unmarshal(raw, &frozen); err != nil {
		return frozen, err
	}
	_, err = freezePageReferences(ctx, q, pageReferenceSnapshot{principal: p, project: project, root: &frozen})
	return frozen, err
}
