package agent

import (
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/agent/voice"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

const (
	maxNameRunes          = 120
	maxTitleRunes         = 120
	maxInstructionsRunes  = 32 * 1024
	maxInstructionsBytes  = 32 * 1024
	maxModelRunes         = 160
	maxThinkingLevelRunes = 64
)

// CreateInput contains only the client-owned fields accepted by Agent
// creation. Provider credentials and Runtime/daemon state are deliberately
// absent. The Agent is a template of Provider (ADR 0014); RuntimeID, when
// set, is "also set it up on my computer": the caller's own runtime of that
// provider, bound to the new Agent for the caller in the same transaction.
// A nil ReportsToAgentID creates a root agent (the org has no seeded CEO),
// and the root requires RuntimeID — onboarding binds the founder.
type CreateInput struct {
	Name             string
	Title            string
	Instructions     string
	ReportsToAgentID *uuid.UUID
	Provider         string
	RuntimeID        *uuid.UUID
	Model            *string
	ThinkingLevel    *string
	// AvatarKey and AccentColor are the Agent's own appearance; nil leaves
	// the client to render initials and its default palette.
	AvatarKey   *string
	AccentColor *string
	// MaxConcurrentRuns is the task-lane cap the Agent starts with; nil takes
	// the column default (3). The 1..10 bound is the request binder's named
	// maxConcurrentRuns rule, as on update, and is not restated here.
	MaxConcurrentRuns *int32
}

type NullableTextChange struct {
	Present bool
	Value   *string
}

// UpdateInput is the template edit. Where the Agent runs is not a template
// field: each member's binding is its own write (BindingService).
//
// MaxConcurrentRuns is nil to leave the cap alone; the 1..10 bound is
// enforced by the request binder and deliberately not restated here.
type UpdateInput struct {
	Voice             *voice.Settings
	Capabilities      *CapabilitySettings
	AvatarKey         NullableTextChange
	AccentColor       NullableTextChange
	ExpectedRevision  int64
	Name              *string
	Title             *string
	Instructions      *string
	ReportsToAgentID  *uuid.UUID
	Model             NullableTextChange
	ThinkingLevel     NullableTextChange
	MaxConcurrentRuns *int32
}

type RevisionInput struct {
	ExpectedRevision int64
}

type ListOptions struct {
	IncludeArchived  bool
	ReportsToAgentID *uuid.UUID
}

func normalizeCreateInput(input CreateInput) (CreateInput, error) {
	if !protocol.IsSupportedProvider(input.Provider) {
		return CreateInput{}, ErrInvalidInput
	}
	if input.RuntimeID != nil && !util.ValidUUID(*input.RuntimeID) {
		return CreateInput{}, ErrInvalidInput
	}
	if input.ReportsToAgentID != nil &&
		!util.ValidUUID(*input.ReportsToAgentID) {
		return CreateInput{}, ErrInvalidInput
	}
	// The root agent is onboarding's step: it is created bound, so the
	// founder's runtime is required there.
	if input.ReportsToAgentID == nil && input.RuntimeID == nil {
		return CreateInput{}, ErrInvalidInput
	}
	input.Name = protocol.TrimContractWhitespace(input.Name)
	input.Title = protocol.TrimContractWhitespace(input.Title)
	input.Instructions = protocol.TrimContractWhitespace(input.Instructions)
	var appearanceOK bool
	if input.AvatarKey, appearanceOK = normalizeAvatarKey(input.AvatarKey); !appearanceOK {
		return CreateInput{}, ErrInvalidInput
	}
	if input.AccentColor, appearanceOK = normalizeAccentColor(input.AccentColor); !appearanceOK {
		return CreateInput{}, ErrInvalidInput
	}

	var ok bool
	if input.Model, ok = normalizeOptionalOverride(input.Model, maxModelRunes); !ok {
		return CreateInput{}, ErrInvalidInput
	}
	if input.ThinkingLevel, ok = normalizeOptionalOverride(input.ThinkingLevel, maxThinkingLevelRunes); !ok {
		return CreateInput{}, ErrInvalidInput
	}
	if input.Name == "" || utf8.RuneCountInString(input.Name) > maxNameRunes ||
		input.Title == "" || utf8.RuneCountInString(input.Title) > maxTitleRunes ||
		utf8.RuneCountInString(
			input.Instructions,
		) > maxInstructionsRunes || len(input.Instructions) > maxInstructionsBytes {
		return CreateInput{}, ErrInvalidInput
	}
	return input, nil
}

// normalizeUpdateInput trims and bounds the fields an update supplies. That an
// update supplies at least one of them is declared once, at the boundary, as
// `required_without_all` on updateAgentRequest.Name — a body carrying none of
// the eight never reaches this function. MaxConcurrentRuns is not normalized
// here at all: its whole rule is the binder's named binding.
func normalizeUpdateInput(input UpdateInput) (UpdateInput, error) {
	if input.Voice != nil && !input.Voice.Valid() {
		return UpdateInput{}, ErrInvalidInput
	}
	if input.Name != nil {
		value := protocol.TrimContractWhitespace(*input.Name)
		if value == "" || utf8.RuneCountInString(value) > maxNameRunes {
			return UpdateInput{}, ErrInvalidInput
		}
		input.Name = &value
	}
	if input.Title != nil {
		value := protocol.TrimContractWhitespace(*input.Title)
		if value == "" || utf8.RuneCountInString(value) > maxTitleRunes {
			return UpdateInput{}, ErrInvalidInput
		}
		input.Title = &value
	}
	if input.Instructions != nil {
		value := protocol.TrimContractWhitespace(*input.Instructions)
		if utf8.RuneCountInString(value) > maxInstructionsRunes || len(value) > maxInstructionsBytes {
			return UpdateInput{}, ErrInvalidInput
		}
		input.Instructions = &value
	}
	if input.ReportsToAgentID != nil && !util.ValidUUID(*input.ReportsToAgentID) {
		return UpdateInput{}, ErrInvalidInput
	}
	var ok bool
	if input.Model, ok = normalizeNullableTextChange(input.Model, maxModelRunes); !ok {
		return UpdateInput{}, ErrInvalidInput
	}
	if input.ThinkingLevel, ok = normalizeNullableTextChange(input.ThinkingLevel, maxThinkingLevelRunes); !ok {
		return UpdateInput{}, ErrInvalidInput
	}
	if input.Capabilities != nil {
		normalized, err := normalizeCapabilitySettings(*input.Capabilities)
		if err != nil {
			return UpdateInput{}, err
		}
		input.Capabilities = &normalized
	}
	if input.AvatarKey.Present && input.AvatarKey.Value != nil {
		value, ok := normalizeAvatarKey(input.AvatarKey.Value)
		if !ok {
			return UpdateInput{}, ErrInvalidInput
		}
		input.AvatarKey.Value = value
	}
	if input.AccentColor.Present && input.AccentColor.Value != nil {
		value, ok := normalizeAccentColor(input.AccentColor.Value)
		if !ok {
			return UpdateInput{}, ErrInvalidInput
		}
		input.AccentColor.Value = value
	}

	return input, nil
}

func normalizeNullableTextChange(change NullableTextChange, maximum int) (NullableTextChange, bool) {
	if !change.Present {
		return NullableTextChange{}, change.Value == nil
	}
	if change.Value == nil {
		return change, true
	}
	normalized, ok := normalizeOptionalOverride(change.Value, maximum)
	if !ok {
		return NullableTextChange{}, false
	}
	change.Value = normalized
	return change, true
}

func normalizeOptionalOverride(value *string, maximum int) (*string, bool) {
	if value == nil {
		return nil, true
	}
	normalized := protocol.TrimContractWhitespace(*value)
	if normalized == "" || utf8.RuneCountInString(normalized) > maximum {
		return nil, false
	}
	return &normalized, true
}

// Appearance is the Agent's own: the Fold avatar look it renders with
// (`fold-<palette>`, a vocabulary the client owns; the server fences only the
// key format) and the accent color behind it. Both are optional and carry no
// authority.

var (
	avatarKeyPattern   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	accentColorPattern = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
)

func normalizeAvatarKey(value *string) (*string, bool) {
	if value == nil {
		return nil, true
	}
	normalized := protocol.TrimContractWhitespace(*value)
	if !avatarKeyPattern.MatchString(normalized) {
		return nil, false
	}
	return &normalized, true
}

func normalizeAccentColor(value *string) (*string, bool) {
	if value == nil {
		return nil, true
	}
	normalized := strings.ToUpper(protocol.TrimContractWhitespace(*value))
	if !accentColorPattern.MatchString(normalized) {
		return nil, false
	}
	return &normalized, true
}

// starterAgentTemplate is one of the personas the CEO setup provisions as
// ordinary Agents under the root. They are templates, not roles: the owner
// renames, retrains, rebinds or archives them like any other Agent, and
// nothing in the product keys on their names.
type starterAgentTemplate struct {
	Name         string
	Title        string
	Instructions string
	AvatarKey    string
	AccentColor  string
}

var starterAgentTemplates = []starterAgentTemplate{
	{
		Name:         "Lyra",
		Title:        "Product Agent",
		Instructions: "You are Lyra, the Product Agent. Own customer discovery, product strategy and scope, requirements and user flows, UX and prototyping, roadmap and prioritization, and acceptance of product work. Follow the Company's standards and report progress clearly.",
		AvatarKey:    "fold-orchid",
		AccentColor:  "#BA97DA",
	},
	{
		Name:         "Eli",
		Title:        "Engineering Agent",
		Instructions: "You are Eli, the Engineering Agent. Own architecture and technical planning, repository and application implementation, backend, database and integrations, testing and browser verification, CI/CD and deployment, and runtime reliability. Follow the Company's standards and report progress clearly.",
		AvatarKey:    "fold-periwinkle",
		AccentColor:  "#8BACE1",
	},
	{
		Name:         "Susan",
		Title:        "Marketing Agent",
		Instructions: "You are Susan, the Marketing Agent. Own positioning and messaging, brand identity and visual creative, content and campaigns, social and community, SEO and CRO strategy, and demand generation analytics. Follow the Company's standards and report progress clearly.",
		AvatarKey:    "fold-coral",
		AccentColor:  "#ED817B",
	},
	{
		Name:         "Ariana",
		Title:        "Operations Agent",
		Instructions: "You are Ariana, the Operations Agent. Own workflow and process design, fulfillment and logistics, vendor and procurement, capacity and resource planning, operational reporting, and cross-system cleanup and compliance coordination. Follow the Company's standards and report progress clearly.",
		AvatarKey:    "fold-lagoon",
		AccentColor:  "#69BABA",
	},
}

// stageAgentLifecycle is the package's only lifecycle-event stager. The
// projection comes from the same canonical read used by the HTTP surface,
// less the acting member's own setup: the frame reaches the whole Company and
// is kept as Company activity, and a member's machine is theirs alone
// (ADR 0014).
func stageAgentLifecycle(
	pending *[]events.Event,
	actorID uuid.UUID,
	name string,
	agent Response,
) {
	*pending = append(*pending, events.Event{
		EntityType: Topic,
		Name:       name,
		CompanyID:  agent.CompanyID,
		CreatedAt:  time.Now().UTC(),
		Actor:      events.UserActor(actorID.String()),
		Payload:    agent.withoutViewerSetup(),
	})
}

// withoutViewerSetup is the Agent as a member who has not set it up sees it:
// no binding, so no machine name or id, and the availability, reason,
// execute verdict and capacity count that follow from having none.
// Access.Configure stays the acting member's; it names no machine, and a
// client applying the frame keeps its own verdicts.
func (r Response) withoutViewerSetup() Response {
	reason := "not_bound"
	if r.ArchivedAt != nil {
		reason = "agent_archived"
	}
	r.Binding = nil
	r.RuntimeAvailable = false
	r.UnavailableReason = &reason
	r.Access.Execute = false
	r.ActiveRunCount = 0
	return r
}

// Response is the credential-free public representation of a Agent. One shape
// serves the REST reads and the agent.created / agent.updated frames, so a
// field added here reaches both.
type Response struct {
	Voice            voice.Settings     `json:"voice"`
	Capabilities     CapabilitySettings `json:"capabilities"`
	ID               uuid.UUID          `json:"id"`
	CompanyID        uuid.UUID          `json:"companyId"`
	Name             string             `json:"name"`
	Title            string             `json:"title"`
	Instructions     string             `json:"instructions"`
	ReportsTo        *NamedRef          `json:"reportsTo"`
	ReportsToAgentID *uuid.UUID         `json:"-"`
	AvatarKey        *string            `json:"avatarKey"`
	AccentColor      *string            `json:"accentColor"`
	// CreatedBy is the member who created the Agent (ADR 0013), nil for
	// Agents that predate the record. Access is the viewer's standing on
	// the Agent: whether they may configure it, and whether they can run it.
	CreatedBy *OwnerRef `json:"createdBy"`
	Access    Access    `json:"access"`
	// Binding is the VIEWER's placement of the Agent on one of their own
	// runtimes, nil when they have not set it up (ADR 0014). Availability
	// and its reason are the viewer's too.
	Binding           *BindingRef `json:"binding"`
	RuntimeAvailable  bool        `json:"runtimeAvailable"`
	UnavailableReason *string     `json:"unavailableReason"`
	Provider          string      `json:"provider"`
	Model             *string     `json:"model"`
	ThinkingLevel     *string     `json:"thinkingLevel"`
	// MaxConcurrentRuns caps the agent's task-lane runs in flight at once.
	// The value rule (1..10) is the binder's — the named maxConcurrentRuns
	// binding on updateAgentRequest — not a DB CHECK and not restated here.
	MaxConcurrentRuns int32 `json:"maxConcurrentRuns"`
	// ActiveRunCount is how many runs hold one of those slots on the
	// viewer's binding runtime right now — capacity is per binding — counted
	// with the claim gate's own task-lane predicate
	// (CountActiveTaskLaneRunsByAgentForBinder mirrors CountActiveTaskLaneRuns)
	// so the capacity display and the enforced cap always agree.
	// Direct-conversation runs are in neither number.
	ActiveRunCount int64      `json:"activeRunCount"`
	ArchivedAt     *time.Time `json:"archivedAt"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
	Revision       int64      `json:"revision"`
}

// NamedRef names a related Agent without repeating its projection.
type NamedRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// RuntimeRef names one of the viewer's own runtimes, as a binding names it.
// CustomName is the user's machine name when one was set, so an agent roster
// renders `customName ?? name` without a runtime read. Only ever the
// viewer's machine: another member's is never named on the wire (ADR 0014).
type RuntimeRef struct {
	ID             uuid.UUID                       `json:"id"`
	Name           string                          `json:"name"`
	CustomName     *string                         `json:"customName"`
	Status         string                          `json:"status"`
	Provider       string                          `json:"provider"`
	SignIn         *RuntimeSignIn                  `json:"signIn"`
	UnusableReason *protocol.RuntimeUnusableReason `json:"unusableReason"`
}

// BindingRef is the viewer's binding of an Agent as the Agent projection and
// the agent.binding_changed frame carry it.
type BindingRef struct {
	ID        uuid.UUID  `json:"id"`
	AgentID   uuid.UUID  `json:"agentId"`
	Runtime   RuntimeRef `json:"runtime"`
	CreatedAt time.Time  `json:"createdAt"`
}
