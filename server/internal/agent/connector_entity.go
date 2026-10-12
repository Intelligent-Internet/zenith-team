package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/util"
	"boardly.dev/boardly/server/pkg/agent"
	"boardly.dev/boardly/server/pkg/protocol"
)

const (
	ConnectorSchemaVersion = 1

	defaultConnectorPageSize             = 50
	maxConnectorPageSize                 = 100
	maxConnectorItems                    = 10000
	maxConnectorResolveIdentities        = 64
	maxDesiredExecutableConnectors       = 128
	maxConnectorNativeBytes              = 240
	maxConnectorNameBytes                = 160
	maxConnectorDetailBytes              = 1000
	maxConnectorCategoryBytes            = 80
	maxConnectorIconURLBytes             = 1024
	maxSafeInteger                 int64 = 1<<53 - 1
	connectorCursorVersion               = byte(1)
	connectorDiscoveryStaleAfter         = protocol.ConnectorDiscoveryFreshWindow
	connectorGenerationClockSkew         = 5 * time.Minute
)

var (
	ErrConnectorDiscoveryConflict   = errors.New("Connector discovery generation conflicts with current state")
	ErrConnectorNotFound            = errors.New("Connector was not found")
	ErrConnectorConflict            = errors.New("Connector policy conflicts with current state")
	ErrConnectorVersionExhausted    = errors.New("Connector policy version is exhausted")
	ErrConnectorProviderUnavailable = errors.New("Company Connector provider is unavailable")
	ErrConnectorActionUnavailable   = errors.New("Connector action is unavailable")
	ErrConnectorSnapshotUnavailable = errors.New("Run Connector selection is no longer executable")
)

type ConnectorProvider string

const (
	ConnectorProviderCodex  ConnectorProvider = "codex"
	ConnectorProviderClaude ConnectorProvider = "claude"
)

type ConnectorSource string

const (
	ConnectorSourceCodexApp     ConnectorSource = "codex_app"
	ConnectorSourceCodexMCP     ConnectorSource = "codex_mcp"
	ConnectorSourceClaudePlugin ConnectorSource = "claude_plugin"
	ConnectorSourceClaudeMCP    ConnectorSource = "claude_mcp"
	ConnectorSourceClaudeCloud  ConnectorSource = "claude_cloud"
)

type connectorCategory string

const (
	connectorCategoryProductivity       connectorCategory = "Productivity"
	connectorCategoryCreativity         connectorCategory = "Creativity"
	connectorCategoryDeveloperTools     connectorCategory = "Developer Tools"
	connectorCategoryBusinessOperations connectorCategory = "Business & Operations"
	connectorCategoryDataAnalytics      connectorCategory = "Data & Analytics"
	connectorCategoryCommunication      connectorCategory = "Communication"
	connectorCategoryEducationResearch  connectorCategory = "Education & Research"
	connectorCategorySecurity           connectorCategory = "Security"
	connectorCategoryFinance            connectorCategory = "Finance"
	connectorCategoryHealthcare         connectorCategory = "Healthcare"
	connectorCategoryTravel             connectorCategory = "Travel"
	connectorCategoryEntertainment      connectorCategory = "Entertainment"
	connectorCategoryOther              connectorCategory = "Other"
)

var connectorCategoryOrder = [...]connectorCategory{
	connectorCategoryProductivity,
	connectorCategoryCreativity,
	connectorCategoryDeveloperTools,
	connectorCategoryBusinessOperations,
	connectorCategoryDataAnalytics,
	connectorCategoryCommunication,
	connectorCategoryEducationResearch,
	connectorCategorySecurity,
	connectorCategoryFinance,
	connectorCategoryHealthcare,
	connectorCategoryTravel,
	connectorCategoryEntertainment,
	connectorCategoryOther,
}

type connectorCategoryIdentity struct {
	Provider ConnectorProvider
	Source   ConnectorSource
	NativeID string
}

func connectorCategoryKey(
	provider ConnectorProvider,
	source ConnectorSource,
	nativeID string,
) connectorCategoryIdentity {
	return connectorCategoryIdentity{
		Provider: provider,
		Source:   source,
		NativeID: nativeID,
	}
}

func codexAppCategoryKey(nativeID string) connectorCategoryIdentity {
	return connectorCategoryKey(ConnectorProviderCodex, ConnectorSourceCodexApp, nativeID)
}

func claudePluginCategoryKey(nativeID string) connectorCategoryIdentity {
	return connectorCategoryKey(ConnectorProviderClaude, ConnectorSourceClaudePlugin, nativeID)
}

func claudeCloudCategoryKey(nativeID string) connectorCategoryIdentity {
	return connectorCategoryKey(ConnectorProviderClaude, ConnectorSourceClaudeCloud, nativeID)
}

var connectorCategoryOverrides = map[connectorCategoryIdentity]connectorCategory{
	// Codex Productivity.
	codexAppCategoryKey("connector_947e0d954944416db111db556030eea6"): connectorCategoryProductivity,
	codexAppCategoryKey("connector_5f3c8c41a1e54ad7a76272c89e2554fa"): connectorCategoryProductivity,
	codexAppCategoryKey("asdk_app_69c18c28f1188191bf5b8445c4ab0a2e"):  connectorCategoryProductivity,
	codexAppCategoryKey("asdk_app_69a089a326dc8191b32a3f2553f5be2c"):  connectorCategoryProductivity,
	codexAppCategoryKey("asdk_app_69431e6d26b88191b4029488aeb42f5b"):  connectorCategoryProductivity,
	codexAppCategoryKey("asdk_app_69616780bd208191b4fb44ba44f72b61"):  connectorCategoryProductivity,
	codexAppCategoryKey("asdk_app_69b31dc2110c8191b8b47dc98fe5a052"):  connectorCategoryProductivity,
	codexAppCategoryKey("connector_e6a7394682e24467ac68c60696f275a4"): connectorCategoryProductivity,
	codexAppCategoryKey("connector_1e4f6a44acf14e3ca1d96672f8c945bc"): connectorCategoryProductivity,

	// Codex Creativity.
	codexAppCategoryKey("connector_68df33b1a2d081918778431a9cfca8ba"): connectorCategoryCreativity,
	codexAppCategoryKey("connector_68df038e0ba48191908c8434991bbac2"): connectorCategoryCreativity,
	codexAppCategoryKey("asdk_app_698a098735908191989f5788d7ee317e"):  connectorCategoryCreativity,
	codexAppCategoryKey("asdk_app_69f0dc45f6048191876c14c1016fe778"):  connectorCategoryCreativity,
	codexAppCategoryKey("connector_69312da8e4dc81919370cb86fd172b6c"): connectorCategoryCreativity,
	codexAppCategoryKey("asdk_app_6a3293e129088191abf0875820e839da"):  connectorCategoryCreativity,
	codexAppCategoryKey("asdk_app_6a1f227d5a848191ae3317c66947b440"):  connectorCategoryCreativity,

	// Codex Developer Tools.
	codexAppCategoryKey("connector_76869538009648d5b282a4bb21c3d157"): connectorCategoryDeveloperTools,
	codexAppCategoryKey("asdk_app_69d3e5ee6a708191baa733f7b8931995"):  connectorCategoryDeveloperTools,
	codexAppCategoryKey("connector_690a90ec05c881918afb6a55dc9bbaa1"): connectorCategoryDeveloperTools,
	codexAppCategoryKey("asdk_app_693a0a79ffe48191901173077edcf914"):  connectorCategoryDeveloperTools,
	codexAppCategoryKey("asdk_app_6934801c799081918131791660f02890"):  connectorCategoryDeveloperTools,
	codexAppCategoryKey("connector_2de447f3f15448ebab48783d7e4f5d81"): connectorCategoryDeveloperTools,
	codexAppCategoryKey("asdk_app_69e8c7f174a08191a28b6da96c8062c4"):  connectorCategoryDeveloperTools,
	codexAppCategoryKey("asdk_app_69ea4ed2cf7c8191b742ef3622479ddd"):  connectorCategoryDeveloperTools,
	codexAppCategoryKey("asdk_app_69e0086d87088191a3edc052fa50c29f"):  connectorCategoryDeveloperTools,
	codexAppCategoryKey("asdk_app_69ef18c674308191a2f952431f91ea61"):  connectorCategoryDeveloperTools,

	// Codex Business & Operations.
	codexAppCategoryKey("asdk_app_697acb8e53d88191bf7a79e62012ae14"):  connectorCategoryBusinessOperations,
	codexAppCategoryKey("asdk_app_6981f663d5cc8191ae0d5717a05ccc89"):  connectorCategoryBusinessOperations,
	codexAppCategoryKey("asdk_app_69377d07cd9c8191a988f06f15b8c674"):  connectorCategoryBusinessOperations,
	codexAppCategoryKey("connector_9f3d361ec84b4c6588a7129a2d4e3cb3"): connectorCategoryBusinessOperations,
	codexAppCategoryKey("asdk_app_69e65c430b3081919aa4d962ab5d1698"):  connectorCategoryBusinessOperations,
	codexAppCategoryKey("asdk_app_6a193ef5e804819197c25f88d92d6bf7"):  connectorCategoryBusinessOperations,
	codexAppCategoryKey("asdk_app_69bd664f2a908191a3a0a47eca8559d1"):  connectorCategoryBusinessOperations,
	codexAppCategoryKey("connector_690aabb71bf481918b8d5b614ed3fd4c"): connectorCategoryBusinessOperations,
	codexAppCategoryKey("asdk_app_693ca6ce2db08191bb52d66743c65184"):  connectorCategoryBusinessOperations,
	codexAppCategoryKey("connector_692de805e3ec8191834719067174a384"): connectorCategoryBusinessOperations,
	codexAppCategoryKey("asdk_app_6a20b18a639081918c1b438f8381b27e"):  connectorCategoryBusinessOperations,

	// Codex Data & Analytics.
	codexAppCategoryKey("asdk_app_699caef2d680819188727b0ddbb349dd"):  connectorCategoryDataAnalytics,
	codexAppCategoryKey("asdk_app_69b2e9aed45c8191b254b207dfcc2bb4"):  connectorCategoryDataAnalytics,
	codexAppCategoryKey("asdk_app_69f4ff416e948191b53586c77b559615"):  connectorCategoryDataAnalytics,
	codexAppCategoryKey("connector_7c3f2c8cdbf64bb0b183ff52f527a06e"): connectorCategoryDataAnalytics,
	codexAppCategoryKey("asdk_app_694a52cfaa3c819192bea84eaa254968"):  connectorCategoryDataAnalytics,
	codexAppCategoryKey("asdk_app_69a1247fceb88191a0fde719fd50920d"):  connectorCategoryDataAnalytics,

	// Codex Communication.
	codexAppCategoryKey("connector_2128aebfecb84f64a069897515042a44"): connectorCategoryCommunication,
	codexAppCategoryKey("asdk_app_69a1d78e929881919bba0dbda1f6436d"):  connectorCategoryCommunication,
	codexAppCategoryKey("connector_4aaab2856305417b993eca9a216aaf6e"): connectorCategoryCommunication,
	codexAppCategoryKey("asdk_app_69a21e4058dc8191a6220fa911310d7b"):  connectorCategoryCommunication,
	codexAppCategoryKey("connector_246af0940da3457da0e751171dc1ce60"): connectorCategoryCommunication,
	codexAppCategoryKey("asdk_app_69373a13116c819189d046aea1278836"):  connectorCategoryCommunication,
	codexAppCategoryKey("asdk_app_6a323a8a890c819190480c9044395170"):  connectorCategoryCommunication,

	// Codex Education & Research.
	codexAppCategoryKey("asdk_app_6943e6f4a928819195962de16fb9ffe4"): connectorCategoryEducationResearch,
	codexAppCategoryKey("asdk_app_6948b485f5bc8191adb4df13f369cec7"): connectorCategoryEducationResearch,
	codexAppCategoryKey("asdk_app_69439d715a7c8191aed9e2f6649e105f"): connectorCategoryEducationResearch,
	codexAppCategoryKey("asdk_app_6952b3a3f1e881918951582d59483c78"): connectorCategoryEducationResearch,
	codexAppCategoryKey("asdk_app_69fe0bf66c8481919c513d799406436e"): connectorCategoryEducationResearch,
	codexAppCategoryKey("asdk_app_699cc1a043688191a3ee44e6a2c2ebc1"): connectorCategoryEducationResearch,
	codexAppCategoryKey("asdk_app_69a0d0b83b5881919dd5f0e53b525d31"): connectorCategoryEducationResearch,

	// Codex Security.
	codexAppCategoryKey("asdk_app_6943d1778c8c81918f4c9ddf4849ca41"): connectorCategorySecurity,
	codexAppCategoryKey("asdk_app_69ae8d10d5b48191bd9eadb52dfcc22d"): connectorCategorySecurity,
	codexAppCategoryKey("asdk_app_6a033addd77881918cea85cd71109f80"): connectorCategorySecurity,
	codexAppCategoryKey("asdk_app_6979fd5b5ddc81918a648f759cc4d719"): connectorCategorySecurity,
	codexAppCategoryKey("asdk_app_69c6417ce320819192d4b9edf3daf4ce"): connectorCategorySecurity,
	codexAppCategoryKey("asdk_app_694756a8563c8191b79b13723b98ec67"): connectorCategorySecurity,

	// Codex Finance.
	codexAppCategoryKey("connector_691f721a77bc8191be115b65c85075c0"): connectorCategoryFinance,
	codexAppCategoryKey("asdk_app_6965faefe2b081919a998e14aa25f738"):  connectorCategoryFinance,
	codexAppCategoryKey("asdk_app_6983c208e5f8819196b7511519f97993"):  connectorCategoryFinance,
	codexAppCategoryKey("asdk_app_6a2baf2fad748191812393c3e00308ef"):  connectorCategoryFinance,
	codexAppCategoryKey("asdk_app_69bc11db874881918718abaca20b68ce"):  connectorCategoryFinance,

	// Codex Healthcare.
	codexAppCategoryKey("asdk_app_6a0694cbb2608191bbefb74ba810ab68"): connectorCategoryHealthcare,
	codexAppCategoryKey("asdk_app_69b5c48a72348191b3ad5abf6ec5dbfb"): connectorCategoryHealthcare,
	codexAppCategoryKey("asdk_app_6945d9d84c5c8191a28343ae9d7b6e8b"): connectorCategoryHealthcare,
	codexAppCategoryKey("asdk_app_69c320d803048191bc2682ea9ff3e5fa"): connectorCategoryHealthcare,
	codexAppCategoryKey("asdk_app_69beacb8780c81919104bb111b56346b"): connectorCategoryHealthcare,
	codexAppCategoryKey("asdk_app_69aef5b699a0819184512d57743fc1cd"): connectorCategoryHealthcare,

	// Codex Travel.
	codexAppCategoryKey("asdk_app_694546cd042881919bb746a8dc300f38"): connectorCategoryTravel,
	codexAppCategoryKey("asdk_app_69c4d4163c8c819183a9bdcf6d2ac262"): connectorCategoryTravel,
	codexAppCategoryKey("asdk_app_69848e517d0c819191695bf9b23f0208"): connectorCategoryTravel,
	codexAppCategoryKey("asdk_app_69a85fe44a188191a43c7dbc80186d70"): connectorCategoryTravel,
	codexAppCategoryKey("asdk_app_69c1784c29208191a35b4ddef3c7e6d5"): connectorCategoryTravel,
	codexAppCategoryKey("asdk_app_6943d405cbac8191bc7aa723c333335e"): connectorCategoryTravel,
	codexAppCategoryKey("asdk_app_6a21c822e22c819194e65ec16411cb29"): connectorCategoryTravel,

	// Codex Entertainment.
	codexAppCategoryKey("asdk_app_6938a94a61d881918ef32cb999ff937c"):  connectorCategoryEntertainment,
	codexAppCategoryKey("asdk_app_6944570636288191b7944d8c4a3fb857"):  connectorCategoryEntertainment,
	codexAppCategoryKey("connector_68de829bf7648191acd70a907364c67c"): connectorCategoryEntertainment,
	codexAppCategoryKey("asdk_app_69ea8fff9b048191961dc39a99d1cf06"):  connectorCategoryEntertainment,
	codexAppCategoryKey("asdk_app_69a0e374670c819190761772d2092135"):  connectorCategoryEntertainment,

	// Claude official plugins.
	claudePluginCategoryKey("asana@claude-plugins-official"):          connectorCategoryProductivity,
	claudePluginCategoryKey("atlassian@claude-plugins-official"):      connectorCategoryProductivity,
	claudePluginCategoryKey("circleback@claude-plugins-official"):     connectorCategoryProductivity,
	claudePluginCategoryKey("linear@claude-plugins-official"):         connectorCategoryProductivity,
	claudePluginCategoryKey("Notion@claude-plugins-official"):         connectorCategoryProductivity,
	claudePluginCategoryKey("figma@claude-plugins-official"):          connectorCategoryCreativity,
	claudePluginCategoryKey("context7@claude-plugins-official"):       connectorCategoryDeveloperTools,
	claudePluginCategoryKey("firebase@claude-plugins-official"):       connectorCategoryDeveloperTools,
	claudePluginCategoryKey("firecrawl@claude-plugins-official"):      connectorCategoryDeveloperTools,
	claudePluginCategoryKey("github@claude-plugins-official"):         connectorCategoryDeveloperTools,
	claudePluginCategoryKey("gitlab@claude-plugins-official"):         connectorCategoryDeveloperTools,
	claudePluginCategoryKey("greptile@claude-plugins-official"):       connectorCategoryDeveloperTools,
	claudePluginCategoryKey("laravel-boost@claude-plugins-official"):  connectorCategoryDeveloperTools,
	claudePluginCategoryKey("pinecone@claude-plugins-official"):       connectorCategoryDeveloperTools,
	claudePluginCategoryKey("playwright@claude-plugins-official"):     connectorCategoryDeveloperTools,
	claudePluginCategoryKey("sentry@claude-plugins-official"):         connectorCategoryDeveloperTools,
	claudePluginCategoryKey("serena@claude-plugins-official"):         connectorCategoryDeveloperTools,
	claudePluginCategoryKey("sonatype-guide@claude-plugins-official"): connectorCategoryDeveloperTools,
	claudePluginCategoryKey("supabase@claude-plugins-official"):       connectorCategoryDeveloperTools,
	claudePluginCategoryKey("vercel@claude-plugins-official"):         connectorCategoryDeveloperTools,
	claudePluginCategoryKey("posthog@claude-plugins-official"):        connectorCategoryDataAnalytics,
	claudePluginCategoryKey("slack@claude-plugins-official"):          connectorCategoryCommunication,
	claudePluginCategoryKey("semgrep@claude-plugins-official"):        connectorCategorySecurity,
	claudePluginCategoryKey("stripe@claude-plugins-official"):         connectorCategoryFinance,

	// Claude subscription cloud connectors.
	claudeCloudCategoryKey("claude-cloud:gmail"):           connectorCategoryCommunication,
	claudeCloudCategoryKey("claude-cloud:slack"):           connectorCategoryCommunication,
	claudeCloudCategoryKey("claude-cloud:google-drive"):    connectorCategoryProductivity,
	claudeCloudCategoryKey("claude-cloud:google-calendar"): connectorCategoryProductivity,
	claudeCloudCategoryKey("claude-cloud:microsoft-365"):   connectorCategoryProductivity,
	claudeCloudCategoryKey("claude-cloud:github"):          connectorCategoryDeveloperTools,
}

type ConnectorStatus string

const (
	ConnectorStatusAvailable         ConnectorStatus = "available"
	ConnectorStatusInstalledNotReady ConnectorStatus = "installed_not_ready"
	ConnectorStatusNeedsAuth         ConnectorStatus = "needs_auth"
	ConnectorStatusConnected         ConnectorStatus = "connected"
	ConnectorStatusManagedInProvider ConnectorStatus = "managed_in_provider"
	ConnectorStatusUnavailable       ConnectorStatus = "unavailable"
	ConnectorStatusError             ConnectorStatus = "error"
)

type ConnectorAction string

const (
	ConnectorActionInstallAndAuth   ConnectorAction = "install_and_auth"
	ConnectorActionAuthenticate     ConnectorAction = "authenticate"
	ConnectorActionManageInProvider ConnectorAction = "manage_in_provider"
	ConnectorActionNone             ConnectorAction = "none"
)

type ConnectorObservationInput struct {
	Source         ConnectorSource
	NativeID       string
	Name           string
	Description    *string
	Category       *string
	IconURL        *string
	InstallCount   *int64
	Status         ConnectorStatus
	Action         ConnectorAction
	CanExecute     bool
	ContractDigest string
	Tools          []agent.ConnectorTool
}

type ConnectorDiscoveryInput struct {
	CompanyID      uuid.UUID
	RuntimeID      uuid.UUID
	DaemonID       string
	Generation     uuid.UUID
	ManifestDigest string
	ObservedAt     time.Time
	Items          []ConnectorObservationInput
}

// ConnectorDiscoveryReaffirmInput names the unchanged generation a Runtime
// re-observed, with the time it did so.
type ConnectorDiscoveryReaffirmInput struct {
	CompanyID      uuid.UUID
	RuntimeID      uuid.UUID
	DaemonID       string
	Generation     uuid.UUID
	ManifestDigest string
	ObservedAt     time.Time
}

type ConnectorDiscoveryResult struct {
	Applied bool `json:"applied"`
}

type ConnectorRunChangeType string

const (
	ConnectorRunChangeDiscovery ConnectorRunChangeType = "discovery_changed"
	ConnectorRunChangePolicy    ConnectorRunChangeType = "policy_changed"
)

// ConnectorRunChange is the transport-neutral fact emitted after a Connector
// discovery or policy transaction commits. RuntimeID is present for discovery
// changes; ConnectorID is present for one policy mutation.
type ConnectorRunChange struct {
	CompanyID   uuid.UUID
	RuntimeID   *uuid.UUID
	ConnectorID *uuid.UUID
	Provider    ConnectorProvider
	Type        ConnectorRunChangeType
}

type ConnectorItemV1 struct {
	Action           ConnectorAction   `json:"action"`
	CanExecute       bool              `json:"canExecute"`
	Category         *string           `json:"category,omitempty"`
	Description      *string           `json:"description,omitempty"`
	DesiredEnabled   bool              `json:"desiredEnabled"`
	EffectiveEnabled bool              `json:"effectiveEnabled"`
	IconURL          *string           `json:"iconUrl,omitempty"`
	ID               uuid.UUID         `json:"id"`
	InstallCount     *int64            `json:"installCount,omitempty"`
	Name             string            `json:"name"`
	NativeID         string            `json:"nativeId"`
	ObservedAt       string            `json:"observedAt"`
	Provider         ConnectorProvider `json:"provider"`
	ReviewRequired   bool              `json:"reviewRequired,omitempty"`
	Revision         int64             `json:"revision"`
	Source           ConnectorSource   `json:"source"`
	Status           ConnectorStatus   `json:"status"`
}

type ConnectorRuntimeSummary struct {
	ID     uuid.UUID `json:"id"`
	Name   string    `json:"name"`
	Online bool      `json:"online"`
}

type ConnectorCategoryCount struct {
	Category string `json:"category"`
	Count    int64  `json:"count"`
}

type ConnectorCatalogResult struct {
	SchemaVersion  int                      `json:"schemaVersion"`
	ActiveProvider ConnectorProvider        `json:"activeProvider"`
	Runtime        *ConnectorRuntimeSummary `json:"runtime"`
	Connectors     []ConnectorItemV1        `json:"connectors"`
	NextCursor     *string                  `json:"nextCursor"`
	TotalCount     int64                    `json:"totalCount"`
	ConnectedCount int64                    `json:"connectedCount"`
	CategoryCounts []ConnectorCategoryCount `json:"categoryCounts"`
	Stale          bool                     `json:"stale"`
}

type ConnectorCursor struct {
	Name string
	ID   uuid.UUID
}

type ConnectorListInput struct {
	RuntimeID      *uuid.UUID
	Provider       ConnectorProvider
	Source         ConnectorSource
	Query          string
	Category       string
	Status         ConnectorStatus
	ReviewRequired bool
	Cursor         *ConnectorCursor
	Limit          int
}

type ConnectorIdentityInput struct {
	Source   ConnectorSource
	NativeID string
}

type ConnectorResolveInput struct {
	Identities []ConnectorIdentityInput
}

type ConnectorResolveResult struct {
	SchemaVersion int               `json:"schemaVersion"`
	Connectors    []ConnectorItemV1 `json:"connectors"`
}

func normalizeConnectorDiscovery(
	provider ConnectorProvider,
	input ConnectorDiscoveryInput,
	serverTime time.Time,
) (ConnectorDiscoveryInput, error) {
	if !validConnectorGeneration(provider, ConnectorDiscoveryReaffirmInput{
		CompanyID: input.CompanyID, RuntimeID: input.RuntimeID, DaemonID: input.DaemonID,
		Generation: input.Generation, ManifestDigest: input.ManifestDigest,
		ObservedAt: input.ObservedAt,
	}, serverTime) || len(input.Items) > maxConnectorItems {
		return ConnectorDiscoveryInput{}, ErrInvalidInput
	}
	input.ObservedAt = input.ObservedAt.UTC()
	input.DaemonID = strings.TrimSpace(input.DaemonID)
	input.Items = slices.Clone(input.Items)
	identities := make(map[string]struct{}, len(input.Items))
	for index := range input.Items {
		item, err := normalizeConnectorObservation(provider, input.Items[index])
		if err != nil {
			return ConnectorDiscoveryInput{}, err
		}
		identity := string(item.Source) + "\x00" + item.NativeID
		if _, duplicate := identities[identity]; duplicate {
			return ConnectorDiscoveryInput{}, ErrInvalidInput
		}
		identities[identity] = struct{}{}
		input.Items[index] = item
	}
	slices.SortFunc(input.Items, func(left, right ConnectorObservationInput) int {
		if order := strings.Compare(string(left.Source), string(right.Source)); order != 0 {
			return order
		}
		return strings.Compare(left.NativeID, right.NativeID)
	})
	digest, err := connectorManifestDigest(input.Items)
	if err != nil || digest != input.ManifestDigest {
		return ConnectorDiscoveryInput{}, ErrInvalidInput
	}
	return input, nil
}

// normalizeConnectorDiscoveryReaffirmation applies the generation rules of a
// full publication to a reaffirmation, which carries no items to digest.
func normalizeConnectorDiscoveryReaffirmation(
	provider ConnectorProvider,
	input ConnectorDiscoveryReaffirmInput,
	serverTime time.Time,
) (ConnectorDiscoveryReaffirmInput, error) {
	if !validConnectorGeneration(provider, input, serverTime) {
		return ConnectorDiscoveryReaffirmInput{}, ErrInvalidInput
	}
	input.ObservedAt = input.ObservedAt.UTC()
	input.DaemonID = strings.TrimSpace(input.DaemonID)
	return input, nil
}

// validConnectorGeneration is the one rule for a generation's identity and
// observation time, shared by publication and reaffirmation so the two cannot
// drift: a daemon clock ahead of the server by more than the skew is refused.
func validConnectorGeneration(
	provider ConnectorProvider,
	generation ConnectorDiscoveryReaffirmInput,
	serverTime time.Time,
) bool {
	return validConnectorProvider(provider) && util.ValidUUID(generation.CompanyID) &&
		util.ValidUUID(generation.RuntimeID) && strings.TrimSpace(generation.DaemonID) != "" &&
		util.ValidUUID(generation.Generation) && validConnectorDigest(generation.ManifestDigest) &&
		!generation.ObservedAt.IsZero() &&
		!generation.ObservedAt.After(serverTime.Add(connectorGenerationClockSkew))
}

func normalizeConnectorObservation(
	provider ConnectorProvider,
	item ConnectorObservationInput,
) (ConnectorObservationInput, error) {
	item.Name = normalizeConnectorText(item.Name)
	item.Description = normalizeOptionalConnectorText(item.Description)
	item.Category = normalizeOptionalConnectorText(item.Category)
	item.IconURL = normalizeOptionalConnectorText(item.IconURL)
	if item.InstallCount != nil {
		installCount := *item.InstallCount
		item.InstallCount = &installCount
	}
	contractDigest, contractErr := agent.ConnectorToolContractDigest(item.Tools)
	hasContract := item.ContractDigest != "" || item.Tools != nil
	if !validConnectorSource(provider, item.Source) ||
		!validConnectorText(item.NativeID, maxConnectorNativeBytes) ||
		item.NativeID != strings.TrimSpace(item.NativeID) ||
		!validConnectorText(item.Name, maxConnectorNameBytes) ||
		!validOptionalConnectorText(item.Description, maxConnectorDetailBytes) ||
		!validOptionalConnectorText(item.Category, maxConnectorCategoryBytes) ||
		item.InstallCount != nil && (item.Source != ConnectorSourceClaudePlugin ||
			*item.InstallCount < 0 || *item.InstallCount > maxSafeInteger) ||
		!validConnectorStatus(item.Status) || !validConnectorAction(item.Action) ||
		item.CanExecute && (!selectiveConnectorSource(provider, item.Source) ||
			item.Status != ConnectorStatusConnected || contractErr != nil ||
			contractDigest != item.ContractDigest) ||
		!item.CanExecute && hasContract || !validConnectorIconURL(item.IconURL) {
		return ConnectorObservationInput{}, ErrInvalidInput
	}
	if item.CanExecute {
		item.Tools = slices.Clone(item.Tools)
		slices.SortFunc(item.Tools, func(left, right agent.ConnectorTool) int {
			return strings.Compare(left.Name, right.Name)
		})
	}
	return item, nil
}

func projectConnectorCategories(
	provider ConnectorProvider,
	items []ConnectorObservationInput,
) []ConnectorObservationInput {
	projected := slices.Clone(items)
	for index := range projected {
		category := canonicalConnectorCategory(provider, projected[index])
		categoryText := string(category)
		projected[index].Category = &categoryText
	}
	return projected
}

func canonicalConnectorCategory(
	provider ConnectorProvider,
	item ConnectorObservationInput,
) connectorCategory {
	identity := connectorCategoryIdentity{
		Provider: provider,
		Source:   item.Source,
		NativeID: item.NativeID,
	}
	if category, overridden := connectorCategoryOverrides[identity]; overridden {
		return category
	}
	if provider != ConnectorProviderCodex || item.Source != ConnectorSourceCodexApp {
		return connectorCategoryOther
	}

	return canonicalConnectorCategoryLabel(optionalConnectorText(item.Category))
}

// canonicalConnectorCategoryLabel keeps the read projection compatible with
// catalogs written before Boardly introduced its closed browse taxonomy. New
// discovery generations are already canonicalized before persistence, while
// legacy or unknown provider labels safely collapse into Other.
func canonicalConnectorCategoryLabel(rawCategory string) connectorCategory {
	switch strings.ToLower(strings.TrimSpace(rawCategory)) {
	case "productivity":
		return connectorCategoryProductivity
	case "creativity", "design":
		return connectorCategoryCreativity
	case "developer tools":
		return connectorCategoryDeveloperTools
	case "business & operations", "business":
		return connectorCategoryBusinessOperations
	case "data & analytics":
		return connectorCategoryDataAnalytics
	case "communication", "collaboration":
		return connectorCategoryCommunication
	case "education & research", "education":
		return connectorCategoryEducationResearch
	case "security":
		return connectorCategorySecurity
	case "finance":
		return connectorCategoryFinance
	case "healthcare":
		return connectorCategoryHealthcare
	case "travel":
		return connectorCategoryTravel
	case "entertainment":
		return connectorCategoryEntertainment
	case "other":
		return connectorCategoryOther
	default:
		return connectorCategoryOther
	}
}

func validConnectorCategory(category connectorCategory) bool {
	return slices.Contains(connectorCategoryOrder[:], category)
}

func normalizeConnectorResolveInput(
	provider ConnectorProvider,
	input ConnectorResolveInput,
) (ConnectorResolveInput, error) {
	if !validConnectorProvider(provider) || len(input.Identities) < 1 ||
		len(input.Identities) > maxConnectorResolveIdentities {
		return ConnectorResolveInput{}, ErrInvalidInput
	}
	input.Identities = slices.Clone(input.Identities)
	seen := make(map[string]struct{}, len(input.Identities))
	for _, identity := range input.Identities {
		if !validConnectorSource(provider, identity.Source) ||
			!validConnectorText(identity.NativeID, maxConnectorNativeBytes) ||
			identity.NativeID != strings.TrimSpace(identity.NativeID) {
			return ConnectorResolveInput{}, ErrInvalidInput
		}
		key := string(identity.Source) + "\x00" + identity.NativeID
		if _, duplicate := seen[key]; duplicate {
			return ConnectorResolveInput{}, ErrInvalidInput
		}
		seen[key] = struct{}{}
	}
	return input, nil
}

func normalizeConnectorListInput(input ConnectorListInput) (ConnectorListInput, error) {
	input.Query = normalizeConnectorText(input.Query)
	input.Category = normalizeConnectorText(input.Category)
	if input.Limit == 0 {
		input.Limit = defaultConnectorPageSize
	}
	if input.Source != "" &&
		(!validConnectorSource(ConnectorProviderCodex, input.Source) &&
			!validConnectorSource(ConnectorProviderClaude, input.Source) ||
			input.Provider != "" && !validConnectorSource(input.Provider, input.Source)) {
		return ConnectorListInput{}, ErrInvalidInput
	}
	if input.RuntimeID != nil && !util.ValidUUID(*input.RuntimeID) ||
		input.Provider != "" && !validConnectorProvider(input.Provider) ||
		len(input.Query) > maxConnectorDetailBytes || len(input.Category) > maxConnectorCategoryBytes ||
		input.Category != "" && !validConnectorCategory(connectorCategory(input.Category)) ||
		input.Status != "" && !validConnectorStatus(input.Status) ||
		input.Limit < 1 || input.Limit > maxConnectorPageSize ||
		input.Cursor != nil && (!util.ValidUUID(input.Cursor.ID) ||
			input.Cursor.Name == "" || input.Cursor.Name != strings.ToLower(input.Cursor.Name) ||
			!validConnectorText(input.Cursor.Name, maxConnectorNameBytes)) {
		return ConnectorListInput{}, ErrInvalidInput
	}
	return input, nil
}

func FormatConnectorCursor(cursor ConnectorCursor) (string, error) {
	if cursor.Name == "" || cursor.Name != strings.ToLower(cursor.Name) ||
		!validConnectorText(cursor.Name, maxConnectorNameBytes) || !util.ValidUUID(cursor.ID) {
		return "", ErrInvalidInput
	}
	nameBytes := []byte(cursor.Name)
	encoded := make([]byte, 3+len(nameBytes)+len(cursor.ID))
	encoded[0] = connectorCursorVersion
	encoded[1] = byte(len(nameBytes) >> 8)
	encoded[2] = byte(len(nameBytes))
	copy(encoded[3:], nameBytes)
	copy(encoded[3+len(nameBytes):], cursor.ID[:])
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func ParseConnectorCursor(raw string) (*ConnectorCursor, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > 256 {
		return nil, ErrInvalidInput
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil || len(decoded) < 3+16 || decoded[0] != connectorCursorVersion {
		return nil, ErrInvalidInput
	}
	nameLength := int(decoded[1])<<8 | int(decoded[2])
	if nameLength == 0 || len(decoded) != 3+nameLength+16 {
		return nil, ErrInvalidInput
	}
	id, err := uuid.FromBytes(decoded[3+nameLength:])
	if err != nil {
		return nil, ErrInvalidInput
	}
	cursor := ConnectorCursor{Name: string(decoded[3 : 3+nameLength]), ID: id}
	canonical, err := FormatConnectorCursor(cursor)
	if err != nil || !bytes.Equal([]byte(canonical), []byte(raw)) {
		return nil, ErrInvalidInput
	}
	return &cursor, nil
}

func validConnectorProvider(provider ConnectorProvider) bool {
	return provider == ConnectorProviderCodex || provider == ConnectorProviderClaude
}

func validConnectorSource(provider ConnectorProvider, source ConnectorSource) bool {
	switch provider {
	case ConnectorProviderCodex:
		return source == ConnectorSourceCodexApp || source == ConnectorSourceCodexMCP
	case ConnectorProviderClaude:
		return source == ConnectorSourceClaudePlugin || source == ConnectorSourceClaudeMCP ||
			source == ConnectorSourceClaudeCloud
	default:
		return false
	}
}

func selectiveConnectorSource(provider ConnectorProvider, source ConnectorSource) bool {
	switch provider {
	case ConnectorProviderCodex:
		return source == ConnectorSourceCodexApp
	case ConnectorProviderClaude:
		return source == ConnectorSourceClaudePlugin || source == ConnectorSourceClaudeMCP
	default:
		return false
	}
}

func validConnectorStatus(status ConnectorStatus) bool {
	switch status {
	case ConnectorStatusAvailable, ConnectorStatusInstalledNotReady, ConnectorStatusNeedsAuth,
		ConnectorStatusConnected, ConnectorStatusManagedInProvider, ConnectorStatusUnavailable,
		ConnectorStatusError:
		return true
	default:
		return false
	}
}

func validConnectorAction(action ConnectorAction) bool {
	switch action {
	case ConnectorActionInstallAndAuth, ConnectorActionAuthenticate,
		ConnectorActionManageInProvider, ConnectorActionNone:
		return true
	default:
		return false
	}
}

func validConnectorDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func normalizeConnectorText(value string) string {
	return strings.TrimSpace(value)
}

func normalizeOptionalConnectorText(value *string) *string {
	if value == nil {
		return nil
	}
	normalized := normalizeConnectorText(*value)
	if normalized == "" {
		return nil
	}
	return &normalized
}

func validConnectorText(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validOptionalConnectorText(value *string, maxBytes int) bool {
	return value == nil || validConnectorText(*value, maxBytes)
}

func validConnectorIconURL(value *string) bool {
	if value == nil {
		return true
	}
	if !validConnectorText(*value, maxConnectorIconURLBytes) {
		return false
	}
	parsed, err := url.ParseRequestURI(*value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.Fragment != "" || parsed.Opaque != "" {
		return false
	}
	if parsed.RawQuery == "" {
		return true
	}
	return validOpenAIConnectorIconAssetURL(parsed)
}

func validOpenAIConnectorIconAssetURL(parsed *url.URL) bool {
	if parsed.Port() != "" {
		return false
	}
	switch {
	case strings.EqualFold(parsed.Hostname(), "files.openai.com") && parsed.Path == "/content":
	case strings.EqualFold(parsed.Hostname(), "chatgpt.com") &&
		parsed.Path == "/backend-api/estuary/content":
	default:
		return false
	}

	query := parsed.Query()
	for key, values := range query {
		if !validOpenAIConnectorIconQueryKey(key) || len(values) != 1 || values[0] == "" {
			return false
		}
	}
	return len(query["id"]) == 1 && len(query["sig"]) == 1
}

func validOpenAIConnectorIconQueryKey(key string) bool {
	switch key {
	case "cdn", "cid", "cp", "id", "ma", "p", "sig", "ts", "v":
		return true
	default:
		return false
	}
}

func connectorManifestDigest(items []ConnectorObservationInput) (string, error) {
	type digestItem struct {
		Source         ConnectorSource       `json:"source"`
		NativeID       string                `json:"nativeId"`
		Name           string                `json:"name"`
		Description    string                `json:"description,omitempty"`
		Category       string                `json:"category,omitempty"`
		IconURL        string                `json:"iconUrl,omitempty"`
		InstallCount   *int64                `json:"installCount,omitempty"`
		Status         ConnectorStatus       `json:"status"`
		Action         ConnectorAction       `json:"action"`
		CanExecute     bool                  `json:"canExecute"`
		ContractDigest string                `json:"contractDigest,omitempty"`
		Tools          []agent.ConnectorTool `json:"tools,omitempty"`
	}
	canonical := make([]digestItem, len(items))
	for index, item := range items {
		canonical[index] = digestItem{
			Source: item.Source, NativeID: item.NativeID, Name: item.Name,
			Description: optionalConnectorText(item.Description),
			Category:    optionalConnectorText(item.Category), IconURL: optionalConnectorText(item.IconURL),
			InstallCount: item.InstallCount,
			Status:       item.Status, Action: item.Action, CanExecute: item.CanExecute,
			ContractDigest: item.ContractDigest, Tools: item.Tools,
		}
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func optionalConnectorText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
