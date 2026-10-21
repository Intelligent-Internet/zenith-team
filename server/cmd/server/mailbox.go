package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/redis/go-redis/v9"

	"github.com/Intelligent-Internet/zenith-team/server/internal/config"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/handler"
	"github.com/Intelligent-Internet/zenith-team/server/internal/infra/email"
	"github.com/Intelligent-Internet/zenith-team/server/internal/mailbox"
)

type mailboxComponents struct {
	service         handler.MailboxService
	draftService    handler.MailboxDraftService
	verifier        handler.MailboxWebhookVerifier
	retentionWorker *mailbox.RetentionWorker
	draftWorker     *mailbox.DraftWorker
}

func newMailboxComponents(
	configuration config.MailboxConfig,
	apiReplicas int,
	store *db.Store,
	bus *events.Bus,
	redisClient *redis.Client,
	observer mailbox.Observer,
	logger *slog.Logger,
) (mailboxComponents, error) {
	if !configuration.Enabled {
		return mailboxComponents{}, nil
	}
	gate, err := newMailboxRequestGate(apiReplicas, redisClient, configuration.RequestsPerSecond)
	if err != nil {
		return mailboxComponents{}, fmt.Errorf("construct Mailbox provider request gate: %w", err)
	}
	provider, err := email.NewResendProvider(email.ResendOptions{
		APIKey: configuration.ResendAPIKey.Reveal(), RequestGate: gate,
		HTTPClient: &http.Client{Timeout: configuration.ProviderTimeout},
	})
	if err != nil {
		return mailboxComponents{}, fmt.Errorf("construct Mailbox Resend provider: %w", err)
	}
	verifier, err := newOptionalMailboxWebhookVerifier(configuration)
	if err != nil {
		return mailboxComponents{}, err
	}
	if verifier == nil {
		logger.Warn(
			"Mailbox webhook ingress disabled",
			"reason", "signing_secret_not_configured",
		)
	}
	var cache mailbox.DetailCache
	var expiryCache mailbox.ProviderExpiryCache
	if redisClient != nil {
		redisCache, cacheErr := mailbox.NewRedisDetailCache(redisClient, "zenith-team:mailbox")
		err = cacheErr
		if err != nil {
			return mailboxComponents{}, fmt.Errorf("construct Mailbox detail cache: %w", err)
		}
		cache = redisCache
		expiryCache = redisCache
	}
	service, err := mailbox.NewMailboxService(mailbox.ServiceOptions{
		Store: store, Provider: provider, Cache: cache, ExpiryCache: expiryCache, Bus: bus,
		Observer: observer, Domain: configuration.Domain,
	})
	if err != nil {
		return mailboxComponents{}, fmt.Errorf("construct Mailbox service: %w", err)
	}
	retentionWorker, err := mailbox.NewRetentionWorker(service, mailbox.RetentionWorkerOptions{
		OnError: newMailboxRetentionErrorHandler(logger),
	})
	if err != nil {
		return mailboxComponents{}, fmt.Errorf("construct Mailbox retention worker: %w", err)
	}
	draftService, err := mailbox.NewDraftService(mailbox.DraftServiceOptions{
		Store: store, Bus: bus, Domain: configuration.Domain,
	})
	if err != nil {
		return mailboxComponents{}, fmt.Errorf("construct Agent Mail draft service: %w", err)
	}
	draftWorker, err := mailbox.NewDraftWorker(mailbox.DraftWorkerOptions{
		Store: store, Provider: provider, Bus: bus,
		OnError: newAgentMailWorkerErrorHandler(logger),
	})
	if err != nil {
		return mailboxComponents{}, fmt.Errorf("construct Agent Mail worker: %w", err)
	}
	return mailboxComponents{
		service: service, draftService: draftService, verifier: verifier,
		retentionWorker: retentionWorker, draftWorker: draftWorker,
	}, nil
}

func newOptionalMailboxWebhookVerifier(
	configuration config.MailboxConfig,
) (handler.MailboxWebhookVerifier, error) {
	signingSecret := configuration.ResendWebhookSecret.Reveal()
	if signingSecret == "" {
		return nil, nil
	}
	verifier, err := email.NewResendWebhookVerifier(signingSecret, nil)
	if err != nil {
		return nil, fmt.Errorf("construct Mailbox Resend webhook verifier: %w", err)
	}
	return verifier, nil
}

func newMailboxRequestGate(
	apiReplicas int,
	redisClient *redis.Client,
	requestsPerSecond int,
) (email.RequestGate, error) {
	if apiReplicas < 1 {
		return nil, errors.New("API replica count must be positive")
	}
	if apiReplicas == 1 {
		return email.NewLocalRequestGate(requestsPerSecond)
	}
	if redisClient == nil {
		return nil, errors.New("Redis is required for multi-replica Mailbox provider limiting")
	}
	return email.NewRedisRequestGate(redisClient, "mailbox-resend", requestsPerSecond)
}
