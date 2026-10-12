package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLoadServerConfigUsesServerOrigin(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, body, want string
		status           int
		wantError        bool
	}{
		{
			name:   "server override",
			body:   `{"studioApiOrigin":"http://localhost:8000/"}`,
			want:   "http://localhost:8000",
			status: 200,
		},
		{name: "missing origin", body: `{}`, status: 200, wantError: true},
		{name: "invalid origin", body: `{"studioApiOrigin":"http://remote.example"}`, status: 200, wantError: true},
		{name: "unavailable", body: `{}`, status: 503, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/api/v1/daemon/config" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer user-session" {
					t.Error("server config request missing user session")
				}
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			d := &Daemon{
				client: NewClient(server.URL),
				cfg:    Config{StudioAPIOrigin: "https://old.example"},
				tools:  newLocalToolService(t.TempDir()),
			}
			d.client.SetToken("user-session")
			d.tools.studioOrigin = d.cfg.StudioAPIOrigin
			err := d.loadServerConfig(context.Background())
			if (err != nil) != test.wantError {
				t.Fatalf("loadServerConfig() error = %v, wantError %v", err, test.wantError)
			}
			if test.wantError {
				if d.cfg.StudioAPIOrigin != "https://old.example" ||
					d.tools.studioOrigin != "https://old.example" {
					t.Fatal("failed config changed origin")
				}
				return
			}
			if d.cfg.StudioAPIOrigin != test.want || d.tools.studioOrigin != test.want {
				t.Fatalf(
					"origin = %q / %q, want %q",
					d.cfg.StudioAPIOrigin,
					d.tools.studioOrigin,
					test.want,
				)
			}
			release, _ := findToolRelease("zenith-studio")
			scope := toolAuthScope{"22222222-2222-4222-8222-222222222222", testToolAgentID}
			if mustToolAuthHome(
				t,
				d.tools,
				scope,
				release,
			) == mustToolAuthHome(
				t,
				newLocalToolService(d.tools.root),
				scope,
				release,
			) {
				t.Fatal("override reused default origin credential home")
			}
		})
	}
}
