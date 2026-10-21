package platformbundle

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/contentbundle"

	"github.com/Intelligent-Internet/zenith-team/server/internal/mcp/tools"
)

// pinnedPlugins is every plugin zenith-team pins today. A bump that adds or
// drops one changes this list on purpose.
var pinnedPlugins = []string{"zenith-workflows"}

func TestPinnedBundlesVerify(t *testing.T) {
	t.Parallel()
	bundles, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	plugins := make([]string, 0, len(bundles))
	for _, bundle := range bundles {
		plugins = append(plugins, bundle.Lock.Plugin)
	}
	if !slices.Equal(plugins, pinnedPlugins) {
		t.Fatalf("pinned plugins = %v, want %v", plugins, pinnedPlugins)
	}
}

// Only a release is ever committed: a development pin from
// bundlectl import --dev turns the gate red until it is replaced by a bump.
func TestPinnedBundlesAreReleases(t *testing.T) {
	t.Parallel()
	bundles, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	for _, bundle := range bundles {
		lock := bundle.Lock
		if lock.Development() || lock.Tag != lock.Plugin+"--v"+lock.Version || lock.ContentTreeID == "" {
			t.Fatalf("%s is pinned to %q (tag %q), not a release; run make bundle-bump", lock.Plugin,
				lock.Version, lock.Tag)
		}
	}
}

func TestEveryArtifactHasALock(t *testing.T) {
	t.Parallel()
	artifacts, err := fs.Glob(pinned, "*"+contentbundle.ArtifactSuffix)
	if err != nil {
		t.Fatal(err)
	}
	locks, err := fs.Glob(pinned, "*"+contentbundle.LockSuffix)
	if err != nil {
		t.Fatal(err)
	}
	for index := range artifacts {
		artifacts[index] = strings.TrimSuffix(artifacts[index], contentbundle.ArtifactSuffix)
	}
	for index := range locks {
		locks[index] = strings.TrimSuffix(locks[index], contentbundle.LockSuffix)
	}
	if !slices.Equal(artifacts, locks) {
		t.Fatalf("artifacts %v and locks %v do not pair up", artifacts, locks)
	}
}

// The catalog the page parses and the skill a Run loads are the pinned files
// with zenith-team's harness SKILL.md at ".", byte for byte.
func TestWorkflowCatalogOverlaysTheHarness(t *testing.T) {
	t.Parallel()
	catalog, locks, err := WorkflowCatalog()
	if err != nil {
		t.Fatalf("WorkflowCatalog() error = %v", err)
	}
	harness, err := os.ReadFile("harness/zenith-workflows/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	instructions, err := fs.ReadFile(catalog, "SKILL.md")
	if err != nil || !bytes.Equal(instructions, harness) {
		t.Fatalf("SKILL.md at the catalog root is not the harness file (%v)", err)
	}
	for _, line := range []string{
		"name: " + tools.ZenithWorkflowsSkillKey + "\n",
		"description: " + workflowsSkillDescription + "\n",
	} {
		if !bytes.Contains(harness, []byte(line)) {
			t.Fatalf("harness SKILL.md does not carry %q", line)
		}
	}
	files := 0
	if err := fs.WalkDir(catalog, ".", func(_ string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			files++
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	want := 1
	for _, lock := range locks {
		want += lock.FileCount
	}
	if files != want {
		t.Fatalf("catalog holds %d files, want %d", files, want)
	}
	forms, err := fs.Glob(catalog, "*/workflow.yaml")
	if err != nil || len(forms) == 0 {
		t.Fatalf("catalog forms = %v, %v", forms, err)
	}
	for _, form := range forms {
		if _, err := fs.Stat(catalog, path.Join(path.Dir(form), "playbook.md")); err != nil {
			t.Fatalf("%s has no playbook: %v", form, err)
		}
	}
}

// Every pinned skill takes a reserved key, loads under the platform-skill
// rules, and lists one playbook reference per Workflow form.
func TestEveryPinnedBundleKeyIsReserved(t *testing.T) {
	t.Parallel()
	skills, locks, err := PlatformSkills()
	if err != nil || len(skills) == 0 || len(locks) == 0 {
		t.Fatalf("PlatformSkills() = %d skills, %d locks, %v", len(skills), len(locks), err)
	}
	summaries, err := tools.PlatformSkills(skills)
	if err != nil {
		t.Fatalf("tools.PlatformSkills() error = %v", err)
	}
	listed, err := PlatformSkillSummaries()
	if err != nil || len(listed) != len(summaries) {
		t.Fatalf("PlatformSkillSummaries() = %d, %v; want %d", len(listed), err, len(summaries))
	}
	workflows := summaries[len(summaries)-1]
	if workflows.Key != tools.ZenithWorkflowsSkillKey || workflows.Name != workflowsSkillName {
		t.Fatalf("last platform skill = %+v", workflows)
	}
	forms, err := fs.Glob(skills[0].Files, "*/workflow.yaml")
	if err != nil || len(forms) != len(workflows.References) {
		t.Fatalf("forms = %d (%v), playbook references = %d", len(forms), err, len(workflows.References))
	}
	for _, reference := range workflows.References {
		if !strings.HasSuffix(reference, "/playbook.md") {
			t.Fatalf("reference %q is not a playbook", reference)
		}
	}
}

func sampleFiles() map[string][]byte {
	return map[string][]byte{
		"account-brief/playbook.md":   []byte("# Account brief\n\nResearch the account.\n"),
		"account-brief/workflow.yaml": []byte("schemaVersion: 1\nkey: account-brief\n"),
		"weekly-recap/playbook.md":    []byte("# Weekly recap\n\nSummarize the week — briefly.\n"),
		"weekly-recap/workflow.yaml":  []byte("schemaVersion: 1\nkey: weekly-recap\n"),
	}
}

func digestHex(contents []byte) string {
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}

// lockFor is the lock bundlectl import would write for these exact bytes.
func lockFor(plugin string, files map[string][]byte, artifact []byte) contentbundle.Lock {
	return contentbundle.Lock{
		LockFormat:     1,
		Plugin:         plugin,
		Component:      contentbundle.ComponentTypeWorkflows,
		Version:        "1.2.3",
		Tag:            plugin + "--v1.2.3",
		Commit:         strings.Repeat("a", 40),
		ManifestPath:   "plugins/" + plugin + "/.boardly-plugin/plugin.json",
		ManifestSHA256: strings.Repeat("b", 64),
		ContentPath:    "plugins/" + plugin + "/boardly/workflows",
		ContentTreeID:  strings.Repeat("c", 40),
		Artifact: contentbundle.Artifact{
			File:   plugin + contentbundle.ArtifactSuffix,
			SHA256: digestHex(artifact),
			Size:   int64(len(artifact)),
		},
		TreeHash:    contentbundle.TreeHash(files),
		FileCount:   len(files),
		RetiredKeys: []string{},
	}
}

type tarMember struct {
	header   tar.Header
	contents []byte
}

func canonicalMember(name string, contents []byte) tarMember {
	return tarMember{header: tar.Header{
		Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: int64(len(contents)),
		ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR,
	}, contents: contents}
}

// writeTar builds an archive member by member, so a test can write what
// contentbundle.Encode never would.
func writeTar(t *testing.T, members []tarMember) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, member := range members {
		header := member.header
		if err := writer.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(member.contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func sampleMembers() []tarMember {
	files := sampleFiles()
	members := make([]tarMember, 0, len(files))
	for _, name := range slices.Sorted(maps.Keys(files)) {
		members = append(members, canonicalMember(name, files[name]))
	}
	return members
}

func TestEncodeIsCanonical(t *testing.T) {
	t.Parallel()
	files := sampleFiles()
	artifact, err := contentbundle.Encode(contentbundle.ComponentTypeWorkflows, files)
	if err != nil {
		t.Fatalf("contentbundle.Encode() error = %v", err)
	}
	if again, err := contentbundle.Encode(
		contentbundle.ComponentTypeWorkflows,
		files,
	); err != nil ||
		!bytes.Equal(again, artifact) {
		t.Fatal("contentbundle.Encode() is not deterministic")
	}
	if !bytes.Equal(artifact, writeTar(t, sampleMembers())) {
		t.Fatal("contentbundle.Encode() differs from canonical USTAR members in bytewise order")
	}
	bundle, err := contentbundle.Decode(lockFor("sample-plugin", files, artifact), artifact)
	if err != nil {
		t.Fatalf("contentbundle.Decode(contentbundle.Encode()) error = %v", err)
	}
	for name, contents := range files {
		got, err := fs.ReadFile(bundle.Files, name)
		if err != nil || !bytes.Equal(got, contents) {
			t.Fatalf("decoded %s = %q, %v", name, got, err)
		}
	}
	for name, bad := range map[string]map[string][]byte{
		"no files":       {},
		"stray root":     {"SKILL.md": []byte("x\n")},
		"third file":     {"account-brief/notes.md": []byte("x\n")},
		"dot file":       {".DS_Store": []byte("x")},
		"nested":         {"a/b/playbook.md": []byte("# x\n")},
		"upper case key": {"Account/playbook.md": []byte("# x\n")},
		"CR byte":        {"account-brief/playbook.md": []byte("# x\r\n")},
		"not UTF-8":      {"account-brief/playbook.md": []byte("# x\xff\n")},
		"byte order":     {"account-brief/playbook.md": []byte("\xef\xbb\xbf# x\n")},
		"over the cap":   {"account-brief/playbook.md": bytes.Repeat([]byte("a"), (1<<20)+1)},
	} {
		if _, err := contentbundle.Encode(
			contentbundle.ComponentTypeWorkflows,
			bad,
		); !errors.Is(
			err,
			contentbundle.ErrInvalid,
		) {
			t.Fatalf("contentbundle.Encode(%s) error = %v, want contentbundle.ErrInvalid", name, err)
		}
	}
	if _, err := contentbundle.Encode("skills", files); err == nil {
		t.Fatal("contentbundle.Encode() accepted an unknown component")
	}
}

// contentbundle.CheckFiles reports every problem, each file's with its name, and its first
// problem is the one contentbundle.Encode refuses with.
func TestCheckFilesReportsEveryProblem(t *testing.T) {
	t.Parallel()
	if problems := contentbundle.CheckFiles(
		contentbundle.ComponentTypeWorkflows,
		sampleFiles(),
	); len(
		problems,
	) != 0 {
		t.Fatalf("contentbundle.CheckFiles(sample) = %v", problems)
	}
	files := sampleFiles()
	files[".DS_Store"] = []byte("\x00\xff")
	files["README.md"] = []byte("x\n")
	files["account-brief/playbook.md"] = []byte("\xef\xbb\xbf# Account brief\r\n")
	files["weekly-recap/workflow.yaml"] = []byte("key: \xff\n")
	problems := contentbundle.CheckFiles(contentbundle.ComponentTypeWorkflows, files)
	type located struct{ name, message string }
	want := []located{
		{".DS_Store", "is not <workflow-key>"},
		{"README.md", "is not <workflow-key>"},
		{"account-brief/playbook.md", "byte order mark"},
		{"account-brief/playbook.md", "CR byte"},
		{"weekly-recap/workflow.yaml", "not UTF-8"},
	}
	if len(problems) != len(want) {
		t.Fatalf("contentbundle.CheckFiles() = %v, want %d problems", problems, len(want))
	}
	for index, problem := range problems {
		var fileErr *contentbundle.FileError
		if !errors.As(problem, &fileErr) || fileErr.Name != want[index].name ||
			!strings.Contains(problem.Error(), want[index].message) {
			t.Fatalf("problem %d = %v, want %s: %s", index, problem, want[index].name, want[index].message)
		}
	}
	if _, err := contentbundle.Encode(contentbundle.ComponentTypeWorkflows, files); err == nil ||
		err.Error() != contentbundle.ErrInvalid.Error()+": "+problems[0].Error() {
		t.Fatalf("contentbundle.Encode() error = %v, want the first problem %v", err, problems[0])
	}
	for name, test := range map[string]struct {
		component contentbundle.ComponentType
		files     map[string][]byte
		want      string
	}{
		"empty":             {contentbundle.ComponentTypeWorkflows, map[string][]byte{}, "1 to 2000 files, got 0"},
		"unknown component": {"skills", sampleFiles(), `unknown component "skills"`},
	} {
		problems := contentbundle.CheckFiles(test.component, test.files)
		var fileErr *contentbundle.FileError
		if len(problems) != 1 || errors.As(problems[0], &fileErr) ||
			!strings.Contains(problems[0].Error(), test.want) {
			t.Fatalf(
				"contentbundle.CheckFiles(%s) = %v, want one component-level problem %q",
				name,
				problems,
				test.want,
			)
		}
	}
}

// contentbundle.LoadFS reads a pin directory exactly as Load reads the embedded one.
func TestLoadFSReadsAPinDirectory(t *testing.T) {
	t.Parallel()
	root := pinFS(t, map[string]map[string][]byte{"alpha-plugin": sampleFiles()})
	bundles, err := contentbundle.LoadFS(root)
	if err != nil || len(bundles) != 1 || bundles[0].Lock.Plugin != "alpha-plugin" {
		t.Fatalf("contentbundle.LoadFS() = %d bundles, %v", len(bundles), err)
	}
	root["orphan"+contentbundle.ArtifactSuffix] = &fstest.MapFile{Data: []byte("x")}
	if _, err := contentbundle.LoadFS(root); !errors.Is(err, contentbundle.ErrInvalid) {
		t.Fatalf("contentbundle.LoadFS(orphan) error = %v, want contentbundle.ErrInvalid", err)
	}
}

// The format-1 bytes are pinned, so a Go release that changed archive/tar's
// output could not silently rewrite every committed artifact on the next bump.
func TestFormat1Golden(t *testing.T) {
	t.Parallel()
	artifact, err := contentbundle.Encode(contentbundle.ComponentTypeWorkflows, sampleFiles())
	if err != nil {
		t.Fatal(err)
	}
	golden := "testdata/format1.tar"
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(golden, artifact, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(artifact, want) {
		t.Fatalf("contentbundle.Encode() no longer produces %s; format 1 changed", golden)
	}
}

func TestDecodeRefuses(t *testing.T) {
	t.Parallel()
	files := sampleFiles()
	artifact, err := contentbundle.Encode(contentbundle.ComponentTypeWorkflows, files)
	if err != nil {
		t.Fatal(err)
	}
	valid := lockFor("sample-plugin", files, artifact)
	// consistent pins a malformed archive with a lock that matches it, so the
	// case reaches the check it names instead of stopping at the sha256.
	consistent := func(members []tarMember, mutate func(*contentbundle.Lock)) (contentbundle.Lock, []byte) {
		t.Helper()
		archive := writeTar(t, members)
		lock := valid
		lock.Artifact.SHA256, lock.Artifact.Size = digestHex(archive), int64(len(archive))
		contents := map[string][]byte{}
		for _, member := range members {
			contents[member.header.Name] = member.contents
		}
		lock.TreeHash, lock.FileCount = contentbundle.TreeHash(contents), len(contents)
		if mutate != nil {
			mutate(&lock)
		}
		return lock, archive
	}
	withMember := func(index int, edit func(*tarMember)) []tarMember {
		members := sampleMembers()
		edit(&members[index])
		return members
	}
	tampered := slices.Clone(artifact)
	tampered[600] ^= 1
	crlf := bytes.ReplaceAll(artifact, []byte("\n"), []byte("\r\n"))
	lfsPointer := []byte("version https://git-lfs.github.com/spec/v1\noid sha256:" + valid.Artifact.SHA256 +
		"\nsize 10240\n")

	type testCase struct {
		lock     contentbundle.Lock
		artifact []byte
		want     string
	}
	cases := map[string]testCase{
		"tampered byte":   {valid, tampered, "size or sha256"},
		"CRLF checkout":   {valid, crlf, "size or sha256"},
		"Git LFS pointer": {valid, lfsPointer, "size or sha256"},
		"truncated":       {valid, artifact[:len(artifact)-1024], "size or sha256"},
	}
	add := func(name string, members []tarMember, mutate func(*contentbundle.Lock), want string) {
		lock, archive := consistent(members, mutate)
		cases[name] = testCase{lock, archive, want}
	}
	add("symlink member", withMember(0, func(member *tarMember) {
		member.header.Typeflag, member.header.Linkname, member.header.Size = tar.TypeSymlink, "../x", 0
		member.contents = nil
	}), nil, "not a canonical regular file")
	add("directory member", append([]tarMember{{header: tar.Header{
		Typeflag: tar.TypeDir,
		Name:     "account-brief/",
		Mode:     0o755,
		ModTime:  time.Unix(0, 0),
		Format:   tar.FormatUSTAR,
	}}}, sampleMembers()...), nil, "not a canonical regular file")
	add("PAX member", withMember(0, func(member *tarMember) {
		member.header.Format = tar.FormatPAX
		member.header.PAXRecords = map[string]string{"comment": "x"}
	}), nil, "not a canonical regular file")
	add("GNU member", withMember(0, func(member *tarMember) { member.header.Format = tar.FormatGNU }), nil,
		"not a canonical regular file")
	add("mode 0600", withMember(0, func(member *tarMember) { member.header.Mode = 0o600 }), nil,
		"not a canonical regular file")
	add("owner", withMember(0, func(member *tarMember) { member.header.Uname = "builder" }), nil,
		"not a canonical regular file")
	add(
		"mtime",
		withMember(0, func(member *tarMember) { member.header.ModTime = time.Unix(1_700_000_000, 0) }),
		nil,
		"not a canonical regular file",
	)
	add(
		"stray file",
		append(sampleMembers(), canonicalMember("zz/notes.txt", []byte("x\n"))),
		nil,
		"is not <workflow-key>",
	)
	add("dot file", append([]tarMember{canonicalMember(".DS_Store", []byte("x"))}, sampleMembers()...), nil,
		"is not <workflow-key>")
	add("harness smuggled in", append([]tarMember{canonicalMember("SKILL.md", []byte("x\n"))}, sampleMembers()...),
		nil, "is not <workflow-key>")
	add("duplicate member", append(sampleMembers(), sampleMembers()[3]), nil, "duplicated or out of bytewise order")
	unsorted := sampleMembers()
	unsorted[0], unsorted[1] = unsorted[1], unsorted[0]
	add("unsorted members", unsorted, nil, "duplicated or out of bytewise order")
	add(
		"file count",
		sampleMembers(),
		func(lock *contentbundle.Lock) { lock.FileCount++ },
		"file count or tree hash",
	)
	add("tree hash", sampleMembers(), func(lock *contentbundle.Lock) { lock.TreeHash = strings.Repeat("d", 64) },
		"file count or tree hash")
	add("CR byte", withMember(0, func(member *tarMember) {
		member.contents = []byte("# Account brief\r\n")
		member.header.Size = int64(len(member.contents))
	}), nil, "CR byte")
	add("not UTF-8", withMember(0, func(member *tarMember) {
		member.contents = []byte("# Account \xff\n")
		member.header.Size = int64(len(member.contents))
	}), nil, "not UTF-8")
	add("byte order mark", withMember(0, func(member *tarMember) {
		member.contents = []byte("\xef\xbb\xbf# Account\n")
		member.header.Size = int64(len(member.contents))
	}), nil, "byte order mark")
	add("over the size cap", withMember(0, func(member *tarMember) {
		member.contents = bytes.Repeat([]byte("a"), (1<<20)+1)
		member.header.Size = int64(len(member.contents))
	}), nil, "size or count cap")
	add(
		"retired key present",
		sampleMembers(),
		func(lock *contentbundle.Lock) { lock.RetiredKeys = []string{"weekly-recap"} },
		`retired key "weekly-recap"`,
	)
	// Two extra zero blocks after the end marker read back as the same
	// members; only the re-encode can tell.
	padded := append(slices.Clone(artifact), make([]byte, 1024)...)
	paddedLock := valid
	paddedLock.Artifact.SHA256, paddedLock.Artifact.Size = digestHex(padded), int64(len(padded))
	cases["trailing padding"] = testCase{paddedLock, padded, "not in canonical form"}
	lockCases := map[string]func(*contentbundle.Lock){
		"lock format":          func(lock *contentbundle.Lock) { lock.LockFormat = 2 },
		"component":            func(lock *contentbundle.Lock) { lock.Component = "skills" },
		"artifact file":        func(lock *contentbundle.Lock) { lock.Artifact.File = "other" + contentbundle.ArtifactSuffix },
		"version":              func(lock *contentbundle.Lock) { lock.Version, lock.Tag = "1.2", "sample-plugin--v1.2" },
		"tag":                  func(lock *contentbundle.Lock) { lock.Tag = "sample-plugin/v1.2.3" },
		"commit":               func(lock *contentbundle.Lock) { lock.Commit = "abc" },
		"content tree":         func(lock *contentbundle.Lock) { lock.ContentTreeID = "" },
		"manifest path":        func(lock *contentbundle.Lock) { lock.ManifestPath = "plugins/other/.boardly-plugin/plugin.json" },
		"content path":         func(lock *contentbundle.Lock) { lock.ContentPath = "plugins/sample-plugin/workflows" },
		"unsorted retirements": func(lock *contentbundle.Lock) { lock.RetiredKeys = []string{"b-key", "a-key"} },
		"plugin name":          func(lock *contentbundle.Lock) { lock.Plugin = "Sample--Plugin" },
	}
	// contentbundle.ValidateLock names the lock it refuses (`: lock "<plugin>": `), which
	// neither the sha256 nor the tree-hash message does.
	for name, mutate := range lockCases {
		lock := valid
		mutate(&lock)
		cases["lock "+name] = testCase{lock, artifact, `: lock "`}
	}

	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			bundle, err := contentbundle.Decode(test.lock, test.artifact)
			if !errors.Is(err, contentbundle.ErrInvalid) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf(
					"contentbundle.Decode() = %+v, %v; want contentbundle.ErrInvalid mentioning %q",
					bundle.Lock,
					err,
					test.want,
				)
			}
		})
	}
}

func TestParseLockAcceptsOnlyTheCanonicalEncoding(t *testing.T) {
	t.Parallel()
	files := sampleFiles()
	artifact, err := contentbundle.Encode(contentbundle.ComponentTypeWorkflows, files)
	if err != nil {
		t.Fatal(err)
	}
	lock := lockFor("sample-plugin", files, artifact)
	lock.RetiredKeys = nil
	canonical, err := contentbundle.MarshalLock(lock)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(canonical, []byte(`"retiredKeys": []`)) || !bytes.HasSuffix(canonical, []byte("}\n")) {
		t.Fatalf("contentbundle.MarshalLock() = %s", canonical)
	}
	if parsed, err := contentbundle.ParseLock(canonical); err != nil || parsed.Plugin != "sample-plugin" {
		t.Fatalf("contentbundle.ParseLock(canonical) = %+v, %v", parsed, err)
	}
	for name, raw := range map[string][]byte{
		"no newline":    bytes.TrimSuffix(canonical, []byte("\n")),
		"tab indent":    bytes.ReplaceAll(canonical, []byte("  "), []byte("\t")),
		"unknown field": bytes.Replace(canonical, []byte(`"lockFormat": 1,`), []byte(`"lockFormat": 1, "extra": 1,`), 1),
		"two values":    append(slices.Clone(canonical), canonical...),
		"not JSON":      []byte("lockFormat: 1\n"),
	} {
		if _, err := contentbundle.ParseLock(raw); !errors.Is(err, contentbundle.ErrInvalid) {
			t.Fatalf("contentbundle.ParseLock(%s) error = %v, want contentbundle.ErrInvalid", name, err)
		}
	}
	development := lock
	development.Version, development.Tag, development.ContentTreeID = "0.0.0-dev+0123456789ab.dirty", "", ""
	if !development.Development() || contentbundle.ValidateLock(development) != nil {
		t.Fatalf("a dirty development pin is refused: %v", contentbundle.ValidateLock(development))
	}
	development.Tag = "sample-plugin--v0.0.0"
	if err := contentbundle.ValidateLock(development); err == nil || !strings.Contains(err.Error(), "has no tag") {
		t.Fatalf("a development pin carried a tag: %v", err)
	}
	development.Tag, development.ContentTreeID = "", "abc"
	err = contentbundle.ValidateLock(development)
	if err == nil || !strings.Contains(err.Error(), "contentTreeId must be empty") {
		t.Fatalf("a development pin carried a malformed tree id: %v", err)
	}
}

// pinFS lays tar and lock pairs out the way the embedded pin does.
func pinFS(t *testing.T, plugins map[string]map[string][]byte) fstest.MapFS {
	t.Helper()
	root := fstest.MapFS{}
	for plugin, files := range plugins {
		artifact, err := contentbundle.Encode(contentbundle.ComponentTypeWorkflows, files)
		if err != nil {
			t.Fatal(err)
		}
		lock, err := contentbundle.MarshalLock(lockFor(plugin, files, artifact))
		if err != nil {
			t.Fatal(err)
		}
		root[plugin+contentbundle.ArtifactSuffix] = &fstest.MapFile{Data: artifact}
		root[plugin+contentbundle.LockSuffix] = &fstest.MapFile{Data: lock}
	}
	return root
}

// A component that fails verification is dropped and named; the others are
// still returned, which is what lets zenith-team-mcp keep serving.
func TestLoadDropsOnlyTheFailingBundle(t *testing.T) {
	t.Parallel()
	other := map[string][]byte{
		"vendor-review/playbook.md":   []byte("# Vendor review\n"),
		"vendor-review/workflow.yaml": []byte("key: vendor-review\n"),
	}
	root := pinFS(t, map[string]map[string][]byte{"alpha-plugin": sampleFiles(), "beta-plugin": other})
	tampered := slices.Clone(root["beta-plugin"+contentbundle.ArtifactSuffix].Data)
	tampered[700] ^= 1
	root["beta-plugin"+contentbundle.ArtifactSuffix] = &fstest.MapFile{Data: tampered}
	root["orphan"+contentbundle.ArtifactSuffix] = &fstest.MapFile{Data: []byte("x")}
	bundles, err := contentbundle.LoadFS(root)
	if len(bundles) != 1 || bundles[0].Lock.Plugin != "alpha-plugin" {
		t.Fatalf("contentbundle.LoadFS() kept %d bundles", len(bundles))
	}
	for _, want := range []string{"beta-plugin.lock.json", "orphan.bundle.tar has no lock"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("contentbundle.LoadFS() error = %v, want it to mention %q", err, want)
		}
	}
	// Each failure names its plugin, so bundlectl can set aside the one pin it
	// is replacing and still refuse on any other.
	var failed []string
	for _, failure := range err.(interface{ Unwrap() []error }).Unwrap() {
		var loadErr *contentbundle.LoadError
		if !errors.As(failure, &loadErr) || !errors.Is(failure, contentbundle.ErrInvalid) {
			t.Fatalf(
				"failure %v is not a *contentbundle.LoadError wrapping contentbundle.ErrInvalid",
				failure,
			)
		}
		failed = append(failed, loadErr.Plugin)
	}
	if !slices.Equal(failed, []string{"beta-plugin", "orphan"}) {
		t.Fatalf("failed plugins = %v", failed)
	}
	misnamed := pinFS(t, map[string]map[string][]byte{"alpha-plugin": sampleFiles()})
	misnamed["renamed"+contentbundle.LockSuffix] = misnamed["alpha-plugin"+contentbundle.LockSuffix]
	delete(misnamed, "alpha-plugin"+contentbundle.LockSuffix)
	if bundles, err := contentbundle.LoadFS(misnamed); len(bundles) != 0 || err == nil {
		t.Fatalf("a lock under another plugin's name loaded: %d, %v", len(bundles), err)
	}
}

func TestMergeWorkflowCatalogRefusesAKeyPinnedTwice(t *testing.T) {
	t.Parallel()
	skill := []byte("---\nname: zenith-workflows\n---\n")
	harness := fstest.MapFS{"harness/zenith-workflows/SKILL.md": &fstest.MapFile{Data: skill}}
	decode := func(plugin string, files map[string][]byte) contentbundle.Bundle {
		t.Helper()
		artifact, err := contentbundle.Encode(contentbundle.ComponentTypeWorkflows, files)
		if err != nil {
			t.Fatal(err)
		}
		bundle, err := contentbundle.Decode(lockFor(plugin, files, artifact), artifact)
		if err != nil {
			t.Fatal(err)
		}
		return bundle
	}
	alpha := decode("alpha-plugin", sampleFiles())
	beta := decode("beta-plugin", map[string][]byte{"vendor-review/playbook.md": []byte("# Vendor review\n")})
	catalog, locks, err := mergeWorkflowCatalog(harness, []contentbundle.Bundle{alpha, beta})
	if err != nil || len(locks) != 2 {
		t.Fatalf("mergeWorkflowCatalog() = %d locks, %v", len(locks), err)
	}
	entries, err := fs.ReadDir(catalog, ".")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if want := []string{"SKILL.md", "account-brief", "vendor-review", "weekly-recap"}; !slices.Equal(names, want) {
		t.Fatalf("merged root = %v, want %v", names, want)
	}
	clash := decode("beta-plugin", map[string][]byte{"weekly-recap/playbook.md": []byte("# Also weekly\n")})
	if _, _, err := mergeWorkflowCatalog(
		harness,
		[]contentbundle.Bundle{alpha, clash},
	); !errors.Is(
		err,
		contentbundle.ErrInvalid,
	) ||
		!strings.Contains(err.Error(), `"weekly-recap"`) {
		t.Fatalf("mergeWorkflowCatalog(clash) error = %v", err)
	}
	if _, _, err := mergeWorkflowCatalog(harness, nil); !errors.Is(err, contentbundle.ErrInvalid) {
		t.Fatalf("mergeWorkflowCatalog(nil) error = %v, want contentbundle.ErrInvalid", err)
	}
	// A key one plugin retired is never reused by another.
	retiring := beta
	retiring.Lock.RetiredKeys = []string{"account-brief"}
	_, _, err = mergeWorkflowCatalog(harness, []contentbundle.Bundle{alpha, retiring})
	want := `Workflow "account-brief" is pinned by alpha-plugin but retired by beta-plugin`
	if !errors.Is(err, contentbundle.ErrInvalid) || !strings.Contains(err.Error(), want) {
		t.Fatalf("mergeWorkflowCatalog(retired elsewhere) error = %v, want %q", err, want)
	}
}

func TestMemoryFSIsAReadOnlyFileSystem(t *testing.T) {
	t.Parallel()
	files := sampleFiles()
	files["SKILL.md"] = []byte("---\nname: zenith-workflows\n---\n")
	memory := contentbundle.NewFS(files)
	if err := fstest.TestFS(memory, slices.Sorted(maps.Keys(files))...); err != nil {
		t.Fatal(err)
	}
	contents, err := fs.ReadFile(memory, "SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	contents[0] = 'X'
	if again, _ := fs.ReadFile(memory, "SKILL.md"); again[0] != '-' {
		t.Fatal("ReadFile() handed out the verified bytes themselves")
	}
}
