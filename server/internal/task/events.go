// The topic this domain publishes, its event names, and the payloads that
// exist only to be published.
//
// A payload the REST surface also serves stays with the domain's other types:
// the event carries the domain's projection, it does not own it. Who may see
// an event lives in cmd/server/realtime_routing.go — a comment here says what
// fact an event records, never who receives it, because a rule written in two
// places is a rule that drifts.

package task

import (
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
)

// Topic is the task family. Every fact this domain publishes travels on it,
// and a consumer subscribes once and switches on the event name.
const Topic = "task"

// The task.* wire names.
const (
	EventCreated       = "task.created"
	EventUpdated       = "task.updated"
	EventDeleted       = "task.deleted"
	EventReportReady   = "task.report_ready"
	EventReportChanged = "task.report_changed"
)

// ChangeType is the Task lifecycle operation a cross-domain transaction must
// announce after it has reconciled Task rows.
type ChangeType string

const (
	ChangeTypeCreated ChangeType = "created"
	ChangeTypeUpdated ChangeType = "updated"
)

// Change identifies one Task projection to publish after a cross-domain
// transaction has finished mutating it. PreviousAssignee and PreviousStatus
// are the row's values before the write, so the Inbox producer can tell a
// new assignment or a fresh blocker from a restated one; both are zero, and
// ignored, for a created Task. Handoff says the write handed the Task's work
// to another Agent and advanced its execution generation (ADR 0014), and
// RunStopped that doing so cancelled a live Run. BriefChanged and
// AssignmentChanged describe actual edits, including assignment edits before
// the first execution generation. UnclaimedScheduleRunCancelled is the exact
// receipt from cancelling a waiting scheduled first attempt before a handoff;
// it permits replacing that occurrence without replaying claimed work.
type Change struct {
	ID                            uuid.UUID
	Type                          ChangeType
	PreviousAssignee              ActorRef
	PreviousStatus                Status
	Handoff                       bool
	RunStopped                    bool
	BriefChanged                  bool
	AssignmentChanged             bool
	UnclaimedScheduleRunCancelled bool
}

// created and updated carry Response, the committed Task exactly as the REST
// surface serves it. Activity stores its own safe before/after
// snapshot in the mutation transaction; the ordinary event remains the
// committed projection that clients can apply directly.

// Removal is what task.deleted carries. The row is gone, so there is no
// projection to snapshot — only the identity and the version it died at, so a
// client can drop its cached copy and ignore a late frame for an older one.
type Removal struct {
	TaskID   uuid.UUID `json:"taskId"`
	Revision int64     `json:"revision"`
	// ProjectID is the deleted Task's Project, whose readers receive the
	// frame (ADR 0017). Read in-process and never on the wire.
	ProjectID uuid.UUID `json:"-"`
	auth.ProjectFrame
}

// Every task.* frame reaches the readers of the Task's Project (ADR 0017).
// A payload that knows the Project names it; otherwise the Task on the
// envelope decides it.

func (r Response) ProjectFrameScope() (uuid.UUID, bool) { return r.ProjectID, true }

func (r Response) WithProjectAudience(audience *auth.ProjectAudience) any {
	r.Audience = audience
	return r
}

func (r Removal) ProjectFrameScope() (uuid.UUID, bool) { return r.ProjectID, true }

func (r Removal) WithProjectAudience(audience *auth.ProjectAudience) any {
	r.Audience = audience
	return r
}

func (p ReportReadyPayload) ProjectFrameScope() (uuid.UUID, bool) { return uuid.Nil, true }

func (p ReportReadyPayload) WithProjectAudience(audience *auth.ProjectAudience) any {
	p.Audience = audience
	return p
}

func (p ReportChangedPayload) ProjectFrameScope() (uuid.UUID, bool) { return p.ProjectID, true }

func (p ReportChangedPayload) WithProjectAudience(audience *auth.ProjectAudience) any {
	p.Audience = audience
	return p
}

// The router finds these payloads by interface, so a changed method would
// silently drop their frames; the compiler checks them instead.
var (
	_ auth.ProjectFramePayload = Response{}
	_ auth.ProjectFramePayload = Removal{}
	_ auth.ProjectFramePayload = ReportReadyPayload{}
	_ auth.ProjectFramePayload = ReportChangedPayload{}
)

// NewDeletedEvent stages task.deleted. It is exported because the one place
// Zenith Team deletes a task is a Company Plan apply, which reconciles a desired
// document against what exists and owns that transaction; the task domain
// still owns what the fact is called and what it carries.
func NewDeletedEvent(
	actorID, companyID, projectID, taskID uuid.UUID,
	revision int64,
) events.Event {
	return events.Event{
		EntityType: Topic,
		Name:       EventDeleted,
		CompanyID:  companyID,
		CreatedAt:  time.Now().UTC(),
		TaskID:     taskID,
		Actor:      events.UserActor(actorID.String()),
		Payload:    Removal{TaskID: taskID, Revision: revision, ProjectID: projectID},
	}
}
