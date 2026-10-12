package conversation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

// StudioEditorRelay is a trusted server-to-daemon seam. Implementations must pin
// the authenticated connection across probe, token and binding operations;
// renderer JSON never implements it.
type StudioEditorRelay interface {
	Connect(context.Context, uuid.UUID, uuid.UUID) (StudioEditorConnection, error)
}

type StudioEditorConnection interface {
	Principal() auth.UserPrincipal
	AgentID() uuid.UUID
	Probe(context.Context, uuid.UUID) (StudioConnectionResponse, error)
	Token(context.Context, uuid.UUID) (protocol.StudioAccessToken, error)
	Ensure(context.Context, StudioEnsureDispatch) (StudioReceiptResponse, error)
	VerifyProject(context.Context, StudioEnsureDispatch) error
}

type StudioEditorService interface {
	ResolveContext(context.Context, auth.UserPrincipal, StudioEditorAccess) (StudioContextResponse, error)
	EnsureBinding(context.Context, auth.UserPrincipal, StudioEnsureInput) (StudioBindingResponse, error)
}

var _ StudioEditorService = (*DefaultStudioEditorService)(nil)

type DefaultStudioEditorService struct {
	store     *db.Store
	relay     StudioEditorRelay
	namespace uuid.UUID
}

func NewStudioEditorService(store *db.Store, relay StudioEditorRelay, namespace uuid.UUID) *DefaultStudioEditorService {
	return &DefaultStudioEditorService{store: store, relay: relay, namespace: namespace}
}

type StudioEditorAccess struct {
	ConversationID uuid.UUID
	RuntimeID      uuid.UUID
	agentID        uuid.UUID
}

type StudioEnsureInput struct {
	Access                  StudioEditorAccess
	RequestID               uuid.UUID
	Generation              uuid.UUID
	ExpectedBindingID       *uuid.UUID
	ExpectedBindingRevision int64
	Title                   string
	Description             string
}

type StudioBindingResponse struct {
	BindingID         uuid.UUID  `json:"bindingId"`
	BindingRevision   int64      `json:"bindingRevision"`
	Origin            string     `json:"origin"`
	State             string     `json:"state"`
	ProjectID         *uuid.UUID `json:"projectId"`
	CreationAccountID *uuid.UUID `json:"creationAccountId"`
	CreationProtocol  string     `json:"creationProtocol"`
	Title             string     `json:"title"`
	Description       string     `json:"description"`
}

type StudioContextResponse struct {
	AgentID              uuid.UUID                   `json:"agentId"`
	RuntimeID            uuid.UUID                   `json:"runtimeId"`
	Binding              *StudioBindingResponse      `json:"binding"`
	ConnectionGeneration *uuid.UUID                  `json:"connectionGeneration"`
	ConnectionState      string                      `json:"connectionState"`
	CanCreate            bool                        `json:"canCreate"`
	StudioAccessToken    *protocol.StudioAccessToken `json:"studioAccessToken,omitempty"`
}

type StudioConnectionResponse struct {
	ProbeID              uuid.UUID `json:"probeId"`
	RuntimeID            uuid.UUID `json:"runtimeId"`
	DaemonID             string    `json:"daemonId"`
	ConnectionGeneration uuid.UUID `json:"connectionGeneration"`
	Origin               string    `json:"origin"`
	StudioAccountID      uuid.UUID `json:"studioAccountId"`
	VerifiedAt           time.Time `json:"verifiedAt"`
	CreationProtocol     string    `json:"creationProtocol"`
	EditorSchemaVersion  int       `json:"editorSchemaVersion"`
}

type StudioReceiptRequest struct {
	Description   string    `json:"description"`
	Namespace     uuid.UUID `json:"namespace"`
	ReservationID uuid.UUID `json:"reservationId"`
	Title         string    `json:"title"`
}

type StudioReceiptResponse struct {
	Namespace     uuid.UUID `json:"namespace"`
	ReservationID uuid.UUID `json:"reservationId"`
	AccountID     uuid.UUID `json:"accountId"`
	ProjectID     uuid.UUID `json:"projectId"`
	RequestHash   string    `json:"requestHash"`
	State         string    `json:"state"`
}

type StudioEnsureDispatch struct {
	RequestID            uuid.UUID             `json:"requestId"`
	DispatchID           uuid.UUID             `json:"dispatchId"`
	ActorUserID          uuid.UUID             `json:"actorUserId"`
	ActorSessionID       uuid.UUID             `json:"actorSessionId"`
	CompanyID            uuid.UUID             `json:"companyId"`
	ConversationID       uuid.UUID             `json:"conversationId"`
	RuntimeID            uuid.UUID             `json:"runtimeId"`
	AuthorityType        string                `json:"authorityType"`
	ConnectionGeneration uuid.UUID             `json:"connectionGeneration"`
	Origin               string                `json:"origin"`
	StudioAccountID      uuid.UUID             `json:"studioAccountId"`
	AuthorizedAt         time.Time             `json:"authorizedAt"`
	DispatchExpiresAt    time.Time             `json:"dispatchExpiresAt"`
	Binding              StudioBindingResponse `json:"binding"`
	Payload              StudioReceiptRequest  `json:"payload"`
}

func canonicalStudioOrigin(raw string) (string, error) {
	origin, err := util.CanonicalStudioOrigin(raw)
	if err != nil {
		return "", ErrStudioEditorInvalid
	}
	return origin, nil
}

func studioRequestHash(request StudioReceiptRequest) (string, error) {
	// Struct field order is lexicographic. Disable HTML escaping to match the
	// Studio UTF-8 canonical JSON, including non-ASCII title/description text.
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(request); err != nil {
		return "", err
	}
	raw := bytes.TrimSuffix(out.Bytes(), []byte("\n"))
	// encoding/json always escapes these separators; canonical UTF-8 does not.
	var canonical bytes.Buffer
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\\' && i+1 < len(raw) {
			if bytes.HasPrefix(raw[i:], []byte(`\u2028`)) {
				canonical.WriteRune('\u2028')
				i += 5
				continue
			}
			if bytes.HasPrefix(raw[i:], []byte(`\u2029`)) {
				canonical.WriteRune('\u2029')
				i += 5
				continue
			}
			canonical.WriteByte(raw[i])
			i++
			canonical.WriteByte(raw[i])
			continue
		}
		canonical.WriteByte(raw[i])
	}
	raw = canonical.Bytes()
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func studioBinding(row generated.ConversationStudioProject) StudioBindingResponse {
	out := StudioBindingResponse{
		BindingID:        row.ID,
		BindingRevision:  row.BindingRevision,
		Origin:           row.ApiOrigin,
		State:            "reserved",
		CreationProtocol: "receiptV1",
		Title:            row.Title,
		Description:      row.Description,
	}
	if row.CreationProtocol == "legacy_marker" {
		out.CreationProtocol = "legacyMarker"
		out.State = "legacyRecoveryRequired"
	}
	if row.ProjectID.Valid {
		id := row.ProjectID.UUID
		out.ProjectID = &id
		out.State = "bound"
	}
	if row.CreationAccountID.Valid {
		id := row.CreationAccountID.UUID
		out.CreationAccountID = &id
	}
	return out
}

type studioAdmission struct {
	principal auth.UserPrincipal
	access    StudioEditorAccess
}

func (s *DefaultStudioEditorService) fence(
	ctx context.Context,
	admission studioAdmission,
	apply func(*generated.Queries, generated.Runtime, uuid.UUID) error,
) error {
	principal, access := admission.principal, admission.access
	return s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if principal.UserID == uuid.Nil || principal.SessionID == uuid.Nil ||
			(!principal.AccessExpiresAt.IsZero() && !principal.AccessExpiresAt.After(time.Now())) {
			return ErrStudioEditorNotFound
		}
		if _, err := q.GetActiveSessionUser(
			ctx,
			generated.GetActiveSessionUserParams{SessionID: principal.SessionID, UserID: principal.UserID},
		); err != nil {
			return studioAccessError(err)
		}
		if err := lockActiveMembership(ctx, q, principal.CompanyID, principal.UserID); err != nil {
			if errors.Is(err, ErrNotFound) {
				return ErrStudioEditorNotFound
			}
			return err
		}
		runtime, err := q.LockStudioEditorRuntime(
			ctx,
			generated.LockStudioEditorRuntimeParams{
				RuntimeID: access.RuntimeID,
				CompanyID: principal.CompanyID,
				UserID:    uuid.NullUUID{UUID: principal.UserID, Valid: true},
			},
		)
		if err != nil {
			return studioAccessError(err)
		}
		conv, err := q.LockConversationForActiveUserMutation(
			ctx,
			generated.LockConversationForActiveUserMutationParams{
				ConversationID: access.ConversationID,
				CompanyID:      principal.CompanyID,
				ActorUserID:    principal.UserID,
			},
		)
		if err != nil {
			return studioAccessError(err)
		}
		if conv.Kind != "direct" && conv.Kind != "task" {
			return ErrStudioEditorNotFound
		}
		if _, err := q.GetActiveSessionUser(
			ctx,
			generated.GetActiveSessionUserParams{SessionID: principal.SessionID, UserID: principal.UserID},
		); err != nil {
			return studioAccessError(err)
		}
		if !principal.AccessExpiresAt.IsZero() && !principal.AccessExpiresAt.After(time.Now()) {
			return ErrStudioEditorNotFound
		}
		agentID := conv.AgentID
		if conv.Kind == "task" {
			task, err := q.GetTaskByConversation(
				ctx,
				generated.GetTaskByConversationParams{
					CompanyID:      principal.CompanyID,
					ConversationID: conv.ID,
				},
			)
			if err != nil {
				return studioAccessError(err)
			}
			if task.AssigneeUserID != principal.UserID {
				return ErrStudioEditorNotFound
			}
			agentID = task.ExecutedByAgentID
		}
		if !agentID.Valid {
			return ErrStudioEditorNotFound
		}
		binding, err := q.GetAgentRuntimeBindingForRun(
			ctx,
			generated.GetAgentRuntimeBindingForRunParams{
				CompanyID: principal.CompanyID,
				UserID:    principal.UserID,
				AgentID:   agentID.UUID,
			},
		)
		if err != nil {
			return studioAccessError(err)
		}
		if binding.RuntimeID != runtime.ID || (access.agentID != uuid.Nil && access.agentID != agentID.UUID) {
			return ErrStudioEditorGenerationConflict
		}
		return apply(q, runtime, agentID.UUID)
	})
}

func studioAccessError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrStudioEditorNotFound
	}
	return fmt.Errorf("check Studio authority: %w", err)
}

func (s *DefaultStudioEditorService) connect(
	ctx context.Context,
	principal auth.UserPrincipal,
	access StudioEditorAccess,
) (StudioEditorConnection, StudioConnectionResponse, error) {
	var runtime generated.Runtime
	err := s.fence(
		ctx,
		studioAdmission{principal, access},
		func(_ *generated.Queries, r generated.Runtime, agentID uuid.UUID) error {
			runtime = r
			access.agentID = agentID
			return nil
		},
	)
	if err != nil {
		return nil, StudioConnectionResponse{}, err
	}
	if s.relay == nil {
		return nil, StudioConnectionResponse{}, ErrStudioEditorUnsupported
	}
	conn, err := s.relay.Connect(ctx, access.RuntimeID, access.agentID)
	if err != nil {
		return nil, StudioConnectionResponse{}, err
	}
	daemon := conn.Principal()
	if daemon.UserID != principal.UserID || conn.AgentID() != access.agentID {
		return nil, StudioConnectionResponse{}, ErrStudioEditorNotFound
	}
	if err = s.checkDaemon(ctx, daemon); err != nil {
		return nil, StudioConnectionResponse{}, err
	}
	probe, err := conn.Probe(ctx, access.RuntimeID)
	if err != nil {
		return nil, probe, err
	}
	origin, e := canonicalStudioOrigin(probe.Origin)
	if e != nil || origin != probe.Origin || probe.ProbeID == uuid.Nil || probe.RuntimeID != access.RuntimeID ||
		probe.DaemonID != runtime.DaemonID ||
		probe.ConnectionGeneration == uuid.Nil ||
		probe.StudioAccountID == uuid.Nil ||
		probe.EditorSchemaVersion != 2 ||
		(probe.CreationProtocol != "receiptV1" && probe.CreationProtocol != "legacyMarker") ||
		probe.VerifiedAt.IsZero() ||
		probe.VerifiedAt.After(time.Now().Add(time.Second*5)) {
		return nil, probe, ErrStudioEditorUpstreamInvalid
	}
	return conn, probe, nil
}

func (s *DefaultStudioEditorService) checkDaemon(ctx context.Context, p auth.UserPrincipal) error {
	if p.SessionID == uuid.Nil || (!p.AccessExpiresAt.IsZero() && !p.AccessExpiresAt.After(time.Now())) {
		return ErrStudioEditorRuntimeUnavailable
	}
	_, err := s.store.Queries().
		GetActiveSessionUser(ctx, generated.GetActiveSessionUserParams{SessionID: p.SessionID, UserID: p.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrStudioEditorRuntimeUnavailable
	}
	return err
}

func (s *DefaultStudioEditorService) ResolveContext(
	ctx context.Context,
	p auth.UserPrincipal,
	a StudioEditorAccess,
) (StudioContextResponse, error) {
	conn, probe, err := s.connect(ctx, p, a)
	if err != nil {
		return StudioContextResponse{}, err
	}
	var result StudioContextResponse
	err = s.fence(
		ctx,
		studioAdmission{p, a},
		func(q *generated.Queries, r generated.Runtime, agentID uuid.UUID) error {
			if agentID != conn.AgentID() {
				return ErrStudioEditorGenerationConflict
			}
			if r.DaemonID != probe.DaemonID {
				return ErrStudioEditorGenerationConflict
			}
			if e := s.checkDaemon(ctx, conn.Principal()); e != nil {
				return e
			}
			row, e := q.GetConversationStudioProject(
				ctx,
				generated.GetConversationStudioProjectParams{
					CompanyID:      p.CompanyID,
					ConversationID: a.ConversationID,
				},
			)
			if e != nil && !errors.Is(e, pgx.ErrNoRows) {
				return e
			}
			result = StudioContextResponse{
				AgentID:              agentID,
				RuntimeID:            a.RuntimeID,
				ConnectionGeneration: &probe.ConnectionGeneration,
				ConnectionState:      "ready",
				CanCreate: s.namespace != uuid.Nil &&
					probe.CreationProtocol == "receiptV1",
			}
			if e == nil {
				if row.ApiOrigin != probe.Origin {
					return ErrStudioEditorOriginConflict
				}
				b := studioBinding(row)
				result.Binding = &b
				result.CanCreate = false
			}
			return nil
		},
	)
	if err != nil {
		return StudioContextResponse{}, err
	}
	// The browser only needs the Studio credential while it is connected to a
	// bound project.  Mint it once at context load; graph reads, writes and
	// collaboration then go directly to Studio instead of traversing the
	// daemon relay for every operation.
	if result.Binding != nil && result.Binding.ProjectID != nil {
		token, tokenErr := conn.Token(ctx, a.RuntimeID)
		if tokenErr != nil {
			return StudioContextResponse{}, tokenErr
		}
		if token.AccessToken != "" {
			if err := s.fence(
				ctx,
				studioAdmission{p, a},
				func(_ *generated.Queries, r generated.Runtime, agentID uuid.UUID) error {
					if agentID != conn.AgentID() || r.DaemonID != probe.DaemonID {
						return ErrStudioEditorGenerationConflict
					}
					return s.checkDaemon(ctx, conn.Principal())
				},
			); err != nil {
				return StudioContextResponse{}, err
			}
			result.StudioAccessToken = &token
		}
	}
	return result, err
}

func (s *DefaultStudioEditorService) EnsureBinding(
	ctx context.Context,
	p auth.UserPrincipal,
	input StudioEnsureInput,
) (StudioBindingResponse, error) {
	input.Title = strings.TrimSpace(input.Title)
	input.Description = strings.TrimSpace(input.Description)
	if input.RequestID == uuid.Nil || input.Generation == uuid.Nil || input.Title == "" ||
		!utf8.ValidString(input.Title+input.Description) ||
		strings.ContainsRune(input.Title+input.Description, 0) ||
		utf8.RuneCountInString(input.Title) > 200 ||
		utf8.RuneCountInString(input.Description) > 2000 ||
		input.ExpectedBindingRevision < 0 ||
		input.ExpectedBindingRevision > 9007199254740991 ||
		(input.ExpectedBindingID == nil) != (input.ExpectedBindingRevision == 0) ||
		input.ExpectedBindingID != nil && *input.ExpectedBindingID == uuid.Nil {
		return StudioBindingResponse{}, ErrStudioEditorInvalid
	}
	conn, probe, err := s.connect(ctx, p, input.Access)
	if err != nil {
		return StudioBindingResponse{}, err
	}
	if probe.ConnectionGeneration != input.Generation {
		return StudioBindingResponse{}, ErrStudioEditorGenerationConflict
	}
	var row generated.ConversationStudioProject
	var dispatch StudioEnsureDispatch
	err = s.fence(
		ctx,
		studioAdmission{p, input.Access},
		func(q *generated.Queries, r generated.Runtime, agentID uuid.UUID) error {
			if agentID != conn.AgentID() {
				return ErrStudioEditorGenerationConflict
			}
			if r.DaemonID != probe.DaemonID {
				return ErrStudioEditorGenerationConflict
			}
			if e := s.checkDaemon(ctx, conn.Principal()); e != nil {
				return e
			}
			var e error
			row, e = q.GetConversationStudioProject(
				ctx,
				generated.GetConversationStudioProjectParams{
					CompanyID:      p.CompanyID,
					ConversationID: input.Access.ConversationID,
				},
			)
			if errors.Is(e, pgx.ErrNoRows) {
				if input.ExpectedBindingID != nil {
					return ErrStudioEditorBindingConflict
				}
				if s.namespace == uuid.Nil || probe.CreationProtocol != "receiptV1" {
					return ErrStudioEditorUnsupported
				}
				row, e = q.ReserveStudioReceipt(
					ctx,
					generated.ReserveStudioReceiptParams{
						CompanyID:      p.CompanyID,
						ConversationID: input.Access.ConversationID,
						Title:          input.Title,
						Description:    input.Description,
						ApiOrigin:      probe.Origin,
						CreationAccountID: uuid.NullUUID{
							UUID:  probe.StudioAccountID,
							Valid: true,
						},
						CreationNamespace: uuid.NullUUID{UUID: s.namespace, Valid: true},
						CreationRequestHash: pgtype.Text{
							String: strings.Repeat("0", 64),
							Valid:  true,
						},
						CreatedByUserID: uuid.NullUUID{UUID: p.UserID, Valid: true},
					},
				)
				if e != nil {
					return e
				}
				digest, e := studioRequestHash(
					StudioReceiptRequest{
						Namespace:     s.namespace,
						ReservationID: row.ID,
						Title:         row.Title,
						Description:   row.Description,
					},
				)
				if e != nil {
					return e
				}
				row, e = q.SetStudioReceiptHash(
					ctx,
					generated.SetStudioReceiptHashParams{
						ID:                  row.ID,
						CreationRequestHash: pgtype.Text{String: digest, Valid: true},
					},
				)
				if e != nil {
					return e
				}
			} else if e != nil {
				return e
			}
			if row.ApiOrigin != probe.Origin {
				return ErrStudioEditorOriginConflict
			}
			if !row.ProjectID.Valid && row.CreationProtocol == "legacy_marker" {
				return ErrStudioEditorRecoveryRequired
			}
			if !row.ProjectID.Valid && row.CreationAccountID.UUID != probe.StudioAccountID {
				return ErrStudioEditorAccountConflict
			}
			if input.ExpectedBindingID != nil {
				if row.ID != *input.ExpectedBindingID ||
					row.BindingRevision != input.ExpectedBindingRevision {
					return ErrStudioEditorBindingConflict
				}
			} else if row.Title != input.Title || row.Description != input.Description || row.CreationAccountID.Valid && row.CreationAccountID.UUID != probe.StudioAccountID {
				return ErrStudioEditorRequestConflict
			}
			now := time.Now().UTC()
			dispatch = StudioEnsureDispatch{
				RequestID:            input.RequestID,
				DispatchID:           uuid.New(),
				ActorUserID:          p.UserID,
				ActorSessionID:       p.SessionID,
				CompanyID:            p.CompanyID,
				ConversationID:       input.Access.ConversationID,
				RuntimeID:            input.Access.RuntimeID,
				AuthorityType:        "human",
				ConnectionGeneration: probe.ConnectionGeneration,
				Origin:               probe.Origin,
				StudioAccountID:      probe.StudioAccountID,
				AuthorizedAt:         now,
				DispatchExpiresAt:    now.Add(5 * time.Second),
				Binding:              studioBinding(row),
				Payload: StudioReceiptRequest{
					Namespace:     row.CreationNamespace.UUID,
					ReservationID: row.ID,
					Title:         row.Title,
					Description:   row.Description,
				},
			}
			return nil
		},
	)
	if err != nil {
		return StudioBindingResponse{}, err
	}
	if row.ProjectID.Valid {
		if err = conn.VerifyProject(ctx, dispatch); err != nil {
			return StudioBindingResponse{}, err
		}
		return studioBinding(row), nil
	}
	receipt, err := conn.Ensure(ctx, dispatch)
	if err != nil {
		return StudioBindingResponse{}, err
	}
	if receipt.Namespace != row.CreationNamespace.UUID || receipt.ReservationID != row.ID ||
		receipt.AccountID != row.CreationAccountID.UUID ||
		receipt.RequestHash != row.CreationRequestHash.String ||
		receipt.ProjectID == uuid.Nil ||
		receipt.State != "bound" {
		return StudioBindingResponse{}, ErrStudioEditorUpstreamInvalid
	}
	// A changed connection/account cannot finalize an old reply. No DB lock is held during the probe.
	current, err := conn.Probe(ctx, input.Access.RuntimeID)
	if err != nil {
		return StudioBindingResponse{}, err
	}
	if current.ConnectionGeneration != probe.ConnectionGeneration || current.Origin != probe.Origin ||
		current.StudioAccountID != probe.StudioAccountID {
		return StudioBindingResponse{}, ErrStudioEditorGenerationConflict
	}
	err = s.fence(
		ctx,
		studioAdmission{p, input.Access},
		func(q *generated.Queries, r generated.Runtime, agentID uuid.UUID) error {
			if agentID != conn.AgentID() {
				return ErrStudioEditorGenerationConflict
			}
			if r.DaemonID != probe.DaemonID {
				return ErrStudioEditorGenerationConflict
			}
			if e := s.checkDaemon(ctx, conn.Principal()); e != nil {
				return e
			}
			latest, e := q.GetConversationStudioProject(
				ctx,
				generated.GetConversationStudioProjectParams{
					CompanyID:      p.CompanyID,
					ConversationID: input.Access.ConversationID,
				},
			)
			if e != nil {
				return e
			}
			if latest.ID != row.ID || latest.ApiOrigin != row.ApiOrigin ||
				latest.CreationAccountID != row.CreationAccountID ||
				latest.CreationRequestHash != row.CreationRequestHash {
				return ErrStudioEditorBindingConflict
			}
			if latest.ProjectID.Valid {
				if latest.ProjectID.UUID != receipt.ProjectID {
					return ErrStudioEditorBindingConflict
				}
				row = latest
				return nil
			}
			row, e = q.FinalizeStudioReceipt(
				ctx,
				generated.FinalizeStudioReceiptParams{
					ID:                  row.ID,
					BindingRevision:     row.BindingRevision,
					ApiOrigin:           row.ApiOrigin,
					CreationAccountID:   row.CreationAccountID,
					CreationRequestHash: row.CreationRequestHash,
					ProjectID:           uuid.NullUUID{UUID: receipt.ProjectID, Valid: true},
				},
			)
			var pgErr *pgconn.PgError
			if errors.Is(e, pgx.ErrNoRows) || errors.As(e, &pgErr) && pgErr.Code == "23505" {
				return ErrStudioEditorBindingConflict
			}
			return e
		},
	)
	return studioBinding(row), err
}
