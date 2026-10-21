package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/mcp/tools"
)

type artifactAPIFake struct {
	manifest  tools.ArtifactManifest
	denied    bool
	downloads int
	body      string
}

func (a *artifactAPIFake) inspect(_ context.Context, _ string) (tools.ArtifactManifest, error) {
	if a.denied {
		return tools.ArtifactManifest{}, errors.New("access revoked")
	}
	return a.manifest, nil
}

func (a *artifactAPIFake) download(_ context.Context, _ string, _ int, dst io.Writer) error {
	a.downloads++
	_, err := io.WriteString(dst, a.body)
	return err
}

func TestArtifactFetchRevalidatesCacheAndRepairsChangedBytes(t *testing.T) {
	t.Parallel()
	api := &artifactAPIFake{
		body: "report",
		manifest: tools.ArtifactManifest{
			ID: uuid.NewString(),
			Items: []tools.ArtifactItem{
				{
					ID:             uuid.NewString(),
					AssetVersionID: uuid.NewString(),
					DisplayName:    "report.txt",
					Role:           "primary",
					SizeBytes:      6,
				},
			},
		},
	}
	service := newRunArtifactReader(api, t.TempDir(), uuid.NewString(), nil)
	input := tools.ArtifactFetchInput{ArtifactID: api.manifest.ID}
	first, err := service.FetchArtifact(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.FetchArtifact(t.Context(), input)
	if err != nil || first.LocalPath != second.LocalPath || api.downloads != 1 {
		t.Fatalf("cache = %+v, %v, downloads %d", second, err, api.downloads)
	}
	if err := os.WriteFile(first.LocalPath, []byte("edited"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.FetchArtifact(t.Context(), input); err != nil || api.downloads != 2 {
		t.Fatalf("repair = %v, downloads %d", err, api.downloads)
	}
	api.denied = true
	if _, err := service.FetchArtifact(t.Context(), input); err == nil {
		t.Fatal("revoked access reused cache")
	}
}

func TestArtifactFetchRejectsPartialFilesAndPathEscape(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"partial", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			api := &artifactAPIFake{
				body: "short",
				manifest: tools.ArtifactManifest{
					ID: uuid.NewString(),
					Items: []tools.ArtifactItem{
						{
							ID:             uuid.NewString(),
							AssetVersionID: uuid.NewString(),
							DisplayName:    "../../report.txt",
							Role:           "primary",
							SizeBytes:      20,
						},
					},
				},
			}
			if scenario == "symlink" {
				if err := os.Symlink(t.TempDir(), filepath.Join(dir, ".zenith")); err != nil {
					t.Fatal(err)
				}
			}
			service := newRunArtifactReader(api, dir, uuid.NewString(), nil)
			result, err := service.FetchArtifact(
				t.Context(),
				tools.ArtifactFetchInput{ArtifactID: api.manifest.ID},
			)
			if scenario == "symlink" && api.downloads != 0 {
				t.Fatal("escaped root reached download")
			}
			if err == nil || result.LocalPath != "" {
				t.Fatalf("unsafe fetch = %+v, %v", result, err)
			}
			_ = filepath.Walk(dir, func(path string, _ os.FileInfo, err error) error {
				if err == nil && strings.HasSuffix(path, "report.txt") {
					t.Errorf("partial file published: %s", path)
				}
				return nil
			})
		})
	}
}

// The daemon writes a fetched file from outside the Run's sandbox, so it
// writes only where the sandbox would let the Run write. A
// link the Run made inside its workspace may not carry the write elsewhere,
// even somewhere inside the workspace (a workspace that is the person's home
// holds launch points and sign-ins), and a folder the Run's guard denies is
// refused. Neither reaches the download.
func TestArtifactFetchWritesOnlyWhereTheRunsSandboxAllows(t *testing.T) {
	t.Parallel()
	newAPI := func() *artifactAPIFake {
		return &artifactAPIFake{
			body: "plist",
			manifest: tools.ArtifactManifest{
				ID: uuid.NewString(),
				Items: []tools.ArtifactItem{{
					ID:             uuid.NewString(),
					AssetVersionID: uuid.NewString(),
					DisplayName:    "agent.plist",
					Role:           "primary",
					SizeBytes:      5,
				}},
			},
		}
	}
	t.Run("a link inside the workspace", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		api := newAPI()
		runID := uuid.NewString()
		launchAgents := filepath.Join(dir, "Library", "LaunchAgents")
		parent := filepath.Join(dir, ".zenith", "artifacts", runID)
		for _, folder := range []string{launchAgents, parent} {
			if err := os.MkdirAll(folder, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		version := api.manifest.Items[0].AssetVersionID
		if err := os.Symlink(
			filepath.Join("..", "..", "..", "Library", "LaunchAgents"),
			filepath.Join(parent, version),
		); err != nil {
			t.Fatal(err)
		}
		reader := newRunArtifactReader(api, dir, runID, nil)
		_, err := reader.FetchArtifact(
			t.Context(),
			tools.ArtifactFetchInput{ArtifactID: api.manifest.ID},
		)
		if !errors.Is(err, errArtifactFolderMoved) || api.downloads != 0 {
			t.Fatalf("fetch through a link = %v (downloads %d), want it refused", err, api.downloads)
		}
		entries, err := os.ReadDir(launchAgents)
		if err != nil || len(entries) != 0 {
			t.Fatalf("the fetch wrote into the linked folder: %v, %v", entries, err)
		}
	})
	t.Run("a folder the guard denies", func(t *testing.T) {
		t.Parallel()
		api := newAPI()
		var guarded []string
		reader := newRunArtifactReader(api, t.TempDir(), uuid.NewString(), func(path string) error {
			guarded = append(guarded, path)
			return errSandboxDeniedRead
		})
		_, err := reader.FetchArtifact(
			t.Context(),
			tools.ArtifactFetchInput{ArtifactID: api.manifest.ID},
		)
		if !errors.Is(err, errSandboxDeniedRead) || api.downloads != 0 {
			t.Fatalf("denied fetch = %v (downloads %d), want it refused", err, api.downloads)
		}
		if len(guarded) != 1 || filepath.Base(guarded[0]) != "agent.plist" ||
			!filepath.IsAbs(guarded[0]) {
			t.Fatalf("the guard saw %v, want the file's real path", guarded)
		}
	})
}

func TestArtifactFetchSelectsPinnedItemAndRejectsAppWithoutFiles(t *testing.T) {
	t.Parallel()
	api := &artifactAPIFake{
		body: "file",
		manifest: tools.ArtifactManifest{
			ID: uuid.NewString(),
			Items: []tools.ArtifactItem{
				{
					ID:             uuid.NewString(),
					AssetVersionID: uuid.NewString(),
					Role:           "supporting",
					DisplayName:    "source.txt",
					SizeBytes:      4,
				},
			},
		},
	}
	reader := newRunArtifactReader(api, t.TempDir(), uuid.NewString(), nil)
	if _, err := reader.FetchArtifact(
		t.Context(),
		tools.ArtifactFetchInput{ArtifactID: api.manifest.ID},
	); err == nil {
		t.Fatal("missing primary was silently replaced")
	}
	result, err := reader.FetchArtifact(
		t.Context(),
		tools.ArtifactFetchInput{ArtifactID: api.manifest.ID, ItemID: api.manifest.Items[0].ID},
	)
	if err != nil || result.AssetVersionID != api.manifest.Items[0].AssetVersionID {
		t.Fatalf("item fetch = %+v, %v", result, err)
	}
	api.manifest.Items = nil
	api.manifest.Type = "app"
	if _, err := reader.FetchArtifact(
		t.Context(),
		tools.ArtifactFetchInput{ArtifactID: api.manifest.ID},
	); err == nil {
		t.Fatal("app without a file was fetched")
	}
}

func TestArtifactToolsEndToEndThroughRunChannel(t *testing.T) {
	t.Parallel()
	run := channelTestRun(t, false)
	manifest := tools.ArtifactManifest{
		ID: uuid.NewString(),
		Items: []tools.ArtifactItem{
			{
				ID:             uuid.NewString(),
				AssetVersionID: uuid.NewString(),
				Role:           "primary",
				DisplayName:    "report.txt",
				SizeBytes:      6,
			},
		},
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer session-token" ||
			r.Header.Get("X-Zenith-Team-Run-Token") != run.AuthToken {
			t.Error("missing server proof")
		}
		prefix := "/api/v1/companies/" + run.CompanyID + "/runs/" + run.ID.String() + "/agent-artifacts/" + manifest.ID
		switch r.URL.Path {
		case prefix:
			_ = json.NewEncoder(w).Encode(manifest)
		case prefix + "/items/0/download":
			_, _ = io.WriteString(w, "report")
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	d := &Daemon{
		cfg:                Config{ServerBaseURL: upstream.URL},
		client:             NewClient(upstream.URL),
		localPlaneEndpoint: "http://127.0.0.1:19713",
	}
	d.client.SetToken("session-token")
	control, revoke, err := d.registerRunChannel(
		run,
		RunContext{},
		run.AuthToken,
		runChannelWorkspace{path: t.TempDir()},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = revoke() }()
	local := httptest.NewServer(d.runChannelHandler())
	defer local.Close()
	service, err := tools.NewArtifactChannelService(local.URL, control.Capability, run.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	inspected, err := service.InspectArtifact(t.Context(), tools.ArtifactInspectInput{ArtifactID: manifest.ID})
	if err != nil || inspected.ID != manifest.ID {
		t.Fatalf("inspect=%+v, %v", inspected, err)
	}
	fetched, err := service.FetchArtifact(t.Context(), tools.ArtifactFetchInput{ArtifactID: manifest.ID})
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(fetched.LocalPath)
	if err != nil || string(content) != "report" {
		t.Fatalf("download=%q, %v", content, err)
	}
	if err := revoke(); err != nil {
		t.Fatal(err)
	}
	if _, err := service.FetchArtifact(t.Context(), tools.ArtifactFetchInput{ArtifactID: manifest.ID}); err == nil {
		t.Fatal("revoked Run fetched cached file")
	}
}

func TestArtifactFetchCancelledWhileWaitingDoesNotRead(t *testing.T) {
	t.Parallel()
	reader := newRunArtifactReader(nil, t.TempDir(), uuid.NewString(), nil)
	reader.gate <- struct{}{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := reader.FetchArtifact(
		ctx,
		tools.ArtifactFetchInput{ArtifactID: uuid.NewString()},
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("cancelled fetch = %v", err)
	}
}
