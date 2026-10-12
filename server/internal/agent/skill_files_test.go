package agent

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"

	"boardly.dev/boardly/server/internal/contentbundle"
)

func TestSkillFileEditsPreserveIdentityAndValidateTheWholePackage(t *testing.T) {
	t.Parallel()
	files := map[string][]byte{
		"SKILL.md":            append(skillMarkdown("review-work"), []byte("\n[Guide](references/guide.md)\n")...),
		"references/guide.md": []byte("Original guide"),
		"scripts/check.py":    []byte("print('original')\n"),
		"assets/icon.bin":     {0, 1, 255},
	}
	bundle, err := contentbundle.BuildSkill("review-work", "Review work.", files)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		input SkillEditInput
		want  error
	}{
		{"edit text and delete binary", SkillEditInput{Changes: []SkillFileChange{{Path: "references/guide.md", Content: "New guide"}}, DeletePaths: []string{"assets/icon.bin"}}, nil},
		{"empty supporting file", SkillEditInput{Changes: []SkillFileChange{{Path: "scripts/check.py", Content: ""}}}, nil},
		{"no changes", SkillEditInput{}, ErrSkillInvalid},
		{"delete instructions", SkillEditInput{DeletePaths: []string{"SKILL.md"}}, ErrSkillInvalid},
		{"missing file", SkillEditInput{DeletePaths: []string{"absent.md"}}, ErrSkillNotFound},
		{"traversal", SkillEditInput{DeletePaths: []string{"../SKILL.md"}}, ErrSkillInvalid},
		{"duplicate path", SkillEditInput{DeletePaths: []string{"scripts/check.py", "scripts/check.py"}}, ErrSkillInvalid},
		{"mixed operations", SkillEditInput{Changes: []SkillFileChange{{Path: "scripts/check.py", Content: "new"}}, DeletePaths: []string{"scripts/check.py"}}, ErrSkillInvalid},
		{"edit binary", SkillEditInput{Changes: []SkillFileChange{{Path: "assets/icon.bin", Content: "text"}}}, ErrSkillInvalid},
		{"binary text input", SkillEditInput{Changes: []SkillFileChange{{Path: "scripts/check.py", Content: "a\x00b"}}}, ErrSkillInvalid},
		{"invalid UTF-8", SkillEditInput{Changes: []SkillFileChange{{Path: "scripts/check.py", Content: string([]byte{255})}}}, ErrSkillInvalid},
		{"oversized supporting file", SkillEditInput{Changes: []SkillFileChange{{Path: "scripts/check.py", Content: strings.Repeat("x", (16<<20)+1)}}}, ErrSkillInvalid},
		{"oversized instructions", SkillEditInput{Changes: []SkillFileChange{{Path: "SKILL.md", Content: string(skillMarkdown("review-work")) + strings.Repeat("x", 1<<20)}}}, ErrSkillInvalid},
		{"referenced file", SkillEditInput{DeletePaths: []string{"references/guide.md"}}, ErrSkillInvalid},
		{"rename to existing skill", SkillEditInput{Changes: []SkillFileChange{{Path: "SKILL.md", Content: string(skillMarkdown("another-skill"))}}}, ErrSkillInvalid},
		{"rename to platform skill", SkillEditInput{Changes: []SkillFileChange{{Path: "SKILL.md", Content: string(skillMarkdown("zenith-platform"))}}}, ErrSkillInvalid},
		{"malformed instructions", SkillEditInput{Changes: []SkillFileChange{{Path: "SKILL.md", Content: "No frontmatter"}}}, ErrSkillInvalid},
		{"remove reference and its file together", SkillEditInput{Changes: []SkillFileChange{{Path: "SKILL.md", Content: string(skillMarkdown("review-work"))}}, DeletePaths: []string{"references/guide.md"}}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := editSkillBundle(bundle, test.input)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if err != nil {
				return
			}
			if got.Name != bundle.Name || got.Hash == bundle.Hash {
				t.Fatalf("edited bundle = %+v", got)
			}
			for _, change := range test.input.Changes {
				found := false
				for _, file := range contentbundle.SkillFiles(got) {
					if file.Path == change.Path {
						found = bytes.Equal(contentbundle.SkillFileBytes(file), []byte(change.Content))
					}
				}
				if !found {
					t.Fatalf("changed bytes missing for %q", change.Path)
				}
			}
			for _, deleted := range test.input.DeletePaths {
				for _, file := range contentbundle.SkillFiles(got) {
					if file.Path == deleted {
						t.Fatalf("deleted file %q remains", deleted)
					}
				}
			}
			for _, original := range contentbundle.SkillFiles(bundle) {
				changed := slices.ContainsFunc(test.input.Changes, func(change SkillFileChange) bool { return change.Path == original.Path })
				if changed || slices.Contains(test.input.DeletePaths, original.Path) {
					continue
				}
				found := slices.ContainsFunc(contentbundle.SkillFiles(got), func(file contentbundle.SkillFile) bool {
					return file.Path == original.Path && bytes.Equal(contentbundle.SkillFileBytes(file), contentbundle.SkillFileBytes(original))
				})
				if !found {
					t.Fatalf("untouched bytes changed for %q", original.Path)
				}
			}
			if bundle.Content != string(files["SKILL.md"]) {
				t.Fatal("original instructions mutated")
			}
		})
	}
}
