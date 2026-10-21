package agent

// Runtime lifecycle: daemon registration, deregistration, and the offline
// sweep. The daemon-plane HTTP handlers and the cmd/server sweeper are thin
// callers into RuntimeService; every roster and agent.status event those
// callers used to assemble inline is published here, after the writes that
// justify it commit.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/inbox"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

// RuntimeStaleThreshold marks runtimes offline when no heartbeat arrives for
// this long. Zenith Team phase 1 writes last_seen_at on every daemon heartbeat
// (~15s) with no Redis liveness lane, so the DB stale window is the only
// authority; 150s tolerates several missed beats while keeping detection
// latency for a genuinely-dead runtime under threshold + sweep interval
// (~3 minutes). Exported because the run sweeper's running-run backstop must
// use the same "not alive" window (execution.StaleRunOptions.RuntimeStale).
const RuntimeStaleThreshold = 150 * time.Second

// offlineRuntimeTTL deletes offline runtimes after this duration. 7 days
// gives users plenty of time to restart daemons.
const offlineRuntimeTTL = 7 * 24 * time.Hour

// RuntimeRegistrationSpec is one provider instance a daemon announces.
// UnusableReason is set for a provider the daemon found but cannot run; the
// runtime is then stored offline whatever Status says, and a later
// announcement without it clears the reason.
type RuntimeRegistrationSpec struct {
	Name           string
	Provider       string
	Version        string
	Status         string
	UnusableReason *protocol.RuntimeUnusableReason
	// SignIn is the provider CLI's sign-in verdict from this probe round;
	// nil keeps the row's last verdict.
	SignIn *RuntimeSignIn
}

// RuntimeRegistration is one daemon's whole roster announcement for one
// company. The handler validates the provider vocabulary and company access
// at the boundary; the registration itself commits as one transaction.
type RuntimeRegistration struct {
	CompanyID   uuid.UUID
	OwnerUserID uuid.UUID
	DaemonID    string
	DeviceName  string
	CLIVersion  string
	LaunchedBy  string
	Runtimes    []RuntimeRegistrationSpec
}

// RegisterRuntimes upserts every announced runtime in ONE transaction — a
// mid-roster failure leaves no partial roster behind — then publishes the
// roster broadcast plus the agent.status availability recomputation for
// every registered runtime that has bound agents. Every runtime, not only
// the online ones: a re-registration can take an online runtime offline
// (an unusable reason forces it), and the bound agents' cached availability
// has to flip with it. The frame is idempotent, so a runtime whose status
// did not change costs one redundant broadcast per registration.
//
// Registration is also a return from offline, and the commonest one: a
// restarted daemon registers before it beats, so every runtime that comes
// back online here resolves its runtime_offline Inbox items in the same
// transaction. Waiting for the first heartbeat would leave the notice open
// on exactly this path.
//
// A runtime is one person's use of one machine: the registrant's rows
// are keyed by their own identity, so registering never moves anyone else's
// row or setup. The machine is one person's at a time, though, so anyone
// else's live rows on it are handed over first (runtime_handover.go), and
// the registrant's own rows that were handed over to someone meanwhile are
// their return.
func (s *DefaultRuntimeService) RegisterRuntimes(
	ctx context.Context,
	registration RuntimeRegistration,
) ([]Runtime, error) {
	if !util.ValidUUID(registration.OwnerUserID) {
		return nil, ErrInvalidInput
	}
	registered := make([]Runtime, 0, len(registration.Runtimes))
	var (
		pending  []events.Event
		handover machineHandover
	)
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		registered = registered[:0]
		pending = nil
		var err error
		if handover, err = handOverMachineInTx(ctx, tx, &pending, registration); err != nil {
			return err
		}
		returning, err := tx.ListHandedOverRuntimesForOwner(ctx, generated.ListHandedOverRuntimesForOwnerParams{
			CompanyID:   registration.CompanyID,
			DaemonID:    registration.DaemonID,
			OwnerUserID: registration.OwnerUserID,
		})
		if err != nil {
			return fmt.Errorf("list the registrant's handed-over runtimes: %w", err)
		}
		for _, spec := range registration.Runtimes {
			row, err := tx.UpsertRuntime(ctx, upsertRuntimeParams(registration, spec))
			if err != nil {
				return fmt.Errorf("register %s runtime: %w", spec.Provider, err)
			}
			registered = append(registered, runtimeFromStoredRow(row))
			if row.Status != "online" {
				continue
			}
			if err := closeRuntimeOfflineNoticesInTx(
				ctx, tx, &pending, row.CompanyID, row.ID,
			); err != nil {
				return fmt.Errorf("resolve %s runtime offline notices: %w", spec.Provider, err)
			}
		}
		return closeReturnNoticesInTx(ctx, tx, &pending, registration.CompanyID, returning)
	})
	if err != nil {
		return nil, err
	}

	runtimeIDs := make([]string, 0, len(registered))
	for _, runtime := range registered {
		runtimeIDs = append(runtimeIDs, runtime.ID.String())
	}
	s.publishRoster(ctx, protocol.DaemonRosterActionRegister, registration.CompanyID,
		registration.OwnerUserID, registration.DaemonID, runtimeIDs)
	for _, runtime := range registered {
		s.publishAgentStatuses(ctx, runtime.CompanyID, runtime.ID)
	}
	// The people the machine was handed over from read their own roster:
	// their rows went offline, which a deregister frame tells their clients.
	for _, owner := range handover.owners {
		s.publishRoster(ctx, protocol.DaemonRosterActionDeregister, registration.CompanyID, owner, "", nil)
	}
	for _, runtime := range handover.runtimes {
		s.publishAgentStatuses(ctx, runtime.CompanyID, runtime.ID)
	}
	s.publishPending(ctx, pending)
	for _, result := range handover.created {
		inbox.PresentCreated(s.presenter, result)
	}
	return registered, nil
}

// upsertRuntimeParams derives one runtime's stored identity from the daemon's
// announcement: a blank name falls back to "<provider> (<device>)", the
// device info carries the provider version, anything but an explicit
// "offline" registers online — unless the announcement carries an unusable
// reason, which forces offline and fills the two typed columns. Without a
// reason both columns are NULL, and because the upsert replaces them
// wholesale that is what clears a stored reason.
func upsertRuntimeParams(
	registration RuntimeRegistration,
	spec RuntimeRegistrationSpec,
) generated.UpsertRuntimeParams {
	name := strings.TrimSpace(spec.Name)
	if name == "" {
		name = spec.Provider
		if registration.DeviceName != "" {
			name = fmt.Sprintf("%s (%s)", spec.Provider, registration.DeviceName)
		}
	}
	deviceInfo := registration.DeviceName
	if spec.Version != "" && deviceInfo != "" {
		deviceInfo = fmt.Sprintf("%s · %s", deviceInfo, spec.Version)
	} else if spec.Version != "" {
		deviceInfo = spec.Version
	}
	status := "online"
	if spec.Status == "offline" || spec.UnusableReason != nil {
		status = "offline"
	}
	billingMode := "unknown"
	if spec.SignIn != nil && spec.SignIn.BillingMode != "" {
		billingMode = spec.SignIn.BillingMode
	}
	metadata, _ := json.Marshal(map[string]any{
		"version":     spec.Version,
		"cliVersion":  registration.CLIVersion,
		"launchedBy":  registration.LaunchedBy,
		"billingMode": billingMode,
	})
	params := generated.UpsertRuntimeParams{
		CompanyID:   registration.CompanyID,
		DaemonID:    registration.DaemonID,
		Name:        name,
		Provider:    spec.Provider,
		Status:      status,
		DeviceInfo:  deviceInfo,
		DeviceName:  util.StrToText(registration.DeviceName),
		Metadata:    metadata,
		OwnerUserID: uuid.NullUUID{UUID: registration.OwnerUserID, Valid: true},
	}
	if spec.UnusableReason != nil {
		params.UnusableReasonCode = util.StrToText(spec.UnusableReason.Code)
		params.UnusableReasonMessage = util.StrToText(spec.UnusableReason.Message)
	}
	if spec.SignIn != nil {
		params.ProviderSignInState = util.StrToText(spec.SignIn.State)
		params.ProviderSignInDetail = util.StrToText(spec.SignIn.Detail)
		params.ProviderSignInCheckedAt = pgtype.Timestamptz{
			Time: spec.SignIn.CheckedAt.UTC(), Valid: true,
		}
	}
	return params
}

// RuntimeDeregistration is one daemon's deregister batch. DaemonID is the
// machine identity the daemon registered with (X-Zenith-Team-Daemon-ID on the
// wire): when set, a runtime registered by another daemon is skipped like an
// unknown id. Empty means no daemon match — the transport's rollout policy
// decides whether a daemon that sent no id is admitted, and this domain then
// keeps the registrant and membership as the gates.
type RuntimeDeregistration struct {
	DaemonID   string
	RuntimeIDs []string
}

// DeregisterRuntimes marks the caller's runtimes offline in ONE transaction.
// An id that names no runtime of the caller's — malformed, unknown, foreign,
// registered by another daemon or by another member — is skipped with a log
// line: deregistering something already gone is the converged state. A
// failed read or write rolls the whole batch back and is returned with
// nothing published, so the daemon's 200 receipt means the batch applied and
// its 5xx means none of it did. Notifications follow the commit — a roster
// frame per affected company to the caller, who registered every runtime
// taken offline, and an agent.status recomputation per runtime taken offline.
func (s *DefaultRuntimeService) DeregisterRuntimes(
	ctx context.Context,
	principal auth.UserPrincipal,
	input RuntimeDeregistration,
) error {
	var offlined []generated.Runtime
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		rows, err := deregisterRuntimesInTx(ctx, tx, principal, input)
		offlined = rows
		return err
	})
	if err != nil {
		return err
	}

	affectedCompanies := make(map[uuid.UUID]struct{}, len(offlined))
	for _, runtime := range offlined {
		affectedCompanies[runtime.CompanyID] = struct{}{}
	}
	for companyID := range affectedCompanies {
		s.publishRoster(ctx, protocol.DaemonRosterActionDeregister, companyID, principal.UserID, "", nil)
	}
	for _, runtime := range offlined {
		s.publishAgentStatuses(ctx, runtime.CompanyID, runtime.ID)
	}
	return nil
}

// deregisterRuntimesInTx is the deregister loop on the caller's transaction
// handle: per id, load the runtime, verify it belongs to the calling daemon
// (when the batch names one), that the caller registered it (ADR 0014) and
// is an active member of its company, and set it offline. Skips (malformed,
// unknown, another daemon's, another member's, foreign) are logged and the
// loop continues; any other failure stops the loop and is returned so the
// caller rolls back everything written before it. Returns the runtimes it
// took offline, as they were before the write.
func deregisterRuntimesInTx(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	input RuntimeDeregistration,
) ([]generated.Runtime, error) {
	var offlined []generated.Runtime
	for _, raw := range input.RuntimeIDs {
		runtimeID, err := util.ParseGoUUID(raw)
		if err != nil {
			slog.Warn("deregister: invalid runtime id", "runtime_id", raw)
			continue
		}
		runtime, err := q.GetRuntime(ctx, runtimeID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				slog.Warn("deregister: runtime not found", "runtime_id", raw)
				continue
			}
			return nil, fmt.Errorf("deregister: load runtime %s: %w", raw, err)
		}
		if input.DaemonID != "" && runtime.DaemonID != input.DaemonID {
			slog.Warn("deregister: daemon mismatch", "runtime_id", raw)
			continue
		}
		if !registeredBy(runtime.OwnerUserID, principal.UserID) {
			slog.Warn("deregister: not the registrant", "runtime_id", raw)
			continue
		}
		if _, err := q.GetActiveCompanyMembership(ctx, generated.GetActiveCompanyMembershipParams{
			CompanyID: runtime.CompanyID,
			UserID:    principal.UserID,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				slog.Warn("deregister: company mismatch", "runtime_id", raw)
				continue
			}
			return nil, fmt.Errorf("deregister: verify membership for runtime %s: %w", raw, err)
		}
		if _, err := q.SetRuntimeOffline(ctx, runtime.ID); err != nil {
			return nil, fmt.Errorf("deregister: set runtime %s offline: %w", raw, err)
		}
		offlined = append(offlined, runtime)
	}
	return offlined, nil
}

// RevokeRuntimeOwnerInTx is the cross-domain participant for a member
// losing their standing in a company through removal, leaving, or account
// deletion (ADR 0014): a member who leaves takes their setup with them, so
// every binding they hold in the company is deleted and one
// agent.binding_changed per binding is staged for them. Their queued and
// in-flight work is the caller's to cancel
// (execution.CancelRuntimeOwnerRunsInTx); the Tasks assigned to them stay
// assigned — reading as not ready — until someone with tasks.assign hands
// them off. The caller owns the transaction and publishes pending after
// commit.
func RevokeRuntimeOwnerInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	companyID, ownerUserID uuid.UUID,
) error {
	if q == nil || pending == nil || !util.ValidUUID(companyID) || !util.ValidUUID(ownerUserID) {
		return ErrInvalidInput
	}
	bindings, err := q.DeleteAgentRuntimeBindingsForUser(ctx, generated.DeleteAgentRuntimeBindingsForUserParams{
		CompanyID: companyID,
		UserID:    ownerUserID,
	})
	if err != nil {
		return fmt.Errorf("delete departed member's agent bindings: %w", err)
	}
	for _, binding := range bindings {
		*pending = append(*pending, bindingChangeEvent(companyID, ownerUserID, binding.AgentID, nil))
	}
	return nil
}

// MarkStaleRuntimesOffline is the sweep policy: demote every online runtime
// whose last heartbeat fell behind RuntimeStaleThreshold, tell the people who
// can restart a machine that died holding work, then broadcast the roster
// change per company and the agent.status flip per demoted runtime. The
// SELECT-then-UPDATE split lets the UPDATE re-check the stale predicate so a
// heartbeat landing in between cannot demote a just-refreshed runtime.
// Returns how many runtimes flipped so the caller can fail their orphaned
// runs.
//
// The whole sweep is ONE transaction because the notification and the offline
// state have to agree: the in-flight count is read while the rows are still
// online, and the Inbox items commit with the demotion that justifies them.
// The dependency runs one way only. A notice that cannot be raised is logged
// and skipped inside the transaction (stageRuntimeOfflineNotices), because
// liveness is what this sweep is for: one unwritable item must never be able
// to keep every stale runtime in the fleet online, its agents available and
// its dead runs in flight.
// The runs themselves are failed just after, by the sweeper's next step
// (execution.FailRunsForOfflineRuntimes) over exactly the statuses
// CountInFlightRunsByRuntime counts here. Lock order is the canonical one —
// company/member (the recipient locks) before the runtime UPDATE before the
// Inbox writes — so the sweep cannot deadlock against a run raising its own
// Inbox items.
func (s *DefaultRuntimeService) MarkStaleRuntimesOffline(ctx context.Context) (int, error) {
	var (
		candidates []generated.SelectStaleOnlineRuntimesRow
		staleRows  []generated.MarkRuntimesOfflineByIDsRow
		pending    []events.Event
		created    []inbox.CreateResult
	)
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		candidates, staleRows, pending, created = nil, nil, nil, nil
		var err error
		candidates, err = tx.SelectStaleOnlineRuntimes(ctx, RuntimeStaleThreshold.Seconds())
		if err != nil {
			return fmt.Errorf("list stale online runtimes: %w", err)
		}
		if len(candidates) == 0 {
			return nil
		}
		ids := make([]uuid.UUID, 0, len(candidates))
		for _, candidate := range candidates {
			ids = append(ids, candidate.ID)
		}
		inFlight, recipients, err := runtimeOfflineAudience(ctx, tx, candidates, ids)
		if err != nil {
			return err
		}
		targets := make([]runtimeOfflineRecipientKey, 0, len(recipients))
		for _, candidate := range candidates {
			for _, recipient := range recipients[candidate.ID] {
				targets = append(targets, runtimeOfflineRecipientKey{
					companyID: candidate.CompanyID, userID: recipient,
				})
			}
		}
		eligible, err := lockRuntimeOfflineRecipients(ctx, tx, targets)
		if err != nil {
			return err
		}

		staleRows, err = tx.MarkRuntimesOfflineByIDs(ctx, generated.MarkRuntimesOfflineByIDsParams{
			Ids:       ids,
			StaleSecs: RuntimeStaleThreshold.Seconds(),
		})
		if err != nil {
			return fmt.Errorf("mark stale runtimes offline: %w", err)
		}
		if len(staleRows) == 0 {
			// All candidates raced into a non-online state between the SELECT
			// and the UPDATE. Nothing to broadcast.
			return nil
		}
		// Only the rows the UPDATE actually demoted, and only those holding
		// work, are worth a notice.
		notify := make([]uuid.UUID, 0, len(staleRows))
		for _, row := range staleRows {
			if inFlight[row.ID] > 0 {
				notify = append(notify, row.ID)
			}
		}
		if len(notify) == 0 {
			return nil
		}
		rows, err := tx.GetRuntimes(ctx, notify)
		if err != nil {
			return fmt.Errorf("load demoted runtimes: %w", err)
		}
		created = stageRuntimeOfflineNotices(
			ctx, tx, &pending, rows, inFlight, recipients, eligible, time.Now().UTC())
		return nil
	})
	if err != nil {
		return 0, err
	}
	if len(staleRows) == 0 {
		return 0, nil
	}

	companies := make(map[uuid.UUID]struct{}, len(staleRows))
	flipped := make(map[uuid.UUID]struct{}, len(staleRows))
	for _, row := range staleRows {
		companies[row.CompanyID] = struct{}{}
		flipped[row.ID] = struct{}{}
	}
	slog.Info("runtime sweep: marked stale runtimes offline",
		"count", len(staleRows), "companies", len(companies), "notices", len(created))

	for companyID := range companies {
		s.publishRoster(ctx, protocol.DaemonRosterActionStaleSweep, companyID, uuid.Nil, "", nil)
	}
	// flipped narrows the candidates to the rows the UPDATE actually demoted.
	for _, candidate := range candidates {
		if _, ok := flipped[candidate.ID]; !ok {
			continue
		}
		s.publishAgentStatuses(ctx, candidate.CompanyID, candidate.ID)
	}
	s.publishPending(ctx, pending)
	for _, result := range created {
		inbox.PresentCreated(s.presenter, result)
	}
	return len(staleRows), nil
}

// runtimeOfflineAudience answers, for the runtimes about to be demoted, how
// much work each is holding and who would be told. Runtimes with nothing in
// flight get no audience at all: the notice goes out only when the demotion
// costs a run.
func runtimeOfflineAudience(
	ctx context.Context,
	tx *generated.Queries,
	candidates []generated.SelectStaleOnlineRuntimesRow,
	ids []uuid.UUID,
) (map[uuid.UUID]int64, map[uuid.UUID][]uuid.UUID, error) {
	counted, err := tx.CountInFlightRunsByRuntime(ctx, ids)
	if err != nil {
		return nil, nil, fmt.Errorf("count in-flight runs of stale runtimes: %w", err)
	}
	inFlight := make(map[uuid.UUID]int64, len(counted))
	for _, row := range counted {
		inFlight[row.RuntimeID] = row.InFlight
	}
	recipients := make(map[uuid.UUID][]uuid.UUID, len(counted))
	for _, candidate := range candidates {
		if inFlight[candidate.ID] == 0 {
			continue
		}
		recipients[candidate.ID] = runtimeOfflineRecipients(candidate.OwnerUserID)
	}
	return inFlight, recipients, nil
}

// publishPending publishes what a transaction staged, one batch per topic,
// after it committed.
func (s *DefaultRuntimeService) publishPending(ctx context.Context, pending []events.Event) {
	for _, batch := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, batch.Topic, batch.Events...)
	}
}

// Heartbeat records one runtime as alive: verify the beat comes from the
// daemon that registered the runtime and that the caller may act for the
// runtime's company, touch last_seen_at, and — when the beat brought an
// offline runtime back — resolve whatever runtime_offline Inbox items the
// sweep raised about it and publish the agent.status availability
// recomputation. Zenith Team phase 1 has no Redis liveness lane, so this DB write
// is the liveness authority the sweeper trusts.
//
// daemonID is the machine identity the daemon registered with
// (X-Zenith-Team-Daemon-ID on the wire). Empty means unfenced — the transport's
// rollout policy decides whether a daemon that sent no id is admitted, and
// membership is then the only gate.
//
// ErrRuntimeNotFound is reserved for a genuinely missing row — never
// registered, garbage-collected, or removed by the owner: the daemon reads
// it as runtime-gone and prunes + re-registers, and the re-registration is
// what brings a removed row back. A denial — membership OR a daemon
// mismatch — is ErrRuntimeAccessDenied: the HTTP plane folds it into the
// same non-disclosing 404, the WS seam logs it instead. Any other failure
// returns wrapped so a transient database error can never masquerade as
// either.
func (s *DefaultRuntimeService) Heartbeat(
	ctx context.Context,
	principal auth.UserPrincipal,
	runtimeID uuid.UUID,
	daemonID string,
) error {
	queries := s.store.Queries()
	runtime, err := queries.GetRuntime(ctx, runtimeID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRuntimeNotFound
		}
		slog.Warn("get runtime failed", "runtime_id", runtimeID.String(), "error", err)
		return fmt.Errorf("load runtime: %w", err)
	}
	if daemonID != "" && runtime.DaemonID != daemonID {
		slog.Warn("heartbeat: daemon mismatch", "runtime_id", runtimeID.String())
		return ErrRuntimeAccessDenied
	}
	// The machine speaks for its registrant alone (ADR 0014): a session of
	// another account, member or not, cannot keep someone else's row alive.
	if !registeredBy(runtime.OwnerUserID, principal.UserID) {
		slog.Warn("heartbeat: registrant mismatch", "runtime_id", runtimeID.String())
		return ErrRuntimeAccessDenied
	}
	if _, err := queries.GetActiveCompanyMembership(ctx, generated.GetActiveCompanyMembershipParams{
		CompanyID: runtime.CompanyID,
		UserID:    principal.UserID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRuntimeAccessDenied
		}
		// A transient failure must not collapse into the 404 the daemon reads
		// as runtime-gone: that reply triggers prune + re-register +
		// RecoverOrphans, which fails every run the healthy daemon is still
		// executing. The daemon client's code relies on this separation.
		slog.Warn("heartbeat membership check failed",
			"runtime_id", runtimeID.String(), "error", err)
		return fmt.Errorf("verify daemon company membership: %w", err)
	}

	// Every beat — one per runtime every 15 s — is a single autocommit
	// UPDATE, and that write is what decides. The read above is an
	// authorization input only: the stale sweep commits a demotion every 30 s,
	// and a beat that judged the return from offline by that read would put
	// the row back online while publishing nothing, leaving every client
	// caching the bound agents as unavailable and the owner holding a notice
	// about a machine that is alive. TouchRuntimeHeartbeat reports the status
	// the row held immediately before its own write, under the lock, so the
	// two facts below cannot disagree with the row.
	touched, err := queries.TouchRuntimeHeartbeat(ctx, runtime.ID)
	if err != nil {
		return heartbeatTouchError(runtimeID, err)
	}
	if touched.PreviousStatus == "online" || touched.Status != "online" {
		// The steady state, and the unusable runtime that stays
		// offline-but-fresh: nothing came back, so nothing is announced and
		// nothing resolves.
		return nil
	}

	// The machine came back on this write. Resolve whatever the sweep raised
	// about it — closing by entity, so a notice a previous return left open
	// closes here too — and announce the availability flip its agents are
	// still caching.
	//
	// The announcement does not depend on the notice, the same way the sweep's
	// demotion does not (runtime_offline_notice.go). The row is already
	// online: every client is caching this machine's agents as unavailable
	// and only agent.status_changed clears that, so a second transaction that
	// fails must not swallow it. The close is best-effort — logged with the
	// runtime and its company, and left open for the next beat or the next
	// registration to close — while the frame always goes out. pending is
	// dropped with the failed transaction, so a close that did not commit is
	// never announced.
	var pending []events.Event
	if err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		pending = nil
		return closeRuntimeOfflineNoticesInTx(ctx, tx, &pending, runtime.CompanyID, runtime.ID)
	}); err != nil {
		slog.Error("heartbeat: runtime offline notices not resolved on the return",
			"runtime_id", runtime.ID.String(), "company_id", runtime.CompanyID.String(), "error", err)
		pending = nil
	}
	execution.PublishRuntimeUnblocked(
		ctx,
		s.bus,
		generated.Run{CompanyID: runtime.CompanyID, RuntimeID: runtime.ID},
	)
	s.publishAgentStatuses(ctx, runtime.CompanyID, runtime.ID)
	s.publishPending(ctx, pending)
	return nil
}

// heartbeatTouchError separates the one failure the daemon acts on from the
// ones it must not: no row means the runtime was removed before the touch
// could take its lock, which the daemon reads as gone and re-registers from.
func heartbeatTouchError(runtimeID uuid.UUID, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		// The read above saw the row before a removal committed; the touch
		// refuses removed rows, so the runtime is gone after all.
		return ErrRuntimeNotFound
	}
	slog.Warn("heartbeat touch failed", "runtime_id", runtimeID.String(), "error", err)
	return fmt.Errorf("heartbeat touch: %w", err)
}

// PruneOfflineRuntimes deletes offline runtimes that exceeded the retention
// TTL and are referenced by neither a binding nor retained Run history,
// notifying each affected company so clients re-fetch the roster.
func (s *DefaultRuntimeService) PruneOfflineRuntimes(ctx context.Context) error {
	deleted, err := s.store.Queries().DeleteStaleOfflineRuntimes(ctx, offlineRuntimeTTL.Seconds())
	if err != nil {
		return fmt.Errorf("delete stale offline runtimes: %w", err)
	}
	if len(deleted) == 0 {
		return nil
	}

	companies := make(map[uuid.UUID]struct{}, len(deleted))
	for _, row := range deleted {
		companies[row.CompanyID] = struct{}{}
	}
	slog.Info("runtime GC: deleted stale offline runtimes",
		"count", len(deleted), "companies", len(companies))
	for companyID := range companies {
		s.publishRoster(ctx, protocol.DaemonRosterActionRuntimeGC, companyID, uuid.Nil, "", nil)
	}
	return nil
}

// publishRoster emits the single roster-changed broadcast: clients re-fetch
// the runtime roster on any action.
func (s *DefaultRuntimeService) publishRoster(
	ctx context.Context,
	action string,
	companyID uuid.UUID,
	userID uuid.UUID,
	daemonID string,
	runtimeIDs []string,
) {
	s.bus.Publish(ctx, TopicDaemon, rosterEvent(action, companyID, userID, daemonID, runtimeIDs))
}

// rosterEvent is the one daemon.roster_changed frame shape, for the
// publishers that emit it directly and the participants that stage it in a
// caller's pending slice.
func rosterEvent(
	action string,
	companyID, userID uuid.UUID,
	daemonID string,
	runtimeIDs []string,
) events.Event {
	return events.Event{
		EntityType: TopicDaemon,
		Name:       EventDaemonRosterChanged,
		CompanyID:  companyID,
		CreatedAt:  time.Now().UTC(),
		Actor:      events.SystemActor(),
		Payload: DaemonRoster{
			UserID: userID,
			DaemonRegisterPayload: protocol.DaemonRegisterPayload{
				Action:     action,
				CompanyID:  companyID.String(),
				DaemonID:   daemonID,
				RuntimeIDs: runtimeIDs,
			},
		},
	}
}

// publishAgentStatuses publishes the agent.status frames for a runtime whose
// liveness just changed: one per binder, carrying every Agent that member
// has bound to the machine with its recomputed availability — the same
// predicate the Agent projection serves that member (not archived, bound,
// runtime online and usable, provider signed in), so the event can never
// disagree with the roster row. Every binding on a runtime belongs to its
// registrant, so in practice that is one frame. Reads the post-write row
// state, which is why every caller publishes after its write commits.
// Best-effort: a lookup or staging failure only skips the broadcast.
func (s *DefaultRuntimeService) publishAgentStatuses(
	ctx context.Context,
	companyID, runtimeID uuid.UUID,
) {
	rows, err := s.store.Queries().ListAgentStatusesForRuntime(ctx, runtimeID)
	if err != nil {
		slog.Warn("list agents for agent.status broadcast failed",
			"runtime_id", runtimeID.String(), "error", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	byBinder := make(map[uuid.UUID][]Availability)
	order := make([]uuid.UUID, 0, 1)
	for _, row := range rows {
		if _, seen := byBinder[row.UserID]; !seen {
			order = append(order, row.UserID)
		}
		byBinder[row.UserID] = append(byBinder[row.UserID], Availability{
			AgentID:          row.ID,
			RuntimeAvailable: row.RuntimeAvailable,
		})
	}
	for _, userID := range order {
		s.bus.Publish(ctx, Topic, availabilityChangeEvent(companyID, userID, byBinder[userID]))
	}
}

// availabilityChangeEvent is the one agent.status_changed frame shape every
// availability publisher uses: a system event on the agent topic carrying
// one member's recomputed statuses under the company and member that route
// it.
func availabilityChangeEvent(companyID, userID uuid.UUID, agents []Availability) events.Event {
	return events.Event{
		EntityType: Topic,
		Name:       EventAgentStatusChanged,
		CompanyID:  companyID,
		CreatedAt:  time.Now().UTC(),
		Actor:      events.SystemActor(),
		Payload: AvailabilityChange{
			Agents:    agents,
			CompanyID: companyID,
			UserID:    userID,
		},
	}
}

// bindingChangeEvent is the one agent.binding_changed frame shape every
// binding writer stages: a system event on the agent topic carrying the
// member's new binding of the Agent — nil once removed — under the company
// and member that route it.
func bindingChangeEvent(companyID, userID, agentID uuid.UUID, binding *BindingRef) events.Event {
	return events.Event{
		EntityType: Topic,
		Name:       EventAgentBindingChanged,
		CompanyID:  companyID,
		CreatedAt:  time.Now().UTC(),
		Actor:      events.UserActor(userID.String()),
		Payload: BindingChange{
			AgentID:   agentID,
			Binding:   binding,
			CompanyID: companyID,
			UserID:    userID,
		},
	}
}
