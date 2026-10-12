package task

// MilestoneTypeChange distinguishes an omitted update field from a replacement value.
type MilestoneTypeChange struct {
	Present bool
	Value   MilestoneType
}
