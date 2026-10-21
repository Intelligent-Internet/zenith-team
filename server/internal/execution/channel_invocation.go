package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	db "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// ErrChannelInvocationDenied signals that a channel Run's frozen authority
// ended: the invocation is gone or no longer live, its caller lost the
// channel, or the channel context moved on. Execution consent is separate:
// a caller without their own binding of the Agent gets ErrRunAgentNotBound.
var ErrChannelInvocationDenied = errors.New("channel invocation authority denied")

// AuthorizeChannelInvocationInTx checks the frozen channel authority alongside
// the caller's own binding of the Agent (ADR 0014). The caller owns the
// channel/conversation locks.
func AuthorizeChannelInvocationInTx(ctx context.Context, q *db.Queries, in RunTrigger) error {
	invocation, err := authorizeChannelAuthorityInTx(ctx, q, in)
	if err != nil {
		return err
	}
	return AuthorizeAgentInvocationInTx(
		ctx,
		q,
		RunTrigger{
			CompanyID:          invocation.CompanyID,
			AgentID:            invocation.AgentID,
			AuthorizedByUserID: invocation.CallerUserID,
		},
	)
}

// authorizeChannelAuthorityInTx is the channel half of
// AuthorizeChannelInvocationInTx: the invocation is live for this Run and
// its caller still holds the channel. It reads no binding.
func authorizeChannelAuthorityInTx(ctx context.Context, q *db.Queries, in RunTrigger) (db.ChannelInvocation, error) {
	invocation, err := q.GetChannelInvocation(
		ctx,
		db.GetChannelInvocationParams{InvocationID: in.InvocationID.UUID, CompanyID: in.CompanyID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.ChannelInvocation{}, ErrChannelInvocationDenied
	}
	if err != nil {
		return db.ChannelInvocation{}, fmt.Errorf("load channel invocation: %w", err)
	}
	if invocation.ConversationID != in.ConversationID || invocation.AgentID != in.AgentID ||
		(invocation.Status != "active" && invocation.Status != "waiting_input") {
		return db.ChannelInvocation{}, ErrChannelInvocationDenied
	}
	allowed, err := q.ChannelInvocationAuthorized(
		ctx,
		db.ChannelInvocationAuthorizedParams{
			InvocationID: invocation.ID,
			ActorRoles:   auth.RolesWith(auth.PermissionConversationsDiscuss),
		},
	)
	if err != nil {
		return db.ChannelInvocation{}, fmt.Errorf("check channel invocation authority: %w", err)
	}
	if !allowed {
		return db.ChannelInvocation{}, ErrChannelInvocationDenied
	}
	return invocation, nil
}

// AuthorizeAgentInvocationInTx applies the same rule as direct chat at
// mention admission, before creating either the shared message or its
// intents: the caller runs the Agent on their own binding of it (ADR 0014),
// so without one the tag is refused with ErrRunAgentNotBound.
func AuthorizeAgentInvocationInTx(ctx context.Context, q *db.Queries, in RunTrigger) error {
	_, err := resolveRunBinding(ctx, q, in.CompanyID, in.AgentID, in.AuthorizedByUserID)
	return err
}

type channelInvocationContext struct {
	ID              uuid.UUID `json:"id"`
	ChannelID       uuid.UUID `json:"channelId"`
	ThreadRootID    uuid.UUID `json:"threadRootId"`
	ContextRevision int64     `json:"contextRevision"`
}

func freezeChannelInvocation(
	ctx context.Context,
	q *db.Queries,
	in RunTrigger,
	document map[string]json.RawMessage,
) error {
	invocation, err := q.GetChannelInvocation(
		ctx,
		db.GetChannelInvocationParams{InvocationID: in.InvocationID.UUID, CompanyID: in.CompanyID},
	)
	if err != nil {
		return fmt.Errorf("load channel context invocation: %w", err)
	}
	head, err := q.GetChannelContextHead(ctx, invocation.ChannelID)
	if err != nil {
		return err
	}
	rows, err := q.ListChannelContext(
		ctx,
		db.ListChannelContextParams{
			ChannelID:        invocation.ChannelID,
			TriggerMessageID: invocation.TriggerMessageID,
			ThreadRootID:     invocation.ThreadRootID,
		},
	)
	if err != nil {
		return err
	}
	contextMessageIDs := []uuid.UUID{invocation.TriggerMessageID}
	for _, row := range rows {
		contextMessageIDs = append(contextMessageIDs, row.ID)
	}
	// The Run uses its caller's access, so it freezes the Artifact
	// references its caller may read, as it does the chat files below.
	references, err := q.ListRunChannelArtifacts(ctx, db.ListRunChannelArtifactsParams{
		CompanyID: invocation.CompanyID, MessageIds: contextMessageIDs, ActorUserID: invocation.CallerUserID,
	})
	if err != nil {
		return fmt.Errorf("freeze channel artifacts: %w", err)
	}
	artifactsByMessage := make(map[uuid.UUID][]uuid.UUID)
	for _, reference := range references {
		artifactsByMessage[reference.MessageID] = append(artifactsByMessage[reference.MessageID], reference.ID)
	}
	triggerArtifacts := artifactsByMessage[invocation.TriggerMessageID]
	if triggerArtifacts == nil {
		triggerArtifacts = []uuid.UUID{}
	}
	document["channelArtifactIds"], err = json.Marshal(triggerArtifacts)
	if err != nil {
		return err
	}
	// The Whiteboards of the chat, as its caller may open them, so the Run
	// knows which ones it can work on.
	whiteboards, err := q.ListRunChannelWhiteboards(ctx, db.ListRunChannelWhiteboardsParams{
		CompanyID:      invocation.CompanyID,
		ConversationID: invocation.ChannelID,
		ActorUserID:    invocation.CallerUserID,
	})
	if err != nil {
		return fmt.Errorf("freeze channel Whiteboards: %w", err)
	}
	briefs := make([]runWhiteboardBrief, 0, len(whiteboards))
	for _, board := range whiteboards {
		briefs = append(briefs, runWhiteboardBrief{ID: board.ID.String(), Title: board.Title})
	}
	if err := freezeChatWhiteboards(document, briefs); err != nil {
		return err
	}
	snapshot := runConversationSnapshot{
		SchemaVersion:  1,
		Kind:           "channel_agent",
		ConversationID: in.ConversationID.String(),
		ThroughSeq:     head.HeadSeq,
		Truncated:      len(rows) == 64,
		Items:          []runConversationSnapshotItem{},
	}
	size := 0
	messageIDs := []uuid.UUID{invocation.TriggerMessageID}
	for _, row := range rows {
		item := runConversationSnapshotItem{
			ID:  row.ID.String(),
			Seq: row.Seq,
			Author: runContextActor{
				ID:   row.AuthorID.UUID.String(),
				Kind: row.AuthorKind,
				Name: row.AuthorName,
			},
			CreatedAt: row.CreatedAt.Time,
			Content:   channelArtifactContent(row.Content, artifactsByMessage[row.ID]),
		}
		encoded, err := json.Marshal(item)
		if err != nil {
			return err
		}
		if size+len(encoded) > 60<<10 {
			snapshot.Truncated = true
			continue
		}
		size += len(encoded)
		snapshot.Items = append(snapshot.Items, item)
		messageIDs = append(messageIDs, row.ID)
	}
	sort.Slice(snapshot.Items, func(i, j int) bool { return snapshot.Items[i].Seq < snapshot.Items[j].Seq })
	document["conversationSnapshot"], err = json.Marshal(snapshot)
	if err != nil {
		return err
	}
	// The Run uses its caller's access, so it freezes the files its
	// caller may open: a Project file stays out unless the caller reads one of
	// its Projects (ADR 0017).
	files, err := q.ListRunChannelAttachments(ctx, db.ListRunChannelAttachmentsParams{
		CompanyID: invocation.CompanyID, MessageIds: messageIDs, ActorUserID: invocation.CallerUserID,
	})
	if err != nil {
		return err
	}
	attachments := make([]runAttachedFileBrief, 0, len(files))
	seen := make(map[uuid.UUID]bool)
	for _, file := range files {
		if seen[file.Attachment.ID] {
			continue
		}
		seen[file.Attachment.ID] = true
		attachments = append(
			attachments,
			runAttachedFileBrief{
				ID:          file.Attachment.ID.String(),
				Filename:    file.Attachment.Filename,
				ContentType: file.Attachment.ContentType,
				SizeBytes:   file.Attachment.SizeBytes,
			},
		)
	}
	document["channelAttachments"], err = json.Marshal(attachments)
	if err != nil {
		return err
	}
	document["channelInvocation"], err = json.Marshal(
		channelInvocationContext{
			ID:              invocation.ID,
			ChannelID:       invocation.ChannelID,
			ThreadRootID:    invocation.ThreadRootID,
			ContextRevision: head.ChannelContextRevision,
		},
	)
	return err
}

// CancelChannelInvocationRunsInTx revokes live Run tokens and emits the same
// cancellation events as other authority revocations. Locks precede Run writes.
func CancelChannelInvocationRunsInTx(
	ctx context.Context,
	q *db.Queries,
	pending *[]events.Event,
	invocation db.ChannelInvocation,
) error {
	runs, err := q.ListActiveInvocationRuns(ctx, uuid.NullUUID{UUID: invocation.ID, Valid: true})
	if err != nil {
		return err
	}
	_, err = cancelRunCandidatesInTx(ctx, q, pending, runs, "channel_access_revoked")
	return err
}

// refreshChannelRunContext runs before claim delivery so offline waits cannot
// dispatch a history packet whose channel content changed while it was queued.
// It checks only the channel authority. The binding is SelectClaimableRun's to
// check: a tag's Run is authorized by the tag's caller, and the claim fence
// requires that member's binding on the Run's runtime in the same
// transaction. Reading the binding here would take its share lock after the
// Run's row lock, the reverse of Bind and Unbind, which lock the binding and
// then the member's pending Runs; the two could deadlock.
func refreshChannelRunContext(ctx context.Context, q *db.Queries, run db.Run) (db.Run, error) {
	if !run.InvocationID.Valid {
		return run, nil
	}
	in := RunTrigger{
		CompanyID:      run.CompanyID,
		ConversationID: run.ConversationID,
		AgentID:        run.AgentID,
		InvocationID:   run.InvocationID,
	}
	if _, err := authorizeChannelAuthorityInTx(ctx, q, in); err != nil {
		return db.Run{}, err
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(run.Context, &document); err != nil {
		return db.Run{}, err
	}
	if err := freezeChannelInvocation(ctx, q, in, document); err != nil {
		return db.Run{}, err
	}
	encoded, err := encodeRunContext(document)
	if err != nil {
		return db.Run{}, err
	}
	return q.RefreshChannelRunContext(ctx, db.RefreshChannelRunContextParams{RunID: run.ID, Context: encoded})
}

func validateChannelRunStart(ctx context.Context, q *db.Queries, run db.Run) error {
	if !run.InvocationID.Valid {
		return nil
	}
	if err := AuthorizeChannelInvocationInTx(
		ctx,
		q,
		RunTrigger{
			CompanyID:      run.CompanyID,
			ConversationID: run.ConversationID,
			AgentID:        run.AgentID,
			InvocationID:   run.InvocationID,
		},
	); err != nil {
		return err
	}
	var doc struct {
		ChannelInvocation channelInvocationContext `json:"channelInvocation"`
	}
	if err := json.Unmarshal(run.Context, &doc); err != nil {
		return err
	}
	head, err := q.GetChannelContextHead(ctx, doc.ChannelInvocation.ChannelID)
	if err != nil {
		return err
	}
	if head.ChannelContextRevision != doc.ChannelInvocation.ContextRevision {
		return ErrChannelInvocationDenied
	}
	return nil
}

// runWhiteboardBrief names a Whiteboard Artifact in a Run's context.
type runWhiteboardBrief struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// freezeChatWhiteboards names the Whiteboards of the chat a Run answers under
// `chatWhiteboards`, or removes the key when there are none, so a caller can
// never supply its own.
func freezeChatWhiteboards(document map[string]json.RawMessage, briefs []runWhiteboardBrief) error {
	delete(document, "chatWhiteboards")
	if len(briefs) == 0 {
		return nil
	}
	encoded, err := json.Marshal(briefs)
	if err != nil {
		return err
	}
	document["chatWhiteboards"] = encoded
	return nil
}

func channelArtifactContent(content string, ids []uuid.UUID) string {
	if len(ids) == 0 {
		return content
	}
	var text strings.Builder
	text.WriteString(content)
	text.WriteString("\n\nReferenced artifacts:")
	for _, id := range ids {
		fmt.Fprintf(&text, "\n- artifactId: %s", id)
	}
	return text.String()
}
