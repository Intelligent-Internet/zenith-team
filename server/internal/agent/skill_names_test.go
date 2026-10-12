package agent

import (
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/contentbundle"
)

func TestNormalizedSkillNamesRemainPortableForNonLatinSources(t *testing.T) {
	t.Parallel()
	for _, original := range []string{"市場調査", "!!!", "é", strings.Repeat("a", 63) + " -tail"} {
		t.Run(original, func(t *testing.T) {
			t.Parallel()
			items, err := readSkillUpload("skill.md", skillMarkdown(strconv.Quote(original)))
			if err != nil || len(items) != 1 {
				t.Fatalf("preview = %+v, %v", items, err)
			}
			bundle := items[0].Bundle
			if bundle.Name != normalizeImportedSkillName(original) || len(bundle.Name) > 64 {
				t.Fatalf("name = %q", bundle.Name)
			}
			if err := contentbundle.ValidateSkill(contentbundle.SkillReference(bundle), bundle); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSkillCopyNamesAvoidInstalledAndIncomingNames(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a", 64)
	for _, test := range []struct {
		name     string
		source   string
		occupied string
		incoming string
		want     string
	}{
		{"incoming suffix", "review-work", "review-work-2", "review-work-3", "review-work-4"},
		{"bounded suffix", long, long[:62] + "-2", long[:62] + "-3", long[:62] + "-4"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			items, err := readSkillUpload("skills.zip", skillZIP(t, map[string][]byte{"a/SKILL.md": skillMarkdown(test.source), "b/SKILL.md": skillMarkdown(test.incoming)}))
			if err != nil {
				t.Fatal(err)
			}
			id := uuid.New()
			drafts, err := prepareSkillPreviewNames(items, []SkillView{{ID: id, Name: test.source, Revision: 7}, {ID: uuid.New(), Name: test.occupied}})
			if err != nil || drafts[0].Copy == nil || drafts[0].Copy.Bundle.Name != test.want || drafts[0].Existing.ID != id || drafts[0].Existing.Revision != 7 {
				t.Fatalf("choices = %+v, %v", drafts, err)
			}
			if drafts[1].Copy != nil {
				t.Fatal("offered a copy for an uninstalled name")
			}
		})
	}
}
