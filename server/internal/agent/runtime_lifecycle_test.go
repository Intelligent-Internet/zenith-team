package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

// lifecycleDBTX is a scriptable generated.DBTX: it records every statement
// and answers single-row reads through scan, multi-row reads through
// queryErr. It stands in for the transaction handle the InTx functions take
// so the unit lane can pin skip and failure semantics without PostgreSQL.
type lifecycleDBTX struct {
	statements []string
	scan       func(dest ...any) error
	queryErr   error
}

func (d *lifecycleDBTX) Exec(_ context.Context, statement string, _ ...any) (pgconn.CommandTag, error) {
	d.statements = append(d.statements, statement)
	return pgconn.CommandTag{}, nil
}

func (d *lifecycleDBTX) Query(_ context.Context, statement string, _ ...any) (pgx.Rows, error) {
	d.statements = append(d.statements, statement)
	return nil, d.queryErr
}

func (d *lifecycleDBTX) QueryRow(_ context.Context, statement string, _ ...any) pgx.Row {
	d.statements = append(d.statements, statement)
	return lifecycleRow{scan: d.scan}
}

type lifecycleRow struct{ scan func(dest ...any) error }

func (r lifecycleRow) Scan(dest ...any) error {
	if r.scan == nil {
		return pgx.ErrNoRows
	}
	return r.scan(dest...)
}

func TestDeregisterRuntimesInTxSkipsMalformedIdsWithoutTouchingTheDatabase(t *testing.T) {
	t.Parallel()
	database := &lifecycleDBTX{}

	offlined, err := deregisterRuntimesInTx(context.Background(), generated.New(database),
		auth.UserPrincipal{UserID: uuid.New()}, RuntimeDeregistration{RuntimeIDs: []string{"not-a-uuid", ""}})
	if err != nil {
		t.Fatalf("deregisterRuntimesInTx() error = %v, want malformed ids skipped", err)
	}
	if len(offlined) != 0 || len(database.statements) != 0 {
		t.Fatalf("offlined = %v, statements = %v, want nothing touched", offlined, database.statements)
	}
}

func TestDeregisterRuntimesInTxSkipsUnknownRuntimes(t *testing.T) {
	t.Parallel()
	database := &lifecycleDBTX{scan: func(...any) error { return pgx.ErrNoRows }}

	offlined, err := deregisterRuntimesInTx(context.Background(), generated.New(database),
		auth.UserPrincipal{UserID: uuid.New()}, RuntimeDeregistration{RuntimeIDs: []string{uuid.NewString()}})
	if err != nil {
		t.Fatalf("deregisterRuntimesInTx() error = %v, want an unknown id skipped", err)
	}
	if len(offlined) != 0 {
		t.Fatalf("offlined = %v, want none", offlined)
	}
	if len(database.statements) != 1 || !strings.Contains(database.statements[0], "FROM runtimes") {
		t.Fatalf("statements = %v, want exactly the runtime lookup", database.statements)
	}
}

// A runtime registered by another daemon is skipped like an unknown id, and
// the skip happens on the runtime row alone: the membership lookup never
// runs and no offline write is issued.
func TestDeregisterRuntimesInTxSkipsRuntimesOfAnotherDaemonBeforeMembership(t *testing.T) {
	t.Parallel()
	database := &lifecycleDBTX{scan: func(dest ...any) error {
		// getRuntime scans id, company_id, daemon_id first.
		*(dest[0].(*uuid.UUID)) = uuid.New()
		*(dest[1].(*uuid.UUID)) = uuid.New()
		*(dest[2].(*string)) = "daemon-a"
		return nil
	}}

	offlined, err := deregisterRuntimesInTx(context.Background(), generated.New(database),
		auth.UserPrincipal{UserID: uuid.New()},
		RuntimeDeregistration{DaemonID: "daemon-m", RuntimeIDs: []string{uuid.NewString()}})
	if err != nil {
		t.Fatalf("deregisterRuntimesInTx() error = %v, want another daemon's runtime skipped", err)
	}
	if len(offlined) != 0 {
		t.Fatalf("offlined = %v, want none", offlined)
	}
	if len(database.statements) != 1 || !strings.Contains(database.statements[0], "FROM runtimes") {
		t.Fatalf("statements = %v, want exactly the runtime lookup", database.statements)
	}
}

func TestDeregisterRuntimesInTxReturnsTheFirstDatabaseFailure(t *testing.T) {
	t.Parallel()
	failure := errors.New("connection reset")
	database := &lifecycleDBTX{scan: func(...any) error { return failure }}

	_, err := deregisterRuntimesInTx(context.Background(), generated.New(database),
		auth.UserPrincipal{UserID: uuid.New()},
		RuntimeDeregistration{RuntimeIDs: []string{uuid.NewString(), uuid.NewString()}})
	if !errors.Is(err, failure) || !strings.Contains(err.Error(), "deregister: load runtime") {
		t.Fatalf("deregisterRuntimesInTx() error = %v, want the wrapped load failure", err)
	}
	// The loop stops at the failing id so the caller can roll the whole
	// batch back: the second id is never looked up.
	if len(database.statements) != 1 {
		t.Fatalf("statements = %v, want the loop to stop after the first failure", database.statements)
	}
}

func TestAvailabilityChangeEventCarriesTheCompanyTheBinderAndSystemActor(t *testing.T) {
	t.Parallel()
	companyID, userID := uuid.New(), uuid.New()
	agents := []Availability{
		{AgentID: uuid.New(), RuntimeAvailable: false},
		{AgentID: uuid.New(), RuntimeAvailable: true},
	}

	event := availabilityChangeEvent(companyID, userID, agents)

	if event.EntityType != Topic || event.Name != EventAgentStatusChanged ||
		event.CompanyID != companyID || event.Actor != events.SystemActor() || event.CreatedAt.IsZero() {
		t.Fatalf("event envelope = %+v, want an agent.status_changed system event for the company", event)
	}
	payload, err := events.PayloadOf[AvailabilityChange](event)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.CompanyID != companyID || payload.UserID != userID || len(payload.Agents) != 2 ||
		payload.Agents[0] != agents[0] || payload.Agents[1] != agents[1] {
		t.Fatalf("payload = %+v, want the agents in the given order under the company and binder", payload)
	}
}

// The binder alone receives agent.binding_changed: the routing facts stay
// off the wire, and a removed binding travels as an explicit null.
func TestBindingChangeEventCarriesTheBinderAndAnExplicitNull(t *testing.T) {
	t.Parallel()
	companyID, userID, agentID := uuid.New(), uuid.New(), uuid.New()

	event := bindingChangeEvent(companyID, userID, agentID, nil)

	if event.EntityType != Topic || event.Name != EventAgentBindingChanged ||
		event.CompanyID != companyID || event.Actor != events.UserActor(userID.String()) {
		t.Fatalf("event envelope = %+v, want an agent.binding_changed event by the binder", event)
	}
	payload, err := events.PayloadOf[BindingChange](event)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.AgentID != agentID || payload.Binding != nil || payload.UserID != userID || payload.CompanyID != companyID {
		t.Fatalf("payload = %+v, want the agent with no binding under the company and binder", payload)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	if string(encoded) != `{"agentId":"`+agentID.String()+`","binding":null}` {
		t.Fatalf("wire payload = %s, want agentId and an explicit null binding only", encoded)
	}
}

func TestRevokeRuntimeOwnerInTxRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database := &lifecycleDBTX{}
	queries := generated.New(database)
	var pending []events.Event

	tests := []struct {
		name string
		call func() error
	}{
		{name: "nil handle", call: func() error {
			return RevokeRuntimeOwnerInTx(ctx, nil, &pending, uuid.New(), uuid.New())
		}},
		{name: "nil pending", call: func() error {
			return RevokeRuntimeOwnerInTx(ctx, queries, nil, uuid.New(), uuid.New())
		}},
		{name: "nil company", call: func() error {
			return RevokeRuntimeOwnerInTx(ctx, queries, &pending, uuid.Nil, uuid.New())
		}},
		{name: "nil owner", call: func() error {
			return RevokeRuntimeOwnerInTx(ctx, queries, &pending, uuid.New(), uuid.Nil)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("RevokeRuntimeOwnerInTx() error = %v, want ErrInvalidInput", err)
			}
		})
	}
	if len(database.statements) != 0 || len(pending) != 0 {
		t.Fatalf("statements = %v, pending = %v, want nothing touched", database.statements, pending)
	}
}

func TestRevokeRuntimeOwnerInTxWrapsAQueryFailure(t *testing.T) {
	t.Parallel()
	failure := errors.New("connection reset")
	database := &lifecycleDBTX{queryErr: failure}
	var pending []events.Event

	// The binding delete is the participant's only statement, so it is the
	// one a failing connection reaches; the wrap names it.
	err := RevokeRuntimeOwnerInTx(context.Background(), generated.New(database),
		&pending, uuid.New(), uuid.New())
	if !errors.Is(err, failure) || !strings.Contains(err.Error(), "delete departed member's agent bindings") {
		t.Fatalf("RevokeRuntimeOwnerInTx() error = %v, want the wrapped query failure", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending = %v, want nothing staged on failure", pending)
	}
}

// An announced unusable reason forces the stored status offline whatever
// the daemon said, and lands in the two typed columns.
func TestUpsertRuntimeParamsForcesOfflineAndStoresUnusableReason(t *testing.T) {
	t.Parallel()
	reason := &protocol.RuntimeUnusableReason{
		Code:    protocol.RuntimeUnusableCodeBelowMinimumVersion,
		Message: "Codex CLI 0.90.0 is below the minimum 0.100.0",
	}
	params := upsertRuntimeParams(
		RuntimeRegistration{
			CompanyID:   uuid.New(),
			OwnerUserID: uuid.New(),
			DaemonID:    "daemon-1",
			DeviceName:  "MacBook Pro",
		},
		RuntimeRegistrationSpec{Provider: "codex", Version: "0.90.0", Status: "online", UnusableReason: reason},
	)
	if params.Status != "offline" {
		t.Fatalf("Status = %q, want offline forced by the unusable reason", params.Status)
	}
	if !params.UnusableReasonCode.Valid || params.UnusableReasonCode.String != reason.Code ||
		!params.UnusableReasonMessage.Valid || params.UnusableReasonMessage.String != reason.Message {
		t.Fatalf(
			"unusable columns = %+v / %+v, want %+v",
			params.UnusableReasonCode,
			params.UnusableReasonMessage,
			reason,
		)
	}
	// The version still rides device_info and metadata: the roster shows
	// which version was found too old.
	if params.DeviceInfo != "MacBook Pro · 0.90.0" {
		t.Fatalf("DeviceInfo = %q", params.DeviceInfo)
	}
}

// The device name is a column SQL reads, never a metadata key; an empty name
// stores NULL.
func TestUpsertRuntimeParamsStoresTheDeviceNameInItsColumn(t *testing.T) {
	t.Parallel()
	registration := RuntimeRegistration{
		CompanyID: uuid.New(), OwnerUserID: uuid.New(), DaemonID: "daemon-1", DeviceName: "MacBook Pro",
	}
	named := upsertRuntimeParams(registration, RuntimeRegistrationSpec{Provider: "codex", Version: "0.101.0"})
	if !named.DeviceName.Valid || named.DeviceName.String != "MacBook Pro" ||
		strings.Contains(string(named.Metadata), "deviceName") {
		t.Fatalf("device name = %+v, metadata = %s", named.DeviceName, named.Metadata)
	}
	registration.DeviceName = ""
	if unnamed := upsertRuntimeParams(registration, RuntimeRegistrationSpec{Provider: "codex"}); unnamed.DeviceName.Valid {
		t.Fatalf("empty device name = %+v, want NULL", unnamed.DeviceName)
	}
}

// Without a reason both columns are NULL — the upsert replaces them
// wholesale, so this is what clears a stored reason — and the announced
// status is honoured.
func TestUpsertRuntimeParamsWithoutReasonClearsColumns(t *testing.T) {
	t.Parallel()
	registration := RuntimeRegistration{CompanyID: uuid.New(), OwnerUserID: uuid.New(), DaemonID: "daemon-1"}
	online := upsertRuntimeParams(registration, RuntimeRegistrationSpec{Provider: "codex", Version: "0.101.0"})
	if online.Status != "online" || online.UnusableReasonCode.Valid || online.UnusableReasonMessage.Valid {
		t.Fatalf("params = %+v, want online with NULL unusable columns", online)
	}
	offline := upsertRuntimeParams(registration, RuntimeRegistrationSpec{Provider: "codex", Status: "offline"})
	if offline.Status != "offline" || offline.UnusableReasonCode.Valid || offline.UnusableReasonMessage.Valid {
		t.Fatalf("params = %+v, want offline with NULL unusable columns", offline)
	}
}

// A sign-in verdict lands in the three typed columns with its check time;
// without one the columns stay NULL so the upsert keeps the row's last
// verdict.
func TestUpsertRuntimeParamsStoresTheSignInVerdict(t *testing.T) {
	t.Parallel()
	registration := RuntimeRegistration{CompanyID: uuid.New(), OwnerUserID: uuid.New(), DaemonID: "daemon-1"}
	signedOut := upsertRuntimeParams(registration, RuntimeRegistrationSpec{
		Provider: "codex", Version: "0.154.0",
		SignIn: &RuntimeSignIn{
			State:     protocol.RuntimeSignInSignedOut,
			Detail:    "Not signed in.",
			CheckedAt: time.Date(2026, 9, 16, 9, 0, 0, 0, time.UTC),
		},
	})
	if signedOut.Status != "online" {
		t.Fatalf("Status = %q, want a signed-out provider left online", signedOut.Status)
	}
	if !signedOut.ProviderSignInState.Valid || signedOut.ProviderSignInState.String != protocol.RuntimeSignInSignedOut ||
		!signedOut.ProviderSignInDetail.Valid ||
		signedOut.ProviderSignInDetail.String != "Not signed in." ||
		!signedOut.ProviderSignInCheckedAt.Valid ||
		!signedOut.ProviderSignInCheckedAt.Time.Equal(time.Date(2026, 9, 16, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf(
			"sign-in columns = %+v / %+v / %+v",
			signedOut.ProviderSignInState,
			signedOut.ProviderSignInDetail,
			signedOut.ProviderSignInCheckedAt,
		)
	}
	silent := upsertRuntimeParams(registration, RuntimeRegistrationSpec{Provider: "codex", Version: "0.154.0"})
	if silent.ProviderSignInState.Valid || silent.ProviderSignInDetail.Valid ||
		silent.ProviderSignInCheckedAt.Valid {
		t.Fatalf("params = %+v, want NULL sign-in columns when no verdict was announced", silent)
	}
}

// The served projection carries the verdict exactly as stored, or nil.
func TestRuntimeFromStoredRowServesTheSignInVerdict(t *testing.T) {
	t.Parallel()
	checkedAt := time.Date(2026, 9, 16, 9, 0, 0, 0, time.UTC)
	row := generated.Runtime{
		ID:                      uuid.New(),
		CompanyID:               uuid.New(),
		DaemonID:                "daemon-1",
		Name:                    "Codex",
		Provider:                "codex",
		Status:                  "online",
		ProviderSignInState:     util.StrToText(protocol.RuntimeSignInSignedIn),
		ProviderSignInDetail:    util.StrToText("Logged in using ChatGPT."),
		ProviderSignInCheckedAt: pgtype.Timestamptz{Time: checkedAt, Valid: true},
	}
	runtime := runtimeFromStoredRow(row)
	if runtime.SignIn == nil || runtime.SignIn.State != protocol.RuntimeSignInSignedIn ||
		runtime.SignIn.Detail != "Logged in using ChatGPT." || !runtime.SignIn.CheckedAt.Equal(checkedAt) {
		t.Fatalf("SignIn = %+v, want the stored verdict", runtime.SignIn)
	}
	row.ProviderSignInState = pgtype.Text{}
	if runtimeFromStoredRow(row).SignIn != nil {
		t.Fatal("a row without a verdict must serve signIn as null")
	}
}
