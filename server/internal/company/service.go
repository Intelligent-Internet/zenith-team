// Package company owns Boardly's tenancy domain: the Company row, membership
// and roles, onboarding, Company-scoped typed resources, cross-entity search,
// and Company Plan application.
package company

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/usage"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

var (
	slugPattern       = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	taskPrefixPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,9}$`)
	reservedSlugs     = map[string]struct{}{
		"admin": {}, "api": {}, "app": {}, "auth": {}, "boardly": {},
		"callback": {}, "companies": {}, "dashboard": {}, "download": {},
		"healthz": {}, "login": {}, "logout": {}, "metrics": {}, "new": {},
		"oauth": {}, "onboarding": {}, "projects": {}, "readyz": {},
		"runs": {}, "settings": {}, "signup": {}, "tasks": {}, "usage": {},
		"v1": {}, "agents": {}, "ws": {}, "www": {},
	}
)

// CompanyService owns the Company row and its membership: onboarding, the
// tenancy lookups, and the authorization that turns a user into a
// Company-scoped principal.
type CompanyService interface { //nolint:revive // AGENTS.md mandates <Domain>Service; bare Service is forbidden.
	Create(context.Context, auth.UserPrincipal, CreateCompanyInput) (Response, error)
	Delete(context.Context, auth.UserPrincipal, int64) error
	Get(context.Context, auth.UserPrincipal, uuid.UUID) (Response, error)
	List(context.Context, auth.UserPrincipal) ([]Response, error)
	Bootstrap(context.Context, auth.UserPrincipal) (Bootstrap, error)
	Statistics(context.Context, auth.UserPrincipal) (Statistics, error)
	Authorize(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error)
	RequirePermission(context.Context, auth.UserPrincipal, uuid.UUID, auth.Permission) (auth.UserPrincipal, error)
	UpdateProfile(context.Context, auth.UserPrincipal, UpdateProfileInput) (Response, error)
	RemoveMember(context.Context, auth.UserPrincipal, MembershipMutation) error
	ChangeRole(context.Context, auth.UserPrincipal, ChangeRoleInput) (ActiveMemberResponse, error)
	TransferOwnership(context.Context, auth.UserPrincipal, MembershipMutation) ([]ActiveMemberResponse, error)
	Leave(context.Context, auth.UserPrincipal, int64) error
	ListMembers(context.Context, auth.UserPrincipal) ([]ActiveMemberResponse, error)
}

var _ CompanyService = (*DefaultCompanyService)(nil)

type DefaultCompanyService struct {
	store *db.Store
	bus   *events.Bus
}

type ServiceOptions struct {
	Store *db.Store
	Bus   *events.Bus
}

func NewCompanyService(options ServiceOptions) (*DefaultCompanyService, error) {
	if options.Store == nil {
		return nil, errors.New("company database store is required")
	}
	if options.Bus == nil {
		return nil, errors.New("Company event bus is required")
	}
	return &DefaultCompanyService{store: options.Store, bus: options.Bus}, nil
}

func (s *DefaultCompanyService) Create(
	ctx context.Context,
	principal auth.UserPrincipal,
	input CreateCompanyInput,
) (Response, error) {
	normalized, err := normalizeCreateInput(input)
	if err != nil {
		return Response{}, err
	}

	var result Response
	var pending []events.Event
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		pending = pending[:0]
		// A new Company has no row account deletion can discover or lock yet.
		// Fence on the enabled founder first: its SHARE lock conflicts with the
		// deletion transaction's NO KEY UPDATE lock. If creation wins, deletion's
		// later Company discovery sees the committed founder membership; if
		// deletion wins, this separate READ COMMITTED statement resumes against
		// the disabled User and returns no row.
		if _, err := tx.LockEnabledUserForMembershipMutation(
			ctx,
			principal.UserID,
		); errors.Is(err, pgx.ErrNoRows) {
			return ErrUnauthenticated
		} else if err != nil {
			return fmt.Errorf("lock enabled Company founder: %w", err)
		}
		companyRow, err := tx.CreateCompany(ctx, generated.CreateCompanyParams{
			Name:            normalized.Name,
			Slug:            normalized.Slug,
			TaskPrefix:      normalized.TaskPrefix,
			BusinessContext: normalized.BusinessContext,
			WebsiteUrl:      util.StrToText(normalized.WebsiteURL),
		})
		if err != nil {
			return mapCreateError("insert Company", err)
		}
		if err := usage.SeedHuddleBudgetInTx(ctx, tx, companyRow.ID); err != nil {
			return fmt.Errorf("seed Huddle AI budget: %w", err)
		}
		result, err = s.createOnboardingAggregate(
			ctx,
			tx,
			&pending,
			principal.UserID,
			companyRow.ID,
			normalized,
		)
		return err
	})
	if err != nil {
		return Response{}, err
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	return result, nil
}

func (s *DefaultCompanyService) Get(
	ctx context.Context,
	principal auth.UserPrincipal,
	companyID uuid.UUID,
) (Response, error) {
	if companyID == uuid.Nil {
		return Response{}, ErrNotFound
	}
	row, err := companyViewRow(ctx, s.store.Queries(), companyID, principal.UserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Response{}, ErrNotFound
	}
	if err != nil {
		return Response{}, fmt.Errorf("get Company for member: %w", err)
	}
	return companyDTOFromGetRow(row), nil
}

func (s *DefaultCompanyService) List(
	ctx context.Context,
	principal auth.UserPrincipal,
) ([]Response, error) {
	rows, err := s.store.Queries().ListCompaniesForUser(ctx, principal.UserID)
	if err != nil {
		return nil, fmt.Errorf("list Companies for user: %w", err)
	}
	companies := make([]Response, 0, len(rows))
	for _, row := range rows {
		companies = append(companies, companyDTOFromListRow(row))
	}
	return companies, nil
}

func (s *DefaultCompanyService) Statistics(
	ctx context.Context,
	principal auth.UserPrincipal,
) (Statistics, error) {
	row, err := s.store.Queries().GetCompanyStatistics(ctx, principal.CompanyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Statistics{}, ErrNotFound
	}
	if err != nil {
		return Statistics{}, fmt.Errorf("get Company statistics: %w", err)
	}
	return Statistics{
		MemberCount:        row.MemberCount,
		RuntimeCount:       row.RuntimeCount,
		RuntimeOnlineCount: row.RuntimeOnlineCount,
	}, nil
}

func (s *DefaultCompanyService) Authorize(
	ctx context.Context,
	principal auth.UserPrincipal,
	companyID uuid.UUID,
) (auth.UserPrincipal, error) {
	membership, err := s.store.Queries().GetActiveCompanyMembership(ctx, generated.GetActiveCompanyMembershipParams{
		CompanyID: companyID,
		UserID:    principal.UserID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.UserPrincipal{}, ErrNotFound
	}
	if err != nil {
		return auth.UserPrincipal{}, fmt.Errorf("authorize Company membership: %w", err)
	}
	principal.CompanyID = companyID
	principal.Role = auth.CompanyRole(membership.Role)
	return principal, nil
}

// RequirePermission resolves membership like Authorize and then refuses a
// role that does not hold the permission. Non-membership stays ErrNotFound so
// the refusal never reveals whether the Company exists.
func (s *DefaultCompanyService) RequirePermission(
	ctx context.Context,
	principal auth.UserPrincipal,
	companyID uuid.UUID,
	permission auth.Permission,
) (auth.UserPrincipal, error) {
	access, err := s.Authorize(ctx, principal, companyID)
	if err != nil {
		return auth.UserPrincipal{}, err
	}
	if !access.Can(permission) {
		return auth.UserPrincipal{}, ErrForbidden
	}
	return access, nil
}

func (s *DefaultCompanyService) createOnboardingAggregate(
	ctx context.Context,
	tx *generated.Queries,
	pending *[]events.Event,
	ownerID uuid.UUID,
	companyID uuid.UUID,
	input CreateCompanyInput,
) (Response, error) {
	memberRow, err := tx.CreateCompanyMember(ctx, generated.CreateCompanyMemberParams{
		CompanyID: companyID,
		UserID:    ownerID,
		Role:      string(auth.CompanyRoleOwner),
	})
	if err != nil {
		return Response{}, mapCreateError("insert Company owner", err)
	}
	if err := conversation.JoinGeneralChannelInTx(
		ctx,
		tx,
		pending,
		conversation.GeneralChannelMembership{CompanyID: companyID, UserID: ownerID},
	); err != nil {
		return Response{}, err
	}
	member := memberResponse(memberRow)
	activeRow, err := tx.GetActiveCompanyMemberProjection(ctx, generated.GetActiveCompanyMemberProjectionParams{
		CompanyID: companyID,
		UserID:    ownerID,
	})
	if err != nil {
		return Response{}, fmt.Errorf("load created Company owner projection: %w", err)
	}
	active := activeMemberResponse(
		memberResponse(activeRow.CompanyMember),
		activeRow.MemberEmail,
		activeRow.MemberDisplayName,
		util.TextToPtr(activeRow.MemberAvatarUrl),
		activeRow.AccountDisabled,
	)
	// No agents (and no CEO conversation) are seeded here: an agent is bound
	// to the runtime that executes it, and no runtime can exist before the
	// company does. The org is built through the agents API once a daemon
	// has registered.
	companyView, err := companyViewRow(ctx, tx, companyID, ownerID)
	if err != nil {
		return Response{}, fmt.Errorf("load created Company view: %w", err)
	}
	result := companyDTOFromGetRow(companyView)
	if err := stageOnboardingEvents(pending, ownerID, member, active, result); err != nil {
		return Response{}, err
	}
	return result, nil
}

// stageOnboardingEvents stages the founding aggregate's realtime events:
// company.created, the founding membership pair, and the daemon wakeup.
func stageOnboardingEvents(
	pending *[]events.Event,
	ownerID uuid.UUID,
	member MemberResponse,
	active ActiveMemberResponse,
	result Response,
) error {
	if err := stageMembershipEvents(pending, ownerID, member, active, MembershipGranted); err != nil {
		return fmt.Errorf("stage founding membership events: %w", err)
	}
	// The company room is empty at creation — nobody has joined it yet — so
	// the receipt also names the founder, whose own room it has to reach,
	// exactly like member.added.
	result.FounderUserID = ownerID
	stamp := time.Now().UTC()
	actor := events.UserActor(ownerID.String())
	*pending = append(*pending,
		events.Event{
			EntityType: Topic,
			Name:       EventCompanyCreated,
			CompanyID:  result.ID,
			CreatedAt:  stamp,
			Actor:      actor,
			Payload:    result,
		},
		events.Event{
			EntityType: Topic,
			Name:       EventCompanyMembershipChanged,
			CreatedAt:  stamp,
			Actor:      actor,
			Payload:    Membership{UserID: ownerID.String()},
		},
	)
	return nil
}

func normalizeCreateInput(input CreateCompanyInput) (CreateCompanyInput, error) {
	input.Name = protocol.TrimContractWhitespace(input.Name)
	input.Slug = protocol.TrimContractWhitespace(input.Slug)
	input.TaskPrefix = protocol.TrimContractWhitespace(input.TaskPrefix)
	input.BusinessContext = protocol.TrimContractWhitespace(input.BusinessContext)
	input.WebsiteURL = protocol.TrimContractWhitespace(input.WebsiteURL)
	_, slugReserved := reservedSlugs[input.Slug]
	if input.Name == "" ||
		!slugPattern.MatchString(input.Slug) ||
		!taskPrefixPattern.MatchString(input.TaskPrefix) || slugReserved || len(input.Slug) > 63 ||
		utf8.RuneCountInString(input.Name) > 120 ||
		len(input.BusinessContext) > 32768 ||
		!validWebsiteURL(input.WebsiteURL) {
		return CreateCompanyInput{}, ErrInvalidInput
	}
	return input, nil
}

// validWebsiteURL admits an absent website and otherwise requires an absolute
// http(s) URL: the CEO Agent fetches it during onboarding research, so a value
// that is not a reachable address is worse than none.
func validWebsiteURL(value string) bool {
	if value == "" {
		return true
	}
	parsed, err := url.ParseRequestURI(value)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}
