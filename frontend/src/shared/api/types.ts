import type {
  components as ApiComponents,
  operations as ApiOperations,
} from "@/shared/api/generated/api.generated";

export type JsonObject = Record<string, unknown>;

export type ActorRefDto = ApiComponents["schemas"]["ActorRef"];

export type ApprovalDto = ApiComponents["schemas"]["Approval"];
export type ApprovalStatusDto = ApiComponents["schemas"]["ApprovalStatus"];
export type ApprovalPage =
  ApiOperations["listApprovals"]["responses"][200]["content"]["application/json"];
export type ApprovalStats =
  ApiOperations["getApprovalStats"]["responses"][200]["content"]["application/json"];
export type ApprovalListQuery = NonNullable<
  ApiOperations["listApprovals"]["parameters"]["query"]
>;
export type ApprovalStatsQuery = NonNullable<
  ApiOperations["getApprovalStats"]["parameters"]["query"]
>;
export type ApprovalResolveRequest =
  ApiOperations["approveApproval"]["requestBody"]["content"]["application/json"];
export type ApprovalCancelRequest =
  ApiOperations["cancelApproval"]["requestBody"]["content"]["application/json"];

export type QuestionDto = ApiComponents["schemas"]["Question"];
export type QuestionStatusDto = ApiComponents["schemas"]["QuestionStatus"];
export type QuestionPromptDto = ApiComponents["schemas"]["QuestionPrompt"];
export type QuestionAnswerDto = ApiComponents["schemas"]["QuestionAnswer"];
export type QuestionPage =
  ApiOperations["listQuestions"]["responses"][200]["content"]["application/json"];
export type QuestionListQuery = NonNullable<
  ApiOperations["listQuestions"]["parameters"]["query"]
>;
export type QuestionAnswerRequest =
  ApiOperations["answerQuestion"]["requestBody"]["content"]["application/json"];
export type QuestionWithdrawRequest =
  ApiOperations["withdrawQuestion"]["requestBody"]["content"]["application/json"];

export type TokenType = "Bearer";
export type OAuthProvider = "google";
export type CommonStatus = "active" | "inactive" | "archived";
export type CompanyPlanMode = "light" | "full";
/** The root Agent (`reportsTo === null`) is the CEO; every other Agent reports to one. */
export type AgentRole = "ceo" | "agent";
export type ConversationStatus = "open" | "archived";
export type WorkStatus =
  | "draft"
  | "active"
  | "pending"
  | "completed"
  | "blocked"
  | "failed"
  | "canceled";
export type Priority = ApiComponents["schemas"]["TaskPriority"];
export type ApprovalStatus = "pending" | "approved" | "rejected" | "canceled";
export type ApprovalDecisionStatus = Extract<
  ApprovalStatusDto,
  "approved" | "rejected"
>;
export type LibraryItemKind =
  "instructions" | "skill" | "template" | "file" | "image" | "plugin" | "tool";
export type LibraryItemStatus = "active" | "pending" | "archived";
export type LibraryItemSourceType =
  "user_upload" | "generated_image" | "tool_output" | "system";
export type LibraryItemPreviewKind =
  | "image"
  | "pdf"
  | "text"
  | "markdown"
  | "code"
  | "spreadsheet"
  | "document"
  | "presentation"
  | "audio"
  | "video"
  | "archive"
  | "unknown";
export type ResourcePreviewStatus =
  "ready" | "pending" | "failed" | "unsupported";
export type SkillEnablementStatus = "enabled" | "disabled";

export type PasswordLoginRequest = {
  email: string;
  password: string;
};

export type PasswordSignupRequest = PasswordLoginRequest & {
  displayName: string;
};

export type EmailOtpVerificationRequest = Omit<
  ApiOperations["verifyEmail"]["requestBody"]["content"]["application/json"],
  "client"
>;
export type EmailOtpResendRequest = Omit<
  ApiOperations["resendEmailVerification"]["requestBody"]["content"]["application/json"],
  "client"
>;
export type PendingEmailVerificationResponse =
  ApiComponents["schemas"]["PendingEmailVerification"];
export type EmailOtpResendResponse =
  ApiOperations["resendEmailVerification"]["responses"][200]["content"]["application/json"];
export type PendingVerificationStatusRequest = Omit<
  ApiOperations["getEmailVerificationStatus"]["requestBody"]["content"]["application/json"],
  "client"
>;
export type PendingVerificationStatusResponse =
  ApiOperations["getEmailVerificationStatus"]["responses"][200]["content"]["application/json"];

export type ForgotPasswordRequest =
  ApiOperations["requestPasswordReset"]["requestBody"]["content"]["application/json"];
export type ForgotPasswordResponse =
  ApiOperations["requestPasswordReset"]["responses"][202]["content"]["application/json"];
export type ResetPasswordRequest =
  ApiOperations["confirmPasswordReset"]["requestBody"]["content"]["application/json"];
export type PasswordResetSuccessResponse =
  ApiOperations["confirmPasswordReset"]["responses"][200]["content"]["application/json"];

export type AuthErrorCode =
  | "UNAUTHENTICATED"
  | "INVALID_CREDENTIALS"
  | "EMAIL_NOT_VERIFIED"
  | "EMAIL_DELIVERY_UNAVAILABLE"
  | "VERIFICATION_CODE_INVALID"
  | "VERIFICATION_CODE_EXPIRED"
  | "VERIFICATION_CODE_LOCKED"
  | "RESET_TOKEN_INVALID"
  | "ACCOUNT_DELETION_DISABLED"
  | "ACCOUNT_DELETION_FORBIDDEN"
  | "TENANT_NOT_FOUND"
  | "MEMBERSHIP_INACTIVE"
  | "TENANT_ACCESS_DENIED";

export type AuthUserResponse = ApiComponents["schemas"]["User"];
export type AuthResult = ApiComponents["schemas"]["AuthResult"];
export type AuthSuccessResponse = ApiComponents["schemas"]["AuthSuccess"];
export type SessionRotation = ApiComponents["schemas"]["SessionRotation"];
export type CookieAuthSessionResponse = Extract<
  ApiComponents["schemas"]["Session"],
  { transport: "cookie" }
>;
export type BearerAuthSessionResponse = Extract<
  ApiComponents["schemas"]["Session"],
  { transport: "bearer" }
>;

export type AuthSessionResponse = {
  user: AuthUserResponse;
};

export type MeProfileRequest =
  ApiOperations["updateMyProfile"]["requestBody"]["content"]["application/json"];
export type MeProfileResponse =
  ApiOperations["updateMyProfile"]["responses"][200]["content"]["application/json"];

export type LoginResponse = AuthSuccessResponse;

export type CompanyDetail = ApiComponents["schemas"]["Company"];
export type CompanySettingsProfileRequest =
  ApiOperations["updateCompanyProfile"]["requestBody"]["content"]["application/json"];
export type CompanySettingsProfileResponse =
  ApiOperations["updateCompanyProfile"]["responses"][200]["content"]["application/json"];
export type CompanyPage =
  ApiOperations["listCompanies"]["responses"][200]["content"]["application/json"];
export type ProactiveModeDto = ApiComponents["schemas"]["ProactiveMode"];
export type UpdateProactiveModeRequest =
  ApiOperations["updateProactiveMode"]["requestBody"]["content"]["application/json"];

export type CompanyBootstrap =
  ApiOperations["getCompanyBootstrap"]["responses"][200]["content"]["application/json"];

export type CompanyOnboardingRequest =
  ApiOperations["createCompany"]["requestBody"]["content"]["application/json"];

export type CompanyOnboardingResult = {
  company: CompanyDetail;
};

export type CompanyStatistics = {
  memberCount: number;
  runtimeCount: number;
  runtimeOnlineCount: number;
};

export type AgentProvider = ApiComponents["schemas"]["Runtime"]["provider"];
export type AgentDto = ApiComponents["schemas"]["Agent"];
export type AgentUnavailableReason = AgentDto["unavailableReason"];
export type AgentBindingDto = ApiComponents["schemas"]["AgentBinding"];
export type AgentBindingRequest =
  ApiOperations["bindAgent"]["requestBody"]["content"]["application/json"];
export type AgentBindingPage =
  ApiOperations["listMyAgentBindings"]["responses"][200]["content"]["application/json"];
export type AgentBindingChangedPayload =
  ApiComponents["schemas"]["AgentBindingChangedPayload"];
export type RuntimeRefDto = ApiComponents["schemas"]["RuntimeRef"];
export type AgentPage =
  ApiOperations["listAgents"]["responses"][200]["content"]["application/json"];
export type AgentRuntimeUpdateRequest =
  ApiOperations["updateAgent"]["requestBody"]["content"]["application/json"];
export type AgentCreateRequest =
  ApiOperations["createAgent"]["requestBody"]["content"]["application/json"];
export type AgentUpdateRequest = AgentRuntimeUpdateRequest;

export type EntityRevisionRequest = {
  expectedRevision: number;
};

export type RuntimeDto = ApiComponents["schemas"]["Runtime"];
export type RuntimeModel = ApiComponents["schemas"]["RuntimeModel"];
export type RuntimeModelDiscoveryRequest =
  ApiComponents["schemas"]["ModelDiscoveryRequest"];
export type RuntimeListResponse =
  ApiOperations["listRuntimes"]["responses"][200]["content"]["application/json"];
export type RuntimeRenameRequest =
  ApiOperations["renameRuntime"]["requestBody"]["content"]["application/json"];
export type RuntimeRemovalRequest =
  ApiOperations["removeRuntime"]["requestBody"]["content"]["application/json"];
export type RuntimeRemovalResponse =
  ApiOperations["removeRuntime"]["responses"][200]["content"]["application/json"];
export type RuntimeAccess = ApiComponents["schemas"]["RuntimeAccess"];

export type SignupResponse = AuthResult;

export type AuthProviderCapabilitiesResponse =
  ApiOperations["listAuthProviders"]["responses"][200]["content"]["application/json"];

export type GoogleWebStartResponse = {
  authorizationUrl: string;
  expiresAt: string;
};

export type GoogleDesktopStartRequest = {
  redirectUri: string;
  codeChallenge: string;
  clientMetadata: {
    appVersion?: string;
    platform?: string;
  };
};

export type GoogleDesktopStartResponse = {
  flowId: string;
  state: string;
  authorizationUrl: string;
  expiresAt: string;
};

export type DesktopBearerSessionResponse = Omit<
  BearerAuthSessionResponse,
  "refreshToken"
>;

export type DesktopLoginResponse = Omit<AuthSuccessResponse, "session"> & {
  session: DesktopBearerSessionResponse;
};

export type DesktopSessionRotation = Omit<SessionRotation, "session"> & {
  session: DesktopBearerSessionResponse;
};

export type ClientLoginResponse = LoginResponse | DesktopLoginResponse;

export type PasswordAuthResponse = SignupResponse | DesktopPasswordAuthResponse;

export type DesktopPasswordAuthResponse =
  DesktopLoginResponse | PendingEmailVerificationResponse;

export type ResetPasswordResponse = PasswordResetSuccessResponse;

// Usage reports are the generated contract; nothing here re-spells a field.
export type UsageTokens = ApiComponents["schemas"]["Tokens"];
export type CostAttribution = ApiComponents["schemas"]["CostAttribution"];
export type UsageModelSlice = ApiComponents["schemas"]["UsageModelSlice"];
export type AgentUsageReport = ApiComponents["schemas"]["AgentUsageReport"];
export type CompanyUsagePeriod = NonNullable<
  ApiOperations["getCompanyUsage"]["parameters"]["query"]
>["period"];
export type CompanyUsageTotals = ApiComponents["schemas"]["CompanyTotals"];
export type CompanyUsageReport = ApiComponents["schemas"]["CompanyUsageReport"];
export type AnalyticsSummary = ApiComponents["schemas"]["AnalyticsSummary"];
export type AnalyticsSeries = ApiComponents["schemas"]["AnalyticsSeries"];
export type AnalyticsBreakdown = ApiComponents["schemas"]["AnalyticsBreakdown"];
export type AnalyticsFailures = ApiComponents["schemas"]["AnalyticsFailures"];
export type AnalyticsTaskUsage = ApiComponents["schemas"]["AnalyticsTaskUsage"];
export type AnalyticsBudget = ApiComponents["schemas"]["AnalyticsBudget"];
export type AnalyticsBudgetPage =
  ApiComponents["schemas"]["AnalyticsBudgetPage"];
export type AnalyticsBudgetInput =
  ApiComponents["schemas"]["AnalyticsBudgetInput"];
export type AnalyticsHeatmap = ApiComponents["schemas"]["AnalyticsHeatmap"];

export type ActivityInsight = ApiComponents["schemas"]["ActivityInsight"];

export type AgentSummary = {
  id: string;
  agent_role: AgentRole;
  display_name: string;
  profile: string;
  status: CommonStatus;
};

export type CreditAccountSummary = {
  balance: number;
  version: number;
};

export type CockpitCounters = {
  active_tasks: number;
  pending_approvals: number;
  open_risks: number;
};

export type ConversationSummary = {
  conversation_id: string;
  title: string | null;
  status: ConversationStatus;
  subject_type: string | null;
  subject_id: string | null;
  created_at: string;
  updated_at: string;
};

export type CockpitResponse = {
  company_id: number | string;
  ceo_agent: AgentSummary;
  credits: CreditAccountSummary;
  counters: CockpitCounters;
  default_conversation: ConversationSummary;
};

export type StartConversationRequest = {
  target_agent_id?: string | null;
  subject_type?: string | null;
  subject_id?: string | null;
  title?: string | null;
};

export type PostConversationMessageRequest = {
  content: string;
  attachments?: ResourceAttachmentSnapshot[];
  client_message_id?: string | null;
};

export type DirectConversationDto = ApiComponents["schemas"]["Conversation"];
export type DirectConversationListItem =
  ApiComponents["schemas"]["ConversationListItem"];
export type DirectConversationDetail =
  ApiComponents["schemas"]["ConversationDetail"];
export type DirectConversationPage =
  ApiOperations["listDirectConversations"]["responses"][200]["content"]["application/json"];
export type CreateDirectConversationRequest =
  ApiOperations["createDirectConversation"]["requestBody"]["content"]["application/json"];
export type RenameDirectConversationRequest =
  ApiOperations["renameDirectConversation"]["requestBody"]["content"]["application/json"];

export type SendConversationMessageRequest =
  ApiOperations["sendConversationMessage"]["requestBody"]["content"]["application/json"];

export type ConversationMessageDto = ApiComponents["schemas"]["Message"];
export type ConversationTranscriptEntryDto =
  ApiComponents["schemas"]["RunTranscriptEntry"];
export type ConversationTranscriptPageDto =
  ApiOperations["getConversationTranscript"]["responses"][200]["content"]["application/json"];

export type ConversationTranscriptListOptions = {
  afterSeq?: number;
  limit?: number;
};

export type ConversationMessageView =
  ApiComponents["schemas"]["MessageWithAttachments"];
export type ConversationMessagePage =
  ApiOperations["listConversationMessages"]["responses"][200]["content"]["application/json"];
export type ConversationMessageContextPage =
  ApiOperations["getMessageContext"]["responses"][200]["content"]["application/json"];

export type ConversationMessageListOptions = {
  afterSeq?: number;
  limit?: number;
  throughSeq?: number;
};

export type ConversationHistoryWindow = {
  earliestAvailableSeq: number;
  earliestLoadedSeq: number | null;
  hasOlder: boolean;
  items: ConversationDisplayItemSummary[];
  throughSeq: number;
};

export type SendConversationMessageResponse =
  ApiComponents["schemas"]["SendMessageResult"];
export type ConversationRunQueueItem = ApiComponents["schemas"]["RunQueueItem"];
export type ConversationRunQueueProjection =
  ApiComponents["schemas"]["RunQueueProjection"];
export type ConversationCurrentRun = NonNullable<
  ConversationRunQueueProjection["currentRun"]
>;

export type ConversationRunQueueOrderRequest = {
  items: Array<{ id: string; expectedRevision: number }>;
};

export type ConversationStateDto =
  ApiComponents["schemas"]["ConversationUserState"];
export type ConversationStateUpdateRequest =
  ApiOperations["updateConversationState"]["requestBody"]["content"]["application/json"];

export type TaskStatusDto = ApiComponents["schemas"]["TaskStatus"];
export type TaskPriorityDto = ApiComponents["schemas"]["TaskPriority"];
export type TaskMilestoneTypeDto =
  ApiComponents["schemas"]["TaskMilestoneTypeDto"];
export type TaskDto = ApiComponents["schemas"]["Task"];
export type TaskExecutionDto = ApiComponents["schemas"]["TaskExecution"];
export type TaskExecutorRef = ApiComponents["schemas"]["ExecutorRef"];
export type TaskAssigneeRef = ApiComponents["schemas"]["AssigneeRef"];
export type TaskPageDto =
  ApiOperations["listTasks"]["responses"][200]["content"]["application/json"];
export type TaskDependencyDto = ApiComponents["schemas"]["TaskDependency"];
export type TaskDependencySetDto = ApiComponents["schemas"]["DependencySet"];
export type TaskDependencyGraphTaskDto =
  ApiComponents["schemas"]["DependencyGraphTask"];
export type TaskDependencyGraphDto =
  ApiComponents["schemas"]["DependencyGraph"];
export type TaskListOptions = NonNullable<
  ApiOperations["listTasks"]["parameters"]["query"]
>;
export type TaskCreateRequest =
  ApiOperations["createTask"]["requestBody"]["content"]["application/json"];
export type TaskUpdateRequest =
  ApiOperations["updateTask"]["requestBody"]["content"]["application/json"];
export type TaskUpdateResponse = ApiComponents["schemas"]["TaskMutationResult"];
export type TaskHandoffConfirmationErrorDto =
  ApiComponents["schemas"]["TaskHandoffConfirmationError"];
export type TaskStartExecutionRequest =
  ApiOperations["startTaskExecution"]["requestBody"]["content"]["application/json"];
export type TaskStartExecutionResponse =
  ApiComponents["schemas"]["TaskMutationResult"];
export type TaskReportEventDto = ApiComponents["schemas"]["TaskReportEventDto"];
export type TaskReportPreviewDto =
  ApiComponents["schemas"]["TaskReportPreviewDto"];
export type SendTaskReportsRequest =
  ApiComponents["schemas"]["SendTaskReportsRequest"];
export type SendTaskReportsResponse =
  ApiComponents["schemas"]["SendTaskReportsResponse"];

export type RunStatusDto = ApiComponents["schemas"]["RunStatus"];
export type RunDto = ApiComponents["schemas"]["Run"];
export type RunPageDto =
  ApiOperations["listRuns"]["responses"][200]["content"]["application/json"];

export type ConversationEventName = string;
export type EventSource =
  "agent" | "system" | "user" | "service" | "integration";

export type ConversationMessageReceipt = {
  event_id: string;
  conversation_id: string;
  sequence: number;
  event_name: ConversationEventName;
  source: EventSource;
  created_at: string;
};

export type ConversationDisplayRole =
  "user" | "assistant" | "system" | "app" | "status" | "tool";

export type ConversationDisplayItemSummary = {
  skillReferences?: ApiComponents["schemas"]["MessageSkillReference"][];
  connectorReferences?: ApiComponents["schemas"]["MessageConnectorReference"][];
  created_at: string;
  event_id: string;
  sequence: number;
  turn_id?: string | null;
  item_id: string | null;
  role: ConversationDisplayRole;
  event_name: ConversationEventName;
  content: string;
  payload: JsonObject;
};

export type TaskSummary = {
  id: number;
  task_id: string;
  key: string;
  owner_agent_id: string | null;
  status: WorkStatus;
  task_status: TaskStatusDto;
  priority: Priority;
  version: number;
  title: string;
  description: string | null;
  source_refs: JsonObject;
  idempotency_key: string | null;
  created_at: string;
  updated_at: string;
};

export type ApprovalSummary = {
  id: number;
  request_id: string;
  agent_id: string | null;
  status: ApprovalStatus;
  request_type: string;
  request_kind?: string;
  title?: string;
  description?: string;
  payload: JsonObject;
  source_refs?: Array<{
    ref: string;
    scope: string;
    summary: string;
  }>;
  decided_by_user_id: number | null;
  created_at: string;
  decided_at: string | null;
};

export type ApprovalDecisionPlanTask = {
  title: string;
  status?: string | null;
  effort_points?: number | null;
  success_metric?: string | null;
  task_agent_capability?: string | null;
};

export type ApprovalDecisionPlanStep = {
  title: string;
  description?: string | null;
  status?: string | null;
  priority?: Priority | null;
  labels?: string[] | null;
  effort_points?: number | null;
  due_date?: string | null;
  assigned_agent_id?: string | null;
  owner_agent_mode?: string | null;
  task_agent_capability?: string | null;
  depends_on_step_indices?: number[] | null;
  task_items?: ApprovalDecisionPlanTask[] | null;
};

export type ApprovalDecisionPlan = {
  title: string;
  steps: ApprovalDecisionPlanStep[];
};

export type ApprovalDecisionRequest = {
  status: ApprovalDecisionStatus;
  decision_note?: string | null;
  reviewed_plan?: ApprovalDecisionPlan | null;
};

export type ResourceSummary = {
  id: number | string;
  resource_id: string;
  agent_id: string | null;
  kind: LibraryItemKind;
  status: LibraryItemStatus;
  title: string;
  description: string | null;
  content: JsonObject;
  file_type: string | null;
  file_size: string | null;
  mime_type: string | null;
  size_bytes: number | null;
  source_type: LibraryItemSourceType;
  source_conversation_id: string | null;
  source_message_id: string | null;
  source_tool_call_id: string | null;
  preview_kind: LibraryItemPreviewKind;
  preview_status: ResourcePreviewStatus;
  metadata: JsonObject;
  content_url: string | null;
  preview_url: string | null;
  thumbnail_url: string | null;
  version: number;
  created_at: string;
  updated_at: string;
};

export type ResourcePreviewResponse = {
  resource_id: string;
  title: string;
  mime_type: string | null;
  size_bytes: number | null;
  preview_kind: LibraryItemPreviewKind;
  preview_status: ResourcePreviewStatus;
  content_url: string | null;
  thumbnail_url: string | null;
  metadata: JsonObject;
};

export type ResourceUploadResponse = {
  resources: ResourceSummary[];
  attachments?: ResourceAttachmentSnapshot[];
};

export type ResourceAttachmentSnapshot = {
  id: string;
  file_name: string;
  file_size: number | null;
  content_type: string | null;
  storage_path?: string | null;
  url?: string | null;
  asset_type?:
    "image" | "video" | "audio" | "document" | "code" | "design" | "other";
  source?: "user_upload" | "generated" | "system";
  is_public?: boolean;
  sandbox_path?: string | null;
  created_at?: string | null;
};

export type ResourceVersionSummary = {
  id: number;
  resource_public_id: string;
  agent_id: string | null;
  kind: LibraryItemKind;
  status: LibraryItemStatus;
  title: string;
  description: string | null;
  content: JsonObject;
  file_type: string | null;
  file_size: string | null;
  version: number;
  created_at: string;
};

export type ResourceCreatePayload = {
  agent_id?: string | null;
  kind: LibraryItemKind;
  status?: LibraryItemStatus;
  title: string;
  description?: string | null;
  content?: JsonObject;
  file_type?: string | null;
  file_size?: string | null;
  version?: number;
};

export type ResourceUpdatePayload = {
  agent_id?: string | null;
  kind?: LibraryItemKind;
  status?: LibraryItemStatus;
  title?: string | null;
  description?: string | null;
  content?: JsonObject | null;
  file_type?: string | null;
  file_size?: string | null;
  version?: number | null;
};

export type ActivityEventSummary = {
  actor: ActivityDto["actor"];
  // The person an Agent acted for in a Run, and the Run; null on every
  // other row.
  on_behalf_of: ActivityDto["onBehalfOf"];
  run_id: ActivityDto["runId"];
  event_id: string;
  action: string;
  data: ActivityDto["details"];
  revision?: ActivityDto["revision"];
  entity: ActivityDto["entity"];
  summary: string;
  created_at: string;
};

export type ActivityDto = ApiComponents["schemas"]["ActivityEntry"];
export type ActivityPageDto =
  ApiOperations["listActivity"]["responses"][200]["content"]["application/json"];
export type ActivityListQuery = NonNullable<
  ApiOperations["listActivity"]["parameters"]["query"]
>;
export type ActivityCalendarDto =
  ApiOperations["getActivityCalendar"]["responses"][200]["content"]["application/json"];
export type ActivityCalendarQuery =
  ApiOperations["getActivityCalendar"]["parameters"]["query"];

export type CompanyResourceDto = ApiComponents["schemas"]["CompanyResource"];
export type CompanyResourcePageDto =
  ApiOperations["listCompanyResources"]["responses"][200]["content"]["application/json"];
export type CompanyResourceCreateRequest =
  ApiOperations["createCompanyResource"]["requestBody"]["content"]["application/json"];
export type CompanyResourceUpdateRequest =
  ApiOperations["updateCompanyResource"]["requestBody"]["content"]["application/json"];

export type AttachmentDto = ApiComponents["schemas"]["Attachment"];

export type CompanySearchResult = ApiComponents["schemas"]["SearchResult"];
export type CompanyTaskSearchResult = Extract<
  CompanySearchResult,
  { kind: "task" }
>;
export type CompanyConversationSearchResult = Extract<
  CompanySearchResult,
  { kind: "conversation" }
>;
export type CompanyMessageSearchResult = Extract<
  CompanySearchResult,
  { kind: "message" }
>;
export type CompanySearchRequest =
  ApiOperations["searchCompany"]["requestBody"]["content"]["application/json"];
export type CompanySearchKind = NonNullable<
  CompanySearchRequest["kinds"]
>[number];
export type CompanySearchPage =
  ApiOperations["searchCompany"]["responses"][200]["content"]["application/json"];

export type ConnectorProvider = ApiComponents["schemas"]["ConnectorProvider"];
export type ConnectorSource = ApiComponents["schemas"]["ConnectorSource"];
export type ConnectorItemV1 = ApiComponents["schemas"]["ConnectorItemV1"];
export type ConnectorCatalogRequest =
  ApiComponents["schemas"]["ConnectorCatalogRequest"];
export type ConnectorCatalogResponseV1 =
  ApiComponents["schemas"]["ConnectorCatalogResponseV1"];
export type ConnectorOperationResponse =
  ApiComponents["schemas"]["ConnectorOperationResponse"];

export type PluginProvider = ApiComponents["schemas"]["PluginProvider"];
export type PluginStatus = ApiComponents["schemas"]["PluginStatus"];
export type PluginItemV1 = ApiComponents["schemas"]["PluginItemV1"];
export type PluginCatalogRequest =
  ApiComponents["schemas"]["PluginCatalogRequest"];
export type PluginCatalogResponseV1 =
  ApiComponents["schemas"]["PluginCatalogResponseV1"];
export type PlatformSkill = ApiComponents["schemas"]["PlatformSkill"];
export type PluginOperationResponse =
  ApiComponents["schemas"]["PluginOperationResponse"];
export type PluginDetailResponseV1 =
  ApiComponents["schemas"]["PluginDetailResponseV1"];
export type PluginDetailApp = ApiComponents["schemas"]["PluginDetailApp"];
export type PluginAgentUse = ApiComponents["schemas"]["PluginAgentUse"];

export type WorkflowSummary = ApiComponents["schemas"]["WorkflowSummary"];
export type Workflow = ApiComponents["schemas"]["Workflow"];
export type WorkflowCatalog =
  ApiOperations["listWorkflows"]["responses"][200]["content"]["application/json"];
export type WorkflowCategory = ApiComponents["schemas"]["WorkflowCategory"];
export type WorkflowDeliverableType =
  ApiComponents["schemas"]["WorkflowDeliverableType"];
export type WorkflowConnectorType =
  ApiComponents["schemas"]["WorkflowConnectorType"];
export type WorkflowFieldType = ApiComponents["schemas"]["WorkflowFieldType"];
export type WorkflowFieldOption =
  ApiComponents["schemas"]["WorkflowFieldOption"];
export type WorkflowField = ApiComponents["schemas"]["WorkflowField"];
export type WorkflowPage = ApiComponents["schemas"]["WorkflowPage"];
export type WorkflowPlaybook = ApiComponents["schemas"]["WorkflowPlaybook"];
export type WorkflowSchedule = ApiComponents["schemas"]["WorkflowSchedule"];
export type WorkflowReadiness = ApiComponents["schemas"]["WorkflowReadiness"];
export type WorkflowRequirement =
  ApiComponents["schemas"]["WorkflowRequirement"];
export type WorkflowAppReadiness =
  ApiComponents["schemas"]["WorkflowAppReadiness"];
export type WorkflowAppState = ApiComponents["schemas"]["WorkflowAppState"];
export type WorkflowDiscoveryState =
  ApiComponents["schemas"]["WorkflowDiscoveryState"];
export type WorkflowAppConnector =
  ApiComponents["schemas"]["WorkflowAppConnector"];
export type WorkflowAppPlugin = ApiComponents["schemas"]["WorkflowAppPlugin"];

export type ConversationModelSettings =
  ApiOperations["getConversationModelSettings"]["responses"][200]["content"]["application/json"];
export type ConversationModelSettingsUpdate =
  ApiOperations["updateConversationModelSettings"]["requestBody"]["content"]["application/json"];
