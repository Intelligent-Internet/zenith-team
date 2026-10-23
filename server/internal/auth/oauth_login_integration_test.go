//go:build integration

package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestOAuthLoginFlowSchemaHasOnlyBoundedMechanicalState(t *testing.T) {
	pool, _ := startAuthPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rows, err := pool.Query(ctx, `
		SELECT column_name
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = 'oauth_login_flows'
		ORDER BY ordinal_position`)
	if err != nil {
		t.Fatalf("query oauth_login_flows columns: %v", err)
	}
	defer rows.Close()

	var columns []string
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			t.Fatalf("scan oauth_login_flows column: %v", err)
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate oauth_login_flows columns: %v", err)
	}

	want := []string{
		"id",
		"provider",
		"client",
		"state_hash",
		"nonce_hash",
		"browser_binding_hash",
		"redirect_uri",
		"return_path",
		"code_challenge",
		"client_metadata",
		"expires_at",
		"created_at",
	}
	if len(columns) != len(want) {
		t.Fatalf("oauth_login_flows columns = %v, want %v", columns, want)
	}
	for index := range want {
		if columns[index] != want[index] {
			t.Fatalf("oauth_login_flows columns = %v, want %v", columns, want)
		}
	}

	var hasExpiryIndex bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM pg_indexes
			WHERE schemaname = 'public'
			  AND tablename = 'oauth_login_flows'
			  AND indexname = 'oauth_login_flows_expiry_idx'
		)`).Scan(&hasExpiryIndex); err != nil {
		t.Fatalf("query oauth_login_flows expiry index: %v", err)
	}
	if !hasExpiryIndex {
		t.Fatal("oauth_login_flows_expiry_idx is missing")
	}
}

func TestOAuthRepositoryConsumesMatchingFlowsWithoutConsumingMismatches(t *testing.T) {
	pool, store := startAuthPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	web := newWebOAuthFlow(t, 2)
	webID, err := createOAuthLoginFlow(ctx, store.Queries(), web)
	if err != nil {
		t.Fatalf("CreateOAuthLoginFlow(web) error = %v", err)
	}
	webMatch := WebOAuthFlowConsumption{
		Provider:           web.Provider,
		StateHash:          web.StateHash,
		BrowserBindingHash: web.BrowserBindingHash,
		RedirectURI:        web.RedirectURI,
	}
	wrongWeb := []WebOAuthFlowConsumption{
		{
			Provider:           web.Provider,
			StateHash:          web.StateHash,
			BrowserBindingHash: bytes.Repeat([]byte{99}, 32),
			RedirectURI:        web.RedirectURI,
		},
		{
			Provider:           web.Provider,
			StateHash:          web.StateHash,
			BrowserBindingHash: web.BrowserBindingHash,
			RedirectURI:        "https://api.zenith-team.test/wrong",
		},
		{
			Provider:           web.Provider,
			StateHash:          bytes.Repeat([]byte{98}, 32),
			BrowserBindingHash: web.BrowserBindingHash,
			RedirectURI:        web.RedirectURI,
		},
	}
	for _, mismatch := range wrongWeb {
		if _, err := consumeWebOAuthLoginFlow(
			ctx,
			store.Queries(),
			mismatch,
		); !errors.Is(
			err,
			ErrOAuthFlowInvalid,
		) {
			t.Fatalf("ConsumeWebOAuthLoginFlow(mismatch) error = %v", err)
		}
	}
	consumedWeb, err := consumeWebOAuthLoginFlow(ctx, store.Queries(), webMatch)
	if err != nil {
		t.Fatalf("ConsumeWebOAuthLoginFlow(valid) error = %v", err)
	}
	if consumedWeb.ID != webID || consumedWeb.ReturnPath == nil || *consumedWeb.ReturnPath != "/onboarding" {
		t.Fatalf("consumed web flow = %#v", consumedWeb)
	}
	if _, err := consumeWebOAuthLoginFlow(ctx, store.Queries(), webMatch); !errors.Is(err, ErrOAuthFlowInvalid) {
		t.Fatalf("ConsumeWebOAuthLoginFlow(replay) error = %v", err)
	}

	desktop := newDesktopOAuthFlow(t, 3)
	desktopID, err := createOAuthLoginFlow(ctx, store.Queries(), desktop)
	if err != nil {
		t.Fatalf("CreateOAuthLoginFlow(desktop) error = %v", err)
	}
	desktopMatch := DesktopOAuthFlowConsumption{
		ID:            desktopID,
		Provider:      desktop.Provider,
		StateHash:     desktop.StateHash,
		RedirectURI:   desktop.RedirectURI,
		CodeChallenge: *desktop.CodeChallenge,
	}
	wrongDesktop := []DesktopOAuthFlowConsumption{
		{
			ID:            desktopID,
			Provider:      desktop.Provider,
			StateHash:     bytes.Repeat([]byte{97}, 32),
			RedirectURI:   desktop.RedirectURI,
			CodeChallenge: *desktop.CodeChallenge,
		},
		{
			ID:            desktopID,
			Provider:      desktop.Provider,
			StateHash:     desktop.StateHash,
			RedirectURI:   "http://127.0.0.1:65530/",
			CodeChallenge: *desktop.CodeChallenge,
		},
		{
			ID:            desktopID,
			Provider:      desktop.Provider,
			StateHash:     desktop.StateHash,
			RedirectURI:   desktop.RedirectURI,
			CodeChallenge: strings.Repeat("B", 43),
		},
	}
	for _, mismatch := range wrongDesktop {
		if _, err := consumeDesktopOAuthLoginFlow(
			ctx,
			store.Queries(),
			mismatch,
		); !errors.Is(
			err,
			ErrOAuthFlowInvalid,
		) {
			t.Fatalf("ConsumeDesktopOAuthLoginFlow(mismatch) error = %v", err)
		}
	}
	consumedDesktop, err := consumeDesktopOAuthLoginFlow(ctx, store.Queries(), desktopMatch)
	if err != nil {
		t.Fatalf("ConsumeDesktopOAuthLoginFlow(valid) error = %v", err)
	}
	if consumedDesktop.ID != desktopID || consumedDesktop.CodeChallenge == nil ||
		*consumedDesktop.CodeChallenge != *desktop.CodeChallenge {
		t.Fatalf("consumed desktop flow = %#v", consumedDesktop)
	}

	expired := newWebOAuthFlow(t, 4)
	expiredID, err := createOAuthLoginFlow(ctx, store.Queries(), expired)
	if err != nil {
		t.Fatalf("CreateOAuthLoginFlow(expired fixture) error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE oauth_login_flows
		SET created_at = clock_timestamp() - INTERVAL '2 hours',
		    expires_at = clock_timestamp() - INTERVAL '1 hour'
		WHERE id = $1`, expiredID); err != nil {
		t.Fatalf("expire oauth flow: %v", err)
	}
	if _, err := consumeWebOAuthLoginFlow(ctx, store.Queries(), WebOAuthFlowConsumption{
		Provider: expired.Provider, StateHash: expired.StateHash,
		BrowserBindingHash: expired.BrowserBindingHash, RedirectURI: expired.RedirectURI,
	}); !errors.Is(err, ErrOAuthFlowInvalid) {
		t.Fatalf("ConsumeWebOAuthLoginFlow(expired) error = %v", err)
	}
	var expiredStillStored bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM oauth_login_flows WHERE id = $1)`, expiredID).
		Scan(&expiredStillStored); err != nil {
		t.Fatalf("query expired flow: %v", err)
	}
	if !expiredStillStored {
		t.Fatal("an expired mismatch was consumed")
	}
}

func TestOAuthRepositoryAllowsExactlyOneConcurrentFlowConsumer(t *testing.T) {
	_, store := startAuthPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	flow := newWebOAuthFlow(t, 5)
	if _, err := createOAuthLoginFlow(ctx, store.Queries(), flow); err != nil {
		t.Fatalf("CreateOAuthLoginFlow() error = %v", err)
	}
	match := WebOAuthFlowConsumption{
		Provider: flow.Provider, StateHash: flow.StateHash,
		BrowserBindingHash: flow.BrowserBindingHash, RedirectURI: flow.RedirectURI,
	}

	start := make(chan struct{})
	errorsByConsumer := make([]error, 2)
	var wait sync.WaitGroup
	for index := range errorsByConsumer {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, errorsByConsumer[index] = consumeWebOAuthLoginFlow(ctx, store.Queries(), match)
		}()
	}
	close(start)
	wait.Wait()

	var succeeded, rejected int
	for _, err := range errorsByConsumer {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrOAuthFlowInvalid):
			rejected++
		default:
			t.Fatalf("concurrent consume error = %v", err)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("concurrent results = success:%d invalid:%d (%v)", succeeded, rejected, errorsByConsumer)
	}
}

func TestOAuthRepositoryLoginUsesProviderSubjectBeforeEmail(t *testing.T) {
	pool, store := startAuthPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	picture := "https://images.zenith-team.test/founder.png"
	locale := "en-US"
	hostedDomain := "zenith-team.test"
	webSession := newOAuthSession(t, 61)
	webSession.Client = ClientWeb
	first := OAuthLoginInput{
		Identity: VerifiedOAuthIdentity{
			Provider: "google",
			Subject:  "google-subject-stable",
			Email:    "founder@zenith-team.test",
			Profile: OAuthProfileSnapshot{
				Name:         "Zenith Team Founder",
				PictureURL:   &picture,
				Locale:       &locale,
				HostedDomain: &hostedDomain,
			},
		},
		InitialDisplayName: "Zenith Team Founder",
		Session:            webSession,
	}
	created, firstSessionID, err := loginWithOAuthAndCreateSession(ctx, store, first)
	if err != nil {
		t.Fatalf("LoginWithOAuthAndCreateSession(first) error = %v", err)
	}
	if created.ID == uuid.Nil || created.Email != first.Identity.Email || created.PasswordHash != "" ||
		created.Version != 1 {
		t.Fatalf("created OAuth user = %#v", created)
	}
	if created.AvatarURL == nil || *created.AvatarURL != picture {
		t.Fatalf("created OAuth user avatar = %#v", created.AvatarURL)
	}
	if _, err := activeSessionUser(ctx, store, firstSessionID, created.ID); err != nil {
		t.Fatalf("GetActiveSessionUser(first OAuth session) error = %v", err)
	}

	updatedPicture := "https://images.zenith-team.test/founder-new.png"
	second := OAuthLoginInput{
		Identity: VerifiedOAuthIdentity{
			Provider: "google",
			Subject:  first.Identity.Subject,
			Email:    "renamed@zenith-team.test",
			Profile: OAuthProfileSnapshot{
				Name:       "Provider Name Changed",
				PictureURL: &updatedPicture,
			},
		},
		InitialDisplayName: "Provider Name Changed",
		Session:            newOAuthSession(t, 62),
	}
	resolved, _, err := loginWithOAuthAndCreateSession(ctx, store, second)
	if err != nil {
		t.Fatalf("LoginWithOAuthAndCreateSession(second) error = %v", err)
	}
	if resolved.ID != created.ID || resolved.Email != first.Identity.Email || resolved.Version != created.Version {
		t.Fatalf("resolved OAuth user = %#v, first = %#v", resolved, created)
	}
	if resolved.DisplayName != created.DisplayName || resolved.AvatarURL == nil || *resolved.AvatarURL != picture {
		t.Fatalf("provider snapshot overwrote Zenith Team profile: first=%#v second=%#v", created, resolved)
	}

	var accountEmail string
	var profile json.RawMessage
	var accountCount, userCount int
	if err := pool.QueryRow(ctx, `
		SELECT email, profile
		FROM oauth_accounts
		WHERE provider = $1 AND subject = $2`, second.Identity.Provider, second.Identity.Subject).Scan(&accountEmail, &profile); err != nil {
		t.Fatalf("query OAuth account snapshot: %v", err)
	}
	if accountEmail != second.Identity.Email {
		t.Fatalf("OAuth account email = %q, want %q", accountEmail, second.Identity.Email)
	}
	var profileValues map[string]any
	if err := json.Unmarshal(profile, &profileValues); err != nil {
		t.Fatalf("decode OAuth profile snapshot: %v", err)
	}
	if len(profileValues) != 2 || profileValues["name"] != "Provider Name Changed" ||
		profileValues["picture"] != updatedPicture {
		t.Fatalf("OAuth profile snapshot = %#v", profileValues)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM oauth_accounts WHERE provider = $1 AND subject = $2`, second.Identity.Provider, second.Identity.Subject).
		Scan(&accountCount); err != nil {
		t.Fatalf("count OAuth accounts: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE id = $1`, created.ID).
		Scan(&userCount); err != nil {
		t.Fatalf("count OAuth users: %v", err)
	}
	if accountCount != 1 || userCount != 1 {
		t.Fatalf("identity cardinality = accounts:%d users:%d", accountCount, userCount)
	}
}

func TestOAuthRepositoryRejectsDisabledIdentity(t *testing.T) {
	pool, store := startAuthPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	linked := OAuthLoginInput{
		Identity: VerifiedOAuthIdentity{
			Provider: "google", Subject: "disabled-google-subject", Email: "disabled@zenith-team.test",
			Profile: OAuthProfileSnapshot{Name: "Disabled"},
		},
		InitialDisplayName: "Disabled",
		Session:            newOAuthSession(t, 65),
	}
	user, _, err := loginWithOAuthAndCreateSession(ctx, store, linked)
	if err != nil {
		t.Fatalf("LoginWithOAuthAndCreateSession(disabled fixture) error = %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE users SET disabled_at = clock_timestamp() WHERE id = $1`,
		user.ID,
	); err != nil {
		t.Fatalf("disable OAuth user: %v", err)
	}
	var sessionsBefore int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM auth_sessions WHERE user_id = $1`, user.ID).
		Scan(&sessionsBefore); err != nil {
		t.Fatalf("count sessions before disabled login: %v", err)
	}
	linked.Identity.Email = "must-not-update@zenith-team.test"
	linked.Session = newOAuthSession(t, 66)
	if _, _, err := loginWithOAuthAndCreateSession(ctx, store, linked); !errors.Is(err, ErrAccountDisabled) {
		t.Fatalf("LoginWithOAuthAndCreateSession(disabled) error = %v", err)
	}
	var sessionsAfter int
	var storedEmail string
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM auth_sessions WHERE user_id = $1`, user.ID).
		Scan(&sessionsAfter); err != nil {
		t.Fatalf("count sessions after disabled login: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT email FROM oauth_accounts WHERE provider = $1 AND subject = $2`, linked.Identity.Provider, linked.Identity.Subject).
		Scan(&storedEmail); err != nil {
		t.Fatalf("query disabled OAuth account: %v", err)
	}
	if sessionsAfter != sessionsBefore || storedEmail == linked.Identity.Email {
		t.Fatalf(
			"disabled login mutated state: sessions %d -> %d, email=%q",
			sessionsBefore,
			sessionsAfter,
			storedEmail,
		)
	}
}

func TestOAuthRepositoryUsesSeparateInitialDisplayNameWithoutFabricatingProviderClaim(t *testing.T) {
	pool, store := startAuthPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	input := OAuthLoginInput{
		Identity: VerifiedOAuthIdentity{
			Provider: googleOAuthProvider,
			Subject:  "fallback-name-google-subject",
			Email:    "fallback.user@zenith-team.test",
			Profile:  OAuthProfileSnapshot{},
		},
		InitialDisplayName: "fallback.user",
		Session:            newOAuthSession(t, 72),
	}
	user, _, err := loginWithOAuthAndCreateSession(ctx, store, input)
	if err != nil {
		t.Fatalf("LoginWithOAuthAndCreateSession() error = %v", err)
	}
	if user.DisplayName != "fallback.user" {
		t.Fatalf("created display name = %q", user.DisplayName)
	}

	var profile map[string]any
	if err := pool.QueryRow(ctx, `SELECT profile FROM oauth_accounts WHERE provider = $1 AND subject = $2`, input.Identity.Provider, input.Identity.Subject).
		Scan(&profile); err != nil {
		t.Fatalf("query provider profile: %v", err)
	}
	if len(profile) != 0 {
		t.Fatalf("provider profile contains fabricated fallback = %#v", profile)
	}
}

func TestOAuthServiceResolvesSameSubjectAcrossWebAndDesktopSessions(t *testing.T) {
	pool, store := startAuthPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	random := make([]byte, 160)
	for index := range random {
		random[index] = byte(index)
	}
	requests := make(map[ClientKind]OAuthAuthorizationRequest)
	provider := &fakeOAuthProvider{
		authorizationURL: func(request OAuthAuthorizationRequest) (string, error) {
			requests[request.Client] = request
			return "https://accounts.google.test/authorize", nil
		},
		exchange: func(_ context.Context, request OAuthExchangeRequest) (OAuthProviderIdentity, error) {
			authorization := requests[request.Client]
			identity := VerifiedOAuthIdentity{
				Provider: googleOAuthProvider,
				Subject:  "cross-client-stable-subject",
				Email:    "first@zenith-team.test",
				Profile:  OAuthProfileSnapshot{Name: "First Provider Name"},
			}
			if request.Client == ClientDesktop {
				identity.Email = "changed@zenith-team.test"
				identity.Profile.Name = "Changed Provider Name"
			}
			return OAuthProviderIdentity{Identity: identity, Nonce: authorization.Nonce}, nil
		},
	}
	refreshCount := byte(0)
	tokens := &fakeOAuthTokenIssuer{
		issueRefresh: func() (IssuedRefresh, error) {
			refreshCount++
			return IssuedRefresh{
				Token:     fmt.Sprintf("brf_cross_client_%d", refreshCount),
				Prefix:    fmt.Sprintf("brf_cross_%d", refreshCount),
				Hash:      bytes.Repeat([]byte{refreshCount}, 32),
				ExpiresAt: time.Now().UTC().Add(24 * time.Hour),
			}, nil
		},
		issueAccess: func(userID, sessionID uuid.UUID) (string, time.Time, error) {
			return userID.String() + "." + sessionID.String(), time.Now().UTC().Add(15 * time.Minute), nil
		},
	}
	callbackURI := "https://api.zenith-team.test/api/v1/auth/google/web/callback"
	service, err := NewOAuthService(OAuthServiceOptions{
		Store: store, Provider: provider, Tokens: tokens,
		WebRedirectURI: callbackURI, Random: bytes.NewReader(random),
	})
	if err != nil {
		t.Fatalf("NewOAuthService() error = %v", err)
	}

	webStart, err := service.StartWeb(ctx, WebOAuthStartInput{
		ReturnPath: "/", ClientMetadata: OAuthClientMetadata{AppVersion: "web-2.0", Platform: "browser"},
	})
	if err != nil {
		t.Fatalf("StartWeb() error = %v", err)
	}
	webResult, err := service.CompleteWeb(ctx, WebOAuthCompleteInput{
		Code: "sentinel-web-code", State: requests[ClientWeb].State, BrowserBinding: webStart.BrowserBinding,
	})
	if err != nil {
		t.Fatalf("CompleteWeb() error = %v", err)
	}

	verifier := strings.Repeat("v", 43)
	desktopStart, err := service.StartDesktop(ctx, DesktopOAuthStartInput{
		RedirectURI: "http://127.0.0.1:49152/", CodeChallenge: oauthS256Challenge(verifier),
		ClientMetadata: OAuthClientMetadata{AppVersion: "desktop-2.0", Platform: "darwin"},
	})
	if err != nil {
		t.Fatalf("StartDesktop() error = %v", err)
	}
	desktopResult, err := service.CompleteDesktop(ctx, DesktopOAuthCompleteInput{
		FlowID: desktopStart.FlowID, Code: "sentinel-desktop-code", State: desktopStart.State,
		RedirectURI: "http://127.0.0.1:49152/", Verifier: verifier,
	})
	if err != nil {
		t.Fatalf("CompleteDesktop() error = %v", err)
	}

	if desktopResult.User.ID != webResult.Result.User.ID || desktopResult.User.Email != "first@zenith-team.test" ||
		desktopResult.User.DisplayName != "First Provider Name" {
		t.Fatalf(
			"cross-client subject resolution: Web=%#v Desktop=%#v",
			webResult.Result.User,
			desktopResult.User,
		)
	}
	var users, accounts, sessions, webSessions, desktopSessions int
	if err := pool.QueryRow(
		ctx, `
		SELECT
			(SELECT count(*) FROM users WHERE id = $1),
			(SELECT count(*) FROM oauth_accounts WHERE provider = 'google' AND subject = $2),
			(SELECT count(*) FROM auth_sessions WHERE user_id = $1),
			(SELECT count(*) FROM auth_sessions WHERE user_id = $1 AND client_metadata->>'client' = 'web'),
			(SELECT count(*) FROM auth_sessions WHERE user_id = $1 AND client_metadata->>'client' = 'desktop')`,
		webResult.Result.User.ID, "cross-client-stable-subject",
	).Scan(&users, &accounts, &sessions, &webSessions, &desktopSessions); err != nil {
		t.Fatalf("query cross-client OAuth cardinality: %v", err)
	}
	if users != 1 || accounts != 1 || sessions != 2 || webSessions != 1 || desktopSessions != 1 {
		t.Fatalf(
			"cross-client cardinality users/accounts/sessions/web/desktop = %d/%d/%d/%d/%d",
			users,
			accounts,
			sessions,
			webSessions,
			desktopSessions,
		)
	}
}

func TestOAuthRepositoryRollsBackIdentityWhenSessionCreationFails(t *testing.T) {
	pool, store := startAuthPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	duplicateSession := newZenithTeamSession(t, 67)
	if _, _, err := createUserWithSession(ctx, store, NewUser{
		Email:        "session-owner@zenith-team.test",
		PasswordHash: "password-hash", DisplayName: "Session Owner",
	}, duplicateSession); err != nil {
		t.Fatalf("CreateUserWithSession(duplicate token owner) error = %v", err)
	}

	input := OAuthLoginInput{
		Identity: VerifiedOAuthIdentity{
			Provider: "google",
			Subject:  "rollback-google-subject",
			Email:    "rollback-oauth@zenith-team.test",
			Profile:  OAuthProfileSnapshot{Name: "Rollback OAuth"},
		},
		InitialDisplayName: "Rollback OAuth",
		Session: NewOAuthSession{
			TokenPrefix: "brf_duplicate",
			TokenHash:   duplicateSession.TokenHash, ExpiresAt: time.Now().UTC().Add(24 * time.Hour),
			Client: ClientDesktop, ClientMetadata: OAuthClientMetadata{AppVersion: "test"},
		},
	}
	if _, _, err := loginWithOAuthAndCreateSession(ctx, store, input); err == nil {
		t.Fatal("LoginWithOAuthAndCreateSession(duplicate session) error = nil")
	}

	var userExists, accountExists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE lower(email) = lower($1))`, input.Identity.Email).
		Scan(&userExists); err != nil {
		t.Fatalf("query rolled-back OAuth user: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM oauth_accounts WHERE provider = $1 AND subject = $2)`, input.Identity.Provider, input.Identity.Subject).
		Scan(&accountExists); err != nil {
		t.Fatalf("query rolled-back OAuth account: %v", err)
	}
	if userExists || accountExists {
		t.Fatalf("failed session left OAuth state: user=%v account=%v", userExists, accountExists)
	}
}

func TestOAuthRepositoryRollsBackExistingIdentitySnapshotWhenSessionCreationFails(t *testing.T) {
	pool, store := startAuthPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	initial := OAuthLoginInput{
		Identity: VerifiedOAuthIdentity{
			Provider: "google",
			Subject:  "existing-rollback-google-subject",
			Email:    "original-oauth@zenith-team.test",
			Profile:  OAuthProfileSnapshot{Name: "Original OAuth Snapshot"},
		},
		InitialDisplayName: "Original OAuth Snapshot",
		Session:            newOAuthSession(t, 68),
	}
	user, _, err := loginWithOAuthAndCreateSession(ctx, store, initial)
	if err != nil {
		t.Fatalf("LoginWithOAuthAndCreateSession(initial) error = %v", err)
	}

	var emailBefore string
	var profileBefore json.RawMessage
	if err := pool.QueryRow(ctx, `
		SELECT email, profile
		FROM oauth_accounts
		WHERE provider = $1 AND subject = $2`, initial.Identity.Provider, initial.Identity.Subject).Scan(&emailBefore, &profileBefore); err != nil {
		t.Fatalf("query OAuth account before failed login: %v", err)
	}
	var sessionsBefore int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM auth_sessions WHERE user_id = $1`, user.ID).
		Scan(&sessionsBefore); err != nil {
		t.Fatalf("count sessions before failed login: %v", err)
	}

	duplicateSession := newZenithTeamSession(t, 69)
	if _, _, err := createUserWithSession(ctx, store, NewUser{
		Email:        "existing-rollback-session-owner@zenith-team.test",
		PasswordHash: "password-hash", DisplayName: "Existing Rollback Session Owner",
	}, duplicateSession); err != nil {
		t.Fatalf("CreateUserWithSession(duplicate token owner) error = %v", err)
	}

	retry := initial
	retry.Identity.Email = "mutated-oauth@zenith-team.test"
	retry.Identity.Profile = OAuthProfileSnapshot{Name: "Mutated OAuth Snapshot"}
	retry.Session = NewOAuthSession{
		TokenPrefix: "brf_duplicate_existing",
		TokenHash:   duplicateSession.TokenHash, ExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Client: ClientDesktop, ClientMetadata: OAuthClientMetadata{AppVersion: "test"},
	}
	if _, _, err := loginWithOAuthAndCreateSession(ctx, store, retry); err == nil {
		t.Fatal("LoginWithOAuthAndCreateSession(existing identity, duplicate session) error = nil")
	}

	var emailAfter string
	var profileAfter json.RawMessage
	if err := pool.QueryRow(ctx, `
		SELECT email, profile
		FROM oauth_accounts
		WHERE provider = $1 AND subject = $2`, initial.Identity.Provider, initial.Identity.Subject).Scan(&emailAfter, &profileAfter); err != nil {
		t.Fatalf("query OAuth account after failed login: %v", err)
	}
	var sessionsAfter int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM auth_sessions WHERE user_id = $1`, user.ID).
		Scan(&sessionsAfter); err != nil {
		t.Fatalf("count sessions after failed login: %v", err)
	}
	if emailAfter != emailBefore || !bytes.Equal(profileAfter, profileBefore) || sessionsAfter != sessionsBefore {
		t.Fatalf(
			"failed existing-identity session mutated state: email %q -> %q, profile %s -> %s, sessions %d -> %d",
			emailBefore,
			emailAfter,
			profileBefore,
			profileAfter,
			sessionsBefore,
			sessionsAfter,
		)
	}
}

func TestOAuthPersistenceHasNoProviderCredentialSurface(t *testing.T) {
	pool, _ := startAuthPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var forbiddenColumns int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name IN ('oauth_accounts', 'oauth_login_flows')
		  AND column_name IN (
			'authorization_code', 'access_token', 'refresh_token',
			'id_token', 'client_secret', 'code_verifier'
		  )`).Scan(&forbiddenColumns); err != nil {
		t.Fatalf("inspect OAuth persistence columns: %v", err)
	}
	if forbiddenColumns != 0 {
		t.Fatalf("OAuth persistence exposes %d provider credential columns", forbiddenColumns)
	}
}

func TestOAuthPersistenceDoesNotPersistProviderCredentialArtifactsFromClientMetadata(t *testing.T) {
	pool, store := startAuthPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	flow := newWebOAuthFlow(t, 70)
	flow.ClientMetadata = oauthClientMetadataFromUntrustedJSON(t, `{
		"client":"web",
		"appVersion":"1.0.0",
		"authorization_code":"sentinel-provider-code",
		"access_token":"sentinel-provider-access-token"
	}`)
	flowID, err := createOAuthLoginFlow(ctx, store.Queries(), flow)
	if err != nil {
		t.Fatalf("CreateOAuthLoginFlow() error = %v", err)
	}

	login := OAuthLoginInput{
		Identity: VerifiedOAuthIdentity{
			Provider: "google",
			Subject:  "credential-artifact-google-subject",
			Email:    "credential-artifact@zenith-team.test",
			Profile:  OAuthProfileSnapshot{Name: "Credential Artifact Sentinel"},
		},
		InitialDisplayName: "Credential Artifact Sentinel",
		Session:            newOAuthSession(t, 71),
	}
	login.Session.ClientMetadata = oauthClientMetadataFromUntrustedJSON(t, `{
		"client":"desktop",
		"appVersion":"1.0.0",
		"refresh_token":"sentinel-provider-refresh-token",
		"id_token":"sentinel-provider-id-token",
		"client_secret":"sentinel-provider-client-secret",
		"code_verifier":"sentinel-pkce-verifier"
	}`)
	_, sessionID, err := loginWithOAuthAndCreateSession(ctx, store, login)
	if err != nil {
		t.Fatalf("LoginWithOAuthAndCreateSession() error = %v", err)
	}
	var accountID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM oauth_accounts WHERE provider = $1 AND subject = $2`, login.Identity.Provider, login.Identity.Subject).
		Scan(&accountID); err != nil {
		t.Fatalf("load database-generated OAuth account ID: %v", err)
	}

	var hasForbiddenArtifact bool
	if err := pool.QueryRow(
		ctx,
		`
		SELECT EXISTS (
			SELECT 1
			FROM oauth_login_flows flow,
				LATERAL jsonb_object_keys(flow.client_metadata) AS field(key)
			WHERE flow.id = $1
			  AND (field.key = ANY($4::text[])
			       OR flow.client_metadata::text LIKE '%sentinel-provider%')
			UNION ALL
			SELECT 1
			FROM oauth_accounts account,
				LATERAL jsonb_object_keys(account.profile) AS field(key)
			WHERE account.id = $2
			  AND (field.key = ANY($4::text[])
			       OR account.profile::text LIKE '%sentinel-provider%')
			UNION ALL
			SELECT 1
			FROM auth_sessions session,
				LATERAL jsonb_object_keys(session.client_metadata) AS field(key)
			WHERE session.id = $3
			  AND (field.key = ANY($4::text[])
			       OR session.client_metadata::text LIKE '%sentinel-provider%')
		)`,
		flowID,
		accountID,
		sessionID,
		[]string{
			"authorization_code",
			"access_token",
			"refresh_token",
			"id_token",
			"client_secret",
			"code_verifier",
		},
	).Scan(&hasForbiddenArtifact); err != nil {
		t.Fatalf("inspect OAuth persistence for provider credential artifacts: %v", err)
	}
	if hasForbiddenArtifact {
		t.Fatal("OAuth persistence accepted a provider credential artifact through client metadata")
	}
}

func newWebOAuthFlow(t *testing.T, seed byte) NewOAuthLoginFlow {
	t.Helper()
	returnPath := "/onboarding"
	return NewOAuthLoginFlow{
		Provider:           "google",
		Client:             ClientWeb,
		StateHash:          bytes.Repeat([]byte{seed}, 32),
		NonceHash:          bytes.Repeat([]byte{seed + 1}, 32),
		BrowserBindingHash: bytes.Repeat([]byte{seed + 2}, 32),
		RedirectURI:        "https://api.zenith-team.test/api/v1/auth/google/web/callback",
		ReturnPath:         &returnPath,
		ClientMetadata:     OAuthClientMetadata{AppVersion: "test"},
		ExpiresAt:          time.Now().UTC().Add(10 * time.Minute),
	}
}

func newDesktopOAuthFlow(t *testing.T, seed byte) NewOAuthLoginFlow {
	t.Helper()
	challenge := strings.Repeat("A", 43)
	return NewOAuthLoginFlow{
		Provider:       "google",
		Client:         ClientDesktop,
		StateHash:      bytes.Repeat([]byte{seed}, 32),
		NonceHash:      bytes.Repeat([]byte{seed + 1}, 32),
		RedirectURI:    "http://127.0.0.1:49152/",
		CodeChallenge:  &challenge,
		ClientMetadata: OAuthClientMetadata{AppVersion: "test"},
		ExpiresAt:      time.Now().UTC().Add(10 * time.Minute),
	}
}

func newOAuthSession(t *testing.T, seed byte) NewOAuthSession {
	t.Helper()
	return NewOAuthSession{
		TokenPrefix:    "brf_oauth_test",
		TokenHash:      bytes.Repeat([]byte{seed}, 32),
		ExpiresAt:      time.Now().UTC().Add(24 * time.Hour),
		Client:         ClientDesktop,
		ClientMetadata: OAuthClientMetadata{AppVersion: "test"},
	}
}

func newZenithTeamSession(t *testing.T, seed byte) NewSession {
	t.Helper()
	return NewSession{
		TokenPrefix:    "brf_oauth_test_fixture",
		TokenHash:      bytes.Repeat([]byte{seed}, 32),
		ExpiresAt:      time.Now().UTC().Add(24 * time.Hour),
		ClientMetadata: json.RawMessage(`{"client":"desktop"}`),
	}
}

func oauthClientMetadataFromUntrustedJSON(t *testing.T, raw string) OAuthClientMetadata {
	t.Helper()
	var metadata OAuthClientMetadata
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
		t.Fatalf("decode OAuth client metadata fixture: %v", err)
	}
	return metadata
}
