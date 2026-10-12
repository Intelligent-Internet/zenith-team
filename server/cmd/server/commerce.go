package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"boardly.dev/boardly/server/internal/commerce"
	"boardly.dev/boardly/server/internal/config"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/handler"
	"boardly.dev/boardly/server/internal/inbox"
	"boardly.dev/boardly/server/internal/infra/siteverify"
	stripeinfra "boardly.dev/boardly/server/internal/infra/stripe"
)

type commerceTermsCatalogEnsurer interface {
	EnsureTermsCatalog(context.Context) error
}

func ensureCommerceTermsCatalog(ctx context.Context, service commerceTermsCatalogEnsurer) error {
	if service == nil {
		return nil
	}
	if err := service.EnsureTermsCatalog(ctx); err != nil {
		return fmt.Errorf("validate Commerce terms catalog: %w", err)
	}
	return nil
}

// handlerCommerceService is what the HTTP handler receives: the configured
// service, or a nil interface when Commerce is not configured here. A typed
// nil pointer must never reach the interface field — it reads as non-nil and
// the routes would call a nil receiver instead of answering
// commerce_checkout_disabled.
func handlerCommerceService(service *commerce.DefaultCommerceService) handler.CommerceService {
	if service == nil {
		return nil
	}
	return service
}

func newCommerceService(
	configuration config.CommerceConfig,
	store *db.Store,
	bus *events.Bus,
	inboxPresenter inbox.Presenter,
) (*commerce.DefaultCommerceService, error) {
	if !configuration.Configured {
		return nil, nil
	}
	siteHTTPClient, err := siteverify.NewSafeHTTPClient(configuration.ProviderTimeout)
	if err != nil {
		return nil, fmt.Errorf("construct Commerce Site verification HTTP client: %w", err)
	}
	siteProvider, err := siteverify.NewProvider(siteHTTPClient)
	if err != nil {
		return nil, fmt.Errorf("construct Commerce Site verification provider: %w", err)
	}
	environments := make(map[commerce.Environment]commerce.EnvironmentOptions, 2)
	for _, entry := range []struct {
		environment   commerce.Environment
		configuration config.CommerceEnvironmentConfig
		liveMode      bool
	}{
		{commerce.EnvironmentSandbox, configuration.Sandbox, false},
		{commerce.EnvironmentLive, configuration.Live, true},
	} {
		options := commerce.EnvironmentOptions{}
		if entry.configuration.Enabled {
			stripeHTTPClient := &http.Client{Timeout: configuration.ProviderTimeout}
			provider, err := stripeinfra.NewConnectProvider(stripeinfra.Options{
				SecretKey:         entry.configuration.ConnectKey.Reveal(),
				LiveMode:          entry.liveMode,
				PlatformAccountID: entry.configuration.PlatformAccountID,
				PlatformCountry:   "GB",
				HTTPClient:        stripeHTTPClient,
			})
			if err != nil {
				return nil, fmt.Errorf(
					"construct %s Stripe Connect provider: %w",
					entry.environment,
					err,
				)
			}
			gateway, err := stripeinfra.NewGatewayProvider(stripeinfra.GatewayOptions{
				CommerceKey:       entry.configuration.CommerceKey.Reveal(),
				ReconcileKey:      entry.configuration.ReconcileKey.Reveal(),
				WebhookSecret:     entry.configuration.WebhookSecret.Reveal(),
				LiveMode:          entry.liveMode,
				PlatformAccountID: entry.configuration.PlatformAccountID,
				HTTPClient:        stripeHTTPClient,
			})
			if err != nil {
				return nil, fmt.Errorf(
					"construct %s Stripe Commerce gateway: %w",
					entry.environment,
					err,
				)
			}
			options = commerce.EnvironmentOptions{
				Configured:     true,
				PublishableKey: entry.configuration.PublishableKey,
				ReturnURL:      configuration.ReturnURL,
				RefreshURL:     configuration.RefreshURL,
				Provider:       &commerceStripeProvider{provider: provider},
				Gateway:        &commerceStripeGateway{provider: gateway},
				Webhook:        &commerceStripeWebhook{provider: gateway},
			}
		}
		environments[entry.environment] = options
	}
	service, err := commerce.NewCommerceService(commerce.ServiceOptions{
		Store:                  store,
		Bus:                    bus,
		InboxPresenter:         inboxPresenter,
		AllowedCountries:       configuration.AllowedConnectedCountries,
		CatalogCheckoutEnabled: configuration.Enabled,
		SiteAuthority:          siteProvider,
		CustomerHMACKey:        []byte(configuration.CustomerHMACKey.Reveal()),
		ProviderCallTimeout:    configuration.ProviderTimeout,
		Terms: commerce.TermsDocument{
			Version:     configuration.Terms.Version,
			SHA256:      configuration.Terms.SHA256,
			DocumentURL: configuration.Terms.DocumentURL,
			PrivacyURL:  configuration.Terms.PrivacyURL,
		},
		Environments: environments,
	})
	if err != nil {
		return nil, fmt.Errorf("construct Commerce service: %w", err)
	}
	return service, nil
}

type commerceStripeProvider struct {
	provider *stripeinfra.ConnectProvider
}

var _ commerce.Provider = (*commerceStripeProvider)(nil)

func (provider *commerceStripeProvider) CreateAccount(
	ctx context.Context,
	input commerce.CreateAccountInput,
) (commerce.ProviderAccount, error) {
	account, err := provider.provider.CreateAccount(ctx, stripeinfra.CreateAccountInput{
		IdempotencyKey: input.IdempotencyKey, Metadata: input.Metadata,
		ControllerTemplateVersion: input.ControllerTemplateVersion,
	})
	return commerceProviderAccount(account), commerceStripeError(err)
}

func commerceStripeError(err error) error {
	if errors.Is(err, stripeinfra.ErrConnectSetupRequired) {
		return errors.Join(commerce.ErrProviderSetupRequired, err)
	}
	return err
}

func (provider *commerceStripeProvider) FindAccount(
	ctx context.Context,
	input commerce.FindAccountInput,
) (commerce.FindAccountResult, error) {
	result, err := provider.provider.FindAccount(ctx, stripeinfra.FindAccountInput{
		Metadata: input.Metadata, CreatedAfter: input.CreatedAfter,
		CreatedBefore: input.CreatedBefore,
		StartingAfter: input.StartingAfter, Limit: input.Limit,
	})
	return commerce.FindAccountResult{
		Account:    commerceProviderAccount(result.Account),
		Found:      result.Found,
		HasMore:    result.HasMore,
		NextCursor: result.NextCursor,
	}, err
}

func (provider *commerceStripeProvider) RetrieveAccount(
	ctx context.Context,
	accountID string,
) (commerce.ProviderAccount, error) {
	account, err := provider.provider.RetrieveAccount(ctx, accountID)
	return commerceProviderAccount(account), err
}

func (provider *commerceStripeProvider) CreateAccountSession(
	ctx context.Context,
	input commerce.AccountSessionInput,
) (commerce.ProviderSession, error) {
	session, err := provider.provider.CreateAccountSession(ctx, stripeinfra.AccountSessionInput{
		AccountID: input.AccountID, IdempotencyKey: input.IdempotencyKey,
	})
	return commerce.ProviderSession{
		ClientSecret: session.ClientSecret,
		ExpiresAt:    session.ExpiresAt,
	}, err
}

func (provider *commerceStripeProvider) CreateOnboardingLink(
	ctx context.Context,
	input commerce.OnboardingLinkInput,
) (commerce.ProviderLink, error) {
	link, err := provider.provider.CreateOnboardingLink(ctx, stripeinfra.OnboardingLinkInput{
		AccountID: input.AccountID, IdempotencyKey: input.IdempotencyKey,
		ReturnURL: input.ReturnURL, RefreshURL: input.RefreshURL,
	})
	return commerce.ProviderLink{URL: link.URL, ExpiresAt: link.ExpiresAt}, err
}

func (provider *commerceStripeProvider) CreateDashboardLink(
	input commerce.DashboardLinkInput,
) (commerce.ProviderDashboardLink, error) {
	link, err := provider.provider.CreateDashboardLink(stripeinfra.DashboardLinkInput{
		AccountID: input.AccountID,
	})
	return commerce.ProviderDashboardLink{URL: link.URL}, err
}

func commerceProviderAccount(account stripeinfra.Account) commerce.ProviderAccount {
	return commerce.ProviderAccount{
		ID: account.ID, Metadata: account.Metadata,
		Country: account.Country, DefaultCurrency: account.DefaultCurrency,
		DetailsSubmitted: account.DetailsSubmitted, ChargesEnabled: account.ChargesEnabled,
		PayoutsEnabled:     account.PayoutsEnabled,
		CardPaymentsStatus: commerce.CapabilityStatus(account.CardPaymentsStatus),
		TransfersStatus:    commerce.CapabilityStatus(account.TransfersStatus),
		Requirements: commerce.ProviderRequirements{
			CurrentlyDue:    account.Requirements.CurrentlyDue,
			EventuallyDue:   account.Requirements.EventuallyDue,
			PastDue:         account.Requirements.PastDue,
			CurrentDeadline: account.Requirements.CurrentDeadline,
			DisabledReason:  account.Requirements.DisabledReason,
		},
		Controller: commerce.ControllerSnapshot{
			LossesPayments:        account.Controller.LossesPayments,
			FeesPayer:             account.Controller.FeesPayer,
			RequirementCollection: account.Controller.RequirementCollection,
			DashboardType:         account.Controller.DashboardType,
		},
	}
}
