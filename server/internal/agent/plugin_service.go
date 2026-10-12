package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

// PluginService owns the server-side presentation catalog for provider
// plugins. Plugin state never grants connector execution authority.
type PluginService interface {
	PublishDiscovery(
		context.Context,
		auth.UserPrincipal,
		PluginDiscoveryInput,
	) (PluginDiscoveryResult, error)
	List(context.Context, auth.UserPrincipal, PluginListInput) (PluginCatalogResult, error)
	Get(context.Context, auth.UserPrincipal, PluginDetailInput) (PluginDetailResult, error)
	Refresh(context.Context, auth.UserPrincipal) error
	Install(context.Context, auth.UserPrincipal, uuid.UUID) error
	Uninstall(context.Context, auth.UserPrincipal, uuid.UUID) error
}

var _ PluginService = (*DefaultPluginService)(nil)

type DefaultPluginService struct {
	store *db.Store
	bus   *events.Bus
}

func NewPluginService(store *db.Store, bus *events.Bus) (*DefaultPluginService, error) {
	if store == nil || bus == nil {
		return nil, errors.New("Plugin database store and event bus are required")
	}
	return &DefaultPluginService{store: store, bus: bus}, nil
}

func (s *DefaultPluginService) PublishDiscovery(
	ctx context.Context,
	principal auth.UserPrincipal,
	input PluginDiscoveryInput,
) (PluginDiscoveryResult, error) {
	if !util.ValidUUID(principal.UserID) || !util.ValidUUID(principal.CompanyID) {
		return PluginDiscoveryResult{}, ErrUnauthenticated
	}
	if principal.CompanyID != input.CompanyID {
		return PluginDiscoveryResult{}, ErrRuntimeNotFound
	}

	result := PluginDiscoveryResult{}
	catalogChanged := false
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
		normalized, err := normalizePluginDiscovery(provider, input, authority.ServerTime.Time)
		if err != nil {
			return err
		}
		current, err := tx.GetPluginDiscoveryGenerationForUpdate(ctx, normalized.RuntimeID)
		catalogChanged = err != nil || current.ManifestDigest != normalized.ManifestDigest ||
			current.ObservedAt.Time.Before(authority.ServerTime.Time.Add(-pluginDiscoveryStaleAfter))
		switch {
		case err == nil && current.Generation == normalized.Generation &&
			current.ManifestDigest == normalized.ManifestDigest:
			return nil
		case err == nil && !normalized.ObservedAt.After(current.ObservedAt.Time):
			return ErrPluginDiscoveryConflict
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("read Plugin discovery generation: %w", err)
		}
		items, err := encodePluginDiscoveryBatch(normalized.Items)
		if err != nil {
			return err
		}
		applied, err := tx.ApplyPluginDiscoveryGeneration(
			ctx,
			generated.ApplyPluginDiscoveryGenerationParams{
				Items:          items,
				CompanyID:      normalized.CompanyID,
				Provider:       string(provider),
				RuntimeID:      normalized.RuntimeID,
				Generation:     normalized.Generation,
				ObservedAt:     pgtype.Timestamptz{Time: normalized.ObservedAt, Valid: true},
				ManifestDigest: normalized.ManifestDigest,
			},
		)
		if err != nil {
			return fmt.Errorf("apply Plugin discovery generation: %w", err)
		}
		itemCount := int64(len(normalized.Items))
		if applied.InputCount != itemCount || applied.CatalogCount != itemCount ||
			applied.ObservationCount != itemCount || applied.GenerationCount != 1 {
			return errors.New("applied Plugin discovery generation is incomplete")
		}
		result.Applied = true
		return nil
	})
	if err != nil {
		return PluginDiscoveryResult{}, err
	}
	if result.Applied && catalogChanged {
		publishCapabilitiesChanged(ctx, s.bus, input.CompanyID, principal.UserID, input.RuntimeID, "plugin")
	}
	return result, nil
}

func (s *DefaultPluginService) List(
	ctx context.Context,
	principal auth.UserPrincipal,
	input PluginListInput,
) (PluginCatalogResult, error) {
	if !util.ValidUUID(principal.UserID) || !util.ValidUUID(principal.CompanyID) {
		return PluginCatalogResult{}, ErrUnauthenticated
	}
	// A read, and the context query already restricts it to a runtime the
	// caller registered (ADR 0014): any active member reads their own
	// machine's catalog — it is what the Agent capabilities view renders for
	// them — while changing it is a plugins.manage write, which every role
	// holds for its own computers (owner decision, 2026-09-29).
	normalized, err := normalizePluginListInput(input)
	if err != nil {
		return PluginCatalogResult{}, err
	}
	pluginContext, err := companyPluginContext(
		ctx, s.store.Queries(), principal, normalized.Provider, normalized.RuntimeID, false,
	)
	if err != nil {
		return PluginCatalogResult{}, err
	}
	provider := ConnectorProvider(pluginContext.Provider)
	selectedRuntimeID := uuid.NullUUID{UUID: pluginContext.RuntimeID, Valid: true}
	params := generated.ListCompanyPluginCatalogParams{
		ActorRoles:        auth.AllRoles(),
		ActorUserID:       principal.UserID,
		SelectedRuntimeID: selectedRuntimeID,
		CompanyID:         principal.CompanyID,
		Provider:          string(provider),
		SearchQuery:       normalized.Query,
		Category:          normalized.Category,
		Status:            string(normalized.Status),
		FeaturedOnly:      normalized.Featured,
		PageSize:          int32(normalized.Limit + 1),
	}
	if normalized.Cursor != nil {
		params.HasCursor = true
		params.AfterRank = int32(normalized.Cursor.Rank)
		params.AfterID = normalized.Cursor.ID
	}
	rows, err := s.store.Queries().ListCompanyPluginCatalog(ctx, params)
	if err != nil {
		return PluginCatalogResult{}, fmt.Errorf("list Company Plugin catalog: %w", err)
	}
	hasMore := len(rows) > normalized.Limit
	if hasMore {
		rows = rows[:normalized.Limit]
	}
	plugins := make([]PluginItemV1, len(rows))
	publishers := make([]pluginPublisher, len(rows))
	for index, row := range rows {
		item, err := pluginItemFromRow(provider, row)
		if err != nil {
			return PluginCatalogResult{}, err
		}
		plugins[index] = item
		publishers[index] = pluginPublisher{
			websiteHost: textPointer(row.WebsiteHost), developerName: textPointer(row.DeveloperName),
		}
	}
	if err := choosePluginLogos(
		ctx, s.store.Queries(), principal, pluginContext.RuntimeID, plugins, publishers, nil,
	); err != nil {
		return PluginCatalogResult{}, err
	}
	var nextCursor *string
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		encoded, err := FormatPluginCursor(PluginCursor{Rank: int(last.CatalogRank), ID: last.ID})
		if err != nil {
			return PluginCatalogResult{}, fmt.Errorf("format Plugin cursor: %w", err)
		}
		nextCursor = &encoded
	}
	countParams := generated.CountCompanyPluginCatalogParams{
		ActorRoles:        auth.AllRoles(),
		ActorUserID:       principal.UserID,
		SelectedRuntimeID: selectedRuntimeID,
		CompanyID:         principal.CompanyID,
		Provider:          string(provider),
	}
	totalCount, err := s.store.Queries().CountCompanyPluginCatalog(ctx, countParams)
	if err != nil {
		return PluginCatalogResult{}, fmt.Errorf("count Company Plugin catalog: %w", err)
	}
	installedCount, err := s.store.Queries().CountCompanyInstalledPlugins(
		ctx,
		generated.CountCompanyInstalledPluginsParams{
			ActorRoles:        auth.AllRoles(),
			ActorUserID:       principal.UserID,
			SelectedRuntimeID: selectedRuntimeID,
			CompanyID:         principal.CompanyID,
			Provider:          string(provider),
		},
	)
	if err != nil {
		return PluginCatalogResult{}, fmt.Errorf("count installed Company Plugins: %w", err)
	}
	categoryRows, err := s.store.Queries().CountCompanyPluginsByCategory(
		ctx,
		generated.CountCompanyPluginsByCategoryParams{
			ActorRoles:        auth.AllRoles(),
			ActorUserID:       principal.UserID,
			SelectedRuntimeID: selectedRuntimeID,
			CompanyID:         principal.CompanyID,
			Provider:          string(provider),
		},
	)
	if err != nil {
		return PluginCatalogResult{}, fmt.Errorf("count Company Plugins by category: %w", err)
	}
	countsByCategory := make(map[connectorCategory]int64, len(categoryRows))
	for _, row := range categoryRows {
		countsByCategory[canonicalConnectorCategoryLabel(row.Category)] += row.Count
	}
	categoryCounts := make([]PluginCategoryCount, 0, len(categoryRows))
	for _, category := range connectorCategoryOrder {
		if count, present := countsByCategory[category]; present {
			categoryCounts = append(categoryCounts, PluginCategoryCount{
				Category: string(category),
				Count:    count,
			})
		}
	}
	return PluginCatalogResult{
		SchemaVersion:  PluginSchemaVersion,
		ActiveProvider: provider,
		Runtime: &PluginRuntimeSummary{
			ID:     pluginContext.RuntimeID,
			Name:   pluginContext.RuntimeName,
			Online: pluginContext.RuntimeOnline,
		},
		Plugins:        plugins,
		NextCursor:     nextCursor,
		TotalCount:     totalCount,
		InstalledCount: installedCount,
		CategoryCounts: categoryCounts,
		Stale:          pluginContext.Stale,
		ObservedAt:     util.TimestampToTime(pluginContext.DiscoveryObservedAt),
	}, nil
}

// Get reads one Plugin's page from one Runtime's current observation: the
// catalog row, the Codex presentation, its apps resolved against that
// Runtime's Connector observations, and the Agents the viewer has bound there
// that can use it. Like the catalog it is a member read of one of the
// viewer's own Runtimes (ADR 0014), selected as the catalog selects one for
// the Plugin's provider;
// another Company's Plugin, or one that Runtime has not observed, is absent.
// A duplicate the catalog leaves out is still read, so a link to it works.
func (s *DefaultPluginService) Get(
	ctx context.Context,
	principal auth.UserPrincipal,
	input PluginDetailInput,
) (PluginDetailResult, error) {
	if !util.ValidUUID(principal.UserID) || !util.ValidUUID(principal.CompanyID) {
		return PluginDetailResult{}, ErrUnauthenticated
	}
	if input.RuntimeID != nil && !util.ValidUUID(*input.RuntimeID) {
		return PluginDetailResult{}, ErrInvalidInput
	}
	if !util.ValidUUID(input.PluginID) {
		return PluginDetailResult{}, ErrPluginNotFound
	}
	var result PluginDetailResult
	// One snapshot answers every read, so a publication that commits between
	// them cannot pair one generation's Plugin with another's apps.
	err := s.store.WithinTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(q *generated.Queries) error {
			provider, err := q.GetCompanyPluginProvider(ctx, generated.GetCompanyPluginProviderParams{
				PluginID: input.PluginID, CompanyID: principal.CompanyID,
			})
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrPluginNotFound
			}
			if err != nil {
				return fmt.Errorf("read Company Plugin provider: %w", err)
			}
			pluginContext, err := companyPluginContext(
				ctx, q, principal, ConnectorProvider(provider), input.RuntimeID, false,
			)
			// A plugin page names no computer of the caller's for this
			// Plugin: whichever way, the page has nothing to show.
			if errors.Is(err, ErrPluginProviderUnavailable) || errors.Is(err, ErrRuntimeNotFound) {
				return ErrPluginNotFound
			}
			if err != nil {
				return err
			}
			row, err := q.GetCompanyPluginDetailForOwner(
				ctx,
				generated.GetCompanyPluginDetailForOwnerParams{
					SelectedRuntimeID: pluginContext.RuntimeID,
					ActorUserID:       principal.UserID,
					ActorRoles:        auth.AllRoles(),
					PluginID:          input.PluginID,
					CompanyID:         principal.CompanyID,
					Provider:          provider,
				},
			)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrPluginNotFound
			}
			if err != nil {
				return fmt.Errorf("read Company Plugin detail: %w", err)
			}
			result, err = pluginDetailFromRow(ConnectorProvider(provider), pluginContext, row)
			if err != nil {
				return err
			}
			if result.Apps, err = readPluginApps(
				ctx, q, principal.CompanyID, pluginContext.RuntimeID, result.Plugin,
			); err != nil {
				return err
			}
			uses, err := q.ListPluginAgentUsesForRuntime(ctx, generated.ListPluginAgentUsesForRuntimeParams{
				PluginID: input.PluginID, CompanyID: principal.CompanyID, RuntimeID: pluginContext.RuntimeID,
				ViewerUserID: principal.UserID,
			})
			if err != nil {
				return fmt.Errorf("read Agents using Plugin: %w", err)
			}
			result.UsedBy = make([]PluginAgentUse, len(uses))
			for index, use := range uses {
				result.UsedBy[index] = PluginAgentUse{AgentID: use.ID, Name: use.Name}
			}
			plugins := []PluginItemV1{result.Plugin}
			publishers := []pluginPublisher{{
				websiteHost:   textPointer(row.PluginRuntimeObservation.WebsiteHost),
				developerName: result.DeveloperName,
			}}
			if err := choosePluginLogos(
				ctx, q, principal, pluginContext.RuntimeID, plugins, publishers, result.Apps,
			); err != nil {
				return err
			}
			result.Plugin = plugins[0]
			return nil
		},
	)
	if err != nil {
		return PluginDetailResult{}, err
	}
	return result, nil
}

func pluginDetailFromRow(
	provider ConnectorProvider,
	pluginContext generated.GetCompanyPluginContextForOwnerRow,
	row generated.GetCompanyPluginDetailForOwnerRow,
) (PluginDetailResult, error) {
	observation := row.PluginRuntimeObservation
	item, err := pluginItemFromRow(provider, generated.ListCompanyPluginCatalogRow{
		ID: observation.PluginID, NativeID: row.PluginNativeID, Name: observation.Name,
		Description: observation.Description, Category: observation.Category,
		IconUrl: observation.IconUrl, ShareUrl: observation.ShareUrl, Status: observation.Status,
		Installed: observation.Installed, Enabled: observation.Enabled,
		Featured: observation.Featured, Action: observation.Action,
		CatalogRank: observation.CatalogRank, AppCount: observation.AppCount,
		McpServerCount: observation.McpServerCount, SkillCount: observation.SkillCount,
		ConnectorIdentities: observation.ConnectorIdentities,
		InstallCount:        observation.InstallCount, ObservedAt: observation.ObservedAt,
	})
	if err != nil {
		return PluginDetailResult{}, err
	}
	prompts := []string{}
	// Trusted structured product state, validated before persistence.
	_ = json.Unmarshal(observation.DefaultPrompts, &prompts)
	if prompts == nil {
		prompts = []string{}
	}
	return PluginDetailResult{
		SchemaVersion: PluginSchemaVersion,
		Plugin:        item,
		Runtime: &PluginRuntimeSummary{
			ID:     pluginContext.RuntimeID,
			Name:   pluginContext.RuntimeName,
			Online: pluginContext.RuntimeOnline,
		},
		Stale:             pluginContext.Stale,
		LongDescription:   textPointer(observation.LongDescription),
		DefaultPrompts:    prompts,
		BrandColor:        textPointer(observation.BrandColor),
		DeveloperName:     textPointer(observation.DeveloperName),
		WebsiteURL:        textPointer(observation.WebsiteUrl),
		PrivacyPolicyURL:  textPointer(observation.PrivacyPolicyUrl),
		TermsOfServiceURL: textPointer(observation.TermsOfServiceUrl),
		Apps:              []PluginDetailApp{},
		UsedBy:            []PluginAgentUse{},
	}, nil
}

// readPluginApps resolves a Plugin's Connector identities on one Runtime,
// ordered by name for its page.
func readPluginApps(
	ctx context.Context,
	q *generated.Queries,
	companyID uuid.UUID,
	runtimeID uuid.UUID,
	plugin PluginItemV1,
) ([]PluginDetailApp, error) {
	apps := make([]PluginDetailApp, 0, len(plugin.ConnectorIdentities))
	if len(plugin.ConnectorIdentities) == 0 {
		return apps, nil
	}
	sources := make([]string, len(plugin.ConnectorIdentities))
	nativeIDs := make([]string, len(plugin.ConnectorIdentities))
	for index, identity := range plugin.ConnectorIdentities {
		sources[index], nativeIDs[index] = string(identity.Source), identity.NativeID
	}
	rows, err := q.ListPluginAppsForRuntime(ctx, generated.ListPluginAppsForRuntimeParams{
		Sources: sources, NativeIds: nativeIDs, CompanyID: companyID,
		Provider: string(plugin.Provider), RuntimeID: runtimeID,
	})
	if err != nil {
		return nil, fmt.Errorf("read Plugin apps: %w", err)
	}
	for _, row := range rows {
		// Every Claude plugin names itself as a Connector identity, but only
		// one that brings an MCP server or is an official connector is ever a
		// Connector; the rest have no app to list.
		if row.Source == string(ConnectorSourceClaudePlugin) && !row.Cataloged {
			continue
		}
		app := PluginDetailApp{
			Source: ConnectorSource(row.Source), NativeID: row.NativeID, Name: row.Name,
			Description: textPointer(row.Description), IconURL: textPointer(row.IconUrl),
		}
		if row.Observed && row.ConnectorID.Valid {
			connectorID := row.ConnectorID.UUID
			status, action := ConnectorStatus(row.Status), ConnectorAction(row.Action)
			app.ConnectorID, app.Status, app.Action = &connectorID, &status, &action
		}
		apps = append(apps, app)
	}
	slices.SortStableFunc(apps, func(left, right PluginDetailApp) int {
		return strings.Compare(strings.ToLower(left.Name), strings.ToLower(right.Name))
	})
	return apps, nil
}

// Refresh asks the caller's own runtime for its list again. The computer is
// the caller's (ADR 0014), so it needs no plugins.manage. The cloud cannot
// reach the machine; the desktop asks its own daemon instead.
func (s *DefaultPluginService) Refresh(
	ctx context.Context,
	principal auth.UserPrincipal,
) error {
	if _, err := s.companyContext(ctx, principal, "", nil, false); err != nil {
		return err
	}
	return ErrPluginActionUnavailable
}

func (s *DefaultPluginService) Install(
	ctx context.Context,
	principal auth.UserPrincipal,
	pluginID uuid.UUID,
) error {
	return s.validateAction(ctx, principal, pluginID, PluginActionInstall)
}

func (s *DefaultPluginService) Uninstall(
	ctx context.Context,
	principal auth.UserPrincipal,
	pluginID uuid.UUID,
) error {
	return s.validateAction(ctx, principal, pluginID, PluginActionUninstall)
}

// companyPluginContext resolves the caller's own runtime the catalog is read
// or changed against, through q so a read can share its snapshot. manage is
// true on install and uninstall, which take the plugins.manage permission —
// every role's, since they reach only the caller's own computer (D28) — and
// a read or a refresh needs membership alone (ADR 0014).
func companyPluginContext(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	explicitProvider ConnectorProvider,
	explicitRuntimeID *uuid.UUID,
	manage bool,
) (generated.GetCompanyPluginContextForOwnerRow, error) {
	if !util.ValidUUID(principal.UserID) || !util.ValidUUID(principal.CompanyID) {
		return generated.GetCompanyPluginContextForOwnerRow{}, ErrUnauthenticated
	}
	if manage && !principal.Can(auth.PermissionPluginsManage) {
		return generated.GetCompanyPluginContextForOwnerRow{}, ErrForbidden
	}
	row, err := q.GetCompanyPluginContextForOwner(
		ctx,
		generated.GetCompanyPluginContextForOwnerParams{
			DiscoveryStaleAfterMilliseconds: pluginDiscoveryStaleAfter.Milliseconds(),
			CompanyID:                       principal.CompanyID,
			ActorUserID:                     principal.UserID,
			RuntimeStaleAfterMilliseconds:   RuntimeStaleThreshold.Milliseconds(),
			ExplicitProvider:                string(explicitProvider),
			ExplicitRuntimeID:               util.NullUUIDFromPtr(explicitRuntimeID),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return generated.GetCompanyPluginContextForOwnerRow{}, catalogRuntimeMissing(
			ctx, q, principal, explicitRuntimeID, ErrPluginProviderUnavailable,
		)
	}
	if err != nil {
		return generated.GetCompanyPluginContextForOwnerRow{},
			fmt.Errorf("read Company Plugin context: %w", err)
	}
	return row, nil
}

// companyContext is companyPluginContext on the service's own queries.
func (s *DefaultPluginService) companyContext(
	ctx context.Context,
	principal auth.UserPrincipal,
	explicitProvider ConnectorProvider,
	explicitRuntimeID *uuid.UUID,
	manage bool,
) (generated.GetCompanyPluginContextForOwnerRow, error) {
	return companyPluginContext(
		ctx, s.store.Queries(), principal, explicitProvider, explicitRuntimeID, manage,
	)
}

func (s *DefaultPluginService) validateAction(
	ctx context.Context,
	principal auth.UserPrincipal,
	pluginID uuid.UUID,
	action PluginAction,
) error {
	if !util.ValidUUID(pluginID) {
		return ErrPluginNotFound
	}
	pluginContext, err := companyPluginContext(ctx, s.store.Queries(), principal, "", nil, true)
	if err != nil {
		return err
	}
	if !pluginContext.RuntimeOnline {
		return ErrPluginActionUnavailable
	}
	row, err := s.store.Queries().GetSelectedCompanyPluginForOwner(
		ctx,
		generated.GetSelectedCompanyPluginForOwnerParams{
			ActorRoles:        auth.RolesWith(auth.PermissionPluginsManage),
			SelectedRuntimeID: pluginContext.RuntimeID,
			ActorUserID:       principal.UserID,
			PluginID:          pluginID,
			CompanyID:         principal.CompanyID,
			Provider:          pluginContext.Provider,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrPluginNotFound
	}
	if err != nil {
		return fmt.Errorf("read selected Company Plugin: %w", err)
	}
	if PluginAction(row.Action) != action {
		return ErrPluginActionUnavailable
	}
	// Installation and removal happen in the daemon's machine-local provider
	// adapter. The cloud API deliberately does not pretend the action ran.
	return ErrPluginActionUnavailable
}

func pluginItemFromRow(
	provider ConnectorProvider,
	row generated.ListCompanyPluginCatalogRow,
) (PluginItemV1, error) {
	connectorIdentities, err := decodePluginConnectorIdentities(row.ConnectorIdentities)
	if err != nil {
		return PluginItemV1{}, fmt.Errorf("decode stored Plugin connector identities: %w", err)
	}
	return PluginItemV1{
		ID: row.ID, Provider: provider, NativeID: row.NativeID, Name: row.Name,
		Description: textPointer(row.Description), Category: pluginTextPointer(row.Category),
		IconURL: textPointer(row.IconUrl), ShareURL: textPointer(row.ShareUrl),
		Status: PluginStatus(row.Status), Installed: row.Installed, Enabled: row.Enabled,
		Featured: row.Featured, Action: PluginAction(row.Action),
		ComponentCounts: PluginComponentCounts{
			Apps: int(row.AppCount), MCPServers: int(row.McpServerCount), Skills: int(row.SkillCount),
		},
		ConnectorIdentities: connectorIdentities,
		InstallCount:        int64Pointer(row.InstallCount),
		ObservedAt:          protocol.FormatWireTime(row.ObservedAt.Time),
	}, nil
}

func decodePluginConnectorIdentities(raw json.RawMessage) ([]PluginConnectorIdentity, error) {
	var identities []PluginConnectorIdentity
	if err := json.Unmarshal(raw, &identities); err != nil {
		return nil, err
	}
	if identities == nil {
		return []PluginConnectorIdentity{}, nil
	}
	return identities, nil
}

func pluginTextPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

type pluginDiscoveryBatchItem struct {
	NativeID            string                    `json:"nativeId"`
	Name                string                    `json:"name"`
	Description         *string                   `json:"description"`
	Category            string                    `json:"category"`
	IconURL             *string                   `json:"iconUrl"`
	ShareURL            *string                   `json:"shareUrl"`
	Status              PluginStatus              `json:"status"`
	Installed           bool                      `json:"installed"`
	Enabled             bool                      `json:"enabled"`
	Featured            bool                      `json:"featured"`
	Action              PluginAction              `json:"action"`
	CatalogRank         int                       `json:"catalogRank"`
	AppCount            int                       `json:"appCount"`
	MCPServerCount      int                       `json:"mcpServerCount"`
	SkillCount          int                       `json:"skillCount"`
	ConnectorIdentities []PluginConnectorIdentity `json:"connectorIdentities"`
	InstallCount        *int64                    `json:"installCount"`
	LongDescription     *string                   `json:"longDescription"`
	DefaultPrompts      []string                  `json:"defaultPrompts"`
	BrandColor          *string                   `json:"brandColor"`
	DeveloperName       *string                   `json:"developerName"`
	WebsiteURL          *string                   `json:"websiteUrl"`
	PrivacyPolicyURL    *string                   `json:"privacyPolicyUrl"`
	TermsOfServiceURL   *string                   `json:"termsOfServiceUrl"`
}

func encodePluginDiscoveryBatch(items []PluginObservationInput) (json.RawMessage, error) {
	batch := make([]pluginDiscoveryBatchItem, len(items))
	for index, item := range items {
		batch[index] = pluginDiscoveryBatchItem{
			NativeID: item.NativeID, Name: item.Name,
			Description: pluginTextPointer(item.Description), Category: item.Category,
			IconURL: pluginTextPointer(item.IconURL), ShareURL: pluginTextPointer(item.ShareURL),
			Status: item.Status, Installed: item.Installed, Enabled: item.Enabled,
			Featured: item.Featured, Action: item.Action, CatalogRank: item.CatalogRank,
			AppCount:            item.ComponentCounts.Apps,
			MCPServerCount:      item.ComponentCounts.MCPServers,
			SkillCount:          item.ComponentCounts.Skills,
			ConnectorIdentities: item.ConnectorIdentities,
			InstallCount:        item.InstallCount,
			LongDescription:     pluginTextPointer(item.LongDescription),
			DefaultPrompts:      item.DefaultPrompts,
			BrandColor:          pluginTextPointer(item.BrandColor),
			DeveloperName:       pluginTextPointer(item.DeveloperName),
			WebsiteURL:          pluginTextPointer(item.WebsiteURL),
			PrivacyPolicyURL:    pluginTextPointer(item.PrivacyPolicyURL),
			TermsOfServiceURL:   pluginTextPointer(item.TermsOfServiceURL),
		}
		if batch[index].DefaultPrompts == nil {
			batch[index].DefaultPrompts = []string{}
		}
	}
	encoded, err := json.Marshal(batch)
	if err != nil {
		return nil, fmt.Errorf("encode Plugin discovery generation: %w", err)
	}
	return encoded, nil
}
