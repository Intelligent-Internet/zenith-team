import { useLayoutEffect, useRef, useState } from "react";
import { Check, RotateCcw, X } from "@/shared/ui/icons";
import { usePromptInputAttachments } from "@/shared/ui/core/ai-elements/prompt-input";
import {
  attachmentFromInput,
  attachmentKind,
  formatAttachmentSize,
  type ChatAttachment,
} from "@/shared/chat/attachment-preview";
import {
  ChatAttachmentIcon,
  ChatAttachmentThumbnail,
  ChatAttachmentViewer,
} from "@/shared/chat/chat-attachment-preview";
import "../styles/chat-composer-attachments.css";

export function ChatComposerAttachments({
  onCountChange,
}: {
  onCountChange: (count: number) => void;
}) {
  const attachments = usePromptInputAttachments();
  const [preview, setPreview] = useState<ChatAttachment | null>(null);
  const previewTriggerRef = useRef<HTMLButtonElement>(null);
  useLayoutEffect(() => {
    onCountChange(attachments.files.length);
  }, [attachments.files.length, onCountChange]);
  if (!attachments.files.length) return null;
  return (
    <>
      <div aria-label="Attached files" className="chat-composer-attachments">
        {attachments.files.map((input) => {
          const file = attachmentFromInput(input, input.id);
          const transfer = attachments.transfers?.[input.id];
          const status = transfer?.status;
          return (
            <div
              className="chat-composer-attachment"
              data-error={status === "error"}
              key={file.id}
            >
              <button
                aria-label={`Preview ${file.name}`}
                className="chat-composer-attachment__preview"
                onClick={(event) => {
                  previewTriggerRef.current = event.currentTarget;
                  setPreview(file);
                }}
                type="button"
              >
                {attachmentKind(file) === "image" ? (
                  <ChatAttachmentThumbnail file={file} />
                ) : (
                  <ChatAttachmentIcon file={file} size={25} />
                )}
              </button>
              <div className="chat-composer-attachment__copy">
                <strong title={file.name}>{file.name}</strong>
                <span>{formatAttachmentSize(file.sizeBytes)}</span>
                <span
                  aria-live="polite"
                  className="chat-composer-attachment__status"
                  title={transfer?.error}
                >
                  {status === "error" ? (
                    "Failed"
                  ) : status === "uploading" ? (
                    `Uploading${transfer?.progress === undefined ? "…" : ` ${transfer?.progress}%`}`
                  ) : status === "preparing" ? (
                    `Preparing ${transfer?.progress ?? 0}%`
                  ) : status === "uploaded" ? (
                    <>
                      <Check size={11} /> Uploaded
                    </>
                  ) : (
                    "Ready"
                  )}
                </span>
                {status === "uploading" || status === "preparing" ? (
                  <progress
                    aria-label={`${file.name} ${status}`}
                    max={100}
                    value={transfer?.progress}
                  />
                ) : null}
              </div>
              <div className="chat-composer-attachment__actions">
                <button
                  aria-label={`Remove ${file.name}`}
                  disabled={attachments.busy}
                  onClick={() => {
                    if (preview?.id === file.id) setPreview(null);
                    attachments.remove(file.id);
                  }}
                  type="button"
                >
                  <X size={13} />
                </button>
                {status === "error" ? (
                  <button
                    aria-label={`Retry ${file.name}`}
                    className="chat-composer-attachment__retry"
                    disabled={attachments.busy}
                    onClick={() => attachments.retry?.(file.id)}
                    type="button"
                  >
                    <RotateCcw size={13} />
                  </button>
                ) : null}
              </div>
            </div>
          );
        })}
      </div>
      {preview ? (
        <ChatAttachmentViewer
          file={preview}
          onClose={() => setPreview(null)}
          returnFocusRef={previewTriggerRef}
        />
      ) : null}
    </>
  );
}
