package tools

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCommerceManifestGoldenFixturesRoundTripTypedContract(t *testing.T) {
	t.Parallel()

	paths, err := filepath.Glob(filepath.Join(
		"..", "..", "..", "testdata", "commerce-manifests-v2", "*.json",
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
			decoder := json.NewDecoder(bytes.NewReader(contents))
			decoder.DisallowUnknownFields()
			var manifest CommerceManifest
			if err := decoder.Decode(&manifest); err != nil {
				t.Fatalf("decode typed manifest: %v", err)
			}
			if err := ensureJSONEOF(decoder); err != nil {
				t.Fatalf("decode trailing manifest data: %v", err)
			}
			encoded, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			var replay CommerceManifest
			if err := json.Unmarshal(encoded, &replay); err != nil {
				t.Fatal(err)
			}
			if replay.SchemaVersion != 2 || replay.Site.Alias == "" || len(replay.Offers) == 0 {
				t.Fatalf("typed manifest replay = %+v", replay)
			}
		})
	}
}
