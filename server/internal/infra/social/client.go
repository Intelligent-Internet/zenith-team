// Package social adapts external social OAuth, credential refresh, discovery and
// immediate publishing. It has no
// database or Zenith product dependency.
package social

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type (
	Credentials struct{ ClientID, ClientSecret, GraphVersion string }
	Token       struct {
		AccessToken  string    `json:"accessToken"`
		RefreshToken string    `json:"refreshToken"`
		ExpiresAt    time.Time `json:"expiresAt"`
		Scopes       string    `json:"scopes"`
	}
)

type Target struct {
	ExternalID, DisplayName, Username, Type string
	Token                                   Token
}
type Account struct {
	ExternalID, DisplayName, Username, AvatarURL string
	Token                                        Token
	Targets                                      []Target
}

type Client struct {
	platform                                         string
	credentials                                      Credentials
	http                                             *http.Client
	authorizationURL, tokenURL, profileURL, graphURL string
	scopes                                           string
}

func NewClient(platform string, credentials Credentials, client *http.Client) *Client {
	if client == nil {
		client = &http.Client{
			Timeout:       20 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	if credentials.GraphVersion == "" {
		credentials.GraphVersion = "v23.0"
	}
	c := &Client{platform: platform, credentials: credentials, http: client}
	switch platform {
	case "x":
		c.authorizationURL, c.tokenURL, c.profileURL = "https://x.com/i/oauth2/authorize", "https://api.x.com/2/oauth2/token", "https://api.x.com/2/users/me?user.fields=profile_image_url"
		c.scopes = "users.read tweet.read tweet.write media.write offline.access"
	case "facebook":
		c.graphURL = "https://graph.facebook.com/" + credentials.GraphVersion
		c.authorizationURL, c.tokenURL, c.profileURL = "https://www.facebook.com/"+credentials.GraphVersion+"/dialog/oauth", c.graphURL+"/oauth/access_token", c.graphURL+"/me?fields=id,name,picture"
		c.scopes = "public_profile,pages_show_list,pages_read_engagement,pages_manage_posts"
	case "instagram":
		c.graphURL = "https://graph.instagram.com"
		c.authorizationURL, c.tokenURL, c.profileURL = "https://www.instagram.com/oauth/authorize", "https://api.instagram.com/oauth/access_token", c.graphURL+"/"+credentials.GraphVersion+"/me?fields=id,user_id,username,name,profile_picture_url,account_type"
		c.scopes = "instagram_business_basic,instagram_business_content_publish"
	case "linkedin":
		c.authorizationURL, c.tokenURL, c.profileURL = "https://www.linkedin.com/oauth/v2/authorization", "https://www.linkedin.com/oauth/v2/accessToken", "https://api.linkedin.com/v2/userinfo"
		c.scopes = "openid profile w_member_social"
	}
	return c
}

func (c *Client) AuthorizationURL(state, challenge, redirect string) string {
	params := url.Values{
		"response_type": {"code"},
		"client_id":     {c.credentials.ClientID},
		"redirect_uri":  {redirect},
		"state":         {state},
		"scope":         {c.scopes},
	}
	if c.platform == "x" {
		params.Set("code_challenge", challenge)
		params.Set("code_challenge_method", "S256")
	}
	if c.platform == "instagram" {
		params.Set("enable_fb_login", "0")
		params.Set("force_authentication", "1")
	}
	return c.authorizationURL + "?" + params.Encode()
}

func (c *Client) Exchange(ctx context.Context, code, verifier, redirect string) (Account, error) {
	form := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {redirect},
		"client_id":    {c.credentials.ClientID},
	}
	if c.platform == "x" {
		form.Set("code_verifier", verifier)
	} else {
		form.Set("client_secret", c.credentials.ClientSecret)
	}
	data, err := c.request(ctx, http.MethodPost, c.tokenURL, form, "", c.platform == "x")
	if err != nil {
		return Account{}, err
	}
	token, err := parseToken(data)
	if err != nil {
		return Account{}, err
	}
	if c.platform == "instagram" || c.platform == "facebook" {
		params := url.Values{"client_secret": {c.credentials.ClientSecret}}
		endpoint := c.graphURL + "/access_token"
		if c.platform == "instagram" {
			params.Set("grant_type", "ig_exchange_token")
			params.Set("access_token", token.AccessToken)
		} else {
			endpoint = c.tokenURL
			params.Set("grant_type", "fb_exchange_token")
			params.Set("fb_exchange_token", token.AccessToken)
			params.Set("client_id", c.credentials.ClientID)
		}
		long, exchangeErr := c.request(ctx, http.MethodGet, endpoint+"?"+params.Encode(), nil, "", false)
		if exchangeErr != nil {
			return Account{}, exchangeErr
		}
		token, err = parseToken(long)
		if err != nil {
			return Account{}, err
		}
	}
	token.Scopes, err = c.grantedScopes(ctx, token)
	if err != nil {
		return Account{}, err
	}
	profile, err := c.request(ctx, http.MethodGet, c.profileURL, nil, token.AccessToken, false)
	if err != nil {
		return Account{}, err
	}
	if c.platform == "x" {
		profile = object(profile["data"])
	}
	account := Account{
		ExternalID:  textField(profile, "id"),
		DisplayName: textField(profile, "name"),
		Username:    textField(profile, "username"),
		Token:       token,
	}
	switch c.platform {
	case "x":
		account.AvatarURL = textField(profile, "profile_image_url")
	case "linkedin":
		account.ExternalID = textField(profile, "sub")
		account.AvatarURL = textField(profile, "picture")
	case "instagram":
		account.AvatarURL = textField(profile, "profile_picture_url")
		if value := textField(profile, "user_id"); value != "" {
			account.ExternalID = value
		}
	case "facebook":
		account.AvatarURL = textField(object(object(profile["picture"])["data"]), "url")
	}
	if account.ExternalID == "" {
		return Account{}, errors.New("social account identity is missing")
	}
	if account.DisplayName == "" {
		account.DisplayName = account.Username
	}
	if account.DisplayName == "" {
		account.DisplayName = account.ExternalID
	}
	if c.platform == "facebook" {
		account.Targets, err = c.facebookPages(ctx, token)
		if err != nil {
			return Account{}, err
		}
	} else {
		account.Targets = []Target{
			{
				ExternalID:  account.ExternalID,
				DisplayName: account.DisplayName,
				Username:    account.Username,
				Type:        "account",
				Token:       token,
			},
		}
	}
	return account, nil
}

func (c *Client) facebookPages(ctx context.Context, token Token) ([]Target, error) {
	result := []Target{}
	after := ""
	for range 20 {
		params := url.Values{"fields": {"id,name,access_token"}, "limit": {"100"}}
		if after != "" {
			params.Set("after", after)
		}
		data, err := c.request(
			ctx,
			http.MethodGet,
			c.graphURL+"/me/accounts?"+params.Encode(),
			nil,
			token.AccessToken,
			false,
		)
		if err != nil {
			return nil, err
		}
		var pages []map[string]json.RawMessage
		if err := json.Unmarshal(data["data"], &pages); err != nil {
			return nil, errors.New("invalid Facebook page response")
		}
		for _, item := range pages {
			id, access := textField(item, "id"), textField(item, "access_token")
			if id == "" || access == "" {
				continue
			}
			result = append(
				result,
				Target{
					ExternalID:  id,
					DisplayName: textField(item, "name"),
					Type:        "page",
					Token:       Token{AccessToken: access},
				},
			)
		}
		paging := object(data["paging"])
		next := textField(object(paging["cursors"]), "after")
		if textField(paging, "next") == "" {
			return result, nil
		}
		if next == "" || next == after {
			return nil, errors.New("invalid Facebook pagination")
		}
		after = next
	}
	return nil, errors.New("Facebook page discovery exceeded its limit")
}

// LinkedIn has no documented member-token revocation endpoint. Its disconnect
// removes the locally held grant; the UI also links to LinkedIn app permissions.
func (c *Client) Revoke(ctx context.Context, token Token) error {
	if c.platform == "linkedin" {
		return nil
	}
	if c.platform == "x" {
		for _, item := range []struct{ value, hint string }{
			{token.RefreshToken, "refresh_token"},
			{token.AccessToken, "access_token"},
		} {
			if item.value == "" {
				continue
			}
			_, err := c.request(
				ctx,
				http.MethodPost,
				"https://api.x.com/2/oauth2/revoke",
				url.Values{
					"token":           {item.value},
					"token_type_hint": {item.hint},
					"client_id":       {c.credentials.ClientID},
				},
				"",
				true,
			)
			if err := revocationError(err); err != nil {
				return err
			}
		}
		return nil
	}
	endpoint := c.graphURL + "/me/permissions"
	if c.platform == "instagram" {
		endpoint = c.graphURL + "/" + c.credentials.GraphVersion + "/me/permissions"
	}
	_, err := c.request(ctx, http.MethodDelete, endpoint, nil, token.AccessToken, false)
	return revocationError(err)
}

func revocationError(err error) error {
	var providerErr *ProviderError
	// Invalid client credentials or a forbidden request do not prove that the
	// grant was revoked. An already-invalid token needs no further revocation.
	if errors.As(err, &providerErr) && providerErr.Code == "invalid_token" {
		return nil
	}
	return err
}

func (c *Client) request(
	ctx context.Context,
	method, address string,
	form url.Values,
	token string,
	basic bool,
) (map[string]json.RawMessage, error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, address, body)
	if err != nil {
		return nil, errors.New("invalid social request")
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if basic && c.credentials.ClientSecret != "" {
		req.SetBasicAuth(c.credentials.ClientID, c.credentials.ClientSecret)
	}
	data, _, err := c.send(req)
	return data, err
}

func parseToken(data map[string]json.RawMessage) (Token, error) {
	result := Token{
		AccessToken:  textField(data, "access_token"),
		RefreshToken: textField(data, "refresh_token"),
		Scopes:       textField(data, "scope"),
	}
	if result.AccessToken == "" {
		return result, errors.New("social service did not return an access token")
	}
	var seconds int64
	_ = json.Unmarshal(data["expires_in"], &seconds)
	if seconds > 0 && seconds < 10*365*24*3600 {
		result.ExpiresAt = time.Now().UTC().Add(time.Duration(seconds) * time.Second)
	}
	return result, nil
}

func object(raw json.RawMessage) map[string]json.RawMessage {
	var result map[string]json.RawMessage
	_ = json.Unmarshal(raw, &result)
	return result
}

func textField(data map[string]json.RawMessage, key string) string {
	var result string
	if json.Unmarshal(data[key], &result) == nil {
		return result
	}
	var number json.Number
	if json.Unmarshal(data[key], &number) == nil {
		return number.String()
	}
	return ""
}
