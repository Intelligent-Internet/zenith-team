package cloudrun

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"strings"
	"testing"
)

func TestPrepareSourceArchiveNormalizesWrappedViteProject(t *testing.T) {
	t.Parallel()

	source := zipFixture(t, map[string]string{
		"generated-app/package.json": `{"scripts":{"build":"vite build"},"dependencies":{"vite":"latest"}}`,
		"generated-app/index.html":   "<main>Zenith Team</main>",
		"generated-app/src/main.tsx": "export default null",
	})
	digest := sha256.Sum256(source)

	prepared, err := prepareSourceArchive(
		context.Background(),
		bytes.NewReader(source),
		int64(len(source)),
		digest[:],
	)
	if err != nil {
		t.Fatalf("prepareSourceArchive() error = %v", err)
	}
	defer prepared.Close()

	if prepared.Template != TemplateVite || prepared.Build != BuildDocker || prepared.Port != 8080 {
		t.Fatalf(
			"prepared source = template %q build %q port %d",
			prepared.Template,
			prepared.Build,
			prepared.Port,
		)
	}
	files := readZipFixture(t, prepared)
	if _, ok := files["generated-app/package.json"]; ok {
		t.Fatal("prepared archive retained the single wrapper directory")
	}
	if files["package.json"] == "" || files["src/main.tsx"] == "" {
		t.Fatalf("prepared archive files = %#v", files)
	}
	if !strings.Contains(files[generatedDockerfilePath], "npm run build") {
		t.Fatalf("generated Dockerfile = %q", files[generatedDockerfilePath])
	}
	if files[generatedNginxPath] == "" {
		t.Fatal("prepared Vite archive has no generated nginx configuration")
	}
}

func TestPrepareSourceArchivePreservesUserDockerfile(t *testing.T) {
	t.Parallel()

	source := zipFixture(t, map[string]string{
		"Dockerfile": "FROM scratch\n",
		"app.txt":    "content",
	})
	digest := sha256.Sum256(source)

	prepared, err := prepareSourceArchive(
		context.Background(),
		bytes.NewReader(source),
		int64(len(source)),
		digest[:],
	)
	if err != nil {
		t.Fatalf("prepareSourceArchive() error = %v", err)
	}
	defer prepared.Close()

	if prepared.Template != TemplateDockerfile || prepared.Build != BuildDocker {
		t.Fatalf("prepared source = template %q build %q", prepared.Template, prepared.Build)
	}
	files := readZipFixture(t, prepared)
	if files["Dockerfile"] != "FROM scratch\n" {
		t.Fatalf("Dockerfile = %q", files["Dockerfile"])
	}
	if _, ok := files[generatedDockerfilePath]; ok {
		t.Fatal("prepared archive replaced a user Dockerfile")
	}
}

func TestPrepareSourceArchiveAcceptsAuthoritativeSizeWithoutStoredDigest(t *testing.T) {
	t.Parallel()

	source := zipFixture(t, map[string]string{"public/index.html": "<main>Zenith Team</main>"})
	prepared, err := prepareSourceArchive(
		t.Context(), bytes.NewReader(source), int64(len(source)), nil,
	)
	if err != nil {
		t.Fatalf("prepareSourceArchive() error = %v", err)
	}
	defer prepared.Close()
	if prepared.Template != TemplateStatic {
		t.Fatalf("prepared source template = %q, want %q", prepared.Template, TemplateStatic)
	}
}

func TestPrepareSourceArchivePublishesOnlySafeStaticFiles(t *testing.T) {
	t.Parallel()

	source := zipFixture(t, map[string]string{
		"public/index.html":         "<main>Zenith Team</main>",
		"public/assets/main.js":     "console.log('Zenith Team')",
		"public/assets/styles.css":  "body { color: black; }",
		"public/content/data.json":  `{"safe":true}`,
		"index.html":                "source entry point",
		"server.js":                 "startServer()",
		"package.json":              `{"scripts":{"start":"node server.js"}}`,
		"package-lock.json":         `{"lockfileVersion":3}`,
		"config/runtime.json":       `{"token":"secret"}`,
		".env":                      "DATABASE_URL=secret",
		"config.yaml":               "token: secret",
		"src/application.ts":        "export const secret = true",
		"public/.env":               "DATABASE_URL=secret",
		"public/config.yaml":        "token: secret",
		"public/src/application.ts": "export const secret = true",
	})
	digest := sha256.Sum256(source)

	prepared, err := prepareSourceArchive(
		t.Context(),
		bytes.NewReader(source),
		int64(len(source)),
		digest[:],
	)
	if err != nil {
		t.Fatalf("prepareSourceArchive() error = %v", err)
	}
	defer prepared.Close()

	files := readZipFixture(t, prepared)
	for _, name := range []string{
		generatedStaticDirectory + "/index.html",
		generatedStaticDirectory + "/assets/main.js",
		generatedStaticDirectory + "/assets/styles.css",
		generatedStaticDirectory + "/content/data.json",
	} {
		if files[name] == "" {
			t.Fatalf("prepared static archive has no safe published file %q", name)
		}
	}
	for _, name := range []string{
		generatedStaticDirectory + "/server.js",
		generatedStaticDirectory + "/package.json",
		generatedStaticDirectory + "/package-lock.json",
		generatedStaticDirectory + "/config/runtime.json",
		generatedStaticDirectory + "/.env",
		generatedStaticDirectory + "/config.yaml",
		generatedStaticDirectory + "/src/application.ts",
		"index.html",
		"server.js",
		"package.json",
		"package-lock.json",
		"config/runtime.json",
		".env",
		"config.yaml",
		"src/application.ts",
	} {
		if _, exists := files[name]; exists {
			t.Fatalf("prepared static archive published unsafe file %q", name)
		}
	}
	dockerfile := files[generatedDockerfilePath]
	if !strings.Contains(dockerfile, "COPY .zenith-team/site/") ||
		strings.Contains(dockerfile, "COPY . /usr/share/nginx/html") {
		t.Fatalf("generated static Dockerfile exposes the source tree: %q", dockerfile)
	}
}

func TestPrepareSourceArchiveRejectsUnsafeOrMismatchedSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		source []byte
		size   int64
		digest []byte
	}{
		{
			name:   "digest mismatch",
			source: zipFixture(t, map[string]string{"index.html": "ok"}),
			size:   -1,
			digest: make([]byte, sha256.Size),
		},
		{
			name:   "path traversal",
			source: unsafeZipFixture(t),
			size:   -1,
			digest: nil,
		},
		{
			name:   "static archive without explicit public directory",
			source: zipFixture(t, map[string]string{"index.html": "<main>Zenith Team</main>"}),
			size:   -1,
			digest: nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			size := test.size
			if size < 0 {
				size = int64(len(test.source))
			}
			digest := test.digest
			if digest == nil {
				sum := sha256.Sum256(test.source)
				digest = sum[:]
			}
			prepared, err := prepareSourceArchive(
				context.Background(), bytes.NewReader(test.source), size, digest,
			)
			if prepared != nil {
				_ = prepared.Close()
			}
			if err == nil {
				t.Fatal("prepareSourceArchive() error = nil, want source rejection")
			}
		})
	}
}

func zipFixture(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, content := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatalf("create ZIP entry %q: %v", name, err)
		}
		if _, err := io.WriteString(entry, content); err != nil {
			t.Fatalf("write ZIP entry %q: %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close ZIP fixture: %v", err)
	}
	return buffer.Bytes()
}

func unsafeZipFixture(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	header := &zip.FileHeader{Name: "../escape.txt", Method: zip.Store}
	entry, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatalf("create unsafe ZIP entry: %v", err)
	}
	if _, err := io.WriteString(entry, "escape"); err != nil {
		t.Fatalf("write unsafe ZIP entry: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close unsafe ZIP fixture: %v", err)
	}
	return buffer.Bytes()
}

func readZipFixture(t *testing.T, source io.Reader) map[string]string {
	t.Helper()
	content, err := io.ReadAll(source)
	if err != nil {
		t.Fatalf("read prepared ZIP: %v", err)
	}
	reader, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatalf("open prepared ZIP: %v", err)
	}
	files := make(map[string]string, len(reader.File))
	for _, file := range reader.File {
		opened, err := file.Open()
		if err != nil {
			t.Fatalf("open prepared entry %q: %v", file.Name, err)
		}
		value, readErr := io.ReadAll(opened)
		closeErr := opened.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("read prepared entry %q: read %v close %v", file.Name, readErr, closeErr)
		}
		files[file.Name] = string(value)
	}
	return files
}
