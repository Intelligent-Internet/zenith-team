package conversation

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"boardly.dev/boardly/server/internal/agent/voice"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/usage"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var ErrHuddleAIConflict = errors.New("huddle AI revision conflict")

// HuddleAIService owns room-scoped voice authority independently of human media.
// Mutations lock Huddle then AI state. External dispatch happens after commit.
type HuddleAIService interface {
	Get(context.Context, auth.UserPrincipal, uuid.UUID) (HuddleAIStatus, error)
	SetEnabled(context.Context, auth.UserPrincipal, uuid.UUID, SetHuddleAIInput) (HuddleAIStatus, error)
	Reconcile(context.Context) error
	Claim(context.Context, uuid.UUID) (HuddleAILease, error)
	Heartbeat(context.Context, uuid.UUID, string, HuddleAIHeartbeat) (HuddleAILease, error)
	Chat(context.Context, uuid.UUID, string) ([]HuddleAIChatItem, error)
	ReadContext(context.Context, uuid.UUID, string, voice.ContextRequest) (voice.ContextResult, error)
	ProposeWork(context.Context, uuid.UUID, string, HuddleProposalInput) (HuddleProposal, error)
	UpdateWorkProposal(context.Context, auth.UserPrincipal, uuid.UUID, UpdateHuddleProposalInput) (HuddleProposal, error)
	Leave(context.Context, uuid.UUID, string) (HuddleAILeaveResult, error)
	Finish(context.Context, uuid.UUID, string, HuddleAIUsage) error
	AppendTranscript(context.Context, uuid.UUID, string, HuddleTranscriptBatch) (HuddleTranscriptReceipt, error)
}

var _ HuddleAIService = (*DefaultHuddleAIService)(nil)

type HuddleAIDispatcher interface {
	DispatchHuddleAI(context.Context, string, uuid.UUID) error
	RemoveHuddleAI(context.Context, string, uuid.UUID) error
}
type HuddleAIServiceOptions struct {
	Bus *events.Bus
	// ReadContext uses the caller's transaction; it must perform only bounded database reads.
	ReadContext           func(context.Context, *generated.Queries, uuid.UUID, voice.ContextRequest) (voice.ContextResult, error)
	Store                 *db.Store
	Dispatcher            HuddleAIDispatcher
	Enabled               bool
	TicksPerSecond        int64
	ReasoningReserveTicks int64
	InputTokenTicks       int64
	CachedInputTokenTicks int64
	OutputTokenTicks      int64
}
type DefaultHuddleAIService struct{ options HuddleAIServiceOptions }

func NewHuddleAIService(options HuddleAIServiceOptions) (*DefaultHuddleAIService, error) {
	if options.Store == nil {
		return nil, errors.New("huddle AI store is required")
	}
	if options.Enabled && (options.TicksPerSecond <= 0 || options.ReasoningReserveTicks < 0) {
		return nil, errors.New("huddle AI reservation rates are required")
	}
	return &DefaultHuddleAIService{options: options}, nil
}

type HuddleAIStatus struct {
	Proposals           []HuddleProposal    `json:"proposals"`
	AllowCompanyContext bool                `json:"allowCompanyContext"`
	ToolActivity        *voice.ToolActivity `json:"toolActivity,omitempty"`
	Agent               *voice.Identity     `json:"agent,omitempty"`
	State               string              `json:"state"`
	UnavailableReason   string              `json:"unavailableReason,omitempty"`
	StopReason          string              `json:"stopReason,omitempty"`
	Disabled            bool                `json:"disabled"`
	Revision            int64               `json:"revision"`
	CanModerate         bool                `json:"canModerate"`
	CanShareContext     bool                `json:"canShareContext"`
	AttemptID           *uuid.UUID          `json:"attemptId,omitempty"`
	DeadlineAt          *time.Time          `json:"deadlineAt,omitempty"`
	CaptureState        string              `json:"captureState"`
	RoomSeconds         float64             `json:"roomSeconds"`
	HumanSeconds        float64             `json:"humanSeconds"`
	ModelSeconds        float64             `json:"modelSeconds"`
}
type SetHuddleAIInput struct {
	AllowCompanyContext bool
	AgentID             uuid.UUID
	Enabled             bool
	ExpectedRevision    int64
	RequestKey          uuid.UUID
}
type HuddleAILease struct {
	WorkNotices           []HuddleWorkNotice `json:"workNotices,omitempty"`
	ClaimedWorkNotice     *HuddleWorkNotice  `json:"claimedWorkNotice,omitempty"`
	AllowCompanyContext   bool               `json:"allowCompanyContext"`
	Agent                 voice.Profile      `json:"agent"`
	PendingChat           []HuddleAIChatItem `json:"pendingChat"`
	CompletedSeq          int64              `json:"completedSeq"`
	Capability            string             `json:"capability,omitempty"`
	HuddleID              uuid.UUID          `json:"huddleId"`
	RoomName              string             `json:"roomName"`
	LeaseExpiresAt        time.Time          `json:"leaseExpiresAt"`
	DeadlineAt            time.Time          `json:"deadlineAt"`
	Greet                 bool               `json:"greet"`
	ChatCursor            int64              `json:"chatCursor"`
	HumanIdentities       []string           `json:"humanIdentities"`
	ReasoningReserveTicks int64              `json:"reasoningReserveTicks"`
}
type HuddleAIHeartbeat struct {
	PollWork        bool   `json:"pollWork"`
	ClaimWorkNotice string `json:"claimWorkNotice"`
	CompletedSeq    int64  `json:"completedSeq"`
	ChatCursor      int64  `json:"chatCursor"`
	State           string `json:"state"`
}
type HuddleAIChatItem struct {
	ID          uuid.UUID `json:"id"`
	Seq         int64     `json:"seq"`
	AuthorID    uuid.UUID `json:"authorId"`
	DisplayName string    `json:"displayName"`
	Text        string    `json:"text"`
	CreatedAt   time.Time `json:"createdAt"`
}

func (s *DefaultHuddleAIService) Get(ctx context.Context, p auth.UserPrincipal, id uuid.UUID) (HuddleAIStatus, error) {
	var result HuddleAIStatus
	err := s.options.Store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		h, role, err := lockHuddleAIActor(ctx, q, p, id)
		if err != nil {
			return err
		}
		viewer, err := huddleAIViewerOf(ctx, q, h, p, role)
		if err != nil {
			return err
		}
		result, err = s.status(ctx, q, h, viewer)
		if err != nil {
			return err
		}
		rows, err := q.ListHuddleProposals(ctx, generated.ListHuddleProposalsParams{CompanyID: h.CompanyID, HuddleID: h.ID})
		if err != nil {
			return err
		}
		for _, row := range rows {
			result.Proposals = append(result.Proposals, huddleProposalProjection(row, h, result.Revision, result.Disabled, result.CanModerate))
		}
		// A person sees work progress only in Projects they read themselves.
		work, err := readHuddleWork(ctx, q, huddleWorkParams(h, p.UserID))
		if err != nil {
			return err
		}
		for i := range result.Proposals {
			for _, item := range work.Items {
				if item.ID == result.Proposals[i].ID {
					result.Proposals[i].Work = item.Work
				}
			}
		}
		return nil
	})
	return result, err
}
func lockHuddleAIActor(ctx context.Context, q *generated.Queries, p auth.UserPrincipal, id uuid.UUID) (generated.Huddle, auth.CompanyRole, error) {
	role, err := lockDiscussionMembership(ctx, q, p)
	if err != nil {
		return generated.Huddle{}, role, err
	}
	h, err := q.LockHuddle(ctx, generated.LockHuddleParams{CompanyID: p.CompanyID, HuddleID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return h, role, ErrHuddleNotFound
	}
	if err != nil {
		return h, role, err
	}
	if _, err = currentDiscussion(ctx, q, p, h.ChatConversationID, false); err != nil {
		if _, err = currentDiscussion(ctx, q, p, h.SourceConversationID, false); err != nil {
			return h, role, ErrHuddleForbidden
		}
	}
	return h, role, nil
}

// huddleAIViewer is what one caller may do with the voice assistant.
type huddleAIViewer struct {
	// moderate: invite or remove the assistant. The call's starter or a
	// holder of company.profile.update.
	moderate bool
	// inCall: connected to the Huddle and an active member of its chat.
	// Switching Company context on makes the caller the assistant's
	// Authorizer, so a moderator also needs this (D31, B5). Read only for a
	// moderator.
	inCall bool
}

func huddleAIViewerOf(
	ctx context.Context,
	q *generated.Queries,
	h generated.Huddle,
	p auth.UserPrincipal,
	role auth.CompanyRole,
) (huddleAIViewer, error) {
	viewer := huddleAIViewer{
		moderate: role.Can(auth.PermissionCompanyProfileUpdate) || h.StartedByUserID == p.UserID,
	}
	if !viewer.moderate {
		return viewer, nil
	}
	var err error
	viewer.inCall, err = huddleChatParticipantActive(ctx, q, h, p.UserID)
	return viewer, err
}

func (s *DefaultHuddleAIService) status(
	ctx context.Context,
	q *generated.Queries,
	h generated.Huddle,
	viewer huddleAIViewer,
) (HuddleAIStatus, error) {
	timing, err := q.GetHuddleAITiming(ctx, generated.GetHuddleAITimingParams{CompanyID: h.CompanyID, ID: h.ID})
	if err != nil {
		return HuddleAIStatus{}, err
	}
	state, err := q.GetHuddleAIState(ctx, generated.GetHuddleAIStateParams{CompanyID: h.CompanyID, HuddleID: h.ID})
	result := HuddleAIStatus{
		Proposals: []HuddleProposal{},
		State:     "disabled", Disabled: true, Revision: 1, CaptureState: "unavailable",
		CanModerate:     viewer.moderate,
		CanShareContext: viewer.moderate && viewer.inCall && s.options.ReadContext != nil,
		RoomSeconds:     timing.RoomSeconds,
		HumanSeconds:    timing.HumanSeconds,
		ModelSeconds:    timing.ModelSeconds,
	}
	if errors.Is(err, pgx.ErrNoRows) {
		if h.State == "ended" || h.State == "ending" {
			result.State = "ended"
		}
		return result, nil
	}
	if err != nil {
		return result, err
	}
	result.State = state.State
	result.AllowCompanyContext = state.AllowCompanyContext
	if state.AllowCompanyContext {
		active, err := huddleContextGrantActive(ctx, q, state)
		if err != nil {
			return result, err
		}
		if active && len(state.ToolActivity) > 2 {
			var activity voice.ToolActivity
			_ = json.Unmarshal(state.ToolActivity, &activity)
			result.ToolActivity = &activity
		}
		if !active && !state.Disabled {
			result.State = "unavailable"
			result.UnavailableReason = "agent_unavailable"
			// Say so when the person who shared context left the call,
			// so someone still in it can share it again.
			inCall, err := huddleChatParticipantActive(ctx, q, h, state.ContextGrantedByUserID.UUID)
			if err != nil {
				return result, err
			}
			if !inCall {
				result.UnavailableReason = "context_sharer_left"
			}
		}
	}
	result.Disabled = state.Disabled
	result.Revision = state.Revision
	if state.AgentID.Valid {
		profile := voice.FromSnapshot(state.AgentProfile)
		result.Agent = &profile.Identity
	}
	eligible, err := huddleVoiceEligible(ctx, q, h.CompanyID, state.AgentID)
	if err != nil {
		return result, err
	}
	if !state.Disabled && !eligible {
		result.State = "unavailable"
		result.UnavailableReason = "agent_unavailable"
	}
	if !s.options.Enabled && !state.Disabled {
		result.State = "unavailable"
		result.UnavailableReason = "service_disabled"
	}
	a, err := q.GetActiveHuddleAISession(ctx, generated.GetActiveHuddleAISessionParams{CompanyID: h.CompanyID, HuddleID: h.ID})
	if err == nil {
		result.AttemptID = &a.ID
		result.DeadlineAt = &a.DeadlineAt.Time
		if !a.LeaseExpiresAt.Time.After(time.Now()) && !state.Disabled {
			result.State = "unavailable"
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return result, err
	}
	if result.State == "unavailable" && result.UnavailableReason == "" {
		result.UnavailableReason = "connection"
		if result.AttemptID == nil {
			now := time.Now().UTC()
			available, err := q.HuddleAIHasStartBudget(ctx, generated.HuddleAIHasStartBudgetParams{
				CompanyID: h.CompanyID, HuddleID: h.ID,
				PeriodStart:  pgtype.Timestamptz{Time: time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC), Valid: true},
				MinimumTicks: s.options.ReasoningReserveTicks + s.options.TicksPerSecond,
			})
			if err != nil {
				return result, err
			}
			if !available {
				result.UnavailableReason = "budget"
			}
		}
	}
	if h.State == "ended" || h.State == "ending" {
		result.State = "ended"
		result.UnavailableReason = ""
	}
	if result.State == "unavailable" {
		latest, err := q.GetLatestHuddleAISession(ctx, generated.GetLatestHuddleAISessionParams{CompanyID: h.CompanyID, HuddleID: h.ID})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return result, err
		}
		if err == nil && latest.Generation == state.Generation {
			result.StopReason = latest.StopReason
			if result.StopReason == "" && !latest.LeaseExpiresAt.Time.After(time.Now()) {
				result.StopReason = "lease_expired"
			}
		}
	}
	return result, nil
}
func (s *DefaultHuddleAIService) SetEnabled(ctx context.Context, p auth.UserPrincipal, id uuid.UUID, in SetHuddleAIInput) (HuddleAIStatus, error) {
	if in.RequestKey == uuid.Nil || in.ExpectedRevision < 1 ||
		(in.Enabled && in.AgentID == uuid.Nil) || (!in.Enabled && (in.AgentID != uuid.Nil || in.AllowCompanyContext)) {
		return HuddleAIStatus{}, ErrHuddleInvalid
	}
	// Removing an Agent stays possible so a call can always be cleaned up.
	if in.Enabled && !s.options.Enabled {
		return HuddleAIStatus{}, ErrHuddleAIDisabled
	}
	var result HuddleAIStatus
	err := s.options.Store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		h, role, err := lockHuddleAIActor(ctx, q, p, id)
		if err != nil {
			return err
		}
		viewer, err := huddleAIViewerOf(ctx, q, h, p, role)
		if err != nil {
			return err
		}
		if !viewer.moderate {
			return ErrHuddleForbidden
		}
		// Company context makes the caller the assistant's Authorizer, so
		// only someone in the call may switch it on (D31, B5). Switching
		// the assistant on without it keeps the moderator rule alone.
		if in.AllowCompanyContext && !viewer.inCall {
			return ErrHuddleForbidden
		}
		if h.State != "open" {
			return ErrHuddleUnavailable
		}
		if err = q.EnsureHuddleAIIntent(ctx, generated.EnsureHuddleAIIntentParams{CompanyID: p.CompanyID, HuddleID: id}); err != nil {
			return err
		}
		state, err := q.LockHuddleAIState(ctx, generated.LockHuddleAIStateParams{CompanyID: p.CompanyID, HuddleID: id})
		if err != nil {
			return err
		}
		receipt, err := q.GetHuddleAIOperation(ctx, generated.GetHuddleAIOperationParams{CompanyID: p.CompanyID, HuddleID: id, UserID: p.UserID, RequestKey: in.RequestKey})
		if err == nil {
			if receipt.Enabled != in.Enabled || receipt.ExpectedRevision != in.ExpectedRevision ||
				receipt.AgentID.UUID != in.AgentID || receipt.AllowCompanyContext != in.AllowCompanyContext {
				return ErrHuddleRequestConflict
			}
			result, err = s.status(ctx, q, h, viewer)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if state.Revision != in.ExpectedRevision {
			return ErrHuddleAIConflict
		}
		profileJSON := []byte(`{}`)
		if in.Enabled {
			profileJSON, err = huddleVoiceSnapshot(ctx, q, p.CompanyID, in.AgentID)
			if err != nil {
				return err
			}
			if in.AllowCompanyContext && s.options.ReadContext == nil {
				return ErrHuddleForbidden
			}
		}
		agentID := uuid.NullUUID{UUID: in.AgentID, Valid: in.Enabled}
		if err = sealHuddleTranscript(ctx, q, state); err != nil {
			return err
		}
		if _, err = q.ToggleHuddleAI(ctx, generated.ToggleHuddleAIParams{
			CompanyID: p.CompanyID, HuddleID: id, Disabled: !in.Enabled,
			AgentID: agentID, AgentProfile: profileJSON,
			AllowCompanyContext:    in.AllowCompanyContext,
			ContextGrantedByUserID: uuid.NullUUID{UUID: p.UserID, Valid: in.AllowCompanyContext},
		}); err != nil {
			return err
		}
		a, err := q.GetActiveHuddleAISession(ctx, generated.GetActiveHuddleAISessionParams{CompanyID: p.CompanyID, HuddleID: id})
		if err == nil {
			if err = q.StopHuddleAISession(ctx, a.ID); err != nil {
				return err
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err = q.InsertHuddleAIOperation(ctx, generated.InsertHuddleAIOperationParams{
			CompanyID: p.CompanyID, HuddleID: id, UserID: p.UserID, RequestKey: in.RequestKey,
			Enabled: in.Enabled, ExpectedRevision: in.ExpectedRevision, AgentID: agentID,
			AllowCompanyContext: in.AllowCompanyContext,
		}); err != nil {
			return err
		}
		result, err = s.status(ctx, q, h, viewer)
		return err
	})
	if err == nil {
		s.signalStatus(ctx, p.CompanyID, id)
	}
	return result, err
}
func (s *DefaultHuddleAIService) Reconcile(ctx context.Context) error {
	rows, err := s.options.Store.Queries().ListHuddleAIWork(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, row := range rows {
		if err = s.reconcileRoom(ctx, row.CompanyID, row.ID); err != nil {
			failures = append(failures, err)
		}
	}
	if err = s.publishTranscripts(ctx); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}
func (s *DefaultHuddleAIService) reconcileRoom(ctx context.Context, companyID, id uuid.UUID) error {
	var dispatch, remove *generated.HuddleAiSession
	var room string
	changed := false
	err := s.options.Store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		h, err := q.LockHuddle(ctx, generated.LockHuddleParams{CompanyID: companyID, HuddleID: id})
		if err != nil {
			return err
		}
		room = h.RoomName.String
		state, err := q.LockHuddleAIState(ctx, generated.LockHuddleAIStateParams{CompanyID: companyID, HuddleID: id})
		if err != nil {
			return err
		}
		now := time.Now()
		budget, err := q.GetHuddleAIBudget(ctx, companyID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		grace, err := q.HuddleAIHasHumanGrace(ctx, generated.HuddleAIHasHumanGraceParams{CompanyID: companyID, HuddleID: id})
		if err != nil {
			return err
		}
		revoked, err := q.HuddleAIHasRevokedListener(ctx, generated.HuddleAIHasRevokedListenerParams{CompanyID: companyID, HuddleID: id})
		if err != nil {
			return err
		}
		eligible, err := huddleVoiceEligible(ctx, q, companyID, state.AgentID)
		if err != nil {
			return err
		}
		if eligible && state.AllowCompanyContext {
			eligible, err = huddleContextGrantActive(ctx, q, state)
			if err != nil {
				return err
			}
		}
		allowed := eligible && !revoked.Bool && grace.Bool && s.options.Enabled && !state.Disabled &&
			h.State == "open" && h.DeadlineAt.Time.After(now) && budget.MonthlyLimitUsdTicks > 0
		if !allowed || state.Failures >= 5 {
			if err = endHuddleCapture(ctx, q, &state); err != nil {
				return err
			}
		}
		observed := "unavailable"
		if state.Disabled {
			observed = "disabled"
		}
		if h.State != "open" {
			observed = "ended"
		}
		a, err := q.GetActiveHuddleAISession(ctx, generated.GetActiveHuddleAISessionParams{CompanyID: companyID, HuddleID: id})
		if err == nil {
			if !allowed || !a.DeadlineAt.Time.After(now) || a.State == "stopping" {
				if err = q.StopHuddleAISession(ctx, a.ID); err != nil {
					return err
				}
				remove = &a
			} else if a.LeaseExpiresAt.Time.After(now) {
				if a.State == "pending" {
					claimed, e := q.ClaimHuddleAIDispatch(ctx, a.ID)
					if e == nil {
						dispatch = &claimed
					} else if !errors.Is(e, pgx.ErrNoRows) {
						return e
					}
				}
				return nil
			}
			if now.Before(a.LeaseExpiresAt.Time.Add(5 * time.Second)) {
				return s.setState(ctx, q, state, observed, state.Generation, state.Failures, now, &changed)
			}
			if err = q.FinalizeUnclaimedHuddleAIUsage(ctx, a.ID); err != nil {
				return err
			}
			if err = q.FinishHuddleAISession(ctx, a.ID); err != nil {
				return err
			}
			remove = &a
			state.Failures++
			state.RetryAt = pgtype.Timestamptz{Time: now.Add(time.Duration(min(state.Failures*5, 60)) * time.Second), Valid: true}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if h.State == "ended" || h.State == "ending" {
			uncertain, err := q.CountUncertainHuddleAISessions(ctx, generated.CountUncertainHuddleAISessionsParams{CompanyID: companyID, HuddleID: id})
			if err != nil {
				return err
			}
			if uncertain == 0 {
				reservation, err := q.GetHuddleAIBudgetReservation(ctx, generated.GetHuddleAIBudgetReservationParams{CompanyID: companyID, HuddleID: id})
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
				if err == nil && !reservation.SettledAt.Valid {
					if err = usage.RecordHuddleSpendInTx(ctx, q, usage.RecordHuddleSpendInput{CompanyID: companyID, HuddleID: id, SpentUSDTicks: reservation.SpentUsdTicks, Final: true}); err != nil {
						return err
					}
				}
			}
		}
		if !allowed || state.Failures >= 5 || state.RetryAt.Time.After(now) {
			if state.Failures >= 5 {
				if err = endHuddleCapture(ctx, q, &state); err != nil {
					return err
				}
			}
			return s.setState(ctx, q, state, observed, state.Generation, state.Failures, state.RetryAt.Time, &changed)
		}
		humans, err := q.ListHuddleAIHumans(ctx, generated.ListHuddleAIHumansParams{CompanyID: companyID, HuddleID: id})
		if err != nil {
			return err
		}
		if len(humans) == 0 {
			return s.setState(ctx, q, state, "pending", state.Generation, state.Failures, now, &changed)
		}
		reservation, err := usage.ReserveHuddleBudgetInTx(ctx, q, usage.ReserveHuddleBudgetInput{CompanyID: companyID, HuddleID: id, Deadline: h.DeadlineAt.Time, TicksPerSecond: s.options.TicksPerSecond, AdditionalTicks: s.options.ReasoningReserveTicks})
		if errors.Is(err, usage.ErrHuddleBudgetExhausted) {
			if err = endHuddleCapture(ctx, q, &state); err != nil {
				return err
			}
			return s.setState(ctx, q, state, "unavailable", state.Generation, state.Failures, now, &changed)
		}
		if err != nil {
			return err
		}
		a, err = q.CreateHuddleAISession(ctx, generated.CreateHuddleAISessionParams{CompanyID: companyID, HuddleID: id, Generation: state.Generation + 1, DeadlineAt: reservation.DeadlineAt})
		if err != nil {
			return err
		}
		claimed, err := q.ClaimHuddleAIDispatch(ctx, a.ID)
		if err != nil {
			return err
		}
		dispatch = &claimed
		return s.setState(ctx, q, state, "starting", a.Generation, state.Failures, now, &changed)
	})
	if err != nil {
		return err
	}
	if changed || dispatch != nil || remove != nil {
		s.signalStatus(ctx, companyID, id)
	}
	if s.options.Dispatcher != nil {
		dispatchContext, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if remove != nil {
			if err = s.options.Dispatcher.RemoveHuddleAI(dispatchContext, room, remove.ID); err != nil {
				return err
			}
		}
		if dispatch != nil {
			return s.options.Dispatcher.DispatchHuddleAI(dispatchContext, room, dispatch.ID)
		}
	}
	return nil
}

// setState records the observed state. changed, when given, is set when a
// client would see a different status, so callers signal only transitions:
// the reconcile worker rewrites the same state every tick.
func (s *DefaultHuddleAIService) setState(ctx context.Context, q *generated.Queries, state generated.HuddleAiState, observed string, generation int64, failures int32, retry time.Time, changed *bool) error {
	if changed != nil && (state.State != observed || state.Generation != generation) {
		*changed = true
	}
	return q.SetHuddleAIState(ctx, generated.SetHuddleAIStateParams{CompanyID: state.CompanyID, HuddleID: state.HuddleID, State: observed, Generation: generation, Failures: failures, RetryAt: pgtype.Timestamptz{Time: retry, Valid: true}})
}
func (s *DefaultHuddleAIService) Claim(ctx context.Context, id uuid.UUID) (HuddleAILease, error) {
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return HuddleAILease{}, err
	}
	capability := base64.RawURLEncoding.EncodeToString(token)
	hash := sha256.Sum256([]byte(capability))
	var result HuddleAILease
	err := s.withAttempt(ctx, id, func(q *generated.Queries, h generated.Huddle, state generated.HuddleAiState, a generated.HuddleAiSession) error {
		if err := s.authorized(ctx, q, h, state, a); err != nil {
			return err
		}
		humans, err := q.ListHuddleAIHumans(ctx, generated.ListHuddleAIHumansParams{CompanyID: h.CompanyID, HuddleID: h.ID})
		if err != nil {
			return err
		}
		if len(humans) == 0 {
			return ErrHuddleUnavailable
		}
		a, err = q.ClaimHuddleAISession(ctx, generated.ClaimHuddleAISessionParams{ID: id, CapabilityHash: hash[:]})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrHuddleUnavailable
		}
		if err != nil {
			return err
		}
		result = s.lease(h, state, a, humans)
		receipts, err := q.ListHuddleAIUsage(ctx, generated.ListHuddleAIUsageParams{CompanyID: h.CompanyID, HuddleID: h.ID})
		if err != nil {
			return err
		}
		for _, receipt := range receipts {
			result.ReasoningReserveTicks -= (receipt.InputTokens-receipt.CachedInputTokens)*s.options.InputTokenTicks + receipt.CachedInputTokens*s.options.CachedInputTokenTicks + receipt.OutputTokens*s.options.OutputTokenTicks
		}
		result.ReasoningReserveTicks = max(0, result.ReasoningReserveTicks)
		pending, err := q.ListPendingHuddleAIChat(ctx, generated.ListPendingHuddleAIChatParams{CompanyID: h.CompanyID, HuddleID: h.ID})
		if err != nil {
			return err
		}
		for _, item := range pending {
			result.PendingChat = append(result.PendingChat, HuddleAIChatItem{ID: item.ID, Seq: item.Seq, AuthorID: item.AuthorID.UUID, DisplayName: item.DisplayName, Text: item.Content, CreatedAt: item.CreatedAt.Time})
		}
		result.Capability = capability
		result.Greet = !state.Greeted
		return q.AdvanceHuddleAIChat(ctx, generated.AdvanceHuddleAIChatParams{CompanyID: h.CompanyID, HuddleID: h.ID, ChatCursor: state.ChatCursor})
	})
	return result, err
}
func (s *DefaultHuddleAIService) withAttempt(ctx context.Context, id uuid.UUID, fn func(*generated.Queries, generated.Huddle, generated.HuddleAiState, generated.HuddleAiSession) error) error {
	return s.options.Store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		a, err := q.GetHuddleAISession(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrHuddleNotFound
		}
		if err != nil {
			return err
		}
		h, err := q.LockHuddle(ctx, generated.LockHuddleParams{CompanyID: a.CompanyID, HuddleID: a.HuddleID})
		if err != nil {
			return err
		}
		state, err := q.LockHuddleAIState(ctx, generated.LockHuddleAIStateParams{CompanyID: a.CompanyID, HuddleID: a.HuddleID})
		if err != nil {
			return err
		}
		a, err = q.GetHuddleAISession(ctx, id)
		if err != nil {
			return err
		}
		return fn(q, h, state, a)
	})
}

// signalStatus tells the people in the call that GET …/ai changed.
func (s *DefaultHuddleAIService) signalStatus(ctx context.Context, companyID, huddleID uuid.UUID) {
	PublishHuddleStatusChanged(ctx, s.options.Store.Queries(), s.options.Bus, companyID, huddleID)
}

// signalAttempt is signalStatus for the voice-worker routes, which only know
// their attempt.
func (s *DefaultHuddleAIService) signalAttempt(ctx context.Context, attemptID uuid.UUID) {
	a, err := s.options.Store.Queries().GetHuddleAISession(ctx, attemptID)
	if err == nil {
		s.signalStatus(ctx, a.CompanyID, a.HuddleID)
	}
}

func (s *DefaultHuddleAIService) authorized(ctx context.Context, q *generated.Queries, h generated.Huddle, state generated.HuddleAiState, a generated.HuddleAiSession) error {
	if state.AllowCompanyContext {
		active, err := huddleContextGrantActive(ctx, q, state)
		if err != nil {
			return err
		}
		if !active {
			return ErrHuddleUnavailable
		}
	}
	eligible, err := huddleVoiceEligible(ctx, q, h.CompanyID, state.AgentID)
	if err != nil {
		return err
	}
	if !eligible {
		return ErrHuddleUnavailable
	}
	revoked, err := q.HuddleAIHasRevokedListener(ctx, generated.HuddleAIHasRevokedListenerParams{CompanyID: h.CompanyID, HuddleID: h.ID})
	if err != nil {
		return err
	}
	now := time.Now()
	if revoked.Bool || !s.options.Enabled || state.Disabled || h.State != "open" || state.Generation != a.Generation || !a.LeaseExpiresAt.Time.After(now) || !a.DeadlineAt.Time.After(now) || !h.DeadlineAt.Time.After(now) || (a.State != "claimed" && a.State != "pending") {
		return ErrHuddleUnavailable
	}
	grace, err := q.HuddleAIHasHumanGrace(ctx, generated.HuddleAIHasHumanGraceParams{CompanyID: h.CompanyID, HuddleID: h.ID})
	if err != nil {
		return err
	}
	if !grace.Bool {
		return ErrHuddleUnavailable
	}
	if _, err := q.LockHuddleAIBudget(ctx, h.CompanyID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrHuddleUnavailable
		}
		return err
	}
	reservation, err := q.GetHuddleAIBudgetReservation(ctx, generated.GetHuddleAIBudgetReservationParams{CompanyID: h.CompanyID, HuddleID: h.ID})
	if err != nil {
		return err
	}
	if reservation.SettledAt.Valid || reservation.SpentUsdTicks >= reservation.ReservedUsdTicks {
		return ErrHuddleUnavailable
	}
	budget, err := q.GetHuddleAIBudget(ctx, h.CompanyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrHuddleUnavailable
	}
	if err != nil {
		return err
	}
	if budget.MonthlyLimitUsdTicks == 0 {
		return ErrHuddleUnavailable
	}
	return nil
}
func checkHuddleAICapability(a generated.HuddleAiSession, token string) error {
	hash := sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare(hash[:], a.CapabilityHash) != 1 {
		return ErrHuddleForbidden
	}
	return nil
}
func (s *DefaultHuddleAIService) lease(h generated.Huddle, state generated.HuddleAiState, a generated.HuddleAiSession, humans []uuid.UUID) HuddleAILease {
	identities := make([]string, 0, len(humans))
	for _, id := range humans {
		identities = append(identities, "huddle:"+h.ID.String()+":user:"+id.String())
	}
	return HuddleAILease{
		AllowCompanyContext: state.AllowCompanyContext,
		Agent:               voice.FromSnapshot(state.AgentProfile), PendingChat: []HuddleAIChatItem{},
		CompletedSeq: state.ChatCompletedSeq, HuddleID: h.ID, RoomName: h.RoomName.String,
		LeaseExpiresAt: a.LeaseExpiresAt.Time, DeadlineAt: a.DeadlineAt.Time,
		ChatCursor: state.ChatCursor, HumanIdentities: identities,
		ReasoningReserveTicks: s.options.ReasoningReserveTicks,
	}
}
func (s *DefaultHuddleAIService) Heartbeat(ctx context.Context, id uuid.UUID, token string, in HuddleAIHeartbeat) (HuddleAILease, error) {
	if len(in.ClaimWorkNotice) > 160 {
		return HuddleAILease{}, ErrHuddleInvalid
	}
	if in.ChatCursor < 0 || in.CompletedSeq < 0 || in.CompletedSeq > in.ChatCursor || (in.State != "" && in.State != "listening" && in.State != "speaking") {
		return HuddleAILease{}, ErrHuddleInvalid
	}
	var result HuddleAILease
	changed := false
	err := s.withAttempt(ctx, id, func(q *generated.Queries, h generated.Huddle, state generated.HuddleAiState, a generated.HuddleAiSession) error {
		if err := checkHuddleAICapability(a, token); err != nil {
			return err
		}
		if err := s.authorized(ctx, q, h, state, a); err != nil {
			return err
		}
		head, err := q.GetHuddleAIChatHead(ctx, generated.GetHuddleAIChatHeadParams{CompanyID: h.CompanyID, ID: h.ID})
		if err != nil {
			return err
		}
		if in.ChatCursor > head {
			return ErrHuddleInvalid
		}
		if err = q.AdvanceHuddleAIChat(ctx, generated.AdvanceHuddleAIChatParams{CompanyID: h.CompanyID, HuddleID: h.ID, ChatCursor: in.ChatCursor}); err != nil {
			return err
		}
		if err = q.CompleteHuddleAIChat(ctx, generated.CompleteHuddleAIChatParams{CompanyID: h.CompanyID, HuddleID: h.ID, ChatCompletedSeq: in.CompletedSeq}); err != nil {
			return err
		}
		a, err = q.RenewHuddleAISession(ctx, id)
		if err != nil {
			return err
		}
		if in.State != "" {
			if err = s.setState(ctx, q, state, in.State, state.Generation, state.Failures, state.RetryAt.Time, &changed); err != nil {
				return err
			}
		}
		humans, err := q.ListHuddleAIHumans(ctx, generated.ListHuddleAIHumansParams{CompanyID: h.CompanyID, HuddleID: h.ID})
		if err != nil {
			return err
		}
		state.ChatCursor = max(state.ChatCursor, in.ChatCursor)
		result = s.lease(h, state, a, humans)
		if in.State != "" {
			greeted, err := q.ClaimHuddleAIGreeting(ctx, generated.ClaimHuddleAIGreetingParams{CompanyID: h.CompanyID, HuddleID: h.ID})
			if err != nil {
				return err
			}
			result.Greet = greeted == 1
		}

		receipts, err := q.ListHuddleAIUsage(ctx, generated.ListHuddleAIUsageParams{CompanyID: h.CompanyID, HuddleID: h.ID})
		if err != nil {
			return err
		}
		for _, receipt := range receipts {
			result.ReasoningReserveTicks -= (receipt.InputTokens-receipt.CachedInputTokens)*s.options.InputTokenTicks + receipt.CachedInputTokens*s.options.CachedInputTokenTicks + receipt.OutputTokens*s.options.OutputTokenTicks
		}
		result.ReasoningReserveTicks = max(0, result.ReasoningReserveTicks)
		if state.AllowCompanyContext && len(humans) > 0 && (in.PollWork || in.ClaimWorkNotice != "") {
			result.WorkNotices, result.ClaimedWorkNotice, err = huddleWorkNotices(ctx, q, h, state.ContextGrantedByUserID.UUID, in.ClaimWorkNotice)
			if err != nil {
				return err
			}
			// A query may overlap audience/grant revocation; fence the disclosure again.
			return s.authorized(ctx, q, h, state, a)
		}
		return nil
	})
	if err != nil {
		return HuddleAILease{}, err
	}
	if changed {
		s.signalAttempt(ctx, id)
	}
	return result, err
}
func (s *DefaultHuddleAIService) Chat(ctx context.Context, id uuid.UUID, token string) ([]HuddleAIChatItem, error) {
	result := []HuddleAIChatItem{}
	err := s.withAttempt(ctx, id, func(q *generated.Queries, h generated.Huddle, state generated.HuddleAiState, a generated.HuddleAiSession) error {
		if err := checkHuddleAICapability(a, token); err != nil {
			return err
		}
		if err := s.authorized(ctx, q, h, state, a); err != nil {
			return err
		}
		rows, err := q.ListHuddleAIChat(ctx, generated.ListHuddleAIChatParams{CompanyID: h.CompanyID, ID: h.ID, Seq: state.ChatCursor})
		if err != nil {
			return err
		}
		for _, row := range rows {
			result = append(result, HuddleAIChatItem{ID: row.ID, Seq: row.Seq, AuthorID: row.AuthorID.UUID, DisplayName: row.DisplayName, Text: row.Content, CreatedAt: row.CreatedAt.Time})
		}
		return nil
	})
	return result, err
}
