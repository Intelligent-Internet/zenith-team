import {
  Archive,
  ArchiveRestore,
  Ellipsis,
  LoaderCircle,
  PencilLine,
  Pin,
  PinOff,
} from "@/shared/ui/icons";
import { useRef } from "react";

import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/shared/ui/core/dropdown-menu";
import "@/features/chat-workspace/styles/conversation-pin-menu.css";

export function ConversationPinMenu({
  disabled,
  pending,
  pinned,
  targetKey,
  title,
  onTogglePin,
  archived,
  onToggleArchive,
  onRename,
}: {
  disabled: boolean;
  pending: boolean;
  pinned: boolean;
  targetKey: string;
  title: string;
  onTogglePin: () => void;
  archived?: boolean;
  onToggleArchive?: () => void;
  onRename?: (trigger: HTMLButtonElement) => void;
}) {
  const triggerRef = useRef<HTMLButtonElement>(null);
  const openingRenameRef = useRef(false);
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          ref={triggerRef}
          aria-label={`Actions for ${title}`}
          aria-busy={pending}
          className="conversation-pin-menu__trigger"
          data-conversation-actions-key={targetKey}
          disabled={disabled}
          title="Conversation actions"
          type="button"
        >
          {pending ? (
            <LoaderCircle
              aria-hidden="true"
              className="animate-spin"
              size={14}
            />
          ) : (
            <Ellipsis aria-hidden="true" size={16} />
          )}
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent
        align="end"
        className="conversation-pin-menu"
        sideOffset={6}
        onCloseAutoFocus={(event) => {
          if (openingRenameRef.current) {
            event.preventDefault();
            openingRenameRef.current = false;
          }
        }}
      >
        {onRename ? (
          <DropdownMenuItem
            className="conversation-pin-menu__item"
            disabled={disabled}
            onSelect={() => {
              if (!triggerRef.current) return;
              openingRenameRef.current = true;
              onRename(triggerRef.current);
            }}
          >
            <PencilLine aria-hidden="true" />
            Rename conversation
          </DropdownMenuItem>
        ) : null}
        {!archived ? (
          <DropdownMenuItem
            className="conversation-pin-menu__item"
            disabled={disabled}
            onSelect={onTogglePin}
          >
            {pinned ? (
              <PinOff aria-hidden="true" />
            ) : (
              <Pin aria-hidden="true" />
            )}
            {pinned ? "Unpin conversation" : "Pin conversation"}
          </DropdownMenuItem>
        ) : null}
        {onToggleArchive ? (
          <DropdownMenuItem
            className="conversation-pin-menu__item"
            disabled={disabled}
            onSelect={onToggleArchive}
          >
            {archived ? (
              <ArchiveRestore aria-hidden="true" />
            ) : (
              <Archive aria-hidden="true" />
            )}
            {archived ? "Restore conversation" : "Archive conversation"}
          </DropdownMenuItem>
        ) : null}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
