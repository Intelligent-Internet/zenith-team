package agent

// The one thing Zenith Team says out loud about a machine: when the stale sweep
// takes a runtime offline while it still held in-flight runs, the member who
// registered the machine gets an Inbox item naming it and the count. Nobody
// else can see the machine (ADR 0014), and a runtime whose registrant was
// deleted raises nothing. The runs themselves
// fail a moment later, in the sweeper's next transaction
// (execution.FailRunsForOfflineRuntimes); this file only decides who is told
// and in what words, and stages the items in the demotion's own transaction
// so the notice and the offline state commit together when the notice can be
// raised at all. It is a courtesy, not a precondition: liveness is what the
// sweep exists for, so an item that cannot be written is logged and dropped
// rather than taking the whole fleet's demotion down with it.

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/inbox"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

// runtimeOfflineRecipientKey addresses one person in one company — the pair
// the Inbox locks and the pair a notice is written to.
type runtimeOfflineRecipientKey struct {
	companyID uuid.UUID
	userID    uuid.UUID
}

// runtimeOfflineRecipients is the notice's audience: the machine's
// registrant alone (ADR 0014) — a machine's name and liveness are theirs,
// and nobody else can restart it. A runtime whose owner was deleted
// (owner_user_id went NULL) notifies nobody. Always non-nil so callers
// range over it without a check.
func runtimeOfflineRecipients(owner uuid.NullUUID) []uuid.UUID {
	if !owner.Valid || owner.UUID == uuid.Nil {
		return []uuid.UUID{}
	}
	return []uuid.UUID{owner.UUID}
}

// runtimeOfflineDisplayName names the machine the way every other surface
// does: the name a person gave it, falling back to the daemon's registration
// name.
func runtimeOfflineDisplayName(row generated.Runtime) string {
	if custom := strings.TrimSpace(row.CustomName.String); row.CustomName.Valid && custom != "" {
		return custom
	}
	return row.Name
}

// Keep the outage visible even when the machine has a long display name.
func runtimeOfflineTitle(name string) string {
	return strings.TrimSpace(runtimeOfflineFit(name, 147)) + " went offline"
}

func runtimeOfflineBody(inFlight int64) string {
	noun, verb := "runs", "were"
	if inFlight == 1 {
		noun, verb = "run", "was"
	}
	// Auto-retry has an attempt limit, so an outage cannot promise every Run
	// will restart. The user can reconnect and check the affected work.
	return fmt.Sprintf(
		"%d active %s %s interrupted. Reconnect this computer and check the affected work.",
		inFlight,
		noun,
		verb,
	)
}

// runtimeOfflineFit caps a name at limit runes, marking the cut so a
// truncated name never reads as the whole one.
func runtimeOfflineFit(name string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(name) <= limit {
		return name
	}
	kept := []rune(name)[:limit-1]
	return strings.TrimRight(string(kept), " ") + "…"
}

// runtimeOfflineDedupeKey names ONE offline episode. Resolving an item
// archives it without deleting it and the UNIQUE (company, recipient,
// dedupe_key) survives the archive, so a key naming only the runtime would
// notify a flapping machine exactly once, ever. last_seen_at is the episode's
// identity: it does not move while the machine is silent, and the heartbeat
// that ends the episode is what moves it. A runtime that never beat borrows
// the sweep's own clock.
func runtimeOfflineDedupeKey(
	runtimeID uuid.UUID,
	lastSeenAt pgtype.Timestamptz,
	sweptAt time.Time,
) string {
	episode := sweptAt
	if lastSeenAt.Valid {
		episode = lastSeenAt.Time
	}
	return fmt.Sprintf("runtime:%s:offline:%d", runtimeID, episode.UTC().Unix())
}

// runtimeOfflineCreateInput is the whole item, addressed to one recipient.
// The entity is the runtime, which is what lets the heartbeat and the
// re-registration close it without knowing anything about how it was raised.
func runtimeOfflineCreateInput(
	row generated.Runtime,
	recipient uuid.UUID,
	inFlight int64,
	sweptAt time.Time,
) inbox.CreateInput {
	name := runtimeOfflineDisplayName(row)
	deepLink := "/settings/agents/" + row.ID.String()
	return inbox.CreateInput{
		CompanyID:       row.CompanyID,
		RecipientUserID: recipient,
		Type:            inbox.TypeRuntimeOffline,
		Entity:          inbox.Entity{Type: protocol.EntityRuntime, ID: row.ID.String()},
		Title:           runtimeOfflineTitle(name),
		Body:            runtimeOfflineBody(inFlight),
		DeepLink:        &deepLink,
		DedupeKey:       runtimeOfflineDedupeKey(row.ID, row.LastSeenAt, sweptAt),
		Actor:           inbox.Actor{Kind: "system"},
	}
}

// lockRuntimeOfflineRecipients takes the Company then membership locks for
// every recipient the sweep is about to write to, in one sorted pass BEFORE
// the runtime and Inbox writes. That is the canonical ladder
// (company/member → runtime → inbox), and keeping it is what stops the sweep
// deadlocking against a run's own Inbox targets. A recipient whose
// membership is gone or no longer active reports false and is skipped
// silently: a revoked machine owner is not a sweep failure.
func lockRuntimeOfflineRecipients(
	ctx context.Context,
	tx *generated.Queries,
	targets []runtimeOfflineRecipientKey,
) (map[runtimeOfflineRecipientKey]bool, error) {
	ordered := make([]runtimeOfflineRecipientKey, 0, len(targets))
	eligible := make(map[runtimeOfflineRecipientKey]bool, len(targets))
	for _, target := range targets {
		if _, seen := eligible[target]; seen {
			continue
		}
		eligible[target] = false
		ordered = append(ordered, target)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].companyID != ordered[j].companyID {
			return ordered[i].companyID.String() < ordered[j].companyID.String()
		}
		return ordered[i].userID.String() < ordered[j].userID.String()
	})
	for _, target := range ordered {
		admitted, err := inbox.LockRecipientInTx(ctx, tx, target.companyID, target.userID)
		if err != nil {
			return nil, fmt.Errorf("lock runtime offline recipient: %w", err)
		}
		eligible[target] = admitted
	}
	return eligible, nil
}

// stageRuntimeOfflineNotices writes one item per (runtime, eligible
// recipient) on the caller's handle and stages their realtime frames. The
// caller publishes pending after the transaction commits and presents the
// returned results.
//
// It returns no error on purpose. One unwritable item — a row whose stored
// name cannot be phrased into a valid title, say — must not be able to hold
// the whole fleet online: the demotion this notice describes is already
// written on the same handle, and returning here would roll it back, so every
// stale runtime would keep its agents available and its dead runs in flight
// while the sweeper logged the same failure every 30 seconds. So a failure is
// logged with the runtime and the recipient it was for, and the next
// recipient and the next runtime are tried. This is not a way to hide a
// database failure: a driver error poisons the transaction and its commit
// still fails, so nothing half-commits. What it recovers from is the item
// Zenith Team could not build.
func stageRuntimeOfflineNotices(
	ctx context.Context,
	tx *generated.Queries,
	pending *[]events.Event,
	rows []generated.Runtime,
	inFlight map[uuid.UUID]int64,
	recipients map[uuid.UUID][]uuid.UUID,
	eligible map[runtimeOfflineRecipientKey]bool,
	sweptAt time.Time,
) []inbox.CreateResult {
	var created []inbox.CreateResult
	for _, row := range rows {
		count := inFlight[row.ID]
		if count == 0 {
			continue
		}
		for _, recipient := range recipients[row.ID] {
			if !eligible[runtimeOfflineRecipientKey{companyID: row.CompanyID, userID: recipient}] {
				continue
			}
			result, err := inbox.CreateInTx(ctx, tx, pending,
				runtimeOfflineCreateInput(row, recipient, count, sweptAt))
			if err != nil {
				slog.Error("runtime sweep: runtime offline notice not raised",
					"company_id", row.CompanyID, "runtime_id", row.ID,
					"recipient_user_id", recipient, "error", err)
				continue
			}
			if result.Created {
				created = append(created, result)
			}
		}
	}
	return created
}

// closeRuntimeOfflineNoticesInTx resolves whatever is open about one runtime.
// It is called on both ways back — the heartbeat that finds the runtime
// offline, and the registration a restarted daemon sends before its first
// beat — and is idempotent: with nothing open it writes nothing and stages
// nothing.
func closeRuntimeOfflineNoticesInTx(
	ctx context.Context,
	tx *generated.Queries,
	pending *[]events.Event,
	companyID, runtimeID uuid.UUID,
) error {
	return inbox.CloseEntityInTx(
		ctx,
		tx,
		pending,
		companyID,
		inbox.Entity{Type: protocol.EntityRuntime, ID: runtimeID.String()},
		inbox.Actor{Kind: "system"},
	)
}
