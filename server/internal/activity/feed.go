package activity

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/util"
	db "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

// Feed reads the company activity feed and per-entity timelines.
type Feed struct {
	queries *db.Queries
}

// ListOptions contains the optional filters for one company feed read. The
// handler validates the closed vocabularies and half-open time window before
// calling Feed.List.
type ListOptions struct {
	UserID         uuid.UUID
	ActorType      string
	ActorID        string
	Revision       *int64
	BeforeRevision *int64
	Before         *Cursor
	Limit          int32
	From           *time.Time
	To             *time.Time
	EntityType     string
	EntityID       string
	Action         string
}

type CalendarOptions struct {
	UserID   uuid.UUID
	Month    string
	TimeZone string
	Location *time.Location
	StartAt  time.Time
	EndAt    time.Time
}

type Calendar struct {
	Month      string           `json:"month"`
	TimeZone   string           `json:"timeZone"`
	StartAt    time.Time        `json:"startAt"`
	EndAt      time.Time        `json:"endAt"`
	TotalCount int64            `json:"totalCount"`
	Days       map[string]int64 `json:"days"`
}

// NewFeed returns a Feed over the generated queries.
func NewFeed(queries *db.Queries) *Feed {
	return &Feed{queries: queries}
}

// List returns the newest matching company activity, optionally keyset-paged
// with the (createdAt, id) cursor of the last row the client has. The returned
// cursor is non-nil only when the overfetch row proves another page exists.
func (f *Feed) List(
	ctx context.Context,
	companyID uuid.UUID,
	options ListOptions,
) ([]Entry, *Cursor, error) {
	params := db.ListActivityParams{
		CompanyID: companyID, UserID: options.UserID,
		ManagementRoles: auth.RolesWith(auth.PermissionProjectsManage),
		MailboxRoles:    auth.RolesWith(auth.PermissionMailboxManage),
		HasCursor:       options.Before != nil, HasFrom: options.From != nil, HasTo: options.To != nil,
		EntityType: options.EntityType, EntityID: options.EntityID, Action: options.Action,
		ActorType: options.ActorType, ActorID: options.ActorID,
		HasRevision: options.Revision != nil, HasBeforeRevision: options.BeforeRevision != nil,
		RevisionOrder: options.Revision != nil || options.BeforeRevision != nil,
		PageSize:      options.Limit + 1,
	}
	if options.Revision != nil {
		params.Revision = *options.Revision
	}
	if options.BeforeRevision != nil {
		params.BeforeRevision = *options.BeforeRevision
	}
	if options.Before != nil {
		params.BeforeCreatedAt = pgtype.Timestamptz{Time: options.Before.CreatedAt.UTC(), Valid: true}
		params.BeforeID = options.Before.ID
		params.CursorRevision = options.Before.Revision
	}
	if options.From != nil {
		params.FromCreatedAt = pgtype.Timestamptz{Time: options.From.UTC(), Valid: true}
	}
	if options.To != nil {
		params.ToCreatedAt = pgtype.Timestamptz{Time: options.To.UTC(), Valid: true}
	}
	rows, err := f.queries.ListActivity(ctx, params)
	if err != nil {
		return nil, nil, err
	}
	rows, next := activityPage(rows, options.Limit)
	return entries(rows), next, nil
}

// Calendar returns a dense month of activity counts grouped by calendar day
// in the requested IANA location. The SQL range stays on raw created_at so the
// company/created_at index can bound the scan; only the bounded rows are
// converted to local dates for grouping.
func (f *Feed) Calendar(
	ctx context.Context,
	companyID uuid.UUID,
	options CalendarOptions,
) (Calendar, error) {
	rows, err := f.queries.CountActivityByLocalDay(ctx, db.CountActivityByLocalDayParams{
		TimeZone:        options.TimeZone,
		CompanyID:       companyID,
		UserID:          options.UserID,
		ManagementRoles: auth.RolesWith(auth.PermissionProjectsManage),
		MailboxRoles:    auth.RolesWith(auth.PermissionMailboxManage),
		StartAt:         pgtype.Timestamptz{Time: options.StartAt.UTC(), Valid: true},
		EndAt:           pgtype.Timestamptz{Time: options.EndAt.UTC(), Valid: true},
	})
	if err != nil {
		return Calendar{}, err
	}
	days := make(map[string]int64, 31)
	localStart := options.StartAt.In(options.Location)
	localEnd := options.EndAt.In(options.Location)
	for day := localStart; day.Before(localEnd); day = day.AddDate(0, 0, 1) {
		days[day.Format(time.DateOnly)] = 0
	}
	var total int64
	for _, row := range rows {
		days[row.Day] = row.ActivityCount
		total += row.ActivityCount
	}
	return Calendar{
		Month: options.Month, TimeZone: options.TimeZone,
		StartAt: options.StartAt.UTC(), EndAt: options.EndAt.UTC(),
		TotalCount: total, Days: days,
	}, nil
}

// Cursor is the keyset pagination position for the feed reads.
type Cursor struct {
	Revision  int64
	CreatedAt time.Time
	ID        uuid.UUID
}

func activityPage(rows []db.ActivityEvent, limit int32) ([]db.ActivityEvent, *Cursor) {
	if limit <= 0 || len(rows) <= int(limit) {
		return rows, nil
	}
	rows = rows[:limit]
	last := rows[len(rows)-1]
	return rows, &Cursor{CreatedAt: last.CreatedAt.Time, ID: last.ID, Revision: last.Revision.Int64}
}

func entries(rows []db.ActivityEvent) []Entry {
	out := make([]Entry, 0, len(rows))
	for _, row := range rows {
		out = append(out, entryOf(row))
	}
	return out
}

// entryOf maps only stored evidence. Renaming or deleting a live subject or
// actor cannot rewrite the labels a person saw when the action happened.
func entryOf(row db.ActivityEvent) Entry {
	var details struct {
		Context historyContext `json:"context"`
	}
	_ = json.Unmarshal(row.Details, &details)
	var revision *int64
	if row.Revision.Valid {
		revision = &row.Revision.Int64
	}
	var onBehalfOf *PersonRef
	if row.OnBehalfOfUserID.Valid {
		onBehalfOf = &PersonRef{
			ID: row.OnBehalfOfUserID.UUID.String(), DisplayName: details.Context.OnBehalfOfDisplayName,
		}
	}
	var runID *string
	if row.RunID.Valid {
		id := row.RunID.UUID.String()
		runID = &id
	}
	return Entry{
		CompanyID: row.CompanyID, VisibilityUserID: row.VisibilityUserID,
		ID: row.ID.String(), Action: row.Action,
		Actor:      ActorRef{ID: util.TextToPtr(row.ActorID), Kind: row.ActorType, DisplayName: details.Context.ActorDisplayName},
		OnBehalfOf: onBehalfOf, RunID: runID,
		Entity:   EntityRef{ID: row.EntityID, Type: protocol.EntityType(row.EntityType), Key: details.Context.EntityKey, Title: details.Context.EntityTitle},
		Revision: revision, Details: withoutMachines(row.EntityType, row.Details), CreatedAt: row.CreatedAt.Time.UTC(),
	}
}

// withoutMachines keeps members' machines out of the entries every member of
// a row's audience reads: a runtime is its registrant's alone (ADR 0014). A
// Run's before/after state and a Run receipt's snapshot withhold its
// runtimeId, and an Agent receipt stored before its frame dropped the acting
// member's setup loses that binding, and the runtime an Agent named before
// bindings existed. Anything else reads back as stored.
func withoutMachines(entityType string, details []byte) json.RawMessage {
	var fields map[string]json.RawMessage
	if (entityType != "run" && entityType != "agent") || json.Unmarshal(details, &fields) != nil {
		return details
	}
	if entityType == "run" {
		editObject(fields, "before", withoutRuntimeID)
		editObject(fields, "after", withoutRuntimeID)
		editObject(fields, "payload", withoutRuntimeID)
	} else {
		editObject(fields, "payload", func(agent map[string]json.RawMessage) {
			if _, ok := agent["binding"]; ok {
				agent["binding"] = json.RawMessage("null")
			}
			delete(agent, "runtime")
		})
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return details
	}
	return encoded
}

func withoutRuntimeID(state map[string]json.RawMessage) {
	if _, ok := state["runtimeId"]; ok {
		state["runtimeId"] = json.RawMessage("null")
	}
}

// editObject applies edit to the object under key; an absent or null value
// is left as it is.
func editObject(fields map[string]json.RawMessage, key string, edit func(map[string]json.RawMessage)) {
	var object map[string]json.RawMessage
	if json.Unmarshal(fields[key], &object) != nil || object == nil {
		return
	}
	edit(object)
	if encoded, err := json.Marshal(object); err == nil {
		fields[key] = encoded
	}
}

// ActorRef is the resolved actor embedded in an activity entry. System
// activity has no concrete identity, so its ID and display name are nil.
type ActorRef struct {
	ID          *string `json:"id"`
	Kind        string  `json:"kind"`
	DisplayName *string `json:"displayName"`
}

// PersonRef is the person an Agent acted for in a Run (D31), with their
// display name as it read when the row was written.
type PersonRef struct {
	ID          string  `json:"id"`
	DisplayName *string `json:"displayName"`
}

// EntityRef is the activity subject with the optional labels its entity type
// can resolve inside the row's Company.
type EntityRef struct {
	ID    string              `json:"id"`
	Type  protocol.EntityType `json:"type"`
	Key   *string             `json:"key"`
	Title *string             `json:"title"`
}

// Entry is one activity feed row shaped for clients and the exact payload of
// activity.created.
type Entry struct {
	Revision         *int64        `json:"revision"`
	VisibilityUserID uuid.NullUUID `json:"-"`
	ID               string        `json:"id"`
	Action           string        `json:"action"`
	Actor            ActorRef      `json:"actor"`
	// OnBehalfOf and RunID name the person an Agent acted for and the Run
	// it acted in (D31); both are null on every other row, including rows
	// written before Activity stored them.
	OnBehalfOf *PersonRef      `json:"onBehalfOf"`
	RunID      *string         `json:"runId"`
	Entity     EntityRef       `json:"entity"`
	Details    json.RawMessage `json:"details"`
	CreatedAt  time.Time       `json:"createdAt"`

	// CompanyID is the routing fact: a feed row belongs to exactly one
	// company, and the realtime bridge reads it off the Go value. It stays
	// off the wire — a client watching a company's feed already knows which
	// company it is watching.
	CompanyID uuid.UUID `json:"-"`

	// ProjectFrame carries the frame's audience when the row names a Project
	// or is Task work that names none (entryAudience, ADR 0017). Feed reads
	// leave it empty.
	auth.ProjectFrame
}

// The router reads the audience by interface; the compiler checks it.
var _ auth.ProjectAudienceCarrier = Entry{}
