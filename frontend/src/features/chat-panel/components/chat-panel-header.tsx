import {
  chatPanelMenuLayerClassName,
  headerIconClassName,
  iconButtonClassName,
} from "@/features/chat-panel/constants";
import { Bot, FileCheck2 } from "@/shared/ui/icons";
import type { ReactNode } from "react";
import type { TeamNodeData } from "@/features/home/types";
import { MemberProfileTrigger } from "@/shared/chat/member-profile";
import { cx } from "@/shared/lib/cx";
import type { FoldAvatarState } from "@/shared/ui/agent-avatar/fold-avatar";
import { ChatAgentAvatar } from "./chat-agent-avatar";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/shared/ui/dropdown-menu";
import { Icon } from "@/shared/ui/icon";
import type { ConversationDockTab } from "@/state";

type ChatPanelHeaderProps = {
  activeTeam: TeamNodeData;
  avatarState?: FoldAvatarState;
  conversationTab?: ConversationDockTab;
  followPointer?: boolean;
  headerActions?: ReactNode;
  isExpanded: boolean;
  isWideLayout: boolean;
  onApproveCompanyDashboard?: () => void;
  onClose: () => void;
  onReviewTask?: () => void;
  onToggleExpanded: () => void;
  reserveMobileNavigatorSpace?: boolean;
  showWindowControls?: boolean;
};

export function ChatPanelHeader({
  activeTeam,
  avatarState = "idle",
  conversationTab,
  followPointer = false,
  headerActions,
  isExpanded,
  isWideLayout,
  onApproveCompanyDashboard,
  onClose,
  onReviewTask,
  onToggleExpanded,
  reserveMobileNavigatorSpace = false,
  showWindowControls = true,
}: ChatPanelHeaderProps) {
  const agentId = conversationTab?.agentId ?? activeTeam.agentId;
  const avatarSrc = conversationTab
    ? conversationTab.avatarSrc
    : activeTeam.iconSrc;
  const title = conversationTab?.title ?? activeTeam.label;
  const subtitle = conversationTab?.subtitle ?? activeTeam.agentRole;
  const hasInlineActions = Boolean(
    (headerActions || onReviewTask) && !showWindowControls,
  );
  const directProfile =
    conversationTab?.kind === "direct" && conversationTab.agentId
      ? {
          id: conversationTab.agentId,
          kind: "agent" as const,
          name: title,
          title: subtitle.replace(/ · Direct message$/, ""),
          avatarSrc,
        }
      : undefined;
  const avatar = agentId ? (
    <ChatAgentAvatar
      agentId={agentId}
      className="workspace-conversation-avatar"
      followPointer={followPointer}
      size={40}
      state={avatarState}
    />
  ) : (
    <span
      className="workspace-conversation-avatar grid size-[46px] shrink-0 place-items-center overflow-hidden rounded-xl bg-white"
      aria-hidden="true"
    >
      {avatarSrc ? (
        <img
          className="workspace-conversation-avatar__image h-[46px] w-[46px] max-w-none object-cover object-[50%_12%]"
          src={avatarSrc}
          alt=""
          draggable={false}
        />
      ) : (
        <Bot className="text-[#0F233F]" size={22} />
      )}
    </span>
  );

  return (
    <header
      className={cx(
        "workspace-conversation-header relative flex w-full shrink-0 items-start gap-4",
        isWideLayout ? "h-[90px] px-0 pt-5" : "h-[78px] px-4 pt-4",
      )}
    >
      <div
        className={cx(
          "workspace-conversation-identity flex min-w-0 items-start gap-3",
          hasInlineActions && "flex-1",
          showWindowControls && "pr-[136px]",
          isWideLayout && "mx-auto w-full max-w-[880px] px-6",
          reserveMobileNavigatorSpace && "max-[899px]:pl-16",
        )}
      >
        {directProfile ? (
          <MemberProfileTrigger className="shrink-0" profile={directProfile}>
            {avatar}
          </MemberProfileTrigger>
        ) : (
          avatar
        )}
        <div className="min-w-0 pt-0.5">
          <h2 className="m-0 truncate text-sm font-bold leading-[19px] text-white">
            {directProfile ? (
              <MemberProfileTrigger
                className="block max-w-full truncate text-left"
                profile={directProfile}
              >
                <span className="truncate">{title}</span>
              </MemberProfileTrigger>
            ) : (
              title
            )}
          </h2>
          <p className="workspace-conversation-subtitle mt-1 inline-flex h-6 max-w-full items-center truncate rounded-full bg-white/10 px-3 text-[12px] font-bold leading-none text-white">
            {subtitle}
          </p>
        </div>
      </div>
      <div
        className={cx(
          "flex shrink-0 items-center gap-4",
          hasInlineActions
            ? "ml-auto self-center"
            : cx("absolute", isWideLayout ? "right-6 top-5" : "right-4 top-4"),
        )}
      >
        {headerActions}
        {onReviewTask ? (
          <button
            aria-label="Review task changes"
            className="workspace-chat-review-button inline-flex h-9 shrink-0 cursor-pointer items-center justify-center gap-2 rounded-lg border border-white/20 bg-white/10 px-3 text-xs font-semibold text-white transition-colors hover:bg-white/16"
            type="button"
            onClick={onReviewTask}
          >
            <FileCheck2 aria-hidden="true" className="size-4 shrink-0" />
            <span className="whitespace-nowrap">
              Review<span className="max-[479px]:hidden"> changes</span>
            </span>
          </button>
        ) : null}
        {onApproveCompanyDashboard ? (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <button
                className={iconButtonClassName}
                type="button"
                aria-label="More chat actions"
              >
                <Icon
                  name="more"
                  className={headerIconClassName}
                  aria-hidden="true"
                />
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent
              align="end"
              className={cx(
                chatPanelMenuLayerClassName,
                "!min-w-[132px] !border-white/16 !bg-[#212121]/95 !p-1.5 !text-white !shadow-[0_18px_42px_rgba(0,0,0,0.38),inset_0_1px_0_rgba(255,255,255,0.12)] backdrop-blur-xl",
              )}
              sideOffset={10}
            >
              <DropdownMenuItem
                className="cursor-pointer rounded-xl px-3 py-2 text-xs font-bold text-white focus:!bg-white/12 focus:!text-white"
                onSelect={() => onApproveCompanyDashboard()}
              >
                Approve
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        ) : showWindowControls ? (
          <button
            className={iconButtonClassName}
            type="button"
            aria-label="More chat actions"
          >
            <Icon
              name="more"
              className={headerIconClassName}
              aria-hidden="true"
            />
          </button>
        ) : null}
        {showWindowControls ? (
          <>
            <button
              className={iconButtonClassName}
              type="button"
              aria-label={isExpanded ? "Collapse chat" : "Expand chat"}
              aria-pressed={isExpanded}
              title={isExpanded ? "Collapse" : "Expand"}
              onClick={onToggleExpanded}
            >
              {isExpanded ? (
                <Icon
                  name="shrink"
                  className={headerIconClassName}
                  aria-hidden="true"
                />
              ) : (
                <Icon
                  name="expand"
                  className={headerIconClassName}
                  aria-hidden="true"
                />
              )}
            </button>
            <button
              className={iconButtonClassName}
              type="button"
              aria-label="Close conversation"
              title="Close"
              onClick={onClose}
            >
              <Icon
                name="close"
                className={headerIconClassName}
                aria-hidden="true"
              />
            </button>
          </>
        ) : null}
      </div>
      <span
        aria-hidden="true"
        className={cx(
          "workspace-conversation-divider pointer-events-none absolute bottom-0 h-px bg-[linear-gradient(90deg,rgba(255,255,255,0)_0%,#FFF_50%,rgba(255,255,255,0)_100%)]",
          isWideLayout
            ? "left-1/2 w-full max-w-[880px] -translate-x-1/2"
            : "left-4 right-4",
        )}
      />
    </header>
  );
}
