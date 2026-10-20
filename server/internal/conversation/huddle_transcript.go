package conversation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"boardly.dev/boardly/server/internal/agent/voice"
	"boardly.dev/boardly/server/internal/events"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type HuddleTranscriptEntry struct {
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
}

type HuddleTranscriptBatch struct {
	Sequence  int64                   `json:"sequence"`
	Entries   []HuddleTranscriptEntry `json:"entries"`
	Truncated bool                    `json:"truncated"`
}

type HuddleTranscriptReceipt struct {
	Truncated bool `json:"truncated"`
}

// AppendTranscript accepts only the current invitation's live capture authority.
// Accounting's revoked-capability exception deliberately does not apply here.
func (s *DefaultHuddleAIService) AppendTranscript(ctx context.Context, id uuid.UUID, token string, in HuddleTranscriptBatch) (HuddleTranscriptReceipt, error) {
	if in.Sequence < 1 || in.Sequence > 9007199254740991 || len(in.Entries) > 32 {
		return HuddleTranscriptReceipt{}, ErrHuddleInvalid
	}
	for _, entry := range in.Entries {
		if (entry.Speaker != "participants" && entry.Speaker != "agent") || len(entry.Text) > 4000 ||
			entry.Text == "" || strings.ContainsRune(entry.Text, 0) || !utf8.ValidString(entry.Text) {
			return HuddleTranscriptReceipt{}, ErrHuddleInvalid
		}
	}
	encoded, err := json.Marshal(in)
	if err != nil || len(encoded) > 24*1024 {
		return HuddleTranscriptReceipt{}, ErrHuddleInvalid
	}
	hash := sha256.Sum256(encoded)
	var result HuddleTranscriptReceipt
	err = s.withAttempt(ctx, id, func(q *generated.Queries, h generated.Huddle, state generated.HuddleAiState, a generated.HuddleAiSession) error {
		if h.NotesMode {
			return ErrHuddleNotesUnavailable
		}
		if err := checkHuddleAICapability(a, token); err != nil {
			return err
		}
		if err := s.authorized(ctx, q, h, state, a); err != nil {
			return err
		}
		if a.State != "claimed" || a.InvitationRevision != state.Revision {
			return ErrHuddleUnavailable
		}
		if in.Sequence != a.TranscriptSequence+1 &&
			(in.Sequence != a.TranscriptSequence || !bytes.Equal(hash[:], a.TranscriptHash)) {
			return ErrHuddleRequestConflict
		}
		row, err := q.EnsureHuddleTranscript(ctx, generated.EnsureHuddleTranscriptParams{
			CompanyID: h.CompanyID, HuddleID: h.ID, InvitationRevision: a.InvitationRevision,
			AgentName: voice.FromSnapshot(state.AgentProfile).Name,
		})
		if err != nil {
			return err
		}
		if row.EndedAt.Valid {
			return ErrHuddleUnavailable
		}
		result.Truncated = row.Truncated
		if in.Sequence == a.TranscriptSequence {
			return nil
		}
		// A new attempt is a coverage gap, never a new invitation/card.
		if a.TranscriptSequence == 0 && row.Content != "" && !row.Truncated {
			row.Content += "\n\n[Connection interrupted; some speech may be missing.]\n"
			row.LastSpeaker = ""
		}
		for _, entry := range in.Entries {
			if row.Truncated {
				break
			}
			next := row.Content
			if row.LastSpeaker != entry.Speaker {
				label := "Participants"
				if entry.Speaker == "agent" {
					label = row.AgentName + " (generated speech)"
				}
				next += "\n\n" + label + ": "
			}
			next += entry.Text
			body, err := json.Marshal(next)
			if err != nil {
				return err
			}
			// Leave room inside the discussion body's 64 KiB budget for metadata
			// and the visible coverage/truncation notice. Never truncate UTF-8.
			if len(body) > 48*1024 {
				row.Truncated = true
				break
			}
			row.Content, row.LastSpeaker = next, entry.Speaker
		}
		row.Truncated = row.Truncated || in.Truncated
		if err = q.UpdateHuddleTranscript(ctx, generated.UpdateHuddleTranscriptParams{
			ID: row.ID, Content: row.Content, LastSpeaker: row.LastSpeaker, Truncated: row.Truncated,
		}); err != nil {
			return err
		}
		result.Truncated = row.Truncated
		return q.AdvanceHuddleTranscript(ctx, generated.AdvanceHuddleTranscriptParams{ID: a.ID, TranscriptSequence: in.Sequence, TranscriptHash: hash[:]})
	})
	return result, err
}

func sealHuddleTranscript(ctx context.Context, q *generated.Queries, state generated.HuddleAiState) error {
	_, err := q.SealHuddleTranscript(ctx, generated.SealHuddleTranscriptParams{
		CompanyID: state.CompanyID, HuddleID: state.HuddleID, InvitationRevision: state.Revision,
	})
	return err
}

// A finalized capture cannot silently resume under the same invitation. Recovery
// attempts remain unsealed; terminal loss requires the moderator to invite again.
func endHuddleCapture(ctx context.Context, q *generated.Queries, state *generated.HuddleAiState) error {
	sealed, err := q.SealHuddleTranscript(ctx, generated.SealHuddleTranscriptParams{
		CompanyID: state.CompanyID, HuddleID: state.HuddleID, InvitationRevision: state.Revision,
	})
	if sealed > 0 {
		state.Failures = 5
	}
	return err
}

func (s *DefaultHuddleAIService) publishTranscripts(ctx context.Context) error {
	rows, err := s.options.Store.Queries().ListUnpostedHuddleTranscripts(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, row := range rows {
		if err = s.publishTranscript(ctx, row); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (s *DefaultHuddleAIService) publishTranscript(ctx context.Context, candidate generated.HuddleTranscript) error {
	var pending []events.Event
	err := s.options.Store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		h, err := q.LockHuddle(ctx, generated.LockHuddleParams{CompanyID: candidate.CompanyID, HuddleID: candidate.HuddleID})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		row, err := q.LockHuddleTranscript(ctx, generated.LockHuddleTranscriptParams{CompanyID: h.CompanyID, ID: candidate.ID})
		if err != nil {
			return err
		}
		if row.PostedAt.Valid {
			return nil
		}
		text := row.Content
		if text == "" {
			text = "No speech was captured."
		}
		if row.Truncated {
			text += "\n\n[Partial transcript: the capture or storage limit was reached.]"
		}
		body, err := json.Marshal(DiscussionBody{Version: 1, Markdown: text, References: []json.RawMessage{}})
		if err != nil {
			return err
		}
		metadata, err := json.Marshal(DiscussionSystem{Action: "huddle_transcript", AgentName: row.AgentName})
		if err != nil {
			return err
		}
		seq, err := q.AllocateDiscussionSequence(ctx, generated.AllocateDiscussionSequenceParams{CompanyID: h.CompanyID, ConversationID: h.ChatConversationID})
		if err != nil {
			return err
		}
		if err = q.InsertHuddleTranscriptMessage(ctx, generated.InsertHuddleTranscriptMessageParams{
			CompanyID: h.CompanyID, ConversationID: h.ChatConversationID, Seq: seq, Body: body, Metadata: metadata,
		}); err != nil {
			return err
		}
		conversation, err := q.BumpDiscussionConversation(ctx, generated.BumpDiscussionConversationParams{CompanyID: h.CompanyID, ConversationID: h.ChatConversationID})
		if err != nil {
			return err
		}
		pending = append(pending, discussionChangedEvent(conversation))
		return q.MarkHuddleTranscriptPosted(ctx, generated.MarkHuddleTranscriptPostedParams{CompanyID: h.CompanyID, ID: row.ID})
	})
	if err == nil && s.options.Bus != nil {
		publishDiscussionEvents(ctx, s.options.Bus, pending)
	}
	return err
}
