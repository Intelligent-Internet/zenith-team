package conversation

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

var (
	ErrStudioProjectNotFound = errors.New("Studio project Run not found")
	ErrStudioProjectInvalid  = errors.New("invalid Studio project operation")
	ErrStudioProjectConflict = errors.New("Studio project is already bound")
)

// StudioProjectService owns the remote project identity for each Conversation.
// Studio authentication and media operations remain on the Run's daemon.
type StudioProjectService interface {
	Resolve(
		context.Context,
		auth.UserPrincipal,
		StudioProjectAccess,
		protocol.StudioProjectRequest,
	) (protocol.StudioProject, error)
}

var _ StudioProjectService = (*DefaultStudioProjectService)(nil)

type DefaultStudioProjectService struct{ store *db.Store }

func NewStudioProjectService(store *db.Store) *DefaultStudioProjectService {
	return &DefaultStudioProjectService{store: store}
}

type StudioProjectAccess struct {
	RunID     uuid.UUID
	RuntimeID uuid.UUID
	DaemonID  string
}

func (s *DefaultStudioProjectService) Resolve(
	ctx context.Context,
	principal auth.UserPrincipal,
	access StudioProjectAccess,
	request protocol.StudioProjectRequest,
) (protocol.StudioProject, error) {
	request.Title = strings.TrimSpace(request.Title)
	request.Description = strings.TrimSpace(request.Description)
	if !validStudioProjectRequest(request) {
		return protocol.StudioProject{}, ErrStudioProjectInvalid
	}
	studioOrigin := request.APIOrigin
	if studioOrigin == "" {
		return protocol.StudioProject{}, ErrStudioProjectInvalid
	}
	canonicalOrigin, err := util.CanonicalStudioOrigin(studioOrigin)
	if err != nil || canonicalOrigin != studioOrigin {
		return protocol.StudioProject{}, ErrStudioProjectInvalid
	}
	digest := sha256.Sum256([]byte(request.RunToken))
	var result protocol.StudioProject
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if err := lockActiveMembership(ctx, q, principal.CompanyID, principal.UserID); err != nil {
			if errors.Is(err, ErrNotFound) {
				return ErrStudioProjectNotFound
			}
			return err
		}
		origin, err := q.ResolveLiveRunOrigin(ctx, digest[:])
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrStudioProjectNotFound
		}
		if err != nil {
			return fmt.Errorf("resolve Studio Run proof: %w", err)
		}
		if origin.RunID != access.RunID || origin.CompanyID != principal.CompanyID {
			return ErrStudioProjectNotFound
		}
		if _, err = q.LockConversationForSystemRunQueueMutation(
			ctx,
			generated.LockConversationForSystemRunQueueMutationParams{
				ConversationID: origin.ConversationID,
				CompanyID:      origin.CompanyID,
			},
		); err != nil {
			return fmt.Errorf("lock Studio Conversation: %w", err)
		}
		_, err = q.LockStudioProjectRun(
			ctx,
			generated.LockStudioProjectRunParams{
				RunID:     access.RunID,
				CompanyID: origin.CompanyID,
				RuntimeID: access.RuntimeID,
				UserID:    uuid.NullUUID{UUID: principal.UserID, Valid: true},
				DaemonID:  access.DaemonID,
				TokenHash: digest[:],
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrStudioProjectNotFound
		}
		if err != nil {
			return fmt.Errorf("lock Studio Run: %w", err)
		}
		var row generated.ConversationStudioProject
		switch request.Action {
		case "reserve":
			row, err = q.ReserveConversationStudioProject(
				ctx,
				generated.ReserveConversationStudioProjectParams{
					CompanyID:      origin.CompanyID,
					ConversationID: origin.ConversationID,
					Title:          request.Title,
					Description:    request.Description,
					ApiOrigin:      studioOrigin,
				},
			)
			result.CanCreate = err == nil
			if errors.Is(err, pgx.ErrNoRows) {
				row, err = q.GetConversationStudioProject(
					ctx,
					generated.GetConversationStudioProjectParams{
						CompanyID:      origin.CompanyID,
						ConversationID: origin.ConversationID,
					},
				)
			}
		case "bind":
			row, err = q.BindConversationStudioProject(
				ctx,
				generated.BindConversationStudioProjectParams{
					CompanyID:      origin.CompanyID,
					ConversationID: origin.ConversationID,
					ID:             uuid.MustParse(request.BindingID),
					ProjectID: uuid.NullUUID{
						UUID:  uuid.MustParse(request.ProjectID),
						Valid: true,
					},
				},
			)
			var pgErr *pgconn.PgError
			if errors.Is(err, pgx.ErrNoRows) || errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return ErrStudioProjectConflict
			}
		case "get":
			row, err = q.GetConversationStudioProject(
				ctx,
				generated.GetConversationStudioProjectParams{
					CompanyID:      origin.CompanyID,
					ConversationID: origin.ConversationID,
				},
			)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
		}
		if err != nil {
			return fmt.Errorf("resolve Studio Conversation project: %w", err)
		}
		result.ID = row.ID.String()
		result.Title = row.Title
		result.Description = row.Description
		if row.ProjectID.Valid {
			result.ProjectID = row.ProjectID.UUID.String()
		}
		return nil
	})
	return result, err
}

func validStudioProjectRequest(request protocol.StudioProjectRequest) bool {
	if request.RunToken == "" || len(request.RunToken) > 256 {
		return false
	}
	switch request.Action {
	case "get":
		return request.Title == "" && request.Description == "" && request.ProjectID == "" &&
			request.BindingID == ""
	case "reserve":
		return request.Title != "" &&
			utf8.ValidString(request.Title) && utf8.ValidString(request.Description) &&
			!strings.ContainsRune(request.Title, '\x00') &&
			!strings.ContainsRune(request.Description, '\x00') &&
			utf8.RuneCountInString(request.Title) <= 200 &&
			utf8.RuneCountInString(request.Description) <= 4000 &&
			request.ProjectID == "" &&
			request.BindingID == ""
	case "bind":
		binding, err := uuid.Parse(request.BindingID)
		if err != nil || binding == uuid.Nil {
			return false
		}
		project, err := uuid.Parse(request.ProjectID)
		return err == nil && project != uuid.Nil && request.Title == "" && request.Description == ""
	default:
		return false
	}
}
