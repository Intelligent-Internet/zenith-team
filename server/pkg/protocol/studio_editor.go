package protocol

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

const (
	EventStudioConnectionProbe         = "studio.connection.probe"
	EventStudioConnectionProbeResponse = "studio.connection.probe.response"
	EventStudioEditorDispatch          = "studio.editor.dispatch"
	EventStudioEditorDispatchResponse  = "studio.editor.dispatch.response"
	EventStudioBindingVerify           = "studio.binding.verify"
	EventStudioBindingVerifyResponse   = "studio.binding.verify.response"
	EventStudioTokenRequest            = "studio.token"
	EventStudioTokenResponse           = "studio.token.response"
	StudioRequestLimit                 = 16 << 10
	StudioResponseLimit                = 8 << 20
)

// StudioAuthority is the canonical Authority from the editor v2 contract.
// Only the authenticated Team control sender can create it.
type StudioAuthority struct {
	ActorUserID          uuid.UUID  `json:"actorUserId"`
	ActorSessionID       uuid.UUID  `json:"actorSessionId"`
	CompanyID            uuid.UUID  `json:"companyId"`
	ConversationID       uuid.UUID  `json:"conversationId"`
	RuntimeID            uuid.UUID  `json:"runtimeId"`
	DaemonID             string     `json:"daemonId"`
	ConnectionGeneration uuid.UUID  `json:"connectionGeneration"`
	BindingID            *uuid.UUID `json:"bindingId"`
	BindingRevision      int64      `json:"bindingRevision"`
	Origin               string     `json:"origin"`
	StudioAccountID      uuid.UUID  `json:"studioAccountId"`
	AuthorizedAt         time.Time  `json:"authorizedAt"`
	DispatchExpiresAt    time.Time  `json:"dispatchExpiresAt"`
	AuthorityType        string     `json:"authorityType"`
}

type StudioBinding struct {
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

type StudioConnectionProbeRequest struct {
	ProbeID   uuid.UUID `json:"probeId"`
	RuntimeID uuid.UUID `json:"runtimeId"`
}

type StudioTokenRequest struct {
	RequestID uuid.UUID `json:"requestId"`
	RuntimeID uuid.UUID `json:"runtimeId"`
}

type StudioConnectionProbeResponse struct {
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

// StudioAccessToken is a short-lived upstream credential obtained from the
// daemon-owned Studio CLI store. It is returned only to an authenticated
// desktop caller during the token-broker migration; refresh credentials never
// cross this boundary.
type StudioAccessToken struct {
	AccessToken string    `json:"accessToken"`
	TokenType   string    `json:"tokenType"`
	ExpiresAt   time.Time `json:"expiresAt"`
	AccountID   uuid.UUID `json:"accountId"`
	Origin      string    `json:"origin"`
}

type StudioEnsurePayload struct {
	Namespace     uuid.UUID `json:"namespace"`
	ReservationID uuid.UUID `json:"reservationId"`
	Title         string    `json:"title"`
	Description   string    `json:"description"`
}

type StudioDaemonRequest struct {
	SchemaVersion int             `json:"schemaVersion"`
	RequestID     uuid.UUID       `json:"requestId"`
	RuntimeID     uuid.UUID       `json:"runtimeId"`
	Operation     string          `json:"operation"`
	Payload       json.RawMessage `json:"payload"`
}

type StudioDaemonDispatch struct {
	SchemaVersion int                 `json:"schemaVersion"`
	DispatchID    uuid.UUID           `json:"dispatchId"`
	Authority     StudioAuthority     `json:"authority"`
	Request       StudioDaemonRequest `json:"request"`
}

type StudioReceipt struct {
	Namespace     uuid.UUID `json:"namespace"`
	ReservationID uuid.UUID `json:"reservationId"`
	AccountID     uuid.UUID `json:"accountId"`
	ProjectID     uuid.UUID `json:"projectId"`
	RequestHash   string    `json:"requestHash"`
	State         string    `json:"state"`
}

type StudioCapabilities struct {
	GetProjectBootstrap bool    `json:"getProjectBootstrap"`
	UpdateNodePosition  bool    `json:"updateNodePosition"`
	CreateNode          bool    `json:"createNode"`
	UpdateNode          bool    `json:"updateNode"`
	DeleteNode          bool    `json:"deleteNode"`
	CreateEdge          bool    `json:"createEdge"`
	DeleteEdge          bool    `json:"deleteEdge"`
	ApplyLayout         bool    `json:"applyLayout"`
	DisabledReason      *string `json:"disabledReason"`
}

// StudioVerifyRequest carries a trusted server binding, never a renderer project selector.
type StudioVerifyRequest struct {
	DispatchID uuid.UUID       `json:"dispatchId"`
	Authority  StudioAuthority `json:"authority"`
	Binding    StudioBinding   `json:"binding"`
}

// StudioControlResponse adds correlation and bounded errors to the paired control frames.
// The nested probe, dispatch and capabilities retain their canonical DTOs.
type StudioControlResponse struct {
	CorrelationID uuid.UUID                      `json:"correlationId"`
	Probe         *StudioConnectionProbeResponse `json:"probe,omitempty"`
	Token         *StudioAccessToken             `json:"token,omitempty"`
	Dispatch      json.RawMessage                `json:"dispatch,omitempty"`
	Capabilities  *StudioCapabilities            `json:"capabilities,omitempty"`
	ErrorCode     string                         `json:"errorCode,omitempty"`
}

// StudioScopedControl selects the server-resolved Agent profile on the daemon.
// Request remains the canonical upstream editor DTO; the scope is not sent to Studio.
type StudioScopedControl struct {
	AgentID uuid.UUID       `json:"agentId"`
	Request json.RawMessage `json:"request"`
}
