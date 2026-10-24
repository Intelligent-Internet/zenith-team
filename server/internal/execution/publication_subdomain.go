package execution

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	artifactdomain "github.com/Intelligent-Internet/zenith-team/server/internal/artifact"

	"github.com/google/uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	dbgen "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

// PublicationRoutes is the projection store consumed by the publication worker.
// The binding pins its account, namespace, and base domain across restarts.
type PublicationRoutes interface {
	BindingID() string
	BaseDomain() string
	Put(context.Context, string, string) error
	Delete(context.Context, string) error
}

type PublicationSubdomainInput struct {
	Subdomain        string `json:"subdomain"`
	ExpectedRevision int64  `json:"expectedRevision"`
}

type PublicationSubdomainOptions struct {
	BaseDomain string `json:"baseDomain"`
	CanSet     bool   `json:"canSet"`
}

var (
	ErrPublicationSubdomainTaken    = errors.New("publication subdomain is already claimed")
	ErrPublicationSubdomainDisabled = errors.New("publication subdomain routing is not configured")
)

func normalizePublicationSubdomain(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,61}[a-z0-9]$`).MatchString(value) ||
		strings.Contains(value, "--") {
		return "", ErrPublicationInvalidInput
	}
	switch value {
	case "www",
		"api",
		"app",
		"admin",
		"mail",
		"email",
		"smtp",
		"pop",
		"imap",
		"ftp",
		"ssh",
		"vpn",
		"proxy",
		"cdn",
		"static",
		"assets",
		"media",
		"docs",
		"help",
		"support",
		"status",
		"billing",
		"account",
		"login",
		"auth",
		"oauth",
		"sso",
		"dashboard",
		"console":
		return "", ErrPublicationInvalidInput
	}
	return value, nil
}

func publicationRoutesAvailable(routes PublicationRoutes, binding string) bool {
	return routes != nil && routes.BindingID() == binding
}

func publicationCanSetSubdomain(row dbgen.OutputPublication) bool {
	return row.Status == "published" || (row.Status == "failed" && row.RoutePending)
}

func (s *DefaultPublicationService) SetSubdomain(
	ctx context.Context,
	principal auth.UserPrincipal,
	runID uuid.UUID,
	input PublicationSubdomainInput,
) (Publication, error) {
	if !validPublicationRequest(ctx, s, principal, runID) || input.ExpectedRevision < 1 ||
		input.ExpectedRevision > protocol.MaxJSONSafeInteger {
		return Publication{}, ErrPublicationInvalidInput
	}
	subdomain, err := normalizePublicationSubdomain(input.Subdomain)
	if err != nil {
		return Publication{}, err
	}
	if s.routes == nil {
		return Publication{}, ErrPublicationSubdomainDisabled
	}
	hostname := subdomain + "." + s.routes.BaseDomain()
	var result Publication
	var changed events.Event
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *dbgen.Queries) error {
		if err := authorizePublicationOwnerInTx(ctx, q, principal); err != nil {
			return err
		}
		authority, err := getPublicationAuthority(ctx, q, principal, runID)
		if err != nil {
			return err
		}
		if err := s.publicationPolicy(authority, principal); err != nil {
			return err
		}
		row, err := q.GetOutputPublicationByOutputForUpdate(
			ctx,
			dbgen.GetOutputPublicationByOutputForUpdateParams{
				CompanyID: principal.CompanyID,
				OutputID:  authority.OutputID,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPublicationNotFound
		}
		if err != nil {
			return fmt.Errorf("lock publication subdomain: %w", err)
		}
		if _, ok := resolvePublicationProvider(s.providers, publicationProviderKeyFromRow(row)); !ok {
			return ErrPublicationDisabled
		}
		if row.Version != input.ExpectedRevision || row.LeaseID.Valid ||
			!publicationCanSetSubdomain(row) ||
			(row.CustomHostname.Valid && row.CustomHostname.String != hostname) {
			return ErrPublicationConflict
		}
		if row.CustomHostname.Valid && !publicationRoutesAvailable(s.routes, row.RouteBindingID.String) {
			return ErrPublicationSubdomainDisabled
		}
		if row.CustomHostname.Valid && !row.RoutePending {
			result = publicationFromRow(row)
			return nil
		}
		row, err = q.SetOutputPublicationSubdomain(ctx, dbgen.SetOutputPublicationSubdomainParams{
			PublicationID:     row.ID,
			ExpectedVersion:   input.ExpectedRevision,
			CustomHostname:    hostname,
			RouteBindingID:    s.routes.BindingID(),
			RequestedByUserID: principal.UserID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPublicationConflict
		}
		if err != nil {
			return fmt.Errorf("reserve publication subdomain: %w", err)
		}
		result = publicationFromRow(row)
		changed, err = publicationChangedInTx(ctx, q, row)
		return err
	})
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) && databaseError.Code == "23505" &&
		databaseError.ConstraintName == "output_publications_custom_hostname_key" {
		return Publication{}, ErrPublicationSubdomainTaken
	}
	if err == nil && changed.Name != "" {
		s.bus.Publish(ctx, artifactdomain.Topic, changed)
	}
	return result, err
}

func (w *PublicationWorker) routePublication(ctx context.Context, claim dbgen.ClaimOutputPublicationWorkRow) error {
	if !publicationRoutesAvailable(w.options.Routes, claim.RouteBindingID.String) {
		return w.failClaim(
			ctx,
			claim,
			claim.LeaseID.UUID,
			PublicationFailureProviderUnavailable,
			ErrPublicationSubdomainDisabled,
		)
	}
	if err := w.options.Routes.Put(ctx, claim.CustomHostname.String, claim.PublicUrl.String); err != nil {
		return w.handleProviderFailure(ctx, claim, claim.LeaseID.UUID, err, false)
	}
	err := w.mutate(ctx, func(q *dbgen.Queries) (dbgen.OutputPublication, error) {
		return q.CompleteOutputPublicationRouting(ctx,
			dbgen.CompleteOutputPublicationRoutingParams{
				PublicationID: claim.ID,
				LeaseID:       claim.LeaseID.UUID,
			})
	})
	if err != nil {
		return errors.Join(errPublicationStoreFailed, err)
	}
	return nil
}
