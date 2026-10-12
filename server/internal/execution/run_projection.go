package execution

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/util"
	db "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
	"boardly.dev/boardly/server/pkg/runfailure"
)

// ErrRunNotFound hides both absent runs and runs outside the caller's visible
// company/conversation scope.
var ErrRunNotFound = errors.New("execution: run not found")

// ErrRunCancelPermissionRequired refuses a stop by a member who can read the
// Run but neither authorized it — it runs on its authorizer's computer
// (ADR 0014) — nor holds tasks.assign.
var ErrRunCancelPermissionRequired = errors.New("execution: stopping a run needs its authorizer or tasks.assign")

// RunView is the REST read model: the snapshot plus the raw provider failure
// text, which an authorized reader may see and a browser frame may not.
// Embedding is what keeps the two from drifting field by field.
type RunView struct {
	RunSnapshot
	Error           *string          `json:"error"`
	Usage           *RunUsageSummary `json:"usage"`
	UsageRestricted bool             `json:"usageRestricted"`
}

// RunUsageSummary is the per-Run usage evidence visible to a Run reader. The
// counters stay decimal strings so large provider totals never lose precision
// in a browser number.
type RunUsageSummary struct {
	InputTokens      string  `json:"inputTokens"`
	OutputTokens     string  `json:"outputTokens"`
	CacheReadTokens  string  `json:"cacheReadTokens"`
	CacheWriteTokens string  `json:"cacheWriteTokens"`
	CostUSDTicks     *string `json:"costUsdTicks"`
}

// Cursor is the stable created-at/id boundary for descending run pagination.
type Cursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// ListOptions selects one page of runs visible to the supplied principal.
type ListOptions struct {
	Before         *Cursor
	Limit          int32
	Statuses       []string
	ActiveOnly     bool
	TaskID         *uuid.UUID
	ConversationID *uuid.UUID
	AgentID        *uuid.UUID
	RuntimeID      *uuid.UUID
	RunType        string
	Revision       *int64
	From           *time.Time
	To             *time.Time
}

// Page is one descending page of authorized run projections.
type Page struct {
	Items []RunView
	Next  *Cursor
}

// TranscriptEntry is one durable provider-normalized item in a Conversation
// transcript. RunID identifies which execution produced it; Seq orders the
// shared Conversation stream.
type TranscriptEntry struct {
	ID         uuid.UUID                    `json:"id"`
	RunID      uuid.UUID                    `json:"runId"`
	Seq        int64                        `json:"seq"`
	ItemKey    string                       `json:"itemKey"`
	Role       protocol.RunItemRole         `json:"role"`
	Parts      []protocol.RunTranscriptPart `json:"parts"`
	ItemDigest string                       `json:"itemDigest"`
	CreatedAt  time.Time                    `json:"createdAt"`
}

func (s *DefaultRunService) List(
	ctx context.Context,
	principal auth.UserPrincipal,
	options ListOptions,
) (Page, error) {
	params := projectionParams(principal.CompanyID, principal.UserID, true)
	params.HasStatuses = len(options.Statuses) > 0
	params.Statuses = options.Statuses
	params.ActiveOnly = options.ActiveOnly
	params.HasTaskID = options.TaskID != nil
	params.TaskID = uuidValue(options.TaskID)
	params.HasConversationID = options.ConversationID != nil
	params.ConversationID = uuidValue(options.ConversationID)
	params.HasAgentID = options.AgentID != nil
	params.FilterAgentID = uuidValue(options.AgentID)
	params.HasRuntimeID = options.RuntimeID != nil
	params.FilterRuntimeID = uuidValue(options.RuntimeID)
	params.HasRunType = options.RunType != ""
	params.FilterRunType = options.RunType
	params.HasRevision = options.Revision != nil
	if options.Revision != nil {
		params.FilterRevision = *options.Revision
	}
	params.HasFrom = options.From != nil
	if options.From != nil {
		params.FromCreatedAt = pgtype.Timestamptz{Time: *options.From, Valid: true}
	}
	params.HasTo = options.To != nil
	if options.To != nil {
		params.ToCreatedAt = pgtype.Timestamptz{Time: *options.To, Valid: true}
	}
	params.HasCursor = options.Before != nil
	if options.Before != nil {
		params.BeforeCreatedAt = pgtype.Timestamptz{Time: options.Before.CreatedAt, Valid: true}
		params.BeforeID = options.Before.ID
	}
	params.PageSize = options.Limit + 1

	rows, err := s.Queries.ListRunProjections(ctx, params)
	if err != nil {
		return Page{}, fmt.Errorf("list visible runs: %w", err)
	}
	views := MapRunProjectionRows(rows, principal.UserID)
	page := Page{Items: views}
	if len(views) > int(options.Limit) {
		page.Items = views[:options.Limit]
		boundary := page.Items[len(page.Items)-1]
		page.Next = &Cursor{CreatedAt: boundary.CreatedAt, ID: boundary.ID}
	}
	return page, nil
}

func (s *DefaultRunService) Get(
	ctx context.Context,
	principal auth.UserPrincipal,
	runID uuid.UUID,
) (RunView, error) {
	params := projectionParams(principal.CompanyID, principal.UserID, true)
	params.HasRunID = true
	params.RunID = runID
	params.PageSize = 1
	rows, err := s.Queries.ListRunProjections(ctx, params)
	if err != nil {
		return RunView{}, fmt.Errorf("get visible run: %w", err)
	}
	if len(rows) == 0 {
		return RunView{}, ErrRunNotFound
	}
	return mapRunView(rows[0], principal.UserID), nil
}

func (s *DefaultRunService) Cancel(
	ctx context.Context,
	principal auth.UserPrincipal,
	runID uuid.UUID,
	reason string,
) (RunView, error) {
	if _, err := s.Get(ctx, principal, runID); err != nil {
		return RunView{}, err
	}
	if _, err := s.cancelRun(ctx, runID, reason, &principal); err != nil {
		return RunView{}, err
	}
	return s.Get(ctx, principal, runID)
}

// MapTranscriptRows maps trusted sqlc conversation-item rows into the
// canonical execution projection. Conversation transcript pages and composite
// readers use this pure seam instead of duplicating the provider-item mapping.
//
// It maps every row it is given. The Conversation stream also contains each
// Run's triggering 'user_message', which has no provider parts, but transcript
// queries exclude it. This function refuses such a row rather than silently
// shrinking a page and leaving its cursor pointing at an item the caller never
// received.
func MapTranscriptRows(rows []db.ConversationItem) ([]TranscriptEntry, error) {
	items := make([]TranscriptEntry, 0, len(rows))
	for _, row := range rows {
		if len(row.Parts) == 0 {
			return nil, fmt.Errorf(
				"transcript item %s (type %q) has no parts: a transcript read returned "+
					"the Run's own user_message",
				row.ID,
				row.Type,
			)
		}
		var parts []protocol.RunItemPart
		if err := json.Unmarshal(row.Parts, &parts); err != nil {
			return nil, fmt.Errorf("decode transcript item %s parts: %w", row.ID, err)
		}
		items = append(items, TranscriptEntry{
			ID:         row.ID,
			RunID:      row.RunID.UUID,
			Seq:        row.Seq,
			ItemKey:    row.ItemKey.String,
			Role:       transcriptEntryRole(row.Type),
			Parts:      transcriptParts(parts),
			ItemDigest: hex.EncodeToString(row.ContentSha256),
			CreatedAt:  row.CreatedAt.Time.UTC(),
		})
	}
	return items, nil
}

// transcriptEntryRole recovers the reader-facing author role from the item
// type the one stream stores.
//
// The stored row carries `type`, not `role`: type is what decides visibility,
// so it is what the schema keeps, and the role the daemon sent survives only
// inside the item's content digest. That makes this a derivation, not a
// round trip — a provider that labeled an error item 'system' reads back as
// 'agent'. Nothing branches on the difference; the wire uses it as a display
// label. If a reader ever needs the original, the role has to be stored,
// which means writing it into the item's metadata at append time.
func transcriptEntryRole(itemType string) protocol.RunItemRole {
	if itemType == transcriptTypeToolResult {
		return protocol.RunItemRoleTool
	}
	return protocol.RunItemRoleAgent
}

func projectionParams(companyID, userID uuid.UUID, enforceVisibility bool) db.ListRunProjectionsParams {
	return db.ListRunProjectionsParams{
		CompanyID: companyID, UserID: userID, EnforceVisibility: enforceVisibility,
		Statuses: []string{}, UsageRoles: auth.RolesWith(auth.PermissionUsageRead),
	}
}

func loadRunView(
	ctx context.Context,
	q *db.Queries,
	run db.Run,
) (RunView, error) {
	params := projectionParams(run.CompanyID, uuid.Nil, false)
	params.HasRunID = true
	params.RunID = run.ID
	params.PageSize = 1
	rows, err := q.ListRunProjections(ctx, params)
	if err != nil {
		return RunView{}, fmt.Errorf("load run projection: %w", err)
	}
	if len(rows) == 0 {
		return RunView{}, ErrRunNotFound
	}
	// A lifecycle frame has no one reader, so it names no machine.
	view := mapRunView(rows[0], uuid.Nil)
	if view.TaskScoped() {
		// Every view this loads becomes a frame: a Task chat's Run reaches
		// the readers of the chat's Project (ADR 0017).
		view.Audience, err = auth.ConversationFrameAudienceInTx(ctx, q, run.CompanyID, run.ConversationID)
		if err != nil {
			return RunView{}, fmt.Errorf("decide Run frame audience: %w", err)
		}
	}
	return view, nil
}

// LoadRunSnapshotInTx loads the canonical lifecycle projection
// for a Run already mutated by a cross-domain transaction owner.
func LoadRunSnapshotInTx(
	ctx context.Context,
	q *db.Queries,
	run db.Run,
) (RunSnapshot, error) {
	view, err := loadRunView(ctx, q, run)
	if err != nil {
		return RunSnapshot{}, err
	}
	return view.RunSnapshot, nil
}

// MapRunProjectionRows maps sqlc projection rows already loaded by a caller
// that owns the transaction into the execution domain's user-plane view for
// the viewer who asked.
func MapRunProjectionRows(rows []db.ListRunProjectionsRow, viewer uuid.UUID) []RunView {
	views := make([]RunView, 0, len(rows))
	for _, row := range rows {
		views = append(views, mapRunView(row, viewer))
	}
	return views
}

// ViewerRuntimeID is a runtime id as one reader may see it: their own
// machine, never another member's (ADR 0014). The registrant is the
// runtime's owner; a reader of uuid.Nil — a Company-wide frame — is nobody's
// registrant, so it is shown no machine.
func ViewerRuntimeID(runtimeID uuid.UUID, registrant uuid.NullUUID, viewer uuid.UUID) *uuid.UUID {
	if viewer == uuid.Nil || !registrant.Valid || registrant.UUID != viewer {
		return nil
	}
	return &runtimeID
}

func mapRunView(row db.ListRunProjectionsRow, viewer uuid.UUID) RunView {
	var usage *RunUsageSummary
	if len(row.Usage) > 0 && string(row.Usage) != "null" {
		var decoded RunUsageSummary
		if err := json.Unmarshal(row.Usage, &decoded); err == nil {
			usage = &decoded
		}
	}
	return RunView{
		RunSnapshot: RunSnapshot{
			ID:                      row.ID,
			RunType:                 row.RunType,
			Revision:                int8Ptr(row.Revision),
			Source:                  json.RawMessage(row.Source),
			CompanyID:               row.CompanyID,
			AgentID:                 row.AgentID,
			Agent:                   NamedRef{ID: row.AgentID, Name: row.AgentName},
			TaskID:                  util.NullUUIDPtr(row.TaskID),
			TaskExecutionGeneration: int8Ptr(row.TaskExecutionGeneration),
			ConversationID:          row.ConversationID,
			RuntimeID:               row.RuntimeID,
			ViewerRuntimeID:         ViewerRuntimeID(row.RuntimeID, row.RuntimeOwnerUserID, viewer),
			Provider:                row.RuntimeProvider,
			Status:                  row.Status,
			Attempt:                 row.Attempt,
			MaxAttempts:             MaxAttemptsFor(row.FailureReason.String, row.Attempt),
			ParentRunID:             util.NullUUIDPtr(row.ParentRunID),
			RetryQueued:             row.RetryQueued,
			TriggerMessageID:        triggerItemPtr(row.TriggerItemID),
			TranscriptThroughSeq:    row.TranscriptThroughSeq,
			FailureReason: util.TextToPtr(
				row.FailureReason,
			),
			FailureMessage: failureMessagePtr(row.FailureReason, row.RuntimeProvider),
			FailureDetail:  failureDetailPtr(row.FailureReason, row.Error),
			WaitReason:     util.TextToPtr(row.WaitReason),
			CreatedAt:      row.CreatedAt.Time.UTC(),
			UpdatedAt:      row.UpdatedAt.Time.UTC(),
			FireAt:         util.TimestampToTime(row.FireAt),
			QueuedAt:       util.TimestampToTime(row.QueuedAt),
			ClaimedAt:      util.TimestampToTime(row.ClaimedAt),
			StartedAt:      util.TimestampToTime(row.StartedAt),
			FinishedAt:     util.TimestampToTime(row.FinishedAt),
			DurationMs:     runDurationMs(row.StartedAt, row.FinishedAt),

			ConversationRoute: ConversationRoute{
				ConversationKind:      row.ConversationKind,
				ConversationCreatedBy: util.NullUUIDPtr(row.ConversationCreatedByUserID),
			},
		},
		Error:           util.TextToPtr(row.Error),
		Usage:           usage,
		UsageRestricted: row.UsageRestricted.Bool,
	}
}

func uuidValue(value *uuid.UUID) uuid.UUID {
	if value == nil {
		return uuid.Nil
	}
	return *value
}

func int8Ptr(value pgtype.Int8) *int64 {
	if !value.Valid {
		return nil
	}
	return &value.Int64
}

// triggerItemPtr reads the projection's trigger item id, which is the id of
// the Run's own first 'user_message' item or absent.
//
// It tests against uuid.Nil rather than a null wrapper because the projection
// reaches the item through a LEFT JOIN LATERAL and sqlc does not infer
// nullability through one: the generated column is a bare uuid.UUID even
// though the join misses for every retry child and every assignment Run. The
// nil id is that miss.
func triggerItemPtr(value uuid.UUID) *uuid.UUID {
	if value == uuid.Nil {
		return nil
	}
	return &value
}

// NamedRef is an id with the display name a reader would otherwise have to
// join for.
type NamedRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// RunSnapshot is the Run's public projection: the payload every run
// lifecycle topic carries, and the body of the REST read model.
//
// Five fields never reach a browser. Agent and RetryQueued are read
// in-process by the activity feed's subscriber; ConversationKind and
// ConversationCreatedBy are read by the realtime bridge to decide who may see
// the Run at all; RuntimeID is read in-process to wake the daemon, and the
// wire carries ViewerRuntimeID instead.
type RunSnapshot struct {
	RunType   string          `json:"runType"`
	Revision  *int64          `json:"revision"`
	Source    json.RawMessage `json:"source"`
	ID        uuid.UUID       `json:"id"`
	CompanyID uuid.UUID       `json:"companyId"`
	AgentID   uuid.UUID       `json:"agentId"`
	// Agent is not serialized: the activity feed's in-process subscriber
	// renders from it, while wire clients join agentId against their roster.
	Agent                   NamedRef   `json:"-"`
	TaskID                  *uuid.UUID `json:"taskId"`
	TaskExecutionGeneration *int64     `json:"taskExecutionGeneration"`
	ConversationID          uuid.UUID  `json:"conversationId"`
	// RuntimeID is the runtime the Run executes on, for in-process readers
	// only. ViewerRuntimeID is the same id as the wire serves it: to the
	// runtime's registrant alone, null for every other reader and on every
	// lifecycle frame (ADR 0014).
	RuntimeID       uuid.UUID  `json:"-"`
	ViewerRuntimeID *uuid.UUID `json:"runtimeId"`
	// Provider is the runtime's provider (protocol.SupportedProviders),
	// carried so a surface can name it without joining the runtime roster.
	Provider    string     `json:"provider"`
	Status      string     `json:"status"`
	Attempt     int32      `json:"attempt"`
	MaxAttempts int32      `json:"maxAttempts"`
	ParentRunID *uuid.UUID `json:"parentRunId"`
	// RetryQueued is not serialized: whether a failed run earned a retry is
	// read from the child's parentRunId linkage. The activity feed's terminal
	// subscriber still consumes it in-process to suppress non-final attempts.
	RetryQueued bool `json:"-"`
	// TriggerMessageID stays a uuid and keeps its wire name: it is the id of
	// the Run's own first 'user_message' conversation item — the user's words
	// this Run was started on — which is still a uuid. It is absent for a
	// retry child and for an assignment Run, neither of which has words of
	// its own.
	TriggerMessageID     *uuid.UUID `json:"triggerMessageId"`
	TranscriptThroughSeq int64      `json:"transcriptThroughSeq"`
	FailureReason        *string    `json:"failureReason"`
	// FailureMessage is the user-facing sentence for FailureReason, owned by
	// pkg/runfailure and naming Provider. It rides the browser-safe snapshot
	// on purpose: a client can explain a failed run from the realtime event
	// alone, without the authorized read that carries the raw provider text.
	FailureMessage *string `json:"failureMessage"`
	// FailureDetail is the bounded, cleaned line pkg/runfailure.Detail makes
	// of the raw failure text — the provider's own words ("try again at …")
	// that the sentence cannot carry. Present exactly when FailureReason is.
	FailureDetail *string    `json:"failureDetail"`
	WaitReason    *string    `json:"waitReason"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
	FireAt        *time.Time `json:"fireAt"`
	QueuedAt      *time.Time `json:"queuedAt"`
	ClaimedAt     *time.Time `json:"claimedAt"`
	StartedAt     *time.Time `json:"startedAt"`
	FinishedAt    *time.Time `json:"finishedAt"`
	DurationMs    *int64     `json:"durationMs"`

	ConversationRoute
}

// ConversationRoute is the facts that decide who may see anything belonging
// to a conversation: its kind, its creator and, for a Task chat, its
// Project audience. It is embedded rather than repeated because every
// conversation-derived frame needs all three, and a frame that forgets one
// cannot be delivered.
//
// Kind is authoritative rather than a Run's TaskID: a plan apply that deletes
// a task nulls runs.task_id while the conversation survives and keeps kind
// 'task', so routing on TaskID would narrow a Project-wide Run to one user —
// and to a null user — at exactly that moment.
//
// A Task chat's frames reach the readers of its Project (ADR 0017), so for
// kind 'task' the producer also decides that audience in its transaction
// (ProjectFrame).
type ConversationRoute struct {
	ConversationKind      string     `json:"-"`
	ConversationCreatedBy *uuid.UUID `json:"-"`
	auth.ProjectFrame
}

// The router reads a Run frame's audience by interface; the compiler checks
// both carriers.
var (
	_ auth.ProjectAudienceCarrier = RunSnapshot{}
	_ auth.ProjectAudienceCarrier = TranscriptItem{}
)

func runDurationMs(start, finish pgtype.Timestamptz) *int64 {
	if !start.Valid || !finish.Valid {
		return nil
	}
	value := finish.Time.Sub(start.Time).Milliseconds()
	if value < 0 {
		value = 0
	}
	return &value
}

// TaskScoped reports whether the conversation is a Task's, and so read by
// its Project's readers (ADR 0017) rather than by one founder.
func (r ConversationRoute) TaskScoped() bool { return r.ConversationKind == "task" }

// failureMessagePtr renders the user-facing sentence for a run's failure
// reason, or nil when the run has not failed. The reason column is an open
// set, so an unrecognized value still produces a sentence rather than a gap.
func failureMessagePtr(reason pgtype.Text, provider string) *string {
	if !reason.Valid || reason.String == "" {
		return nil
	}
	message := runfailure.MessageFor(runfailure.Reason(reason.String), provider)
	return &message
}

// failureDetailPtr renders the bounded detail line for a failed run, or nil
// when the run has not failed or the failure text is empty.
func failureDetailPtr(reason, failureText pgtype.Text) *string {
	if !reason.Valid || reason.String == "" {
		return nil
	}
	detail := runfailure.Detail(failureText.String)
	if detail == "" {
		return nil
	}
	return &detail
}
