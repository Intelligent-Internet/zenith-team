//go:build integration

package social

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/config"
	"boardly.dev/boardly/server/internal/db"
	adapter "boardly.dev/boardly/server/internal/infra/social"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func TestCredentialMaintenanceRotationAndReconnectFence(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := context.Background()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSocialService(
		store,
		config.SocialConfig{
			Platforms: map[string]config.SocialPlatformConfig{"x": {Enabled: true, ClientID: "client"}},
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	s.vault, _ = newVault(bytes.Repeat([]byte{8}, 32))
	var session uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO auth_sessions(user_id,token_prefix,token_hash,expires_at) VALUES($1,'maintain',decode('7890','hex'),now()+interval '1 hour') RETURNING id`, f.UserA).
		Scan(&session); err != nil {
		t.Fatal(err)
	}
	p := auth.UserPrincipal{UserID: f.UserA, SessionID: session}
	token := adapter.Token{
		AccessToken:  "old",
		RefreshToken: "refresh-old",
		Scopes:       "tweet.write",
		ExpiresAt:    time.Now().Add(time.Minute),
	}
	account := adapter.Account{ExternalID: "mine", DisplayName: "Mine", Token: token}
	save := func() {
		t.Helper()
		if err := s.saveAccount(ctx, p.UserID, session, "x", account); err != nil {
			t.Fatal(err)
		}
	}
	save()
	rows, err := s.List(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.MustParse(rows[0].ID)
	var calls atomic.Int32
	var code atomic.Int32
	code.Store(200)
	s.providerHTTP = &http.Client{Transport: socialTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/2/oauth2/token" {
			t.Errorf("unexpected provider path: %s", r.URL.Path)
		}
		calls.Add(1)
		body := `{"access_token":"new","refresh_token":"refresh-new","expires_in":7200}`
		if code.Load() == 400 {
			body = `{"error":"invalid_grant"}`
		} else if code.Load() == 503 {
			body = `{"error":"unavailable"}`
		}
		return &http.Response{
			StatusCode: int(code.Load()),
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})}
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			if err := s.MaintainCredentials(ctx); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	row, err := store.Queries().
		GetSocialConnection(ctx, generated.GetSocialConnectionParams{ID: id, UserID: p.UserID})
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := s.token(row)
	if err != nil || refreshed.RefreshToken != "refresh-new" || refreshed.Scopes != "tweet.write" ||
		calls.Load() != 1 {
		t.Fatalf("refresh rotation failed: err=%v, calls=%d", err, calls.Load())
	}
	if bytes.Contains(row.Credentials, []byte("refresh-new")) {
		t.Fatal("plaintext refreshed credential")
	}

	save()
	lease, err := s.claim(ctx, p.UserID, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Disconnect(ctx, p, id.String(), 2); !errors.Is(err, ErrConflict) {
		t.Fatalf("disconnect during refresh: %v", err)
	}
	// A reconnect replaces the grant and fences out the earlier refresh result.
	account.Token.AccessToken = "reconnected"
	save()
	if _, err := s.maintain(ctx, lease); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale refresh result: %v", err)
	}
	row, err = store.Queries().
		GetSocialConnection(ctx, generated.GetSocialConnectionParams{ID: id, UserID: p.UserID})
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.token(row)
	if err != nil || current.AccessToken != "reconnected" {
		t.Fatal("reconnected credentials were overwritten")
	}

	code.Store(503)
	if err := s.MaintainCredentials(ctx); err == nil {
		t.Fatal("temporary failure was swallowed")
	}
	row, err = store.Queries().
		GetSocialConnection(ctx, generated.GetSocialConnectionParams{ID: id, UserID: p.UserID})
	if err != nil {
		t.Fatal(err)
	}
	if row.ReconnectRequired || !row.MaintenanceAt.Valid || !row.MaintenanceAt.Time.After(time.Now()) {
		t.Fatal("temporary error should retry without discarding grant")
	}
	save()
	code.Store(400)
	if err := s.MaintainCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	row, err = store.Queries().
		GetSocialConnection(ctx, generated.GetSocialConnectionParams{ID: id, UserID: p.UserID})
	if err != nil {
		t.Fatal(err)
	}
	if !row.ReconnectRequired || row.MaintenanceAt.Valid || row.OperationToken.Valid {
		t.Fatal("revoked grant must stop refresh and release its lease")
	}
}
