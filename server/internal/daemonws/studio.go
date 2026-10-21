package daemonws

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/conversation"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

type studioPending struct {
	responseType string
	response     chan protocol.StudioControlResponse
}

type studioConnection struct {
	client  *client
	agentID uuid.UUID
}

// Connect pins one authenticated socket. Ambiguous duplicate connections fail
// closed rather than selecting an arbitrary credential owner.
func (h *Hub) Connect(ctx context.Context, runtimeID, agentID uuid.UUID) (conversation.StudioEditorConnection, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	connections := h.byRuntime[runtimeID.String()]
	if len(connections) != 1 {
		return nil, conversation.ErrStudioEditorRuntimeUnavailable
	}
	for c := range connections {
		return &studioConnection{client: c, agentID: agentID}, nil
	}
	return nil, conversation.ErrStudioEditorRuntimeUnavailable
}

func (s *studioConnection) AgentID() uuid.UUID { return s.agentID }

func (s *studioConnection) Principal() auth.UserPrincipal { return s.client.identity.Principal }

func (s *studioConnection) call(
	ctx context.Context,
	kind string,
	id uuid.UUID,
	payload any,
	timeout time.Duration,
) (protocol.StudioControlResponse, error) {
	var empty protocol.StudioControlResponse
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if s.client.ctx.Err() != nil {
		return empty, conversation.ErrStudioEditorRuntimeUnavailable
	}
	raw, err := json.Marshal(
		protocol.Message{
			Type: kind,
			Payload: mustMarshalRaw(
				protocol.StudioScopedControl{AgentID: s.agentID, Request: mustMarshalRaw(payload)},
			),
		},
	)
	if err != nil || len(raw) > protocol.StudioRequestLimit {
		return empty, conversation.ErrStudioEditorUnsupported
	}
	c := s.client
	pending := studioPending{
		responseType: kind + ".response",
		response:     make(chan protocol.StudioControlResponse, 1),
	}
	c.studioMu.Lock()
	if _, exists := c.studioPending[id]; exists {
		c.studioMu.Unlock()
		return empty, conversation.ErrStudioEditorRequestConflict
	}
	if len(c.studioPending) >= 8 {
		c.studioMu.Unlock()
		return empty, conversation.ErrStudioEditorRateLimited
	}
	if c.studioPending == nil {
		c.studioPending = make(map[uuid.UUID]studioPending)
	}
	c.studioPending[id] = pending
	c.studioMu.Unlock()
	defer func() { c.studioMu.Lock(); delete(c.studioPending, id); c.studioMu.Unlock() }()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if !c.trySend(raw) {
		return empty, conversation.ErrStudioEditorRuntimeUnavailable
	}
	select {
	case <-ctx.Done():
		return empty, conversation.ErrStudioEditorOutcomeUnknown
	case <-c.ctx.Done():
		// The frame was sent; a socket loss cannot prove the mutation failed.
		return empty, conversation.ErrStudioEditorOutcomeUnknown
	case response := <-pending.response:
		if response.ErrorCode != "" {
			return empty, studioControlError(response.ErrorCode)
		}
		return response, nil
	}
}

func (c *client) handleStudioResponse(kind string, raw json.RawMessage) {
	var response protocol.StudioControlResponse
	if json.Unmarshal(raw, &response) != nil {
		return
	}
	c.studioMu.Lock()
	defer c.studioMu.Unlock()
	pending, ok := c.studioPending[response.CorrelationID]
	if !ok || pending.responseType != kind {
		return
	}
	select {
	case pending.response <- response:
	default:
	}
}

func (s *studioConnection) Probe(
	ctx context.Context,
	runtimeID uuid.UUID,
) (conversation.StudioConnectionResponse, error) {
	id := uuid.New()
	response, err := s.call(
		ctx,
		protocol.EventStudioConnectionProbe,
		id,
		protocol.StudioConnectionProbeRequest{ProbeID: id, RuntimeID: runtimeID},
		15*time.Second,
	)
	if err != nil {
		return conversation.StudioConnectionResponse{}, err
	}
	if response.Probe == nil || response.Probe.ProbeID != id || response.Probe.RuntimeID != runtimeID ||
		response.Probe.DaemonID != s.client.identity.DaemonID {
		return conversation.StudioConnectionResponse{}, conversation.ErrStudioEditorUpstreamInvalid
	}
	return conversation.StudioConnectionResponse(*response.Probe), nil
}

func (s *studioConnection) Token(
	ctx context.Context,
	runtimeID uuid.UUID,
) (protocol.StudioAccessToken, error) {
	id := uuid.New()
	response, err := s.call(
		ctx,
		protocol.EventStudioTokenRequest,
		id,
		protocol.StudioTokenRequest{RequestID: id, RuntimeID: runtimeID},
		15*time.Second,
	)
	if err != nil {
		return protocol.StudioAccessToken{}, err
	}
	if response.Token == nil || response.Token.AccessToken == "" || response.Token.AccountID == uuid.Nil ||
		response.Token.TokenType != "Bearer" || response.Token.ExpiresAt.Before(time.Now()) {
		return protocol.StudioAccessToken{}, conversation.ErrStudioEditorUpstreamInvalid
	}
	return *response.Token, nil
}

func (s *studioConnection) authority(d conversation.StudioEnsureDispatch) protocol.StudioAuthority {
	return protocol.StudioAuthority{
		ActorUserID:          d.ActorUserID,
		ActorSessionID:       d.ActorSessionID,
		CompanyID:            d.CompanyID,
		ConversationID:       d.ConversationID,
		RuntimeID:            d.RuntimeID,
		DaemonID:             s.client.identity.DaemonID,
		ConnectionGeneration: d.ConnectionGeneration,
		BindingID:            &d.Binding.BindingID,
		BindingRevision:      d.Binding.BindingRevision,
		Origin:               d.Origin,
		StudioAccountID:      d.StudioAccountID,
		AuthorizedAt:         d.AuthorizedAt,
		DispatchExpiresAt:    d.DispatchExpiresAt,
		AuthorityType:        d.AuthorityType,
	}
}

func (s *studioConnection) Ensure(
	ctx context.Context,
	d conversation.StudioEnsureDispatch,
) (conversation.StudioReceiptResponse, error) {
	dispatch := protocol.StudioDaemonDispatch{
		SchemaVersion: 2,
		DispatchID:    d.DispatchID,
		Authority:     s.authority(d),
		Request: protocol.StudioDaemonRequest{
			SchemaVersion: 2,
			RequestID:     d.RequestID,
			RuntimeID:     d.RuntimeID,
			Operation:     "ensureBinding",
			Payload: mustMarshalRaw(
				protocol.StudioEnsurePayload{
					Namespace:     d.Payload.Namespace,
					ReservationID: d.Payload.ReservationID,
					Title:         d.Payload.Title,
					Description:   d.Payload.Description,
				},
			),
		},
	}
	response, err := s.call(ctx, protocol.EventStudioEditorDispatch, d.DispatchID, dispatch, 30*time.Second)
	if err != nil {
		return conversation.StudioReceiptResponse{}, err
	}
	var result struct {
		DispatchID uuid.UUID `json:"dispatchId"`
		Result     struct {
			SchemaVersion int                    `json:"schemaVersion"`
			RequestID     uuid.UUID              `json:"requestId"`
			Operation     string                 `json:"operation"`
			Data          protocol.StudioReceipt `json:"data"`
		} `json:"result"`
	}
	if json.Unmarshal(response.Dispatch, &result) != nil || result.DispatchID != d.DispatchID ||
		result.Result.RequestID != d.RequestID ||
		result.Result.SchemaVersion != 2 ||
		result.Result.Operation != "ensureBinding" {
		return conversation.StudioReceiptResponse{}, conversation.ErrStudioEditorUpstreamInvalid
	}
	return conversation.StudioReceiptResponse(result.Result.Data), nil
}

func (s *studioConnection) VerifyProject(ctx context.Context, d conversation.StudioEnsureDispatch) error {
	response, err := s.call(
		ctx,
		protocol.EventStudioBindingVerify,
		d.DispatchID,
		protocol.StudioVerifyRequest{
			DispatchID: d.DispatchID,
			Authority:  s.authority(d),
			Binding:    protocol.StudioBinding(d.Binding),
		},
		15*time.Second,
	)
	if err == nil && (response.Capabilities == nil || !response.Capabilities.GetProjectBootstrap) {
		return conversation.ErrStudioEditorUpstreamDenied
	}
	return err
}

func studioControlError(code string) error {
	switch code {
	case "authority_unavailable":
		return conversation.ErrStudioEditorAuthorityUnavailable
	case "permission_denied":
		return conversation.ErrStudioEditorPermissionDenied
	case "binding_conflict":
		return conversation.ErrStudioEditorBindingConflict
	case "generation_conflict":
		return conversation.ErrStudioEditorGenerationConflict
	case "request_conflict":
		return conversation.ErrStudioEditorRequestConflict
	case "reauth_required":
		return conversation.ErrStudioEditorReauthRequired
	case "upstream_denied":
		return conversation.ErrStudioEditorUpstreamDenied
	case "source_unavailable":
		return conversation.ErrStudioEditorSourceUnavailable
	case "rate_limited":
		return conversation.ErrStudioEditorRateLimited
	case "outcome_unknown":
		return conversation.ErrStudioEditorOutcomeUnknown
	case "unsupported":
		return conversation.ErrStudioEditorUnsupported
	case "origin_conflict":
		return conversation.ErrStudioEditorOriginConflict
	case "account_conflict":
		return conversation.ErrStudioEditorAccountConflict
	case "runtime_unavailable":
		return conversation.ErrStudioEditorRuntimeUnavailable
	default:
		return conversation.ErrStudioEditorUpstreamInvalid
	}
}
