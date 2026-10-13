import { useEffect, useId, useMemo, useState } from "react";
import { LoaderCircle, Plus, Save, Send, Trash2, X } from "@/shared/ui/icons";
import {
  useDeleteAgentMailDraftMutation,
  useSendAgentMailDraftMutation,
  useUpdateAgentMailDraftMutation,
} from "@/features/agent-mail/api/agent-mail-api";
import type {
  AgentMailDraft,
  AgentMailDraftDeliveryInput,
} from "@/features/agent-mail/model/agent-mail";
import { toast } from "@/shared/ui/sonner";

type EditableDelivery = { cc: string; key: string; to: string };

export function AgentMailDraftEditor({
  companyId,
  conversationId,
  draft,
  otherRecipientCount,
  onDirtyChange,
  disabled = false,
}: {
  disabled?: boolean;
  companyId: string;
  conversationId: string;
  draft: AgentMailDraft;
  otherRecipientCount: number;
  onDirtyChange: (draftId: string, dirty: boolean) => void;
}) {
  const formId = useId();
  const [subject, setSubject] = useState(draft.subject);
  const [text, setText] = useState(draft.text);
  const [deliveries, setDeliveries] = useState<EditableDelivery[]>(() =>
    editableDeliveries(draft),
  );
  const [dirty, setDirty] = useState(false);
  const [expectedRevision, setExpectedRevision] = useState(draft.revision);
  const [updateDraft, updateState] = useUpdateAgentMailDraftMutation();
  const [deleteDraft, deleteState] = useDeleteAgentMailDraftMutation();
  const [sendDraft, sendState] = useSendAgentMailDraftMutation();
  const editable =
    !disabled && (draft.status === "draft" || draft.status === "failed");
  const mutationPending =
    updateState.isLoading || deleteState.isLoading || sendState.isLoading;
  const parsedDeliveries = useMemo(
    () => deliveries.map(parseEditableDelivery),
    [deliveries],
  );
  const validation = useMemo(
    () =>
      validateDraftEditor(subject, text, parsedDeliveries, otherRecipientCount),
    [otherRecipientCount, parsedDeliveries, subject, text],
  );
  const visibleRecipientCount = parsedDeliveries.reduce(
    (total, delivery) => total + new Set([...delivery.to, ...delivery.cc]).size,
    0,
  );

  useEffect(() => {
    if (dirty) return;
    setSubject(draft.subject);
    setText(draft.text);
    setDeliveries(editableDeliveries(draft));
    setExpectedRevision(draft.revision);
  }, [draft.id, draft.revision]);

  useEffect(() => {
    onDirtyChange(draft.id, dirty);
    return () => onDirtyChange(draft.id, false);
  }, [dirty, draft.id, onDirtyChange]);

  const changeDelivery = (key: string, field: "to" | "cc", value: string) => {
    setDeliveries((current) =>
      current.map((delivery) =>
        delivery.key === key ? { ...delivery, [field]: value } : delivery,
      ),
    );
    setDirty(true);
  };

  const addDelivery = () => {
    setDeliveries((current) => [
      ...current,
      { cc: "", key: editorKey(), to: "" },
    ]);
    setDirty(true);
  };

  const removeDelivery = (key: string) => {
    setDeliveries((current) =>
      current.filter((delivery) => delivery.key !== key),
    );
    setDirty(true);
  };

  const handleSave = async () => {
    if (!editable || !dirty || validation) return;
    try {
      const updated = await updateDraft({
        companyId,
        conversationId,
        deliveries: parsedDeliveries,
        draftId: draft.id,
        expectedRevision,
        subject,
        text,
      }).unwrap();
      setSubject(updated.subject);
      setText(updated.text);
      setDeliveries(editableDeliveries(updated));
      setExpectedRevision(updated.revision);
      setDirty(false);
      toast.success("Mail draft saved.");
    } catch (error) {
      toast.error(errorMessage(error, "Mail draft could not be saved."));
    }
  };

  const handleSend = async () => {
    if (!editable || dirty || validation || mutationPending) return;
    try {
      await sendDraft({
        companyId,
        conversationId,
        draftId: draft.id,
        expectedRevision,
      }).unwrap();
      toast.success(
        `${recipientLabel(draft.recipientCount)} queued for sending.`,
      );
    } catch (error) {
      toast.error(errorMessage(error, "Mail draft could not be queued."));
    }
  };

  const handleDelete = async () => {
    if (!editable || mutationPending) return;
    try {
      await deleteDraft({
        companyId,
        conversationId,
        draftId: draft.id,
        expectedRevision,
        groupId: draft.groupId,
      }).unwrap();
      toast.success("Mail draft deleted.");
    } catch (error) {
      toast.error(errorMessage(error, "Mail draft could not be deleted."));
    }
  };

  return (
    <article className="agent-mail-editor" aria-label={draft.subject}>
      <div className="flex shrink-0 items-center gap-[10px] p-[12px_16px] [border-bottom:1px_solid_var(--workspace-divider)] text-[length:12px]">
        <span
          className="inline-flex shrink-0 items-center gap-[5px] rounded-[6px] p-[3px_7px] [background:var(--workspace-accent-soft)] text-workspace-accent text-[length:11px] font-[600]"
          data-status={draft.status}
        >
          {draft.status === "sending" ? (
            <LoaderCircle className="artifact-spinner" size={13} />
          ) : null}
          {
            {
              draft: "Draft",
              failed: "Failed",
              sending: "Sending",
              sent: "Sent",
            }[draft.status]
          }
        </span>
        <span
          className="flex-1 min-w-0 overflow-hidden text-ellipsis whitespace-nowrap text-workspace-muted"
          title={`${draft.fromName} <${draft.fromAddress}>`}
        >
          From {draft.fromName} &lt;{draft.fromAddress}&gt;
        </span>
        {editable ? (
          <button
            aria-label="Delete mail draft"
            className="artifacts-icon-button"
            disabled={mutationPending}
            type="button"
            onClick={() => void handleDelete()}
          >
            {deleteState.isLoading ? (
              <LoaderCircle className="artifact-spinner" size={16} />
            ) : (
              <Trash2 size={16} />
            )}
          </button>
        ) : null}
      </div>
      <div className="flex flex-1 flex-col min-h-0 overflow-y-auto overscroll-contain p-[0_18px_16px]">
        {deliveries.map((delivery, index) => (
          <fieldset
            className="agent-mail-delivery min-w-0 m-0 p-0 [border:0]"
            key={delivery.key}
          >
            {deliveries.length > 1 ? (
              <legend>Delivery {index + 1}</legend>
            ) : null}
            <label className="agent-mail-field flex shrink-0 items-center gap-[12px] min-h-[46px] [border-bottom:1px_solid_var(--workspace-divider)] text-[length:13px]">
              <span>To</span>
              <input
                aria-label={`To addresses${deliveries.length > 1 ? ` for delivery ${index + 1}` : ""}`}
                disabled={!editable || mutationPending}
                placeholder="name@example.com"
                value={delivery.to}
                onChange={(event) =>
                  changeDelivery(delivery.key, "to", event.target.value)
                }
              />
            </label>
            <label className="agent-mail-field flex shrink-0 items-center gap-[12px] min-h-[46px] [border-bottom:1px_solid_var(--workspace-divider)] text-[length:13px]">
              <span>Cc</span>
              <input
                aria-label={`CC addresses${deliveries.length > 1 ? ` for delivery ${index + 1}` : ""}`}
                disabled={!editable || mutationPending}
                placeholder="Optional"
                value={delivery.cc}
                onChange={(event) =>
                  changeDelivery(delivery.key, "cc", event.target.value)
                }
              />
            </label>
            {editable && deliveries.length > 1 ? (
              <button
                aria-label={`Remove delivery ${index + 1}`}
                className="agent-mail-text-button"
                disabled={mutationPending}
                type="button"
                onClick={() => removeDelivery(delivery.key)}
              >
                <X size={14} /> Remove delivery
              </button>
            ) : null}
          </fieldset>
        ))}
        {editable && deliveries.length < 100 ? (
          <button
            className="agent-mail-text-button"
            disabled={mutationPending}
            type="button"
            onClick={addDelivery}
          >
            <Plus size={14} /> Add private delivery
          </button>
        ) : null}
        <label
          className="agent-mail-field flex shrink-0 items-center gap-[12px] min-h-[46px] [border-bottom:1px_solid_var(--workspace-divider)] text-[length:13px] agent-mail-field--subject"
          htmlFor={`${formId}-subject`}
        >
          <span>Subject</span>
          <input
            disabled={!editable || mutationPending}
            id={`${formId}-subject`}
            maxLength={200}
            value={subject}
            onChange={(event) => {
              setSubject(event.target.value);
              setDirty(true);
            }}
          />
        </label>
        <label
          className="agent-mail-body flex flex-1 min-h-[200px] pt-[18px]"
          htmlFor={`${formId}-body`}
        >
          <span className="sr-only">Message</span>
          <textarea
            disabled={!editable || mutationPending}
            id={`${formId}-body`}
            spellCheck
            value={text}
            onChange={(event) => {
              setText(event.target.value);
              setDirty(true);
            }}
          />
        </label>
      </div>
      <footer className="shrink-0 [border-top:1px_solid_var(--workspace-divider)] p-[14px_16px]">
        {validation ? (
          <p
            className="m-[0_0_10px] text-workspace-text text-[length:12px] leading-[1.5]"
            role="status"
          >
            {validation}
          </p>
        ) : null}
        {draft.status === "failed" && draft.failureReason ? (
          <p
            className="m-[0_0_10px] text-workspace-text text-[length:12px] leading-[1.5]"
            role="alert"
          >
            {draft.failureReason}
          </p>
        ) : null}
        {editable ? (
          <>
            {dirty ? (
              <p className="agent-mail-hint">
                Save your changes before sending.
              </p>
            ) : null}
            <div className="agent-mail-editor__actions">
              <span className="agent-mail-hint">
                {recipientLabel(
                  dirty ? visibleRecipientCount : draft.recipientCount,
                )}
              </span>
              <button
                className="agent-mail-button"
                disabled={!dirty || Boolean(validation) || mutationPending}
                type="button"
                onClick={() => void handleSave()}
              >
                {updateState.isLoading ? (
                  <LoaderCircle className="artifact-spinner" size={16} />
                ) : (
                  <Save size={16} />
                )}
                Save draft
              </button>
              <button
                className="agent-mail-button agent-mail-button--primary"
                disabled={dirty || Boolean(validation) || mutationPending}
                title={dirty ? "Save this draft before sending" : undefined}
                type="button"
                onClick={() => void handleSend()}
              >
                {sendState.isLoading ? (
                  <LoaderCircle className="artifact-spinner" size={16} />
                ) : (
                  <Send size={16} />
                )}
                Send
              </button>
            </div>
          </>
        ) : (
          <p className="agent-mail-hint">
            {draft.status === "sent"
              ? "This email has been sent."
              : draft.status === "sending"
                ? "This email is being sent."
                : "This draft is read-only."}
          </p>
        )}
      </footer>
    </article>
  );
}

function editableDeliveries(draft: AgentMailDraft): EditableDelivery[] {
  return draft.deliveries.map((delivery) => ({
    cc: editableAddressList(delivery.cc),
    key: delivery.id || editorKey(),
    to: editableAddressList(delivery.to),
  }));
}

function editableAddressList(addresses: string[] | null | undefined): string {
  return Array.isArray(addresses) ? addresses.join(", ") : "";
}

function parseEditableDelivery(
  delivery: EditableDelivery,
): AgentMailDraftDeliveryInput {
  return {
    cc: splitAddresses(delivery.cc),
    to: splitAddresses(delivery.to),
  };
}

function splitAddresses(value: string): string[] {
  return value
    .split(/[;,\n]/u)
    .map((address) => address.trim().toLowerCase())
    .filter(Boolean);
}

function validateDraftEditor(
  subject: string,
  text: string,
  deliveries: AgentMailDraftDeliveryInput[],
  otherRecipientCount: number,
): string | null {
  const trimmedSubject = subject.trim();
  if (
    !trimmedSubject ||
    Array.from(trimmedSubject).length > 200 ||
    /[\r\n]/u.test(subject)
  ) {
    return "Subject must be 1–200 characters on one line.";
  }
  if (!text || new TextEncoder().encode(text).length > 65_536) {
    return "Message must be between 1 byte and 64 KiB.";
  }
  if (!deliveries.length || deliveries.length > 100) {
    return "Add between 1 and 100 deliveries.";
  }
  let totalRecipients = 0;
  for (const delivery of deliveries) {
    if (!delivery.to.length)
      return "Every delivery needs at least one To address.";
    const recipients = [...delivery.to, ...delivery.cc];
    if (recipients.length > 50) {
      return "A delivery can contain at most 50 To and CC recipients.";
    }
    const unique = new Set(recipients);
    if (unique.size !== recipients.length) {
      return "The same address cannot appear twice in one delivery.";
    }
    if (recipients.some((address) => !simpleEmailPattern.test(address))) {
      return "Enter simple email addresses separated by commas.";
    }
    totalRecipients += unique.size;
  }
  return totalRecipients + Math.max(0, otherRecipientCount) > 100
    ? "A draft group can contain at most 100 recipients."
    : null;
}

const simpleEmailPattern = /^[^\s@<>(),:;"]+@[^\s@<>(),:;"]+$/u;

function editorKey(): string {
  return globalThis.crypto?.randomUUID?.() ?? Math.random().toString(36);
}

function recipientLabel(count: number): string {
  return `${count} ${count === 1 ? "recipient" : "recipients"}`;
}

function errorMessage(error: unknown, fallback: string): string {
  if (
    error &&
    typeof error === "object" &&
    "message" in error &&
    typeof error.message === "string"
  ) {
    return error.message;
  }
  return fallback;
}
