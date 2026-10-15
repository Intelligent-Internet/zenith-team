package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"boardly.dev/boardly/server/internal/agent/connectorselection"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/util"
	"boardly.dev/boardly/server/pkg/agent"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

// ConnectorAppType names an external app a Workflow works best with. Its
// values are the WorkflowConnectorType enum of docs/api/spec/workflows.yaml,
// the same closed set internal/workflow parses without importing this
// package. Readiness resolves an app to exact provider identities through
// connectorAppPins, never by display name: names are not unique ("Linear" and
// the unrelated "LINEAR") and substrings catch third-party apps.
type ConnectorAppType string

var connectorAppTypes = []ConnectorAppType{
	"gmail", "google_calendar", "google_drive", "slack", "notion", "linear", "jira", "asana",
	"clickup", "github", "hubspot", "salesforce", "apollo", "gong", "fireflies", "zendesk",
	"stripe", "shopify", "xero", "linkedin", "figma", "firecrawl", "ashby", "typeform",
}

// ConnectorAppState is whether one app reaches a Run of one Agent now.
type ConnectorAppState string

const (
	ConnectorAppStateReady            ConnectorAppState = "ready"
	ConnectorAppStatePending          ConnectorAppState = "pending"
	ConnectorAppStateNeedsConnection  ConnectorAppState = "needs_connection"
	ConnectorAppStateReviewRequired   ConnectorAppState = "review_required"
	ConnectorAppStateDisabledForAgent ConnectorAppState = "disabled_for_agent"
	ConnectorAppStateProviderManaged  ConnectorAppState = "provider_managed"
	ConnectorAppStateNotOffered       ConnectorAppState = "not_offered"
	ConnectorAppStateUnknown          ConnectorAppState = "unknown"
)

// connectorAppStatePrecedence ranks what an app can be when several pinned
// identities resolve it: the most usable answer wins, and a tie keeps the
// pin's preference order. `unknown` is never ranked; it is the answer for a
// Runtime whose discovery cannot be read at all.
var connectorAppStatePrecedence = []ConnectorAppState{
	ConnectorAppStateReady,
	ConnectorAppStatePending,
	ConnectorAppStateDisabledForAgent,
	ConnectorAppStateReviewRequired,
	ConnectorAppStateNeedsConnection,
	ConnectorAppStateProviderManaged,
	ConnectorAppStateNotOffered,
}

// ConnectorDiscoveryState is the standing of the discovery of the Runtime one
// viewer's binding of an Agent names.
type ConnectorDiscoveryState string

const (
	ConnectorDiscoveryStateFresh   ConnectorDiscoveryState = "fresh"
	ConnectorDiscoveryStateStale   ConnectorDiscoveryState = "stale"
	ConnectorDiscoveryStateMissing ConnectorDiscoveryState = "missing"
	ConnectorDiscoveryStateUnbound ConnectorDiscoveryState = "unbound"
)

// connectorAppPin lists, in preference order, the exact identities that
// resolve one app on one provider: the Connectors a Run can use and the
// Plugin packages that install them. An installed pinned Plugin also
// contributes its own Connector identities, so an app still resolves when the
// provider re-issues its id. Every id here was verified against a recorded
// discovery catalog; an app without a verified id has no pin and reports
// not_offered rather than guessing.
type connectorAppPin struct {
	Connectors []ConnectorIdentityInput
	Plugins    []string
}

func codexAppPin(plugin string, nativeIDs ...string) connectorAppPin {
	pin := connectorAppPin{}
	for _, nativeID := range nativeIDs {
		pin.Connectors = append(pin.Connectors, ConnectorIdentityInput{
			Source: ConnectorSourceCodexApp, NativeID: nativeID,
		})
	}
	if plugin != "" {
		pin.Plugins = []string{plugin}
	}
	return pin
}

// claudePluginPin pins an official Claude plugin, which is both the
// installable package and, once installed, the claude_plugin Connector of the
// same native id. A Claude.ai cloud connector for the same app, when one
// exists, is the lower preference and always reports provider_managed.
func claudePluginPin(plugin, cloud string) connectorAppPin {
	pin := connectorAppPin{
		Connectors: []ConnectorIdentityInput{{Source: ConnectorSourceClaudePlugin, NativeID: plugin}},
		Plugins:    []string{plugin},
	}
	if cloud != "" {
		pin.Connectors = append(pin.Connectors, ConnectorIdentityInput{
			Source: ConnectorSourceClaudeCloud, NativeID: cloud,
		})
	}
	return pin
}

func claudeCloudPin(cloud string) connectorAppPin {
	return connectorAppPin{Connectors: []ConnectorIdentityInput{{
		Source: ConnectorSourceClaudeCloud, NativeID: cloud,
	}}}
}

var connectorAppPins = map[ConnectorAppType]map[ConnectorProvider]connectorAppPin{
	"gmail": {
		ConnectorProviderCodex: codexAppPin(
			"gmail@openai-curated-remote", "connector_2128aebfecb84f64a069897515042a44"),
		ConnectorProviderClaude: claudeCloudPin("claude-cloud:gmail"),
	},
	"google_calendar": {
		ConnectorProviderCodex: codexAppPin(
			"google-calendar@openai-curated-remote", "connector_947e0d954944416db111db556030eea6"),
		ConnectorProviderClaude: claudeCloudPin("claude-cloud:google-calendar"),
	},
	"google_drive": {
		ConnectorProviderCodex: codexAppPin(
			"google-drive@openai-curated-remote", "connector_5f3c8c41a1e54ad7a76272c89e2554fa"),
		ConnectorProviderClaude: claudeCloudPin("claude-cloud:google-drive"),
	},
	"slack": {
		ConnectorProviderCodex: codexAppPin(
			"slack@openai-curated-remote", "asdk_app_69a1d78e929881919bba0dbda1f6436d"),
		ConnectorProviderClaude: claudePluginPin("slack@claude-plugins-official", "claude-cloud:slack"),
	},
	"notion": {
		ConnectorProviderCodex: codexAppPin(
			"notion@openai-curated-remote", "asdk_app_69c18c28f1188191bf5b8445c4ab0a2e"),
		ConnectorProviderClaude: claudePluginPin("notion@claude-plugins-official", ""),
	},
	"linear": {
		// Never asdk_app_6a8fb168…, the unrelated third-party "LINEAR".
		ConnectorProviderCodex: codexAppPin(
			"linear@openai-curated-remote", "asdk_app_69a089a326dc8191b32a3f2553f5be2c"),
		ConnectorProviderClaude: claudePluginPin("linear@claude-plugins-official", ""),
	},
	"jira": {
		// Atlassian Rovo first, then Atlassian Rovo (Legacy).
		ConnectorProviderCodex: codexAppPin(
			"atlassian-rovo@openai-curated-remote",
			"asdk_app_6a83901dde988191b3f3cefdcc19acfa",
			"connector_692de805e3ec8191834719067174a384",
		),
		ConnectorProviderClaude: claudePluginPin("atlassian@claude-plugins-official", ""),
	},
	"asana": {
		ConnectorProviderCodex: codexAppPin(
			"asana@openai-curated-remote", "asdk_app_69616780bd208191b4fb44ba44f72b61"),
		ConnectorProviderClaude: claudePluginPin("asana@claude-plugins-official", ""),
	},
	"clickup": {
		ConnectorProviderCodex: codexAppPin(
			"clickup@openai-curated-remote", "asdk_app_69431e6d26b88191b4029488aeb42f5b"),
	},
	"github": {
		ConnectorProviderCodex: codexAppPin(
			"github@openai-curated-remote", "connector_76869538009648d5b282a4bb21c3d157"),
		ConnectorProviderClaude: claudePluginPin("github@claude-plugins-official", "claude-cloud:github"),
	},
	"hubspot": {
		ConnectorProviderCodex: codexAppPin(
			"hubspot@openai-curated-remote", "asdk_app_697acb8e53d88191bf7a79e62012ae14"),
	},
	"salesforce": {
		ConnectorProviderCodex: codexAppPin("salesforce@openai-curated-remote"),
	},
	"apollo": {
		// Apollo.io.
		ConnectorProviderCodex: codexAppPin(
			"apollo@openai-curated-remote", "asdk_app_69bd664f2a908191a3a0a47eca8559d1"),
	},
	"gong": {
		ConnectorProviderCodex: codexAppPin("", "asdk_app_6a589d56de588191a5023a243c7bcfd4"),
	},
	"fireflies": {
		ConnectorProviderCodex: codexAppPin(
			"fireflies@openai-curated-remote", "connector_6912075cb358819187346bcafb601db8"),
	},
	"zendesk": {
		ConnectorProviderCodex: codexAppPin(
			"zendesk@openai-curated-remote", "connector_47d5e934d8f546b98f2d2db564ca04a3"),
	},
	"stripe": {
		ConnectorProviderCodex: codexAppPin(
			"stripe@openai-curated-remote", "asdk_app_6983c208e5f8819196b7511519f97993"),
		ConnectorProviderClaude: claudePluginPin("stripe@claude-plugins-official", ""),
	},
	"shopify": {
		ConnectorProviderCodex: codexAppPin(
			"shopify@openai-curated-remote", "asdk_app_69e65c430b3081919aa4d962ab5d1698"),
	},
	"xero": {
		ConnectorProviderCodex: codexAppPin("", "asdk_app_6a7ee05cee1881918dbfaba26fb555e1"),
	},
	"linkedin": {
		ConnectorProviderCodex: codexAppPin("", "asdk_app_69949aa62bf48191be5e57a01202beca"),
	},
	"figma": {
		ConnectorProviderCodex: codexAppPin(
			"figma@openai-curated-remote", "connector_68df038e0ba48191908c8434991bbac2"),
		ConnectorProviderClaude: claudePluginPin("figma@claude-plugins-official", ""),
	},
	"firecrawl": {
		ConnectorProviderCodex:  codexAppPin("", "asdk_app_6a314a73f8ac819195b0d55e36b9c609"),
		ConnectorProviderClaude: claudePluginPin("firecrawl@claude-plugins-official", ""),
	},
	"ashby": {
		ConnectorProviderCodex: codexAppPin("", "asdk_app_6a33192d4f9481919b05403bdae08b95"),
	},
	"typeform": {
		ConnectorProviderCodex: codexAppPin("", "asdk_app_6a3e889766b481919d6b2861e94a8afa"),
	},
}

type ConnectorAppReadinessInput struct {
	AgentID uuid.UUID
	Apps    []ConnectorAppType
}

type ConnectorAppDiscovery struct {
	State      ConnectorDiscoveryState `json:"state"`
	ObservedAt *string                 `json:"observedAt"`
}

// ConnectorAppConnector identifies the Connector behind one app: a holder of
// plugins.manage connects it on their own computer, and a holder of
// connectors.manage also reviews or adjusts it for the Company. ToolCount is
// the tools a Run can call from its current observation — a Codex app's
// enabled tools — and WriteToolCount those not read-only; both are 0 for an
// app that reports no tools, which includes every Claude source, whose
// namespace lists MCP servers.
type ConnectorAppConnector struct {
	ID             uuid.UUID       `json:"id"`
	Source         ConnectorSource `json:"source"`
	NativeID       string          `json:"nativeId"`
	Name           string          `json:"name"`
	Status         ConnectorStatus `json:"status"`
	Action         ConnectorAction `json:"action"`
	ToolCount      int             `json:"toolCount"`
	WriteToolCount int             `json:"writeToolCount"`
}

// ConnectorAppPlugin identifies the pinned Plugin package for one app.
type ConnectorAppPlugin struct {
	ID        uuid.UUID    `json:"id"`
	NativeID  string       `json:"nativeId"`
	Name      string       `json:"name"`
	Installed bool         `json:"installed"`
	Action    PluginAction `json:"action"`
}

// ConnectorAppReadiness is one app's answer. IconURL is its logo, visible to
// every member because it names nothing a member could act on.
type ConnectorAppReadiness struct {
	App       ConnectorAppType       `json:"app"`
	State     ConnectorAppState      `json:"state"`
	Connector *ConnectorAppConnector `json:"connector,omitempty"`
	Plugin    *ConnectorAppPlugin    `json:"plugin,omitempty"`
	IconURL   *string                `json:"iconUrl,omitempty"`
}

type ConnectorAppReadinessResult struct {
	AgentID  uuid.UUID
	Provider ConnectorProvider
	// RuntimeID is the Runtime of the viewer's binding of the Agent, absent
	// when the viewer has not set the Agent up.
	RuntimeID uuid.NullUUID
	Discovery ConnectorAppDiscovery
	Apps      []ConnectorAppReadiness
}

// ReadAppReadiness answers, for each app a Workflow works best with, whether
// it reaches a Run of this Agent the viewer starts now. Apps resolve on the
// Runtime of the viewer's own binding of the Agent (ADR 0014), where that Run
// executes, and its current discovery generation; `ready` is the claim
// freeze's per-Connector rule plus fresh discovery and no per-Agent denial.
// Any active member reads the states; one who has not set the Agent up has
// no binding and answers `unbound` with every app `unknown`, as a bound
// Runtime that is gone or never reported answers `missing`. The Connector and
// Plugin behind each app are attached for a holder of plugins.manage, who
// connects and installs them on their own computer, and the Connector also
// for a holder of connectors.manage, who reviews it; every role holds
// plugins.manage since the 2026-09-29 owner decision.
func (s *DefaultConnectorService) ReadAppReadiness(
	ctx context.Context,
	principal auth.UserPrincipal,
	input ConnectorAppReadinessInput,
) (ConnectorAppReadinessResult, error) {
	if !util.ValidUUID(principal.UserID) || !util.ValidUUID(principal.CompanyID) {
		return ConnectorAppReadinessResult{}, ErrUnauthenticated
	}
	if err := normalizeConnectorAppReadinessInput(input); err != nil {
		return ConnectorAppReadinessResult{}, err
	}
	var (
		result        ConnectorAppReadinessResult
		plugins       = make(map[string]generated.ListPluginsForRuntimeByNativeIDRow)
		appIdentities = make([][]ConnectorIdentityInput, len(input.Apps))
		connectors    = make(map[ConnectorIdentityInput]generated.ListConnectorAppReadinessRow)
		denial        connectorAppDenial
		logoFallbacks appLogoFallbacks
	)
	// One snapshot answers every read, so a publication that commits between
	// them cannot pair one generation's header with another's observations.
	err := s.store.WithinTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(q *generated.Queries) error {
			readiness, err := q.GetAgentConnectorReadinessContext(
				ctx,
				generated.GetAgentConnectorReadinessContextParams{
					ViewerUserID:                    principal.UserID,
					DiscoveryStaleAfterMilliseconds: connectorDiscoveryStaleAfter.Milliseconds(),
					AgentID:                         input.AgentID,
					CompanyID:                       principal.CompanyID,
				},
			)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return fmt.Errorf("read Agent Connector readiness: %w", err)
			}
			result = ConnectorAppReadinessResult{
				AgentID: readiness.AgentID, Provider: ConnectorProvider(readiness.Provider),
				RuntimeID: readiness.BoundRuntimeID,
				Discovery: connectorAppDiscovery(
					readiness.BoundRuntimeID.Valid,
					readiness.RuntimeEligible,
					readiness.Generation.Valid,
					readiness.DiscoveryFresh,
					readiness.DiscoveryObservedAt.Time,
				),
				Apps: make([]ConnectorAppReadiness, 0, len(input.Apps)),
			}
			if result.Discovery.State == ConnectorDiscoveryStateUnbound ||
				result.Discovery.State == ConnectorDiscoveryStateMissing {
				return nil
			}
			provider, runtimeID := result.Provider, readiness.BoundRuntimeID.UUID
			denial, err = readConnectorAppDenial(ctx, q, principal.CompanyID, runtimeID, readiness)
			if err != nil {
				return err
			}

			var pluginNativeIDs []string
			for _, app := range input.Apps {
				pluginNativeIDs = append(pluginNativeIDs, connectorAppPins[app][provider].Plugins...)
			}
			if len(pluginNativeIDs) > 0 {
				rows, err := q.ListPluginsForRuntimeByNativeID(
					ctx,
					generated.ListPluginsForRuntimeByNativeIDParams{
						RuntimeID: runtimeID, CompanyID: principal.CompanyID,
						Provider: string(provider), NativeIds: pluginNativeIDs,
					},
				)
				if err != nil {
					return fmt.Errorf("read pinned Plugins: %w", err)
				}
				for _, row := range rows {
					plugins[row.NativeID] = row
				}
			}

			var requested []ConnectorIdentityInput
			for index, app := range input.Apps {
				pin := connectorAppPins[app][provider]
				identities := slices.Clone(pin.Connectors)
				for _, nativeID := range pin.Plugins {
					plugin, observed := plugins[nativeID]
					if !observed {
						continue
					}
					var linked []PluginConnectorIdentity
					// Trusted structured product state, validated before persistence.
					_ = json.Unmarshal(plugin.ConnectorIdentities, &linked)
					for _, identity := range linked {
						if !slices.Contains(identities, ConnectorIdentityInput(identity)) {
							identities = append(
								identities,
								ConnectorIdentityInput(identity),
							)
						}
					}
				}
				appIdentities[index] = identities
				for _, identity := range identities {
					if !slices.Contains(requested, identity) {
						requested = append(requested, identity)
					}
				}
			}
			if len(requested) > 0 {
				sources := make([]string, len(requested))
				nativeIDs := make([]string, len(requested))
				for index, identity := range requested {
					sources[index], nativeIDs[index] = string(identity.Source), identity.NativeID
				}
				rows, err := q.ListConnectorAppReadiness(
					ctx,
					generated.ListConnectorAppReadinessParams{
						CompanyID: principal.CompanyID, Provider: string(provider),
						RuntimeID: runtimeID, Generation: readiness.Generation.UUID,
						Sources: sources, NativeIds: nativeIDs,
					},
				)
				if err != nil {
					return fmt.Errorf("read pinned Connectors: %w", err)
				}
				for _, row := range rows {
					connectors[ConnectorIdentityInput{Source: ConnectorSource(row.Source), NativeID: row.NativeID}] = row
				}
			}
			// An observed candidate or pinned Plugin without a logo a client
			// can load (every Claude source, and a Codex app whose logo loads
			// only in a ChatGPT session) borrows a Codex logo of the same
			// name, only from the viewer's own Runtimes; a Codex app also
			// takes the logo of a Plugin installed on this Runtime that
			// bundles it.
			var unlogged []string
			bundles := false
			for _, row := range connectors {
				if row.Observed && !loadableLogo(textPointer(row.IconUrl)) {
					unlogged = append(unlogged, row.Name)
					bundles = bundles || ConnectorSource(row.Source) == ConnectorSourceCodexApp
				}
			}
			for _, plugin := range plugins {
				if !loadableLogo(textPointer(plugin.IconUrl)) {
					unlogged = append(unlogged, plugin.Name)
				}
			}
			logoFallbacks, err = readAppLogoFallbacks(
				ctx, q, principal, runtimeID, bundles, unlogged,
			)
			return err
		},
	)
	if err != nil {
		return ConnectorAppReadinessResult{}, err
	}
	if result.Discovery.State == ConnectorDiscoveryStateUnbound ||
		result.Discovery.State == ConnectorDiscoveryStateMissing {
		for _, app := range input.Apps {
			result.Apps = append(
				result.Apps,
				ConnectorAppReadiness{App: app, State: ConnectorAppStateUnknown},
			)
		}
		return result, nil
	}
	provider := result.Provider

	revealPlugins := principal.Can(auth.PermissionPluginsManage)
	revealConnectors := revealPlugins || principal.Can(auth.PermissionConnectorsManage)
	for index, app := range input.Apps {
		identities := appIdentities[index]
		rows := make([]generated.ListConnectorAppReadinessRow, len(identities))
		candidates := make([]connectorAppCandidate, len(identities))
		for position, identity := range identities {
			row, found := connectors[identity]
			rows[position] = row
			candidates[position] = connectorAppCandidate{
				Source: identity.Source, Observed: found && row.Observed,
				Status: ConnectorStatus(row.Status), CanExecute: row.CanExecute,
				ReviewRequired: row.ReviewRequired, Eligible: row.Eligible,
				Denied: found && denial.denies(row.ID, identity),
			}
		}
		var pinnedPlugin *generated.ListPluginsForRuntimeByNativeIDRow
		installable := false
		for _, nativeID := range connectorAppPins[app][provider].Plugins {
			plugin, observed := plugins[nativeID]
			if !observed {
				continue
			}
			if pinnedPlugin == nil {
				pinnedPlugin = &plugin
			}
			installable = installable || PluginAction(plugin.Action) == PluginActionInstall
		}
		state, winner := pickConnectorAppState(provider, result.Discovery.State, candidates, installable)
		readinessEntry := ConnectorAppReadiness{
			App: app, State: state,
			IconURL: connectorAppIcon(rows, candidates, winner, pinnedPlugin, logoFallbacks),
		}
		if revealConnectors && winner >= 0 {
			row := rows[winner]
			tools, writeTools := connectorAppToolCounts(ConnectorSource(row.Source), row.ToolNamespace)
			readinessEntry.Connector = &ConnectorAppConnector{
				ID:             row.ID,
				Source:         ConnectorSource(row.Source),
				NativeID:       row.NativeID,
				Name:           row.Name,
				Status:         ConnectorStatus(row.Status),
				Action:         ConnectorAction(row.Action),
				ToolCount:      tools,
				WriteToolCount: writeTools,
			}
		}
		if revealPlugins && pinnedPlugin != nil {
			readinessEntry.Plugin = &ConnectorAppPlugin{
				ID: pinnedPlugin.ID, NativeID: pinnedPlugin.NativeID, Name: pinnedPlugin.Name,
				Installed: pinnedPlugin.Installed, Action: PluginAction(pinnedPlugin.Action),
			}
		}
		result.Apps = append(result.Apps, readinessEntry)
	}
	return result, nil
}

// connectorAppIcon picks one app's logo, the first a client can load of: the
// winning Connector's, any other observed candidate's, that of a Plugin
// installed on the Runtime that bundles one of those Codex apps, the pinned
// Plugin's, and one borrowed by the name of any of them. With none loadable
// it keeps the first present, which the client's failover replaces (D26).
func connectorAppIcon(
	rows []generated.ListConnectorAppReadinessRow,
	candidates []connectorAppCandidate,
	winner int,
	plugin *generated.ListPluginsForRuntimeByNativeIDRow,
	fallbacks appLogoFallbacks,
) *string {
	order := make([]int, 0, len(rows))
	if winner >= 0 {
		order = append(order, winner)
	}
	for position, candidate := range candidates {
		if candidate.Observed && position != winner {
			order = append(order, position)
		}
	}
	logos := make([]*string, 0, 3*len(order)+2)
	for _, position := range order {
		logos = append(logos, textPointer(rows[position].IconUrl))
	}
	for _, position := range order {
		if ConnectorSource(rows[position].Source) == ConnectorSourceCodexApp {
			logos = append(logos, fallbacks.bundled(rows[position].NativeID, rows[position].Name))
		}
	}
	if plugin != nil {
		logos = append(logos, textPointer(plugin.IconUrl))
	}
	for _, position := range order {
		logos = append(logos, fallbacks.named(rows[position].Name))
	}
	if plugin != nil {
		logos = append(logos, fallbacks.named(plugin.Name))
	}
	return preferLoadableLogo(logos...)
}

// connectorAppDenial restates the daemon's per-Agent capability denial for one
// Runtime: a Connector the Agent disabled by id, and every Connector a
// disabled Plugin provides there — its own claude_plugin Connector and each
// identity it declares.
type connectorAppDenial struct{ denial connectorselection.Denial }

func (d connectorAppDenial) denies(id uuid.UUID, identity ConnectorIdentityInput) bool {
	return d.denial.Denies(id, string(identity.Source), identity.NativeID)
}

func readConnectorAppDenial(
	ctx context.Context,
	q *generated.Queries,
	companyID, runtimeID uuid.UUID,
	readiness generated.GetAgentConnectorReadinessContextRow,
) (connectorAppDenial, error) {
	denial, err := connectorselection.ReadDenial(ctx, q, companyID, runtimeID, readiness)
	return connectorAppDenial{denial: denial}, err
}

// connectorAppToolCounts counts the tools a Run can call from one current
// observation: only a Codex app's namespace lists tools (a Claude namespace
// lists MCP servers, never marked read-only), and a tool the provider reports
// disabled is disabled in the Run too. Write tools are those not read-only.
func connectorAppToolCounts(source ConnectorSource, namespace []byte) (tools, writeTools int) {
	if source != ConnectorSourceCodexApp {
		return 0, 0
	}
	var observed []agent.ConnectorTool
	_ = json.Unmarshal(namespace, &observed)
	for _, tool := range observed {
		if !tool.Enabled {
			continue
		}
		tools++
		if !tool.ReadOnly {
			writeTools++
		}
	}
	return tools, writeTools
}

// normalizeConnectorAppReadinessInput admits at most the six app hints a
// Workflow may carry, each a known app named once.
func normalizeConnectorAppReadinessInput(input ConnectorAppReadinessInput) error {
	if !util.ValidUUID(input.AgentID) || len(input.Apps) > 6 {
		return ErrInvalidInput
	}
	for index, app := range input.Apps {
		if !slices.Contains(connectorAppTypes, app) || slices.Contains(input.Apps[:index], app) {
			return ErrInvalidInput
		}
	}
	return nil
}

// connectorAppDiscovery reports the discovery of the Runtime one viewer's
// binding of an Agent names. A viewer who has not set the Agent up is
// unbound; a bound Runtime that never reported or was removed cannot execute
// a Connector and is missing. Only a readable generation discloses its time.
func connectorAppDiscovery(
	bound, eligible, reported, fresh bool,
	observedAt time.Time,
) ConnectorAppDiscovery {
	switch {
	case !bound:
		return ConnectorAppDiscovery{State: ConnectorDiscoveryStateUnbound}
	case !eligible || !reported:
		return ConnectorAppDiscovery{State: ConnectorDiscoveryStateMissing}
	}
	formatted := protocol.FormatWireTime(observedAt)
	state := ConnectorDiscoveryStateStale
	if fresh {
		state = ConnectorDiscoveryStateFresh
	}
	return ConnectorAppDiscovery{State: state, ObservedAt: &formatted}
}

// connectorAppCandidate is one pinned identity as the Agent's Runtime
// currently observes it. Eligible restates the claim freeze's per-Connector
// rule without its freshness; Denied is the daemon's per-Agent capability
// denial.
type connectorAppCandidate struct {
	Source         ConnectorSource
	Observed       bool
	Status         ConnectorStatus
	CanExecute     bool
	ReviewRequired bool
	Eligible       bool
	Denied         bool
}

// pickConnectorAppState ranks every candidate of one app and returns the best
// state with the index of the candidate that produced it, or -1 when an
// installable pinned Plugin or nothing did. It is pure so the whole state
// table is testable without a database.
func pickConnectorAppState(
	provider ConnectorProvider,
	discovery ConnectorDiscoveryState,
	candidates []connectorAppCandidate,
	installablePlugin bool,
) (ConnectorAppState, int) {
	if discovery != ConnectorDiscoveryStateFresh && discovery != ConnectorDiscoveryStateStale {
		return ConnectorAppStateUnknown, -1
	}
	best, winner := ConnectorAppStateNotOffered, -1
	for index, candidate := range candidates {
		state := connectorAppCandidateState(
			provider,
			discovery == ConnectorDiscoveryStateFresh,
			candidate,
		)
		if slices.Index(connectorAppStatePrecedence, state) <
			slices.Index(connectorAppStatePrecedence, best) {
			best, winner = state, index
		}
	}
	if installablePlugin && slices.Index(connectorAppStatePrecedence, ConnectorAppStateNeedsConnection) <
		slices.Index(connectorAppStatePrecedence, best) {
		return ConnectorAppStateNeedsConnection, -1
	}
	return best, winner
}

func connectorAppCandidateState(
	provider ConnectorProvider,
	fresh bool,
	candidate connectorAppCandidate,
) ConnectorAppState {
	connectedExecutable := candidate.Eligible ||
		candidate.Status == ConnectorStatusConnected && candidate.CanExecute
	switch {
	case !candidate.Observed:
		return ConnectorAppStateNotOffered
	case !selectiveConnectorSource(provider, candidate.Source):
		// Claude.ai cloud connectors and Codex MCP servers never enter a Run.
		return ConnectorAppStateProviderManaged
	case connectedExecutable && candidate.Denied:
		return ConnectorAppStateDisabledForAgent
	case candidate.Eligible && fresh:
		return ConnectorAppStateReady
	case candidate.Eligible:
		return ConnectorAppStatePending
	case connectedExecutable && candidate.ReviewRequired:
		return ConnectorAppStateReviewRequired
	case connectedExecutable, candidate.Status == ConnectorStatusConnected,
		candidate.Status == ConnectorStatusManagedInProvider:
		// Connected, but not yet enabled by Company policy or its tool
		// contract not yet verified; discovery settles it.
		return ConnectorAppStatePending
	case candidate.Status == ConnectorStatusUnavailable:
		return ConnectorAppStateNotOffered
	default:
		return ConnectorAppStateNeedsConnection
	}
}
