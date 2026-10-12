package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

var (
	errPluginsUnavailable     = errors.New("plugins are unavailable")
	errPluginNotDiscovered    = errors.New("plugin was not discovered")
	errPluginActionInvalid    = errors.New("plugin action is invalid")
	errPluginIconUnavailable  = errors.New("plugin icon is unavailable")
	errPluginProviderMismatch = errors.New("plugin provider is not the runtime's")
)

type nativePluginComponentCounts struct {
	Apps       int
	MCPServers int
	Skills     int
}

// PluginConnectorIdentity links a provider plugin to one sanitized connector
// identity without exposing provider-local configuration.
type PluginConnectorIdentity struct {
	Source   string `json:"source"`
	NativeID string `json:"nativeId"`
}

type nativePluginItem struct {
	NativeID            string
	Name                string
	Description         string
	Category            string
	IconURL             string
	ShareURL            string
	Status              string
	Installed           bool
	Enabled             bool
	Featured            bool
	Action              string
	CatalogRank         int
	InstallCount        *int64
	ComponentCounts     nativePluginComponentCounts
	ConnectorIdentities []PluginConnectorIdentity
	// The presentation fields reach the cloud only after the server
	// advertises protocol.DaemonCapabilityPluginPresentationV1.
	LongDescription   string
	DefaultPrompts    []string
	BrandColor        string
	DeveloperName     string
	WebsiteURL        string
	PrivacyPolicyURL  string
	TermsOfServiceURL string
}

type nativePluginInstallResult struct {
	AwaitingUser bool
	ContinueURL  string
	// AuthPolicy and AppsNeedingAuth are the provider's own install answer,
	// when it gives one.
	AuthPolicy      string
	AppsNeedingAuth []pluginDetailApp
}

type nativePluginBackend interface {
	Discover(context.Context, bool) ([]nativePluginItem, error)
	Install(context.Context, string) (nativePluginInstallResult, error)
	Uninstall(context.Context, string) error
}

type nativePluginIconBackend interface {
	ReadIcon(context.Context, string) (nativePluginIcon, error)
}

type nativePluginIcon struct {
	ContentType string
	Bytes       []byte
}

type nativePluginDetailBackend interface {
	ReadDetail(context.Context, string) (pluginDetail, error)
}

// pluginDetail is the desktop plugin page's live read (POST /v1/plugins/read).
// Its lists are always present; every other optional value is omitted when
// the provider has none.
type pluginDetail struct {
	SchemaVersion     int      `json:"schemaVersion"`
	NativeID          string   `json:"nativeId"`
	Name              string   `json:"name"`
	LongDescription   string   `json:"longDescription,omitempty"`
	DefaultPrompts    []string `json:"defaultPrompts"`
	BrandColor        string   `json:"brandColor,omitempty"`
	DeveloperName     string   `json:"developerName,omitempty"`
	WebsiteURL        string   `json:"websiteUrl,omitempty"`
	PrivacyPolicyURL  string   `json:"privacyPolicyUrl,omitempty"`
	TermsOfServiceURL string   `json:"termsOfServiceUrl,omitempty"`
	ShareURL          string   `json:"shareUrl,omitempty"`
	// AuthPolicy ("on_install" or "on_use") and InstallationInterstitial tell
	// the desktop how to install: connect the plugin's app first, and whether
	// through the connect dialog or straight in the browser.
	AuthPolicy               string              `json:"authPolicy,omitempty"`
	InstallationInterstitial bool                `json:"installationInterstitial,omitempty"`
	Apps                     []pluginDetailApp   `json:"apps"`
	Skills                   []pluginDetailSkill `json:"skills"`
	MCPServers               []string            `json:"mcpServers"`
}

type pluginDetailApp struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Category    string `json:"category,omitempty"`
	IconURL     string `json:"iconUrl,omitempty"`
}

type pluginDetailSkill struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Description string `json:"description,omitempty"`
}

type nativePluginLifecycle interface {
	Close()
	SetChangeHandler(func())
}

type pluginDiscoveryPublisher interface {
	publishPluginDiscovery(
		context.Context,
		string,
		string,
		pluginDiscoveryManifest,
	) (discoveryPublishResult, error)
}

type pluginRuntimeAssignment struct {
	companyID     string
	runtimeID     string
	publishedHash string
}

type pluginOperation struct {
	SchemaVersion int    `json:"schemaVersion"`
	State         string `json:"state"`
	ContinueURL   string `json:"continueUrl,omitempty"`
	// AuthPolicy and AppsNeedingAuth come with an install the provider
	// answered them for: the desktop asks to connect those apps, not the
	// browser at once.
	AuthPolicy      string            `json:"authPolicy,omitempty"`
	AppsNeedingAuth []pluginDetailApp `json:"appsNeedingAuth,omitempty"`
}

type pluginIcon struct {
	ContentType string
	Bytes       []byte
}

type localPluginService struct {
	mu        sync.RWMutex
	refreshMu sync.Mutex
	publisher pluginDiscoveryPublisher
	backends  map[string]nativePluginBackend
	catalogs  map[string][]nativePluginItem
	runtimes  map[string]map[string]pluginRuntimeAssignment
	// presentation is whether the server's latest heartbeat ack advertised
	// protocol.DaemonCapabilityPluginPresentationV1; until then discovery
	// keeps the shape every server accepts.
	presentation bool
	refresh      *capabilityRefreshQueue
	now          func() time.Time
	generation   func() (string, error)
}

func newLocalPluginService(publisher pluginDiscoveryPublisher) *localPluginService {
	service := &localPluginService{
		publisher: publisher,
		backends:  make(map[string]nativePluginBackend),
		catalogs:  make(map[string][]nativePluginItem),
		runtimes:  make(map[string]map[string]pluginRuntimeAssignment),
		now:       time.Now,
		generation: func() (string, error) {
			id, err := uuid.NewV7()
			return id.String(), err
		},
	}
	// Plugin discovery has no keepalive: its staleness only drives a UI hint,
	// so only a forced refresh bypasses the cached catalog.
	service.refresh = newCapabilityRefreshQueue(
		func(ctx context.Context, provider string, refreshType capabilityRefreshType) error {
			return service.refreshProvider(ctx, provider, refreshType == capabilityRefreshTypeForced)
		},
	)
	return service
}

func (s *localPluginService) startBackground(ctx context.Context, report func(error)) func() {
	stopRefresh := s.refresh.start(ctx, report)
	return func() {
		stopRefresh()
		s.closeBackends()
	}
}

func (s *localPluginService) setBackend(provider string, backend nativePluginBackend) {
	s.mu.Lock()
	previous := s.backends[provider]
	if backend == nil {
		delete(s.backends, provider)
		delete(s.catalogs, provider)
		delete(s.runtimes, provider)
	} else {
		s.backends[provider] = backend
		delete(s.catalogs, provider)
	}
	s.mu.Unlock()
	closeNativePluginBackend(previous)
	if lifecycle, ok := backend.(nativePluginLifecycle); ok {
		lifecycle.SetChangeHandler(func() { _ = s.refresh.schedule(provider, capabilityRefreshTypeForced) })
	}
}

func (s *localPluginService) replaceRuntimes(runtimes []Runtime) {
	next := make(map[string]map[string]pluginRuntimeAssignment)
	s.mu.Lock()
	for _, runtime := range runtimes {
		if _, supported := s.backends[runtime.Provider]; !supported {
			continue
		}
		if next[runtime.Provider] == nil {
			next[runtime.Provider] = make(map[string]pluginRuntimeAssignment)
		}
		assignment := pluginRuntimeAssignment{companyID: runtime.CompanyID, runtimeID: runtime.ID}
		if previous, ok := s.runtimes[runtime.Provider][runtime.ID]; ok &&
			previous.companyID == runtime.CompanyID {
			assignment.publishedHash = previous.publishedHash
		}
		next[runtime.Provider][runtime.ID] = assignment
	}
	changed := make([]string, 0, len(s.backends))
	for provider := range s.backends {
		if !samePluginRuntimeAssignments(s.runtimes[provider], next[provider]) {
			changed = append(changed, provider)
		}
	}
	s.runtimes = next
	s.mu.Unlock()
	sort.Strings(changed)
	for _, provider := range changed {
		_ = s.refresh.schedule(provider, capabilityRefreshTypeChanged)
	}
}

// setPresentation records whether the server's latest heartbeat ack
// advertised plugin presentation. A change re-publishes every provider whose
// manifest it changes, so the server gets the fields without waiting for
// another discovery trigger (and stops getting them after a downgrade).
func (s *localPluginService) setPresentation(enabled bool) {
	s.mu.Lock()
	if s.presentation == enabled {
		s.mu.Unlock()
		return
	}
	s.presentation = enabled
	providers := make([]string, 0, len(s.backends))
	for provider := range s.backends {
		providers = append(providers, provider)
	}
	s.mu.Unlock()
	sort.Strings(providers)
	for _, provider := range providers {
		_ = s.refresh.schedule(provider, capabilityRefreshTypeChanged)
	}
}

func (s *localPluginService) refreshProvider(
	ctx context.Context,
	provider string,
	force bool,
) error {
	return s.syncProvider(ctx, provider, force, force)
}

// syncProvider publishes the provider's catalog, rediscovering it when asked
// or when none is known; a rediscovered catalog is a new observation, so it
// goes to every runtime. refetch also bypasses the provider's own caches,
// which for Codex refetches every remote marketplace.
func (s *localPluginService) syncProvider(
	ctx context.Context,
	provider string,
	rediscover, refetch bool,
) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	s.mu.RLock()
	backend := s.backends[provider]
	items, known := s.catalogs[provider]
	items = slices.Clone(items)
	presentation := s.presentation
	s.mu.RUnlock()
	if backend == nil {
		return errPluginsUnavailable
	}
	if rediscover || !known {
		var err error
		items, err = backend.Discover(ctx, refetch)
		if err != nil {
			return fmt.Errorf("discover %s plugins: %w", provider, err)
		}
		items, err = canonicalNativePluginItems(items)
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.catalogs[provider] = slices.Clone(items)
		s.mu.Unlock()
	}
	manifestItems := pluginManifestItems(items, presentation)
	digest := pluginManifestDigest(manifestItems)
	if digest == "" || len(manifestItems) > maximumPluginDiscoveryItems {
		return errors.New("plugin discovery manifest is invalid")
	}
	s.mu.RLock()
	assignments := make([]pluginRuntimeAssignment, 0, len(s.runtimes[provider]))
	for _, assignment := range s.runtimes[provider] {
		if rediscover || assignment.publishedHash != digest {
			assignments = append(assignments, assignment)
		}
	}
	s.mu.RUnlock()
	sort.Slice(assignments, func(left, right int) bool {
		return assignments[left].runtimeID < assignments[right].runtimeID
	})
	var publishErrors []error
	for _, assignment := range assignments {
		generation, err := s.generation()
		if err != nil {
			publishErrors = append(publishErrors, fmt.Errorf("generate plugin discovery: %w", err))
			continue
		}
		manifest := pluginDiscoveryManifest{
			SchemaVersion: 1, Generation: generation, ManifestDigest: digest,
			ObservedAt: s.now().UTC(), Items: manifestItems,
		}
		encoded, err := json.Marshal(manifest)
		if err != nil || len(encoded) > maximumPluginDiscoveryRequestBytes {
			publishErrors = append(
				publishErrors,
				errors.New("plugin discovery manifest exceeds transport limit"),
			)
			continue
		}
		if s.publisher != nil {
			if _, err = s.publisher.publishPluginDiscovery(
				ctx, assignment.companyID, assignment.runtimeID, manifest,
			); err != nil {
				publishErrors = append(publishErrors, err)
				continue
			}
		}
		s.markPublished(provider, assignment, digest)
	}
	return errors.Join(publishErrors...)
}

func (s *localPluginService) install(
	ctx context.Context,
	companyID, runtimeID, provider, nativeID string,
) (pluginOperation, error) {
	return s.mutate(ctx, companyID, runtimeID, provider, nativeID, "install")
}

func (s *localPluginService) uninstall(
	ctx context.Context,
	companyID, runtimeID, provider, nativeID string,
) (pluginOperation, error) {
	return s.mutate(ctx, companyID, runtimeID, provider, nativeID, "uninstall")
}

func (s *localPluginService) mutate(
	ctx context.Context,
	companyID, runtimeID, provider, nativeID, action string,
) (pluginOperation, error) {
	if !s.runtimeMatches(provider, companyID, runtimeID) {
		return pluginOperation{}, errPluginNotDiscovered
	}
	s.mu.RLock()
	backend := s.backends[provider]
	var catalogAction string
	for _, item := range s.catalogs[provider] {
		if item.NativeID == nativeID {
			catalogAction = item.Action
			break
		}
	}
	s.mu.RUnlock()
	if backend == nil {
		return pluginOperation{}, errPluginsUnavailable
	}
	if catalogAction == "" {
		return pluginOperation{}, errPluginNotDiscovered
	}
	if catalogAction != action {
		return pluginOperation{}, errPluginActionInvalid
	}
	operation := pluginOperation{SchemaVersion: 1, State: "complete"}
	if action == "install" {
		result, err := backend.Install(ctx, nativeID)
		if err != nil {
			return pluginOperation{}, err
		}
		if result.AwaitingUser {
			operation.State, operation.ContinueURL = "awaiting_user", result.ContinueURL
		}
		operation.AuthPolicy = result.AuthPolicy
		operation.AppsNeedingAuth = slices.Clone(result.AppsNeedingAuth)
	} else if err := backend.Uninstall(ctx, nativeID); err != nil {
		return pluginOperation{}, err
	}
	_ = s.refresh.schedule(provider, capabilityRefreshTypeForced)
	return operation, nil
}

// refreshRuntime always rediscovers and publishes; only a forced one makes
// the provider refetch its remote catalogs.
func (s *localPluginService) refreshRuntime(
	ctx context.Context,
	companyID, runtimeID, provider string,
	force bool,
) (pluginOperation, error) {
	if !s.runtimeMatches(provider, companyID, runtimeID) {
		return pluginOperation{}, errPluginNotDiscovered
	}
	if err := s.syncProvider(ctx, provider, true, force); err != nil {
		return pluginOperation{}, err
	}
	return pluginOperation{SchemaVersion: 1, State: "complete"}, nil
}

func (s *localPluginService) readIcon(
	ctx context.Context,
	companyID, runtimeID, provider, nativeID string,
) (pluginIcon, error) {
	if !s.runtimeMatches(provider, companyID, runtimeID) {
		return pluginIcon{}, errPluginNotDiscovered
	}
	s.mu.RLock()
	backend := s.backends[provider]
	known := slices.ContainsFunc(s.catalogs[provider], func(item nativePluginItem) bool {
		return item.NativeID == nativeID
	})
	s.mu.RUnlock()
	if !known {
		return pluginIcon{}, errPluginNotDiscovered
	}
	iconBackend, ok := backend.(nativePluginIconBackend)
	if !ok {
		return pluginIcon{}, errPluginIconUnavailable
	}
	icon, err := iconBackend.ReadIcon(ctx, nativeID)
	if err != nil {
		return pluginIcon{}, err
	}
	return pluginIcon{ContentType: icon.ContentType, Bytes: append([]byte(nil), icon.Bytes...)}, nil
}

// readDetail reads one discovered plugin live from its provider. A runtime
// this daemon serves for another provider is a conflict, not an unknown
// plugin, so the caller can tell a stale page from a wrong request.
func (s *localPluginService) readDetail(
	ctx context.Context,
	companyID, runtimeID, provider, nativeID string,
) (pluginDetail, error) {
	if !s.runtimeMatches(provider, companyID, runtimeID) {
		if s.runtimeServedByAnotherProvider(provider, companyID, runtimeID) {
			return pluginDetail{}, errPluginProviderMismatch
		}
		return pluginDetail{}, errPluginNotDiscovered
	}
	s.mu.RLock()
	backend := s.backends[provider]
	known := slices.ContainsFunc(s.catalogs[provider], func(item nativePluginItem) bool {
		return item.NativeID == nativeID
	})
	s.mu.RUnlock()
	if !known {
		return pluginDetail{}, errPluginNotDiscovered
	}
	detailBackend, ok := backend.(nativePluginDetailBackend)
	if !ok {
		return pluginDetail{}, errPluginsUnavailable
	}
	detail, err := detailBackend.ReadDetail(ctx, nativeID)
	if err != nil {
		return pluginDetail{}, err
	}
	detail.SchemaVersion = 1
	if detail.DefaultPrompts == nil {
		detail.DefaultPrompts = []string{}
	}
	if detail.Apps == nil {
		detail.Apps = []pluginDetailApp{}
	}
	if detail.Skills == nil {
		detail.Skills = []pluginDetailSkill{}
	}
	if detail.MCPServers == nil {
		detail.MCPServers = []string{}
	}
	return detail, nil
}

func (s *localPluginService) runtimeServedByAnotherProvider(
	provider, companyID, runtimeID string,
) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for other, assignments := range s.runtimes {
		if assignment, ok := assignments[runtimeID]; ok && other != provider &&
			assignment.companyID == companyID {
			return true
		}
	}
	return false
}

func canonicalNativePluginItems(items []nativePluginItem) ([]nativePluginItem, error) {
	if len(items) > maximumPluginDiscoveryItems {
		return nil, errors.New("plugin discovery item limit exceeded")
	}
	result := slices.Clone(items)
	sort.Slice(result, func(left, right int) bool {
		if result[left].CatalogRank == result[right].CatalogRank {
			return result[left].NativeID < result[right].NativeID
		}
		return result[left].CatalogRank < result[right].CatalogRank
	})
	seen := make(map[string]struct{}, len(result))
	for index, item := range result {
		if item.CatalogRank != index {
			return nil, errors.New("plugin discovery ranks are invalid")
		}
		if _, duplicate := seen[item.NativeID]; duplicate {
			return nil, errors.New("plugin discovery identities are duplicated")
		}
		seen[item.NativeID] = struct{}{}
	}
	return result, nil
}

// pluginManifestItems projects a catalog onto the discovery wire items. With
// presentation, the storefront fields ride along, trimmed until the encoded
// manifest fits in 6 MiB. The digest is computed over the result, so it
// always covers exactly what is sent.
func pluginManifestItems(items []nativePluginItem, presentation bool) []pluginDiscoveryItem {
	result := make([]pluginDiscoveryItem, len(items))
	for index, item := range items {
		identities := make([]pluginConnectorIdentity, len(item.ConnectorIdentities))
		for identityIndex, identity := range item.ConnectorIdentities {
			identities[identityIndex] = pluginConnectorIdentity(identity)
		}
		result[index] = pluginDiscoveryItem{
			NativeID: item.NativeID, Name: item.Name, Description: item.Description,
			Category: item.Category, IconURL: item.IconURL, ShareURL: item.ShareURL,
			Status: item.Status, Installed: item.Installed, Enabled: item.Enabled,
			Featured: item.Featured, Action: item.Action, CatalogRank: item.CatalogRank,
			InstallCount: item.InstallCount,
			ComponentCounts: pluginComponentCounts{
				Apps: item.ComponentCounts.Apps, MCPServers: item.ComponentCounts.MCPServers,
				Skills: item.ComponentCounts.Skills,
			},
			ConnectorIdentities: identities,
		}
		if presentation {
			result[index].LongDescription = item.LongDescription
			result[index].DefaultPrompts = slices.Clone(item.DefaultPrompts)
			result[index].BrandColor = item.BrandColor
			result[index].DeveloperName = item.DeveloperName
			result[index].WebsiteURL = item.WebsiteURL
			result[index].PrivacyPolicyURL = item.PrivacyPolicyURL
			result[index].TermsOfServiceURL = item.TermsOfServiceURL
		}
	}
	if presentation {
		fitPluginManifestPresentation(result)
	}
	return result
}

// fitPluginManifestPresentation sheds presentation until the manifest fits:
// first the long descriptions of plugins that are not installed, then every
// long description, then every presentation field, which leaves the shape
// discovery had before presentation existed.
func fitPluginManifestPresentation(items []pluginDiscoveryItem) {
	sheds := []func(*pluginDiscoveryItem){
		func(item *pluginDiscoveryItem) {
			if !item.Installed {
				item.LongDescription = ""
			}
		},
		func(item *pluginDiscoveryItem) { item.LongDescription = "" },
		func(item *pluginDiscoveryItem) {
			item.DefaultPrompts, item.BrandColor, item.DeveloperName = nil, "", ""
			item.WebsiteURL, item.PrivacyPolicyURL, item.TermsOfServiceURL = "", "", ""
		},
	}
	for _, shed := range sheds {
		if pluginManifestItemsFit(items) {
			return
		}
		for index := range items {
			shed(&items[index])
		}
	}
}

// pluginManifestItemsFit leaves 1 KiB of the 6 MiB for the manifest envelope
// (schema version, generation, digest and observation time).
func pluginManifestItemsFit(items []pluginDiscoveryItem) bool {
	encoded, err := json.Marshal(items)
	return err == nil && len(encoded) <= 6<<20-1<<10
}

func (s *localPluginService) runtimeMatches(provider, companyID, runtimeID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	assignment, ok := s.runtimes[provider][runtimeID]
	return ok && assignment.companyID == companyID
}

func (s *localPluginService) markPublished(
	provider string,
	published pluginRuntimeAssignment,
	digest string,
) {
	s.mu.Lock()
	defer s.mu.Unlock()
	assignment, ok := s.runtimes[provider][published.runtimeID]
	if !ok || assignment.companyID != published.companyID {
		return
	}
	assignment.publishedHash = digest
	s.runtimes[provider][published.runtimeID] = assignment
}

func samePluginRuntimeAssignments(left, right map[string]pluginRuntimeAssignment) bool {
	if len(left) != len(right) {
		return false
	}
	for runtimeID, leftAssignment := range left {
		rightAssignment, ok := right[runtimeID]
		if !ok || leftAssignment.companyID != rightAssignment.companyID {
			return false
		}
	}
	return true
}

func (s *localPluginService) closeBackends() {
	s.mu.RLock()
	backends := make([]nativePluginBackend, 0, len(s.backends))
	for _, backend := range s.backends {
		backends = append(backends, backend)
	}
	s.mu.RUnlock()
	for _, backend := range backends {
		closeNativePluginBackend(backend)
	}
}

func closeNativePluginBackend(backend nativePluginBackend) {
	if lifecycle, ok := backend.(nativePluginLifecycle); ok {
		lifecycle.Close()
	}
}

type claudeNativePluginBackend struct{ backend *ClaudePluginBackend }

func (b claudeNativePluginBackend) Discover(ctx context.Context, force bool) ([]nativePluginItem, error) {
	snapshot, err := b.backend.Discover(ctx, force)
	if err != nil {
		return nil, err
	}
	items := make([]nativePluginItem, len(snapshot.Plugins))
	for index, plugin := range snapshot.Plugins {
		items[index] = nativePluginItem{
			NativeID: plugin.NativeID, Name: plugin.Name, Description: plugin.Description,
			Status: string(plugin.Status), Installed: plugin.Installed, Enabled: plugin.Enabled,
			Action: string(plugin.Action), CatalogRank: plugin.CatalogRank,
			InstallCount: plugin.InstallCount,
			ConnectorIdentities: []PluginConnectorIdentity{{
				Source: "claude_plugin", NativeID: plugin.NativeID,
			}},
		}
	}
	return items, nil
}

func (b claudeNativePluginBackend) Install(ctx context.Context, nativeID string) (nativePluginInstallResult, error) {
	err := b.backend.Install(ctx, nativeID)
	switch {
	case errors.Is(err, ErrClaudePluginNotFound):
		return nativePluginInstallResult{}, errPluginNotDiscovered
	case errors.Is(err, ErrClaudePluginNotInstallable):
		return nativePluginInstallResult{}, errPluginActionInvalid
	default:
		return nativePluginInstallResult{}, err
	}
}

func (b claudeNativePluginBackend) Uninstall(ctx context.Context, nativeID string) error {
	err := b.backend.Uninstall(ctx, nativeID)
	switch {
	case errors.Is(err, ErrClaudePluginNotFound):
		return errPluginNotDiscovered
	case errors.Is(err, ErrClaudePluginNotUninstallable):
		return errPluginActionInvalid
	default:
		return err
	}
}

func (b claudeNativePluginBackend) ReadDetail(ctx context.Context, nativeID string) (pluginDetail, error) {
	detail, err := b.backend.ReadDetail(ctx, nativeID)
	switch {
	case errors.Is(err, ErrClaudePluginNotFound):
		return pluginDetail{}, errPluginNotDiscovered
	case err != nil:
		return pluginDetail{}, errPluginsUnavailable
	default:
		return pluginDetail{
			NativeID: detail.NativeID, Name: detail.Name,
			MCPServers: slices.Clone(detail.MCPServers),
		}, nil
	}
}

func (b claudeNativePluginBackend) SetChangeHandler(handler func()) {
	b.backend.SetChangeHandler(handler)
}
func (b claudeNativePluginBackend) Close() { b.backend.Close() }

type codexNativePluginBackend struct{ backend *CodexPluginBackend }

func (b codexNativePluginBackend) Discover(ctx context.Context, force bool) ([]nativePluginItem, error) {
	snapshot, err := b.backend.Discover(ctx, force)
	if err != nil {
		return nil, err
	}
	items := make([]nativePluginItem, len(snapshot.Plugins))
	for index, plugin := range snapshot.Plugins {
		items[index] = nativePluginItem{
			NativeID: plugin.NativeID, Name: plugin.Name, Description: plugin.Description,
			Category: plugin.Category, IconURL: plugin.IconURL, ShareURL: plugin.ShareURL,
			Status: string(plugin.Status), Installed: plugin.Installed, Enabled: plugin.Enabled,
			Featured: plugin.Featured, Action: string(plugin.Action), CatalogRank: plugin.CatalogRank,
			ConnectorIdentities: plugin.ConnectorIdentities,
			ComponentCounts: nativePluginComponentCounts{
				Apps: plugin.ComponentCounts.Apps, MCPServers: plugin.ComponentCounts.MCPServers,
				Skills: plugin.ComponentCounts.Skills,
			},
			LongDescription: plugin.LongDescription, DefaultPrompts: plugin.DefaultPrompts,
			BrandColor: plugin.BrandColor, DeveloperName: plugin.DeveloperName,
			WebsiteURL: plugin.WebsiteURL, PrivacyPolicyURL: plugin.PrivacyPolicyURL,
			TermsOfServiceURL: plugin.TermsOfServiceURL,
		}
	}
	return items, nil
}

func (b codexNativePluginBackend) Install(ctx context.Context, nativeID string) (nativePluginInstallResult, error) {
	result, err := b.backend.Install(ctx, nativeID)
	switch {
	case errors.Is(err, ErrCodexPluginNotFound):
		return nativePluginInstallResult{}, errPluginNotDiscovered
	case errors.Is(err, ErrCodexPluginNotInstallable):
		return nativePluginInstallResult{}, errPluginActionInvalid
	case err != nil:
		return nativePluginInstallResult{}, err
	default:
		apps := make([]pluginDetailApp, len(result.AuthApps))
		for index, app := range result.AuthApps {
			apps[index] = pluginDetailApp{ID: app.ID, Name: app.Name}
		}
		return nativePluginInstallResult{
			AwaitingUser: result.NeedsAuthentication, ContinueURL: result.ContinueURL,
			AuthPolicy: result.AuthPolicy, AppsNeedingAuth: apps,
		}, nil
	}
}

func (b codexNativePluginBackend) Uninstall(ctx context.Context, nativeID string) error {
	err := b.backend.Uninstall(ctx, nativeID)
	switch {
	case errors.Is(err, ErrCodexPluginNotFound):
		return errPluginNotDiscovered
	case errors.Is(err, ErrCodexPluginNotUninstallable):
		return errPluginActionInvalid
	default:
		return err
	}
}

func (b codexNativePluginBackend) ReadDetail(ctx context.Context, nativeID string) (pluginDetail, error) {
	detail, err := b.backend.ReadDetail(ctx, nativeID)
	switch {
	case errors.Is(err, ErrCodexPluginNotFound):
		return pluginDetail{}, errPluginNotDiscovered
	case err != nil:
		return pluginDetail{}, errPluginsUnavailable
	}
	result := pluginDetail{
		NativeID: detail.NativeID, Name: detail.Name,
		AuthPolicy: detail.AuthPolicy, InstallationInterstitial: detail.InstallationInterstitial,
		LongDescription: detail.LongDescription, DefaultPrompts: slices.Clone(detail.DefaultPrompts),
		BrandColor: detail.BrandColor, DeveloperName: detail.DeveloperName,
		WebsiteURL: detail.WebsiteURL, PrivacyPolicyURL: detail.PrivacyPolicyURL,
		TermsOfServiceURL: detail.TermsOfServiceURL, ShareURL: detail.ShareURL,
		Apps:       make([]pluginDetailApp, len(detail.Apps)),
		Skills:     make([]pluginDetailSkill, len(detail.Skills)),
		MCPServers: slices.Clone(detail.MCPServers),
	}
	for index, app := range detail.Apps {
		result.Apps[index] = pluginDetailApp{
			ID: app.ID, Name: app.Name, Description: app.Description, Category: app.Category,
		}
	}
	for index, skill := range detail.Skills {
		result.Skills[index] = pluginDetailSkill(skill)
	}
	return result, nil
}

func (b codexNativePluginBackend) ReadIcon(ctx context.Context, nativeID string) (nativePluginIcon, error) {
	icon, err := b.backend.ReadIcon(ctx, nativeID)
	if err != nil {
		return nativePluginIcon{}, errPluginIconUnavailable
	}
	return nativePluginIcon{ContentType: icon.ContentType, Bytes: append([]byte(nil), icon.Bytes...)}, nil
}

func (b codexNativePluginBackend) SetChangeHandler(handler func()) {
	b.backend.SetChangeHandler(handler)
}
func (b codexNativePluginBackend) Close() { b.backend.Close() }
