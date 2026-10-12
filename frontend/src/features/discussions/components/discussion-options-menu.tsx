import { useState } from "react";
import type { components } from "@/shared/api/generated/api.generated";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuPortal,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "@/shared/ui/dropdown-menu";
import {
  Bell,
  Bot,
  Info,
  LogOut,
  MoreHorizontal,
  Pin,
  PinOff,
} from "@/shared/ui/icons";
import { Tooltip } from "@/shared/ui/tooltip";

type NotificationLevel =
  components["schemas"]["DiscussionUserState"]["notificationLevel"];

export function DiscussionOptionsMenu({
  isChannel,
  pinned,
  notificationLevel,
  disabled = false,
  pending = false,
  onAbout,
  onAgents,
  onTogglePin,
  onNotificationChange,
  onLeave,
}: {
  isChannel: boolean;
  pinned: boolean;
  notificationLevel: NotificationLevel;
  disabled?: boolean;
  pending?: boolean;
  onAbout?: () => void;
  onAgents?: () => void;
  onTogglePin: () => void;
  onNotificationChange: (level: NotificationLevel) => void;
  onLeave?: () => void;
}) {
  const [open, setOpen] = useState(false);
  const actionsDisabled = disabled || pending;

  return (
    <DropdownMenu open={open} onOpenChange={setOpen}>
      <Tooltip label="More options" disabled={disabled || open}>
        <DropdownMenuTrigger asChild>
          <button
            type="button"
            className="discussion-icon-button"
            aria-label="More options"
            disabled={disabled}
          >
            <MoreHorizontal size={18} aria-hidden />
          </button>
        </DropdownMenuTrigger>
      </Tooltip>
      <DropdownMenuContent align="end" className="w-56" collisionPadding={12}>
        {onAbout ? (
          <DropdownMenuItem disabled={disabled} onSelect={onAbout}>
            <Info aria-hidden />
            About channel
          </DropdownMenuItem>
        ) : null}
        {onAgents ? (
          <DropdownMenuItem disabled={disabled} onSelect={onAgents}>
            <Bot aria-hidden />
            Agents
          </DropdownMenuItem>
        ) : null}
        {onAbout || onAgents ? <DropdownMenuSeparator /> : null}
        <DropdownMenuItem disabled={actionsDisabled} onSelect={onTogglePin}>
          {pinned ? <PinOff aria-hidden /> : <Pin aria-hidden />}
          {pinned ? "Unpin conversation" : "Pin conversation"}
        </DropdownMenuItem>
        <DropdownMenuSub>
          <DropdownMenuSubTrigger
            className="gap-2 rounded-md px-2.5 text-[13px] font-medium data-[disabled]:pointer-events-none data-[disabled]:opacity-50"
            disabled={actionsDisabled}
          >
            <Bell size={16} className="text-muted-foreground" aria-hidden />
            Notifications
          </DropdownMenuSubTrigger>
          <DropdownMenuPortal>
            <DropdownMenuSubContent
              className="w-72 max-w-[calc(100vw-24px)]"
              collisionPadding={12}
            >
              <DropdownMenuLabel>Notify you about…</DropdownMenuLabel>
              <DropdownMenuRadioGroup
                value={
                  !isChannel && notificationLevel === "mentions"
                    ? "all"
                    : notificationLevel
                }
                onValueChange={(value) => {
                  if (
                    value === "all" ||
                    value === "muted" ||
                    (isChannel && value === "mentions")
                  )
                    onNotificationChange(value);
                }}
              >
                <DropdownMenuRadioItem value="all" disabled={actionsDisabled}>
                  <span>
                    {isChannel ? "All new posts" : "All messages"}
                    {isChannel ? (
                      <small className="block text-xs font-normal text-muted-foreground">
                        Messages and threads you follow
                      </small>
                    ) : null}
                  </span>
                </DropdownMenuRadioItem>
                {isChannel ? (
                  <DropdownMenuRadioItem
                    value="mentions"
                    disabled={actionsDisabled}
                  >
                    <span>
                      Just mentions
                      <small className="block text-xs font-normal text-muted-foreground">
                        Mentions and threads you follow
                      </small>
                    </span>
                  </DropdownMenuRadioItem>
                ) : null}
                <DropdownMenuRadioItem value="muted" disabled={actionsDisabled}>
                  <span>
                    Mute notifications
                    <small className="block text-xs font-normal text-muted-foreground">
                      {isChannel
                        ? "Keep mentions in Inbox, without push"
                        : "Keep messages in Inbox, without push"}
                    </small>
                  </span>
                </DropdownMenuRadioItem>
              </DropdownMenuRadioGroup>
            </DropdownMenuSubContent>
          </DropdownMenuPortal>
        </DropdownMenuSub>
        {onLeave ? (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              className="discussion-danger"
              variant="destructive"
              disabled={actionsDisabled}
              onSelect={onLeave}
            >
              <LogOut className="text-current" aria-hidden />
              Leave channel
            </DropdownMenuItem>
          </>
        ) : null}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
