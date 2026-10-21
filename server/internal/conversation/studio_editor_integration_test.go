//go:build integration

package conversation

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

type editorTestRelay struct{ conn *editorTestConnection }

func (r editorTestRelay) Connect(_ context.Context, _, agentID uuid.UUID) (StudioEditorConnection, error) {
	r.conn.agentID = agentID
	return r.conn, nil
}

type editorTestConnection struct {
	principal   auth.UserPrincipal
	agentID     uuid.UUID
	probe       StudioConnectionResponse
	mu          sync.Mutex
	receipts    map[uuid.UUID]StudioReceiptResponse
	afterEnsure func()
	fail        bool
	calls       int
}

func (c *editorTestConnection) AgentID() uuid.UUID            { return c.agentID }
func (c *editorTestConnection) Principal() auth.UserPrincipal { return c.principal }
func (c *editorTestConnection) Probe(context.Context, uuid.UUID) (StudioConnectionResponse, error) {
	return c.probe, nil
}

func (c *editorTestConnection) Token(context.Context, uuid.UUID) (protocol.StudioAccessToken, error) {
	return protocol.StudioAccessToken{
		TokenType: "Bearer",
		AccountID: c.probe.StudioAccountID,
		Origin:    c.probe.Origin,
		ExpiresAt: time.Now().Add(time.Minute),
	}, nil
}
func (c *editorTestConnection) VerifyProject(context.Context, StudioEnsureDispatch) error { return nil }
func (c *editorTestConnection) Ensure(_ context.Context, d StudioEnsureDispatch) (StudioReceiptResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	r, ok := c.receipts[d.Payload.ReservationID]
	if !ok {
		h, _ := studioRequestHash(d.Payload)
		r = StudioReceiptResponse{
			Namespace:     d.Payload.Namespace,
			ReservationID: d.Payload.ReservationID,
			AccountID:     c.probe.StudioAccountID,
			ProjectID:     uuid.New(),
			RequestHash:   h,
			State:         "bound",
		}
		c.receipts[d.Payload.ReservationID] = r
	}
	if c.afterEnsure != nil {
		c.afterEnsure()
	}
	if c.fail {
		return StudioReceiptResponse{}, ErrStudioEditorOutcomeUnknown
	}
	return r, nil
}

func TestStudioEditorAuthorityRecoveryAndCAS(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	session := func(user uuid.UUID) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO auth_sessions(user_id,token_prefix,token_hash,expires_at) VALUES($1,'test',gen_random_bytes(32),now()+interval '1 hour') RETURNING id`, user).
			Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	human := auth.UserPrincipal{
		UserID:    f.UserA,
		SessionID: session(f.UserA),
		CompanyID: f.CompanyA,
		Role:      auth.CompanyRoleOwner,
		AuthKind:  auth.TokenKindBearer,
	}
	daemon := human
	daemon.SessionID = session(f.UserA)
	conn := &editorTestConnection{
		principal: daemon,
		probe: StudioConnectionResponse{
			ProbeID:              uuid.New(),
			RuntimeID:            f.RuntimeA,
			DaemonID:             f.DaemonA,
			ConnectionGeneration: uuid.New(),
			Origin:               "https://studio.example",
			StudioAccountID:      uuid.New(),
			VerifiedAt:           time.Now(),
			CreationProtocol:     "receiptV1",
			EditorSchemaVersion:  2,
		},
		receipts: map[uuid.UUID]StudioReceiptResponse{},
	}
	namespace := uuid.New()
	service := NewStudioEditorService(store, editorTestRelay{conn}, namespace)
	access := StudioEditorAccess{RuntimeID: f.RuntimeA, ConversationID: f.DirectConversationA}
	input := StudioEnsureInput{
		Access:      access,
		RequestID:   uuid.New(),
		Generation:  conn.probe.ConnectionGeneration,
		Title:       "Unicode 漢 <>&",
		Description: "receipt",
	}
	conn.fail = true
	if _, err := service.EnsureBinding(ctx, human, input); !errors.Is(err, ErrStudioEditorOutcomeUnknown) {
		t.Fatalf("lost reply: %v", err)
	}
	service = NewStudioEditorService(store, editorTestRelay{conn}, namespace)
	state, err := service.ResolveContext(ctx, human, access)
	if err != nil || state.Binding == nil || state.Binding.State != "reserved" {
		t.Fatalf("reserved: %+v %v", state, err)
	}
	// Wrong identity or intent never resets the reserved key.
	wrong := input
	wrong.Title = "other"
	if _, err := service.EnsureBinding(ctx, human, wrong); !errors.Is(err, ErrStudioEditorRequestConflict) {
		t.Fatalf("intent: %v", err)
	}
	origin := conn.probe.Origin
	conn.probe.Origin = "http://localhost:8000"
	if _, err := service.EnsureBinding(ctx, human, input); !errors.Is(err, ErrStudioEditorOriginConflict) {
		t.Fatalf("origin: %v", err)
	}
	conn.probe.Origin = origin
	account := conn.probe.StudioAccountID
	conn.probe.StudioAccountID = uuid.New()
	if _, err := service.EnsureBinding(ctx, human, input); !errors.Is(err, ErrStudioEditorAccountConflict) {
		t.Fatalf("account: %v", err)
	}
	conn.probe.StudioAccountID = account
	conn.fail = false
	// Revocation after the upstream commit leaves the reservation recoverable.
	conn.afterEnsure = func() {
		dbtest.MustExec(t, ctx, pool, `UPDATE auth_sessions SET revoked_at=now() WHERE id=$1`, human.SessionID)
	}
	if _, err := service.EnsureBinding(ctx, human, input); !errors.Is(err, ErrStudioEditorNotFound) {
		t.Fatalf("revoked completion: %v", err)
	}
	conn.afterEnsure = nil
	human.SessionID = session(f.UserA)
	if len(conn.receipts) != 1 {
		t.Fatalf("receipts: %d", len(conn.receipts))
	}
	state, err = service.ResolveContext(ctx, human, access)
	if err != nil {
		t.Fatal(err)
	}
	wrong = input
	wrong.ExpectedBindingID = &state.Binding.BindingID
	wrong.ExpectedBindingRevision = state.Binding.BindingRevision + 1
	if _, err := service.EnsureBinding(ctx, human, wrong); !errors.Is(err, ErrStudioEditorBindingConflict) {
		t.Fatalf("CAS: %v", err)
	}
	// Another public registrant, hidden Conversation, revoked daemon and removed
	// Runtime all fail before the relay can create anything.
	other := human
	other.UserID = f.UserB
	other.SessionID = session(f.UserB)
	if _, err := service.ResolveContext(ctx, other, access); !errors.Is(err, ErrStudioEditorNotFound) {
		t.Fatalf("other registrant: %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE auth_sessions SET revoked_at=now() WHERE id=$1`, daemon.SessionID)
	if _, err := service.ResolveContext(ctx, human, access); !errors.Is(err, ErrStudioEditorRuntimeUnavailable) {
		t.Fatalf("daemon revoke: %v", err)
	}
	conn.principal.SessionID = session(f.UserA)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE company_members SET status='revoked' WHERE company_id=$1 AND user_id=$2`,
		f.CompanyA,
		human.UserID,
	)
	if _, e := service.EnsureBinding(ctx, human, input); !errors.Is(e, ErrStudioEditorNotFound) {
		t.Fatalf("membership revocation = %v", e)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE company_members SET status='active' WHERE company_id=$1 AND user_id=$2`,
		f.CompanyA,
		human.UserID,
	)
	dbtest.MustExec(t, ctx, pool, `UPDATE users SET disabled_at=now() WHERE id=$1`, human.UserID)
	if _, e := service.ResolveContext(ctx, human, access); !errors.Is(e, ErrStudioEditorNotFound) {
		t.Fatalf("disabled user = %v", e)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE users SET disabled_at=NULL WHERE id=$1`, human.UserID)
	dbtest.MustExec(t, ctx, pool, `UPDATE runtimes SET removed_at=now() WHERE id=$1`, f.RuntimeA)
	if _, err := service.ResolveContext(ctx, human, access); !errors.Is(err, ErrStudioEditorNotFound) {
		t.Fatalf("runtime removal: %v", err)
	}
}

func TestStudioEditorProfileComesFromAuthorizedConversation(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	var sessionID uuid.UUID
	if err = pool.QueryRow(ctx, `INSERT INTO auth_sessions(user_id,token_prefix,token_hash,expires_at) VALUES($1,'scope',gen_random_bytes(32),now()+interval '1 hour') RETURNING id`, f.UserA).
		Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	principal := auth.UserPrincipal{
		UserID:    f.UserA,
		CompanyID: f.CompanyA,
		SessionID: sessionID,
		Role:      auth.CompanyRoleOwner,
	}
	conn := &editorTestConnection{
		principal: principal,
		probe: StudioConnectionResponse{
			ProbeID:              uuid.New(),
			RuntimeID:            f.RuntimeA,
			DaemonID:             f.DaemonA,
			ConnectionGeneration: uuid.New(),
			Origin:               "https://studio.example",
			StudioAccountID:      uuid.New(),
			VerifiedAt:           time.Now(),
			CreationProtocol:     "receiptV1",
			EditorSchemaVersion:  2,
		},
	}
	service := NewStudioEditorService(store, editorTestRelay{conn}, uuid.New())
	var peer, peerConversation uuid.UUID
	if err = pool.QueryRow(ctx, `INSERT INTO agents(company_id,name,title,model,provider,created_by_user_id,reports_to_agent_id) VALUES($1,'Studio peer','Artist','test','codex',$2,$3) RETURNING id`, f.CompanyA, f.UserA, f.AgentA).
		Scan(&peer); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO agent_runtime_bindings(company_id,agent_id,user_id,runtime_id,provider) VALUES($1,$2,$3,$4,'codex')`,
		f.CompanyA,
		peer,
		f.UserA,
		f.RuntimeA,
	)
	if err = pool.QueryRow(ctx, `INSERT INTO conversations(company_id,kind,agent_id,created_by_user_id,title) VALUES($1,'direct',$2,$3,'Studio peer') RETURNING id`, f.CompanyA, peer, f.UserA).
		Scan(&peerConversation); err != nil {
		t.Fatal(err)
	}
	peerContext, peerErr := service.ResolveContext(
		ctx,
		principal,
		StudioEditorAccess{ConversationID: peerConversation, RuntimeID: f.RuntimeA},
	)
	if peerErr != nil || peerContext.AgentID != peer || conn.AgentID() != peer {
		t.Fatalf("peer profile=%+v, %v", peerContext, peerErr)
	}
	for _, id := range []uuid.UUID{f.DirectConversationA, f.ConversationA} {
		result, err := service.ResolveContext(
			ctx,
			principal,
			StudioEditorAccess{ConversationID: id, RuntimeID: f.RuntimeA},
		)
		if err != nil || result.AgentID != f.AgentA || conn.AgentID() != f.AgentA {
			t.Fatalf("profile=%+v, %v", result, err)
		}
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`DELETE FROM agent_runtime_bindings WHERE company_id=$1 AND agent_id=$2 AND user_id=$3`,
		f.CompanyA,
		f.AgentA,
		f.UserA,
	)
	if _, err = service.ResolveContext(
		ctx,
		principal,
		StudioEditorAccess{ConversationID: f.DirectConversationA, RuntimeID: f.RuntimeA},
	); !errors.Is(
		err,
		ErrStudioEditorNotFound,
	) {
		t.Fatalf("removed binding error=%v", err)
	}
}
