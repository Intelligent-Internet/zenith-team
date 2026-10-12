package company

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/pkg/protocol"
)

// CreateCompanyInput creates the bare company: owner membership and starter
// journey only. Agents (and their conversations) are created afterwards,
// once a daemon has registered a runtime — an agent cannot exist without the
// runtime that executes it.
type CreateCompanyInput struct {
	Name            string
	Slug            string
	TaskPrefix      string
	BusinessContext string
	WebsiteURL      string
}

type Statistics struct {
	MemberCount        int64 `json:"memberCount"`
	RuntimeCount       int64 `json:"runtimeCount"`
	RuntimeOnlineCount int64 `json:"runtimeOnlineCount"`
}

type MemberResponse struct {
	CompanyID           uuid.UUID        `json:"companyId"`
	UserID              uuid.UUID        `json:"userId"`
	Role                auth.CompanyRole `json:"role"`
	Status              MembershipStatus `json:"status"`
	RevokedAt           *time.Time       `json:"revokedAt"`
	RevokedByUserID     *uuid.UUID       `json:"revokedByUserId"`
	ReactivatedAt       *time.Time       `json:"reactivatedAt"`
	ReactivatedByUserID *uuid.UUID       `json:"reactivatedByUserId"`
	CreatedAt           time.Time        `json:"createdAt"`
	UpdatedAt           time.Time        `json:"updatedAt"`
	Revision            int64            `json:"revision"`
}

type MembershipStatus string

const (
	MembershipStatusActive  MembershipStatus = "active"
	MembershipStatusRevoked MembershipStatus = "revoked"
)

type MembershipOutcome string

const (
	MembershipGranted        MembershipOutcome = "granted"
	MembershipReactivated    MembershipOutcome = "reactivated"
	MembershipRoleChanged    MembershipOutcome = "role_changed"
	MembershipRemoved        MembershipOutcome = "removed"
	MembershipLeft           MembershipOutcome = "left"
	MembershipCompanyDeleted MembershipOutcome = "company_deleted"
	MembershipUnchanged      MembershipOutcome = "unchanged"
)

type ActorType = protocol.ActorKind

const (
	ActorTypeUser  ActorType = "user"
	ActorTypeAgent ActorType = "agent"
)

type ActorRef = protocol.ActorRef

func activeMemberResponse(
	member MemberResponse,
	email, displayName string,
	avatarURL *string,
	accountDisabled bool,
) ActiveMemberResponse {
	return ActiveMemberResponse{
		CompanyID: member.CompanyID,
		Actor: ActorRef{
			ID: member.UserID, Kind: ActorTypeUser,
			DisplayName: displayName, AvatarURL: avatarURL,
		},
		Email:           email,
		Role:            member.Role,
		AccountDisabled: accountDisabled,
		CreatedAt:       member.CreatedAt,
		UpdatedAt:       member.UpdatedAt,
		Revision:        member.Revision,
	}
}

// Response is the Company's public projection: one shape for the REST
// read and the company.created / company.updated frames, so a field added
// here reaches both.
type Response struct {
	ID                    uuid.UUID        `json:"id"`
	Name                  string           `json:"name"`
	AvatarAttachmentID    *uuid.UUID       `json:"avatarAttachmentId"`
	Slug                  string           `json:"slug"`
	TaskPrefix            string           `json:"taskPrefix"`
	BusinessContext       string           `json:"businessContext"`
	WebsiteURL            *string          `json:"websiteUrl"`
	ContactEmail          *string          `json:"contactEmail"`
	PhoneCountryISO2      *string          `json:"phoneCountryIso2"`
	PhoneNumber           *string          `json:"phoneNumber"`
	Role                  auth.CompanyRole `json:"role"`
	InitializationPending bool             `json:"initializationPending,omitempty"`
	// Permissions is the calling member's resolved set: what their Role
	// grants, computed once here so no client re-derives policy from the
	// role name. The set is never null.
	Permissions []auth.Permission `json:"permissions"`
	Revision    int64             `json:"revision"`
	CreatedAt   time.Time         `json:"createdAt"`
	UpdatedAt   time.Time         `json:"updatedAt"`

	// The two server-owned onboarding stamps. They are Company attributes, not
	// a progress cursor: AgentSetupCompletedAt is set when the Company gains
	// its root Agent, FirstPlanAppliedAt when its first Company Plan applies.
	// Neither participates in Revision, so a stamp never conflicts with a
	// founder's profile edit.
	AgentSetupCompletedAt *time.Time `json:"agentSetupCompletedAt"`
	FirstPlanAppliedAt    *time.Time `json:"firstPlanAppliedAt"`

	// FounderUserID is the routing fact for company.created: the company
	// room is empty at creation, so the frame has to reach the founder's own
	// user room as well. It is read in-process and stays off the wire — the
	// REST read and company.updated leave it zero, because neither routes to
	// a user.
	FounderUserID uuid.UUID `json:"-"`
}

// ActiveMemberResponse is an active membership's public projection: one shape
// for the owner's member list and the member.added / member.updated frames.
// Actor names the member, which is also what member.added routes on.
type ActiveMemberResponse struct {
	CompanyID       uuid.UUID         `json:"companyId"`
	Actor           protocol.ActorRef `json:"actor"`
	Email           string            `json:"email"`
	Role            auth.CompanyRole  `json:"role"`
	AccountDisabled bool              `json:"accountDisabled"`
	CreatedAt       time.Time         `json:"createdAt"`
	UpdatedAt       time.Time         `json:"updatedAt"`
	Revision        int64             `json:"revision"`
}

// ResourceType names a company resource kind. Resources are stored as flat,
// typed columns (daemon_id + local_path for local_directory); a new type
// widens the table in a migration and adds its constant plus mapping in
// companyResourceFromRow.
type ResourceType string

// Company resource types.
const (
	ResourceTypeLocalDirectory ResourceType = "local_directory"
)

// ResourceRefResponse is the public projection of a resource reference. For
// local directories it names the registering daemon; the local path never
// leaves the record's redacted LocalPath field.
type ResourceRefResponse struct {
	DaemonID   string `json:"daemonId"`
	DaemonName string `json:"daemonName"`
}

// DaemonStatus is how the daemon behind a resource looked when the projection
// was read.
type DaemonStatus string

// Daemon statuses.
const (
	DaemonStatusOnline       DaemonStatus = "online"
	DaemonStatusOffline      DaemonStatus = "offline"
	DaemonStatusUnregistered DaemonStatus = "unregistered"
)

// ResourceResponse is a Company resource's public projection: one shape for
// the REST read and the resource.created / resource.updated frames.
type ResourceResponse struct {
	ID               uuid.UUID           `json:"id"`
	CompanyID        uuid.UUID           `json:"companyId"`
	ResourceType     ResourceType        `json:"resourceType"`
	ResourceRef      ResourceRefResponse `json:"resourceRef"`
	DaemonStatus     DaemonStatus        `json:"daemonStatus"`
	DaemonLastSeenAt *time.Time          `json:"daemonLastSeenAt"`
	Label            string              `json:"label"`
	Position         int32               `json:"position"`
	Metadata         json.RawMessage     `json:"metadata"`
	CreatedBy        protocol.ActorRef   `json:"createdBy"`
	CreatedAt        time.Time           `json:"createdAt"`
	UpdatedAt        time.Time           `json:"updatedAt"`
	Revision         int64               `json:"revision"`
}
