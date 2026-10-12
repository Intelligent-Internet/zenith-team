package contentbundle

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestSkillPackagePreservesBinaryAssetsAndRejectsTampering(t *testing.T) {
	t.Parallel()
	binary := []byte{0x89, 'P', 'N', 'G', 0, 0xff}
	bundle, err := BuildSkill("visual-guide", "Use for visual work.", map[string][]byte{
		"SKILL.md": []byte(
			"---\nname: visual-guide\ndescription: Use for visual work.\n---\nRead assets/example.png.",
		),
		"assets/example.png": binary,
	})
	if err != nil {
		t.Fatal(err)
	}
	bundle.ID, bundle.Source = "version-1", "company"
	ref := SkillReference(bundle)
	if err := ValidateSkill(ref, bundle); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(SkillFileBytes(bundle.Files[0]), binary) {
		t.Fatal("binary asset changed")
	}
	bundle.Files[0].Data[0]++
	if err := ValidateSkill(ref, bundle); err == nil {
		t.Fatal("accepted tampered asset")
	}
}

func TestSkillPackageRejectsChangedFrozenIdentity(t *testing.T) {
	t.Parallel()
	bundle, err := BuildSkill("review", "Review work.", map[string][]byte{"SKILL.md": []byte("instructions")})
	if err != nil {
		t.Fatal(err)
	}
	bundle.ID, bundle.Source = "version-1", "company"
	ref := SkillReference(bundle)
	bundle.Content = "replacement"
	if err := ValidateSkill(ref, bundle); err == nil {
		t.Fatal("accepted content with a stale digest")
	}
	ref.Hash = "wrong"
	if err := ValidateSkill(ref, bundle); err == nil {
		t.Fatal("accepted different frozen digest")
	}
}

func TestSkillPackageRejectsPathsThatCannotMaterialize(t *testing.T) {
	t.Parallel()
	for _, paths := range [][]string{
		{"assets/icon.png", "assets/ICON.png"},
		{"assets", "assets/icon.png"},
		{"SKILL.md/reference.md"},
		{"assets/CON.txt"},
		{"assets/image.png."},
		{"assets/question?.png"},
		{"assets/é.png", "assets/e\u0301.png"},
		{strings.Repeat("a", 256)},
	} {
		t.Run(paths[0], func(t *testing.T) {
			t.Parallel()
			files := map[string][]byte{"SKILL.md": []byte("instructions")}
			for _, name := range paths {
				files[name] = []byte("asset")
			}
			if _, err := BuildSkill("review", "Review work.", files); err == nil {
				t.Fatal("accepted paths that collide or cannot be materialized portably")
			}
		})
	}
}

func TestSkillPackageRejectsBinaryInstructions(t *testing.T) {
	t.Parallel()
	if _, err := BuildSkill(
		"review",
		"Review work.",
		map[string][]byte{"SKILL.md": []byte("instructions\x00binary")},
	); err == nil {
		t.Fatal("accepted instructions that the text reader cannot load")
	}
}

func TestSkillPackageClassifiesInvalidAuthorContent(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		description string
		files       map[string][]byte
		want        error
	}{
		{"description length", strings.Repeat("x", 1025), map[string][]byte{"SKILL.md": []byte("Read.")}, ErrSkillDescriptionInvalid},
		{"description control", "Read\x07work", map[string][]byte{"SKILL.md": []byte("Read.")}, ErrSkillDescriptionInvalid},
		{"instruction bytes", "Read.", map[string][]byte{"SKILL.md": []byte("Read.\x00")}, ErrSkillInstructionsInvalid},
		{"instruction encoding", "Read.", map[string][]byte{"SKILL.md": {255}}, ErrSkillInstructionsInvalid},
		{"instruction size", "Read.", map[string][]byte{"SKILL.md": bytes.Repeat([]byte("x"), (1<<20)+1)}, ErrSkillPackageLimit},
		{"file collision", "Read.", map[string][]byte{"SKILL.md": []byte("Read."), "asset": []byte("one"), "asset/file": []byte("two")}, ErrSkillFilesInvalid},
		{"unsafe file", "Read.", map[string][]byte{"SKILL.md": []byte("Read."), "../secret": []byte("secret")}, ErrSkillFilesInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := BuildSkill("review", test.description, test.files)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestSkillPackageRejectsNamesReservedByFilesystems(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"con", "prn", "lpt1", "com1"} {
		if _, err := BuildSkill(
			name,
			"Review work.",
			map[string][]byte{"SKILL.md": []byte("instructions")},
		); err == nil {
			t.Fatalf("accepted nonportable skill directory %q", name)
		}
	}
}
