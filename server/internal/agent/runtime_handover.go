package agent

// One computer, one person at a time (D33). A runtime is one person's use of
// one machine, so a second person signing in on a computer registers rows of
// their own, and nothing of anyone else's moves: their bindings, queued Runs,
// machine name and discovery stay on their rows. What changes hands is the
// machine itself. The registration takes every other person's live rows on
// it offline and marks them handed over, and tells each of them once — an
// Inbox notice naming who signed in, and an Activity row only they read,
// whose actor is that person, not them. Their own next registration on the
// machine is their return: the upsert clears the mark and the notice
// closes.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/activity"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/inbox"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

// RuntimeHandover is the runtime.handed_over Activity payload: the previous
// person's runtimes on the computer, all of which went offline. Who signed in
// is the row's actor.
type RuntimeHandover struct {
	DaemonID   string      `json:"daemonId"`
	RuntimeIDs []uuid.UUID `json:"runtimeIds"`
}

// machineHandover is what a registration took from other people: their rows
// on the machine, grouped per person, and the Inbox items it raised.
type machineHandover struct {
	runtimes []generated.Runtime
	owners   []uuid.UUID
	created  []inbox.CreateResult
}

// handOverMachineInTx runs before the registrant's own upsert. The other
// people's Company then membership locks come first, in owner order, then
// their rows: the canonical ladder (company/member, runtime, Inbox) the
// offline sweep keeps too. The common registration — the same person
// restarting their daemon — finds nobody else and takes no lock.
//
// Telling them is a courtesy, as the offline notice is: a notice or receipt
// that cannot be built is logged and skipped rather than refusing the person
// signing in. A database failure still poisons the transaction, so the
// registration fails with it instead of half-committing.
func handOverMachineInTx(
	ctx context.Context,
	tx *generated.Queries,
	pending *[]events.Event,
	registration RuntimeRegistration,
) (machineHandover, error) {
	owners, err := tx.ListMachineRuntimeOwners(ctx, generated.ListMachineRuntimeOwnersParams{
		CompanyID:        registration.CompanyID,
		DaemonID:         registration.DaemonID,
		RegistrantUserID: registration.OwnerUserID,
	})
	if err != nil {
		return machineHandover{}, fmt.Errorf("list the machine's other people: %w", err)
	}
	if len(owners) == 0 {
		return machineHandover{}, nil
	}
	eligible := make(map[uuid.UUID]bool, len(owners))
	for _, owner := range owners {
		admitted, err := inbox.LockRecipientInTx(ctx, tx, registration.CompanyID, owner)
		if err != nil {
			return machineHandover{}, fmt.Errorf("lock the machine's previous person: %w", err)
		}
		eligible[owner] = admitted
	}
	rows, err := tx.HandOverMachineRuntimes(ctx, generated.HandOverMachineRuntimesParams{
		CompanyID:        registration.CompanyID,
		DaemonID:         registration.DaemonID,
		RegistrantUserID: registration.OwnerUserID,
	})
	if err != nil {
		return machineHandover{}, fmt.Errorf("hand the machine over: %w", err)
	}
	handover := machineHandover{runtimes: rows}
	if len(rows) == 0 {
		return handover, nil
	}

	byOwner := make(map[uuid.UUID][]generated.Runtime, len(owners))
	for _, row := range rows {
		owner := row.OwnerUserID.UUID
		if _, seen := byOwner[owner]; !seen {
			handover.owners = append(handover.owners, owner)
		}
		byOwner[owner] = append(byOwner[owner], row)
	}
	sort.Slice(handover.owners, func(i, j int) bool {
		return handover.owners[i].String() < handover.owners[j].String()
	})
	registrantName, err := registrantDisplayName(ctx, tx, registration.OwnerUserID)
	if err != nil {
		return machineHandover{}, err
	}
	for _, owner := range handover.owners {
		// Not admitted: a former member, or someone whose row appeared
		// between the two reads and whose locks this transaction does not
		// hold. Their rows are handed over all the same.
		if !eligible[owner] {
			continue
		}
		theirs := byOwner[owner]
		sort.Slice(theirs, func(i, j int) bool {
			if theirs[i].Provider != theirs[j].Provider {
				return theirs[i].Provider < theirs[j].Provider
			}
			return theirs[i].ID.String() < theirs[j].ID.String()
		})
		created := tellPreviousPersonInTx(ctx, tx, pending, registration, registrantName, owner, theirs)
		if created.Created {
			handover.created = append(handover.created, created)
		}
	}
	return handover, nil
}

// tellPreviousPersonInTx raises the notice and records the receipt for one
// person whose rows the registration handed over. Both name the same runtime
// of theirs, so their return closes the notice by entity.
func tellPreviousPersonInTx(
	ctx context.Context,
	tx *generated.Queries,
	pending *[]events.Event,
	registration RuntimeRegistration,
	registrantName string,
	owner uuid.UUID,
	theirs []generated.Runtime,
) inbox.CreateResult {
	subject := theirs[0]
	computer := runtimeComputerName(theirs)
	registrant := registration.OwnerUserID
	deepLink := "/settings/agents/" + subject.ID.String()
	created, err := inbox.CreateInTx(ctx, tx, pending, inbox.CreateInput{
		CompanyID:       registration.CompanyID,
		RecipientUserID: owner,
		Type:            inbox.TypeRuntimeOffline,
		Entity:          inbox.Entity{Type: protocol.EntityRuntime, ID: subject.ID.String()},
		Title: runtimeOfflineFit(registrantName, 60) + " is using " +
			strings.TrimSpace(runtimeOfflineFit(computer, 80)),
		Body:      "Your agents on this computer wait until you sign in on it again.",
		DeepLink:  &deepLink,
		DedupeKey: fmt.Sprintf("runtime:%s:handed_over:%d", subject.ID, subject.HandedOverAt.Time.UTC().Unix()),
		Actor:     inbox.Actor{Kind: "user", ID: &registrant},
	})
	if err != nil {
		slog.Error("runtime registration: handover notice not raised",
			"company_id", registration.CompanyID, "runtime_id", subject.ID,
			"recipient_user_id", owner, "error", err)
		created = inbox.CreateResult{}
	}
	runtimeIDs := make([]uuid.UUID, 0, len(theirs))
	for _, row := range theirs {
		runtimeIDs = append(runtimeIDs, row.ID)
	}
	if err := activity.RecordEvidenceInTx(ctx, tx, pending, activity.EvidenceInput{
		CompanyID:       registration.CompanyID,
		EntityID:        subject.ID,
		Action:          "runtime.handed_over",
		Actor:           events.UserActor(registrant.String()),
		RecipientUserID: owner,
		Payload:         RuntimeHandover{DaemonID: registration.DaemonID, RuntimeIDs: runtimeIDs},
		EntityTitle:     computer,
	}); err != nil {
		slog.Error("runtime registration: handover receipt not recorded",
			"company_id", registration.CompanyID, "runtime_id", subject.ID,
			"recipient_user_id", owner, "error", err)
	}
	return created
}

// runtimeComputerName names the computer the way the person reads it: the
// name they gave it, else the name the device announced, else the
// registration name of the first row.
func runtimeComputerName(rows []generated.Runtime) string {
	for _, row := range rows {
		if custom := strings.TrimSpace(row.CustomName.String); row.CustomName.Valid && custom != "" {
			return custom
		}
	}
	for _, row := range rows {
		if device := strings.TrimSpace(row.DeviceName.String); row.DeviceName.Valid && device != "" {
			return device
		}
	}
	return rows[0].Name
}

// registrantDisplayName is the name the previous person reads in the notice.
func registrantDisplayName(ctx context.Context, tx *generated.Queries, userID uuid.UUID) (string, error) {
	user, err := tx.GetUserByID(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "Someone else", nil
	}
	if err != nil {
		return "", fmt.Errorf("read the person signing in: %w", err)
	}
	if name := strings.TrimSpace(user.DisplayName); name != "" {
		return name, nil
	}
	return "Someone else", nil
}

// closeReturnNoticesInTx is the registrant's return: whatever notice a
// handover raised about their rows on the machine closes, whether or not
// each row comes back online (an unusable provider stays offline, yet the
// person is back).
func closeReturnNoticesInTx(
	ctx context.Context,
	tx *generated.Queries,
	pending *[]events.Event,
	companyID uuid.UUID,
	returning []uuid.UUID,
) error {
	for _, runtimeID := range returning {
		if err := closeRuntimeOfflineNoticesInTx(ctx, tx, pending, companyID, runtimeID); err != nil {
			return fmt.Errorf("resolve handover notices: %w", err)
		}
	}
	return nil
}
