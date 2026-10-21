package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// OutputChannelService presents through the daemon's Run channel. The daemon
// owns file access and app supervision; the MCP child never chooses a workspace.
// The fixed wire request mirrors runchannel.Request, and the endpoint and
// capability come from the Run's run.json. Channel transport tests pin that
// boundary.
type OutputChannelService interface {
	PresentOutput(context.Context, PresentOutputInput) (PresentOutputResult, error)
}

var _ OutputChannelService = (*DefaultOutputChannelService)(nil)

type DefaultOutputChannelService struct {
	endpoint, capability, runID string
	client                      *http.Client
}

func NewOutputChannelService(endpoint, capability, runID string) (*DefaultOutputChannelService, error) {
	parsed, err := url.Parse(endpoint)
	id, idErr := uuid.Parse(runID)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" ||
		parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		capability == "" || strings.ContainsAny(capability, " \t\r\n") || idErr != nil || id == uuid.Nil {
		return nil, errors.New("Run channel is unavailable")
	}
	return &DefaultOutputChannelService{
		endpoint:   endpoint,
		capability: capability,
		runID:      id.String(),
		client: &http.Client{
			Timeout:       65 * time.Second,
			Transport:     &http.Transport{Proxy: nil},
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func (s *DefaultOutputChannelService) PresentOutput(
	ctx context.Context,
	input PresentOutputInput,
) (PresentOutputResult, error) {
	var result PresentOutputResult
	if err := s.call(ctx, "/runs/"+s.runID+"/output", input, &result); err != nil {
		return PresentOutputResult{}, err
	}
	id, err := uuid.Parse(result.OutputID)
	if err != nil || id == uuid.Nil {
		return PresentOutputResult{}, errors.New("invalid output identity")
	}
	return result, nil
}

func (s *DefaultOutputChannelService) call(ctx context.Context, path string, input, destination any) error {
	body, err := json.Marshal(struct {
		SchemaVersion int    `json:"schemaVersion"`
		Method        string `json:"method"`
		Path          string `json:"path"`
		RequestKey    string `json:"requestKey"`
		Body          any    `json:"body"`
	}{
		SchemaVersion: 1, Method: http.MethodPost, Path: path,
		RequestKey: uuid.NewString(), Body: input,
	})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		s.endpoint+"/v1/run/api",
		bytes.NewReader(body),
	)
	if err != nil {
		return errors.New("create Run channel request")
	}
	request.Header.Set("Authorization", "ZenithRun "+s.capability)
	request.Header.Set("Content-Type", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return errors.New("Run channel could not be reached")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Run channel refused request (HTTP %d)", response.StatusCode)
	}
	var verdict struct {
		SchemaVersion int             `json:"schemaVersion"`
		Status        int             `json:"status"`
		Body          json.RawMessage `json:"body"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).
		Decode(&verdict); err != nil ||
		verdict.SchemaVersion != 1 {
		return errors.New("invalid Run channel response")
	}
	if verdict.Status < 200 || verdict.Status >= 300 {
		var failure struct {
			Detail string `json:"detail"`
			Code   string `json:"code"`
		}
		if json.Unmarshal(verdict.Body, &failure) == nil && failure.Detail != "" {
			return fmt.Errorf("Run channel: %s (%s)", failure.Detail, failure.Code)
		}
		return fmt.Errorf("Run channel operation refused (HTTP %d)", verdict.Status)
	}
	if err := json.Unmarshal(verdict.Body, destination); err != nil {
		return errors.New("invalid Run channel result")
	}
	return nil
}
