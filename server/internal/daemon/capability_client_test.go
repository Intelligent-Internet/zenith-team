package daemon

import (
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientPublishesConnectorDiscoveryToDaemonRuntimeRoute(t *testing.T) {
	t.Parallel()

	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		if r.URL.Path != "/api/v1/daemon/companies/company-1/runtimes/runtime-1/connectors/discovery" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		_ = json.NewEncoder(w).Encode(discoveryPublishResult{Applied: true})
	}))
	defer server.Close()

	client := NewClient(server.URL)
	result, err := client.publishConnectorDiscovery(
		t.Context(),
		"company-1",
		"runtime-1",
		connectorDiscoveryManifest{
			SchemaVersion: 1, Generation: "generation", ManifestDigest: "digest",
			ObservedAt: time.Now().UTC(), Items: []connectorDiscoveryItem{},
		},
	)
	if err != nil {
		t.Fatalf("publishConnectorDiscovery() error = %v", err)
	}
	if !result.Applied || requestBody["schemaVersion"] != float64(1) {
		t.Fatalf("result = %+v, body = %#v", result, requestBody)
	}
	if _, oldName := requestBody["schema_version"]; oldName {
		t.Fatalf("body uses non-camelCase key: %#v", requestBody)
	}
}

func TestClientReaffirmsConnectorDiscoveryWithTheContractBody(t *testing.T) {
	t.Parallel()

	observedAt := time.Date(2026, 9, 24, 12, 3, 0, 0, time.UTC)
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/v1/daemon/companies/company-1/runtimes/runtime-1/connectors/discovery/reaffirm" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		_ = json.NewEncoder(w).Encode(discoveryPublishResult{Applied: true})
	}))
	defer server.Close()

	digest := strings.Repeat("a", 64)
	result, err := NewClient(server.URL).reaffirmConnectorDiscovery(
		t.Context(),
		"company-1",
		"runtime-1",
		connectorDiscoveryReaffirmation{
			SchemaVersion: 1, Generation: "01900000-0000-7000-8000-000000000001",
			ManifestDigest: digest, ObservedAt: observedAt,
		},
	)
	if err != nil {
		t.Fatalf("reaffirmConnectorDiscovery() error = %v", err)
	}
	if !result.Applied {
		t.Fatalf("result = %+v, want applied", result)
	}
	want := map[string]any{
		"schemaVersion":  float64(1),
		"generation":     "01900000-0000-7000-8000-000000000001",
		"manifestDigest": digest,
		"observedAt":     "2026-09-24T12:03:00Z",
	}
	if !maps.Equal(requestBody, want) {
		t.Fatalf("body = %#v, want exactly %#v", requestBody, want)
	}
}

func TestClientReaffirmationSurfacesTheRejectionStatus(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"detail":"stale","code":"connector_discovery_conflict","requestId":"r"}`))
	}))
	defer server.Close()

	_, err := NewClient(server.URL).reaffirmConnectorDiscovery(
		t.Context(), "company-1", "runtime-1",
		connectorDiscoveryReaffirmation{SchemaVersion: 1, ObservedAt: time.Now().UTC()},
	)
	if !connectorReaffirmationRejected(err) {
		t.Fatalf("reaffirmConnectorDiscovery() error = %v, want a rejection the daemon falls back from", err)
	}
	if connectorReaffirmationRejected(errors.New("connection reset")) {
		t.Fatal("a transport error was treated as a rejection")
	}
}
