package conversation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/inbox"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// HuddleState is the server-owned lifecycle state of one call session.
type HuddleState string

const (
	HuddleStateStarting HuddleState = "starting"
	HuddleStateOpen     HuddleState = "open"
	HuddleStateEnding   HuddleState = "ending"
	HuddleStateEnded    HuddleState = "ended"
)

// Huddle is the durable call projection. LiveKit room readiness and human
// admission are separate transitions and are added by the next lifecycle slice.
type Huddle struct {
	ID                   uuid.UUID   `json:"id"`
	CompanyID            uuid.UUID   `json:"companyId"`
	SourceConversationID uuid.UUID   `json:"sourceConversationId"`
	ChatConversationID   uuid.UUID   `json:"chatConversationId"`
	StartedByUserID      uuid.UUID   `json:"startedByUserId"`
	State                HuddleState `json:"state"`
	Revision             int64       `json:"revision"`
	DeadlineAt           time.Time   `json:"deadlineAt"`
	RoomName             *string     `json:"roomName,omitempty"`
	CreatedAt            time.Time   `json:"createdAt"`
	UpdatedAt            time.Time   `json:"updatedAt"`
	EndedAt              *time.Time  `json:"endedAt,omitempty"`
}

type StartHuddleRequest struct {
	RequestKey uuid.UUID
}

type StartHuddleResult struct {
	Huddle   Huddle `json:"-"`
	Created  bool   `json:"-"`
	Replayed bool   `json:"-"`
}

// HuddleService owns call-session identity and lifecycle intent. Media vendor
// calls happen after this transaction through the future LiveKit adapter.
type HuddleService interface {
	Start(context.Context, auth.UserPrincipal, uuid.UUID, StartHuddleRequest) (StartHuddleResult, error)
	Join(context.Context, auth.UserPrincipal, uuid.UUID, JoinHuddleRequest) (JoinHuddleResult, error)
	ConfirmConnected(context.Context, uuid.UUID, uuid.UUID) (JoinHuddleResult, error)
	ConfirmConnectedByIdentity(context.Context, uuid.UUID, string, string) (JoinHuddleResult, error)
	ConfirmConnectedByIdentityAnyCompany(context.Context, string, string) (JoinHuddleResult, error)
	EndFromMedia(context.Context, uuid.UUID, string) error
	EndFromMediaAnyCompany(context.Context, string) error
	EndParticipantFromMedia(context.Context, uuid.UUID, string, string) error
	EndParticipantFromMediaAnyCompany(context.Context, string, string) error
	Leave(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID) (JoinHuddleResult, error)
	SweepExpiredReservations(context.Context, time.Time) (int, error)
	ReconcileMediaRooms(context.Context) (int, error)
	SweepExpiredInvitations(context.Context, time.Time) (int, error)
	Invite(context.Context, auth.UserPrincipal, uuid.UUID, InviteHuddleRequest) (HuddleInvitation, error)
	RespondInvitation(context.Context, auth.UserPrincipal, uuid.UUID, RespondHuddleInvitationRequest) (HuddleInvitation, error)
	ListPendingInvitations(context.Context, auth.UserPrincipal) ([]HuddleInvitation, error)
	ListInvitationHistory(context.Context, auth.UserPrincipal) ([]HuddleInvitation, error)
	DeliverMissedInvitations(context.Context) (int, error)
	ListConnections(context.Context, auth.UserPrincipal, uuid.UUID) ([]HuddleConnection, error)
}

// HuddleRoomProvider is implemented by the media adapter. It intentionally
// exposes only room lifecycle operations to the conversation domain.
type HuddleRoomProvider interface {
	EnsureRoom(context.Context, HuddleRoom) error
	DeleteRoom(context.Context, string) error
	// ReadyRooms reports which named rooms exist; absent names are not ready.
	ReadyRooms(context.Context, []string) (map[string]bool, error)
	IssueParticipantToken(context.Context, string, string) (HuddleMediaToken, error)
}

type HuddleRoom struct {
	Name             string
	ParticipantLimit int
}

type HuddleMediaToken struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

var _ HuddleService = (*DefaultHuddleService)(nil)

type HuddleServiceOptions struct {
	Store        *db.Store
	Bus          *events.Bus
	Media        HuddleRoomProvider
	Presenter    inbox.Presenter
	NotesEnabled bool
}

type DefaultHuddleService struct {
	store        *db.Store
	bus          *events.Bus
	media        HuddleRoomProvider
	presenter    inbox.Presenter
	notesEnabled bool
}

type JoinHuddleRequest struct {
	RequestKey uuid.UUID
}

type HuddleConnection struct {
	ID          uuid.UUID  `json:"id"`
	HuddleID    uuid.UUID  `json:"huddleId"`
	UserID      uuid.UUID  `json:"userId"`
	Status      string     `json:"status"`
	Revision    int64      `json:"revision"`
	CreatedAt   time.Time  `json:"createdAt"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
	DisplayName string     `json:"displayName"`
}

func (s *DefaultHuddleService) ListConnections(ctx context.Context, principal auth.UserPrincipal, huddleID uuid.UUID) ([]HuddleConnection, error) {
	var result []HuddleConnection
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		huddle, err := q.GetHuddle(ctx, generated.GetHuddleParams{CompanyID: principal.CompanyID, HuddleID: huddleID})
		if err != nil {
			return ErrHuddleNotFound
		}
		if _, err := q.GetOpenHuddleConnection(ctx, generated.GetOpenHuddleConnectionParams{
			CompanyID: principal.CompanyID, HuddleID: huddleID, UserID: principal.UserID,
		}); errors.Is(err, pgx.ErrNoRows) {
			if err := lockDiscussion(ctx, q, principal, huddle.SourceConversationID); err != nil {
				return ErrHuddleForbidden
			}
		} else if err != nil {
			return err
		}
		rows, err := q.ListOpenHuddleConnections(ctx, generated.ListOpenHuddleConnectionsParams{CompanyID: principal.CompanyID, HuddleID: huddleID})
		if err != nil {
			return err
		}
		result = make([]HuddleConnection, 0, len(rows))
		for _, row := range rows {
			result = append(result, HuddleConnection{ID: row.ID, HuddleID: row.HuddleID, UserID: row.UserID, Status: row.Status, Revision: row.Revision, CreatedAt: timestampValue(row.CreatedAt), ExpiresAt: nullableTimestamp(row.ReservationExpiresAt), DisplayName: row.DisplayName})
		}
		return nil
	})
	return result, err
}

type JoinHuddleResult struct {
	Huddle     Huddle            `json:"huddle"`
	Connection HuddleConnection  `json:"connection"`
	Media      *HuddleMediaToken `json:"media,omitempty"`
	Replayed   bool              `json:"-"`
}

func NewHuddleService(options HuddleServiceOptions) (*DefaultHuddleService, error) {
	if options.Store == nil {
		return nil, errors.New("Huddle store is required")
	}
	if options.Bus == nil {
		return nil, errors.New("Huddle event bus is required")
	}
	return &DefaultHuddleService{store: options.Store, bus: options.Bus, media: options.Media, presenter: options.Presenter, notesEnabled: options.NotesEnabled}, nil
}

func (s *DefaultHuddleService) Start(
	ctx context.Context,
	principal auth.UserPrincipal,
	sourceConversationID uuid.UUID,
	request StartHuddleRequest,
) (StartHuddleResult, error) {
	if request.RequestKey == uuid.Nil || sourceConversationID == uuid.Nil {
		return StartHuddleResult{}, ErrHuddleInvalid
	}
	var result StartHuddleResult
	var pending []events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		role, err := lockDiscussionMembership(ctx, q, principal)
		if err != nil {
			return err
		}
		if !role.Can(auth.PermissionConversationsDiscuss) {
			return ErrHuddleForbidden
		}
		if err := lockDiscussion(ctx, q, principal, sourceConversationID); err != nil {
			return err
		}
		source, err := currentDiscussion(ctx, q, principal, sourceConversationID, false)
		if err != nil || !source.ContentAccess || (source.Kind != "human_direct" && source.Kind != "channel") {
			return ErrHuddleNotFound
		}

		existing, err := q.GetHuddleByRequestKey(ctx, generated.GetHuddleByRequestKeyParams{
			CompanyID:       principal.CompanyID,
			StartedByUserID: principal.UserID,
			RequestKey:      request.RequestKey,
		})
		if err == nil {
			if existing.SourceConversationID != sourceConversationID {
				return ErrHuddleRequestConflict
			}
			result.Huddle = huddleFromRow(existing)
			result.Replayed = true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		existing, err = q.GetActiveHuddleForSource(ctx, generated.GetActiveHuddleForSourceParams{
			CompanyID:            principal.CompanyID,
			SourceConversationID: sourceConversationID,
		})
		if err == nil {
			result.Huddle = huddleFromRow(existing)
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		chat, err := q.InsertDiscussionConversation(ctx, generated.InsertDiscussionConversationParams{
			CompanyID:   principal.CompanyID,
			Kind:        "huddle",
			ActorUserID: principal.UserID,
			Title:       pgtype.Text{},
			RequestKey:  uuid.NullUUID{},
		})
		if err != nil {
			return err
		}
		row, err := q.InsertHuddle(ctx, generated.InsertHuddleParams{
			CompanyID:            principal.CompanyID,
			SourceConversationID: sourceConversationID,
			ChatConversationID:   chat.ID,
			StartedByUserID:      principal.UserID,
			RequestKey:           request.RequestKey,
			DeadlineAt:           pgtype.Timestamptz{Time: time.Now().UTC().Add(60 * time.Minute), Valid: true},
		})
		if err != nil {
			return err
		}
		if s.notesEnabled {
			if err = q.SetHuddleNotesMode(ctx, generated.SetHuddleNotesModeParams{CompanyID: row.CompanyID, ID: row.ID}); err != nil {
				return err
			}
		}
		result.Huddle = huddleFromRow(row)
		result.Created = true
		cardEvent, err := insertHuddleCard(ctx, q, row)
		if err != nil {
			return err
		}
		pending = append(pending, cardEvent)
		return nil
	})
	if err != nil {
		if discussionConflict(err) {
			return StartHuddleResult{}, ErrHuddleUnavailable
		}
		return StartHuddleResult{}, err
	}
	publishDiscussionEvents(ctx, s.bus, pending)
	if s.media != nil {
		var err error
		result.Huddle, err = s.ensureRoom(ctx, principal.CompanyID, result.Huddle)
		if err != nil {
			return StartHuddleResult{}, ErrHuddleUnavailable
		}
	}
	if result.Huddle.State == HuddleStateStarting {
		if err := s.callSourceMembers(ctx, result.Huddle); err != nil {
			return StartHuddleResult{}, err
		}
	}
	return result, nil
}

// callSourceMembers creates the initial invitation round only after the media
// room is available. A retry after room creation failure can complete it.
func (s *DefaultHuddleService) callSourceMembers(ctx context.Context, huddle Huddle) error {
	var pending []events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		members, err := q.ListDiscussionMembers(ctx, generated.ListDiscussionMembersParams{
			CompanyID: huddle.CompanyID, ConversationID: huddle.SourceConversationID,
		})
		if err != nil {
			return err
		}
		expiresAt := time.Now().UTC().Add(5 * time.Minute)
		for _, member := range members {
			if !member.Active || member.UserID == huddle.StartedByUserID {
				continue
			}
			row, err := q.InsertInitialHuddleInvitation(ctx, generated.InsertInitialHuddleInvitationParams{
				CompanyID: huddle.CompanyID, HuddleID: huddle.ID, InviterUserID: huddle.StartedByUserID,
				InviteeUserID: member.UserID, RequestKey: uuid.NewMD5(huddle.ID, []byte(member.UserID.String())),
				ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
			})
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			pending = append(pending, huddleInvitationEvent(huddle.CompanyID, EventHuddleInvitationCreated, invitationFromRow(row)))
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	return nil
}

func HuddleRoomName(huddleID uuid.UUID) string {
	return "huddle:" + huddleID.String()
}

func HuddleRoomIdentity(huddleID, userID uuid.UUID) string {
	return "huddle:" + huddleID.String() + ":user:" + userID.String()
}

func parseHuddleRoomIdentity(identity string) (uuid.UUID, error) {
	parts := strings.Split(identity, ":")
	if len(parts) != 4 || parts[0] != "huddle" || parts[2] != "user" {
		return uuid.Nil, ErrHuddleInvalid
	}
	userID, err := uuid.Parse(parts[3])
	if err != nil {
		return uuid.Nil, ErrHuddleInvalid
	}
	return userID, nil
}

func (s *DefaultHuddleService) ConfirmConnectedByIdentity(ctx context.Context, companyID uuid.UUID, roomName, identity string) (JoinHuddleResult, error) {
	userID, err := parseHuddleRoomIdentity(identity)
	if err != nil || roomName == "" {
		return JoinHuddleResult{}, ErrHuddleInvalid
	}
	var connection generated.HuddleConnection
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		connection, err = q.GetHuddleConnectionByRoomIdentity(ctx, generated.GetHuddleConnectionByRoomIdentityParams{CompanyID: companyID, RoomName: roomName, UserID: userID})
		return err
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return JoinHuddleResult{}, ErrHuddleNotFound
		}
		return JoinHuddleResult{}, err
	}
	return s.ConfirmConnected(ctx, companyID, connection.ID)
}

func (s *DefaultHuddleService) ConfirmConnectedByIdentityAnyCompany(ctx context.Context, roomName, identity string) (JoinHuddleResult, error) {
	userID, err := parseHuddleRoomIdentity(identity)
	if err != nil || roomName == "" {
		return JoinHuddleResult{}, ErrHuddleInvalid
	}
	connection, err := s.store.Queries().GetHuddleConnectionByRoomIdentityAnyCompany(ctx, generated.GetHuddleConnectionByRoomIdentityAnyCompanyParams{RoomName: roomName, UserID: userID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return JoinHuddleResult{}, ErrHuddleNotFound
		}
		return JoinHuddleResult{}, err
	}
	return s.ConfirmConnected(ctx, connection.CompanyID, connection.ID)
}

func (s *DefaultHuddleService) EndFromMedia(ctx context.Context, companyID uuid.UUID, roomName string) error {
	if companyID == uuid.Nil || roomName == "" {
		return ErrHuddleInvalid
	}
	var pending []events.Event
	var cards []events.Event
	var ended *generated.Huddle
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		row, err := q.EndHuddleByRoom(ctx, generated.EndHuddleByRoomParams{CompanyID: companyID, RoomName: roomName})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		ended = &row
		cards = append(cards, huddleCardChangedEvent(row))
		return revokeHuddleInvitations(ctx, q, row.CompanyID, row.ID, &pending)
	})
	if err == nil {
		s.bus.Publish(ctx, TopicHuddle, pending...)
		publishDiscussionEvents(ctx, s.bus, cards)
		if ended != nil {
			PublishHuddleStatusChanged(ctx, s.store.Queries(), s.bus, ended.CompanyID, ended.ID)
		}
	}
	return err
}

func (s *DefaultHuddleService) EndFromMediaAnyCompany(ctx context.Context, roomName string) error {
	if roomName == "" {
		return ErrHuddleInvalid
	}
	var pending []events.Event
	var cards []events.Event
	var ended generated.Huddle
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		row, err := q.EndHuddleByRoomAnyCompany(ctx, roomName)
		if err != nil {
			return err
		}
		ended = row
		cards = append(cards, huddleCardChangedEvent(row))
		return revokeHuddleInvitations(ctx, q, row.CompanyID, row.ID, &pending)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err == nil {
		s.bus.Publish(ctx, TopicHuddle, pending...)
		publishDiscussionEvents(ctx, s.bus, cards)
		PublishHuddleStatusChanged(ctx, s.store.Queries(), s.bus, ended.CompanyID, ended.ID)
	}
	return err
}

func revokeHuddleInvitations(ctx context.Context, q *generated.Queries, companyID, huddleID uuid.UUID, pending *[]events.Event) error {
	rows, err := q.RevokePendingHuddleInvitations(ctx, generated.RevokePendingHuddleInvitationsParams{
		CompanyID: companyID, HuddleID: huddleID,
	})
	if err != nil {
		return err
	}
	for _, row := range rows {
		*pending = append(*pending, huddleInvitationEvent(companyID, EventHuddleInvitationChanged, invitationFromRow(row)))
	}
	return nil
}

func (s *DefaultHuddleService) EndParticipantFromMedia(ctx context.Context, companyID uuid.UUID, roomName, identity string) error {
	userID, err := parseHuddleRoomIdentity(identity)
	if err != nil {
		return err
	}
	var connection generated.HuddleConnection
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		connection, err = q.GetHuddleConnectionByRoomIdentity(ctx, generated.GetHuddleConnectionByRoomIdentityParams{CompanyID: companyID, RoomName: roomName, UserID: userID})
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var rosterEvents []events.Event
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		_, err := q.MarkHuddleConnectionEnded(ctx, generated.MarkHuddleConnectionEndedParams{CompanyID: companyID, ConnectionID: connection.ID, UserID: userID})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		roster, err := huddleRosterEvent(ctx, q, companyID, connection.HuddleID, userID)
		if err != nil {
			return err
		}
		rosterEvents = append(rosterEvents, roster)
		return nil
	})
	if err != nil {
		return err
	}
	s.bus.Publish(ctx, TopicHuddle, rosterEvents...)
	return nil
}

func (s *DefaultHuddleService) EndParticipantFromMediaAnyCompany(ctx context.Context, roomName, identity string) error {
	userID, err := parseHuddleRoomIdentity(identity)
	if err != nil {
		return err
	}
	connection, err := s.store.Queries().GetHuddleConnectionByRoomIdentityAnyCompany(ctx, generated.GetHuddleConnectionByRoomIdentityAnyCompanyParams{RoomName: roomName, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.EndParticipantFromMedia(ctx, connection.CompanyID, roomName, identity)
}

// Join records an admission reservation after rechecking source access. It
// deliberately stops before issuing a media token; a LiveKit adapter and an
// authenticated connection callback complete the transition to connected.
func (s *DefaultHuddleService) Join(
	ctx context.Context,
	principal auth.UserPrincipal,
	huddleID uuid.UUID,
	request JoinHuddleRequest,
) (JoinHuddleResult, error) {
	if huddleID == uuid.Nil || request.RequestKey == uuid.Nil {
		return JoinHuddleResult{}, ErrHuddleInvalid
	}
	var result JoinHuddleResult
	var rosterEvents []events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		role, err := lockDiscussionMembership(ctx, q, principal)
		if err != nil {
			return err
		}
		if !role.Can(auth.PermissionConversationsDiscuss) {
			return ErrHuddleForbidden
		}
		huddle, err := q.LockHuddle(ctx, generated.LockHuddleParams{CompanyID: principal.CompanyID, HuddleID: huddleID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrHuddleNotFound
			}
			return err
		}
		if (huddle.State != string(HuddleStateStarting) && huddle.State != string(HuddleStateOpen)) ||
			!timestampValue(huddle.DeadlineAt).After(time.Now()) {
			return ErrHuddleUnavailable
		}
		invitation, invitationErr := q.GetAcceptedHuddleInvitation(ctx, generated.GetAcceptedHuddleInvitationParams{
			CompanyID: principal.CompanyID, HuddleID: huddleID, InviteeUserID: principal.UserID,
		})
		if invitationErr != nil && !errors.Is(invitationErr, pgx.ErrNoRows) {
			return invitationErr
		}
		independentInvite := invitationErr == nil && invitation.RequestKey != uuid.NewMD5(huddle.ID, []byte(principal.UserID.String()))
		if !independentInvite {
			if err := lockDiscussion(ctx, q, principal, huddle.SourceConversationID); err != nil {
				return ErrHuddleForbidden
			}
			source, err := currentDiscussion(ctx, q, principal, huddle.SourceConversationID, false)
			if err != nil || !source.ContentAccess || (source.Kind != "human_direct" && source.Kind != "channel") {
				return ErrHuddleForbidden
			}
		}
		connection, err := q.GetHuddleConnectionByRequestKey(ctx, generated.GetHuddleConnectionByRequestKeyParams{
			CompanyID: principal.CompanyID, HuddleID: huddleID, UserID: principal.UserID, RequestKey: request.RequestKey,
		})
		if err == nil {
			result.Huddle = huddleFromRow(huddle)
			result.Connection = huddleConnectionFromRow(connection)
			result.Replayed = true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		connection, err = q.GetOpenHuddleConnection(ctx, generated.GetOpenHuddleConnectionParams{
			CompanyID: principal.CompanyID, HuddleID: huddleID, UserID: principal.UserID,
		})
		if err == nil {
			// A confirmed connection is live media in another window or device.
			// Issuing a token for the same room identity would make LiveKit drop
			// that session, so the caller is told they are already participating.
			if connection.Status == "connected" {
				return ErrHuddleAlreadyConnected
			}
			result.Huddle = huddleFromRow(huddle)
			result.Connection = huddleConnectionFromRow(connection)
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		connection, err = q.InsertHuddleConnection(ctx, generated.InsertHuddleConnectionParams{
			CompanyID: principal.CompanyID, HuddleID: huddleID, UserID: principal.UserID, RequestKey: request.RequestKey,
		})
		if err != nil {
			return err
		}
		roster, err := huddleRosterEvent(ctx, q, principal.CompanyID, huddleID, principal.UserID)
		if err != nil {
			return err
		}
		rosterEvents = append(rosterEvents, roster)
		result.Huddle = huddleFromRow(huddle)
		result.Connection = huddleConnectionFromRow(connection)
		return nil
	})
	if err != nil {
		return JoinHuddleResult{}, err
	}
	s.bus.Publish(ctx, TopicHuddle, rosterEvents...)
	if s.media != nil {
		result.Huddle, err = s.ensureRoom(ctx, principal.CompanyID, result.Huddle)
		if err != nil {
			return JoinHuddleResult{}, ErrHuddleUnavailable
		}
	}
	if s.media != nil && result.Huddle.RoomName != nil {
		media, mediaErr := s.media.IssueParticipantToken(ctx, *result.Huddle.RoomName, HuddleRoomIdentity(result.Huddle.ID, result.Connection.UserID))
		if mediaErr != nil {
			return JoinHuddleResult{}, ErrHuddleUnavailable
		}
		result.Media = &media
	}
	return result, nil
}

func (s *DefaultHuddleService) ensureRoom(ctx context.Context, companyID uuid.UUID, huddle Huddle) (Huddle, error) {
	if s.media == nil || huddle.RoomName != nil {
		return huddle, nil
	}
	roomName := HuddleRoomName(huddle.ID)
	if err := s.media.EnsureRoom(ctx, HuddleRoom{Name: roomName, ParticipantLimit: 10}); err != nil {
		return Huddle{}, err
	}
	var row generated.Huddle
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		var err error
		row, err = q.SetHuddleRoomName(ctx, generated.SetHuddleRoomNameParams{CompanyID: companyID, HuddleID: huddle.ID, RoomName: roomName})
		return err
	})
	if err != nil {
		return Huddle{}, err
	}
	return huddleFromRow(row), nil
}

// ConfirmConnected is called only by a verified media adapter/webhook. It
// turns a reservation into a confirmed participant and grants Huddle chat
// access in the same transaction. It does not trust a client Join response.
func (s *DefaultHuddleService) ConfirmConnected(ctx context.Context, companyID, connectionID uuid.UUID) (JoinHuddleResult, error) {
	if companyID == uuid.Nil || connectionID == uuid.Nil {
		return JoinHuddleResult{}, ErrHuddleInvalid
	}
	var result JoinHuddleResult
	var cards []events.Event
	var rosterEvents []events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		connection, err := q.GetHuddleConnection(ctx, generated.GetHuddleConnectionParams{CompanyID: companyID, ID: connectionID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrHuddleNotFound
			}
			return err
		}
		huddle, err := q.LockHuddle(ctx, generated.LockHuddleParams{CompanyID: companyID, HuddleID: connection.HuddleID})
		if err != nil {
			return err
		}
		connection, err = q.GetHuddleConnectionForUpdate(ctx, generated.GetHuddleConnectionForUpdateParams{CompanyID: companyID, ConnectionID: connectionID})
		if err != nil {
			return err
		}
		if connection.Status != "connected" && (connection.Status != "reserved" || !connection.ReservationExpiresAt.Time.After(time.Now())) {
			return ErrHuddleUnavailable
		}
		if (huddle.State != string(HuddleStateStarting) && huddle.State != string(HuddleStateOpen)) ||
			!timestampValue(huddle.DeadlineAt).After(time.Now()) {
			return ErrHuddleUnavailable
		}
		if connection.Status == "connected" {
			result.Huddle = huddleFromRow(huddle)
			result.Connection = huddleConnectionFromRow(connection)
			return nil
		}
		connection, err = q.MarkHuddleConnectionConnected(ctx, generated.MarkHuddleConnectionConnectedParams{CompanyID: companyID, ConnectionID: connectionID})
		if err != nil {
			return err
		}
		if _, err = q.GrantDiscussionMember(ctx, generated.GrantDiscussionMemberParams{
			CompanyID: companyID, ConversationID: huddle.ChatConversationID, UserID: connection.UserID,
			SourceType: "huddle", JoinedSeq: 0, ActorUserID: uuid.NullUUID{UUID: connection.UserID, Valid: true},
		}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if huddle.State == string(HuddleStateStarting) {
			huddle, err = q.OpenHuddle(ctx, generated.OpenHuddleParams{CompanyID: companyID, HuddleID: huddle.ID})
			if err != nil {
				return err
			}
		}
		roster, err := huddleRosterEvent(ctx, q, companyID, huddle.ID, connection.UserID)
		if err != nil {
			return err
		}
		rosterEvents = append(rosterEvents, roster)
		result.Huddle = huddleFromRow(huddle)
		result.Connection = huddleConnectionFromRow(connection)
		cards = append(cards, huddleCardChangedEvent(huddle))
		return nil
	})
	if err != nil {
		return JoinHuddleResult{}, err
	}
	s.bus.Publish(ctx, TopicHuddle, rosterEvents...)
	publishDiscussionEvents(ctx, s.bus, cards)
	return result, nil
}

// Leave ends the caller's media connection. The Huddle chat grant is retained
// so a former participant can read the call history after leaving.
func (s *DefaultHuddleService) Leave(ctx context.Context, principal auth.UserPrincipal, huddleID, connectionID uuid.UUID) (JoinHuddleResult, error) {
	if principal.CompanyID == uuid.Nil || principal.UserID == uuid.Nil || huddleID == uuid.Nil || connectionID == uuid.Nil {
		return JoinHuddleResult{}, ErrHuddleInvalid
	}
	var result JoinHuddleResult
	var rosterEvents []events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		connection, err := q.GetHuddleConnectionForUpdate(ctx, generated.GetHuddleConnectionForUpdateParams{CompanyID: principal.CompanyID, ConnectionID: connectionID})
		if err != nil || connection.HuddleID != huddleID || connection.UserID != principal.UserID {
			if errors.Is(err, pgx.ErrNoRows) || err == nil {
				return ErrHuddleNotFound
			}
			return err
		}
		huddle, err := q.GetHuddle(ctx, generated.GetHuddleParams{CompanyID: principal.CompanyID, HuddleID: huddleID})
		if err != nil {
			return err
		}
		if connection.Status == "ended" {
			result.Huddle = huddleFromRow(huddle)
			result.Connection = huddleConnectionFromRow(connection)
			return nil
		}
		connection, err = q.MarkHuddleConnectionEnded(ctx, generated.MarkHuddleConnectionEndedParams{CompanyID: principal.CompanyID, ConnectionID: connectionID, UserID: principal.UserID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrHuddleUnavailable
			}
			return err
		}
		roster, err := huddleRosterEvent(ctx, q, principal.CompanyID, huddleID, principal.UserID)
		if err != nil {
			return err
		}
		rosterEvents = append(rosterEvents, roster)
		result.Huddle = huddleFromRow(huddle)
		result.Connection = huddleConnectionFromRow(connection)
		return nil
	})
	if err != nil {
		return JoinHuddleResult{}, err
	}
	s.bus.Publish(ctx, TopicHuddle, rosterEvents...)
	return result, nil
}

// SweepExpiredReservations is intended for the lifecycle worker. It only
// revokes unconfirmed reservations and is safe to retry.
func (s *DefaultHuddleService) SweepExpiredReservations(ctx context.Context, asOf time.Time) (int, error) {
	if asOf.IsZero() {
		return 0, ErrHuddleInvalid
	}
	var count int
	var rosterEvents []events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		rows, err := q.RevokeExpiredHuddleConnections(ctx, pgtype.Timestamptz{Time: asOf.UTC(), Valid: true})
		if err != nil {
			return err
		}
		count = len(rows)
		type huddleKey struct{ companyID, huddleID uuid.UUID }
		var order []huddleKey
		revoked := make(map[huddleKey][]uuid.UUID)
		for _, row := range rows {
			key := huddleKey{row.CompanyID, row.HuddleID}
			if _, ok := revoked[key]; !ok {
				order = append(order, key)
			}
			revoked[key] = append(revoked[key], row.UserID)
		}
		for _, key := range order {
			roster, err := huddleRosterEvent(ctx, q, key.companyID, key.huddleID, revoked[key]...)
			if err != nil {
				return err
			}
			rosterEvents = append(rosterEvents, roster)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	s.bus.Publish(ctx, TopicHuddle, rosterEvents...)
	return count, nil
}

// huddleRoomlessGrace bounds how long a Huddle may wait for its media room.
// Start and Join retry room creation, so only an abandoned Huddle reaches it.
const huddleRoomlessGrace = 2 * time.Minute

// huddleUnjoinedGrace bounds how long a Huddle with a room may wait for its
// first person to connect before it closes and stops showing as in progress.
const huddleUnjoinedGrace = 2 * time.Minute

// ReconcileMediaRooms persists closure intent before deleting expired rooms,
// and reconciles rooms that disappeared from the media provider. An ending
// room remains fenced against admission while provider cleanup is retried.
// A failure on one Huddle is reported but does not hold back the others.
func (s *DefaultHuddleService) ReconcileMediaRooms(ctx context.Context) (int, error) {
	if s.media == nil {
		return 0, nil
	}
	closed, err := s.endStaleRoomlessHuddles(ctx)
	if err != nil {
		return 0, err
	}
	rows, err := s.store.Queries().ListLiveHuddles(ctx)
	if err != nil || len(rows) == 0 {
		return closed, err
	}
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, row.RoomName.String)
	}
	ready, err := s.media.ReadyRooms(ctx, names)
	if err != nil {
		return closed, err
	}
	var errs []error
	for _, row := range rows {
		ended, err := s.reconcileMediaRoom(ctx, row, ready[row.RoomName.String])
		if err != nil {
			errs = append(errs, fmt.Errorf("huddle %s: %w", row.ID, err))
			continue
		}
		if ended {
			closed++
		}
	}
	return closed, errors.Join(errs...)
}

func (s *DefaultHuddleService) reconcileMediaRoom(ctx context.Context, row generated.Huddle, ready bool) (bool, error) {
	closing := false
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		var err error
		row, err = q.LockHuddle(ctx, generated.LockHuddleParams{CompanyID: row.CompanyID, HuddleID: row.ID})
		if err != nil {
			return err
		}
		ending, err := q.BeginExpiredHuddleClosure(ctx, generated.BeginExpiredHuddleClosureParams{
			CompanyID: row.CompanyID, HuddleID: row.ID,
			UnjoinedGrace: pgtype.Interval{Microseconds: huddleUnjoinedGrace.Microseconds(), Valid: true},
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err == nil {
			row = ending
			closing = true
		}
		return err
	})
	if err != nil || row.State == string(HuddleStateEnded) {
		return false, err
	}
	if closing {
		PublishHuddleStatusChanged(ctx, s.store.Queries(), s.bus, row.CompanyID, row.ID)
	}
	if ready && row.State != string(HuddleStateEnding) {
		return false, nil
	}
	if ready {
		if err := s.media.DeleteRoom(ctx, row.RoomName.String); err != nil {
			return false, err
		}
	}
	if err := s.EndFromMedia(ctx, row.CompanyID, row.RoomName.String); err != nil {
		return false, err
	}
	return true, nil
}

// endStaleRoomlessHuddles ends Huddles whose room creation failed and was
// never retried. They own no media room, so there is nothing to delete.
func (s *DefaultHuddleService) endStaleRoomlessHuddles(ctx context.Context) (int, error) {
	var pending []events.Event
	var cards []events.Event
	var count int
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		rows, err := q.EndStaleRoomlessHuddles(ctx, pgtype.Interval{Microseconds: huddleRoomlessGrace.Microseconds(), Valid: true})
		if err != nil {
			return err
		}
		count = len(rows)
		for _, row := range rows {
			cards = append(cards, huddleCardChangedEvent(row))
			if err := revokeHuddleInvitations(ctx, q, row.CompanyID, row.ID, &pending); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	s.bus.Publish(ctx, TopicHuddle, pending...)
	publishDiscussionEvents(ctx, s.bus, cards)
	return count, nil
}

// SweepExpiredInvitations transitions pending invitations past their deadline
// so clients never see an invitation that can no longer be accepted.
func (s *DefaultHuddleService) SweepExpiredInvitations(ctx context.Context, asOf time.Time) (int, error) {
	if asOf.IsZero() {
		return 0, ErrHuddleInvalid
	}
	var count int
	var changed []events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		rows, err := q.ExpireHuddleInvitations(ctx, pgtype.Timestamptz{Time: asOf.UTC(), Valid: true})
		count = len(rows)
		for _, row := range rows {
			changed = append(changed, huddleInvitationEvent(row.CompanyID, EventHuddleInvitationChanged, invitationFromRow(row)))
		}
		return err
	})
	if err == nil {
		s.bus.Publish(ctx, TopicHuddle, changed...)
	}
	return count, err
}

func huddleConnectionFromRow(row generated.HuddleConnection) HuddleConnection {
	return HuddleConnection{ID: row.ID, HuddleID: row.HuddleID, UserID: row.UserID, Status: row.Status, Revision: row.Revision, CreatedAt: timestampValue(row.CreatedAt), ExpiresAt: nullableTimestamp(row.ReservationExpiresAt)}
}

func huddleFromRow(row generated.Huddle) Huddle {
	return Huddle{
		ID:                   row.ID,
		CompanyID:            row.CompanyID,
		SourceConversationID: row.SourceConversationID,
		ChatConversationID:   row.ChatConversationID,
		StartedByUserID:      row.StartedByUserID,
		State:                HuddleState(row.State),
		Revision:             row.Revision,
		DeadlineAt:           timestampValue(row.DeadlineAt),
		RoomName:             nullableText(row.RoomName),
		CreatedAt:            timestampValue(row.CreatedAt),
		UpdatedAt:            timestampValue(row.UpdatedAt),
		EndedAt:              nullableTimestamp(row.EndedAt),
	}
}

func nullableText(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func nullableTimestamp(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time.UTC()
	return &result
}

func timestampValue(value pgtype.Timestamptz) time.Time {
	return (*util.TimestampToTime(value)).UTC()
}
