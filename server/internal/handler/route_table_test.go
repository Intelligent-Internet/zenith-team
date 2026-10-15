package handler

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/agent"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/calendar"
	"boardly.dev/boardly/server/internal/canvas"
	"boardly.dev/boardly/server/internal/commerce"
	"boardly.dev/boardly/server/internal/company"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/daemonws"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/inbox"
	"boardly.dev/boardly/server/internal/mailbox"
	"boardly.dev/boardly/server/internal/schedule"
	"boardly.dev/boardly/server/internal/social"
	"boardly.dev/boardly/server/internal/task"
	"boardly.dev/boardly/server/internal/usage"
	"boardly.dev/boardly/server/internal/wiki"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// Resource registrars mount routes conditionally, so a dependency that app.Run
// forgets to wire does not fail startup — it silently removes routes and the
// endpoints answer 404. Nothing else in the suite would notice: every other
// test in this package builds a Handler with only the services it exercises.
//
// This test builds a Handler with every field populated and pins the resulting
// route table. A wiring regression shows up here as a removed line rather than
// as a 404 in production.
func TestRouteTableIsFullyWiredWhenEveryDependencyIsPresent(t *testing.T) {
	t.Parallel()

	engine := New(fullyWiredHandler()).router

	got := make([]string, 0, len(engine.Routes()))
	for _, route := range engine.Routes() {
		got = append(got, route.Method+" "+route.Path)
	}
	sort.Strings(got)

	want := append([]string(nil), wantRouteTable...)
	sort.Strings(want)

	if diff := routeDiff(want, got); diff != "" {
		t.Fatalf("route table changed:\n%s", diff)
	}
}

func routeDiff(want, got []string) string {
	wantSet := make(map[string]bool, len(want))
	for _, route := range want {
		wantSet[route] = true
	}
	gotSet := make(map[string]bool, len(got))
	for _, route := range got {
		gotSet[route] = true
	}

	var builder strings.Builder
	for _, route := range want {
		if !gotSet[route] {
			fmt.Fprintf(&builder, "  missing: %s\n", route)
		}
	}
	for _, route := range got {
		if !wantSet[route] {
			fmt.Fprintf(&builder, "  unexpected: %s\n", route)
		}
	}
	return builder.String()
}

func fullyWiredHandler() Handler {
	return Handler{
		BoardService:             canvas.NewBoardService(nil, nil),
		BoardMediaService:        canvas.NewBoardMediaService(nil, nil),
		CanvasService:            canvas.NewCanvasService(nil, nil, nil),
		PageService:              canvas.NewPageService(nil, nil),
		PresentationService:      canvas.NewPresentationService(nil, nil, nil),
		Readiness:                routeTableReadiness{},
		StudioProjectService:     conversation.NewStudioProjectService(nil),
		StudioEditorService:      conversation.NewStudioEditorService(nil, nil, uuid.Nil),
		AllowedOrigins:           []string{"https://app.boardly.test"},
		CSRF:                     routeTableCSRF{},
		AuthService:              &authServiceStub{},
		AccountService:           &accountServiceStub{},
		DesktopHandoffService:    &desktopHandoffServiceStub{},
		GoogleOAuthService:       &googleOAuthServiceStub{},
		OAuthWebAppOrigin:        "https://app.boardly.test",
		CompanyService:           &companyServiceStub{},
		InitializationService:    &company.DefaultInitializationService{},
		MilestoneService:         &milestoneServiceStub{},
		ReferenceService:         &referenceServiceStub{},
		InvitationService:        &company.DefaultInvitationService{},
		CommerceService:          routeTableCommerce{},
		ProactiveModeService:     routeTableProactiveMode{},
		CompanySearchService:     routeTableCompanySearch{},
		AgentService:             &agentServiceStub{},
		SkillService:             &agent.DefaultSkillService{},
		AgentBindingService:      &agent.DefaultBindingService{},
		RuntimeService:           routeTableRuntime{},
		ConnectorService:         &agent.DefaultConnectorService{},
		PluginService:            &agent.DefaultPluginService{},
		UsageService:             routeTableUsage{},
		AnalyticsService:         routeTableUsage{},
		TaskService:              &taskServiceStub{},
		CompanyPlanService:       &companyPlanServiceStub{},
		ApprovalService:          &approvalServiceStub{},
		QuestionService:          &questionServiceStub{},
		RunProofResolver:         runProofResolverStub{},
		ConversationService:      &conversationServiceStub{},
		HumanConversationService: &conversation.DefaultConversationService{},
		DiscussionService:        &conversation.DefaultDiscussionService{},
		ChannelInvocationService: &conversation.DefaultChannelInvocationService{},
		HuddleService:            routeTableHuddleService{},
		HuddleBudgetService:      &usage.DefaultHuddleBudgetService{},
		HuddleAIService:          &conversation.DefaultHuddleAIService{},
		MessageContextService:    routeTableMessageContext{},
		ActivityFeed:             activityFeedStub{},
		InboxService:             routeTableInbox{},
		CrashReportService:       &crashReportServiceStub{},
		ProjectService:           &company.DefaultProjectService{},
		GroupService:             &company.DefaultGroupService{},
		WikiService:              &wiki.DefaultWikiService{},
		WikiAgentReadService:     &wiki.DefaultAgentReadService{},
		WikiAgentWriteService:    &wiki.DefaultAgentWriteService{},
		CompanyResourceService:   &companyResourceServiceStub{},
		AttachmentService:        &attachmentServiceStub{},
		DeliverableService:       &execution.DefaultDeliverableService{},
		PublicationService:       routeTablePublication{},
		MailboxService:           &mailbox.DefaultMailboxService{},
		MailboxDraftService:      &mailbox.DefaultDraftService{},
		MailboxWebhookVerifier:   &mailboxWebhookVerifierStub{},
		ScheduleService:          &schedule.DefaultScheduleService{},
		TaskCalendarService:      &task.DefaultCalendarService{},
		TaskReportService:        &task.DefaultReportService{},
		TaskMergeService:         routeTableTaskMerge{},
		RunService:               &execution.DefaultRunService{},
		Queries:                  &generated.Queries{},
		Bus:                      routeTableBus(),
		DaemonWSHub:              daemonws.NewHub(),
		AuthCookies:              CookieOptions{Secure: true},
		MetricsHandler:           http.NotFoundHandler(),
		HTTPMetrics:              routeTableMetrics{},
		ClientWebSocket:          http.NotFoundHandler(),
		CalendarService:          calendar.NewCalendarService(nil, nil, nil),
		SocialService:            &social.DefaultSocialService{},
	}
}

type routeTableHuddleService struct{}

func (routeTableHuddleService) Start(
	context.Context,
	auth.UserPrincipal,
	uuid.UUID,
	conversation.StartHuddleRequest,
) (conversation.StartHuddleResult, error) {
	return conversation.StartHuddleResult{}, nil
}

func (routeTableHuddleService) Join(
	context.Context,
	auth.UserPrincipal,
	uuid.UUID,
	conversation.JoinHuddleRequest,
) (conversation.JoinHuddleResult, error) {
	return conversation.JoinHuddleResult{}, nil
}

func (routeTableHuddleService) ConfirmConnected(
	context.Context,
	uuid.UUID,
	uuid.UUID,
) (conversation.JoinHuddleResult, error) {
	return conversation.JoinHuddleResult{}, nil
}

func (routeTableHuddleService) ConfirmConnectedByIdentity(
	context.Context,
	uuid.UUID,
	string,
	string,
) (conversation.JoinHuddleResult, error) {
	return conversation.JoinHuddleResult{}, nil
}

func (routeTableHuddleService) ConfirmConnectedByIdentityAnyCompany(
	context.Context,
	string,
	string,
) (conversation.JoinHuddleResult, error) {
	return conversation.JoinHuddleResult{}, nil
}
func (routeTableHuddleService) EndFromMedia(context.Context, uuid.UUID, string) error { return nil }
func (routeTableHuddleService) EndFromMediaAnyCompany(context.Context, string) error  { return nil }
func (routeTableHuddleService) EndParticipantFromMedia(context.Context, uuid.UUID, string, string) error {
	return nil
}

func (routeTableHuddleService) EndParticipantFromMediaAnyCompany(context.Context, string, string) error {
	return nil
}

func (routeTableHuddleService) Invite(
	context.Context,
	auth.UserPrincipal,
	uuid.UUID,
	conversation.InviteHuddleRequest,
) (conversation.HuddleInvitation, error) {
	return conversation.HuddleInvitation{}, nil
}

func (routeTableHuddleService) RespondInvitation(
	context.Context,
	auth.UserPrincipal,
	uuid.UUID,
	conversation.RespondHuddleInvitationRequest,
) (conversation.HuddleInvitation, error) {
	return conversation.HuddleInvitation{}, nil
}

func (routeTableHuddleService) Leave(
	context.Context,
	auth.UserPrincipal,
	uuid.UUID,
	uuid.UUID,
) (conversation.JoinHuddleResult, error) {
	return conversation.JoinHuddleResult{}, nil
}

func (routeTableHuddleService) SweepExpiredReservations(context.Context, time.Time) (int, error) {
	return 0, nil
}

func (routeTableHuddleService) SweepExpiredInvitations(context.Context, time.Time) (int, error) {
	return 0, nil
}

func (routeTableHuddleService) ListPendingInvitations(
	context.Context,
	auth.UserPrincipal,
) ([]conversation.HuddleInvitation, error) {
	return nil, nil
}

func (routeTableHuddleService) ListInvitationHistory(
	context.Context,
	auth.UserPrincipal,
) ([]conversation.HuddleInvitation, error) {
	return nil, nil
}
func (routeTableHuddleService) DeliverMissedInvitations(context.Context) (int, error) { return 0, nil }

func (routeTableHuddleService) ListConnections(
	context.Context,
	auth.UserPrincipal,
	uuid.UUID,
) ([]conversation.HuddleConnection, error) {
	return nil, nil
}

func (routeTableHuddleService) ReconcileMediaRooms(context.Context) (int, error) {
	return 0, nil
}

// routeTableBus exists because the daemon-route registrar gates on a non-nil
// Bus; construction cannot fail with default options.
func routeTableBus() *events.Bus {
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		panic(err)
	}
	return bus
}

// The stubs below exist only so New takes the wired branch for services whose
// own route tests build fakes inline. Handlers are enumerated, never invoked.

type routeTableReadiness struct{}

func (routeTableReadiness) Ping(context.Context) error { return nil }

type routeTablePublication struct{}

func (routeTablePublication) SetSubdomain(
	context.Context,
	auth.UserPrincipal,
	uuid.UUID,
	execution.PublicationSubdomainInput,
) (execution.Publication, error) {
	return execution.Publication{}, nil
}

func (routeTablePublication) Fetch(
	context.Context, auth.UserPrincipal, uuid.UUID,
) (execution.PublicationView, error) {
	return execution.PublicationView{}, nil
}

func (routeTablePublication) Create(
	context.Context, auth.UserPrincipal, uuid.UUID, execution.PublicationCreateInput,
) (execution.PublicationCreateResult, error) {
	return execution.PublicationCreateResult{}, nil
}

func (routeTablePublication) Unpublish(
	context.Context, auth.UserPrincipal, uuid.UUID, execution.PublicationUnpublishInput,
) (execution.Publication, error) {
	return execution.Publication{}, nil
}

type routeTableCompanySearch struct{}

func (routeTableCompanySearch) Search(
	context.Context,
	auth.UserPrincipal,
	company.SearchInput,
) (company.SearchPage, error) {
	return company.SearchPage{}, nil
}

type routeTableProactiveMode struct{}

func (routeTableProactiveMode) Get(
	context.Context,
	auth.UserPrincipal,
) (company.ProactiveMode, error) {
	return company.ProactiveMode{}, nil
}

func (routeTableProactiveMode) Update(
	context.Context,
	auth.UserPrincipal,
	company.UpdateProactiveModeInput,
) (company.ProactiveMode, error) {
	return company.ProactiveMode{}, nil
}

type routeTableRuntime struct{}

type routeTableTaskMerge struct{}

func (routeTableTaskMerge) Claim(
	context.Context,
	auth.UserPrincipal,
	uuid.UUID,
	string,
) (*task.MergeClaim, error) {
	return nil, nil
}

func (routeTableTaskMerge) ClaimManual(
	context.Context,
	auth.UserPrincipal,
	uuid.UUID,
	uuid.UUID,
	string,
	task.ManualMergeClaimInput,
) (task.MergeClaim, error) {
	return task.MergeClaim{}, nil
}

func (routeTableTaskMerge) RecordResult(
	context.Context,
	auth.UserPrincipal,
	uuid.UUID,
	string,
	uuid.UUID,
	task.MergeResultInput,
) error {
	return nil
}

func (routeTableTaskMerge) RenewLease(
	context.Context,
	auth.UserPrincipal,
	uuid.UUID,
	string,
	uuid.UUID,
	int32,
) error {
	return nil
}

func (routeTableRuntime) List(
	context.Context,
	auth.UserPrincipal,
	agent.RuntimeListOptions,
) (agent.RuntimePage, error) {
	return agent.RuntimePage{}, nil
}

func (routeTableRuntime) Get(
	context.Context,
	auth.UserPrincipal,
	uuid.UUID,
) (agent.Runtime, error) {
	return agent.Runtime{}, nil
}

func (routeTableRuntime) Rename(
	context.Context,
	auth.UserPrincipal,
	uuid.UUID,
	agent.RuntimeRenameInput,
) (agent.Runtime, error) {
	return agent.Runtime{}, nil
}

func (routeTableRuntime) Remove(
	context.Context,
	auth.UserPrincipal,
	uuid.UUID,
	agent.RuntimeRemovalInput,
) (agent.RuntimeRemoval, error) {
	return agent.RuntimeRemoval{}, nil
}

func (routeTableRuntime) RegisterRuntimes(
	context.Context,
	agent.RuntimeRegistration,
) ([]agent.Runtime, error) {
	return nil, nil
}

func (routeTableRuntime) DeregisterRuntimes(
	context.Context,
	auth.UserPrincipal,
	agent.RuntimeDeregistration,
) error {
	return nil
}

func (routeTableRuntime) Heartbeat(context.Context, auth.UserPrincipal, uuid.UUID, string) error {
	return nil
}

func (routeTableRuntime) InitiateModelDiscovery(
	context.Context,
	auth.UserPrincipal,
	uuid.UUID,
) (agent.ModelDiscoveryRequest, error) {
	return agent.ModelDiscoveryRequest{}, nil
}

func (routeTableRuntime) RequestRefresh(context.Context, auth.UserPrincipal, uuid.UUID) error {
	return nil
}

func (routeTableRuntime) GetModelDiscovery(
	context.Context,
	auth.UserPrincipal,
	uuid.UUID,
	uuid.UUID,
) (agent.ModelDiscoveryRequest, error) {
	return agent.ModelDiscoveryRequest{}, nil
}

func (routeTableRuntime) ClaimModelDiscovery(
	context.Context,
	uuid.UUID,
) (*agent.ModelDiscoveryRequest, error) {
	return nil, nil
}

func (routeTableRuntime) FinishModelDiscovery(
	context.Context,
	uuid.UUID,
	uuid.UUID,
	agent.ModelDiscoveryResult,
) (agent.ModelDiscoveryRequest, error) {
	return agent.ModelDiscoveryRequest{}, nil
}

type routeTableMessageContext struct{}

func (routeTableMessageContext) FetchMessageContext(
	context.Context,
	auth.UserPrincipal,
	uuid.UUID,
	uuid.UUID,
	int,
) (conversation.MessageContextPage, error) {
	return conversation.MessageContextPage{}, nil
}

type routeTableCommerce struct{}

func (routeTableCommerce) Summary(
	context.Context,
	auth.UserPrincipal,
) (commerce.Summary, error) {
	return commerce.Summary{}, nil
}

func (routeTableCommerce) IntegrationReadiness(
	context.Context,
	auth.UserPrincipal,
) (commerce.IntegrationReadiness, error) {
	return commerce.IntegrationReadiness{}, nil
}

func (routeTableCommerce) AcceptTerms(
	context.Context,
	auth.UserPrincipal,
	commerce.AcceptTermsInput,
) (commerce.AcceptanceResult, error) {
	return commerce.AcceptanceResult{}, nil
}

func (routeTableCommerce) CreateAccountSession(
	context.Context,
	auth.UserPrincipal,
	commerce.Environment,
) (commerce.AccountSession, error) {
	return commerce.AccountSession{}, nil
}

func (routeTableCommerce) CreateOnboardingLink(
	context.Context,
	auth.UserPrincipal,
	commerce.Environment,
) (commerce.OnboardingLink, error) {
	return commerce.OnboardingLink{}, nil
}

func (routeTableCommerce) CreateDashboardLink(
	context.Context,
	auth.UserPrincipal,
	commerce.Environment,
) (commerce.DashboardLink, error) {
	return commerce.DashboardLink{}, nil
}

func (routeTableCommerce) SubmitManifest(
	context.Context,
	auth.UserPrincipal,
	commerce.TrustedManifestContext,
	commerce.SubmitManifestInput,
) (commerce.ManifestSubmission, error) {
	return commerce.ManifestSubmission{}, nil
}

func (routeTableCommerce) ManifestStatus(
	context.Context,
	auth.UserPrincipal,
	commerce.ManifestStatusInput,
) (commerce.ManifestStatus, error) {
	return commerce.ManifestStatus{}, nil
}

func (routeTableCommerce) ListManifests(
	context.Context,
	auth.UserPrincipal,
	commerce.ManifestListOptions,
) (commerce.ManifestPage, error) {
	return commerce.ManifestPage{}, nil
}

func (routeTableCommerce) ApproveAndPublishManifest(
	context.Context,
	auth.UserPrincipal,
	commerce.ApproveManifestInput,
) (commerce.ManifestStatus, error) {
	return commerce.ManifestStatus{}, nil
}

func (routeTableCommerce) CreateSiteChallenge(
	context.Context,
	commerce.SiteChallengeRequest,
) (commerce.SiteChallenge, error) {
	return commerce.SiteChallenge{}, nil
}

func (routeTableCommerce) RegisterLocalSigningKey(
	context.Context,
	auth.UserPrincipal,
	commerce.TrustedLocalSigningKeyContext,
	commerce.LocalSigningKeyRequest,
) (commerce.LocalSigningKeyRegistration, error) {
	return commerce.LocalSigningKeyRegistration{}, nil
}

func (routeTableCommerce) SyncSigningKeys(
	context.Context,
	auth.UserPrincipal,
	commerce.SyncSigningKeysRequest,
) ([]commerce.SigningKeySummary, error) {
	return []commerce.SigningKeySummary{}, nil
}

func (routeTableCommerce) RevokeSigningKey(
	context.Context,
	auth.UserPrincipal,
	commerce.RevokeSigningKeyRequest,
) (commerce.SigningKeySummary, error) {
	return commerce.SigningKeySummary{}, nil
}

func (routeTableCommerce) CreateCheckout(
	context.Context,
	commerce.SiteSignedRequest,
	commerce.CheckoutRequest,
) (commerce.HostedRedirect, error) {
	return commerce.HostedRedirect{}, nil
}

func (routeTableCommerce) CreatePortal(
	context.Context,
	commerce.SiteSignedRequest,
	commerce.PortalRequest,
) (commerce.HostedRedirect, error) {
	return commerce.HostedRedirect{}, nil
}

func (routeTableCommerce) VerifyEntitlements(
	context.Context,
	commerce.SiteSignedRequest,
) (commerce.EntitlementVerification, error) {
	return commerce.EntitlementVerification{}, nil
}

func (routeTableCommerce) SessionJWKSet() (commerce.SessionJWKSet, error) {
	return commerce.SessionJWKSet{}, nil
}

func (routeTableCommerce) ProcessWebhook(
	context.Context,
	[]byte,
	string,
) (commerce.WebhookReceiptResult, error) {
	return commerce.WebhookReceiptResult{}, nil
}

type routeTableUsage struct{}

func (routeTableUsage) ReportRunUsage(
	context.Context,
	uuid.UUID,
	[]usage.RunUsageInput,
) error {
	return nil
}

func (routeTableUsage) GetActivityInsight(context.Context, auth.UserPrincipal) (usage.ActivityInsight, error) {
	return usage.ActivityInsight{}, nil
}

func (routeTableUsage) Get(
	context.Context,
	auth.UserPrincipal,
	uuid.UUID,
	usage.Period,
) (usage.AgentUsageReport, error) {
	return usage.AgentUsageReport{}, nil
}

func (routeTableUsage) GetCompany(
	context.Context,
	auth.UserPrincipal,
	usage.CompanyPeriod,
) (usage.CompanyUsageReport, error) {
	return usage.CompanyUsageReport{}, nil
}

func (routeTableUsage) GetAnalyticsSummary(
	context.Context,
	auth.UserPrincipal,
	usage.AnalyticsQuery,
) (usage.AnalyticsSummary, error) {
	return usage.AnalyticsSummary{}, nil
}

func (routeTableUsage) GetAnalyticsSeries(
	context.Context,
	auth.UserPrincipal,
	usage.AnalyticsQuery,
) (usage.AnalyticsSeries, error) {
	return usage.AnalyticsSeries{}, nil
}

func (routeTableUsage) GetAnalyticsHeatmap(
	context.Context,
	auth.UserPrincipal,
	usage.AnalyticsQuery,
) (usage.AnalyticsHeatmap, error) {
	return usage.AnalyticsHeatmap{}, nil
}

func (routeTableUsage) GetAnalyticsBreakdown(
	context.Context,
	auth.UserPrincipal,
	usage.AnalyticsQuery,
) (usage.AnalyticsBreakdown, error) {
	return usage.AnalyticsBreakdown{}, nil
}

func (routeTableUsage) GetAnalyticsFailures(
	context.Context,
	auth.UserPrincipal,
	usage.AnalyticsQuery,
) (usage.AnalyticsFailures, error) {
	return usage.AnalyticsFailures{}, nil
}

func (routeTableUsage) GetTaskUsage(context.Context, auth.UserPrincipal, uuid.UUID) (usage.AnalyticsTaskUsage, error) {
	return usage.AnalyticsTaskUsage{}, nil
}

func (routeTableUsage) GetAnalyticsBudgets(context.Context, auth.UserPrincipal) ([]usage.AnalyticsBudget, error) {
	return nil, nil
}

func (routeTableUsage) UpsertAnalyticsBudget(
	context.Context,
	auth.UserPrincipal,
	usage.AnalyticsBudgetInput,
) (usage.AnalyticsBudget, error) {
	return usage.AnalyticsBudget{}, nil
}

type routeTableCSRF struct{}

func (routeTableCSRF) Validate(string, string) bool { return true }
func (routeTableCSRF) Token(string) string          { return "csrf" }

type routeTableMetrics struct{}

func (routeTableMetrics) ObserveHTTP(string, string, int, time.Duration) {}

type routeTableInbox struct{}

func (routeTableInbox) List(context.Context, auth.UserPrincipal, inbox.ListOptions) (inbox.Page, error) {
	return inbox.Page{}, nil
}

func (routeTableInbox) Counts(context.Context, auth.UserPrincipal) (inbox.Counts, error) {
	return inbox.Counts{}, nil
}

func (routeTableInbox) Get(context.Context, auth.UserPrincipal, uuid.UUID) (inbox.Item, error) {
	return inbox.Item{}, nil
}

func (routeTableInbox) Mutate(
	context.Context,
	auth.UserPrincipal,
	uuid.UUID,
	inbox.Mutation,
	int64,
) (inbox.MutationResult, error) {
	return inbox.MutationResult{}, nil
}

func (routeTableInbox) ReadAll(context.Context, auth.UserPrincipal) (inbox.ReadAllResult, error) {
	return inbox.ReadAllResult{}, nil
}

func (routeTableInbox) ReadConversation(
	context.Context,
	auth.UserPrincipal,
	uuid.UUID,
) (inbox.ReadAllResult, error) {
	return inbox.ReadAllResult{}, nil
}

// wantRouteTable is the complete served surface. Regenerate deliberately, never
// by pasting whatever the failure printed: an unexpected line is usually a real
// new endpoint that wants review, and a missing line is usually a lost wire.
var wantRouteTable = []string{
	"GET /api/v1/companies/:companyId/calendar/tasks",
	"GET /api/v1/companies/:companyId/tasks/:taskId/schedule",
	"PUT /api/v1/companies/:companyId/tasks/:taskId/schedule",
	"POST /api/v1/companies/:companyId/tasks/:taskId/schedule/occurrences",
	"POST /api/v1/companies/:companyId/tasks/:taskId/assignment",
	"PUT /api/v1/companies/:companyId/artifacts/:artifactId/review/items/:outputItemId/document/history/:sequence/name",
	"POST /api/v1/companies/:companyId/artifacts/:artifactId/review/items/:outputItemId/document/history/:sequence/restore",
	"GET /api/v1/companies/:companyId/artifacts/:artifactId/review/items/:outputItemId/document/history/:sequence",
	"GET /api/v1/companies/:companyId/artifacts/:artifactId/review/items/:outputItemId/document/history",
	"PUT /api/v1/companies/:companyId/projects/:projectId/canvas/versions/:versionId/document/history/:sequence/name",
	"POST /api/v1/companies/:companyId/projects/:projectId/canvas/versions/:versionId/document/history/:sequence/restore",
	"GET /api/v1/companies/:companyId/projects/:projectId/canvas/versions/:versionId/document/history/:sequence",
	"GET /api/v1/companies/:companyId/projects/:projectId/canvas/versions/:versionId/document/history",
	"GET /api/v1/public/artifacts/:token/whiteboard",
	"POST /api/v1/public/artifacts/:token/whiteboard/hydrate",
	"GET /api/v1/public/artifacts/:token/whiteboard/files/:attachmentId",
	"POST /api/v1/companies/:companyId/runs/:runId/agent-artifacts/:artifactId/whiteboard/view",
	"POST /api/v1/companies/:companyId/runs/:runId/agent-artifacts/:artifactId/whiteboard/operations",
	"POST /api/v1/companies/:companyId/runs/:runId/agent-artifacts/:artifactId/whiteboard/compose",
	"POST /api/v1/companies/:companyId/runs/:runId/agent-artifacts/:artifactId/whiteboard/context",
	"GET /api/v1/companies/:companyId/artifacts/:artifactId/whiteboard",
	"POST /api/v1/companies/:companyId/artifacts/:artifactId/whiteboard/operations",
	"POST /api/v1/companies/:companyId/artifacts/:artifactId/whiteboard/hydrate",
	"POST /api/v1/companies/:companyId/artifacts/:artifactId/whiteboard/text",
	"PUT /api/v1/companies/:companyId/artifacts/:artifactId/whiteboard/title",
	"PUT /api/v1/companies/:companyId/artifacts/:artifactId/whiteboard/presence",
	"GET /api/v1/companies/:companyId/artifacts/:artifactId/whiteboard/files/:attachmentId",
	"POST /api/v1/companies/:companyId/artifacts/:artifactId/whiteboard/files",
	"GET /api/v1/companies/:companyId/artifacts/:artifactId/whiteboard/preview",

	"GET /api/v1/companies/:companyId/message-skills",
	"GET /api/v1/companies/:companyId/message-connectors",
	"GET /api/v1/companies/:companyId/agents/:agentId/skills",
	"POST /api/v1/companies/:companyId/agents/:agentId/skills",
	"POST /api/v1/companies/:companyId/agents/:agentId/skill-imports/upload",
	"POST /api/v1/companies/:companyId/agents/:agentId/skill-imports/github",
	"PATCH /api/v1/companies/:companyId/agents/:agentId/skills/:skillId",
	"DELETE /api/v1/companies/:companyId/agents/:agentId/skills/:skillId",
	"GET /api/v1/companies/:companyId/agents/:agentId/skills/:skillId/reference",
	"GET /api/v1/companies/:companyId/projects/:projectId/canvas",
	"GET /api/v1/companies/:companyId/projects/:projectId/overview",
	"POST /api/v1/companies/:companyId/projects/:projectId/overview",
	"GET /api/v1/companies/:companyId/projects/:projectId/pages",
	"POST /api/v1/companies/:companyId/projects/:projectId/pages",
	"GET /api/v1/companies/:companyId/projects/:projectId/pages/:pageId",
	"PATCH /api/v1/companies/:companyId/projects/:projectId/pages/:pageId",
	"GET /api/v1/companies/:companyId/projects/:projectId/pages/:pageId/threads",
	"GET /api/v1/companies/:companyId/projects/:projectId/pages/:pageId/changes",
	"GET /api/v1/companies/:companyId/projects/:projectId/pages/:pageId/changes/:changeId",
	"POST /api/v1/companies/:companyId/projects/:projectId/pages/:pageId/changes/:changeId",
	"GET /api/v1/companies/:companyId/projects/:projectId/pages/:pageId/presentation-preview",
	"GET /api/v1/companies/:companyId/projects/:projectId/pages/:pageId/presentations",
	"POST /api/v1/companies/:companyId/projects/:projectId/pages/:pageId/presentations",
	"GET /api/v1/companies/:companyId/projects/:projectId/presentations",
	"GET /api/v1/companies/:companyId/projects/:projectId/presentations/:presentationId",
	"GET /api/v1/companies/:companyId/projects/:projectId/presentations/:presentationId/export",
	"GET /api/v1/companies/:companyId/runs/:runId/agent-project",
	"GET /api/v1/companies/:companyId/runs/:runId/agent-sources",
	"POST /api/v1/companies/:companyId/runs/:runId/agent-pages",
	"GET /api/v1/companies/:companyId/runs/:runId/agent-pages/:pageId",
	"POST /api/v1/companies/:companyId/runs/:runId/agent-pages/:pageId",
	"GET /api/v1/companies/:companyId/runs/:runId/agent-pages/:pageId/presentation-preview",
	"POST /api/v1/companies/:companyId/runs/:runId/agent-pages/:pageId/presentations",
	"GET /api/v1/companies/:companyId/projects/:projectId/canvas/timeline",
	"POST /api/v1/companies/:companyId/projects/:projectId/canvas/operations",
	"GET /api/v1/companies/:companyId/projects/:projectId/canvas/artifacts/:artifactId/history",
	"GET /api/v1/companies/:companyId/projects/:projectId/canvas/versions/:versionId/threads",
	"GET /api/v1/companies/:companyId/projects/:projectId/canvas/versions/:versionId/body",
	"GET /api/v1/companies/:companyId/projects/:projectId/canvas/versions/:versionId/document",
	"POST /api/v1/companies/:companyId/projects/:projectId/canvas/versions/:versionId/document",
	"POST /api/v1/companies/:companyId/projects/:projectId/canvas/versions/:versionId/document/ids",
	"GET /api/v1/companies/:companyId/projects/:projectId/canvas/library",
	"GET /api/v1/companies/:companyId/projects/:projectId/canvas/search",
	"PUT /api/v1/companies/:companyId/projects/:projectId/canvas/presence",

	"GET /api/v1/me/inbox",
	"GET /api/v1/me/inbox/counts",
	"GET /api/v1/me/inbox/:itemId",
	"POST /api/v1/me/inbox/read-all",
	"POST /api/v1/me/inbox/:itemId/:mutation",
	"GET /api/v1/me/calendar/catalog",
	"POST /api/v1/me/calendar/events/:eventId/response",
	"GET /api/v1/me/calendar/events",
	"POST /api/v1/me/calendar/events",
	"PUT /api/v1/me/calendar/events/:eventId",
	"DELETE /api/v1/me/calendar/events/:eventId",
	"POST /api/v1/me/calendar/events/:eventId/occurrences",
	"GET /api/v1/companies/:companyId/calendar/catalog",
	"POST /api/v1/companies/:companyId/calendar/events/:eventId/response",
	"GET /api/v1/companies/:companyId/calendar/events",
	"POST /api/v1/companies/:companyId/calendar/events",
	"PUT /api/v1/companies/:companyId/calendar/events/:eventId",
	"DELETE /api/v1/companies/:companyId/calendar/events/:eventId",
	"POST /api/v1/companies/:companyId/calendar/events/:eventId/occurrences",
	"GET /api/v1/companies/:companyId/conversations/:conversationId/calendar",
	"POST /api/v1/companies/:companyId/conversations/:conversationId/calendar",
	"GET /api/v1/me/social/platforms",
	"POST /api/v1/me/social/oauth/:platform/desktop",
	"GET /api/v1/social/oauth/:platform/launch",
	"GET /api/v1/companies/:companyId/conversations/:conversationId/social",
	"POST /api/v1/companies/:companyId/conversations/:conversationId/social/posts",
	"GET /api/v1/me/social/connections",
	"POST /api/v1/me/social/oauth/:platform",
	"DELETE /api/v1/me/social/connections/:connectionId",
	"GET /api/v1/social/oauth/:platform/callback",

	"POST /api/v1/companies/:companyId/human-conversations",
	"GET /api/v1/companies/:companyId/human-conversations",
	"GET /api/v1/companies/:companyId/human-conversations/:conversationId",
	"POST /api/v1/companies/:companyId/human-conversations/:conversationId/members",
	"POST /api/v1/companies/:companyId/channels",
	"GET /api/v1/companies/:companyId/channels",
	"GET /api/v1/companies/:companyId/channels/:conversationId",
	"PATCH /api/v1/companies/:companyId/channels/:conversationId",
	"PUT /api/v1/companies/:companyId/channels/:conversationId/members/:userId",
	"DELETE /api/v1/companies/:companyId/channels/:conversationId/members/:userId",
	"GET /api/v1/companies/:companyId/channels/:conversationId/agent-invocations",
	"GET /api/v1/companies/:companyId/channels/:conversationId/agents/:agentId/activity",
	"POST /api/v1/companies/:companyId/channels/:conversationId/agent-invocations/:invocationId/retry",
	"PUT /api/v1/companies/:companyId/channels/:conversationId/agents/:agentId",
	"DELETE /api/v1/companies/:companyId/channels/:conversationId/agents/:agentId",
	"POST /api/v1/companies/:companyId/channels/:conversationId/leave",
	"POST /api/v1/companies/:companyId/channels/:conversationId/archive",
	"POST /api/v1/companies/:companyId/channels/:conversationId/restore",
	"GET /api/v1/companies/:companyId/conversations/:conversationId/discussion-messages",
	"POST /api/v1/companies/:companyId/conversations/:conversationId/huddles",
	"POST /api/v1/companies/:companyId/huddles/:huddleId/connections",
	"GET /api/v1/companies/:companyId/huddles/:huddleId/connections",
	"DELETE /api/v1/companies/:companyId/huddles/:huddleId/connections/:connectionId",
	"GET /api/v1/companies/:companyId/huddles/capabilities",
	"GET /api/v1/companies/:companyId/huddles/invitations",
	"GET /api/v1/companies/:companyId/huddles/:huddleId/ai",
	"PUT /api/v1/companies/:companyId/huddles/:huddleId/ai",
	"POST /api/v1/internal/huddle-ai/:attemptId/claim",
	"POST /api/v1/internal/huddle-ai/:attemptId/heartbeat",
	"GET /api/v1/internal/huddle-ai/:attemptId/chat",
	"POST /api/v1/internal/huddle-ai/:attemptId/usage",
	"POST /api/v1/internal/huddle-ai/:attemptId/tools",
	"POST /api/v1/internal/huddle-ai/:attemptId/proposals",
	"POST /api/v1/internal/huddle-ai/:attemptId/leave",
	"POST /api/v1/internal/huddle-ai/:attemptId/transcript",
	"POST /api/v1/companies/:companyId/huddles/:huddleId/ai/proposals/:proposalId/submit",
	"PUT /api/v1/companies/:companyId/huddles/:huddleId/ai/proposals/:proposalId",
	"GET /api/v1/companies/:companyId/huddle-budget",
	"PUT /api/v1/companies/:companyId/huddle-budget",
	"GET /api/v1/companies/:companyId/huddles/invitations/history",
	"POST /api/v1/companies/:companyId/huddles/:huddleId/invitations",
	"POST /api/v1/companies/:companyId/huddles/:huddleId/invitations/:invitationId/respond",
	"GET /api/v1/companies/:companyId/conversations/:conversationId/threads/:rootId/messages",
	"GET /api/v1/companies/:companyId/conversations/:conversationId/discussion-messages/:messageId/context",
	"PATCH /api/v1/companies/:companyId/conversations/:conversationId/discussion-messages/:messageId",
	"DELETE /api/v1/companies/:companyId/conversations/:conversationId/discussion-messages/:messageId",
	"GET /api/v1/companies/:companyId/conversations/:conversationId/discussion-pins",
	"PUT /api/v1/companies/:companyId/conversations/:conversationId/discussion-pins/:messageId",
	"DELETE /api/v1/companies/:companyId/conversations/:conversationId/discussion-pins/:messageId",
	"GET /api/v1/companies/:companyId/conversations/:conversationId/discussion-messages/:messageId/reactions",
	"PUT /api/v1/companies/:companyId/conversations/:conversationId/discussion-messages/:messageId/reactions/:emoji",
	"DELETE /api/v1/companies/:companyId/conversations/:conversationId/discussion-messages/:messageId/reactions/:emoji",
	"PUT /api/v1/companies/:companyId/conversations/:conversationId/threads/:rootId/state",

	"POST /api/v1/companies/:companyId/conversations/:conversationId/discussion-messages",
	"POST /api/v1/companies/:companyId/conversation-states/query",
	"POST /api/v1/companies/:companyId/member-presence",
	"PATCH /api/v1/companies/:companyId/conversations/:conversationId/discussion-state",
	"PUT /api/v1/companies/:companyId/conversations/:conversationId/discussion-read-state",
	"GET /api/v1/companies/:companyId/share-link",
	"POST /api/v1/companies/:companyId/share-link",
	"POST /api/v1/companies/:companyId/share-link/reset",
	"DELETE /api/v1/companies/:companyId/share-link",
	"GET /api/v1/companies/:companyId/invitations",
	"POST /api/v1/companies/:companyId/invitations",
	"DELETE /api/v1/companies/:companyId/invitations/:invitationId",
	"POST /api/v1/companies/:companyId/invitations/:invitationId/resend",
	"GET /api/v1/invitations/:token",
	"POST /api/v1/invitations/:token/accept",
	"POST /api/v1/invitations/:token/request-renewal",

	"GET /api/v1/companies/:companyId/projects",
	"POST /api/v1/companies/:companyId/projects",
	"GET /api/v1/companies/:companyId/projects/access-report",
	"GET /api/v1/companies/:companyId/projects/:projectId",
	"PATCH /api/v1/companies/:companyId/projects/:projectId",
	"POST /api/v1/companies/:companyId/projects/:projectId/archive",
	"PUT /api/v1/companies/:companyId/projects/:projectId/files",
	"GET /api/v1/companies/:companyId/projects/:projectId/grants",
	"PUT /api/v1/companies/:companyId/projects/:projectId/grants/:userId",
	"DELETE /api/v1/companies/:companyId/projects/:projectId/grants/:userId",
	"POST /api/v1/companies/:companyId/projects/:projectId/grants/:userId/preview",
	"POST /api/v1/companies/:companyId/projects/:projectId/join",
	"PUT /api/v1/companies/:companyId/projects/:projectId/group-grants/:groupId",
	"DELETE /api/v1/companies/:companyId/projects/:projectId/group-grants/:groupId",
	"POST /api/v1/companies/:companyId/projects/:projectId/group-grants/:groupId/preview",
	"GET /api/v1/companies/:companyId/groups",
	"POST /api/v1/companies/:companyId/groups",
	"GET /api/v1/companies/:companyId/groups/:groupId",
	"PATCH /api/v1/companies/:companyId/groups/:groupId",
	"DELETE /api/v1/companies/:companyId/groups/:groupId",
	"POST /api/v1/companies/:companyId/groups/:groupId/members",
	"POST /api/v1/companies/:companyId/groups/:groupId/preview",
	"GET /api/v1/companies/:companyId/wiki/collections",
	"POST /api/v1/companies/:companyId/wiki/collections",
	"PATCH /api/v1/companies/:companyId/wiki/collections/:collectionId",
	"DELETE /api/v1/companies/:companyId/wiki/collections/:collectionId",
	"GET /api/v1/companies/:companyId/wiki/pages",
	"POST /api/v1/companies/:companyId/wiki/pages",
	"GET /api/v1/companies/:companyId/wiki/pages/:pageId",
	"GET /api/v1/companies/:companyId/wiki/pages/:pageId/links",
	"GET /api/v1/companies/:companyId/wiki/pages/:pageId/document",
	"POST /api/v1/companies/:companyId/wiki/pages/:pageId/document",
	"POST /api/v1/companies/:companyId/wiki/pages/:pageId/document/ids",
	"PATCH /api/v1/companies/:companyId/wiki/pages/:pageId/document/metadata",
	"POST /api/v1/companies/:companyId/wiki/pages/:pageId/publish",
	"POST /api/v1/companies/:companyId/wiki/pages/:pageId/archive",
	"POST /api/v1/companies/:companyId/wiki/pages/:pageId/restore",
	"GET /api/v1/companies/:companyId/wiki/pages/:pageId/revisions",
	"POST /api/v1/companies/:companyId/wiki/pages/:pageId/restore-revision",
	"PUT /api/v1/companies/:companyId/wiki/pages/:pageId/favorite",
	"GET /api/v1/companies/:companyId/wiki/graph",
	"GET /api/v1/companies/:companyId/wiki/pages/:pageId/agent-reads",
	"GET /api/v1/companies/:companyId/runs/:runId/wiki-reads",
	"GET /api/v1/companies/:companyId/runs/:runId/agent-wiki/search",
	"GET /api/v1/companies/:companyId/runs/:runId/agent-wiki/pages/:pageId",
	"POST /api/v1/companies/:companyId/runs/:runId/agent-wiki/pages",

	"DELETE /api/v1/companies/:companyId/resources/:resourceId",
	"DELETE /api/v1/companies/:companyId",
	"DELETE /api/v1/companies/:companyId/runtimes/:runtimeId",
	"DELETE /api/v1/me",
	"GET /api/v1/attachments/:attachmentId/download",
	"GET /api/v1/auth/google/web/callback",
	"GET /api/v1/auth/providers",
	"GET /api/v1/companies",
	"GET /api/v1/companies/:companyId",
	"GET /api/v1/companies/:companyId/bootstrap",
	"GET /api/v1/companies/:companyId/activity",
	"GET /api/v1/companies/:companyId/activity/calendar",
	"GET /api/v1/companies/:companyId/approvals",
	"GET /api/v1/companies/:companyId/approvals/:approvalId",
	"GET /api/v1/companies/:companyId/approvals/stats",
	"GET /api/v1/companies/:companyId/conversations",
	"GET /api/v1/companies/:companyId/conversations/pins",
	"GET /api/v1/companies/:companyId/conversations/states",
	"GET /api/v1/companies/:companyId/conversations/:conversationId",
	"PATCH /api/v1/companies/:companyId/conversations/:conversationId",
	"POST /api/v1/companies/:companyId/conversations/:conversationId/studio/operations",
	"GET /api/v1/companies/:companyId/runs",
	"GET /api/v1/companies/:companyId/runs/:runId",
	"GET /api/v1/companies/:companyId/conversations/:conversationId/transcript",
	"GET /api/v1/companies/:companyId/runtimes",
	"GET /api/v1/companies/:companyId/runtimes/:runtimeId",
	"GET /api/v1/companies/:companyId/runtimes/:runtimeId/models/:requestId",
	"POST /api/v1/companies/:companyId/runs/:runId/cancel",
	"GET /api/v1/companies/:companyId/inbox",
	"GET /api/v1/companies/:companyId/inbox/:itemId",
	"GET /api/v1/companies/:companyId/inbox/counts",
	"GET /api/v1/companies/:companyId/me/activity-insight",
	"GET /api/v1/companies/:companyId/analytics/summary",
	"GET /api/v1/companies/:companyId/analytics/series",
	"GET /api/v1/companies/:companyId/analytics/breakdown",
	"GET /api/v1/companies/:companyId/analytics/failures",
	"GET /api/v1/companies/:companyId/analytics/heatmap",
	"GET /api/v1/companies/:companyId/analytics/budget",
	"PUT /api/v1/companies/:companyId/analytics/budget",
	"GET /api/v1/companies/:companyId/tasks/:taskId/usage",
	"GET /api/v1/companies/:companyId/members",
	"PATCH /api/v1/companies/:companyId/members/:userId",
	"DELETE /api/v1/companies/:companyId/members/:userId",
	"POST /api/v1/companies/:companyId/members/:userId/transfer-ownership",
	"POST /api/v1/companies/:companyId/leave",
	"POST /api/v1/companies/:companyId/milestone-attestations",
	"POST /api/v1/companies/:companyId/reference-documents",
	"GET /api/v1/companies/:companyId/resources",
	"GET /api/v1/companies/:companyId/resources/:resourceId",
	"GET /api/v1/companies/:companyId/questions",
	"GET /api/v1/companies/:companyId/questions/:questionId",
	"GET /api/v1/companies/:companyId/statistics",
	"GET /api/v1/companies/:companyId/tasks",
	"GET /api/v1/companies/:companyId/tasks/:taskId",
	"GET /api/v1/companies/:companyId/tasks/:taskId/dependencies",
	"GET /api/v1/companies/:companyId/tasks/:taskId/results",
	"POST /api/v1/companies/:companyId/tasks/:taskId/results",
	"GET /api/v1/companies/:companyId/conversations/:conversationId/artifacts",
	"GET /api/v1/companies/:companyId/artifacts",
	"GET /api/v1/companies/:companyId/artifacts/resolve",
	"GET /api/v1/companies/:companyId/artifacts/:artifactId",
	"GET /api/v1/companies/:companyId/artifacts/:artifactId/review",
	"POST /api/v1/companies/:companyId/artifacts/:artifactId/review/operations",
	"POST /api/v1/companies/:companyId/artifacts/:artifactId/review/items/:outputItemId/document/checkpoints",
	"GET /api/v1/companies/:companyId/artifacts/:artifactId/review/versions/:versionId/body",
	"POST /api/v1/companies/:companyId/artifacts/:artifactId/review/items/:outputItemId/document/open",
	"PUT /api/v1/companies/:companyId/artifacts/:artifactId/review/items/:outputItemId/document/presence",
	"GET /api/v1/companies/:companyId/artifacts/:artifactId/review/items/:outputItemId/document",
	"POST /api/v1/companies/:companyId/artifacts/:artifactId/review/items/:outputItemId/document",
	"POST /api/v1/companies/:companyId/artifacts/:artifactId/review/items/:outputItemId/document/ids",
	"GET /api/v1/companies/:companyId/runs/:runId/agent-artifacts/:artifactId",
	"GET /api/v1/companies/:companyId/runs/:runId/agent-artifacts/:artifactId/items/:ordinal/download",
	"PUT /api/v1/companies/:companyId/artifacts/:artifactId/visibility",
	"DELETE /api/v1/companies/:companyId/artifacts/:artifactId/conversations/:conversationId",
	"GET /api/v1/public/artifacts/:token",
	"GET /api/v1/public/artifacts/:token/items/:ordinal/download",

	"GET /api/v1/companies/:companyId/artifacts/unowned",
	"GET /api/v1/companies/:companyId/artifacts/:artifactId/items/:ordinal/download",
	"GET /api/v1/companies/:companyId/tasks/:taskId/workspace",
	"GET /api/v1/companies/:companyId/task-dependencies",
	"GET /api/v1/companies/:companyId/agents",
	"GET /api/v1/companies/:companyId/agent-bindings",
	"PUT /api/v1/companies/:companyId/agents/:agentId/binding",
	"DELETE /api/v1/companies/:companyId/agents/:agentId/binding",
	"GET /api/v1/companies/:companyId/platform-skills",
	"GET /api/v1/companies/:companyId/workflows",
	"GET /api/v1/companies/:companyId/workflows/:workflowKey",
	"GET /api/v1/companies/:companyId/workflows/:workflowKey/readiness",
	"GET /api/v1/companies/:companyId/agents/:agentId",
	"GET /api/v1/companies/:companyId/agents/:agentId/usage",
	"GET /api/v1/companies/:companyId/usage",
	"GET /api/v1/daemon/companies",
	"GET /api/v1/daemon/config",
	"GET /api/v1/daemon/runs/:runId/status",
	"GET /api/v1/daemon/ws",
	"GET /api/v1/conversations/:conversationId/messages",
	"GET /api/v1/conversations/:conversationId/messages/:messageId/context",
	"GET /api/v1/conversations/:conversationId/run-queue",
	"GET /api/v1/conversations/:conversationId/state",
	"GET /api/v1/conversations/:conversationId/model-settings",
	"PUT /api/v1/conversations/:conversationId/model-settings",
	"GET /api/v1/me",
	"GET /api/v1/tasks/:taskId/messages",
	"GET /api/v1/ws",
	"GET /health/realtime",
	"GET /healthz",
	"GET /metrics",
	"GET /readyz",
	"PATCH /api/v1/companies/:companyId/profile",
	"PATCH /api/v1/companies/:companyId/resources/:resourceId",
	"PATCH /api/v1/companies/:companyId/runtimes/:runtimeId",
	"PATCH /api/v1/companies/:companyId/tasks/:taskId",
	"PATCH /api/v1/companies/:companyId/agents/:agentId",
	"PATCH /api/v1/conversations/:conversationId/state",
	"PUT /api/v1/conversations/:conversationId/run-queue/order",
	"DELETE /api/v1/conversations/:conversationId/run-queue/:queueItemId",
	"PATCH /api/v1/me/profile",
	"POST /api/v1/attachments/:attachmentId/complete",
	"POST /api/v1/attachments/initiate",
	"POST /api/v1/auth/desktop/handoff/complete",
	"POST /api/v1/auth/desktop/handoff/start",
	"POST /api/v1/auth/email-verification/resend",
	"POST /api/v1/auth/email-verification/status",
	"POST /api/v1/auth/email-verification/verify",
	"POST /api/v1/auth/google/desktop/complete",
	"POST /api/v1/auth/google/desktop/start",
	"POST /api/v1/auth/google/web/start",
	"POST /api/v1/auth/login",
	"POST /api/v1/auth/logout",
	"POST /api/v1/auth/daemon-session",
	"POST /api/v1/auth/password-reset/confirm",
	"POST /api/v1/auth/password-reset/request",
	"POST /api/v1/auth/refresh",
	"POST /api/v1/auth/signup",
	"POST /api/v1/companies",
	"POST /api/v1/companies/:companyId/company-plan/apply",
	"POST /api/v1/companies/:companyId/approvals/:approvalId/approve",
	"POST /api/v1/companies/:companyId/approvals/:approvalId/reject",
	"POST /api/v1/companies/:companyId/approvals/:approvalId/cancel",
	"POST /api/v1/companies/:companyId/conversations",
	"POST /api/v1/companies/:companyId/inbox/:itemId/:mutation",
	"POST /api/v1/companies/:companyId/inbox/read",
	"POST /api/v1/companies/:companyId/inbox/read-all",
	"POST /api/v1/companies/:companyId/resources",
	"POST /api/v1/companies/:companyId/questions",
	"POST /api/v1/companies/:companyId/questions/:questionId/answer",
	"POST /api/v1/companies/:companyId/questions/:questionId/withdraw",
	"POST /api/v1/companies/:companyId/runtimes/:runtimeId/models",
	"POST /api/v1/companies/:companyId/runtimes/:runtimeId/refresh",
	"POST /api/v1/companies/:companyId/runtimes/:runtimeId/remove",
	"POST /api/v1/companies/:companyId/search",
	"POST /api/v1/companies/:companyId/tasks",
	"POST /api/v1/companies/:companyId/task-failures",
	"POST /api/v1/companies/:companyId/tasks/:taskId/runs",
	"POST /api/v1/companies/:companyId/agents",
	"POST /api/v1/companies/:companyId/agents/:agentId/archive",
	"POST /api/v1/companies/:companyId/agents/:agentId/restore",
	"POST /api/v1/conversations/:conversationId/messages",
	"POST /api/v1/conversations/:conversationId/whiteboards",
	"POST /api/v1/crash-reports",
	"POST /api/v1/daemon/deregister",
	"POST /api/v1/daemon/heartbeat",
	"POST /api/v1/daemon/register",
	"POST /api/v1/daemon/runs/:runId/cancel-ack",
	"POST /api/v1/daemon/runs/:runId/complete",
	"POST /api/v1/daemon/runs/:runId/fail",
	"POST /api/v1/daemon/runs/:runId/session",
	"POST /api/v1/daemon/runs/:runId/start",
	"POST /api/v1/daemon/runs/:runId/usage",
	"POST /api/v1/daemon/runs/:runId/wait-local-directory",
	"POST /api/v1/daemon/runs/:runId/wait-studio-auth",
	"POST /api/v1/daemon/runs/claim",
	"POST /api/v1/daemon/runtimes/:runtimeId/tools",
	"GET /api/v1/daemon/runtimes/:runtimeId/agent-bindings",
	"POST /api/v1/daemon/runtimes/:runtimeId/runs/:runId/studio-project",
	"POST /api/v1/daemon/runtimes/:runtimeId/recover-orphans",
	"POST /api/v1/daemon/runtimes/:runtimeId/task-merges/claim",
	"POST /api/v1/daemon/runtimes/:runtimeId/task-merges/manual",
	"POST /api/v1/daemon/runtimes/:runtimeId/task-merges/:jobId/lease",
	"POST /api/v1/daemon/runtimes/:runtimeId/task-merges/:jobId/result",
	"POST /api/v1/daemon/runtimes/:runtimeId/model-discoveries/:requestId/result",
	"POST /api/v1/daemon/runtimes/:runtimeId/runs/:runId/prepare-lease",
	"GET /api/v1/daemon/runtimes/:runtimeId/runs/:runId/attachments/:attachmentId",
	"POST /api/v1/daemon/runtimes/:runtimeId/runs/:runId/skill-bundles/resolve",
	"POST /api/v1/tasks/:taskId/messages",
	"POST /api/v1/runs/:runId/output",
	"DELETE /api/v1/companies/:companyId/conversations/:conversationId/mail-drafts/:draftId",
	"GET /api/v1/companies/:companyId/connectors",
	"GET /api/v1/companies/:companyId/conversations/:conversationId/mail-draft-groups",
	"GET /api/v1/companies/:companyId/conversations/:conversationId/task-reports",
	"GET /api/v1/companies/:companyId/mailbox",
	"GET /api/v1/companies/:companyId/mailbox/messages",
	"GET /api/v1/companies/:companyId/mailbox/messages/:messageId",
	"GET /api/v1/companies/:companyId/mailbox/messages/:messageId/attachments/:attachmentId",
	"GET /api/v1/companies/:companyId/plugins",
	"GET /api/v1/companies/:companyId/plugins/:pluginId",
	"GET /api/v1/companies/:companyId/proactive-mode",
	"GET /api/v1/companies/:companyId/initialization",
	"POST /api/v1/companies/:companyId/initialization",
	"GET /api/v1/companies/:companyId/schedules",
	"GET /api/v1/companies/:companyId/schedules/:scheduleId",
	"GET /api/v1/companies/:companyId/schedules/:scheduleId/runs",
	"GET /api/v1/companies/:companyId/runs/:runId/result",
	"POST /api/v1/companies/:companyId/runs/:runId/result/studio-canvas",
	"GET /api/v1/companies/:companyId/runs/:runId/artifacts/:artifactId/items/:ordinal/download",
	"POST /api/v1/companies/:companyId/runs/:runId/artifacts/:artifactId/items/:ordinal/share",
	"POST /api/v1/companies/:companyId/runs/:runId/result/publication",
	"POST /api/v1/companies/:companyId/runs/:runId/result/publication/unpublish",
	"PUT /api/v1/companies/:companyId/runs/:runId/result/publication/subdomain",
	"PATCH /api/v1/companies/:companyId/schedules/:scheduleId",
	"PATCH /api/v1/companies/:companyId/proactive-mode",
	"POST /api/v1/companies/:companyId/connectors/:connectorId/connect",
	"POST /api/v1/companies/:companyId/connectors/refresh",
	"POST /api/v1/companies/:companyId/connectors/resolve",
	"POST /api/v1/companies/:companyId/artifacts/:artifactId/claim",
	"PUT /api/v1/companies/:companyId/artifacts/:artifactId/grants/:userId",
	"DELETE /api/v1/companies/:companyId/artifacts/:artifactId/grants/:userId",
	"POST /api/v1/companies/:companyId/conversations/:conversationId/mail-draft-groups",
	"POST /api/v1/companies/:companyId/conversations/:conversationId/mail-draft-groups/:groupId/send",
	"POST /api/v1/companies/:companyId/conversations/:conversationId/mail-drafts/:draftId/send",
	"POST /api/v1/companies/:companyId/conversations/:conversationId/task-reports/send",
	"POST /api/v1/companies/:companyId/mailbox",
	"POST /api/v1/companies/:companyId/mailbox/messages/:messageId/read",
	"POST /api/v1/companies/:companyId/plugins/:pluginId/install",
	"POST /api/v1/companies/:companyId/plugins/:pluginId/uninstall",
	"POST /api/v1/companies/:companyId/plugins/refresh",
	"POST /api/v1/companies/:companyId/schedules",
	"POST /api/v1/companies/:companyId/schedules/:scheduleId/cancel",
	"POST /api/v1/companies/:companyId/schedules/:scheduleId/pause",
	"POST /api/v1/companies/:companyId/schedules/:scheduleId/resume",
	"POST /api/v1/companies/:companyId/schedules/:scheduleId/runs",
	"POST /api/v1/companies/:companyId/schedules/preview",
	"POST /api/v1/webhooks/resend/mailbox",
	"PUT /api/v1/companies/:companyId/conversations/:conversationId/mail-drafts/:draftId",
	"PUT /api/v1/daemon/companies/:companyId/runtimes/:runtimeId/connectors/discovery",
	"POST /api/v1/daemon/companies/:companyId/runtimes/:runtimeId/connectors/discovery/reaffirm",
	"PUT /api/v1/daemon/companies/:companyId/runtimes/:runtimeId/plugins/discovery",
	"GET /api/v1/companies/:companyId/commerce/account-status",
	"GET /api/v1/companies/:companyId/commerce/manifests",
	"GET /api/v1/companies/:companyId/commerce/manifests/:manifestId",
	"GET /api/v1/companies/:companyId/commerce/summary",
	"GET /api/v1/site-commerce/jwks",
	"POST /api/v1/companies/:companyId/commerce/accounts/:environment/account-session",
	"POST /api/v1/companies/:companyId/commerce/accounts/:environment/dashboard-link",
	"POST /api/v1/companies/:companyId/commerce/accounts/:environment/onboarding-link",
	"POST /api/v1/companies/:companyId/commerce/manifests",
	"POST /api/v1/companies/:companyId/commerce/manifests/:manifestId/approve-publish",
	"POST /api/v1/companies/:companyId/commerce/sites/:siteId/signing-keys/:keyId/revoke",
	"POST /api/v1/companies/:companyId/commerce/sites/:siteId/signing-keys/sync",
	"POST /api/v1/companies/:companyId/commerce/terms-acceptances",
	"POST /api/v1/daemon/companies/:companyId/commerce/sites/:siteAlias/local-signing-keys",
	"POST /api/v1/site-commerce/challenges",
	"POST /api/v1/site-commerce/checkout-sessions",
	"POST /api/v1/site-commerce/entitlements/verify",
	"POST /api/v1/site-commerce/portal-sessions",
	"POST /api/v1/webhooks/stripe/commerce",
}

func (routeTableRuntime) ReportTools(context.Context, auth.UserPrincipal, agent.ToolObservationReport) error {
	return nil
}
