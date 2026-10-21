// Package milestone owns the versioned Task milestone planning catalog shared
// by the cloud Task domain and the machine-local daemon prompt builder.
//
// It nests under internal/task for domain ownership but imports nothing from
// its parent. This leaf exists so the daemon can consume the catalog without
// linking the Task service or its PostgreSQL dependencies.
//
// Import it as taskmilestone.
package milestone

// CatalogVersion identifies the planning policy represented by the current
// milestone catalog. Persisted milestone values must not be reinterpreted
// without incrementing this version.
//
// Version 3 split prerequisites into hard and soft, retired the unused
// per-milestone capability keys, and added the presence and finance outcomes so
// a company that is already operating has somewhere to stand.
const CatalogVersion = 3

// Type identifies a stable company-building outcome that can guide Task
// generation.
type Type string

const (
	TypePositioning   Type = "positioning"
	TypeBrand         Type = "brand"
	TypePresence      Type = "presence"
	TypeFinance       Type = "finance"
	TypeProduct       Type = "product"
	TypeLaunch        Type = "launch"
	TypeGrowth        Type = "growth"
	TypeSalesOutbound Type = "sales_outbound"
	TypeOperations    Type = "operations"
	TypeSupport       Type = "support"
)

// StageType groups milestones into a broad company-building horizon. Stages
// organize recommendations; they do not lock every company into the same
// sequence.
type StageType string

const (
	StageTypeFoundation StageType = "foundation"
	StageTypeBuild      StageType = "build"
	StageTypeLaunch     StageType = "launch"
	StageTypeGrow       StageType = "grow"
)

// Definition describes one planning outcome.
//
// HardPrerequisites name the outcomes whose absence makes this milestone's work
// fail; SoftPrerequisites name the ones that merely make it better. Both seed
// plan generation only — persisted Task dependencies remain execution
// authority, and a founder attestation can satisfy either kind.
//
// Description carries the boundary against neighboring milestones. With ten
// outcomes the overlaps are real, so each description states what the milestone
// is *not*; without that the planner's choice of milestone_type is arbitrary.
type Definition struct {
	Type              Type
	Name              string
	Stage             StageType
	Description       string
	HardPrerequisites []Type
	SoftPrerequisites []Type
	DisplayOrder      int
}

var catalog = []Definition{
	{
		Type:  TypePositioning,
		Name:  "Positioning",
		Stage: StageTypeFoundation,
		Description: "Decide who the company serves, the problem it owns, and the message customers " +
			"should remember, and test that decision against real prospects or customers rather than " +
			"settling it on paper alone. Not brand expression and not campaign copy.",
		DisplayOrder: 1,
	},
	{
		Type:  TypeBrand,
		Name:  "Brand kit",
		Stage: StageTypeFoundation,
		Description: "Create the reusable visual identity and brand guidance that keeps customer-facing " +
			"work consistent. Not the positioning argument itself, and not the public site, which is presence.",
		HardPrerequisites: []Type{TypePositioning},
		DisplayOrder:      2,
	},
	{
		Type:  TypePresence,
		Name:  "Company presence",
		Stage: StageTypeFoundation,
		Description: "Stand up the company's minimum public existence so it can be found and replied to: " +
			"a reachable site, a company email address, and the basic listings customers look for. Not the " +
			"product being sold, and not launch messaging.",
		SoftPrerequisites: []Type{TypeBrand},
		DisplayOrder:      3,
	},
	{
		Type:  TypeFinance,
		Name:  "Finance and legal",
		Stage: StageTypeFoundation,
		Description: "Put the legal and money shell in place: incorporation, banking, bookkeeping, and the " +
			"records the company must keep. Not pricing, billing implementation, or revenue operations, which " +
			"belong to product. Most of this work is the founder's own.",
		DisplayOrder: 4,
	},
	{
		Type:  TypeProduct,
		Name:  "Product",
		Stage: StageTypeBuild,
		Description: "Build and validate the customer-facing product or service, including the way customers " +
			"pay for it. Not the company's own marketing site, which is presence.",
		HardPrerequisites: []Type{TypePositioning},
		SoftPrerequisites: []Type{TypeBrand},
		DisplayOrder:      5,
	},
	{
		Type:  TypeLaunch,
		Name:  "Launch",
		Stage: StageTypeLaunch,
		Description: "Ship a coordinated release: a ready customer surface, the launch message, verification " +
			"that it works, and a measurable path for the response. Not ongoing demand generation, which is growth.",
		HardPrerequisites: []Type{TypeProduct},
		SoftPrerequisites: []Type{TypePresence, TypeBrand},
		DisplayOrder:      6,
	},
	{
		Type:  TypeGrowth,
		Name:  "Growth",
		Stage: StageTypeGrow,
		Description: "Grow demand and conversion through content, community, search, and campaigns, and measure " +
			"what acquires customers. Not one-to-one prospecting, which is sales outbound.",
		HardPrerequisites: []Type{TypeLaunch},
		SoftPrerequisites: []Type{TypePresence},
		DisplayOrder:      7,
	},
	{
		Type:  TypeSalesOutbound,
		Name:  "Sales outbound",
		Stage: StageTypeGrow,
		Description: "Run a focused prospecting and outreach motion with a durable follow-up process. Not " +
			"broad-audience marketing, which is growth.",
		HardPrerequisites: []Type{TypePositioning},
		SoftPrerequisites: []Type{TypePresence},
		DisplayOrder:      8,
	},
	{
		Type:  TypeOperations,
		Name:  "Operations",
		Stage: StageTypeFoundation,
		Description: "Establish the repeatable operating workflow the business runs on: ownership, capacity, " +
			"fulfillment, and internal reporting. Not incorporation or bookkeeping, which belong to finance.",
		SoftPrerequisites: []Type{TypeFinance},
		DisplayOrder:      9,
	},
	{
		Type:  TypeSupport,
		Name:  "Customer support",
		Stage: StageTypeGrow,
		Description: "Create a dependable customer onboarding, support, and retention workflow informed by " +
			"customer feedback. Not the internal operating workflow, which is operations.",
		HardPrerequisites: []Type{TypeLaunch},
		SoftPrerequisites: []Type{TypeOperations},
		DisplayOrder:      10,
	},
}

// Catalog returns the ordered definitions for the current catalog version.
// The returned definitions and their slices are independent copies.
func Catalog() []Definition {
	definitions := make([]Definition, len(catalog))
	for index, definition := range catalog {
		definition.HardPrerequisites = append([]Type(nil), definition.HardPrerequisites...)
		definition.SoftPrerequisites = append([]Type(nil), definition.SoftPrerequisites...)
		definitions[index] = definition
	}
	return definitions
}

// ValidType reports whether milestoneType belongs to the current catalog
// version.
func ValidType(milestoneType Type) bool {
	for _, definition := range catalog {
		if definition.Type == milestoneType {
			return true
		}
	}
	return false
}
