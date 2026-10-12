package company

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

// ResourceService owns the Company-scoped typed resources: the catalog a
// Company member reads and the mutations its owner applies to it.
type ResourceService interface {
	ListResources(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		ResourceListOptions,
	) ([]ResourceResponse, error)
	FetchResource(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID) (ResourceResponse, error)
	CreateLocalDirectoryResource(
		context.Context,
		uuid.UUID,
		uuid.UUID,
		CreateLocalDirectoryResourceInput,
	) (ResourceCreateResult, error)
	UpdateResource(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		uuid.UUID,
		ResourceUpdateInput,
	) (ResourceResponse, error)
	DeleteResource(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, RevisionInput) error
}

var _ ResourceService = (*DefaultResourceService)(nil)

type DefaultResourceService struct {
	store *db.Store
	bus   *events.Bus
}

type ResourceServiceOptions struct {
	Store *db.Store
	Bus   *events.Bus
}

func NewResourceService(options ResourceServiceOptions) (*DefaultResourceService, error) {
	if options.Store == nil {
		return nil, errors.New("Company resource database store is required")
	}
	if options.Bus == nil {
		return nil, errors.New("Company resource event bus is required")
	}
	return &DefaultResourceService{store: options.Store, bus: options.Bus}, nil
}

func (s *DefaultResourceService) ListResources(
	ctx context.Context,
	principal auth.UserPrincipal,
	companyID uuid.UUID,
	options ResourceListOptions,
) ([]ResourceResponse, error) {
	if err := s.resourceAccess(ctx, principal, companyID); err != nil {
		return nil, err
	}
	resources, err := listResourcesForOwner(
		ctx,
		s.store.Queries(),
		principal.UserID,
		companyID,
		options,
	)
	if err != nil {
		return nil, err
	}
	if len(resources) == 0 {
		if err := s.resourceAccess(ctx, principal, companyID); err != nil {
			return nil, err
		}
	}
	return resources, nil
}

func (s *DefaultResourceService) FetchResource(
	ctx context.Context,
	principal auth.UserPrincipal,
	companyID uuid.UUID,
	resourceID uuid.UUID,
) (ResourceResponse, error) {
	if err := s.resourceAccess(ctx, principal, companyID); err != nil {
		return ResourceResponse{}, err
	}
	resource, err := getResourceForOwner(ctx, s.store.Queries(), principal.UserID, companyID, resourceID)
	if errors.Is(err, ErrResourceNotFound) {
		if authErr := s.resourceAccess(ctx, principal, companyID); authErr != nil {
			return ResourceResponse{}, authErr
		}
		return ResourceResponse{}, ErrResourceNotFound
	}
	if err != nil {
		return ResourceResponse{}, err
	}
	return resource, nil
}

// CreateLocalDirectoryResource registers a daemon-scoped local directory for
// ownerUserID. The daemon control plane (wave 4) is the calling transport; it
// authenticates the daemon and resolves the owning user before calling in.
func (s *DefaultResourceService) CreateLocalDirectoryResource(
	ctx context.Context,
	ownerUserID, companyID uuid.UUID,
	input CreateLocalDirectoryResourceInput,
) (ResourceCreateResult, error) {
	if !util.ValidUUID(companyID) {
		return ResourceCreateResult{}, ErrResourceNotFound
	}
	if !util.ValidUUID(ownerUserID) {
		return ResourceCreateResult{}, ErrUnauthenticated
	}
	normalized, err := normalizeCreateLocalDirectoryResourceInput(input)
	if err != nil {
		return ResourceCreateResult{}, err
	}
	var result ResourceCreateResult
	var pending []events.Event
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		pending = pending[:0]
		membership, err := tx.GetActiveCompanyMembership(ctx, generated.GetActiveCompanyMembershipParams{
			CompanyID: companyID,
			UserID:    ownerUserID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrResourceNotFound
		}
		if err != nil {
			return fmt.Errorf("authorize local-directory resource owner: %w", err)
		}
		if !auth.CompanyRole(membership.Role).Can(auth.PermissionCompanyResourcesManage) {
			return ErrForbidden
		}
		result, err = createLocalDirectoryResource(ctx, tx, companyID, normalized, ownerUserID)
		if err != nil {
			return err
		}
		if result.Created {
			return stageResourceEvent(
				&pending, events.UserActor(ownerUserID.String()),
				EventResourceCreated, result.Resource,
			)
		}
		return nil
	})
	if err != nil {
		return ResourceCreateResult{}, err
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	return result, nil
}

func (s *DefaultResourceService) UpdateResource(
	ctx context.Context,
	principal auth.UserPrincipal,
	companyID uuid.UUID,
	resourceID uuid.UUID,
	input ResourceUpdateInput,
) (ResourceResponse, error) {
	if err := s.resourceAccess(ctx, principal, companyID); err != nil {
		return ResourceResponse{}, err
	}
	if !util.ValidUUID(resourceID) {
		return ResourceResponse{}, ErrResourceNotFound
	}
	normalized, err := normalizeResourceUpdateInput(input)
	if err != nil {
		return ResourceResponse{}, err
	}
	var result ResourceResponse
	var pending []events.Event
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		pending = pending[:0]
		if err := authorizeResourceMutation(ctx, tx, principal.UserID, companyID); err != nil {
			return err
		}
		current, err := lockResourceForUpdate(ctx, tx, companyID, resourceID)
		if err != nil {
			return err
		}
		if current.Revision != normalized.ExpectedRevision {
			if !isReplayOfResourceUpdate(current, normalized) {
				return ErrResourceConflict
			}
			// Answer from the stored row without writing or re-emitting.
			result, err = getResourceProjection(ctx, tx, companyID, resourceID)
			return err
		}
		label, position := current.Label, current.Position
		changedFields := make([]ResourceField, 0, 2)
		if normalized.Label != nil {
			label = *normalized.Label
			if label != current.Label {
				changedFields = append(changedFields, ResourceFieldLabel)
			}
		}
		if normalized.Position != nil {
			position = *normalized.Position
			if position != current.Position {
				changedFields = append(changedFields, ResourceFieldPosition)
			}
		}
		if len(changedFields) == 0 {
			result, err = getResourceProjection(ctx, tx, companyID, resourceID)
			return err
		}
		updated, err := updateResource(
			ctx,
			tx,
			companyID,
			resourceID,
			label,
			position,
			current.Revision,
		)
		if err != nil {
			return err
		}
		if err := stageResourceEvent(
			&pending, events.UserActor(principal.UserID.String()),
			EventResourceUpdated, updated,
		); err != nil {
			return err
		}
		result = updated
		return nil
	})
	if err != nil {
		return ResourceResponse{}, err
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	return result, nil
}

func (s *DefaultResourceService) DeleteResource(
	ctx context.Context,
	principal auth.UserPrincipal,
	companyID uuid.UUID,
	resourceID uuid.UUID,
	input RevisionInput,
) error {
	if err := s.resourceAccess(ctx, principal, companyID); err != nil {
		return err
	}
	if !util.ValidUUID(resourceID) {
		return ErrResourceNotFound
	}
	var pending []events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		pending = pending[:0]
		if err := authorizeResourceMutation(ctx, tx, principal.UserID, companyID); err != nil {
			return err
		}
		version, err := lockResourceVersion(ctx, tx, companyID, resourceID)
		if err != nil {
			return err
		}
		if version != input.ExpectedRevision {
			return ErrResourceConflict
		}
		if err := deleteResource(ctx, tx, companyID, resourceID, input.ExpectedRevision); err != nil {
			return err
		}
		pending = append(pending, newResourceDeletedEvent(
			events.UserActor(principal.UserID.String()),
			companyID, resourceID, input.ExpectedRevision+1,
		))
		return nil
	})
	if err != nil {
		return err
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	return nil
}

// stageResourceEvent appends one resource lifecycle event to the caller-owned
// post-commit batch. A staging failure aborts the transaction that produced
// the resource.
func stageResourceEvent(
	pending *[]events.Event,
	actor events.Actor,
	name string,
	resource ResourceResponse,
) error {
	*pending = append(*pending, newResourceEvent(actor, name, resource))
	return nil
}

// resourceAccess resolves the actor's active membership in one query and
// enforces the owner-only policy. A missing membership or Company is
// ErrResourceNotFound so a non-member cannot distinguish "no access" from "no such
// Company".
func (s *DefaultResourceService) resourceAccess(
	ctx context.Context,
	principal auth.UserPrincipal,
	companyID uuid.UUID,
) error {
	row, err := s.store.Queries().GetCompanyResourceAccess(ctx, generated.GetCompanyResourceAccessParams{
		ActorUserID: principal.UserID, CompanyID: companyID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrResourceNotFound
	}
	if err != nil {
		return fmt.Errorf("authorize Company resource access: %w", err)
	}
	if !auth.CompanyRole(row.Role).Can(auth.PermissionCompanyResourcesManage) {
		return ErrForbidden
	}
	return nil
}

func authorizeResourceMutation(ctx context.Context, tx *generated.Queries, actorUserID, companyID uuid.UUID) error {
	return authorizeMutationPermission(ctx, tx, actorUserID, companyID)
}

// authorizeMutationPermission takes the canonical Company and membership locks
// before checking company.resources.manage, so a concurrent revocation or role
// change serializes behind this transaction instead of racing it. Only
// meaningful inside a transaction.
func authorizeMutationPermission(ctx context.Context, tx *generated.Queries, actorUserID, companyID uuid.UUID) error {
	if _, err := tx.LockCompanyForMembershipMutation(
		ctx, companyID,
	); errors.Is(err, pgx.ErrNoRows) {
		return ErrResourceNotFound
	} else if err != nil {
		return fmt.Errorf("lock Company for Company resource mutation: %w", err)
	}
	membership, err := tx.GetCompanyMemberForUpdate(ctx, generated.GetCompanyMemberForUpdateParams{
		CompanyID: companyID,
		UserID:    actorUserID,
	})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && membership.Status != "active") {
		return ErrResourceNotFound
	}
	if err != nil {
		return fmt.Errorf("lock acting Company member for Company resource mutation: %w", err)
	}
	if !auth.CompanyRole(membership.Role).Can(auth.PermissionCompanyResourcesManage) {
		return ErrForbidden
	}
	return nil
}

// listResourcesForOwner returns every resource; the caller has already been
// authorized for company.resources.manage.
func listResourcesForOwner(
	ctx context.Context,
	q *generated.Queries,
	actorUserID, companyID uuid.UUID,
	options ResourceListOptions,
) ([]ResourceResponse, error) {
	params := generated.ListCompanyResourcesForOwnerParams{
		ActorRoles:  auth.RolesWith(auth.PermissionCompanyResourcesManage),
		ActorUserID: actorUserID,
		CompanyID:   companyID,
		DaemonID:    util.PtrToText(options.DaemonID),
	}
	rows, err := q.ListCompanyResourcesForOwner(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list Company resources: %w", err)
	}
	resources := make([]ResourceResponse, 0, len(rows))
	for _, row := range rows {
		resources = append(resources, companyResourceResponse(
			row.CompanyResource,
			row.DaemonName,
			DaemonStatus(row.DaemonStatus),
			util.TimestampToTime(row.DaemonLastSeenAt),
			row.CreatedByDisplayName,
			util.TextToPtr(row.CreatedByAvatarUrl),
		))
	}
	return resources, nil
}

// getResourceForOwner reads one resource. Absence is bare ErrResourceNotFound: the
// service re-authorizes on it before conceding the resource does not exist.
func getResourceForOwner(
	ctx context.Context,
	q *generated.Queries,
	actorUserID, companyID, resourceID uuid.UUID,
) (ResourceResponse, error) {
	row, err := q.GetCompanyResourceForOwner(ctx, generated.GetCompanyResourceForOwnerParams{
		ActorRoles:        auth.RolesWith(auth.PermissionCompanyResourcesManage),
		ActorUserID:       actorUserID,
		CompanyResourceID: resourceID,
		CompanyID:         companyID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ResourceResponse{}, ErrResourceNotFound
	}
	if err != nil {
		return ResourceResponse{}, fmt.Errorf("get Company resource: %w", err)
	}
	return companyResourceResponse(
		row.CompanyResource,
		row.DaemonName,
		DaemonStatus(row.DaemonStatus),
		util.TimestampToTime(row.DaemonLastSeenAt),
		row.CreatedByDisplayName,
		util.TextToPtr(row.CreatedByAvatarUrl),
	), nil
}

func getResourceProjection(
	ctx context.Context,
	q *generated.Queries,
	companyID, resourceID uuid.UUID,
) (ResourceResponse, error) {
	row, err := q.GetCompanyResourceProjection(ctx, generated.GetCompanyResourceProjectionParams{
		CompanyResourceID: resourceID,
		CompanyID:         companyID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ResourceResponse{}, ErrResourceNotFound
	}
	if err != nil {
		return ResourceResponse{}, fmt.Errorf("get Company resource projection: %w", err)
	}
	return companyResourceResponse(
		row.CompanyResource,
		row.DaemonName,
		DaemonStatus(row.DaemonStatus),
		util.TimestampToTime(row.DaemonLastSeenAt),
		row.CreatedByDisplayName,
		util.TextToPtr(row.CreatedByAvatarUrl),
	), nil
}

// lockResourceForUpdate locks the resource row ahead of a mutation.
func lockResourceForUpdate(
	ctx context.Context,
	q *generated.Queries,
	companyID, resourceID uuid.UUID,
) (ResourceResponse, error) {
	row, err := q.GetCompanyResourceByIDForUpdate(ctx, generated.GetCompanyResourceByIDForUpdateParams{
		CompanyResourceID: resourceID, CompanyID: companyID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ResourceResponse{}, ErrResourceNotFound
	}
	if err != nil {
		return ResourceResponse{}, fmt.Errorf("lock Company resource: %w", err)
	}
	record := companyResourceFromRow(row)
	return record.Response, nil
}

// lockResourceVersion locks the resource row ahead of a delete and returns only
// its version: the delete flow never validated the stored record. A missing row
// is bare ErrResourceNotFound — with the tombstone table gone, a replayed delete no
// longer distinguishes "already deleted" from "never existed".
func lockResourceVersion(
	ctx context.Context,
	q *generated.Queries,
	companyID, resourceID uuid.UUID,
) (int64, error) {
	row, err := q.GetCompanyResourceByIDForUpdate(ctx, generated.GetCompanyResourceByIDForUpdateParams{
		CompanyResourceID: resourceID, CompanyID: companyID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrResourceNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("lock Company resource before delete: %w", err)
	}
	return row.Version, nil
}

// createLocalDirectoryResource lets the company/daemon unique index arbitrate
// concurrent creates. sqlc reports the DO NOTHING branch as pgx.ErrNoRows;
// that branch is an exact replay only when every canonical field still matches.
func createLocalDirectoryResource(
	ctx context.Context,
	q *generated.Queries,
	companyID uuid.UUID,
	input CreateLocalDirectoryResourceInput,
	ownerUserID uuid.UUID,
) (ResourceCreateResult, error) {
	row, err := q.InsertCompanyResource(ctx, generated.InsertCompanyResourceParams{
		CompanyID:       companyID,
		ResourceType:    string(ResourceTypeLocalDirectory),
		DaemonID:        input.DaemonID,
		LocalPath:       input.Path,
		Label:           input.Label,
		Position:        input.Position,
		Metadata:        []byte("{}"),
		CreatedByUserID: ownerUserID,
	})
	if err == nil {
		resource, err := getResourceProjection(ctx, q, companyID, row.ID)
		if err != nil {
			return ResourceCreateResult{}, err
		}
		return ResourceCreateResult{Resource: resource, Created: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ResourceCreateResult{}, mapMutationError("insert local-directory Company resource", err)
	}

	existing, err := q.GetCompanyResourceByDaemonForUpdate(
		ctx,
		generated.GetCompanyResourceByDaemonForUpdateParams{
			CompanyID: companyID,
			DaemonID:  input.DaemonID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ResourceCreateResult{}, ErrResourceConflict
	}
	if err != nil {
		return ResourceCreateResult{}, fmt.Errorf("load existing daemon Company resource: %w", err)
	}
	if existing.ResourceType != string(ResourceTypeLocalDirectory) ||
		existing.LocalPath != input.Path || existing.Label != input.Label ||
		existing.Position != input.Position {
		return ResourceCreateResult{}, ErrResourceConflict
	}
	resource, err := getResourceProjection(ctx, q, companyID, existing.ID)
	if err != nil {
		return ResourceCreateResult{}, err
	}
	return ResourceCreateResult{Resource: resource}, nil
}

// updateResource is a compare-and-swap on the resource row; the query updates no
// row when the version moved, which surfaces as ErrResourceConflict.
func updateResource(
	ctx context.Context,
	q *generated.Queries,
	companyID, resourceID uuid.UUID,
	label string,
	position int32,
	expectedVersion int64,
) (ResourceResponse, error) {
	_, err := q.UpdateCompanyResource(ctx, generated.UpdateCompanyResourceParams{
		Label: label, Position: position,
		CompanyResourceID: resourceID, CompanyID: companyID,
		ExpectedVersion: expectedVersion,
	})
	if err != nil {
		return ResourceResponse{}, mapMutationError("update Company resource", err)
	}
	return getResourceProjection(ctx, q, companyID, resourceID)
}

// deleteResource is a compare-and-swap delete; the query deletes no row when
// the version moved, which surfaces as ErrResourceConflict. The returned row is
// deliberately discarded: the query keys on id, company_id, and
// version=expected, so the deleted row's identity equals the parameters the
// caller already holds.
func deleteResource(
	ctx context.Context,
	q *generated.Queries,
	companyID, resourceID uuid.UUID,
	expectedVersion int64,
) error {
	if _, err := q.DeleteCompanyResource(ctx, generated.DeleteCompanyResourceParams{
		CompanyResourceID: resourceID, CompanyID: companyID,
		ExpectedVersion: expectedVersion,
	}); err != nil {
		return mapMutationError("delete Company resource", err)
	}
	return nil
}

// isReplayOfResourceUpdate reports whether a revision mismatch is this
// caller's own retry rather than someone else's write.
//
// Two facts together are the evidence: the row sits exactly one revision past
// the one the request expects, and label and position already hold the values
// it asks for. A racing writer that moved the row somewhere else fails at
// least one of them — and one that happened to ask for the same change has
// already produced the state this caller wants, so answering from the stored
// row is the true answer either way. The CAS in updateResource cannot tell the
// two apart; all it sees is a version that did not match.
//
// The ceiling belongs in here rather than at the call site, because
// ExpectedRevision+1 is the whole comparison and it only means anything below
// that. At protocol.MaxJSONSafeInteger the successor is a revision the wire
// cannot carry and no row can hold; at math.MaxInt64 the addition overflows to
// a negative number a row conceivably could. Refusing the whole range at and
// above the ceiling makes the answer right for any int64 this is handed,
// including the ones `binding:"revision"` already refused at the boundary.
func isReplayOfResourceUpdate(current ResourceResponse, input ResourceUpdateInput) bool {
	if input.ExpectedRevision >= protocol.MaxJSONSafeInteger {
		return false
	}
	return current.Revision == input.ExpectedRevision+1 &&
		(input.Label == nil || current.Label == *input.Label) &&
		(input.Position == nil || current.Position == *input.Position)
}
