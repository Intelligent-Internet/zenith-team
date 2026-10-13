import { TurnArtifactCard } from "@/features/chat-panel/components/artifacts/turn-artifact-card";
import { ChannelInvocationStatus, useChannelAgents } from "./channel-agents";
import { DiscussionSystemMessage } from "./discussion-system-message";
import { WithheldAttachmentChip } from "@/shared/chat/withheld-attachment-chip";
import { useChatMentions } from "@/features/chat-panel/hooks/use-chat-mentions";
import { ChatMentionSuggestions } from "@/features/chat-panel/components/chat-mention-suggestions";
import { ChatLinkPreviews } from "@/features/chat-panel/components/chat-link-previews";
import { memo, useRef, useState, useSyncExternalStore } from "react";
import { DiscussionMessageBody } from "./discussion-message-body";
import {
  Copy,
  Link,
  Mail,
  MessageSquareText,
  MoreHorizontal,
  Pencil,
  Pin,
  PinOff,
  AddReaction,
  Trash2,
} from "@/shared/ui/icons";
import { MemberProfileTrigger } from "@/shared/chat/member-profile";
import { ChatEmojiPicker } from "@/shared/chat/chat-emoji-picker";
import { useCompanyPermissions } from "@/shared/permissions";
import { toast } from "@/shared/ui/sonner";
import { copyTextToClipboard } from "@/shared/lib/clipboard";
import { webAppUrl } from "@/shared/lib/web-app-url";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/shared/ui/dropdown-menu";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { createChatWorkspaceHref } from "@/features/chat-workspace/model/chat-workspace-route";
import type {
  DiscussionDetail,
  DiscussionMessage,
  DiscussionMessageAction,
} from "../api/discussions-api";
import { remapDiscussionReferences } from "../model/discussion-mentions";
import { discussionError } from "../model/discussion-runtime";
import { useDiscussionRuntime } from "./discussion-provider";
import { DiscussionAvatar } from "./discussion-identity";
import { DiscussionReactionPeople } from "./discussion-reaction-people";
import { DiscussionAttachments } from "./discussion-attachment";
import { useDiscussionPreview } from "./discussion-preview";

export const DiscussionMessageRow = memo(function DiscussionMessageRow({
  message,
  compact = false,
  detail,
  onOpenThread,
  inThread = false,
  focused = false,
}: {
  message: DiscussionMessage;
  compact?: boolean;
  detail: DiscussionDetail;
  onOpenThread: (rootId: string) => void;
  inThread?: boolean;
  focused?: boolean;
}) {
  const runtime = useDiscussionRuntime();
  const { openArtifact } = useDiscussionPreview();
  const channelAgents = useChannelAgents();
  const id = detail.conversation.id;
  const busy = useSyncExternalStore(
    runtime.subscribe,
    () => runtime.isMutating(id),
    () => false,
  );
  const capabilities = useSyncExternalStore(
    runtime.subscribe,
    () => runtime.state(id)?.capabilities,
    () => undefined,
  );
  const { can } = useCompanyPermissions();
  const [editor, setEditor] = useState<{
    text: string;
    revision: number;
    references: NonNullable<DiscussionMessage["body"]>["references"];
  }>();
  const editTextarea = useRef<HTMLTextAreaElement>(null);
  const editMentions = useChatMentions({
    disabled: !editor || busy,
    textareaRef: editTextarea,
    members: detail.members
      .filter((member) => member.active)
      .map((member) => ({
        id: member.userId,
        name: member.displayName,
        avatarSrc: member.avatarUrl,
        description: "Team member",
        kind: "human" as const,
      })),
    onDraftChange: (text) =>
      setEditor((current) =>
        current
          ? {
              ...current,
              text,
              references: remapDiscussionReferences(
                current.text,
                text,
                current.references,
              ),
            }
          : current,
      ),
    onMentionInserted: (member, range) =>
      setEditor((current) =>
        current
          ? {
              ...current,
              references: [
                ...current.references,
                { ...range, type: "user" as const, userId: member.id },
              ].sort((a, b) => a.start - b.start),
            }
          : current,
      ),
  });
  const [deleting, setDeleting] = useState<number>();
  const [error, setError] = useState("");
  const author = message.author;
  const mine = author.id === runtime.session.userId;
  const live = !message.deletedAt;
  const editable = author.type === "user" && live && mine && capabilities?.edit;
  const deletable =
    author.type === "user" &&
    live &&
    capabilities?.delete &&
    (mine ||
      (detail.conversation.type === "channel" && can("channels.manage")));
  const member = detail.members.find((item) => item.userId === author.id);
  const profile = {
    id: author.id ?? "",
    kind: author.type === "agent" ? ("agent" as const) : ("human" as const),
    name: author.displayName,
    avatarSrc: author.avatarUrl,
    title: member?.active === false ? "No longer active" : "Team member",
    archived: member?.active === false,
    isCurrentUser: mine,
  };

  async function change(action: DiscussionMessageAction) {
    setError("");
    try {
      const receipt = await runtime.mutateMessage(id, message.id, action);
      if (!receipt) return false;
      return true;
    } catch (cause) {
      setError(discussionError(cause));
      return false;
    }
  }
  async function copy(link: boolean) {
    const path = createChatWorkspaceHref({
      kind: "discussion",
      conversationId: id,
      discussionType: detail.conversation.type,
      messageId: message.id,
      threadRootId: message.threadRootId ?? undefined,
    });
    try {
      await copyTextToClipboard(
        link ? webAppUrl(path) : (message.body?.markdown ?? ""),
      );
      toast.success(link ? "Message link copied" : "Message copied");
    } catch {
      toast.error("Couldn’t copy. Please try again.");
    }
  }
  if (message.type === "discussion_system") {
    return <DiscussionSystemMessage message={message} detail={detail} />;
  }

  return (
    <article
      tabIndex={focused ? -1 : undefined}
      className={`discussion-message ${compact ? "discussion-message--compact" : ""} ${message.pinned ? "discussion-message--pinned" : ""} ${focused ? "discussion-message--focused" : ""}`}
      aria-label={`${author.displayName}, ${new Date(message.createdAt).toLocaleString()}`}
    >
      <div className="discussion-message-avatar">
        {!compact ? (
          <MemberProfileTrigger profile={profile}>
            <DiscussionAvatar
              name={author.displayName}
              src={author.avatarUrl}
              userId={author.type === "user" ? author.id : undefined}
              agentId={
                author.type === "agent" ? (author.id ?? undefined) : undefined
              }
            />
          </MemberProfileTrigger>
        ) : null}
      </div>
      <div className="discussion-message-main">
        {!compact ? (
          <header>
            <MemberProfileTrigger profile={profile}>
              <strong>{author.displayName}</strong>
              {author.type === "agent" ? (
                <small>
                  Managed by{" "}
                  {channelAgents.agents.find(
                    (agent) => agent.agentId === author.id,
                  )?.managerName ?? "former member"}
                </small>
              ) : null}
            </MemberProfileTrigger>
            <time
              dateTime={message.createdAt}
              title={new Date(message.createdAt).toLocaleString()}
            >
              {new Date(message.createdAt).toLocaleString([], {
                ...(inThread
                  ? ({ month: "short", day: "numeric" } as const)
                  : {}),
                hour: "numeric",
                minute: "2-digit",
              })}
            </time>
          </header>
        ) : null}
        {message.pinned ? (
          <span className="discussion-pin-label">
            <Pin size={12} /> Pinned for everyone
          </span>
        ) : null}
        {!live ? (
          <p className="discussion-tombstone">This message was deleted.</p>
        ) : editor ? (
          <form
            className="discussion-message-editor"
            onSubmit={async (event) => {
              event.preventDefault();
              if (
                await change({
                  type: "edit",
                  body: {
                    expectedRevision: editor.revision,
                    body: {
                      version: 1,
                      markdown: editor.text,
                      references: editor.references,
                    },
                  },
                })
              )
                setEditor(undefined);
            }}
          >
            <textarea
              autoFocus
              aria-label="Edit message"
              ref={editTextarea}
              aria-controls={
                editMentions.isOpen ? editMentions.listId : undefined
              }
              aria-activedescendant={editMentions.activeOptionId}
              onKeyUp={(event) =>
                editMentions.syncSelection(event.currentTarget)
              }
              onClick={(event) =>
                editMentions.syncSelection(event.currentTarget)
              }
              onCompositionStart={editMentions.onCompositionStart}
              onCompositionEnd={(event) =>
                editMentions.onCompositionEnd(event.currentTarget)
              }
              value={editor.text}
              disabled={busy}
              onChange={(event) =>
                setEditor({
                  ...editor,
                  text: event.target.value,
                  references: remapDiscussionReferences(
                    editor.text,
                    event.target.value,
                    editor.references,
                  ),
                })
              }
              onKeyDown={(event) => {
                editMentions.onKeyDown(event);
                if (event.defaultPrevented) return;
                if (event.key === "Escape" && !event.nativeEvent.isComposing)
                  setEditor(undefined);
              }}
            />
            {editMentions.isOpen ? (
              <ChatMentionSuggestions
                activeIndex={editMentions.activeIndex}
                listId={editMentions.listId}
                members={editMentions.matches}
                onActiveIndexChange={editMentions.setActiveIndex}
                onSelect={editMentions.selectMember}
                textareaRef={editTextarea}
              />
            ) : null}
            {message.revision !== editor.revision ? (
              <p className="discussion-notice">
                This message changed while you were editing.{" "}
                <button
                  type="button"
                  onClick={() =>
                    setEditor({ ...editor, revision: message.revision })
                  }
                >
                  Keep my edit and use the latest version
                </button>
              </p>
            ) : null}
            <div>
              <button
                type="button"
                className="discussion-button"
                disabled={busy}
                onClick={() => {
                  setEditor(undefined);
                  setError("");
                }}
              >
                Cancel
              </button>
              <button
                className="discussion-button discussion-button--primary"
                disabled={
                  busy ||
                  !editor.text.trim() ||
                  !editable ||
                  editor.revision !== message.revision
                }
              >
                Save changes
              </button>
            </div>
          </form>
        ) : (
          <>
            <DiscussionMessageBody
              body={message.body}
              members={detail.members}
            />
            <ChatLinkPreviews text={message.body?.markdown ?? ""} />
            <DiscussionAttachments files={message.attachments} />
            {message.attachmentsWithheld ? (
              <div className="discussion-message-attachments">
                <WithheldAttachmentChip />
              </div>
            ) : null}
            {message.editedAt ? (
              <span
                className="discussion-edited"
                title={`Edited ${new Date(message.editedAt).toLocaleString()}`}
              >
                (edited)
              </span>
            ) : null}
          </>
        )}
        {live && message.artifacts?.length ? (
          <div
            className="my-3 flex min-w-0 flex-wrap gap-2"
            aria-label="Message outputs"
          >
            {message.artifacts.map((artifact) => (
              <TurnArtifactCard
                key={artifact.id}
                title={artifact.title}
                type={artifact.type}
                files={artifact.files ?? []}
                onClick={() => void openArtifact(artifact.id)}
              />
            ))}
          </div>
        ) : null}
        <ChannelInvocationStatus conversationId={id} messageId={message.id} />
        {live && message.reactionSummary.length ? (
          <div className="discussion-reactions" aria-label="Message reactions">
            {message.reactionSummary.map((reaction) => (
              <DiscussionReactionPeople
                key={reaction.emoji}
                conversationId={id}
                messageId={message.id}
                reaction={reaction}
                disabled={busy}
                canReact={Boolean(capabilities?.reactions)}
                onToggle={() =>
                  void change({
                    type: "reaction",
                    emoji: reaction.emoji,
                    active: !reaction.reacted,
                  })
                }
              />
            ))}
          </div>
        ) : null}
        {!inThread &&
        message.threadSummary &&
        message.threadSummary.replyCount > 0 ? (
          <button
            className="discussion-thread-link"
            onClick={() => onOpenThread(message.threadRootId ?? message.id)}
            aria-label={`Open thread, ${message.threadSummary.replyCount} ${message.threadSummary.replyCount === 1 ? "reply" : "replies"}`}
          >
            <span className="discussion-thread-authors">
              {message.threadSummary.recentAuthors.map((item) => (
                <DiscussionAvatar
                  key={item.id}
                  size={21}
                  name={item.displayName}
                  src={item.avatarUrl}
                  userId={item.type === "user" ? item.id : undefined}
                  agentId={
                    item.type === "agent" ? (item.id ?? undefined) : undefined
                  }
                />
              ))}
            </span>
            <span>
              {message.threadSummary.replyCount}{" "}
              {message.threadSummary.replyCount === 1 ? "reply" : "replies"}
              {message.threadSummary.unreadCount
                ? ` · ${message.threadSummary.unreadCount} unread`
                : message.threadSummary.manualUnreadSeq
                  ? " · Unread"
                  : ""}
            </span>
          </button>
        ) : null}
        {error ? (
          <p className="discussion-error" role="alert">
            {error}
          </p>
        ) : null}
      </div>
      {!editor ? (
        <div className="discussion-message-actions">
          {live && capabilities?.reactions ? (
            <ChatEmojiPicker
              label="React to message"
              onSelect={(emoji) =>
                void change({ type: "reaction", emoji, active: true })
              }
            >
              <button
                className="discussion-icon-button"
                aria-label="Add reaction"
                title="Add reaction"
                disabled={busy}
              >
                <AddReaction size={16} />
              </button>
            </ChatEmojiPicker>
          ) : null}
          {capabilities?.threads ? (
            <button
              className="discussion-icon-button"
              aria-label="Reply in thread"
              title="Reply in thread"
              onClick={() => onOpenThread(message.threadRootId ?? message.id)}
            >
              <MessageSquareText size={16} />
            </button>
          ) : null}
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <button
                className="discussion-icon-button"
                aria-label={`More actions for message from ${author.displayName}`}
                title="More actions"
              >
                <MoreHorizontal size={16} />
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              {live ? (
                <DropdownMenuItem onSelect={() => void copy(false)}>
                  <Copy size={15} />
                  Copy text
                </DropdownMenuItem>
              ) : null}
              <DropdownMenuItem onSelect={() => void copy(true)}>
                <Link size={15} />
                Copy message link
              </DropdownMenuItem>
              {live && capabilities?.readState ? (
                <DropdownMenuItem
                  onSelect={() => {
                    void runtime
                      .markUnread(
                        id,
                        message.seq,
                        message.threadRootId ?? undefined,
                      )
                      .catch((cause) => toast.error(discussionError(cause)));
                  }}
                >
                  <Mail size={15} />
                  Mark as unread
                </DropdownMenuItem>
              ) : null}
              {live && capabilities?.pins ? (
                <DropdownMenuItem
                  disabled={busy}
                  onSelect={() =>
                    void change({ type: "pin", active: !message.pinned })
                  }
                >
                  {message.pinned ? <PinOff size={15} /> : <Pin size={15} />}
                  {message.pinned ? "Unpin for everyone" : "Pin for everyone"}
                </DropdownMenuItem>
              ) : null}
              {editable ? (
                <DropdownMenuItem
                  disabled={busy}
                  onSelect={() => {
                    setEditor({
                      references: message.body?.references ?? [],
                      text: message.body?.markdown ?? "",
                      revision: message.revision,
                    });
                    setError("");
                  }}
                >
                  <Pencil size={15} />
                  Edit message
                </DropdownMenuItem>
              ) : null}
              {deletable ? (
                <DropdownMenuItem
                  className="discussion-delete-message"
                  disabled={busy}
                  onSelect={() => {
                    setDeleting(message.revision);
                    setError("");
                  }}
                >
                  <Trash2 size={15} />
                  Delete message
                </DropdownMenuItem>
              ) : null}
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      ) : null}
      <Dialog
        open={deleting !== undefined && live}
        onOpenChange={(open) => {
          if (!open) setDeleting(undefined);
        }}
      >
        <DialogContent className="discussion-dialog">
          <DialogTitle>Delete this message?</DialogTitle>
          <DialogDescription>
            The message text, pins and reactions will be removed. Thread replies
            will stay available.
          </DialogDescription>
          {error ? (
            <p className="discussion-error" role="alert">
              {error}
            </p>
          ) : null}
          <div className="discussion-dialog-footer">
            <button
              className="discussion-button"
              onClick={() => setDeleting(undefined)}
            >
              Cancel
            </button>
            <button
              className="discussion-button discussion-button--danger"
              disabled={busy || !deletable}
              onClick={async () => {
                if (
                  deleting !== undefined &&
                  (await change({
                    type: "delete",
                    body: { expectedRevision: deleting },
                  }))
                )
                  setDeleting(undefined);
              }}
            >
              Delete message
            </button>
          </div>
        </DialogContent>
      </Dialog>
    </article>
  );
});
