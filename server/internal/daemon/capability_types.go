package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/pkg/agent"
)

// The discovery transport caps, mirrored by the server's body limits on the
// two publish routes (handler.maxConnectorDiscoveryBodyBytes and
// maxPluginDiscoveryBodyBytes). A Codex computer signed in to ChatGPT lists
// the whole hosted app directory as Connectors: 4,593 items in 2,102,658
// bytes on 2026-09-28, just past the old 2 MiB cap, which left every such
// computer with a stale catalog. At that size per item the 10,000-item cap
// below needs about 4.4 MiB; 8 MiB, the Plugin cap, leaves room for longer
// descriptions and tool lists.
const (
	maximumConnectorDiscoveryRequestBytes = 8 << 20
	maximumPluginDiscoveryRequestBytes    = 8 << 20
)

// maximumConnectorDiscoveryItems leaves room to grow: Codex's hosted app
// directory held 4,568 apps in September 2026, up from 4,161 a week before,
// and its manifest about 1.8 MB.
const maximumConnectorDiscoveryItems = 10000

// maximumPluginDiscoveryItems has its own bound: the Codex plugin catalog
// passed 5,000 entries in September 2026.
const maximumPluginDiscoveryItems = 10000

type connectorDiscoveryItem struct {
	Source         string                `json:"source"`
	NativeID       string                `json:"nativeId"`
	Name           string                `json:"name"`
	Description    string                `json:"description,omitempty"`
	Category       string                `json:"category,omitempty"`
	IconURL        string                `json:"iconUrl,omitempty"`
	InstallCount   *int64                `json:"installCount,omitempty"`
	Status         string                `json:"status"`
	Action         string                `json:"action"`
	CanExecute     bool                  `json:"canExecute"`
	ContractDigest string                `json:"contractDigest,omitempty"`
	Tools          []agent.ConnectorTool `json:"tools,omitempty"`
}

type connectorDiscoveryManifest struct {
	SchemaVersion  int                      `json:"schemaVersion"`
	Generation     string                   `json:"generation"`
	ManifestDigest string                   `json:"manifestDigest"`
	ObservedAt     time.Time                `json:"observedAt"`
	Items          []connectorDiscoveryItem `json:"items"`
}

// connectorDiscoveryReaffirmation renews the freshness of the generation the
// daemon last published when a fresh observation left its manifest unchanged.
type connectorDiscoveryReaffirmation struct {
	SchemaVersion  int       `json:"schemaVersion"`
	Generation     string    `json:"generation"`
	ManifestDigest string    `json:"manifestDigest"`
	ObservedAt     time.Time `json:"observedAt"`
}

type discoveryPublishResult struct {
	Applied bool `json:"applied"`
}

type pluginComponentCounts struct {
	Apps       int `json:"apps"`
	MCPServers int `json:"mcpServers"`
	Skills     int `json:"skills"`
}

type pluginConnectorIdentity struct {
	Source   string `json:"source"`
	NativeID string `json:"nativeId"`
}

type pluginDiscoveryItem struct {
	NativeID            string                    `json:"nativeId"`
	Name                string                    `json:"name"`
	Description         string                    `json:"description,omitempty"`
	Category            string                    `json:"category,omitempty"`
	IconURL             string                    `json:"iconUrl,omitempty"`
	ShareURL            string                    `json:"shareUrl,omitempty"`
	Status              string                    `json:"status"`
	Installed           bool                      `json:"installed"`
	Enabled             bool                      `json:"enabled"`
	Featured            bool                      `json:"featured"`
	Action              string                    `json:"action"`
	CatalogRank         int                       `json:"catalogRank"`
	InstallCount        *int64                    `json:"installCount,omitempty"`
	ComponentCounts     pluginComponentCounts     `json:"componentCounts"`
	ConnectorIdentities []pluginConnectorIdentity `json:"connectorIdentities,omitempty"`
	// Presentation (protocol.DaemonCapabilityPluginPresentationV1 only). The
	// server's pluginDigestItem mirrors this order and these tags exactly.
	LongDescription   string   `json:"longDescription,omitempty"`
	DefaultPrompts    []string `json:"defaultPrompts,omitempty"`
	BrandColor        string   `json:"brandColor,omitempty"`
	DeveloperName     string   `json:"developerName,omitempty"`
	WebsiteURL        string   `json:"websiteUrl,omitempty"`
	PrivacyPolicyURL  string   `json:"privacyPolicyUrl,omitempty"`
	TermsOfServiceURL string   `json:"termsOfServiceUrl,omitempty"`
}

type pluginDiscoveryManifest struct {
	SchemaVersion  int                   `json:"schemaVersion"`
	Generation     string                `json:"generation"`
	ManifestDigest string                `json:"manifestDigest"`
	ObservedAt     time.Time             `json:"observedAt"`
	Items          []pluginDiscoveryItem `json:"items"`
}

func connectorManifestDigest(items []connectorDiscoveryItem) string {
	encoded, err := json.Marshal(items)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func pluginManifestDigest(items []pluginDiscoveryItem) string {
	canonical := slices.Clone(items)
	sort.Slice(canonical, func(left, right int) bool {
		if canonical[left].CatalogRank == canonical[right].CatalogRank {
			return strings.Compare(canonical[left].NativeID, canonical[right].NativeID) < 0
		}
		return canonical[left].CatalogRank < canonical[right].CatalogRank
	})
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}
