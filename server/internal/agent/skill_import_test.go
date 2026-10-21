package agent

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/Intelligent-Internet/zenith-team/server/internal/contentbundle"
)

func skillMarkdown(name string) []byte {
	return []byte("---\nname: " + name + "\ndescription: Review the work carefully.\n---\nFollow the procedure.\n")
}

func skillZIP(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, data := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestReadSkillUploadMarkdownAndMultiplePackages(t *testing.T) {
	t.Parallel()
	items, err := readSkillUpload("guide.md", skillMarkdown("review-work"))
	if err != nil || len(items) != 1 || items[0].Bundle.Name != "review-work" {
		t.Fatalf("Markdown = %+v, %v", items, err)
	}
	data := skillZIP(t, map[string][]byte{
		"repo-main/skills/review-work/SKILL.md":         skillMarkdown("review-work"),
		"repo-main/skills/review-work/assets/image.png": {0x89, 0, 0xff},
		"repo-main/skills/write-docs/SKILL.md":          skillMarkdown("write-docs"),
		"repo-main/README.md":                           []byte("repository"),
	})
	items, err = readSkillUpload("skills.zip", data)
	if err != nil || len(items) != 2 || items[0].Bundle.Name != "review-work" || len(items[0].Bundle.Files) != 1 {
		t.Fatalf("ZIP = %+v, %v", items, err)
	}
}

func TestReadSkillUploadRejectsUnsafeOrInvalidPackages(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		files map[string][]byte
	}{
		{"traversal", map[string][]byte{"../SKILL.md": skillMarkdown("review-work")}},
		{"missing frontmatter", map[string][]byte{"review-work/SKILL.md": []byte("# Instructions")}},
		{"plugin manifest", map[string][]byte{"review-work/SKILL.md": skillMarkdown("review-work"), "review-work/.claude-plugin/plugin.json": []byte("{}")}},
		{"missing reference", map[string][]byte{"review-work/SKILL.md": append(skillMarkdown("review-work"), []byte("[Guide](references/missing.md)")...)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := readSkillUpload("skills.zip", skillZIP(t, test.files)); err == nil {
				t.Fatal("accepted invalid package")
			}
		})
	}
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	header := &zip.FileHeader{Name: "review-work/SKILL.md"}
	header.SetMode(fs.ModeSymlink | 0o777)
	entry, _ := writer.CreateHeader(header)
	_, _ = entry.Write(skillMarkdown("review-work"))
	_ = writer.Close()
	if _, err := readSkillUpload("skills.zip", buffer.Bytes()); err == nil {
		t.Fatal("accepted symlink")
	}
}

func TestReadSkillUploadClassifiesValidationProblems(t *testing.T) {
	t.Parallel()
	tooMany := make(map[string][]byte)
	for i := range 33 {
		tooMany[fmt.Sprintf("skill-%d/SKILL.md", i)] = skillMarkdown(fmt.Sprintf("skill-%d", i))
	}
	for _, test := range []struct {
		name     string
		filename string
		data     []byte
		want     error
	}{
		{"missing header", "skill.md", []byte("# Instructions"), ErrSkillFrontmatterInvalid},
		{"unclosed header", "skill.md", []byte("---\nname: review"), ErrSkillFrontmatterInvalid},
		{"unsupported field", "skill.md", []byte("---\nname: review\ndescription: Review.\nsecretField: private\n---\nRead."), ErrSkillFrontmatterInvalid},
		{"duplicate field", "skill.md", []byte("---\nname: one\nname: two\ndescription: Review.\n---\nRead."), ErrSkillFrontmatterInvalid},
		{"missing name", "skill.md", []byte("---\ndescription: Review.\n---\nRead."), ErrSkillNameRequired},
		{"blank name", "skill.md", skillMarkdown("' '"), ErrSkillNameRequired},
		{"missing description", "skill.md", []byte("---\nname: review\n---\nRead."), ErrSkillDescriptionInvalid},
		{"long description", "skill.md", []byte("---\nname: review\ndescription: " + strings.Repeat("x", 1025) + "\n---\nRead."), ErrSkillDescriptionInvalid},
		{"control in description", "skill.md", []byte("---\nname: review\ndescription: \"Review \\a work\"\n---\nRead."), ErrSkillDescriptionInvalid},
		{"long compatibility", "skill.md", []byte("---\nname: review\ndescription: Review.\ncompatibility: " + strings.Repeat("x", 501) + "\n---\nRead."), ErrSkillFrontmatterInvalid},
		{"missing reference", "skill.md", append(skillMarkdown("review"), []byte("[Guide](references/private-file.md)")...), ErrSkillReferenceMissing},
		{"escaping reference", "skill.md", append(skillMarkdown("review"), []byte("[Guide](../private-file.md)")...), ErrSkillReferenceInvalid},
		{"malformed reference", "skill.md", append(skillMarkdown("review"), []byte("[Guide](%zz)")...), ErrSkillReferenceInvalid},
		{"unreadable archive", "skills.zip", []byte("private archive bytes"), ErrSkillArchiveInvalid},
		{"unsafe archive path", "skills.zip", skillZIP(t, map[string][]byte{"../SKILL.md": skillMarkdown("review")}), ErrSkillFilesInvalid},
		{"hidden file", "skills.zip", skillZIP(t, map[string][]byte{"SKILL.md": skillMarkdown("review"), ".private": []byte("secret")}), ErrSkillFilesInvalid},
		{"duplicate path", "skills.zip", skillZIP(t, map[string][]byte{"SKILL.md": skillMarkdown("review"), "GUIDE.md": []byte("one"), "guide.md": []byte("two")}), ErrSkillFilesInvalid},
		{"directory collision", "skills.zip", skillZIP(t, map[string][]byte{"SKILL.md": skillMarkdown("review"), "assets": []byte("one"), "assets/icon.png": []byte("two")}), ErrSkillFilesInvalid},
		{"Unicode path collision", "skills.zip", skillZIP(t, map[string][]byte{"SKILL.md": skillMarkdown("review"), "assets/é.png": []byte("one"), "assets/e\u0301.png": []byte("two")}), ErrSkillFilesInvalid},
		{"oversized instructions", "skill.md", append(skillMarkdown("review"), bytes.Repeat([]byte("x"), 1<<20)...), ErrSkillPackageLimit},
		{"empty instructions", "skill.md", nil, ErrSkillInstructionsInvalid},
		{"binary instructions", "skill.md", append(skillMarkdown("review"), 0), ErrSkillInstructionsInvalid},
		{"invalid UTF-8", "skill.md", []byte{255}, ErrSkillInstructionsInvalid},
		{"no skills", "skills.zip", skillZIP(t, map[string][]byte{"README.md": []byte("private repository")}), ErrSkillCountInvalid},
		{"too many skills", "skills.zip", skillZIP(t, tooMany), ErrSkillCountInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := readSkillUpload(test.filename, test.data)
			if !errors.Is(err, test.want) || !errors.Is(err, ErrSkillInvalid) {
				t.Fatalf("error = %v, want %v and ErrSkillInvalid", err, test.want)
			}
		})
	}
}

func TestReadSkillUploadNormalizesNamesBeforeReview(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		input string
		want  string
	}{
		{"portable name", "market-research", "market-research"},
		{"uppercase and spaces", "Market Research", "market-research"},
		{"underscores and punctuation", "__Market__Research!!", "market-research"},
		{"Vietnamese", "Đánh Giá Công Việc", "danh-gia-cong-viec"},
		{"long name", strings.Repeat("a", 70), strings.Repeat("a", 64)},
		{"custom Zenith prefix", "zenith-research", "zenith-research"},
		{"platform collision", "zenith-platform", "custom-zenith-platform"},
		{"workflow collision", "zenith-workflows", "custom-zenith-workflows"},
		{"Studio collision", "zenith-studio-cli", "custom-zenith-studio-cli"},
		{"native slot collision", "synced", "custom-synced"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			original := skillMarkdown(test.input)
			items, err := readSkillUpload("skill.md", original)
			if err != nil || len(items) != 1 {
				t.Fatalf("preview = %+v, %v", items, err)
			}
			bundle := items[0].Bundle
			if bundle.Name != test.want || items[0].OriginalName != test.input {
				t.Fatalf(
					"name = %q (%q), want %q (%q)",
					bundle.Name,
					items[0].OriginalName,
					test.want,
					test.input,
				)
			}
			front := strings.SplitN(bundle.Content, "---", 3)
			var metadata struct {
				Name string `yaml:"name"`
			}
			if err := yaml.Unmarshal(
				[]byte(front[1]),
				&metadata,
			); err != nil ||
				metadata.Name != test.want {
				t.Fatalf("installed frontmatter = %+v, %v, want name %q", metadata, err, test.want)
			}
			if test.input == test.want && bundle.Content != string(original) {
				t.Fatal("changed an already portable skill")
			}
			if err := contentbundle.ValidateSkill(
				contentbundle.SkillReference(bundle),
				bundle,
			); err != nil {
				t.Fatalf("normalized bundle does not verify: %v", err)
			}
		})
	}
}

func TestReadSkillUploadRenamingPreservesInstructionsAndRelativeFiles(t *testing.T) {
	t.Parallel()
	body := "\r\nRead [guide](references/guide.md).\r\nRun scripts/Market_Research.sh; keep Market Research in the report.\r\n"
	markdown := "---\r\nname: >-\r\n  Market Research\r\ndescription: |\r\n  Review the work.\r\nlicense: MIT\r\nmetadata:\r\n  author: Example\r\n---\r\n" + body
	script := []byte("#!/bin/sh\necho 'Market Research'\n")
	items, err := readSkillUpload("skills.zip", skillZIP(t, map[string][]byte{
		"repository/skills/unrelated-folder/SKILL.md": []byte(markdown),
		"repository/skills/unrelated-folder/references/guide.md": []byte(
			"[script](../scripts/Market_Research.sh)",
		),
		"repository/skills/unrelated-folder/scripts/Market_Research.sh": script,
		"repository/skills/unrelated-folder/assets/image.bin":           {0, 255, 128},
	}))
	if err != nil || len(items) != 1 {
		t.Fatalf("preview = %+v, %v", items, err)
	}
	bundle := items[0].Bundle
	if bundle.Name != "market-research" || !strings.HasSuffix(bundle.Content, "---\r\n"+body) {
		t.Fatalf("renamed instructions = %q", bundle.Content)
	}
	var front struct {
		License  string            `yaml:"license"`
		Metadata map[string]string `yaml:"metadata"`
	}
	if err := yaml.Unmarshal(
		[]byte(strings.SplitN(bundle.Content, "---", 3)[1]),
		&front,
	); err != nil || front.License != "MIT" ||
		front.Metadata["author"] != "Example" {
		t.Fatalf("metadata = %+v, %v", front, err)
	}
	if items[0].Path != "repository/skills/unrelated-folder/SKILL.md" {
		t.Fatalf("source path = %q", items[0].Path)
	}
	for _, file := range bundle.Files {
		if file.Path == "scripts/Market_Research.sh" &&
			!bytes.Equal(contentbundle.SkillFileBytes(file), script) {
			t.Fatal("renamed script contents")
		}
	}
	renamed, err := renameImportedSkill(items[0], "market-research-2")
	if err != nil || renamed.Bundle.Name != "market-research-2" || renamed.Bundle.Hash == bundle.Hash ||
		!strings.HasSuffix(renamed.Bundle.Content, "---\r\n"+body) {
		t.Fatalf("renamed copy = %+v, %v", renamed, err)
	}
}

func TestReadSkillUploadAllocatesUniqueNamesAndKeepsPortableNames(t *testing.T) {
	t.Parallel()
	items, err := readSkillUpload("skills.zip", skillZIP(t, map[string][]byte{
		"a/SKILL.md": skillMarkdown("Market Research"),
		"b/SKILL.md": skillMarkdown("market-research"),
		"c/SKILL.md": skillMarkdown("market-research"),
		"d/SKILL.md": skillMarkdown("market-research-2"),
	}))
	if err != nil || len(items) != 4 {
		t.Fatalf("preview = %+v, %v", items, err)
	}
	for i, want := range []string{"market-research-3", "market-research", "market-research-4", "market-research-2"} {
		if items[i].Bundle.Name != want {
			t.Errorf("skill %d name = %q, want %q", i, items[i].Bundle.Name, want)
		}
	}
}

func TestReadSkillUploadStillRequiresNameAndDescription(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		markdown string
	}{
		{"missing name", "---\ndescription: Review work.\n---\nInstructions."},
		{"empty name", "---\nname: ' '\ndescription: Review work.\n---\nInstructions."},
		{"missing description", "---\nname: Market Research\n---\nInstructions."},
		{"duplicate name keys", "---\nname: one\nname: two\ndescription: Review work.\n---\nInstructions."},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := readSkillUpload("skill.md", []byte(test.markdown)); err == nil {
				t.Fatal("accepted incomplete or ambiguous metadata")
			}
		})
	}
}

func TestReadSkillUploadRepositoryEnvelopeAndPortableFrontmatter(t *testing.T) {
	t.Parallel()
	markdown := []byte(
		"---\r\nname: review-work\r\ndescription: |\r\n  Review the work\r\n  carefully.\r\n---\r\n[Guide](references/guide.md)\r\n",
	)
	items, err := readSkillUpload("private-repo.zip", skillZIP(t, map[string][]byte{
		"private-repo-main/SKILL.md":            markdown,
		"private-repo-main/references/guide.md": []byte("Follow this guide."),
	}))
	if err != nil || len(items) != 1 {
		t.Fatalf("repository envelope = %+v, %v", items, err)
	}
	if items[0].Bundle.Content != string(markdown) || items[0].Bundle.Description != "Review the work carefully." {
		t.Fatalf("frontmatter was changed or not normalized: %+v", items[0].Bundle)
	}
}
