package handler

// Cross-company authorization invariants for the HTTP surface.
//
// Cross-company IDOR attempts must fail, and they must fail with
// non-disclosing errors: a caller who is not a member of a company learns
// only 404, never the difference between "does not exist" and "exists but
// is not yours". Mutations always travel over authenticated HTTP so that
// transactions, replay rules, body limits, and audit provenance have one
// authoritative path.
//
// This file walks every mounted /api/v1 route whose path binds :companyId and
// proves three properties for each one, so a newly added company-scoped route
// cannot ship without joining the matrix:
//
//  1. Cross-company: an authenticated member of Company A receives a
//     non-disclosing 404 for Company B,
//     and no domain service ever observes a Company B principal.
//  2. Authentication: an anonymous request receives 401 before any service.
//  3. Body limits: every mutation body is bounded and an oversized body is
//     rejected with 413 request_too_large before any domain side effect.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/agent"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/company"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/mailbox"
	"boardly.dev/boardly/server/internal/schedule"
	"boardly.dev/boardly/server/internal/task"
	"boardly.dev/boardly/server/internal/usage"
)

var (
	crossCompanyUser     = uuid.MustParse("0198aa00-0000-7000-8000-00000000000a")
	crossCompanyA        = uuid.MustParse("0198aa00-0000-7000-8000-00000000000b")
	crossCompanyB        = uuid.MustParse("0198aa00-0000-7000-8000-00000000000c")
	crossCompanyResource = uuid.MustParse("0198aa00-0000-7000-8000-00000000000d")
)

type crossCompanyRouteSpec struct {
	// body is the request body used for the cross-company probe. Routes whose
	// company authorization lives in the service (not route middleware) need a
	// body that survives handler validation so the probe reaches the
	// authorization seam instead of failing as invalid input.
	body string
	// wantStatus/wantCode assert the non-disclosing cross-company denial.
	wantStatus int
	wantCode   string
	// wantAnonymousCode asserts the credential-required denial for the lane.
	wantAnonymousCode string
}

// crossCompanyMatrix must contain exactly the mounted company-scoped routes.
// The walk fails when a mounted :companyId route is missing here and when an
// entry no longer matches a mounted route.
func crossCompanyMatrix() map[string]crossCompanyRouteSpec {
	memberDenied := crossCompanyRouteSpec{}
	resourceDenied := func(body string) crossCompanyRouteSpec {
		return crossCompanyRouteSpec{body: body, wantCode: "company_resource_not_found"}
	}
	return map[string]crossCompanyRouteSpec{
		"GET /api/v1/companies/:companyId/share-link":                        memberDenied,
		"POST /api/v1/companies/:companyId/share-link":                       memberDenied,
		"POST /api/v1/companies/:companyId/share-link/reset":                 memberDenied,
		"DELETE /api/v1/companies/:companyId/share-link":                     memberDenied,
		"GET /api/v1/companies/:companyId/invitations":                       memberDenied,
		"POST /api/v1/companies/:companyId/invitations":                      memberDenied,
		"POST /api/v1/companies/:companyId/invitations/:invitationId/resend": memberDenied,
		"DELETE /api/v1/companies/:companyId/invitations/:invitationId":      memberDenied,
		"GET /api/v1/companies/:companyId":                                   memberDenied,
		"GET /api/v1/companies/:companyId/bootstrap":                         memberDenied,
		"DELETE /api/v1/companies/:companyId/members/:userId":                memberDenied,
		"PATCH /api/v1/companies/:companyId/members/:userId": {
			body: `{"expectedRevision":1,"role":"admin"}`,
		},
		"POST /api/v1/companies/:companyId/members/:userId/transfer-ownership": memberDenied,
		"POST /api/v1/companies/:companyId/leave":                              memberDenied,
		"GET /api/v1/companies/:companyId/members":                             memberDenied,
		"GET /api/v1/companies/:companyId/statistics":                          memberDenied,
		"PATCH /api/v1/companies/:companyId/profile": {
			body: `{"expectedRevision":1,"name":"Acme","contactEmail":null,"phoneCountryIso2":null,"phoneNumber":null}`,
		},
		"POST /api/v1/companies/:companyId/reference-documents": {
			body: `{"attachmentIds":["0198aa00-0000-7000-8000-0000000000aa"]}`,
		},
		"POST /api/v1/companies/:companyId/milestone-attestations": {
			body: `{"milestones":["launch"],"note":"Shipped in 2021."}`,
		},
		"GET /api/v1/companies/:companyId/activity":                       memberDenied,
		"GET /api/v1/companies/:companyId/activity/calendar":              memberDenied,
		"GET /api/v1/companies/:companyId/commerce/account-status":        memberDenied,
		"GET /api/v1/companies/:companyId/commerce/summary":               memberDenied,
		"GET /api/v1/companies/:companyId/commerce/manifests":             memberDenied,
		"GET /api/v1/companies/:companyId/commerce/manifests/:manifestId": memberDenied,
		"POST /api/v1/companies/:companyId/commerce/manifests": {
			body: `{"schemaVersion":1,"environment":"live","claimId":"01900000-0000-7000-8000-000000000010","manifest":{"schemaVersion":1}}`,
		},
		"POST /api/v1/companies/:companyId/commerce/manifests/:manifestId/approve-publish": {
			body: `{"schemaVersion":1,"digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`,
		},
		"POST /api/v1/companies/:companyId/commerce/sites/:siteId/signing-keys/sync": {
			body: `{"schemaVersion":1}`,
		},
		"POST /api/v1/companies/:companyId/commerce/sites/:siteId/signing-keys/:keyId/revoke": {
			body: `{"schemaVersion":1}`,
		},
		"POST /api/v1/companies/:companyId/commerce/terms-acceptances": {
			body: `{"schemaVersion":1,"termsVersion":"2026-08-20","accepted":true}`,
		},
		"POST /api/v1/companies/:companyId/commerce/accounts/:environment/account-session": {
			body: `{"schemaVersion":1}`,
		},
		"POST /api/v1/companies/:companyId/commerce/accounts/:environment/onboarding-link": {
			body: `{"schemaVersion":1}`,
		},
		"POST /api/v1/companies/:companyId/commerce/accounts/:environment/dashboard-link": {
			body: `{"schemaVersion":1}`,
		},
		"GET /api/v1/companies/:companyId/agents":                           memberDenied,
		"GET /api/v1/companies/:companyId/platform-skills":                  memberDenied,
		"GET /api/v1/companies/:companyId/workflows":                        memberDenied,
		"GET /api/v1/companies/:companyId/workflows/:workflowKey":           memberDenied,
		"GET /api/v1/companies/:companyId/workflows/:workflowKey/readiness": memberDenied,
		"GET /api/v1/companies/:companyId/usage":                            memberDenied,
		"DELETE /api/v1/companies/:companyId": {
			body: `{"expectedRevision":1}`,
		},
		"GET /api/v1/companies/:companyId/me/activity-insight":      memberDenied,
		"POST /api/v1/companies/:companyId/agents":                  memberDenied,
		"GET /api/v1/companies/:companyId/agents/:agentId":          memberDenied,
		"GET /api/v1/companies/:companyId/agents/:agentId/usage":    memberDenied,
		"PATCH /api/v1/companies/:companyId/agents/:agentId":        memberDenied,
		"POST /api/v1/companies/:companyId/agents/:agentId/archive": memberDenied,
		"POST /api/v1/companies/:companyId/agents/:agentId/restore": memberDenied,
		"GET /api/v1/companies/:companyId/agent-bindings":           memberDenied,
		"PUT /api/v1/companies/:companyId/agents/:agentId/binding": {
			body: `{"runtimeId":"0198aa00-0000-7000-8000-00000000000b"}`,
		},
		"DELETE /api/v1/companies/:companyId/agents/:agentId/binding": memberDenied,
		"GET /api/v1/companies/:companyId/runtimes":                   memberDenied,
		"GET /api/v1/companies/:companyId/runtimes/:runtimeId":        memberDenied,
		"PATCH /api/v1/companies/:companyId/runtimes/:runtimeId": {
			body: `{"customName":"Office Mac","applyToMachine":true}`,
		},
		"POST /api/v1/companies/:companyId/runtimes/:runtimeId/models":           memberDenied,
		"POST /api/v1/companies/:companyId/runtimes/:runtimeId/refresh":          memberDenied,
		"GET /api/v1/companies/:companyId/runtimes/:runtimeId/models/:requestId": memberDenied,
		"DELETE /api/v1/companies/:companyId/runtimes/:runtimeId":                memberDenied,
		"POST /api/v1/companies/:companyId/runtimes/:runtimeId/remove":           memberDenied,

		"GET /api/v1/companies/:companyId/resources": resourceDenied(""),
		"POST /api/v1/companies/:companyId/resources": resourceDenied(
			`{"daemonId":"daemon-a","localPath":"/tmp/acme","label":"Acme"}`,
		),
		"GET /api/v1/companies/:companyId/resources/:resourceId": resourceDenied(""),
		"PATCH /api/v1/companies/:companyId/resources/:resourceId": resourceDenied(
			`{"expectedRevision":1,"label":"repo"}`,
		),
		"DELETE /api/v1/companies/:companyId/resources/:resourceId": resourceDenied(`{"expectedRevision":1}`),

		"GET /api/v1/companies/:companyId/projects":                                    memberDenied,
		"POST /api/v1/companies/:companyId/projects":                                   memberDenied,
		"GET /api/v1/companies/:companyId/projects/access-report":                      memberDenied,
		"GET /api/v1/companies/:companyId/projects/:projectId":                         memberDenied,
		"PATCH /api/v1/companies/:companyId/projects/:projectId":                       memberDenied,
		"POST /api/v1/companies/:companyId/projects/:projectId/archive":                memberDenied,
		"PUT /api/v1/companies/:companyId/projects/:projectId/files":                   memberDenied,
		"GET /api/v1/companies/:companyId/projects/:projectId/grants":                  memberDenied,
		"PUT /api/v1/companies/:companyId/projects/:projectId/grants/:userId":          memberDenied,
		"DELETE /api/v1/companies/:companyId/projects/:projectId/grants/:userId":       memberDenied,
		"POST /api/v1/companies/:companyId/projects/:projectId/grants/:userId/preview": memberDenied,
		"POST /api/v1/companies/:companyId/projects/:projectId/join":                   memberDenied,

		"PUT /api/v1/companies/:companyId/projects/:projectId/group-grants/:groupId":          memberDenied,
		"DELETE /api/v1/companies/:companyId/projects/:projectId/group-grants/:groupId":       memberDenied,
		"POST /api/v1/companies/:companyId/projects/:projectId/group-grants/:groupId/preview": memberDenied,

		"GET /api/v1/companies/:companyId/groups":                   memberDenied,
		"POST /api/v1/companies/:companyId/groups":                  memberDenied,
		"GET /api/v1/companies/:companyId/groups/:groupId":          memberDenied,
		"PATCH /api/v1/companies/:companyId/groups/:groupId":        memberDenied,
		"DELETE /api/v1/companies/:companyId/groups/:groupId":       memberDenied,
		"POST /api/v1/companies/:companyId/groups/:groupId/members": memberDenied,
		"POST /api/v1/companies/:companyId/groups/:groupId/preview": memberDenied,

		"GET /api/v1/companies/:companyId/tasks":                      memberDenied,
		"POST /api/v1/companies/:companyId/tasks":                     memberDenied,
		"GET /api/v1/companies/:companyId/task-dependencies":          memberDenied,
		"GET /api/v1/companies/:companyId/tasks/:taskId":              memberDenied,
		"PATCH /api/v1/companies/:companyId/tasks/:taskId":            memberDenied,
		"GET /api/v1/companies/:companyId/tasks/:taskId/dependencies": memberDenied,
		"GET /api/v1/companies/:companyId/tasks/:taskId/workspace":    memberDenied,
		"POST /api/v1/companies/:companyId/tasks/:taskId/runs": {
			body: `{"expectedRevision":1}`,
		},
		"POST /api/v1/companies/:companyId/task-failures": {
			body: `{"reason":"Task cannot proceed"}`,
		},

		"GET /api/v1/companies/:companyId/connectors":                                            memberDenied,
		"POST /api/v1/companies/:companyId/connectors/refresh":                                   memberDenied,
		"POST /api/v1/companies/:companyId/connectors/resolve":                                   memberDenied,
		"POST /api/v1/companies/:companyId/connectors/:connectorId/connect":                      memberDenied,
		"GET /api/v1/companies/:companyId/plugins":                                               memberDenied,
		"GET /api/v1/companies/:companyId/plugins/:pluginId":                                     memberDenied,
		"POST /api/v1/companies/:companyId/plugins/refresh":                                      memberDenied,
		"POST /api/v1/companies/:companyId/plugins/:pluginId/install":                            memberDenied,
		"POST /api/v1/companies/:companyId/plugins/:pluginId/uninstall":                          memberDenied,
		"GET /api/v1/companies/:companyId/mailbox":                                               memberDenied,
		"POST /api/v1/companies/:companyId/mailbox":                                              memberDenied,
		"GET /api/v1/companies/:companyId/mailbox/messages":                                      memberDenied,
		"GET /api/v1/companies/:companyId/mailbox/messages/:messageId":                           memberDenied,
		"POST /api/v1/companies/:companyId/mailbox/messages/:messageId/read":                     memberDenied,
		"GET /api/v1/companies/:companyId/mailbox/messages/:messageId/attachments/:attachmentId": memberDenied,

		"GET /api/v1/companies/:companyId/conversations/:conversationId/mail-draft-groups":                memberDenied,
		"POST /api/v1/companies/:companyId/conversations/:conversationId/mail-draft-groups":               memberDenied,
		"POST /api/v1/companies/:companyId/conversations/:conversationId/mail-draft-groups/:groupId/send": memberDenied,
		"PUT /api/v1/companies/:companyId/conversations/:conversationId/mail-drafts/:draftId":             memberDenied,
		"DELETE /api/v1/companies/:companyId/conversations/:conversationId/mail-drafts/:draftId":          memberDenied,
		"POST /api/v1/companies/:companyId/conversations/:conversationId/mail-drafts/:draftId/send":       memberDenied,
		"GET /api/v1/companies/:companyId/conversations/:conversationId/task-reports":                     memberDenied,
		"POST /api/v1/companies/:companyId/conversations/:conversationId/task-reports/send":               memberDenied,

		"POST /api/v1/companies/:companyId/schedules/preview":            memberDenied,
		"GET /api/v1/companies/:companyId/schedules":                     memberDenied,
		"POST /api/v1/companies/:companyId/schedules":                    memberDenied,
		"GET /api/v1/companies/:companyId/schedules/:scheduleId":         memberDenied,
		"PATCH /api/v1/companies/:companyId/schedules/:scheduleId":       memberDenied,
		"POST /api/v1/companies/:companyId/schedules/:scheduleId/pause":  memberDenied,
		"POST /api/v1/companies/:companyId/schedules/:scheduleId/resume": memberDenied,
		"POST /api/v1/companies/:companyId/schedules/:scheduleId/cancel": memberDenied,
		"GET /api/v1/companies/:companyId/schedules/:scheduleId/runs":    memberDenied,
		"POST /api/v1/companies/:companyId/schedules/:scheduleId/runs":   memberDenied,

		"GET /api/v1/companies/:companyId/conversations":                            memberDenied,
		"GET /api/v1/companies/:companyId/conversations/pins":                       memberDenied,
		"GET /api/v1/companies/:companyId/conversations/states":                     memberDenied,
		"POST /api/v1/companies/:companyId/conversations":                           memberDenied,
		"GET /api/v1/companies/:companyId/conversations/:conversationId":            memberDenied,
		"PATCH /api/v1/companies/:companyId/conversations/:conversationId":          memberDenied,
		"GET /api/v1/companies/:companyId/conversations/:conversationId/transcript": memberDenied,

		"GET /api/v1/companies/:companyId/wiki/collections":                       memberDenied,
		"POST /api/v1/companies/:companyId/wiki/collections":                      memberDenied,
		"PATCH /api/v1/companies/:companyId/wiki/collections/:collectionId":       memberDenied,
		"DELETE /api/v1/companies/:companyId/wiki/collections/:collectionId":      memberDenied,
		"GET /api/v1/companies/:companyId/wiki/pages":                             memberDenied,
		"POST /api/v1/companies/:companyId/wiki/pages":                            memberDenied,
		"GET /api/v1/companies/:companyId/wiki/pages/:pageId":                     memberDenied,
		"GET /api/v1/companies/:companyId/wiki/pages/:pageId/links":               memberDenied,
		"GET /api/v1/companies/:companyId/wiki/pages/:pageId/document":            memberDenied,
		"POST /api/v1/companies/:companyId/wiki/pages/:pageId/document":           memberDenied,
		"POST /api/v1/companies/:companyId/wiki/pages/:pageId/document/ids":       memberDenied,
		"PATCH /api/v1/companies/:companyId/wiki/pages/:pageId/document/metadata": memberDenied,
		"POST /api/v1/companies/:companyId/wiki/pages/:pageId/publish":            memberDenied,
		"POST /api/v1/companies/:companyId/wiki/pages/:pageId/archive":            memberDenied,
		"POST /api/v1/companies/:companyId/wiki/pages/:pageId/restore":            memberDenied,
		"GET /api/v1/companies/:companyId/wiki/pages/:pageId/revisions":           memberDenied,
		"POST /api/v1/companies/:companyId/wiki/pages/:pageId/restore-revision":   memberDenied,
		"PUT /api/v1/companies/:companyId/wiki/pages/:pageId/favorite":            memberDenied,
		"GET /api/v1/companies/:companyId/wiki/graph":                             memberDenied,
		"GET /api/v1/companies/:companyId/wiki/pages/:pageId/agent-reads":         memberDenied,
		"GET /api/v1/companies/:companyId/runs/:runId/wiki-reads":                 memberDenied,
		"GET /api/v1/companies/:companyId/runs/:runId/agent-wiki/search":          memberDenied,
		"GET /api/v1/companies/:companyId/runs/:runId/agent-wiki/pages/:pageId":   memberDenied,
	}
}

// crossCompanyServiceChecks records every companyId that reaches a
// service-level authorization seam, proving those routes were exercised at
// the seam instead of short-circuiting on a parse error.
type crossCompanyServiceChecks struct {
	mu        sync.Mutex
	companyID []uuid.UUID
}

func (c *crossCompanyServiceChecks) record(companyID uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.companyID = append(c.companyID, companyID)
}

func (c *crossCompanyServiceChecks) snapshot() []uuid.UUID {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]uuid.UUID(nil), c.companyID...)
}

func crossCompanyUserPrincipal(role auth.CompanyRole) auth.UserPrincipal {
	return auth.UserPrincipal{
		UserID:    crossCompanyUser,
		SessionID: uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19"),
		AuthKind:  auth.TokenKindBearer,
		CompanyID: crossCompanyA,
		Role:      role,
	}
}

// crossCompanyHandler wires every company-scoped route with stub services.
// Only Company A memberships exist; every service-level authorization seam
// answers exactly like the real service does for a foreign company.
func crossCompanyHandler(t *testing.T, checks *crossCompanyServiceChecks) *Handler {
	t.Helper()
	companyService := &companyServiceStub{
		authorize: func(_ context.Context, user auth.UserPrincipal, companyID uuid.UUID) (auth.UserPrincipal, error) {
			if user.UserID != crossCompanyUser || companyID != crossCompanyA {
				return auth.UserPrincipal{}, company.ErrNotFound
			}
			return crossCompanyUserPrincipal(auth.CompanyRoleMember), nil
		},
		requirePermission: func(_ context.Context, user auth.UserPrincipal, companyID uuid.UUID) (auth.UserPrincipal, error) {
			if user.UserID != crossCompanyUser || companyID != crossCompanyA {
				return auth.UserPrincipal{}, company.ErrNotFound
			}
			return crossCompanyUserPrincipal(auth.CompanyRoleOwner), nil
		},
		get: func(_ context.Context, _ auth.UserPrincipal, companyID uuid.UUID) (company.Response, error) {
			checks.record(companyID)
			return company.Response{}, company.ErrNotFound
		},
	}
	resourceService := &companyResourceServiceStub{
		list: func(
			_ context.Context,
			_ auth.UserPrincipal,
			companyID uuid.UUID,
			_ company.ResourceListOptions,
		) ([]company.ResourceResponse, error) {
			checks.record(companyID)
			return nil, company.ErrResourceNotFound
		},
		get: func(_ context.Context, _ auth.UserPrincipal, companyID, _ uuid.UUID) (company.ResourceResponse, error) {
			checks.record(companyID)
			return company.ResourceResponse{}, company.ErrResourceNotFound
		},
		update: func(_ context.Context, _ auth.UserPrincipal, companyID, _ uuid.UUID, _ company.ResourceUpdateInput) (company.ResourceResponse, error) {
			checks.record(companyID)
			return company.ResourceResponse{}, company.ErrResourceNotFound
		},
		delete: func(_ context.Context, _ auth.UserPrincipal, companyID, _ uuid.UUID, _ company.RevisionInput) error {
			checks.record(companyID)
			return company.ErrResourceNotFound
		},
		create: func(
			_ context.Context,
			_, companyID uuid.UUID,
			_ company.CreateLocalDirectoryResourceInput,
		) (company.ResourceCreateResult, error) {
			checks.record(companyID)
			return company.ResourceCreateResult{}, company.ErrResourceNotFound
		},
	}
	return New(Handler{
		InvitationService: &company.DefaultInvitationService{},
		AuthService:       authenticatedAuthStub(crossCompanyUser),
		ActivityFeed:      activityFeedStub{},
		CompanyService:    companyService,
		MilestoneService:  &milestoneServiceStub{},
		ReferenceService:  &referenceServiceStub{},
		AgentService:      &agentServiceStub{},
		AgentBindingService: &agentBindingServiceStub{
			bind: func(context.Context, auth.UserPrincipal, uuid.UUID, agent.BindInput) (agent.Response, error) {
				t.Fatal("AgentBindingService.Bind reached across the Company fence")
				return agent.Response{}, nil
			},
			unbind: func(context.Context, auth.UserPrincipal, uuid.UUID) error {
				t.Fatal("AgentBindingService.Unbind reached across the Company fence")
				return nil
			},
			list: func(context.Context, auth.UserPrincipal, agent.BindingListOptions) ([]agent.Binding, error) {
				t.Fatal("AgentBindingService.ListForViewer reached across the Company fence")
				return nil, nil
			},
		},
		RuntimeService: runtimeServiceStub{
			list: func(context.Context, auth.UserPrincipal, agent.RuntimeListOptions) (agent.RuntimePage, error) {
				t.Fatal("RuntimeService.List reached across the Company fence")
				return agent.RuntimePage{}, nil
			},
			initiate: func(context.Context, auth.UserPrincipal, uuid.UUID) (agent.ModelDiscoveryRequest, error) {
				t.Fatal("RuntimeService.InitiateModelDiscovery reached across the Company fence")
				return agent.ModelDiscoveryRequest{}, nil
			},
			get: func(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID) (agent.ModelDiscoveryRequest, error) {
				t.Fatal("RuntimeService.GetModelDiscovery reached across the Company fence")
				return agent.ModelDiscoveryRequest{}, nil
			},
			rename: func(context.Context, auth.UserPrincipal, uuid.UUID, agent.RuntimeRenameInput) (agent.Runtime, error) {
				t.Fatal("RuntimeService.Rename reached across the Company fence")
				return agent.Runtime{}, nil
			},
			remove: func(context.Context, auth.UserPrincipal, uuid.UUID, agent.RuntimeRemovalInput) (agent.RuntimeRemoval, error) {
				t.Fatal("RuntimeService.Remove reached across the Company fence")
				return agent.RuntimeRemoval{}, nil
			},
		},
		UsageService: &usageServiceStub{
			getActivity: func(context.Context, auth.UserPrincipal) (usage.ActivityInsight, error) {
				return usage.ActivityInsight{}, usage.ErrActivityInsightForbidden
			},
			get: func(context.Context, auth.UserPrincipal, uuid.UUID, usage.Period) (usage.AgentUsageReport, error) {
				return usage.AgentUsageReport{}, usage.ErrNotFound
			},
			getCompany: func(context.Context, auth.UserPrincipal, usage.CompanyPeriod) (usage.CompanyUsageReport, error) {
				return usage.CompanyUsageReport{}, usage.ErrForbidden
			},
		},
		// The Project guard admits Company A's member with every Project
		// permission, so the oversized-body probe reaches the body limit; no
		// Project service method is reachable across the Company fence. Every
		// Group route is fenced by the Company middleware before its body is
		// read; a leaked call panics on the Group stub's nil service.
		ProjectService:         &projectServiceStub{authorize: allProjectPermissions},
		GroupService:           &groupServiceStub{},
		TaskService:            &taskServiceStub{},
		RunProofResolver:       runProofResolverStub{},
		ConversationService:    &conversationServiceStub{},
		CompanyResourceService: resourceService,
		ConnectorService:       &agent.DefaultConnectorService{},
		PluginService:          &agent.DefaultPluginService{},
		MailboxService:         &mailbox.DefaultMailboxService{},
		MailboxDraftService:    &mailbox.DefaultDraftService{},
		ScheduleService:        &schedule.DefaultScheduleService{},
		TaskReportService:      &task.DefaultReportService{},
		CommerceService:        routeTableCommerce{},
		// Every Wiki route is fenced by company membership middleware and
		// binds any body before calling the service, so no probe reaches it.
		// A leaked call would get 200 from wikiAgentReadServiceStub or a
		// recovered panic (500) from wikiServiceStub; both fail the assertion.
		WikiService:          &wikiServiceStub{},
		WikiAgentReadService: &wikiAgentReadServiceStub{},
	})
}

func crossCompanyPath(routePath string, companyID uuid.UUID) string {
	segments := strings.Split(routePath, "/")
	for index, segment := range segments {
		switch {
		case segment == ":companyId":
			segments[index] = companyID.String()
		case strings.HasPrefix(segment, ":"):
			segments[index] = crossCompanyResource.String()
		}
	}
	return strings.Join(segments, "/")
}

func serveCrossCompany(
	handler http.Handler,
	method, path, body string,
	authenticated bool,
) *httptest.ResponseRecorder {
	var request *http.Request
	if body == "" {
		request = httptest.NewRequest(method, path, nil)
	} else {
		request = httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
	}
	if authenticated {
		request.Header.Set("Authorization", "Bearer access-token")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestCrossCompanyAuthorizationMatrixCoversEveryMountedCompanyScopedRoute(t *testing.T) {
	t.Parallel()

	checks := &crossCompanyServiceChecks{}
	handler := crossCompanyHandler(t, checks)
	matrix := crossCompanyMatrix()
	remaining := make(map[string]struct{}, len(matrix))
	for key := range matrix {
		remaining[key] = struct{}{}
	}

	// Oversized body: strictly larger than every company-scoped route's body
	// limit (the largest is 12 MiB) so http.MaxBytesReader must trip first.
	oversizedBody := `{"pad":"` + strings.Repeat("a", 13<<20) + `"}`

	walked := 0
	for _, route := range handler.router.Routes() {
		if !strings.Contains(route.Path, ":companyId") {
			continue
		}
		walked++
		key := route.Method + " " + route.Path
		spec, listed := matrix[key]
		if !listed {
			t.Errorf(
				"mounted company-scoped route %q is missing from the cross-company authorization matrix; every :companyId route must prove its denial",
				key,
			)
			continue
		}
		delete(remaining, key)
		if spec.wantStatus == 0 {
			spec.wantStatus = http.StatusNotFound
		}
		if spec.wantCode == "" {
			spec.wantCode = "company_not_found"
		}
		if spec.wantAnonymousCode == "" {
			spec.wantAnonymousCode = "authentication_required"
		}
		if spec.body == "" && route.Method != http.MethodGet {
			spec.body = "{}"
		}

		t.Run(key, func(t *testing.T) {
			t.Run("member of company A is denied company B without disclosure", func(t *testing.T) {
				response := serveCrossCompany(
					handler, route.Method, crossCompanyPath(route.Path, crossCompanyB),
					spec.body, true,
				)
				assertErrorResponse(t, response, spec.wantStatus, spec.wantCode)
			})
			t.Run("anonymous request is rejected before any service", func(t *testing.T) {
				response := serveCrossCompany(
					handler, route.Method, crossCompanyPath(route.Path, crossCompanyB),
					spec.body, false,
				)
				assertErrorResponse(t, response, http.StatusUnauthorized, spec.wantAnonymousCode)
			})
			if route.Method == http.MethodGet {
				return
			}
			t.Run("oversized body is rejected with 413 before any side effect", func(t *testing.T) {
				before := len(checks.snapshot())
				response := serveCrossCompany(
					handler, route.Method, crossCompanyPath(route.Path, crossCompanyA),
					oversizedBody, true,
				)
				assertErrorResponse(t, response, http.StatusRequestEntityTooLarge, "request_too_large")
				if after := len(checks.snapshot()); after != before {
					t.Fatalf(
						"oversized body reached a domain service: checks %d -> %d",
						before,
						after,
					)
				}
			})
		})
	}

	for key := range remaining {
		t.Errorf("matrix entry %q does not match any mounted route; update the matrix", key)
	}
	if walked != len(matrix) {
		t.Errorf("walked %d mounted company-scoped routes, matrix lists %d", walked, len(matrix))
	}

	// Every service-level authorization seam must have been consulted with
	// exactly Company B, and nothing else: the middleware-guarded routes never
	// reach a domain service for a foreign company.
	serviceChecked := checks.snapshot()
	const wantServiceChecks = 6 // company get + 5 company-resource routes
	if len(serviceChecked) != wantServiceChecks {
		t.Errorf("service-level authorization checks = %d, want %d", len(serviceChecked), wantServiceChecks)
	}
	for _, companyID := range serviceChecked {
		if companyID != crossCompanyB {
			t.Errorf(
				"service-level authorization saw company %s, want only cross-company probes for %s",
				companyID,
				crossCompanyB,
			)
		}
	}
}

// projectAccessRoute is one mounted route under /projects/:projectId with a
// body that passes handler validation, so a refusal can only come from the
// Project guards.
type projectAccessRoute struct {
	method, path, body string
}

func projectAccessRoutes() []projectAccessRoute {
	return []projectAccessRoute{
		{http.MethodGet, "", ""},
		{http.MethodPatch, "", `{"expectedRevision":1,"title":"Renamed"}`},
		{http.MethodPost, "/archive", `{"expectedRevision":1}`},
		{http.MethodPut, "/files", `{"logoId":null,"documentIds":[],"expectedRevision":1}`},
		{http.MethodGet, "/grants", ""},
		{http.MethodPut, "/grants/" + crossCompanyResource.String(), `{"permissions":["project.read"],"expectedRevision":1}`},
		{http.MethodDelete, "/grants/" + crossCompanyResource.String(), `{"expectedRevision":1}`},
		{http.MethodPost, "/grants/" + crossCompanyResource.String() + "/preview", `{"permissions":[]}`},
		{http.MethodPost, "/join", `{"expectedRevision":1}`},
		{
			http.MethodPut, "/group-grants/" + crossCompanyResource.String(),
			`{"permissions":["project.read"],"expectedRevision":1}`,
		},
		{http.MethodDelete, "/group-grants/" + crossCompanyResource.String(), `{"expectedRevision":1}`},
		{
			http.MethodPost, "/group-grants/" + crossCompanyResource.String() + "/preview",
			`{"permissions":["project.read"]}`,
		},
	}
}

// projectAccessHandler serves the Project routes for Company A's member with
// the given role and Project standing. Every service method counts the
// calls that reached it and answers success.
func projectAccessHandler(role auth.CompanyRole, access auth.ProjectAccess, visible bool, reached *int) *Handler {
	principal := crossCompanyUserPrincipal(role)
	project := company.ProjectResponse{ID: crossCompanyResource, CompanyID: crossCompanyA}
	count := func() { *reached++ }
	service := &projectServiceStub{
		authorize: func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.ProjectAccess, error) {
			if !visible {
				return auth.ProjectAccess{}, auth.ErrProjectNotVisible
			}
			return access, nil
		},
		get: func(context.Context, auth.UserPrincipal, uuid.UUID) (company.ProjectResponse, error) {
			count()
			return project, nil
		},
		update: func(context.Context, auth.UserPrincipal, uuid.UUID, company.ProjectUpdateInput) (company.ProjectResponse, error) {
			count()
			return project, nil
		},
		archive: func(context.Context, auth.UserPrincipal, uuid.UUID, int64) (company.ProjectResponse, error) {
			count()
			return project, nil
		},
		replaceFiles: func(context.Context, auth.UserPrincipal, uuid.UUID, company.ProjectFilesInput, int64) (company.ProjectResponse, error) {
			count()
			return project, nil
		},
		listGrants: func(context.Context, auth.UserPrincipal, uuid.UUID) (company.ProjectGrantList, error) {
			count()
			return company.ProjectGrantList{
				Items:  []company.ProjectGrant{},
				Groups: []company.ProjectGroupGrant{},
			}, nil
		},
		setGroup: func(context.Context, auth.UserPrincipal, uuid.UUID, company.ProjectGroupGrantInput) (company.ProjectGroupGrantResult, error) {
			count()
			return company.ProjectGroupGrantResult{}, nil
		},
		removeGroup: func(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, int64) (company.ProjectGroupGrantResult, error) {
			count()
			return company.ProjectGroupGrantResult{}, nil
		},
		previewGroup: func(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, []string) (company.GroupPreview, error) {
			count()
			return company.GroupPreview{}, nil
		},
		setGrants: func(context.Context, auth.UserPrincipal, uuid.UUID, company.ProjectGrantInput) (company.ProjectGrantResult, error) {
			count()
			return company.ProjectGrantResult{}, nil
		},
		removeGrants: func(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, int64) (company.ProjectGrantResult, error) {
			count()
			return company.ProjectGrantResult{}, nil
		},
		previewGrant: func(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, []string) (company.ProjectGrantPreview, error) {
			count()
			return company.ProjectGrantPreview{}, nil
		},
		join: func(context.Context, auth.UserPrincipal, uuid.UUID, int64) (company.ProjectResponse, error) {
			count()
			return project, nil
		},
		getForRun: func(context.Context, auth.UserPrincipal, uuid.UUID, *task.RunScope) (company.ProjectResponse, error) {
			count()
			return project, nil
		},
		accessReport: func(context.Context, auth.UserPrincipal) (company.ProjectAccessReport, error) {
			count()
			return company.ProjectAccessReport{
				Since: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
				Items: []company.ProjectAccessReportEntry{{
					ProjectID: crossCompanyResource, Key: "web", Title: "Website",
					People: []company.ProjectAccessReportPerson{{
						UserID: crossCompanyUser,
						Reasons: []company.ProjectAccessReportReason{
							company.ProjectAccessReportReasonRunAuthorizer, company.ProjectAccessReportReasonTaskAssignee,
						},
					}},
				}},
			}, nil
		},
	}
	authorizer := crossCompanyUser
	return New(Handler{
		RunProofResolver: runProofResolverStub{origin: execution.RunOrigin{
			RunID: uuid.New(), CompanyID: crossCompanyA, AgentID: uuid.New(),
			ConversationID: uuid.New(), ConversationKind: "direct", AuthorizedByUserID: &authorizer,
		}},
		AuthService: authenticatedAuthStub(crossCompanyUser),
		CompanyService: &companyServiceStub{
			authorize: func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error) {
				return principal, nil
			},
			requirePermission: func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error) {
				if !role.Can(auth.PermissionProjectsManage) {
					return auth.UserPrincipal{}, company.ErrForbidden
				}
				return principal, nil
			},
		},
		ProjectService: service,
	})
}

// TestProjectRoutesAnswerFromProjectAccess is the Project half of the matrix
// (ADR 0017): inside a Company, every route under /projects/:projectId
// answers from the caller's Project access. A Project the caller cannot see
// is 404, never 403; a visible one without the permission is 403
// project_permission_required; a Run proof never reaches grants,
// visibility, files, archive, Join or the manage view; and every refusal
// lands before a service runs.
func TestProjectRoutesAnswerFromProjectAccess(t *testing.T) {
	t.Parallel()

	root := "/api/v1/companies/" + crossCompanyA.String() + "/projects/" + crossCompanyResource.String()
	serve := func(h *Handler, route projectAccessRoute, runProof bool) *httptest.ResponseRecorder {
		request := httptest.NewRequest(route.method, root+route.path, strings.NewReader(route.body))
		request.Header.Set("Authorization", "Bearer access-token")
		if route.body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		if runProof {
			request.Header.Set(runTokenHeader, "brt_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
		}
		response := httptest.NewRecorder()
		h.ServeHTTP(response, request)
		return response
	}
	type want struct {
		status int
		code   string
	}
	notFound := want{http.StatusNotFound, "project_not_found"}
	ok := want{status: http.StatusOK}
	humanOnly := want{http.StatusForbidden, "human_actor_required"}
	reader := auth.ProjectAccess{Via: auth.ProjectAccessViaGrant, Permissions: auth.ProjectPermissions()}
	bridge := auth.ProjectAccess{Management: true, Permissions: auth.ManagementProjectPermissions()}

	for _, scenario := range []struct {
		name     string
		role     auth.CompanyRole
		access   auth.ProjectAccess
		visible  bool
		runProof bool
		want     map[string]want
	}{
		{
			// A member who cannot read the Project learns nothing about it.
			// Join is a Company-level route and answers from the Company
			// catalog first.
			name: "non-reader", role: auth.CompanyRoleMember,
			want: map[string]want{
				"POST /join": {http.StatusForbidden, "company_permission_required"},
			},
		},
		{
			// Management is not content: an admin holding only the bridge
			// sees the Project and manages its settings, grants and archive,
			// but files need an explicit grant.
			name: "bridge-only admin", role: auth.CompanyRoleAdmin, access: bridge, visible: true,
			want: map[string]want{
				"GET ": ok, "PATCH ": ok, "POST /archive": ok,
				"PUT /files":  {http.StatusForbidden, "project_permission_required"},
				"GET /grants": ok, "PUT /grants/": ok, "DELETE /grants/": ok, "POST /grants/preview": ok,
				"POST /join":         ok,
				"PUT /group-grants/": ok, "DELETE /group-grants/": ok, "POST /group-grants/preview": ok,
			},
		},
		{
			// A reader without project.grants.manage sees the grants but
			// changes none, a Group's included (Access TECH D4).
			name: "reader without access management", role: auth.CompanyRoleMember, visible: true,
			access: auth.ProjectAccess{
				Via: auth.ProjectAccessViaGrant, Permissions: []auth.ProjectPermission{auth.ProjectPermissionRead},
			},
			want: map[string]want{
				"GET ": ok, "PATCH ": ok, "GET /grants": ok,
				"POST /archive":              {http.StatusForbidden, "project_permission_required"},
				"PUT /files":                 {http.StatusForbidden, "project_permission_required"},
				"PUT /grants/":               {http.StatusForbidden, "project_permission_required"},
				"DELETE /grants/":            {http.StatusForbidden, "project_permission_required"},
				"POST /grants/preview":       {http.StatusForbidden, "project_permission_required"},
				"POST /join":                 {http.StatusForbidden, "company_permission_required"},
				"PUT /group-grants/":         {http.StatusForbidden, "project_permission_required"},
				"DELETE /group-grants/":      {http.StatusForbidden, "project_permission_required"},
				"POST /group-grants/preview": {http.StatusForbidden, "project_permission_required"},
			},
		},
		{
			// A reader holding every Project permission, acting through a
			// Run proof, is refused management and every change of who may
			// see what.
			name: "Run proof", role: auth.CompanyRoleAdmin, access: reader, visible: true, runProof: true,
			want: map[string]want{
				"GET ": ok, "POST /archive": humanOnly,
				"PATCH ": humanOnly, "PUT /files": humanOnly, "GET /grants": humanOnly,
				"PUT /grants/": humanOnly, "DELETE /grants/": humanOnly, "POST /grants/preview": humanOnly,
				"POST /join":         humanOnly,
				"PUT /group-grants/": humanOnly, "DELETE /group-grants/": humanOnly,
				"POST /group-grants/preview": humanOnly,
			},
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			for _, route := range projectAccessRoutes() {
				path := strings.ReplaceAll(route.path, crossCompanyResource.String()+"/", "")
				key := route.method + " " + strings.TrimSuffix(path, crossCompanyResource.String())
				expected, listed := scenario.want[key]
				if !listed {
					expected = notFound
				}
				reached := 0
				h := projectAccessHandler(scenario.role, scenario.access, scenario.visible, &reached)
				response := serve(h, route, scenario.runProof)
				if expected.code == "" {
					if response.Code != expected.status || reached != 1 {
						t.Errorf(
							"%s = %d (service calls %d); body %s",
							key,
							response.Code,
							reached,
							response.Body.String(),
						)
					}
					continue
				}
				if response.Code != expected.status ||
					!strings.Contains(
						response.Body.String(),
						`"code":"`+expected.code+`"`,
					) || reached != 0 {
					t.Errorf(
						"%s = %d %s (service calls %d), want %d %s",
						key,
						response.Code,
						response.Body.String(),
						reached,
						expected.status,
						expected.code,
					)
				}
			}
		})
	}

	t.Run("PATCH naming files is refused by the strict decoder", func(t *testing.T) {
		t.Parallel()
		reached := 0
		h := projectAccessHandler(auth.CompanyRoleOwner, reader, true, &reached)
		response := serve(h, projectAccessRoute{
			http.MethodPatch, "", `{"expectedRevision":1,"files":{"logoId":null,"documentIds":[]}}`,
		}, false)
		assertErrorResponse(t, response, http.StatusBadRequest, "invalid_request")
		if reached != 0 {
			t.Fatalf("PATCH with files reached the service %d times", reached)
		}
	})

	t.Run("a malformed grant target is bad input", func(t *testing.T) {
		t.Parallel()
		for _, route := range []projectAccessRoute{
			{http.MethodPut, "/grants/not-a-user", `{"permissions":["project.read"],"expectedRevision":1}`},
			{http.MethodDelete, "/grants/not-a-user", `{"expectedRevision":1}`},
		} {
			reached := 0
			h := projectAccessHandler(auth.CompanyRoleOwner, reader, true, &reached)
			assertErrorResponse(
				t,
				serve(h, route, false),
				http.StatusUnprocessableEntity,
				"validation_failed",
			)
			if reached != 0 {
				t.Fatalf("%s %s reached the service %d times", route.method, route.path, reached)
			}
		}
	})

	t.Run("the access report is a manager's, and a person's", func(t *testing.T) {
		t.Parallel()
		report := func(role auth.CompanyRole, runProof bool) (*httptest.ResponseRecorder, int) {
			reached := 0
			h := projectAccessHandler(role, auth.ProjectAccess{}, false, &reached)
			request := httptest.NewRequest(http.MethodGet,
				"/api/v1/companies/"+crossCompanyA.String()+"/projects/access-report", nil)
			request.Header.Set("Authorization", "Bearer access-token")
			if runProof {
				request.Header.Set(
					runTokenHeader,
					"brt_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				)
			}
			response := httptest.NewRecorder()
			h.ServeHTTP(response, request)
			return response, reached
		}
		// The report is Company-level: it never asks for Project access, so
		// an owner who reads no Project still gets it, in the projects.yaml
		// ProjectAccessReport shape.
		want := `{"since":"2026-09-01T00:00:00Z","items":[{"projectId":"` + crossCompanyResource.String() +
			`","key":"web","title":"Website","people":[{"userId":"` + crossCompanyUser.String() +
			`","reasons":["run_authorizer","task_assignee"]}]}]}`
		if response, reached := report(
			auth.CompanyRoleOwner,
			false,
		); response.Code != http.StatusOK ||
			reached != 1 ||
			response.Body.String() != want {
			t.Fatalf(
				"owner report = %d %s (service calls %d)",
				response.Code,
				response.Body.String(),
				reached,
			)
		}
		response, reached := report(auth.CompanyRoleMember, false)
		assertErrorResponse(t, response, http.StatusForbidden, "company_permission_required")
		if reached != 0 {
			t.Fatalf("a member reached the report %d times", reached)
		}
		response, reached = report(auth.CompanyRoleOwner, true)
		assertErrorResponse(t, response, http.StatusForbidden, "human_actor_required")
		if reached != 0 {
			t.Fatalf("a Run proof reached the report %d times", reached)
		}
	})

	t.Run("the manage view is a person's", func(t *testing.T) {
		t.Parallel()
		h := projectAccessHandler(auth.CompanyRoleAdmin, bridge, true, new(int))
		request := httptest.NewRequest(http.MethodGet,
			"/api/v1/companies/"+crossCompanyA.String()+"/projects?view=manage", nil)
		request.Header.Set("Authorization", "Bearer access-token")
		request.Header.Set("X-Boardly-Run-Token", "run-proof")
		response := httptest.NewRecorder()
		h.ServeHTTP(response, request)
		assertErrorResponse(t, response, http.StatusForbidden, "human_actor_required")
	})
}
