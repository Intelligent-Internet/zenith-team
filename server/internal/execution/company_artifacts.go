package execution

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	dbgen "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// CompanyArtifactListOptions pages the Company-wide Artifact list; Search
// matches the title and is empty for no filter.
type CompanyArtifactListOptions struct {
	ArtifactListOptions
	Search    string
	Ownership string
	Source    string
}

type ArtifactPage struct {
	Items []ArtifactRunResult `json:"items"`
	Next  *Cursor             `json:"-"`
}

// ListArtifacts lists every Artifact the actor can open in the Company, newest
// first. It is the picker behind Wiki citations; it reveals nothing the actor
// could not already open one by one.
func (s *DefaultDeliverableService) ListArtifacts(
	ctx context.Context,
	principal auth.UserPrincipal,
	options CompanyArtifactListOptions,
) (ArtifactPage, error) {
	search := strings.TrimSpace(options.Search)
	if !validDeliverableService(s) || options.Limit < 1 || options.Limit > 50 ||
		!utf8.ValidString(search) || utf8.RuneCountInString(search) > 200 ||
		strings.ContainsRune(search, 0) {
		return ArtifactPage{}, ErrDeliverableInvalidInput
	}
	switch options.Ownership {
	case "", "owned", "shared":
	default:
		return ArtifactPage{}, ErrDeliverableInvalidInput
	}
	switch options.Source {
	case "", "channel", "direct", "task", "schedule":
	default:
		return ArtifactPage{}, ErrDeliverableInvalidInput
	}
	// The query takes a ready ILIKE pattern: `\`, `%` and `_` match literally.
	titlePattern := ""
	if search != "" {
		titlePattern = "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(search) + "%"
	}
	page := ArtifactPage{Items: []ArtifactRunResult{}}
	err := s.store.WithinTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(q *dbgen.Queries) error {
			params := dbgen.ListCompanyArtifactsParams{
				CompanyID: principal.CompanyID, ActorUserID: principal.UserID,
				Ownership: options.Ownership, SourceType: options.Source,
				TitlePattern: titlePattern, RowLimit: options.Limit + 1,
			}
			if options.Before != nil {
				params.BeforeCreatedAt = pgtype.Timestamptz{Time: options.Before.CreatedAt, Valid: true}
				params.BeforeID = options.Before.ID
			}
			found, err := q.ListCompanyArtifacts(ctx, params)
			if err != nil {
				return fmt.Errorf("list Company Artifacts: %w", err)
			}
			rows := make([]dbgen.ListConversationArtifactsRow, len(found))
			for i, row := range found {
				rows[i] = dbgen.ListConversationArtifactsRow(row)
			}
			rows, page.Next = pageArtifactRows(rows, params.RowLimit)
			page.Items, err = artifactRunResults(ctx, q, principal.CompanyID, principal.UserID, rows)
			if err != nil {
				return err
			}
			return enrichArtifactLibrary(ctx, q, principal, page.Items)
		},
	)
	if err != nil {
		return ArtifactPage{}, err
	}
	return page, nil
}

// ResolveArtifacts returns the named Artifacts the actor can open now. An id
// that is missing, deleted or not visible is simply absent, so a caller cannot
// tell those cases apart; a Wiki reader shows each of them as unavailable.
func (s *DefaultDeliverableService) ResolveArtifacts(
	ctx context.Context,
	principal auth.UserPrincipal,
	ids []uuid.UUID,
) ([]ArtifactRunResult, error) {
	if !validDeliverableService(s) || len(ids) == 0 || len(ids) > 50 {
		return nil, ErrDeliverableInvalidInput
	}
	var results []ArtifactRunResult
	err := s.store.WithinTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(q *dbgen.Queries) error {
			found, err := q.ListArtifactsByID(ctx, dbgen.ListArtifactsByIDParams{
				CompanyID: principal.CompanyID, ActorUserID: principal.UserID, OutputIds: ids,
			})
			if err != nil {
				return fmt.Errorf("resolve Artifacts: %w", err)
			}
			rows := make([]dbgen.ListConversationArtifactsRow, len(found))
			for i, row := range found {
				rows[i] = dbgen.ListConversationArtifactsRow(row)
			}
			results, err = artifactRunResults(ctx, q, principal.CompanyID, principal.UserID, rows)
			if err != nil {
				return err
			}
			return enrichArtifactLibrary(ctx, q, principal, results)
		},
	)
	if err != nil {
		return nil, err
	}
	return results, nil
}

// Library metadata belongs to the picker and its selection revalidation.
// Source titles are returned only when the caller can read the source itself.
type ArtifactLibraryMetadata struct {
	OwnerName string                `json:"ownerName"`
	Owned     bool                  `json:"owned"`
	CanShare  bool                  `json:"canShare"`
	Source    ArtifactLibrarySource `json:"source"`
}
type ArtifactLibrarySource struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

func enrichArtifactLibrary(
	ctx context.Context,
	q *dbgen.Queries,
	p auth.UserPrincipal,
	items []ArtifactRunResult,
) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(items))
	for i, item := range items {
		ids[i] = item.Artifact.ID
	}
	rows, err := q.ListArtifactLibraryMetadata(
		ctx,
		dbgen.ListArtifactLibraryMetadataParams{CompanyID: p.CompanyID, ActorUserID: p.UserID, OutputIds: ids},
	)
	if err != nil {
		return fmt.Errorf("read Artifact library metadata: %w", err)
	}
	metadata := make(map[uuid.UUID]*ArtifactLibraryMetadata, len(rows))
	for _, row := range rows {
		metadata[row.ID] = &ArtifactLibraryMetadata{
			OwnerName: row.OwnerName,
			Owned:     row.Owned,
			CanShare:  row.CanShare,
			Source: ArtifactLibrarySource{
				Type:   row.SourceType,
				Title:  row.SourceTitle,
				Detail: row.SourceDetail,
			},
		}
	}
	for i := range items {
		items[i].Library = metadata[items[i].Artifact.ID]
	}
	return nil
}
