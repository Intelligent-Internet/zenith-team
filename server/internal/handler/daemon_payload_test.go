package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/agent"
	"github.com/Intelligent-Internet/zenith-team/server/internal/attachment"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/daemonws"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	db "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

type daemonRegisterDBTX struct {
	statements []string
}

func (d *daemonRegisterDBTX) Exec(
	_ context.Context,
	statement string,
	_ ...any,
) (pgconn.CommandTag, error) {
	d.statements = append(d.statements, statement)
	return pgconn.CommandTag{}, nil
}

func (d *daemonRegisterDBTX) Query(
	_ context.Context,
	statement string,
	_ ...any,
) (pgx.Rows, error) {
	d.statements = append(d.statements, statement)
	return nil, nil
}

func (d *daemonRegisterDBTX) QueryRow(
	_ context.Context,
	statement string,
	_ ...any,
) pgx.Row {
	d.statements = append(d.statements, statement)
	return daemonRegisterRow{}
}

type daemonRegisterRow struct{}

func (daemonRegisterRow) Scan(...any) error { return pgx.ErrNoRows }

func TestDaemonRegisterRejectsUnsupportedProviderBeforeDatabaseAccess(t *testing.T) {
	t.Parallel()

	companyID := uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f275a")
	principal := auth.UserPrincipal{
		UserID:    uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f275b"),
		SessionID: uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f275c"),
		AuthKind:  auth.TokenKindBearer,
	}
	tests := []struct {
		name      string
		providers []string
	}{
		{name: "case variant", providers: []string{strings.ToUpper(protocol.SupportedProviders[0])}},
		{name: "surrounding whitespace", providers: []string{" " + protocol.SupportedProviders[0] + " "}},
		{name: "unsupported value", providers: []string{"unsupported"}},
		{
			name:      "unsupported value after supported value",
			providers: []string{protocol.SupportedProviders[0], "unsupported"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			database := &daemonRegisterDBTX{}
			runtimes := make([]daemonRegisterRuntime, 0, len(test.providers))
			for _, provider := range test.providers {
				runtimes = append(runtimes, daemonRegisterRuntime{Provider: provider})
			}

			body, err := json.Marshal(daemonRegisterRequest{
				CompanyID: companyID.String(),
				DaemonID:  "daemon-1",
				Runtimes:  runtimes,
			})
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}

			router := gin.New()
			router.POST("/register", func(c *gin.Context) {
				c.Request = c.Request.WithContext(
					auth.WithUserPrincipal(c.Request.Context(), principal),
				)
				(&Handler{Queries: db.New(database)}).daemonRegister(c)
			})
			request := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(string(body)))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()

			router.ServeHTTP(response, request)

			if response.Code != http.StatusUnprocessableEntity {
				t.Fatalf(
					"status = %d, want %d; body = %s",
					response.Code,
					http.StatusUnprocessableEntity,
					response.Body.String(),
				)
			}
			var payload map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if payload["code"] != "validation_failed" {
				t.Fatalf("code = %v, want validation_failed", payload["code"])
			}
			if len(database.statements) != 0 {
				t.Fatalf("database statements = %d, want 0", len(database.statements))
			}
		})
	}
}

func TestDaemonRegisterAcceptsSupportedProviderVocabularyAtBoundary(t *testing.T) {
	t.Parallel()

	companyID := uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f275a")
	principal := auth.UserPrincipal{
		UserID:    uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f275b"),
		SessionID: uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f275c"),
		AuthKind:  auth.TokenKindBearer,
	}
	for _, provider := range protocol.SupportedProviders {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			database := &daemonRegisterDBTX{}
			// The second entry carries a valid unusable reason: the
			// vocabulary check admits it and the request reaches the
			// membership lookup like any other.
			body, err := json.Marshal(daemonRegisterRequest{
				CompanyID: companyID.String(),
				DaemonID:  "daemon-1",
				Runtimes: []daemonRegisterRuntime{
					{Provider: provider},
					{
						Provider: provider,
						Status:   "offline",
						UnusableReason: &daemonRuntimeUnusableReason{
							Code:    protocol.RuntimeUnusableCodeBelowMinimumVersion,
							Message: "Codex CLI 0.90.0 is below the minimum 0.100.0",
						},
					},
				},
			})
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}

			router := gin.New()
			router.POST("/register", func(c *gin.Context) {
				c.Request = c.Request.WithContext(
					auth.WithUserPrincipal(c.Request.Context(), principal),
				)
				(&Handler{Queries: db.New(database)}).daemonRegister(c)
			})
			request := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(string(body)))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()

			router.ServeHTTP(response, request)

			if response.Code != http.StatusNotFound {
				t.Fatalf(
					"status = %d, want %d; body = %s",
					response.Code,
					http.StatusNotFound,
					response.Body.String(),
				)
			}
			if len(database.statements) != 1 {
				t.Fatalf("database statements = %d, want 1 membership lookup", len(database.statements))
			}
		})
	}
}

// daemonRegisterBoundaryStatus posts one register body through the handler
// with a principal set and answers the status, decoded error code, and how
// many statements reached the database.
func daemonRegisterBoundaryStatus(t *testing.T, body []byte) (int, string, string, int) {
	t.Helper()
	principal := auth.UserPrincipal{
		UserID:    uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f275b"),
		SessionID: uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f275c"),
		AuthKind:  auth.TokenKindBearer,
	}
	database := &daemonRegisterDBTX{}
	router := gin.New()
	router.POST("/register", func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithUserPrincipal(c.Request.Context(), principal))
		(&Handler{Queries: db.New(database)}).daemonRegister(c)
	})
	request := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	var payload struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response %s: %v", response.Body.String(), err)
	}
	return response.Code, payload.Code, payload.Detail, len(database.statements)
}

// The unusable-reason code is a closed vocabulary checked exactly like the
// provider, before any database access, and the rejection names the entry.
func TestDaemonRegisterRejectsUnknownUnusableReasonCodeBeforeDatabaseAccess(t *testing.T) {
	t.Parallel()
	for _, code := range []string{"broken", "", "BELOW_MINIMUM_VERSION", " not_executable"} {
		t.Run("code "+strconv.Quote(code), func(t *testing.T) {
			t.Parallel()
			body, err := json.Marshal(daemonRegisterRequest{
				CompanyID: "019fa834-a515-79b2-b1bd-19e6bd4f275a",
				DaemonID:  "daemon-1",
				Runtimes: []daemonRegisterRuntime{
					{Provider: protocol.SupportedProviders[0]},
					{
						Provider: protocol.SupportedProviders[1],
						Status:   "offline",
						UnusableReason: &daemonRuntimeUnusableReason{
							Code:    code,
							Message: "broken",
						},
					},
				},
			})
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			status, errorCode, detail, statements := daemonRegisterBoundaryStatus(t, body)
			if status != http.StatusUnprocessableEntity || errorCode != "validation_failed" {
				t.Fatalf(
					"status/code = %d/%s, want 422 validation_failed (detail %q)",
					status,
					errorCode,
					detail,
				)
			}
			if code != "" && !strings.Contains(detail, "runtimes[1].unusableReason.code") {
				t.Fatalf("detail = %q, want the entry named", detail)
			}
			if statements != 0 {
				t.Fatalf("database statements = %d, want 0", statements)
			}
		})
	}
}

// The message is bounded at the wire limit and required whenever a reason is
// sent; both fall out of the binding tags, before any database access.
func TestDaemonRegisterRejectsOversizedUnusableReasonMessage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		message string
	}{
		{name: "over the limit", message: strings.Repeat("x", protocol.RuntimeUnusableMessageMaxLen+1)},
		{name: "missing", message: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			body, err := json.Marshal(daemonRegisterRequest{
				CompanyID: "019fa834-a515-79b2-b1bd-19e6bd4f275a",
				DaemonID:  "daemon-1",
				Runtimes: []daemonRegisterRuntime{{
					Provider: protocol.SupportedProviders[1], Status: "offline",
					UnusableReason: &daemonRuntimeUnusableReason{
						Code:    protocol.RuntimeUnusableCodeNotExecutable,
						Message: test.message,
					},
				}},
			})
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			status, errorCode, detail, statements := daemonRegisterBoundaryStatus(t, body)
			if status != http.StatusUnprocessableEntity || errorCode != "validation_failed" {
				t.Fatalf(
					"status/code = %d/%s, want 422 validation_failed (detail %q)",
					status,
					errorCode,
					detail,
				)
			}
			if statements != 0 {
				t.Fatalf("database statements = %d, want 0", statements)
			}
		})
	}
	// Exactly the limit is accepted and reaches the membership lookup.
	body, err := json.Marshal(daemonRegisterRequest{
		CompanyID: "019fa834-a515-79b2-b1bd-19e6bd4f275a",
		DaemonID:  "daemon-1",
		Runtimes: []daemonRegisterRuntime{{
			Provider: protocol.SupportedProviders[1], Status: "offline",
			UnusableReason: &daemonRuntimeUnusableReason{
				Code:    protocol.RuntimeUnusableCodeNotExecutable,
				Message: strings.Repeat("x", protocol.RuntimeUnusableMessageMaxLen),
			},
		}},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if status, _, _, statements := daemonRegisterBoundaryStatus(
		t,
		body,
	); status != http.StatusNotFound ||
		statements != 1 {
		t.Fatalf(
			"status/statements = %d/%d at the limit, want 404 after one membership lookup",
			status,
			statements,
		)
	}
}

func TestParseDaemonRuntimeIDsCanonicalizesAtWebSocketBoundary(t *testing.T) {
	t.Parallel()

	const first = "019FA834-A515-79B2-B1BD-19E6BD4F275A"
	const second = "019fa834-a515-79b2-b1bd-19e6bd4f275b"
	got, err := parseDaemonRuntimeIDs("  " + first + ",," + second + "  ")
	if err != nil {
		t.Fatalf("parseDaemonRuntimeIDs() error = %v", err)
	}
	want := []string{strings.ToLower(first), second}
	if len(got) != len(want) || got[0].String() != want[0] || got[1].String() != want[1] {
		t.Fatalf("parseDaemonRuntimeIDs() = %v, want %v", got, want)
	}

	if _, err := parseDaemonRuntimeIDs("not-a-uuid"); err == nil {
		t.Fatal("parseDaemonRuntimeIDs() accepted a malformed runtime id")
	}
}

// TestDaemonWSClaimRunsBoundarySemantics pins the WS runs.claim adapter: the
// same transport-free core as the HTTP batch claim, with the request boundary
// re-declared for the RPC frame — connection principal required, strict JSON,
// required daemonId, and the max_runs vocabulary — all without any synthetic
// HTTP replay.
func TestDaemonWSClaimRunsBoundarySemantics(t *testing.T) {
	t.Parallel()

	principal := auth.UserPrincipal{
		UserID:    uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2771"),
		SessionID: uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2772"),
		AuthKind:  auth.TokenKindBearer,
	}
	identity := daemonws.ClientIdentity{DaemonID: "daemon-1", Principal: principal}

	tests := []struct {
		name       string
		identity   daemonws.ClientIdentity
		body       string
		wantStatus int
		wantBody   string
		wantErr    bool
	}{
		{
			name:       "no principal",
			identity:   daemonws.ClientIdentity{DaemonID: "daemon-1"},
			body:       `{"daemonId":"daemon-1","maxRuns":1}`,
			wantStatus: http.StatusUnauthorized,
			wantErr:    true,
		},
		{
			name:       "malformed body",
			identity:   identity,
			body:       `{"daemonId":`,
			wantStatus: http.StatusBadRequest,
			wantErr:    true,
		},
		{
			name:       "unknown field",
			identity:   identity,
			body:       `{"daemonId":"daemon-1","surprise":true}`,
			wantStatus: http.StatusBadRequest,
			wantErr:    true,
		},
		{
			name:       "missing daemon id",
			identity:   identity,
			body:       `{"maxRuns":1}`,
			wantStatus: http.StatusUnprocessableEntity,
			wantErr:    true,
		},
		{
			name:       "negative max runs",
			identity:   identity,
			body:       `{"daemonId":"daemon-1","maxRuns":-1}`,
			wantStatus: http.StatusBadRequest,
			wantErr:    true,
		},
		{
			name:       "zero max runs claims nothing",
			identity:   identity,
			body:       `{"daemonId":"daemon-1","maxRuns":0}`,
			wantStatus: http.StatusOK,
			wantBody:   `{"items":[]}`,
		},
		{
			name:       "no runtime ids claims nothing",
			identity:   identity,
			body:       `{"daemonId":"daemon-1","maxRuns":3}`,
			wantStatus: http.StatusOK,
			wantBody:   `{"items":[]}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			database := &daemonRegisterDBTX{}
			handler := &Handler{Queries: db.New(database)}

			status, body, err := handler.DaemonWSClaimRuns(
				context.Background(), test.identity, "runs.claim", json.RawMessage(test.body),
			)
			if status != test.wantStatus {
				t.Fatalf("status = %d, want %d (err = %v)", status, test.wantStatus, err)
			}
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr %t", err, test.wantErr)
			}
			if test.wantBody != "" && string(body) != test.wantBody {
				t.Fatalf("body = %s, want %s", body, test.wantBody)
			}
			// Every case above resolves at the boundary or inside the core's
			// short-circuits; none may touch the database.
			if len(database.statements) != 0 {
				t.Fatalf("database statements = %d, want 0", len(database.statements))
			}
		})
	}
}

func TestDaemonWebSocketRejectsMalformedRuntimeIDAtIngress(t *testing.T) {
	t.Parallel()

	principal := auth.UserPrincipal{
		UserID:    uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2761"),
		SessionID: uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2762"),
		AuthKind:  auth.TokenKindBearer,
	}
	router := gin.New()
	router.GET("/ws", func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithUserPrincipal(c.Request.Context(), principal))
		(&Handler{}).daemonWebSocket(c)
	})
	request := httptest.NewRequest(http.MethodGet, "/ws?runtime_ids=not-a-uuid", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf(
			"status = %d, want %d; body = %s",
			response.Code,
			http.StatusBadRequest,
			response.Body.String(),
		)
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["code"] != "invalid_request" {
		t.Fatalf("code = %v, want invalid_request", payload["code"])
	}
}

// daemonRuntimeAccessDBTX scripts the reads the daemon gates make — the run
// row, the runtime row and the caller's membership — and records every
// statement so a test can prove which lookups ran and in what order. A
// multi-row read fails on purpose: the WS upgrade test uses that to show the
// daemon-id stage was passed and the company lookup reached. runtimeErr, when
// set, is what the runtime read answers instead of the row. runtimeRemoved
// marks the row removed: a runtime read that hides removed rows finds
// nothing, and only the read that keeps them visible returns it.
type daemonRuntimeAccessDBTX struct {
	runtime        db.Runtime
	runtimeErr     error
	runtimeRemoved bool
	run            db.Run
	member         bool
	statements     []string
}

func (d *daemonRuntimeAccessDBTX) Exec(
	_ context.Context,
	statement string,
	_ ...any,
) (pgconn.CommandTag, error) {
	d.statements = append(d.statements, statement)
	return pgconn.CommandTag{}, nil
}

func (d *daemonRuntimeAccessDBTX) Query(
	_ context.Context,
	statement string,
	_ ...any,
) (pgx.Rows, error) {
	d.statements = append(d.statements, statement)
	return nil, errors.New("scripted: multi-row reads are not answered")
}

func (d *daemonRuntimeAccessDBTX) QueryRow(
	_ context.Context,
	statement string,
	_ ...any,
) pgx.Row {
	d.statements = append(d.statements, statement)
	return daemonRuntimeAccessRow{fake: d, statement: statement}
}

type daemonRuntimeAccessRow struct {
	fake      *daemonRuntimeAccessDBTX
	statement string
}

func (r daemonRuntimeAccessRow) Scan(dest ...any) error {
	switch {
	case strings.Contains(r.statement, "FROM runs"):
		// Positions follow the runs column order (generated models.go Run).
		*(dest[0].(*uuid.UUID)) = r.fake.run.ID
		*(dest[1].(*uuid.UUID)) = r.fake.run.CompanyID
		*(dest[5].(*uuid.UUID)) = r.fake.run.RuntimeID
		*(dest[6].(*string)) = r.fake.run.Status
		*(dest[12].(*[]byte)) = r.fake.run.Context
		*(dest[27].(*uuid.NullUUID)) = r.fake.run.ExecutionAuthorizedByUserID
		return nil
	case strings.Contains(r.statement, "FROM runtimes"):
		if r.fake.runtimeErr != nil {
			return r.fake.runtimeErr
		}
		if r.fake.runtimeRemoved && strings.Contains(r.statement, "removed_at IS NULL") {
			return pgx.ErrNoRows
		}
		// getRuntime scans id, company_id, daemon_id first and owner_user_id
		// tenth; the rest of the row is irrelevant to the gate.
		*(dest[0].(*uuid.UUID)) = r.fake.runtime.ID
		*(dest[1].(*uuid.UUID)) = r.fake.runtime.CompanyID
		*(dest[2].(*string)) = r.fake.runtime.DaemonID
		*(dest[9].(*uuid.NullUUID)) = r.fake.runtime.OwnerUserID
		return nil
	case strings.Contains(r.statement, "FROM company_members"):
		if !r.fake.member {
			return pgx.ErrNoRows
		}
		*(dest[0].(*uuid.UUID)) = r.fake.runtime.CompanyID
		return nil
	default:
		return pgx.ErrNoRows
	}
}

func daemonTestPrincipal() auth.UserPrincipal {
	return auth.UserPrincipal{
		UserID:    uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2771"),
		SessionID: uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2772"),
		AuthKind:  auth.TokenKindBearer,
	}
}

func decodeErrorBody(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response %q: %v", response.Body.String(), err)
	}
	return payload
}

// The one gate every runtime-scoped daemon route shares proves the
// runtime was registered by the calling daemon, before membership and with
// no extra statement; a mismatch is the non-disclosing 404 the route would
// answer for a runtime that does not exist. The rollout knob decides what a
// missing header means: admitted with a warning, or refused before any
// database access.
func TestRequireDaemonRuntimeAccessFencesOnTheDaemonID(t *testing.T) {
	t.Parallel()

	runtimeID := uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2773")
	companyID := uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2774")
	registrant := uuid.NullUUID{UUID: daemonTestPrincipal().UserID, Valid: true}
	tests := []struct {
		name            string
		header          string
		requireDaemonID bool
		member          bool
		owner           uuid.NullUUID
		wantOK          bool
		wantStatus      int
		wantCode        string
		wantDetail      string
		wantStatements  int
	}{
		{
			name: "foreign daemon id", header: "daemon-m", member: true, owner: registrant,
			wantStatus: http.StatusNotFound, wantCode: "runtime_not_found", wantStatements: 1,
		},
		{
			name: "matching daemon id", header: "daemon-a", member: true, owner: registrant,
			wantOK: true, wantStatements: 2,
		},
		{
			name: "missing header at the warn stage", member: true, owner: registrant,
			wantOK: true, wantStatements: 2,
		},
		{
			name:            "missing header at the require stage",
			requireDaemonID: true,
			member:          true,
			owner:           registrant,
			wantStatus:      http.StatusBadRequest,
			wantCode:        "invalid_request",
			wantDetail:      "X-Zenith-Team-Daemon-ID header is required",
			wantStatements:  0,
		},
		{
			name: "matching daemon id but not a member", header: "daemon-a", owner: registrant,
			wantStatus: http.StatusNotFound, wantCode: "company_not_found", wantStatements: 2,
		},
		// The registrant fence (ADR 0014): the same machine signed in as
		// another account, or a row with no stored registrant, is answered
		// exactly like a foreign daemon, before the membership read.
		{
			name:   "matching daemon id under another account",
			header: "daemon-a",
			member: true,
			owner: uuid.NullUUID{
				UUID:  uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2779"),
				Valid: true,
			},
			wantStatus:     http.StatusNotFound,
			wantCode:       "runtime_not_found",
			wantStatements: 1,
		},
		{
			name: "matching daemon id on a row with no registrant", header: "daemon-a", member: true,
			wantStatus: http.StatusNotFound, wantCode: "runtime_not_found", wantStatements: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			database := &daemonRuntimeAccessDBTX{
				runtime: db.Runtime{
					ID:          runtimeID,
					CompanyID:   companyID,
					DaemonID:    "daemon-a",
					OwnerUserID: test.owner,
				},
				member: test.member,
			}
			response := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(response)
			c.Request = httptest.NewRequest(
				http.MethodPost, "/api/v1/daemon/runtimes/"+runtimeID.String()+"/recover-orphans", nil,
			)
			if test.header != "" {
				c.Request.Header.Set("X-Zenith-Team-Daemon-ID", test.header)
			}
			c.Request = c.Request.WithContext(
				auth.WithUserPrincipal(c.Request.Context(), daemonTestPrincipal()),
			)

			handler := &Handler{Queries: db.New(database), RequireDaemonID: test.requireDaemonID}
			got, ok := handler.requireDaemonRuntimeAccess(c, runtimeID.String())

			if ok != test.wantOK {
				t.Fatalf("ok = %v, want %v; body = %s", ok, test.wantOK, response.Body.String())
			}
			if len(database.statements) != test.wantStatements {
				t.Fatalf("statements = %v, want %d", database.statements, test.wantStatements)
			}
			if test.wantOK {
				if got.ID != runtimeID || got.DaemonID != "daemon-a" {
					t.Fatalf("runtime = %+v, want the fenced row", got)
				}
				return
			}
			if response.Code != test.wantStatus {
				t.Fatalf(
					"status = %d, want %d; body = %s",
					response.Code,
					test.wantStatus,
					response.Body.String(),
				)
			}
			payload := decodeErrorBody(t, response)
			if payload["code"] != test.wantCode {
				t.Fatalf("code = %v, want %s", payload["code"], test.wantCode)
			}
			if test.wantDetail != "" && payload["detail"] != test.wantDetail {
				t.Fatalf("detail = %v, want %q", payload["detail"], test.wantDetail)
			}
		})
	}
}

// ADR 0014 rule 2 on the run-scoped routes: a daemon speaks for a run only
// when the run's runtime is its own — registered by this account, on this
// machine when it names one. Every other run answers exactly like one that
// does not exist, before the membership read, so another member's run id is
// not an oracle. Only a missing row is that 404; a failed read is a 500 the
// daemon must not take for "run gone".
func TestRequireDaemonRunAccessFencesOnTheRunsRuntime(t *testing.T) {
	t.Parallel()

	runID := uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2780")
	runtimeID := uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2781")
	companyID := uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2782")
	registrant := uuid.NullUUID{UUID: daemonTestPrincipal().UserID, Valid: true}
	colleague := uuid.NullUUID{UUID: uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2783"), Valid: true}
	tests := []struct {
		name            string
		header          string
		requireDaemonID bool
		member          bool
		owner           uuid.NullUUID
		runtimeErr      error
		runtimeRemoved  bool
		wantOK          bool
		wantStatus      int
		wantCode        string
		wantStatements  int
	}{
		{
			name: "own run on this machine", header: "daemon-a", member: true, owner: registrant,
			wantOK: true, wantStatements: 3,
		},
		{
			name: "own run without a header at the warn stage", member: true, owner: registrant,
			wantOK: true, wantStatements: 3,
		},
		// A Run keeps its runtime through a removal, so the daemon that ran
		// it still reports on it: the gate must read the runtime with the
		// query that keeps removed rows, or a run finished while its runtime
		// was removed would look gone.
		{
			name: "own run on a removed runtime still reports", header: "daemon-a", member: true,
			owner: registrant, runtimeRemoved: true,
			wantOK: true, wantStatements: 3,
		},
		{
			name:            "missing header at the require stage",
			requireDaemonID: true, member: true, owner: registrant,
			wantStatus: http.StatusBadRequest, wantCode: "invalid_request", wantStatements: 0,
		},
		{
			name: "another member's run, sending their machine's id", header: "daemon-a", member: true,
			owner: colleague, wantStatus: http.StatusNotFound, wantCode: "run_not_found", wantStatements: 2,
		},
		{
			name: "another member's run without a header", member: true, owner: colleague,
			wantStatus: http.StatusNotFound, wantCode: "run_not_found", wantStatements: 2,
		},
		{
			name: "own account on another machine", header: "daemon-m", member: true, owner: registrant,
			wantStatus: http.StatusNotFound, wantCode: "run_not_found", wantStatements: 2,
		},
		{
			name: "runtime with no registrant", header: "daemon-a", member: true,
			wantStatus: http.StatusNotFound, wantCode: "run_not_found", wantStatements: 2,
		},
		{
			name: "own run but no longer a member", header: "daemon-a", owner: registrant,
			wantStatus: http.StatusNotFound, wantCode: "company_not_found", wantStatements: 3,
		},
		{
			name: "runtime row gone", header: "daemon-a", member: true, owner: registrant,
			runtimeErr: pgx.ErrNoRows,
			wantStatus: http.StatusNotFound, wantCode: "run_not_found", wantStatements: 2,
		},
		{
			name: "runtime read fails", header: "daemon-a", member: true, owner: registrant,
			runtimeErr: errors.New("connection reset"),
			wantStatus: http.StatusInternalServerError, wantCode: "internal_error", wantStatements: 2,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			database := &daemonRuntimeAccessDBTX{
				runtime: db.Runtime{
					ID:          runtimeID,
					CompanyID:   companyID,
					DaemonID:    "daemon-a",
					OwnerUserID: test.owner,
				},
				runtimeErr:     test.runtimeErr,
				runtimeRemoved: test.runtimeRemoved,
				run: db.Run{
					ID:        runID,
					CompanyID: companyID,
					RuntimeID: runtimeID,
					Status:    "running",
				},
				member: test.member,
			}
			response := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(response)
			c.Request = httptest.NewRequest(
				http.MethodGet, "/api/v1/daemon/runs/"+runID.String()+"/status", nil,
			)
			if test.header != "" {
				c.Request.Header.Set("X-Zenith-Team-Daemon-ID", test.header)
			}
			c.Request = c.Request.WithContext(
				auth.WithUserPrincipal(c.Request.Context(), daemonTestPrincipal()),
			)

			handler := &Handler{Queries: db.New(database), RequireDaemonID: test.requireDaemonID}
			got, ok := handler.requireDaemonRunAccess(c, runID.String())

			if ok != test.wantOK {
				t.Fatalf("ok = %v, want %v; body = %s", ok, test.wantOK, response.Body.String())
			}
			if len(database.statements) != test.wantStatements {
				t.Fatalf("statements = %v, want %d", database.statements, test.wantStatements)
			}
			if test.wantOK {
				if got.ID != runID || got.RuntimeID != runtimeID {
					t.Fatalf("run = %+v, want the fenced row", got)
				}
				return
			}
			if response.Code != test.wantStatus {
				t.Fatalf(
					"status = %d, want %d; body = %s",
					response.Code,
					test.wantStatus,
					response.Body.String(),
				)
			}
			if payload := decodeErrorBody(t, response); payload["code"] != test.wantCode {
				t.Fatalf("code = %v, want %s", payload["code"], test.wantCode)
			}
		})
	}
}

// The smoke's G7.5-run probe as a test: signed in as one member and naming
// the colleague's machine id, a daemon reaches none of the colleague's run
// through any run-scoped route, reads included. Each refusal is the plain
// run_not_found and touches nothing past the run and runtime reads. The
// registrant's own daemon still reads the status.
func TestDaemonRunRoutesAnswerAnotherMembersRunAsMissing(t *testing.T) {
	t.Parallel()

	runID := uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2784")
	runtimeID := uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2785")
	companyID := uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2786")
	colleague := uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2787")
	serve := func(
		owner uuid.UUID,
		runtimeRemoved bool,
		method, route string,
	) (*httptest.ResponseRecorder, *daemonRuntimeAccessDBTX) {
		database := &daemonRuntimeAccessDBTX{
			runtime: db.Runtime{
				ID: runtimeID, CompanyID: companyID, DaemonID: "daemon-a",
				OwnerUserID: uuid.NullUUID{UUID: owner, Valid: true},
			},
			runtimeRemoved: runtimeRemoved,
			run: db.Run{
				ID:        runID,
				CompanyID: companyID,
				RuntimeID: runtimeID,
				Status:    "running",
			},
			member: true,
		}
		handler := New(Handler{
			AuthService:  authenticatedAuthStub(daemonTestPrincipal().UserID),
			RunService:   &execution.DefaultRunService{},
			UsageService: routeTableUsage{},
			Queries:      db.New(database),
			Bus:          routeTableBus(),
		})
		var body io.Reader
		if method == http.MethodPost {
			body = strings.NewReader(`{}`)
		}
		request := httptest.NewRequest(method, "/api/v1/daemon/runs/"+runID.String()+"/"+route, body)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer access-token")
		request.Header.Set("X-Zenith-Team-Daemon-ID", "daemon-a")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response, database
	}

	routes := []struct{ method, route string }{
		{http.MethodGet, "status"},
		{http.MethodPost, "start"},
		{http.MethodPost, "wait-local-directory"},
		{http.MethodPost, "wait-studio-auth"},
		{http.MethodPost, "usage"},
		{http.MethodPost, "session"},
		{http.MethodPost, "complete"},
		{http.MethodPost, "fail"},
		{http.MethodPost, "cancel-ack"},
	}
	for _, route := range routes {
		t.Run(route.route, func(t *testing.T) {
			t.Parallel()
			response, database := serve(colleague, false, route.method, route.route)
			assertErrorResponse(t, response, http.StatusNotFound, "run_not_found")
			if len(database.statements) != 2 {
				t.Fatalf("statements = %v, want only the run and runtime reads", database.statements)
			}
		})
	}

	t.Run("the registrant's own daemon", func(t *testing.T) {
		t.Parallel()
		response, _ := serve(daemonTestPrincipal().UserID, false, http.MethodGet, "status")
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"running"`) {
			t.Fatalf("status read = %d %s, want 200 running", response.Code, response.Body.String())
		}
	})

	// The runtime was removed while its run was still out: the run keeps
	// it, so the registrant's daemon still reads the run rather than being
	// told it is gone and interrupting the agent.
	t.Run("the registrant's own daemon after the runtime was removed", func(t *testing.T) {
		t.Parallel()
		response, _ := serve(daemonTestPrincipal().UserID, true, http.MethodGet, "status")
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"running"`) {
			t.Fatalf("status read = %d %s, want 200 running", response.Code, response.Body.String())
		}
	})
}

func newDaemonRouteHandler(stub runtimeServiceStub, requireDaemonID bool) *Handler {
	return New(Handler{
		AuthService:     authenticatedAuthStub(daemonTestPrincipal().UserID),
		RuntimeService:  stub,
		RunService:      &execution.DefaultRunService{},
		Queries:         &db.Queries{},
		Bus:             routeTableBus(),
		RequireDaemonID: requireDaemonID,
	})
}

// Over HTTP, heartbeat and deregister carry X-Zenith-Team-Daemon-ID
// into the domain, a daemon-mismatch denial is the same 404 runtime_not_found
// a non-member gets, and the deregister batch still answers its receipt. The
// rollout stages: a missing header is forwarded as the unfenced empty id, or
// refused with 400 before the service is called.
func TestDaemonHeartbeatAndDeregisterRoutesCarryTheDaemonID(t *testing.T) {
	t.Parallel()

	runtimeID := uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2775")
	heartbeatBody, err := json.Marshal(daemonHeartbeatRequest{RuntimeID: runtimeID.String()})
	if err != nil {
		t.Fatalf("marshal heartbeat: %v", err)
	}
	deregisterBody, err := json.Marshal(daemonDeregisterRequest{RuntimeIDs: []string{runtimeID.String()}})
	if err != nil {
		t.Fatalf("marshal deregister: %v", err)
	}
	post := func(handler http.Handler, path, daemonID string, body []byte) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer access-token")
		request.Header.Set("Content-Type", "application/json")
		if daemonID != "" {
			request.Header.Set("X-Zenith-Team-Daemon-ID", daemonID)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	t.Run("heartbeat forwards the header and folds a daemon mismatch into 404", func(t *testing.T) {
		t.Parallel()
		var seen string
		handler := newDaemonRouteHandler(runtimeServiceStub{
			heartbeat: func(_ context.Context, _ auth.UserPrincipal, id uuid.UUID, daemonID string) error {
				seen = daemonID
				if id != runtimeID {
					t.Errorf("runtime id = %s, want %s", id, runtimeID)
				}
				return agent.ErrRuntimeAccessDenied
			},
		}, false)
		response := post(handler, "/api/v1/daemon/heartbeat", "daemon-m", heartbeatBody)
		if seen != "daemon-m" {
			t.Fatalf("service saw daemon id %q, want daemon-m", seen)
		}
		assertErrorResponse(t, response, http.StatusNotFound, "runtime_not_found")
	})

	t.Run("heartbeat without a header is unfenced at the warn stage", func(t *testing.T) {
		t.Parallel()
		called := false
		handler := newDaemonRouteHandler(runtimeServiceStub{
			heartbeat: func(_ context.Context, _ auth.UserPrincipal, _ uuid.UUID, daemonID string) error {
				called = true
				if daemonID != "" {
					t.Errorf("service saw daemon id %q, want the unfenced empty id", daemonID)
				}
				return nil
			},
		}, false)
		response := post(handler, "/api/v1/daemon/heartbeat", "", heartbeatBody)
		if !called {
			t.Fatal("service was not called")
		}
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
		}
	})

	t.Run("heartbeat without a header is refused at the require stage", func(t *testing.T) {
		t.Parallel()
		handler := newDaemonRouteHandler(runtimeServiceStub{
			heartbeat: func(context.Context, auth.UserPrincipal, uuid.UUID, string) error {
				t.Error("service must not be called when the header is required and missing")
				return nil
			},
		}, true)
		response := post(handler, "/api/v1/daemon/heartbeat", "", heartbeatBody)
		assertErrorResponse(t, response, http.StatusBadRequest, "invalid_request")
		if detail := decodeErrorBody(t, response)["detail"]; detail != "X-Zenith-Team-Daemon-ID header is required" {
			t.Fatalf("detail = %v", detail)
		}
	})

	t.Run("deregister forwards the header in the batch input", func(t *testing.T) {
		t.Parallel()
		var seen agent.RuntimeDeregistration
		handler := newDaemonRouteHandler(runtimeServiceStub{
			deregister: func(_ context.Context, _ auth.UserPrincipal, input agent.RuntimeDeregistration) error {
				seen = input
				return nil
			},
		}, false)
		response := post(handler, "/api/v1/daemon/deregister", "daemon-m", deregisterBody)
		if seen.DaemonID != "daemon-m" || len(seen.RuntimeIDs) != 1 ||
			seen.RuntimeIDs[0] != runtimeID.String() {
			t.Fatalf("service saw %+v, want daemon-m with the one id", seen)
		}
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
		}
		var receipt daemonStatusResponse
		if err := json.Unmarshal(response.Body.Bytes(), &receipt); err != nil || receipt.Status != "ok" {
			t.Fatalf("receipt = %s (%v), want {status: ok}", response.Body.String(), err)
		}
	})

	t.Run("deregister without a header is refused at the require stage", func(t *testing.T) {
		t.Parallel()
		handler := newDaemonRouteHandler(runtimeServiceStub{
			deregister: func(context.Context, auth.UserPrincipal, agent.RuntimeDeregistration) error {
				t.Error("service must not be called when the header is required and missing")
				return nil
			},
		}, true)
		response := post(handler, "/api/v1/daemon/deregister", "", deregisterBody)
		assertErrorResponse(t, response, http.StatusBadRequest, "invalid_request")
	})
}

// The WS heartbeat seam forwards the daemon id pinned at upgrade; a
// daemon-mismatch denial yields no ack (logged by the hub), while a missing
// row still answers runtime_gone so the daemon prunes and re-registers.
func TestDaemonWSHeartbeatUsesTheConnectionDaemonID(t *testing.T) {
	t.Parallel()

	runtimeID := uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2776")
	identity := daemonws.ClientIdentity{DaemonID: "daemon-m", Principal: daemonTestPrincipal()}
	beat := protocol.DaemonHeartbeatRequestPayload{RuntimeID: runtimeID.String()}

	var seen string
	denied := &Handler{RuntimeService: runtimeServiceStub{
		heartbeat: func(_ context.Context, _ auth.UserPrincipal, _ uuid.UUID, daemonID string) error {
			seen = daemonID
			return agent.ErrRuntimeAccessDenied
		},
	}}
	ack, err := denied.DaemonWSHeartbeat(context.Background(), identity, beat)
	if seen != "daemon-m" {
		t.Fatalf("service saw daemon id %q, want daemon-m", seen)
	}
	if err == nil || ack != nil {
		t.Fatalf("denied heartbeat = (%+v, %v), want no ack and an error", ack, err)
	}

	gone := &Handler{RuntimeService: runtimeServiceStub{
		heartbeat: func(context.Context, auth.UserPrincipal, uuid.UUID, string) error {
			return agent.ErrRuntimeNotFound
		},
	}}
	ack, err = gone.DaemonWSHeartbeat(context.Background(), identity, beat)
	if err != nil || ack == nil || ack.Status != protocol.HeartbeatStatusRuntimeGone || !ack.RuntimeGone {
		t.Fatalf("gone heartbeat = (%+v, %v), want a runtime_gone ack", ack, err)
	}
}

// A runtime reports its credential isolation on every heartbeat. Both
// transports take only the two reports, or none from an older daemon, and
// never store the value.
func TestDaemonHeartbeatTakesOnlyAKnownCredentialIsolationReport(t *testing.T) {
	t.Parallel()

	runtimeID := "019fa834-a515-79b2-b1bd-19e6bd4f2778"
	var beats atomic.Int32
	stub := runtimeServiceStub{
		heartbeat: func(context.Context, auth.UserPrincipal, uuid.UUID, string) error {
			beats.Add(1)
			return nil
		},
	}
	h := &Handler{RuntimeService: stub}
	identity := daemonws.ClientIdentity{DaemonID: "daemon-m", Principal: daemonTestPrincipal()}
	for _, report := range []string{"", protocol.CredentialIsolationEnforced, protocol.CredentialIsolationNotEnforced} {
		beat := protocol.DaemonHeartbeatRequestPayload{RuntimeID: runtimeID, CredentialIsolation: report}
		if ack, err := h.DaemonWSHeartbeat(context.Background(), identity, beat); err != nil || ack == nil {
			t.Fatalf("WS heartbeat reporting %q = (%+v, %v), want an ack", report, ack, err)
		}
	}
	for _, report := range []string{"ENFORCED", "protected", " enforced"} {
		beat := protocol.DaemonHeartbeatRequestPayload{RuntimeID: runtimeID, CredentialIsolation: report}
		if ack, err := h.DaemonWSHeartbeat(context.Background(), identity, beat); err == nil || ack != nil {
			t.Fatalf("WS heartbeat reporting %q = (%+v, %v), want no ack", report, ack, err)
		}
	}
	if got := beats.Load(); got != 3 {
		t.Fatalf("liveness writes = %d, want one per accepted heartbeat", got)
	}

	routes := newDaemonRouteHandler(stub, false)
	for report, want := range map[string]int{
		protocol.CredentialIsolationEnforced:    http.StatusOK,
		protocol.CredentialIsolationNotEnforced: http.StatusOK,
		"protected":                             http.StatusUnprocessableEntity,
	} {
		body := `{"runtimeId":"` + runtimeID + `","credentialIsolation":"` + report + `"}`
		request := httptest.NewRequest(http.MethodPost, "/api/v1/daemon/heartbeat", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer access-token")
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Zenith-Team-Daemon-ID", "daemon-m")
		response := httptest.NewRecorder()
		routes.ServeHTTP(response, request)
		if response.Code != want {
			t.Fatalf(
				"HTTP heartbeat reporting %q = %d %s, want %d",
				report,
				response.Code,
				response.Body,
				want,
			)
		}
	}
	if got := beats.Load(); got != 5 {
		t.Fatalf("liveness writes = %d after the HTTP heartbeats, want 5", got)
	}
}

// The ack advertises plugin-presentation-v1, so a daemon adds the Codex
// presentation to Plugin discovery only for a server that accepts it.
func TestDaemonHeartbeatAckAdvertisesPluginPresentation(t *testing.T) {
	t.Parallel()

	h := &Handler{RuntimeService: runtimeServiceStub{
		heartbeat: func(context.Context, auth.UserPrincipal, uuid.UUID, string) error { return nil },
	}}
	identity := daemonws.ClientIdentity{DaemonID: "daemon-m", Principal: daemonTestPrincipal()}
	beat := protocol.DaemonHeartbeatRequestPayload{RuntimeID: "019fa834-a515-79b2-b1bd-19e6bd4f2777"}
	ack, err := h.DaemonWSHeartbeat(context.Background(), identity, beat)
	if err != nil || ack == nil {
		t.Fatalf("heartbeat = (%+v, %v), want an ack", ack, err)
	}
	want := []string{
		protocol.DaemonCapabilityRPCV1,
		protocol.DaemonCapabilityModelDiscoveryV1,
		protocol.DaemonCapabilityPluginPresentationV1,
	}
	if strings.Join(ack.ServerCapabilities, ",") != strings.Join(want, ",") {
		t.Fatalf("server capabilities = %v, want %v", ack.ServerCapabilities, want)
	}
}

// The upgrade applies the same rollout rule: with the knob on, a socket that
// names no daemon (neither header nor daemon_id query) is refused before the
// company lookup; with it off, the upgrade proceeds to that lookup.
func TestDaemonWebSocketRequiresADaemonIDOnlyAtTheRequireStage(t *testing.T) {
	t.Parallel()

	serve := func(requireDaemonID bool) (*httptest.ResponseRecorder, *daemonRuntimeAccessDBTX) {
		database := &daemonRuntimeAccessDBTX{}
		router := gin.New()
		router.GET("/ws", func(c *gin.Context) {
			c.Request = c.Request.WithContext(
				auth.WithUserPrincipal(c.Request.Context(), daemonTestPrincipal()),
			)
			(&Handler{Queries: db.New(database), RequireDaemonID: requireDaemonID}).daemonWebSocket(c)
		})
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/ws", nil))
		return response, database
	}

	response, database := serve(true)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", response.Code, response.Body.String())
	}
	if payload := decodeErrorBody(t, response); payload["code"] != "invalid_request" {
		t.Fatalf("code = %v, want invalid_request", payload["code"])
	}
	if len(database.statements) != 0 {
		t.Fatalf("statements = %v, want none before the daemon id is proven", database.statements)
	}

	response, database = serve(false)
	if len(database.statements) != 1 || !strings.Contains(database.statements[0], "companies") {
		t.Fatalf("statements = %v, want the company lookup reached at the warn stage", database.statements)
	}
	if response.Code == http.StatusBadRequest {
		t.Fatalf("warn stage refused the upgrade: %s", response.Body.String())
	}
}

// daemonSocketScopeDBTX answers the two multi-row reads the socket upgrade
// makes: the caller's one company, and the requested runtimes the way
// GetRuntimes returns them — only the ids asked for, unknown ids absent.
type daemonSocketScopeDBTX struct {
	companyID   uuid.UUID
	runtimes    []db.Runtime
	runtimesErr error
	statements  []string
}

func (d *daemonSocketScopeDBTX) Exec(
	_ context.Context,
	statement string,
	_ ...any,
) (pgconn.CommandTag, error) {
	d.statements = append(d.statements, statement)
	return pgconn.CommandTag{}, nil
}

func (d *daemonSocketScopeDBTX) Query(
	_ context.Context,
	statement string,
	args ...any,
) (pgx.Rows, error) {
	d.statements = append(d.statements, statement)
	if !strings.Contains(statement, "FROM runtimes") {
		return &scriptedRows{scans: []func(...any){func(dest ...any) {
			*(dest[0].(*uuid.UUID)) = d.companyID
		}}}, nil
	}
	if d.runtimesErr != nil {
		return nil, d.runtimesErr
	}
	requested, _ := args[0].([]uuid.UUID)
	rows := &scriptedRows{}
	for _, runtime := range d.runtimes {
		if !slices.Contains(requested, runtime.ID) {
			continue
		}
		rows.scans = append(rows.scans, func(dest ...any) {
			*(dest[0].(*uuid.UUID)) = runtime.ID
			*(dest[1].(*uuid.UUID)) = runtime.CompanyID
			*(dest[2].(*string)) = runtime.DaemonID
			*(dest[9].(*uuid.NullUUID)) = runtime.OwnerUserID
		})
	}
	return rows, nil
}

func (d *daemonSocketScopeDBTX) QueryRow(
	_ context.Context,
	statement string,
	_ ...any,
) pgx.Row {
	d.statements = append(d.statements, statement)
	return daemonRegisterRow{}
}

// scriptedRows serves one scan function per row.
type scriptedRows struct {
	scans []func(...any)
	next  int
}

func (r *scriptedRows) Close()                                       {}
func (r *scriptedRows) Err() error                                   { return nil }
func (r *scriptedRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *scriptedRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *scriptedRows) Values() ([]any, error)                       { return nil, nil }
func (r *scriptedRows) RawValues() [][]byte                          { return nil }
func (r *scriptedRows) Conn() *pgx.Conn                              { return nil }
func (r *scriptedRows) TypeMap() *pgtype.Map                         { return nil }

func (r *scriptedRows) Next() bool {
	r.next++
	return r.next <= len(r.scans)
}

func (r *scriptedRows) Scan(dest ...any) error {
	r.scans[r.next-1](dest...)
	return nil
}

// ADR 0014 rule 2 on the control socket (the smoke's G7.5-ws): the upgrade
// keeps only the runtime_ids the caller registered, from this machine, in
// one of its companies, and drops the rest instead of refusing the socket —
// the claim's rule for a stale id. The hub indexes the socket under what is
// left, so a run.available for a colleague's runtime never reaches it, and
// the transcript-append RPC, which trusts that scope, cannot write into the
// colleague's Runs.
func TestDaemonWebSocketScopesOnlyTheCallersRuntimes(t *testing.T) {
	t.Parallel()

	principal := daemonTestPrincipal()
	registrant := uuid.NullUUID{UUID: principal.UserID, Valid: true}
	companyID := uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2790")
	own := db.Runtime{
		ID: uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2791"), CompanyID: companyID,
		DaemonID: "daemon-b", OwnerUserID: registrant,
	}
	colleagues := db.Runtime{
		ID: uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2792"), CompanyID: companyID,
		DaemonID: "daemon-a", OwnerUserID: uuid.NullUUID{
			UUID: uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2793"), Valid: true,
		},
	}
	ownOnAnotherMachine := db.Runtime{
		ID: uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2794"), CompanyID: companyID,
		DaemonID: "daemon-m", OwnerUserID: registrant,
	}
	ownOutsideTheCompanies := db.Runtime{
		ID:        uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2795"),
		CompanyID: uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2796"),
		DaemonID:  "daemon-b", OwnerUserID: registrant,
	}
	unknown := uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2797")
	database := &daemonSocketScopeDBTX{
		companyID: companyID,
		runtimes:  []db.Runtime{own, colleagues, ownOnAnotherMachine, ownOutsideTheCompanies},
	}

	hub := daemonws.NewHub()
	hub.SetRPCHandler(func(
		_ context.Context,
		identity daemonws.ClientIdentity,
		_ string,
		_ json.RawMessage,
	) (int, json.RawMessage, error) {
		scope, err := json.Marshal(identity.RuntimeIDs)
		return http.StatusOK, scope, err
	})
	handler := &Handler{Queries: db.New(database), DaemonWSHub: hub}
	router := gin.New()
	router.GET("/ws", func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithUserPrincipal(c.Request.Context(), principal))
		handler.daemonWebSocket(c)
	})
	server := httptest.NewServer(router)
	defer server.Close()

	requested := []string{
		own.ID.String(), colleagues.ID.String(), ownOnAnotherMachine.ID.String(),
		ownOutsideTheCompanies.ID.String(), unknown.String(),
	}
	conn, _, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(server.URL, "http")+"/ws?runtime_ids="+strings.Join(requested, ","),
		http.Header{"X-Zenith-Team-Daemon-ID": {"daemon-b"}},
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	readFrame := func(wantType string, payload any) {
		t.Helper()
		var message protocol.Message
		if err := conn.ReadJSON(&message); err != nil {
			t.Fatalf("read %s: %v", wantType, err)
		}
		if message.Type != wantType {
			t.Fatalf("frame type = %q, want %q (payload %s)", message.Type, wantType, message.Payload)
		}
		if err := json.Unmarshal(message.Payload, payload); err != nil {
			t.Fatalf("decode %s: %v", wantType, err)
		}
	}

	// An RPC round trip proves the socket is registered with the hub, and
	// answers with the scope the connection was pinned with.
	request, err := json.Marshal(protocol.RPCRequestPayload{RequestID: "scope", Method: "scope"})
	if err != nil {
		t.Fatalf("marshal rpc request: %v", err)
	}
	if err := conn.WriteJSON(protocol.Message{Type: protocol.EventDaemonRPCRequest, Payload: request}); err != nil {
		t.Fatalf("write rpc request: %v", err)
	}
	var response protocol.RPCResponsePayload
	readFrame(protocol.EventDaemonRPCResponse, &response)
	var scope []string
	if err := json.Unmarshal(response.Body, &scope); err != nil {
		t.Fatalf("decode scope %s: %v", response.Body, err)
	}
	if len(scope) != 1 || scope[0] != own.ID.String() {
		t.Fatalf("socket scope = %v, want only the caller's own runtime %s", scope, own.ID)
	}

	// Frames reach one socket in the order they were sent, so had the
	// colleague's wakeup been delivered it would be read first.
	hub.NotifyRunAvailable(colleagues.ID.String(), "colleague-run")
	hub.NotifyRunAvailable(own.ID.String(), "own-run")
	var wakeup protocol.RunAvailablePayload
	readFrame(protocol.EventDaemonRunAvailable, &wakeup)
	if wakeup.RuntimeID != own.ID.String() || wakeup.RunID != "own-run" {
		t.Fatalf("wakeup = %+v, want only the caller's own runtime", wakeup)
	}
}

// A failed runtime read refuses the upgrade with a 500 rather than opening
// the socket with a scope nobody checked.
func TestDaemonWebSocketRefusesWhenTheScopeCannotBeChecked(t *testing.T) {
	t.Parallel()

	database := &daemonSocketScopeDBTX{
		companyID:   uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2798"),
		runtimesErr: errors.New("connection reset"),
	}
	router := gin.New()
	router.GET("/ws", func(c *gin.Context) {
		c.Request = c.Request.WithContext(
			auth.WithUserPrincipal(c.Request.Context(), daemonTestPrincipal()),
		)
		(&Handler{Queries: db.New(database), DaemonWSHub: daemonws.NewHub()}).daemonWebSocket(c)
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/ws?runtime_ids=019fa834-a515-79b2-b1bd-19e6bd4f2799", nil,
	))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", response.Code, response.Body.String())
	}
	if payload := decodeErrorBody(t, response); payload["code"] != "internal_error" {
		t.Fatalf("code = %v, want internal_error", payload["code"])
	}
}

// The sign-in verdict is a closed vocabulary with an RFC 3339 check time,
// validated before any database access like the unusable-reason code.
func TestDaemonRegisterRejectsMalformedSignInBeforeDatabaseAccess(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		signIn daemonRuntimeSignIn
		want   string
	}{
		{
			name:   "unknown state",
			signIn: daemonRuntimeSignIn{State: "expired", CheckedAt: "2026-09-16T09:00:00Z"},
			want:   "runtimes[1].signIn.state",
		},
		{
			name:   "upper-case state",
			signIn: daemonRuntimeSignIn{State: "SIGNED_IN", CheckedAt: "2026-09-16T09:00:00Z"},
			want:   "runtimes[1].signIn.state",
		},
		{
			name:   "bad check time",
			signIn: daemonRuntimeSignIn{State: "signed_in", CheckedAt: "yesterday"},
			want:   "runtimes[1].signIn.checkedAt",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			body, err := json.Marshal(daemonRegisterRequest{
				CompanyID: "019fa834-a515-79b2-b1bd-19e6bd4f275a",
				DaemonID:  "daemon-1",
				Runtimes: []daemonRegisterRuntime{
					{Provider: protocol.SupportedProviders[0]},
					{Provider: protocol.SupportedProviders[1], SignIn: &test.signIn},
				},
			})
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			status, errorCode, detail, statements := daemonRegisterBoundaryStatus(t, body)
			if status != http.StatusUnprocessableEntity || errorCode != "validation_failed" {
				t.Fatalf(
					"status/code = %d/%s, want 422 validation_failed (detail %q)",
					status,
					errorCode,
					detail,
				)
			}
			if !strings.Contains(detail, test.want) {
				t.Fatalf("detail = %q, want it to name %s", detail, test.want)
			}
			if statements != 0 {
				t.Fatalf("database statements = %d, want 0", statements)
			}
		})
	}
}

// A runtime's credential-isolation report is enforced or
// not_enforced, refused otherwise before any database access.
func TestDaemonRegisterRejectsAnUnknownCredentialIsolationReport(t *testing.T) {
	t.Parallel()
	for _, report := range []string{"protected", "ENFORCED", "true"} {
		t.Run(report, func(t *testing.T) {
			t.Parallel()
			body, err := json.Marshal(daemonRegisterRequest{
				CompanyID: "019fa834-a515-79b2-b1bd-19e6bd4f275a",
				DaemonID:  "daemon-1",
				Runtimes: []daemonRegisterRuntime{
					{
						Provider:            protocol.SupportedProviders[0],
						CredentialIsolation: protocol.CredentialIsolationEnforced,
					},
					{Provider: protocol.SupportedProviders[1], CredentialIsolation: report},
				},
			})
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			status, errorCode, detail, statements := daemonRegisterBoundaryStatus(t, body)
			if status != http.StatusUnprocessableEntity || errorCode != "validation_failed" {
				t.Fatalf(
					"status/code = %d/%s, want 422 validation_failed (detail %q)",
					status,
					errorCode,
					detail,
				)
			}
			if statements != 0 {
				t.Fatalf("database statements = %d, want 0", statements)
			}
		})
	}
}

func TestDaemonDownloadRunAttachmentUsesFrozenAuthorizer(t *testing.T) {
	t.Parallel()
	for _, source := range []string{"companyReferences", "channelAttachments", "projectDocuments"} {
		for _, authorizer := range []uuid.UUID{uuid.New(), uuid.Nil} {
			t.Run(source+"/"+authorizer.String(), func(t *testing.T) {
				t.Parallel()
				companyID, runtimeID, runID, attachmentID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
				// The daemon is signed in as the runtime's registrant (ADR 0014);
				// the download must still act as the Run's frozen authorizer.
				database := &daemonRuntimeAccessDBTX{
					member: true,
					runtime: db.Runtime{
						ID:        runtimeID,
						CompanyID: companyID,
						DaemonID:  "test-daemon",
						OwnerUserID: uuid.NullUUID{
							UUID:  daemonTestPrincipal().UserID,
							Valid: true,
						},
					},
					run: db.Run{
						ID:        runID,
						CompanyID: companyID,
						RuntimeID: runtimeID,
						Status:    "dispatched",
						Context: []byte(
							`{"` + source + `":[{"id":"` + attachmentID.String() + `"}]}`,
						),
						ExecutionAuthorizedByUserID: uuid.NullUUID{
							UUID:  authorizer,
							Valid: authorizer != uuid.Nil,
						},
					},
				}
				called := false
				h := &Handler{Queries: db.New(database), AttachmentService: &attachmentServiceStub{
					downloadForRun: func(_ context.Context, input attachment.RunDownloadInput) (attachment.DownloadResult, error) {
						called = true
						if input.ID != attachmentID || input.CompanyID != companyID ||
							input.RunID != runID || input.AuthorizedByUserID != authorizer {
							t.Fatalf(
								"Run download authority = %+v, want Run %s and frozen authorizer %s",
								input,
								runID,
								authorizer,
							)
						}
						// Only a frozen Project document may outlive its retirement.
						if input.FrozenProjectDocument != (source == "projectDocuments") {
							t.Fatalf(
								"FrozenProjectDocument = %t for a file frozen in %s",
								input.FrozenProjectDocument,
								source,
							)
						}
						return attachment.DownloadResult{}, attachment.ErrNotFound
					},
				}}
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Params = gin.Params{
					{Key: "runtimeId", Value: runtimeID.String()},
					{Key: "runId", Value: runID.String()},
					{Key: "attachmentId", Value: attachmentID.String()},
				}
				request := httptest.NewRequest(http.MethodGet, "/", nil)
				request.Header.Set("X-Zenith-Team-Daemon-ID", "test-daemon")
				c.Request = request.WithContext(
					auth.WithUserPrincipal(request.Context(), daemonTestPrincipal()),
				)
				h.daemonDownloadRunAttachment(c)
				if !called || recorder.Code != http.StatusNotFound {
					t.Fatalf(
						"download called = %v, HTTP status = %d, want true, 404",
						called,
						recorder.Code,
					)
				}
			})
		}
	}
}

// priorSessionDBTX answers the claim's resume lookup with one prior
// terminal Run of the same Conversation and Agent.
type priorSessionDBTX struct {
	skillSetHash   string
	sessionID      string
	rolloutMissing bool
	throughSeq     int64
	runtimeID      uuid.UUID
}

func (d *priorSessionDBTX) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (d *priorSessionDBTX) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("scripted: multi-row reads are not answered")
}

func (d *priorSessionDBTX) QueryRow(_ context.Context, statement string, _ ...any) pgx.Row {
	return lookupRow(func(dest ...any) error {
		if !strings.Contains(statement, "GetLastRunSessionForConversation") {
			return pgx.ErrNoRows
		}
		*(dest[0].(*pgtype.Text)) = pgtype.Text{String: d.sessionID, Valid: d.sessionID != ""}
		*(dest[1].(*string)) = d.skillSetHash
		*(dest[2].(*bool)) = d.rolloutMissing
		*(dest[4].(*int64)) = d.throughSeq
		*(dest[5].(*uuid.UUID)) = d.runtimeID
		return nil
	})
}

type lookupRow func(dest ...any) error

func (scan lookupRow) Scan(dest ...any) error { return scan(dest...) }

// A provider session lives on the machine that made it. A claim offers the
// prior session only to a Run on that same runtime; on any other — a Task
// handed to a colleague, or moved to another of one's own computers — it
// keeps just the watermark, so the daemon reports lost continuity instead of
// receiving a session id its provider home never held.
func TestBuildClaimedRunResponseOffersThePriorSessionOnlyOnItsRuntime(t *testing.T) {
	t.Parallel()

	runtimeID := uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f27a0")
	run := db.Run{
		ID:             uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f27a1"),
		CompanyID:      uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f27a2"),
		AgentID:        uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f27a3"),
		ConversationID: uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f27a4"),
		RuntimeID:      runtimeID,
	}
	tests := []struct {
		name        string
		prior       priorSessionDBTX
		wantSession string
	}{
		{
			name:        "same runtime",
			prior:       priorSessionDBTX{sessionID: "session-1", throughSeq: 7, runtimeID: runtimeID},
			wantSession: "session-1",
		},
		{
			name: "another runtime",
			prior: priorSessionDBTX{
				sessionID: "session-1", throughSeq: 7,
				runtimeID: uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f27a5"),
			},
		},
		{
			name: "same runtime with its rollout missing",
			prior: priorSessionDBTX{
				sessionID: "session-1", throughSeq: 7, rolloutMissing: true, runtimeID: runtimeID,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler := &Handler{Queries: db.New(&test.prior)}
			got := handler.buildClaimedRunResponse(context.Background(), run)
			if got.PriorSessionID != test.wantSession || got.PriorSessionThroughSeq != 7 {
				t.Fatalf(
					"prior session = (%q, %d), want (%q, 7)",
					got.PriorSessionID, got.PriorSessionThroughSeq, test.wantSession,
				)
			}
		})
	}
}

// The daemon's cancellation poll learns why a cancelled run was cancelled,
// which is how it tells a confirmed handoff (its worktree is superseded) from
// any other cancel (ADR 0014). A failed run's error is provider text and never
// travels as a reason.
func TestDaemonRunStatusResponseCarriesOnlyACancellationReason(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		run  db.Run
		want daemonRunStatusResponse
	}{
		{
			name: "handoff cancel",
			run:  db.Run{Status: "cancelled", Error: pgtype.Text{String: "task_reassigned", Valid: true}},
			want: daemonRunStatusResponse{Status: "cancelled", Reason: "task_reassigned"},
		},
		{
			name: "cancel without a reason",
			run:  db.Run{Status: "cancelled"},
			want: daemonRunStatusResponse{Status: "cancelled"},
		},
		{
			name: "provider failure",
			run:  db.Run{Status: "failed", Error: pgtype.Text{String: "codex exited 1", Valid: true}},
			want: daemonRunStatusResponse{Status: "failed"},
		},
		{name: "running", run: db.Run{Status: "running"}, want: daemonRunStatusResponse{Status: "running"}},
	} {
		if got := newDaemonRunStatusResponse(test.run); got != test.want {
			t.Errorf("%s: newDaemonRunStatusResponse() = %+v, want %+v", test.name, got, test.want)
		}
	}
}

func TestSkillChangeRetiresProviderSessionAndPreservesChatWatermark(t *testing.T) {
	t.Parallel()
	runtimeID := uuid.New()
	for _, test := range []struct {
		old, current string
		resume       bool
	}{
		{"", "", true}, {"a", "a", true}, {"", "a", false}, {"a", "b", false}, {"a", "", false},
	} {
		prior := priorSessionDBTX{
			sessionID:    "session",
			runtimeID:    runtimeID,
			throughSeq:   27,
			skillSetHash: test.old,
		}
		handler := Handler{Queries: db.New(&prior)}
		raw, _ := json.Marshal(map[string]any{"agent": map[string]any{"skillSetHash": test.current}})
		got := handler.buildClaimedRunResponse(
			context.Background(),
			db.Run{
				ID:             uuid.New(),
				CompanyID:      uuid.New(),
				AgentID:        uuid.New(),
				ConversationID: uuid.New(),
				RuntimeID:      runtimeID,
				Context:        raw,
			},
		)
		if (got.PriorSessionID != "") != test.resume || got.PriorSessionThroughSeq != 27 {
			t.Fatalf(
				"%q -> %q: session=%q watermark=%d",
				test.old,
				test.current,
				got.PriorSessionID,
				got.PriorSessionThroughSeq,
			)
		}
	}
}
