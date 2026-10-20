package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"boardly.dev/boardly/server/internal/auth"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type HuddleNotesStatement struct {
	Text      string      `json:"text"`
	SourceIDs []uuid.UUID `json:"sourceIds"`
}
type HuddleNotesRecap struct {
	Summary   string                 `json:"summary"`
	Decisions []HuddleNotesStatement `json:"decisions"`
	Actions   []HuddleNotesStatement `json:"actions"`
	Questions []HuddleNotesStatement `json:"questions"`
}
type HuddleNotesRecapInput struct{ Segments []HuddleNotesSegment }

func emptyNotesRecap() HuddleNotesRecap {
	return HuddleNotesRecap{Decisions: []HuddleNotesStatement{}, Actions: []HuddleNotesStatement{}, Questions: []HuddleNotesStatement{}}
}
func validateNotesRecap(in HuddleNotesRecap, segments []HuddleNotesSegment) error {
	if len(in.Summary) > 8000 || len(in.Actions) > 20 || len(in.Decisions) > 40 || len(in.Questions) > 40 {
		return ErrHuddleInvalid
	}
	sources := make(map[uuid.UUID]bool, len(segments))
	for _, s := range segments {
		sources[s.ID] = true
	}
	for _, group := range [][]HuddleNotesStatement{in.Decisions, in.Actions, in.Questions} {
		if group == nil {
			return ErrHuddleInvalid
		}
		for _, item := range group {
			if strings.TrimSpace(item.Text) == "" || len(item.Text) > 4000 || len(item.SourceIDs) == 0 || len(item.SourceIDs) > 20 {
				return ErrHuddleInvalid
			}
			for _, id := range item.SourceIDs {
				if !sources[id] {
					return ErrHuddleInvalid
				}
			}
		}
	}
	return nil
}
func notesSegments(rows []generated.HuddleNotesSegment) []HuddleNotesSegment {
	result := make([]HuddleNotesSegment, 0, len(rows))
	for _, r := range rows {
		result = append(result, HuddleNotesSegment{ID: r.ID, IntervalID: r.IntervalID, Speaker: r.Speaker, Text: r.Content, StartMs: r.StartMs, EndMs: r.EndMs})
	}
	return result
}
func (s *DefaultHuddleNotesService) GenerateRecaps(ctx context.Context) error {
	if !s.options.Enabled || s.options.Summarize == nil {
		return nil
	}
	if err := s.options.Store.Queries().FailExpiredHuddleNotesRecaps(ctx); err != nil {
		return err
	}
	candidates, err := s.options.Store.Queries().ListHuddleNotesRecapCandidates(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, candidate := range candidates {
		if err = s.generateRecap(ctx, candidate); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
func (s *DefaultHuddleNotesService) generateRecap(ctx context.Context, c generated.ListHuddleNotesRecapCandidatesRow) error {
	var claimed generated.HuddleNote
	var input HuddleNotesRecapInput
	err := s.options.Store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		h, err := q.LockHuddle(ctx, generated.LockHuddleParams{CompanyID: c.CompanyID, HuddleID: c.HuddleID})
		if err != nil {
			return err
		}
		policy, err := q.LockHuddleNotesPolicy(ctx, c.CompanyID)
		if err != nil {
			return err
		}
		if !policy.Enabled {
			return ErrHuddleNotesUnavailable
		}
		n, err := q.LockHuddleNotes(ctx, generated.LockHuddleNotesParams{CompanyID: c.CompanyID, HuddleID: c.HuddleID})
		if err != nil {
			return err
		}
		if h.State != "ended" || n.State == "expired" || !n.ExpiresAt.Time.After(time.Now()) {
			return ErrHuddleNotesUnavailable
		}
		if err = s.stop(ctx, q, n, "ended", notesEndReason(n)); err != nil {
			return err
		}
		claimed, err = q.ClaimHuddleNotesRecap(ctx, generated.ClaimHuddleNotesRecapParams{CompanyID: n.CompanyID, ID: n.ID})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrHuddleNotesConflict
		}
		if err != nil {
			return err
		}
		if err = q.SnapshotHuddleNotesChat(ctx, generated.SnapshotHuddleNotesChatParams{CompanyID: n.CompanyID, ID: n.ID}); err != nil {
			return err
		}
		rows, err := q.AllHuddleNotesSegments(ctx, generated.AllHuddleNotesSegmentsParams{CompanyID: n.CompanyID, NotesID: n.ID})
		if err != nil {
			return err
		}
		input.Segments = notesSegments(rows)
		return nil
	})
	if errors.Is(err, ErrHuddleNotesConflict) || errors.Is(err, ErrHuddleNotesUnavailable) {
		return nil
	}
	if err != nil {
		return err
	}
	recap := emptyNotesRecap()
	var generationErr error
	if len(input.Segments) > 10000 {
		generationErr = errors.New("notes recap segment limit reached")
	} else if len(input.Segments) == 0 {
		recap.Summary = "No speech was captured."
	} else {
		pass, cancel := context.WithTimeout(ctx, 90*time.Second)
		recap, generationErr = s.options.Summarize(pass, input)
		cancel()
		if generationErr == nil {
			generationErr = validateNotesRecap(recap, input.Segments)
		}
	}
	state := "ready"
	if generationErr != nil {
		state = "failed"
		recap = emptyNotesRecap()
	}
	err = s.options.Store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := q.LockHuddle(ctx, generated.LockHuddleParams{CompanyID: c.CompanyID, HuddleID: c.HuddleID}); err != nil {
			return err
		}
		policy, err := q.LockHuddleNotesPolicy(ctx, c.CompanyID)
		if err != nil {
			return err
		}
		if !policy.Enabled {
			return nil
		}
		n, err := q.LockHuddleNotes(ctx, generated.LockHuddleNotesParams{CompanyID: c.CompanyID, HuddleID: c.HuddleID})
		if err != nil {
			return err
		}
		if n.State == "expired" || !n.ExpiresAt.Time.After(time.Now()) || n.RecapToken != claimed.RecapToken || n.RecapState != "processing" || n.RecapEditedByUserID.Valid {
			return nil
		}
		return saveNotesRecap(ctx, q, n, recap, state, uuid.NullUUID{})
	})
	if err != nil {
		return err
	}
	s.signal(ctx, c.CompanyID, c.HuddleID)
	if generationErr != nil {
		return fmt.Errorf("generate huddle notes recap: %w", generationErr)
	}
	return nil
}
func saveNotesRecap(ctx context.Context, q *generated.Queries, n generated.HuddleNote, in HuddleNotesRecap, state string, editor uuid.NullUUID) error {
	encoded, err := json.Marshal(in)
	if err != nil {
		return err
	}
	row, err := q.SetHuddleNotesRecap(ctx, generated.SetHuddleNotesRecapParams{CompanyID: n.CompanyID, ID: n.ID, Recap: encoded, RecapState: state, RecapEditedByUserID: editor})
	if err != nil {
		return err
	}
	return q.InsertHuddleNotesRecapRevision(ctx, generated.InsertHuddleNotesRecapRevisionParams{CompanyID: n.CompanyID, NotesID: n.ID, Revision: row.RecapRevision, Content: encoded, EditedByUserID: editor})
}

type EditHuddleNotesRecapInput struct {
	HuddleID         uuid.UUID
	ExpectedRevision int64
	Recap            HuddleNotesRecap
}

func (s *DefaultHuddleNotesService) EditRecap(ctx context.Context, p auth.UserPrincipal, in EditHuddleNotesRecapInput) (HuddleNotesStatus, error) {
	err := s.options.Store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		_, v, err := s.read(ctx, q, p, in.HuddleID)
		if err != nil {
			return err
		}
		if !v.moderate {
			return ErrHuddleForbidden
		}
		n, err := q.LockHuddleNotes(ctx, generated.LockHuddleNotesParams{CompanyID: p.CompanyID, HuddleID: in.HuddleID})
		if err != nil {
			return err
		}
		if n.State == "expired" || !n.ExpiresAt.Time.After(time.Now()) {
			return ErrHuddleNotesUnavailable
		}
		if n.RecapState != "ready" || n.RecapRevision != in.ExpectedRevision {
			return ErrHuddleNotesConflict
		}
		rows, err := q.AllHuddleNotesSegments(ctx, generated.AllHuddleNotesSegmentsParams{CompanyID: p.CompanyID, NotesID: n.ID})
		if err != nil {
			return err
		}
		if err = validateNotesRecap(in.Recap, notesSegments(rows)); err != nil {
			return err
		}
		// Preserve action positions after submission; edits cannot silently retarget a receipt.
		var old HuddleNotesRecap
		if err = json.Unmarshal(n.Recap, &old); err != nil {
			return err
		}
		if len(old.Actions) != len(in.Recap.Actions) {
			return ErrHuddleNotesConflict
		}
		for index := range old.Actions {
			_, err = q.GetHuddleNotesAction(ctx, generated.GetHuddleNotesActionParams{CompanyID: p.CompanyID, NotesID: n.ID, ActionIndex: int32(index)})
			if err == nil {
				a, _ := json.Marshal(old.Actions[index])
				b, _ := json.Marshal(in.Recap.Actions[index])
				if string(a) != string(b) {
					return ErrHuddleNotesConflict
				}
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		return saveNotesRecap(ctx, q, n, in.Recap, "ready", uuid.NullUUID{UUID: p.UserID, Valid: true})
	})
	if err != nil {
		return HuddleNotesStatus{}, err
	}
	s.signal(ctx, p.CompanyID, in.HuddleID)
	return s.Get(ctx, p, in.HuddleID)
}

type HuddleNotesActionInput struct {
	HuddleID         uuid.UUID
	ActionIndex      int32
	ExpectedRevision int64
}

// LockHuddleNotesActionInTx participates in the Task owner's transaction.
func LockHuddleNotesActionInTx(ctx context.Context, q *generated.Queries, p auth.UserPrincipal, in HuddleNotesActionInput) (generated.HuddleNote, HuddleNotesStatement, error) {
	h, role, err := lockHuddleAIActor(ctx, q, p, in.HuddleID)
	if err != nil {
		return generated.HuddleNote{}, HuddleNotesStatement{}, err
	}
	if _, err = currentDiscussion(ctx, q, p, h.ChatConversationID, false); err != nil {
		return generated.HuddleNote{}, HuddleNotesStatement{}, ErrHuddleForbidden
	}
	if h.StartedByUserID != p.UserID && !role.Can(auth.PermissionCompanyProfileUpdate) {
		return generated.HuddleNote{}, HuddleNotesStatement{}, ErrHuddleForbidden
	}
	n, err := q.LockHuddleNotes(ctx, generated.LockHuddleNotesParams{CompanyID: p.CompanyID, HuddleID: in.HuddleID})
	if err != nil {
		return n, HuddleNotesStatement{}, err
	}
	if n.State == "expired" || !n.ExpiresAt.Time.After(time.Now()) || n.RecapState != "ready" {
		return n, HuddleNotesStatement{}, ErrHuddleNotesUnavailable
	}
	var recap HuddleNotesRecap
	if err = json.Unmarshal(n.Recap, &recap); err != nil {
		return n, HuddleNotesStatement{}, err
	}
	if in.ActionIndex < 0 || int(in.ActionIndex) >= len(recap.Actions) {
		return n, HuddleNotesStatement{}, ErrHuddleInvalid
	}
	return n, recap.Actions[in.ActionIndex], nil
}
