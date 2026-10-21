package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/contentbundle"
)

// prepare is the build-time network boundary. Committed pins contain only
// metadata; downloaded content lives in a verified cache and ignored output.
func runPrepare(ctx context.Context, args []string, stdout io.Writer) error {
	flags := newFlagSet("prepare")
	pins := flags.String("pins", "internal/daemon/tool_releases", "managed Tool metadata pins")
	out := flags.String("out", "internal/daemon/tool_releases/generated", "generated assets directory")
	cache := flags.String("cache", "", "npm cache directory (defaults to the user cache)")
	if err := parseFlags(flags, args, stdout, false); err != nil {
		return err
	}
	if *cache == "" {
		root, err := os.UserCacheDir()
		if err != nil {
			return err
		}
		*cache = filepath.Join(root, "zenith-team", "npm")
	}
	bundles, err := contentbundle.LoadFS(os.DirFS(*pins))
	if err != nil {
		return err
	}
	if len(bundles) == 0 {
		return errors.New("no managed Tool pin; import the plugin before preparing assets")
	}
	preparer := toolPreparer{cache: *cache, fetch: fetchToolPackage}
	generated := map[string][]byte{}
	for _, bundle := range bundles {
		if bundle.Lock.Component != contentbundle.ComponentTypeManagedTools {
			return errors.New("prepare accepts only managedTools pins")
		}
		catalog, err := contentbundle.ReadManagedTools(bundle.Files)
		if err != nil {
			return err
		}
		raw, err := preparer.prepare(ctx, catalog)
		if err != nil {
			return fmt.Errorf("prepare %s: %w", bundle.Lock.Plugin, err)
		}
		generated[bundle.Lock.Plugin+".json"] = raw
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(generated)) {
		if err := writeFileAtomic(filepath.Join(*out, name), generated[name]); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "prepared %s (%d bytes; generated, not committed)\n", name, len(generated[name]))
	}
	return nil
}

type toolPreparer struct {
	cache string
	fetch func(context.Context, string) ([]byte, error)
}

func (p toolPreparer) prepare(ctx context.Context, catalog contentbundle.ManagedTools) ([]byte, error) {
	var assets []contentbundle.ToolAsset
	for _, release := range catalog.Releases {
		if release.Skill.ID == "" {
			continue
		}
		raw, err := p.packageBytes(ctx, release)
		if err != nil {
			return nil, err
		}
		asset, err := materializeTool(release, raw)
		if err != nil {
			return nil, err
		}
		assets = append(assets, asset)
	}
	return contentbundle.EncodeToolAssets(catalog, assets)
}

func (p toolPreparer) packageBytes(ctx context.Context, release contentbundle.ToolRelease) ([]byte, error) {
	digest, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(release.Integrity, "sha512-"))
	if err != nil || len(digest) != 64 {
		return nil, errors.New("invalid npm integrity")
	}
	name := filepath.Join(p.cache, hex.EncodeToString(digest)+".tgz")
	file, err := os.Open(name)
	var raw []byte
	switch {
	case err == nil:
		defer file.Close()
		raw, err = io.ReadAll(io.LimitReader(file, 32<<20+1))
	case errors.Is(err, os.ErrNotExist):
		address := "https://registry.npmjs.org/" + release.PackageName + "/-/" + path.Base(
			release.PackageName,
		) + "-" + release.Version + ".tgz"
		raw, err = p.fetch(ctx, address)
	default:
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	actual := sha512.Sum512(raw)
	if len(raw) > 32<<20 || !bytes.Equal(actual[:], digest) {
		return nil, fmt.Errorf(
			"npm integrity differs for %s; discard a corrupt cache entry before retrying",
			release.ID(),
		)
	}
	if file == nil {
		if err := os.MkdirAll(p.cache, 0o755); err != nil {
			return nil, err
		}
		if err := writeFileAtomic(name, raw); err != nil {
			return nil, err
		}
	}
	return raw, nil
}

func fetchToolPackage(ctx context.Context, address string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{
		Timeout: 60 * time.Second,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if request.URL.Scheme != "https" || request.URL.Host != "registry.npmjs.org" || len(via) > 3 {
				return errors.New("npm download redirected outside the registry")
			}
			return nil
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("npm download returned HTTP %d", response.StatusCode)
	}
	return io.ReadAll(io.LimitReader(response.Body, 32<<20+1))
}

// Compose only the reviewed source directories, without installing or running
// npm code. The final export hash detects any upstream layout/content change.
func materializeTool(release contentbundle.ToolRelease, raw []byte) (contentbundle.ToolAsset, error) {
	compressed, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return contentbundle.ToolAsset{}, err
	}
	defer compressed.Close()
	archive := tar.NewReader(compressed)
	files := map[string][]byte{}
	seen := map[string]bool{}
	sourceCounts := make([]int, len(release.SkillSources))
	var packageJSON, license []byte
	var expanded int64
	for count := 0; ; count++ {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return contentbundle.ToolAsset{}, err
		}
		name := strings.TrimSuffix(header.Name, "/")
		if count >= 10000 || !contentbundle.SafeSkillPath(name) || !strings.HasPrefix(name, "package/") ||
			seen[name] {
			return contentbundle.ToolAsset{}, errors.New("unsafe or duplicate npm package path")
		}
		seen[name] = true
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > 16<<20 ||
			expanded+header.Size > 128<<20 {
			return contentbundle.ToolAsset{}, errors.New("unsupported npm package entry or expanded size")
		}
		expanded += header.Size
		body, err := io.ReadAll(archive)
		if err != nil {
			return contentbundle.ToolAsset{}, err
		}
		switch name {
		case "package/package.json":
			packageJSON = body
		case "package/LICENSE":
			license = body
		}
		for index, source := range release.SkillSources {
			relative, ok := strings.CutPrefix(name, "package/"+source+"/")
			if !ok {
				continue
			}
			destination := "skills/" + release.Skill.Name + "/" + relative
			if _, exists := files[destination]; exists {
				return contentbundle.ToolAsset{}, fmt.Errorf(
					"npm skill sources collide at %s",
					relative,
				)
			}
			files[destination] = body
			sourceCounts[index]++
		}
	}
	var manifest struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if json.Unmarshal(packageJSON, &manifest) != nil || manifest.Name != release.PackageName ||
		manifest.Version != release.Version ||
		len(license) == 0 {
		return contentbundle.ToolAsset{}, errors.New("npm package identity or license differs from the release")
	}
	for _, count := range sourceCounts {
		if count == 0 {
			return contentbundle.ToolAsset{}, errors.New("npm package is missing a reviewed skill source")
		}
	}
	skill, err := contentbundle.ReadSkill(contentbundle.NewFS(files), release.Skill)
	if err != nil {
		return contentbundle.ToolAsset{}, err
	}
	return contentbundle.ToolAsset{ToolKey: release.Key, License: string(license), Skill: skill}, nil
}
