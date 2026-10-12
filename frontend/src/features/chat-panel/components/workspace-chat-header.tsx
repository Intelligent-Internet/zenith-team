import { FileCheck2, Hash, LockKeyhole } from "@/shared/ui/icons";
import type { ReactNode, Ref } from "react";
import { Link } from "react-router";
import type { WorkspaceChatContext } from "../types";
import type { TeamNodeData } from "@/features/home/types";
import type { ConversationDockTab } from "@/state";
import { TaskStatusIcon } from "@/features/company-plan/components/task-metadata-icons";
import {
  taskChatState,
  type TaskChatActivity,
} from "@/features/company-plan/model/task-work-state";
import { MemberProfileTrigger } from "@/shared/chat/member-profile";
import type { FoldAvatarState } from "@/shared/ui/agent-avatar/fold-avatar";
import { ChatAgentAvatar } from "./chat-agent-avatar";
import { ConversationArtifactsToggle } from "./artifacts/conversation-artifacts-toggle";

export function WorkspaceChatHeader({
  context,
  avatarState,
  activeTeam,
  conversationTab,
  onReviewTask,
  onApprovePlan,
  headerActions,
  onToggleArtifacts,
  artifactsOpen,
  artifactCount,
  artifactsHasMore,
  artifactsPanelId,
  artifactsButtonRef,
  taskActivity,
}: {
  context: WorkspaceChatContext;
  avatarState: FoldAvatarState;
  activeTeam: TeamNodeData;
  conversationTab?: ConversationDockTab;
  onReviewTask?: () => void;
  onApprovePlan?: () => void;
  headerActions?: ReactNode;
  onToggleArtifacts?: () => void;
  artifactsOpen: boolean;
  artifactCount: number;
  artifactsHasMore?: boolean;
  artifactsPanelId?: string;
  artifactsButtonRef: Ref<HTMLButtonElement>;
  taskActivity?: TaskChatActivity;
}) {
  const { task, taskLabel, agentName } = context;
  const chatState = task ? taskChatState(task, taskActivity ?? {}) : undefined;
  const agentId = conversationTab?.agentId ?? activeTeam.agentId;
  return (
    <header className="workspace-chat-title" data-task={Boolean(task)}>
      <div className="workspace-chat-title-identity">
        <MemberProfileTrigger
          className="workspace-chat-title-avatar"
          profile={{
            id: agentId ?? agentName,
            kind: "agent",
            name: agentName,
            title: activeTeam.agentRole,
          }}
        >
          <ChatAgentAvatar
            agentId={agentId ?? agentName}
            size={40}
            state={avatarState}
          />
        </MemberProfileTrigger>
        <div className="workspace-chat-title-main">
          <h1 title={task?.title ?? conversationTab?.title ?? agentName}>
            {task?.title ?? conversationTab?.title ?? agentName}
          </h1>
          <div className="workspace-chat-title-meta">
            {task ? (
              <>
                <div className="workspace-chat-task-reference">
                  <Link
                    aria-label={`Task #${taskLabel}`}
                    className="workspace-task-tag"
                    to={`/tasks?taskId=${task.id}&taskView=full`}
                    title={`#${taskLabel}`}
                  >
                    <Hash size={12} />
                    <span>{taskLabel}</span>
                  </Link>
                  <span aria-hidden="true">·</span>
                  <span
                    className="workspace-chat-title-agent"
                    title={agentName}
                  >
                    {agentName}
                  </span>
                </div>
                {chatState ? (
                  <span
                    className="workspace-chat-task-state"
                    data-attention={chatState.needsViewer}
                    role="status"
                    title={`Task: ${chatState.label}`}
                  >
                    <TaskStatusIcon status={chatState.status} />
                    <span>{chatState.label}</span>
                  </span>
                ) : null}
              </>
            ) : (
              <>
                <span>{activeTeam.agentRole}</span>
                <span>·</span>
                <span
                  className="workspace-chat-privacy"
                  aria-label="Private chat"
                  title="Only you and this agent can see this chat"
                >
                  <LockKeyhole aria-hidden="true" size={13} />
                </span>
              </>
            )}
          </div>
        </div>
      </div>
      <div className="workspace-chat-title-actions">
        {task && onReviewTask ? (
          <button
            className="dashboard-button workspace-chat-review"
            type="button"
            onClick={onReviewTask}
          >
            <FileCheck2 size={16} aria-hidden /> Review changes
          </button>
        ) : null}
        {headerActions}
        {onApprovePlan ? (
          <button
            type="button"
            className="dashboard-button"
            onClick={onApprovePlan}
          >
            <FileCheck2 size={15} />
            Approve plan
          </button>
        ) : null}
        {onToggleArtifacts ? (
          <ConversationArtifactsToggle
            ref={artifactsButtonRef}
            count={artifactCount}
            hasMore={artifactsHasMore}
            open={artifactsOpen}
            panelId={artifactsPanelId}
            onToggle={onToggleArtifacts}
          />
        ) : null}
      </div>
    </header>
  );
}
