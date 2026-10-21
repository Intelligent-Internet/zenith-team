package apiclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/Intelligent-Internet/zenith-team/server/internal/runchannel"
)

// RunChannel is zenith-team-mcp's transport. Every call is one request on the
// daemon's Run channel, named by run.json in the Run's isolated home; the
// daemon checks it against the Run's policy and forwards it with its own
// session and the Run proof. zenith-team-mcp holds no bearer and no path to one,
// so a tool can never call the server as the person.
type RunChannel struct {
	channel *runchannel.Client
}

// NewRunChannel binds the transport to one Run's channel.
func NewRunChannel(control runchannel.RunControlConfig) *RunChannel {
	return &RunChannel{channel: runchannel.NewClient(control)}
}

// FetchRunJSON reads path, relative to /api/v1, as the Run.
func (c *RunChannel) FetchRunJSON(ctx context.Context, path string, destination any) error {
	return c.do(ctx, http.MethodGet, path, "", nil, destination)
}

// PostRunJSON posts source to path as the Run. The request key makes a
// retried tool call converge; the channel refuses a mutation without one.
func (c *RunChannel) PostRunJSON(
	ctx context.Context,
	path, requestKey string,
	source, destination any,
) error {
	if requestKey == "" {
		return errors.New("Zenith Team idempotency key is required")
	}
	return c.do(ctx, http.MethodPost, path, requestKey, source, destination)
}

func (c *RunChannel) do(
	ctx context.Context,
	method, path, requestKey string,
	source, destination any,
) error {
	if c == nil || c.channel == nil {
		return errors.New("Zenith Team Run channel is unavailable")
	}
	request := runchannel.Request{Method: method, Path: path, RequestKey: requestKey}
	if source != nil {
		encoded, err := json.Marshal(source)
		if err != nil {
			return fmt.Errorf("encode Zenith Team API request: %w", err)
		}
		request.Body = encoded
	}
	response, err := c.channel.Do(ctx, request)
	if err != nil {
		return fmt.Errorf("call Zenith Team API: %w", err)
	}
	if response.Status < 200 || response.Status >= 300 {
		return apiErrorFromBody(response.Status, response.Body)
	}
	if destination != nil && len(response.Body) != 0 {
		if err := json.Unmarshal(response.Body, destination); err != nil {
			return fmt.Errorf("decode Zenith Team API response: %w", err)
		}
	}
	return nil
}
