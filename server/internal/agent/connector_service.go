package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"boardly.dev/boardly/server/internal/agent/connectorselection"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/util"
	provideragent "boardly.dev/boardly/server/pkg/agent"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

// ConnectorService owns the server-side connector catalog and the
// execution allowlist derived from one daemon Runtime's discovery snapshot.
type ConnectorService interface {
	ListMessageConnectors(context.Context, auth.UserPrincipal, MessageConnectorScope) ([]MessageConnector, error)
	PublishDiscovery(
		context.Context,
		auth.UserPrincipal,
		ConnectorDiscoveryInput,
	) (ConnectorDiscoveryResult, error)
	ReaffirmDiscovery(
		context.Context,
		auth.UserPrincipal,
		ConnectorDiscoveryReaffirmInput,
	) (ConnectorDiscoveryResult, error)
	ReadAppReadiness(
		context.Context,
		auth.UserPrincipal,
		ConnectorAppReadinessInput,
	) (ConnectorAppReadinessResult, error)
	List(context.Context, auth.UserPrincipal, ConnectorListInput) (ConnectorCatalogResult, error)
	Resolve(context.Context, auth.UserPrincipal, ConnectorResolveInput) (ConnectorResolveResult, error)
	Refresh(context.Context, auth.UserPrincipal) error
	Connect(context.Context, auth.UserPrincipal, uuid.UUID) error
}

var _ ConnectorService = (*DefaultConnectorService)(nil)

// ConnectorRunInvalidator receives committed capability changes without
// coupling the agent domain to execution or the daemon transport.
type ConnectorRunInvalidator interface {
	InvalidateConnectorRuns(context.Context, ConnectorRunChange)
}

type DefaultConnectorService struct {
	store          *db.Store
	bus            *events.Bus
	runInvalidator ConnectorRunInvalidator
}

type ConnectorServiceOptions struct {
	Store          *db.Store
	Bus            *events.Bus
	RunInvalidator ConnectorRunInvalidator
}

func NewConnectorService(options ConnectorServiceOptions) (*DefaultConnectorService, error) {
	if options.Store == nil || options.Bus == nil {
		return nil, errors.New("Connector database store and event bus are required")
	}
	return &DefaultConnectorService{
		store:          options.Store,
		bus:            options.Bus,
		runInvalidator: options.RunInvalidator,
	}, nil
}

func (s *DefaultConnectorService) PublishDiscovery(
	ctx context.Context,
	principal auth.UserPrincipal,
	input ConnectorDiscoveryInput,
) (ConnectorDiscoveryResult, error) {
	if !util.ValidUUID(principal.UserID) || !util.ValidUUID(principal.CompanyID) {
		return ConnectorDiscoveryResult{}, ErrUnauthenticated
	}
	if principal.CompanyID != input.CompanyID {
		return ConnectorDiscoveryResult{}, ErrRuntimeNotFound
	}

	result := ConnectorDiscoveryResult{}
	var discoveredProvider ConnectorProvider
	catalogChanged := false
	var policyEvents []events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		authority, err := lockDiscoveryAuthority(
			ctx,
			tx,
			generated.LockConnectorDiscoveryAuthorityParams{
				RuntimeID:   input.RuntimeID,
				CompanyID:   input.CompanyID,
				DaemonID:    input.DaemonID,
				ActorUserID: principal.UserID,
			},
		)
		if err != nil {
			return err
		}

		provider := ConnectorProvider(authority.Provider)
		discoveredProvider = provider
		normalized, err := normalizeConnectorDiscovery(provider, input, authority.ServerTime.Time)
		if err != nil {
			return err
		}
		current, err := tx.GetConnectorDiscoveryGenerationForUpdate(ctx, normalized.RuntimeID)
		switch {
		case err == nil && current.Generation == normalized.Generation &&
			current.ManifestDigest == normalized.ManifestDigest:
			return nil
		case err == nil && !normalized.ObservedAt.After(current.ObservedAt.Time):
			return ErrConnectorDiscoveryConflict
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("read Connector discovery generation: %w", err)
		}

		catalogChanged = errors.Is(err, pgx.ErrNoRows) || current.ManifestDigest != normalized.ManifestDigest ||
			current.ObservedAt.Time.Before(authority.ServerTime.Time.Add(-connectorDiscoveryStaleAfter))

		projectedItems := projectConnectorCategories(provider, normalized.Items)
		items, err := encodeConnectorDiscoveryBatch(projectedItems)
		if err != nil {
			return err
		}
		applied, err := tx.ApplyConnectorDiscoveryGeneration(
			ctx,
			generated.ApplyConnectorDiscoveryGenerationParams{
				RuntimeID:      normalized.RuntimeID,
				CompanyID:      normalized.CompanyID,
				Provider:       string(provider),
				Generation:     normalized.Generation,
				ManifestDigest: normalized.ManifestDigest,
				ObservedAt:     pgtype.Timestamptz{Time: normalized.ObservedAt, Valid: true},
				Items:          items,
			},
		)
		if err != nil {
			return fmt.Errorf("apply Connector discovery generation: %w", err)
		}
		itemCount := int64(len(normalized.Items))
		if applied.InputCount != itemCount || applied.CatalogCount != itemCount ||
			applied.PolicyCount != itemCount || applied.ObservationCount != itemCount ||
			applied.GenerationCount != 1 {
			return errors.New("applied Connector discovery generation is incomplete")
		}
		if _, err := tx.RefreshCompanyConnectorCatalogFromObservations(
			ctx,
			generated.RefreshCompanyConnectorCatalogFromObservationsParams{
				CompanyID: normalized.CompanyID,
				Provider:  string(provider),
			},
		); err != nil {
			return fmt.Errorf("refresh Company Connector catalog: %w", err)
		}

		agents, err := tx.ListAgentStatusesForRuntime(ctx, normalized.RuntimeID)
		if err != nil {
			return fmt.Errorf("list Runtime Agents for Connector reconciliation: %w", err)
		}
		if len(agents) > 0 {
			reconciled, err := tx.ReconcileCompanyConnectorPolicies(
				ctx,
				generated.ReconcileCompanyConnectorPoliciesParams{
					RuntimeID:                      normalized.RuntimeID,
					CompanyID:                      normalized.CompanyID,
					Provider:                       string(provider),
					MaxDesiredExecutableConnectors: maxDesiredExecutableConnectors,
				},
			)
			if err != nil {
				return fmt.Errorf("reconcile Company Connector policies: %w", err)
			}
			catalogChanged = catalogChanged || reconciled.ReconciledCount > 0
			if reconciled.ReconciledCount > 0 {
				policyEvents = append(policyEvents, connectorPolicyChangedEvent(normalized.CompanyID))
			}
		}
		result.Applied = true
		return nil
	})
	if err != nil {
		return ConnectorDiscoveryResult{}, err
	}
	if result.Applied {
		s.bus.Publish(ctx, TopicConnector, policyEvents...)
		if catalogChanged {
			publishCapabilitiesChanged(
				ctx,
				s.bus,
				input.CompanyID,
				principal.UserID,
				input.RuntimeID,
				"connector",
			)
		}
		runtimeID := input.RuntimeID
		s.invalidateRuns(ctx, ConnectorRunChange{
			CompanyID: input.CompanyID,
			RuntimeID: &runtimeID,
			Provider:  discoveredProvider,
			Type:      ConnectorRunChangeDiscovery,
		})
	}
	return result, nil
}

// ReaffirmDiscovery renews the freshness of the Runtime's unchanged current
// generation after the daemon re-observed its provider, without resending or
// rewriting the snapshot. It takes the same Company-then-Runtime fence and
// applies the same validity and clock-skew rules as PublishDiscovery. Only a
// different or missing current generation conflicts; an observation that is
// not newer is acknowledged unapplied. An applied reaffirmation enables the
// Connectors this Runtime can execute for Agents bound since the last publish,
// and revises no other policy: it re-observed nothing, and a full
// reconciliation would flip a policy two Runtimes of one provider see
// differently on every keepalive. It invalidates Runs only when it enabled one.
func (s *DefaultConnectorService) ReaffirmDiscovery(
	ctx context.Context,
	principal auth.UserPrincipal,
	input ConnectorDiscoveryReaffirmInput,
) (ConnectorDiscoveryResult, error) {
	if !util.ValidUUID(principal.UserID) || !util.ValidUUID(principal.CompanyID) {
		return ConnectorDiscoveryResult{}, ErrUnauthenticated
	}
	if principal.CompanyID != input.CompanyID {
		return ConnectorDiscoveryResult{}, ErrRuntimeNotFound
	}

	result := ConnectorDiscoveryResult{}
	var provider ConnectorProvider
	policiesChanged := false
	freshnessRecovered := false
	var policyEvents []events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		authority, err := lockDiscoveryAuthority(
			ctx,
			tx,
			generated.LockConnectorDiscoveryAuthorityParams{
				RuntimeID:   input.RuntimeID,
				CompanyID:   input.CompanyID,
				DaemonID:    input.DaemonID,
				ActorUserID: principal.UserID,
			},
		)
		if err != nil {
			return err
		}
		provider = ConnectorProvider(authority.Provider)
		normalized, err := normalizeConnectorDiscoveryReaffirmation(provider, input, authority.ServerTime.Time)
		if err != nil {
			return err
		}
		current, err := tx.GetConnectorDiscoveryGenerationForUpdate(ctx, normalized.RuntimeID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return ErrConnectorDiscoveryConflict
		case err != nil:
			return fmt.Errorf("read Connector discovery generation: %w", err)
		case current.Generation != normalized.Generation ||
			current.ManifestDigest != normalized.ManifestDigest:
			return ErrConnectorDiscoveryConflict
		// PostgreSQL keeps microseconds, so a replay that differs only below
		// that precision is not newer either.
		case !normalized.ObservedAt.Truncate(time.Microsecond).After(current.ObservedAt.Time):
			return nil
		}
		freshnessRecovered = current.ObservedAt.Time.Before(
			authority.ServerTime.Time.Add(-connectorDiscoveryStaleAfter),
		)

		if _, err := tx.TouchConnectorDiscoveryGeneration(
			ctx,
			generated.TouchConnectorDiscoveryGenerationParams{
				ObservedAt:     pgtype.Timestamptz{Time: normalized.ObservedAt, Valid: true},
				RuntimeID:      normalized.RuntimeID,
				CompanyID:      normalized.CompanyID,
				Provider:       string(provider),
				Generation:     normalized.Generation,
				ManifestDigest: normalized.ManifestDigest,
			},
		); err != nil {
			return fmt.Errorf("renew Connector discovery generation: %w", err)
		}
		result.Applied = true

		agents, err := tx.ListAgentStatusesForRuntime(ctx, normalized.RuntimeID)
		if err != nil {
			return fmt.Errorf("list Runtime Agents for Connector reconciliation: %w", err)
		}
		if len(agents) > 0 {
			reconciled, err := tx.ReconcileCompanyConnectorPolicies(
				ctx,
				generated.ReconcileCompanyConnectorPoliciesParams{
					RuntimeID:                      normalized.RuntimeID,
					CompanyID:                      normalized.CompanyID,
					Provider:                       string(provider),
					MaxDesiredExecutableConnectors: maxDesiredExecutableConnectors,
					EnableOnly:                     true,
				},
			)
			if err != nil {
				return fmt.Errorf("reconcile Company Connector policies: %w", err)
			}
			policiesChanged = reconciled.ReconciledCount > 0
			if policiesChanged {
				policyEvents = append(policyEvents, connectorPolicyChangedEvent(normalized.CompanyID))
			}
		}
		return nil
	})
	if err != nil {
		return ConnectorDiscoveryResult{}, err
	}
	if policiesChanged || freshnessRecovered {
		publishCapabilitiesChanged(ctx, s.bus, input.CompanyID, principal.UserID, input.RuntimeID, "connector")
	}
	if policiesChanged {
		s.bus.Publish(ctx, TopicConnector, policyEvents...)
		runtimeID := input.RuntimeID
		s.invalidateRuns(ctx, ConnectorRunChange{
			CompanyID: input.CompanyID,
			RuntimeID: &runtimeID,
			Provider:  provider,
			Type:      ConnectorRunChangeDiscovery,
		})
	}
	return result, nil
}

// lockDiscoveryAuthority takes the Company and Runtime locks in separate
// statements so a publisher that waited behind membership revocation reads
// the newly committed membership state before any discovery write.
func lockDiscoveryAuthority(
	ctx context.Context,
	q *generated.Queries,
	params generated.LockConnectorDiscoveryAuthorityParams,
) (generated.LockConnectorDiscoveryAuthorityRow, error) {
	if _, err := q.LockCompanyForMembershipMutation(ctx, params.CompanyID); errors.Is(err, pgx.ErrNoRows) {
		return generated.LockConnectorDiscoveryAuthorityRow{}, ErrRuntimeNotFound
	} else if err != nil {
		return generated.LockConnectorDiscoveryAuthorityRow{},
			fmt.Errorf("lock Company for discovery publication: %w", err)
	}
	authority, err := q.LockConnectorDiscoveryAuthority(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		return generated.LockConnectorDiscoveryAuthorityRow{}, ErrRuntimeNotFound
	}
	if err != nil {
		return generated.LockConnectorDiscoveryAuthorityRow{},
			fmt.Errorf("lock Runtime discovery authority: %w", err)
	}
	return authority, nil
}

func (s *DefaultConnectorService) List(
	ctx context.Context,
	principal auth.UserPrincipal,
	input ConnectorListInput,
) (ConnectorCatalogResult, error) {
	if !util.ValidUUID(principal.UserID) || !util.ValidUUID(principal.CompanyID) {
		return ConnectorCatalogResult{}, ErrUnauthenticated
	}
	// A read, and the context query already restricts it to a runtime the
	// caller registered (ADR 0014): any active member reads their own
	// machine's catalog — it is what the Agent capabilities view renders for
	// them — while reviewing an app or changing Company Connector policy
	// stays a connectors.manage write.
	normalized, err := normalizeConnectorListInput(input)
	if err != nil {
		return ConnectorCatalogResult{}, err
	}
	contextRow, err := s.store.Queries().GetCompanyConnectorContextForOwner(
		ctx,
		generated.GetCompanyConnectorContextForOwnerParams{
			DiscoveryStaleAfterMilliseconds: connectorDiscoveryStaleAfter.Milliseconds(),
			RuntimeStaleAfterMilliseconds:   RuntimeStaleThreshold.Milliseconds(),
			CompanyID:                       principal.CompanyID,
			ActorUserID:                     principal.UserID,
			ExplicitProvider:                string(normalized.Provider),
			ExplicitRuntimeID:               util.NullUUIDFromPtr(normalized.RuntimeID),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ConnectorCatalogResult{}, catalogRuntimeMissing(
			ctx, s.store.Queries(), principal, normalized.RuntimeID, ErrConnectorProviderUnavailable,
		)
	}
	if err != nil {
		return ConnectorCatalogResult{}, fmt.Errorf("read Company Connector context: %w", err)
	}
	activeProvider := ConnectorProvider(contextRow.Provider)
	selectedRuntimeID := uuid.NullUUID{UUID: contextRow.RuntimeID, Valid: true}

	params := generated.ListCompanyConnectorCatalogParams{
		ActorRoles:  auth.AllRoles(),
		ActorUserID: principal.UserID, CompanyID: principal.CompanyID,
		Provider: string(activeProvider), SearchQuery: normalized.Query,
		Source:   string(normalized.Source),
		Category: normalized.Category, Status: string(normalized.Status),
		ReviewRequired: normalized.ReviewRequired,
		PageSize:       int32(normalized.Limit + 1), SelectedRuntimeID: selectedRuntimeID,
	}
	if normalized.Cursor != nil {
		params.HasCursor = true
		params.AfterName = normalized.Cursor.Name
		params.AfterID = normalized.Cursor.ID
	}
	rows, err := s.store.Queries().ListCompanyConnectorCatalog(ctx, params)
	if err != nil {
		return ConnectorCatalogResult{}, fmt.Errorf("list Company Connector catalog: %w", err)
	}
	hasMore := len(rows) > normalized.Limit
	if hasMore {
		rows = rows[:normalized.Limit]
	}
	connectors := make([]ConnectorItemV1, len(rows))
	for index, row := range rows {
		connectors[index] = connectorItemFromProjection(
			row.ConnectorCatalogItem,
			row.DesiredEnabled,
			row.PolicyVersion,
			row.ReviewRequired,
			!contextRow.Stale,
			connectorSelectedProjection{
				Name: row.SelectedName, HasObservation: row.HasSelectedObservation,
				Description: row.SelectedDescription, Category: row.SelectedCategory,
				IconURL: row.SelectedIconUrl, InstallCount: row.SelectedInstallCount,
				Status: row.SelectedStatus, Action: row.SelectedAction,
				CanExecute: row.SelectedCanExecute, ObservedAt: row.SelectedObservedAt,
			},
		)
	}
	if err := chooseConnectorLogos(
		ctx, s.store.Queries(), principal, contextRow.RuntimeID, connectors,
	); err != nil {
		return ConnectorCatalogResult{}, err
	}
	var nextCursor *string
	if hasMore && len(connectors) > 0 {
		last := rows[len(rows)-1]
		encoded, err := FormatConnectorCursor(ConnectorCursor{
			Name: last.SortName,
			ID:   last.ConnectorCatalogItem.ID,
		})
		if err != nil {
			return ConnectorCatalogResult{}, fmt.Errorf("format Connector cursor: %w", err)
		}
		nextCursor = &encoded
	}
	countsParams := generated.CountCompanyConnectorCatalogParams{
		ActorRoles:  auth.AllRoles(),
		ActorUserID: principal.UserID, CompanyID: principal.CompanyID,
		Provider: string(activeProvider), SearchQuery: normalized.Query,
		Source:   string(normalized.Source),
		Category: normalized.Category, Status: string(normalized.Status),
		ReviewRequired:    normalized.ReviewRequired,
		SelectedRuntimeID: selectedRuntimeID,
	}
	totalCount, err := s.store.Queries().CountCompanyConnectorCatalog(ctx, countsParams)
	if err != nil {
		return ConnectorCatalogResult{}, fmt.Errorf("count Company Connector catalog: %w", err)
	}
	connectedCount, err := s.store.Queries().CountCompanyConnectedConnectors(
		ctx,
		generated.CountCompanyConnectedConnectorsParams{
			ActorRoles:  auth.AllRoles(),
			ActorUserID: principal.UserID, CompanyID: principal.CompanyID,
			Provider: string(activeProvider), SelectedRuntimeID: selectedRuntimeID,
			Source: string(normalized.Source),
		},
	)
	if err != nil {
		return ConnectorCatalogResult{}, fmt.Errorf("count connected Company Connectors: %w", err)
	}
	categoryRows, err := s.store.Queries().CountCompanyConnectorsByCategory(
		ctx,
		generated.CountCompanyConnectorsByCategoryParams{
			ActorRoles:  auth.AllRoles(),
			ActorUserID: principal.UserID, CompanyID: principal.CompanyID,
			Provider: string(activeProvider), SelectedRuntimeID: selectedRuntimeID,
			Source: string(normalized.Source),
		},
	)
	if err != nil {
		return ConnectorCatalogResult{}, fmt.Errorf("count Company Connectors by category: %w", err)
	}
	countsByCategory := make(map[connectorCategory]int64, len(categoryRows))
	for _, row := range categoryRows {
		countsByCategory[canonicalConnectorCategoryLabel(row.Category)] += row.Count
	}
	categoryCounts := make([]ConnectorCategoryCount, 0, len(categoryRows))
	for _, category := range connectorCategoryOrder {
		if count, present := countsByCategory[category]; present {
			categoryCounts = append(categoryCounts, ConnectorCategoryCount{
				Category: string(category),
				Count:    count,
			})
		}
	}

	return ConnectorCatalogResult{
		SchemaVersion:  ConnectorSchemaVersion,
		ActiveProvider: activeProvider,
		Runtime: &ConnectorRuntimeSummary{
			ID:     contextRow.RuntimeID,
			Name:   contextRow.RuntimeName,
			Online: contextRow.RuntimeOnline,
		},
		Connectors:     connectors,
		NextCursor:     nextCursor,
		TotalCount:     totalCount,
		ConnectedCount: connectedCount,
		CategoryCounts: categoryCounts,
		Stale:          contextRow.Stale,
	}, nil
}

func (s *DefaultConnectorService) Resolve(
	ctx context.Context,
	principal auth.UserPrincipal,
	input ConnectorResolveInput,
) (ConnectorResolveResult, error) {
	if !util.ValidUUID(principal.UserID) || !util.ValidUUID(principal.CompanyID) {
		return ConnectorResolveResult{}, ErrUnauthenticated
	}
	if !principal.Can(auth.PermissionConnectorsManage) {
		return ConnectorResolveResult{}, ErrForbidden
	}
	contextRow, err := s.store.Queries().GetCompanyConnectorContextForOwner(
		ctx,
		generated.GetCompanyConnectorContextForOwnerParams{
			DiscoveryStaleAfterMilliseconds: connectorDiscoveryStaleAfter.Milliseconds(),
			RuntimeStaleAfterMilliseconds:   RuntimeStaleThreshold.Milliseconds(),
			CompanyID:                       principal.CompanyID,
			ActorUserID:                     principal.UserID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ConnectorResolveResult{}, ErrConnectorProviderUnavailable
	}
	if err != nil {
		return ConnectorResolveResult{}, fmt.Errorf("read Company Connector context: %w", err)
	}
	provider := ConnectorProvider(contextRow.Provider)
	normalized, err := normalizeConnectorResolveInput(provider, input)
	if err != nil {
		return ConnectorResolveResult{}, err
	}
	sources, nativeIDs := connectorIdentityColumns(normalized.Identities)
	rows, err := s.store.Queries().ResolveCompanyConnectorsByIdentity(
		ctx,
		generated.ResolveCompanyConnectorsByIdentityParams{
			ActorRoles: auth.RolesWith(auth.PermissionConnectorsManage),
			CompanyID:  principal.CompanyID, Provider: string(provider),
			ActorUserID: principal.UserID, Sources: sources, NativeIds: nativeIDs,
			SelectedRuntimeID: uuid.NullUUID{UUID: contextRow.RuntimeID, Valid: true},
		},
	)
	if err != nil {
		return ConnectorResolveResult{}, fmt.Errorf("resolve Company Connectors by identity: %w", err)
	}
	connectors := make([]ConnectorItemV1, len(rows))
	for index, row := range rows {
		connectors[index] = connectorItemFromProjection(
			row.ConnectorCatalogItem,
			row.DesiredEnabled,
			row.PolicyVersion,
			row.ReviewRequired,
			!contextRow.Stale,
			connectorSelectedProjection{
				Name: row.SelectedName, HasObservation: row.HasSelectedObservation,
				Description: row.SelectedDescription, Category: row.SelectedCategory,
				IconURL: row.SelectedIconUrl, InstallCount: row.SelectedInstallCount,
				Status: row.SelectedStatus, Action: row.SelectedAction,
				CanExecute: row.SelectedCanExecute, ObservedAt: row.SelectedObservedAt,
			},
		)
	}
	if err := chooseConnectorLogos(
		ctx, s.store.Queries(), principal, contextRow.RuntimeID, connectors,
	); err != nil {
		return ConnectorResolveResult{}, err
	}
	return ConnectorResolveResult{
		SchemaVersion: ConnectorSchemaVersion,
		Connectors:    connectors,
	}, nil
}

// Refresh asks the caller's own runtime for its list again. Like the read it
// starts from, it needs no connectors.manage: the computer is the caller's
// (ADR 0014). The cloud cannot reach the machine; the desktop asks its own
// daemon instead.
func (s *DefaultConnectorService) Refresh(
	ctx context.Context,
	principal auth.UserPrincipal,
) error {
	if _, err := s.List(ctx, principal, ConnectorListInput{Limit: 1}); err != nil {
		return err
	}
	return ErrConnectorActionUnavailable
}

func (s *DefaultConnectorService) Connect(
	ctx context.Context,
	principal auth.UserPrincipal,
	connectorID uuid.UUID,
) error {
	if !util.ValidUUID(principal.UserID) || !util.ValidUUID(principal.CompanyID) {
		return ErrUnauthenticated
	}
	if !principal.Can(auth.PermissionConnectorsManage) {
		return ErrForbidden
	}
	if !util.ValidUUID(connectorID) {
		return ErrConnectorNotFound
	}
	var changedProvider ConnectorProvider
	var policyEvents []events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		if err := lockActiveConnectorOwner(ctx, tx, principal.CompanyID, principal.UserID); err != nil {
			return err
		}
		current, err := tx.GetCompanyConnectorForOwner(
			ctx,
			generated.GetCompanyConnectorForOwnerParams{
				ActorRoles:  auth.RolesWith(auth.PermissionConnectorsManage),
				ActorUserID: principal.UserID,
				ConnectorID: connectorID,
				CompanyID:   principal.CompanyID,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConnectorNotFound
		}
		if err != nil {
			return fmt.Errorf("read Company Connector policy: %w", err)
		}
		if !current.ReviewRequired {
			return ErrConnectorActionUnavailable
		}
		changedProvider = ConnectorProvider(current.ConnectorCatalogItem.Provider)
		contractDigest, err := tx.GetEnableConnectorContractOnSelectedRuntimeForOwner(
			ctx,
			generated.GetEnableConnectorContractOnSelectedRuntimeForOwnerParams{
				ActorRoles:  auth.RolesWith(auth.PermissionConnectorsManage),
				ActorUserID: principal.UserID, ConnectorID: connectorID,
				CompanyID:                       principal.CompanyID,
				RuntimeStaleAfterMilliseconds:   RuntimeStaleThreshold.Milliseconds(),
				DiscoveryStaleAfterMilliseconds: connectorDiscoveryStaleAfter.Milliseconds(),
				MaxDesiredExecutableConnectors:  maxDesiredExecutableConnectors,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConnectorActionUnavailable
		}
		if err != nil {
			return fmt.Errorf("authorize selected Runtime Connector review: %w", err)
		}
		if !contractDigest.Valid || !validConnectorDigest(contractDigest.String) {
			return ErrConnectorActionUnavailable
		}
		if current.PolicyVersion >= maxSafeInteger {
			return ErrConnectorVersionExhausted
		}
		if _, err := tx.UpdateConnectorPolicyForOwner(
			ctx,
			generated.UpdateConnectorPolicyForOwnerParams{
				ActorRoles:  auth.RolesWith(auth.PermissionConnectorsManage),
				ActorUserID: principal.UserID, ConnectorID: connectorID,
				CompanyID: principal.CompanyID, DesiredEnabled: true,
				ExpectedVersion:        current.PolicyVersion,
				ReviewedContractDigest: contractDigest.String,
			},
		); errors.Is(err, pgx.ErrNoRows) {
			return ErrConnectorConflict
		} else if err != nil {
			return fmt.Errorf("accept Company Connector contract review: %w", err)
		}
		policyEvents = append(policyEvents, connectorPolicyChangedEvent(principal.CompanyID))
		return nil
	})
	if err != nil {
		return err
	}
	changedConnectorID := connectorID
	s.bus.Publish(ctx, TopicConnector, policyEvents...)
	s.invalidateRuns(ctx, ConnectorRunChange{
		CompanyID:   principal.CompanyID,
		ConnectorID: &changedConnectorID,
		Provider:    changedProvider,
		Type:        ConnectorRunChangePolicy,
	})
	return nil
}

// lockActiveConnectorOwner takes the canonical Company then membership locks
// in separate statements. A mutation that waited behind account deletion
// therefore reads the committed membership state instead of continuing from
// the stale snapshot that resolved the request principal.
func lockActiveConnectorOwner(
	ctx context.Context,
	q *generated.Queries,
	companyID, userID uuid.UUID,
) error {
	if _, err := q.LockCompanyForMembershipMutation(ctx, companyID); errors.Is(err, pgx.ErrNoRows) {
		return ErrConnectorNotFound
	} else if err != nil {
		return fmt.Errorf("lock Company for Connector mutation: %w", err)
	}
	membership, err := q.GetCompanyMemberForUpdate(
		ctx,
		generated.GetCompanyMemberForUpdateParams{CompanyID: companyID, UserID: userID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrForbidden
	}
	if err != nil {
		return fmt.Errorf("lock Connector owner membership: %w", err)
	}
	if membership.Status != "active" || !auth.CompanyRole(membership.Role).Can(auth.PermissionConnectorsManage) {
		return ErrForbidden
	}
	return nil
}

func (s *DefaultConnectorService) invalidateRuns(
	ctx context.Context,
	change ConnectorRunChange,
) {
	if s.runInvalidator != nil {
		s.runInvalidator.InvalidateConnectorRuns(context.WithoutCancel(ctx), change)
	}
}

// FreezeRunConnectorSnapshotInTx writes the reviewed Connector contract for a
// freshly claimed Run through the caller's transaction-bound query set. The
// execution domain owns the claim transaction; this exported function keeps
// Connector policy and discovery rules inside the domain that owns them.
func FreezeRunConnectorSnapshotInTx(
	ctx context.Context,
	q *generated.Queries,
	runID uuid.UUID,
	runtimeID uuid.UUID,
) (generated.Run, error) {
	if q == nil || runID == uuid.Nil || !util.ValidUUID(runtimeID) {
		return generated.Run{}, ErrInvalidInput
	}
	run, err := q.FreezeRunConnectorSnapshot(ctx, generated.FreezeRunConnectorSnapshotParams{
		DiscoveryStaleAfterMilliseconds: connectorDiscoveryStaleAfter.Milliseconds(),
		RunID:                           runID,
		RuntimeID:                       runtimeID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return generated.Run{}, ErrConnectorSnapshotUnavailable
	}
	if err != nil {
		return generated.Run{}, fmt.Errorf("freeze Run Connector snapshot: %w", err)
	}
	var selection struct {
		MessageConnectorSelection bool      `json:"messageConnectorSelection"`
		MessageConnectorRuntimeID uuid.UUID `json:"messageConnectorRuntimeId"`
	}
	if err := json.Unmarshal(run.Context, &selection); err != nil {
		return generated.Run{}, err
	}
	if selection.MessageConnectorSelection {
		available, err := connectorselection.ReadAgentMessageConnectors(
			ctx,
			q,
			auth.UserPrincipal{CompanyID: run.CompanyID, UserID: run.ExecutionAuthorizedByUserID.UUID},
			run.AgentID,
		)
		if err != nil {
			return generated.Run{}, err
		}
		if available.RuntimeID != runtimeID || selection.MessageConnectorRuntimeID != runtimeID {
			return generated.Run{}, ErrConnectorSnapshotUnavailable
		}
		selected, err := q.ListSelectedRunConnectorIDs(ctx, runID)
		if err != nil {
			return generated.Run{}, err
		}
		for _, id := range selected {
			found := false
			for _, item := range available.Items {
				if item.ID == id {
					found = true
					break
				}
			}
			if !found {
				return generated.Run{}, ErrConnectorSnapshotUnavailable
			}
		}
	}

	return run, nil
}

// ListRunIDsWithInvalidConnectorSnapshots reports active Runs whose frozen
// Connector contract no longer matches reviewed policy and fresh discovery.
// It performs no lifecycle mutation; the composition root hands the returned
// identities to the execution domain after the Connector transaction commits.
func ListRunIDsWithInvalidConnectorSnapshots(
	ctx context.Context,
	q *generated.Queries,
	change ConnectorRunChange,
) ([]uuid.UUID, error) {
	if q == nil || !util.ValidUUID(change.CompanyID) || !validConnectorProvider(change.Provider) {
		return nil, ErrInvalidInput
	}
	runtimeID := uuid.NullUUID{}
	connectorID := uuid.NullUUID{}
	switch change.Type {
	case ConnectorRunChangeDiscovery:
		if change.RuntimeID == nil || !util.ValidUUID(*change.RuntimeID) || change.ConnectorID != nil {
			return nil, ErrInvalidInput
		}
		runtimeID = uuid.NullUUID{UUID: *change.RuntimeID, Valid: true}
	case ConnectorRunChangePolicy:
		if change.ConnectorID == nil || !util.ValidUUID(*change.ConnectorID) || change.RuntimeID != nil {
			return nil, ErrInvalidInput
		}
		connectorID = uuid.NullUUID{UUID: *change.ConnectorID, Valid: true}
	default:
		return nil, ErrInvalidInput
	}
	runIDs, err := q.ListRunIDsWithInvalidConnectorSnapshots(
		ctx,
		generated.ListRunIDsWithInvalidConnectorSnapshotsParams{
			CompanyID: change.CompanyID, Provider: string(change.Provider),
			RuntimeID: runtimeID, ConnectorID: connectorID,
			DiscoveryStaleAfterMilliseconds: connectorDiscoveryStaleAfter.Milliseconds(),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list Runs with invalid Connector snapshots: %w", err)
	}
	return runIDs, nil
}

func connectorItemFromRow(
	row generated.ConnectorCatalogItem,
	desiredEnabled bool,
	policyVersion int64,
	reviewRequired bool,
	executionProofFresh bool,
) ConnectorItemV1 {
	provider := ConnectorProvider(row.Provider)
	source := ConnectorSource(row.Source)
	status := ConnectorStatus(row.Status)
	return ConnectorItemV1{
		Action: ConnectorAction(row.Action), CanExecute: row.CanExecute,
		Category: textPointer(row.Category), Description: textPointer(row.Description),
		DesiredEnabled: desiredEnabled,
		EffectiveEnabled: desiredEnabled && !reviewRequired && executionProofFresh &&
			selectiveConnectorSource(provider, source) && status == ConnectorStatusConnected &&
			row.CanExecute,
		IconURL: textPointer(row.IconUrl), ID: row.ID, Name: row.Name, NativeID: row.NativeID,
		InstallCount: int64Pointer(row.InstallCount),
		ObservedAt:   protocol.FormatWireTime(row.ObservedAt.Time), Provider: provider,
		ReviewRequired: reviewRequired,
		Revision:       policyVersion, Source: source, Status: status,
	}
}

type connectorSelectedProjection struct {
	Name           string
	HasObservation bool
	Description    pgtype.Text
	Category       pgtype.Text
	IconURL        pgtype.Text
	InstallCount   pgtype.Int8
	Status         string
	Action         string
	CanExecute     bool
	ObservedAt     pgtype.Timestamptz
}

func connectorItemFromProjection(
	catalog generated.ConnectorCatalogItem,
	desiredEnabled bool,
	policyVersion int64,
	reviewRequired bool,
	executionProofFresh bool,
	selected connectorSelectedProjection,
) ConnectorItemV1 {
	catalog.Name = selected.Name
	if selected.HasObservation {
		catalog.Description = selected.Description
		catalog.Category = selected.Category
		catalog.IconUrl = selected.IconURL
		catalog.InstallCount = selected.InstallCount
	}
	catalog.Status = selected.Status
	catalog.Action = selected.Action
	catalog.CanExecute = selected.CanExecute
	catalog.ObservedAt = selected.ObservedAt
	return connectorItemFromRow(
		catalog,
		desiredEnabled,
		policyVersion,
		reviewRequired,
		executionProofFresh,
	)
}

func textPointer(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func int64Pointer(value pgtype.Int8) *int64 {
	if !value.Valid {
		return nil
	}
	return &value.Int64
}

type connectorDiscoveryBatchItem struct {
	Source         ConnectorSource               `json:"source"`
	NativeID       string                        `json:"nativeId"`
	Name           string                        `json:"name"`
	Description    *string                       `json:"description"`
	Category       *string                       `json:"category"`
	IconURL        *string                       `json:"iconUrl"`
	InstallCount   *int64                        `json:"installCount"`
	Status         ConnectorStatus               `json:"status"`
	Action         ConnectorAction               `json:"action"`
	CanExecute     bool                          `json:"canExecute"`
	ContractDigest string                        `json:"contractDigest,omitempty"`
	ToolNamespace  []provideragent.ConnectorTool `json:"toolNamespace,omitempty"`
}

func encodeConnectorDiscoveryBatch(items []ConnectorObservationInput) (json.RawMessage, error) {
	batch := make([]connectorDiscoveryBatchItem, len(items))
	for index, item := range items {
		batch[index] = connectorDiscoveryBatchItem{
			Source: item.Source, NativeID: item.NativeID, Name: item.Name,
			Description: item.Description, Category: item.Category, IconURL: item.IconURL,
			InstallCount: item.InstallCount,
			Status:       item.Status, Action: item.Action, CanExecute: item.CanExecute,
			ContractDigest: item.ContractDigest, ToolNamespace: item.Tools,
		}
	}
	encoded, err := json.Marshal(batch)
	if err != nil {
		return nil, fmt.Errorf("encode Connector discovery generation: %w", err)
	}
	return encoded, nil
}

// connectorIdentityColumns splits identities into the two parallel arrays the
// resolve query unnests. One loop fills both, so they share length and order.
func connectorIdentityColumns(identities []ConnectorIdentityInput) (sources, nativeIDs []string) {
	sources = make([]string, len(identities))
	nativeIDs = make([]string, len(identities))
	for index, identity := range identities {
		sources[index], nativeIDs[index] = string(identity.Source), identity.NativeID
	}
	return sources, nativeIDs
}
