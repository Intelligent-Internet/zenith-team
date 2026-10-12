package cloudrun

import (
	"archive/zip"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maximumSourceArchiveBytes   = int64(100 << 20)
	maximumSourceExpandedBytes  = uint64(1 << 30)
	maximumSourceArchiveEntries = 20_000
	generatedDockerfilePath     = ".boardly/Dockerfile"
	generatedNginxPath          = ".boardly/nginx.conf"
	generatedSourceDirectory    = ".boardly"
	generatedStaticDirectory    = ".boardly/site"
	staticPublicDirectory       = "public"
	defaultApplicationPort      = 8080
)

var generatedArchiveTime = time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)

type preparedSource struct {
	*os.File
	Template       Template
	Build          BuildType
	Port           int64
	DockerfilePath string
	Size           int64
	MD5            []byte
	path           string
}

func (s *preparedSource) Close() error {
	if s == nil {
		return nil
	}
	return errors.Join(s.File.Close(), os.Remove(s.path))
}

type sourceEntry struct {
	file *zip.File
	name string
}

func prepareSourceArchive(
	ctx context.Context,
	source io.Reader,
	expectedSize int64,
	expectedSHA256 []byte,
) (*preparedSource, error) {
	if ctx == nil || source == nil || expectedSize < 1 || expectedSize > maximumSourceArchiveBytes ||
		(len(expectedSHA256) != 0 && len(expectedSHA256) != sha256.Size) {
		return nil, sourceFailure(errors.New("invalid source archive metadata"))
	}
	original, err := os.CreateTemp("", "boardly-source-*.zip")
	if err != nil {
		return nil, providerFailure(
			FailureProviderUnavailable,
			true,
			fmt.Errorf("create source staging file: %w", err),
		)
	}
	originalPath := original.Name()
	defer func() {
		_ = original.Close()
		_ = os.Remove(originalPath)
	}()
	hasher := sha256.New()
	limited := &io.LimitedReader{R: contextReader{ctx: ctx, reader: source}, N: expectedSize + 1}
	written, err := io.Copy(io.MultiWriter(original, hasher), limited)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, providerFailure(
			FailureProviderUnavailable,
			true,
			fmt.Errorf("stage source archive: %w", err),
		)
	}
	if written != expectedSize || len(expectedSHA256) != 0 &&
		subtle.ConstantTimeCompare(hasher.Sum(nil), expectedSHA256) != 1 {
		return nil, sourceFailure(errors.New("source archive integrity mismatch"))
	}
	reader, err := zip.NewReader(original, expectedSize)
	if err != nil {
		return nil, sourceFailure(fmt.Errorf("open source ZIP: %w", err))
	}
	entries, err := inspectSourceEntries(reader.File)
	if err != nil {
		return nil, sourceFailure(err)
	}
	template, build, err := detectTemplate(entries)
	if err != nil {
		return nil, sourceFailure(err)
	}
	prepared, err := writePreparedArchive(ctx, entries, template, build)
	if err != nil {
		return nil, err
	}
	return prepared, nil
}

func inspectSourceEntries(files []*zip.File) ([]sourceEntry, error) {
	if len(files) == 0 || len(files) > maximumSourceArchiveEntries {
		return nil, errors.New("source ZIP has an unsupported entry count")
	}
	entries := make([]sourceEntry, 0, len(files))
	seen := make(map[string]struct{}, len(files))
	var expanded uint64
	for _, file := range files {
		name, err := safeSourceEntryName(file.Name)
		if err != nil {
			return nil, err
		}
		if file.Mode()&os.ModeSymlink != 0 || (!file.FileInfo().IsDir() && !file.Mode().IsRegular()) {
			return nil, errors.New("source ZIP contains a non-regular entry")
		}
		if _, exists := seen[name]; exists {
			return nil, errors.New("source ZIP contains duplicate paths")
		}
		seen[name] = struct{}{}
		if !file.FileInfo().IsDir() {
			if file.UncompressedSize64 > maximumSourceExpandedBytes-expanded {
				return nil, errors.New("source ZIP expands beyond the supported limit")
			}
			expanded += file.UncompressedSize64
		}
		entries = append(entries, sourceEntry{file: file, name: name})
	}
	if expanded == 0 {
		return nil, errors.New("source ZIP has no files")
	}
	wrapper := commonSourceWrapper(entries)
	for index := range entries {
		name := strings.TrimPrefix(entries[index].name, wrapper)
		if name == "" {
			continue
		}
		if name == generatedSourceDirectory || strings.HasPrefix(name, generatedSourceDirectory+"/") {
			return nil, errors.New("source ZIP uses Zenith Team's reserved build directory")
		}
		entries[index].name = name
	}
	return entries, nil
}

func safeSourceEntryName(value string) (string, error) {
	if value == "" || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') ||
		strings.Contains(value, "\\") || strings.HasPrefix(value, "/") {
		return "", errors.New("source ZIP contains an unsafe path")
	}
	trimmed := strings.TrimSuffix(value, "/")
	if trimmed == "" || path.Clean(trimmed) != trimmed || trimmed == "." || trimmed == ".." ||
		strings.HasPrefix(trimmed, "../") {
		return "", errors.New("source ZIP contains an unsafe path")
	}
	if strings.HasSuffix(value, "/") {
		return trimmed + "/", nil
	}
	return trimmed, nil
}

func commonSourceWrapper(entries []sourceEntry) string {
	var wrapper string
	fileCount := 0
	for _, entry := range entries {
		if entry.file.FileInfo().IsDir() {
			continue
		}
		fileCount++
		first, remainder, found := strings.Cut(entry.name, "/")
		if !found || first == "" || remainder == "" {
			return ""
		}
		if wrapper == "" {
			wrapper = first
		} else if wrapper != first {
			return ""
		}
	}
	if fileCount == 0 || wrapper == "" {
		return ""
	}
	if wrapper == staticPublicDirectory {
		return ""
	}
	return wrapper + "/"
}

func detectTemplate(entries []sourceEntry) (Template, BuildType, error) {
	files := make(map[string]sourceEntry, len(entries))
	for _, entry := range entries {
		if !entry.file.FileInfo().IsDir() {
			files[entry.name] = entry
		}
	}
	if _, ok := files["Dockerfile"]; ok {
		return TemplateDockerfile, BuildDocker, nil
	}
	dependencies := sourcePackageDependencies(files["package.json"])
	if dependencies["next"] || hasSourcePrefix(files, "next.config.") {
		return TemplateNextJS, BuildDocker, nil
	}
	if dependencies["vite"] || hasSourcePrefix(files, "vite.config.") {
		return TemplateVite, BuildDocker, nil
	}
	if dependencies["react-scripts"] {
		return TemplateCRA, BuildDocker, nil
	}
	if _, ok := files[staticPublicDirectory+"/index.html"]; ok {
		return TemplateStatic, BuildDocker, nil
	}
	if _, ok := files["index.html"]; ok {
		return "", "", errors.New("static source must place public files under public/")
	}
	return TemplateBuildpack, BuildPack, nil
}

func sourcePackageDependencies(entry sourceEntry) map[string]bool {
	dependencies := make(map[string]bool)
	if entry.file == nil {
		return dependencies
	}
	reader, err := entry.file.Open()
	if err != nil {
		return dependencies
	}
	defer reader.Close()
	var manifest struct {
		Dependencies    map[string]json.RawMessage `json:"dependencies"`
		DevDependencies map[string]json.RawMessage `json:"devDependencies"`
	}
	decoder := json.NewDecoder(io.LimitReader(reader, 1<<20))
	if decoder.Decode(&manifest) != nil {
		return dependencies
	}
	for name := range manifest.Dependencies {
		dependencies[name] = true
	}
	for name := range manifest.DevDependencies {
		dependencies[name] = true
	}
	return dependencies
}

func hasSourcePrefix(files map[string]sourceEntry, prefix string) bool {
	for name := range files {
		if strings.HasPrefix(name, prefix) && !strings.Contains(strings.TrimPrefix(name, prefix), "/") {
			return true
		}
	}
	return false
}

func writePreparedArchive(
	ctx context.Context,
	entries []sourceEntry,
	template Template,
	build BuildType,
) (*preparedSource, error) {
	target, err := os.CreateTemp("", "boardly-prepared-source-*.zip")
	if err != nil {
		return nil, providerFailure(
			FailureProviderUnavailable,
			true,
			fmt.Errorf("create prepared source: %w", err),
		)
	}
	targetPath := target.Name()
	keep := false
	defer func() {
		if !keep {
			_ = target.Close()
			_ = os.Remove(targetPath)
		}
	}()
	writer := zip.NewWriter(target)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			_ = writer.Close()
			return nil, err
		}
		if entry.name == "" || entry.file.FileInfo().IsDir() {
			continue
		}
		if template == TemplateStatic {
			publicPath, ok := staticPublicPath(entry.name)
			if !ok {
				continue
			}
			name := generatedStaticDirectory + "/" + publicPath
			if err := copySourceEntry(ctx, writer, entry, name); err != nil {
				_ = writer.Close()
				return nil, err
			}
			continue
		}
		if err := copySourceEntry(ctx, writer, entry, entry.name); err != nil {
			_ = writer.Close()
			return nil, err
		}
	}
	dockerfilePath := "Dockerfile"
	if build == BuildDocker && template != TemplateDockerfile {
		dockerfilePath = generatedDockerfilePath
		if err := writeGeneratedEntry(writer, generatedDockerfilePath, dockerfileFor(template)); err != nil {
			_ = writer.Close()
			return nil, err
		}
		if template == TemplateVite || template == TemplateCRA || template == TemplateStatic {
			if err := writeGeneratedEntry(writer, generatedNginxPath, generatedNginxConfig); err != nil {
				_ = writer.Close()
				return nil, err
			}
		}
	}
	if err := writer.Close(); err != nil {
		return nil, providerFailure(
			FailureProviderUnavailable,
			true,
			fmt.Errorf("finalize prepared source: %w", err),
		)
	}
	if err := target.Sync(); err != nil {
		return nil, providerFailure(
			FailureProviderUnavailable,
			true,
			fmt.Errorf("sync prepared source: %w", err),
		)
	}
	info, err := target.Stat()
	if err != nil {
		return nil, providerFailure(
			FailureProviderUnavailable,
			true,
			fmt.Errorf("inspect prepared source: %w", err),
		)
	}
	if _, err := target.Seek(0, io.SeekStart); err != nil {
		return nil, providerFailure(
			FailureProviderUnavailable,
			true,
			fmt.Errorf("rewind prepared source: %w", err),
		)
	}
	md5Hasher := md5.New()
	if _, err := io.Copy(md5Hasher, target); err != nil {
		return nil, providerFailure(
			FailureProviderUnavailable,
			true,
			fmt.Errorf("hash prepared source: %w", err),
		)
	}
	if _, err := target.Seek(0, io.SeekStart); err != nil {
		return nil, providerFailure(
			FailureProviderUnavailable,
			true,
			fmt.Errorf("rewind prepared source: %w", err),
		)
	}
	keep = true
	return &preparedSource{
		File: target, Template: template, Build: build, Port: defaultApplicationPort,
		DockerfilePath: dockerfilePath, Size: info.Size(), MD5: md5Hasher.Sum(nil), path: targetPath,
	}, nil
}

func copySourceEntry(
	ctx context.Context,
	writer *zip.Writer,
	entry sourceEntry,
	name string,
) error {
	header := entry.file.FileHeader
	header.Name = name
	header.Comment = ""
	header.Extra = nil
	header.NonUTF8 = false
	destination, err := writer.CreateHeader(&header)
	if err != nil {
		return sourceFailure(fmt.Errorf("create prepared ZIP entry: %w", err))
	}
	source, err := entry.file.Open()
	if err != nil {
		return sourceFailure(fmt.Errorf("open source ZIP entry: %w", err))
	}
	_, copyErr := io.Copy(destination, contextReader{ctx: ctx, reader: source})
	closeErr := source.Close()
	if copyErr == nil && closeErr == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return sourceFailure(fmt.Errorf("copy source ZIP entry: %w", errors.Join(copyErr, closeErr)))
}

func staticPublicPath(name string) (string, bool) {
	name, ok := strings.CutPrefix(name, staticPublicDirectory+"/")
	if !ok {
		return "", false
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == "" || strings.HasPrefix(segment, ".") {
			return "", false
		}
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".html", ".htm", ".css", ".js", ".mjs", ".json", ".webmanifest", ".xml", ".txt",
		".wasm", ".ico", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".avif", ".svg",
		".woff", ".woff2", ".ttf", ".otf", ".eot", ".mp4", ".webm", ".ogg", ".mp3",
		".wav", ".pdf":
		return name, true
	default:
		return "", false
	}
}

func writeGeneratedEntry(writer *zip.Writer, name, content string) error {
	header := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: generatedArchiveTime}
	header.SetMode(0o644)
	entry, err := writer.CreateHeader(header)
	if err != nil {
		return providerFailure(
			FailureProviderUnavailable,
			true,
			fmt.Errorf("create generated source entry: %w", err),
		)
	}
	if _, err := io.WriteString(entry, content); err != nil {
		return providerFailure(
			FailureProviderUnavailable,
			true,
			fmt.Errorf("write generated source entry: %w", err),
		)
	}
	return nil
}

func dockerfileFor(template Template) string {
	switch template {
	case TemplateNextJS:
		return generatedNextJSDockerfile
	case TemplateVite, TemplateCRA:
		if template == TemplateCRA {
			return generatedCRADockerfile
		}
		return generatedViteDockerfile
	case TemplateStatic:
		return generatedStaticDockerfile
	default:
		return ""
	}
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

const generatedNextJSDockerfile = `FROM node:20-slim
WORKDIR /app
COPY package*.json ./
RUN if [ -f package-lock.json ]; then npm ci; else npm install; fi
COPY . .
ENV NEXT_TELEMETRY_DISABLED=1
RUN npm run build
ENV NODE_ENV=production PORT=8080 HOSTNAME=0.0.0.0
EXPOSE 8080
CMD ["sh", "-c", "npm start -- -p ${PORT:-8080}"]
`

const generatedViteDockerfile = `FROM node:20-slim AS builder
WORKDIR /app
COPY package*.json ./
RUN if [ -f package-lock.json ]; then npm ci; else npm install; fi
COPY . .
RUN npm run build
FROM nginxinc/nginx-unprivileged:alpine
COPY .boardly/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=builder /app/dist /usr/share/nginx/html
EXPOSE 8080
`

const generatedCRADockerfile = `FROM node:20-slim AS builder
WORKDIR /app
COPY package*.json ./
RUN if [ -f package-lock.json ]; then npm ci; else npm install; fi
COPY . .
RUN npm run build
FROM nginxinc/nginx-unprivileged:alpine
COPY .boardly/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=builder /app/build /usr/share/nginx/html
EXPOSE 8080
`

const generatedStaticDockerfile = `FROM nginxinc/nginx-unprivileged:alpine
COPY .boardly/nginx.conf /etc/nginx/conf.d/default.conf
COPY .boardly/site/ /usr/share/nginx/html/
EXPOSE 8080
`

const generatedNginxConfig = `server {
  listen 8080;
  server_name _;
  root /usr/share/nginx/html;
  index index.html;
  location / { try_files $uri $uri/ /index.html; }
  location ~* \.(js|css|png|jpg|jpeg|gif|ico|svg|woff|woff2|ttf|eot)$ {
    expires 1y;
    add_header Cache-Control "public, immutable";
  }
}
`
