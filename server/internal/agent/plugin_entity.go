package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
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

	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
)

const (
	PluginSchemaVersion = 1

	defaultPluginPageSize        = 50
	maxPluginPageSize            = 100
	maxPluginItems               = 10000
	maxPluginComponentCount      = 2048
	maxPluginConnectorIdentities = maxPluginComponentCount * 2
	maxPluginNativeBytes         = 240
	maxPluginNameBytes           = 160
	maxPluginDetailBytes         = 1000
	maxPluginCategoryBytes       = 80
	maxPluginURLBytes            = 2048
	pluginCursorVersion          = byte(1)
	pluginDiscoveryStaleAfter    = 10 * time.Minute
	pluginGenerationClockSkew    = 5 * time.Minute
	maximumPluginCatalogRank     = maxPluginItems - 1
)

var (
	ErrPluginDiscoveryConflict   = errors.New("Plugin discovery generation conflicts with current state")
	ErrPluginNotFound            = errors.New("Plugin was not found")
	ErrPluginProviderUnavailable = errors.New("Company Plugin provider is unavailable")
	ErrPluginActionUnavailable   = errors.New("Plugin action is unavailable")
)

type PluginStatus string

const (
	PluginStatusAvailable PluginStatus = "available"
	PluginStatusInstalled PluginStatus = "installed"
	PluginStatusDisabled  PluginStatus = "disabled"
	PluginStatusError     PluginStatus = "error"
)

type PluginAction string

const (
	PluginActionInstall   PluginAction = "install"
	PluginActionUninstall PluginAction = "uninstall"
	PluginActionNone      PluginAction = "none"
)

type PluginComponentCounts struct {
	Apps       int `json:"apps"`
	MCPServers int `json:"mcpServers"`
	Skills     int `json:"skills"`
}

// PluginConnectorIdentity links one presentation Plugin package to an exact
// Connector discovered independently by the same Runtime.
type PluginConnectorIdentity struct {
	Source   ConnectorSource `json:"source"`
	NativeID string          `json:"nativeId"`
}

type PluginObservationInput struct {
	NativeID            string
	Name                string
	Description         string
	Category            string
	IconURL             string
	ShareURL            string
	Status              PluginStatus
	Installed           bool
	Enabled             bool
	Featured            bool
	Action              PluginAction
	CatalogRank         int
	ComponentCounts     PluginComponentCounts
	ConnectorIdentities []PluginConnectorIdentity
	InstallCount        *int64
	// The Codex presentation, sent only by a daemon that saw the
	// server advertise plugin-presentation-v1.
	LongDescription   string
	DefaultPrompts    []string
	BrandColor        string
	DeveloperName     string
	WebsiteURL        string
	PrivacyPolicyURL  string
	TermsOfServiceURL string
}

type PluginDiscoveryInput struct {
	CompanyID      uuid.UUID
	RuntimeID      uuid.UUID
	DaemonID       string
	Generation     uuid.UUID
	ManifestDigest string
	ObservedAt     time.Time
	Items          []PluginObservationInput
}

type PluginDiscoveryResult struct {
	Applied bool `json:"applied"`
}

type PluginItemV1 struct {
	ID                  uuid.UUID                 `json:"id"`
	Provider            ConnectorProvider         `json:"provider"`
	NativeID            string                    `json:"nativeId"`
	Name                string                    `json:"name"`
	Description         *string                   `json:"description,omitempty"`
	Category            *string                   `json:"category,omitempty"`
	IconURL             *string                   `json:"iconUrl,omitempty"`
	ShareURL            *string                   `json:"shareUrl,omitempty"`
	Status              PluginStatus              `json:"status"`
	Installed           bool                      `json:"installed"`
	Enabled             bool                      `json:"enabled"`
	Featured            bool                      `json:"featured"`
	Action              PluginAction              `json:"action"`
	InstallCount        *int64                    `json:"installCount,omitempty"`
	ComponentCounts     PluginComponentCounts     `json:"componentCounts"`
	ConnectorIdentities []PluginConnectorIdentity `json:"connectorIdentities"`
	ObservedAt          string                    `json:"observedAt"`
}

type PluginRuntimeSummary struct {
	ID     uuid.UUID `json:"id"`
	Name   string    `json:"name"`
	Online bool      `json:"online"`
}

type PluginCategoryCount struct {
	Category string `json:"category"`
	Count    int64  `json:"count"`
}

type PluginCatalogResult struct {
	SchemaVersion  int                   `json:"schemaVersion"`
	ActiveProvider ConnectorProvider     `json:"activeProvider"`
	Runtime        *PluginRuntimeSummary `json:"runtime"`
	Plugins        []PluginItemV1        `json:"plugins"`
	NextCursor     *string               `json:"nextCursor"`
	TotalCount     int64                 `json:"totalCount"`
	InstalledCount int64                 `json:"installedCount"`
	CategoryCounts []PluginCategoryCount `json:"categoryCounts"`
	Stale          bool                  `json:"stale"`
	// ObservedAt is when the selected Runtime last reported its catalog; nil
	// before its first discovery.
	ObservedAt *time.Time `json:"observedAt"`
}

type PluginCursor struct {
	Rank int
	ID   uuid.UUID
}

// PluginDetailApp is one app a Plugin bundles, resolved from its Connector
// identities against the selected Runtime's Connector observations. The
// Connector id and state are present only when that Runtime observed it.
type PluginDetailApp struct {
	Source      ConnectorSource  `json:"source"`
	NativeID    string           `json:"nativeId"`
	ConnectorID *uuid.UUID       `json:"connectorId,omitempty"`
	Name        string           `json:"name"`
	Description *string          `json:"description,omitempty"`
	IconURL     *string          `json:"iconUrl,omitempty"`
	Status      *ConnectorStatus `json:"status,omitempty"`
	Action      *ConnectorAction `json:"action,omitempty"`
}

// PluginAgentUse is an unarchived Agent the viewer runs on the selected
// Runtime (their binding, ADR 0014) that can use the Plugin: installed and
// enabled there, and not turned off for the Agent.
type PluginAgentUse struct {
	AgentID uuid.UUID `json:"agentId"`
	Name    string    `json:"name"`
}

// PluginDetailResult is one Plugin's page (wire name PluginDetailResponseV1).
type PluginDetailResult struct {
	SchemaVersion     int                   `json:"schemaVersion"`
	Plugin            PluginItemV1          `json:"plugin"`
	Runtime           *PluginRuntimeSummary `json:"runtime"`
	Stale             bool                  `json:"stale"`
	LongDescription   *string               `json:"longDescription,omitempty"`
	DefaultPrompts    []string              `json:"defaultPrompts"`
	BrandColor        *string               `json:"brandColor,omitempty"`
	DeveloperName     *string               `json:"developerName,omitempty"`
	WebsiteURL        *string               `json:"websiteUrl,omitempty"`
	PrivacyPolicyURL  *string               `json:"privacyPolicyUrl,omitempty"`
	TermsOfServiceURL *string               `json:"termsOfServiceUrl,omitempty"`
	Apps              []PluginDetailApp     `json:"apps"`
	UsedBy            []PluginAgentUse      `json:"usedBy"`
}

// PluginDetailInput names one Company Plugin and, optionally, the Runtime
// whose observation to read; without one the default eligible Runtime of the
// Plugin's provider is read, as the catalog selects it.
type PluginDetailInput struct {
	PluginID  uuid.UUID
	RuntimeID *uuid.UUID
}

type PluginListInput struct {
	RuntimeID *uuid.UUID
	Provider  ConnectorProvider
	Query     string
	Category  string
	Status    PluginStatus
	Featured  bool
	Cursor    *PluginCursor
	Limit     int
}

func normalizePluginDiscovery(
	provider ConnectorProvider,
	input PluginDiscoveryInput,
	serverTime time.Time,
) (PluginDiscoveryInput, error) {
	if !validConnectorProvider(provider) || !util.ValidUUID(input.CompanyID) ||
		!util.ValidUUID(input.RuntimeID) || strings.TrimSpace(input.DaemonID) == "" ||
		!util.ValidUUID(input.Generation) ||
		!validConnectorDigest(input.ManifestDigest) || input.ObservedAt.IsZero() ||
		input.ObservedAt.After(serverTime.Add(pluginGenerationClockSkew)) ||
		len(input.Items) > maxPluginItems {
		return PluginDiscoveryInput{}, ErrInvalidInput
	}
	input.ObservedAt = input.ObservedAt.UTC()
	input.DaemonID = strings.TrimSpace(input.DaemonID)
	input.Items = slices.Clone(input.Items)
	identities := make(map[string]struct{}, len(input.Items))
	ranks := make(map[int]struct{}, len(input.Items))
	for index := range input.Items {
		item, err := normalizePluginObservation(provider, input.Items[index])
		if err != nil {
			return PluginDiscoveryInput{}, err
		}
		if _, duplicate := identities[item.NativeID]; duplicate {
			return PluginDiscoveryInput{}, ErrInvalidInput
		}
		if _, duplicate := ranks[item.CatalogRank]; duplicate {
			return PluginDiscoveryInput{}, ErrInvalidInput
		}
		identities[item.NativeID] = struct{}{}
		ranks[item.CatalogRank] = struct{}{}
		input.Items[index] = item
	}
	for rank := range len(input.Items) {
		if _, present := ranks[rank]; !present {
			return PluginDiscoveryInput{}, ErrInvalidInput
		}
	}
	slices.SortFunc(input.Items, comparePluginObservation)
	digest, err := pluginManifestDigest(input.Items)
	if err != nil || digest != input.ManifestDigest {
		return PluginDiscoveryInput{}, ErrInvalidInput
	}
	for index := range input.Items {
		input.Items[index].Category = string(canonicalPluginCategory(provider, input.Items[index]))
	}
	return input, nil
}

func normalizePluginObservation(
	provider ConnectorProvider,
	item PluginObservationInput,
) (PluginObservationInput, error) {
	item.Name = normalizeConnectorText(item.Name)
	item.Description = normalizeConnectorText(item.Description)
	item.Category = normalizeConnectorText(item.Category)
	item.IconURL = normalizeConnectorText(item.IconURL)
	item.ShareURL = normalizeConnectorText(item.ShareURL)
	item.LongDescription = normalizeConnectorText(item.LongDescription)
	item.BrandColor = normalizeConnectorText(item.BrandColor)
	item.DeveloperName = normalizeConnectorText(item.DeveloperName)
	item.WebsiteURL = normalizeConnectorText(item.WebsiteURL)
	item.PrivacyPolicyURL = normalizeConnectorText(item.PrivacyPolicyURL)
	item.TermsOfServiceURL = normalizeConnectorText(item.TermsOfServiceURL)
	if item.DefaultPrompts != nil {
		prompts := make([]string, len(item.DefaultPrompts))
		for index, prompt := range item.DefaultPrompts {
			prompts[index] = normalizeConnectorText(prompt)
		}
		item.DefaultPrompts = prompts
	}
	if item.InstallCount != nil {
		installCount := *item.InstallCount
		item.InstallCount = &installCount
	}
	connectorIdentities, err := normalizePluginConnectorIdentities(
		provider,
		item.ConnectorIdentities,
	)
	if err != nil {
		return PluginObservationInput{}, err
	}
	item.ConnectorIdentities = connectorIdentities
	if !validConnectorText(item.NativeID, maxPluginNativeBytes) ||
		item.NativeID != strings.TrimSpace(item.NativeID) ||
		!validConnectorText(item.Name, maxPluginNameBytes) ||
		!validOptionalPluginText(item.Description, maxPluginDetailBytes) ||
		!validOptionalPluginText(item.Category, maxPluginCategoryBytes) ||
		!validPluginIconURL(item.IconURL) || !validPluginShareURL(provider, item.ShareURL) ||
		item.InstallCount != nil && (provider != ConnectorProviderClaude ||
			*item.InstallCount < 0 || *item.InstallCount > maxSafeInteger) ||
		item.CatalogRank < 0 || item.CatalogRank > maximumPluginCatalogRank ||
		!validPluginState(item) || !validPluginComponentCounts(item.ComponentCounts) ||
		!validPluginPresentation(provider, item) {
		return PluginObservationInput{}, ErrInvalidInput
	}
	return item, nil
}

// validPluginPresentation admits the Codex presentation fields exactly as the
// daemon sends them, so the manifest digest it computed still matches; a
// Claude observation carries none. Lengths count characters, as the daemon's
// bounds do.
func validPluginPresentation(provider ConnectorProvider, item PluginObservationInput) bool {
	if provider != ConnectorProviderCodex {
		return item.LongDescription == "" && len(item.DefaultPrompts) == 0 &&
			item.BrandColor == "" && item.DeveloperName == "" && item.WebsiteURL == "" &&
			item.PrivacyPolicyURL == "" && item.TermsOfServiceURL == ""
	}
	if len(item.DefaultPrompts) > 3 {
		return false
	}
	for _, prompt := range item.DefaultPrompts {
		if !validPluginLineText(prompt, 128) {
			return false
		}
	}
	return validPluginLongDescription(item.LongDescription) &&
		validPluginBrandColor(item.BrandColor) &&
		(item.DeveloperName == "" || validPluginLineText(item.DeveloperName, 120)) &&
		validPluginLinkURL(item.WebsiteURL) && validPluginLinkURL(item.PrivacyPolicyURL) &&
		validPluginLinkURL(item.TermsOfServiceURL)
}

// validPluginLineText is one non-empty line of at most maxRunes characters.
func validPluginLineText(value string, maxRunes int) bool {
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > maxRunes {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

// validPluginLongDescription keeps paragraphs: a newline is the one control
// character allowed, at most two in a row, within 4000 characters.
func validPluginLongDescription(value string) bool {
	if value == "" {
		return true
	}
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 4000 ||
		strings.Contains(value, "\n\n\n") {
		return false
	}
	for _, character := range value {
		if character != '\n' && unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validPluginBrandColor(value string) bool {
	if value == "" {
		return true
	}
	if len(value) != 7 || value[0] != '#' {
		return false
	}
	for _, character := range value[1:] {
		if !strings.ContainsRune("0123456789abcdefABCDEF", character) {
			return false
		}
	}
	return true
}

// validPluginLinkURL is a developer's page: https, without credentials or a
// fragment, within 2048 characters.
func validPluginLinkURL(value string) bool {
	if value == "" {
		return true
	}
	if !validPluginLineText(value, 2048) || strings.Contains(value, "#") {
		return false
	}
	parsed, err := url.ParseRequestURI(value)
	return err == nil && parsed.Scheme == "https" && parsed.Hostname() != "" &&
		parsed.User == nil && parsed.Opaque == ""
}

func normalizePluginConnectorIdentities(
	provider ConnectorProvider,
	identities []PluginConnectorIdentity,
) ([]PluginConnectorIdentity, error) {
	if len(identities) > maxPluginConnectorIdentities {
		return nil, ErrInvalidInput
	}
	canonical := slices.Clone(identities)
	slices.SortFunc(canonical, func(left, right PluginConnectorIdentity) int {
		if bySource := strings.Compare(string(left.Source), string(right.Source)); bySource != 0 {
			return bySource
		}
		return strings.Compare(left.NativeID, right.NativeID)
	})
	for index, identity := range canonical {
		if !validPluginConnectorSource(provider, identity.Source) ||
			!validConnectorText(identity.NativeID, maxConnectorNativeBytes) ||
			identity.NativeID != strings.TrimSpace(identity.NativeID) {
			return nil, ErrInvalidInput
		}
		if index > 0 && canonical[index-1] == identity {
			return nil, ErrInvalidInput
		}
	}
	if canonical == nil {
		return []PluginConnectorIdentity{}, nil
	}
	return canonical, nil
}

func validPluginConnectorSource(
	provider ConnectorProvider,
	source ConnectorSource,
) bool {
	switch provider {
	case ConnectorProviderCodex:
		return source == ConnectorSourceCodexApp || source == ConnectorSourceCodexMCP
	case ConnectorProviderClaude:
		return source == ConnectorSourceClaudePlugin
	default:
		return false
	}
}

func canonicalPluginCategory(
	provider ConnectorProvider,
	item PluginObservationInput,
) connectorCategory {
	if provider == ConnectorProviderClaude {
		identity := connectorCategoryKey(provider, ConnectorSourceClaudePlugin, item.NativeID)
		if category, overridden := connectorCategoryOverrides[identity]; overridden {
			return category
		}
	}
	return canonicalConnectorCategoryLabel(item.Category)
}

func comparePluginObservation(left, right PluginObservationInput) int {
	if left.CatalogRank < right.CatalogRank {
		return -1
	}
	if left.CatalogRank > right.CatalogRank {
		return 1
	}
	return strings.Compare(left.NativeID, right.NativeID)
}

func normalizePluginListInput(input PluginListInput) (PluginListInput, error) {
	input.Query = normalizeConnectorText(input.Query)
	input.Category = normalizeConnectorText(input.Category)
	if input.Limit == 0 {
		input.Limit = defaultPluginPageSize
	}
	if input.RuntimeID != nil && !util.ValidUUID(*input.RuntimeID) ||
		input.Provider != "" && !validConnectorProvider(input.Provider) ||
		len(input.Query) > maxPluginDetailBytes || len(input.Category) > maxPluginCategoryBytes ||
		input.Category != "" && !validConnectorCategory(connectorCategory(input.Category)) ||
		input.Status != "" && !validPluginStatus(input.Status) ||
		input.Limit < 1 || input.Limit > maxPluginPageSize ||
		input.Cursor != nil && (input.Cursor.Rank < 0 ||
			input.Cursor.Rank > maximumPluginCatalogRank || !util.ValidUUID(input.Cursor.ID)) {
		return PluginListInput{}, ErrInvalidInput
	}
	return input, nil
}

func FormatPluginCursor(cursor PluginCursor) (string, error) {
	if cursor.Rank < 0 || cursor.Rank > maximumPluginCatalogRank ||
		!util.ValidUUID(cursor.ID) {
		return "", ErrInvalidInput
	}
	encoded := make([]byte, 1+4+len(cursor.ID))
	encoded[0] = pluginCursorVersion
	binary.BigEndian.PutUint32(encoded[1:5], uint32(cursor.Rank))
	copy(encoded[5:], cursor.ID[:])
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func ParsePluginCursor(raw string) (*PluginCursor, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > 64 {
		return nil, ErrInvalidInput
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil || len(decoded) != 1+4+16 || decoded[0] != pluginCursorVersion {
		return nil, ErrInvalidInput
	}
	id, err := uuid.FromBytes(decoded[5:])
	if err != nil {
		return nil, ErrInvalidInput
	}
	cursor := PluginCursor{Rank: int(binary.BigEndian.Uint32(decoded[1:5])), ID: id}
	canonical, err := FormatPluginCursor(cursor)
	if err != nil || !bytes.Equal([]byte(canonical), []byte(raw)) {
		return nil, ErrInvalidInput
	}
	return &cursor, nil
}

func validPluginStatus(status PluginStatus) bool {
	switch status {
	case PluginStatusAvailable, PluginStatusInstalled, PluginStatusDisabled, PluginStatusError:
		return true
	default:
		return false
	}
}

func validPluginAction(action PluginAction) bool {
	switch action {
	case PluginActionInstall, PluginActionUninstall, PluginActionNone:
		return true
	default:
		return false
	}
}

func validPluginState(item PluginObservationInput) bool {
	if !validPluginStatus(item.Status) || !validPluginAction(item.Action) {
		return false
	}
	switch item.Status {
	case PluginStatusAvailable:
		return !item.Installed && !item.Enabled && item.Action == PluginActionInstall
	case PluginStatusInstalled:
		return item.Installed && item.Enabled &&
			(item.Action == PluginActionUninstall || item.Action == PluginActionNone)
	case PluginStatusDisabled:
		return item.Installed && !item.Enabled &&
			(item.Action == PluginActionUninstall || item.Action == PluginActionNone)
	case PluginStatusError:
		return item.Action == PluginActionNone
	default:
		return false
	}
}

func validPluginComponentCounts(counts PluginComponentCounts) bool {
	values := [...]int{counts.Apps, counts.MCPServers, counts.Skills}
	for _, value := range values {
		if value < 0 || value > maxPluginComponentCount {
			return false
		}
	}
	return true
}

func validOptionalPluginText(value string, maxBytes int) bool {
	return value == "" || validConnectorText(value, maxBytes)
}

func validPluginIconURL(value string) bool {
	if value == "" {
		return true
	}
	if !validConnectorText(value, maxPluginURLBytes) {
		return false
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.Fragment != "" || parsed.Opaque != "" {
		return false
	}
	if parsed.RawQuery == "" {
		return true
	}
	return validOpenAIConnectorIconAssetURL(parsed)
}

func validPluginShareURL(provider ConnectorProvider, value string) bool {
	if value == "" {
		return true
	}
	if provider != ConnectorProviderCodex || !validConnectorText(value, maxPluginURLBytes) {
		return false
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" ||
		parsed.Fragment != "" || parsed.Opaque != "" ||
		!strings.EqualFold(parsed.Hostname(), "chatgpt.com") ||
		!strings.HasPrefix(parsed.EscapedPath(), "/plugins/") {
		return false
	}
	if parsed.RawQuery == "" {
		return true
	}
	query := parsed.Query()
	return len(query) == 1 && len(query["open_in_app"]) == 1 &&
		(query.Get("open_in_app") == "" || query.Get("open_in_app") == "true")
}

func pluginManifestDigest(items []PluginObservationInput) (string, error) {
	canonical := slices.Clone(items)
	slices.SortFunc(canonical, comparePluginObservation)
	encoded, err := json.Marshal(canonicalPluginDigestItems(canonical))
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func canonicalPluginDigestItems(items []PluginObservationInput) []pluginDigestItem {
	if items == nil {
		return nil
	}
	canonical := make([]pluginDigestItem, len(items))
	for index, item := range items {
		canonical[index] = pluginDigestItem{
			NativeID: item.NativeID, Name: item.Name, Description: item.Description,
			Category: item.Category, IconURL: item.IconURL, ShareURL: item.ShareURL,
			Status: item.Status, Installed: item.Installed, Enabled: item.Enabled,
			Featured: item.Featured, Action: item.Action, CatalogRank: item.CatalogRank,
			InstallCount:        item.InstallCount,
			ComponentCounts:     item.ComponentCounts,
			ConnectorIdentities: item.ConnectorIdentities,
			LongDescription:     item.LongDescription,
			DefaultPrompts:      item.DefaultPrompts,
			BrandColor:          item.BrandColor,
			DeveloperName:       item.DeveloperName,
			WebsiteURL:          item.WebsiteURL,
			PrivacyPolicyURL:    item.PrivacyPolicyURL,
			TermsOfServiceURL:   item.TermsOfServiceURL,
		}
	}
	return canonical
}

type pluginDigestItem struct {
	NativeID            string                    `json:"nativeId"`
	Name                string                    `json:"name"`
	Description         string                    `json:"description,omitempty"`
	Category            string                    `json:"category,omitempty"`
	IconURL             string                    `json:"iconUrl,omitempty"`
	ShareURL            string                    `json:"shareUrl,omitempty"`
	Status              PluginStatus              `json:"status"`
	Installed           bool                      `json:"installed"`
	Enabled             bool                      `json:"enabled"`
	Featured            bool                      `json:"featured"`
	Action              PluginAction              `json:"action"`
	CatalogRank         int                       `json:"catalogRank"`
	InstallCount        *int64                    `json:"installCount,omitempty"`
	ComponentCounts     PluginComponentCounts     `json:"componentCounts"`
	ConnectorIdentities []PluginConnectorIdentity `json:"connectorIdentities,omitempty"`
	// Mirrors the daemon's pluginDiscoveryItem, appended in its order.
	LongDescription   string   `json:"longDescription,omitempty"`
	DefaultPrompts    []string `json:"defaultPrompts,omitempty"`
	BrandColor        string   `json:"brandColor,omitempty"`
	DeveloperName     string   `json:"developerName,omitempty"`
	WebsiteURL        string   `json:"websiteUrl,omitempty"`
	PrivacyPolicyURL  string   `json:"privacyPolicyUrl,omitempty"`
	TermsOfServiceURL string   `json:"termsOfServiceUrl,omitempty"`
}
