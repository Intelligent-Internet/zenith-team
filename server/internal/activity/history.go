package activity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/events"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/redact"
)

// Snapshot is a safe, complete owner-state projection read under the mutation's
// existing locks. It is not a public live-entity DTO or a replayable database row.
type Snapshot struct {
	EntityType       string
	EntityID         uuid.UUID
	CompanyID        uuid.UUID
	Revision         pgtype.Int8
	Visibility       string
	VisibilityUserID uuid.NullUUID
	State            json.RawMessage
	Context          json.RawMessage
	// ProjectID is the Project the subject belongs to (ADR 0017), stored with
	// the Activity row so its readers follow the Project.
	ProjectID uuid.NullUUID
}

// ObserveInTx captures the subject before a mutation. The caller holds the
// owner's locks and passes the same transaction-bound query handle to RecordInTx.
// A missing subject is nil so creation and deletion use the same seam.
func ObserveInTx(ctx context.Context, q *generated.Queries, entityType string, entityID uuid.UUID) (*Snapshot, error) {
	row, err := q.GetActivitySubjectState(ctx, generated.GetActivitySubjectStateParams{
		EntityType: entityType, EntityID: entityID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("observe %s history: %w", entityType, err)
	}
	return snapshotOf(generated.ActivitySubjectState(row)), nil
}

func snapshotOf(row generated.ActivitySubjectState) *Snapshot {
	return &Snapshot{
		EntityType: row.EntityType, EntityID: row.EntityID, CompanyID: row.CompanyID,
		Revision: row.Revision, Visibility: row.Visibility, VisibilityUserID: row.VisibilityUserID,
		State: row.State, Context: row.Context, ProjectID: row.ProjectID,
	}
}

// RecordInTx appends the actual before/after change in the owner's transaction.
// An insertion failure must propagate to the transaction owner. The staged
// activity.created event may only be published after that transaction commits.
func RecordInTx(ctx context.Context, q *generated.Queries, pending *[]events.Event, actor events.Actor,
	action string, before *Snapshot, entityType string, entityID uuid.UUID,
) error {
	after, err := ObserveInTx(ctx, q, entityType, entityID)
	if err != nil {
		return err
	}
	return appendChange(ctx, q, pending, actor, action, before, after)
}

func appendChange(ctx context.Context, q *generated.Queries, pending *[]events.Event, actor events.Actor,
	action string, before, after *Snapshot,
) error {
	subject := after
	if subject == nil {
		subject = before
	}
	if subject == nil {
		return nil
	}
	if before != nil && after != nil {
		if before.CompanyID != after.CompanyID || before.EntityID != after.EntityID ||
			before.EntityType != after.EntityType {
			return errors.New("history subject changed during mutation")
		}
		if bytes.Equal(before.State, after.State) {
			return nil
		}
		if before.Revision.Valid && (!after.Revision.Valid || after.Revision.Int64 <= before.Revision.Int64) {
			return errors.New("history mutation did not advance its subject revision")
		}
	}
	if !events.ValidEventName(action) || !strings.HasPrefix(action, subject.EntityType+".") {
		return errors.New("history action does not name its subject")
	}
	revision := subject.Revision
	if after == nil && revision.Valid {
		revision.Int64++
	}
	if revision.Valid && (revision.Int64 < 1 || revision.Int64 > 9007199254740991) {
		return errors.New("history subject revision exhausted")
	}
	switch actor.Type {
	case events.ActorTypeSystem:
		if actor.ID != "" {
			return errors.New("system history actor has an identity")
		}
	case events.ActorTypeUser, events.ActorTypeAgent:
		id, err := uuid.Parse(actor.ID)
		if err != nil || id == uuid.Nil || id.String() != actor.ID {
			return errors.New("history actor identity is invalid")
		}
	default:
		return errors.New("history actor type is invalid")
	}
	onBehalfOf, run, err := actorAttribution(actor)
	if err != nil {
		return err
	}
	var labels historyContext
	if err := json.Unmarshal(subject.Context, &labels); err != nil {
		return fmt.Errorf("history labels: %w", err)
	}
	if err := labels.captureActor(ctx, q, subject.CompanyID, actor); err != nil {
		return err
	}
	details := snapshotDetails{SchemaVersion: snapshotSchemaVersion, Context: labels}
	if before != nil {
		details.Before = before.State
	}
	if after != nil {
		details.After = after.State
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("marshal history change: %w", err)
	}
	row, err := q.CreateActivity(ctx, generated.CreateActivityParams{
		CompanyID:        subject.CompanyID,
		ActorType:        string(actor.Type),
		ActorID:          optionalText(actor.ID),
		Action:           action,
		EntityType:       subject.EntityType,
		EntityID:         subject.EntityID.String(),
		Revision:         revision,
		Visibility:       optionalText(subject.Visibility),
		VisibilityUserID: subject.VisibilityUserID,
		SchemaVersion:    snapshotSchemaVersion,
		Details:          redact.JSON(encoded),
		ProjectID:        subject.ProjectID,
		OnBehalfOfUserID: onBehalfOf,
		RunID:            run,
	})
	if err != nil {
		return fmt.Errorf("append %s history: %w", action, err)
	}
	// Internal evidence has no public audience; it must not become an
	// activity.created frame merely because its user recipient is null.
	if pending != nil && row.Visibility.Valid {
		created, err := createdEventOf(ctx, q, row, actor)
		if err != nil {
			return err
		}
		*pending = append(*pending, created)
	}
	return nil
}

type historyContext struct {
	ActorDisplayName *string `json:"actorDisplayName"`
	// OnBehalfOfDisplayName is the name of the person an Agent acted for in a
	// Run, frozen like the actor's (D31). Every other row leaves it out.
	OnBehalfOfDisplayName *string `json:"onBehalfOfDisplayName,omitempty"`
	EntityKey             *string `json:"entityKey"`
	EntityTitle           *string `json:"entityTitle"`
}

// captureActor freezes the actor's display name and, for an Agent acting in
// a Run, the name of the person it acted for, as both read when the row is
// written.
func (labels *historyContext) captureActor(
	ctx context.Context,
	q *generated.Queries,
	companyID uuid.UUID,
	actor events.Actor,
) error {
	var err error
	if labels.ActorDisplayName, err = actorDisplayName(ctx, q, companyID, actor); err != nil {
		return err
	}
	labels.OnBehalfOfDisplayName = nil
	if actor.OnBehalfOf == "" {
		return nil
	}
	labels.OnBehalfOfDisplayName, err = actorDisplayName(ctx, q, companyID, events.UserActor(actor.OnBehalfOf))
	return err
}

// actorAttribution is the person an Agent acted for and the Run it acted in
// (D31), as the two Activity columns. Both are NULL for a person, the
// platform, and an Agent that names no Run.
func actorAttribution(actor events.Actor) (onBehalfOf, run uuid.NullUUID, err error) {
	if !actor.Valid() {
		return uuid.NullUUID{}, uuid.NullUUID{}, errors.New("history actor attribution is invalid")
	}
	if onBehalfOf, err = optionalUUID(actor.OnBehalfOf); err != nil {
		return uuid.NullUUID{}, uuid.NullUUID{}, err
	}
	if run, err = optionalUUID(actor.RunID); err != nil {
		return uuid.NullUUID{}, uuid.NullUUID{}, err
	}
	return onBehalfOf, run, nil
}

func optionalUUID(value string) (uuid.NullUUID, error) {
	if value == "" {
		return uuid.NullUUID{}, nil
	}
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil || id.String() != value {
		return uuid.NullUUID{}, errors.New("history actor attribution is invalid")
	}
	return uuid.NullUUID{UUID: id, Valid: true}, nil
}

type snapshotDetails struct {
	SchemaVersion int             `json:"schemaVersion"`
	Before        json.RawMessage `json:"before"`
	After         json.RawMessage `json:"after"`
	Context       historyContext  `json:"context"`
}

func actorDisplayName(
	ctx context.Context,
	q *generated.Queries,
	companyID uuid.UUID,
	actor events.Actor,
) (*string, error) {
	if actor.Type == events.ActorTypeSystem {
		return nil, nil
	}
	name, err := q.GetActivityActorDisplayName(ctx, generated.GetActivityActorDisplayNameParams{
		CompanyID: companyID, ActorType: string(actor.Type), ActorID: actor.ID,
	})
	if err != nil {
		return nil, fmt.Errorf("capture history actor label: %w", err)
	}
	if name == "" {
		return nil, nil
	}
	return &name, nil
}

func optionalText(value string) pgtype.Text { return pgtype.Text{String: value, Valid: value != ""} }

// BaselineInTx records observed starting states before serving traffic after the
// cutover. It is safe to resume; it never invents earlier source revisions or
// creation events. The advisory lock serializes concurrent server initializers.
func BaselineInTx(ctx context.Context, q *generated.Queries) error {
	if err := q.LockActivityBaselines(ctx); err != nil {
		return fmt.Errorf("lock history baseline: %w", err)
	}
	rows, err := q.ListActivitySubjectsWithoutHistory(ctx)
	if err != nil {
		return fmt.Errorf("read history baseline subjects: %w", err)
	}
	for _, row := range rows {
		if err := appendChange(
			ctx,
			q,
			nil,
			events.SystemActor(),
			row.EntityType+".history_baselined",
			nil,
			snapshotOf(generated.ActivitySubjectState(row)),
		); err != nil {
			return err
		}
	}
	return nil
}
