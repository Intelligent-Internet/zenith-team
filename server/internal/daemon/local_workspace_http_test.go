package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/daemon/workspace"
)

func TestLocalWorkspaceFilePreviewHandlerReadsBoundedManagedRunFile(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:8080")
	d.localPreviewToken = "local-capability"
	acquired, err := d.workspaces.AcquireScratch(
		"01900000-0000-4000-8000-0000000000f1",
		uuid.MustParse("00000000-0000-4000-8000-000000001501"),
	)
	if err != nil {
		t.Fatalf("AcquireScratch() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(acquired.WorkDir, "notes.md"), []byte("# Result\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	response := serveLocalWorkspaceRequest(
		t,
		d.localWorkspaceFilePreviewHandler(),
		`{"schemaVersion":1,"runId":"00000000-0000-4000-8000-000000001501","path":"notes.md"}`,
		"Bearer local-capability",
	)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var got localWorkspaceFilePreviewResponse
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.SchemaVersion != 1 || got.Path != "notes.md" || got.Filename != "notes.md" ||
		got.Format != "markdown" || got.Content != "# Result\n" || got.SizeBytes != 9 || got.Truncated {
		t.Fatalf("preview = %+v", got)
	}
}

func TestLocalWorkspaceFileResolveHandlerUsesRunWorkspace(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:8080")
	d.localPreviewToken = "local-capability"
	companyID := "01900000-0000-4000-8000-0000000000f1"
	runID := uuid.MustParse("00000000-0000-4000-8000-000000001507")
	mainDir, err := d.workspaces.EnsureMain(t.Context(), companyID, "company")
	if err != nil {
		t.Fatalf("EnsureMain() error = %v", err)
	}
	if err := d.workspaces.RecordInPlaceRunStart(t.Context(), runID, workspace.InPlaceRunIdentity{
		CompanyID: companyID, ConversationID: "00000000-0000-4000-8000-000000001508",
	}, mainDir); err != nil {
		t.Fatalf("RecordInPlaceRunStart() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(mainDir, "reports"), 0o700); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(mainDir, "reports", "receipt.md")
	if err := os.WriteFile(filePath, []byte("# Receipt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.workspaces.CompleteInPlaceRun(t.Context(), runID); err != nil {
		t.Fatalf("CompleteInPlaceRun() error = %v", err)
	}

	request, err := json.Marshal(localWorkspaceFileRequest{
		SchemaVersion: 1, RunID: runID.String(), Path: filePath,
	})
	if err != nil {
		t.Fatal(err)
	}
	response := serveLocalWorkspaceRequest(
		t,
		d.localWorkspaceFileResolveHandler(),
		string(request),
		"Bearer local-capability",
	)
	if response.Code != http.StatusOK {
		t.Fatalf("resolve status = %d, body = %s", response.Code, response.Body.String())
	}
	var got struct {
		SchemaVersion int    `json:"schemaVersion"`
		Path          string `json:"path"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.SchemaVersion != 1 || got.Path != "reports/receipt.md" {
		t.Fatalf("resolved file = %+v", got)
	}

	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{outside, mainDir + "/reports/../../outside.md"} {
		request, err := json.Marshal(localWorkspaceFileRequest{
			SchemaVersion: 1, RunID: runID.String(), Path: path,
		})
		if err != nil {
			t.Fatal(err)
		}
		response := serveLocalWorkspaceRequest(
			t,
			d.localWorkspaceFileResolveHandler(),
			string(request),
			"Bearer local-capability",
		)
		if response.Code == http.StatusOK || strings.Contains(response.Body.String(), outside) ||
			strings.Contains(response.Body.String(), "private") {
			t.Fatalf(
				"outside path %q resolved: status = %d, body = %s",
				path,
				response.Code,
				response.Body.String(),
			)
		}
	}

	if runtime.GOOS != "windows" {
		escape := filepath.Join(mainDir, "reports", "escape.md")
		if err := os.Symlink(outside, escape); err != nil {
			t.Fatal(err)
		}
		request, err := json.Marshal(localWorkspaceFileRequest{
			SchemaVersion: 1, RunID: runID.String(), Path: escape,
		})
		if err != nil {
			t.Fatal(err)
		}
		response := serveLocalWorkspaceRequest(
			t,
			d.localWorkspaceFileResolveHandler(),
			string(request),
			"Bearer local-capability",
		)
		if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), outside) {
			t.Fatalf(
				"symlink escape resolved: status = %d, body = %s",
				response.Code,
				response.Body.String(),
			)
		}
	}
}

func TestLocalWorkspaceFileExportHandlerRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	d := newTestDaemon(t, "http://127.0.0.1:8080")
	d.localPreviewToken = "local-capability"
	acquired, err := d.workspaces.AcquireScratch(
		"01900000-0000-4000-8000-0000000000f2",
		uuid.MustParse("00000000-0000-4000-8000-000000001502"),
	)
	if err != nil {
		t.Fatalf("AcquireScratch() error = %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(acquired.WorkDir, "escape.txt")); err != nil {
		t.Fatal(err)
	}

	response := serveLocalWorkspaceRequest(
		t,
		d.localWorkspaceFileExportHandler(),
		`{"schemaVersion":1,"runId":"00000000-0000-4000-8000-000000001502","path":"escape.txt"}`,
		"Bearer local-capability",
	)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), outside) || strings.Contains(response.Body.String(), "secret") {
		t.Fatalf("response leaked escaped path or content: %s", response.Body.String())
	}
}

// A Run never generates a deliverable under .git, and docs/api/workspaces.md
// promises that Git internals are rejected on every local file route, so the
// Run file plane refuses them like the asset plane does. Other dotfiles a Run
// wrote, such as .gitignore, still preview.
func TestLocalWorkspaceFilePreviewHandlerRejectsGitInternals(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:8080")
	d.localPreviewToken = "local-capability"
	acquired, err := d.workspaces.AcquireScratch(
		"01900000-0000-4000-8000-0000000000f8",
		uuid.MustParse("00000000-0000-4000-8000-000000001506"),
	)
	if err != nil {
		t.Fatalf("AcquireScratch() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(acquired.WorkDir, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	head := filepath.Join(acquired.WorkDir, ".git", "HEAD")
	if err := os.WriteFile(head, []byte("ref: refs/heads/main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(acquired.WorkDir, ".gitignore"), []byte("dist/\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, handler := range []http.HandlerFunc{
		d.localWorkspaceFilePreviewHandler(), d.localWorkspaceFileExportHandler(),
	} {
		response := serveLocalWorkspaceRequest(
			t,
			handler,
			`{"schemaVersion":1,"runId":"00000000-0000-4000-8000-000000001506","path":".git/HEAD"}`,
			"Bearer local-capability",
		)
		if response.Code != http.StatusNotFound ||
			!strings.Contains(response.Body.String(), `"file_unavailable"`) {
			t.Fatalf(".git/HEAD status = %d, body = %s", response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "refs/heads") {
			t.Fatalf("response leaked Git internals: %s", response.Body.String())
		}
	}
	response := serveLocalWorkspaceRequest(
		t,
		d.localWorkspaceFilePreviewHandler(),
		`{"schemaVersion":1,"runId":"00000000-0000-4000-8000-000000001506","path":".gitignore"}`,
		"Bearer local-capability",
	)
	if response.Code != http.StatusOK {
		t.Fatalf(".gitignore status = %d, body = %s", response.Code, response.Body.String())
	}
	var got localWorkspaceFilePreviewResponse
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Path != ".gitignore" || got.Content != "dist/\n" {
		t.Fatalf("preview = %+v", got)
	}
}

func TestLocalWorkspaceFileExportStreamsOpenedDescriptorAfterPathReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	d := newTestDaemon(t, "http://127.0.0.1:8080")
	d.localPreviewToken = "local-capability"
	acquired, err := d.workspaces.AcquireScratch(
		"01900000-0000-4000-8000-0000000000f7",
		uuid.MustParse("00000000-0000-4000-8000-000000001505"),
	)
	if err != nil {
		t.Fatalf("AcquireScratch() error = %v", err)
	}
	source := filepath.Join(acquired.WorkDir, "result.txt")
	if err := os.WriteFile(source, []byte("validated bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("redirected secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(
		http.MethodPost,
		"http://127.0.0.1:19714/v1/workspace-files/export",
		bytes.NewBufferString(
			`{"schemaVersion":1,"runId":"00000000-0000-4000-8000-000000001505","path":"result.txt"}`,
		),
	)
	request.RemoteAddr = "127.0.0.1:1234"
	request.Header.Set("Authorization", "Bearer local-capability")
	response := &replaceOnHeaderRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		replace: func() {
			if err := os.Remove(source); err != nil {
				t.Errorf("remove opened source: %v", err)
				return
			}
			if err := os.Symlink(outside, source); err != nil {
				t.Errorf("replace opened source with symlink: %v", err)
			}
		},
	}

	d.localWorkspaceFileExportHandler().ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Body.String() != "validated bytes\n" {
		t.Fatalf("export status = %d, body = %q", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "redirected secret") {
		t.Fatalf("export followed replaced source path: %q", response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("Content-Type = %q", got)
	}
}

func TestLocalWorkspaceAssetHandlersPreviewAndExportMainAsset(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:8080")
	d.localPreviewToken = "local-capability"
	acquired, err := d.workspaces.AcquireScopedScratch(
		t.Context(),
		"01900000-0000-4000-8000-0000000000f3",
		"engineering",
		"01900000-0000-4000-8000-0000000000f4",
		uuid.MustParse("00000000-0000-4000-8000-000000001503"),
		0,
	)
	if err != nil {
		t.Fatalf("AcquireScopedScratch(, 0) error = %v", err)
	}
	mainDir := filepath.Join(filepath.Dir(filepath.Dir(acquired.Root)), "main")
	if err := os.MkdirAll(filepath.Join(mainDir, "assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	assetPath := filepath.Join(mainDir, "assets", "brief.md")
	if err := os.WriteFile(assetPath, []byte("launch brief\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := `{"schemaVersion":1,"companyId":"01900000-0000-4000-8000-0000000000f3","scope":"engineering","path":"assets/brief.md"}`
	preview := serveLocalWorkspaceRequest(
		t,
		d.localWorkspaceAssetPreviewHandler(),
		body,
		"Bearer local-capability",
	)
	if preview.Code != http.StatusOK || !strings.Contains(preview.Body.String(), "launch brief") {
		t.Fatalf("preview status = %d, body = %s", preview.Code, preview.Body.String())
	}
	exported := serveLocalWorkspaceRequest(
		t,
		d.localWorkspaceAssetExportHandler(),
		body,
		"Bearer local-capability",
	)
	if exported.Code != http.StatusOK || exported.Body.String() != "launch brief\n" ||
		strings.Contains(exported.Body.String(), assetPath) {
		t.Fatalf("export status = %d, body = %q", exported.Code, exported.Body.String())
	}
	if err := os.WriteFile(
		filepath.Join(mainDir, "implementation.go"),
		[]byte("package example\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	source := serveLocalWorkspaceRequest(
		t,
		d.localWorkspaceAssetPreviewHandler(),
		strings.Replace(body, "assets/brief.md", "implementation.go", 1),
		"Bearer local-capability",
	)
	if source.Code != http.StatusNotFound {
		t.Fatalf("source asset status = %d, body = %s", source.Code, source.Body.String())
	}
}

type replaceOnHeaderRecorder struct {
	*httptest.ResponseRecorder
	once    sync.Once
	replace func()
}

func (r *replaceOnHeaderRecorder) Header() http.Header {
	r.once.Do(r.replace)
	return r.ResponseRecorder.Header()
}

func TestLocalTaskReviewHandlersReviewAndAcceptThroughWorkspaceManager(t *testing.T) {
	const (
		companyID      = "01900000-0000-4000-8000-0000000000f5"
		conversationID = "01900000-0000-4000-8000-0000000000f6"
	)
	var leaseCalls, resultCalls int
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/daemon/runtimes/" + taskMergeRuntimeID + "/task-merges/manual":
			var request manualTaskMergeClaimRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode manual merge claim: %v", err)
			}
			if request.TaskID != taskMergeTaskID ||
				request.RunID != uuid.MustParse("00000000-0000-4000-8000-000000001504") ||
				request.ExpectedRevision != 11 ||
				request.ExpectedExecutionGeneration != 3 {
				t.Errorf("manual merge claim request = %+v", request)
			}
			contextDocument, err := json.Marshal(RunContext{
				ExecutionProfile: RunExecutionProfileContext{
					Project: &RunProjectContext{ID: taskMergeTaskID},
					Task: &RunTaskContext{
						ConversationID: conversationID,
					},
				},
			})
			if err != nil {
				t.Errorf("encode manual merge context: %v", err)
			}
			_ = json.NewEncoder(w).Encode(TaskMergeClaim{
				JobID:     taskMergeJobID,
				CompanyID: companyID,
				TaskID:    taskMergeTaskID,
				RunID: uuid.MustParse(
					"00000000-0000-4000-8000-000000001504",
				),
				TaskExecutionGeneration: 3,
				Attempt:                 1,
				ConversationID:          conversationID,
				Context:                 contextDocument,
			})
		case "/api/v1/daemon/runtimes/" + taskMergeRuntimeID + "/task-merges/" + taskMergeJobID + "/lease":
			leaseCalls++
			w.WriteHeader(http.StatusNoContent)
		case "/api/v1/daemon/runtimes/" + taskMergeRuntimeID + "/task-merges/" + taskMergeJobID + "/result":
			resultCalls++
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer cloud.Close()

	d := newTestDaemon(t, cloud.URL)
	d.cfg.DaemonID = taskMergeDaemonID
	d.localPreviewToken = "local-capability"
	acquired, err := d.workspaces.AcquireScopedScratch(
		t.Context(),
		companyID,
		"projects/"+taskMergeTaskID,
		conversationID,
		uuid.MustParse("00000000-0000-4000-8000-000000001504"),
		0,
	)
	if err != nil {
		t.Fatalf("AcquireScopedScratch(, 0) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(acquired.WorkDir, "result.txt"), []byte("ready\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.workspaces.MarkCompleted(acquired.Root); err != nil {
		t.Fatalf("MarkCompleted() error = %v", err)
	}
	prepareBody := `{"schemaVersion":1,"runId":"00000000-0000-4000-8000-000000001504"}`
	prepare := serveLocalWorkspaceRequest(
		t,
		d.localTaskReviewPrepareHandler(),
		prepareBody,
		"Bearer local-capability",
	)
	if prepare.Code != http.StatusOK || !strings.Contains(prepare.Body.String(), `"status":"ready"`) ||
		!strings.Contains(prepare.Body.String(), "ready") {
		t.Fatalf("prepare status = %d, body = %s", prepare.Code, prepare.Body.String())
	}
	acceptBody := `{"schemaVersion":1,"companyId":"` + companyID +
		`","taskId":"` + taskMergeTaskID + `","runId":"00000000-0000-4000-8000-000000001504","runtimeId":"` +
		taskMergeRuntimeID + `","expectedRevision":11,"expectedExecutionGeneration":3}`
	accept := serveLocalWorkspaceRequest(
		t,
		d.localTaskReviewAcceptHandler(),
		acceptBody,
		"Bearer local-capability",
	)
	if accept.Code != http.StatusOK ||
		!strings.Contains(accept.Body.String(), `"status":"merged"`) {
		t.Fatalf("accept status = %d, body = %s", accept.Code, accept.Body.String())
	}
	if leaseCalls < 2 || resultCalls != 1 {
		t.Fatalf("manual merge cloud calls = leases %d results %d", leaseCalls, resultCalls)
	}
	mainDir := filepath.Join(filepath.Dir(filepath.Dir(acquired.Root)), "main")
	if content, err := os.ReadFile(
		filepath.Join(mainDir, "result.txt"),
	); err != nil ||
		string(content) != "ready\n" {
		t.Fatalf("accepted main file = %q, %v", content, err)
	}
}

// TestLocalTaskReviewAcceptPassesThePermissionRefusalThrough pins the reason
// the desktop gets when the server refuses a manual merge because the
// daemon's member may not run Tasks in the Project: the permission, never
// the stale-review conflict every other claim failure answers.
func TestLocalTaskReviewAcceptPassesThePermissionRefusalThrough(t *testing.T) {
	const companyID = "01900000-0000-4000-8000-0000000000f5"
	for _, test := range []struct {
		name               string
		status             int
		body               string
		wantStatus         int
		wantCode, wantText string
	}{
		{
			name:   "missing project.tasks.execute",
			status: http.StatusForbidden,
			body: `{"detail":"Your access to this Project does not allow this action",` +
				`"code":"project_permission_required","requestId":"r"}`,
			wantStatus: http.StatusForbidden, wantCode: "project_permission_required",
			wantText: "You can't run tasks here.",
		},
		{
			name:   "stale review",
			status: http.StatusConflict,
			body: `{"detail":"Task merge conflicts with current state",` +
				`"code":"task_merge_conflict","requestId":"r"}`,
			wantStatus: http.StatusConflict, wantCode: "task_merge_conflict",
			wantText: "Task review is stale or another merge is already running.",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/daemon/runtimes/"+taskMergeRuntimeID+"/task-merges/manual" {
					t.Errorf("unexpected cloud call %s", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer cloud.Close()

			d := newTestDaemon(t, cloud.URL)
			d.cfg.DaemonID = taskMergeDaemonID
			d.localPreviewToken = "local-capability"
			acceptBody := `{"schemaVersion":1,"companyId":"` + companyID +
				`","taskId":"` + taskMergeTaskID + `","runId":"00000000-0000-4000-8000-000000001504","runtimeId":"` +
				taskMergeRuntimeID + `","expectedRevision":11,"expectedExecutionGeneration":3}`
			accept := serveLocalWorkspaceRequest(
				t,
				d.localTaskReviewAcceptHandler(),
				acceptBody,
				"Bearer local-capability",
			)
			var answer localWorkspaceErrorResponse
			if err := json.Unmarshal(accept.Body.Bytes(), &answer); err != nil {
				t.Fatalf("decode accept answer %q: %v", accept.Body.String(), err)
			}
			if accept.Code != test.wantStatus || answer.Code != test.wantCode ||
				answer.Reason != test.wantText {
				t.Fatalf("accept = %d %+v, want %d %s %q",
					accept.Code, answer, test.wantStatus, test.wantCode, test.wantText)
			}
		})
	}
}

func TestLocalWorkspaceAuthorizationRejectsUntrustedRequests(t *testing.T) {
	d := &Daemon{localPreviewToken: "local-capability"}
	tests := []struct {
		name          string
		method        string
		remoteAddr    string
		origin        string
		authorization string
		want          int
	}{
		{
			name:       "missing capability",
			method:     http.MethodPost,
			remoteAddr: "127.0.0.1:1234",
			want:       http.StatusUnauthorized,
		},
		{
			name:          "cross origin",
			method:        http.MethodPost,
			remoteAddr:    "127.0.0.1:1234",
			origin:        "http://evil.example",
			authorization: "Bearer local-capability",
			want:          http.StatusForbidden,
		},
		{
			name:          "non loopback peer",
			method:        http.MethodPost,
			remoteAddr:    "192.0.2.1:1234",
			authorization: "Bearer local-capability",
			want:          http.StatusForbidden,
		},
		{
			name:          "wrong method",
			method:        http.MethodGet,
			remoteAddr:    "127.0.0.1:1234",
			authorization: "Bearer local-capability",
			want:          http.StatusMethodNotAllowed,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(
				test.method,
				"http://127.0.0.1:19714/v1/workspace-files/preview",
				bytes.NewBufferString(`{}`),
			)
			request.RemoteAddr = test.remoteAddr
			request.Header.Set("Authorization", test.authorization)
			request.Header.Set("Origin", test.origin)
			response := httptest.NewRecorder()
			d.localWorkspaceFilePreviewHandler().ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf(
					"status = %d, want %d, body = %s",
					response.Code,
					test.want,
					response.Body.String(),
				)
			}
		})
	}
}

func serveLocalWorkspaceRequest(
	t *testing.T,
	handler http.Handler,
	body string,
	authorization string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(
		http.MethodPost,
		"http://127.0.0.1:19714/v1/local-workspace",
		bytes.NewBufferString(body),
	)
	request.RemoteAddr = "127.0.0.1:1234"
	request.Header.Set("Authorization", authorization)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

// A Run that executed in place — in the directory a Company Resource binds
// to this daemon — reviews from its signed receipt: prepare shows the Run's
// own commits, and file access resolves the directory without the in-memory
// registry a restart would have emptied.
func TestLocalTaskReviewPrepareReviewsAnInPlaceRun(t *testing.T) {
	const (
		companyID      = "01900000-0000-4000-8000-0000000000f7"
		conversationID = "01900000-0000-4000-8000-0000000000f8"
	)
	runID := uuid.MustParse("00000000-0000-4000-8000-000000001505")
	d := newTestDaemon(t, "http://unreachable.invalid")
	d.localPreviewToken = "local-capability"
	localDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runInPlaceTestGit(t, localDir, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(localDir, "README.md"), []byte("ready\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runInPlaceTestGit(t, localDir, "add", "-A")
	runInPlaceTestGit(
		t,
		localDir,
		"-c",
		"user.name=Test",
		"-c",
		"user.email=test@zenith-team.local",
		"commit",
		"-m",
		"Initial commit",
	)

	if err := d.workspaces.RecordInPlaceRunStart(t.Context(), runID, workspace.InPlaceRunIdentity{
		CompanyID: companyID, ConversationID: conversationID, TaskID: taskMergeTaskID,
	}, localDir); err != nil {
		t.Fatalf("RecordInPlaceRunStart() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "result.txt"), []byte("built\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runInPlaceTestGit(t, localDir, "add", "-A")
	runInPlaceTestGit(
		t,
		localDir,
		"-c",
		"user.name=Test",
		"-c",
		"user.email=test@zenith-team.local",
		"commit",
		"-m",
		"Build",
	)
	if err := d.workspaces.CompleteInPlaceRun(t.Context(), runID); err != nil {
		t.Fatalf("CompleteInPlaceRun() error = %v", err)
	}

	prepare := serveLocalWorkspaceRequest(
		t,
		d.localTaskReviewPrepareHandler(),
		`{"schemaVersion":1,"runId":"`+runID.String()+`"}`,
		"Bearer local-capability",
	)
	if prepare.Code != http.StatusOK || !strings.Contains(prepare.Body.String(), `"status":"ready"`) ||
		!strings.Contains(prepare.Body.String(), "+built") ||
		!strings.Contains(prepare.Body.String(), "committed in place") {
		t.Fatalf("prepare status = %d, body = %s", prepare.Code, prepare.Body.String())
	}
	if got, err := d.resolveLocalRunWorkDir(t.Context(), runID); err != nil || got != localDir {
		t.Fatalf("resolveLocalRunWorkDir(in place) = %q, %v; want %q", got, err, localDir)
	}
}

func runInPlaceTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.CommandContext(t.Context(), "git", args...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
