package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"

	"github.com/google/uuid"
)

// testStudioOrigin turns the optional Zenith Studio integration on.
const testStudioOrigin = "https://studio.example"

func TestStudioRunEnvironmentLaunchesConversationScopedCommand(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX command invocation")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is unavailable")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tools/studio-project/11111111-1111-4111-8111-111111111111/33333333-3333-4333-8333-333333333333" ||
			r.Header.Get("Authorization") != "Bearer live-run-proof" {
			t.Errorf("incorrect Run route or proof: %s", r.URL.Path)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(protocol.StudioProject{ProjectID: "this-chat-project", Title: "Campaign"})
	}))
	defer server.Close()
	root := t.TempDir()
	release, _ := findToolRelease("zenith-studio")
	environment := map[string]string{}
	run := Run{
		ID:        uuid.MustParse("33333333-3333-4333-8333-333333333333"),
		CompanyID: "company-a",
		RuntimeID: "11111111-1111-4111-8111-111111111111",
	}
	_, err = prepareStudioRun(
		t.Context(),
		studioRunInput{
			Enabled:  true,
			EnvRoot:  root,
			Run:      run,
			APIURL:   testStudioOrigin,
			Endpoint: server.URL + "/v1/tools/studio-project/" + run.RuntimeID + "/" + run.ID.String(),
			RunToken: "live-run-proof",
		},
		func(context.Context, string, toolRelease) (toolInstallation, error) {
			return toolInstallation{Node: node, Entrypoint: filepath.Join(root, "must-not-run.cjs")}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(root, ".managed-tools", release.Command)
	output, err := runToolProcess(
		t.Context(),
		toolProcess{Executable: command, Args: []string{"--json", "projects", "list"}, Env: environment},
	)
	if err != nil || !bytes.Contains(output, []byte("this-chat-project")) {
		t.Fatalf("managed project command = %s, %v", output, err)
	}
}

func TestStudioLauncherPreservesUpstreamFailure(t *testing.T) {
	t.Parallel()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is unavailable")
	}
	root := t.TempDir()
	entry := filepath.Join(root, "cli.cjs")
	want := `{"ok":false,"error":{"code":"download_size_mismatch","retryable":false}}` + "\n"
	if err := os.WriteFile(
		entry,
		[]byte(
			`if(process.argv.includes("auth")){console.log(JSON.stringify({ok:true,data:{authenticated:true}}));process.exit(0);}console.log('{"ok":false,"error":{"code":"download_size_mismatch","retryable":false}}');process.exitCode=5;`,
		),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(protocol.StudioProject{ProjectID: "project"})
	}))
	defer server.Close()
	installed, err := writeStudioLauncher(
		root,
		toolInstallation{Node: node, Entrypoint: entry},
		studioLauncherConfig{Endpoint: server.URL},
	)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(
		t.Context(),
		node,
		installed.Entrypoint,
		"--json",
		"assets",
		"download",
		"project",
		"node",
	)
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 5 {
		t.Fatalf("expected CLI exit 5, got %v: %s", err, output)
	}
	if string(output) != want {
		t.Fatalf("expected one unchanged CLI error, got %s", output)
	}
}

func TestStudioLauncherReportsTeamBindingFailure(t *testing.T) {
	t.Parallel()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is unavailable")
	}
	for _, tt := range []struct {
		name   string
		status int
		code   string
		want   string
	}{
		{"run proof", 404, "studio_project_run_not_found", "studio_project_run_not_found"},
		{"session", 401, "session_invalid", "studio_team_auth_required"},
		{"runtime", 404, "runtime_not_found", "studio_runtime_unavailable"},
		{"invalid operation", 422, "studio_project_invalid", "studio_project_invalid"},
		{"conflict", 409, "studio_project_conflict", "studio_project_conflict"},
		{"server unavailable", 503, "internal_error", "studio_project_unavailable"},
		{"unknown error", 400, "private-upstream-value", "studio_project_unavailable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_ = json.NewEncoder(w).
					Encode(map[string]string{"code": tt.code, "detail": "private-upstream-value"})
			}))
			defer cloud.Close()
			d := &Daemon{client: NewClient(cloud.URL), cfg: Config{StudioAPIOrigin: testStudioOrigin}}
			mux := http.NewServeMux()
			mux.HandleFunc(
				"POST /v1/tools/studio-project/{runtimeId}/{runId}",
				d.localStudioProjectHandler(),
			)
			bridge := httptest.NewServer(mux)
			defer bridge.Close()
			root := t.TempDir()
			installed, err := writeStudioLauncher(
				root,
				toolInstallation{Node: node, Entrypoint: filepath.Join(root, "must-not-run.cjs")},
				studioLauncherConfig{
					Endpoint: bridge.URL + "/v1/tools/studio-project/11111111-1111-4111-8111-111111111111/33333333-3333-4333-8333-333333333333",
					RunToken: "private-run-proof",
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(
				t.Context(),
				node,
				installed.Entrypoint,
				"--json",
				"projects",
				"list",
			)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			output, err := cmd.Output()
			if err == nil {
				t.Fatal("binding failure returned success")
			}
			var result struct {
				OK    bool `json:"ok"`
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(output, &result); err != nil {
				t.Fatal(err)
			}
			if result.OK || result.Error.Code != tt.want {
				t.Fatalf("response = %s, want code %s", output, tt.want)
			}
			for _, secret := range []string{"private-upstream-value", "private-run-proof"} {
				if bytes.Contains(output, []byte(secret)) || strings.Contains(stderr.String(), secret) {
					t.Fatal("binding failure exposed private data")
				}
			}
		})
	}
}

func TestStudioLauncherChecksAuthenticationBeforeReservingProject(t *testing.T) {
	t.Parallel()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is unavailable")
	}
	root := t.TempDir()
	entry := filepath.Join(root, "cli.cjs")
	if err := os.WriteFile(entry, []byte(`process.exit(1);`), 0o600); err != nil {
		t.Fatal(err)
	}
	var reservations atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request protocol.StudioProjectRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request.Action == "reserve" {
			reservations.Add(1)
		}
		_ = json.NewEncoder(w).Encode(protocol.StudioProject{ID: "reservation", CanCreate: true})
	}))
	defer server.Close()
	installed, err := writeStudioLauncher(
		root,
		toolInstallation{Node: node, Entrypoint: entry},
		studioLauncherConfig{Endpoint: server.URL, RunToken: "secret"},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runToolProcess(
		t.Context(),
		toolProcess{
			Executable: node,
			Args: []string{
				installed.Entrypoint,
				"--json",
				"projects",
				"create",
				"--title",
				"Campaign",
			},
		},
	)
	if err == nil || reservations.Load() != 0 {
		t.Fatalf(
			"unavailable Studio account left a reservation: err=%v reservations=%d",
			err,
			reservations.Load(),
		)
	}
}

func TestStudioLauncherRecoversRemoteCreationBeforeBinding(t *testing.T) {
	t.Parallel()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is unavailable")
	}
	root := t.TempDir()
	entry := filepath.Join(root, "cli.cjs")
	if err := os.WriteFile(
		entry,
		[]byte(
			`if(process.argv.includes("auth")){console.log(JSON.stringify({ok:true,data:{authenticated:true}}));process.exit(0);}const fs=require('node:fs');const args=process.argv.slice(2),file=process.env.REMOTE_PROJECT;let data;if(args.includes('create')){if(fs.existsSync(file))process.exit(9);data={id:'11111111-1111-4111-8111-111111111111',title:'Campaign',description:args[args.indexOf('--description')+1]};fs.writeFileSync(file,JSON.stringify(data));}else{data={projects:[{id:'old-project',title:'Campaign',description:'unrelated'},...(fs.existsSync(file)?[JSON.parse(fs.readFileSync(file))]:[])]};}console.log(JSON.stringify({ok:true,data}));`,
		),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	current := protocol.StudioProject{ID: "22222222-2222-4222-8222-222222222222", Title: "Campaign"}
	reserved, failed := false, false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request protocol.StudioProjectRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		mu.Lock()
		defer mu.Unlock()
		current.CanCreate = request.Action == "reserve" && !reserved
		if request.Action == "reserve" {
			reserved = true
		}
		if request.Action == "bind" {
			if !failed {
				failed = true
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			current.ProjectID = request.ProjectID
		}
		_ = json.NewEncoder(w).Encode(current)
	}))
	defer server.Close()
	installed, err := writeStudioLauncher(
		root,
		toolInstallation{Node: node, Entrypoint: entry},
		studioLauncherConfig{Endpoint: server.URL, RunToken: "secret"},
	)
	if err != nil {
		t.Fatal(err)
	}
	invoke := func() ([]byte, error) {
		return runToolProcess(
			t.Context(),
			toolProcess{
				Executable: node,
				Args: []string{
					installed.Entrypoint,
					"--json",
					"projects",
					"create",
					"--title",
					"Campaign",
				},
				Env: map[string]string{"REMOTE_PROJECT": filepath.Join(root, "remote.json")},
			},
		)
	}
	if _, err := invoke(); err == nil {
		t.Fatal("lost bind response returned success")
	}
	output, err := invoke()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(output, []byte("11111111-1111-4111-8111-111111111111")) {
		t.Fatalf("recovered = %s", output)
	}
	mu.Lock()
	defer mu.Unlock()
	if current.ProjectID != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("bound = %+v", current)
	}
}

func TestLocalStudioProjectBridgePreservesDaemonAndRunProof(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer user-session" ||
			r.Header.Get(daemonIDHeader) != "daemon-a" {
			t.Error("missing daemon identity")
		}
		if r.URL.Path != "/api/v1/daemon/runtimes/11111111-1111-4111-8111-111111111111/runs/33333333-3333-4333-8333-333333333333/studio-project" {
			t.Error(r.URL.Path)
		}
		var request protocol.StudioProjectRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request.RunToken != "live-run-proof" {
			t.Error("Run proof was not taken from the authorization header")
		}
		_ = json.NewEncoder(w).Encode(protocol.StudioProject{ProjectID: "project"})
	}))
	defer server.Close()
	client := NewClient(server.URL)
	client.SetToken("user-session")
	client.daemonID = "daemon-a"
	d := &Daemon{client: client, cfg: Config{StudioAPIOrigin: testStudioOrigin}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/tools/studio-project/{runtimeId}/{runId}", d.localStudioProjectHandler())
	for _, authenticated := range []bool{false, true} {
		request := httptest.NewRequest(
			http.MethodPost,
			"/v1/tools/studio-project/11111111-1111-4111-8111-111111111111/33333333-3333-4333-8333-333333333333",
			strings.NewReader(`{"action":"get","runToken":"spoofed-body"}`),
		)
		if authenticated {
			request.Header.Set("Authorization", "Bearer live-run-proof")
		}
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		want := 401
		if authenticated {
			want = 200
		}
		if response.Code != want {
			t.Fatalf("status=%d want=%d: %s", response.Code, want, response.Body)
		}
	}
}

func TestStudioLauncherScopesProjectsAndRejectsOldIDs(t *testing.T) {
	t.Parallel()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is unavailable")
	}
	root := t.TempDir()
	entry := filepath.Join(root, "cli.cjs")
	record := filepath.Join(root, "calls.jsonl")
	if err := os.WriteFile(
		entry,
		[]byte(
			`if(process.argv.includes("auth")){console.log(JSON.stringify({ok:true,data:{authenticated:true}}));process.exit(0);}const fs=require('node:fs'); const args=process.argv.slice(2); fs.appendFileSync(process.env.RECORD,JSON.stringify(args)+'\n'); const data=args.includes('create')?{id:'11111111-1111-4111-8111-111111111111',title:'New campaign'}:args.includes('list')?{projects:[]}:{args}; console.log(JSON.stringify({ok:true,data}));`,
		),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	bindings := map[string]protocol.StudioProject{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer run-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var request protocol.StudioProjectRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		value := bindings[r.URL.Path]
		value.CanCreate = false
		if request.Action == "reserve" && value.ID == "" {
			value = protocol.StudioProject{
				ID:          "22222222-2222-4222-8222-222222222222",
				Title:       request.Title,
				Description: request.Description,
				CanCreate:   true,
			}
		}
		if request.Action == "bind" {
			value.ProjectID = request.ProjectID
		}
		bindings[r.URL.Path] = value
		_ = json.NewEncoder(w).Encode(value)
	}))
	defer server.Close()
	installed, err := writeStudioLauncher(
		root,
		toolInstallation{Node: node, Entrypoint: entry},
		studioLauncherConfig{Endpoint: server.URL + "/chat-a", RunToken: "run-secret"},
	)
	if err != nil {
		t.Fatal(err)
	}
	invoke := func(args ...string) ([]byte, error) {
		t.Helper()
		return runToolProcess(
			t.Context(),
			toolProcess{
				Executable: node,
				Args:       append([]string{installed.Entrypoint}, args...),
				Env:        map[string]string{"RECORD": record},
			},
		)
	}
	for _, args := range [][]string{
		{"--json", "generate", "image", "--project", "old-project", "--model", "model", "--prompt", "new"},
		{"--json", "projects", "resolve", "old-project"},
		{"--json", "jobs", "get", "old-project", "node"},
		{"--json", "generate", "image", "--project", "old-project", "--prompt", "--help"},
		{"--json", "generate", "image", "--project", "old-project", "--prompt", "-h"},
	} {
		if _, err := invoke(args...); err == nil {
			t.Fatalf("accepted old project: %v", args)
		}
	}
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Fatal("unbound project reached CLI")
	}
	for range 2 {
		if _, err := invoke("--json", "projects", "create", "--title", "New campaign"); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(record)
	var calls [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var call []string
		_ = json.Unmarshal([]byte(line), &call)
		calls = append(calls, call)
	}
	creates := 0
	for _, call := range calls {
		for _, arg := range call {
			if arg == "create" {
				creates++
			}
		}
	}
	if creates != 1 {
		t.Fatalf("create count = %d, want 1; calls=%s", creates, data)
	}
	for _, args := range [][]string{
		{"--json", "generate", "image", "--project=old-project"},
		{"--json", "generate", "image", "--project", "11111111-1111-4111-8111-111111111111", "--project=old-project"},
		{"--json", "--api-url=https://wrong.invalid", "projects", "list"},
		{"--json", "canvas", "export", "old-project"},
	} {
		if _, err := invoke(args...); err == nil {
			t.Fatalf("accepted mismatched project: %v", args)
		}
	}
	if _, err := invoke(
		"--json",
		"generate",
		"image",
		"--project",
		"11111111-1111-4111-8111-111111111111",
		"--model",
		"model",
		"--prompt",
		"new",
	); err != nil {
		t.Fatal(err)
	}
	listed, err := invoke("--json", "projects", "list")
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Data struct{ Projects []struct{ ID string } }
	}
	if json.Unmarshal(listed, &response) != nil || len(response.Data.Projects) != 1 ||
		response.Data.Projects[0].ID != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("scoped projects = %s", listed)
	}
	otherDir := t.TempDir()
	other, err := writeStudioLauncher(
		otherDir,
		toolInstallation{Node: node, Entrypoint: entry},
		studioLauncherConfig{Endpoint: server.URL + "/chat-b", RunToken: "run-secret"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runToolProcess(
		t.Context(),
		toolProcess{
			Executable: node,
			Args: []string{
				other.Entrypoint,
				"--json",
				"projects",
				"resolve",
				"11111111-1111-4111-8111-111111111111",
			},
		},
	); err == nil {
		t.Fatal("new chat inherited old project")
	}
	if _, err := invoke("generate", "image", "--help"); err != nil {
		t.Fatalf("scoped command help: %v", err)
	}
}

func TestStudioCommandReturnsSetupRequirementWithoutWaiting(t *testing.T) {
	t.Parallel()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable")
	}
	for _, test := range []struct {
		name      string
		args      []string
		connected bool
		want      string
		wantError bool
	}{
		{name: "status before connection", args: []string{"--json", "auth", "status"}, want: `"authenticated":false`},
		{name: "models need connection", args: []string{"--json", "models", "list"}, want: `"code":"studio_auth_required"`, wantError: true},
		{name: "status after connection", args: []string{"--json", "auth", "status"}, connected: true, want: `"authenticated":true`},
		{name: "models after connection", args: []string{"--json", "models", "list"}, connected: true, want: `"models":[]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			entry := filepath.Join(root, "cli.cjs")
			script := `const auth=process.argv.includes("auth");if(!auth&&process.env.CONNECTED!=="yes")process.exit(9);console.log(JSON.stringify({ok:true,data:auth?{authenticated:process.env.CONNECTED==="yes"}:{models:[]}}));`
			if err := os.WriteFile(entry, []byte(script), 0o600); err != nil {
				t.Fatal(err)
			}
			var bridgeCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				bridgeCalls.Add(1)
				_ = json.NewEncoder(w).Encode(protocol.StudioProject{})
			}))
			defer server.Close()
			installed, err := writeStudioLauncher(
				root,
				toolInstallation{Node: node, Entrypoint: entry},
				studioLauncherConfig{Endpoint: server.URL},
			)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, node, append([]string{installed.Entrypoint}, test.args...)...)
			connected := "no"
			if test.connected {
				connected = "yes"
			}
			cmd.Env = append(os.Environ(), "CONNECTED="+connected)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			output, err := cmd.Output()
			if ctx.Err() != nil {
				t.Fatalf(
					"Studio command blocked instead of returning setup requirement: %s",
					stderr.String(),
				)
			}
			if (err != nil) != test.wantError || !bytes.Contains(output, []byte(test.want)) {
				t.Fatalf(
					"Studio result = %s, %v, stderr %s; want %s, error %t",
					output,
					err,
					stderr.String(),
					test.want,
					test.wantError,
				)
			}
			if bridgeCalls.Load() != 0 {
				t.Fatalf(
					"read-only authentication check changed Run wait state %d times",
					bridgeCalls.Load(),
				)
			}
		})
	}
}

func TestStudioAuthWaitBridgeRequiresLiveRunProof(t *testing.T) {
	t.Parallel()
	var verified, waits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/wait-studio-auth") {
			var body struct {
				Waiting bool `json:"waiting"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if verified.Load() == 0 || !body.Waiting {
				t.Error("wait was not authorized or payload was lost")
			}
			waits.Add(1)
			w.WriteHeader(http.StatusOK)
			return
		}
		var request protocol.StudioProjectRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request.Action != "get" {
			t.Errorf("authorization action = %s", request.Action)
		}
		if request.RunToken != "live-proof" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		verified.Add(1)
		_ = json.NewEncoder(w).Encode(protocol.StudioProject{})
	}))
	defer server.Close()
	d := &Daemon{client: NewClient(server.URL), cfg: Config{StudioAPIOrigin: testStudioOrigin}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/tools/studio-project/{runtimeId}/{runId}", d.localStudioProjectHandler())
	for _, token := range []string{"stale-proof", "live-proof"} {
		r := httptest.NewRequest(
			http.MethodPost,
			"/v1/tools/studio-project/11111111-1111-4111-8111-111111111111/33333333-3333-4333-8333-333333333333",
			strings.NewReader(`{"action":"auth_wait"}`),
		)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if token == "stale-proof" && (w.Code != 403 || waits.Load() != 0) {
			t.Fatalf("stale proof changed wait: %d, %d", w.Code, waits.Load())
		}
		if token == "live-proof" && (w.Code != 200 || waits.Load() != 1) {
			t.Fatalf("live proof did not wait: %d, %d", w.Code, waits.Load())
		}
	}
}

func TestStudioLauncherScopesRenderPublication(t *testing.T) {
	t.Parallel()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is unavailable")
	}
	for _, tt := range []struct {
		name    string
		project string
		want    string
	}{
		{name: "bound project", project: "project", want: `"published":true`},
		{name: "other project", project: "other", want: "studio_project_mismatch"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			entry := filepath.Join(root, "cli.cjs")
			if err := os.WriteFile(
				entry,
				[]byte(
					`if(process.argv.includes("auth")){console.log(JSON.stringify({ok:true,data:{authenticated:true}}));}else{console.log(JSON.stringify({ok:true,data:{published:true}}));}`,
				),
				0o600,
			); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(protocol.StudioProject{ProjectID: "project"})
			}))
			defer server.Close()
			installed, err := writeStudioLauncher(
				root,
				toolInstallation{Node: node, Entrypoint: entry},
				studioLauncherConfig{Endpoint: server.URL},
			)
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(
				t.Context(),
				node,
				installed.Entrypoint,
				"--json",
				"renders",
				"publish",
				tt.project,
				"final.mp4",
				"--manifest",
				"render.json",
			)
			output, err := cmd.CombinedOutput()
			if !strings.Contains(string(output), tt.want) || (tt.project == "project" && err != nil) ||
				(tt.project != "project" && err == nil) {
				t.Fatalf("publication = %s, %v; want %s", output, err, tt.want)
			}
		})
	}
}
