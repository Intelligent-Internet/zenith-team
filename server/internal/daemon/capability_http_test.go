package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

type pluginIconBackendStub struct {
	pluginBackendStub
	icon nativePluginIcon
}

func (s *pluginIconBackendStub) ReadIcon(context.Context, string) (nativePluginIcon, error) {
	return s.icon, nil
}

func newPluginHTTPTestDaemon(t *testing.T, codex nativePluginBackend) *Daemon {
	t.Helper()
	plugins := newLocalPluginService(nil)
	plugins.setBackend("codex", codex)
	plugins.setBackend("claude", &pluginBackendStub{})
	plugins.replaceRuntimes([]Runtime{{
		ID: pluginTestRuntimeID, CompanyID: pluginTestCompanyID, Provider: "codex",
	}})
	if err := plugins.refreshProvider(t.Context(), "codex", false); err != nil {
		t.Fatal(err)
	}
	connectors := newLocalConnectorService(nil)
	connectors.catalogs["codex"] = []nativeConnectorItem{
		{Source: "codex_app", NativeID: "connector_gmail", IconURL: "https://cdn.openai.com/apps/gmail.png"},
		{Source: "codex_mcp", NativeID: "connector_drive", IconURL: "https://cdn.openai.com/apps/mcp.png"},
		{
			Source: "codex_app", NativeID: "connector_calendar",
			IconURL: "https://chatgpt.com/images/ecosystem/apps/google-calendar/icon.png",
		},
	}
	return &Daemon{plugins: plugins, connectors: connectors, localPreviewToken: "local-capability"}
}

func pluginHTTPRequestBody(provider, nativeID string) string {
	return `{"schemaVersion":1,"companyId":"` + pluginTestCompanyID + `","runtimeId":"` +
		pluginTestRuntimeID + `","provider":"` + provider + `","nativeId":"` + nativeID + `"}`
}

func TestLocalPluginReadHandlerAnswersTheLiveRead(t *testing.T) {
	t.Parallel()

	d := newPluginHTTPTestDaemon(t, &pluginBackendStub{
		items: []nativePluginItem{presentationPluginItem()},
		detail: pluginDetail{
			NativeID: "gmail@openai-curated-remote", Name: "Gmail", BrandColor: "#EA4335",
			Apps: []pluginDetailApp{
				{ID: "connector_gmail", Name: "Gmail"},
				{ID: "connector_drive", Name: "Drive"},
				{ID: "connector_docs", Name: "Docs", IconURL: "https://cdn.openai.com/apps/docs.png"},
				// A session-only catalog logo is left out, so the page keeps the
				// server's loadable one.
				{ID: "connector_calendar", Name: "Calendar"},
			},
		},
	})

	response := serveLocalWorkspaceRequest(
		t, d.localPluginReadHandler(),
		pluginHTTPRequestBody("codex", "gmail@openai-curated-remote"), "Bearer local-capability",
	)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	const want = `{"ok":true,"value":{"schemaVersion":1,"nativeId":"gmail@openai-curated-remote",` +
		`"name":"Gmail","defaultPrompts":[],"brandColor":"#EA4335","apps":[` +
		`{"id":"connector_gmail","name":"Gmail","iconUrl":"https://cdn.openai.com/apps/gmail.png"},` +
		`{"id":"connector_drive","name":"Drive"},` +
		`{"id":"connector_docs","name":"Docs","iconUrl":"https://cdn.openai.com/apps/docs.png"},` +
		`{"id":"connector_calendar","name":"Calendar"}],` +
		`"skills":[],"mcpServers":[]}}` + "\n"
	if response.Body.String() != want {
		t.Fatalf("body = %s, want %s", response.Body.String(), want)
	}
}

func TestLocalPluginReadHandlerRejections(t *testing.T) {
	t.Parallel()

	d := newPluginHTTPTestDaemon(t, &pluginBackendStub{
		items:  []nativePluginItem{presentationPluginItem()},
		detail: pluginDetail{NativeID: "gmail@openai-curated-remote", Name: "Gmail"},
	})
	tests := []struct {
		name          string
		body          string
		authorization string
		wantStatus    int
		wantCode      string
	}{
		{
			name:          "unknown plugin",
			body:          pluginHTTPRequestBody("codex", "unknown@openai-curated-remote"),
			authorization: "Bearer local-capability",
			wantStatus:    http.StatusNotFound, wantCode: "capability_not_discovered",
		},
		{
			name:          "other provider",
			body:          pluginHTTPRequestBody("claude", "gmail@openai-curated-remote"),
			authorization: "Bearer local-capability",
			wantStatus:    http.StatusConflict, wantCode: "plugin_provider_mismatch",
		},
		{
			name:          "invalid native ID",
			body:          pluginHTTPRequestBody("codex", " gmail"),
			authorization: "Bearer local-capability",
			wantStatus:    http.StatusBadRequest, wantCode: "invalid_request",
		},
		{
			name:          "unknown field",
			body:          `{"schemaVersion":1,"extra":true}`,
			authorization: "Bearer local-capability",
			wantStatus:    http.StatusBadRequest, wantCode: "invalid_request",
		},
		{
			name:          "wrong capability",
			body:          pluginHTTPRequestBody("codex", "gmail@openai-curated-remote"),
			authorization: "Bearer other-capability",
			wantStatus:    http.StatusUnauthorized, wantCode: "unauthorized",
		},
	}
	for _, test := range tests {
		response := serveLocalWorkspaceRequest(
			t, d.localPluginReadHandler(), test.body, test.authorization,
		)
		var got localCapabilityError
		if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
			t.Fatalf("%s: decode %q: %v", test.name, response.Body.String(), err)
		}
		if response.Code != test.wantStatus || got.OK || got.Code != test.wantCode {
			t.Errorf("%s: status = %d, body = %s, want %d %s",
				test.name, response.Code, response.Body.String(), test.wantStatus, test.wantCode)
		}
	}
}

// The loopback route serves what the Codex backend reads, up to the same
// 4 MiB, and still checks the signature of the declared type.
func TestValidLocalPluginIconBounds(t *testing.T) {
	t.Parallel()

	png := func(size int) []byte {
		content := make([]byte, size)
		copy(content, "\x89PNG\r\n\x1a\n")
		return content
	}
	for _, test := range []struct {
		name string
		icon pluginIcon
		want bool
	}{
		{"full-size app icon", pluginIcon{ContentType: "image/png", Bytes: png(1_167_902)}, true},
		{"at the cap", pluginIcon{ContentType: "image/png", Bytes: png(maximumPluginIconBytes)}, true},
		{"over the cap", pluginIcon{ContentType: "image/png", Bytes: png(maximumPluginIconBytes + 1)}, false},
		{"wrong signature", pluginIcon{ContentType: "image/jpeg", Bytes: png(1_167_902)}, false},
		{"empty", pluginIcon{ContentType: "image/png"}, false},
	} {
		if got := validLocalPluginIcon(test.icon); got != test.want {
			t.Errorf("%s: validLocalPluginIcon() = %v, want %v", test.name, got, test.want)
		}
	}
	if maximumPluginIconBytes != codexPluginMaximumIconBytes {
		t.Fatalf("loopback icon cap %d differs from the Codex read cap %d",
			maximumPluginIconBytes, codexPluginMaximumIconBytes)
	}
}

func TestLocalPluginIconHandlerServesSandboxedSVG(t *testing.T) {
	t.Parallel()

	const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"/>`
	backend := &pluginIconBackendStub{
		pluginBackendStub: pluginBackendStub{items: []nativePluginItem{presentationPluginItem()}},
		icon:              nativePluginIcon{ContentType: "image/svg+xml", Bytes: []byte(svg)},
	}
	d := newPluginHTTPTestDaemon(t, backend)

	response := serveLocalWorkspaceRequest(
		t, d.localPluginIconHandler(),
		pluginHTTPRequestBody("codex", "gmail@openai-curated-remote"), "Bearer local-capability",
	)
	if response.Code != http.StatusOK || response.Body.String() != svg {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	for header, want := range map[string]string{
		"Content-Type":            "image/svg+xml",
		"Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'; sandbox",
		"X-Content-Type-Options":  "nosniff",
	} {
		if got := response.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}

	backend.icon = nativePluginIcon{ContentType: "image/svg+xml", Bytes: []byte("<html/>")}
	response = serveLocalWorkspaceRequest(
		t, d.localPluginIconHandler(),
		pluginHTTPRequestBody("codex", "gmail@openai-curated-remote"), "Bearer local-capability",
	)
	if response.Code != http.StatusInternalServerError ||
		response.Header().Get("Content-Security-Policy") != "" {
		t.Fatalf("invalid SVG status = %d, headers = %v", response.Code, response.Header())
	}

	backend.icon = nativePluginIcon{ContentType: "image/png", Bytes: []byte("\x89PNG\r\n\x1a\nrest")}
	response = serveLocalWorkspaceRequest(
		t, d.localPluginIconHandler(),
		pluginHTTPRequestBody("codex", "gmail@openai-curated-remote"), "Bearer local-capability",
	)
	if response.Code != http.StatusOK || response.Header().Get("Content-Security-Policy") != "" {
		t.Fatalf("PNG status = %d, headers = %v", response.Code, response.Header())
	}
}

func TestLocalConnectorStatusHandlerAnswersTheLiveCheck(t *testing.T) {
	t.Parallel()

	connectors := newLocalConnectorService(nil)
	connectors.setBackend("codex", statusConnectorBackend{
		connected: map[string]bool{"codex_app:connector_gmail": true},
	})
	connectors.replaceRuntimes([]Runtime{
		{ID: pluginTestRuntimeID, CompanyID: pluginTestCompanyID, Provider: "codex"},
	})
	connectors.catalogs["codex"] = []nativeConnectorItem{
		{Source: "codex_app", NativeID: "connector_gmail", Status: "connected"},
	}
	d := &Daemon{connectors: connectors, localPreviewToken: "local-capability"}
	body := func(source, nativeID string) string {
		return `{"schemaVersion":1,"companyId":"` + pluginTestCompanyID + `","runtimeId":"` +
			pluginTestRuntimeID + `","provider":"codex","source":"` + source +
			`","nativeId":"` + nativeID + `"}`
	}

	response := serveLocalWorkspaceRequest(
		t, d.localConnectorStatusHandler(), body("codex_app", "connector_gmail"), "Bearer local-capability",
	)
	const want = `{"ok":true,"value":{"schemaVersion":1,"connected":true}}` + "\n"
	if response.Code != http.StatusOK || response.Body.String() != want {
		t.Fatalf("status = %d, body = %s, want %s", response.Code, response.Body.String(), want)
	}
	response = serveLocalWorkspaceRequest(
		t, d.localConnectorStatusHandler(), body("claude_cloud", "connector_gmail"), "Bearer local-capability",
	)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("wrong-source status = %d, body = %s", response.Code, response.Body.String())
	}
}
