package commerce

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManifestGoldenFixturesRemainValidDomainDocuments(t *testing.T) {
	t.Parallel()

	paths, err := filepath.Glob(filepath.Join(
		"..", "..", "testdata", "commerce-manifests-v2", "*.json",
	))
	if err != nil || len(paths) != 4 {
		t.Fatalf("golden manifest paths = %v, error = %v", paths, err)
	}
	for _, fixturePath := range paths {
		t.Run(filepath.Base(fixturePath), func(t *testing.T) {
			t.Parallel()
			contents, err := os.ReadFile(fixturePath)
			if err != nil {
				t.Fatal(err)
			}
			document, err := ParseManifest(contents, EnvironmentLive)
			if err != nil {
				t.Fatalf("ParseManifest() error = %v", err)
			}
			if document.Manifest.SchemaVersion != 2 || document.Manifest.Site.Alias == "" ||
				len(document.Manifest.Offers) == 0 || len(document.CanonicalJSON) == 0 {
				t.Fatalf("ParseManifest() = %+v", document)
			}
		})
	}
}
