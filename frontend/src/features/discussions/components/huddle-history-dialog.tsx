import { useState } from "react";
import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import {
  useGetHuddleAIQuery,
  useGetHuddleHistoryInfiniteQuery,
} from "@/features/huddles/huddle-ai-api";
import { HuddleWorkProgress } from "@/features/huddles/huddle-work-progress";
import "@/features/huddles/huddle-work-proposals.css";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/shared/ui/core/dialog";
import { Button } from "@/shared/ui/core/button";
import type { DiscussionMessage } from "../api/discussions-api";
import { HuddleTranscriptMessage } from "./huddle-transcript-message";

export function HuddleHistoryDialog({
  companyId,
  conversationId,
  call,
  onClose,
}: {
  companyId: string;
  conversationId: string;
  call: NonNullable<DiscussionMessage["huddle"]>;
  onClose: () => void;
}) {
  const session = useSessionSnapshot();
  const [view, setView] = useState<"chat" | "work">("chat");
  const history = useGetHuddleHistoryInfiniteQuery(
    { companyId, conversationId, userId: session.userId ?? "" },
    {
      skip: !session.userId,
      pollingInterval: 5000,
      skipPollingIfUnfocused: true,
      refetchOnMountOrArgChange: true,
    },
  );
  const authorized = !!history.currentData && !history.isError;
  const work = useGetHuddleAIQuery(
    { companyId, huddleId: call.id },
    {
      skip: !authorized || view !== "work",
      refetchOnMountOrArgChange: true,
      pollingInterval: view === "work" && authorized ? 10000 : 0,
      skipPollingIfUnfocused: true,
    },
  );
  const messages = authorized
    ? [
        ...new Map(
          history
            .currentData!.pages.flatMap((page) => page.items)
            .map((message) => [message.id, message]),
        ).values(),
      ].sort((a, b) => a.seq - b.seq)
    : [];
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DialogContent className="huddle-history-dialog">
        <DialogHeader>
          <DialogTitle>Huddle conversation</DialogTitle>
          <DialogDescription>
            {new Date(call.startedAt).toLocaleString()} · Saved chat and Agent
            transcripts
          </DialogDescription>
        </DialogHeader>
        <div className="huddle-history-tabs" aria-label="History view">
          <button
            type="button"
            aria-pressed={view === "chat"}
            onClick={() => setView("chat")}
          >
            Conversation
          </button>
          <button
            type="button"
            aria-pressed={view === "work"}
            onClick={() => setView("work")}
          >
            Work proposals
          </button>
        </div>
        <div className="huddle-history-content">
          {history.isError ? (
            <div role="alert">
              <p>History is unavailable or your access has changed.</p>
              <Button variant="outline" onClick={() => void history.refetch()}>
                Try again
              </Button>
            </div>
          ) : !authorized ? (
            <p role="status">Loading conversation…</p>
          ) : view === "chat" ? (
            <>
              {history.hasNextPage && (
                <Button
                  variant="outline"
                  disabled={history.isFetching}
                  onClick={() => void history.fetchNextPage()}
                >
                  Load earlier messages
                </Button>
              )}
              {messages.length === 0 && (
                <p>
                  No messages yet. Agent transcripts appear here after the Agent
                  leaves.
                </p>
              )}
              {messages.map((message) =>
                message.system?.action === "huddle_transcript" ? (
                  <HuddleTranscriptMessage key={message.id} message={message} />
                ) : (
                  <article key={message.id} className="huddle-history-message">
                    <header>
                      <strong>
                        {message.author.displayName || "Participant"}
                      </strong>
                      <time dateTime={message.createdAt}>
                        {new Date(message.createdAt).toLocaleTimeString([], {
                          hour: "2-digit",
                          minute: "2-digit",
                        })}
                      </time>
                    </header>
                    <p>
                      {message.deletedAt
                        ? "Message deleted"
                        : message.body?.markdown}
                    </p>
                  </article>
                ),
              )}
            </>
          ) : work.isError ? (
            <div role="alert">
              <p>Work proposals could not be loaded.</p>
              <Button variant="outline" onClick={() => void work.refetch()}>
                Try again
              </Button>
            </div>
          ) : !work.currentData ? (
            <p role="status">Loading work proposals…</p>
          ) : work.currentData.proposals.length === 0 ? (
            <p>No work proposals were created in this Huddle.</p>
          ) : (
            work.currentData.proposals.map((proposal) => (
              <article className="huddle-history-proposal" key={proposal.id}>
                <header>
                  <strong>{proposal.agentName}</strong>
                  <span>
                    {proposal.state === "pending" ? "Draft" : proposal.state}
                  </span>
                </header>
                <small>{proposal.projectTitle}</small>
                <h3>{proposal.title}</h3>
                <p>{proposal.brief}</p>
                {proposal.state === "submitted" && (
                  <HuddleWorkProgress proposal={proposal} stale={false} />
                )}
              </article>
            ))
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
