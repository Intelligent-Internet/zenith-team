package schedule

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/util"
	"boardly.dev/boardly/server/pkg/protocol"
)

const (
	maximumNameRunes         = 240
	maximumInstructionsRunes = 64 * 1024
	maximumInstructionsBytes = 64 * 1024
	maximumSources           = 128
	maximumSafeInteger       = int64(9007199254740991)
	defaultListLimit         = 50
	maximumListLimit         = 100
)

var (
	safeCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	digestPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Status string

const (
	StatusDraft     Status = "draft"
	StatusActive    Status = "active"
	StatusPaused    Status = "paused"
	StatusCompleted Status = "completed"
	StatusCancelled Status = "cancelled"
)

const (
	ReasonUserPaused       = "user_paused"
	ReasonDefinitionUpdate = "definition_updated"
	ReasonCompanyChanged   = "company_changed"
	ReasonCreatorChanged   = "creator_changed"
	ReasonAgentUnavailable = "agent_unavailable"
	ReasonProviderChanged  = "provider_changed"
	ReasonSourceChanged    = "source_changed"
)

type Source struct {
	ConnectorID            uuid.UUID `json:"id"`
	PolicyRevision         int64     `json:"policyRevision"`
	Provider               string    `json:"provider"`
	Source                 string    `json:"source"`
	NativeID               string    `json:"nativeId"`
	ReviewedContractDigest string    `json:"contractDigest"`
	Position               int32     `json:"position"`
}

type Response struct {
	ID               uuid.UUID  `json:"id"`
	CompanyID        uuid.UUID  `json:"companyId"`
	ConversationID   uuid.UUID  `json:"conversationId"`
	CreatorUserID    uuid.UUID  `json:"creatorUserId"`
	AgentID          uuid.UUID  `json:"agentId"`
	ReviewedProvider string     `json:"reviewedProvider"`
	Name             string     `json:"name"`
	Instructions     string     `json:"instructions"`
	PresetKey        *string    `json:"presetKey"`
	Trigger          Trigger    `json:"trigger"`
	Sources          []Source   `json:"sources"`
	NextOccurrenceAt *time.Time `json:"nextOccurrenceAt"`
	LastOccurrenceAt *time.Time `json:"lastOccurrenceAt"`
	Status           Status     `json:"status"`
	PauseReason      *string    `json:"pauseReason"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
	Revision         int64      `json:"revision"`
}

type PreviewInput struct {
	AgentID      uuid.UUID
	Name         string
	Instructions string
	PresetKey    *string
	Trigger      Trigger
	ConnectorIDs []uuid.UUID
}

type PreviewResult struct {
	AgentID          uuid.UUID   `json:"agentId"`
	ReviewedProvider string      `json:"reviewedProvider"`
	Name             string      `json:"name"`
	Instructions     string      `json:"instructions"`
	PresetKey        *string     `json:"presetKey"`
	Trigger          Trigger     `json:"trigger"`
	Sources          []Source    `json:"sources"`
	Occurrences      []time.Time `json:"occurrences"`
	ReviewDigest     string      `json:"reviewDigest"`
}

type CreateInput struct {
	RequestKey   uuid.UUID
	AgentID      uuid.UUID
	Name         string
	Instructions string
	PresetKey    *string
	Trigger      Trigger
	ConnectorIDs []uuid.UUID
	Status       Status
	ReviewDigest string
}

type CreateResult struct {
	Schedule Response
	Created  bool
}

type NullableTextChange struct {
	Present bool
	Value   *string
}

type SourceChange struct {
	Present      bool
	ConnectorIDs []uuid.UUID
}

type UpdateInput struct {
	ExpectedRevision int64
	AgentID          *uuid.UUID
	Name             *string
	Instructions     *string
	PresetKey        NullableTextChange
	Trigger          *Trigger
	Sources          SourceChange
	ReviewDigest     string
}

type RevisionInput struct {
	ExpectedRevision int64
}

type ReviewRevisionInput struct {
	ExpectedRevision int64
	ReviewDigest     string
}

type MutationResult struct {
	Schedule Response
}

type ListCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

type ListOptions struct {
	Limit     int
	After     *ListCursor
	Status    *Status
	PresetKey *string
}

type Page struct {
	Schedules  []Response
	NextCursor *ListCursor
}

type FiringTrigger string

const (
	FiringTriggerScheduled FiringTrigger = "scheduled"
	FiringTriggerManual    FiringTrigger = "manual"
)

type FiringDisposition string

const (
	FiringDispositionMaterialized   FiringDisposition = "materialized"
	FiringDispositionSuperseded     FiringDisposition = "superseded"
	FiringDispositionSkipped        FiringDisposition = "skipped"
	FiringDispositionNeedsAttention FiringDisposition = "needs_attention"
)

type Firing struct {
	ID              uuid.UUID         `json:"id"`
	CompanyID       uuid.UUID         `json:"companyId"`
	ScheduleID      uuid.UUID         `json:"scheduleId"`
	ScheduleVersion int64             `json:"scheduleVersion"`
	ConversationID  uuid.UUID         `json:"conversationId"`
	MessageID       *uuid.UUID        `json:"messageId"`
	MessageSeq      *int64            `json:"messageSeq"`
	RunID           *uuid.UUID        `json:"runId"`
	OccurrenceAt    time.Time         `json:"occurrenceAt"`
	Trigger         FiringTrigger     `json:"trigger"`
	Disposition     FiringDisposition `json:"disposition"`
	Reason          *string           `json:"reason"`
	RunStatus       *string           `json:"runStatus"`
	Output          *string           `json:"output"`
	CreatedAt       time.Time         `json:"createdAt"`
	UpdatedAt       time.Time         `json:"updatedAt"`
	Revision        int64             `json:"revision"`
}

type FiringCursor struct {
	OccurrenceAt time.Time
	ID           uuid.UUID
}

type FiringListOptions struct {
	Limit int
	After *FiringCursor
}

type FiringPage struct {
	Firings    []Firing
	NextCursor *FiringCursor
}

type RunNowInput struct {
	RequestKey uuid.UUID
}

type RunNowResult struct {
	Firing  Firing
	Created bool
}

func normalizePreviewInput(input PreviewInput) (PreviewInput, error) {
	if !util.ValidUUID(input.AgentID) {
		return PreviewInput{}, ErrInvalidInput
	}
	input.Name = protocol.TrimContractWhitespace(input.Name)
	input.Instructions = protocol.TrimContractWhitespace(input.Instructions)
	if !validName(input.Name) || !validInstructions(input.Instructions) {
		return PreviewInput{}, ErrInvalidInput
	}
	preset, ok := normalizeOptionalCode(input.PresetKey)
	if !ok {
		return PreviewInput{}, ErrInvalidInput
	}
	input.PresetKey = preset
	connectorIDs, ok := normalizeConnectorIDs(input.ConnectorIDs)
	if !ok {
		return PreviewInput{}, ErrInvalidInput
	}
	input.ConnectorIDs = connectorIDs
	return input, nil
}

func normalizeCreateInput(input CreateInput) (CreateInput, error) {
	if !util.ValidUUID(input.RequestKey) || !digestPattern.MatchString(input.ReviewDigest) {
		return CreateInput{}, ErrInvalidInput
	}
	preview, err := normalizePreviewInput(PreviewInput{
		AgentID: input.AgentID, Name: input.Name, Instructions: input.Instructions,
		PresetKey: input.PresetKey, Trigger: input.Trigger, ConnectorIDs: input.ConnectorIDs,
	})
	if err != nil {
		return CreateInput{}, err
	}
	if input.Status == "" {
		input.Status = StatusDraft
	}
	if input.Status != StatusDraft && input.Status != StatusActive {
		return CreateInput{}, ErrInvalidInput
	}
	input.AgentID, input.Name, input.Instructions = preview.AgentID, preview.Name, preview.Instructions
	input.PresetKey, input.Trigger, input.ConnectorIDs = preview.PresetKey, preview.Trigger, preview.ConnectorIDs
	return input, nil
}

func normalizeUpdateInput(input UpdateInput) (UpdateInput, error) {
	hasChange := input.AgentID != nil || input.Name != nil || input.Instructions != nil ||
		input.PresetKey.Present || input.Trigger != nil || input.Sources.Present
	if !validVersion(input.ExpectedRevision) || !hasChange || !digestPattern.MatchString(input.ReviewDigest) {
		return UpdateInput{}, ErrInvalidInput
	}
	if input.AgentID != nil && !util.ValidUUID(*input.AgentID) {
		return UpdateInput{}, ErrInvalidInput
	}
	if input.Name != nil {
		name := protocol.TrimContractWhitespace(*input.Name)
		if !validName(name) {
			return UpdateInput{}, ErrInvalidInput
		}
		input.Name = &name
	}
	if input.Instructions != nil {
		instructions := protocol.TrimContractWhitespace(*input.Instructions)
		if !validInstructions(instructions) {
			return UpdateInput{}, ErrInvalidInput
		}
		input.Instructions = &instructions
	}
	if !input.PresetKey.Present && input.PresetKey.Value != nil {
		return UpdateInput{}, ErrInvalidInput
	}
	if input.PresetKey.Present {
		preset, ok := normalizeOptionalCode(input.PresetKey.Value)
		if !ok {
			return UpdateInput{}, ErrInvalidInput
		}
		input.PresetKey.Value = preset
	}
	if !input.Sources.Present && input.Sources.ConnectorIDs != nil {
		return UpdateInput{}, ErrInvalidInput
	}
	if input.Sources.Present {
		connectors, ok := normalizeConnectorIDs(input.Sources.ConnectorIDs)
		if !ok {
			return UpdateInput{}, ErrInvalidInput
		}
		input.Sources.ConnectorIDs = connectors
	}
	return input, nil
}

func normalizeListOptions(options ListOptions) (ListOptions, error) {
	if options.Limit == 0 {
		options.Limit = defaultListLimit
	}
	if options.Limit < 1 || options.Limit > maximumListLimit ||
		options.After != nil && (options.After.CreatedAt.IsZero() || !util.ValidUUID(options.After.ID)) ||
		options.Status != nil && !validStatus(*options.Status) {
		return ListOptions{}, ErrInvalidInput
	}
	if options.PresetKey != nil {
		preset, ok := normalizeOptionalCode(options.PresetKey)
		if !ok || preset == nil {
			return ListOptions{}, ErrInvalidInput
		}
		options.PresetKey = preset
	}
	return options, nil
}

func normalizeFiringListOptions(options FiringListOptions) (FiringListOptions, error) {
	if options.Limit == 0 {
		options.Limit = defaultListLimit
	}
	if options.Limit < 1 || options.Limit > maximumListLimit ||
		options.After != nil && (options.After.OccurrenceAt.IsZero() || !util.ValidUUID(options.After.ID)) {
		return FiringListOptions{}, ErrInvalidInput
	}
	return options, nil
}

func normalizeRunNowInput(input RunNowInput) (RunNowInput, error) {
	if !util.ValidUUID(input.RequestKey) {
		return RunNowInput{}, ErrInvalidInput
	}
	return input, nil
}

func normalizeConnectorIDs(values []uuid.UUID) ([]uuid.UUID, bool) {
	if values == nil || len(values) > maximumSources {
		return nil, false
	}
	result := slices.Clone(values)
	seen := make(map[uuid.UUID]struct{}, len(result))
	for _, id := range result {
		if !util.ValidUUID(id) {
			return nil, false
		}
		if _, exists := seen[id]; exists {
			return nil, false
		}
		seen[id] = struct{}{}
	}
	return result, true
}

func normalizeOptionalCode(value *string) (*string, bool) {
	if value == nil {
		return nil, true
	}
	normalized := protocol.TrimContractWhitespace(*value)
	if !safeCodePattern.MatchString(normalized) {
		return nil, false
	}
	return &normalized, true
}

func validName(value string) bool {
	return value != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= maximumNameRunes
}

func validInstructions(value string) bool {
	return value != "" && utf8.ValidString(value) &&
		utf8.RuneCountInString(value) <= maximumInstructionsRunes && len(value) <= maximumInstructionsBytes
}

func validStatus(value Status) bool {
	switch value {
	case StatusDraft, StatusActive, StatusPaused, StatusCompleted, StatusCancelled:
		return true
	default:
		return false
	}
}

func validVersion(value int64) bool {
	return value >= 1 && value <= maximumSafeInteger
}

func validPrincipal(principal auth.UserPrincipal) bool {
	return util.ValidUUID(principal.UserID) && util.ValidUUID(principal.SessionID) &&
		util.ValidUUID(principal.CompanyID) &&
		(principal.AuthKind == auth.TokenKindSession || principal.AuthKind == auth.TokenKindBearer) &&
		principal.Can(auth.PermissionSchedulesManage)
}

func computeReviewDigest(preview PreviewResult) (string, error) {
	canonical := struct {
		SchemaVersion int       `json:"schemaVersion"`
		AgentID       uuid.UUID `json:"agentId"`
		Provider      string    `json:"provider"`
		Name          string    `json:"name"`
		Instructions  string    `json:"instructions"`
		PresetKey     *string   `json:"presetKey"`
		Trigger       Trigger   `json:"trigger"`
		Sources       []Source  `json:"sources"`
	}{
		SchemaVersion: 1, AgentID: preview.AgentID, Provider: preview.ReviewedProvider,
		Name: preview.Name, Instructions: preview.Instructions, PresetKey: preview.PresetKey,
		Trigger: preview.Trigger, Sources: preview.Sources,
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", errors.New("encode Schedule review digest")
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
