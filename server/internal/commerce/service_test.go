package commerce

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/db"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

type validationProvider struct{}

func (validationProvider) CreateAccount(
	context.Context,
	CreateAccountInput,
) (ProviderAccount, error) {
	return ProviderAccount{}, nil
}

func (validationProvider) FindAccount(
	context.Context,
	FindAccountInput,
) (FindAccountResult, error) {
	return FindAccountResult{}, nil
}

func (validationProvider) RetrieveAccount(context.Context, string) (ProviderAccount, error) {
	return ProviderAccount{}, nil
}

func (validationProvider) CreateAccountSession(
	context.Context,
	AccountSessionInput,
) (ProviderSession, error) {
	return ProviderSession{}, nil
}

func (validationProvider) CreateOnboardingLink(
	context.Context,
	OnboardingLinkInput,
) (ProviderLink, error) {
	return ProviderLink{}, nil
}

func (validationProvider) CreateDashboardLink(
	DashboardLinkInput,
) (ProviderDashboardLink, error) {
	return ProviderDashboardLink{}, nil
}

func TestNewServiceAllowsLoopbackHTTPOnlyForSandbox(t *testing.T) {
	t.Parallel()

	digest := [32]byte{1}
	base := ServiceOptions{
		Store: &db.Store{},
		Terms: TermsDocument{
			Version:     "2026-08-20",
			SHA256:      digest,
			DocumentURL: "https://app.boardly.test/legal/commerce-addendum",
			PrivacyURL:  "https://app.boardly.test/privacy",
		},
		AllowedCountries: []string{"GB"},
		Environments: map[Environment]EnvironmentOptions{
			EnvironmentSandbox: {
				Configured:     true,
				PublishableKey: "pk_test_BoardlyLocal123",
				ReturnURL:      "http://127.0.0.1:5173/connect/stripe/return",
				RefreshURL:     "http://127.0.0.1:5173/connect/stripe/refresh",
				Provider:       validationProvider{},
			},
		},
	}
	if _, err := NewCommerceService(base); err != nil {
		t.Fatalf("NewCommerceService(sandbox loopback) error = %v", err)
	}
	for _, timeout := range []time.Duration{
		time.Second - time.Millisecond,
		30*time.Second + time.Millisecond,
		time.Second + time.Nanosecond,
	} {
		base.ProviderCallTimeout = timeout
		if _, err := NewCommerceService(base); err == nil {
			t.Fatalf("NewCommerceService(provider call timeout %s) error = nil", timeout)
		}
	}
	base.ProviderCallTimeout = 0

	base.Environments = map[Environment]EnvironmentOptions{
		EnvironmentLive: {
			Configured:     true,
			PublishableKey: "pk_live_BoardlyProduction123",
			ReturnURL:      "http://127.0.0.1:5173/connect/stripe/return",
			RefreshURL:     "http://127.0.0.1:5173/connect/stripe/refresh",
			Provider:       validationProvider{},
		},
	}
	if _, err := NewCommerceService(base); err == nil {
		t.Fatal("NewCommerceService(live loopback) error = nil")
	}
}

func TestStatusFromProviderAccount(t *testing.T) {
	t.Parallel()
	allowedCountries := map[string]struct{}{"GB": {}}

	tests := []struct {
		name    string
		account ProviderAccount
		want    Status
	}{
		{name: "new account needs onboarding", account: validProviderAccount(), want: StatusOnboarding},
		{
			name: "currently due requirement needs action",
			account: withProviderAccount(func(account *ProviderAccount) {
				account.Requirements.CurrentlyDue = 1
			}),
			want: StatusActionRequired,
		},
		{
			name: "permanent provider rejection is restricted",
			account: withProviderAccount(func(account *ProviderAccount) {
				account.Requirements.DisabledReason = "rejected.terms_of_service"
			}),
			want: StatusRestricted,
		},
		{
			name: "submitted details pending capability review",
			account: withProviderAccount(func(account *ProviderAccount) {
				account.DetailsSubmitted = true
				account.CardPaymentsStatus = CapabilityStatusPending
			}),
			want: StatusPendingReview,
		},
		{
			name: "charges and payouts ready",
			account: withProviderAccount(func(account *ProviderAccount) {
				account.Country = "GB"
				account.DefaultCurrency = "gbp"
				account.DetailsSubmitted = true
				account.ChargesEnabled = true
				account.PayoutsEnabled = true
				account.CardPaymentsStatus = CapabilityStatusActive
				account.TransfersStatus = CapabilityStatusActive
			}),
			want: StatusReady,
		},
		{
			name: "enabled booleans without active capabilities stay pending",
			account: withProviderAccount(func(account *ProviderAccount) {
				account.Country = "GB"
				account.DefaultCurrency = "gbp"
				account.DetailsSubmitted = true
				account.ChargesEnabled = true
				account.PayoutsEnabled = true
			}),
			want: StatusPendingReview,
		},
		{
			name: "active capabilities without settlement identity stay pending",
			account: withProviderAccount(func(account *ProviderAccount) {
				account.DetailsSubmitted = true
				account.ChargesEnabled = true
				account.PayoutsEnabled = true
				account.CardPaymentsStatus = CapabilityStatusActive
				account.TransfersStatus = CapabilityStatusActive
			}),
			want: StatusPendingReview,
		},
		{
			name: "account outside release corridor is restricted",
			account: withProviderAccount(func(account *ProviderAccount) {
				account.Country = "VN"
				account.DetailsSubmitted = true
				account.ChargesEnabled = true
				account.PayoutsEnabled = true
				account.CardPaymentsStatus = CapabilityStatusActive
			}),
			want: StatusRestricted,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := statusFromProviderAccount(test.account, allowedCountries); got != test.want {
				t.Fatalf("statusFromProviderAccount() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestIntegrationAccountManifestReadinessRequiresEveryAuthority(t *testing.T) {
	t.Parallel()
	country := "GB"
	account := Account{
		Environment: EnvironmentLive, Configured: true, Status: StatusReady,
		ChargesEnabled: true, PayoutsEnabled: true, Country: &country,
		SyncStatus: "current",
	}
	allowedCountries := map[string]struct{}{"GB": {}}

	ready := integrationAccountFromAccount(account, true, true, allowedCountries)
	if !ready.ManifestReady {
		t.Fatalf("ready account = %+v", ready)
	}
	for _, test := range []struct {
		name             string
		termsAccepted    bool
		gatewayEnabled   bool
		allowedCountries map[string]struct{}
		mutate           func(*Account)
	}{
		{name: "terms missing", gatewayEnabled: true, allowedCountries: allowedCountries},
		{name: "gateway disabled", termsAccepted: true, allowedCountries: allowedCountries},
		{name: "country blocked", termsAccepted: true, gatewayEnabled: true, allowedCountries: map[string]struct{}{}},
		{name: "charges disabled", termsAccepted: true, gatewayEnabled: true, allowedCountries: allowedCountries, mutate: func(value *Account) { value.ChargesEnabled = false }},
		{name: "payouts disabled", termsAccepted: true, gatewayEnabled: true, allowedCountries: allowedCountries, mutate: func(value *Account) { value.PayoutsEnabled = false }},
		{name: "status pending", termsAccepted: true, gatewayEnabled: true, allowedCountries: allowedCountries, mutate: func(value *Account) { value.Status = StatusPendingReview }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := account
			if test.mutate != nil {
				test.mutate(&candidate)
			}
			got := integrationAccountFromAccount(
				candidate,
				test.termsAccepted,
				test.gatewayEnabled,
				test.allowedCountries,
			)
			if got.ManifestReady {
				t.Fatalf("integrationAccountFromAccount() = %+v", got)
			}
		})
	}
}

func TestValidateProviderAccountRejectsWrongControllerAndMalformedProjection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*ProviderAccount)
	}{
		{name: "malformed provider account id", mutate: func(account *ProviderAccount) {
			account.ID = "acct_secret/invalid"
		}},
		{name: "Boardly pays fees", mutate: func(account *ProviderAccount) {
			account.Controller.FeesPayer = "application"
		}},
		{name: "Boardly collects requirements", mutate: func(account *ProviderAccount) {
			account.Controller.RequirementCollection = "application"
		}},
		{name: "unsupported country shape", mutate: func(account *ProviderAccount) {
			account.Country = "gb"
		}},
		{name: "oversized requirement projection", mutate: func(account *ProviderAccount) {
			account.Requirements.EventuallyDue = 1025
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			account := validProviderAccount()
			test.mutate(&account)
			if err := validateProviderAccount(account); !errors.Is(err, ErrInvalidProviderState) {
				t.Fatalf("validateProviderAccount() error = %v, want invalid provider state", err)
			}
		})
	}
}

func TestPublicAccountJSONNeverExposesProviderAccountID(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 20, 12, 0, 0, 0, time.UTC)
	account := accountFromRow(
		validCommerceAccountRow(now),
		EnvironmentOptions{Configured: true, PublishableKey: "pk_test_public"},
	)
	payload, err := json.Marshal(account)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if strings.Contains(string(payload), "acct_") || strings.Contains(string(payload), "providerAccount") {
		t.Fatalf("public account leaked provider identity: %s", payload)
	}
}

func TestProviderErrorPreservesActionableProviderClass(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		target error
	}{
		{name: "provider setup", target: ErrProviderSetupRequired},
		{name: "provider credentials", target: ErrProviderCredentialsInvalid},
		{name: "provider permission", target: ErrProviderPermissionRequired},
		{name: "provider rate limit", target: ErrProviderRateLimited},
		{name: "tax setup", target: ErrTaxSetupRequired},
		{name: "checkout tax location", target: ErrCheckoutTaxLocationInvalid},
		{name: "provider request", target: ErrProviderRequestInvalid},
		{name: "operation recovery", target: ErrGatewayRecovery},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := providerError("create checkout", errors.Join(test.target, errors.New("provider cause")))
			if !errors.Is(err, test.target) {
				t.Fatalf("providerError() = %v, want %v", err, test.target)
			}
			if errors.Is(err, ErrProviderUnavailable) {
				t.Fatalf("providerError() = %v, unexpectedly classified as unavailable", err)
			}
		})
	}

	err := providerError("create checkout", errors.New("unclassified provider cause"))
	if !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("providerError(unclassified) = %v, want provider unavailable", err)
	}
}

func validProviderAccount() ProviderAccount {
	return ProviderAccount{
		ID: "acct_12345678",
		Controller: ControllerSnapshot{
			LossesPayments:        "stripe",
			FeesPayer:             "account",
			RequirementCollection: "stripe",
			DashboardType:         "full",
		},
		CardPaymentsStatus: CapabilityStatusUnrequested,
		TransfersStatus:    CapabilityStatusUnrequested,
	}
}

func withProviderAccount(mutate func(*ProviderAccount)) ProviderAccount {
	account := validProviderAccount()
	mutate(&account)
	return account
}

func validCommerceAccountRow(now time.Time) generated.CommerceAccount {
	return generated.CommerceAccount{
		ID:                              uuid.MustParse("0198c09b-4600-7000-8000-000000000001"),
		CompanyID:                       uuid.MustParse("0198c09b-4600-7000-8000-000000000002"),
		Environment:                     string(EnvironmentSandbox),
		ProviderAccountID:               pgtype.Text{String: "acct_12345678", Valid: true},
		Status:                          string(StatusOnboarding),
		CardPaymentsStatus:              pgtype.Text{String: string(CapabilityStatusUnrequested), Valid: true},
		TransfersStatus:                 pgtype.Text{String: string(CapabilityStatusUnrequested), Valid: true},
		ControllerLossesPayments:        pgtype.Text{String: "stripe", Valid: true},
		ControllerFeesPayer:             pgtype.Text{String: "account", Valid: true},
		ControllerRequirementCollection: pgtype.Text{String: "stripe", Valid: true},
		ControllerDashboardType:         pgtype.Text{String: "full", Valid: true},
		ControllerTemplateVersion:       pgtype.Int4{Int32: controllerTemplateVersion, Valid: true},
		CreatedByUserID:                 uuid.MustParse("0198c09b-4600-7000-8000-000000000003"),
		ReconciliationGeneration:        1,
		LastReconciledAt:                pgtype.Timestamptz{Time: now, Valid: true},
		CreatedAt:                       pgtype.Timestamptz{Time: now, Valid: true},
		UpdatedAt:                       pgtype.Timestamptz{Time: now, Valid: true},
		Version:                         2,
	}
}
