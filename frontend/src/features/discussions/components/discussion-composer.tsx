import {
  useDiscussionRichEditor,
  DiscussionRichFormatting,
  DiscussionRichEmoji,
} from "./discussion-rich-editor";
import { ChatAddMenu } from "@/features/chat-panel/components/artifact-picker/chat-add-menu";
import { ArtifactPickerDialog } from "@/features/chat-panel/components/artifact-picker/artifact-picker-dialog";
import { ArtifactDraftCards } from "@/features/chat-panel/components/artifact-picker/artifact-draft-cards";
import { useChannelAgents } from "./channel-agents";
import { generateUuidV7 } from "@/shared/lib/uuid-v7";
import { useId, useRef, useState } from "react";
import {
  AtSign,
  ArrowUp,
  FileCode,
  FileText,
  ImageIcon,
  Info,
  Music,
  Paperclip,
  LoaderCircle,
  Text,
  Video,
  X,
} from "@/shared/ui/icons";
import {
  discussionAgentHint,
  discussionTaggedAgentNames,
} from "../model/discussion-mentions";
import type { DiscussionSummary } from "../api/discussions-api";
import type { ChatMentionMember } from "@/features/chat-panel/model/chat-mentions";
import { useListCompanyAgentsQuery } from "@/shared/api/agent-runtime-api";
import { useBindAgentDialog } from "@/features/agents/hooks/use-bind-agent-dialog";
import { useDiscussionUpdates } from "./discussion-provider";
import { useDiscussionPreview } from "./discussion-preview";
import { attachmentService } from "@/shared/api/attachment.service";
import type { DiscussionDraftAttachment } from "../model/discussion-runtime";

const MAX_DISCUSSION_FILE_BYTES = 25 * 1024 * 1024;
const MAX_DISCUSSION_FILES = 20;

export function DiscussionComposer({
  conversationId,
  rootId,
  title,
  canSend,
  readOnlyReason,
  mentionMembers = [],
  conversationType,
  channelName,
  onCreateWhiteboard,
}: {
  conversationId: string;
  rootId?: string;
  title: string;
  canSend: boolean;
  readOnlyReason: string;
  mentionMembers?: readonly ChatMentionMember[];
  conversationType?: DiscussionSummary["type"];
  // The channel's own name, also for a reply in one of its threads.
  channelName?: string;
  /** Makes a Whiteboard in the chat. */
  onCreateWhiteboard?: () => void;
}) {
  const isChannel = conversationType === "channel";
  const runtime = useDiscussionUpdates();
  const preview = useDiscussionPreview();
  const channelAgents = useChannelAgents();
  const agentHintId = useId();
  const formattingId = useId();
  const [formattingOpen, setFormattingOpen] = useState(false);
  // A tag runs the Agent on the tagger's own computer (ADR 0014). An Agent
  // the viewer has not set up is listed as "Not set up" rather than hidden,
  // and choosing it offers the setup.
  const companyId = runtime.session.companyId;
  const roster = useListCompanyAgentsQuery(companyId, { skip: !companyId })
    .currentData?.items;
  const { dialog: bindDialog, openBindDialog } = useBindAgentDialog(companyId);
  const unboundAgent = (agentId: string) =>
    roster?.find((agent) => agent.id === agentId && agent.binding === null);
  const draft = runtime.draft(conversationId, rootId);
  // A tag in a channel or a chat between people starts a Run with the
  // tagger's access, and everyone in the chat reads its answer. The line
  // says so; it never blocks sending.
  const agentHint =
    canSend && (isChannel || conversationType === "human_direct")
      ? discussionAgentHint(
          discussionTaggedAgentNames(
            draft.text,
            draft.references,
            channelAgents.agents,
          ),
          isChannel ? channelName : undefined,
        )
      : undefined;
  const pending = runtime.pending(conversationId, rootId);
  const fileInput = useRef<HTMLInputElement>(null);
  const [artifactPickerOpen, setArtifactPickerOpen] = useState(false);
  const [uploading, setUploading] = useState(false);
  const [uploadError, setUploadError] = useState("");
  const [uploadProgress, setUploadProgress] = useState("");
  const [failedUploads, setFailedUploads] = useState<
    { file: File; requestKey: string }[]
  >([]);
  const upload = async (items: { file: File; requestKey: string }[]) => {
    const before = runtime.state(conversationId);
    if (!before?.capabilities.send) return;
    setUploading(true);
    setUploadError("");
    const failed: typeof items = [];
    for (const item of items) {
      const { file, requestKey } = item;
      try {
        const attachment = await attachmentService.uploadFile(
          runtime.session.companyId,
          file,
          (progress) =>
            setUploadProgress(
              `${file.name} · ${progress === undefined ? "Uploading…" : `${progress}%`}`,
            ),
          requestKey,
        );
        const state = runtime.state(conversationId);
        if (
          !runtime.active ||
          !state?.capabilities.send ||
          state.grantGeneration !== before.grantGeneration ||
          state.companyMembershipRevision !== before.companyMembershipRevision
        )
          break;
        const current = runtime.draft(conversationId, rootId);
        const addition: DiscussionDraftAttachment = {
          id: attachment.id,
          name: file.name,
          size: file.size,
          contentType: file.type || "application/octet-stream",
          previewUrl: file.type.startsWith("image/")
            ? URL.createObjectURL(file)
            : undefined,
        };
        runtime.setDraft(
          conversationId,
          {
            ...current,
            attachmentIds: [...current.attachmentIds, addition.id],
            attachments: [...current.attachments, addition],
          },
          rootId,
        );
      } catch {
        failed.push(item);
      }
    }
    setFailedUploads(failed);
    if (failed.length)
      setUploadError(`${failed.length} file(s) could not be uploaded.`);
    setUploadProgress("");
    setUploading(false);
  };
  const tooLarge =
    new TextEncoder().encode(
      JSON.stringify({
        version: 1,
        markdown: draft.text.replace(/\r\n?/g, "\n"),
        references: draft.references,
      }),
    ).length > 65_536;
  const members: ChatMentionMember[] = [
    ...mentionMembers.filter(
      (member) =>
        member.kind !== "agent" && member.id !== runtime.session.userId,
    ),
    ...channelAgents.agents.flatMap((agent): ChatMentionMember[] => {
      const member = {
        id: agent.agentId,
        name: agent.name,
        description: `Managed by ${agent.managerName}`,
        kind: "agent" as const,
      };
      if (agent.canInvoke) return [member];
      // With the composer open, the one thing that still keeps a tag
      // from running is the viewer's own setup of the Agent.
      return canSend && unboundAgent(agent.agentId)
        ? [
            {
              ...member,
              description: "Set it up on your computer to tag it",
              unavailable: "Not set up",
            },
          ]
        : [];
    }),
    ...(isChannel
      ? [
          {
            id: "everyone",
            name: "everyone",
            description: "All channel members",
            kind: "human" as const,
            broadcastScope: "everyone" as const,
          },
          {
            id: "here",
            name: "here",
            description: "Online channel members",
            kind: "human" as const,
            broadcastScope: "here" as const,
          },
        ]
      : []),
  ];
  const ready =
    canSend &&
    !runtime.isMutating(conversationId) &&
    !pending &&
    (Boolean(draft.text.trim()) ||
      draft.attachmentIds.length > 0 ||
      draft.artifacts.length > 0) &&
    !uploading &&
    failedUploads.length === 0 &&
    !tooLarge;
  const rich = useDiscussionRichEditor({
    sessionKey: `${conversationId}:${rootId ?? "root"}`,
    draft: { text: draft.text, references: draft.references },
    disabled: !canSend,
    label: `Message ${title}`,
    placeholder: canSend
      ? rootId
        ? "Reply…"
        : `Message ${title}`
      : "This conversation is read-only",
    describedBy:
      [
        agentHint ? agentHintId : "",
        tooLarge ? `discussion-message-limit-${rootId ?? "root"}` : "",
      ]
        .filter(Boolean)
        .join(" ") || undefined,
    members,
    onChange: (body) =>
      runtime.setDraft(
        conversationId,
        { ...runtime.draft(conversationId, rootId), ...body },
        rootId,
        { references: "replace" },
      ),
    onSend: () => {
      if (ready) void runtime.send(conversationId, false, rootId);
    },
    onUnavailableSelect: (member) => {
      const agent = unboundAgent(member.id);
      if (agent) openBindDialog(agent, { onBound: channelAgents.refresh });
    },
  });
  return (
    <div className="discussion-compose-area">
      {artifactPickerOpen && (
        <ArtifactPickerDialog
          companyId={companyId}
          selected={draft.artifacts}
          onClose={() => {
            setArtifactPickerOpen(false);
            requestAnimationFrame(() => rich.focus());
          }}
          onAttach={(artifacts) => {
            runtime.setDraft(
              conversationId,
              { ...runtime.draft(conversationId, rootId), artifacts },
              rootId,
            );
            setArtifactPickerOpen(false);
            requestAnimationFrame(() => rich.focus());
          }}
        />
      )}
      {pending?.status === "failed" ? (
        <div className="discussion-send-state discussion-error" role="alert">
          <span>{pending.error ?? "Message could not be sent."}</span>
          <>
            <button
              className="discussion-button"
              onClick={() => void runtime.send(conversationId, true, rootId)}
            >
              Retry message
            </button>
            <button
              className="discussion-button"
              onClick={() => runtime.dismissPending(conversationId, rootId)}
            >
              Dismiss
            </button>
          </>
        </div>
      ) : null}
      {!canSend ? (
        <p className="discussion-notice" role="status">
          {readOnlyReason}
        </p>
      ) : null}
      {/* The live region stays mounted so the line is read when it appears. */}
      <div role="status">
        {agentHint ? (
          <p
            className="discussion-notice discussion-agent-hint"
            id={agentHintId}
          >
            <Info size={13} aria-hidden />
            <span>{agentHint}</span>
          </p>
        ) : null}
      </div>
      <form
        className="discussion-composer"
        onSubmit={(event) => {
          event.preventDefault();
          if (ready && !rich.editor?.view.composing)
            void runtime.send(conversationId, false, rootId);
        }}
      >
        {formattingOpen && (
          <div id={formattingId}>
            <DiscussionRichFormatting
              editor={rich.editor}
              disabled={!canSend}
            />
          </div>
        )}
        <ArtifactDraftCards
          artifacts={draft.artifacts}
          disabled={!canSend || Boolean(pending)}
          onRemove={(id) =>
            runtime.setDraft(
              conversationId,
              {
                ...runtime.draft(conversationId, rootId),
                artifacts: draft.artifacts.filter((a) => a.id !== id),
              },
              rootId,
            )
          }
        />
        {draft.attachments.length ? (
          <div
            className="flex gap-[8px] m-0 min-h-[72px] p-[6px_10px_4px] overflow-x-auto flex-nowrap"
            aria-label="Attached files"
          >
            {draft.attachments.map((file) => (
              <div className="discussion-draft-attachment" key={file.id}>
                <button
                  className="discussion-draft-attachment-preview"
                  type="button"
                  aria-label={`Preview ${file.name}`}
                  onClick={() =>
                    preview.openAttachment({
                      id: file.id,
                      filename: file.name,
                      contentType: file.contentType,
                      sizeBytes: file.size,
                    })
                  }
                >
                  {file.previewUrl ? (
                    <img src={file.previewUrl} alt="" />
                  ) : (
                    <AttachmentTypeIcon
                      contentType={file.contentType}
                      name={file.name}
                    />
                  )}
                </button>
                <span className="discussion-draft-attachment-copy flex min-w-0 flex-1 flex-col gap-[2px]">
                  <strong title={file.name}>{file.name}</strong>
                  <span>{formatFileSize(file.size)}</span>
                </span>
                <button
                  type="button"
                  aria-label={`Remove ${file.name}`}
                  onClick={() => {
                    if (preview.attachment?.id === file.id) preview.close();
                    if (file.previewUrl) URL.revokeObjectURL(file.previewUrl);
                    runtime.setDraft(
                      conversationId,
                      {
                        ...draft,
                        attachmentIds: draft.attachmentIds.filter(
                          (id) => id !== file.id,
                        ),
                        attachments: draft.attachments.filter(
                          (item) => item.id !== file.id,
                        ),
                      },
                      rootId,
                    );
                  }}
                >
                  <X size={12} />
                </button>
              </div>
            ))}
          </div>
        ) : null}
        <div className="discussion-composer-editor">{rich.input}</div>
        {uploadProgress && (
          <div className="discussion-upload-status" role="status">
            {uploadProgress}
          </div>
        )}
        {uploadError && (
          <div
            className="discussion-upload-status discussion-error"
            role="alert"
          >
            {uploadError}{" "}
            {failedUploads.length > 0 && (
              <>
                <button
                  type="button"
                  className="discussion-button"
                  disabled={uploading || !canSend}
                  onClick={() => void upload(failedUploads)}
                >
                  Retry
                </button>
                <button
                  type="button"
                  className="discussion-button"
                  disabled={uploading}
                  onClick={() => {
                    setFailedUploads([]);
                    setUploadError("");
                  }}
                >
                  Dismiss
                </button>
              </>
            )}
          </div>
        )}
        <div className="discussion-composer-tools flex items-center gap-[8px] p-[4px_12px_8px]">
          <div>
            <ChatAddMenu
              disabled={!canSend || Boolean(pending)}
              uploadDisabled={uploading || failedUploads.length > 0}
              onUpload={() => fileInput.current?.click()}
              onSelectArtifact={() => setArtifactPickerOpen(true)}
              onCreateWhiteboard={onCreateWhiteboard}
            />
            <button
              className="discussion-icon-button"
              type="button"
              aria-label="Message formatting"
              aria-expanded={formattingOpen}
              aria-controls={formattingOpen ? formattingId : undefined}
              title={formattingOpen ? "Hide formatting" : "Show formatting"}
              disabled={!canSend}
              onMouseDown={(event) => event.preventDefault()}
              onClick={() => setFormattingOpen((open) => !open)}
            >
              <Text size={18} aria-hidden />
            </button>
            <DiscussionRichEmoji editor={rich.editor} disabled={!canSend} />
            <button
              className="discussion-icon-button"
              type="button"
              aria-label="Mention someone"
              title="Mention someone"
              disabled={!canSend}
              onClick={rich.insertMention}
            >
              <AtSign size={17} />
            </button>
            <input
              ref={fileInput}
              className="hidden"
              type="file"
              multiple
              onChange={async (event) => {
                const files = Array.from(event.target.files ?? []);
                event.target.value = "";
                if (!files.length) return;
                const oversized = files.find(
                  (file) => file.size > MAX_DISCUSSION_FILE_BYTES,
                );
                const current = runtime.draft(conversationId, rootId);
                if (
                  oversized ||
                  current.attachments.length + files.length >
                    MAX_DISCUSSION_FILES
                ) {
                  setUploadError(
                    oversized
                      ? `${oversized.name} exceeds the 25 MB limit.`
                      : `You can attach up to ${MAX_DISCUSSION_FILES} files.`,
                  );
                  return;
                }
                setFailedUploads([]);
                await upload(
                  files.map((file) => ({ file, requestKey: generateUuidV7() })),
                );
              }}
            />
          </div>
          <button
            className="discussion-send workspace-chat-send-button"
            type="submit"
            aria-label={
              pending?.status === "sending" ? "Sending message" : "Send message"
            }
            aria-busy={pending?.status === "sending"}
            title={
              pending?.status === "sending" ? "Sending message" : "Send message"
            }
            disabled={!ready}
          >
            {pending?.status === "sending" ? (
              <LoaderCircle
                size={18}
                aria-hidden
                className="motion-safe:animate-spin"
              />
            ) : (
              <ArrowUp size={18} aria-hidden />
            )}
          </button>
        </div>
      </form>
      {rich.suggestions}
      {bindDialog}
      {tooLarge ? (
        <p
          className="discussion-error"
          id={`discussion-message-limit-${rootId ?? "root"}`}
          role="alert"
        >
          This message is too long. Shorten it before sending.
        </p>
      ) : null}
    </div>
  );
}

function formatFileSize(bytes: number) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function AttachmentTypeIcon({
  contentType,
  name,
}: {
  contentType: string;
  name: string;
}) {
  const extension = name.split(".").pop()?.toLowerCase();
  const Icon = contentType.startsWith("image/")
    ? ImageIcon
    : contentType.startsWith("video/")
      ? Video
      : contentType.startsWith("audio/")
        ? Music
        : contentType.startsWith("text/") ||
            ["txt", "pdf", "doc", "docx"].includes(extension ?? "")
          ? FileText
          : [
                "js",
                "ts",
                "tsx",
                "jsx",
                "json",
                "html",
                "css",
                "go",
                "py",
              ].includes(extension ?? "")
            ? FileCode
            : Paperclip;
  return <Icon size={24} />;
}
