// Package activity persists Company action history. Owners capture Task,
// Conversation and Run changes in their mutation transaction. The Bus sink
// retains other domain events as explicitly unversioned evidence. Both readers
// and notifications use the visibility frozen with the stored row.
package activity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	db "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/redact"
)

// Every Activity row names the shape of its details twice: in the details
// envelope, where clients read it, and in the schema_version column, where SQL
// filters on it. Each writer sets both from these constants.
const (
	// evidenceSchemaVersion marks an event payload with no owner state.
	evidenceSchemaVersion = 0
	// snapshotSchemaVersion marks the before/after owner state captured in the
	// mutation's own transaction.
	snapshotSchemaVersion = 1
)

// RegisterListeners retains events whose owners have not adopted transactional
// snapshots. Covered actions are recorded by their owner, never copied twice.
func RegisterListeners(bus *events.Bus, queries *db.Queries) error {
	return bus.SubscribeAll(func(ctx context.Context, e events.Event) error { return record(ctx, bus, queries, e) })
}

func transactionallyCaptured(action string) bool {
	switch action {
	case "conversation.discussion_changed",
		"conversation.discussion_state_changed",
		"conversation.discussion_access_revoked",
		"task.created",
		"task.updated",
		"task.deleted",
		"conversation.created",
		"conversation.updated",
		"run.queued",
		"run.dispatched",
		"run.auth_wait_changed",
		"run.running",
		"run.waiting_local_directory",
		"run.completed",
		"run.failed",
		"run.cancelled":
		return true
	default:
		return false
	}
}

// transportHint identifies nudges that record no user action. Some carry private
// routing identities; retaining them as Company activity would widen access.
func transportHint(action string) bool {
	switch action {
	case "runtime.work_available", "runtime.pending_work", "runtime.capabilities_changed",
		"connector.policy_changed",
		"conversation.channel_agents_changed", "conversation.discussion_resync",
		"wiki.run_reads_changed", "schedule.changed", "calendar.changed",
		"canvas.changed", "canvas.document_changed", "canvas.page_preview_changed", "whiteboard.changed", "whiteboard.renamed":
		return true
	default:
		return false
	}
}

// deliveryHint names the opaque frames that only tell a browser to refetch
// something. They record nothing anyone did, and a row would tell the whole
// Company that a chat it may not read changed (ADR 0017).
func deliveryHint(action string) bool {
	return action == "message.updated"
}

func record(ctx context.Context, bus *events.Bus, queries *db.Queries, e events.Event) error {
	if e.EntityType == Topic || e.EntityType == "deliverable" || e.EntityType == "artifact" ||
		e.EntityType == "inbox" || e.EntityType == "notification" ||
		e.ActivityRecorded || transactionallyCaptured(e.Name) || transportHint(e.Name) ||
		deliveryHint(e.Name) {
		return nil
	}
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return fmt.Errorf("marshal %s evidence: %w", e.Name, err)
	}
	if e.Name == "message.created" {
		if payload, err = withoutMessageFiles(payload); err != nil {
			return err
		}
	}
	subject := subjectID(payload)
	entityType := e.EntityType
	switch e.Name {
	case "run.message_created":
		// The payload identifies the persisted Conversation item. The Run in
		// the envelope attributes execution; it is not the changed subject.
		entityType = "message"
	case "mailbox.changed":
		subject = e.CompanyID.String()
	case "mailbox.draft_changed":
		var draft struct {
			DraftID uuid.UUID `json:"draftId"`
		}
		if err := json.Unmarshal(payload, &draft); err != nil {
			return fmt.Errorf("read Mailbox draft subject: %w", err)
		}
		if draft.DraftID != uuid.Nil {
			subject = draft.DraftID.String()
		}
	}
	scope, err := queries.ResolveActivityScope(ctx, db.ResolveActivityScopeParams{
		CompanyID: e.CompanyID, EntityType: entityType, EntityID: subject,
		ConversationID: uuid.NullUUID{UUID: e.ConversationID, Valid: e.ConversationID != uuid.Nil},
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("resolve Activity visibility: %w", err)
	}
	// A Task chat's filing state belongs to the member, not to the shared
	// Conversation. Its revision orders user state and is never indexed as a
	// Conversation revision. Direct filing changes were captured in the owner.
	if e.Name == "conversation.read" {
		var state struct {
			ConversationID uuid.UUID `json:"conversationId"`
			UserID         uuid.UUID `json:"userId"`
		}
		if err := json.Unmarshal(payload, &state); err != nil {
			return fmt.Errorf("read Conversation filing actor: %w", err)
		}
		subject = state.ConversationID.String()
		scope.Visibility = ""
		scope.VisibilityUserID = uuid.NullUUID{}
		if state.UserID != uuid.Nil {
			scope.Visibility = "user"
			scope.VisibilityUserID = uuid.NullUUID{UUID: state.UserID, Valid: true}
		}
	}
	onBehalfOf, run, err := actorAttribution(e.Actor)
	if err != nil {
		return err
	}
	labels := historyContext{}
	if err := labels.captureActor(ctx, queries, e.CompanyID, e.Actor); err != nil {
		return err
	}
	details, err := json.Marshal(struct {
		SchemaVersion int             `json:"schemaVersion"`
		Payload       json.RawMessage `json:"payload"`
		Context       historyContext  `json:"context"`
	}{SchemaVersion: evidenceSchemaVersion, Payload: payload, Context: labels})
	if err != nil {
		return fmt.Errorf("marshal Activity evidence: %w", err)
	}
	// The envelope names the chat and the Task the event is about; the row
	// keeps both, and the Project they (or the subject) belong to decides who
	// reads the row (ADR 0017). A deletion's subject is gone, but its chat is
	// still on the envelope.
	project, err := subjectProject(ctx, queries, e.CompanyID, entityType, subject, e.ConversationID, e.TaskID)
	if err != nil {
		return err
	}
	row, err := queries.CreateActivity(ctx, db.CreateActivityParams{
		CompanyID: e.CompanyID, ActorType: string(e.Actor.Type), ActorID: optionalText(e.Actor.ID),
		Action: e.Name, EntityType: entityType, EntityID: subject,
		SchemaVersion: evidenceSchemaVersion, Details: redact.JSON(details),
		Visibility: optionalText(scope.Visibility), VisibilityUserID: scope.VisibilityUserID,
		TaskID:           uuid.NullUUID{UUID: e.TaskID, Valid: e.TaskID != uuid.Nil},
		ConversationID:   uuid.NullUUID{UUID: e.ConversationID, Valid: e.ConversationID != uuid.Nil},
		ProjectID:        project,
		OnBehalfOfUserID: onBehalfOf,
		RunID:            run,
	})
	if err != nil {
		return fmt.Errorf("record %s evidence: %w", e.Name, err)
	}
	if row.Visibility.Valid && row.CompanyID != uuid.Nil {
		created, err := createdEventOf(ctx, queries, row, e.Actor)
		if err != nil {
			return err
		}
		bus.Publish(ctx, Topic, created)
	}
	return nil
}

// withoutMessageFiles keeps a message's files out of its durable evidence,
// leaving only how many there were. Who sees a file changes when it joins or
// leaves a Project (ADR 0017); the chat's own list follows that change, a
// stored filename would not.
func withoutMessageFiles(payload []byte) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return nil, fmt.Errorf("read message evidence: %w", err)
	}
	files, ok := fields["attachments"]
	if !ok {
		return payload, nil
	}
	var list []json.RawMessage
	if err := json.Unmarshal(files, &list); err != nil {
		return nil, fmt.Errorf("read message evidence files: %w", err)
	}
	delete(fields, "attachments")
	count, err := json.Marshal(len(list))
	if err != nil {
		return nil, err
	}
	fields["attachmentCount"] = count
	trimmed, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("write message evidence: %w", err)
	}
	return trimmed, nil
}

// subjectProject is the Project a row is about: the chat the event names,
// else its Task, else its subject. It is not valid when the row is about no
// Project, or about no Company.
func subjectProject(
	ctx context.Context,
	q *db.Queries,
	companyID uuid.UUID,
	entityType, subject string,
	conversationID, taskID uuid.UUID,
) (uuid.NullUUID, error) {
	if companyID == uuid.Nil {
		return uuid.NullUUID{}, nil
	}
	subjectID, parseErr := uuid.Parse(subject)
	project, err := q.ResolveActivityProject(ctx, db.ResolveActivityProjectParams{
		CompanyID:      companyID,
		ConversationID: uuid.NullUUID{UUID: conversationID, Valid: conversationID != uuid.Nil},
		TaskID:         uuid.NullUUID{UUID: taskID, Valid: taskID != uuid.Nil},
		EntityType:     entityType,
		SubjectID:      uuid.NullUUID{UUID: subjectID, Valid: parseErr == nil},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.NullUUID{}, nil
	}
	if err != nil {
		return uuid.NullUUID{}, fmt.Errorf("resolve Activity Project: %w", err)
	}
	return uuid.NullUUID{UUID: project, Valid: true}, nil
}

func subjectID(payload []byte) string {
	var named struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(payload, &named); err != nil {
		return ""
	}
	return strings.Trim(string(named.ID), `"`)
}

func createdEventOf(
	ctx context.Context,
	q *db.Queries,
	row db.ActivityEvent,
	actor events.Actor,
) (events.Event, error) {
	entry := entryOf(row)
	audience, err := entryAudience(ctx, q, row)
	if err != nil {
		return events.Event{}, err
	}
	entry.Audience = audience
	return events.Event{
		EntityType: Topic, Name: EventCreated, CompanyID: row.CompanyID,
		CreatedAt: row.CreatedAt.Time.UTC(), Actor: actor, Payload: entry,
	}, nil
}

// entryAudience is who receives a row's activity.created frame: exactly who
// reads the row in the feed (ListActivity, ADR 0017). A row that names a
// Project reaches that Project's readers, and only its addressee among them
// when it is addressed to one person; a project_grant row reaches the readers
// and the projects.manage holders whoever it is addressed to. A Company-wide
// row about Task work that names no Project reaches nobody, and a
// project_grant row that names none reaches the projects.manage holders and
// its addressee. Any other row has no Project audience (nil) and keeps the
// frozen visibility route.
func entryAudience(ctx context.Context, q *db.Queries, row db.ActivityEvent) (*auth.ProjectAudience, error) {
	if row.EntityType == "mailbox" {
		return mailboxEntryAudience(ctx, q, row)
	}
	if !row.ProjectID.Valid {
		if row.EntityType == "project_grant" {
			managers, err := q.ListActiveCompanyMemberIDsByRoles(
				ctx,
				db.ListActiveCompanyMemberIDsByRolesParams{
					CompanyID: row.CompanyID, Roles: auth.RolesWith(auth.PermissionProjectsManage),
				},
			)
			if err != nil {
				return nil, fmt.Errorf("list Activity frame managers: %w", err)
			}
			if row.VisibilityUserID.Valid && !slices.Contains(managers, row.VisibilityUserID.UUID) {
				managers = append(managers, row.VisibilityUserID.UUID)
				slices.SortFunc(managers, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
			}
			return &auth.ProjectAudience{UserIDs: managers}, nil
		}
		if !row.VisibilityUserID.Valid && projectWorkEntity(row.EntityType) {
			return &auth.ProjectAudience{UserIDs: []uuid.UUID{}}, nil
		}
		return nil, nil
	}
	grantRow := row.EntityType == "project_grant"
	audience, err := auth.ProjectFrameAudienceInTx(ctx, q, row.CompanyID, row.ProjectID.UUID, grantRow)
	if err != nil {
		return nil, fmt.Errorf("decide Activity frame audience: %w", err)
	}
	if grantRow || !row.VisibilityUserID.Valid {
		return audience, nil
	}
	addressee := row.VisibilityUserID.UUID
	if audience.CompanyWide || slices.Contains(audience.UserIDs, addressee) {
		return &auth.ProjectAudience{UserIDs: []uuid.UUID{addressee}}, nil
	}
	return &auth.ProjectAudience{UserIDs: []uuid.UUID{}}, nil
}

// Mailbox evidence keeps its stored Company/creator/Project scope, intersected
// with the current Mailbox permission holders just like the feed and calendar.
func mailboxEntryAudience(ctx context.Context, q *db.Queries, row db.ActivityEvent) (*auth.ProjectAudience, error) {
	recipients, err := q.ListActiveCompanyMemberIDsByRoles(ctx, db.ListActiveCompanyMemberIDsByRolesParams{
		CompanyID: row.CompanyID, Roles: auth.RolesWith(auth.PermissionMailboxManage),
	})
	if err != nil {
		return nil, fmt.Errorf("list Mailbox Activity audience: %w", err)
	}
	if row.VisibilityUserID.Valid {
		recipients = slices.DeleteFunc(
			recipients,
			func(id uuid.UUID) bool { return id != row.VisibilityUserID.UUID },
		)
	}
	if row.ProjectID.Valid {
		audience, err := auth.ProjectFrameAudienceInTx(ctx, q, row.CompanyID, row.ProjectID.UUID, false)
		if err != nil {
			return nil, err
		}
		if !audience.CompanyWide {
			recipients = slices.DeleteFunc(
				recipients,
				func(id uuid.UUID) bool { return !slices.Contains(audience.UserIDs, id) },
			)
		}
	}
	return &auth.ProjectAudience{UserIDs: recipients}, nil
}

// projectWorkEntities are the rows about work inside a Project. One that names
// no Project predates Project access, and the feed shows it to nobody. The
// list restates the one activity.sql hides; a test keeps the two equal.
var projectWorkEntities = []string{
	"task", "conversation", "run", "message", "attachment", "approval", "question", "project_grant",
}

func projectWorkEntity(entityType string) bool {
	return slices.Contains(projectWorkEntities, entityType)
}

// EvidenceInput records an unversioned domain receipt without copying secrets
// or introducing subject revisions outside Task and Conversation. Payload is
// the receipt's own facts; the entry's envelope (schema version and the actor
// label the feed renders) is added here so every receipt carries it.
type EvidenceInput struct {
	CompanyID       uuid.UUID
	EntityID        uuid.UUID
	Action          string
	Actor           events.Actor
	RecipientUserID uuid.UUID
	Payload         any
	// ProjectID names the Project the receipt is about (ADR 0017); it is
	// stored in activity_events.project_id. When it is zero, the Project is
	// read from the subject (a Task, chat, Run, Approval or Question names
	// one).
	ProjectID uuid.UUID
	// EntityTitle freezes the subject's name as it read at the change, for a
	// subject the feed cannot name on its own (a computer). Empty stores none.
	EntityTitle string
}

// RecordEvidenceInTx captures the receipt in the mutation's own transaction.
func RecordEvidenceInTx(ctx context.Context, q *db.Queries, pending *[]events.Event, input EvidenceInput) error {
	entityType, _, _ := strings.Cut(input.Action, ".")
	visibility := "company"
	recipient := uuid.NullUUID{}
	if input.RecipientUserID != uuid.Nil {
		visibility = "user"
		recipient = uuid.NullUUID{UUID: input.RecipientUserID, Valid: true}
	}
	onBehalfOf, run, err := actorAttribution(input.Actor)
	if err != nil {
		return err
	}
	labels := historyContext{}
	if err := labels.captureActor(ctx, q, input.CompanyID, input.Actor); err != nil {
		return err
	}
	if input.EntityTitle != "" {
		labels.EntityTitle = &input.EntityTitle
	}
	project := uuid.NullUUID{UUID: input.ProjectID, Valid: input.ProjectID != uuid.Nil}
	if !project.Valid {
		if project, err = subjectProject(ctx, q, input.CompanyID, entityType, input.EntityID.String(),
			uuid.Nil, uuid.Nil); err != nil {
			return err
		}
	}
	details, err := json.Marshal(struct {
		SchemaVersion int            `json:"schemaVersion"`
		Payload       any            `json:"payload"`
		Context       historyContext `json:"context"`
	}{SchemaVersion: evidenceSchemaVersion, Payload: input.Payload, Context: labels})
	if err != nil {
		return fmt.Errorf("marshal domain receipt: %w", err)
	}
	row, err := q.CreateActivity(ctx, db.CreateActivityParams{
		CompanyID: input.CompanyID, ActorType: string(input.Actor.Type), ActorID: optionalText(input.Actor.ID),
		Action: input.Action, EntityType: entityType, EntityID: input.EntityID.String(),
		Visibility: optionalText(visibility), VisibilityUserID: recipient,
		SchemaVersion: evidenceSchemaVersion, Details: redact.JSON(details),
		ProjectID: project, OnBehalfOfUserID: onBehalfOf, RunID: run,
	})
	if err != nil {
		return fmt.Errorf("record domain receipt: %w", err)
	}
	created, err := createdEventOf(ctx, q, row, input.Actor)
	if err != nil {
		return err
	}
	*pending = append(*pending, created)
	return nil
}
