package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/wiki"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

type SearchMatch struct {
	ArtifactID uuid.UUID `json:"artifactId"`
	VersionID  uuid.UUID `json:"versionId"`
	BlockID    string    `json:"blockId"`
	Title      string    `json:"title"`
	Excerpt    string    `json:"excerpt"`
}

func (s *DefaultCanvasService) Search(
	ctx context.Context,
	p auth.UserPrincipal,
	project uuid.UUID,
	term string,
) ([]SearchMatch, error) {
	term = strings.TrimSpace(term)
	if len(term) < 2 || utf8.RuneCountInString(term) > 120 {
		return nil, ErrInvalid
	}
	result := []SearchMatch{}
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := authorize(ctx, q, p, project, false); err != nil {
			return err
		}
		rows, err := q.SearchCanvasDocuments(
			ctx,
			generated.SearchCanvasDocumentsParams{CompanyID: p.CompanyID, ProjectID: project, Term: term},
		)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if err = canVersion(ctx, q, p, project, row.VersionID); err != nil {
				if errors.Is(err, ErrNotFound) {
					continue
				}
				return err
			}
			var root wiki.BlockNode
			if err = json.Unmarshal(row.Content, &root); err != nil {
				return err
			}
			id, excerpt := searchBlocks(root, strings.ToLower(term))
			result = append(
				result,
				SearchMatch{
					ArtifactID: row.ArtifactID,
					VersionID:  row.VersionID,
					BlockID:    id,
					Title:      row.Title,
					Excerpt:    excerpt,
				},
			)
		}
		return nil
	})
	return result, err
}

func searchBlocks(node wiki.BlockNode, term string) (string, string) {
	if node.Type == "paragraph" || node.Type == "heading" || node.Type == "codeBlock" || node.Type == "mermaid" {
		var text strings.Builder
		for _, child := range node.Content {
			text.WriteString(child.Text)
		}
		value := text.String()
		if strings.Contains(strings.ToLower(value), term) {
			id, _ := node.Attrs["blockId"].(string)
			chars := []rune(value)
			if len(chars) > 240 {
				value = string(chars[:240]) + "…"
			}
			return id, value
		}
	}
	for _, child := range node.Content {
		if id, value := searchBlocks(child, term); id != "" {
			return id, value
		}
	}
	return "", ""
}
