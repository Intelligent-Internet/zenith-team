// Package approval owns durable action approvals and human questions. Approval
// kinds carry typed domain-resource edges; their owning domain prepares or
// executes the action while approval owns the human decision and continuation
// receipt. These input nouns share one lifecycle worker without being collapsed
// into one service or one status vocabulary.
package approval

import (
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/pkg/protocol"
)

var (
	ErrApprovalInvalidInput  = errors.New("approval: invalid input")
	ErrApprovalNotFound      = errors.New("approval: not found")
	ErrApprovalForbidden     = errors.New("approval: resolver forbidden")
	ErrApprovalConflict      = errors.New("approval: conflict")
	ErrApprovalExpired       = errors.New("approval: expired")
	ErrApprovalConfiguration = errors.New("approval: service configuration is invalid")
)

// The types below keep their Approval prefix even though revive reads it as
// package stutter. This package hosts two nouns — Approval and Question — and
// each carries its own cursor, list options, page and stats shape. The prefix
// names the subject, which is what AGENTS.md asks for; dropping it would leave
// approval.Page sitting next to approval.QuestionPage and hide which noun a
// call site is paging. Each declaration therefore carries its own directive.

//nolint:revive // Approval, not the package: Question owns the mirror type.
type ApprovalDecisionInput struct {
	ExpectedRevision int64
	DecisionNote     string
}

//nolint:revive // Approval, not the package: QuestionCursor is the sibling.
type ApprovalCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

//nolint:revive // Approval, not the package: QuestionListOptions is the sibling.
type ApprovalListOptions struct {
	Statuses []ApprovalStatus
	AgentID  *uuid.UUID
	TaskID   *uuid.UUID
	Limit    int32
	Before   *ApprovalCursor
	// Decide keeps only the Approvals the caller may decide (view=decide):
	// the Company Approvals page and its counts.
	Decide bool
}

//nolint:revive // Approval, not the package: QuestionPage is the sibling.
type ApprovalPage struct {
	Items []Approval
	Next  *ApprovalCursor
}

//nolint:revive // Approval, not the package: these stats count approvals only.
type ApprovalStatsOptions struct {
	Since   time.Time
	AgentID *uuid.UUID
}

//nolint:revive // Approval, not the package: these stats count approvals only.
type ApprovalStatsItem struct {
	// Kind names the same fact an Approval's kind names: the gate that
	// raised the request. One fact, one name, on every approvals surface.
	Kind      string `json:"kind"`
	Requested int64  `json:"requested"`
	Approved  int64  `json:"approved"`
	Rejected  int64  `json:"rejected"`
	Expired   int64  `json:"expired"`
	Cancelled int64  `json:"cancelled"`
}

//nolint:revive // Approval, not the package: these stats count approvals only.
type ApprovalStats struct {
	Since     time.Time           `json:"since"`
	Requested int64               `json:"requested"`
	Approved  int64               `json:"approved"`
	Rejected  int64               `json:"rejected"`
	Expired   int64               `json:"expired"`
	Cancelled int64               `json:"cancelled"`
	Items     []ApprovalStatsItem `json:"items"`
}

func normalizeApprovalDecisionInput(input ApprovalDecisionInput) (ApprovalDecisionInput, error) {
	input.DecisionNote = protocol.TrimContractWhitespace(input.DecisionNote)
	if !utf8.ValidString(input.DecisionNote) || utf8.RuneCountInString(input.DecisionNote) > 2048 ||
		len(input.DecisionNote) > 8192 {
		return ApprovalDecisionInput{}, ErrApprovalInvalidInput
	}
	return input, nil
}

func validApprovalStatus(status ApprovalStatus) bool {
	switch status {
	case ApprovalStatusPending, ApprovalStatusApproved, ApprovalStatusRejected,
		ApprovalStatusCancelled, ApprovalStatusExpired:
		return true
	default:
		return false
	}
}

// ActorRef names the agent that raised a request or the person who resolved
// it. It is protocol's shared wire shape, not a second one.
type ActorRef = protocol.ActorRef

// ApprovalType, ApprovalStatus, ApprovalExecutionStatus and ApprovalExecution
// keep the names they had before this refactor. revive would rather they lost
// the Approval prefix, and it is right, but renaming a domain's public
// vocabulary is separate work from moving it.
//
//nolint:revive // pre-existing names; dropping the prefix is a separate change.
type ApprovalType string

const (
	ApprovalTypeCompanyPlanApply        ApprovalType = "company_plan_apply"
	ApprovalTypeCommerceManifestPublish ApprovalType = "commerce_manifest_publish"
)

//nolint:revive // pre-existing name; dropping the prefix is a separate change.
type ApprovalStatus string

const (
	ApprovalStatusPending   ApprovalStatus = "pending"
	ApprovalStatusApproved  ApprovalStatus = "approved"
	ApprovalStatusRejected  ApprovalStatus = "rejected"
	ApprovalStatusCancelled ApprovalStatus = "cancelled"
	ApprovalStatusExpired   ApprovalStatus = "expired"
)

//nolint:revive // pre-existing name; dropping the prefix is a separate change.
type ApprovalExecutionStatus string

const (
	ApprovalExecutionExecuted ApprovalExecutionStatus = "executed"
	ApprovalExecutionSkipped  ApprovalExecutionStatus = "skipped"
)

//nolint:revive // pre-existing name; dropping the prefix is a separate change.
type ApprovalExecution struct {
	Status        ApprovalExecutionStatus `json:"status"`
	SkippedReason *string                 `json:"skippedReason"`
	Result        json.RawMessage         `json:"result,omitempty"`
}

// Approval is the wire projection. A terminal status IS the decision —
// approved and rejected say what the human decided, cancelled and expired say
// no one did — so there is no separate decision field. The stored rule key
// and resolver policy stay in the row; the wire discriminator is kind alone.
//
// Both Run identities are UUID strings. ContinuationRunID appears only once
// the continuation Run exists:
// it is stamped by the transaction that inserts that Run — and stays nil while
// the continuation is pending, and forever if it is abandoned. A non-nil value
// therefore always names a Run readable through the runs API.
type Approval struct {
	ID                uuid.UUID          `json:"id"`
	CompanyID         uuid.UUID          `json:"companyId"`
	Type              ApprovalType       `json:"kind"`
	SubjectID         *uuid.UUID         `json:"subjectId"`
	Status            ApprovalStatus     `json:"status"`
	Title             string             `json:"title"`
	Summary           *string            `json:"summary"`
	PreviewMarkdown   string             `json:"previewMarkdown"`
	Presentation      json.RawMessage    `json:"presentation"`
	RequestedByAgent  ActorRef           `json:"requestedByAgent"`
	OriginRunID       uuid.UUID          `json:"originRunId"`
	TaskID            *uuid.UUID         `json:"taskId"`
	ConversationID    uuid.UUID          `json:"conversationId"`
	ExpiresAt         time.Time          `json:"expiresAt"`
	DecisionNote      *string            `json:"decisionNote"`
	DecidedBy         *ActorRef          `json:"decidedBy"`
	Execution         *ApprovalExecution `json:"execution"`
	ContinuationRunID *uuid.UUID         `json:"continuationRunId"`
	CreatedAt         time.Time          `json:"createdAt"`
	UpdatedAt         time.Time          `json:"updatedAt"`
	Revision          int64              `json:"revision"`

	// OriginUserID is the routing fact for an Approval no Task owns: it
	// belongs to the one user its origin Run started for, and the realtime
	// bridge reads it off the Go value. It stays off the wire — the company
	// members who may see a Task-scoped Approval must not be told whose
	// direct conversation raised one.
	OriginUserID uuid.UUID `json:"-"`
	// ProjectFrame carries a Task-origin frame's audience, the readers of
	// the Project it was raised in (ADR 0017).
	auth.ProjectFrame
}

type QuestionStatus string

const (
	QuestionStatusPending   QuestionStatus = "pending"
	QuestionStatusAnswered  QuestionStatus = "answered"
	QuestionStatusWithdrawn QuestionStatus = "withdrawn"
	QuestionStatusExpired   QuestionStatus = "expired"
)

type QuestionSelectionMode string

const (
	QuestionSelectionSingle QuestionSelectionMode = "single"
	QuestionSelectionMulti  QuestionSelectionMode = "multi"
)

type QuestionOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// QuestionToolSetupType requests a human setup surface, never tool execution authority.
type QuestionToolSetupType string

const QuestionToolSetupZenithStudio QuestionToolSetupType = "zenith-studio"

type QuestionPrompt struct {
	ID                string                `json:"id"`
	Prompt            string                `json:"prompt"`
	HelpText          *string               `json:"helpText"`
	SelectionMode     QuestionSelectionMode `json:"selectionMode"`
	Required          bool                  `json:"required"`
	AllowCustomAnswer bool                  `json:"allowCustomAnswer"`
	Options           []QuestionOption      `json:"options"`
	ToolSetup         QuestionToolSetupType `json:"toolSetup,omitempty"`
	SocialPost        *QuestionSocialPost   `json:"socialPost,omitempty"`
}

type QuestionAnswer struct {
	PromptID  string   `json:"promptId"`
	OptionIDs []string `json:"optionIds"`
	Text      *string  `json:"text"`
}

// Question carries UUID Run identities exactly as Approval does. Its continuation
// identity
// appears only once the Run it names has been inserted.
type Question struct {
	ID                uuid.UUID        `json:"id"`
	CompanyID         uuid.UUID        `json:"companyId"`
	Status            QuestionStatus   `json:"status"`
	Title             string           `json:"title"`
	AskedByAgent      ActorRef         `json:"askedByAgent"`
	OriginRunID       uuid.UUID        `json:"originRunId"`
	TaskID            *uuid.UUID       `json:"taskId"`
	ConversationID    uuid.UUID        `json:"conversationId"`
	Prompts           []QuestionPrompt `json:"prompts"`
	ExpiresAt         time.Time        `json:"expiresAt"`
	Answers           []QuestionAnswer `json:"answers"`
	AnsweredBy        *ActorRef        `json:"answeredBy"`
	ContinuationRunID *uuid.UUID       `json:"continuationRunId"`
	CreatedAt         time.Time        `json:"createdAt"`
	UpdatedAt         time.Time        `json:"updatedAt"`
	Revision          int64            `json:"revision"`

	// OriginUserID is the routing fact for a Question no Task owns, read
	// in-process by the realtime bridge exactly as an Approval's is, and kept
	// off the wire for the same reason.
	OriginUserID uuid.UUID `json:"-"`
	auth.ProjectFrame
}

// A frame about an Approval or Question raised in a Task chat, or naming a
// Task, reaches the readers of that Project (ADR 0017). The chat and the
// Task are on the envelope; one raised anywhere else keeps its origin route.

func (a Approval) ProjectFrameScope() (uuid.UUID, bool) { return uuid.Nil, true }

func (a Approval) WithProjectAudience(audience *auth.ProjectAudience) any {
	a.Audience = audience
	return a
}

func (q Question) ProjectFrameScope() (uuid.UUID, bool) { return uuid.Nil, true }

func (q Question) WithProjectAudience(audience *auth.ProjectAudience) any {
	q.Audience = audience
	return q
}

// The router finds these payloads by interface, so a changed method would
// silently drop their frames; the compiler checks them instead.
var (
	_ auth.ProjectFramePayload = Approval{}
	_ auth.ProjectFramePayload = Question{}
)
