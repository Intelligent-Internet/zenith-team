package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/mcp/tools"
	"boardly.dev/boardly/server/internal/runchannel"
)

type runArtifactAPI interface {
	inspect(context.Context, string) (tools.ArtifactManifest, error)
	download(context.Context, string, int, io.Writer) error
}

// runArtifactReader completes artifact_inspect and artifact_fetch for one
// Run. It runs in the daemon, outside the Run's sandbox: it reads the
// Artifact on the server with the daemon's session and the Run proof, so the
// server allows only what the Run's Authorizer may read, and it writes a
// fetched file into the Run's workspace only where the sandbox would let the
// Run write it (Access TECH B3b, B3e).
type runArtifactReader struct {
	api            runArtifactAPI
	workDir, runID string
	// guard refuses, by real path, what the Run's own sandbox denies it
	// (runFileGuard); nil adds no check beyond the workspace itself.
	guard  func(realPath string) error
	gate   chan struct{}
	cached map[string]tools.ArtifactFetchResult
}

func newRunArtifactReader(
	api runArtifactAPI,
	workDir, runID string,
	guard func(realPath string) error,
) *runArtifactReader {
	return &runArtifactReader{
		api:     api,
		workDir: workDir,
		runID:   runID,
		guard:   guard,
		gate:    make(chan struct{}, 1),
		cached:  make(map[string]tools.ArtifactFetchResult),
	}
}

// errArtifactFolderMoved refuses a fetch whose folder is not where its path
// puts it inside the Run's workspace: a link on the way could carry the
// daemon's write into a sign-in folder or a launch point the Run's sandbox
// keeps it from writing.
var errArtifactFolderMoved = errors.New(
	"the artifact folder in this Run's workspace is a link or was moved; remove it and fetch again",
)

// openArtifactFolder opens directory, a folder inside the workspace root
// opened, for the fetch to write the file name in. The folder's real path
// must be the workspace's real path with the same rest, so no link the Run
// made sits on the way, and the Run's guard must allow the file there. Every
// write then goes through the opened folder, so a link swapped in afterwards
// cannot move it.
func (s *runArtifactReader) openArtifactFolder(
	root *os.Root,
	workDir, directory, name string,
) (*os.Root, error) {
	folder, err := root.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	realWorkDir, err := filepath.EvalSymlinks(workDir)
	want := filepath.Join(realWorkDir, directory)
	resolved, resolveErr := filepath.EvalSymlinks(want)
	opened, statErr := folder.Stat(".")
	current, lstatErr := os.Lstat(want)
	if err != nil || resolveErr != nil || statErr != nil || lstatErr != nil ||
		resolved != want || !os.SameFile(opened, current) {
		_ = folder.Close()
		return nil, errArtifactFolderMoved
	}
	if s.guard != nil {
		if err := s.guard(filepath.Join(want, name)); err != nil {
			_ = folder.Close()
			return nil, err
		}
	}
	return folder, nil
}

func (s *runArtifactReader) InspectArtifact(
	ctx context.Context,
	in tools.ArtifactInspectInput,
) (tools.ArtifactManifest, error) {
	id, err := uuid.Parse(in.ArtifactID)
	if err != nil || id == uuid.Nil {
		return tools.ArtifactManifest{}, errors.New("artifactId must be a UUID")
	}
	return s.api.inspect(ctx, id.String())
}

func (s *runArtifactReader) FetchArtifact(
	ctx context.Context,
	in tools.ArtifactFetchInput,
) (tools.ArtifactFetchResult, error) {
	// Serialize cache verification and replacement, but let a cancelled Run
	// abandon a queued fetch before it starts any I/O.
	select {
	case s.gate <- struct{}{}:
		defer func() { <-s.gate }()
	case <-ctx.Done():
		return tools.ArtifactFetchResult{}, ctx.Err()
	}
	// Reauthorize even a cache hit: bytes on disk never grant product access.
	manifest, err := s.InspectArtifact(ctx, tools.ArtifactInspectInput{ArtifactID: in.ArtifactID})
	if err != nil {
		return tools.ArtifactFetchResult{}, err
	}
	var item *tools.ArtifactItem
	for i := range manifest.Items {
		candidate := &manifest.Items[i]
		if (in.ItemID != "" && candidate.ID == in.ItemID) || (in.ItemID == "" && candidate.Role == "primary") {
			item = candidate
			break
		}
	}
	if item == nil {
		return tools.ArtifactFetchResult{}, errors.New(
			"artifact has no matching downloadable file; inspect its document destination or app URL",
		)
	}
	version, err := uuid.Parse(item.AssetVersionID)
	if err != nil || version == uuid.Nil || item.SizeBytes < 0 || item.SizeBytes > 100<<20 {
		return tools.ArtifactFetchResult{}, errors.New("invalid artifact file metadata")
	}
	workDir, err := filepath.Abs(s.workDir)
	if err != nil {
		return tools.ArtifactFetchResult{}, err
	}
	root, err := os.OpenRoot(workDir)
	if err != nil {
		return tools.ArtifactFetchResult{}, err
	}
	defer root.Close()
	directory := filepath.Join(".zenith", "artifacts", s.runID, version.String())
	if err := root.MkdirAll(directory, 0o700); err != nil {
		return tools.ArtifactFetchResult{}, fmt.Errorf("prepare artifact input: %w", err)
	}
	base := safeAttachmentFilename(item.DisplayName, version.String())
	folder, err := s.openArtifactFolder(root, workDir, directory, base)
	if err != nil {
		return tools.ArtifactFetchResult{}, err
	}
	defer folder.Close()
	name := filepath.Join(directory, base)
	if cached, ok := s.cached[name]; ok && cached.AssetVersionID == item.AssetVersionID {
		file, openErr := folder.Open(base)
		if openErr == nil {
			hash := sha256.New()
			count, copyErr := io.Copy(hash, io.LimitReader(file, item.SizeBytes+1))
			_ = file.Close()
			if copyErr == nil && count == item.SizeBytes &&
				hex.EncodeToString(hash.Sum(nil)) == cached.SHA256 {
				if err := ctx.Err(); err != nil {
					return tools.ArtifactFetchResult{}, err
				}
				cached.ArtifactID, cached.ItemID = manifest.ID, item.ID
				return cached, nil
			}
		}
	}
	temporary := ".download-" + uuid.NewString()
	file, err := folder.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return tools.ArtifactFetchResult{}, err
	}
	defer func() {
		_ = file.Close()
		_ = folder.Remove(temporary)
	}()
	hash := sha256.New()
	writer := &artifactFileWriter{writer: io.MultiWriter(file, hash), remaining: item.SizeBytes}
	if err := s.api.download(ctx, manifest.ID, item.Ordinal, writer); err != nil {
		return tools.ArtifactFetchResult{}, err
	}
	if writer.remaining != 0 {
		return tools.ArtifactFetchResult{}, errors.New("artifact download is incomplete")
	}
	if err := ctx.Err(); err != nil {
		return tools.ArtifactFetchResult{}, err
	}
	if err := file.Close(); err != nil {
		return tools.ArtifactFetchResult{}, err
	}
	if err := folder.Rename(temporary, base); err != nil {
		return tools.ArtifactFetchResult{}, err
	}
	result := tools.ArtifactFetchResult{
		ArtifactID:     manifest.ID,
		ItemID:         item.ID,
		AssetVersionID: item.AssetVersionID,
		LocalPath:      filepath.Join(workDir, name),
		ContentType:    item.ContentType,
		SizeBytes:      item.SizeBytes,
		SHA256:         hex.EncodeToString(hash.Sum(nil)),
	}
	s.cached[name] = result
	return result, nil
}

// Bound bytes before writing, including responses that omit Content-Length.
type artifactFileWriter struct {
	writer    io.Writer
	remaining int64
}

func (w *artifactFileWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, errors.New("artifact download exceeds declared size")
	}
	n, err := w.writer.Write(p)
	w.remaining -= int64(n)
	return n, err
}

type runArtifactRemote struct {
	daemon  *Daemon
	channel *runChannel
}

func (a *runArtifactRemote) path(id string) string {
	return "/companies/" + a.channel.run.CompanyID + "/runs/" + a.channel.run.ID.String() + "/agent-artifacts/" + id
}

func (a *runArtifactRemote) inspect(ctx context.Context, id string) (tools.ArtifactManifest, error) {
	response, err := a.daemon.forwardRunChannelRequest(
		ctx,
		a.channel,
		runchannel.Request{SchemaVersion: 1, Method: http.MethodGet, Path: a.path(id)},
		runChannelForwardTimeout,
	)
	if err != nil {
		return tools.ArtifactManifest{}, err
	}
	if response.Status != http.StatusOK {
		return tools.ArtifactManifest{}, fmt.Errorf("artifact access refused (HTTP %d)", response.Status)
	}
	var result tools.ArtifactManifest
	if err := json.Unmarshal(response.Body, &result); err != nil {
		return result, err
	}
	return result, nil
}

func (a *runArtifactRemote) download(ctx context.Context, id string, ordinal int, dst io.Writer) error {
	c := a.daemon.client
	path := "/api/v1" + a.path(id) + "/items/" + strconv.Itoa(ordinal) + "/download"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	token, err := c.authorize(ctx, req)
	if err != nil {
		return err
	}
	req.Header.Set("X-Boardly-Run-Token", a.channel.runToken)
	c.setIdentityHeaders(req)
	client := *c.bundleClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	if response.StatusCode == http.StatusUnauthorized && token != "" {
		_ = response.Body.Close()
		response, err = c.retryUnauthorized(ctx, &client, req, token, nil)
		if err != nil {
			return err
		}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("artifact download refused (HTTP %d)", response.StatusCode)
	}
	_, err = io.Copy(dst, response.Body)
	return err
}

func readRunArtifact(
	ctx context.Context,
	reader *runArtifactReader,
	request runchannel.Request,
) (runchannel.Response, error) {
	var result any
	var err error
	if request.Path == "/runs/"+reader.runID+"/artifact-inspect" {
		var input tools.ArtifactInspectInput
		if err = json.Unmarshal(request.Body, &input); err == nil {
			result, err = reader.InspectArtifact(ctx, input)
		}
	} else {
		var input tools.ArtifactFetchInput
		if err = json.Unmarshal(request.Body, &input); err == nil {
			result, err = reader.FetchArtifact(ctx, input)
		}
	}
	if err != nil {
		return runChannelVerdict(http.StatusUnprocessableEntity, "artifact_read_failed", err.Error())
	}
	encoded, err := json.Marshal(result)
	return runchannel.Response{SchemaVersion: 1, Status: http.StatusOK, Body: encoded}, err
}
