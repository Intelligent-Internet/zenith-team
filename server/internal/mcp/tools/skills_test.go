package tools

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNewSkillServiceSearchesOnlyIndexedSummaries(t *testing.T) {
	t.Parallel()
	root := newSkillSnapshotFixture(t, []skillFixture{
		{key: "incident-response", description: "Handle service incidents safely."},
		{key: "release-notes", description: "Prepare customer-facing launch notes."},
	})
	service, err := NewSkillService(root)
	if err != nil {
		t.Fatalf("NewSkillService() error = %v", err)
	}

	result, err := service.SearchSkills(t.Context(), SearchSkillsInput{Query: "INCIDENT"})
	if err != nil {
		t.Fatalf("SearchSkills() error = %v", err)
	}
	if len(result.Skills) != 1 || result.Skills[0].Key != "incident-response" {
		t.Fatalf("SearchSkills() = %+v", result)
	}
	if strings.Contains(result.Skills[0].Description, root) {
		t.Fatalf("SearchSkills() leaked snapshot path in %+v", result)
	}
}

func TestOpenTaskSkillServiceDoesNotAllowSnapshotToShadowReservedPlatformSkill(t *testing.T) {
	t.Parallel()

	// The snapshot index is sorted by key; the reserved key sorts last here.
	root := newSkillSnapshotFixture(t, []skillFixture{
		{key: "incident-response", description: "Handle incidents."},
		{key: "zenith-platform", description: "Untrusted replacement."},
	})
	service, err := OpenTaskSkillService(root, nil, nil)
	if err != nil {
		t.Fatalf("OpenTaskSkillService() error = %v", err)
	}
	// Windows cannot remove the TempDir while the snapshot root is open.
	defer service.Close()
	loaded, err := service.LoadSkillReference(t.Context(), LoadSkillReferenceInput{
		SkillKey: "zenith-platform",
		Path:     "SKILL.md",
	})
	if err != nil {
		t.Fatalf("LoadSkillReference() error = %v", err)
	}
	if strings.Contains(loaded.Content, "Untrusted replacement") ||
		!strings.Contains(loaded.Content, "Operating Zenith Team") {
		t.Fatalf("reserved platform Skill was shadowed: %s", loaded.Content)
	}
	result, err := service.SearchSkills(t.Context(), SearchSkillsInput{Query: "incident"})
	if err != nil || len(result.Skills) != 1 || result.Skills[0].Key != "incident-response" {
		t.Fatalf("Skill snapshot merge = %+v, %v", result, err)
	}
}

func TestSkillServiceSearchBoundsAndResultCap(t *testing.T) {
	t.Parallel()
	fixtures := make([]skillFixture, 0, 21)
	for index := range 21 {
		fixtures = append(fixtures, skillFixture{
			key:         "matching-skill-" + string(rune('a'+index)),
			description: "Matching summary",
		})
	}
	service, err := NewSkillService(newSkillSnapshotFixture(t, fixtures))
	if err != nil {
		t.Fatalf("NewSkillService() error = %v", err)
	}

	result, err := service.SearchSkills(
		t.Context(),
		SearchSkillsInput{Query: strings.Repeat("q", 256)},
	)
	if err != nil || len(result.Skills) != 0 {
		t.Fatalf("SearchSkills(boundary) = %+v, %v", result, err)
	}
	result, err = service.SearchSkills(t.Context(), SearchSkillsInput{Query: "matching"})
	if err != nil || len(result.Skills) != maxSkillSearchResults {
		t.Fatalf("SearchSkills(cap) count = %d, error = %v", len(result.Skills), err)
	}

	invalidUTF8 := string([]byte{0xff})
	for _, query := range []string{strings.Repeat("q", 256+1), invalidUTF8} {
		if _, err := service.SearchSkills(
			t.Context(),
			SearchSkillsInput{Query: query},
		); !errors.Is(
			err,
			ErrSkillSearchQuery,
		) {
			t.Fatalf("SearchSkills(%q) error = %v, want ErrSkillSearchQuery", query, err)
		}
	}
}

func TestSkillServiceLoadsOnlyVerifiedIndexedText(t *testing.T) {
	t.Parallel()
	root := newSkillSnapshotFixture(t, []skillFixture{{
		key:         "incident-response",
		description: "Handle incidents.",
		files: []fixtureFile{
			{path: "references/runbook.md", contents: []byte("unique safe procedure\n"), text: true},
			{path: "assets/icon.png", contents: []byte{0x89, 0x50, 0x4e, 0x47}, text: false},
		},
	}})
	service, err := NewSkillService(root)
	if err != nil {
		t.Fatalf("NewSkillService() error = %v", err)
	}

	result, err := service.LoadSkillReference(t.Context(), LoadSkillReferenceInput{
		SkillKey: "incident-response",
		Path:     "references/runbook.md",
	})
	if err != nil {
		t.Fatalf("LoadSkillReference() error = %v", err)
	}
	if result.Content != "unique safe procedure\n" || result.Path != "references/runbook.md" {
		t.Fatalf("LoadSkillReference() = %+v", result)
	}
	if strings.Contains(result.Path, root) {
		t.Fatalf("LoadSkillReference() leaked snapshot root in %+v", result)
	}

	for _, input := range []LoadSkillReferenceInput{
		{SkillKey: "unknown-skill", Path: "references/runbook.md"},
		{SkillKey: "incident-response", Path: "references/missing.md"},
		{SkillKey: "incident-response", Path: "assets/icon.png"},
		{SkillKey: "incident-response", Path: "../outside.txt"},
		{SkillKey: "incident-response", Path: filepath.Join(root, "outside.txt")},
	} {
		if _, err := service.LoadSkillReference(
			t.Context(),
			input,
		); !errors.Is(
			err,
			ErrSkillReferenceUnavailable,
		) {
			t.Fatalf("LoadSkillReference(%+v) error = %v, want ErrSkillReferenceUnavailable", input, err)
		}
	}
}

func TestSkillServiceLoadsRootWithoutReferenceInventory(t *testing.T) {
	t.Parallel()
	var files []fixtureFile
	var wantReferences []string
	for i := range 450 {
		path := fmt.Sprintf("references/guide-%03d.md", i)
		files = append(files, fixtureFile{path: path, contents: []byte("Media workflow"), text: true})
		wantReferences = append(wantReferences, path)
	}
	files = append(files, fixtureFile{path: "assets/icon.png", contents: []byte{0x89, 0x50}, text: false})
	root := newSkillSnapshotFixture(t, []skillFixture{{
		key: "media", description: "Create media.",
		files: files,
	}})
	service, err := NewSkillService(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	result, err := service.LoadSkillReference(t.Context(), LoadSkillReferenceInput{
		SkillKey: "media", Path: "SKILL.md",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content == "" || result.SHA256 == "" || len(result.References) != 0 {
		t.Fatalf("root read = %+v, want verified instructions without the reference inventory", result)
	}
	// Omitting the inventory must not remove access to deeper indexed guides.
	guide, err := service.LoadSkillReference(t.Context(), LoadSkillReferenceInput{
		SkillKey: "media", Path: wantReferences[449], IncludeReferences: true,
	})
	if err != nil || guide.Content != "Media workflow" || len(guide.References) != 0 {
		t.Fatalf("deep guide = %+v, %v", guide, err)
	}
	inventory, err := service.LoadSkillReference(t.Context(), LoadSkillReferenceInput{
		SkillKey: "media", Path: "SKILL.md", IncludeReferences: true,
	})
	if err != nil || !slices.Equal(inventory.References, wantReferences) {
		t.Fatalf("explicit inventory = %v, %v, want all readable guides", inventory.References, err)
	}
	if inventory.Content != result.Content || inventory.SHA256 != result.SHA256 {
		t.Fatal("requesting the inventory changed the verified instructions")
	}
}

func TestSkillServiceRejectsTamperingAfterOpen(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(t *testing.T, root string)
	}{
		{
			name: "hash mismatch",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				writeFile(
					t,
					filepath.Join(root, "incident-response", "references", "runbook.md"),
					[]byte("changed"),
				)
			},
		},
		{
			name: "symlink replacement",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				target := filepath.Join(t.TempDir(), "outside.txt")
				writeFile(t, target, []byte("outside"))
				path := filepath.Join(root, "incident-response", "references", "runbook.md")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := newSkillSnapshotFixture(t, []skillFixture{
				{
					key:         "incident-response",
					description: "Handle incidents.",
					files: []fixtureFile{
						{
							path:     "references/runbook.md",
							contents: []byte("original"),
							text:     true,
						},
					},
				},
			})
			service, err := NewSkillService(root)
			if err != nil {
				t.Fatalf("NewSkillService() error = %v", err)
			}
			test.mutate(t, root)
			_, err = service.LoadSkillReference(t.Context(), LoadSkillReferenceInput{
				SkillKey: "incident-response",
				Path:     "references/runbook.md",
			})
			if !errors.Is(err, ErrSkillReferenceUnavailable) {
				t.Fatalf("LoadSkillReference() error = %v, want ErrSkillReferenceUnavailable", err)
			}
		})
	}
}

func TestSkillServiceLoadSizeBoundary(t *testing.T) {
	t.Parallel()
	boundary := []byte(strings.Repeat("a", maxSkillInstructionsBytes))
	overBoundary := append(append([]byte{}, boundary...), 'a')
	root := newSkillSnapshotFixture(t, []skillFixture{{
		key:         "incident-response",
		description: "Handle incidents.",
		files: []fixtureFile{
			{path: "references/boundary.txt", contents: boundary, text: true},
			{path: "references/over-boundary.txt", contents: overBoundary, text: true},
		},
	}})
	service, err := NewSkillService(root)
	if err != nil {
		t.Fatalf("NewSkillService() error = %v", err)
	}
	result, err := service.LoadSkillReference(t.Context(), LoadSkillReferenceInput{
		SkillKey: "incident-response",
		Path:     "references/boundary.txt",
	})
	if err != nil || len(result.Content) != maxSkillInstructionsBytes {
		t.Fatalf("LoadSkillReference(boundary) size = %d, error = %v", len(result.Content), err)
	}
	_, err = service.LoadSkillReference(t.Context(), LoadSkillReferenceInput{
		SkillKey: "incident-response",
		Path:     "references/over-boundary.txt",
	})
	if !errors.Is(err, ErrSkillReferenceUnavailable) {
		t.Fatalf("LoadSkillReference(over boundary) error = %v", err)
	}
}

func TestNewSkillServiceRejectsMalformedOrUntrustedSnapshots(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(t *testing.T, root string, index *skillSnapshotIndex)
	}{
		{
			name: "unsupported schema",
			mutate: func(_ *testing.T, _ string, index *skillSnapshotIndex) {
				index.SchemaVersion = 2
			},
		},
		{
			name: "noncanonical company UUID",
			mutate: func(_ *testing.T, _ string, index *skillSnapshotIndex) {
				index.CompanyID = strings.ToUpper(index.CompanyID)
			},
		},
		{
			name: "zero run id",
			mutate: func(_ *testing.T, _ string, index *skillSnapshotIndex) {
				index.RunID = uuid.Nil
			},
		},
		{
			name: "traversing indexed path",
			mutate: func(_ *testing.T, _ string, index *skillSnapshotIndex) {
				index.Skills[0].Files[1].Path = "../outside.txt"
			},
		},
		{
			name: "absolute indexed path",
			mutate: func(_ *testing.T, root string, index *skillSnapshotIndex) {
				index.Skills[0].Files[1].Path = filepath.Join(root, "outside.txt")
			},
		},
		{
			name: "uppercase hash",
			mutate: func(_ *testing.T, _ string, index *skillSnapshotIndex) {
				index.Skills[0].Files[1].Hash = strings.ToUpper(index.Skills[0].Files[1].Hash)
			},
		},
		{
			name: "invalid UTF-8 text",
			mutate: func(t *testing.T, root string, index *skillSnapshotIndex) {
				t.Helper()
				contents := []byte{0xff}
				path := filepath.Join(root, "incident-response", "references", "runbook.md")
				writeFile(t, path, contents)
				index.Skills[0].Files[1].Size = int64(len(contents))
				index.Skills[0].Files[1].Hash = hashBytes(contents)
			},
		},
		{
			name: "content hash mismatch",
			mutate: func(t *testing.T, root string, _ *skillSnapshotIndex) {
				t.Helper()
				writeFile(
					t,
					filepath.Join(root, "incident-response", "references", "runbook.md"),
					[]byte("changed"),
				)
			},
		},
		{
			name: "indexed size mismatch",
			mutate: func(_ *testing.T, _ string, index *skillSnapshotIndex) {
				index.Skills[0].Files[1].Size++
			},
		},
		{
			name: "symlinked indexed file",
			mutate: func(t *testing.T, root string, index *skillSnapshotIndex) {
				t.Helper()
				target := filepath.Join(t.TempDir(), "outside.txt")
				contents := []byte("outside")
				writeFile(t, target, contents)
				path := filepath.Join(root, "incident-response", "references", "runbook.md")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
				index.Skills[0].Files[1].Size = int64(len(contents))
				index.Skills[0].Files[1].Hash = hashBytes(contents)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := newSkillSnapshotFixture(t, []skillFixture{
				{
					key:         "incident-response",
					description: "Handle incidents.",
					files: []fixtureFile{
						{
							path:     "references/runbook.md",
							contents: []byte("runbook"),
							text:     true,
						},
					},
				},
			})
			index := readSkillIndex(t, root)
			test.mutate(t, root, &index)
			writeSkillIndex(t, root, index)
			if _, err := NewSkillService(root); !errors.Is(err, ErrSkillSnapshotUnavailable) {
				t.Fatalf("NewSkillService() error = %v, want ErrSkillSnapshotUnavailable", err)
			}
		})
	}
}

func TestNewSkillServiceRejectsNonCanonicalJSON(t *testing.T) {
	t.Parallel()
	root := newSkillSnapshotFixture(t, []skillFixture{{
		key: "incident-response", description: "Handle incidents.",
	}})
	path := filepath.Join(root, skillSnapshotIndexFilename)
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		contents []byte
	}{
		{
			name: "duplicate field",
			contents: []byte(strings.Replace(
				string(contents), `"schemaVersion":1`, `"schemaVersion":1,"schemaVersion":1`, 1,
			)),
		},
		{
			name: "unknown field",
			contents: []byte(strings.Replace(
				string(contents), `"schemaVersion":1`, `"schemaVersion":1,"unknown":true`, 1,
			)),
		},
		{
			name: "legacy numeric run id",
			contents: []byte(strings.Replace(
				string(contents), `"runId":"00000000-0000-4000-8000-000000000071"`, `"runId":71`, 1,
			)),
		},
		{name: "multiple values", contents: append(append([]byte{}, contents...), []byte(` {}`)...)},
	} {
		t.Run(test.name, func(t *testing.T) {
			testRoot := newSkillSnapshotFixture(t, []skillFixture{{
				key: "incident-response", description: "Handle incidents.",
			}})
			writeFile(t, filepath.Join(testRoot, skillSnapshotIndexFilename), test.contents)
			if _, err := NewSkillService(testRoot); !errors.Is(err, ErrSkillSnapshotUnavailable) {
				t.Fatalf("NewSkillService() error = %v, want ErrSkillSnapshotUnavailable", err)
			}
		})
	}
}

func TestValidateSkillRelativePathBounds(t *testing.T) {
	t.Parallel()
	boundaryPath := "references/" + strings.Repeat("a", 512-len("references/"))
	boundaryDepth := "references/" + strings.Repeat("a/", 16-2) + "file.md"
	for _, path := range []string{"SKILL.md", "README.md", boundaryPath, boundaryDepth} {
		if err := validateSkillRelativePath(path); err != nil {
			t.Fatalf("validateSkillRelativePath(%q) error = %v", path, err)
		}
	}
	for _, path := range []string{
		boundaryPath + "a",
		"references/" + strings.Repeat("a/", 16-1) + "file.md",
		"references//file.md",
		"references/../assets/file.txt",
		"nested/SKILL.md",
		".zenith-team.json",
		string([]byte{'r', 0xff}),
	} {
		if err := validateSkillRelativePath(path); err == nil {
			t.Fatalf("validateSkillRelativePath(%q) error = nil", path)
		}
	}
}

type fixtureFile struct {
	path     string
	contents []byte
	text     bool
}

type skillFixture struct {
	key         string
	description string
	files       []fixtureFile
}

func newSkillSnapshotFixture(t *testing.T, fixtures []skillFixture) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	index := skillSnapshotIndex{
		SchemaVersion: 1,
		CompanyID:     "018f3f6e-57f6-7c4d-8c33-089b673439a9",
		RunID:         uuid.MustParse("00000000-0000-4000-8000-000000000071"),
		CreatedAt:     time.Date(2026, time.August, 6, 1, 2, 3, 0, time.UTC).Format(time.RFC3339Nano),
		Skills:        make([]skillSnapshotSkill, 0, len(fixtures)),
	}
	for _, fixture := range fixtures {
		directory := filepath.Join(root, fixture.key)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		files := append([]fixtureFile{
			{
				path: "SKILL.md",
				contents: []byte(
					"---\nname: " + fixture.key + "\ndescription: " + fixture.description + "\n---\n",
				),
				text: true,
			},
		}, fixture.files...)
		indexedFiles := make([]skillSnapshotFile, 0, len(files))
		for _, file := range files {
			path := filepath.Join(directory, filepath.FromSlash(file.path))
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			writeFile(t, path, file.contents)
			indexedFiles = append(indexedFiles, skillSnapshotFile{
				Path: file.path,
				Hash: hashBytes(file.contents),
				Size: int64(len(file.contents)),
				Text: file.text,
			})
		}
		sort.Slice(indexedFiles, func(i, j int) bool { return indexedFiles[i].Path < indexedFiles[j].Path })
		index.Skills = append(index.Skills, skillSnapshotSkill{
			Key: fixture.key, Name: fixture.key, Description: fixture.description,
			ContentHash: fixturePackageHash(t, indexedFiles, files), Files: indexedFiles,
		})
	}
	writeSkillIndex(t, root, index)
	return root
}

func readSkillIndex(t *testing.T, root string) skillSnapshotIndex {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(root, skillSnapshotIndexFilename))
	if err != nil {
		t.Fatal(err)
	}
	var index skillSnapshotIndex
	if err := json.Unmarshal(contents, &index); err != nil {
		t.Fatal(err)
	}
	return index
}

func writeSkillIndex(t *testing.T, root string, index skillSnapshotIndex) {
	t.Helper()
	contents, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, skillSnapshotIndexFilename), contents)
}

func writeFile(t *testing.T, path string, contents []byte) {
	t.Helper()
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

func hashBytes(contents []byte) string {
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}

func fixturePackageHash(t *testing.T, indexedFiles []skillSnapshotFile, files []fixtureFile) string {
	t.Helper()
	contentsByPath := make(map[string][]byte, len(files))
	for _, file := range files {
		contentsByPath[file.path] = file.contents
	}
	hash := sha256.New()
	var length [8]byte
	for _, indexedFile := range indexedFiles {
		binary.BigEndian.PutUint64(length[:], uint64(len(indexedFile.Path)))
		if _, err := hash.Write(length[:]); err != nil {
			t.Fatal(err)
		}
		if _, err := hash.Write([]byte(indexedFile.Path)); err != nil {
			t.Fatal(err)
		}
		binary.BigEndian.PutUint64(length[:], uint64(indexedFile.Size))
		if _, err := hash.Write(length[:]); err != nil {
			t.Fatal(err)
		}
		if _, err := hash.Write(contentsByPath[indexedFile.Path]); err != nil {
			t.Fatal(err)
		}
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func TestOpenTaskSkillServiceRejectsUnavailableFrozenSnapshot(t *testing.T) {
	t.Parallel()
	for _, invalid := range []string{"missing", "corrupt"} {
		t.Run(invalid, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if invalid == "missing" {
				root = filepath.Join(root, "absent")
			} else if err := os.WriteFile(filepath.Join(root, "index.json"), []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
			service, err := OpenTaskSkillService(root, nil, nil)
			if service != nil {
				_ = service.Close()
			}
			if err == nil || service != nil {
				t.Fatal("unavailable frozen snapshot silently fell back to platform skills")
			}
		})
	}
}
