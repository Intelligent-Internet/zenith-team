package conversation

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

var (
	ErrHuddleNotesUnavailable = errors.New("huddle notes unavailable")
	ErrHuddleNotesConflict    = errors.New("huddle notes revision conflict")
)

type HuddleNotesService interface {
	Get(context.Context, auth.UserPrincipal, uuid.UUID) (HuddleNotesStatus, error)
	GetPage(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID) (HuddleNotesStatus, error)
	SetEnabled(context.Context, auth.UserPrincipal, uuid.UUID, SetHuddleNotesInput) (HuddleNotesStatus, error)
	Policy(context.Context, auth.UserPrincipal, *HuddleNotesPolicy) (HuddleNotesPolicy, error)
	Claim(context.Context, uuid.UUID) (HuddleNotesLease, error)
	Append(context.Context, uuid.UUID, string, HuddleNotesBatch) (HuddleNotesLease, error)
	Reconcile(context.Context) error
	GenerateRecaps(context.Context) error
	EditRecap(context.Context, auth.UserPrincipal, EditHuddleNotesRecapInput) (HuddleNotesStatus, error)
}

var _ HuddleNotesService = (*DefaultHuddleNotesService)(nil)

type HuddleNotesDispatcher interface {
	DispatchHuddleNotes(context.Context, string, uuid.UUID) error
	RemoveHuddleNotes(context.Context, string, uuid.UUID) error
}
type HuddleNotesServiceOptions struct {
	Store      *db.Store
	Bus        *events.Bus
	Dispatcher HuddleNotesDispatcher
	Enabled    bool
	Summarize  func(context.Context, HuddleNotesRecapInput) (HuddleNotesRecap, error)
}
type DefaultHuddleNotesService struct{ options HuddleNotesServiceOptions }

func NewHuddleNotesService(o HuddleNotesServiceOptions) (*DefaultHuddleNotesService, error) {
	if o.Store == nil {
		return nil, errors.New("huddle notes store is required")
	}
	return &DefaultHuddleNotesService{options: o}, nil
}

type HuddleNotesPolicy struct {
	Enabled         bool  `json:"enabled"`
	RetentionDays   int32 `json:"retentionDays"`
	MonthlySeconds  int64 `json:"monthlySeconds"`
	ReservedSeconds int64 `json:"reservedSeconds"`
	Revision        int64 `json:"revision"`
}
type HuddleNotesSegment struct {
	ID         uuid.UUID `json:"id"`
	IntervalID uuid.UUID `json:"intervalId"`
	Speaker    string    `json:"speaker"`
	Text       string    `json:"text"`
	StartMs    int64     `json:"startMs"`
	EndMs      int64     `json:"endMs"`
}
type HuddleNotesSubmittedAction struct {
	ActionIndex int32     `json:"actionIndex"`
	TaskID      uuid.UUID `json:"taskId"`
}
type HuddleNotesStatus struct {
	SubmittedActions []HuddleNotesSubmittedAction `json:"submittedActions"`
	NextCursor       *uuid.UUID                   `json:"nextCursor,omitempty"`
	Recap            *HuddleNotesRecap            `json:"recap,omitempty"`
	RecapState       string                       `json:"recapState"`
	RecapRevision    int64                        `json:"recapRevision"`
	CanConfigure     bool                         `json:"canConfigure"`
	CanEditRecap     bool                         `json:"canEditRecap"`
	State            string                       `json:"state"`
	Revision         int64                        `json:"revision"`
	CanModerate      bool                         `json:"canModerate"`
	Available        bool                         `json:"available"`
	StopReason       string                       `json:"stopReason"`
	ExpiresAt        *time.Time                   `json:"expiresAt,omitempty"`
	Segments         []HuddleNotesSegment         `json:"segments"`
	HasMore          bool                         `json:"hasMore"`
}
type SetHuddleNotesInput struct {
	Enabled          bool
	ExpectedRevision int64
	RequestKey       uuid.UUID
}
type HuddleNotesLease struct {
	StopAfterMs    *int64    `json:"stopAfterMs,omitempty"`
	Finished       bool      `json:"finished"`
	Capability     string    `json:"capability,omitempty"`
	RoomName       string    `json:"roomName"`
	LeaseExpiresAt time.Time `json:"leaseExpiresAt"`
	DeadlineAt     time.Time `json:"deadlineAt"`
	Identities     []string  `json:"identities"`
}
type HuddleNotesBatch struct {
	Finished bool   `json:"finished"`
	Sequence int64  `json:"sequence"`
	Speaker  string `json:"speaker"`
	Text     string `json:"text"`
	StartMs  int64  `json:"startMs"`
	EndMs    int64  `json:"endMs"`
	Ready    bool   `json:"ready"`
}

func (s *DefaultHuddleNotesService) Policy(
	ctx context.Context,
	p auth.UserPrincipal,
	in *HuddleNotesPolicy,
) (HuddleNotesPolicy, error) {
	var result HuddleNotesPolicy
	err := s.options.Store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		role, err := lockDiscussionMembership(ctx, q, p)
		if err != nil {
			return err
		}
		if !role.Can(auth.PermissionCompanyProfileUpdate) {
			return ErrHuddleForbidden
		}
		if err = q.EnsureHuddleNotesPolicy(ctx, p.CompanyID); err != nil {
			return err
		}
		row, err := q.LockHuddleNotesPolicy(ctx, p.CompanyID)
		if err != nil {
			return err
		}
		if in != nil {
			if in.RetentionDays < 1 || in.RetentionDays > 365 || in.MonthlySeconds < 0 ||
				in.MonthlySeconds > 100000000 ||
				in.Enabled && in.MonthlySeconds < 9*3600 {
				return ErrHuddleInvalid
			}
			if in.Revision != row.Revision {
				return ErrHuddleNotesConflict
			}
			row, err = q.SetHuddleNotesPolicy(
				ctx,
				generated.SetHuddleNotesPolicyParams{
					CompanyID:      p.CompanyID,
					Enabled:        in.Enabled,
					RetentionDays:  in.RetentionDays,
					MonthlySeconds: in.MonthlySeconds,
				},
			)
			if err != nil {
				return err
			}
		}
		if in != nil {
			if err = q.ShortenHuddleNotesRetention(
				ctx,
				generated.ShortenHuddleNotesRetentionParams{
					CompanyID: p.CompanyID,
					Days:      in.RetentionDays,
				},
			); err != nil {
				return err
			}
		}
		reserved := row.ReservedSeconds
		if row.PeriodStart.Time.Format("2006-01") != time.Now().UTC().Format("2006-01") {
			reserved = 0
		}
		result = HuddleNotesPolicy{
			Enabled:         row.Enabled,
			RetentionDays:   row.RetentionDays,
			MonthlySeconds:  row.MonthlySeconds,
			ReservedSeconds: reserved,
			Revision:        row.Revision,
		}
		return nil
	})
	return result, err
}

func (s *DefaultHuddleNotesService) read(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	id uuid.UUID,
) (generated.Huddle, huddleAIViewer, error) {
	h, role, err := lockHuddleAIActor(ctx, q, p, id)
	if err != nil {
		return h, huddleAIViewer{}, err
	}
	// Source membership alone never grants notes/history access.
	if _, err = currentDiscussion(ctx, q, p, h.ChatConversationID, false); err != nil {
		return h, huddleAIViewer{}, ErrHuddleForbidden
	}
	viewer, err := huddleAIViewerOf(ctx, q, h, p, role)
	return h, viewer, err
}

func (s *DefaultHuddleNotesService) Get(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
) (HuddleNotesStatus, error) {
	return s.GetPage(ctx, p, id, uuid.Nil)
}

func (s *DefaultHuddleNotesService) GetPage(
	ctx context.Context,
	p auth.UserPrincipal,
	id, after uuid.UUID,
) (HuddleNotesStatus, error) {
	var result HuddleNotesStatus
	err := s.options.Store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		h, v, err := s.read(ctx, q, p, id)
		if err != nil {
			return err
		}
		result = HuddleNotesStatus{
			SubmittedActions: []HuddleNotesSubmittedAction{},
			CanConfigure:     p.Can(auth.PermissionCompanyProfileUpdate),
			State:            "off",
			Revision:         1,
			CanModerate:      v.moderate && v.inCall,
			Available:        s.options.Enabled && h.NotesMode,
			Segments:         []HuddleNotesSegment{},
		}
		if result.Available {
			if result.Available, err = q.HuddleNotesPolicyAllows(ctx, p.CompanyID); err != nil {
				return err
			}
		}
		n, err := q.GetHuddleNotes(ctx, generated.GetHuddleNotesParams{CompanyID: p.CompanyID, HuddleID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		result.State, result.Revision, result.StopReason = n.State, n.Revision, n.StopReason
		result.ExpiresAt = &n.ExpiresAt.Time
		if n.State == "expired" || !n.ExpiresAt.Time.After(time.Now()) {
			result.State = "expired"
			return nil
		}
		afterTime := pgtype.Timestamptz{Time: time.Unix(0, 0), Valid: true}
		if after != uuid.Nil {
			cursor, e := q.GetHuddleNotesSegmentCursor(
				ctx,
				generated.GetHuddleNotesSegmentCursorParams{
					CompanyID: p.CompanyID,
					NotesID:   n.ID,
					ID:        after,
				},
			)
			if errors.Is(e, pgx.ErrNoRows) {
				return ErrHuddleInvalid
			}
			if e != nil {
				return e
			}
			afterTime = cursor.CreatedAt
		}
		rows, err := q.ListHuddleNotesSegments(
			ctx,
			generated.ListHuddleNotesSegmentsParams{
				CompanyID: p.CompanyID,
				NotesID:   n.ID,
				AfterTime: afterTime,
				AfterID:   after,
			},
		)
		if err != nil {
			return err
		}
		for _, r := range rows {
			result.Segments = append(
				result.Segments,
				HuddleNotesSegment{
					ID:         r.ID,
					IntervalID: r.IntervalID,
					Speaker:    r.Speaker,
					Text:       r.Content,
					StartMs:    r.StartMs,
					EndMs:      r.EndMs,
				},
			)
		}
		actions, e := q.ListHuddleNotesActions(
			ctx,
			generated.ListHuddleNotesActionsParams{CompanyID: p.CompanyID, NotesID: n.ID},
		)
		if e != nil {
			return e
		}
		for _, action := range actions {
			if !action.TaskID.Valid {
				continue
			}
			if _, e = auth.RequireTaskPermission(
				ctx,
				q,
				p,
				action.TaskID.UUID,
				auth.ProjectPermissionRead,
			); e == nil {
				result.SubmittedActions = append(
					result.SubmittedActions,
					HuddleNotesSubmittedAction{
						ActionIndex: action.ActionIndex,
						TaskID:      action.TaskID.UUID,
					},
				)
			} else if !errors.Is(e, auth.ErrTaskNotVisible) && !errors.Is(e, auth.ErrProjectNotVisible) &&
				!errors.Is(e, auth.ErrProjectPermissionRequired) {
				return e
			}
		}
		result.HasMore = len(rows) == 100
		if result.HasMore {
			cursor := rows[len(rows)-1].ID
			result.NextCursor = &cursor
		}
		result.RecapState, result.RecapRevision = n.RecapState, n.RecapRevision
		result.CanEditRecap = v.moderate && n.RecapState == "ready"
		if n.RecapState == "ready" {
			var recap HuddleNotesRecap
			if err = json.Unmarshal(n.Recap, &recap); err != nil {
				return err
			}
			result.Recap = &recap
		}
		return nil
	})
	return result, err
}

func (s *DefaultHuddleNotesService) SetEnabled(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
	in SetHuddleNotesInput,
) (HuddleNotesStatus, error) {
	if in.RequestKey == uuid.Nil || in.ExpectedRevision < 1 {
		return HuddleNotesStatus{}, ErrHuddleInvalid
	}
	var dispatch *generated.HuddleNotesInterval
	var room string
	var removeIDs []uuid.UUID
	err := s.options.Store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		h, v, err := s.read(ctx, q, p, id)
		if err != nil {
			return err
		}
		if !v.moderate || !v.inCall {
			return ErrHuddleForbidden
		}
		if h.State != "open" || !h.NotesMode {
			return ErrHuddleNotesUnavailable
		}
		if err = q.EnsureHuddleNotesPolicy(ctx, p.CompanyID); err != nil {
			return err
		}
		policy, err := q.LockHuddleNotesPolicy(ctx, p.CompanyID)
		if err != nil {
			return err
		}
		if err = q.EnsureHuddleNotes(
			ctx,
			generated.EnsureHuddleNotesParams{
				CompanyID: p.CompanyID,
				HuddleID:  id,
				ExpiresAt: pgtype.Timestamptz{
					Time:  time.Now().Add(time.Duration(policy.RetentionDays) * 24 * time.Hour),
					Valid: true,
				},
			},
		); err != nil {
			return err
		}
		n, err := q.LockHuddleNotes(ctx, generated.LockHuddleNotesParams{CompanyID: p.CompanyID, HuddleID: id})
		if err != nil {
			return err
		}
		if n.State == "expired" || !n.ExpiresAt.Time.After(time.Now()) {
			return ErrHuddleNotesUnavailable
		}
		if in.Enabled {
			_, err = q.GetHuddleNotesIntervalReplay(
				ctx,
				generated.GetHuddleNotesIntervalReplayParams{
					CompanyID:  p.CompanyID,
					NotesID:    n.ID,
					RequestKey: in.RequestKey,
				},
			)
			if err == nil {
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if n.State == "stopping" {
			if in.Enabled {
				return ErrHuddleNotesConflict
			}
			return nil
		}
		if in.ExpectedRevision != n.Revision {
			return ErrHuddleNotesConflict
		}
		if !in.Enabled {
			intervals, e := q.ListHuddleNotesIntervals(
				ctx,
				generated.ListHuddleNotesIntervalsParams{CompanyID: p.CompanyID, NotesID: n.ID},
			)
			if e != nil {
				return e
			}
			for _, i := range intervals {
				if i.State == "claimed" && i.LeaseExpiresAt.Time.After(time.Now()) {
					if e = q.BeginHuddleNotesDrain(
						ctx,
						generated.BeginHuddleNotesDrainParams{
							CompanyID: p.CompanyID,
							NotesID:   n.ID,
						},
					); e != nil {
						return e
					}
					return q.SetHuddleNotesState(
						ctx,
						generated.SetHuddleNotesStateParams{
							CompanyID:  p.CompanyID,
							ID:         n.ID,
							State:      "stopping",
							StopReason: "person_stopped",
						},
					)
				}
				if i.State != "finished" {
					removeIDs = append(removeIDs, i.ID)
				}
			}
			room = h.RoomName.String
			return s.stop(ctx, q, n, "stopped", "person_stopped")
		}
		if !s.options.Enabled || s.options.Dispatcher == nil || !policy.Enabled {
			return ErrHuddleNotesUnavailable
		}
		if n.State == "starting" || n.State == "capturing" {
			return nil
		}
		// Reserve the maximum interval up front, including nine admitted audio tracks.
		count, err := q.ReserveHuddleNotes(
			ctx,
			generated.ReserveHuddleNotesParams{CompanyID: p.CompanyID, Seconds: 9 * 3600},
		)
		if err != nil {
			return err
		}
		if count != 1 {
			return ErrHuddleNotesUnavailable
		}
		if _, err = q.StartHuddleNotes(
			ctx,
			generated.StartHuddleNotesParams{
				CompanyID:       p.CompanyID,
				ID:              n.ID,
				StartedByUserID: uuid.NullUUID{UUID: p.UserID, Valid: true},
			},
		); err != nil {
			return err
		}
		deadline := minTime(h.DeadlineAt.Time, time.Now().Add(time.Hour))
		interval, err := q.CreateHuddleNotesInterval(
			ctx,
			generated.CreateHuddleNotesIntervalParams{
				CompanyID:  p.CompanyID,
				NotesID:    n.ID,
				RequestKey: in.RequestKey,
				DeadlineAt: pgtype.Timestamptz{Time: deadline, Valid: true},
			},
		)
		if err != nil {
			return err
		}
		dispatch = &interval
		room = h.RoomName.String
		return nil
	})
	if err != nil {
		return HuddleNotesStatus{}, err
	}
	s.signal(ctx, p.CompanyID, id)
	if s.options.Dispatcher != nil {
		for _, attemptID := range removeIDs {
			if err = s.options.Dispatcher.RemoveHuddleNotes(ctx, room, attemptID); err != nil {
				return HuddleNotesStatus{}, err
			}
		}
	}
	if dispatch != nil {
		_ = s.options.Dispatcher.DispatchHuddleNotes(ctx, room, dispatch.ID)
	}
	return s.Get(ctx, p, id)
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func (s *DefaultHuddleNotesService) stop(
	ctx context.Context,
	q *generated.Queries,
	n generated.HuddleNote,
	state, reason string,
) error {
	if err := q.FinishHuddleNotesIntervals(
		ctx,
		generated.FinishHuddleNotesIntervalsParams{CompanyID: n.CompanyID, NotesID: n.ID},
	); err != nil {
		return err
	}
	return q.SetHuddleNotesState(
		ctx,
		generated.SetHuddleNotesStateParams{CompanyID: n.CompanyID, ID: n.ID, State: state, StopReason: reason},
	)
}

func (s *DefaultHuddleNotesService) signal(ctx context.Context, companyID, huddleID uuid.UUID) {
	PublishHuddleStatusChanged(ctx, s.options.Store.Queries(), s.options.Bus, companyID, huddleID)
}

func (s *DefaultHuddleNotesService) withInterval(
	ctx context.Context,
	id uuid.UUID,
	fn func(*generated.Queries, generated.Huddle, generated.HuddleNote, generated.GetHuddleNotesIntervalRow) error,
) error {
	return s.options.Store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		i, err := q.GetHuddleNotesInterval(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrHuddleNotFound
		}
		if err != nil {
			return err
		}
		n, err := q.GetHuddleNotes(
			ctx,
			generated.GetHuddleNotesParams{CompanyID: i.CompanyID, HuddleID: i.HuddleID},
		)
		if err != nil {
			return err
		}
		authorizer := n.StartedByUserID
		role, err := lockDiscussionMembership(
			ctx,
			q,
			auth.UserPrincipal{CompanyID: i.CompanyID, UserID: authorizer.UUID},
		)
		if errors.Is(err, ErrDiscussionNotFound) {
			return ErrHuddleNotesUnavailable
		}
		if err != nil {
			return err
		}
		h, err := q.LockHuddle(ctx, generated.LockHuddleParams{CompanyID: i.CompanyID, HuddleID: i.HuddleID})
		if err != nil {
			return err
		}
		if h.StartedByUserID != n.StartedByUserID.UUID && !role.Can(auth.PermissionCompanyProfileUpdate) {
			return ErrHuddleNotesUnavailable
		}
		_, err = currentDiscussion(
			ctx,
			q,
			auth.UserPrincipal{CompanyID: i.CompanyID, UserID: authorizer.UUID},
			h.ChatConversationID,
			false,
		)
		if errors.Is(err, ErrDiscussionNotFound) || errors.Is(err, ErrDiscussionForbidden) {
			return ErrHuddleNotesUnavailable
		}
		if err != nil {
			return err
		}
		policy, err := q.LockHuddleNotesPolicy(ctx, i.CompanyID)
		if err != nil {
			return err
		}
		n, err = q.LockHuddleNotes(
			ctx,
			generated.LockHuddleNotesParams{CompanyID: i.CompanyID, HuddleID: i.HuddleID},
		)
		if err != nil {
			return err
		}
		i, err = q.GetHuddleNotesInterval(ctx, id)
		if err != nil {
			return err
		}
		now := time.Now()
		if n.StartedByUserID != authorizer {
			return ErrHuddleNotesUnavailable
		}
		// A finished drain can acknowledge a retry, but Append cannot write to it.
		finishing := i.StopRequestedAt.Valid &&
			(n.State == "stopping" || (n.State == "stopped" && i.State == "finished"))
		if !s.options.Enabled || !policy.Enabled || policy.MonthlySeconds == 0 ||
			policy.ReservedSeconds > policy.MonthlySeconds ||
			h.State != "open" ||
			!h.NotesMode ||
			!h.DeadlineAt.Time.After(now) ||
			!n.ExpiresAt.Time.After(now) ||
			(n.State != "starting" && n.State != "capturing" && !finishing) ||
			(i.State == "finished" && !finishing) ||
			!i.LeaseExpiresAt.Time.After(now) ||
			!i.DeadlineAt.Time.After(now) ||
			(i.DrainUntil.Valid && !i.DrainUntil.Time.After(now)) {
			return ErrHuddleNotesUnavailable
		}
		revoked, err := q.HuddleAIHasRevokedListener(
			ctx,
			generated.HuddleAIHasRevokedListenerParams{CompanyID: h.CompanyID, HuddleID: h.ID},
		)
		if err != nil {
			return err
		}
		if revoked.Bool {
			return ErrHuddleNotesUnavailable
		}
		humans, err := q.ListHuddleAIHumans(
			ctx,
			generated.ListHuddleAIHumansParams{CompanyID: h.CompanyID, HuddleID: h.ID},
		)
		if err != nil {
			return err
		}
		if len(humans) == 0 {
			return ErrHuddleNotesUnavailable
		}
		return fn(q, h, n, i)
	})
}

func (s *DefaultHuddleNotesService) lease(
	ctx context.Context,
	q *generated.Queries,
	h generated.Huddle,
	i generated.HuddleNotesInterval,
) (HuddleNotesLease, error) {
	humans, err := q.ListHuddleAIHumans(
		ctx,
		generated.ListHuddleAIHumansParams{CompanyID: h.CompanyID, HuddleID: h.ID},
	)
	if err != nil {
		return HuddleNotesLease{}, err
	}
	identities := make([]string, 0, len(humans)+1)
	for _, id := range humans {
		identities = append(identities, "huddle:"+h.ID.String()+":user:"+id.String())
	}
	ai, err := q.GetActiveHuddleAISession(
		ctx,
		generated.GetActiveHuddleAISessionParams{CompanyID: h.CompanyID, HuddleID: h.ID},
	)
	if err == nil {
		identities = append(identities, "helia:"+ai.ID.String())
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return HuddleNotesLease{}, err
	}
	lease := HuddleNotesLease{
		RoomName:       h.RoomName.String,
		LeaseExpiresAt: i.LeaseExpiresAt.Time,
		DeadlineAt:     i.DeadlineAt.Time,
		Identities:     identities,
		Finished:       i.State == "finished",
	}
	if i.StopRequestedAt.Valid {
		cutoff := max(int64(0), i.StopRequestedAt.Time.Sub(i.StartedAt.Time).Milliseconds())
		lease.StopAfterMs = &cutoff
		lease.DeadlineAt = minTime(lease.DeadlineAt, i.DrainUntil.Time)
	}
	return lease, nil
}

func (s *DefaultHuddleNotesService) Claim(ctx context.Context, id uuid.UUID) (HuddleNotesLease, error) {
	var result HuddleNotesLease
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return result, err
	}
	token := base64.RawURLEncoding.EncodeToString(secret)
	hash := sha256.Sum256([]byte(token))
	err := s.withInterval(
		ctx,
		id,
		func(q *generated.Queries, h generated.Huddle, _ generated.HuddleNote, _ generated.GetHuddleNotesIntervalRow) error {
			row, err := q.ClaimHuddleNotesInterval(
				ctx,
				generated.ClaimHuddleNotesIntervalParams{ID: id, CapabilityHash: hash[:]},
			)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrHuddleNotesConflict
			}
			if err != nil {
				return err
			}
			result, err = s.lease(ctx, q, h, row)
			result.Capability = token
			return err
		},
	)
	return result, err
}

func (s *DefaultHuddleNotesService) Append(
	ctx context.Context,
	id uuid.UUID,
	token string,
	in HuddleNotesBatch,
) (HuddleNotesLease, error) {
	var result HuddleNotesLease
	if (in.Finished && (in.Sequence != 0 || in.Text != "" || in.Ready)) || in.Sequence < 0 || in.Sequence > 10000 ||
		len(in.Speaker) > 200 ||
		len(in.Text) > 16000 ||
		!utf8.ValidString(in.Text) ||
		strings.ContainsRune(in.Text, 0) ||
		in.StartMs < 0 ||
		in.EndMs < in.StartMs ||
		in.EndMs > 3600000 ||
		(in.Sequence == 0 && in.Text != "") ||
		(in.Sequence > 0 && in.Text == "") {
		return result, ErrHuddleInvalid
	}
	encoded, err := json.Marshal(in)
	if err != nil {
		return result, err
	}
	batchHash := sha256.Sum256(encoded)
	err = s.withInterval(
		ctx,
		id,
		func(q *generated.Queries, h generated.Huddle, n generated.HuddleNote, i generated.GetHuddleNotesIntervalRow) error {
			hash := sha256.Sum256([]byte(token))
			if subtle.ConstantTimeCompare(hash[:], i.CapabilityHash) != 1 {
				return ErrHuddleForbidden
			}
			if i.State == "finished" && in.Finished && i.StopRequestedAt.Valid {
				result = HuddleNotesLease{
					Finished:       true,
					RoomName:       h.RoomName.String,
					LeaseExpiresAt: i.LeaseExpiresAt.Time,
					DeadlineAt:     i.DeadlineAt.Time,
					Identities:     []string{},
				}
				return nil
			}
			if i.State != "claimed" {
				return ErrHuddleNotesUnavailable
			}
			if in.Finished && n.State != "stopping" {
				return ErrHuddleNotesConflict
			}
			if i.StopRequestedAt.Valid && in.Sequence > i.Sequence &&
				in.EndMs > i.StopRequestedAt.Time.Sub(i.StartedAt.Time).Milliseconds() {
				return ErrHuddleNotesConflict
			}
			if in.Sequence > 0 {
				if in.Sequence == i.Sequence {
					if !bytes.Equal(batchHash[:], i.BatchHash) {
						return ErrHuddleNotesConflict
					}
				} else {
					if in.Sequence != i.Sequence+1 {
						return ErrHuddleNotesConflict
					}
					if err := q.InsertHuddleNotesSegment(
						ctx,
						generated.InsertHuddleNotesSegmentParams{
							CompanyID:  i.CompanyID,
							IntervalID: i.ID,
							Sequence:   in.Sequence,
							Speaker:    in.Speaker,
							Content:    in.Text,
							StartMs:    in.StartMs,
							EndMs:      in.EndMs,
						},
					); err != nil {
						return err
					}
					if err := q.AdvanceHuddleNotesInterval(
						ctx,
						generated.AdvanceHuddleNotesIntervalParams{
							ID:        id,
							Sequence:  in.Sequence,
							BatchHash: batchHash[:],
						},
					); err != nil {
						return err
					}
				}
			}
			if in.Ready && n.State == "starting" {
				if err := q.SetHuddleNotesState(
					ctx,
					generated.SetHuddleNotesStateParams{
						CompanyID: n.CompanyID,
						ID:        n.ID,
						State:     "capturing",
					},
				); err != nil {
					return err
				}
			}
			row, err := q.RenewHuddleNotesInterval(ctx, id)
			if err != nil {
				return err
			}
			result, err = s.lease(ctx, q, h, row)
			if err != nil {
				return err
			}
			if in.Sequence > 0 && !slices.Contains(result.Identities, in.Speaker) {
				return ErrHuddleForbidden
			}
			if in.Finished {
				if err = s.stop(ctx, q, n, "stopped", "person_stopped"); err != nil {
					return err
				}
				result.Finished = true
			}
			return nil
		},
	)
	return result, err
}
