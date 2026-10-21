package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLocalPreviewQueue_CoalescesAdjacentFrames(t *testing.T) {
	t.Parallel()

	queue := newLocalPreviewQueue(64)
	queue.enqueue(localRunPreviewFrame{
		CompanyID:      "company-1",
		RunID:          "run-1",
		ConversationID: "conversation-1",
		ItemKey:        "item-1",
		Channel:        localPreviewChannelText,
		Operation:      localPreviewOperationAppend,
		Content:        "hello ",
	})
	queue.enqueue(localRunPreviewFrame{
		CompanyID:      "company-1",
		RunID:          "run-1",
		ConversationID: "conversation-1",
		ItemKey:        "item-1",
		Channel:        localPreviewChannelText,
		Operation:      localPreviewOperationAppend,
		Content:        "world",
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, ok := queue.next(ctx)
	if !ok {
		t.Fatal("next() = closed, want frame")
	}
	if got.Content != "hello world" {
		t.Fatalf("Content = %q, want %q", got.Content, "hello world")
	}
}

func TestLocalPreviewQueue_ReplaceSupersedesPendingReplacement(t *testing.T) {
	t.Parallel()

	queue := newLocalPreviewQueue(64)
	base := localRunPreviewFrame{
		CompanyID:      "company-1",
		RunID:          "run-1",
		ConversationID: "conversation-1",
		ItemKey:        "item-1",
		Channel:        localPreviewChannelText,
		Operation:      localPreviewOperationReplace,
	}
	first := base
	first.Content = "draft"
	queue.enqueue(first)
	second := base
	second.Content = "final preview"
	queue.enqueue(second)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, ok := queue.next(ctx)
	if !ok {
		t.Fatal("next() = closed, want frame")
	}
	if got.Content != "final preview" || got.Operation != localPreviewOperationReplace {
		t.Fatalf("frame = %+v, want latest replacement", got)
	}
}

func TestLocalPreviewQueue_DropsOldestFrameAtByteLimit(t *testing.T) {
	t.Parallel()

	queue := newLocalPreviewQueue(5)
	queue.enqueue(localRunPreviewFrame{
		RunID:     "run-1",
		ItemKey:   "old",
		Channel:   localPreviewChannelText,
		Operation: localPreviewOperationAppend,
		Content:   "1234",
	})
	queue.enqueue(localRunPreviewFrame{
		RunID:     "run-1",
		ItemKey:   "new",
		Channel:   localPreviewChannelText,
		Operation: localPreviewOperationAppend,
		Content:   "abc",
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, ok := queue.next(ctx)
	if !ok {
		t.Fatal("next() = closed, want frame")
	}
	if got.ItemKey != "new" || got.Content != "abc" {
		t.Fatalf("frame = %+v, want newest frame", got)
	}
}

func TestLocalPreviewQueue_DropsOldestFrameAtFrameLimit(t *testing.T) {
	t.Parallel()

	queue := newLocalPreviewQueue(1 << 20)
	for index := range 257 {
		queue.enqueue(localRunPreviewFrame{
			CompanyID:      "company-1",
			RunID:          "run-1",
			ConversationID: "conversation-1",
			ItemKey:        fmt.Sprintf("item-%d", index),
			Channel:        localPreviewChannelText,
			Operation:      localPreviewOperationAppend,
			Content:        "x",
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, ok := queue.next(ctx)
	if !ok {
		t.Fatal("next() = closed, want frame")
	}
	if got.ItemKey != "item-1" {
		t.Fatalf("ItemKey = %q, want oldest retained frame", got.ItemKey)
	}
}

func TestLocalPreviewHub_CapsConcurrentSubscribers(t *testing.T) {
	t.Parallel()

	hub := newLocalPreviewHub(64 << 10)
	unsubscribes := make([]func(), 0, 4)
	for range 4 {
		_, unsubscribe, ok := hub.subscribe()
		if !ok {
			t.Fatal("subscribe() rejected a subscriber below the cap")
		}
		unsubscribes = append(unsubscribes, unsubscribe)
	}
	if _, _, ok := hub.subscribe(); ok {
		t.Fatal("subscribe() accepted a subscriber above the cap")
	}
	for _, unsubscribe := range unsubscribes {
		unsubscribe()
	}
	if _, unsubscribe, ok := hub.subscribe(); !ok {
		t.Fatal("subscribe() did not release capacity")
	} else {
		unsubscribe()
	}
}

func TestDaemonLocalPreviewHandler_RequiresHeaderCapability(t *testing.T) {
	t.Parallel()

	d := &Daemon{
		localPreview:      newLocalPreviewHub(64 << 10),
		localPreviewToken: "local-capability",
	}
	server := httptest.NewServer(d.localPreviewHandler())
	t.Cleanup(server.Close)

	tests := []struct {
		name          string
		target        string
		authorization string
		wantStatus    int
	}{
		{
			name:       "missing capability",
			target:     server.URL,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "query capability is rejected",
			target:     server.URL + "?capability=local-capability",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:          "wrong capability",
			target:        server.URL,
			authorization: "Bearer wrong",
			wantStatus:    http.StatusUnauthorized,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			req, err := http.NewRequestWithContext(
				context.Background(),
				http.MethodGet,
				test.target,
				nil,
			)
			if err != nil {
				t.Fatalf("NewRequestWithContext() error = %v", err)
			}
			if test.authorization != "" {
				req.Header.Set("Authorization", test.authorization)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("Do() error = %v", err)
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, resp.Body)
			if resp.StatusCode != test.wantStatus {
				t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, test.wantStatus)
			}
		})
	}
}

func TestDaemonLocalPreviewHandler_StreamsPublishedFrame(t *testing.T) {
	t.Parallel()

	d := &Daemon{
		localPreview:      newLocalPreviewHub(64 << 10),
		localPreviewToken: "local-capability",
	}
	server := httptest.NewServer(d.localPreviewHandler())
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext() error = %v", err)
	}
	req.Header.Set("Authorization", "Bearer local-capability")

	type responseResult struct {
		response *http.Response
		err      error
	}
	responseReady := make(chan responseResult, 1)
	go func() {
		response, requestErr := http.DefaultClient.Do(req)
		responseReady <- responseResult{response: response, err: requestErr}
	}()

	var response *http.Response
	select {
	case result := <-responseReady:
		if result.err != nil {
			t.Fatalf("Do() error = %v", result.err)
		}
		response = result.response
	case <-ctx.Done():
		t.Fatal("stream response headers were not flushed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("StatusCode = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if got := response.Header.Get("Content-Type"); got != "application/x-ndjson" {
		t.Fatalf("Content-Type = %q, want application/x-ndjson", got)
	}

	want := localRunPreviewFrame{
		Type:           "run.preview.delta",
		CompanyID:      "company-1",
		RunID:          "run-1",
		ConversationID: "conversation-1",
		ItemKey:        "item-1",
		Channel:        localPreviewChannelText,
		Operation:      localPreviewOperationAppend,
		Content:        "hello",
	}
	d.publishLocalRunPreview(want)

	reader := bufio.NewReader(response.Body)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("ReadString(reset) error = %v", err)
	}
	var reset localPreviewResetFrame
	if err := json.Unmarshal([]byte(line), &reset); err != nil {
		t.Fatalf("Unmarshal(reset) error = %v", err)
	}
	if reset.Type != "run.preview.reset" {
		t.Fatalf("reset Type = %q, want %q", reset.Type, "run.preview.reset")
	}

	line, err = reader.ReadString('\n')
	if err != nil {
		t.Fatalf("ReadString(frame) error = %v", err)
	}
	var got localRunPreviewFrame
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got != want {
		t.Fatalf("frame = %+v, want %+v", got, want)
	}
}

func TestDaemonShutdownPreservesLocalPreviewConfig(t *testing.T) {
	for _, test := range []struct {
		name    string
		replace bool
	}{
		{name: "current daemon"},
		{name: "replacement daemon", replace: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			defer listener.Close()
			replacement := &Daemon{cfg: Config{LaunchedBy: "desktop"}, logger: testLogger()}
			// The preflight response must complete before Run returns, so retain
			// its one result until the test can inspect the shutdown outcome.
			prepared := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/daemon/config" {
					_, _ = w.Write([]byte(`{"studioApiOrigin":"https://studio.example"}`))
					return
				}
				if r.URL.Path != "/api/v1/daemon/companies" {
					http.NotFound(w, r)
					return
				}
				var prepareErr error
				if test.replace {
					prepareErr = replacement.prepareLocalPreview(listener)
				}
				prepared <- prepareErr
				// Fail after local startup to exercise the old daemon's complete
				// shutdown after a replacement has published its capability.
				http.Error(w, "preflight unavailable", http.StatusServiceUnavailable)
			}))
			defer server.Close()
			daemon := newTestDaemon(t, server.URL)
			daemon.cfg.LaunchedBy = "desktop"
			t.Setenv("ZENITH_TEAM_HOME", home)
			writeCredential(t, server.URL, "access-1", "refresh-1", time.Hour)
			if err := daemon.Run(t.Context()); err == nil {
				t.Fatal("Run() error = nil, want preflight failure")
			}
			select {
			case err := <-prepared:
				if err != nil {
					t.Fatalf("prepare replacement: %v", err)
				}
			default:
				t.Fatal("Run() exited before local preview was prepared")
			}
			raw, err := os.ReadFile(filepath.Join(home, "local-preview.json"))
			if err != nil {
				t.Fatalf("read local preview config after daemon shutdown: %v", err)
			}
			var config localPreviewConfig
			if err := json.Unmarshal(raw, &config); err != nil {
				t.Fatalf("decode retained local preview config: %v", err)
			}
			wantCapability := daemon.localPreviewToken
			if test.replace {
				wantCapability = replacement.localPreviewToken
				if validLocalPreviewAuthorization("Bearer "+daemon.localPreviewToken, wantCapability) {
					t.Fatal("replacement daemon accepts the retired capability")
				}
				if want := "http://" + listener.Addr().
					String() +
					localPreviewPath; config.Endpoint != want {
					t.Fatalf("retained endpoint = %q, want %q", config.Endpoint, want)
				}
			}
			if wantCapability == "" || config.Capability != wantCapability {
				t.Fatal("retained capability does not match the newest daemon")
			}
		})
	}
}

func TestWriteLocalPreviewConfig_CreatesPrivateCapabilityFile(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path, err := writeLocalPreviewConfig(
		directory,
		"http://127.0.0.1:19714/local-preview",
		"local-capability",
	)
	if err != nil {
		t.Fatalf("writeLocalPreviewConfig() error = %v", err)
	}
	if path != filepath.Join(directory, "local-preview.json") {
		t.Fatalf("path = %q, want local preview config path", path)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if strings.Contains(string(raw), "?capability=") {
		t.Fatal("capability leaked into endpoint URL")
	}
	var got localPreviewConfig
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got.Version != 1 || got.Endpoint != "http://127.0.0.1:19714/local-preview" ||
		got.Capability != "local-capability" {
		t.Fatalf("config = %+v, want endpoint and separate capability", got)
	}
}
