package agent

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestSkillInstallRequiresUnambiguousReviewedReplacementTargets(t *testing.T) {
	t.Parallel()
	versionID, otherID, skillID := uuid.New(), uuid.New(), uuid.New()
	valid := SkillReplacementInput{VersionID: versionID, SkillID: skillID, ExpectedRevision: 2}
	for _, test := range []struct {
		name  string
		input SkillInstallInput
		want  bool
	}{
		{"add only", SkillInstallInput{}, true},
		{"explicit update", SkillInstallInput{Replacements: []SkillReplacementInput{valid}}, true},
		{"single target update", SkillInstallInput{ReplaceSkillID: skillID, ExpectedRevision: 2}, true},
		{"unselected version", SkillInstallInput{Replacements: []SkillReplacementInput{{VersionID: otherID, SkillID: skillID, ExpectedRevision: 2}}}, false},
		{"missing target", SkillInstallInput{Replacements: []SkillReplacementInput{{VersionID: versionID, ExpectedRevision: 2}}}, false},
		{"missing revision", SkillInstallInput{Replacements: []SkillReplacementInput{{VersionID: versionID, SkillID: skillID}}}, false},
		{"duplicate update", SkillInstallInput{Replacements: []SkillReplacementInput{valid, valid}}, false},
		{"mixed update forms", SkillInstallInput{ReplaceSkillID: skillID, ExpectedRevision: 2, Replacements: []SkillReplacementInput{valid}}, false},
		{"orphan revision", SkillInstallInput{ExpectedRevision: 2}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := skillInstallReplacements(test.input, []uuid.UUID{versionID})
			if test.want && err != nil || !test.want && !errors.Is(err, ErrSkillInvalid) {
				t.Fatalf("replacements = %+v, %v, want valid %v", got, err, test.want)
			}
		})
	}
}
