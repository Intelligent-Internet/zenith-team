package task

import (
	"errors"
)

var (
	ErrResultSelectionRequired   = errors.New("Task result selection is required")
	ErrResultApplicationRequired = errors.New("Task result suggestions require application")
	ErrProjectArchived           = errors.New("Task Project is archived")
	ErrUnauthenticated           = errors.New("user is not authenticated")
	ErrInvalidInput              = errors.New("invalid Task input")
	ErrConflict                  = errors.New("Task mutation conflict")
	ErrNotFound                  = errors.New("Task or Company not found")
	ErrDependencyBlocked         = errors.New("Task execution is blocked by an incomplete dependency")
	ErrAgentBusy                 = errors.New("Task report recipient Agent is busy")
	ErrNoPendingReports          = errors.New("no pending Task reports")
	// ErrAssigneeRequired refuses a Task Run start by anyone but the Task's
	// assignee: the Run executes on the assignee's own computer (ADR 0014).
	ErrAssigneeRequired = errors.New("only the Task's assignee can start it")
	// ErrAssignPermissionRequired refuses a create that assigns the Task to
	// another member by a caller without tasks.assign.
	ErrAssignPermissionRequired = errors.New("assigning a Task to another member needs tasks.assign")
	// ErrHandoffPermissionRequired refuses a handoff by someone who is
	// neither a tasks.assign holder nor the current assignee. It is its own
	// sentinel because the refusal tells a different person what they may do.
	ErrHandoffPermissionRequired = errors.New("handing off a Task needs tasks.assign or its assignee")
	// ErrReviewPermissionRequired refuses moving a Task out of review by
	// someone who is neither its assignee nor a tasks.assign holder. The
	// review decides the work on the assignee's computer, so it is theirs,
	// as a handoff is.
	ErrReviewPermissionRequired = errors.New("moving a Task out of review needs tasks.assign or its assignee")
	// ErrAssigneeInactive refuses an assignment to a member who is not an
	// active member of the Company.
	ErrAssigneeInactive = errors.New("the Task assignee is not an active Company member")
	// ErrHandoffConfirmationRequired refuses a handoff of a Task that has
	// run without confirmHandoff: the caller is told what the handoff
	// cancels and resends with the confirmation. Every error that satisfies
	// it is a *HandoffConfirmationError saying whether a Run is live.
	ErrHandoffConfirmationRequired = errors.New("handing off a Task that has run needs confirmHandoff")
	// ErrOutsideRunScope refuses a Run's read of a Task that is not its own:
	// a Run reads only the Tasks its Agent does for the member it works for.
	// It carries nothing of the refused Task. It also refuses a Run's create
	// of a Task for another member, or in another Project than a Task Run's.
	ErrOutsideRunScope = errors.New("the Task is not one of this Run's own")
	// ErrAssigneeOutsideProject refuses assigning or handing a Task to a member
	// who cannot read its Project (ADR 0017): they could not see the Task they
	// would be responsible for. The refusal names nobody.
	ErrAssigneeOutsideProject = errors.New("the Task assignee cannot read the Task's Project")
	// ErrAttachmentUnavailable refuses a create whose files the creator
	// cannot attach: not theirs, not finished uploading, or already attached.
	ErrAttachmentUnavailable = errors.New("Task attachment is unavailable")
)

// HandoffConfirmationError is the refusal of an unconfirmed handoff. RunLive
// says whether confirming would cancel a Run in flight right now, or only
// move a Task that ran before; the boundary picks its sentence from it. Both
// are one code, so no caller branches on the difference.
type HandoffConfirmationError struct {
	RunLive bool
}

func (e *HandoffConfirmationError) Error() string {
	if e.RunLive {
		return ErrHandoffConfirmationRequired.Error() + ": a Run of it is live"
	}
	return ErrHandoffConfirmationRequired.Error() + ": no Run of it is live"
}

// Unwrap keeps every errors.Is(err, ErrHandoffConfirmationRequired) call
// site — the 409 mapping among them — working unchanged.
func (e *HandoffConfirmationError) Unwrap() error { return ErrHandoffConfirmationRequired }
