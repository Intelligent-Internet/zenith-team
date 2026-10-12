package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"boardly.dev/boardly/server/internal/commerce"
	"boardly.dev/boardly/server/internal/config"
	stripeinfra "boardly.dev/boardly/server/internal/infra/stripe"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func commerceDevelopmentEventFallbackEnabled(
	appEnvironment string,
	configuration config.CommerceConfig,
) bool {
	return appEnvironment == "local" && configuration.Configured && configuration.Enabled &&
		configuration.Sandbox.Enabled
}

func newCommerceDevelopmentEventPoller(
	appEnvironment string,
	configuration config.CommerceConfig,
	queries *generated.Queries,
	service *commerce.DefaultCommerceService,
	logger *slog.Logger,
) (*commerce.DevelopmentEventPoller, error) {
	if !commerceDevelopmentEventFallbackEnabled(appEnvironment, configuration) {
		return nil, nil
	}
	provider, err := stripeinfra.NewDevelopmentEventProvider(
		stripeinfra.DevelopmentEventOptions{
			ReconcileKey:      configuration.Sandbox.ReconcileKey.Reveal(),
			PlatformAccountID: configuration.Sandbox.PlatformAccountID,
			HTTPClient:        &http.Client{Timeout: configuration.ProviderTimeout},
		},
	)
	if err != nil {
		return nil, fmt.Errorf("construct Stripe development Event provider: %w", err)
	}
	poller, err := commerce.NewDevelopmentEventPoller(
		queries,
		&commerceStripeDevelopmentEvents{provider: provider},
		service,
		commerce.DevelopmentEventPollerOptions{
			OnIssue: newCommerceDevelopmentEventPollingIssueHandler(logger),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("construct Commerce development Event poller: %w", err)
	}
	return poller, nil
}

type commerceStripeDevelopmentEvents struct {
	provider *stripeinfra.DevelopmentEventProvider
}

var _ commerce.DevelopmentEventProvider = (*commerceStripeDevelopmentEvents)(nil)

func (provider *commerceStripeDevelopmentEvents) ListEvents(
	ctx context.Context,
	input commerce.DevelopmentEventListInput,
) (commerce.DevelopmentEventListResult, error) {
	result, err := provider.provider.ListEvents(ctx, stripeinfra.DevelopmentEventListInput{
		AccountID: input.AccountID, CreatedAtOrAfter: input.CreatedAtOrAfter,
		StartingAfter: input.StartingAfter, Limit: input.Limit, Types: input.Types,
	})
	if err != nil {
		if errors.Is(err, stripeinfra.ErrDevelopmentEventPermissionRequired) {
			return commerce.DevelopmentEventListResult{},
				commerce.ErrDevelopmentEventProviderPermission
		}
		return commerce.DevelopmentEventListResult{}, err
	}
	mapped := commerce.DevelopmentEventListResult{
		Events:  make([]commerce.WebhookEvent, 0, len(result.Events)),
		HasMore: result.HasMore, NextCursor: result.NextCursor,
	}
	for _, event := range result.Events {
		mapped.Events = append(mapped.Events, commerce.WebhookEvent{
			ID: event.ID, AccountID: event.AccountID, Type: event.Type,
			ObjectID: event.ObjectID, LiveMode: event.LiveMode, ProviderTime: event.ProviderTime,
		})
	}
	return mapped, nil
}

func newCommerceDevelopmentEventPollingIssueHandler(
	logger *slog.Logger,
) func(context.Context, commerce.DevelopmentEventPollingIssue) {
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, issue commerce.DevelopmentEventPollingIssue) {
		logger.WarnContext(
			ctx,
			"Commerce local development Event fallback had an issue",
			"component", "commerce-development-event-fallback",
			"error_code", string(issue),
			"operation", "poll_sandbox_connected_account_events",
		)
	}
}
