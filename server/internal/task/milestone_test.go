package task

import "testing"

func TestMilestoneCatalogDefinesStableVersionedPlanningBackbone(t *testing.T) {
	t.Parallel()

	if MilestoneCatalogVersion != 3 {
		t.Fatalf("MilestoneCatalogVersion = %d, want 3", MilestoneCatalogVersion)
	}

	wantTypes := []MilestoneType{
		MilestoneTypePositioning,
		MilestoneTypeBrand,
		MilestoneTypePresence,
		MilestoneTypeFinance,
		MilestoneTypeProduct,
		MilestoneTypeLaunch,
		MilestoneTypeGrowth,
		MilestoneTypeSalesOutbound,
		MilestoneTypeOperations,
		MilestoneTypeSupport,
	}
	definitions := MilestoneCatalog()
	if len(definitions) != len(wantTypes) {
		t.Fatalf("MilestoneCatalog() returned %d definitions, want %d", len(definitions), len(wantTypes))
	}

	definitionsByType := make(map[MilestoneType]MilestoneDefinition, len(definitions))
	for index, definition := range definitions {
		if definition.Type != wantTypes[index] {
			t.Errorf("definition %d type = %q, want %q", index, definition.Type, wantTypes[index])
		}
		if definition.DisplayOrder != index+1 {
			t.Errorf(
				"definition %q display order = %d, want %d",
				definition.Type,
				definition.DisplayOrder,
				index+1,
			)
		}
		if definition.Type == "" || definition.Name == "" || definition.Description == "" {
			t.Errorf("definition %d has an empty required field: %+v", index, definition)
		}
		if !validMilestoneStage(definition.Stage) {
			t.Errorf("definition %q has invalid stage %q", definition.Type, definition.Stage)
		}
		if _, duplicate := definitionsByType[definition.Type]; duplicate {
			t.Errorf("duplicate milestone type %q", definition.Type)
		}
		definitionsByType[definition.Type] = definition
	}

	for _, definition := range definitions {
		assertPrerequisiteSet(t, definitionsByType, definition, definition.HardPrerequisites, "hard")
		assertPrerequisiteSet(t, definitionsByType, definition, definition.SoftPrerequisites, "soft")

		// A milestone that is both required and merely advisable is a
		// contradiction the planner cannot act on.
		hard := make(map[MilestoneType]struct{}, len(definition.HardPrerequisites))
		for _, prerequisite := range definition.HardPrerequisites {
			hard[prerequisite] = struct{}{}
		}
		for _, prerequisite := range definition.SoftPrerequisites {
			if _, both := hard[prerequisite]; both {
				t.Errorf(
					"definition %q lists %q as both a hard and a soft prerequisite",
					definition.Type,
					prerequisite,
				)
			}
		}
	}

	assertAcyclicMilestoneCatalog(t, definitionsByType)
}

func TestMilestoneCatalogReturnsIndependentCopies(t *testing.T) {
	t.Parallel()

	first := MilestoneCatalog()
	first[0].Name = "mutated"
	first[1].HardPrerequisites[0] = MilestoneTypeSupport
	first[2].SoftPrerequisites[0] = MilestoneTypeSupport

	second := MilestoneCatalog()
	if second[0].Name == "mutated" {
		t.Error("MilestoneCatalog() exposes mutable definition state")
	}
	if second[1].HardPrerequisites[0] == MilestoneTypeSupport {
		t.Error("MilestoneCatalog() exposes mutable hard prerequisite state")
	}
	if second[2].SoftPrerequisites[0] == MilestoneTypeSupport {
		t.Error("MilestoneCatalog() exposes mutable soft prerequisite state")
	}
}

func TestValidMilestoneType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		milestoneType MilestoneType
		want          bool
	}{
		{name: "catalog value", milestoneType: MilestoneTypeGrowth, want: true},
		{name: "catalog presence", milestoneType: MilestoneTypePresence, want: true},
		{name: "catalog finance", milestoneType: MilestoneTypeFinance, want: true},
		{name: "retired website", milestoneType: "website", want: false},
		{name: "retired payments", milestoneType: "payments", want: false},
		{name: "retired social content", milestoneType: "social_content", want: false},
		{name: "retired SEO", milestoneType: "seo", want: false},
		{name: "retired unsure help topic", milestoneType: "unsure", want: false},
		{name: "empty", milestoneType: "", want: false},
		{name: "unknown", milestoneType: "unknown", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := ValidMilestoneType(test.milestoneType); got != test.want {
				t.Errorf("ValidMilestoneType(%q) = %t, want %t", test.milestoneType, got, test.want)
			}
		})
	}
}

func assertPrerequisiteSet(
	t *testing.T,
	definitions map[MilestoneType]MilestoneDefinition,
	definition MilestoneDefinition,
	prerequisites []MilestoneType,
	kind string,
) {
	t.Helper()

	seen := make(map[MilestoneType]struct{}, len(prerequisites))
	for _, prerequisite := range prerequisites {
		if prerequisite == definition.Type {
			t.Errorf("definition %q lists itself as a %s prerequisite", definition.Type, kind)
		}
		if _, exists := definitions[prerequisite]; !exists {
			t.Errorf(
				"definition %q has unknown %s prerequisite %q",
				definition.Type,
				kind,
				prerequisite,
			)
		}
		if _, duplicate := seen[prerequisite]; duplicate {
			t.Errorf("definition %q repeats %s prerequisite %q", definition.Type, kind, prerequisite)
		}
		seen[prerequisite] = struct{}{}
	}
}

func validMilestoneStage(stage MilestoneStageType) bool {
	switch stage {
	case MilestoneStageTypeFoundation,
		MilestoneStageTypeBuild,
		MilestoneStageTypeLaunch,
		MilestoneStageTypeGrow:
		return true
	default:
		return false
	}
}

// assertAcyclicMilestoneCatalog walks hard and soft prerequisites together: a
// cycle through either kind would leave the planner with no admissible order.
func assertAcyclicMilestoneCatalog(
	t *testing.T,
	definitions map[MilestoneType]MilestoneDefinition,
) {
	t.Helper()

	type visitState uint8
	const (
		visitStateUnseen visitState = iota
		visitStateActive
		visitStateComplete
	)

	states := make(map[MilestoneType]visitState, len(definitions))
	var visit func(MilestoneType)
	visit = func(milestoneType MilestoneType) {
		switch states[milestoneType] {
		case visitStateActive:
			t.Errorf("milestone catalog contains a prerequisite cycle at %q", milestoneType)
			return
		case visitStateComplete:
			return
		}

		states[milestoneType] = visitStateActive
		definition := definitions[milestoneType]
		for _, prerequisite := range definition.HardPrerequisites {
			visit(prerequisite)
		}
		for _, prerequisite := range definition.SoftPrerequisites {
			visit(prerequisite)
		}
		states[milestoneType] = visitStateComplete
	}

	for milestoneType := range definitions {
		visit(milestoneType)
	}
}
