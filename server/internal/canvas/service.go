// Package canvas owns Project arrangement, collaborative Pages and immutable
// presentations, plus collaborative review of non-Task Outputs. Keeping these
// transactions separate from Company identity and Run execution is the forcing
// function for the package.
package canvas

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"boardly.dev/boardly/server/internal/attachment"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/wiki"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

type CanvasService interface { //nolint:revive // AGENTS.md requires <Domain>Service; bare Service is forbidden.
	OpenOutputDocument(context.Context, auth.UserPrincipal, OutputDocumentRef) (uuid.UUID, error)
	OutputReview(context.Context, auth.UserPrincipal, uuid.UUID) (OutputReview, error)
	MutateOutputReview(context.Context, auth.UserPrincipal, uuid.UUID, Command) (Result, error)
	Library(context.Context, auth.UserPrincipal, uuid.UUID) (Library, error)
	Search(context.Context, auth.UserPrincipal, uuid.UUID, string) ([]SearchMatch, error)
	Viewer(context.Context, auth.UserPrincipal, uuid.UUID) (string, error)
	Snapshot(context.Context, auth.UserPrincipal, uuid.UUID, int64) (Snapshot, error)
	Mutate(context.Context, auth.UserPrincipal, uuid.UUID, Command) (Result, error)
	Threads(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID) ([]Thread, error)
	Timeline(context.Context, auth.UserPrincipal, uuid.UUID, string) (TimelinePage, error)
	History(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID) (History, error)
	VersionBody(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID) (string, error)
}

var _ CanvasService = (*DefaultCanvasService)(nil)

type DefaultCanvasService struct {
	store               *db.Store
	bus                 *events.Bus
	attachments         attachment.AttachmentService
	invokeDocumentAgent DocumentAgentInvoker
}

func NewCanvasService(
	store *db.Store,
	bus *events.Bus,
	attachments attachment.AttachmentService,
	options ...CanvasOption,
) *DefaultCanvasService {
	s := &DefaultCanvasService{store: store, bus: bus, attachments: attachments}
	for _, option := range options {
		option(s)
	}
	return s
}

func authorize(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	project uuid.UUID,
	write bool,
) (auth.ProjectAccess, error) {
	// Shared lifecycle locks fence revocation without serializing unrelated boards.
	if _, err := q.LockCompanyForMembershipRead(ctx, p.CompanyID); err != nil {
		return auth.ProjectAccess{}, lookup(err)
	}
	member, err := q.GetCompanyMemberForShare(
		ctx,
		generated.GetCompanyMemberForShareParams{CompanyID: p.CompanyID, UserID: p.UserID},
	)
	if err != nil {
		return auth.ProjectAccess{}, lookup(err)
	}
	if member.Status != "active" {
		return auth.ProjectAccess{}, ErrNotFound
	}
	row, err := q.GetProjectForActiveMember(
		ctx,
		generated.GetProjectForActiveMemberParams{
			ManagementRoles: []string{},
			CompanyID:       p.CompanyID,
			ProjectID:       project,
			ActorUserID:     p.UserID,
		},
	)
	if err != nil {
		return auth.ProjectAccess{}, lookup(err)
	}
	access := auth.ProjectAccessOf(
		auth.ProjectAccessFacts{
			ProjectID:          project,
			CompanyID:          p.CompanyID,
			Visibility:         row.Project.Visibility,
			Via:                row.Via,
			Management:         row.Management,
			Revision:           row.Project.AccessVersion,
			GrantedPermissions: row.GrantedPermissions,
			GroupPermissions:   row.GroupPermissions,
		},
	)
	if !access.Can(auth.ProjectPermissionRead) {
		return auth.ProjectAccess{}, ErrNotFound
	}
	if write && !access.Can(auth.ProjectPermissionTasksWrite) {
		return auth.ProjectAccess{}, auth.ErrProjectPermissionRequired
	}
	if write && row.Project.ArchivedAt.Valid {
		return auth.ProjectAccess{}, ErrForbidden
	}
	if row.Project.ArchivedAt.Valid {
		return auth.ProjectAccess{}, nil
	}
	return access, nil
}

func lookup(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func mapWrite(err error) error {
	switch {
	case errors.Is(err, pgx.ErrNoRows), errors.Is(err, wiki.ErrPageNotFound):
		return ErrNotFound
	case errors.Is(err, wiki.ErrPageForbidden):
		return ErrForbidden
	case errors.Is(err, wiki.ErrPageConflict):
		return ErrConflict
	case errors.Is(err, wiki.ErrPageInvalidInput), errors.Is(err, wiki.ErrPageArchived):
		return ErrInvalid
	case errors.Is(err, wiki.ErrPageBodyTooLarge):
		return ErrLimit
	}

	var constraint *pgconn.PgError
	if errors.As(err, &constraint) {
		switch constraint.Code {
		case "23505", "40001", "40P01":
			return ErrConflict
		case "23503", "23514", "22003":
			return ErrInvalid
		}
	}
	return err
}

func nullable(id *uuid.UUID) uuid.NullUUID {
	if id == nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: *id, Valid: true}
}
func pointer(id uuid.UUID) *uuid.UUID { return &id }
func requireID(id *uuid.UUID) (uuid.UUID, error) {
	if id == nil || *id == uuid.Nil {
		return uuid.Nil, ErrInvalid
	}
	return *id, nil
}

func canVersion(ctx context.Context, q *generated.Queries, p auth.UserPrincipal, project, id uuid.UUID) error {
	return canRetainedVersion(ctx, q, p, project, id, map[uuid.UUID]bool{})
}

func canRetainedVersion(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	project, id uuid.UUID,
	seen map[uuid.UUID]bool,
) error {
	if seen[id] {
		return nil
	}
	if len(seen) >= 256 {
		return ErrLimit
	}
	seen[id] = true
	v, err := canVersionSource(ctx, q, p, project, id)
	if err != nil {
		return err
	}
	if v.AccessOutputID.Valid {
		if err = PresentationOutputAccessInTx(ctx, q, p, v.AccessOutputID.UUID); err != nil {
			return err
		}
	}
	if !v.WikiRevisionID.Valid {
		return nil
	}
	revision, err := q.GetCanvasWikiRevision(
		ctx,
		generated.GetCanvasWikiRevisionParams{
			CompanyID: p.CompanyID,
			PageID:    v.WikiPageID.UUID,
			ID:        v.WikiRevisionID.UUID,
		},
	)
	if err != nil {
		return lookup(err)
	}
	var root wiki.BlockNode
	if err = json.Unmarshal(revision.BlockContent, &root); err != nil {
		return err
	}
	var walk func(wiki.BlockNode) error
	walk = func(n wiki.BlockNode) error {
		ids := sourceVersionIDs(n)
		if n.Type == "artifactReference" {
			if source, err := uuid.Parse(fmt.Sprint(n.Attrs["versionId"])); err == nil {
				ids = append(ids, source)
			}
		}
		if n.Type == "outputCollection" {
			raw, err := json.Marshal(n.Attrs["capturedOutputs"])
			if err != nil {
				return err
			}
			var rows []Artifact
			if err = json.Unmarshal(raw, &rows); err != nil {
				return err
			}
			for _, row := range rows {
				ids = append(ids, row.LatestVersionID)
			}
		}
		for _, source := range ids {
			if err := canRetainedVersion(ctx, q, p, project, source, seen); err != nil {
				return err
			}
		}
		for _, child := range n.Content {
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(root)
}

// Native presentations flatten their retained evidence at capture. Checking the
// underlying source access here avoids recursively traversing earlier captures.
func canVersionSource(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	project, id uuid.UUID,
) (generated.CanvasVersionSource, error) {
	v, err := q.GetCanvasVersion(
		ctx,
		generated.GetCanvasVersionParams{CompanyID: p.CompanyID, ProjectID: project, ID: id},
	)
	if err != nil {
		return v, lookup(err)
	}
	if v.AccessOutputID.Valid {
		if _, err = q.LockCanvasOutput(
			ctx,
			generated.LockCanvasOutputParams{CompanyID: p.CompanyID, ID: v.AccessOutputID.UUID},
		); err != nil {
			return v, lookup(err)
		}
	}
	yes, err := q.CanvasVersionAccess(
		ctx,
		generated.CanvasVersionAccessParams{
			CompanyID:   p.CompanyID,
			ProjectID:   project,
			VersionID:   id,
			ActorUserID: p.UserID,
		},
	)
	if err != nil {
		return v, err
	}
	if !yes {
		return v, ErrNotFound
	}
	return v, nil
}

func decodeRows[T any](rows [][]byte) ([]T, error) {
	result := make([]T, 0, len(rows))
	for _, raw := range rows {
		var value T
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, fmt.Errorf("decode canvas projection: %w", err)
		}
		result = append(result, value)
	}
	return result, nil
}

func (s *DefaultCanvasService) Mutate(
	ctx context.Context,
	p auth.UserPrincipal,
	project uuid.UUID,
	input Command,
) (Result, error) {
	if err := normalizeCommand(&input); err != nil {
		return Result{}, err
	}
	if input.Type == "import_output_document" {
		// The source bytes are server-resolved; caller-provided text is never imported.
		input.Body = ""
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return Result{}, ErrInvalid
	}
	hash := sha256.Sum256(encoded)
	if input.Type == "import_output_document" {
		input.Body, err = s.outputDocumentBody(ctx, p, project, input)
		if err != nil {
			return Result{}, mapWrite(err)
		}
	}
	var result Result
	var pending *events.Event
	var agentEvents []events.Event
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		access, err := authorize(ctx, q, p, project, false)
		if err != nil {
			return err
		}
		permission := auth.ProjectPermissionTasksWrite
		if (input.Type == "add_artifact" || input.Type == "add_version") && input.AttachmentID != nil {
			permission = auth.ProjectPermissionFilesManage
		}
		if !access.Can(permission) {
			return auth.ErrProjectPermissionRequired
		}
		// Existing execution conversations precede canvas and thread locks, as
		// they do when a completed Run posts its reply.
		if input.ThreadID != nil && len(input.AgentIDs) > 0 {
			if _, err = q.LockDocumentAgentConversations(ctx, generated.LockDocumentAgentConversationsParams{CompanyID: p.CompanyID, DocumentThreadID: nullable(input.ThreadID), CreatedByUserID: nullable(&p.UserID), AgentIds: input.AgentIDs}); err != nil {
				return err
			}
		}
		board, err := q.EnsureProjectCanvas(
			ctx,
			generated.EnsureProjectCanvasParams{CompanyID: p.CompanyID, ProjectID: project},
		)
		if err != nil {
			return err
		}
		receipt, err := q.GetCanvasReceipt(
			ctx,
			generated.GetCanvasReceiptParams{
				ProjectID:   project,
				ActorUserID: p.UserID,
				RequestKey:  input.RequestKey,
			},
		)
		if err == nil {
			if string(receipt.RequestHash) != string(hash[:]) {
				return ErrConflict
			}
			return json.Unmarshal(receipt.Result, &result)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		changes := Changes{
			Items:            []uuid.UUID{},
			Artifacts:        []uuid.UUID{},
			Edges:            []uuid.UUID{},
			Removed:          []uuid.UUID{},
			ReviewVersionIDs: []uuid.UUID{},
		}
		result.ID, err = s.apply(
			ctx,
			q,
			mutation{
				Principal: p,
				ProjectID: project,
				CanvasID:  board.ID,
				Access:    access,
				Input:     input,
				Changes:   &changes,
			},
		)
		if err != nil {
			return err
		}
		if err = s.invokeCommentAgents(ctx, q, &agentEvents, commentAgentRequest{Principal: p, ThreadID: result.ID, Command: input}); err != nil {
			return err
		}
		result.Sequence, err = q.AdvanceCanvasHead(
			ctx,
			generated.AdvanceCanvasHeadParams{CompanyID: p.CompanyID, ProjectID: project},
		)
		if err != nil {
			return err
		}
		changeBytes, err := json.Marshal(changes)
		if err != nil {
			return err
		}
		resultBytes, err := json.Marshal(result)
		if err != nil {
			return err
		}
		if err = q.InsertCanvasReceipt(
			ctx,
			generated.InsertCanvasReceiptParams{
				CompanyID:   p.CompanyID,
				ProjectID:   project,
				Seq:         result.Sequence,
				ActorUserID: p.UserID,
				RequestKey:  input.RequestKey,
				RequestHash: hash[:],
				Changes:     changeBytes,
				Result:      resultBytes,
			},
		); err != nil {
			return err
		}
		pending, err = changeEvent(ctx, q, p, project, result.Sequence, uuid.Nil)
		return err
	})
	if err != nil {
		return Result{}, mapWrite(err)
	}
	if pending != nil {
		s.bus.Publish(ctx, Topic, *pending)
	}
	for _, group := range events.GroupByTopic(agentEvents) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	return result, nil
}

func (s *DefaultCanvasService) Viewer(ctx context.Context, p auth.UserPrincipal, project uuid.UUID) (string, error) {
	var name string
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := authorize(ctx, q, p, project, false); err != nil {
			return err
		}
		user, err := q.GetUserByID(ctx, p.UserID)
		if err != nil {
			return err
		}
		name = user.DisplayName
		return nil
	})
	return name, err
}

func decodeArtifacts(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	project uuid.UUID,
	rows [][]byte,
) ([]Artifact, error) {
	result, err := decodeRows[Artifact](rows)
	if err != nil {
		return nil, err
	}
	for i := range result {
		if err = redactVersionExcerpt(ctx, q, p, project, &result[i].LatestVersion); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func decodeVersions(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	project uuid.UUID,
	rows [][]byte,
) ([]Version, error) {
	result, err := decodeRows[Version](rows)
	if err != nil {
		return nil, err
	}
	for i := range result {
		if err = redactVersionExcerpt(ctx, q, p, project, &result[i]); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func redactVersionExcerpt(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	project uuid.UUID,
	version *Version,
) error {
	if version.WikiRevisionID == nil {
		return nil
	}
	if err := canVersion(ctx, q, p, project, version.ID); errors.Is(err, ErrNotFound) {
		version.Excerpt = ""
		return nil
	} else {
		return err
	}
}

// Live embeds authorize the working Page; pinned embeds authorize the retained tree.
func canReference(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	project, id uuid.UUID,
	live bool,
) error {
	if !live {
		return canVersion(ctx, q, p, project, id)
	}
	source, err := canVersionSource(ctx, q, p, project, id)
	if err != nil {
		return err
	}
	if !source.WikiPageID.Valid || source.AccessOutputID.Valid {
		return ErrInvalid
	}
	return nil
}
