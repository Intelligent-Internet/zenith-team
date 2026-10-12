package stripe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	stripeapi "github.com/stripe/stripe-go/v86"
)

// DevelopmentEventOptions configures the local-development-only Stripe Events
// reader. The constructor accepts test restricted keys only, so this adapter
// cannot be accidentally reused as a live delivery mechanism.
type DevelopmentEventOptions struct {
	ReconcileKey      string
	PlatformAccountID string
	HTTPClient        *http.Client
}

// ErrDevelopmentEventPermissionRequired reports that the platform's local
// sandbox reconciliation key cannot read Stripe Events.
var ErrDevelopmentEventPermissionRequired = errors.New(
	"development event permission is required",
)

// DevelopmentEventProvider reads a bounded page of immutable test-mode Events
// for one connected account. Production delivery remains webhook-only.
type DevelopmentEventProvider struct {
	client            *stripeapi.Client
	platformAccountID string
}

// DevelopmentEventListInput bounds one connected account Events API request.
type DevelopmentEventListInput struct {
	AccountID        string
	CreatedAtOrAfter time.Time
	StartingAfter    string
	Limit            int64
	Types            []string
}

// DevelopmentEventResource is the immutable routing envelope Boardly uses to
// refetch the current Stripe resource before changing a projection.
type DevelopmentEventResource struct {
	ID           string
	AccountID    string
	Type         string
	ObjectID     string
	LiveMode     bool
	ProviderTime time.Time
}

// DevelopmentEventListResult is one provider page. HasMore is surfaced so the
// local worker can report that its deliberately bounded pass was truncated.
type DevelopmentEventListResult struct {
	Events     []DevelopmentEventResource
	HasMore    bool
	NextCursor string
}

// NewDevelopmentEventProvider constructs a test-key-only Events API reader.
func NewDevelopmentEventProvider(
	options DevelopmentEventOptions,
) (*DevelopmentEventProvider, error) {
	isCredentialInvalid := !validRestrictedKey(options.ReconcileKey, "rk_test_")
	isPlatformInvalid := !validAccountID(options.PlatformAccountID)
	isHTTPClientInvalid := options.HTTPClient == nil || options.HTTPClient.Timeout <= 0
	if isCredentialInvalid || isPlatformInvalid || isHTTPClientInvalid {
		return nil, errors.New("Stripe development Event provider configuration is invalid")
	}
	backends := stripeapi.NewBackends(options.HTTPClient)
	return &DevelopmentEventProvider{
		client:            stripeapi.NewClient(options.ReconcileKey, stripeapi.WithBackends(backends)),
		platformAccountID: options.PlatformAccountID,
	}, nil
}

// ListEvents reads one bounded recent page for exactly one connected account.
func (provider *DevelopmentEventProvider) ListEvents(
	ctx context.Context,
	input DevelopmentEventListInput,
) (DevelopmentEventListResult, error) {
	isProviderUnavailable := provider == nil || provider.client == nil || ctx == nil
	isAccountInvalid := provider != nil &&
		(!validAccountID(input.AccountID) || input.AccountID == provider.platformAccountID)
	isWindowInvalid := input.CreatedAtOrAfter.IsZero()
	isCursorInvalid := input.StartingAfter != "" &&
		!validProviderID(input.StartingAfter, "evt_")
	isBatchInvalid := input.Limit < 1 || input.Limit > 100
	if isProviderUnavailable || isAccountInvalid || isWindowInvalid || isCursorInvalid || isBatchInvalid ||
		!validDevelopmentEventTypes(input.Types) {
		return DevelopmentEventListResult{}, errors.New("invalid Stripe development Event request")
	}
	params := &stripeapi.EventListParams{
		CreatedRange: &stripeapi.RangeQueryParams{
			GreaterThanOrEqual: input.CreatedAtOrAfter.UTC().Unix(),
		},
	}
	params.Limit = stripeapi.Int64(input.Limit)
	params.Single = true
	if input.StartingAfter != "" {
		params.StartingAfter = stripeapi.String(input.StartingAfter)
	}
	params.SetStripeAccount(input.AccountID)
	for _, eventType := range input.Types {
		params.Types = append(params.Types, stripeapi.String(eventType))
	}
	page := provider.client.V1Events.List(ctx, params)
	if err := page.Err(); err != nil {
		if developmentEventPermissionRequired(err) {
			return DevelopmentEventListResult{}, fmt.Errorf(
				"list Stripe development events: %w",
				errors.Join(ErrDevelopmentEventPermissionRequired, err),
			)
		}
		return DevelopmentEventListResult{}, fmt.Errorf(
			"list Stripe development events: %w",
			err,
		)
	}
	result := DevelopmentEventListResult{
		Events:  make([]DevelopmentEventResource, 0, len(page.Data())),
		HasMore: page.Meta().HasMore,
	}
	allowedTypes := make(map[string]struct{}, len(input.Types))
	for _, eventType := range input.Types {
		allowedTypes[eventType] = struct{}{}
	}
	for _, event := range page.Data() {
		mapped, err := mapDevelopmentEvent(input.AccountID, allowedTypes, event)
		if err != nil {
			return DevelopmentEventListResult{}, err
		}
		result.Events = append(result.Events, mapped)
	}
	if result.HasMore {
		if len(result.Events) == 0 {
			return DevelopmentEventListResult{}, errors.New(
				"Stripe development Event page cannot advance",
			)
		}
		result.NextCursor = result.Events[len(result.Events)-1].ID
	}
	return result, nil
}

func developmentEventPermissionRequired(err error) bool {
	var stripeErr *stripeapi.Error
	return errors.As(err, &stripeErr) &&
		stripeErr.HTTPStatusCode == http.StatusForbidden &&
		stripeErr.Type == stripeapi.ErrorTypeInvalidRequest &&
		stripeErr.Code == stripeapi.ErrorCode("more_permissions_required")
}

func mapDevelopmentEvent(
	accountID string,
	allowedTypes map[string]struct{},
	event *stripeapi.Event,
) (DevelopmentEventResource, error) {
	isEventInvalid := event == nil || event.Data == nil || event.Livemode
	isAccountInvalid := event != nil && event.Account != "" && event.Account != accountID
	if isEventInvalid || isAccountInvalid {
		return DevelopmentEventResource{}, errors.New("Stripe development Event account or mode is invalid")
	}
	eventType := string(event.Type)
	if _, allowed := allowedTypes[eventType]; !allowed {
		return DevelopmentEventResource{}, errors.New("Stripe development Event type is unexpected")
	}
	var object struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(event.Data.Raw, &object); err != nil ||
		!validWebhookEnvelope(event.ID, accountID, eventType, object.ID, event.Created) {
		return DevelopmentEventResource{}, errors.New("Stripe development Event envelope is invalid")
	}
	return DevelopmentEventResource{
		ID: event.ID, AccountID: accountID, Type: eventType, ObjectID: object.ID,
		ProviderTime: time.Unix(event.Created, 0).UTC(),
	}, nil
}

func validDevelopmentEventTypes(values []string) bool {
	if len(values) < 1 || len(values) > 20 {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validWebhookType(value) || strings.TrimSpace(value) != value {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}
