package canvas

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

type PageSource struct {
	VersionID  uuid.UUID       `json:"versionId"`
	ArtifactID uuid.UUID       `json:"artifactId"`
	Title      string          `json:"title"`
	SourceType string          `json:"sourceType"`
	Content    json.RawMessage `json:"content,omitempty"`
	Markdown   string          `json:"markdown,omitempty"`
}

func (s *DefaultPresentationService) ReadPageSource(
	ctx context.Context,
	p auth.UserPrincipal,
	run, version uuid.UUID,
	block string,
) (PageSource, error) {
	var result PageSource
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		project, _, err := admitPageRun(ctx, q, p, run, false)
		if err != nil {
			return err
		}
		if err = canVersion(ctx, q, p, project, version); err != nil {
			return err
		}
		source, err := q.GetCanvasVersion(
			ctx,
			generated.GetCanvasVersionParams{CompanyID: p.CompanyID, ProjectID: project, ID: version},
		)
		if err != nil {
			return lookup(err)
		}
		title, err := q.GetOverviewArtifactTitle(
			ctx,
			generated.GetOverviewArtifactTitleParams{
				CompanyID: p.CompanyID,
				ProjectID: project,
				ID:        source.ArtifactID,
			},
		)
		if err != nil {
			return lookup(err)
		}
		result = PageSource{
			VersionID:  version,
			ArtifactID: source.ArtifactID,
			Title:      title,
			SourceType: source.SourceType,
		}
		if !source.WikiRevisionID.Valid {
			return nil
		}
		page, err := q.GetCanvasWikiPage(
			ctx,
			generated.GetCanvasWikiPageParams{CompanyID: p.CompanyID, ID: source.WikiPageID.UUID},
		)
		if err != nil {
			return lookup(err)
		}
		if page.ArtifactType == "wiki" {
			return ErrForbidden
		}
		revision, err := q.GetCanvasWikiRevision(
			ctx,
			generated.GetCanvasWikiRevisionParams{
				CompanyID: p.CompanyID,
				PageID:    source.WikiPageID.UUID,
				ID:        source.WikiRevisionID.UUID,
			},
		)
		if err != nil {
			return lookup(err)
		}
		content := revision.BlockContent
		if len(content) == 0 || string(content) == "null" || string(content) == "{}" {
			content, err = json.Marshal(wiki.BlocksFromMarkdown(revision.Body))
			if err != nil {
				return err
			}
		}
		result.Content, err = documentProjection(content, block, true)
		if err != nil {
			return err
		}
		if len(result.Content) > 64<<10 {
			return ErrLimit
		}
		var root wiki.BlockNode
		if err = json.Unmarshal(result.Content, &root); err != nil {
			return err
		}
		if block != "" && len(root.Content) == 0 {
			return ErrNotFound
		}
		if err = redactUnavailableReferences(ctx, q, p, project, &root); err != nil {
			return err
		}
		result.Content, err = json.Marshal(root)
		result.Markdown = wiki.BlocksMarkdown(root)
		return err
	})
	return result, mapWrite(err)
}
