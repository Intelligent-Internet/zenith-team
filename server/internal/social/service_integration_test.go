//go:build integration

package social

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/config"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	adapter "github.com/Intelligent-Internet/zenith-team/server/internal/infra/social"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestDisconnectRequiresSuccessfulProviderRevocation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"provider unavailable", 503, `{"error":"unavailable"}`},
		{"invalid client credentials", 401, `{"error":"invalid_client"}`},
		{"forbidden request", 403, `{"error":"forbidden"}`},
		{"malformed revoke request", 400, `{"error":"invalid_request"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			pool, _ := dbtest.StartPostgres(t)
			if err := db.MigrateUp(ctx, pool); err != nil {
				t.Fatal(err)
			}
			fixture := dbtest.SeedFixture(t, ctx, pool)
			store, err := db.NewStore(pool)
			if err != nil {
				t.Fatal(err)
			}
			s, err := NewSocialService(
				store,
				config.SocialConfig{
					Platforms: map[string]config.SocialPlatformConfig{
						"x": {Enabled: true, ClientID: "client"},
					},
				},
				nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			s.vault, err = newVault(bytes.Repeat([]byte{9}, 32))
			if err != nil {
				t.Fatal(err)
			}
			var sessionID uuid.UUID
			if err := pool.QueryRow(ctx, `INSERT INTO auth_sessions(user_id,token_prefix,token_hash,expires_at) VALUES ($1,'disconnect',decode('1234','hex'),now()+interval '1 hour') RETURNING id`, fixture.UserA).
				Scan(&sessionID); err != nil {
				t.Fatal(err)
			}
			p := auth.UserPrincipal{UserID: fixture.UserA, SessionID: sessionID}
			token := adapter.Token{AccessToken: "access", RefreshToken: "refresh", Scopes: "tweet.write"}
			account := adapter.Account{
				ExternalID:  "mine",
				DisplayName: "Mine",
				Token:       token,
				Targets: []adapter.Target{
					{ExternalID: "mine", DisplayName: "Mine", Type: "account", Token: token},
				},
			}
			if err := s.saveAccount(ctx, p.UserID, sessionID, "x", account); err != nil {
				t.Fatal(err)
			}
			connections, err := s.List(ctx, p)
			if err != nil || len(connections) != 1 {
				t.Fatalf("connections = %v, err = %v", connections, err)
			}
			connection := connections[0]
			s.providerHTTP = &http.Client{
				Transport: socialTransport(func(*http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: tc.status,
						Body:       io.NopCloser(strings.NewReader(tc.body)),
						Header:     make(http.Header),
					}, nil
				}),
			}
			if err := s.Disconnect(
				ctx,
				p,
				connection.ID,
				connection.Revision,
			); !errors.Is(
				err,
				ErrProvider,
			) {
				t.Fatalf("disconnect = %v, want provider failure", err)
			}
			row, err := store.Queries().
				GetSocialConnection(ctx, generated.GetSocialConnectionParams{ID: uuid.MustParse(connection.ID), UserID: p.UserID})
			if err != nil {
				t.Fatalf("connection disappeared after rejected revoke: %v", err)
			}
			if row.OperationToken.Valid {
				t.Fatal("failed revoke kept its operation lease")
			}
			retained, err := s.token(row)
			if err != nil || retained.AccessToken != token.AccessToken ||
				retained.RefreshToken != token.RefreshToken {
				t.Fatal("failed revoke discarded credentials")
			}
			disconnectCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			s.providerHTTP = &http.Client{
				Transport: socialTransport(func(r *http.Request) (*http.Response, error) {
					if err := r.ParseForm(); err != nil {
						t.Fatal(err)
					}
					if r.Form.Get("token_type_hint") == "access_token" {
						cancel()
					}
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`{"revoked":true}`)),
						Header:     make(http.Header),
					}, nil
				}),
			}
			if err := s.Disconnect(disconnectCtx, p, connection.ID, connection.Revision); err != nil {
				t.Fatal(err)
			}
			connections, err = s.List(ctx, p)
			if err != nil || len(connections) != 0 {
				t.Fatalf("connections after retry = %v, err = %v", connections, err)
			}
			targets, err := store.Queries().ListSocialTargets(ctx, p.UserID)
			if err != nil || len(targets) != 0 {
				t.Fatalf("targets after disconnect = %v, err = %v", targets, err)
			}
		})
	}
}

func TestSocialOAuthConnectReconnectAndDisconnect(t *testing.T) {
	t.Parallel()
	for _, desktop := range []bool{false, true} {
		name := "web"
		if desktop {
			name = "desktop"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			pool, _ := dbtest.StartPostgres(t)
			if err := db.MigrateUp(ctx, pool); err != nil {
				t.Fatal(err)
			}
			fixture := dbtest.SeedFixture(t, ctx, pool)
			store, err := db.NewStore(pool)
			if err != nil {
				t.Fatal(err)
			}
			s, err := NewSocialService(
				store,
				config.SocialConfig{
					CallbackOrigin: "https://api.test",
					Platforms: map[string]config.SocialPlatformConfig{
						"x": {Enabled: true, ClientID: "client"},
					},
				},
				nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			s.vault, err = newVault(bytes.Repeat([]byte{5}, 32))
			if err != nil {
				t.Fatal(err)
			}
			var sessionID uuid.UUID
			if err := pool.QueryRow(ctx, `INSERT INTO auth_sessions(user_id,token_prefix,token_hash,expires_at) VALUES ($1,'oauth',decode('1234','hex'),now()+interval '1 hour') RETURNING id`, fixture.UserA).
				Scan(&sessionID); err != nil {
				t.Fatal(err)
			}
			p := auth.UserPrincipal{UserID: fixture.UserA, SessionID: sessionID}
			var challenge string
			revocations := 0
			s.providerHTTP = &http.Client{
				Transport: socialTransport(func(r *http.Request) (*http.Response, error) {
					body := ""
					switch r.URL.Path {
					case "/2/oauth2/token":
						if err := r.ParseForm(); err != nil {
							t.Fatal(err)
						}
						hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
						if base64.RawURLEncoding.EncodeToString(hash[:]) != challenge ||
							r.Form.Get("code") != "approved-code" ||
							r.Form.Get(
								"redirect_uri",
							) != "https://api.test/api/v1/social/oauth/x/callback" {
							t.Error("token exchange lost the OAuth binding")
						}
						body = `{"access_token":"access","refresh_token":"refresh","scope":"tweet.write offline.access","expires_in":7200}`
					case "/2/users/me":
						body = `{"data":{"id":"mine","name":"My account","username":"my-account"}}`
					case "/2/oauth2/revoke":
						revocations++
						body = `{"revoked":true}`
					default:
						t.Errorf("unexpected provider path = %s", r.URL.Path)
					}
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(body)),
						Header:     make(http.Header),
					}, nil
				}),
			}
			var connection Connection
			for revision := int64(1); revision <= 2; revision++ {
				var started StartResult
				wantReturn := ""
				if desktop {
					started, err = s.StartDesktop(
						ctx,
						p,
						"x",
						DesktopInput{
							RedirectURI: "http://127.0.0.1:34567/social/callback",
							State:       strings.Repeat("a", 43),
						},
					)
					if err != nil {
						t.Fatal(err)
					}
					launch, _ := url.Parse(started.AuthorizationURL)
					started, err = s.LaunchDesktop(
						ctx,
						"x",
						launch.Query().Get("state"),
						launch.Query().Get("ticket"),
					)
					wantReturn = "http://127.0.0.1:34567/social/callback?state=" + strings.Repeat(
						"a",
						43,
					)
				} else {
					started, err = s.Start(ctx, p, "x")
				}
				if err != nil {
					t.Fatal(err)
				}
				authorize, _ := url.Parse(started.AuthorizationURL)
				challenge = authorize.Query().Get("code_challenge")
				flow := CompleteInput{
					Platform: "x",
					State:    authorize.Query().Get("state"),
					Binding:  started.Binding,
					Code:     "approved-code",
				}
				gotReturn, err := s.Complete(ctx, flow)
				if err != nil || gotReturn != wantReturn {
					t.Fatalf("callback return = %q, err = %v, want %q", gotReturn, err, wantReturn)
				}
				if _, err := s.Complete(ctx, flow); !errors.Is(err, ErrInvalidFlow) {
					t.Fatalf("replayed callback = %v", err)
				}
				items, err := s.List(ctx, p)
				if err != nil || len(items) != 1 {
					t.Fatalf("connections = %v, err = %v", items, err)
				}
				if items[0].Revision != revision || !items[0].CanPublish ||
					len(items[0].Targets) != 1 ||
					(connection.ID != "" && items[0].ID != connection.ID) {
					t.Fatalf("connection after OAuth = %+v", items[0])
				}
				connection = items[0]
			}
			if err := s.Disconnect(
				ctx,
				p,
				connection.ID,
				1,
			); !errors.Is(err, ErrConflict) ||
				revocations != 0 {
				t.Fatalf("stale disconnect = %v, revocations = %d", err, revocations)
			}
			if err := s.Disconnect(ctx, p, connection.ID, connection.Revision); err != nil {
				t.Fatal(err)
			}
			items, err := s.List(ctx, p)
			if err != nil || len(items) != 0 || revocations != 2 {
				t.Fatalf(
					"disconnect: accounts = %v, err = %v, revocations = %d",
					items,
					err,
					revocations,
				)
			}
		})
	}
}

func TestSocialOwnershipEncryptionAndOAuthReplay(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := context.Background()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSocialService(
		store,
		config.SocialConfig{
			CallbackOrigin: "https://api.test",
			Platforms: map[string]config.SocialPlatformConfig{
				"x":        {Enabled: true, ClientID: "client"},
				"linkedin": {ClientID: "linkedin"},
			},
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	s.vault, err = newVault(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	var sessionID uuid.UUID
	err = pool.QueryRow(ctx, `INSERT INTO auth_sessions(user_id,token_prefix,token_hash,expires_at) VALUES ($1,'test',decode('1234','hex'),now()+interval '1 hour') RETURNING id`, fixture.UserA).
		Scan(&sessionID)
	if err != nil {
		t.Fatal(err)
	}
	a := auth.UserPrincipal{UserID: fixture.UserA, SessionID: sessionID}
	b := auth.UserPrincipal{UserID: fixture.UserB}
	started, err := s.Start(ctx, a, "x")
	if err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse(started.AuthorizationURL)
	flow := CompleteInput{
		Platform:      "x",
		State:         target.Query().Get("state"),
		Binding:       started.Binding,
		ProviderError: "access_denied",
	}
	wrong := flow
	wrong.Binding = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
	if _, err := s.Complete(ctx, wrong); !errors.Is(err, ErrInvalidFlow) {
		t.Fatalf("wrong browser = %v", err)
	}
	if _, err := s.Complete(ctx, flow); !errors.Is(err, ErrProvider) {
		t.Fatalf("provider denial = %v", err)
	}
	if _, err := s.Complete(ctx, flow); !errors.Is(err, ErrInvalidFlow) {
		t.Fatalf("replayed flow = %v", err)
	}
	desktop, err := s.StartDesktop(
		ctx,
		a,
		"x",
		DesktopInput{
			RedirectURI: "http://127.0.0.1:34567/social/callback",
			State:       "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	launch, _ := url.Parse(desktop.AuthorizationURL)
	state, ticket := launch.Query().Get("state"), launch.Query().Get("ticket")
	if _, err := s.LaunchDesktop(
		ctx,
		"x",
		state,
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	); !errors.Is(
		err,
		ErrInvalidFlow,
	) {
		t.Fatalf("wrong launch ticket: %v", err)
	}
	browser, err := s.LaunchDesktop(ctx, "x", state, ticket)
	if err != nil || browser.Binding != ticket {
		t.Fatalf("browser launch: %v", err)
	}
	if _, err := s.LaunchDesktop(ctx, "x", state, ticket); !errors.Is(err, ErrInvalidFlow) {
		t.Fatalf("launch replay: %v", err)
	}
	returnURI, err := s.Complete(
		ctx,
		CompleteInput{Platform: "x", State: state, Binding: ticket, ProviderError: "access_denied"},
	)
	if !errors.Is(err, ErrProvider) ||
		returnURI != "http://127.0.0.1:34567/social/callback?state=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("desktop denial return: %q %v", returnURI, err)
	}
	account := adapter.Account{
		ExternalID:  "42",
		DisplayName: "Test account",
		Token: adapter.Token{
			AccessToken: "never-expose-this-token",
			ExpiresAt:   time.Now().Add(time.Hour),
		},
		Targets: []adapter.Target{
			{
				ExternalID:  "42",
				DisplayName: "Test account",
				Type:        "account",
				Token:       adapter.Token{AccessToken: "target-secret"},
			},
		},
	}
	if err := s.saveAccount(ctx, a.UserID, sessionID, "linkedin", account); err != nil {
		t.Fatal(err)
	}
	mine, err := s.List(ctx, a)
	if err != nil || len(mine) != 1 {
		t.Fatalf("owner list = %v %v", mine, err)
	}
	others, err := s.List(ctx, b)
	if err != nil || len(others) != 0 {
		t.Fatalf("other list = %v %v", others, err)
	}
	data, _ := json.Marshal(mine)
	if bytes.Contains(data, []byte("token")) || bytes.Contains(data, []byte("secret")) {
		t.Fatal("credentials reached the public response")
	}
	row, err := store.Queries().
		GetSocialConnection(ctx, generated.GetSocialConnectionParams{ID: uuid.MustParse(mine[0].ID), UserID: a.UserID})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(row.Credentials, []byte(account.Token.AccessToken)) {
		t.Fatal("token stored as plaintext")
	}
	if err := s.Disconnect(ctx, b, mine[0].ID, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user disconnect = %v", err)
	}
	if err := s.Disconnect(ctx, a, mine[0].ID, 99); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale disconnect = %v", err)
	}
	if err := s.Disconnect(ctx, a, mine[0].ID, 1); err != nil {
		t.Fatal(err)
	}
	started, err = s.Start(ctx, a, "x")
	if err != nil {
		t.Fatal(err)
	}
	target, _ = url.Parse(started.AuthorizationURL)
	if _, err := pool.Exec(ctx, `UPDATE auth_sessions SET revoked_at=now() WHERE id=$1`, sessionID); err != nil {
		t.Fatal(err)
	}
	flow.State, flow.Binding = target.Query().Get("state"), started.Binding
	if _, err := s.Complete(ctx, flow); !errors.Is(err, ErrInvalidFlow) {
		t.Fatalf("revoked session callback = %v", err)
	}
	if err := s.saveAccount(ctx, a.UserID, sessionID, "linkedin", account); !errors.Is(err, ErrInvalidFlow) {
		t.Fatalf("revoked session save = %v", err)
	}
}

func TestSocialOAuthSessionRotation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		stage      string
		reason     string
		expired    bool
		disabled   bool
		wantReject bool
	}{
		{name: "rotation before desktop launch", stage: "launch"},
		{name: "rotations before callback", stage: "callback"},
		{name: "rotation during exchange", stage: "exchange"},
		{name: "logout before launch", stage: "launch", reason: "logout", wantReject: true},
		{name: "logout before callback", stage: "callback", reason: "logout", wantReject: true},
		{name: "logout during exchange", stage: "exchange", reason: "logout", wantReject: true},
		{name: "refresh reuse", stage: "callback", reason: "refresh_token_reuse", wantReject: true},
		{name: "password reset", stage: "callback", reason: "password_reset", wantReject: true},
		{name: "administrative revocation", stage: "callback", reason: "administrative_revocation", wantReject: true},
		{name: "expired descendant", stage: "callback", expired: true, wantReject: true},
		{name: "disabled user", stage: "callback", disabled: true, wantReject: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			pool, _ := dbtest.StartPostgres(t)
			if err := db.MigrateUp(ctx, pool); err != nil {
				t.Fatal(err)
			}
			fixture := dbtest.SeedFixture(t, ctx, pool)
			store, err := db.NewStore(pool)
			if err != nil {
				t.Fatal(err)
			}
			s, err := NewSocialService(
				store,
				config.SocialConfig{
					CallbackOrigin: "https://api.test",
					Platforms: map[string]config.SocialPlatformConfig{
						"x": {Enabled: true, ClientID: "client"},
					},
				},
				nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			s.vault, err = newVault(bytes.Repeat([]byte{3}, 32))
			if err != nil {
				t.Fatal(err)
			}
			var sessionID uuid.UUID
			if err := pool.QueryRow(ctx, `INSERT INTO auth_sessions(user_id,token_prefix,token_hash,expires_at) VALUES ($1,'oauth',decode('1234','hex'),now()+interval '1 hour') RETURNING id`, fixture.UserA).
				Scan(&sessionID); err != nil {
				t.Fatal(err)
			}
			p := auth.UserPrincipal{UserID: fixture.UserA, SessionID: sessionID}
			started, err := s.StartDesktop(
				ctx,
				p,
				"x",
				DesktopInput{
					RedirectURI: "http://127.0.0.1:34567/social/callback",
					State:       strings.Repeat("a", 43),
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			launch, _ := url.Parse(started.AuthorizationURL)
			state, ticket := launch.Query().Get("state"), launch.Query().Get("ticket")
			rotate := func() {
				// Several refreshes must preserve the original flow's session family.
				for range 3 {
					if err := pool.QueryRow(ctx, `WITH parent AS (
						UPDATE auth_sessions SET revoked_at=now(),revocation_reason='refresh_rotated' WHERE id=$1 RETURNING id,user_id
					) INSERT INTO auth_sessions(user_id,token_prefix,token_hash,expires_at,rotated_from_session_id)
					SELECT user_id,'rotated',gen_random_bytes(32),now()+interval '1 hour',id FROM parent RETURNING id`, sessionID).Scan(&sessionID); err != nil {
						t.Fatal(err)
					}
				}
				if tc.reason != "" {
					if _, err := pool.Exec(
						ctx,
						`UPDATE auth_sessions SET revoked_at=now(),revocation_reason=$2 WHERE id=$1`,
						sessionID,
						tc.reason,
					); err != nil {
						t.Fatal(err)
					}
				}
				if tc.expired {
					if _, err := pool.Exec(
						ctx,
						`UPDATE auth_sessions SET expires_at=now()-interval '1 second' WHERE id=$1`,
						sessionID,
					); err != nil {
						t.Fatal(err)
					}
				}
				if tc.disabled {
					if _, err := pool.Exec(
						ctx,
						`UPDATE users SET disabled_at=now() WHERE id=$1`,
						p.UserID,
					); err != nil {
						t.Fatal(err)
					}
				}
				// A separate login must never rescue an invalidated OAuth flow.
				if _, err := pool.Exec(
					ctx,
					`INSERT INTO auth_sessions(user_id,token_prefix,token_hash,expires_at) VALUES ($1,'unrelated',gen_random_bytes(32),now()+interval '1 hour')`,
					p.UserID,
				); err != nil {
					t.Fatal(err)
				}
			}
			if tc.stage == "launch" {
				rotate()
			}
			_, err = s.LaunchDesktop(ctx, "x", state, ticket)
			if tc.stage == "launch" && tc.wantReject {
				if !errors.Is(err, ErrInvalidFlow) {
					t.Fatalf("invalidated launch = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.stage == "callback" {
				rotate()
			}
			requests := 0
			s.providerHTTP = &http.Client{
				Transport: socialTransport(func(r *http.Request) (*http.Response, error) {
					requests++
					body := `{"data":{"id":"mine","name":"Mine","username":"my-account"}}`
					if r.URL.Path == "/2/oauth2/token" {
						if tc.stage == "exchange" {
							rotate()
						}
						body = `{"access_token":"access","refresh_token":"refresh","scope":"tweet.write","expires_in":7200}`
					}
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(body)),
						Header:     make(http.Header),
					}, nil
				}),
			}
			returnURI, err := s.Complete(
				ctx,
				CompleteInput{Platform: "x", State: state, Binding: ticket, Code: "approved-code"},
			)
			if tc.wantReject {
				if !errors.Is(err, ErrInvalidFlow) {
					t.Fatalf("invalidated callback = %v", err)
				}
				if tc.stage != "exchange" && requests != 0 {
					t.Fatalf("invalidated callback sent %d provider requests", requests)
				}
			} else if err != nil || returnURI != "http://127.0.0.1:34567/social/callback?state="+strings.Repeat("a", 43) {
				t.Fatalf("rotated callback return = %q, err = %v", returnURI, err)
			}
			connections, err := store.Queries().ListSocialConnections(ctx, p.UserID)
			if err != nil {
				t.Fatal(err)
			}
			wantCount := 1
			if tc.wantReject {
				wantCount = 0
			}
			if len(connections) != wantCount {
				t.Fatalf("saved accounts = %d, want %d", len(connections), wantCount)
			}
		})
	}
}
