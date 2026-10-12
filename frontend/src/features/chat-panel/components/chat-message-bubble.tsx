import { ChatAttachmentIcon } from "@/shared/chat/chat-attachment-preview";
import { attachmentFromInput } from "@/shared/chat/attachment-preview";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";
import { ChatThinkingIndicator } from "./chat-thinking-indicator";
import { ChatUserMessageContent } from "./chat-user-message-content";
import { personInitials } from "@/shared/ui/person-initials";
import { Attachments } from "@/shared/ui/core/ai-elements/attachments";
import {
  Dialog,
  DialogContent as CoreDialogContent,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { Message, MessageContent } from "@/shared/ui/core/ai-elements/message";
import {
  parseTaskReportMessage,
  TaskReportMessage,
} from "@/features/chat-panel/components/task-report-message";
import { memo, useEffect, useMemo, useState, type ReactNode } from "react";
import { Check, ChevronRight, CircleAlert, Copy } from "@/shared/ui/icons";
import { WithheldAttachmentChip } from "@/shared/chat/withheld-attachment-chip";
import { Streamdown } from "streamdown";
import {
  ChatCompanyPlanOverview,
  type ChatCompanyPlanOverviewProps,
} from "@/features/chat-panel/components/chat-company-plan-dialog";
import { providerLabel } from "@/features/agent-setup/model/ceo-agent-setup";
import { ChatCompanyModeSelect } from "@/features/chat-panel/components/chat-company-mode-select";
import {
  StructuredChatMessage,
  type DiscoveryOtherOptionSelection,
} from "@/features/chat-panel/components/structured-chat-message";
import {
  ChatMarkdownLinkProvider,
  chatMarkdownLinkComponents,
  chatMarkdownRehypePlugins,
  type ChatWorkspaceFileLinkTarget,
} from "@/features/chat-panel/components/chat-markdown-link";
import type { ChatWorkspaceFileSelection } from "../model/chat-file-source";
import { createChatGeneratedFileSelection } from "@/features/chat-panel/model/generated-file-access";
import {
  normalizeRuntimeActivities,
  runtimeActivitiesFromTools,
  runtimeActivityIsInProgress,
} from "@/features/chat-panel/model/chat-runtime-trace";
import {
  WIKI_READ_TOOL,
  WIKI_SEARCH_TOOL,
  wikiReadDetail,
  wikiSearchDetail,
  wikiSearchResultLabel,
} from "@/features/chat-panel/model/wiki-tool-activity";
import { plainCompanyPlanPayloadFromMessage } from "@/features/chat-panel/model/ceo-company-plan-message";
import {
  generatedImagePreviewsFromActivities,
  stripGeneratedImageAssetIdText,
  type GeneratedImagePreview,
} from "@/features/chat-panel/model/generated-image-assets";
import {
  plainModeRecommendationFromMessage,
  plainModeRecommendationPayloadFromMessage,
} from "@/features/chat-panel/model/chat-plain-mode-recommendation";
import type {
  ChatCompanyModeSelection,
  DiscoveryPlanHandoff,
} from "@/features/chat-panel/types";
import type {
  ChatMessage,
  ChatMessageContentBlock,
  ChatRunFailure,
  ChatRuntimeActivity,
} from "@/shared/chat/types";
import { MemberProfileTrigger } from "@/shared/chat/member-profile";
import { formatChatTimestamp } from "@/features/chat-panel/utils";
import { cockpitService } from "@/shared/api";
import { cx } from "@/shared/lib/cx";
import { Icon, type IconName } from "@/shared/ui/icon";
import { toast } from "@/shared/ui/sonner";
import "../styles/chat-runtime-activity.css";

const SUBAGENT_RUNTIME_TOOLS = new Set([
  "close_agent",
  "resume_agent",
  "send_input",
  "spawn_agent",
  "subagent_activity",
  "wait_for_agent",
]);

const SUBAGENT_RUNTIME_METADATA_KEYS = new Set([
  "activityKind",
  "agentPath",
  "agentThreadId",
  "name",
  "receiverThreadIds",
]);

const FILE_CHANGE_RUNTIME_TOOLS = new Set([
  "apply_patch",
  "create_file",
  "edit_file",
  "file_change",
  "write_file",
]);

const FILE_INSPECTION_RUNTIME_TOOLS = new Set([
  "glob",
  "grep",
  "list_files",
  "read_file",
  "read_files",
  "search_files",
]);

const COMMAND_RUNTIME_TOOLS = new Set([
  "command",
  "exec_command",
  "run_command",
  "shell",
  "terminal",
]);

const RUNTIME_ACTIVITY_FAILURE_STATUSES = new Set([
  "canceled",
  "cancelled",
  "error",
  "failed",
]);

const RUNTIME_ACTIVITY_INTERVENTION_STATUSES = new Set([
  "blocked",
  "denied",
  "pending_approval",
  "requires_action",
  "waiting_for_approval",
]);

const RUNTIME_ACTIVITY_GROUP_MINIMUM = 3;

type SubagentNamesByThreadId = ReadonlyMap<string, string>;
const chatMarkdownClassName = [
  "boardly-chat-markdown h-auto w-full min-w-0 break-words text-xs font-medium leading-[22px] text-white",
  "[&>*:first-child]:mt-0 [&>*:last-child]:mb-0",
  "[&_a]:text-white [&_a]:underline [&_a]:underline-offset-2",
  "[&_blockquote]:my-3 [&_blockquote]:border-l-2 [&_blockquote]:border-white/30 [&_blockquote]:pl-3 [&_blockquote]:text-white/80",
  "[&_code]:rounded [&_code]:bg-white/10 [&_code]:px-1 [&_code]:py-0.5 [&_code]:font-mono [&_code]:text-[0.92em] [&_code]:text-white",
  "[&_h1]:mb-2 [&_h1]:mt-4 [&_h1]:text-base [&_h1]:font-semibold [&_h1]:leading-6",
  "[&_h2]:mb-2 [&_h2]:mt-4 [&_h2]:text-base [&_h2]:font-semibold [&_h2]:leading-6",
  "[&_h3]:mb-2 [&_h3]:mt-4 [&_h3]:text-base [&_h3]:font-semibold [&_h3]:leading-6",
  "[&_li]:pl-1",
  "[&_ol]:my-3 [&_ol]:list-decimal [&_ol]:pl-5",
  "[&_p]:my-3",
  "[&_pre]:my-3 [&_pre]:overflow-x-auto [&_pre]:rounded-lg [&_pre]:bg-black/40 [&_pre]:p-3 [&_pre]:text-left",
  "[&_ul]:my-3 [&_ul]:list-disc [&_ul]:pl-5",
].join(" ");

// Streamdown's incomplete-markdown repair auto-closes emphasis that a token
// stream has not finished yet. A committed item is complete, so the repair
// only runs while the text is still streaming; otherwise a `**/` glob inside
// inline code would grow a stray closing `**`.
const ChatMessageMarkdown = memo(function ChatMessageMarkdown({
  children,
  isStreaming = false,
}: {
  children: string;
  isStreaming?: boolean;
}) {
  return (
    <Streamdown
      className={chatMarkdownClassName}
      components={chatMarkdownLinkComponents}
      rehypePlugins={chatMarkdownRehypePlugins}
      controls={false}
      mode={isStreaming ? "streaming" : "static"}
      parseIncompleteMarkdown={isStreaming}
    >
      {children}
    </Streamdown>
  );
});

function ChatMessageBubbleComponent({
  hideRuntimeActivities = false,
  hideThinking = false,
  isDiscoveryPlanSubmitted = false,
  isDiscoveryReadOnly = false,
  isActorGroupStart = true,
  isExpanded = false,
  isFocused = false,
  message,
  runToggle,
  showUserIdentity = false,
  thinkingAgentId,
  onDiscoveryOtherOptionSelectionChange,
  onDiscoveryPlanHandoff,
  onDiscoveryOptionSubmit,
  onGeneratedFileSelect,
  onUploadedFileSelect,
  onRenderCompanyPlanOverview,
}: {
  hideRuntimeActivities?: boolean;
  hideThinking?: boolean;
  isDiscoveryPlanSubmitted?: boolean;
  isDiscoveryReadOnly?: boolean;
  isActorGroupStart?: boolean;
  isExpanded?: boolean;
  isFocused?: boolean;
  message: ChatMessage;
  runToggle?: ReactNode;
  showUserIdentity?: boolean;
  thinkingAgentId?: string;
  onDiscoveryOtherOptionSelectionChange?: (
    selection: DiscoveryOtherOptionSelection,
  ) => void;
  onDiscoveryPlanHandoff?: (handoff: DiscoveryPlanHandoff) => void;
  onDiscoveryOptionSubmit?: (message: string) => void;
  onGeneratedFileSelect?: (selection: ChatWorkspaceFileSelection) => void;
  onUploadedFileSelect?: (
    file: NonNullable<ChatMessage["files"]>[number],
  ) => void;
  onRenderCompanyPlanOverview?: (
    message: ChatMessage,
  ) => Omit<ChatCompanyPlanOverviewProps, "plan"> | null;
}) {
  const isUserMessage = message.author === "user";
  const thinkingAvatarId =
    (message.actor?.kind === "agent" ? message.actor.id : undefined) ??
    thinkingAgentId;
  const taskReportItems = useMemo(
    () => (isUserMessage ? parseTaskReportMessage(message.text) : null),
    [isUserMessage, message.text],
  );
  const normalizedRuntimeActivities = useMemo(
    () => normalizeRuntimeActivities(message.runtimeActivities),
    [message.runtimeActivities],
  );
  const subagentNamesByThreadId = useMemo(
    () => indexSubagentNamesByThreadId(normalizedRuntimeActivities),
    [normalizedRuntimeActivities],
  );
  const generatedImages = useMemo(
    () => generatedImagePreviewsFromActivities(message.runtimeActivities),
    [message.runtimeActivities],
  );
  const renderedContentBlocks = message.contentBlocks ?? [];
  const renderedAssistantText = renderedContentBlocks
    .filter((block) => block.type === "text")
    .map((block) => block.text)
    .join("");
  const displayText = stripGeneratedImageAssetIdText(
    renderedAssistantText || message.text,
    generatedImages,
  );
  const hasText = displayText.trim().length > 0;
  const richContent = message.richContent;
  const displayedGeneratedFiles = message.generatedFiles;
  const handleWorkspaceFileOpen = (target: ChatWorkspaceFileLinkTarget) => {
    if (message.runId === undefined || !onGeneratedFileSelect) {
      toast.error("This workspace file is no longer connected to its run.");
      return;
    }

    const runId = message.runId;
    if (target.kind === "workspace-file-link") {
      onGeneratedFileSelect({ href: target.href, runId });
      return;
    }
    const path = target.path;

    const file = displayedGeneratedFiles?.find(
      (candidate) => candidate.path.replaceAll("\\", "/") === path,
    ) ?? {
      id: `message-link:${message.id}:${path}`,
      operation: "create" as const,
      path,
      truncated: false,
    };
    onGeneratedFileSelect(
      createChatGeneratedFileSelection({
        file,
        runId,
        team: message.team,
      }),
    );
  };
  const hiddenFileActivityIds = useMemo(() => {
    const activityIds = new Set(
      (normalizedRuntimeActivities ?? [])
        .filter(isFileChangeRuntimeActivity)
        .map((activity) => activity.id),
    );
    return activityIds.size ? activityIds : undefined;
  }, [normalizedRuntimeActivities]);
  const orderedRuntimeActivityIds = useMemo(
    () =>
      new Set(
        renderedContentBlocks.flatMap((block) =>
          block.type === "runtime_activity" ? [block.activityId] : [],
        ),
      ),
    [renderedContentBlocks],
  );
  const hasOrderedContent = renderedContentBlocks.some((block) => {
    if (block.type === "runtime_activity") {
      return orderedRuntimeActivityIds.has(block.activityId);
    }
    return (
      stripGeneratedImageAssetIdText(block.text, generatedImages).trim()
        .length > 0
    );
  });
  const discoveryPayload =
    richContent?.type === "ceo_discovery_message" ? richContent : undefined;
  const companyPlanPayload =
    richContent?.type === "ceo_company_plan_message" ? richContent : undefined;
  const plainCompanyPlanPayload = useMemo(
    () =>
      companyPlanPayload ? null : plainCompanyPlanPayloadFromMessage(message),
    [companyPlanPayload, message],
  );
  const resolvedCompanyPlanPayload =
    companyPlanPayload ?? plainCompanyPlanPayload ?? undefined;
  const companyPlanOverviewProps = resolvedCompanyPlanPayload
    ? onRenderCompanyPlanOverview?.(message)
    : null;
  const shouldRenderOrderedContent = Boolean(
    hasOrderedContent &&
    !discoveryPayload &&
    !(resolvedCompanyPlanPayload && companyPlanOverviewProps),
  );
  const excludedRuntimeActivityIds = useMemo(() => {
    const activityIds = new Set<string>(hiddenFileActivityIds);
    if (shouldRenderOrderedContent) {
      for (const activityId of orderedRuntimeActivityIds) {
        activityIds.add(activityId);
      }
    }
    return activityIds.size ? activityIds : undefined;
  }, [
    orderedRuntimeActivityIds,
    shouldRenderOrderedContent,
    hiddenFileActivityIds,
  ]);
  const standaloneRuntimeActivities = useMemo(() => {
    const activities = normalizedRuntimeActivities?.length
      ? normalizedRuntimeActivities
      : (normalizeRuntimeActivities(
          runtimeActivitiesFromTools(
            message.activeTools,
            message.activeSkills,
            "before_message",
          ),
        ) ?? []);

    return activities.filter(
      (activity) => !excludedRuntimeActivityIds?.has(activity.id),
    );
  }, [
    excludedRuntimeActivityIds,
    message.activeSkills,
    message.activeTools,
    normalizedRuntimeActivities,
  ]);
  const plainModeRecommendation = useMemo(
    () =>
      discoveryPayload || resolvedCompanyPlanPayload || message.isStreaming
        ? undefined
        : plainModeRecommendationFromMessage(
            message,
            displayText,
            generatedImages,
          ),
    [
      discoveryPayload,
      displayText,
      generatedImages,
      message,
      resolvedCompanyPlanPayload,
    ],
  );
  const shouldShowGeneratedImagesInline =
    !plainModeRecommendation &&
    discoveryPayload?.flow_step !== "mode_recommendation";
  const runFailure = message.runFailure;
  const isTurnActive = Boolean(
    (message.isThinking || message.isStreaming) &&
    !message.pausedForUserInput &&
    !runFailure &&
    message.runStatus !== "completed" &&
    message.runStatus !== "failed" &&
    message.runStatus !== "cancelled",
  );
  // A tool result finishes a call, not the activity group. Keep the latest
  // group live through reasoning gaps until assistant text or a terminal state.
  const latestContentBlock = [...renderedContentBlocks]
    .reverse()
    .find((block) =>
      block.type === "runtime_activity"
        ? !hiddenFileActivityIds?.has(block.activityId)
        : block.type === "text" &&
          stripGeneratedImageAssetIdText(block.text, generatedImages).trim(),
    );
  const latestActivity =
    standaloneRuntimeActivities
      .filter((activity) => activity.position === "after_message")
      .at(-1) ??
    (shouldRenderOrderedContent
      ? latestContentBlock?.type === "runtime_activity"
        ? normalizedRuntimeActivities?.find(
            (activity) => activity.id === latestContentBlock.activityId,
          )
        : undefined
      : !hasText && !richContent
        ? standaloneRuntimeActivities.at(-1)
        : undefined);
  const activeActivityId =
    isTurnActive &&
    latestActivity &&
    !runtimeActivityBreaksGroup(latestActivity)
      ? latestActivity.id
      : undefined;
  const hasRunningTool = (
    normalizedRuntimeActivities ?? standaloneRuntimeActivities
  ).some(
    (activity) =>
      !hiddenFileActivityIds?.has(activity.id) &&
      runtimeActivityIsInProgress(activity),
  );
  const isInitialThinking = Boolean(
    !hideThinking &&
    isTurnActive &&
    message.isThinking &&
    !hasText &&
    !richContent &&
    !shouldRenderOrderedContent &&
    !runFailure &&
    standaloneRuntimeActivities.length === 0,
  );
  const isContinuationThinking = Boolean(
    !hideThinking &&
    isTurnActive &&
    message.isThinking &&
    !isInitialThinking &&
    !hasRunningTool &&
    !activeActivityId,
  );
  const shouldShowRunHeader = Boolean(runToggle);

  if (isUserMessage) {
    return (
      <Message
        from="user"
        className={cx(
          "mb-3 flex w-full flex-col items-end self-end gap-0 py-0",
          isActorGroupStart ? "mt-4" : "mt-1",
          isExpanded ? "max-w-[760px]" : "max-w-[360px]",
        )}
        data-chat-message-id={message.id}
        data-actor-group-start={isActorGroupStart || undefined}
        data-focused-message-id={isFocused ? message.id : undefined}
        data-boardly-desktop-context-copy={
          taskReportItems
            ? taskReportItems
                .map((item) => `${item.outcome}: ${item.title}`)
                .join("\n")
            : message.text
        }
        data-boardly-desktop-context-kind="message"
        data-boardly-desktop-context-label={
          taskReportItems ? "Task report" : "Founder message"
        }
      >
        {!taskReportItems && showUserIdentity && isActorGroupStart ? (
          <ChatMessageHeader actor={message.actor} align="right" />
        ) : null}
        <ChatMessageAttachments
          attachmentsWithheld={message.attachmentsWithheld}
          files={message.files}
          onUploadedFileSelect={onUploadedFileSelect}
        />
        {taskReportItems ? (
          <TaskReportMessage items={taskReportItems} />
        ) : hasText ? (
          <MessageContent
            variant={null}
            className="workspace-user-message-bubble min-w-0 max-w-full break-words rounded-xl bg-white px-4 py-3 text-left text-xs font-medium leading-[17px] text-black shadow-[4px_38px_31px_rgba(0,0,0,0.5),inset_-3px_-4px_7px_rgba(255,255,255,0.15)]"
          >
            <ChatMarkdownLinkProvider
              onWorkspaceFileOpen={handleWorkspaceFileOpen}
            >
              <ChatUserMessageContent key={message.id}>
                <ChatMessageMarkdown>{message.text}</ChatMessageMarkdown>
              </ChatUserMessageContent>
            </ChatMarkdownLinkProvider>
          </MessageContent>
        ) : null}
        {!taskReportItems ? <UserMessageActions message={message} /> : null}
      </Message>
    );
  }

  return (
    <Message
      from="assistant"
      className={cx(
        "mb-2 w-full min-w-0 max-w-full flex-col items-start self-start gap-0 py-0 text-xs font-medium leading-[22px] text-white",
        isActorGroupStart ? "mt-2" : "mt-0",
      )}
      data-chat-message-id={message.id}
      data-actor-group-start={
        shouldShowRunHeader || isActorGroupStart || undefined
      }
      data-focused-message-id={isFocused ? message.id : undefined}
      data-boardly-desktop-context-copy={
        runFailure ? runFailureCopyText(runFailure, displayText) : displayText
      }
      data-boardly-desktop-context-kind="message"
      data-boardly-desktop-context-label="Agent message"
    >
      {shouldShowRunHeader ? (
        <ChatMessageHeader
          runToggle={runToggle}
          timestamp={getMessageTimestamp(message) ?? undefined}
        />
      ) : null}
      <ChatMarkdownLinkProvider onWorkspaceFileOpen={handleWorkspaceFileOpen}>
        <MessageContent
          variant={null}
          className="workspace-assistant-message-content !w-full !min-w-0 !max-w-full !overflow-visible !rounded-none !bg-transparent !p-0 text-xs font-medium leading-[22px] text-white"
        >
          {!hideRuntimeActivities ? (
            <ChatRuntimeActivities
              activities={standaloneRuntimeActivities}
              activeActivityId={activeActivityId}
              allowWorkingState={isTurnActive}
              position="before_message"
              runFailed={Boolean(runFailure)}
              subagentNamesByThreadId={subagentNamesByThreadId}
            />
          ) : null}
          {discoveryPayload ? (
            <StructuredChatMessage
              isDiscoveryPlanSubmitted={isDiscoveryPlanSubmitted}
              isReadOnly={isDiscoveryReadOnly}
              messageId={message.id}
              onOtherOptionSelectionChange={
                onDiscoveryOtherOptionSelectionChange
              }
              onOptionSubmit={onDiscoveryOptionSubmit}
              onPlanHandoff={onDiscoveryPlanHandoff}
              payload={discoveryPayload}
            />
          ) : resolvedCompanyPlanPayload && companyPlanOverviewProps ? (
            <div className="w-full min-w-0 max-w-full overflow-hidden">
              <ChatCompanyPlanOverview
                {...companyPlanOverviewProps}
                plan={resolvedCompanyPlanPayload}
                showActions={
                  !isDiscoveryReadOnly &&
                  companyPlanOverviewProps.showActions !== false
                }
              />
            </div>
          ) : shouldRenderOrderedContent ? (
            <>
              <ChatOrderedMessageContent
                activities={message.runtimeActivities}
                activeActivityId={activeActivityId}
                blocks={renderedContentBlocks}
                generatedImages={generatedImages}
                hiddenActivityIds={hiddenFileActivityIds}
                allowWorkingState={isTurnActive}
                runFailed={Boolean(runFailure)}
                isStreaming={Boolean(message.isStreaming)}
                subagentNamesByThreadId={subagentNamesByThreadId}
              />
              {plainModeRecommendation ? (
                <ChatPlainModeRecommendationSelector
                  isDiscoveryPlanSubmitted={isDiscoveryPlanSubmitted}
                  isReadOnly={isDiscoveryReadOnly}
                  message={message}
                  messageText={displayText}
                  onPlanHandoff={onDiscoveryPlanHandoff}
                  recommendedMode={plainModeRecommendation}
                />
              ) : null}
            </>
          ) : isInitialThinking ? (
            <ChatThinkingIndicator agentId={thinkingAvatarId} />
          ) : hasText ? (
            <>
              <ChatMessageMarkdown isStreaming={Boolean(message.isStreaming)}>
                {displayText}
              </ChatMessageMarkdown>
              {plainModeRecommendation ? (
                <ChatPlainModeRecommendationSelector
                  isDiscoveryPlanSubmitted={isDiscoveryPlanSubmitted}
                  isReadOnly={isDiscoveryReadOnly}
                  message={message}
                  messageText={displayText}
                  onPlanHandoff={onDiscoveryPlanHandoff}
                  recommendedMode={plainModeRecommendation}
                />
              ) : null}
            </>
          ) : null}
          {shouldShowGeneratedImagesInline ? (
            <ChatGeneratedImagePreviews images={generatedImages} />
          ) : null}
          {runFailure ? <ChatRunFailureNotice failure={runFailure} /> : null}
          {!hideRuntimeActivities ? (
            <ChatRuntimeActivities
              activities={standaloneRuntimeActivities}
              activeActivityId={activeActivityId}
              allowWorkingState={isTurnActive}
              position="after_message"
              runFailed={Boolean(runFailure)}
              subagentNamesByThreadId={subagentNamesByThreadId}
            />
          ) : null}
          {isContinuationThinking && !hideRuntimeActivities ? (
            <ChatThinkingIndicator agentId={thinkingAvatarId} />
          ) : null}
        </MessageContent>
      </ChatMarkdownLinkProvider>
      {!shouldShowRunHeader ? (
        <ChatMessageTimestamp
          timestamp={getMessageTimestamp(message) ?? undefined}
        />
      ) : null}
    </Message>
  );
}

export const ChatMessageBubble = memo(ChatMessageBubbleComponent);

// The failure the server attached to this Run: its provider-aware sentence,
// the provider's own words, and whether Zenith Team is already retrying. The
// copy comes from the server (docs/api/runs.md, "Show failureMessage, then
// failureDetail"); this component only decides how to lay it out.
function ChatRunFailureNotice({ failure }: { failure: ChatRunFailure }) {
  const detailLabel =
    failure.provider && failure.reason?.startsWith("agent_error.provider_")
      ? `${providerLabel(failure.provider)} said`
      : "Details";
  // A single final failure is "attempt 1 of 1": a denominator with nothing
  // to say, so the caption is left out until there is a lineage to count.
  const attempts =
    failure.attempt !== undefined &&
    failure.maxAttempts !== undefined &&
    failure.maxAttempts > 1
      ? `Attempt ${failure.attempt} of ${failure.maxAttempts}`
      : null;
  return (
    <div
      className="mt-2 flex max-w-[640px] gap-2.5 rounded-xl border border-[color-mix(in_srgb,var(--workspace-native-danger)_28%,transparent)] bg-[var(--workspace-danger-soft)] px-3 py-2.5 text-xs font-medium leading-5 text-[var(--workspace-text)]"
      data-run-failure-reason={failure.reason ?? undefined}
      role="alert"
    >
      <CircleAlert
        aria-hidden
        className="mt-0.5 size-4 shrink-0 text-[var(--workspace-native-danger)]"
      />
      <div className="min-w-0">
        <p className="font-semibold text-[var(--workspace-native-danger)]">
          {failure.message}
        </p>
        {failure.detail ? (
          <p className="mt-1 break-words text-[var(--workspace-text-muted)]">
            <span className="font-semibold">{detailLabel}:</span>{" "}
            {failure.detail}
          </p>
        ) : null}
        {failure.retried || attempts ? (
          <p className="mt-1 text-[11px] text-[var(--workspace-text-muted)]">
            {[
              attempts,
              failure.retried
                ? failure.retryActive
                  ? "Zenith Team is retrying this run automatically."
                  : "Retried automatically."
                : null,
            ]
              .filter(Boolean)
              .join(" · ")}
          </p>
        ) : null}
      </div>
    </div>
  );
}

function runFailureCopyText(failure: ChatRunFailure, text: string): string {
  const lines = [text.trim(), failure.message];
  if (failure.detail) lines.push(failure.detail);
  return lines.filter(Boolean).join("\n");
}

function ChatMessageHeader({
  actor,
  align = "left",
  runToggle,
  timestamp,
}: {
  timestamp?: string;
  actor?: ChatMessage["actor"];
  align?: "left" | "right";
  runToggle?: ReactNode;
}) {
  if (!actor) {
    return runToggle ? (
      <div
        data-has-run
        className="workspace-chat-actor mb-1.5 flex items-center gap-2 px-1 text-[12px] font-bold leading-none text-white/55"
      >
        {runToggle}
        {timestamp ? (
          <time className="workspace-chat-actor__time">{timestamp}</time>
        ) : null}
      </div>
    ) : null;
  }

  const initials = personInitials(actor.name);

  const className = cx(
    "workspace-chat-actor mb-1.5 flex items-center gap-2 px-1 text-[12px] font-bold leading-none text-white/55",
    align === "right" && "flex-row-reverse",
  );
  const identity = (
    <>
      <span
        aria-hidden="true"
        data-actor-kind={actor.kind}
        className={cx(
          "workspace-chat-actor__avatar grid size-5 shrink-0 place-items-center overflow-hidden rounded-md border border-white/12 text-[12px] text-white/72",
          actor.kind === "agent" ? "bg-transparent" : "bg-white/10",
        )}
      >
        {actor.kind === "agent" ? (
          <FoldAvatar
            agentId={actor.id ?? actor.name}
            size={24}
            motion="expressive"
            paused
          />
        ) : actor.avatarUrl ? (
          <img
            alt=""
            className="h-full w-full object-cover"
            src={actor.avatarUrl}
          />
        ) : (
          initials || "U"
        )}
      </span>
      <span className="max-w-48 truncate">{actor.name}</span>
    </>
  );

  return (
    <div className={className} data-has-run={Boolean(runToggle) || undefined}>
      {actor.id && (actor.kind === "user" || actor.kind === "agent") ? (
        <MemberProfileTrigger
          className={cx(
            "workspace-chat-actor__profile inline-flex items-center gap-2",
            align === "right" && "flex-row-reverse",
          )}
          profile={{
            id: actor.id,
            kind: actor.kind === "agent" ? "agent" : "human",
            name: actor.name,
            title: actor.kind === "agent" ? "Agent" : "Company member",
            avatarSrc: actor.avatarUrl,
          }}
        >
          {identity}
        </MemberProfileTrigger>
      ) : (
        identity
      )}
      {runToggle}
      {timestamp ? (
        <time className="workspace-chat-actor__time">{timestamp}</time>
      ) : null}
    </div>
  );
}

function ChatPlainModeRecommendationSelector({
  isDiscoveryPlanSubmitted,
  isReadOnly,
  message,
  messageText,
  onPlanHandoff,
  recommendedMode,
}: {
  isDiscoveryPlanSubmitted: boolean;
  isReadOnly: boolean;
  message: ChatMessage;
  messageText: string;
  onPlanHandoff?: (handoff: DiscoveryPlanHandoff) => void;
  recommendedMode: ChatCompanyModeSelection;
}) {
  const [selectedMode, setSelectedMode] =
    useState<ChatCompanyModeSelection>(recommendedMode);
  const isSelectorReadOnly = isReadOnly || isDiscoveryPlanSubmitted;

  function submitMode() {
    if (isSelectorReadOnly || !onPlanHandoff) {
      return;
    }

    onPlanHandoff({
      messageId: message.id,
      payload: plainModeRecommendationPayloadFromMessage(
        message,
        messageText,
        recommendedMode,
      ),
      selectedMode,
    });
  }

  return (
    <ChatCompanyModeSelect
      className="mt-4 w-full"
      layout="compact"
      readOnly={isSelectorReadOnly}
      selectedId={selectedMode}
      submitDisabled={!onPlanHandoff || isSelectorReadOnly}
      submitLabel={isDiscoveryPlanSubmitted ? "Selected" : undefined}
      onSelect={(mode) => setSelectedMode(mode as ChatCompanyModeSelection)}
      onSubmit={submitMode}
    />
  );
}

function ChatGeneratedImagePreviews({
  images,
}: {
  images: GeneratedImagePreview[];
}) {
  if (!images.length) {
    return null;
  }

  return (
    <div className="mt-3 grid w-full max-w-[520px] gap-3">
      {images.map((image) => (
        <ChatGeneratedImagePreview image={image} key={image.id} />
      ))}
    </div>
  );
}

function ChatGeneratedImagePreview({
  image,
}: {
  image: GeneratedImagePreview;
}) {
  const [isPreviewOpen, setIsPreviewOpen] = useState(false);
  const imageLabel = image.alt || "Generated image";

  return (
    <>
      <button
        aria-label={`Open ${imageLabel}`}
        className="group block w-full cursor-pointer overflow-hidden rounded-xl border border-white/12 bg-white/[0.06] p-0 shadow-[0_18px_36px_rgba(0,0,0,0.24),inset_0_1px_0_rgba(255,255,255,0.08)] transition hover:border-white/28"
        type="button"
        onClick={() => setIsPreviewOpen(true)}
      >
        <img
          alt={imageLabel}
          className="block max-h-[420px] w-full bg-black/18 object-contain transition duration-200 group-hover:scale-[1.01]"
          height={image.height}
          loading="lazy"
          src={image.url}
          width={image.width}
        />
      </button>
      <ChatImagePreviewDialog
        imageLabel={imageLabel}
        open={isPreviewOpen}
        src={image.url}
        onOpenChange={setIsPreviewOpen}
      />
    </>
  );
}

type OrderedMessageRenderItem =
  | {
      block: Extract<ChatMessageContentBlock, { text: string }>;
      id: string;
      type: "block";
    }
  | {
      activities: ChatRuntimeActivity[];
      id: string;
      type: "activities";
    };

type RuntimeActivityRenderItem =
  | {
      activity: ChatRuntimeActivity;
      id: string;
      type: "activity";
    }
  | {
      activities: ChatRuntimeActivity[];
      id: string;
      type: "group";
    };

function orderedMessageRenderItems(
  blocks: ChatMessageContentBlock[],
  activityById: ReadonlyMap<string, ChatRuntimeActivity>,
  hiddenActivityIds?: ReadonlySet<string>,
): OrderedMessageRenderItem[] {
  const renderItems: OrderedMessageRenderItem[] = [];
  let adjacentActivities: ChatRuntimeActivity[] = [];

  const flushAdjacentActivities = () => {
    const firstActivity = adjacentActivities[0];
    if (!firstActivity) {
      return;
    }

    renderItems.push({
      activities: adjacentActivities,
      id: `activities:${firstActivity.id}`,
      type: "activities",
    });
    adjacentActivities = [];
  };

  for (const block of blocks) {
    if (block.type === "runtime_activity") {
      const activity = activityById.get(block.activityId);
      if (activity && !hiddenActivityIds?.has(activity.id)) {
        adjacentActivities.push(activity);
        continue;
      }

      flushAdjacentActivities();
      continue;
    }

    flushAdjacentActivities();
    renderItems.push({ block, id: block.id, type: "block" });
  }

  flushAdjacentActivities();
  return renderItems;
}

function groupRuntimeActivitiesForDisplay(
  activities: ChatRuntimeActivity[],
): RuntimeActivityRenderItem[] {
  const renderItems: RuntimeActivityRenderItem[] = [];
  let groupableActivities: ChatRuntimeActivity[] = [];

  const flushGroupableActivities = () => {
    if (groupableActivities.length >= RUNTIME_ACTIVITY_GROUP_MINIMUM) {
      const firstActivity = groupableActivities[0];
      if (firstActivity) {
        renderItems.push({
          activities: groupableActivities,
          id: `activity-group:${firstActivity.id}`,
          type: "group",
        });
      }
    } else {
      renderItems.push(
        ...groupableActivities.map((activity) => ({
          activity,
          id: activity.id,
          type: "activity" as const,
        })),
      );
    }
    groupableActivities = [];
  };

  for (const activity of activities) {
    if (runtimeActivityBreaksGroup(activity)) {
      flushGroupableActivities();
      renderItems.push({ activity, id: activity.id, type: "activity" });
      continue;
    }
    groupableActivities.push(activity);
  }

  flushGroupableActivities();
  return renderItems;
}

function ChatOrderedMessageContent({
  activities,
  activeActivityId,
  allowWorkingState,
  blocks,
  generatedImages,
  hiddenActivityIds,
  runFailed,
  isStreaming,
  subagentNamesByThreadId,
}: {
  activities?: ChatRuntimeActivity[];
  activeActivityId?: string;
  allowWorkingState: boolean;
  blocks: ChatMessageContentBlock[];
  generatedImages: GeneratedImagePreview[];
  hiddenActivityIds?: ReadonlySet<string>;
  runFailed: boolean;
  isStreaming: boolean;
  subagentNamesByThreadId: SubagentNamesByThreadId;
}) {
  const activityById = new Map(
    (normalizeRuntimeActivities(activities) ?? []).map((activity) => [
      activity.id,
      activity,
    ]),
  );
  const renderItems = orderedMessageRenderItems(
    blocks,
    activityById,
    hiddenActivityIds,
  );
  return (
    <div className="flex w-full min-w-0 max-w-full flex-col items-start gap-2">
      {renderItems.map((item) => {
        if (item.type === "activities") {
          return (
            <div className="chat-runtime-activities" key={item.id}>
              <ChatRuntimeActivityItems
                activities={item.activities}
                activeActivityId={activeActivityId}
                allowWorkingState={allowWorkingState}
                runFailed={runFailed}
                subagentNamesByThreadId={subagentNamesByThreadId}
              />
            </div>
          );
        }

        const { block } = item;
        if (block.type === "text") {
          const text = stripGeneratedImageAssetIdText(
            block.text,
            generatedImages,
          );
          return text.trim() ? (
            <div className="w-full" key={block.id}>
              <ChatMessageMarkdown isStreaming={isStreaming}>
                {text}
              </ChatMessageMarkdown>
            </div>
          ) : null;
        }
        if (block.type === "reasoning") {
          return block.text.trim() ? (
            <p
              className="w-full whitespace-pre-wrap text-xs italic text-white/55"
              key={block.id}
            >
              {block.text}
            </p>
          ) : null;
        }

        return null;
      })}
    </div>
  );
}

function ChatRuntimeActivities({
  activities,
  activeActivityId,
  allowWorkingState,
  position,
  runFailed = false,
  subagentNamesByThreadId,
}: {
  activities: ChatRuntimeActivity[];
  activeActivityId?: string;
  allowWorkingState: boolean;
  position: ChatRuntimeActivity["position"];
  runFailed?: boolean;
  subagentNamesByThreadId: SubagentNamesByThreadId;
}) {
  const runtimeActivities = activities.filter(
    (activity) => activity.position === position,
  );

  if (!runtimeActivities.length) {
    return null;
  }

  return (
    <div className="chat-runtime-activities">
      <ChatRuntimeActivityItems
        activities={runtimeActivities}
        activeActivityId={activeActivityId}
        allowWorkingState={allowWorkingState}
        runFailed={runFailed}
        subagentNamesByThreadId={subagentNamesByThreadId}
      />
    </div>
  );
}

function ChatRuntimeActivityItems({
  activities,
  activeActivityId,
  allowWorkingState,
  runFailed,
  subagentNamesByThreadId,
}: {
  activities: ChatRuntimeActivity[];
  activeActivityId?: string;
  allowWorkingState: boolean;
  runFailed: boolean;
  subagentNamesByThreadId: SubagentNamesByThreadId;
}) {
  const renderItems = groupRuntimeActivitiesForDisplay(activities);
  return renderItems.map((item) => {
    if (item.type === "group") {
      return (
        <ChatRuntimeActivityStack
          activities={item.activities}
          activeActivityId={activeActivityId}
          allowWorkingState={allowWorkingState}
          key={item.id}
          runFailed={runFailed}
          subagentNamesByThreadId={subagentNamesByThreadId}
        />
      );
    }
    return (
      <ChatRuntimeActivityRow
        activity={item.activity}
        allowWorkingState={allowWorkingState}
        isLatestActivity={item.activity.id === activeActivityId}
        key={item.id}
        subagentNamesByThreadId={subagentNamesByThreadId}
      />
    );
  });
}

function ChatRuntimeActivityStack({
  activities,
  activeActivityId,
  allowWorkingState,
  runFailed,
  subagentNamesByThreadId,
}: {
  activities: ChatRuntimeActivity[];
  activeActivityId?: string;
  allowWorkingState: boolean;
  runFailed: boolean;
  subagentNamesByThreadId: SubagentNamesByThreadId;
}) {
  const latestActivity = activities.at(-1);
  const isWorking =
    allowWorkingState &&
    activities.some(
      (activity) =>
        activity.id === activeActivityId ||
        runtimeActivityIsInProgress(activity),
    );
  const latestLabel = latestActivity
    ? runtimeActivityDisplayLabel(latestActivity, true)
    : "Working";
  const latestDetail = latestActivity
    ? runtimeActivityPrimaryDetail(
        latestActivity,
        subagentNamesByThreadId,
        true,
      )
    : undefined;
  const { label: completedSummary, iconName: completedIconName } =
    summarizeRuntimeActivities(activities);
  const failedActivityCount = activities.filter(
    runtimeActivityHasTerminalFailure,
  ).length;
  // A tool call that errored while the Run went on to finish is a step the
  // agent recovered from, worth a look but not an alarm; the alarm is the
  // Run's own failure, which the bubble reports separately.
  const alarming = failedActivityCount > 0 && runFailed;
  const completedLabel = failedActivityCount
    ? `${completedSummary} · ${failedActivityCount} ${
        runFailed
          ? "failed"
          : failedActivityCount === 1
            ? "tool error, recovered"
            : "tool errors, recovered"
      }`
    : completedSummary;

  return (
    <details className="chat-runtime-stack">
      <summary
        className="chat-runtime-summary"
        data-attention={alarming || undefined}
        data-working={isWorking || undefined}
        title={
          isWorking
            ? [latestLabel, latestDetail].filter(Boolean).join(" · ")
            : completedLabel
        }
      >
        {isWorking && latestActivity ? (
          <RuntimeActivitySummary
            activity={latestActivity}
            isWorking
            label={latestLabel}
            primaryDetail={latestDetail}
          />
        ) : (
          <>
            <Icon
              aria-hidden
              name={alarming ? "warning-2" : completedIconName}
              size={14}
            />
            <span className="chat-runtime-label">{completedLabel}</span>
            <span className="sr-only">
              {alarming
                ? "Completed activities with failures"
                : failedActivityCount
                  ? "Completed activities, some tool errors recovered"
                  : "Completed activities"}
            </span>
          </>
        )}
        <ChevronRight aria-hidden className="chat-runtime-chevron" size={14} />
      </summary>
      <div className="chat-runtime-stack__items">
        {activities.map((activity) => (
          <ChatRuntimeActivityRow
            activity={activity}
            allowWorkingState={allowWorkingState}
            key={activity.id}
            subagentNamesByThreadId={subagentNamesByThreadId}
          />
        ))}
      </div>
    </details>
  );
}

type RuntimeActivitySummaryKind =
  | "checked_code"
  | "coordinated_subagents"
  | "edited_files"
  | "generated_images"
  | "inspected_images"
  | "installed_dependencies"
  | "loaded_skills"
  | "ran_commands"
  | "ran_tests"
  | "read_files"
  | "read_wiki_pages"
  | "researched_sources"
  | "searched_wiki"
  | "used_tools"
  | "validated_build";

function summarizeRuntimeActivities(activities: ChatRuntimeActivity[]) {
  const countsByKind = new Map<RuntimeActivitySummaryKind, number>();

  for (const activity of activities) {
    const kind = runtimeActivitySummaryKind(activity);
    countsByKind.set(kind, (countsByKind.get(kind) ?? 0) + 1);
  }

  const phrases = [...countsByKind].map(([kind, count]) =>
    runtimeActivitySummaryPhrase(kind, count),
  );
  const visiblePhrases: string[] = phrases.slice(0, 3);
  const hiddenPhraseCount = phrases.length - visiblePhrases.length;
  if (hiddenPhraseCount > 0) {
    visiblePhrases.push(`${hiddenPhraseCount} more`);
  }

  const summary = visiblePhrases.join(", ");
  return {
    iconName: runtimeActivitySummaryIconName(
      countsByKind.keys().next().value ?? "used_tools",
    ),
    label: summary
      ? `${summary.charAt(0).toUpperCase()}${summary.slice(1)}`
      : "Completed activities",
  };
}

function runtimeActivitySummaryKind(
  activity: ChatRuntimeActivity,
): RuntimeActivitySummaryKind {
  if (isFileChangeRuntimeActivity(activity)) {
    return "edited_files";
  }
  if (isFileInspectionRuntimeActivity(activity)) {
    return "read_files";
  }
  if (isCommandRuntimeActivity(activity)) {
    switch (runtimeCommandKind(activity)) {
      case "build":
        return "validated_build";
      case "install":
        return "installed_dependencies";
      case "inspect":
        return "ran_commands";
      case "quality":
        return "checked_code";
      case "test":
        return "ran_tests";
      default:
        return "ran_commands";
    }
  }
  if (activity.toolName === "web_search" || activity.toolName === "web_visit") {
    return "researched_sources";
  }
  if (activity.toolName === WIKI_SEARCH_TOOL) {
    return "searched_wiki";
  }
  if (activity.toolName === WIKI_READ_TOOL) {
    return "read_wiki_pages";
  }
  if (activity.toolName === "view_image") {
    return "inspected_images";
  }
  if (activity.toolName === "generate_image") {
    return "generated_images";
  }
  if (
    activity.toolName === "load_skill" ||
    activity.toolName === "load_skill_reference" ||
    activity.toolName === "search_skills"
  ) {
    return "loaded_skills";
  }
  if (SUBAGENT_RUNTIME_TOOLS.has(activity.toolName)) {
    return "coordinated_subagents";
  }
  return "used_tools";
}

function runtimeActivitySummaryPhrase(
  kind: RuntimeActivitySummaryKind,
  count: number,
) {
  switch (kind) {
    case "checked_code":
      return "checked code quality";
    case "coordinated_subagents":
      return "coordinated sub-agents";
    case "edited_files":
      return count === 1 ? "edited a file" : "edited files";
    case "generated_images":
      return count === 1 ? "generated an image" : "generated images";
    case "inspected_images":
      return count === 1 ? "inspected an image" : "inspected images";
    case "installed_dependencies":
      return "installed dependencies";
    case "loaded_skills":
      return count === 1 ? "loaded a skill" : "loaded skills";
    case "ran_commands":
      return count === 1 ? "ran a command" : "ran commands";
    case "ran_tests":
      return "ran tests";
    case "read_files":
      return "read files";
    case "researched_sources":
      return "researched sources";
    case "validated_build":
      return "validated the build";
    case "read_wiki_pages":
      return count === 1 ? "read 1 wiki page" : `read ${count} wiki pages`;
    case "searched_wiki":
      return "searched the wiki";
    case "used_tools":
      return count === 1 ? "used a tool" : "used tools";
  }
}

function ChatRuntimeActivityRow({
  activity,
  allowWorkingState,
  isLatestActivity = false,
  subagentNamesByThreadId,
}: {
  activity: ChatRuntimeActivity;
  allowWorkingState: boolean;
  isLatestActivity?: boolean;
  subagentNamesByThreadId: SubagentNamesByThreadId;
}) {
  const isWorking =
    allowWorkingState &&
    (isLatestActivity || runtimeActivityIsInProgress(activity));
  const label = runtimeActivityDisplayLabel(activity, isWorking);
  const primaryDetail = runtimeActivityPrimaryDetail(
    activity,
    subagentNamesByThreadId,
    isWorking,
  );
  const hasExpandableDetail = runtimeActivityHasExpandableDetail(activity);
  const needsAttention = runtimeActivityNeedsAttention(activity);
  const expandForIntervention = runtimeActivityBreaksGroup(activity);
  const [isOpen, setIsOpen] = useState(expandForIntervention);
  useEffect(() => {
    if (expandForIntervention) {
      setIsOpen(true);
    }
  }, [expandForIntervention]);
  if (!hasExpandableDetail) {
    return (
      <div
        className="chat-runtime-summary"
        data-attention={needsAttention || undefined}
        data-working={isWorking || undefined}
      >
        <RuntimeActivitySummary
          activity={activity}
          isWorking={isWorking}
          label={label}
          primaryDetail={primaryDetail}
        />
      </div>
    );
  }

  return (
    <details
      className="chat-runtime-activity"
      open={isOpen}
      onToggle={(event) => setIsOpen(event.currentTarget.open)}
    >
      <summary
        className="chat-runtime-summary"
        data-attention={needsAttention || undefined}
        data-working={isWorking || undefined}
        title={[label, primaryDetail].filter(Boolean).join(" · ")}
      >
        <RuntimeActivitySummary
          activity={activity}
          isWorking={isWorking}
          label={label}
          primaryDetail={primaryDetail}
        />
        <ChevronRight aria-hidden className="chat-runtime-chevron" size={14} />
      </summary>
      <div className="chat-runtime-activity__details">
        {activity.input ? (
          <RuntimeActivityDetailBlock label="Input" value={activity.input} />
        ) : null}
        {activity.output ? (
          <RuntimeActivityDetailBlock label="Result" value={activity.output} />
        ) : null}
        {activity.error ? (
          <RuntimeActivityDetailBlock label="Error" value={activity.error} />
        ) : null}
        {activity.exitCode !== undefined && activity.exitCode !== 0 ? (
          <RuntimeActivityDetailBlock
            label="Exit code"
            value={String(activity.exitCode)}
          />
        ) : null}
      </div>
    </details>
  );
}

function RuntimeActivitySummary({
  activity,
  isWorking,
  label,
  primaryDetail,
}: {
  activity: ChatRuntimeActivity;
  isWorking: boolean;
  label: string;
  primaryDetail?: string;
}) {
  const wikiRevision =
    activity.toolName === WIKI_READ_TOOL
      ? wikiReadDetail(activity).revision
      : undefined;
  return (
    <span className="chat-runtime-activity__label">
      <Icon name={runtimeActivityIconName(activity)} size={14} aria-hidden />
      <span className="sr-only">
        {runtimeActivityNeedsAttention(activity)
          ? "Needs attention: "
          : isWorking
            ? "In progress: "
            : "Completed: "}
      </span>
      <span className="chat-runtime-label">
        {label}
        {primaryDetail ? (
          <span className="chat-runtime-primary-detail"> {primaryDetail}</span>
        ) : null}
      </span>
      {wikiRevision !== undefined ? (
        <span className="chat-runtime-revision" title="Revision the Agent read">
          v{wikiRevision}
        </span>
      ) : null}
    </span>
  );
}

function RuntimeActivityDetailBlock({
  label,
  value,
}: {
  label: string;
  value: string;
}) {
  return (
    <div className="chat-runtime-detail">
      <div className="chat-runtime-detail__label">{label}</div>
      <pre className="chat-runtime-detail__payload">
        {formatRuntimeActivityPayload(value)}
      </pre>
    </div>
  );
}

function runtimeActivityPrimaryDetail(
  activity: ChatRuntimeActivity,
  subagentNamesByThreadId: SubagentNamesByThreadId,
  isWorking = runtimeActivityIsInProgress(activity),
) {
  const input = parseRuntimeActivityPayload(activity.input);

  if (isCommandRuntimeActivity(activity) && isWorking) {
    return runtimeActivityCommand(activity);
  }

  if (activity.toolName === "load_skill") {
    return activity.label.replace(/^Load skill\s*/, "").trim() || undefined;
  }

  if (SUBAGENT_RUNTIME_TOOLS.has(activity.toolName)) {
    return subagentPrimaryDetail(input, subagentNamesByThreadId);
  }

  if (activity.toolName === WIKI_SEARCH_TOOL) {
    const { query, resultCount } = wikiSearchDetail(activity);
    const count = wikiSearchResultLabel(resultCount);
    return (
      [query ? `“${query}”` : undefined, count].filter(Boolean).join(" · ") ||
      undefined
    );
  }

  if (activity.toolName === WIKI_READ_TOOL) {
    return wikiReadDetail(activity).title;
  }

  if (activity.toolName === "web_search") {
    const query = stringFromPayload(input, [["query"], ["request", "query"]]);
    return query ? `query: ${query}` : undefined;
  }

  if (activity.toolName === "web_visit") {
    const url = stringFromPayload(input, [["url"], ["request", "url"]]);
    return url ? `url: ${url}` : undefined;
  }

  if (activity.toolName === "generate_image") {
    const prompt = stringFromPayload(input, [
      ["prompt"],
      ["request", "prompt"],
    ]);
    return prompt ? `prompt: ${prompt}` : undefined;
  }

  return undefined;
}

function indexSubagentNamesByThreadId(
  activities: ChatRuntimeActivity[] | undefined,
): SubagentNamesByThreadId {
  const namesByThreadId = new Map<string, string>();

  for (const activity of activities ?? []) {
    const input = parseRuntimeActivityPayload(activity.input);
    const threadId = stringFromPayload(input, [["agentThreadId"]]);
    const name = subagentNameFromPath(
      stringFromPayload(input, [["agentPath"]]),
    );
    if (threadId && name) {
      namesByThreadId.set(threadId, name);
    }
  }

  return namesByThreadId;
}

function subagentPrimaryDetail(
  input: unknown,
  subagentNamesByThreadId: SubagentNamesByThreadId,
) {
  const names = new Set<string>();
  const directName = subagentNameFromPath(
    stringFromPayload(input, [["agentPath"]]),
  );
  if (directName) {
    names.add(directName);
  }

  const agentThreadId = stringFromPayload(input, [["agentThreadId"]]);
  const indexedName = agentThreadId
    ? subagentNamesByThreadId.get(agentThreadId)
    : undefined;
  if (indexedName) {
    names.add(indexedName);
  }

  for (const receiverThreadId of stringArrayFromPayload(
    input,
    "receiverThreadIds",
  )) {
    const receiverName = subagentNamesByThreadId.get(receiverThreadId);
    if (receiverName) {
      names.add(receiverName);
    }
  }

  const resolvedNames = [...names];
  const firstName = resolvedNames[0];
  if (!firstName) {
    return undefined;
  }
  return resolvedNames.length === 1
    ? firstName
    : `${firstName} +${resolvedNames.length - 1}`;
}

function subagentNameFromPath(agentPath: string | undefined) {
  if (!agentPath) {
    return undefined;
  }

  const pathSegments = agentPath.split("/").filter(Boolean);
  return pathSegments[pathSegments.length - 1];
}

function stringArrayFromPayload(value: unknown, key: string) {
  if (!isRecord(value) || !Array.isArray(value[key])) {
    return [];
  }

  return value[key].flatMap((item) => {
    if (typeof item !== "string") {
      return [];
    }
    const normalized = item.trim();
    return normalized ? [normalized] : [];
  });
}

function runtimeActivityDisplayLabel(
  activity: ChatRuntimeActivity,
  isInProgress = runtimeActivityIsInProgress(activity),
) {
  if (isCommandRuntimeActivity(activity)) {
    return runtimeCommandDisplayLabel(activity, isInProgress);
  }
  switch (activity.toolName) {
    case "load_skill":
      return "Load skill";
    case "web_search":
      return isInProgress ? "Searching the web" : "Searched the web";
    case "wiki_search":
      return isInProgress ? "Searching the wiki" : "Searched the wiki";
    case "wiki_read":
      return isInProgress ? "Reading a wiki page" : "Read wiki page";
    case "web_visit":
      return isInProgress ? "Opening source" : "Reviewed source";
    case "view_image":
      return isInProgress ? "Inspecting image" : "Inspected image";
    case "generate_image":
      return isInProgress ? "Generating image" : "Generated image";
    case "subagent_activity":
      switch (subagentActivityKind(activity)) {
        case "started":
          return "Sub-agent started";
        case "interacted":
          return "Sub-agent collaborated";
        case "interrupted":
          return "Sub-agent interrupted";
        default:
          return "Sub-agent activity";
      }
    case "spawn_agent":
      return runtimeActivitySucceeded(activity)
        ? "Sub-agent started"
        : "Starting sub-agent";
    case "send_input":
      return runtimeActivitySucceeded(activity)
        ? "Input sent to sub-agent"
        : "Sending input to sub-agent";
    case "resume_agent":
      return runtimeActivitySucceeded(activity)
        ? "Sub-agent resumed"
        : "Resuming sub-agent";
    case "wait_for_agent":
      return runtimeActivitySucceeded(activity)
        ? "Finished waiting for sub-agent"
        : "Waiting for sub-agent";
    case "close_agent":
      return runtimeActivitySucceeded(activity)
        ? "Sub-agent closed"
        : "Closing sub-agent";
    default:
      return (
        activity.label.replace(/^Use tool\s*/, "").trim() || activity.label
      );
  }
}

function runtimeCommandDisplayLabel(
  activity: ChatRuntimeActivity,
  isInProgress = runtimeActivityIsInProgress(activity),
) {
  switch (runtimeCommandKind(activity)) {
    case "build":
      return isInProgress ? "Validating build" : "Validated build";
    case "install":
      return isInProgress
        ? "Installing dependencies"
        : "Installed dependencies";
    case "inspect":
      return isInProgress ? "Inspecting workspace" : "Inspected workspace";
    case "quality":
      return isInProgress ? "Checking code quality" : "Checked code quality";
    case "test":
      return isInProgress ? "Running tests" : "Ran tests";
    default:
      return isInProgress ? "Running command" : "Ran command";
  }
}

type RuntimeCommandKind =
  "build" | "command" | "install" | "inspect" | "quality" | "test";

function runtimeCommandKind(activity: ChatRuntimeActivity): RuntimeCommandKind {
  const command = runtimeActivityCommand(activity)?.toLowerCase();

  if (!command) {
    return "command";
  }
  if (
    /\b(vitest|jest|pytest|go test|cargo test|playwright test|(npm|pnpm|yarn|bun)( exec)? test)\b/.test(
      command,
    )
  ) {
    return "test";
  }
  if (/\b(lint|eslint|golangci-lint|go vet|prettier --check)\b/.test(command)) {
    return "quality";
  }
  if (
    /\b(typecheck|tsc|vite build|go build|cargo check|make gate|(npm|pnpm|yarn|bun)( run)? build)\b/.test(
      command,
    )
  ) {
    return "build";
  }
  if (
    /\b(npm|pnpm|yarn|bun)\s+(install|add)\b|\bgo mod download\b/.test(command)
  ) {
    return "install";
  }
  if (
    /\b(git (status|diff|log|show)|rg|sed|cat|find|ls|head|tail|pwd)\b/.test(
      command,
    )
  ) {
    return "inspect";
  }
  return "command";
}

function runtimeActivityCommand(activity: ChatRuntimeActivity) {
  return stringFromPayload(parseRuntimeActivityPayload(activity.input), [
    ["cmd"],
    ["command"],
    ["request", "cmd"],
    ["request", "command"],
  ]);
}

function isCommandRuntimeActivity(activity: ChatRuntimeActivity) {
  return (
    COMMAND_RUNTIME_TOOLS.has(activity.toolName.toLowerCase()) ||
    activity.label
      .replace(/^Use tool\s*/i, "")
      .trim()
      .toLowerCase() === "command"
  );
}

function isFileChangeRuntimeActivity(activity: ChatRuntimeActivity) {
  const normalizedToolName = activity.toolName.toLowerCase();
  const normalizedLabel = activity.label
    .replace(/^Use tool\s*/i, "")
    .replace(/[^a-z]/gi, "")
    .toLowerCase();
  return (
    FILE_CHANGE_RUNTIME_TOOLS.has(normalizedToolName) ||
    normalizedLabel === "filechange" ||
    normalizedLabel === "applypatch"
  );
}

function isFileInspectionRuntimeActivity(activity: ChatRuntimeActivity) {
  return FILE_INSPECTION_RUNTIME_TOOLS.has(activity.toolName.toLowerCase());
}

function runtimeActivityHasExpandableDetail(activity: ChatRuntimeActivity) {
  if (
    activity.output?.trim() ||
    activity.error?.trim() ||
    (activity.exitCode !== undefined && activity.exitCode !== 0)
  ) {
    return true;
  }
  if (!activity.input?.trim()) {
    return false;
  }

  const input = parseRuntimeActivityPayload(activity.input);
  if (!isRecord(input)) {
    return true;
  }
  if (!SUBAGENT_RUNTIME_TOOLS.has(activity.toolName)) {
    return Object.keys(input).length > 0;
  }
  const visibleKeys = Object.keys(input).filter(
    (key) => !SUBAGENT_RUNTIME_METADATA_KEYS.has(key),
  );
  return visibleKeys.length > 0;
}

function runtimeActivityIconName(activity: ChatRuntimeActivity): IconName {
  if (runtimeActivityNeedsAttention(activity)) return "warning-2";
  if (
    isCommandRuntimeActivity(activity) &&
    runtimeCommandKind(activity) === "inspect"
  ) {
    return "folder-open";
  }
  switch (activity.toolName) {
    case "list_agents":
      return "cpu-charge";
    case "company_plan_export":
      return "task-square";
    case "company_plan_transaction_preview":
      return "eye";
    case "company_plan_transaction_apply":
      return "tick-square";
  }
  return runtimeActivitySummaryIconName(runtimeActivitySummaryKind(activity));
}

function runtimeActivitySummaryIconName(
  kind: RuntimeActivitySummaryKind,
): IconName {
  switch (kind) {
    case "checked_code":
    case "ran_tests":
    case "validated_build":
      return "shield-tick";
    case "coordinated_subagents":
      return "cpu-charge";
    case "edited_files":
      return "edit";
    case "generated_images":
    case "inspected_images":
      return "gallery";
    case "installed_dependencies":
      return "box";
    case "loaded_skills":
      return "book";
    case "ran_commands":
      return "code";
    case "read_files":
      return "document-text";
    case "read_wiki_pages":
      return "book-open";
    case "researched_sources":
    case "searched_wiki":
      return "search";
    case "used_tools":
      return "toolbox";
  }
}

function runtimeActivitySucceeded(activity: ChatRuntimeActivity) {
  const status = activity.status?.toLowerCase();
  return status === "completed" || status === "succeeded";
}

function runtimeActivityNeedsAttention(activity: ChatRuntimeActivity) {
  return (
    subagentActivityKind(activity) === "interrupted" ||
    runtimeActivityHasTerminalFailure(activity) ||
    RUNTIME_ACTIVITY_INTERVENTION_STATUSES.has(
      activity.status?.toLowerCase() ?? "",
    )
  );
}

function runtimeActivityBreaksGroup(activity: ChatRuntimeActivity) {
  return (
    subagentActivityKind(activity) === "interrupted" ||
    RUNTIME_ACTIVITY_INTERVENTION_STATUSES.has(
      activity.status?.toLowerCase() ?? "",
    )
  );
}

function runtimeActivityHasTerminalFailure(activity: ChatRuntimeActivity) {
  return RUNTIME_ACTIVITY_FAILURE_STATUSES.has(
    activity.status?.toLowerCase() ?? "",
  );
}

function subagentActivityKind(activity: ChatRuntimeActivity) {
  if (activity.toolName !== "subagent_activity") {
    return undefined;
  }
  return stringFromPayload(parseRuntimeActivityPayload(activity.input), [
    ["activityKind"],
    ["name"],
  ]);
}

function formatRuntimeActivityPayload(value: string) {
  const parsed = parseRuntimeActivityPayload(value);

  if (parsed === undefined) {
    return truncateRuntimeActivityText(value);
  }

  return truncateRuntimeActivityText(JSON.stringify(parsed, null, 2));
}

function parseRuntimeActivityPayload(value: string | undefined): unknown {
  if (!value) {
    return undefined;
  }

  try {
    return JSON.parse(value);
  } catch {
    return undefined;
  }
}

function stringFromPayload(
  value: unknown,
  paths: Array<readonly string[]>,
): string | undefined {
  for (const path of paths) {
    const result = stringAtPath(value, path);
    if (result) {
      return result;
    }
  }
  return undefined;
}

function stringAtPath(
  value: unknown,
  path: readonly string[],
): string | undefined {
  let current = value;

  for (const key of path) {
    if (!isRecord(current)) {
      return undefined;
    }
    current = current[key];
  }

  if (typeof current !== "string") {
    return undefined;
  }

  const normalized = current.trim();
  return normalized || undefined;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function truncateRuntimeActivityText(value: string) {
  const maxLength = 2_000;
  return value.length > maxLength
    ? `${value.slice(0, maxLength).trimEnd()}\n... truncated`
    : value;
}

function ChatMessageAttachments({
  attachmentsWithheld,
  files,
  onUploadedFileSelect,
}: Pick<ChatMessage, "attachmentsWithheld" | "files"> & {
  onUploadedFileSelect?: (
    file: NonNullable<ChatMessage["files"]>[number],
  ) => void;
}) {
  if (!files?.length) {
    return attachmentsWithheld ? (
      <div className="mb-2 flex max-w-full justify-end">
        <WithheldAttachmentChip />
      </div>
    ) : null;
  }

  const imageFiles = files.filter(isImageFile);
  const fileChips = files.filter((file) => !isImageFile(file));

  return (
    <div className="mb-2 flex max-w-full flex-col items-end gap-2">
      {imageFiles.length ? (
        <div className="flex max-w-full flex-wrap justify-end gap-2">
          {imageFiles.map((file, index) => (
            <ImageAttachmentPreview
              key={`${file.url}-${file.filename ?? "image"}-${index}`}
              file={file}
              onSelect={() =>
                onUploadedFileSelect?.((files ?? []).filter(isImageFile)[index])
              }
            />
          ))}
        </div>
      ) : null}
      {fileChips.length ? (
        <Attachments variant="inline" className="justify-end">
          {fileChips.map((file, index) => (
            <UploadedFileAttachment
              key={`${file.url}-${file.filename ?? "file"}-${index}`}
              file={file}
              onSelect={() => onUploadedFileSelect?.(file)}
            />
          ))}
        </Attachments>
      ) : null}
      {attachmentsWithheld ? <WithheldAttachmentChip /> : null}
    </div>
  );
}

function UploadedFileAttachment({
  file,
  onSelect,
}: {
  file: NonNullable<ChatMessage["files"]>[number];
  onSelect: () => void;
}) {
  const attachment = attachmentFromInput(file, file.url);
  return (
    <button
      type="button"
      aria-label={`Preview ${attachment.name}`}
      title={attachment.name}
      onClick={onSelect}
      className="inline-flex h-8 max-w-[220px] cursor-pointer items-center gap-1.5 rounded-md border border-[var(--workspace-divider)] bg-[var(--workspace-panel-bg)] px-1.5 text-sm font-medium text-[var(--workspace-native-text)] hover:border-[var(--workspace-native-accent)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--workspace-native-accent)]"
    >
      <span className="grid size-5 shrink-0 place-items-center rounded bg-[var(--workspace-control-bg)] text-[var(--workspace-native-text-muted)]">
        <ChatAttachmentIcon file={attachment} size={14} />
      </span>
      <span className="min-w-0 truncate">{attachment.name}</span>
    </button>
  );
}

function ImageAttachmentPreview({
  file,
  onSelect,
}: {
  file: NonNullable<ChatMessage["files"]>[number];
  onSelect: () => void;
}) {
  const imageLabel = file.filename || "Image attachment";
  const storedImageUrl = isStoredImageFile(file) ? file.url : null;
  const [previewAttempt, setPreviewAttempt] = useState(0);
  const [storedPreview, setStoredPreview] = useState<{
    source: string;
    url: string | null;
  } | null>(null);

  useEffect(() => {
    if (!storedImageUrl) return;
    let active = true;
    let objectUrl: string | undefined;
    setStoredPreview(null);

    void cockpitService.getResourceBlob(storedImageUrl).then(
      (blob) => {
        if (!active) return;
        objectUrl = URL.createObjectURL(blob);
        setStoredPreview({ source: storedImageUrl, url: objectUrl });
      },
      () => {
        if (active) setStoredPreview({ source: storedImageUrl, url: null });
      },
    );

    return () => {
      active = false;
      if (objectUrl) URL.revokeObjectURL(objectUrl);
    };
  }, [storedImageUrl, previewAttempt]);

  const previewUrl = storedImageUrl
    ? storedPreview?.source === storedImageUrl
      ? storedPreview.url
      : ""
    : file.url;

  if (storedPreview?.source === storedImageUrl && storedPreview.url === null) {
    return (
      <button
        aria-label={`Retry loading ${imageLabel}`}
        className="flex size-24 flex-col items-center justify-center gap-2 rounded-xl border border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] text-xs text-[var(--workspace-native-text-muted)]"
        type="button"
        onClick={() => setPreviewAttempt((attempt) => attempt + 1)}
      >
        <Icon name="gallery" className="size-5" />
        <span>Retry image</span>
      </button>
    );
  }

  if (!previewUrl) {
    return (
      <span
        aria-label={
          file.filename ? `Loading ${file.filename}` : "Loading image"
        }
        className="block size-24 animate-pulse rounded-xl border border-white/16 bg-white/12 shadow-[0_12px_24px_rgba(0,0,0,0.26)]"
        role="img"
      />
    );
  }

  return (
    <>
      <button
        aria-label={`Open ${imageLabel}`}
        className="group block size-24 overflow-hidden rounded-xl border border-white/18 bg-white/8 p-0 shadow-[0_12px_24px_rgba(0,0,0,0.28)] transition hover:border-white/34"
        type="button"
        onClick={onSelect}
      >
        <img
          alt={imageLabel}
          className="size-full object-cover transition duration-200 group-hover:scale-[1.03]"
          src={previewUrl}
        />
      </button>
    </>
  );
}

function ChatImagePreviewDialog({
  imageLabel,
  open,
  src,
  onOpenChange,
}: {
  imageLabel: string;
  open: boolean;
  src: string;
  onOpenChange: (open: boolean) => void;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <CoreDialogContent
        className="!fixed !left-1/2 !top-1/2 !z-[90] !w-[min(980px,calc(100vw-32px))] !max-w-[min(980px,calc(100vw-32px))] !-translate-x-1/2 !-translate-y-1/2 !border-0 !bg-transparent !p-0 !shadow-none !outline-none"
        overlayClassName="!fixed !inset-0 !z-[88] !bg-[#111]/85"
        showCloseButton={false}
      >
        <DialogTitle className="sr-only">{imageLabel}</DialogTitle>
        <div className="pointer-events-auto relative grid max-h-[calc(100dvh-32px)] place-items-center overflow-hidden rounded-[20px] border border-white/18 bg-[#101417]/92 p-3 shadow-[0_32px_90px_rgba(0,0,0,0.58)] backdrop-blur-xl">
          <button
            aria-label="Close image preview"
            className="absolute right-3 top-3 z-10 grid size-10 place-items-center rounded-xl border border-white/18 bg-black/34 text-white transition hover:bg-black/50"
            type="button"
            onClick={() => onOpenChange(false)}
          >
            <Icon name="close" className="size-5" />
          </button>
          <img
            alt={imageLabel}
            className="max-h-[calc(100dvh-56px)] max-w-full rounded-xl object-contain"
            src={src}
          />
        </div>
      </CoreDialogContent>
    </Dialog>
  );
}

function isStoredImageFile(file: NonNullable<ChatMessage["files"]>[number]) {
  return (
    file.mediaType.startsWith("image/") && file.url.startsWith("/attachments/")
  );
}

function isImageFile(file: NonNullable<ChatMessage["files"]>[number]) {
  return file.mediaType.startsWith("image/");
}

function ChatMessageTimestamp({ timestamp }: Pick<ChatMessage, "timestamp">) {
  if (!timestamp) {
    return null;
  }

  return (
    <span className="workspace-chat-message-time mt-2 block text-xs font-medium leading-none text-white/50">
      {timestamp}
    </span>
  );
}

function UserMessageActions({ message }: { message: ChatMessage }) {
  const [copied, setCopied] = useState(false);
  const timestamp = getMessageTimestamp(message);
  const text = message.text.trim();

  useEffect(() => {
    if (!copied) {
      return;
    }

    const resetTimer = window.setTimeout(() => setCopied(false), 1_500);
    return () => window.clearTimeout(resetTimer);
  }, [copied]);

  if (!timestamp && !text) {
    return null;
  }

  const handleCopy = async () => {
    try {
      await copyTextToClipboard(text);
      setCopied(true);
    } catch {
      toast.error("Message could not be copied.");
    }
  };

  return (
    <div className="workspace-user-message-actions pointer-events-none mt-2 flex min-h-5 items-center justify-end gap-2 pr-1 text-white/50 opacity-0 transition-opacity duration-150 group-focus-within:pointer-events-auto group-focus-within:opacity-100 group-hover:pointer-events-auto group-hover:opacity-100">
      {timestamp ? (
        <time
          className="text-xs font-medium leading-none"
          dateTime={message.createdAt}
        >
          {timestamp}
        </time>
      ) : null}
      {text ? (
        <button
          aria-label={copied ? "Message copied" : "Copy message"}
          className="rounded-md p-1 text-white/55 transition-colors hover:bg-white/10 hover:text-white focus-visible:outline-none"
          onClick={handleCopy}
          title={copied ? "Copied" : "Copy message"}
          type="button"
        >
          {copied ? (
            <Check aria-hidden="true" className="size-4" />
          ) : (
            <Copy aria-hidden="true" className="size-4" />
          )}
        </button>
      ) : null}
    </div>
  );
}

function getMessageTimestamp(message: ChatMessage) {
  if (message.timestamp) {
    return message.timestamp;
  }

  if (!message.createdAt) {
    return null;
  }

  const createdAt = Date.parse(message.createdAt);
  return Number.isFinite(createdAt) ? formatChatTimestamp(createdAt) : null;
}

async function copyTextToClipboard(text: string) {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return;
    } catch {
      // Electron can expose the Clipboard API while denying renderer writes.
      // Fall through to the selection-based copy that still runs in the
      // original click interaction.
    }
  }

  const textarea = document.createElement("textarea");
  const previouslyFocusedElement =
    document.activeElement instanceof HTMLElement
      ? document.activeElement
      : null;
  textarea.value = text;
  textarea.setAttribute("readonly", "");
  textarea.style.position = "fixed";
  textarea.style.left = "-9999px";
  textarea.style.top = "0";
  document.body.appendChild(textarea);
  textarea.focus({ preventScroll: true });
  textarea.select();

  try {
    if (!document.execCommand("copy")) {
      throw new Error("Copy command failed");
    }
  } finally {
    textarea.remove();
    previouslyFocusedElement?.focus({ preventScroll: true });
  }
}
