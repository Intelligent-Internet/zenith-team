package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/pkg/agent"
)

type connectorPublisherRecorder struct {
	companyID string
	runtimeID string
	manifest  connectorDiscoveryManifest
}

func (p *connectorPublisherRecorder) publishConnectorDiscovery(
	_ context.Context,
	companyID, runtimeID string,
	manifest connectorDiscoveryManifest,
) (discoveryPublishResult, error) {
	p.companyID, p.runtimeID, p.manifest = companyID, runtimeID, manifest
	return discoveryPublishResult{Applied: true}, nil
}

func (*connectorPublisherRecorder) reaffirmConnectorDiscovery(
	context.Context,
	string,
	string,
	connectorDiscoveryReaffirmation,
) (discoveryPublishResult, error) {
	return discoveryPublishResult{Applied: true}, nil
}

type staticConnectorBackend struct {
	items []nativeConnectorItem
}

func (b staticConnectorBackend) Discover(context.Context, bool) ([]nativeConnectorItem, error) {
	return cloneNativeConnectorItems(b.items), nil
}

func (staticConnectorBackend) Connect(
	context.Context,
	nativeConnectorIdentity,
) (nativeConnectorOperation, error) {
	return nativeConnectorOperation{State: "complete"}, nil
}

func TestLocalConnectorServicePublishesSanitizedManifest(t *testing.T) {
	t.Parallel()

	tools := []agent.ConnectorTool{{Name: "search", Enabled: true, ReadOnly: true}}
	digest, err := agent.ConnectorToolContractDigest(tools)
	if err != nil {
		t.Fatalf("ConnectorToolContractDigest() error = %v", err)
	}
	publisher := &connectorPublisherRecorder{}
	service := newLocalConnectorService(publisher)
	service.setBackend("codex", staticConnectorBackend{items: []nativeConnectorItem{{
		Source: "codex_app", NativeID: "drive", Name: "Drive", Status: "connected",
		Action: "none", CanExecute: true, ContractDigest: digest, Tools: tools,
	}}})
	service.runtimes["codex"] = map[string]connectorRuntimeAssignment{
		"runtime-1": {companyID: "company-1", runtimeID: "runtime-1"},
	}
	service.now = func() time.Time { return time.Unix(1, 0).UTC() }
	service.generation = func() (string, error) { return "generation-1", nil }

	if err := service.refreshProvider(t.Context(), "codex", capabilityRefreshTypeForced); err != nil {
		t.Fatalf("refreshProvider() error = %v", err)
	}
	if publisher.companyID != "company-1" || publisher.runtimeID != "runtime-1" ||
		publisher.manifest.SchemaVersion != 1 || len(publisher.manifest.Items) != 1 {
		t.Fatalf("publication = %+v", publisher)
	}
	item := publisher.manifest.Items[0]
	if item.ContractDigest != digest || len(item.Tools) != 1 || item.Tools[0].Name != "search" {
		t.Fatalf("manifest item = %+v", item)
	}
}

// A Codex computer signed in to ChatGPT lists the whole hosted app directory
// as Connectors. Sized like the real one (about 460 bytes an item), a
// directory at the item cap is well over 2 MiB and must still publish; a
// snapshot past the transport cap is still refused rather than sent.
func TestLocalConnectorServicePublishesAHostedAppDirectory(t *testing.T) {
	t.Parallel()

	directory := func(description, iconURL func(int) string) []nativeConnectorItem {
		items := make([]nativeConnectorItem, maximumConnectorDiscoveryItems)
		for index := range items {
			items[index] = nativeConnectorItem{
				Source: "codex_app", NativeID: fmt.Sprintf("asdk_app_%032x", index),
				Name: fmt.Sprintf("Directory app %04d", index), Description: description(index),
				Category: "Productivity", IconURL: iconURL(index),
				Status: "available", Action: "install_and_auth",
			}
		}
		return items
	}
	publish := func(items []nativeConnectorItem) (*connectorPublisherRecorder, error) {
		publisher := &connectorPublisherRecorder{}
		service := newLocalConnectorService(publisher)
		service.setBackend("codex", staticConnectorBackend{items: items})
		service.runtimes["codex"] = map[string]connectorRuntimeAssignment{
			"runtime-1": {companyID: "company-1", runtimeID: "runtime-1"},
		}
		service.generation = func() (string, error) { return "generation-1", nil }
		return publisher, service.refreshProvider(t.Context(), "codex", capabilityRefreshTypeForced)
	}

	realistic := directory(
		func(int) string { return "Search, read and update your workspace." },
		func(index int) string {
			return fmt.Sprintf(
				"https://files.openai.com/content?id=file-%022d&sig=%064x&ts=1790000000&p=fs&v=0",
				index, index,
			)
		},
	)
	publisher, err := publish(realistic)
	if err != nil {
		t.Fatalf("refreshProvider() on a hosted app directory error = %v", err)
	}
	encoded, err := json.Marshal(publisher.manifest)
	if err != nil {
		t.Fatalf("encode published manifest: %v", err)
	}
	if len(publisher.manifest.Items) != maximumConnectorDiscoveryItems || len(encoded) <= 2<<20 {
		t.Fatalf(
			"published %d items in %d bytes, want %d items over 2 MiB",
			len(publisher.manifest.Items), len(encoded), maximumConnectorDiscoveryItems,
		)
	}

	oversized := directory(
		func(int) string { return strings.Repeat("d", 1000) },
		func(int) string { return "https://chatgpt.com/images/" + strings.Repeat("i", 1000) + ".png" },
	)
	publisher, err = publish(oversized)
	if err == nil || !strings.Contains(err.Error(), "exceeds transport limit") {
		t.Fatalf("refreshProvider() past the transport cap error = %v, want the limit", err)
	}
	if publisher.runtimeID != "" {
		t.Fatalf("an oversized manifest was published to %q", publisher.runtimeID)
	}
}

type statusConnectorBackend struct {
	staticConnectorBackend
	connected map[string]bool
}

func (b statusConnectorBackend) Connected(
	_ context.Context,
	identity nativeConnectorIdentity,
) (bool, error) {
	return b.connected[identity.Source+":"+identity.NativeID], nil
}

// The desktop polls status while someone connects an app in the browser. A
// connection the cached catalog does not show yet schedules a linked
// discovery, so the cloud catches up too.
func TestLocalConnectorServiceStatusReadsTheConnectionLive(t *testing.T) {
	t.Parallel()

	service := newLocalConnectorService(nil)
	service.setBackend("codex", statusConnectorBackend{
		connected: map[string]bool{"codex_app:connector_gmail": true},
	})
	service.setBackend("claude", staticConnectorBackend{})
	service.replaceRuntimes([]Runtime{
		{ID: pluginTestRuntimeID, CompanyID: pluginTestCompanyID, Provider: "codex"},
	})
	service.catalogs["codex"] = []nativeConnectorItem{
		{Source: "codex_app", NativeID: "connector_gmail", Status: "available"},
		{Source: "codex_app", NativeID: "connector_drive", Status: "available"},
	}
	service.catalogs["claude"] = []nativeConnectorItem{
		{Source: "claude_cloud", NativeID: "gmail", Status: "available"},
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// A queue without its worker, so the scheduled refresh stays observable.
	service.refresh.ctx, service.refresh.cancel = ctx, cancel
	service.refresh.queue = make(chan string, 8)

	status, err := service.status(
		t.Context(), pluginTestCompanyID, pluginTestRuntimeID, "codex", "codex_app", "connector_drive",
	)
	if err != nil || status != (connectorStatus{SchemaVersion: 1}) {
		t.Fatalf("status(drive) = %+v, %v; want not connected", status, err)
	}
	if len(service.refresh.queue) != 0 {
		t.Fatal("an unconnected app scheduled a refresh")
	}
	status, err = service.status(
		t.Context(), pluginTestCompanyID, pluginTestRuntimeID, "codex", "codex_app", "connector_gmail",
	)
	if err != nil || status != (connectorStatus{SchemaVersion: 1, Connected: true}) {
		t.Fatalf("status(gmail) = %+v, %v; want connected", status, err)
	}
	if provider := <-service.refresh.queue; provider != "codex" ||
		service.refresh.types["codex"] != capabilityRefreshTypeLinked {
		t.Fatalf("scheduled %q as %v, want a linked codex refresh", provider, service.refresh.types["codex"])
	}

	if _, err := service.status(
		t.Context(), pluginTestCompanyID, pluginTestRuntimeID, "codex", "codex_app", "connector_docs",
	); !errors.Is(err, errConnectorNotDiscovered) {
		t.Fatalf("status(undiscovered) error = %v, want errConnectorNotDiscovered", err)
	}
	if _, err := service.status(
		t.Context(), pluginTestCompanyID, "019fa834-a515-79b2-b1bd-19e6bd4f2799", "codex", "codex_app",
		"connector_gmail",
	); !errors.Is(err, errConnectorNotDiscovered) {
		t.Fatalf("status(other runtime) error = %v, want errConnectorNotDiscovered", err)
	}
	service.replaceRuntimes([]Runtime{
		{ID: pluginTestRuntimeID, CompanyID: pluginTestCompanyID, Provider: "claude"},
	})
	if _, err := service.status(
		t.Context(), pluginTestCompanyID, pluginTestRuntimeID, "claude", "claude_cloud", "gmail",
	); !errors.Is(err, errConnectorsUnavailable) {
		t.Fatalf("status(no live check) error = %v, want errConnectorsUnavailable", err)
	}
}

// Discovery publishes a linked app Codex cannot call yet as
// installed_not_ready. The live check answers by the same rule, so once it
// finds that app connected the catalog is behind, and the check schedules the
// linked discovery that catches it up.
func TestLocalConnectorServiceStatusCatchesUpAnAppThatBecameCallable(t *testing.T) {
	t.Parallel()

	service := newLocalConnectorService(nil)
	service.setBackend("codex", statusConnectorBackend{
		connected: map[string]bool{"codex_app:connector_mail": true},
	})
	service.replaceRuntimes([]Runtime{
		{ID: pluginTestRuntimeID, CompanyID: pluginTestCompanyID, Provider: "codex"},
	})
	service.catalogs["codex"] = []nativeConnectorItem{
		{Source: "codex_app", NativeID: "connector_mail", Status: "installed_not_ready"},
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// A queue without its worker, so the scheduled refresh stays observable.
	service.refresh.ctx, service.refresh.cancel = ctx, cancel
	service.refresh.queue = make(chan string, 8)

	status, err := service.status(
		t.Context(), pluginTestCompanyID, pluginTestRuntimeID, "codex", "codex_app", "connector_mail",
	)
	if err != nil || status != (connectorStatus{SchemaVersion: 1, Connected: true}) {
		t.Fatalf("status(mail) = %+v, %v; want connected", status, err)
	}
	if len(service.refresh.queue) != 1 || service.refresh.types["codex"] != capabilityRefreshTypeLinked {
		t.Fatalf("scheduled %d refreshes as %v, want one linked codex refresh",
			len(service.refresh.queue), service.refresh.types["codex"])
	}
}

// recordingConnectorBackend records each read Discover makes.
type recordingConnectorBackend struct {
	staticConnectorBackend
	reads *[]string
}

func (b recordingConnectorBackend) Discover(ctx context.Context, force bool) ([]nativeConnectorItem, error) {
	read := "cached"
	if force {
		read = "forced"
	}
	*b.reads = append(*b.reads, read)
	return b.staticConnectorBackend.Discover(ctx, force)
}

type linkedConnectorBackend struct{ recordingConnectorBackend }

func (b linkedConnectorBackend) DiscoverLinked(ctx context.Context) ([]nativeConnectorItem, error) {
	*b.reads = append(*b.reads, "linked")
	return b.staticConnectorBackend.Discover(ctx, false)
}

// A linked refresh always reads the provider again, even right after a
// keepalive, and asks a backend that can for a linked read; any other
// backend reads forced.
func TestLocalConnectorServiceLinkedRefreshReadsLinkedAccounts(t *testing.T) {
	t.Parallel()

	var reads []string
	service := newLocalConnectorService(&connectorPublisherRecorder{})
	service.now = func() time.Time { return time.Unix(1, 0).UTC() }
	service.generation = func() (string, error) { return "generation-1", nil }
	service.setBackend("codex", linkedConnectorBackend{recordingConnectorBackend{reads: &reads}})
	for _, refreshType := range []capabilityRefreshType{
		capabilityRefreshTypeReobserve, capabilityRefreshTypeReobserve, capabilityRefreshTypeLinked,
	} {
		if err := service.refreshProvider(t.Context(), "codex", refreshType); err != nil {
			t.Fatalf("refreshProvider(%v) error = %v", refreshType, err)
		}
	}
	if want := []string{"cached", "linked"}; !slices.Equal(reads, want) {
		t.Fatalf("reads = %v, want %v", reads, want)
	}

	var plain []string
	service.setBackend("claude", recordingConnectorBackend{reads: &plain})
	if err := service.refreshProvider(t.Context(), "claude", capabilityRefreshTypeLinked); err != nil {
		t.Fatalf("refreshProvider(claude) error = %v", err)
	}
	if want := []string{"forced"}; !slices.Equal(plain, want) {
		t.Fatalf("reads without a linked read = %v, want %v", plain, want)
	}
}
