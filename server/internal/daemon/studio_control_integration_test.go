//go:build integration

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/company"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/daemonws"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/handler"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

// This lane uses two disposable PostgreSQL databases, production HTTP auth,
// Redis between two API hubs, the actual authenticated control WebSocket and
// the compiled P1c CLI process. Human admission never runs on the socket owner.
func TestStudioProductionControlChain(t *testing.T) {
	source, cli := os.Getenv("STUDIO_P1D_SOURCE"), os.Getenv("STUDIO_P1D_CLI")
	// testpostgres owns the disposable database and exposes it through the
	// shared Boardly test variable; allow the Studio fixture to consume that
	// same production-shaped database without a second service double.
	if os.Getenv("STUDIO_P1D_TEST_DATABASE_URL") == "" {
		if shared := os.Getenv(dbtest.SharedDatabaseURLEnv); shared != "" {
			_ = os.Setenv("STUDIO_P1D_TEST_DATABASE_URL", shared)
		}
	}
	if source == "" || cli == "" || os.Getenv("STUDIO_P1D_TEST_DATABASE_URL") == "" {
		t.Skip("requires isolated Studio source/database and compiled CLI")
	}
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	// The Studio fixture deliberately requires a separately named disposable
	// database so a Team schema mistake cannot masquerade as a source-auth pass.
	if shared := os.Getenv(dbtest.SharedDatabaseURLEnv); shared != "" {
		studioURL := replaceDatabaseName(shared, "studio_p1d_fixture")
		studioURL = strings.Replace(studioURL, "postgres://", "postgresql+asyncpg://", 1)
		studioURL = strings.Replace(studioURL, "postgresql://", "postgresql+asyncpg://", 1)
		adminURL := replaceDatabaseName(shared, "postgres")
		admin, err := pgx.Connect(ctx, adminURL)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = admin.Exec(ctx, `DROP DATABASE IF EXISTS studio_p1d_fixture`)
		if _, err = admin.Exec(ctx, `CREATE DATABASE studio_p1d_fixture`); err != nil {
			_ = admin.Close(ctx)
			t.Fatal(err)
		}
		_ = admin.Close(ctx)
		_ = os.Setenv("STUDIO_P1D_TEST_DATABASE_URL", studioURL)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	root := t.TempDir()
	script, err := filepath.Abs("testdata/studio_fixture.py")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(
		ctx,
		"uv",
		"run",
		"--no-sync",
		"--directory",
		source,
		"python",
		script,
		"--root",
		root,
		"--company",
		fixture.CompanyA.String(),
		"--agent",
		fixture.AgentA.String(),
	)
	var fixtureLog bytes.Buffer
	command.WaitDelay = 2 * time.Second
	command.Stdout = &fixtureLog
	command.Stderr = &fixtureLog
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if raw, e := os.ReadFile(filepath.Join(root, "ready.json")); e == nil {
			var process struct {
				PID int `json:"pid"`
			}
			if json.Unmarshal(raw, &process) == nil && process.PID > 0 {
				if owned, e := os.FindProcess(process.PID); e == nil {
					_ = owned.Kill()
				}
			}
		}
		_ = command.Process.Kill()
		_ = command.Wait()
		if t.Failed() {
			t.Log(fixtureLog.String())
		}
	})
	var studio struct {
		Origin    string    `json:"origin"`
		AccountID uuid.UUID `json:"accountId"`
	}
	ready := false
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		raw, e := os.ReadFile(filepath.Join(root, "ready.json"))
		if e == nil && json.Unmarshal(raw, &studio) == nil {
			ready = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		t.Fatal("Studio fixture did not become ready")
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	passwords, err := auth.NewPasswordHasher(auth.PasswordHasherOptions{})
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := auth.NewTokenManager(
		auth.TokenManagerOptions{
			Secret:     []byte("p1d-disposable-team-secret-at-least-32-bytes"),
			AccessTTL:  15 * time.Minute,
			RefreshTTL: time.Hour,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	authService, err := auth.NewAuthService(
		auth.ServiceOptions{
			Store:     store,
			Bus:       bus,
			Passwords: passwords,
			Tokens:    tokens,
			SessionInvalidator: auth.SessionInvalidatorFunc(
				func(context.Context, []auth.SessionRevocation) {},
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	companies, err := company.NewCompanyService(company.ServiceOptions{Store: store, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	issue := func() (uuid.UUID, string) {
		t.Helper()
		var sid uuid.UUID
		if e := pool.QueryRow(ctx, `INSERT INTO auth_sessions(user_id,token_prefix,token_hash,expires_at) VALUES($1,'fixture',gen_random_bytes(32),now()+interval '1 hour') RETURNING id`, fixture.UserA).
			Scan(&sid); e != nil {
			t.Fatal(e)
		}
		token, _, e := tokens.IssueAccess(fixture.UserA, sid)
		if e != nil {
			t.Fatal(e)
		}
		return sid, token
	}
	humanSession, humanToken := issue()
	_, daemonToken := issue()
	hub := daemonws.NewHub()
	redisContainer, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "redis:7-alpine", ExposedPorts: []string{"6379/tcp"},
			WaitingFor: wait.ForListeningPort("6379/tcp"),
		}, Started: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = redisContainer.Terminate(context.Background()) })
	redisAddress, err := redisContainer.Endpoint(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: redisAddress, ContextTimeoutEnabled: true, MaxRetries: -1})
	t.Cleanup(func() { _ = rdb.Close() })
	startRelay := func(h *daemonws.Hub) *daemonws.StudioRelay {
		t.Helper()
		r, e := daemonws.NewStudioRelay(ctx, h, rdb)
		if e != nil {
			t.Fatal(e)
		}
		workerCtx, stopWorker := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- r.Run(workerCtx) }()
		t.Cleanup(func() {
			stopWorker()
			if e := <-done; e != nil && !errors.Is(e, context.Canceled) {
				t.Error(e)
			}
			_ = r.Close()
		})
		return r
	}
	startRelay(hub)
	callerRelay := startRelay(daemonws.NewHub())
	namespace := uuid.New()
	editor := conversation.NewStudioEditorService(store, callerRelay, namespace)
	api := httptest.NewServer(
		handler.New(
			handler.Handler{
				AuthService:         authService,
				CompanyService:      companies,
				StudioEditorService: editor,
				DaemonWSHub:         hub,
				Queries:             store.Queries(),
				Bus:                 bus,
				RunService:          &execution.DefaultRunService{},
			},
		),
	)
	defer api.Close()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	tools := newLocalToolService(root)
	tools.studioOrigin = studio.Origin
	release, _ := findToolRelease("zenith-studio")
	tools.installed[release.ID()] = toolInstallation{Node: node, Entrypoint: cli}
	if tarball := os.Getenv("STUDIO_P1D_TARBALL"); tarball != "" {
		if err := verifyToolTarball(tarball, release.Integrity); err != nil {
			t.Fatal(err)
		}
		prefix := filepath.Join(root, "installed-cli")
		npm, err := exec.LookPath("npm")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := runToolProcess(
			ctx,
			npmToolProcess(
				node,
				npm,
				[]string{
					"install",
					"--global",
					"--prefix",
					prefix,
					"--no-audit",
					"--no-fund",
					"--ignore-scripts",
					tarball,
				},
				nil,
			),
		); err != nil {
			t.Fatal(err)
		}
		installed, err := inspectToolInstallation(ctx, prefix, node, release)
		if err != nil {
			t.Fatal(err)
		}
		tools.installed[release.ID()] = installed
	}
	client := NewClient(api.URL)
	client.SetToken(daemonToken)
	client.daemonID = fixture.DaemonA
	d := &Daemon{
		cfg:    Config{ServerBaseURL: api.URL, DaemonID: fixture.DaemonA, StudioAPIOrigin: studio.Origin},
		client: client,
		tools:  tools,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		runtimeIndex: map[string]Runtime{
			fixture.RuntimeA.String(): {
				ID:        fixture.RuntimeA.String(),
				CompanyID: fixture.CompanyA.String(),
			},
		},
		wsRPC: newWSRPCClient(wsRPCResponseGrace),
	}
	connect := func() func() {
		connection, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = d.runWakeupConnection(
				connection,
				[]string{fixture.RuntimeA.String()},
				make(chan runWakeup, 8),
				make(chan struct{}),
			)
		}()
		return func() { cancel(); <-done }
	}
	stop := connect()
	defer func() { stop() }()
	var stableRequestID uuid.UUID
	call := func(conv uuid.UUID, operation string, payload any, want int) json.RawMessage {
		t.Helper()
		body := map[string]any{
			"schemaVersion": 2,
			"requestId":     uuid.New(),
			"runtimeId":     fixture.RuntimeA,
			"operation":     operation,
			"payload":       payload,
		}
		if stableRequestID != uuid.Nil {
			body["requestId"] = stableRequestID
		}
		raw, _ := json.Marshal(body)
		request, e := http.NewRequestWithContext(
			ctx,
			http.MethodPost,
			api.URL+"/api/v1/companies/"+fixture.CompanyA.String()+"/conversations/"+conv.String()+"/studio/operations",
			bytes.NewReader(raw),
		)
		if e != nil {
			t.Fatal(e)
		}
		request.Header.Set("Authorization", "Bearer "+humanToken)
		request.Header.Set("Content-Type", "application/json")
		response, e := api.Client().Do(request)
		if e != nil {
			t.Fatal(e)
		}
		defer response.Body.Close()
		result, _ := io.ReadAll(response.Body)
		if response.StatusCode != want {
			t.Fatalf("%s HTTP %d, want %d: %s", operation, response.StatusCode, want, result)
		}
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		_ = json.Unmarshal(result, &envelope)
		return envelope.Data
	}
	// Let the authenticated upgrade finish without issuing an admitted dispatch.
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		if _, e := callerRelay.Connect(ctx, fixture.RuntimeA, fixture.AgentA); e == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	var contextResult conversation.StudioContextResponse
	if err = json.Unmarshal(
		call(fixture.DirectConversationA, "resolveContext", struct{}{}, 200),
		&contextResult,
	); err != nil {
		t.Fatal(err)
	}
	var binding conversation.StudioBindingResponse
	ensure := map[string]any{
		"title":                   "P1d isolated receipt",
		"description":             "",
		"expectedBindingId":       nil,
		"expectedBindingRevision": 0,
		"connectionGeneration":    contextResult.ConnectionGeneration,
	}
	if err = json.Unmarshal(
		call(fixture.DirectConversationA, "ensureBinding", ensure, 200),
		&binding,
	); err != nil {
		t.Fatal(err)
	}
	firstProject := binding.ProjectID
	if firstProject == nil {
		t.Fatal("no receipt project")
	}
	// Opening an existing canvas must carry the credential through the actual
	// server/daemon WebSocket and CLI pipe, then read Studio directly.
	if err = json.Unmarshal(
		call(fixture.DirectConversationA, "resolveContext", struct{}{}, 200),
		&contextResult,
	); err != nil {
		t.Fatal(err)
	}
	access := contextResult.StudioAccessToken
	if access == nil || access.AccessToken == "" || access.AccountID != studio.AccountID ||
		access.Origin != studio.Origin || !access.ExpiresAt.After(time.Now()) {
		t.Fatal("bound canvas did not receive a valid Studio credential")
	}
	bootstrapRequest, err := http.NewRequestWithContext(
		ctx, http.MethodGet, studio.Origin+"/v1/factory/projects/"+firstProject.String()+"/bootstrap", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	bootstrapRequest.Header.Set("Authorization", "Bearer "+access.AccessToken)
	bootstrapResponse, err := api.Client().Do(bootstrapRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer bootstrapResponse.Body.Close()
	if bootstrapResponse.StatusCode != http.StatusOK {
		t.Fatalf("direct Studio bootstrap status = %d, want 200", bootstrapResponse.StatusCode)
	}
	var bootstrap struct {
		Project struct{ ID uuid.UUID } `json:"project"`
	}
	if err := json.NewDecoder(bootstrapResponse.Body).Decode(&bootstrap); err != nil ||
		bootstrap.Project.ID != *firstProject {
		t.Fatalf(
			"direct Studio bootstrap project = %s, want %s (decode error: %v)",
			bootstrap.Project.ID,
			firstProject,
			err,
		)
	}
	if err = json.Unmarshal(
		call(fixture.DirectConversationA, "ensureBinding", ensure, 200),
		&binding,
	); err != nil ||
		*firstProject != *binding.ProjectID {
		t.Fatal("receipt replay changed binding", err)
	}
	connection, e := callerRelay.Connect(ctx, fixture.RuntimeA, fixture.AgentA)
	if e != nil {
		t.Fatal(e)
	}
	admitted := time.Now().UTC()
	dispatch := conversation.StudioEnsureDispatch{
		RequestID:            uuid.New(),
		DispatchID:           uuid.New(),
		ActorUserID:          fixture.UserA,
		ActorSessionID:       humanSession,
		CompanyID:            fixture.CompanyA,
		ConversationID:       fixture.DirectConversationA,
		RuntimeID:            fixture.RuntimeA,
		AuthorityType:        "human",
		ConnectionGeneration: *contextResult.ConnectionGeneration,
		Origin:               studio.Origin,
		StudioAccountID:      studio.AccountID,
		AuthorizedAt:         admitted,
		DispatchExpiresAt:    admitted.Add(5 * time.Second),
		Binding:              binding,
		Payload: conversation.StudioReceiptRequest{
			Namespace:     namespace,
			ReservationID: binding.BindingID,
			Title:         binding.Title,
			Description:   binding.Description,
		},
	}
	receipt, e := connection.Ensure(ctx, dispatch)
	if e != nil {
		t.Fatal("transport receipt replay", e)
	}
	duplicate, e := connection.Ensure(ctx, dispatch)
	if e != nil || duplicate != receipt {
		t.Fatal("dispatch replay", e)
	}
	dispatch.Payload.Title = "different"
	if _, e = connection.Ensure(ctx, dispatch); !errors.Is(e, conversation.ErrStudioEditorRequestConflict) {
		t.Fatalf("conflicting dispatch = %v", e)
	}
	dispatch.DispatchID = uuid.New()
	dispatch.AuthorizedAt = time.Now().UTC()
	dispatch.DispatchExpiresAt = dispatch.AuthorizedAt.Add(5 * time.Second)
	if _, e = connection.Ensure(ctx, dispatch); !errors.Is(e, conversation.ErrStudioEditorRequestConflict) {
		t.Fatalf("durable receipt payload conflict = %v", e)
	}
	dispatch.DispatchID = uuid.New()
	dispatch.AuthorizedAt = admitted.Add(-time.Minute)
	dispatch.DispatchExpiresAt = admitted.Add(-55 * time.Second)
	if _, e = connection.Ensure(ctx, dispatch); !errors.Is(e, conversation.ErrStudioEditorGenerationConflict) {
		t.Fatalf("expired dispatch = %v", e)
	}
}

func replaceDatabaseName(raw, name string) string {
	idx := strings.LastIndexByte(raw, '/')
	if idx < 0 {
		return raw
	}
	return raw[:idx+1] + name
}
