//go:build integration

package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/company"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

func TestStudioEditorHTTPUsesDurableHumanAuthority(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
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
			Secret:     []byte("p1b-synthetic-secret-at-least-32-bytes"),
			AccessTTL:  15 * time.Minute,
			RefreshTTL: time.Hour,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	service, err := auth.NewAuthService(
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
	var sid uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO auth_sessions(user_id,token_prefix,token_hash,expires_at) VALUES($1,'test',gen_random_bytes(32),now()+interval '1 hour') RETURNING id`, fixture.UserA).
		Scan(&sid); err != nil {
		t.Fatal(err)
	}
	token, _, err := tokens.IssueAccess(fixture.UserA, sid)
	if err != nil {
		t.Fatal(err)
	}
	h := New(
		Handler{
			AuthService:         service,
			CompanyService:      companies,
			StudioEditorService: conversation.NewStudioEditorService(store, nil, uuid.New()),
		},
	)
	server := httptest.NewServer(h)
	defer server.Close()
	path := server.URL + "/api/v1/companies/" + fixture.CompanyA.String() + "/conversations/" + fixture.DirectConversationA.String() + "/studio/operations"
	body := `{"schemaVersion":2,"requestId":"` + uuid.NewString() + `","runtimeId":"` + fixture.RuntimeA.String() + `","operation":"resolveContext","payload":{}}`
	call := func(token, body string, want int, code string) {
		t.Helper()
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Code      string `json:"code"`
			RequestID string `json:"requestId"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		if response.StatusCode != want || result.Code != code || result.RequestID == "" {
			t.Fatalf("HTTP %d %s; want %d %s", response.StatusCode, raw, want, code)
		}
	}
	call(token, body, 409, "studio_editor_unsupported")
	call(
		token,
		strings.Replace(body, `"payload":{}`, `"payload":{},"origin":"http://localhost"`, 1),
		400,
		"invalid_request",
	)
	call(
		token,
		strings.Replace(body, `"payload":{}`, `"payload":{"actorUserId":"`+fixture.UserA.String()+`"}`, 1),
		422,
		"studio_editor_invalid",
	)
	call(token+"broken", body, 401, "session_invalid")
	dbtest.MustExec(t, ctx, pool, `UPDATE auth_sessions SET revoked_at=now() WHERE id=$1`, sid)
	call(token, body, 401, "session_invalid")
}
