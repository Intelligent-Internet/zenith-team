package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

type codexConnectorCatalogFixture struct {
	AppPages      []json.RawMessage      `json:"appPages"`
	Installed     json.RawMessage        `json:"installed"`
	AppReads      []json.RawMessage      `json:"appReads"`
	MCPPages      []json.RawMessage      `json:"mcpPages"`
	Notifications []codexRPCNotification `json:"notifications"`
}

type connectorRPCFixture struct {
	mu            sync.Mutex
	appPages      []json.RawMessage
	installed     json.RawMessage
	appReads      []json.RawMessage
	mcpPages      []json.RawMessage
	notifications []codexRPCNotification
	requests      []connectorFixtureRequest
	appIndex      int
	appReadIndex  int
	mcpIndex      int
	closed        bool
	// appFailures fails that many app/list requests first with err, or with
	// an app-server error when err is nil.
	appFailures int
	appErr      error
}

type connectorFixtureRequest struct {
	method string
	params map[string]any
}

func (s *connectorRPCFixture) request(
	_ context.Context,
	method string,
	params any,
) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mapped, _ := params.(map[string]any)
	s.requests = append(s.requests, connectorFixtureRequest{method: method, params: mapped})
	switch method {
	case "app/list":
		if s.appFailures > 0 {
			s.appFailures--
			if s.appErr != nil {
				return nil, s.appErr
			}
			return nil, &codexRPCError{code: -32603}
		}
		if s.appIndex >= len(s.appPages) {
			return nil, errors.New("unexpected app page")
		}
		response := s.appPages[s.appIndex]
		s.appIndex++
		return response, nil
	case "app/installed":
		return s.installed, nil
	case "app/read":
		if s.appReadIndex >= len(s.appReads) {
			return nil, errors.New("unexpected app read")
		}
		response := s.appReads[s.appReadIndex]
		s.appReadIndex++
		return response, nil
	case "mcpServerStatus/list":
		if s.mcpIndex >= len(s.mcpPages) {
			return nil, errors.New("unexpected MCP page")
		}
		response := s.mcpPages[s.mcpIndex]
		s.mcpIndex++
		return response, nil
	default:
		return nil, errors.New("unexpected method")
	}
}

func (s *connectorRPCFixture) nextNotification(ctx context.Context) (codexRPCNotification, error) {
	s.mu.Lock()
	if len(s.notifications) > 0 {
		notification := s.notifications[0]
		s.notifications = s.notifications[1:]
		s.mu.Unlock()
		return notification, nil
	}
	s.mu.Unlock()
	<-ctx.Done()
	return codexRPCNotification{}, ctx.Err()
}

func (s *connectorRPCFixture) drainNotifications() ([]codexRPCNotification, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	notifications := append([]codexRPCNotification(nil), s.notifications...)
	s.notifications = nil
	return notifications, false
}

func (s *connectorRPCFixture) close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}

func TestReadCodexConnectorCatalogPaginatesJoinsAndSanitizes(t *testing.T) {
	t.Parallel()

	fixture := loadCodexConnectorCatalogFixture(t)
	session := &connectorRPCFixture{
		appPages: fixture.AppPages, installed: fixture.Installed,
		appReads: fixture.AppReads, mcpPages: fixture.MCPPages,
		notifications: fixture.Notifications,
	}
	catalog, err := readCodexConnectorCatalog(t.Context(), session, true)
	if err != nil {
		t.Fatal(err)
	}

	wantRequests := []struct {
		method string
		cursor any
	}{
		{method: "app/list", cursor: nil},
		{method: "app/list", cursor: "apps-page-2"},
		{method: "app/installed", cursor: nil},
		{method: "app/read", cursor: nil},
		{method: "mcpServerStatus/list", cursor: nil},
		{method: "mcpServerStatus/list", cursor: "mcp-page-2"},
	}
	if len(session.requests) != len(wantRequests) {
		t.Fatalf("requests = %#v", session.requests)
	}
	for index, want := range wantRequests {
		request := session.requests[index]
		if request.method != want.method || request.params["cursor"] != want.cursor {
			t.Fatalf(
				"request[%d] = %#v, want method=%s cursor=%v",
				index,
				request,
				want.method,
				want.cursor,
			)
		}
	}
	if session.requests[0].params["forceRefetch"] != true ||
		session.requests[1].params["forceRefetch"] != false ||
		session.requests[2].params["forceRefresh"] != true {
		t.Fatalf("refresh params = %#v", session.requests)
	}
	if !reflect.DeepEqual(
		session.requests[3].params["appIds"],
		[]string{"app-connected", "app-runtime-only"},
	) || session.requests[3].params["includeTools"] != true {
		t.Fatalf("app/read params = %#v", session.requests[3].params)
	}
	for _, request := range session.requests[4:] {
		if request.params["detail"] != "toolsAndAuthOnly" {
			t.Fatalf("MCP detail = %v", request.params["detail"])
		}
	}

	byID := make(map[string]CodexConnector, len(catalog.connectors))
	for _, connector := range catalog.connectors {
		byID[connector.NativeID] = connector
	}
	wantStatuses := map[string]CodexConnectorStatus{
		"app-connected":    CodexConnectorStatusConnected,
		"app-available":    CodexConnectorStatusAvailable,
		"app-not-ready":    CodexConnectorStatusInstalledNotReady,
		"app-runtime-only": CodexConnectorStatusConnected,
		"figma":            CodexConnectorStatusConnected,
		"slack":            CodexConnectorStatusNeedsAuth,
		"idle-mcp":         CodexConnectorStatusInstalledNotReady,
	}
	for id, want := range wantStatuses {
		connector, exists := byID[id]
		if !exists || connector.Status != want {
			t.Fatalf("connector %q = %+v, want status %q", id, connector, want)
		}
	}
	if byID["app-available"].Action != CodexConnectorActionInstallAndAuth ||
		byID["slack"].Action != CodexConnectorActionAuthenticate ||
		byID["app-runtime-only"].Action != CodexConnectorActionNone ||
		!byID["app-runtime-only"].CanExecute {
		t.Fatalf("connector actions are inconsistent: %+v", byID)
	}
	for _, absent := range []string{"bad-url", " bad-id ", "bad\x00mcp"} {
		if _, exists := byID[absent]; exists {
			t.Fatalf("malformed connector %q was retained", absent)
		}
	}
	if byID["app-available"].Name != "Available App" {
		t.Fatalf("duplicate replaced first app: %+v", byID["app-available"])
	}
	if byID["app-connected"].IconURL != "https://cdn.example.test/drive.png" {
		t.Fatalf("icon URL = %q", byID["app-connected"].IconURL)
	}
	if strings.ContainsRune(byID["app-connected"].Name, '\x00') {
		t.Fatalf("control character survived sanitization: %q", byID["app-connected"].Name)
	}
	if catalog.warnings.duplicates < 3 || catalog.warnings.skipped < 3 {
		t.Fatalf("warnings = %+v", catalog.warnings)
	}

	publicJSON, err := json.Marshal(catalog.connectors)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"SECRET", "/Users/", "Authorization", "headers", "config", "env", "installUrl",
	} {
		if strings.Contains(string(publicJSON), secret) {
			t.Fatalf("public catalog leaks %q: %s", secret, publicJSON)
		}
	}
}

func TestReadCodexConnectorCatalogRejectsCursorLoop(t *testing.T) {
	t.Parallel()

	page := json.RawMessage(`{"data":[],"nextCursor":"same"}`)
	session := &connectorRPCFixture{appPages: []json.RawMessage{page, page}}
	if _, err := readCodexConnectorCatalog(t.Context(), session, false); err == nil {
		t.Fatal("cursor loop was accepted")
	}
}

func TestReadCodexConnectorCatalogWaitsForFinalStartupStatusNotification(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		reader, writer := io.Pipe()
		defer reader.Close()
		defer writer.Close()
		session := &codexRPCSession{stdin: writer, messages: make(chan []byte)}
		go func() {
			decoder := json.NewDecoder(reader)
			for _, response := range []struct {
				method string
				result string
			}{
				{"app/list", `{"data":[],"nextCursor":""}`},
				{"app/installed", `{"apps":[]}`},
				{"mcpServerStatus/list", `{"data":[{"name":"slack","authStatus":"oAuth","tools":{}}],"nextCursor":""}`},
			} {
				var request struct {
					ID     int    `json:"id"`
					Method string `json:"method"`
				}
				if err := decoder.Decode(&request); err != nil {
					t.Error(err)
					return
				}
				if request.Method != response.method {
					t.Errorf("method = %q, want %q", request.Method, response.method)
					return
				}
				message, err := json.Marshal(
					map[string]any{"id": request.ID, "result": json.RawMessage(response.result)},
				)
				if err != nil {
					t.Error(err)
					return
				}
				select {
				case session.messages <- message:
				case <-ctx.Done():
					return
				}
			}
			// Virtual time keeps the notification after the response but within
			// the settle window, independent of subprocess scheduling on CI.
			time.Sleep(10 * time.Millisecond)
			select {
			case session.messages <- []byte(`{"method":"mcpServer/startupStatus/updated","params":{"name":"slack","failureReason":"reauthenticationRequired"}}`):
			case <-ctx.Done():
			}
		}()
		catalog, err := readCodexConnectorCatalog(ctx, session, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(catalog.connectors) != 1 ||
			catalog.connectors[0].Status != CodexConnectorStatusNeedsAuth {
			t.Fatalf("connectors = %+v, want one connector needing authentication", catalog.connectors)
		}
	})
}

func TestReadCodexConnectorCatalogRejectsMoreThanManifestLimit(t *testing.T) {
	t.Parallel()

	apps := make([]map[string]any, codexConnectorMaximumItems+1)
	for index := range apps {
		apps[index] = map[string]any{
			"id":           "app-" + strconv.Itoa(index),
			"name":         "App",
			"installUrl":   "https://chatgpt.com/apps/catalog/app",
			"isAccessible": false,
		}
	}
	page, err := json.Marshal(map[string]any{"data": apps, "nextCursor": ""})
	if err != nil {
		t.Fatal(err)
	}
	session := &connectorRPCFixture{
		appPages:  []json.RawMessage{page},
		installed: json.RawMessage(`{"apps":[]}`),
		mcpPages:  []json.RawMessage{json.RawMessage(`{"data":[],"nextCursor":""}`)},
	}
	if _, err := readCodexConnectorCatalog(t.Context(), session, false); err == nil {
		t.Fatal("oversized connector catalog was accepted")
	}
}

// Codex's app/list fails a page now and then. The page is asked again after
// 1 s and 3 s; a third error, or a broken session, still fails the read.
func TestReadCodexConnectorCatalogRetriesAFailedPage(t *testing.T) {
	t.Parallel()

	page := json.RawMessage(`{"data":[{"id":"app-1","name":"Drive",` +
		`"installUrl":"https://chatgpt.com/apps/drive/app-1","isAccessible":false}],"nextCursor":""}`)
	newSession := func(failures int, err error) *connectorRPCFixture {
		return &connectorRPCFixture{
			appPages:    []json.RawMessage{page},
			installed:   json.RawMessage(`{"apps":[]}`),
			mcpPages:    []json.RawMessage{json.RawMessage(`{"data":[],"nextCursor":""}`)},
			appFailures: failures, appErr: err,
		}
	}
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		session := newSession(2, nil)
		catalog, err := readCodexConnectorCatalog(t.Context(), session, true)
		if err != nil || len(catalog.connectors) != 1 {
			t.Fatalf("catalog = %+v, error = %v; want the page read on its third try", catalog, err)
		}
		// 1 s and 3 s of retries, then the usual notification settle.
		if waited := time.Since(start); waited != 4*time.Second+codexConnectorNotificationSettle {
			t.Fatalf("read took %s, want 4s of retries and the notification settle", waited)
		}
		var appLists []any
		for _, request := range session.requests {
			if request.method == "app/list" {
				appLists = append(appLists, request.params["forceRefetch"])
			}
		}
		if !reflect.DeepEqual(appLists, []any{true, true, true}) {
			t.Fatalf("app/list forceRefetch = %v, want the same first page three times", appLists)
		}

		var rpcErr *codexRPCError
		if _, err := readCodexConnectorCatalog(
			t.Context(),
			newSession(3, nil),
			true,
		); !errors.As(
			err,
			&rpcErr,
		) {
			t.Fatalf("error after three failures = %v, want the app-server error", err)
		}
		broken := newSession(1, io.ErrUnexpectedEOF)
		if _, err := readCodexConnectorCatalog(
			t.Context(),
			broken,
			true,
		); !errors.Is(
			err,
			io.ErrUnexpectedEOF,
		) {
			t.Fatalf("broken session error = %v, want it final", err)
		}
		if broken.appFailures != 0 || broken.appIndex != 0 {
			t.Fatal("a broken session was retried")
		}
	})
}

// ChatGPT takes about 30 s to list every hosted app and sometimes times out,
// so a keepalive or linked read reuses a directory under 30 minutes old, a
// forced read or an older directory lists again, and a failed listing falls
// back to a directory under a day old. The installed apps are read on every
// call, past Codex's cache unless the read is cached.
func TestReadCodexConnectorCatalogReusesTheAppDirectory(t *testing.T) {
	t.Parallel()

	page := json.RawMessage(`{"data":[` +
		`{"id":"app-drive","name":"Drive","installUrl":"https://chatgpt.com/apps/drive/app-drive","isAccessible":false},` +
		`{"id":"app-mail","name":"Mail","installUrl":"https://chatgpt.com/apps/mail/app-mail","isAccessible":false}` +
		`],"nextCursor":""}`)
	newSession := func(failures int) *connectorRPCFixture {
		return &connectorRPCFixture{
			appPages: []json.RawMessage{page},
			// Mail was linked after the directory was read.
			installed:   json.RawMessage(`{"apps":[{"id":"app-mail","enabled":true,"callable":false}]}`),
			mcpPages:    []json.RawMessage{json.RawMessage(`{"data":[],"nextCursor":""}`)},
			appFailures: failures,
		}
	}
	listed := func(session *connectorRPCFixture) bool {
		for _, request := range session.requests {
			if request.method == "app/list" {
				return true
			}
		}
		return false
	}
	liveInstalled := func(session *connectorRPCFixture) bool {
		for _, request := range session.requests {
			if request.method == "app/installed" {
				return request.params["forceRefresh"] == true
			}
		}
		return false
	}
	statuses := func(catalog codexConnectorCatalog) map[string]CodexConnectorStatus {
		result := make(map[string]CodexConnectorStatus)
		for _, connector := range catalog.connectors {
			result[connector.NativeID] = connector.Status
		}
		return result
	}

	synctest.Test(t, func(t *testing.T) {
		now := time.Now()
		first := newSession(0)
		_, directory, err := readCodexConnectorCatalogFrom(t.Context(), first, codexCatalogCached, nil, now)
		if err != nil || !listed(first) || directory == nil || len(directory.order) != 2 {
			t.Fatalf("first read: directory %+v, error %v; want a listed directory", directory, err)
		}

		want := map[string]CodexConnectorStatus{
			"app-drive": CodexConnectorStatusAvailable,
			"app-mail":  CodexConnectorStatusInstalledNotReady,
		}
		for _, test := range []struct {
			name    string
			refresh codexCatalogRefresh
		}{
			{name: "keepalive", refresh: codexCatalogCached},
			{name: "linked", refresh: codexCatalogLinked},
		} {
			reused := newSession(0)
			catalog, again, err := readCodexConnectorCatalogFrom(
				t.Context(), reused, test.refresh, directory, now.Add(29*time.Minute),
			)
			if err != nil || listed(reused) || again != directory {
				t.Fatalf("%s read listed apps again (error %v)", test.name, err)
			}
			if live := liveInstalled(reused); live != (test.refresh == codexCatalogLinked) {
				t.Fatalf("%s read asked for live installed apps = %v", test.name, live)
			}
			if got := statuses(catalog); !reflect.DeepEqual(got, want) {
				t.Fatalf("%s statuses = %v, want %v", test.name, got, want)
			}
		}

		for _, test := range []struct {
			name    string
			refresh codexCatalogRefresh
			age     time.Duration
		}{
			{name: "forced", refresh: codexCatalogForced, age: time.Minute},
			{name: "stale", refresh: codexCatalogCached, age: 31 * time.Minute},
			{name: "stale linked", refresh: codexCatalogLinked, age: 31 * time.Minute},
		} {
			session := newSession(0)
			_, next, err := readCodexConnectorCatalogFrom(
				t.Context(), session, test.refresh, directory, now.Add(test.age),
			)
			if err != nil || !listed(session) || next == directory {
				t.Fatalf(
					"%s read: listed %v, error %v; want a new listing",
					test.name,
					listed(session),
					err,
				)
			}
		}

		failing := newSession(3)
		catalog, fallback, err := readCodexConnectorCatalogFrom(
			t.Context(), failing, codexCatalogCached, directory, now.Add(2*time.Hour),
		)
		if err != nil || fallback != directory || len(catalog.connectors) != 2 {
			t.Fatalf("failed listing: directory %p, error %v; want the previous directory", fallback, err)
		}
		if _, _, err := readCodexConnectorCatalogFrom(
			t.Context(), newSession(3), codexCatalogCached, directory, now.Add(25*time.Hour),
		); err == nil {
			t.Fatal("a directory over a day old stood in for a failed listing")
		}
	})
}

type codexDirectoryTransportStub struct {
	codexConnectorTransportStub
	mu        sync.Mutex
	previous  []*codexAppDirectory
	refreshes []codexCatalogRefresh
}

func (s *codexDirectoryTransportStub) ReadWithDirectory(
	_ context.Context,
	_ string,
	refresh codexCatalogRefresh,
	previous *codexAppDirectory,
) (codexConnectorCatalog, *codexAppDirectory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.previous = append(s.previous, previous)
	s.refreshes = append(s.refreshes, refresh)
	if previous != nil {
		return codexConnectorCatalog{}, previous, nil
	}
	return codexConnectorCatalog{}, &codexAppDirectory{readAt: time.Now()}, nil
}

func (s *codexDirectoryTransportStub) reads() ([]*codexAppDirectory, []codexCatalogRefresh) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*codexAppDirectory(nil), s.previous...),
		append([]codexCatalogRefresh(nil), s.refreshes...)
}

// A cold read must not wait on the app directory: it answers from the linked
// apps on an empty stand-in directory, lists the directory in the background,
// and the next read reuses the listing once it lands.
func TestCodexConnectorBackendListsTheFirstDirectoryInTheBackground(t *testing.T) {
	t.Parallel()

	transport := &codexDirectoryTransportStub{}
	backend := newCodexConnectorBackend("/usr/local/bin/codex", transport)
	defer backend.Close()
	landed := make(chan struct{}, 1)
	backend.SetDirectoryHandler(func() { landed <- struct{}{} })
	if _, err := backend.Discover(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-landed:
	case <-time.After(5 * time.Second):
		t.Fatal("the background directory listing never landed")
	}
	backend.mu.RLock()
	listed := backend.directory
	backend.mu.RUnlock()
	if listed == nil {
		t.Fatal("the background listing was not kept")
	}
	if _, err := backend.DiscoverLinked(t.Context()); err != nil {
		t.Fatal(err)
	}

	previous, refreshes := transport.reads()
	if len(previous) != 3 {
		t.Fatalf("reads = %d, want the cold read, the background listing and the linked read", len(previous))
	}
	standIn, background := -1, -1
	for index := range 2 {
		if previous[index] == nil {
			background = index
		} else {
			standIn = index
		}
	}
	if standIn < 0 || background < 0 {
		t.Fatalf("previous directories = %v, want one stand-in and one listing", previous[:2])
	}
	if stand := previous[standIn]; len(stand.apps) != 0 || stand == listed {
		t.Fatalf("the cold read's stand-in = %+v, want an empty directory that is never kept", stand)
	}
	if refreshes[standIn] != codexCatalogCached || refreshes[background] != codexCatalogCached {
		t.Fatalf("cold refreshes = %v, want both cached", refreshes[:2])
	}
	if previous[2] != listed || refreshes[2] != codexCatalogLinked {
		t.Fatalf("linked read got %p (%v), want the listed directory %p", previous[2], refreshes[2], listed)
	}
}

// coldConnectTransportStub offers one unlinked app, connectable only through
// the URL its directory entry carries.
type coldConnectTransportStub struct {
	codexConnectorTransportStub
	mu       sync.Mutex
	listings int
}

const coldConnectApp = "connector_2128aebfecb84f64a069897515042a44"

func (s *coldConnectTransportStub) ReadWithDirectory(
	_ context.Context,
	_ string,
	_ codexCatalogRefresh,
	previous *codexAppDirectory,
) (codexConnectorCatalog, *codexAppDirectory, error) {
	directory := previous
	if directory == nil {
		s.mu.Lock()
		s.listings++
		s.mu.Unlock()
		directory = &codexAppDirectory{
			apps:   map[string]codexAppCandidate{coldConnectApp: {}},
			order:  []string{coldConnectApp},
			readAt: time.Now(),
		}
	}
	catalog := codexConnectorCatalog{actions: make(map[string]codexConnectorAction)}
	if _, listed := directory.apps[coldConnectApp]; listed {
		catalog.actions[coldConnectApp] = codexConnectorAction{
			source:     CodexConnectorSourceHostedApp,
			connectURL: "https://chatgpt.com/apps/gmail/" + coldConnectApp,
			status:     CodexConnectorStatusAvailable,
		}
	}
	return catalog, directory, nil
}

// Connecting an app right after the daemon starts waits for the directory
// that carries its connect URL instead of answering not found.
func TestCodexConnectorBackendConnectWaitsForTheFirstDirectory(t *testing.T) {
	t.Parallel()

	transport := &coldConnectTransportStub{}
	backend := newCodexConnectorBackend("/usr/local/bin/codex", transport)
	defer backend.Close()
	if _, err := backend.Discover(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	result, err := backend.Connect(t.Context(), CodexConnectorSourceHostedApp, coldConnectApp)
	if err != nil {
		t.Fatalf("Connect before the directory landed: %v", err)
	}
	if result.AuthorizationURL == "" {
		t.Fatal("Connect answered no authorization URL")
	}
	transport.mu.Lock()
	listings := transport.listings
	transport.mu.Unlock()
	if listings != 1 {
		t.Fatalf("directory listings = %d, want the one background listing", listings)
	}
}

// A forced read lists the directory itself, as before.
func TestCodexConnectorBackendForcedColdReadListsTheDirectory(t *testing.T) {
	t.Parallel()

	transport := &codexDirectoryTransportStub{}
	backend := newCodexConnectorBackend("/usr/local/bin/codex", transport)
	if _, err := backend.Discover(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	backend.Close()
	previous, refreshes := transport.reads()
	if len(previous) != 1 || previous[0] != nil || refreshes[0] != codexCatalogForced {
		t.Fatalf("reads = %v %v, want one forced listing", previous, refreshes)
	}
	if backend.directory == nil {
		t.Fatal("the forced listing was not kept")
	}
}

// After a forced app/list Codex sends app/list/updated with the whole
// directory in one frame, about 1.4 KB per app, so the frame limit must hold
// the item limit (at 2 KB per app) or discovery fails before it is reached.
func TestCodexConnectorFrameLimitHoldsTheItemLimit(t *testing.T) {
	t.Parallel()

	const bytesPerApp = 2 << 10
	if codexConnectorMessageLimit < codexConnectorMaximumItems*bytesPerApp {
		t.Fatalf("codexConnectorMessageLimit = %d, want at least %d for %d apps",
			codexConnectorMessageLimit, codexConnectorMaximumItems*bytesPerApp, codexConnectorMaximumItems)
	}
	if maximumConnectorDiscoveryItems < codexConnectorMaximumItems {
		t.Fatalf("maximumConnectorDiscoveryItems = %d, below the Codex read's %d",
			maximumConnectorDiscoveryItems, codexConnectorMaximumItems)
	}
}

func TestHostedCodexConnectURLIsStrictlyChatGPTHTTPS(t *testing.T) {
	t.Parallel()

	tests := []struct {
		value string
		want  string
	}{
		{value: "https://chatgpt.com/apps/drive/native-id", want: "https://chatgpt.com/apps/drive/native-id"},
		{value: "http://chatgpt.com/apps/drive/native-id"},
		{value: "https://chatgpt.com.evil.test/apps/drive/native-id"},
		{value: "https://user:pass@chatgpt.com/apps/drive/native-id"},
		{value: "https://chatgpt.com/apps/drive/native-id?token=secret"},
		{value: "https://chatgpt.com/not-apps/native-id"},
	}
	for _, test := range tests {
		if got := validatedHostedConnectURL(test.value); got != test.want {
			t.Errorf("validatedHostedConnectURL(%q) = %q, want %q", test.value, got, test.want)
		}
	}
}

func TestCodexOAuthURLFitsLocalOperationContract(t *testing.T) {
	t.Parallel()

	valid := "https://auth.example.test/start?state=" + strings.Repeat("a", 4000)
	if validatedCodexOAuthURL(valid) == "" {
		t.Fatal("bounded OAuth URL was rejected")
	}
	oversized := "https://auth.example.test/start?state=" + strings.Repeat("a", 4097)
	if validatedCodexOAuthURL(oversized) != "" {
		t.Fatal("OAuth URL larger than the local operation contract was accepted")
	}
}

func TestCodexConnectorNativeIDRejectsPathAndArgumentSyntax(t *testing.T) {
	t.Parallel()

	for _, valid := range []string{"connector_123", "plugin:name@example", "mcp-server.1"} {
		if !validCodexConnectorNativeID(valid) {
			t.Errorf("valid native ID %q was rejected", valid)
		}
	}
	for _, invalid := range []string{
		"/Users/example/.codex", "../config.toml", "--header=secret", "name with space", "name\\path",
	} {
		if validCodexConnectorNativeID(invalid) {
			t.Errorf("unsafe native ID %q was accepted", invalid)
		}
	}
}

type codexOAuthFixture struct {
	SuccessResponse     json.RawMessage      `json:"successResponse"`
	StartupNotification codexRPCNotification `json:"startupNotification"`
	SuccessNotification codexRPCNotification `json:"successNotification"`
	FailureNotification codexRPCNotification `json:"failureNotification"`
	MalformedResponse   json.RawMessage      `json:"malformedResponse"`
}

type oauthRPCFixture struct {
	response      json.RawMessage
	notifications []codexRPCNotification
	requestMethod string
	requestParams map[string]any
	closed        bool
}

func (s *oauthRPCFixture) request(
	_ context.Context,
	method string,
	params any,
) (json.RawMessage, error) {
	s.requestMethod = method
	s.requestParams, _ = params.(map[string]any)
	return s.response, nil
}

func (s *oauthRPCFixture) nextNotification(ctx context.Context) (codexRPCNotification, error) {
	if len(s.notifications) == 0 {
		<-ctx.Done()
		return codexRPCNotification{}, ctx.Err()
	}
	notification := s.notifications[0]
	s.notifications = s.notifications[1:]
	return notification, nil
}

func (s *oauthRPCFixture) drainNotifications() ([]codexRPCNotification, bool) {
	notifications := append([]codexRPCNotification(nil), s.notifications...)
	s.notifications = nil
	return notifications, false
}

func (s *oauthRPCFixture) close() { s.closed = true }

func TestStartCodexMCPOAuthReturnsURLAndConsumesCompletionNotification(t *testing.T) {
	t.Parallel()

	fixture := loadCodexOAuthFixture(t)
	session := &oauthRPCFixture{
		response: fixture.SuccessResponse,
		notifications: []codexRPCNotification{
			fixture.StartupNotification,
			fixture.SuccessNotification,
		},
	}
	flow, err := startCodexMCPOAuth(t.Context(), session, "figma")
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	if flow.NativeID != "figma" || !strings.HasPrefix(flow.AuthorizationURL, "https://auth.example.test/") {
		t.Fatalf("flow = %+v", flow)
	}
	if session.requestMethod != "mcpServer/oauth/login" || session.requestParams["name"] != "figma" {
		t.Fatalf("OAuth request = %s %#v", session.requestMethod, session.requestParams)
	}
	result, err := flow.Wait(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.NativeID != "figma" || result.Status != CodexMCPOAuthStatusSucceeded {
		t.Fatalf("OAuth result = %+v", result)
	}
	if !session.closed {
		t.Fatal("completed OAuth session was not closed")
	}
}

func TestStartCodexMCPOAuthSanitizesFailureAndRejectsMalformedURL(t *testing.T) {
	t.Parallel()

	fixture := loadCodexOAuthFixture(t)
	failureSession := &oauthRPCFixture{
		response:      fixture.SuccessResponse,
		notifications: []codexRPCNotification{fixture.FailureNotification},
	}
	flow, err := startCodexMCPOAuth(t.Context(), failureSession, "figma")
	if err != nil {
		t.Fatal(err)
	}
	result, err := flow.Wait(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != CodexMCPOAuthStatusFailed {
		t.Fatalf("OAuth result = %+v", result)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "SECRET") || strings.Contains(string(encoded), "/Users/") {
		t.Fatalf("OAuth result leaked provider error: %s", encoded)
	}

	malformedSession := &oauthRPCFixture{response: fixture.MalformedResponse}
	if _, err := startCodexMCPOAuth(t.Context(), malformedSession, "figma"); err == nil {
		t.Fatal("insecure OAuth URL was accepted")
	}
	if !malformedSession.closed {
		t.Fatal("malformed OAuth session was not closed")
	}
}

func TestCodexConnectorPublicConstantsStayStable(t *testing.T) {
	t.Parallel()

	if !reflect.DeepEqual(
		[]CodexConnectorStatus{
			CodexConnectorStatusConnected,
			CodexConnectorStatusNeedsAuth,
			CodexConnectorStatusAvailable,
			CodexConnectorStatusInstalledNotReady,
		},
		[]CodexConnectorStatus{"connected", "needs_auth", "available", "installed_not_ready"},
	) {
		t.Fatal("connector statuses changed")
	}
	if CodexConnectorSourceHostedApp != "codex_app" ||
		CodexConnectorSourceLocalMCP != "codex_mcp" {
		t.Fatal("connector sources changed")
	}
	if !reflect.DeepEqual(
		[]CodexConnectorAction{
			CodexConnectorActionInstallAndAuth,
			CodexConnectorActionAuthenticate,
			CodexConnectorActionNone,
		},
		[]CodexConnectorAction{"install_and_auth", "authenticate", "none"},
	) {
		t.Fatal("connector actions changed")
	}
}

type codexConnectorTransportStub struct {
	mu          sync.Mutex
	catalog     codexConnectorCatalog
	discoveries []bool
	oauthID     string
	oauthStarts int

	oauthFlow *CodexMCPOAuthFlow
}

func (s *codexConnectorTransportStub) Read(
	_ context.Context,
	_ string,
	force bool,
) (codexConnectorCatalog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.discoveries = append(s.discoveries, force)
	return cloneCodexConnectorCatalog(s.catalog), nil
}

func (s *codexConnectorTransportStub) StartMCPOAuth(
	_ context.Context,
	_ string,
	nativeID string,
) (*CodexMCPOAuthFlow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.oauthID = nativeID
	s.oauthStarts++
	return s.oauthFlow, nil
}

type codexInstalledAppsTransportStub struct {
	codexConnectorTransportStub
	installed map[string]codexInstalledCandidate
	reads     int
}

func (s *codexInstalledAppsTransportStub) ReadInstalledApps(
	context.Context,
	string,
) (map[string]codexInstalledCandidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	return maps.Clone(s.installed), nil
}

// A connect page in the browser has no callback into Zenith Team, so the desktop
// asks whether the app is connected yet; checks in quick succession share one
// live read. The answer follows discovery's rule: a linked app Codex cannot
// call yet is installed_not_ready there, so it is not connected here.
func TestCodexConnectorBackendReadsHostedAppConnectionLive(t *testing.T) {
	t.Parallel()

	transport := &codexInstalledAppsTransportStub{
		installed: map[string]codexInstalledCandidate{
			"connector_gmail": {name: "Gmail", enabled: true, callable: true},
			"connector_mail":  {name: "Mail", enabled: true},
		},
	}
	backend := newCodexConnectorBackend("/usr/local/bin/codex", transport)
	defer backend.Close()

	connected, err := backend.HostedAppConnected(t.Context(), "connector_gmail")
	if err != nil || !connected {
		t.Fatalf("HostedAppConnected(gmail) = %v, %v; want true", connected, err)
	}
	connected, err = backend.HostedAppConnected(t.Context(), "connector_drive")
	if err != nil || connected {
		t.Fatalf("HostedAppConnected(drive) = %v, %v; want false", connected, err)
	}
	connected, err = backend.HostedAppConnected(t.Context(), "connector_mail")
	if err != nil || connected {
		t.Fatalf("HostedAppConnected(linked, not callable) = %v, %v; want false", connected, err)
	}
	if transport.reads != 1 {
		t.Fatalf("installed-app reads = %d, want 1", transport.reads)
	}
	if _, err := backend.HostedAppConnected(t.Context(), "bad id"); !errors.Is(
		err, ErrCodexConnectorsUnavailable,
	) {
		t.Fatalf("invalid ID error = %v, want ErrCodexConnectorsUnavailable", err)
	}
	unsupported := newCodexConnectorBackend("/usr/local/bin/codex", &codexConnectorTransportStub{})
	if _, err := unsupported.HostedAppConnected(t.Context(), "connector_gmail"); !errors.Is(
		err, ErrCodexConnectorsUnavailable,
	) {
		t.Fatalf("readerless transport error = %v, want ErrCodexConnectorsUnavailable", err)
	}
}

func TestCodexConnectorBackendDiscoversBeforeExactNativeConnect(t *testing.T) {
	t.Parallel()

	oauthSession := &oauthRPCFixture{}
	transport := &codexConnectorTransportStub{catalog: codexConnectorCatalog{
		connectors: []CodexConnector{
			{
				Source: CodexConnectorSourceHostedApp, NativeID: "app-available",
				Name: "Available", Status: CodexConnectorStatusAvailable,
			},
			{
				Source: CodexConnectorSourceLocalMCP, NativeID: "slack",
				Name: "Slack", Status: CodexConnectorStatusNeedsAuth,
			},
		},
		actions: map[string]codexConnectorAction{
			"app-available": {
				source: CodexConnectorSourceHostedApp, status: CodexConnectorStatusAvailable,
				connectURL: "https://chatgpt.com/apps/available/app-available",
			},
			"slack": {
				source: CodexConnectorSourceLocalMCP, status: CodexConnectorStatusNeedsAuth,
				oauth: true,
			},
		},
	}, oauthFlow: &CodexMCPOAuthFlow{
		NativeID: "slack", AuthorizationURL: "https://auth.example.test/start",
		session: oauthSession, done: make(chan struct{}),
	}}
	backend := newCodexConnectorBackend("/usr/local/bin/codex", transport)
	defer backend.Close()

	connect, err := backend.Connect(
		t.Context(),
		CodexConnectorSourceHostedApp,
		"app-available",
	)
	if err != nil {
		t.Fatal(err)
	}
	const wantAuthorizationURL = "https://chatgpt.com/apps/available/app-available" +
		"#settings/Connectors?add-connector-link=true&connector=app-available&" +
		"product-sku=CODEX&referrer=codex"
	if connect.AuthorizationURL != wantAuthorizationURL {
		t.Fatalf("authorization URL = %q, want %q", connect.AuthorizationURL, wantAuthorizationURL)
	}
	if !reflect.DeepEqual(transport.discoveries, []bool{false}) {
		t.Fatalf("discoveries = %v, want [false]", transport.discoveries)
	}
	if _, err := backend.Connect(
		t.Context(),
		CodexConnectorSourceLocalMCP,
		"app-available",
	); !errors.Is(err, ErrCodexConnectorNotConnectable) {
		t.Fatalf("source-confused connect error = %v", err)
	}
	oauth, err := backend.Connect(t.Context(), CodexConnectorSourceLocalMCP, "slack")
	if err != nil {
		t.Fatal(err)
	}
	if oauth.AuthorizationURL != "https://auth.example.test/start" || transport.oauthID != "slack" {
		t.Fatalf("OAuth connect = %+v, native ID = %q", oauth, transport.oauthID)
	}
	if _, err := backend.Discover(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(transport.discoveries, []bool{false, true}) {
		t.Fatalf("discoveries = %v", transport.discoveries)
	}
}

func TestCodexConnectorBackendDeduplicatesAndClosesOwnedOAuthFlow(t *testing.T) {
	t.Parallel()

	session := &oauthRPCFixture{}
	flow := &CodexMCPOAuthFlow{
		NativeID: "slack", AuthorizationURL: "https://auth.example.test/start",
		session: session, done: make(chan struct{}),
	}
	transport := &codexConnectorTransportStub{
		catalog: codexConnectorCatalog{
			connectors: []CodexConnector{{
				Source: CodexConnectorSourceLocalMCP, NativeID: "slack", Name: "Slack",
				Status: CodexConnectorStatusNeedsAuth,
			}},
			actions: map[string]codexConnectorAction{"slack": {
				source: CodexConnectorSourceLocalMCP,
				status: CodexConnectorStatusNeedsAuth,
				oauth:  true,
			}},
		},
		oauthFlow: flow,
	}
	backend := newCodexConnectorBackend("/usr/local/bin/codex", transport)
	changes := make(chan struct{}, 1)
	backend.SetChangeHandler(func() { changes <- struct{}{} })
	first, err := backend.Connect(t.Context(), CodexConnectorSourceLocalMCP, "slack")
	if err != nil {
		t.Fatal(err)
	}
	second, err := backend.Connect(t.Context(), CodexConnectorSourceLocalMCP, "slack")
	if err != nil {
		t.Fatal(err)
	}
	if transport.oauthStarts != 1 || first.AuthorizationURL != second.AuthorizationURL {
		t.Fatalf("OAuth starts = %d, results = %+v / %+v", transport.oauthStarts, first, second)
	}

	closed := make(chan struct{})
	go func() {
		backend.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("backend Close did not cancel and wait for its OAuth flow")
	}
	if !session.closed {
		t.Fatal("owned OAuth session was not closed")
	}
	select {
	case <-changes:
		t.Fatal("shutdown cancellation triggered a catalog refresh")
	default:
	}
	if _, err := backend.Connect(
		t.Context(), CodexConnectorSourceLocalMCP, "slack",
	); !errors.Is(err, ErrCodexConnectorsUnavailable) {
		t.Fatalf("Connect after Close error = %v", err)
	}
}

func loadCodexConnectorCatalogFixture(t *testing.T) codexConnectorCatalogFixture {
	t.Helper()
	var fixture codexConnectorCatalogFixture
	loadCodexConnectorFixture(t, "testdata/codex_connectors/catalog.json", &fixture)
	return fixture
}

func loadCodexOAuthFixture(t *testing.T) codexOAuthFixture {
	t.Helper()
	var fixture codexOAuthFixture
	loadCodexConnectorFixture(t, "testdata/codex_connectors/oauth.json", &fixture)
	return fixture
}

func loadCodexConnectorFixture(t *testing.T, path string, destination any) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, destination); err != nil {
		t.Fatal(err)
	}
}

// An app's logo is the first valid icon asset or logo URL that loads outside a
// ChatGPT session; some apps carry a chatgpt.com light 256_square and a
// files.openai.com dark one (measured 2026-09-25). A session-only logo is kept
// only when no other is valid.
func TestCodexAppIconURLPrefersALoadableLogo(t *testing.T) {
	t.Parallel()

	const (
		estuary = "https://chatgpt.com/backend-api/estuary/content?id=file_1&sig=abc"
		appPath = "/images/ecosystem/apps/gmail/icon.png"
		appURL  = "https://chatgpt.com" + appPath
		files   = "https://files.openai.com/content?cdn=1&id=file_2&sig=def"
		cdn     = "https://cdn.test/logo.png"
	)
	for _, test := range []struct {
		name        string
		light, dark map[string]string
		logo, dark2 string
		want        string
	}{
		{
			name:  "light square first",
			light: map[string]string{"256_square": files}, dark: map[string]string{"256_square": cdn},
			want: files,
		},
		{
			name:  "session-only light square, loadable dark square",
			light: map[string]string{"256_square": estuary}, dark: map[string]string{"256_square": files},
			want: files,
		},
		{
			name:  "loadable logo URL after session-only assets",
			light: map[string]string{"256_square": appPath, "original": estuary},
			logo:  cdn, want: cdn,
		},
		{
			name:  "first valid session-only kept",
			light: map[string]string{"256_square": "http://cdn.test/a.png", "original": appPath},
			dark:  map[string]string{"256_square": estuary}, want: estuary,
		},
		{name: "relative first-party path", light: map[string]string{"256_square": appPath}, want: appURL},
		{name: "none"},
	} {
		if got := codexAppIconURL(test.light, test.dark, test.logo, test.dark2); got != test.want {
			t.Errorf("%s: codexAppIconURL() = %q, want %q", test.name, got, test.want)
		}
	}
}
