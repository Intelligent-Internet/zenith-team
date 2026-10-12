// The topics this domain publishes: its event catalog, and nothing else.
//
// The projections they carry live with the rest of the domain's types. Who
// may see them lives in cmd/server/realtime_routing.go — a comment here says
// what fact an event records, never who receives it, because a rule written
// in two places is a rule that drifts.

package approval

// Topic is the approval family: the Approval as it is requested, decided —
// approved, rejected or cancelled, with whatever its action executed — or
// expired. Every frame on it carries an Approval.
const Topic = "approval"

// The approval.* wire names. Requested opens one, resolved records the
// decision, and expired is the lifecycle worker closing one nobody handled
// before its deadline.
const (
	EventApprovalRequested = "approval.requested"
	EventApprovalResolved  = "approval.resolved"
	EventApprovalExpired   = "approval.expired"
)

// TopicQuestion is the question family: the Question as it is asked, answered
// or withdrawn, or expired. Same domain, its own prefix, because it is a
// different fact with a different shape — every frame on it carries a
// Question.
const TopicQuestion = "question"

// The question.* wire names, mirroring the approval ones: asked opens one,
// resolved records the answer or the withdrawal, expired closes one nobody
// handled in time.
const (
	EventQuestionAsked    = "question.asked"
	EventQuestionResolved = "question.resolved"
	EventQuestionExpired  = "question.expired"
)
