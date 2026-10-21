package execution

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	db "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/redact"
)

const (
	maxTranscriptPartIDBytes = 512
)

var (
	// ErrTranscriptItemInvalid means the normalized Zenith Team item is malformed
	// or exceeds the durable transcript bounds.
	ErrTranscriptItemInvalid = errors.New("transcript item is invalid")
	// ErrTranscriptItemConflict means ItemKey already names different
	// normalized content in this Run.
	ErrTranscriptItemConflict = errors.New("transcript item conflicts with existing item key")
	// ErrTranscriptRunUnavailable deliberately collapses unknown Runs, Runs
	// assigned to another Runtime, and Runs that are no longer running.
	ErrTranscriptRunUnavailable = errors.New("run is unavailable for transcript append")
	// ErrTranscriptSeqCeiling means the Conversation counter has reached
	// protocol.MaxJSONSafeInteger, so the next seq would be a number a
	// JavaScript reader silently rounds. Every writer of that counter refuses
	// it in SQL; this is what AppendRunItem's refusal reads as in Go.
	ErrTranscriptSeqCeiling = errors.New("conversation sequence exceeds the JSON-safe ceiling")
)

// TranscriptItemInput is one completed provider-neutral item. ItemKey is the
// provider item's natural replay identity; callers never assign seq.
type TranscriptItemInput struct {
	ItemKey string
	Role    protocol.RunItemRole
	Parts   []protocol.RunItemPart
}

// TranscriptItemReceipt acknowledges one durable item and the current
// transcript head. A replay preserves ID, Seq, and ItemDigest while reporting
// the Run's latest ThroughSeq.
//
// Seq is the Conversation-wide counter this item was allocated, not a per-Run
// ordinal: the user's own words and every other Run in the Conversation draw
// from the same sequence, so one Run's items are ordered but not contiguous.
type TranscriptItemReceipt struct {
	ID          uuid.UUID
	RunID       uuid.UUID
	ItemKey     string
	Seq         int64
	ThroughSeq  int64
	ItemDigest  [sha256.Size]byte
	CommittedAt time.Time
}

type normalizedTranscriptItem struct {
	itemKey       string
	role          protocol.RunItemRole
	itemType      string
	content       string
	parts         []protocol.RunItemPart
	partsJSON     []byte
	contentDigest [sha256.Size]byte
	// finalAnswer: a text part the provider marked final, the proof a channel
	// publisher requires before it posts the answer.
	finalAnswer bool
}

// AppendTranscriptItem is the single durable transcript mutation seam. It
// appends the item to the one Conversation stream, advances the Run's
// transcript head in the same transaction, and stages exactly one full
// run.message event for a fresh item. The event is published only after the
// transaction commits.
//
// The lock is the CONVERSATION row, not the Run row: seq is allocated
// conversation-wide, so an append races the user's own sends, not just this
// Run's other appends. Taking the Conversation lock first and the Run row
// second is also the order every terminal transition takes, which is what
// keeps the two paths from deadlocking against each other.
func (s *DefaultRunService) AppendTranscriptItem(
	ctx context.Context,
	runtimeID uuid.UUID,
	runID uuid.UUID,
	input TranscriptItemInput,
) (TranscriptItemReceipt, error) {
	item, err := normalizeTranscriptItem(input)
	if err != nil {
		return TranscriptItemReceipt{}, err
	}
	var (
		receipt TranscriptItemReceipt
		pending []events.Event
	)
	err = s.runInTx(ctx, func(q *db.Queries) error {
		// Unlocked read purely to learn which Conversation to lock. Every
		// authoritative fact — runtime, status, transcript head — is re-read
		// under the locks below, so a row that moves between the two reads
		// simply fails there.
		scope, err := q.GetRun(ctx, runID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrTranscriptRunUnavailable
		}
		if err != nil {
			return fmt.Errorf("load Run for transcript append: %w", err)
		}
		_, err = q.LockConversationForSystemRunQueueMutation(
			ctx,
			db.LockConversationForSystemRunQueueMutationParams{
				ConversationID: scope.ConversationID,
				CompanyID:      scope.CompanyID,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			// The Conversation was deleted between the two reads, which took
			// the Run with it through runs_company_conversation_fkey. Same
			// collapsed answer as an unknown Run, not a 500.
			return ErrTranscriptRunUnavailable
		}
		if err != nil {
			return fmt.Errorf("lock Conversation for transcript append: %w", err)
		}
		run, err := q.LockRunningRunForTranscript(
			ctx,
			db.LockRunningRunForTranscriptParams{RunID: runID, RuntimeID: runtimeID},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrTranscriptRunUnavailable
		}
		if err != nil {
			return fmt.Errorf("lock Run for transcript append: %w", err)
		}

		existing, err := q.GetRunItemByKey(
			ctx,
			db.GetRunItemByKeyParams{RunID: runID, ItemKey: item.itemKey},
		)
		if err == nil {
			if !bytes.Equal(existing.ContentSha256, item.contentDigest[:]) {
				return ErrTranscriptItemConflict
			}
			receipt = transcriptReceipt(existing, run)
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("load transcript item replay: %w", err)
		}

		// seq is allocated by the INSERT, under the Conversation lock — the
		// caller cannot choose it and cannot predict it, because the user may
		// have sent a message between this Run's last two items. So the head
		// is advanced to the seq the row came back with.
		created, err := q.AppendRunItem(ctx, db.AppendRunItemParams{
			RunID:         runID,
			Type:          item.itemType,
			ItemKey:       item.itemKey,
			Content:       item.content,
			Parts:         item.partsJSON,
			ContentSha256: item.contentDigest[:],
		})
		if errors.Is(err, pgx.ErrNoRows) {
			// AppendRunItem is an INSERT ... SELECT and the only way its
			// SELECT can come back empty is the JSON-safe ceiling on seq: the
			// Run row is locked above in this same transaction, so the
			// `FROM runs` side is there. Nothing was written; the transaction
			// rolls back with the ceiling named.
			//
			// There is no companion test of created.Seq. Every writer of the
			// one counter now refuses the same number in the statement that
			// allocates it — the three user-message inserts always did — so
			// such a test could not fire, and a guard that cannot fire is a
			// second, weaker copy of an invariant the database owns.
			return ErrTranscriptSeqCeiling
		}
		if err != nil {
			return fmt.Errorf("append transcript item: %w", err)
		}
		run, err = q.UpdateRunTranscriptHead(ctx, db.UpdateRunTranscriptHeadParams{
			RunID:              runID,
			ThroughSeq:         created.Seq,
			FinalAnswerItemKey: pgtype.Text{String: item.itemKey, Valid: item.finalAnswer},
		})
		if err != nil {
			return fmt.Errorf("advance Run transcript head: %w", err)
		}
		route, err := s.resolveTranscriptRoute(ctx, q, run)
		if err != nil {
			return err
		}

		receipt = transcriptReceipt(created, run)
		pending = append(pending, transcriptItemEvent(run, created, item, receipt, route))
		return nil
	})
	if err != nil {
		return TranscriptItemReceipt{}, err
	}
	// Empty on exact replay. A fresh append always contributes exactly one
	// event, so there is no batch/coalescing behavior hidden at this seam.
	for _, group := range events.GroupByTopic(pending) {
		s.Bus.Publish(ctx, group.Topic, group.Events...)
	}
	return receipt, nil
}

func normalizeTranscriptItem(input TranscriptItemInput) (normalizedTranscriptItem, error) {
	itemKey := input.ItemKey
	if strings.TrimSpace(itemKey) != itemKey || !validTranscriptString(itemKey, true, 512) {
		return normalizedTranscriptItem{}, transcriptItemInvalid("item key")
	}
	switch input.Role {
	case protocol.RunItemRoleAgent, protocol.RunItemRoleTool, protocol.RunItemRoleSystem:
	default:
		return normalizedTranscriptItem{}, transcriptItemInvalid("role")
	}
	if len(input.Parts) == 0 || len(input.Parts) > 128 {
		return normalizedTranscriptItem{}, transcriptItemInvalid("parts")
	}

	parts := make([]protocol.RunItemPart, len(input.Parts))
	finalAnswer := false
	for index, part := range input.Parts {
		if part.Final &&
			(input.Role != protocol.RunItemRoleAgent || part.Type != protocol.RunItemPartTypeText) {
			return normalizedTranscriptItem{}, transcriptItemInvalid("final answer part")
		}
		finalAnswer = finalAnswer || part.Final
		normalized, err := normalizeTranscriptPart(part)
		if err != nil {
			return normalizedTranscriptItem{}, fmt.Errorf("%w: part %d", err, index)
		}
		parts[index] = normalized
	}
	partsJSON, err := marshalCanonicalJSON(parts)
	if err != nil {
		return normalizedTranscriptItem{}, transcriptItemInvalid("parts encoding")
	}
	contentJSON, err := marshalCanonicalJSON(struct {
		ItemKey string                 `json:"itemKey"`
		Role    protocol.RunItemRole   `json:"role"`
		Parts   []protocol.RunItemPart `json:"parts"`
	}{ItemKey: itemKey, Role: input.Role, Parts: parts})
	if err != nil || len(contentJSON) > protocol.RunTranscriptMaxItemBytes {
		return normalizedTranscriptItem{}, transcriptItemInvalid("encoded content")
	}
	itemType, content := transcriptItemTypeAndContent(input.Role, parts)
	return normalizedTranscriptItem{
		itemKey:       itemKey,
		role:          input.Role,
		itemType:      itemType,
		content:       content,
		parts:         parts,
		partsJSON:     partsJSON,
		contentDigest: sha256.Sum256(contentJSON),
		finalAnswer:   finalAnswer,
	}, nil
}

// Conversation item types an Agent can produce. The vocabulary is the
// conversation_items type CHECK; 'user_message' is deliberately absent,
// because this seam only ever writes what a Run produced.
const (
	transcriptTypeAgentText  = "agent_text"
	transcriptTypeReasoning  = "reasoning"
	transcriptTypeToolCall   = "tool_call"
	transcriptTypeToolResult = "tool_result"
	transcriptTypeError      = "error"
)

// transcriptItemTypeAndContent collapses one provider item's (role, parts)
// into the single `type` the one stream stores and the plain-text `content`
// every reader shares. The type is the whole visibility rule — 'agent_text'
// reaches the chat page, everything else is run detail — so this function is
// where an Agent's raw tool output would leak into the user's chat if it were
// wrong, and it is deliberately total: every input maps somewhere.
//
// The precedence answers a question the schema poses but does not settle: an
// item may carry several kinds of part at once. Agent text wins because it is
// the only thing the user asked for, and because the daemon nominates its
// terminal final item by exactly this test (agent role, a non-empty text
// part). If the two disagreed, a completion would name a final item that
// GetFinalAgentTextItem cannot find, and the Run would wedge on a terminal
// 409 with the answer already durable — so they must not disagree.
func transcriptItemTypeAndContent(
	role protocol.RunItemRole,
	parts []protocol.RunItemPart,
) (string, string) {
	if role == protocol.RunItemRoleAgent {
		if text := joinTranscriptText(parts, protocol.RunItemPartTypeText); text != "" {
			return transcriptTypeAgentText, text
		}
	}
	if text := joinTranscriptText(parts, protocol.RunItemPartTypeError); text != "" {
		return transcriptTypeError, text
	}
	for _, part := range parts {
		if part.Type == protocol.RunItemPartTypeToolResult {
			return transcriptTypeToolResult, joinTranscriptToolOutput(parts)
		}
	}
	for _, part := range parts {
		if part.Type == protocol.RunItemPartTypeToolCall {
			return transcriptTypeToolCall, joinTranscriptToolNames(parts)
		}
	}
	// What is left is text or reasoning the user is not the audience for: a
	// tool- or system-role item carrying prose, and the Agent's own thinking.
	// Both are run detail, and 'reasoning' is the type that says so.
	reasoning := joinTranscriptText(parts, protocol.RunItemPartTypeReasoning)
	if reasoning == "" {
		reasoning = joinTranscriptText(parts, protocol.RunItemPartTypeText)
	}
	return transcriptTypeReasoning, reasoning
}

func joinTranscriptText(parts []protocol.RunItemPart, want protocol.RunItemPartType) string {
	var joined strings.Builder
	for _, part := range parts {
		if part.Type == want {
			joined.WriteString(part.Text)
		}
	}
	return joined.String()
}

func joinTranscriptToolOutput(parts []protocol.RunItemPart) string {
	var joined strings.Builder
	for _, part := range parts {
		if part.Type != protocol.RunItemPartTypeToolResult || part.Output == "" {
			continue
		}
		if joined.Len() > 0 {
			joined.WriteString("\n")
		}
		joined.WriteString(part.Output)
	}
	return joined.String()
}

func joinTranscriptToolNames(parts []protocol.RunItemPart) string {
	var joined strings.Builder
	for _, part := range parts {
		if part.Type != protocol.RunItemPartTypeToolCall || part.Name == "" {
			continue
		}
		if joined.Len() > 0 {
			joined.WriteString(" ")
		}
		joined.WriteString(part.Name)
	}
	return joined.String()
}

func normalizeTranscriptPart(part protocol.RunItemPart) (protocol.RunItemPart, error) {
	switch part.Type {
	case protocol.RunItemPartTypeText, protocol.RunItemPartTypeReasoning:
		if !validTranscriptString(part.Text, true, protocol.RunTranscriptMaxItemBytes) ||
			part.CallID != "" || part.Name != "" || len(part.Input) != 0 ||
			part.Output != "" || part.Status != "" || part.ExitCode != nil ||
			part.DurationMs != nil {
			return protocol.RunItemPart{}, transcriptItemInvalid("text part shape")
		}
		part.Text = redact.Redact(part.Text)
		return part, nil

	case protocol.RunItemPartTypeToolCall:
		part.CallID = strings.TrimSpace(part.CallID)
		part.Name = strings.TrimSpace(part.Name)
		if !validTranscriptString(part.CallID, true, maxTranscriptPartIDBytes) ||
			!validTranscriptString(part.Name, true, maxTranscriptPartIDBytes) ||
			part.Text != "" || part.Output != "" || part.Status != "" ||
			part.ExitCode != nil || part.DurationMs != nil {
			return protocol.RunItemPart{}, transcriptItemInvalid("tool call part shape")
		}
		canonical, err := canonicalTranscriptJSONObject(part.Input)
		if err != nil {
			return protocol.RunItemPart{}, transcriptItemInvalid("tool call input")
		}
		part.Input = redact.JSON(canonical)
		return part, nil

	case protocol.RunItemPartTypeToolResult:
		part.CallID = strings.TrimSpace(part.CallID)
		part.Name = strings.TrimSpace(part.Name)
		part.Status = protocol.RunToolResultStatus(strings.TrimSpace(string(part.Status)))
		if !validTranscriptString(part.CallID, true, maxTranscriptPartIDBytes) ||
			!validTranscriptString(part.Name, false, maxTranscriptPartIDBytes) ||
			!validTranscriptToolStatus(part.Status) ||
			!validTranscriptString(part.Output, false, protocol.RunTranscriptMaxItemBytes) ||
			part.Text != "" || len(part.Input) != 0 ||
			part.ExitCode != nil && *part.ExitCode < 0 ||
			part.DurationMs != nil && *part.DurationMs < 0 {
			return protocol.RunItemPart{}, transcriptItemInvalid("tool result part shape")
		}
		part.Output = redact.JSONOrText(part.Output)
		return part, nil

	case protocol.RunItemPartTypeError:
		if !validTranscriptString(part.Text, true, protocol.RunTranscriptMaxItemBytes) ||
			part.CallID != "" || part.Name != "" || len(part.Input) != 0 ||
			part.Output != "" || part.Status != "" || part.ExitCode != nil ||
			part.DurationMs != nil {
			return protocol.RunItemPart{}, transcriptItemInvalid("error part shape")
		}
		part.Text = redact.Redact(part.Text)
		return part, nil

	default:
		return protocol.RunItemPart{}, transcriptItemInvalid("part type")
	}
}

func validTranscriptToolStatus(status protocol.RunToolResultStatus) bool {
	switch status {
	case "",
		protocol.RunToolResultStatusInProgress,
		protocol.RunToolResultStatusCompleted,
		protocol.RunToolResultStatusFailed,
		protocol.RunToolResultStatusDeclined,
		protocol.RunToolResultStatusCancelled:
		return true
	default:
		return false
	}
}

func canonicalTranscriptJSONObject(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if len(raw) > protocol.RunTranscriptMaxItemBytes || !utf8.Valid(raw) {
		return nil, ErrTranscriptItemInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("tool call input is not an object")
	}
	encoded, err := marshalCanonicalJSON(object)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(encoded), nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("multiple JSON values")
	}
	return err
}

func marshalCanonicalJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

func validTranscriptString(value string, required bool, maxBytes int) bool {
	if required && value == "" {
		return false
	}
	return len(value) <= maxBytes && utf8.ValidString(value)
}

func transcriptItemInvalid(field string) error {
	return fmt.Errorf("%w: invalid %s", ErrTranscriptItemInvalid, field)
}

// itemContentDigest lifts one item's stored content_sha256 into the fixed-size
// value the receipt carries. This is the PER-ITEM digest — the one that tells
// a harmless replay from an attempt to rewrite committed history — not a chain
// over the transcript; there is no such chain.
func itemContentDigest(value []byte) [sha256.Size]byte {
	var digest [sha256.Size]byte
	copy(digest[:], value)
	return digest
}

func transcriptReceipt(row db.ConversationItem, run db.Run) TranscriptItemReceipt {
	return TranscriptItemReceipt{
		ID:          row.ID,
		RunID:       row.RunID.UUID,
		ItemKey:     row.ItemKey.String,
		Seq:         row.Seq,
		ThroughSeq:  run.TranscriptThroughSeq,
		ItemDigest:  itemContentDigest(row.ContentSha256),
		CommittedAt: row.CreatedAt.Time.UTC(),
	}
}

func (s *DefaultRunService) resolveTranscriptRoute(
	ctx context.Context,
	q *db.Queries,
	run db.Run,
) (ConversationRoute, error) {
	if s.RouteResolver == nil {
		return ConversationRoute{}, errors.New("conversation route resolver is not wired")
	}
	route, err := s.RouteResolver(ctx, q, run.CompanyID, run.ConversationID)
	if err != nil {
		return ConversationRoute{}, fmt.Errorf("resolve transcript item route: %w", err)
	}
	return route, nil
}

func transcriptItemEvent(
	run db.Run,
	row db.ConversationItem,
	item normalizedTranscriptItem,
	receipt TranscriptItemReceipt,
	route ConversationRoute,
) events.Event {
	return events.Event{
		EntityType:     Topic,
		Name:           EventRunMessageCreated,
		CompanyID:      run.CompanyID,
		CreatedAt:      time.Now().UTC(),
		RunID:          run.ID,
		TaskID:         util.NullUUIDToValue(run.TaskID),
		ConversationID: run.ConversationID,
		Actor:          events.SystemActor(),
		Payload: TranscriptItem{
			RunMessagePayload: protocol.RunMessagePayload{
				ID:             row.ID.String(),
				RunID:          run.ID,
				ConversationID: run.ConversationID.String(),
				ItemKey:        item.itemKey,
				Seq:            row.Seq,
				Role:           item.role,
				Parts:          transcriptParts(item.parts),
				ItemDigest:     hex.EncodeToString(receipt.ItemDigest[:]),
				ThroughSeq:     receipt.ThroughSeq,
				CreatedAt:      row.CreatedAt.Time.UTC().Format(time.RFC3339Nano),
			},
			CompanyID:         run.CompanyID,
			ConversationRoute: route,
		},
	}
}

func transcriptParts(parts []protocol.RunItemPart) []protocol.RunTranscriptPart {
	result := make([]protocol.RunTranscriptPart, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case protocol.RunItemPartTypeText:
			result = append(result, protocol.RunTextPart{
				Type: protocol.RunTextPartTypeText, Final: part.Final,
				Text: part.Text,
			})
		case protocol.RunItemPartTypeReasoning:
			result = append(result, protocol.RunReasoningPart{
				Type: protocol.RunReasoningPartTypeReasoning,
				Text: part.Text,
			})
		case protocol.RunItemPartTypeToolCall:
			result = append(result, protocol.RunToolCallPart{
				Type:   protocol.RunToolCallPartTypeToolCall,
				CallID: part.CallID,
				Name:   part.Name,
				Input:  part.Input,
			})
		case protocol.RunItemPartTypeToolResult:
			result = append(result, protocol.RunToolResultPart{
				Type:       protocol.RunToolResultPartTypeToolResult,
				CallID:     part.CallID,
				Name:       part.Name,
				Output:     part.Output,
				Status:     part.Status,
				ExitCode:   part.ExitCode,
				DurationMs: part.DurationMs,
			})
		case protocol.RunItemPartTypeError:
			result = append(result, protocol.RunErrorPart{
				Type: protocol.RunErrorPartTypeError,
				Text: part.Text,
			})
		}
	}
	return result
}

// ActivityParts maps stored provider activity to public wire parts. Reasoning
// stays outside the channel manager's activity projection.
func ActivityParts(raw json.RawMessage) ([]protocol.RunTranscriptPart, error) {
	var parts []protocol.RunItemPart
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &parts); err != nil {
			return nil, fmt.Errorf("decode activity parts: %w", err)
		}
	}
	visible := make([]protocol.RunItemPart, 0, len(parts))
	for _, part := range parts {
		if part.Type != protocol.RunItemPartTypeReasoning {
			visible = append(visible, part)
		}
	}
	return transcriptParts(visible), nil
}
