import { createPortal } from "react-dom";
import { useOpenArtifactSource } from "@/features/run-deliverables/hooks/use-open-artifact-source";
import { skipToken } from "@reduxjs/toolkit/query";
import { useGetRunResultQuery } from "@/features/run-deliverables/api/run-deliverables-api";
import { WorkspaceChatHeader } from "./workspace-chat-header";
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ComponentProps,
  type KeyboardEvent as ReactKeyboardEvent,
} from "react";
import { PencilLine, X } from "@/shared/ui/icons";
import type { PromptInputMessage } from "@/shared/ui/core/ai-elements/prompt-input";
import { ChatComposer } from "./chat-composer";
import { ChatModelSettings } from "./chat-model-settings";
import { createModelChangeTimeline } from "./chat-model-change";
import { useGetConversationModelSettingsQuery } from "@/features/chat-panel/api/conversation-model-settings-api";
import { ceoMessageSuggestions } from "./chat-message-suggestions";
import { ChatRunQueue } from "./chat-run-queue";
import { ChatConversation } from "./chat-conversation";
import { ChatAgentPlanDialog } from "./chat-company-plan-dialog";
import { ChatFileViewer } from "./chat-file-viewer";
import {
  chatFileDescription,
  type ChatFileSource,
  type ChatWorkspaceFileSelection,
} from "../model/chat-file-source";
import type { FileUIPart } from "ai";
import { ChatPanelHeader } from "./chat-panel-header";
import { ChatConversationTabs } from "./chat-conversation-tabs";
import { ChatProductSummaryDialog } from "./chat-product-summary-dialog";
import {
  chatPanelBaseClassName,
  chatPanelDockedClassName,
  chatPanelExpandedClassName,
} from "@/features/chat-panel/constants";
import type { DiscoveryOtherOptionSelection } from "@/features/chat-panel/components/structured-chat-message";
import {
  productSummaryFromDiscoveryHandoff,
  productSummaryImageFromDiscoveryPayload,
} from "@/features/chat-panel/model/discovery-product-summary";
import { findLatestCeoCompanyPlanMessage } from "@/features/chat-panel/model/ceo-company-plan-message";
import {
  createChatConnectionRecoveryNotice,
  type ChatConnectionRecoveryNotice,
} from "@/features/chat-panel/model/chat-connection-recovery";
import type { AgentPlan } from "@/features/chat-panel/components/chat-company-plan-data";
import { CEO_CHAT_TEAM_ID } from "@/shared/chat/chat-team";
import type {
  ChatPanelProps,
  DiscoveryPlanHandoff,
} from "@/features/chat-panel/types";
import { chatPanelDataSources } from "@/features/chat-panel/model/chat-panel-surface";
import { useChatAvatarState } from "@/features/chat-panel/hooks/use-chat-avatar-state";
import {
  ChatAgentAttention,
  ChatAgentWelcome,
  ChatAvatarPresence,
} from "./chat-agent-avatar";
import { cx } from "@/shared/lib/cx";
import { canPreviewDesktopGeneratedFiles } from "@/shared/desktop/desktop-environment.service";
import { ContentPanel } from "@/shared/ui/content-panel";
import { toast } from "@/shared/ui/sonner";
import { createConversationRequestTimeline } from "@/features/chat-panel/model/conversation-request-timeline";
import {
  hidePreviousStopNotices,
  type ChatTurnActivity,
} from "@/features/chat-panel/model/chat-run-groups";
import { QuestionCard } from "@/features/approvals/components/question-card";
import { useConversationQuestions } from "@/features/chat-panel/hooks/use-conversation-questions";
import { useInputContinuation } from "@/features/chat-panel/hooks/use-input-continuation";
import { useConversationApprovals } from "@/features/chat-panel/hooks/use-conversation-approvals";
import { useWheelScrollIsolation } from "@/features/chat-panel/hooks/use-wheel-scroll-isolation";
import { useConversationRunQueue } from "@/features/chat-panel/hooks/use-conversation-run-queue";
import { ConversationApprovalCard } from "@/features/approvals/components/conversation-approval-card";
import { useAgentMailConversation } from "@/features/agent-mail/components/use-agent-mail-conversation";
import { AgentMailDraftPanel } from "@/features/agent-mail/components/agent-mail-draft-panel";
import type { AgentMailDraftSelection } from "@/features/agent-mail/model/agent-mail";
import { ConversationArtifactsPanel } from "@/features/chat-panel/components/artifacts/conversation-artifacts-panel";
import { ConversationArtifactsToggle } from "@/features/chat-panel/components/artifacts/conversation-artifacts-toggle";
import { ConversationArtifactViewer } from "@/features/chat-panel/components/artifacts/conversation-artifact-viewer";
import { useConversationArtifacts } from "@/features/chat-panel/hooks/use-conversation-artifacts";
import { useConversationArtifactsPanel } from "@/features/chat-panel/hooks/use-conversation-artifacts-panel";
import { useConversationArtifactNavigation } from "@/features/chat-panel/hooks/use-conversation-artifact-navigation";
import { useArtifactsResize } from "@/features/chat-panel/hooks/use-artifacts-resize";
import { useCreateChatWhiteboardMutation } from "@/features/chat-panel/api/conversation-artifacts-api";
import {
  artifactFeedbackReference,
  conversationArtifactReference,
  type ConversationArtifactReference,
} from "@/features/chat-panel/model/conversation-artifact";
import { useReadConversationNotificationsOnFocus } from "@/features/notifications/hooks/use-read-conversation-notifications-on-focus";
import "@/features/chat-panel/styles/conversation-artifacts.css";
import { StudioModePanel } from "@/features/studio-mode/components/studio-mode-panel";
import { readSessionSnapshot } from "@/shared/session/session";
import { useCan } from "@/shared/permissions";
import { useCompanyInitialization } from "@/features/onboarding/company/components/company-initialization-provider";
import { useCompanyResearch } from "@/features/onboarding/hooks/use-company-research";
import { useListCompanyAgentsQuery } from "@/shared/api/agent-runtime-api";
import { createChatWorkspaceHref } from "@/features/chat-workspace/model/chat-workspace-route";
import { Link } from "react-router";

const COMPANY_PLAN_REVISION_REQUEST_MESSAGE =
  "I want to modify the plan a bit. Please ask me what I want to change.";

export function ChatPanel({
  activeTeam,
  artifactContext,
  allowAgentMailActions,
  allowStructuredActions = true,
  canDecide,
  composerAriaLabel,
  composerAccessory,
  workspaceContext,
  composerPlaceholder,
  connectionStatus,
  conversationTab,
  conversationId,
  studioRuntimeId,
  studioAgentId,
  conversationDecorators,
  conversationSlot,
  conversationSlotClassName,
  draftMessage,
  focusRequestKey,
  focusedHistory,
  hasOlderMessages = false,
  headerActions,
  historyLoadError,
  initialRunQueue,
  isExpanded: isExpandedProp,
  isLoadingOlderMessages = false,
  layout = "dock",
  messages,
  onApproveCompanyDashboard,
  onClose,
  onDiscoveryPlanApproval,
  onDraftMessageChange,
  onExpandedChange,
  onLoadOlderMessages,
  onReviewTask,
  onSendMessage,
  readOnly = false,
  readOnlyPlaceholder = "Scheduled run history is read-only",
  selectedTeam,
  showApprovals = true,
  surface = "conversation",
  teams,
}: ChatPanelProps) {
  const onComposerFocus =
    useReadConversationNotificationsOnFocus(conversationId);
  const [localIsExpanded, setLocalIsExpanded] = useState(false);
  const [selectedFile, setSelectedFile] = useState<ChatFileSource | null>(null);
  const [modelSettingsSaving, setModelSettingsSaving] = useState(false);
  const [studioOpen, setStudioOpen] = useState(false);
  const [hasUnsavedMail, setHasUnsavedMail] = useState(false);
  const studioCompanyId =
    artifactContext?.companyId ?? readSessionSnapshot().company?.id;
  const initialization = useCompanyInitialization();
  const initializationBlocked = Boolean(
    initialization?.companyId === studioCompanyId &&
    conversationId &&
    initialization?.state?.conversationId === conversationId &&
    initialization.conversationPending,
  );
  // While the onboarding research runs in this chat, a message waits for it.
  // Say so before the founder types, and offer a chat that answers now.
  const research = useCompanyResearch(studioCompanyId ?? "");
  const researchHoldsChat = Boolean(
    research.active &&
    conversationId &&
    research.state?.conversationId === conversationId,
  );
  const researchAgentName =
    useListCompanyAgentsQuery(studioCompanyId ?? "", {
      skip: !researchHoldsChat || !studioCompanyId,
    }).currentData?.items.find((agent) => agent.id === conversationTab?.agentId)
      ?.name || "Your CEO";
  const researchNewChatHref =
    researchHoldsChat && conversationTab?.agentId
      ? `${createChatWorkspaceHref({ kind: "direct", agentId: conversationTab.agentId })}&newSession=1`
      : undefined;
  const dataSources = chatPanelDataSources(surface, conversationId);
  const structuredActionsEnabled =
    dataSources.structuredActionsEnabled && allowStructuredActions;
  // Who may answer the chat's questions. A Task chat says so apart from who
  // may post (ADR 0017), and each card then says why when the viewer may
  // not; every other chat, and a history, locks them with its composer,
  // without a reason.
  const questionDecision = dataSources.structuredActionsEnabled
    ? canDecide
    : undefined;
  const questionsReadOnly =
    questionDecision === undefined && (readOnly || !structuredActionsEnabled);
  // Only a viewer who may answer is told the Agent needs their input: the
  // line above the first pending request and the Task header's state. A
  // reader who may not answer sees the plain status. The avatar still
  // waits, for everyone, because the Agent does.
  const viewerMayRespond = questionDecision ?? !readOnly;
  const agentMailActionsEnabled =
    allowAgentMailActions ?? conversationTab?.kind !== "task";
  const conversationApprovals = useConversationApprovals(
    showApprovals ? dataSources.approvalsConversationId : undefined,
  );
  const conversationQuestions = useConversationQuestions(
    dataSources.questionsConversationId,
  );
  const runQueue = useConversationRunQueue(
    dataSources.runQueueConversationId,
    initialRunQueue,
  );
  // Stopping a run acts on the computer it runs on (ADR 0014): the viewer's
  // own, which the queue names to them alone, or anybody's for an owner or
  // admin.
  const canAssignTasks = useCan("tasks.assign");
  const canUseMailbox = useCan("mailbox.manage");
  const canStopRun = Boolean(runQueue.currentRun?.runtimeId) || canAssignTasks;
  const artifacts = useConversationArtifacts(artifactContext);
  const artifactsPanel = useConversationArtifactsPanel(artifactContext);
  const artifactNavigation = useConversationArtifactNavigation(
    artifactContext,
    artifacts,
    artifactsPanel,
  );
  const artifactsToggleRef = useRef<HTMLButtonElement>(null);
  const artifactTriggerRef = useRef<HTMLElement | null>(null);
  const artifactsAsideRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!artifactNavigation.requestKey) return;
    setSelectedFile(null);
    setStudioOpen(false);
  }, [artifactNavigation.requestKey]);
  useEffect(() => {
    if (!selectedFile) return;
    const frame = requestAnimationFrame(() =>
      artifactsAsideRef.current
        ?.querySelector<HTMLButtonElement>(".artifact-viewer__header button")
        ?.focus(),
    );
    return () => cancelAnimationFrame(frame);
  }, [selectedFile]);
  const mailSelection =
    canUseMailbox && dataSources.agentMailConversationId
      ? artifactsPanel.mailSelection
      : null;
  const sidePanelVisible = artifactsPanel.isOpen || Boolean(selectedFile);
  const artifactsVisible = sidePanelVisible && !mailSelection && !selectedFile;
  useEffect(() => {
    if (
      !sidePanelVisible ||
      !mailSelection ||
      window.matchMedia("(min-width: 1440px)").matches
    )
      return;
    const frame = requestAnimationFrame(() =>
      artifactsAsideRef.current
        ?.querySelector<HTMLButtonElement>(".agent-mail-panel__close")
        ?.focus(),
    );
    return () => cancelAnimationFrame(frame);
  }, [sidePanelVisible, mailSelection?.groupId]);
  const selectMail = useCallback(
    (selection: AgentMailDraftSelection) => {
      if (
        hasUnsavedMail &&
        artifactsPanel.mailSelection?.groupId !== selection.groupId
      ) {
        toast.info("Save your email changes before opening another draft.");
        return;
      }
      artifactNavigation.clear();
      setSelectedFile(null);
      setStudioOpen(false);
      artifactsPanel.selectMail(selection);
    },
    [
      artifactNavigation.clear,
      artifactsPanel.mailSelection?.groupId,
      artifactsPanel.selectMail,
      hasUnsavedMail,
    ],
  );
  const listedArtifact = artifacts.items.find(
    (item) => item.artifact.id === artifactsPanel.selection?.artifactId,
  );
  const selectedResult = useGetRunResultQuery(
    artifactContext && artifactsPanel.selection && !listedArtifact
      ? {
          companyId: artifactContext.companyId,
          runId: artifactsPanel.selection.runId,
          artifactId: artifactsPanel.selection.artifactId,
        }
      : skipToken,
    { refetchOnMountOrArgChange: true },
  );
  const selectedOutput = selectedResult.isError
    ? undefined
    : selectedResult.currentData;
  const selectedArtifact =
    artifactNavigation.loading || artifactNavigation.unavailable
      ? undefined
      : artifactsPanel.selection
        ? artifactNavigation.artifact?.artifact.id ===
          artifactsPanel.selection.artifactId
          ? artifactNavigation.artifact
          : (listedArtifact ??
            (selectedOutput?.output?.id === artifactsPanel.selection.artifactId
              ? {
                  runId: artifactsPanel.selection.runId,
                  runtimeId: selectedOutput.runtimeId,
                  runStatus: selectedOutput.runStatus,
                  artifact: selectedOutput.output,
                  deliverable: selectedOutput.deliverable,
                  publication: selectedOutput.publication,
                }
              : undefined))
        : undefined;
  const artifactsResize = useArtifactsResize(
    artifactsAsideRef,
    Boolean(selectedFile || selectedArtifact || mailSelection),
    sidePanelVisible,
  );
  useEffect(() => {
    if (
      artifacts.isLoading ||
      artifacts.isFetching ||
      selectedResult.isLoading ||
      selectedResult.isFetching ||
      artifactNavigation.loading ||
      artifactNavigation.unavailable
    )
      return;
    if (
      artifactsPanel.selection &&
      !selectedArtifact &&
      !selectedResult.isError
    )
      artifactsPanel.back();
    if (
      artifactsPanel.feedback &&
      artifactNavigation.artifact?.artifact.id !==
        artifactsPanel.feedback.artifactId &&
      selectedArtifact?.artifact.id !== artifactsPanel.feedback.artifactId &&
      !artifacts.items.some(
        (item) => item.artifact.id === artifactsPanel.feedback?.artifactId,
      )
    ) {
      artifactsPanel.clearFeedback();
    }
  }, [
    artifacts.isLoading,
    artifacts.isFetching,
    selectedResult.isLoading,
    selectedResult.isFetching,
    selectedResult.isError,
    artifacts.items,
    artifactNavigation.loading,
    artifactNavigation.unavailable,
    artifactNavigation.artifact?.artifact.id,
    artifactsPanel.selection,
    artifactsPanel.feedback,
    artifactsPanel.back,
    artifactsPanel.clearFeedback,
    selectedArtifact,
  ]);
  useEffect(() => {
    if (
      !artifactNavigation.requestKey ||
      artifactNavigation.loading ||
      !artifactsVisible ||
      (!artifactNavigation.target && !artifactNavigation.reviewOnly)
    )
      return;
    const frame = requestAnimationFrame(() =>
      artifactsAsideRef.current
        ?.querySelector<HTMLButtonElement>(
          selectedArtifact
            ? ".artifact-viewer__header button"
            : ".artifacts-panel__header button",
        )
        ?.focus(),
    );
    return () => cancelAnimationFrame(frame);
  }, [
    artifactNavigation.requestKey,
    artifactNavigation.loading,
    artifactNavigation.target,
    artifactNavigation.reviewOnly,
    artifactsVisible,
    conversationTab?.id,
    focusRequestKey,
    selectedArtifact?.artifact.id,
  ]);
  const [pendingDiscoveryPlanHandoff, setPendingDiscoveryPlanHandoff] =
    useState<DiscoveryPlanHandoff | null>(null);
  const [
    submittedDiscoveryPlanMessageIds,
    setSubmittedDiscoveryPlanMessageIds,
  ] = useState<ReadonlySet<number>>(() => new Set());
  const [reviewingAgentPlan, setReviewingAgentPlan] =
    useState<AgentPlan | null>(null);
  const isExpanded = isExpandedProp ?? localIsExpanded;
  const previousIsExpandedRef = useRef(isExpanded);
  const isWorkspaceLayout = layout === "workspace";
  const isWideLayout = isExpanded || isWorkspaceLayout || sidePanelVisible;
  const panelScrollBoundaryRef = useWheelScrollIsolation<HTMLDivElement>();
  const teamMessages = useMemo(
    () => messages.filter((message) => message.team === selectedTeam),
    [messages, selectedTeam],
  );
  const visibleMessages = useMemo(
    () => hidePreviousStopNotices(teamMessages),
    [teamMessages],
  );
  const chatModelSettings = useGetConversationModelSettingsQuery(
    surface === "conversation" &&
      conversationId &&
      (conversationTab?.kind === "direct" || conversationTab?.kind === "task")
      ? conversationId
      : skipToken,
  );
  const agentMail = useAgentMailConversation({
    conversationId: dataSources.agentMailConversationId,
    messages: visibleMessages,
    selection: mailSelection,
    autoOpenInitial:
      !artifactsPanel.hasSavedState && !artifactNavigation.requestKey,
    onSelect: selectMail,
  });
  const resolvedConversationDecorators = useMemo(
    () => [...(conversationDecorators ?? []), ...agentMail.decorators],
    [agentMail.decorators, conversationDecorators],
  );
  const latestCompanyPlanMessage = useMemo(
    () => findLatestCeoCompanyPlanMessage(visibleMessages),
    [visibleMessages],
  );
  const pendingDiscoveryChoiceKeys = useMemo(
    () => findPendingDiscoveryOptionChoiceKeys(visibleMessages),
    [visibleMessages],
  );
  const [activeOtherChoiceKey, setActiveOtherChoiceKey] = useState<
    string | null
  >(null);
  const hasPendingDiscoveryOtherChoice =
    activeOtherChoiceKey !== null &&
    pendingDiscoveryChoiceKeys.includes(activeOtherChoiceKey);
  const hasPendingDiscoveryChoice = pendingDiscoveryChoiceKeys.length > 0;
  const isComposerBlockedByDiscoveryChoice =
    hasPendingDiscoveryChoice && !hasPendingDiscoveryOtherChoice;
  const hasPendingApproval = conversationApprovals.approvals.some(
    (approval) => approval.status === "pending",
  );
  const hasPendingQuestion = conversationQuestions.questions.some(
    (question) => question.status === "pending",
  );
  const hasPendingRequests = hasPendingApproval || hasPendingQuestion;
  const hasStreamingMessage = visibleMessages.some(
    (message) => message.isStreaming,
  );
  const hasThinkingMessage = visibleMessages.some(
    (message) => message.isThinking,
  );
  const {
    isPending: isInputContinuing,
    onResolved: onInputResolved,
    onSubmittingChange: onInputSubmittingChange,
  } = useInputContinuation({
    approvals: conversationApprovals.approvals,
    conversationId: dataSources.questionsConversationId,
    currentRun: runQueue.currentRun,
    messages: visibleMessages,
    questions: conversationQuestions.questions,
  });
  const turnActivity = useMemo<ChatTurnActivity>(
    () => ({
      currentRun: runQueue.currentRun,
      isContinuing: isInputContinuing,
      pendingInputRunIds: new Set(
        [...conversationQuestions.questions, ...conversationApprovals.approvals]
          .filter((request) => request.status === "pending")
          .map((request) => request.originRunId),
      ),
    }),
    [
      runQueue.currentRun,
      isInputContinuing,
      conversationQuestions.questions,
      conversationApprovals.approvals,
    ],
  );
  const showRunThinking = Boolean(
    isInputContinuing ||
    (!hasThinkingMessage &&
      !hasStreamingMessage &&
      (runQueue.currentRun?.status === "dispatched" ||
        runQueue.currentRun?.status === "running")),
  );
  const [scrollToBottomRequestKey, setScrollToBottomRequestKey] = useState(0);
  const connectionRecoveryNotice = useMemo(
    () => createChatConnectionRecoveryNotice(connectionStatus ?? "idle"),
    [connectionStatus],
  );
  const messageScrollRef = useRef<HTMLDivElement>(null);
  const requestScrollToBottomForNewTurn = useCallback(() => {
    setScrollToBottomRequestKey((currentKey) => currentKey + 1);
  }, []);
  const requestTimelineItems = useMemo(
    () =>
      createConversationRequestTimeline({
        approvals: conversationApprovals.approvals,
        messages: visibleMessages,
        questions: conversationQuestions.questions,
      }).map(
        (
          { kind, request, afterMessageId, beforeMessageId },
          _index,
          requests,
        ) => ({
          afterMessageId,
          beforeMessageId,
          isPending: request.status === "pending",
          className: "workspace-chat-request",
          key: `${kind}:${request.id}`,
          createdAt: request.createdAt,
          content: (
            <div
              data-pending-response={request.status === "pending" || undefined}
              tabIndex={-1}
            >
              {viewerMayRespond &&
              !isInputContinuing &&
              request.status === "pending" &&
              request.id ===
                requests.find((item) => item.request.status === "pending")
                  ?.request.id ? (
                <ChatAgentAttention
                  agentId={
                    kind === "question"
                      ? request.askedByAgent.id
                      : (conversationTab?.agentId ??
                        activeTeam.agentId ??
                        selectedTeam)
                  }
                  name={
                    kind === "question"
                      ? request.askedByAgent.name
                      : (workspaceContext?.agentName ?? activeTeam.label)
                  }
                />
              ) : null}
              {kind === "question" ? (
                <QuestionCard
                  canDecide={questionDecision}
                  compact
                  onSubmittingChange={onInputSubmittingChange}
                  question={request}
                  readOnly={questionsReadOnly}
                  variant={workspaceContext?.task ? "task" : "conversation"}
                  onResolved={(question) => {
                    onInputResolved(question);
                    requestScrollToBottomForNewTurn();
                  }}
                />
              ) : (
                <ConversationApprovalCard
                  approval={request}
                  onSubmittingChange={onInputSubmittingChange}
                  onResolved={(approval) => {
                    onInputResolved(approval);
                    requestScrollToBottomForNewTurn();
                  }}
                />
              )}
            </div>
          ),
        }),
      ),
    [
      conversationApprovals.approvals,
      conversationQuestions.questions,
      workspaceContext?.task,
      visibleMessages,
      requestScrollToBottomForNewTurn,
      questionDecision,
      questionsReadOnly,
      viewerMayRespond,
      onInputResolved,
      onInputSubmittingChange,
      isInputContinuing,
      conversationTab?.agentId,
      activeTeam.agentId,
      activeTeam.label,
      selectedTeam,
      workspaceContext?.agentName,
    ],
  );
  const conversationTimelineItems = useMemo(
    () =>
      [
        ...requestTimelineItems,
        ...createModelChangeTimeline({
          changes: chatModelSettings.currentData?.changes ?? [],
          messages: visibleMessages,
          hasOlderMessages,
        }),
      ].sort(
        (left, right) =>
          Date.parse(left.createdAt) - Date.parse(right.createdAt),
      ),
    [
      requestTimelineItems,
      chatModelSettings.currentData?.changes,
      visibleMessages,
      hasOlderMessages,
    ],
  );
  useEffect(() => {
    if (
      activeOtherChoiceKey &&
      !pendingDiscoveryChoiceKeys.includes(activeOtherChoiceKey)
    ) {
      setActiveOtherChoiceKey(null);
    }
  }, [activeOtherChoiceKey, pendingDiscoveryChoiceKeys]);

  const setExpandedState = useCallback(
    (nextIsExpanded: boolean) => {
      setLocalIsExpanded(nextIsExpanded);
      onExpandedChange?.(nextIsExpanded);
    },
    [onExpandedChange],
  );
  const handleToggleExpanded = useCallback(() => {
    const nextIsExpanded = !isExpanded;
    if (!nextIsExpanded) {
      setSelectedFile(null);
    }
    setExpandedState(nextIsExpanded);
  }, [isExpanded, setExpandedState]);
  const handleGeneratedFileSelect = useCallback(
    (selection: ChatWorkspaceFileSelection) => {
      if (hasUnsavedMail) {
        toast.info("Save your email changes before opening a file.");
        return;
      }
      artifactTriggerRef.current =
        document.activeElement instanceof HTMLElement
          ? document.activeElement
          : null;
      if ("href" in selection) {
        setSelectedFile({ type: "workspace", selection });
        setExpandedState(true);
        return;
      }
      if (!selection.runId && !selection.workspaceReference) {
        toast.error("This generated file is no longer connected to its run.");
        return;
      }
      const filename = selection.file.path
        .replaceAll("\\", "/")
        .split("/")
        .at(-1);
      const savedArtifacts = artifacts.items.filter(
        (item) =>
          item.runId === selection.runId &&
          item.artifact.items.some((file) => file.displayName === filename),
      );
      if (savedArtifacts.length === 1) {
        setSelectedFile(null);
        artifactsPanel.select(conversationArtifactReference(savedArtifacts[0]));
        setExpandedState(true);
        return;
      }
      if (!canPreviewDesktopGeneratedFiles()) {
        toast.info("File preview is available in Zenith Team desktop.");
        return;
      }
      setSelectedFile({ type: "workspace", selection });

      setExpandedState(true);
    },
    [artifacts.items, artifactsPanel, hasUnsavedMail, setExpandedState],
  );
  const handleUploadedFileSelect = useCallback(
    (file: FileUIPart) => {
      if (hasUnsavedMail) {
        toast.info("Save your email changes before opening a file.");
        return;
      }
      artifactTriggerRef.current =
        document.activeElement instanceof HTMLElement
          ? document.activeElement
          : null;
      setStudioOpen(false);
      setSelectedFile({ type: "upload", file });
      setExpandedState(true);
    },
    [hasUnsavedMail, setExpandedState],
  );
  const handleClosePanel = useCallback(() => {
    setSelectedFile(null);
    setStudioOpen(false);
    onClose();
  }, [onClose]);

  const closeArtifacts = () => {
    setSelectedFile(null);
    artifactNavigation.clear();
    setStudioOpen(false);
    artifactsPanel.close();
    requestAnimationFrame(() =>
      (artifactTriggerRef.current?.isConnected
        ? artifactTriggerRef.current
        : artifactsToggleRef.current
      )?.focus({ preventScroll: true }),
    );
  };
  const openArtifact = useOpenArtifactSource(artifactContext?.companyId ?? "");
  const showArtifact = (selection: ConversationArtifactReference) => {
    setSelectedFile(null);
    if (artifactNavigation.target) artifactNavigation.clear();
    setStudioOpen(false);
    artifactTriggerRef.current =
      document.activeElement instanceof HTMLElement
        ? document.activeElement
        : null;
    artifactsPanel.select(selection);
  };
  const selectArtifact = (selection: ConversationArtifactReference) => {
    void openArtifact(selection.artifactId, () => showArtifact(selection));
  };
  // A Whiteboard is one more Artifact of an Agent or Task chat: it is made at
  // once, joins the chat's Artifacts and opens beside the chat.
  const [createChatWhiteboard, whiteboardCreation] =
    useCreateChatWhiteboardMutation();
  const canCreateWhiteboard =
    surface === "conversation" &&
    !readOnly &&
    Boolean(artifactContext) &&
    (conversationTab?.kind === "direct" || conversationTab?.kind === "task");
  const createWhiteboard = () => {
    if (!artifactContext || whiteboardCreation.isLoading) return;
    void createChatWhiteboard({
      companyId: artifactContext.companyId,
      conversationId: artifactContext.conversationId,
      title: "Untitled whiteboard",
    })
      .unwrap()
      .then((created) => showArtifact(conversationArtifactReference(created)))
      .catch((error: { message?: string }) =>
        toast.error(error.message ?? "The whiteboard could not be created."),
      );
  };
  const backToArtifacts = () => {
    if (artifactNavigation.target) artifactNavigation.clear();
    setStudioOpen(false);
    artifactsPanel.back();
    requestAnimationFrame(() => {
      if (artifactTriggerRef.current?.isConnected)
        artifactTriggerRef.current.focus({ preventScroll: true });
      else
        artifactsAsideRef.current
          ?.querySelector<HTMLButtonElement>("button")
          ?.focus();
    });
  };
  const handleArtifactsKeyDown = (
    event: ReactKeyboardEvent<HTMLDivElement>,
  ) => {
    if (event.key === "Escape") {
      // A Whiteboard, menu or field inside already used this Escape.
      if (event.defaultPrevented) return;
      event.preventDefault();
      if (studioOpen) {
        setStudioOpen(false);
        return;
      }
      closeArtifacts();
      return;
    }
    if (event.key !== "Tab" || window.matchMedia("(min-width: 1440px)").matches)
      return;
    const controls = [
      ...event.currentTarget.querySelectorAll<HTMLElement>(
        "button:not(:disabled), a[href], summary, input, select, textarea, iframe",
      ),
    ].filter((element) => element.getClientRects().length > 0);
    const first = controls[0];
    const last = controls[controls.length - 1];
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last?.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first?.focus();
    }
  };

  useEffect(() => {
    setSelectedFile(null);
  }, [conversationId, selectedTeam]);
  useEffect(() => {
    const wasExpanded = previousIsExpandedRef.current;
    previousIsExpandedRef.current = isExpanded;
    if (wasExpanded && !isExpanded) {
      setSelectedFile(null);
    }
  }, [isExpanded]);
  const handleDiscoveryOptionSubmit = useCallback(
    (message: string) => {
      setActiveOtherChoiceKey(null);
      requestScrollToBottomForNewTurn();
      onSendMessage({ files: [], text: message });
    },
    [onSendMessage, requestScrollToBottomForNewTurn],
  );
  const handleDiscoveryOtherOptionSelectionChange = useCallback(
    (selection: DiscoveryOtherOptionSelection) => {
      const nextChoiceKey = discoveryChoiceKey(selection);

      setActiveOtherChoiceKey((currentChoiceKey) => {
        if (selection.isSelected) {
          return nextChoiceKey;
        }

        return currentChoiceKey === nextChoiceKey ? null : currentChoiceKey;
      });
    },
    [],
  );
  const handleComposerSendMessage = useCallback(
    async (message: PromptInputMessage) => {
      if (initializationBlocked) {
        throw new Error(
          "This chat will be available when the company research starts.",
        );
      }
      const messageText = message.text ?? "";
      const feedbackRevision = artifactsPanel.feedbackRevision;

      if (hasPendingDiscoveryOtherChoice && messageText.trim().length === 0) {
        return;
      }

      if (hasPendingDiscoveryOtherChoice) {
        setActiveOtherChoiceKey(null);
      }
      requestScrollToBottomForNewTurn();
      await onSendMessage(message);
      if (
        artifactsPanel.feedback &&
        messageText.startsWith(
          `${artifactFeedbackReference(artifactsPanel.feedback)}\n\n`,
        )
      ) {
        artifactsPanel.clearFeedback(feedbackRevision);
      }
    },
    [
      initializationBlocked,
      hasPendingDiscoveryOtherChoice,
      onSendMessage,
      requestScrollToBottomForNewTurn,
      artifactsPanel.feedback,
      artifactsPanel.feedbackRevision,
      artifactsPanel.clearFeedback,
    ],
  );
  const prepareComposerMessage = useCallback(
    (message: PromptInputMessage): PromptInputMessage =>
      artifactsPanel.feedback
        ? {
            ...message,
            text: `${artifactFeedbackReference(artifactsPanel.feedback)}\n\n${message.text ?? ""}`,
          }
        : message,
    [artifactsPanel.feedback],
  );
  const handleDiscoveryPlanHandoffRequest = useCallback(
    (handoff: DiscoveryPlanHandoff) => {
      setPendingDiscoveryPlanHandoff(handoff);
    },
    [],
  );
  const handleConfirmDiscoveryPlanHandoff = useCallback(() => {
    if (!pendingDiscoveryPlanHandoff) {
      return;
    }

    requestScrollToBottomForNewTurn();
    onDiscoveryPlanApproval?.(pendingDiscoveryPlanHandoff);
    setSubmittedDiscoveryPlanMessageIds((currentIds) => {
      const nextIds = new Set(currentIds);

      nextIds.add(pendingDiscoveryPlanHandoff.messageId);

      return nextIds;
    });
    setPendingDiscoveryPlanHandoff(null);
  }, [
    onDiscoveryPlanApproval,
    pendingDiscoveryPlanHandoff,
    requestScrollToBottomForNewTurn,
  ]);
  const handleEditDiscoveryPlanHandoff = useCallback(() => {
    setPendingDiscoveryPlanHandoff(null);
  }, []);
  const handleModifyProposedPlan = useCallback(() => {
    setReviewingAgentPlan(null);
    requestScrollToBottomForNewTurn();
    onSendMessage({ files: [], text: COMPANY_PLAN_REVISION_REQUEST_MESSAGE });
  }, [onSendMessage, requestScrollToBottomForNewTurn]);
  const handleReviewAgentPlan = useCallback((agentPlan: AgentPlan) => {
    setReviewingAgentPlan(agentPlan);
  }, []);
  const handleCloseAgentPlan = useCallback(() => {
    setReviewingAgentPlan(null);
  }, []);
  const renderCompanyPlanOverview = useCallback(
    (message: (typeof visibleMessages)[number]) => {
      if (
        !latestCompanyPlanMessage ||
        message.id !== latestCompanyPlanMessage.messageId
      ) {
        return null;
      }

      return {
        onModifyPlan: handleModifyProposedPlan,
        onReviewAgentPlan: handleReviewAgentPlan,
      };
    },
    [handleModifyProposedPlan, handleReviewAgentPlan, latestCompanyPlanMessage],
  );
  const pendingProductSummary = pendingDiscoveryPlanHandoff
    ? productSummaryFromDiscoveryHandoff(pendingDiscoveryPlanHandoff)
    : null;
  const pendingProductImage = pendingDiscoveryPlanHandoff
    ? productSummaryImageFromDiscoveryPayload(
        pendingDiscoveryPlanHandoff.payload,
      )
    : undefined;
  const disabledDiscoveryPlanMessageIds = useMemo(() => {
    if (!pendingDiscoveryPlanHandoff) {
      return submittedDiscoveryPlanMessageIds;
    }

    const nextIds = new Set(submittedDiscoveryPlanMessageIds);

    nextIds.add(pendingDiscoveryPlanHandoff.messageId);

    return nextIds;
  }, [pendingDiscoveryPlanHandoff, submittedDiscoveryPlanMessageIds]);

  const avatarState = useChatAvatarState(conversationId ?? selectedTeam, {
    currentRun: runQueue.currentRun,
    messages: visibleMessages,
    unavailable: activeTeam.available === false || Boolean(activeTeam.disabled),
    needsInput: hasPendingRequests || hasPendingDiscoveryChoice,
    continuing: isInputContinuing,
  });

  return (
    <ChatAvatarPresence>
      <ChatPanelContainer
        floating={!isWorkspaceLayout}
        className={cx(
          !isWorkspaceLayout && chatPanelBaseClassName,
          isWorkspaceLayout
            ? "workspace-conversation-surface relative h-full min-h-0 w-full overflow-hidden rounded-3xl border border-white/10 bg-[#0B1A2B] shadow-[0_24px_80px_rgba(0,0,0,0.24),inset_0_1px_0_rgba(255,255,255,0.06)]"
            : isWideLayout
              ? chatPanelExpandedClassName
              : chatPanelDockedClassName,
        )}
        style={{
          transform: "translate3d(0, 0, 0)",
          zIndex: !isWorkspaceLayout ? 61 : undefined,
        }}
        contentRef={panelScrollBoundaryRef}
        contentClassName="workspace-conversation-content conversation-chat artifacts-layout pointer-events-auto h-full w-full flex-row overflow-hidden rounded-3xl text-white"
        role="region"
        aria-label="Conversation chat"
      >
        <div
          className={cx(
            "relative flex h-full min-w-0 flex-col overflow-hidden",
            artifactContext && "artifacts-chat-column",
            "flex-1",
          )}
          data-artifacts-open={sidePanelVisible || undefined}
        >
          {conversationTab && !isWorkspaceLayout ? (
            <ChatConversationTabs
              currentTab={conversationTab}
              isWideLayout={isWideLayout}
              teams={teams}
              onClosePanel={handleClosePanel}
            />
          ) : null}

          {isWorkspaceLayout && workspaceContext ? (
            <WorkspaceChatHeader
              context={workspaceContext}
              avatarState={avatarState}
              activeTeam={activeTeam}
              conversationTab={conversationTab}
              onReviewTask={hasPendingRequests ? undefined : onReviewTask}
              taskActivity={{
                canRespond: viewerMayRespond,
                hasPendingApproval,
                hasPendingQuestion,
                running: runQueue.currentRun?.status === "running",
              }}
              onApprovePlan={
                structuredActionsEnabled ? onApproveCompanyDashboard : undefined
              }
              headerActions={headerActions}
              onToggleArtifacts={
                artifactContext
                  ? () => {
                      if (artifactsVisible) closeArtifacts();
                      else {
                        setSelectedFile(null);
                        artifactsPanel.open();
                        requestAnimationFrame(() =>
                          artifactsAsideRef.current
                            ?.querySelector<HTMLButtonElement>("button")
                            ?.focus(),
                        );
                      }
                    }
                  : undefined
              }
              artifactsOpen={artifactsVisible}
              artifactCount={artifacts.count}
              artifactsHasMore={artifacts.hasMore}
              artifactsPanelId={
                artifactContext
                  ? `conversation-artifacts-${artifactContext.conversationId}`
                  : undefined
              }
              artifactsButtonRef={artifactsToggleRef}
            />
          ) : (
            <ChatPanelHeader
              activeTeam={activeTeam}
              avatarState={avatarState}
              conversationTab={conversationTab}
              followPointer={isWorkspaceLayout}
              headerActions={
                artifactContext ? (
                  <>
                    {headerActions}
                    <ConversationArtifactsToggle
                      ref={artifactsToggleRef}
                      count={artifacts.count}
                      hasMore={artifacts.hasMore}
                      open={artifactsVisible}
                      panelId={`conversation-artifacts-${artifactContext.conversationId}`}
                      onToggle={() => {
                        if (artifactsVisible) {
                          closeArtifacts();
                          return;
                        }
                        setSelectedFile(null);
                        artifactsPanel.open();
                        requestAnimationFrame(() =>
                          artifactsAsideRef.current
                            ?.querySelector<HTMLButtonElement>("button")
                            ?.focus(),
                        );
                      }}
                    />
                  </>
                ) : (
                  headerActions
                )
              }
              isExpanded={isWideLayout}
              isWideLayout={isWideLayout}
              onApproveCompanyDashboard={
                structuredActionsEnabled ? onApproveCompanyDashboard : undefined
              }
              onClose={handleClosePanel}
              onReviewTask={onReviewTask}
              onToggleExpanded={handleToggleExpanded}
              reserveMobileNavigatorSpace={isWorkspaceLayout}
              showWindowControls={!isWorkspaceLayout}
            />
          )}

          <ChatConnectionRecoveryNotice
            isExpanded={isWideLayout}
            notice={connectionRecoveryNotice}
          />

          <ChatConversation
            artifacts={artifactContext ? artifacts : undefined}
            onArtifactSelect={selectArtifact}
            emptyState={
              conversationTab?.kind === "direct" &&
              conversationTab.agentId &&
              connectionStatus === "connected" &&
              !hasOlderMessages &&
              !historyLoadError &&
              !focusedHistory &&
              !runQueue.currentRun &&
              !hasPendingRequests &&
              !readOnly ? (
                <ChatAgentWelcome
                  agentId={conversationTab.agentId}
                  name={workspaceContext?.agentName ?? conversationTab.title}
                  role={activeTeam.agentRole}
                />
              ) : undefined
            }
            conversationDecorators={resolvedConversationDecorators}
            conversationSlot={conversationSlot}
            conversationSlotClassName={conversationSlotClassName}
            conversationTimelineItems={conversationTimelineItems}
            focusedMessageId={focusedHistory?.targetSeq}
            hasStreamingMessage={hasStreamingMessage || showRunThinking}
            hasOlderMessages={hasOlderMessages}
            historyLoadError={historyLoadError}
            isExpanded={isWideLayout}
            isLoadingOlderMessages={isLoadingOlderMessages}
            messageScrollRef={messageScrollRef}
            messages={visibleMessages}
            turnActivity={turnActivity}
            readOnly={readOnly || !structuredActionsEnabled}
            scrollToBottomRequestKey={scrollToBottomRequestKey}
            showUserIdentity={conversationTab?.kind === "task"}
            showRunThinking={showRunThinking}
            thinkingAgentId={conversationTab?.agentId ?? activeTeam.agentId}
            onDiscoveryOtherOptionSelectionChange={
              handleDiscoveryOtherOptionSelectionChange
            }
            onDiscoveryPlanHandoff={handleDiscoveryPlanHandoffRequest}
            onDiscoveryOptionSubmit={handleDiscoveryOptionSubmit}
            onGeneratedFileSelect={handleGeneratedFileSelect}
            onUploadedFileSelect={handleUploadedFileSelect}
            onLoadOlderMessages={onLoadOlderMessages}
            onRenderCompanyPlanOverview={renderCompanyPlanOverview}
            submittedDiscoveryPlanMessageIds={disabledDiscoveryPlanMessageIds}
          />

          {composerAccessory ? (
            <div
              className={cx(
                "workspace-chat-composer-accessory mb-3 min-w-0 shrink-0",
                isWideLayout
                  ? "mx-auto w-[calc(100%-48px)] max-w-[832px]"
                  : "mx-4 w-[calc(100%-32px)]",
              )}
            >
              {composerAccessory}
            </div>
          ) : null}

          {researchHoldsChat ? (
            <div
              aria-live="polite"
              className={cx(
                "workspace-chat-connection-notice workspace-chat-research-notice mt-3 flex shrink-0 items-start gap-2 rounded-2xl border px-3 py-2 text-xs leading-5",
                isWideLayout
                  ? "mx-auto w-[calc(100%-48px)] max-w-[832px]"
                  : "mx-4",
              )}
              data-tone="checking"
              role="status"
            >
              <span
                aria-hidden="true"
                className="workspace-chat-connection-notice__dot mt-[7px] size-1.5 shrink-0 animate-pulse rounded-full"
              />
              <span className="min-w-0 flex-1">
                <strong className="block font-black leading-5">
                  {researchAgentName} is researching your company
                </strong>
                <span className="block opacity-80">
                  Messages you send here run when the research finishes.
                </span>
              </span>
              {researchNewChatHref ? (
                <Link
                  className="shrink-0 self-center font-semibold underline underline-offset-2"
                  to={researchNewChatHref}
                >
                  Start a new chat
                </Link>
              ) : null}
            </div>
          ) : null}

          {surface === "conversation" ? (
            <ChatRunQueue
              availability={runQueue.availability}
              deletingItemIds={runQueue.deletingItemIds}
              error={runQueue.error}
              isExpanded={isWideLayout}
              isReordering={runQueue.isReordering}
              items={runQueue.items}
              onRefresh={runQueue.refresh}
              onRemove={runQueue.remove}
              onReorder={runQueue.reorder}
            />
          ) : null}

          {initializationBlocked ? (
            <div
              className={cx(
                "workspace-chat-composer-accessory min-w-0 shrink-0 rounded-xl bg-[var(--workspace-panel-raised-bg)] px-4 py-3 text-[var(--workspace-native-text)]",
                isWorkspaceLayout ? "my-4" : "mb-[38px]",
                isWideLayout
                  ? "mx-auto w-[calc(100%-48px)] max-w-[832px]"
                  : "mx-4 w-[calc(100%-32px)]",
              )}
              role="status"
              aria-atomic="true"
            >
              <p className="m-0 text-sm font-medium leading-5">
                {initialization?.statusText}
              </p>
              <p className="m-0 mt-1 text-[13px] leading-5 text-[var(--workspace-native-text-muted)]">
                You can message here once the company research starts. Other
                chats and tasks are available.
              </p>
            </div>
          ) : (
            <ChatComposer
              context={
                artifactsPanel.feedback && !readOnly ? (
                  <div className="artifacts-feedback flex items-center gap-[10px] flex-none box-border h-[60px] min-w-0 m-[0_12px] p-[10px_0] [border-bottom:1px_solid_var(--workspace-divider,_#dedbd2)] text-[var(--workspace-native-text,_#30352f)]">
                    <PencilLine size={18} aria-hidden />
                    <div className="flex flex-1 flex-col gap-[2px] min-w-0">
                      <span className="text-workspace-muted text-[length:11px] leading-[16px]">
                        Request changes
                      </span>
                      <span
                        className="overflow-hidden text-ellipsis whitespace-nowrap text-[length:13px] font-[500] leading-[20px]"
                        title={artifactsPanel.feedback.title}
                      >
                        {artifactsPanel.feedback.title}
                      </span>
                    </div>
                    <button
                      className="artifacts-icon-button"
                      type="button"
                      aria-label="Cancel change request"
                      title="Cancel change request"
                      onClick={(event) => {
                        artifactsPanel.clearFeedback();
                        event.currentTarget.form
                          ?.querySelector("textarea")
                          ?.focus();
                      }}
                    >
                      <X size={14} aria-hidden />
                    </button>
                  </div>
                ) : undefined
              }
              artifactContext={
                conversationTab?.kind === "direct" && studioCompanyId
                  ? { companyId: studioCompanyId, draftKey: conversationTab.id }
                  : undefined
              }
              key={conversationTab?.id ?? conversationId ?? selectedTeam}
              sessionKey={conversationTab?.id ?? conversationId ?? selectedTeam}
              ariaLabel={
                artifactsPanel.feedback && !readOnly
                  ? `Request change to ${artifactsPanel.feedback.title}`
                  : composerAriaLabel
              }
              disabled={readOnly || isComposerBlockedByDiscoveryChoice}
              readOnly={readOnly}
              onCreateWhiteboard={
                canCreateWhiteboard ? createWhiteboard : undefined
              }
              sendBlocked={modelSettingsSaving}
              modelControls={
                surface === "conversation" &&
                !readOnly &&
                studioCompanyId &&
                conversationId &&
                (conversationTab?.kind === "direct" ||
                  conversationTab?.kind === "task") ? (
                  <ChatModelSettings
                    key={conversationId}
                    companyId={studioCompanyId}
                    conversationId={conversationId}
                    onSavingChange={setModelSettingsSaving}
                  />
                ) : undefined
              }
              draftMessage={draftMessage}
              focusRequestKey={focusRequestKey + artifactsPanel.focusRequestKey}
              isExpanded={isWideLayout}
              currentRun={runQueue.currentRun}
              queueAvailability={runQueue.availability}
              isQueueFull={runQueue.items.length >= runQueue.limit}
              queueLimit={runQueue.limit}
              isStopping={runQueue.isStopping}
              minimal={isWorkspaceLayout}
              suggestions={
                isWorkspaceLayout &&
                conversationTab?.kind === "direct" &&
                selectedTeam === CEO_CHAT_TEAM_ID &&
                connectionStatus === "connected" &&
                visibleMessages.length === 0 &&
                !hasOlderMessages &&
                !historyLoadError &&
                !focusedHistory &&
                !runQueue.currentRun &&
                runQueue.items.length === 0
                  ? ceoMessageSuggestions
                  : undefined
              }
              onDraftMessageChange={onDraftMessageChange}
              onFocus={onComposerFocus}
              onSendMessage={handleComposerSendMessage}
              prepareMessage={prepareComposerMessage}
              onStop={
                canStopRun
                  ? () =>
                      runQueue.stop().catch(() => {
                        toast.error("The current run could not be stopped.");
                      })
                  : undefined
              }
              placeholder={
                readOnly
                  ? readOnlyPlaceholder
                  : isComposerBlockedByDiscoveryChoice
                    ? "Choose an option above to continue"
                    : hasPendingDiscoveryOtherChoice
                      ? "Type another answer..."
                      : artifactsPanel.feedback
                        ? "What would you like to change?"
                        : composerPlaceholder
              }
            />
          )}

          <p
            className={cx(
              "pointer-events-none absolute bottom-[17px] left-1/2 m-0 -translate-x-1/2 whitespace-nowrap text-[12px] font-normal leading-none text-white/35",
              isWorkspaceLayout && "workspace-chat-tagline",
            )}
          >
            Zenith Team Agents can learn through conversations
          </p>
        </div>

        {artifactContext && (
          <div
            id={`conversation-artifacts-${artifactContext.conversationId}`}
            className="artifacts-aside"
            data-viewer={Boolean(
              selectedFile || selectedArtifact || mailSelection,
            )}
            data-resizing={artifactsResize.resizing || undefined}
            style={{ flexBasis: artifactsResize.width }}
            hidden={!sidePanelVisible}
            ref={artifactsAsideRef}
            onKeyDown={handleArtifactsKeyDown}
          >
            <div
              className="artifacts-resize-handle"
              aria-controls={`conversation-artifacts-${artifactContext.conversationId}`}
              {...artifactsResize.separatorProps}
            />
            <div
              className="artifacts-list-container"
              hidden={Boolean(
                selectedFile || selectedArtifact || mailSelection,
              )}
            >
              <ConversationArtifactsPanel
                key={artifactsPanel.key}
                companyId={artifactContext.companyId}
                artifacts={
                  artifactNavigation.reviewOnly
                    ? {
                        ...artifacts,
                        items: artifactNavigation.pending,
                        count: artifactNavigation.pending.length,
                      }
                    : artifacts
                }
                reviewOnly={artifactNavigation.reviewOnly}
                linkedArtifactLoading={
                  artifactNavigation.loading ||
                  Boolean(
                    artifactsPanel.selection &&
                    !selectedArtifact &&
                    selectedResult.isFetching,
                  )
                }
                linkedArtifactUnavailable={
                  artifactNavigation.unavailable || selectedResult.isError
                }
                onRetryLinkedArtifact={
                  artifactNavigation.target
                    ? artifactNavigation.retry
                    : () => void selectedResult.refetch()
                }
                onShowAll={() => {
                  artifactNavigation.clear();
                  artifactsPanel.back();
                }}
                onClose={closeArtifacts}
                onSelect={selectArtifact}
                onCreateWhiteboard={
                  canCreateWhiteboard ? createWhiteboard : undefined
                }
              />
            </div>
            {selectedFile ? (
              <ChatFileViewer
                key={chatFileDescription(selectedFile).id}
                source={selectedFile}
                onClose={closeArtifacts}
              />
            ) : sidePanelVisible && mailSelection && conversationId ? (
              <AgentMailDraftPanel
                key={`${artifactContext.companyId}:${conversationId}:${mailSelection.groupId}`}
                companyId={artifactContext.companyId}
                conversationId={conversationId}
                group={agentMail.group}
                loading={agentMail.loading}
                error={agentMail.error}
                readOnly={readOnly || !agentMailActionsEnabled}
                onClose={closeArtifacts}
                onRetry={agentMail.refresh}
                onDirtyChange={setHasUnsavedMail}
              />
            ) : artifactsVisible &&
              selectedArtifact &&
              studioOpen &&
              conversationId &&
              studioCompanyId ? (
              <StudioModePanel
                key={`${studioCompanyId}:${conversationId}:${studioRuntimeId}:${studioAgentId}`}
                companyId={studioCompanyId}
                conversationId={conversationId}
                runtimeId={studioRuntimeId ?? ""}
                agentId={studioAgentId ?? ""}
                onClose={() => setStudioOpen(false)}
              />
            ) : artifactsVisible && selectedArtifact ? (
              <ConversationArtifactViewer
                artifact={selectedArtifact}
                companyId={artifactContext.companyId}
                initialFileId={
                  artifactNavigation.target?.artifactId ===
                  selectedArtifact.artifact.id
                    ? artifactNavigation.target.fileId
                    : undefined
                }
                onBack={backToArtifacts}
                onClose={closeArtifacts}
                onOpenStudio={
                  conversationId && studioCompanyId && !readOnly
                    ? () => setStudioOpen(true)
                    : undefined
                }
                onFeedback={
                  readOnly || isComposerBlockedByDiscoveryChoice
                    ? undefined
                    : artifactsPanel.giveFeedback
                }
              />
            ) : null}
          </div>
        )}

        {selectedFile && !artifactContext ? (
          <div
            className="artifacts-aside"
            data-viewer="true"
            style={{ flexBasis: artifactsResize.width }}
            ref={artifactsAsideRef}
            onKeyDown={handleArtifactsKeyDown}
          >
            <div
              className="artifacts-resize-handle"
              {...artifactsResize.separatorProps}
            />
            <ChatFileViewer
              key={chatFileDescription(selectedFile).id}
              source={selectedFile}
              onClose={closeArtifacts}
            />
          </div>
        ) : null}
      </ChatPanelContainer>
      {pendingProductSummary ? (
        <ChatProductSummaryDialog
          image={pendingProductImage}
          open={Boolean(pendingDiscoveryPlanHandoff)}
          summary={pendingProductSummary}
          onConfirm={handleConfirmDiscoveryPlanHandoff}
          onEdit={handleEditDiscoveryPlanHandoff}
        />
      ) : null}
      <ChatAgentPlanDialog
        agentPlan={reviewingAgentPlan}
        onClose={handleCloseAgentPlan}
      />
    </ChatAvatarPresence>
  );
}

function findPendingDiscoveryOptionChoiceKeys(
  messages: ChatPanelProps["messages"],
) {
  for (let index = messages.length - 1; index >= 0; index -= 1) {
    const message = messages[index];

    if (message.author === "user") {
      return [];
    }

    if (message.author === "team") {
      if (
        message.isStreaming ||
        message.richContent?.type !== "ceo_discovery_message"
      ) {
        return [];
      }

      return message.richContent.blocks.flatMap((block) =>
        block.kind === "option_group"
          ? [
              discoveryChoiceKey({
                groupId: block.id,
                messageId: message.id,
              }),
            ]
          : [],
      );
    }
  }

  return [];
}

function discoveryChoiceKey({
  groupId,
  messageId,
}: Pick<DiscoveryOtherOptionSelection, "groupId" | "messageId">) {
  return `${messageId}:${groupId}`;
}

function ChatConnectionRecoveryNotice({
  isExpanded,
  notice,
}: {
  isExpanded: boolean;
  notice: ChatConnectionRecoveryNotice;
}) {
  if (!notice.visible) {
    return null;
  }

  return (
    <div
      aria-live={notice.role === "alert" ? "assertive" : "polite"}
      className={cx(
        "workspace-chat-connection-notice mx-4 mt-3 flex shrink-0 items-start gap-2 rounded-2xl border px-3 py-2 text-xs leading-5 shadow-[inset_0_1px_0_rgba(255,255,255,0.08)]",
        isExpanded && "mx-auto w-[calc(100%-48px)] max-w-[832px]",
        recoveryNoticeToneClassName(notice.tone),
      )}
      data-tone={notice.tone}
      role={notice.role}
    >
      <span
        aria-hidden="true"
        className={cx(
          "workspace-chat-connection-notice__dot mt-[7px] size-1.5 shrink-0 rounded-full",
          notice.tone === "danger" ? "bg-red-300" : "animate-pulse bg-sky-300",
        )}
      />
      <span className="min-w-0">
        <strong className="block font-black leading-5">{notice.label}</strong>
        <span className="block text-white/64">{notice.detail}</span>
      </span>
    </div>
  );
}

function recoveryNoticeToneClassName(
  tone: Extract<ChatConnectionRecoveryNotice, { visible: true }>["tone"],
) {
  switch (tone) {
    case "danger":
      return "border-red-300/24 bg-red-500/12 text-red-50";
    case "checking":
      return "border-sky-300/20 bg-sky-300/10 text-sky-50";
  }
}

function ChatPanelContainer({
  floating,
  ...props
}: ComponentProps<typeof ContentPanel> & { floating: boolean }) {
  const panel = <ContentPanel {...props} />;
  return floating ? createPortal(panel, document.body) : panel;
}
