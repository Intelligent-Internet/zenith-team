//go:build integration

package daemon

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// Opt-in because the ordinary test gate must not depend on the npm registry.
// This exercises the public package, npm layout, relocation, export, repeat
// startup, and repair without using a person's Studio credentials.
func TestPublishedStudioNPMInstallation(t *testing.T) {
	if os.Getenv("ZENITH_TEAM_TEST_MANAGED_NPM") != "1" {
		t.Skip("set ZENITH_TEAM_TEST_MANAGED_NPM=1 for the published npm smoke test")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	root := t.TempDir()
	release := reviewedToolReleases()[0]
	first, err := installToolRelease(ctx, root, release)
	if err != nil {
		t.Fatal(err)
	}
	renderer, _ := findToolRelease("hyperframes")
	rendererInstallation, err := installToolRelease(ctx, root, renderer)
	if err != nil {
		t.Fatalf("install renderer dependency: %v", err)
	}
	if err := prepareHyperframes(ctx, rendererInstallation); err != nil {
		t.Fatalf("prepare video renderer: %v", err)
	}
	launcherRoot := filepath.Join(root, "launchers")
	if err := os.MkdirAll(launcherRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	managedRenderer, err := writeHyperframesLauncher(launcherRoot, rendererInstallation)
	if err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(root, "composition")
	if _, err := runToolProcess(
		ctx,
		toolProcess{
			Executable: managedRenderer.Node,
			Args:       []string{managedRenderer.Entrypoint, "init", project},
		},
	); err != nil {
		t.Fatalf("initialize composition: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatal("renderer injected upstream agent instructions")
	}
	// Two minutes and three distinct scenes exercise the complete timeline,
	// including audio, rather than only a short model-generated source clip.
	html := `<!doctype html><html><head><style>html,body{margin:0;width:320px;height:180px;overflow:hidden}#main{position:relative;width:320px;height:180px}.clip{position:absolute;inset:0}</style></head><body><div id="main" data-composition-id="main" data-start="0" data-duration="120" data-width="320" data-height="180"><section id="opening" class="clip" data-start="0" data-duration="40" data-track-index="1" style="background:rgb(220,30,30)"></section><section id="middle" class="clip" data-start="40" data-duration="40" data-track-index="1" style="background:rgb(30,220,30)"></section><section id="ending" class="clip" data-start="80" data-duration="40" data-track-index="1" style="background:rgb(30,30,220)"></section><audio id="narration" class="clip" data-start="0" data-duration="120" data-track-index="2" src="tone.wav"></audio></div><script>window.__timelines={};</script></body></html>`
	if err := os.WriteFile(filepath.Join(project, "index.html"), []byte(html), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runToolProcess(
		ctx,
		toolProcess{
			Executable: hyperframesEnvironment()["HYPERFRAMES_FFMPEG_PATH"],
			Args: []string{
				"-v",
				"error",
				"-f",
				"lavfi",
				"-i",
				"sine=frequency=440:duration=120",
				"-ar",
				"22050",
				filepath.Join(project, "tone.wav"),
			},
		},
	); err != nil {
		t.Fatalf("create deterministic audio: %v", err)
	}
	outputFile := filepath.Join(root, "render.mp4")
	if _, err := runToolProcess(
		ctx,
		toolProcess{
			Executable: managedRenderer.Node,
			Args: []string{
				managedRenderer.Entrypoint,
				"render",
				"--fps",
				"2",
				"--output",
				outputFile,
			},
			Dir: project,
		},
	); err != nil {
		t.Fatalf("render real composition: %v", err)
	}
	if info, err := os.Stat(outputFile); err != nil || info.Size() == 0 {
		t.Fatalf("missing rendered video: %v", err)
	}

	probe, err := runToolProcess(
		ctx,
		toolProcess{
			Executable: hyperframesEnvironment()["HYPERFRAMES_FFPROBE_PATH"],
			Args: []string{
				"-v",
				"error",
				"-show_entries",
				"stream=codec_type,width,height:format=duration",
				"-of",
				"json",
				outputFile,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var mediaInfo struct {
		Streams []struct {
			CodecType     string `json:"codec_type"`
			Width, Height int
		}
		Format struct{ Duration string }
	}
	if err := json.Unmarshal(probe, &mediaInfo); err != nil {
		t.Fatal(err)
	}
	duration, err := strconv.ParseFloat(mediaInfo.Format.Duration, 64)
	if err != nil || math.Abs(duration-120) > 0.1 {
		t.Fatalf("duration: %s", probe)
	}
	video, audio := false, false
	for _, stream := range mediaInfo.Streams {
		if stream.CodecType == "video" {
			video = stream.Width == 320 && stream.Height == 180
		}
		if stream.CodecType == "audio" {
			audio = true
		}
	}
	if !video || !audio {
		t.Fatalf("missing full video/audio streams: %s", probe)
	}
	for index, second := range []string{"1", "60", "119"} {
		frame, err := runToolProcess(
			ctx,
			toolProcess{
				Executable: hyperframesEnvironment()["HYPERFRAMES_FFMPEG_PATH"],
				Args: []string{
					"-v",
					"error",
					"-ss",
					second,
					"-i",
					outputFile,
					"-frames:v",
					"1",
					"-vf",
					"scale=1:1",
					"-f",
					"rawvideo",
					"-pix_fmt",
					"rgb24",
					"pipe:1",
				},
			},
		)
		if err != nil || len(frame) != 3 || frame[index] < 150 || frame[(index+1)%3] > 80 ||
			frame[(index+2)%3] > 80 {
			t.Fatalf("scene at %ss = %v, %v", second, frame, err)
		}
	}
	t.Log("verified two-minute MP4: 320x180, audio stream, distinct opening/middle/ending scenes")
	studioHome := filepath.Join(root, "company-account")
	// An invalid ambient store proves auth status uses the isolated account
	// without entering a transient Run wait or inheriting another account.
	ambientHome := filepath.Join(root, "ambient-account")
	if err := os.MkdirAll(ambientHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(ambientHome, "credentials.json"),
		[]byte("invalid-json"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	bridgeCalls := make(chan struct{}, 1)
	admission := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case bridgeCalls <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer admission.Close()
	studioLauncher, err := writeStudioLauncher(
		launcherRoot,
		first,
		studioLauncherConfig{
			Home: studioHome, APIURL: release.APIURL,
			Endpoint: admission.URL, RunToken: "fixture-run",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	authOutput, err := runToolProcess(
		ctx,
		toolProcess{
			Executable: studioLauncher.Node,
			Args:       []string{studioLauncher.Entrypoint, "--json", "auth", "status"},
			Env:        map[string]string{"ZENITH_STUDIO_CLI_HOME": ambientHome},
		},
	)
	var authStatus struct {
		OK   bool `json:"ok"`
		Data struct {
			Authenticated bool `json:"authenticated"`
		} `json:"data"`
	}
	if err != nil || json.Unmarshal(authOutput, &authStatus) != nil || !authStatus.OK ||
		authStatus.Data.Authenticated {
		t.Fatalf("signed-out scoped auth status = %s, %v", authOutput, err)
	}
	select {
	case <-bridgeCalls:
		t.Fatal("read-only auth status changed Run admission")
	default:
	}
	t.Log("real managed renderer produced MP4; real Studio launcher uses the isolated account")
	second, err := installToolRelease(ctx, root, release)
	if err != nil {
		t.Fatal(err)
	}
	if second.Prefix != first.Prefix {
		t.Fatal("repeat startup reinstalled a valid release")
	}
	info, err := os.Stat(first.Entrypoint)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(first.Entrypoint, []byte("process.exit(1)"), info.Mode()); err != nil {
		t.Fatal(err)
	}
	repaired, err := installToolRelease(ctx, root, release)
	if err != nil {
		t.Fatal(err)
	}
	if repaired.Prefix == first.Prefix {
		t.Fatal("repair replaced an existing prefix")
	}
	if _, err := os.Stat(first.Prefix); err != nil {
		t.Fatal("repair deleted a previously published prefix")
	}
	if err := verifyExportedToolSkill(filepath.Join(root, "missing-export"), release); err == nil {
		t.Fatal("accepted missing export")
	}
	t.Logf(
		"published CLI installed and verified: version=%s files=%d hash=%s",
		release.Version,
		release.Skill.FileCount,
		release.Skill.Hash,
	)
}
