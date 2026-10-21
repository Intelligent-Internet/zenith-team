package company

import (
	"strings"
	"testing"

	taskdomain "github.com/Intelligent-Internet/zenith-team/server/internal/task"
)

func TestNormalizeMilestoneAttestationsRejectsValuesOutsideTheCatalogue(t *testing.T) {
	t.Parallel()

	for _, milestones := range [][]taskdomain.MilestoneType{
		nil,
		{},
		{""},
		// The retired help-topic vocabulary and an older catalogue's values
		// must not quietly mark the wrong outcome done.
		{"unsure"},
		{"sales_growth"},
		{"website"},
		{taskdomain.MilestoneTypeLaunch, taskdomain.MilestoneTypeLaunch},
	} {
		if _, _, err := normalizeMilestoneAttestations(milestones, ""); err == nil {
			t.Errorf("normalizeMilestoneAttestations(%v) was accepted", milestones)
		}
	}
}

func TestNormalizeMilestoneAttestationsKeepsTheFoundersWords(t *testing.T) {
	t.Parallel()

	milestones, note, err := normalizeMilestoneAttestations(
		[]taskdomain.MilestoneType{
			taskdomain.MilestoneTypeProduct,
			taskdomain.MilestoneTypeLaunch,
		},
		"  Selling since 2021.  ",
	)
	if err != nil {
		t.Fatalf("normalizeMilestoneAttestations() error = %v", err)
	}
	if len(milestones) != 2 {
		t.Fatalf("milestones = %v, want both preserved", milestones)
	}
	if note != "Selling since 2021." {
		t.Errorf("note = %q, want it trimmed and otherwise verbatim", note)
	}
}

func TestNormalizeMilestoneAttestationsBoundsTheNote(t *testing.T) {
	t.Parallel()

	if _, _, err := normalizeMilestoneAttestations(
		[]taskdomain.MilestoneType{taskdomain.MilestoneTypeFinance},
		strings.Repeat("a", maxMilestoneAttestationNote+1),
	); err == nil {
		t.Error("an unbounded note was accepted")
	}
}
