package social

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"strconv"

	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

type DesktopInput struct{ RedirectURI, State string }

func validDesktopRedirect(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	port, err := strconv.Atoi(u.Port())
	return err == nil && port >= 1024 && port <= 65535 && u.Scheme == "http" && u.Hostname() == "127.0.0.1" &&
		u.User == nil &&
		u.Path == "/social/callback" &&
		u.RawPath == "" &&
		u.RawQuery == "" &&
		!u.ForceQuery &&
		u.Fragment == ""
}

func (s *DefaultSocialService) StartDesktop(
	ctx context.Context,
	p auth.UserPrincipal,
	platform string,
	input DesktopInput,
) (StartResult, error) {
	if !validDesktopRedirect(input.RedirectURI) || len(input.State) != 43 {
		return StartResult{}, ErrInvalidFlow
	}
	return s.start(ctx, p, platform, input)
}

// The launch ticket bridges the authenticated desktop session to its browser.
// It is consumed once before setting the browser-only callback binding cookie.
func (s *DefaultSocialService) LaunchDesktop(ctx context.Context, platform, state, ticket string) (StartResult, error) {
	if len(state) != 43 || len(ticket) != 43 {
		return StartResult{}, ErrInvalidFlow
	}
	client, err := s.connector(platform, true)
	if err != nil {
		return StartResult{}, err
	}
	stateHash, bindingHash := sha256.Sum256([]byte(state)), sha256.Sum256([]byte(ticket))
	var flow generated.SocialOauthFlow
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		var err error
		flow, err = q.LaunchSocialDesktopFlow(
			ctx,
			generated.LaunchSocialDesktopFlowParams{
				StateHash:   stateHash[:],
				BindingHash: bindingHash[:],
				Platform:    platform,
			},
		)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return StartResult{}, ErrInvalidFlow
	}
	if err != nil {
		return StartResult{}, err
	}
	verifier, err := s.vault.open(flow.Verifier, flow.UserID.String()+":"+platform)
	if err != nil {
		return StartResult{}, ErrInvalidFlow
	}
	challenge := sha256.Sum256(verifier)
	return StartResult{
		AuthorizationURL: client.AuthorizationURL(
			state,
			base64.RawURLEncoding.EncodeToString(challenge[:]),
			s.callback(platform),
		),
		Binding: ticket,
	}, nil
}

func desktopReturn(redirectURI, state string) string {
	if redirectURI == "" {
		return ""
	}
	return redirectURI + "?" + url.Values{"state": {state}}.Encode()
}
