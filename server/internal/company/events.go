// The topics this domain publishes: the topic names, the event names, and the
// payloads that exist only to be published.
//
// A payload the REST surface also serves stays with the rest of the domain's
// types — the event carries the domain's projection, it does not own it. Who
// may see them lives in cmd/server/realtime_routing.go — a comment here says
// what fact an event records, never who receives it, because a rule written
// in two places is a rule that drifts.

package company

import (
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
)

// Topic carries the Company facts.
const Topic = "company"

const (
	// EventCompanyCreated and EventCompanyUpdated carry the Response
	// projection: the committed Company, as created or amended.
	EventCompanyCreated = "company.created"
	EventCompanyUpdated = "company.updated"

	// EventCompanyMembershipChanged carries a Membership: it asks every
	// daemon signed in as this user to reconcile the set of companies it
	// serves. Nothing forwards it to a browser.
	EventCompanyMembershipChanged = "company.membership_changed"
)

// Membership names the user whose company set moved.
type Membership struct {
	UserID string `json:"userId"`
}

// TopicMember carries the membership facts as the Company sees them.
const TopicMember = "member"

const (
	// EventMemberAdded and EventMemberUpdated carry the active membership:
	// the member as the Company now sees it.
	EventMemberAdded   = "member.added"
	EventMemberUpdated = "member.updated"

	// EventMemberRemoved names a membership that ended, by removal or by the
	// member leaving. The row is gone, so there is no projection to snapshot,
	// only a MemberRemovalReceipt.
	EventMemberRemoved = "member.removed"
)

// MemberRemovalReceipt names the ended membership and its terminal revision;
// the row is gone, so there is no projection to snapshot.
type MemberRemovalReceipt struct {
	UserID   uuid.UUID `json:"userId"`
	Revision int64     `json:"revision"`

	// CompanyID is the routing fact: this frame belongs to one Company. It
	// is read in-process and stays off the wire, which is where the receipt
	// has always been silent about it.
	CompanyID uuid.UUID `json:"-"`
}

// TopicAuth carries the personal half of a membership change: what access the
// affected user now holds. Each is staged in the same transaction as the
// member.* frame it accompanies.
const TopicAuth = "auth"

const (
	EventAuthCompanyAccessChanged = "auth.company_access_changed"
	EventAuthCompanyAccessRevoked = "auth.company_access_revoked"
)

// AccessReason says why a user's Company access changed. It is on the wire,
// so a client can tell a role change from a revocation it did not ask for.
type AccessReason string

// Company access change reasons.
const (
	AccessReasonMembershipGranted     AccessReason = "membership_granted"
	AccessReasonMembershipReactivated AccessReason = "membership_reactivated"
	AccessReasonRoleChanged           AccessReason = "role_changed"
	AccessReasonMembershipRemoved     AccessReason = "membership_removed"
	AccessReasonMembershipLeft        AccessReason = "membership_left"
	AccessReasonCompanyDeleted        AccessReason = "company_deleted"
)

// AccessChangedReceipt tells one user what access they now hold in a Company:
// the role and the permissions it resolves to, so a client can re-gate from
// the frame before it refetches anything.
type AccessChangedReceipt struct {
	CompanyID   uuid.UUID         `json:"companyId"`
	Role        auth.CompanyRole  `json:"role"`
	Permissions []auth.Permission `json:"permissions"`
	Reason      AccessReason      `json:"reason"`

	// UserID is the routing fact: this frame belongs to one user, wherever
	// they are signed in. It is read in-process and stays off the wire — a
	// user told about their own access learns nothing from being told whose
	// it is.
	UserID uuid.UUID `json:"-"`
}

// AccessRevokedReceipt tells one user their access to a Company is gone.
type AccessRevokedReceipt struct {
	CompanyID uuid.UUID    `json:"companyId"`
	Reason    AccessReason `json:"reason"`

	// UserID is the routing fact: this frame belongs to one user, wherever
	// they are signed in. It is read in-process and stays off the wire.
	UserID uuid.UUID `json:"-"`
}

// TopicInvitation carries the invitation surface: email invitations and the
// share link. Both reach only the members whose role holds members.invite —
// the people who can see that surface — never the whole Company room.
const TopicInvitation = "invitation"

const (
	// EventInvitationChanged carries the invitation as the Team page renders
	// it after any change: created, resent, revoked, accepted, expired, or a
	// renewal request.
	EventInvitationChanged = "invitation.changed"
	// EventShareLinkChanged carries the link's state without its URL: the
	// URL is a credential and is only ever read through the authorized route.
	EventShareLinkChanged = "invitation.share_link_changed"
)

// InvitationChanged is the invitation.changed payload: the invitation the
// Team page renders, plus the routing fact of who may see it.
type InvitationChanged struct {
	InvitationResponse

	// RecipientUserIDs are the active members holding members.invite at the
	// moment of the change. Read in-process and never on the wire.
	RecipientUserIDs []uuid.UUID `json:"-"`
}

// ShareLinkChanged is the share_link.changed payload: enough for a client to
// know the link changed and refetch it through the authorized route.
type ShareLinkChanged struct {
	Role      auth.CompanyRole `json:"role"`
	ExpiresAt *time.Time       `json:"expiresAt"`
	Revision  int64            `json:"revision"`
	Available bool             `json:"available"`

	// RecipientUserIDs are the active members holding members.invite. Read
	// in-process and never on the wire.
	RecipientUserIDs []uuid.UUID `json:"-"`
}

// TopicResource carries the Resource facts.
const TopicResource = "resource"

const (
	// EventResourceCreated and EventResourceUpdated carry the
	// ResourceResponse projection: the committed Resource, as created or
	// amended.
	EventResourceCreated = "resource.created"
	EventResourceUpdated = "resource.updated"

	// EventResourceDeleted is a removal receipt: the row is gone, so it
	// names the Resource rather than snapshots it.
	EventResourceDeleted = "resource.deleted"
)

// ResourceRemoval is the terminal removal receipt for resource.deleted.
type ResourceRemoval struct {
	ResourceID uuid.UUID `json:"resourceId"`
	Revision   int64     `json:"revision"`

	// CompanyID is the routing fact: this frame belongs to one Company. It
	// is read in-process and stays off the wire, which is where the receipt
	// has always carried identity and revision alone.
	CompanyID uuid.UUID `json:"-"`
}

// TopicGroup carries the Group facts: a Group's name,
// members or revision moved, or the Group is gone.
const TopicGroup = "group"

const (
	// EventGroupChanged carries a GroupChanged: a Group was created, renamed,
	// or gained or lost members, a member leaving the Company included.
	EventGroupChanged = "group.changed"
	// EventGroupDeleted carries a GroupDeleted.
	EventGroupDeleted = "group.deleted"
)

// GroupChanged names the Group that moved and its new revision. Nothing else
// is on the wire: clients refetch the Groups.
type GroupChanged struct {
	GroupID  uuid.UUID `json:"groupId"`
	Revision int64     `json:"revision"`
}

// GroupDeleted names the Group that is gone.
type GroupDeleted struct {
	GroupID uuid.UUID `json:"groupId"`
}

// TopicProject carries Project identity and lifecycle projections.
const TopicProject = "project"

const (
	EventProjectCreated  = "project.created"
	EventProjectUpdated  = "project.updated"
	EventProjectArchived = "project.archived"

	// EventProjectAccessChanged says who may do what on a Project changed: a
	// grant set, a removal, a Join, a visibility change or a Company
	// revocation (ADR 0017). It carries ProjectAccessChanged, an opaque
	// invalidation: the client refetches the Project, and a 404 drops it.
	EventProjectAccessChanged = "project.access_changed"
)

// ProjectLifecycle is what project.created, project.updated and
// project.archived carry: the Project's identity and settings, never its
// files or anyone's access (ADR 0017). A client refetches the Project for
// those, through its own authorized read.
type ProjectLifecycle struct {
	ID          uuid.UUID              `json:"id"`
	CompanyID   uuid.UUID              `json:"companyId"`
	Key         string                 `json:"key"`
	Title       string                 `json:"title"`
	Description string                 `json:"description"`
	Visibility  auth.ProjectVisibility `json:"visibility"`
	Revision    int64                  `json:"revision"`
	CreatedAt   time.Time              `json:"createdAt"`
	UpdatedAt   time.Time              `json:"updatedAt"`
	ArchivedAt  *time.Time             `json:"archivedAt"`

	// ProjectFrame carries the audience decided in the mutation's
	// transaction. Read in-process and never on the wire.
	auth.ProjectFrame
}

// The router reads the audience by interface; the compiler checks it.
var _ auth.ProjectAudienceCarrier = ProjectLifecycle{}

// ProjectAccessChanged names the Project whose access moved and its new
// access revision. Nothing else is on the wire: who holds what is read back
// through the authorized routes.
type ProjectAccessChanged struct {
	ProjectID uuid.UUID `json:"projectId"`
	Revision  int64     `json:"revision"`

	// RecipientUserIDs are the Project's readers before and after the change,
	// the active projects.manage holders and the actor, decided inside the
	// mutation's transaction. Read in-process and never on the wire.
	RecipientUserIDs []uuid.UUID `json:"-"`
	// CompanyWide is set when the Project was or became Company-visible:
	// every active member read it on one side of the change. Read in-process
	// and never on the wire.
	CompanyWide bool `json:"-"`
}
