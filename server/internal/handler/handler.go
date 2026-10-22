// Package handler owns the Gin transport boundary: one file per resource,
// plus handler.go, which holds the per-router Handler and its New composer.
//
// Gin types never leave this package (AGENTS.md, "Dependency rules"), and it
// is terminal — only cmd/* may import it.
package handler

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"
	"slices"

	"github.com/Intelligent-Internet/zenith-team/server/internal/artifact"

	"github.com/Intelligent-Internet/zenith-team/server/internal/task"

	"github.com/Intelligent-Internet/zenith-team/server/internal/agent"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/calendar"
	"github.com/Intelligent-Internet/zenith-team/server/internal/canvas"
	"github.com/Intelligent-Internet/zenith-team/server/internal/company"
	"github.com/Intelligent-Internet/zenith-team/server/internal/conversation"
	"github.com/Intelligent-Internet/zenith-team/server/internal/daemonws"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
	"github.com/Intelligent-Internet/zenith-team/server/internal/realtime"
	"github.com/Intelligent-Internet/zenith-team/server/internal/social"
	"github.com/Intelligent-Internet/zenith-team/server/internal/usage"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
	db "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"

	"github.com/gin-gonic/gin"
)

type Readiness interface {
	Ping(context.Context) error
}

// Handler owns one fully composed Zenith Team HTTP API. The application
// composition root assigns its transport configuration and consumer-side
// service interfaces before New installs the private Gin engine.
type Handler struct {
	CalendarService          calendar.CalendarService
	Readiness                Readiness
	Logger                   *slog.Logger
	AllowedOrigins           []string
	TrustedProxies           []netip.Prefix
	CSRF                     auth.CSRFProtector
	AuthService              AuthService
	AccountService           AccountService
	DesktopHandoffService    DesktopHandoffService
	GoogleOAuthService       GoogleOAuthService
	OAuthWebAppOrigin        string
	InitializationService    company.InitializationService
	CompanyService           CompanyService
	MilestoneService         MilestoneService
	ReferenceService         ReferenceService
	InvitationService        company.InvitationService
	InvitationEmailEnabled   bool
	ProactiveModeService     ProactiveModeService
	CompanySearchService     CompanySearchService
	AgentService             AgentService
	SkillService             agent.SkillService
	AgentBindingService      AgentBindingService
	RuntimeService           RuntimeService
	ConnectorService         agent.ConnectorService
	PluginService            agent.PluginService
	UsageService             UsageService
	AnalyticsService         AnalyticsService
	TaskService              TaskService
	BoardService             canvas.BoardService
	BoardMediaService        canvas.BoardMediaService
	EphemeralRelay           *realtime.EphemeralRelay
	CanvasService            canvas.CanvasService
	PageService              canvas.PageService
	PresentationService      canvas.PresentationService
	CanvasPresence           *realtime.CanvasPresence
	ProjectService           company.ProjectService
	GroupService             company.GroupService
	WikiService              wiki.WikiService
	WikiAgentReadService     wiki.AgentReadService
	WikiAgentWriteService    wiki.AgentWriteService
	CompanyPlanService       CompanyPlanService
	ApprovalService          ApprovalService
	QuestionService          QuestionService
	RunProofResolver         RunProofResolver
	ConversationService      ConversationService
	HumanConversationService conversation.ConversationService
	DiscussionService        conversation.DiscussionService
	ChannelInvocationService conversation.ChannelInvocationService
	HuddleService            conversation.HuddleService
	HuddleBudgetService      usage.HuddleBudgetService
	HuddleAIService          conversation.HuddleAIService
	HuddleNotesService       conversation.HuddleNotesService
	HuddleAIWorkloadSecret   string
	// HuddleCallsEnabled is true when LiveKit is configured. Without it there
	// is no media, so calls cannot be started or joined and clients hide them.
	HuddleCallsEnabled bool
	// HuddleVoiceAgentsEnabled reports HUDDLE_AI_ENABLED to clients,
	// which hide voice Agent features while it is off.
	HuddleVoiceAgentsEnabled bool
	LiveKitAPIKey            string
	LiveKitAPISecret         string
	StudioProjectService     conversation.StudioProjectService
	StudioEditorService      conversation.StudioEditorService
	MessageContextService    MessageContextService
	ActivityFeed             ActivityFeed
	InboxService             InboxService
	CrashReportService       CrashReportService
	CompanyResourceService   CompanyResourceService
	AttachmentService        AttachmentService
	ArtifactService          artifact.ArtifactService
	DeliverableService       DeliverableService
	PublicationService       PublicationService
	MailboxService           MailboxService
	MailboxDraftService      MailboxDraftService
	MailboxWebhookVerifier   MailboxWebhookVerifier
	MailboxWebhookObserver   MailboxWebhookObserver
	ScheduleService          ScheduleService
	TaskCalendarService      task.CalendarService
	TaskReportService        TaskReportService
	TaskMergeService         TaskMergeService
	SocialService            social.SocialService
	// Daemon plane (wave 3): the run lifecycle service, the raw query set
	// and transaction starter the daemon handlers use directly, the
	// post-commit event bus, and the daemon WS hub seam (no-op until wave 4).
	RunService execution.RunService
	Queries    *db.Queries
	Bus        *events.Bus
	// DaemonWSHub serves GET /api/v1/daemon/ws (wave 6). Nil leaves the
	// route unregistered so a composition root without the hub still boots.
	DaemonWSHub    *daemonws.Hub
	AuthCookies    CookieOptions
	MetricsHandler http.Handler
	// RealtimeMetricsToken admits non-loopback readers of /health/realtime.
	RealtimeMetricsToken string
	StudioAPIOrigin      string
	// RequireDaemonID is the X-Zenith-Team-Daemon-ID rollout stage for the
	// daemon plane's runtime-scoped routes: false admits a missing header
	// with a warning log, true refuses it with 400 invalid_request before
	// any database access.
	RequireDaemonID bool
	HTTPMetrics     middleware.HTTPObserver
	ClientWebSocket http.Handler
	router          *gin.Engine
}

var _ http.Handler = (*Handler)(nil)

type healthStatusResponse struct {
	Status string `json:"status"`
}

func New(h Handler) *Handler {
	// Set here rather than in a package init(): an init() side effect fires in
	// every test binary that links this package, including the many that never
	// build a router, and it mutates process-global state in gin to do it.
	gin.SetMode(gin.ReleaseMode)

	h.AllowedOrigins = slices.Clone(h.AllowedOrigins)
	h.TrustedProxies = slices.Clone(h.TrustedProxies)
	h.router = gin.New()
	h.router.Use(
		middleware.RequestID(),
		middleware.ClientMetadata(),
		middleware.HTTPMetrics(h.HTTPMetrics),
		middleware.RequestLogger(h.Logger),
		middleware.Recovery(h.Logger),
		middleware.OAuthSecurityHeaders(
			h.AuthCookies.Secure, googleOAuthWebCallbackPath, clearGoogleOAuthWebBindingCookie),
		middleware.CORS(middleware.CORSOptions{
			AllowedOrigins: h.AllowedOrigins,
			BypassPaths:    []string{clientWebSocketHTTPPath},
		}),
		middleware.RequireCSRF(h.CSRF),
		// Before authentication and every route guard: a Run proof on a
		// route that does not accept a Run is 403 human_actor_required. It
		// also tells the credential guards how to admit a daemon's own
		// session off the daemon plane: only as a live Run.
		middleware.RunProofPolicy(routeActorTypes, h.runOriginResolver()),
	)
	// Gin's own miss is plain-text "404 page not found". Every failure the
	// API serves carries the {detail, code, requestId} envelope, and the
	// global middleware above runs before this handler, so the request id is
	// already set. Gin sends a method mismatch on a known path here too.
	h.router.NoRoute(func(c *gin.Context) {
		writeBoundaryError(c, boundaryErrorRouteNotFound)
	})
	h.router.GET("/healthz", h.health)
	h.router.GET("/readyz", h.readiness)
	h.router.GET("/health/realtime", h.realtimeMetrics)
	if h.MetricsHandler != nil {
		h.router.GET("/metrics", gin.WrapH(h.MetricsHandler))
	}
	api := h.router.Group("/api/v1")
	h.registerRoutes(api)
	return &h
}

func (h *Handler) registerRoutes(api *gin.RouterGroup) {
	h.registerClientWebSocketRoute(api)
	h.registerGoogleOAuthRoutes(api)
	h.registerAuthRoutes(api)
	h.registerAccountRoutes(api)
	h.registerCalendarRoutes(api)
	h.registerSocialRoutes(api)
	h.registerDesktopHandoffRoutes(api)
	h.registerConversationRoutes(api)
	h.registerDiscussionRoutes(api)
	h.registerStudioEditorRoutes(api)
	h.registerHuddleRoutes(api)
	h.registerHuddleAIRoutes(api)
	h.registerHuddleNotesRoutes(api)
	h.registerDaemonRoutes(api)
	h.registerConnectorDaemonRoutes(api)
	h.registerPluginDaemonRoutes(api)
	h.registerRunRoutes(api)
	h.registerRuntimeRoutes(api)
	h.registerConnectorRoutes(api)
	h.registerPluginRoutes(api)
	h.registerAttachmentRoutes(api)
	h.registerArtifactContentRoutes(api)
	h.registerDeliverableRoutes(api)
	h.registerPublicationRoutes(api)
	h.registerCrashReportRoutes(api)
	h.registerCompanyResourceRoutes(api)
	h.registerCompanyRoutes(api)
	h.registerInitializationRoutes(api)
	h.registerMilestoneRoutes(api)
	h.registerReferenceDocumentRoutes(api)
	h.registerInvitationRoutes(api)
	h.registerProactiveModeRoutes(api)
	h.registerCompanySearchRoutes(api)
	h.registerActivityRoutes(api)
	h.registerInboxRoutes(api)
	h.registerAgentRoutes(api)
	h.registerAgentSkillRoutes(api)
	h.registerAgentBindingRoutes(api)
	h.registerPlatformSkillRoutes(api)
	h.registerWorkflowRoutes(api)
	h.registerUsageRoutes(api)
	h.registerAnalyticsRoutes(api)
	h.registerHuddleBudgetRoutes(api)
	h.registerProjectRoutes(api)
	h.registerGroupRoutes(api)
	h.registerCanvasRoutes(api)
	h.registerWhiteboardRoutes(api)
	h.registerPageRoutes(api)
	h.registerWikiRoutes(api)
	h.registerWikiAgentRoutes(api)
	h.registerTaskRoutes(api)
	h.registerTaskFailureRoutes(api)
	h.registerCompanyPlanRoutes(api)
	h.registerApprovalRoutes(api)
	h.registerMailboxRoutes(api)
	h.registerScheduleRoutes(api)
	h.registerTaskReportRoutes(api)
}

// ServeHTTP implements http.Handler without exposing the mutable Gin engine to
// the composition root.
func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	h.router.ServeHTTP(writer, request)
}

func (h *Handler) health(c *gin.Context) {
	c.JSON(http.StatusOK, healthStatusResponse{Status: "ok"})
}

func (h *Handler) readiness(c *gin.Context) {
	readiness := h.Readiness
	if readiness == nil || readiness.Ping(c.Request.Context()) != nil {
		writeBoundaryError(c, boundaryErrorServiceNotReady)
		return
	}
	c.JSON(http.StatusOK, healthStatusResponse{Status: "ready"})
}
