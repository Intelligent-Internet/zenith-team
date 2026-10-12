import { useCallback, useEffect, useState } from "react";
import { LoaderCircle, Mail, Send, X } from "@/shared/ui/icons";
import { useSendAgentMailDraftGroupMutation } from "@/features/agent-mail/api/agent-mail-api";
import type { AgentMailDraftGroup } from "@/features/agent-mail/model/agent-mail";
import { AgentMailDraftEditor } from "./agent-mail-draft-editor";
import { toast } from "@/shared/ui/sonner";
import "../styles/agent-mail-panel.css";

export function AgentMailDraftPanel({
  companyId,
  conversationId,
  group,
  loading,
  error,
  readOnly = false,
  onClose,
  onRetry,
  onDirtyChange,
}: {
  companyId: string;
  conversationId: string;
  group?: AgentMailDraftGroup;
  loading: boolean;
  error: boolean;
  readOnly?: boolean;
  onClose: () => void;
  onRetry: () => void;
  onDirtyChange: (dirty: boolean) => void;
}) {
  return (
    <section className="agent-mail-panel" aria-label="Email draft review">
      <header className="artifacts-panel__header">
        <Mail aria-hidden size={19} />
        <h2>Email draft{(group?.drafts.length ?? 0) > 1 ? "s" : ""}</h2>
        <button
          className="artifacts-icon-button agent-mail-panel__close"
          type="button"
          onClick={onClose}
          aria-label="Close email draft panel"
          title="Close"
        >
          <X size={18} />
        </button>
      </header>
      {group && error ? (
        <div className="agent-mail-panel__refresh-error" role="alert">
          <p>
            Email drafts could not be refreshed. Your unsaved changes are kept.
          </p>
          <button className="agent-mail-button" type="button" onClick={onRetry}>
            Try again
          </button>
        </div>
      ) : null}
      {group ? (
        <DraftGroupReview
          key={group.id}
          companyId={companyId}
          conversationId={conversationId}
          group={group}
          readOnly={readOnly}
          onDirtyChange={onDirtyChange}
        />
      ) : loading ? (
        <div className="agent-mail-panel__state" role="status">
          <LoaderCircle className="artifact-spinner" size={22} /> Loading email
          drafts…
        </div>
      ) : (
        <div className="agent-mail-panel__state" role="alert">
          <p>
            {error
              ? "Email drafts could not be loaded."
              : "This draft is no longer available."}
          </p>
          <button className="agent-mail-button" type="button" onClick={onRetry}>
            Try again
          </button>
        </div>
      )}
    </section>
  );
}

function DraftGroupReview({
  companyId,
  conversationId,
  group,
  readOnly,
  onDirtyChange,
}: {
  companyId: string;
  conversationId: string;
  group: AgentMailDraftGroup;
  readOnly: boolean;
  onDirtyChange: (dirty: boolean) => void;
}) {
  const [selectedDraftId, setSelectedDraftId] = useState(group.drafts[0]?.id);
  const [dirtyDraftIds, setDirtyDraftIds] = useState<ReadonlySet<string>>(
    () => new Set(),
  );
  const [sendGroup, sendGroupState] = useSendAgentMailDraftGroupMutation();
  const sendable = group.drafts.filter((draft) => draft.status === "draft");
  const hasUnsavedChanges = group.drafts.some((draft) =>
    dirtyDraftIds.has(draft.id),
  );
  const selected =
    group.drafts.find((draft) => draft.id === selectedDraftId) ??
    group.drafts[0];
  const handleDirtyChange = useCallback((id: string, dirty: boolean) => {
    setDirtyDraftIds((current) => {
      if (current.has(id) === dirty) return current;
      const next = new Set(current);
      if (dirty) next.add(id);
      else next.delete(id);
      return next;
    });
  }, []);
  useEffect(() => {
    onDirtyChange(hasUnsavedChanges);
    return () => onDirtyChange(false);
  }, [hasUnsavedChanges, onDirtyChange]);

  const handleSendAll = async () => {
    if (
      readOnly ||
      hasUnsavedChanges ||
      sendGroupState.isLoading ||
      !sendable.length
    )
      return;
    try {
      await sendGroup({
        companyId,
        conversationId,
        groupId: group.id,
        drafts: sendable.map((draft) => ({
          id: draft.id,
          expectedRevision: draft.revision,
        })),
      }).unwrap();
      toast.success("Email drafts queued for sending.");
    } catch (error) {
      toast.error(
        error &&
          typeof error === "object" &&
          "message" in error &&
          typeof error.message === "string"
          ? error.message
          : "Email drafts could not be queued.",
      );
    }
  };

  return (
    <>
      {group.drafts.length > 1 ? (
        <div className="agent-mail-panel__picker">
          <label>
            <span className="sr-only">Email draft</span>
            <select
              value={selected?.id}
              onChange={(event) => setSelectedDraftId(event.target.value)}
            >
              {group.drafts.map((draft, index) => (
                <option key={draft.id} value={draft.id}>
                  {index + 1}. {draft.subject}
                </option>
              ))}
            </select>
          </label>
          {sendable.length > 1 && !readOnly ? (
            <button
              className="agent-mail-button"
              type="button"
              onClick={() => void handleSendAll()}
              title={
                hasUnsavedChanges
                  ? "Save every changed draft before sending all"
                  : undefined
              }
              disabled={hasUnsavedChanges || sendGroupState.isLoading}
            >
              {sendGroupState.isLoading ? (
                <LoaderCircle className="artifact-spinner" size={15} />
              ) : (
                <Send size={15} />
              )}
              Send all
            </button>
          ) : null}
        </div>
      ) : null}
      {group.drafts.map((draft) => (
        <div
          className="agent-mail-panel__draft"
          key={draft.id}
          hidden={draft.id !== selected?.id}
        >
          <AgentMailDraftEditor
            companyId={companyId}
            conversationId={conversationId}
            draft={draft}
            disabled={readOnly || sendGroupState.isLoading}
            otherRecipientCount={group.recipientCount - draft.recipientCount}
            onDirtyChange={handleDirtyChange}
          />
        </div>
      ))}
    </>
  );
}
