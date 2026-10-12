package task

import taskmilestone "boardly.dev/boardly/server/internal/task/milestone"

// MilestoneCatalogVersion identifies the current planning catalog version.
const MilestoneCatalogVersion = taskmilestone.CatalogVersion

// MilestoneType identifies a stable company-building outcome.
type MilestoneType = taskmilestone.Type

const (
	MilestoneTypePositioning   = taskmilestone.TypePositioning
	MilestoneTypeBrand         = taskmilestone.TypeBrand
	MilestoneTypePresence      = taskmilestone.TypePresence
	MilestoneTypeFinance       = taskmilestone.TypeFinance
	MilestoneTypeProduct       = taskmilestone.TypeProduct
	MilestoneTypeLaunch        = taskmilestone.TypeLaunch
	MilestoneTypeGrowth        = taskmilestone.TypeGrowth
	MilestoneTypeSalesOutbound = taskmilestone.TypeSalesOutbound
	MilestoneTypeOperations    = taskmilestone.TypeOperations
	MilestoneTypeSupport       = taskmilestone.TypeSupport
)

// MilestoneStageType groups milestones into a company-building horizon.
type MilestoneStageType = taskmilestone.StageType

const (
	MilestoneStageTypeFoundation = taskmilestone.StageTypeFoundation
	MilestoneStageTypeBuild      = taskmilestone.StageTypeBuild
	MilestoneStageTypeLaunch     = taskmilestone.StageTypeLaunch
	MilestoneStageTypeGrow       = taskmilestone.StageTypeGrow
)

// MilestoneDefinition describes one planning outcome.
type MilestoneDefinition = taskmilestone.Definition

// MilestoneCatalog returns independent copies of the ordered definitions.
func MilestoneCatalog() []MilestoneDefinition {
	return taskmilestone.Catalog()
}

// ValidMilestoneType reports whether milestoneType belongs to the catalog.
func ValidMilestoneType(milestoneType MilestoneType) bool {
	return taskmilestone.ValidType(milestoneType)
}
