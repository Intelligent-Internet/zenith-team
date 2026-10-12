package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"boardly.dev/boardly/server/pkg/protocol"
)

func studioMediaLauncher(t *testing.T) (string, func(...string) ([]byte, error), *atomic.Int32) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is unavailable")
	}
	root := t.TempDir()
	fixture, err := filepath.Abs("testdata/studio_media_cli.cjs")
	if err != nil {
		t.Fatal(err)
	}
	var checks atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer run-proof" {
			t.Error("missing Run proof")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		checks.Add(1)
		if _, err := os.Stat(filepath.Join(root, "revoked")); err == nil {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(protocol.StudioProject{ProjectID: "project"})
	}))
	t.Cleanup(server.Close)
	installed, err := writeStudioLauncher(
		root,
		toolInstallation{Node: node, Entrypoint: fixture},
		studioLauncherConfig{
			Endpoint: server.URL,
			RunToken: "run-proof",
			Home:     filepath.Join(root, "agent-account"),
			APIURL:   "https://studio.invalid",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	invoke := func(args ...string) ([]byte, error) {
		t.Helper()
		cmd := exec.CommandContext(
			t.Context(),
			node,
			append([]string{installed.Entrypoint, "--json"}, args...)...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "RECORD="+filepath.Join(root, "calls.jsonl"))
		return cmd.CombinedOutput()
	}
	return root, invoke, &checks
}

func TestStudioLauncherCollectStopsAtFailureAndKeepsCheckpoint(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		settings  string
		wantStage string
		wantCalls []string
	}{
		{"wait", `{"failStage":"jobs"}`, "wait", []string{"auth", "jobs"}},
		{"download", `{"failStage":"assets"}`, "download", []string{"auth", "jobs", "auth", "assets"}},
		{"canvas", `{"failStage":"canvas"}`, "canvas", []string{"auth", "jobs", "auth", "assets", "auth", "canvas"}},
		{"revoked after wait", `{"revokeAfterWait":true}`, "download", []string{"auth", "jobs"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root, invoke, _ := studioMediaLauncher(t)
			if err := os.WriteFile(
				filepath.Join(root, "settings.json"),
				[]byte(test.settings),
				0o600,
			); err != nil {
				t.Fatal(err)
			}
			output, err := invoke("team", "collect", "project", "node", "--output-dir", "output")
			if err == nil {
				t.Fatalf("failure returned success: %s", output)
			}
			if bytes.Contains(output, []byte("private-upstream-value")) ||
				bytes.Contains(output, []byte("signed-secret")) {
				t.Fatal("failure leaked upstream data")
			}
			files, _ := filepath.Glob(filepath.Join(root, "output", "studio-*", "trace.json"))
			if len(files) != 1 {
				t.Fatalf("checkpoints = %v", files)
			}
			data, _ := os.ReadFile(files[0])
			var trace struct {
				ProjectID string
				NodeID    string
				Status    string
				Stage     string
			}
			if err := json.Unmarshal(data, &trace); err != nil {
				t.Fatal(err)
			}
			if trace.ProjectID != "project" || trace.NodeID != "node" || trace.Status != "incomplete" ||
				trace.Stage != test.wantStage {
				t.Fatalf("checkpoint = %s", data)
			}
			data, _ = os.ReadFile(filepath.Join(root, "calls.jsonl"))
			var groups []string
			for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
				var call struct{ Args []string }
				if err := json.Unmarshal(line, &call); err != nil {
					t.Fatal(err)
				}
				groups = append(groups, call.Args[1])
			}
			if !slices.Equal(groups, test.wantCalls) {
				t.Fatalf("operations = %v, want %v", groups, test.wantCalls)
			}
		})
	}
}

func TestStudioLauncherCollectPreservesMediaFormats(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ mimeType, extension string }{
		{"image/jpeg", ".jpg"},
		{"image/webp", ".webp"},
		{"audio/mpeg", ".mp3"},
		{"audio/wav", ".wav"},
		{"video/mp4", ".mp4"},
		{"video/webm", ".webm"},
	} {
		t.Run(test.mimeType, func(t *testing.T) {
			t.Parallel()
			root, invoke, _ := studioMediaLauncher(t)
			settings, _ := json.Marshal(map[string]string{"mimeType": test.mimeType})
			if err := os.WriteFile(filepath.Join(root, "settings.json"), settings, 0o600); err != nil {
				t.Fatal(err)
			}
			if output, err := invoke(
				"team",
				"collect",
				"project",
				"node",
				"--output-dir",
				"output",
			); err != nil {
				t.Fatalf("collect: %s: %v", output, err)
			}
			files, _ := filepath.Glob(filepath.Join(root, "output", "studio-*", "media"+test.extension))
			if len(files) != 1 {
				t.Fatalf("media files = %v", files)
			}
			data, _ := os.ReadFile(files[0])
			if string(data) != "media" {
				t.Fatal("collection changed original media bytes")
			}
		})
	}
}

func TestStudioLauncherFiltersModelBeforeReturningCatalog(t *testing.T) {
	t.Parallel()
	for _, selector := range []string{"wanted", "Wanted Model", "missing"} {
		t.Run(selector, func(t *testing.T) {
			t.Parallel()
			root, invoke, _ := studioMediaLauncher(t)
			output, err := invoke("models", "list", "--kind", "image", "--model", selector)
			if err != nil {
				t.Fatalf("filter: %s: %v", output, err)
			}
			var response struct {
				Data struct {
					Models []struct {
						ID           string
						Capabilities map[string]any
					}
				}
			}
			if err := json.Unmarshal(output, &response); err != nil {
				t.Fatal(err)
			}
			want := 1
			if selector == "missing" {
				want = 0
			}
			if len(response.Data.Models) != want {
				t.Fatalf("models = %s, want %d exact matches", output, want)
			}
			if want == 1 &&
				(response.Data.Models[0].ID != "wanted" || response.Data.Models[0].Capabilities["resolutions"] == nil) {
				t.Fatalf("selected model lost capabilities: %s", output)
			}
			calls, _ := os.ReadFile(filepath.Join(root, "calls.jsonl"))
			if bytes.Contains(calls, []byte("--model")) {
				t.Fatal("Team filter leaked into upstream CLI arguments")
			}
		})
	}
}

func TestStudioLauncherCollectsCLICheckpointWithRunAuthority(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		project  string
		account  string
		origin   string
		revoked  bool
		wantFail bool
	}{
		{"same account and project", "project", "account", "https://studio.invalid", false, false},
		{"different project", "other", "account", "https://studio.invalid", false, true},
		{"different account", "project", "other", "https://studio.invalid", false, true},
		{"different origin", "project", "account", "https://other.invalid", false, true},
		{"revoked after wait", "project", "account", "https://studio.invalid", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root, invoke, checks := studioMediaLauncher(t)
			// This fixture represents the upstream CLI's versioned receipt.
			receipt := map[string]any{
				"version": 1, "status": "submitted", "project_id": test.project,
				"node_id": "node", "execution_id": "execution", "account_id": test.account,
				"api_url": test.origin, "idempotency_key": "stable",
			}
			data, err := json.Marshal(receipt)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "intent.json"), data, 0o600); err != nil {
				t.Fatal(err)
			}
			if test.revoked {
				if err := os.WriteFile(
					filepath.Join(root, "settings.json"),
					[]byte(`{"revokeAfterWait":true}`),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			}
			output, err := invoke(
				"jobs",
				"collect",
				"--checkpoint",
				"intent.json",
				"--output-dir",
				"output",
			)
			if (err != nil) != test.wantFail {
				t.Fatalf("collect = %s, %v, want failure %v", output, err, test.wantFail)
			}
			if !test.wantFail && checks.Load() != 3 {
				t.Fatalf("binding checks = %d, want 3", checks.Load())
			}
			calls, _ := os.ReadFile(filepath.Join(root, "calls.jsonl"))
			if bytes.Contains(calls, []byte(`"generate"`)) || bytes.Contains(calls, []byte(`"collect"`)) {
				t.Fatal(
					"managed collection must retain per-step authority, never delegate the compound command",
				)
			}
			if test.wantFail && bytes.Contains(calls, []byte(`"assets"`)) {
				t.Fatal("unauthorized download")
			}
		})
	}
}

func TestStudioLauncherCollectsMediaWithoutGenerationOrReview(t *testing.T) {
	t.Parallel()
	root, invoke, checks := studioMediaLauncher(t)
	for range 2 {
		output, err := invoke("team", "collect", "project", "node", "--output-dir", "output")
		if err != nil {
			t.Fatalf("collect: %s: %v", output, err)
		}
	}
	dirs, err := filepath.Glob(filepath.Join(root, "output", "studio-*"))
	if err != nil || len(dirs) != 2 {
		t.Fatalf("collection directories = %v, %v", dirs, err)
	}
	for _, dir := range dirs {
		for _, name := range []string{"media.png", "source.canvas.json", "trace.json"} {
			if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
		}
		trace, _ := os.ReadFile(filepath.Join(dir, "trace.json"))
		var paths struct {
			WorkspaceRelativePath string
			StudioCanvasPath      string
			NodeCreatedAt         string
			NodeSucceededAt       string
			TimingsMS             map[string]int `json:"timingsMs"`
		}
		if err := json.Unmarshal(trace, &paths); err != nil {
			t.Fatal(err)
		}
		if paths.WorkspaceRelativePath == "" || filepath.IsAbs(paths.WorkspaceRelativePath) ||
			filepath.IsAbs(paths.StudioCanvasPath) {
			t.Fatalf("presentation paths must be workspace-relative: %s", trace)
		}
		if paths.NodeCreatedAt == "" || paths.NodeSucceededAt == "" || len(paths.TimingsMS) != 3 {
			t.Fatalf("missing source timestamps or stage timings: %s", trace)
		}
		for _, want := range []string{`"projectId": "project"`, `"nodeId": "node"`, `"executionId": "execution"`, `"assetId": "asset"`, `"status": "readyForReview"`} {
			if !bytes.Contains(trace, []byte(want)) {
				t.Fatalf("trace missing %s: %s", want, trace)
			}
		}
		if bytes.Contains(trace, []byte("signed-secret")) {
			t.Fatal("trace contains a signed URL")
		}
	}
	if checks.Load() != 6 {
		t.Fatalf("binding checks = %d, want every wait/download/export authorized", checks.Load())
	}
	calls, _ := os.ReadFile(filepath.Join(root, "calls.jsonl"))
	var groups []string
	for _, line := range bytes.Split(bytes.TrimSpace(calls), []byte("\n")) {
		var call struct {
			Args   []string
			Home   string
			APIURL string `json:"apiUrl"`
		}
		if err := json.Unmarshal(line, &call); err != nil {
			t.Fatal(err)
		}
		if call.Home != filepath.Join(root, "agent-account") || call.APIURL != "https://studio.invalid" {
			t.Fatalf("lost account isolation: %+v", call)
		}
		groups = append(groups, call.Args[1])
	}
	if !slices.Equal(
		groups,
		[]string{
			"auth",
			"jobs",
			"auth",
			"assets",
			"auth",
			"canvas",
			"auth",
			"jobs",
			"auth",
			"assets",
			"auth",
			"canvas",
		},
	) {
		t.Fatalf("unexpected operations (generation/review must stay outside collect): %v", groups)
	}
}

func TestStudioLauncherCollectRejectsInvalidInputsBeforeUpstream(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"team", "collect", "other-project", "node", "--output-dir", "output"},
		{"team", "collect", "project", "node", "--output-dir", "../outside"},
		{"team", "collect", "project", "node", "--force"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			root, invoke, _ := studioMediaLauncher(t)
			if output, err := invoke(args...); err == nil {
				t.Fatalf("accepted invalid input: %s", output)
			}
			if _, err := os.Stat(filepath.Join(root, "calls.jsonl")); !os.IsNotExist(err) {
				t.Fatal("invalid request reached upstream")
			}
		})
	}
}
