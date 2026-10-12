package social

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ProviderError deliberately excludes provider messages, request URLs and tokens.
type ProviderError struct {
	Status    int
	Code      string
	Uncertain bool
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("social provider request failed (%d, %s)", e.Status, e.Code)
}

func RequiresReconnect(err error) bool {
	var e *ProviderError
	return errors.As(err, &e) &&
		(e.Status == 401 || e.Code == "invalid_grant" || e.Code == "invalid_token" || e.Code == "permission_denied")
}

func OutcomeUncertain(err error) bool {
	var e *ProviderError
	return errors.As(err, &e) && e.Uncertain
}

func (c *Client) send(req *http.Request) (map[string]json.RawMessage, http.Header, error) {
	response, err := c.http.Do(req)
	if err != nil {
		return nil, nil, &ProviderError{Code: "transport", Uncertain: true}
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil || len(raw) > 2<<20 {
		return nil, nil, &ProviderError{Code: "invalid_response", Uncertain: true}
	}
	var result map[string]json.RawMessage
	decodeErr := error(nil)
	if len(raw) > 0 && (req.Method != http.MethodDelete || string(raw) != "true") {
		decodeErr = json.Unmarshal(raw, &result)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || len(result["error"]) > 0 {
		code := "rejected"
		vendorCode := textField(result, "error")
		if vendorCode == "invalid_grant" || vendorCode == "invalid_token" {
			code = vendorCode
		}
		metaCode := textField(object(result["error"]), "code")
		if metaCode == "190" {
			code = "invalid_token"
		}
		if metaCode == "10" || metaCode == "200" {
			code = "permission_denied"
		}
		return nil, nil, &ProviderError{
			Status:    response.StatusCode,
			Code:      code,
			Uncertain: response.StatusCode >= 500,
		}
	}
	if decodeErr != nil {
		return nil, nil, &ProviderError{Code: "invalid_response", Uncertain: true}
	}
	return result, response.Header, nil
}

func (c *Client) CanRefresh(token Token) bool {
	return c.platform == "instagram" ||
		((c.platform == "x" || c.platform == "linkedin") && token.RefreshToken != "")
}

func (c *Client) Refresh(ctx context.Context, previous Token) (Token, error) {
	if !c.CanRefresh(previous) {
		return Token{}, &ProviderError{Code: "invalid_grant"}
	}
	var data map[string]json.RawMessage
	var err error
	if c.platform == "instagram" {
		params := url.Values{"grant_type": {"ig_refresh_token"}, "access_token": {previous.AccessToken}}
		data, err = c.request(
			ctx,
			http.MethodGet,
			c.graphURL+"/refresh_access_token?"+params.Encode(),
			nil,
			"",
			false,
		)
	} else {
		form := url.Values{
			"grant_type":    {"refresh_token"},
			"refresh_token": {previous.RefreshToken},
			"client_id":     {c.credentials.ClientID},
		}
		if c.platform == "linkedin" {
			form.Set("client_secret", c.credentials.ClientSecret)
		}
		data, err = c.request(ctx, http.MethodPost, c.tokenURL, form, "", c.platform == "x")
	}
	if err != nil {
		return Token{}, err
	}
	token, err := parseToken(data)
	if err != nil {
		return Token{}, &ProviderError{Code: "invalid_response", Uncertain: true}
	}
	if token.RefreshToken == "" {
		token.RefreshToken = previous.RefreshToken
	}
	if _, present := data["scope"]; !present {
		token.Scopes = previous.Scopes
	}
	return token, nil
}

func (c *Client) Validate(ctx context.Context, token Token) error {
	_, err := c.request(ctx, http.MethodGet, c.profileURL, nil, token.AccessToken, false)
	return err
}

func (c *Client) CanPublish(token Token) bool {
	permission := map[string]string{"x": "tweet.write", "facebook": "pages_manage_posts", "instagram": "instagram_business_content_publish", "linkedin": "w_member_social"}[c.platform]
	for _, scope := range strings.FieldsFunc(token.Scopes, func(r rune) bool { return r == ',' || r == ' ' }) {
		if scope == permission && permission != "" {
			return true
		}
	}
	return false
}

func (c *Client) grantedScopes(ctx context.Context, token Token) (string, error) {
	if c.platform != "facebook" && c.platform != "instagram" {
		return token.Scopes, nil
	}
	address := c.graphURL + "/me/permissions"
	if c.platform == "instagram" {
		address = c.graphURL + "/" + c.credentials.GraphVersion + "/me/permissions"
	}
	data, err := c.request(ctx, http.MethodGet, address, nil, token.AccessToken, false)
	if err != nil {
		return "", err
	}
	var permissions []struct {
		Permission string `json:"permission"`
		Status     string `json:"status"`
	}
	if err := json.Unmarshal(data["data"], &permissions); err != nil {
		return "", errors.New("invalid social permissions response")
	}
	var granted []string
	for _, p := range permissions {
		if p.Status == "granted" {
			granted = append(granted, p.Permission)
		}
	}
	return strings.Join(granted, " "), nil
}
