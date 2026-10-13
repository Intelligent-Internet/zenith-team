import { useEffect, useId, useMemo, useRef, useState } from "react";
import * as HoverCard from "@radix-ui/react-hover-card";
import type { DiscussionMessage } from "../api/discussions-api";
import { discussionsApi } from "../api/discussions-api";
import { discussionError } from "../model/discussion-runtime";
import { useDiscussionUpdates } from "./discussion-provider";
import { DiscussionAvatar } from "./discussion-identity";

type Reaction = DiscussionMessage["reactionSummary"][number];

export function DiscussionReactionPeople({
  conversationId,
  messageId,
  reaction,
  disabled,
  canReact,
  onToggle,
}: {
  conversationId: string;
  messageId: string;
  reaction: Reaction;
  disabled: boolean;
  canReact: boolean;
  onToggle: () => void;
}) {
  const [open, setOpen] = useState(false);
  const popoverId = useId();
  const action = canReact
    ? reaction.reacted
      ? "Remove your reaction"
      : "Add your reaction"
    : "Show who reacted";

  return (
    <HoverCard.Root
      open={open}
      onOpenChange={setOpen}
      openDelay={180}
      closeDelay={100}
    >
      <HoverCard.Trigger asChild>
        <button
          type="button"
          className={`discussion-reaction ${reaction.reacted ? "discussion-reaction--mine" : ""}`}
          aria-label={`${reaction.emoji}, ${reaction.count} ${reaction.count === 1 ? "reaction" : "reactions"}${reaction.reacted ? ", you reacted" : ""}. ${action}`}
          aria-pressed={canReact ? reaction.reacted : undefined}
          aria-describedby={open ? popoverId : undefined}
          disabled={disabled}
          onFocus={() => setOpen(true)}
          onBlur={() => setOpen(false)}
          onClick={() => (canReact ? onToggle() : setOpen(true))}
        >
          <span>{reaction.emoji}</span>
          <span>{reaction.count}</span>
        </button>
      </HoverCard.Trigger>
      <HoverCard.Portal>
        <HoverCard.Content
          id={popoverId}
          role="tooltip"
          className="z-[90] w-[max-content] min-w-[170px] max-w-[min(260px,_calc(100vw_-_24px))] p-[10px_12px] [border:1px_solid_var(--workspace-divider,_#26374b)] rounded-[var(--workspace-radius-overlay,_14px)] [background:var(--workspace-overlay-bg,_#14263a)] text-[var(--workspace-native-text,_#e6edf6)] [box-shadow:var(--workspace-overlay-shadow)] text-[length:12px]"
          side="top"
          sideOffset={8}
          collisionPadding={8}
        >
          <ReactionPeopleContent
            conversationId={conversationId}
            messageId={messageId}
            emoji={reaction.emoji}
            count={reaction.count}
          />
        </HoverCard.Content>
      </HoverCard.Portal>
    </HoverCard.Root>
  );
}

function ReactionPeopleContent({
  conversationId,
  messageId,
  emoji,
  count,
}: {
  conversationId: string;
  messageId: string;
  emoji: string;
  count: number;
}) {
  const runtime = useDiscussionUpdates();
  const ready = runtime.status(conversationId)?.phase === "ready";
  const busy = runtime.isMutating(conversationId);
  const revision = runtime.state(conversationId)?.conversationRevision;
  const query = discussionsApi.useGetDiscussionReactionsInfiniteQuery(
    { ...runtime.session, conversationId, messageId },
    { skip: !ready },
  );
  const denied =
    query.isError &&
    "status" in query.error &&
    [403, 404].includes(Number(query.error.status));
  useEffect(() => {
    if (denied) runtime.schedule();
  }, [denied, runtime]);

  const attempted = useRef<number | undefined>(undefined);
  const pages = query.currentData?.pages;
  const latestRevision = pages?.reduce(
    (latest, page) => Math.max(latest, page.conversationRevision),
    0,
  );
  useEffect(() => {
    if (
      !ready ||
      busy ||
      query.isFetching ||
      !latestRevision ||
      !revision ||
      latestRevision === revision ||
      attempted.current === revision
    )
      return;
    attempted.current = revision;
    void query.refetch();
  }, [ready, busy, query.isFetching, query.refetch, latestRevision, revision]);

  const people = useMemo(() => {
    const users = new Map<
      string | null,
      NonNullable<typeof pages>[number]["items"][number]["users"][number]
    >();
    for (const page of pages ?? []) {
      if (page.conversationRevision !== latestRevision) continue;
      for (const item of page.items) {
        if (item.emoji !== emoji) continue;
        for (const user of item.users) users.set(user.id, user);
      }
    }
    return [...users.values()];
  }, [pages, latestRevision, emoji]);

  useEffect(() => {
    if (
      !ready ||
      denied ||
      query.isError ||
      query.isFetching ||
      !query.hasNextPage ||
      people.length >= Math.min(count, 5)
    )
      return;
    void query.fetchNextPage();
  }, [
    ready,
    denied,
    query.isError,
    query.isFetching,
    query.hasNextPage,
    query.fetchNextPage,
    people.length,
    count,
  ]);

  const shown = people.slice(0, 5);
  const remaining = Math.max(0, count - shown.length);

  return (
    <>
      <div className="discussion-reaction-popover-heading">
        <span aria-hidden="true">{emoji}</span>
        <strong>
          {count} {count === 1 ? "reaction" : "reactions"}
        </strong>
      </div>
      {!ready ? (
        <span className="block text-[var(--workspace-native-text-muted,_#98aabd)] text-[length:12px]">
          Checking access…
        </span>
      ) : denied ? (
        <span className="block text-[var(--workspace-native-text-muted,_#98aabd)] text-[length:12px]">
          Reactions unavailable
        </span>
      ) : query.isError ? (
        <span className="block text-[var(--workspace-native-text-muted,_#98aabd)] text-[length:12px]">
          {discussionError(query.error)}
        </span>
      ) : query.isLoading && !shown.length ? (
        <span className="block text-[var(--workspace-native-text-muted,_#98aabd)] text-[length:12px]">
          Loading people…
        </span>
      ) : (
        <>
          {shown.map((person) => (
            <div
              className="discussion-reaction-popover-person"
              key={person.id ?? person.displayName}
            >
              <DiscussionAvatar
                name={person.displayName}
                src={person.avatarUrl}
                userId={person.id}
              />
              <span>
                {person.displayName}
                {person.id === runtime.session.userId ? " (you)" : ""}
              </span>
            </div>
          ))}
          {remaining > 0 ? (
            <span className="block text-[var(--workspace-native-text-muted,_#98aabd)] text-[length:12px]">
              {query.isFetching && shown.length < 5
                ? "Loading more people…"
                : `and ${remaining} ${remaining === 1 ? "other" : "others"}`}
            </span>
          ) : null}
        </>
      )}
    </>
  );
}
