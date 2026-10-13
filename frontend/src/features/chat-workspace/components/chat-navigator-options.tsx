import { useState } from "react";
import { Tooltip } from "@/shared/ui/tooltip";
import {
  Archive,
  Bot,
  Ellipsis,
  EyeOff,
  Hash,
  MessagesSquare,
  Plus,
  Settings,
} from "@/shared/ui/icons";
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
  DropdownMenuSub,
  DropdownMenuSubTrigger,
  DropdownMenuSubContent,
  DropdownMenuPortal,
} from "@/shared/ui/core/dropdown-menu";
import type { DiscussionNavigation } from "@/features/discussions/model/use-discussion-navigation";
import type { ChatNavigatorBrowseState } from "../model/chat-navigator-browse";
import "../styles/conversation-pin-menu.css";

export function ChatNavigatorOptions({
  browse,
  discussions,
  onBrowseChange,
  onCreateGroup,
}: {
  browse: ChatNavigatorBrowseState;
  discussions?: DiscussionNavigation;
  onBrowseChange: (browse: ChatNavigatorBrowseState) => void;
  onCreateGroup?: () => void;
}) {
  const [menuOpen, setMenuOpen] = useState(false);
  return (
    <DropdownMenu open={menuOpen} onOpenChange={setMenuOpen}>
      <Tooltip label="Chat options" disabled={menuOpen}>
        <DropdownMenuTrigger asChild>
          <button
            aria-label="Chat options"
            className="workspace-chat-navigator__icon-button"
            type="button"
          >
            <Ellipsis size={18} aria-hidden />
          </button>
        </DropdownMenuTrigger>
      </Tooltip>
      <DropdownMenuContent
        align="end"
        className="conversation-pin-menu workspace-chat-navigator__menu"
      >
        <DropdownMenuRadioGroup
          value={browse.view}
          onValueChange={(view) => {
            if (view === "recent" || view === "agents" || view === "archived")
              onBrowseChange({ view, agentId: null, query: "" });
          }}
        >
          <DropdownMenuRadioItem
            className="conversation-pin-menu__item"
            value="recent"
          >
            <MessagesSquare size={16} aria-hidden /> All chats
          </DropdownMenuRadioItem>
          <DropdownMenuRadioItem
            className="conversation-pin-menu__item"
            value="agents"
          >
            <Bot size={16} aria-hidden /> By agent
          </DropdownMenuRadioItem>
          <DropdownMenuRadioItem
            className="conversation-pin-menu__item"
            value="archived"
          >
            <Archive size={16} aria-hidden /> Archived chats
          </DropdownMenuRadioItem>
        </DropdownMenuRadioGroup>
        {discussions ? (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuCheckboxItem
              className="conversation-pin-menu__item"
              checked={discussions.includeHidden}
              onCheckedChange={(checked) =>
                discussions.setIncludeHidden(checked === true)
              }
            >
              <EyeOff size={16} aria-hidden /> Show hidden chats
            </DropdownMenuCheckboxItem>
            <DropdownMenuSub>
              <DropdownMenuSubTrigger className="conversation-pin-menu__item gap-2">
                <Hash size={15} aria-hidden />
                Channels
              </DropdownMenuSubTrigger>
              <DropdownMenuPortal>
                <DropdownMenuSubContent className="conversation-pin-menu workspace-chat-navigator__menu">
                  <DropdownMenuCheckboxItem
                    className="conversation-pin-menu__item"
                    checked={discussions.archived}
                    onCheckedChange={(checked) => {
                      discussions.setArchived(checked === true);
                      onBrowseChange({
                        view: "recent",
                        agentId: null,
                        query: "",
                      });
                    }}
                  >
                    <Archive size={15} aria-hidden />
                    Show archived
                  </DropdownMenuCheckboxItem>
                  {discussions.canManage ? (
                    <>
                      <DropdownMenuCheckboxItem
                        className="conversation-pin-menu__item"
                        checked={discussions.managing}
                        onCheckedChange={(checked) => {
                          discussions.setManage(checked === true);
                          onBrowseChange({
                            view: "recent",
                            agentId: null,
                            query: "",
                          });
                        }}
                      >
                        <Settings size={15} aria-hidden />
                        Manage channels
                      </DropdownMenuCheckboxItem>
                      <DropdownMenuSeparator />
                      <DropdownMenuItem
                        className="conversation-pin-menu__item"
                        onSelect={onCreateGroup}
                      >
                        <Plus size={15} aria-hidden />
                        Create channel
                      </DropdownMenuItem>
                    </>
                  ) : null}
                </DropdownMenuSubContent>
              </DropdownMenuPortal>
            </DropdownMenuSub>
          </>
        ) : null}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

export function ChatNavigatorPageStatus({
  label,
  query,
}: {
  label: "direct messages" | "channels";
  query: {
    isError: boolean;
    isFetching: boolean;
    isLoading: boolean;
    currentData?: unknown;
    hasNextPage: boolean;
    refetch: () => unknown;
    fetchNextPage: () => unknown;
  };
}) {
  if (query.isError)
    return (
      <p className="workspace-chat-navigator__status" role="alert">
        Couldn’t load {label}.{" "}
        <button type="button" onClick={() => void query.refetch()}>
          Retry
        </button>
      </p>
    );
  if (query.isLoading || (query.isFetching && !query.currentData))
    return (
      <p className="workspace-chat-navigator__status" role="status">
        Loading {label}…
      </p>
    );
  if (!query.hasNextPage) return null;
  return (
    <button
      className="workspace-chat-navigator__more"
      disabled={query.isFetching}
      type="button"
      onClick={() => void query.fetchNextPage()}
    >
      {query.isFetching ? "Loading…" : `Load more ${label}`}
    </button>
  );
}
