package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"boardly.dev/boardly/server/pkg/protocol"
)

// RuntimeService owns the runtime noun: the daemon plane's registration,
// deregistration, heartbeat, and liveness sweep (runtime_lifecycle.go), the
// transient model-discovery coordination, the registrant-filtered read
// projections the user plane serves, and the two user-plane writes — the
// machine rename (runtime_rename.go) and the registrant's removal
// (runtime_removal.go).
type RuntimeService interface {
	ReportTools(context.Context, auth.UserPrincipal, ToolObservationReport) error
	List(context.Context, auth.UserPrincipal, RuntimeListOptions) (RuntimePage, error)
	Get(context.Context, auth.UserPrincipal, uuid.UUID) (Runtime, error)
	Rename(context.Context, auth.UserPrincipal, uuid.UUID, RuntimeRenameInput) (Runtime, error)
	Remove(context.Context, auth.UserPrincipal, uuid.UUID, RuntimeRemovalInput) (RuntimeRemoval, error)
	RegisterRuntimes(context.Context, RuntimeRegistration) ([]Runtime, error)
	DeregisterRuntimes(context.Context, auth.UserPrincipal, RuntimeDeregistration) error
	Heartbeat(context.Context, auth.UserPrincipal, uuid.UUID, string) error
	MarkStaleRuntimesOffline(context.Context) (int, error)
	PruneOfflineRuntimes(context.Context) error
	InitiateModelDiscovery(context.Context, auth.UserPrincipal, uuid.UUID) (ModelDiscoveryRequest, error)
	RequestRefresh(context.Context, auth.UserPrincipal, uuid.UUID) error
	GetModelDiscovery(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID) (ModelDiscoveryRequest, error)
	ClaimModelDiscovery(context.Context, uuid.UUID) (*ModelDiscoveryRequest, error)
	FinishModelDiscovery(context.Context, uuid.UUID, uuid.UUID, ModelDiscoveryResult) (ModelDiscoveryRequest, error)
}

var _ RuntimeService = (*DefaultRuntimeService)(nil)

type DefaultRuntimeService struct {
	store            *db.Store
	bus              *events.Bus
	modelDiscoveries ModelDiscoveryStore
	presenter        inbox.Presenter
}

type RuntimeServiceOptions struct {
	Store            *db.Store
	Bus              *events.Bus
	ModelDiscoveries ModelDiscoveryStore
	// Presenter raises the transient toast beside a runtime_offline Inbox
	// item. Optional: without one the durable item is still written, and
	// only the desktop hint is lost.
	Presenter inbox.Presenter
}

func NewRuntimeService(options RuntimeServiceOptions) (*DefaultRuntimeService, error) {
	if options.Store == nil {
		return nil, errors.New("runtime database store is required")
	}
	if options.Bus == nil {
		return nil, errors.New("runtime event bus is required")
	}
	if options.ModelDiscoveries == nil {
		return nil, errors.New("runtime model discovery store is required")
	}
	return &DefaultRuntimeService{
		store:            options.Store,
		bus:              options.Bus,
		modelDiscoveries: options.ModelDiscoveries,
		presenter:        options.Presenter,
	}, nil
}

type RuntimeListOptions struct {
	Status   *string
	Provider *string
}

// OwnerRef is the runtime owner as the shared wire actor shape.
type OwnerRef = protocol.ActorRef

// The two LaunchedBy values. The daemon announces "desktop" when the desktop
// app spawned it and an empty string from a terminal start; the projection
// names the second case so clients never branch on "".
const (
	RuntimeLaunchedByDesktop = "desktop"
	RuntimeLaunchedByCLI     = "cli"
)

type Runtime struct {
	ID        uuid.UUID `json:"id"`
	CompanyID uuid.UUID `json:"companyId"`
	DaemonID  string    `json:"daemonId"`
	Name      string    `json:"name"`
	// CustomName is the name a person gave the machine, or nil when nobody
	// renamed it. It never replaces Name: the daemon's name stays the
	// registration's, so clearing the custom name restores it.
	CustomName *string `json:"customName"`
	Provider   string  `json:"provider"`
	Status     string  `json:"status"`
	// UnusableReason is why the machine's provider CLI cannot run work
	// (below the minimum version, not executable). Set only while Status is
	// offline; null for an ordinary row. The distinction the roster draws
	// between "machine off" and "machine on, CLI broken".
	UnusableReason *protocol.RuntimeUnusableReason `json:"unusableReason"`
	// SignIn is the daemon's last sign-in verdict for the provider CLI on
	// that machine, or nil when no registration has carried one yet. It
	// never changes Status: a signed-out provider is still a machine that
	// can be asked to sign in.
	SignIn *RuntimeSignIn `json:"signIn"`
	// Owner is the member whose machine this is — always the viewer on a
	// user-plane row (ADR 0014); nil when the registration carried no user.
	Owner      *OwnerRef `json:"owner"`
	DeviceInfo string    `json:"deviceInfo"`
	// ProviderVersion is the provider CLI version the daemon probed at
	// registration; CLIVersion is the zen daemon's own version. Both are
	// empty when the daemon reported none.
	Tools           []protocol.ToolObservation `json:"tools"`
	ProviderVersion string                     `json:"providerVersion"`
	CLIVersion      string                     `json:"cliVersion"`
	LaunchedBy      string                     `json:"launchedBy"`
	LastSeenAt      *time.Time                 `json:"lastSeenAt"`
	CreatedAt       time.Time                  `json:"createdAt"`
	UpdatedAt       time.Time                  `json:"updatedAt"`
}

// RuntimeSignIn is the domain form of protocol.RuntimeSignIn, on the way in
// (a registration, parsed once at the boundary) and out (served): the same
// state vocabulary, the daemon's sentence, and when it looked.
type RuntimeSignIn struct {
	BillingMode string    `json:"billingMode,omitempty"`
	State       string    `json:"state"`
	Detail      string    `json:"detail"`
	CheckedAt   time.Time `json:"checkedAt"`
}

// runtimeMetadata is the served subset of the registration announcement
// upsertRuntimeParams stores in runtimes.metadata. Rows that predate the
// announcement keys, or carry the '{}' default, decode to zero values.
type runtimeMetadata struct {
	BillingMode string                     `json:"billingMode"`
	Tools       []protocol.ToolObservation `json:"tools"`
	Version     string                     `json:"version"`
	CLIVersion  string                     `json:"cliVersion"`
	LaunchedBy  string                     `json:"launchedBy"`
}

type RuntimePage struct {
	Items []Runtime `json:"items"`
}

// List serves the roster the principal may see (ADR 0007): their own
// connections and the ones shared with the company, plus — for a company
// owner or admin — other members' private registrations in the registration
// projection. The filter is the query's, so a hidden row never reaches a
// count or a picker.
func (s *DefaultRuntimeService) List(
	ctx context.Context,
	principal auth.UserPrincipal,
	options RuntimeListOptions,
) (RuntimePage, error) {
	rows, err := s.store.Queries().ListRuntimesForCompany(
		ctx,
		generated.ListRuntimesForCompanyParams{
			CompanyID:    principal.CompanyID,
			ViewerUserID: principal.UserID,
			Status:       util.PtrToText(options.Status),
			Provider:     util.PtrToText(options.Provider),
		},
	)
	if err != nil {
		return RuntimePage{}, fmt.Errorf("list runtimes: %w", err)
	}
	items := make([]Runtime, 0, len(rows))
	for _, row := range rows {
		items = append(items, runtimeFromRow(row))
	}
	return RuntimePage{Items: items}, nil
}

// Get reads one runtime of the principal's company with its owner joined.
// The query carries the company fence and the viewer filter, so a foreign
// id, an unknown id and another member's private runtime the principal may
// not see are all the same ErrRuntimeNotFound.
func (s *DefaultRuntimeService) Get(
	ctx context.Context,
	principal auth.UserPrincipal,
	runtimeID uuid.UUID,
) (Runtime, error) {
	row, err := s.store.Queries().GetRuntimeForCompany(ctx, generated.GetRuntimeForCompanyParams{
		CompanyID:    principal.CompanyID,
		ID:           runtimeID,
		ViewerUserID: principal.UserID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Runtime{}, ErrRuntimeNotFound
	}
	if err != nil {
		return Runtime{}, fmt.Errorf("get runtime: %w", err)
	}
	return runtimeFromDetailRow(row), nil
}

func runtimeFromRow(row generated.ListRuntimesForCompanyRow) Runtime {
	return runtimeWithOwner(row.Runtime, row.OwnerID, row.OwnerDisplayName, row.OwnerAvatarUrl)
}

func runtimeFromDetailRow(row generated.GetRuntimeForCompanyRow) Runtime {
	return runtimeWithOwner(row.Runtime, row.OwnerID, row.OwnerDisplayName, row.OwnerAvatarUrl)
}

// runtimeWithOwner maps a runtimes row joined with its owning user, the
// shape both user-plane reads (the roster and the detail) share. The reads
// filter in SQL to the viewer's own machines, so every row that arrives here
// is the viewer's.
func runtimeWithOwner(
	row generated.Runtime,
	ownerID uuid.NullUUID,
	displayName, avatarURL pgtype.Text,
) Runtime {
	runtime := runtimeFromStoredRow(row)
	if ownerID.Valid {
		runtime.Owner = &OwnerRef{
			ID:          ownerID.UUID,
			Kind:        protocol.ActorKindUser,
			DisplayName: displayName.String,
			AvatarURL:   util.TextToPtr(avatarURL),
		}
	}
	return runtime
}

// runtimeFromStoredRow maps the bare runtimes row. Owner stays nil — only
// the user-plane reads join the owning user, and the daemon plane never
// serves the owner at all.
func runtimeFromStoredRow(row generated.Runtime) Runtime {
	// A read-back, not input: a document without the announcement keys is
	// a legitimate older row, so a decode failure yields zero values and is
	// never surfaced.
	var metadata runtimeMetadata
	_ = json.Unmarshal(row.Metadata, &metadata)
	launchedBy := RuntimeLaunchedByCLI
	if metadata.LaunchedBy == RuntimeLaunchedByDesktop {
		launchedBy = RuntimeLaunchedByDesktop
	}
	// The CHECK constraint keeps code and message together, so one column
	// decides presence.
	var unusableReason *protocol.RuntimeUnusableReason
	if row.UnusableReasonCode.Valid {
		unusableReason = &protocol.RuntimeUnusableReason{
			Code:    row.UnusableReasonCode.String,
			Message: row.UnusableReasonMessage.String,
		}
	}
	var signIn *RuntimeSignIn
	if row.ProviderSignInState.Valid {
		signIn = &RuntimeSignIn{
			BillingMode: metadata.BillingMode,
			State:       row.ProviderSignInState.String,
			Detail:      row.ProviderSignInDetail.String,
			CheckedAt:   row.ProviderSignInCheckedAt.Time.UTC(),
		}
	}
	if metadata.Tools == nil {
		metadata.Tools = []protocol.ToolObservation{}
	}
	return Runtime{
		Tools:           metadata.Tools,
		ID:              row.ID,
		CompanyID:       row.CompanyID,
		DaemonID:        row.DaemonID,
		Name:            row.Name,
		CustomName:      util.TextToPtr(row.CustomName),
		Provider:        row.Provider,
		Status:          row.Status,
		UnusableReason:  unusableReason,
		SignIn:          signIn,
		DeviceInfo:      row.DeviceInfo,
		ProviderVersion: metadata.Version,
		CLIVersion:      metadata.CLIVersion,
		LaunchedBy:      launchedBy,
		LastSeenAt:      util.TimestampToTime(row.LastSeenAt),
		CreatedAt:       row.CreatedAt.Time.UTC(),
		UpdatedAt:       row.UpdatedAt.Time.UTC(),
	}
}
