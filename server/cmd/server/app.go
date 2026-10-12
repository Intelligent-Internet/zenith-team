// The Boardly cloud API composition root. run builds every service, hub,
// relay and background worker in dependency order, serves HTTP until ctx is
// done, and joins the workers on shutdown.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/account"
	"boardly.dev/boardly/server/internal/activity"
	"boardly.dev/boardly/server/internal/agent"
	"boardly.dev/boardly/server/internal/approval"
	"boardly.dev/boardly/server/internal/attachment"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/calendar"
	"boardly.dev/boardly/server/internal/canvas"
	"boardly.dev/boardly/server/internal/commerce"
	"boardly.dev/boardly/server/internal/company"
	"boardly.dev/boardly/server/internal/config"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/daemonws"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/handler"
	"boardly.dev/boardly/server/internal/inbox"
	"boardly.dev/boardly/server/internal/infra/cloudrun"
	"boardly.dev/boardly/server/internal/infra/livekit"
	"boardly.dev/boardly/server/internal/infra/redis"
	"boardly.dev/boardly/server/internal/infra/storage"
	"boardly.dev/boardly/server/internal/platformbundle"
	"boardly.dev/boardly/server/internal/realtime"
	"boardly.dev/boardly/server/internal/schedule"
	"boardly.dev/boardly/server/internal/social"
	"boardly.dev/boardly/server/internal/task"
	"boardly.dev/boardly/server/internal/telemetry"
	"boardly.dev/boardly/server/internal/usage"
	"boardly.dev/boardly/server/internal/util"
	"boardly.dev/boardly/server/internal/wiki"
	"boardly.dev/boardly/server/internal/workflow"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

type liveKitRoomProviderAdapter struct{ provider *livekit.HTTPRoomProvider }

func (p liveKitRoomProviderAdapter) EnsureRoom(ctx context.Context, room conversation.HuddleRoom) error {
	return p.provider.EnsureRoom(ctx, livekit.Room{Name: room.Name, ParticipantLimit: room.ParticipantLimit})
}

func (p liveKitRoomProviderAdapter) DeleteRoom(ctx context.Context, name string) error {
	return p.provider.DeleteRoom(ctx, name)
}

func (p liveKitRoomProviderAdapter) ReadyRooms(ctx context.Context, names []string) (map[string]bool, error) {
	return p.provider.ReadyRooms(ctx, names)
}

func (p liveKitRoomProviderAdapter) IssueParticipantToken(
	ctx context.Context,
	roomName, identity string,
) (conversation.HuddleMediaToken, error) {
	token, err := p.provider.IssueParticipantToken(ctx, roomName, identity)
	if err != nil {
		return conversation.HuddleMediaToken{}, err
	}
	return conversation.HuddleMediaToken{URL: token.URL, Token: token.Token}, nil
}

func newLiveKitRoomProvider(cfg config.LiveKitConfig) conversation.HuddleRoomProvider {
	if cfg.URL == "" && cfg.APIKey == "" && cfg.APISecret == "" {
		return nil
	}
	issuer, err := livekit.NewTokenIssuer(
		livekit.TokenOptions{APIKey: cfg.APIKey, APISecret: cfg.APISecret, TTL: 10 * time.Minute},
	)
	if err != nil {
		slog.Warn("LiveKit media disabled", "error", err)
		return nil
	}
	provider, err := livekit.NewHTTPRoomProvider(cfg.URL, issuer, nil)
	if err != nil {
		slog.Warn("LiveKit media disabled", "error", err)
		return nil
	}
	return liveKitRoomProviderAdapter{provider: provider}
}

// run composes and operates the Boardly cloud API: it loads configuration
// through lookup, installs the process logger on logDestination, applies
// migrations, serves HTTP until ctx is done, and returns once every
// background worker has been joined.
func run(ctx context.Context, lookup config.LookupEnv, logDestination io.Writer) error {
	cfg, err := config.Load(lookup)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	logger, err := config.NewLogger(logDestination, cfg.LogLevel)
	if err != nil {
		return fmt.Errorf("construct logger: %w", err)
	}
	logger = logger.With("app", cfg.Name, "environment", cfg.Environment)
	slog.SetDefault(logger)
	logger.Info("starting")
	// The Workflow catalog is release-owned pinned content (ADR 0016): a
	// release that cannot verify or parse it refuses to start rather than
	// serving 500s later.
	if _, err := workflow.List(); err != nil {
		return fmt.Errorf("load workflow catalog: %w", err)
	}
	pinnedBundles, err := platformbundle.Load()
	if err != nil {
		return fmt.Errorf("load pinned platform content: %w", err)
	}
	for _, bundle := range pinnedBundles {
		lock := bundle.Lock
		logger.Info("platform bundle pinned", "plugin", lock.Plugin, "component", lock.Component,
			"version", lock.Version, "sha256", lock.Artifact.SHA256, "commit", lock.Commit)
	}

	pool, err := db.NewPool(ctx, db.PoolConfig{
		URL:               cfg.DatabaseURL,
		MaxConns:          cfg.Database.MaxConns,
		MinConns:          cfg.Database.MinConns,
		MaxConnLifetime:   cfg.Database.MaxConnLifetime,
		MaxConnIdleTime:   cfg.Database.MaxConnIdleTime,
		HealthCheckPeriod: cfg.Database.HealthCheckPeriod,
	})
	if err != nil {
		return fmt.Errorf("open PostgreSQL pool: %w", err)
	}
	defer pool.Close()
	if err := db.MigrateUp(ctx, pool); err != nil {
		return fmt.Errorf("apply PostgreSQL migrations: %w", err)
	}
	databaseStore, err := db.NewStore(pool)
	if err != nil {
		return fmt.Errorf("construct PostgreSQL store: %w", err)
	}
	if err := databaseStore.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		return activity.BaselineInTx(ctx, q)
	}); err != nil {
		return fmt.Errorf("record initial Activity history: %w", err)
	}
	queries := databaseStore.Queries()
	bus, err := events.NewBus(events.BusOptions{Logger: logger})
	if err != nil {
		return fmt.Errorf("construct in-process event bus: %w", err)
	}
	inboxPresenter := inbox.NewRealtimePresenter(bus)
	registry := telemetry.NewRegistry()
	commerceService, err := newCommerceService(
		cfg.Commerce,
		databaseStore,
		bus,
		inboxPresenter,
	)
	if err != nil {
		return err
	}
	var commerceWebhookReconciler *commerce.WebhookReconciler
	var commerceDevelopmentEventPoller *commerce.DevelopmentEventPoller
	if commerceService != nil {
		if err := ensureCommerceTermsCatalog(ctx, commerceService); err != nil {
			return err
		}
		commerceWebhookReconciler, err = commerce.NewWebhookReconciler(
			commerceService,
			commerce.WebhookReconcilerOptions{
				OnIssue: newCommerceWebhookReconciliationIssueHandler(logger, registry),
			},
		)
		if err != nil {
			return fmt.Errorf("construct Commerce webhook reconciler: %w", err)
		}
		commerceDevelopmentEventPoller, err = newCommerceDevelopmentEventPoller(
			cfg.Environment,
			cfg.Commerce,
			queries,
			commerceService,
			logger,
		)
		if err != nil {
			return err
		}
	}
	objectStore, err := storage.NewFromConfig(ctx, cfg.Storage)
	if err != nil {
		return fmt.Errorf("initialize object storage: %w", err)
	}
	var (
		publicationProviders execution.PublicationProviderCatalog
		publicationWorker    *execution.PublicationWorker
		publicationRoutes    execution.PublicationRoutes
	)
	if cfg.Cloudflare.Enabled {
		publicationRoutes, err = storage.NewCloudflareRoutes(storage.CloudflareRoutesConfig{
			APIToken: cfg.Cloudflare.APIToken, AccountID: cfg.Cloudflare.AccountID,
			NamespaceID: cfg.Cloudflare.NamespaceID, BaseDomain: cfg.Cloudflare.BaseDomain,
		})
		if err != nil {
			return fmt.Errorf("construct publication routing store: %w", err)
		}
	}
	if cfg.CloudRun.Enabled {
		publisher, err := cloudrun.NewPublisher(ctx, cloudrun.Config{
			ProjectID: cfg.CloudRun.ProjectID, Region: cfg.CloudRun.Region,
			SourceBucket: cfg.CloudRun.SourceBucket, ArtifactRegistry: cfg.CloudRun.ArtifactRegistry,
			BuildServiceAccount:   cfg.CloudRun.BuildServiceAccount,
			RuntimeServiceAccount: cfg.CloudRun.RuntimeServiceAccount,
			MaxInstances:          cfg.CloudRun.MaxInstances, MinInstances: cfg.CloudRun.MinInstances,
			Memory: cfg.CloudRun.Memory, CPU: cfg.CloudRun.CPU, Timeout: cfg.CloudRun.Timeout,
			Concurrency: cfg.CloudRun.Concurrency,
		})
		if err != nil {
			return fmt.Errorf("construct Cloud Run publisher: %w", err)
		}
		provider, err := newCloudRunPublicationProvider(publisher)
		if err != nil {
			return fmt.Errorf("construct Cloud Run publication provider: %w", err)
		}
		publicationProviders, err = newPublicationProviderCatalog(provider)
		if err != nil {
			return fmt.Errorf("construct publication provider catalog: %w", err)
		}
		publicationWorker, err = execution.NewPublicationWorker(
			databaseStore,
			objectStore,
			publicationProviders,
			execution.PublicationWorkerOptions{
				Bus:     bus,
				OnIssue: newPublicationIssueHandler(logger),
				Routes:  publicationRoutes,
			},
		)
		if err != nil {
			return fmt.Errorf("construct publication worker: %w", err)
		}
	}
	publicationService, err := execution.NewPublicationService(execution.PublicationServiceOptions{
		Bus:   bus,
		Store: databaseStore, Providers: publicationProviders, Routes: publicationRoutes,
	})
	if err != nil {
		return fmt.Errorf("construct publication service: %w", err)
	}

	redisClient, err := redis.NewClient(cfg.RedisURL)
	if err != nil {
		return err
	}
	if redisClient != nil {
		defer func() {
			if err := redisClient.Close(); err != nil {
				logger.Warn("close Redis client", "error", err)
			}
		}()
	}
	mailboxParts, err := newMailboxComponents(
		cfg.Mailbox,
		cfg.APIReplicas,
		databaseStore,
		bus,
		redisClient,
		registry,
		logger,
	)
	if err != nil {
		return err
	}

	daemonHub := daemonws.NewHub()
	var studioRelay conversation.StudioEditorRelay = daemonHub
	var distributedStudio *daemonws.StudioRelay
	if redisClient != nil {
		distributedStudio, err = daemonws.NewStudioRelay(ctx, daemonHub, redisClient)
		if err != nil {
			return err
		}
		defer distributedStudio.Close()
		studioRelay = distributedStudio
	}
	// daemonWakeup is what the run domain kicks when work becomes claimable.
	// Without Redis it is the local hub only; with Redis the RelayNotifier
	// also publishes the wakeup on the stream so whichever API node holds the
	// daemon's socket delivers it.
	var daemonWakeup daemonws.Wakeup = daemonHub

	// Realtime plane: the in-process hub is always the local delivery target.
	// The EventBroadcaster mints one id and frame before local delivery, then
	// optionally publishes that same pair through Redis for cross-node fanout.
	hub := realtime.NewHub()
	go hub.Run()
	var relay realtime.ManagedRelay
	if redisClient != nil {
		relayRead, err := redis.NewClient(cfg.RedisURL)
		if err != nil {
			return err
		}
		defer func() {
			if err := relayRead.Close(); err != nil {
				logger.Warn("close Redis relay read client", "error", err)
			}
		}()
		sharded := realtime.NewShardedStreamRelay(
			hub, redisClient, relayRead, realtime.DefaultShardedStreamRelayConfig(),
		)
		sharded.SetDaemonRuntimeDeliverer(daemonHub)
		relay = sharded
		daemonWakeup = daemonws.NewRelayNotifier(daemonHub, sharded)
	}
	broadcaster := realtime.NewEventBroadcaster(hub, relay)
	ephemeralRelay, err := realtime.NewEphemeralRelay(ctx, hub, redisClient)
	if err != nil {
		return fmt.Errorf("start ephemeral relay: %w", err)
	}
	defer ephemeralRelay.Close()

	passwords, err := auth.NewPasswordHasher(auth.PasswordHasherOptions{})
	if err != nil {
		return fmt.Errorf("construct password hasher: %w", err)
	}
	tokens, err := auth.NewTokenManager(auth.TokenManagerOptions{
		Secret:     []byte(cfg.CSRFSecret),
		AccessTTL:  cfg.Auth.AccessTTL,
		RefreshTTL: cfg.Auth.RefreshTTL,
	})
	if err != nil {
		return fmt.Errorf("construct auth token manager: %w", err)
	}
	// Live socket teardown on revocation returns with the frontend realtime
	// phase; revocation events still reach clients as auth.session_revoked.
	sessionInvalidator := auth.SessionInvalidatorFunc(func(context.Context, []auth.SessionRevocation) {})
	emailVerification, passwordReset, err := newEmailAuthOptions(
		cfg.Auth.EmailVerification, cfg.Auth.WebAppOrigin,
	)
	if err != nil {
		return err
	}
	authService, err := auth.NewAuthService(auth.ServiceOptions{
		Store:              databaseStore,
		Bus:                bus,
		Passwords:          passwords,
		Tokens:             tokens,
		SessionInvalidator: sessionInvalidator,
		EmailVerification:  emailVerification,
		PasswordReset:      passwordReset,
	})
	if err != nil {
		return fmt.Errorf("construct auth service: %w", err)
	}
	oauthService, err := newGoogleOAuthService(cfg.Auth, databaseStore, tokens)
	if err != nil {
		return err
	}
	desktopHandoffService, err := auth.NewDesktopHandoffService(auth.DesktopHandoffOptions{
		Store:  databaseStore,
		Tokens: tokens,
		TTL:    cfg.Auth.DesktopHandoffCodeTTL,
	})
	if err != nil {
		return fmt.Errorf("construct desktop handoff service: %w", err)
	}

	inboxService, err := inbox.NewService(databaseStore, bus, inboxPresenter)
	if err != nil {
		return fmt.Errorf("construct Inbox service: %w", err)
	}
	companyService, err := company.NewCompanyService(company.ServiceOptions{
		Store: databaseStore,
		Bus:   bus,
	})
	if err != nil {
		return fmt.Errorf("construct Company service: %w", err)
	}
	milestoneService, err := company.NewMilestoneService(databaseStore)
	if err != nil {
		return fmt.Errorf("construct Company milestone service: %w", err)
	}
	referenceService, err := company.NewReferenceService(databaseStore)
	if err != nil {
		return fmt.Errorf("construct Company reference document service: %w", err)
	}
	invitationService, err := newInvitationService(
		databaseStore, bus, cfg,
	)
	if err != nil {
		return err
	}
	companySearchService, err := company.NewSearchService(databaseStore)
	if err != nil {
		return fmt.Errorf("construct Company search service: %w", err)
	}
	proactiveModeService, err := company.NewProactiveModeService(databaseStore)
	if err != nil {
		return fmt.Errorf("construct Proactive Mode service: %w", err)
	}
	agentService, err := agent.NewAgentService(agent.ServiceOptions{Store: databaseStore, Bus: bus})
	if err != nil {
		return fmt.Errorf("construct Agent service: %w", err)
	}
	skillService, err := agent.NewSkillService(
		agent.SkillServiceOptions{Store: databaseStore, Bus: bus, Objects: objectStore},
	)
	if err != nil {
		return fmt.Errorf("construct Skill service: %w", err)
	}
	agentBindingService, err := agent.NewBindingService(agent.BindingServiceOptions{Store: databaseStore, Bus: bus})
	if err != nil {
		return fmt.Errorf("construct Agent binding service: %w", err)
	}
	pluginService, err := agent.NewPluginService(databaseStore, bus)
	if err != nil {
		return fmt.Errorf("construct Plugin service: %w", err)
	}
	var modelDiscoveryStore agent.ModelDiscoveryStore = agent.NewInMemoryModelDiscoveryStore(
		agent.ModelDiscoveryStoreOptions{},
	)
	if redisClient != nil {
		modelDiscoveryStore, err = agent.NewRedisModelDiscoveryStore(
			redisClient,
			agent.ModelDiscoveryStoreOptions{},
		)
		if err != nil {
			return fmt.Errorf("construct shared runtime model discovery store: %w", err)
		}
	}
	runtimeService, err := agent.NewRuntimeService(agent.RuntimeServiceOptions{
		Store:            databaseStore,
		Bus:              bus,
		ModelDiscoveries: modelDiscoveryStore,
		Presenter:        inboxPresenter,
	})
	if err != nil {
		return fmt.Errorf("construct runtime service: %w", err)
	}
	usageService, err := usage.NewUsageService(databaseStore, registry)
	if err != nil {
		return fmt.Errorf("construct usage reporting service: %w", err)
	}
	analyticsWorker, err := usage.NewAnalyticsWorker(databaseStore, inboxPresenter, bus)
	if err != nil {
		return fmt.Errorf("construct analytics worker: %w", err)
	}
	companyResourceService, err := company.NewResourceService(company.ResourceServiceOptions{
		Store: databaseStore, Bus: bus,
	})
	if err != nil {
		return fmt.Errorf("construct Company resource service: %w", err)
	}
	taskService, err := task.NewTaskService(databaseStore, bus, inboxPresenter)
	if err != nil {
		return fmt.Errorf("construct Task service: %w", err)
	}
	taskMessageRuns := conversation.TaskMessageRunPreparerFuncs{
		Check:    task.CheckMessageRunInTx,
		Prepare:  task.PrepareMessageRunInTx,
		NextStep: task.RunNextStepInTx,
	}
	initializationService, err := company.NewInitializationService(company.InitializationServiceOptions{
		Store: databaseStore, Bus: bus, Runtimes: runtimeService, TaskMessageRuns: taskMessageRuns,
	})
	if err != nil {
		return fmt.Errorf("construct initialization service: %w", err)
	}
	taskTerminalEffects := execution.TaskTerminalEffectWriterFunc(task.ApplyRunTerminalInTx)
	taskReportService, err := task.NewReportService(databaseStore, bus)
	if err != nil {
		return fmt.Errorf("construct Task report service: %w", err)
	}
	taskReportDispatcher, err := task.NewReportDispatcher(taskReportService, logger)
	if err != nil {
		return fmt.Errorf("construct Task report dispatcher: %w", err)
	}
	if err := bus.Subscribe(
		task.EventReportReady,
		func(context.Context, events.Event) error {
			taskReportDispatcher.Wake()
			return nil
		},
	); err != nil {
		return fmt.Errorf("subscribe Task report dispatcher wake: %w", err)
	}
	taskMergeService, err := task.NewMergeService(databaseStore, bus)
	if err != nil {
		return fmt.Errorf("construct Task merge service: %w", err)
	}
	taskMergeReconciler, err := task.NewMergeReconciler(databaseStore, logger)
	if err != nil {
		return fmt.Errorf("construct Task merge reconciler: %w", err)
	}
	projectService, err := company.NewProjectService(databaseStore, bus, inboxPresenter)
	if err != nil {
		return fmt.Errorf("construct Project service: %w", err)
	}
	groupService, err := company.NewGroupService(databaseStore, bus, inboxPresenter)
	if err != nil {
		return fmt.Errorf("construct Group service: %w", err)
	}
	wikiService, err := wiki.NewWikiService(databaseStore, bus)
	if err != nil {
		return fmt.Errorf("construct Wiki service: %w", err)
	}
	wikiAgentReadService, err := wiki.NewAgentReadService(databaseStore, bus)
	if err != nil {
		return fmt.Errorf("construct Wiki agent read service: %w", err)
	}
	companyPlanService, err := company.NewPlanService(databaseStore, bus, inboxPresenter)
	if err != nil {
		return fmt.Errorf("construct Work Plan service: %w", err)
	}
	studioEditorService := conversation.NewStudioEditorService(
		databaseStore,
		studioRelay,
		cfg.StudioReceiptNamespace,
	)
	conversationService, err := conversation.NewConversationService(conversation.ServiceOptions{
		Store: databaseStore, Bus: bus, TaskMessageRuns: taskMessageRuns,
	})
	if err != nil {
		return fmt.Errorf("construct Conversation service: %w", err)
	}
	scheduleParts, err := newScheduleComponents(databaseStore, bus, logger, registry, cfg.ScheduleSweepInterval)
	if err != nil {
		return err
	}
	taskCalendarService := task.NewCalendarService(taskService)
	scheduleClient := asynq.NewClientFromRedisClient(redisClient)
	taskCalendarBeat := &task.CalendarBeat{
		Service: taskCalendarService,
		Client:  scheduleClient,
		OnError: func(ctx context.Context, err error) {
			logger.ErrorContext(ctx, "Task calendar job failed", "error", err)
		},
	}
	scheduleBeat := &schedule.Beat{
		Queries: databaseStore.Queries(), Worker: scheduleParts.firingWorker, Client: scheduleClient,
		ScanTasks: taskCalendarBeat.Scan, ExtraHandler: taskCalendarBeat,
		ObserveTask: registry.ObserveScheduleTask, ObserveEnqueue: registry.ObserveScheduleEnqueue,
	}
	scheduleQueue, err := newScheduleQueue(redisClient, scheduleBeat, registry, logger, cfg.ScheduleWorkers)
	if err != nil {
		return err
	}
	attachmentService, err := attachment.NewAttachmentService(attachment.ServiceOptions{
		Store: databaseStore, Bus: bus, Objects: objectStore,
	})
	if err != nil {
		return fmt.Errorf("construct Attachment service: %w", err)
	}
	attachmentCleanup, err := attachment.NewCleanupWorker(
		databaseStore,
		attachmentService,
		attachment.CleanupWorkerOptions{
			AbandonedReadyGrace: cfg.Attachment.AbandonedReadyGrace,
			OnIssue:             newAttachmentCleanupIssueHandler(logger),
		},
	)
	if err != nil {
		return fmt.Errorf("construct Attachment cleanup worker: %w", err)
	}
	deliverableService, err := execution.NewDeliverableService(execution.DeliverableServiceOptions{
		Store: databaseStore,
		Bus:   bus,
	})
	if err != nil {
		return fmt.Errorf("construct Deliverable service: %w", err)
	}
	crashReportService, err := telemetry.NewCrashReportService(telemetry.CrashReportServiceOptions{
		Store: databaseStore,
	})
	if err != nil {
		return fmt.Errorf("construct crash report service: %w", err)
	}
	crashReportRetention, err := telemetry.NewRetentionWorker(
		databaseStore,
		telemetry.RetentionWorkerOptions{
			OnIssue: newCrashReportRetentionIssueHandler(logger),
		},
	)
	if err != nil {
		return fmt.Errorf("construct crash report retention worker: %w", err)
	}
	// The run domain: EmptyClaim short-circuits idle polls when Redis is
	// present; the daemon WS hub receives bump-before-wakeup notifications.
	runService := execution.NewRunService(execution.RunServiceOptions{
		Queries:        queries,
		Transactor:     databaseStore,
		Bus:            bus,
		InboxPresenter: inboxPresenter,
		RouteResolver:  conversation.ResolveConversationRoute,
		ConnectorSnapshotter: execution.ConnectorSnapshotterFunc(func(
			ctx context.Context,
			queries *generated.Queries,
			runID uuid.UUID,
			runtimeID uuid.UUID,
		) (generated.Run, error) {
			run, freezeErr := agent.FreezeRunConnectorSnapshotInTx(
				ctx,
				queries,
				runID,
				runtimeID,
			)
			if errors.Is(freezeErr, agent.ErrConnectorSnapshotUnavailable) {
				return generated.Run{}, execution.ErrRunConnectorSnapshotUnavailable
			}
			return run, freezeErr
		}),
		TaskTerminalEffects: taskTerminalEffects,
		RunObserver:         registry,
	})
	runService.EmptyClaim = execution.NewEmptyClaimCache(redisClient)
	runService.SetRunQueuePromoter(execution.RunQueuePromoterFunc(func(
		ctx context.Context,
		q *generated.Queries,
		pending *[]events.Event,
		run generated.Run,
		output string,
	) (*uuid.UUID, error) {
		promotion, err := conversation.FinalizeRunConversationInTx(
			ctx,
			q,
			pending,
			taskMessageRuns,
			run,
			output,
		)
		return promotion.RunID, err
	}))
	connectorService, err := agent.NewConnectorService(agent.ConnectorServiceOptions{
		Bus:   bus,
		Store: databaseStore,
		RunInvalidator: connectorRunInvalidator{
			queries: queries,
			runs:    runService,
			logger:  logger,
		},
	})
	if err != nil {
		return fmt.Errorf("construct Connector service: %w", err)
	}
	approvalService, err := approval.NewApprovalService(
		databaseStore,
		bus,
		companyPlanService,
		inboxPresenter,
	)
	if err != nil {
		return fmt.Errorf("construct Approval service: %w", err)
	}
	questionService, err := approval.NewQuestionService(databaseStore, bus, inboxPresenter)
	if err != nil {
		return fmt.Errorf("construct Question service: %w", err)
	}
	humanInputLifecycle, err := approval.NewLifecycleWorker(databaseStore, bus, inboxPresenter)
	if err != nil {
		return fmt.Errorf("construct human-input lifecycle worker: %w", err)
	}
	accountService, err := account.NewAccountService(account.ServiceOptions{
		Store:               databaseStore,
		Bus:                 bus,
		SessionInvalidator:  sessionInvalidator,
		TaskMessageRuns:     taskMessageRuns,
		TaskTerminalEffects: taskTerminalEffects,
	})
	if err != nil {
		return fmt.Errorf("construct account service: %w", err)
	}

	socialService, err := social.NewSocialService(databaseStore, cfg.Social, attachmentService)
	if err != nil {
		return fmt.Errorf("construct social service: %w", err)
	}
	calendarService := calendar.NewCalendarService(databaseStore, bus, inboxPresenter)

	// The consumer ledger: every reaction to a published fact registers here,
	// in the order handlers run. The daemon wakeups first, then the browser
	// routing — so in-process reactions finish before frames leave for
	// clients — and the audit log last, because it takes the whole stream and
	// the bus delivers those after every topic's own consumers.
	if err := daemonws.RegisterListeners(bus, runService.EmptyClaim, daemonWakeup); err != nil {
		return fmt.Errorf("register daemon wakeup listeners: %w", err)
	}
	humanInputWorker, err := newHumanInputLifecycleWorker(bus, humanInputLifecycle.Tick)
	if err != nil {
		return err
	}
	discussionDispatcher := newDiscussionDispatcher(queries, broadcaster)
	// message.updated (conversation.TopicMessage) on a channel or human direct
	// message goes to its current members through the same dispatcher.
	for _, topic := range []string{
		conversation.Topic, conversation.TopicMessage, execution.Topic, approval.Topic, approval.TopicQuestion,
	} {
		if err := bus.Subscribe(topic, discussionDispatcher.enqueue); err != nil {
			return fmt.Errorf("register discussion dispatcher: %w", err)
		}
	}
	if err := registerTaskReadiness(bus, queries); err != nil {
		return fmt.Errorf("register Task readiness listeners: %w", err)
	}
	boardChannels, err := conversation.NewBoardChannelService(
		conversation.BoardChannelServiceOptions{Store: databaseStore, Bus: bus},
	)
	if err != nil {
		return fmt.Errorf("create Board chat service: %w", err)
	}
	if err := registerBoardChannelSync(bus, boardChannels); err != nil {
		return fmt.Errorf("register Board chat sync: %w", err)
	}
	canvasPresence := realtime.NewCanvasPresence(redisClient)
	if err := registerRealtimeRouting(bus, broadcaster, whiteboardHintRouting{viewers: canvasPresence, runs: canvasPresence}); err != nil {
		return fmt.Errorf("register realtime routing: %w", err)
	}
	if err := activity.RegisterListeners(bus, queries); err != nil {
		return fmt.Errorf("register activity listeners: %w", err)
	}

	discussionService, err := conversation.NewDiscussionService(
		conversation.DiscussionServiceOptions{
			Store: databaseStore,
			Bus:   bus,
		},
	)
	if err != nil {
		return fmt.Errorf("construct discussion service: %w", err)
	}
	huddleBudgetService, err := usage.NewHuddleBudgetService(databaseStore)
	if err != nil {
		return fmt.Errorf("construct huddle budget service: %w", err)
	}
	huddleMedia := newLiveKitRoomProvider(cfg.LiveKit)
	var huddleDispatcher conversation.HuddleAIDispatcher
	if adapter, ok := huddleMedia.(liveKitRoomProviderAdapter); ok {
		huddleDispatcher = adapter.provider
	}
	huddleAIService, err := conversation.NewHuddleAIService(conversation.HuddleAIServiceOptions{
		Bus:         bus,
		ReadContext: readHuddleContext,
		Store:       databaseStore, Dispatcher: huddleDispatcher, Enabled: cfg.LiveKit.AIEnabled,
		TicksPerSecond: 8333334, ReasoningReserveTicks: 10000000000,
		InputTokenTicks: 2000, CachedInputTokenTicks: 200, OutputTokenTicks: 12000,
	})
	if err != nil {
		return fmt.Errorf("construct huddle AI service: %w", err)
	}
	huddleService, err := conversation.NewHuddleService(conversation.HuddleServiceOptions{
		Store:     databaseStore,
		Bus:       bus,
		Media:     huddleMedia,
		Presenter: inbox.NewRealtimePresenter(bus),
	})
	if err != nil {
		return fmt.Errorf("construct Huddle service: %w", err)
	}
	var studioReadiness db.Pinger
	if distributedStudio != nil {
		studioReadiness = distributedStudio
	}
	ready, err := db.NewReadiness(pool, studioReadiness)
	if err != nil {
		return fmt.Errorf("construct readiness checker: %w", err)
	}
	csrf, err := auth.NewCSRF([]byte(cfg.CSRFSecret))
	if err != nil {
		return fmt.Errorf("construct CSRF protection: %w", err)
	}
	realtime.SetAllowedOrigins(cfg.WebOrigins)
	realtime.SetTrustedProxies(cfg.TrustedProxies)
	boardService := canvas.NewBoardService(databaseStore, bus)
	httpHandler := handler.New(handler.Handler{
		LiveKitAPIKey:         cfg.LiveKit.APIKey,
		LiveKitAPISecret:      cfg.LiveKit.APISecret,
		Readiness:             ready,
		Logger:                logger,
		AllowedOrigins:        cfg.WebOrigins,
		TrustedProxies:        cfg.TrustedProxies,
		CSRF:                  csrf,
		AuthService:           authService,
		AccountService:        accountService,
		DesktopHandoffService: desktopHandoffService,
		GoogleOAuthService:    oauthService,
		OAuthWebAppOrigin:     cfg.Auth.WebAppOrigin,
		AuthCookies:           handler.CookieOptions{Secure: cfg.Auth.CookieSecure},
		MetricsHandler:        registry.Handler(),
		RealtimeMetricsToken:  cfg.RealtimeMetricsToken,
		RequireDaemonID:       cfg.DaemonIDRequired,
		StudioAPIOrigin:       cfg.StudioAPIOrigin,
		HTTPMetrics:           registry,
		ClientWebSocket:       newClientWebSocket(hub, authService, companyService),
		CompanyService:        companyService,
		InitializationService: initializationService,
		MilestoneService:      milestoneService,
		ReferenceService:      referenceService,
		InvitationService:     invitationService,
		InvitationEmailEnabled: cfg.Auth.EmailVerification.Enabled &&
			cfg.Auth.EmailVerification.InvitationTemplateID != "",
		ProactiveModeService:   proactiveModeService,
		CompanySearchService:   companySearchService,
		AgentService:           agentService,
		SkillService:           skillService,
		AgentBindingService:    agentBindingService,
		RuntimeService:         runtimeService,
		ConnectorService:       connectorService,
		PluginService:          pluginService,
		UsageService:           usageService,
		AnalyticsService:       usageService,
		CompanyResourceService: companyResourceService,
		TaskService:            taskService,
		TaskCalendarService:    taskCalendarService,
		ProjectService:         projectService,
		GroupService:           groupService,
		CanvasService: canvas.NewCanvasService(
			databaseStore,
			bus,
			attachmentService,
			canvas.WithDocumentAgentInvoker(conversation.InvokeDocumentAgentInTx),
		),
		BoardService:        boardService,
		BoardMediaService:   canvas.NewBoardMediaService(databaseStore, attachmentService),
		BoardChannelService: boardChannels,
		EphemeralRelay:      ephemeralRelay,
		PageService:         canvas.NewPageService(databaseStore, bus),
		PresentationService: canvas.NewPresentationService(
			databaseStore,
			bus,
			attachmentService,
			func(key string) (json.RawMessage, error) {
				definition, err := workflow.Get(key)
				if err != nil {
					return nil, err
				}
				return json.Marshal(definition)
			},
		),
		CanvasPresence:           canvasPresence,
		WikiService:              wikiService,
		WikiAgentReadService:     wikiAgentReadService,
		CompanyPlanService:       companyPlanService,
		ApprovalService:          approvalService,
		QuestionService:          questionService,
		RunProofResolver:         runService,
		ConversationService:      conversationService,
		HumanConversationService: conversationService,
		DiscussionService:        discussionService,
		ChannelInvocationService: conversation.NewChannelInvocationService(databaseStore, bus),
		HuddleService:            huddleService,
		HuddleBudgetService:      huddleBudgetService,
		HuddleAIService:          huddleAIService,
		HuddleAIWorkloadSecret:   cfg.LiveKit.AIWorkloadSecret,
		StudioProjectService:     conversation.NewStudioProjectService(databaseStore),
		StudioEditorService:      studioEditorService,
		MessageContextService:    conversationService,
		ActivityFeed:             activity.NewFeed(queries),
		InboxService:             inboxService,
		AttachmentService:        attachmentService,
		DeliverableService:       deliverableService,
		PublicationService:       publicationService,
		MailboxService:           mailboxParts.service,
		MailboxDraftService:      mailboxParts.draftService,
		MailboxWebhookVerifier:   mailboxParts.verifier,
		MailboxWebhookObserver:   registry,
		ScheduleService:          scheduleParts.service,
		TaskReportService:        taskReportService,
		TaskMergeService:         taskMergeService,
		CommerceService:          handlerCommerceService(commerceService),
		CrashReportService:       crashReportService,
		RunService:               runService,
		Queries:                  queries,
		Bus:                      bus,
		DaemonWSHub:              daemonHub,
		CalendarService:          calendarService,
		SocialService:            socialService,
	})
	// The daemon WS dispatcher calls the transport-free adapters directly:
	// runs.claim runs the same core as the HTTP batch-claim handler, completed
	// transcript items use the direct domain adapter, and WS heartbeats run
	// the same core as the HTTP heartbeat handler.
	dispatcher := daemonws.NewRPCDispatcher(httpHandler.DaemonWSClaimRuns)
	dispatcher.SetRunTranscriptAppendHandler(httpHandler.DaemonWSRunTranscriptAppend)
	daemonHub.SetRPCHandler(dispatcher.Dispatch)
	daemonHub.SetHeartbeatHandler(httpHandler.DaemonWSHeartbeat)

	server := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: httpHandler,
		// Bound header reads so a slow-loris dial cannot pin a connection;
		// everything else keeps net/http defaults.
		ReadHeaderTimeout: 5 * time.Second,
	}

	// The background ledger: supervised work, including timer and event wakeups.
	// The harness owns shutdown and the "it stopped early" check.
	calendarReminders := calendar.NewReminderService(databaseStore, bus, inbox.NewRealtimePresenter(bus))
	workers := []backgroundWorker{
		repeating("company-initialization", 5*time.Second, func(ctx context.Context) {
			passCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			if err := initializationService.Reconcile(passCtx); err != nil {
				logger.ErrorContext(ctx, "company initialization reconciliation failed", "error", err)
			}
		}),
		repeating("whiteboard-retention", 15*time.Minute, func(ctx context.Context) {
			if _, err := boardService.Sweep(ctx, time.Now()); err != nil {
				logger.ErrorContext(ctx, "whiteboard retention failed", "error", err)
			}
		}),
		repeating("skill-import-cleanup", 15*time.Minute, func(ctx context.Context) {
			passCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			if err := skillService.Cleanup(passCtx); err != nil {
				logger.Warn("clean up skill imports", "error", err)
			}
		}),
		repeating("analytics-maintenance", 15*time.Minute, func(ctx context.Context) {
			if err := analyticsWorker.Tick(ctx); err != nil {
				logger.ErrorContext(ctx, "analytics maintenance failed", "error", err)
			}
		}),
		repeating("social-credentials", time.Minute, func(ctx context.Context) {
			if err := socialService.MaintainCredentials(ctx); err != nil {
				logger.ErrorContext(ctx, "social credential maintenance failed", "error", err)
			}
		}),
		repeating("calendar-reminders", 5*time.Second, func(ctx context.Context) {
			if err := calendarReminders.Deliver(ctx, time.Now().UTC()); err != nil {
				logger.ErrorContext(ctx, "calendar reminder delivery failed", "error", err)
			}
		}),
		{name: "discussion-dispatch", run: discussionDispatcher.run},
		repeating("huddle-lifecycle", huddleLifecycleInterval, func(ctx context.Context) {
			if err := huddleAIService.Reconcile(ctx); err != nil {
				logger.ErrorContext(ctx, "huddle AI reconciliation failed", "error", err)
			}
			if _, err := huddleService.ReconcileMediaRooms(ctx); err != nil {
				logger.ErrorContext(ctx, "huddle media reconciliation failed", "error", err)
			}
			if _, err := huddleService.SweepExpiredReservations(ctx, time.Now().UTC()); err != nil {
				logger.ErrorContext(ctx, "huddle reservation sweep failed", "error", err)
			}
			if _, err := huddleService.SweepExpiredInvitations(ctx, time.Now().UTC()); err != nil {
				logger.ErrorContext(ctx, "huddle invitation sweep failed", "error", err)
			}
			if _, err := huddleService.DeliverMissedInvitations(ctx); err != nil {
				logger.ErrorContext(ctx, "missed huddle delivery failed", "error", err)
			}
		}),
		repeating("invitation-delivery", 5*time.Second, func(ctx context.Context) {
			if err := invitationService.Deliver(ctx); err != nil {
				logger.ErrorContext(ctx, "invitation delivery pass failed", "error", err)
			}
		}),
		settling("runtime-sweeper", sweepInterval, func(ctx context.Context) {
			sweepOnce(ctx, queries, runService, runtimeService)
		}),
		repeating("attachment-cleanup", attachmentCleanupInterval, attachmentCleanup.Tick),
		repeating("crash-report-retention", crashRetentionInterval, crashReportRetention.Tick),
		humanInputWorker,
		{name: "schedule-sweep", run: scheduleParts.firingWorker.Run},
		{name: "schedule-queue", run: scheduleQueue.Run},
		repeating("schedule-lag-probe", 15*time.Second, func(ctx context.Context) {
			probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			lag, err := databaseStore.Queries().GetScheduleLag(probeCtx)
			registry.ObserveScheduleProbe(lag.LagSeconds, lag.DueCount, lag.OverdueCount, err)
			if err != nil {
				logger.Warn("Schedule lag probe failed", "component", "schedule", "error", err)
			}
			scheduleQueue.probe(probeCtx)
		}),
		{name: "task-report-dispatch", run: taskReportDispatcher.Run},
		{name: "task-merge-reconcile", run: taskMergeReconciler.Run},
	}
	if commerceWebhookReconciler != nil {
		workers = append(workers, backgroundWorker{
			name: "commerce-webhook-reconcile",
			run:  commerceWebhookReconciler.Run,
		})
	}
	if commerceDevelopmentEventPoller != nil {
		workers = append(workers, backgroundWorker{
			name: "commerce-development-event-fallback",
			run:  commerceDevelopmentEventPoller.Run,
		})
	}
	if publicationWorker != nil {
		workers = append(workers, backgroundWorker{name: "output-publication", run: publicationWorker.Run})
	}
	if mailboxParts.retentionWorker != nil {
		workers = append(workers, backgroundWorker{
			name: "mailbox-retention",
			run:  mailboxParts.retentionWorker.Run,
		})
	}
	if mailboxParts.draftWorker != nil {
		workers = append(workers, backgroundWorker{
			name: "agent-mail-send",
			run:  mailboxParts.draftWorker.Run,
		})
	}
	if distributedStudio != nil {
		workers = append(workers, backgroundWorker{name: "studio-relay", run: distributedStudio.Run})
	}
	if relay != nil {
		workers = append(
			workers,
			backgroundWorker{name: "realtime-relay", run: func(ctx context.Context) error {
				relay.Start(ctx)
				<-ctx.Done()
				relay.Stop()
				relay.Wait()
				return nil
			}},
		)
	}
	if err := serveApplication(ctx, server, ready, cfg.ShutdownTimeout, workers...); err != nil {
		return err
	}
	logger.Info("Zenith Team server stopped")
	return nil
}

// newClientWebSocket composes the client realtime socket: cookie/bearer auth,
// company_id membership authorization, the origin-checked upgrade, then the
// hub takes the receive-only connection. The hub derives fixed company and
// user identity rooms from this authorization; browser frames cannot add
// resource subscriptions.
func newClientWebSocket(
	hub *realtime.Hub,
	authService *auth.DefaultAuthService,
	companyService *company.DefaultCompanyService,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := ""
		kind := auth.TokenKindBearer
		if cookie, err := r.Cookie("boardly_access"); err == nil {
			token = cookie.Value
			kind = auth.TokenKindSession
		} else if header := r.Header.Get("Authorization"); len(header) > 7 && header[:7] == "Bearer " {
			token = header[7:]
		}
		if token == "" {
			http.Error(w, "authentication is required", http.StatusUnauthorized)
			return
		}
		principal, err := authService.Authenticate(r.Context(), token, kind)
		if err != nil {
			http.Error(w, "authentication is required", http.StatusUnauthorized)
			return
		}
		// The client socket is a person's: a daemon's own session never
		// opens it (D31).
		if principal.DaemonSession() {
			http.Error(w, "a daemon session cannot open the client socket", http.StatusForbidden)
			return
		}
		companyID := r.URL.Query().Get("company_id")
		if companyID == "" {
			http.Error(w, "company_id is required", http.StatusUnprocessableEntity)
			return
		}
		parsed, err := util.ParseGoUUID(companyID)
		if err != nil {
			http.Error(w, "company_id is not a valid uuid", http.StatusUnprocessableEntity)
			return
		}
		if _, err := companyService.Authorize(r.Context(), principal, parsed); err != nil {
			http.Error(w, "company access denied", http.StatusNotFound)
			return
		}
		conn, err := realtime.Upgrade(w, r)
		if err != nil {
			return
		}
		hub.ServeClient(conn, principal.UserID.String(), companyID, func() bool {
			_, err := companyService.Authorize(r.Context(), principal, parsed)
			return err == nil
		})
	})
}
