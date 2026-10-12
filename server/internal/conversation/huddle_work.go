package conversation

import (
	"context"
	"slices"
	"time"

	"boardly.dev/boardly/server/internal/agent/voice"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"github.com/google/uuid"
)

// HuddleWorkNotice contains routing facts only, never a result or private input.
type HuddleWorkNotice struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	TaskKey string `json:"taskKey"`
}

func huddleWorkNotice(row generated.ReadHuddleWorkRow) *HuddleWorkNotice {
	if row.NewerTaskExecution || len(row.VoiceNoticeKeys) >= 128 {
		return nil
	}
	state := huddleWorkState(row.RunStatus, row.RuntimeOnline, row.WaitingInput, row.Resuming)
	if state != "waiting_for_input" && (state != "completed" || !row.HasResult) {
		return nil
	}
	id := row.ProposalID.String() + ":" + row.RunID.String() + ":" + state
	if slices.Contains(row.VoiceNoticeKeys, id) {
		return nil
	}
	return &HuddleWorkNotice{ID: id, State: state, TaskKey: row.SubmittedTaskKey.String}
}

// Called under the existing attempt/lease transaction. A committed claim is an
// at-most-once speech attempt, not proof that any participant heard it.
func huddleWorkNotices(
	ctx context.Context,
	q *generated.Queries,
	h generated.Huddle,
	sharerID uuid.UUID,
	claim string,
) ([]HuddleWorkNotice, *HuddleWorkNotice, error) {
	// Notices are spoken to the call with the access of the person who
	// shared Company context (D31), the voice assistant's Authorizer.
	rows, err := q.ReadHuddleWork(ctx, huddleWorkParams(h, sharerID))
	if err != nil {
		return nil, nil, err
	}
	notices := []HuddleWorkNotice{}
	var claimed *HuddleWorkNotice
	for _, row := range rows {
		notice := huddleWorkNotice(row)
		if notice == nil {
			continue
		}
		if notice.ID != claim {
			notices = append(notices, *notice)
			continue
		}
		count, err := q.ClaimHuddleWorkNotice(ctx, generated.ClaimHuddleWorkNoticeParams{
			CompanyID: h.CompanyID, HuddleID: h.ID, ProposalID: row.ProposalID, NoticeKey: notice.ID,
		})
		if err != nil {
			return nil, nil, err
		}
		if count == 1 {
			claimed = notice
		}
	}
	return notices, claimed, nil
}

// huddleWorkParams reads a Huddle's submitted work as readerID may see it:
// work is kept only in Projects that person reads by the delegate rule (D31).
func huddleWorkParams(h generated.Huddle, readerID uuid.UUID) generated.ReadHuddleWorkParams {
	return generated.ReadHuddleWorkParams{
		CompanyID: h.CompanyID, HuddleID: h.ID, ReaderUserID: readerID,
		ReadPermission:            string(auth.ProjectPermissionRead),
		CompanyDefaultPermissions: auth.ProjectPermissionHeldSets().CompanyDefaultPermissions,
	}
}

// The caller establishes Huddle visibility or checks a live voice capability.
// One bounded query serves UI polling and on-demand voice reads identically.
func readHuddleWork(ctx context.Context, q *generated.Queries, in generated.ReadHuddleWorkParams) (voice.ContextResult, error) {
	result := voice.ContextResult{Items: []voice.ContextItem{}}
	rows, err := q.ReadHuddleWork(ctx, in)
	if err != nil {
		return result, err
	}
	checkedAt := time.Now().UTC()
	for _, row := range rows {
		work := &voice.WorkProgress{
			State:  huddleWorkState(row.RunStatus, row.RuntimeOnline, row.WaitingInput, row.Resuming),
			TaskID: util.NullUUIDPtr(row.TaskID), TaskStatus: row.TaskStatus, RunStatus: row.RunStatus,
			NewerTaskExecution: row.NewerTaskExecution, HasResult: row.HasResult,
			ResultMessageID: util.NullUUIDPtr(row.ResultMessageID), ResultMessageSeq: row.ResultMessageSeq,
			ResultConversationID: util.NullUUIDPtr(row.ResultConversationID),
			ResultText:           row.ResultText, ResultTruncated: row.ResultTruncated, CheckedAt: checkedAt,
		}
		if row.RunID != uuid.Nil {
			work.RunID = &row.RunID
		} else if row.TaskID.Valid {
			work.State = "confirmed"
		}
		result.Items = append(result.Items, voice.ContextItem{
			ID: row.ProposalID, Type: "work", Title: row.Title, Key: row.SubmittedTaskKey.String,
			Status: work.State, Work: work, UpdatedAt: row.UpdatedAt.Time, Truncated: work.ResultTruncated,
		})
	}
	return result, nil
}

func huddleWorkState(run string, online, input, continuation bool) string {
	if run == "" {
		return "unavailable"
	}
	if run == "cancelled" || run == "failed" {
		return run
	}
	if input {
		return "waiting_for_input"
	}
	if continuation {
		return "resuming"
	}
	switch run {
	case "queued":
		if !online {
			return "waiting_for_runtime"
		}
	case "dispatched":
		return "preparing"
	}
	return run
}
