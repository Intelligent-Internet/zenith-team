import {
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type ComponentProps,
  type DragEvent,
} from "react";

import type { WorkflowAnswer } from "@/features/workflows/model/workflow-template";
import type { WorkflowUpload } from "@/features/workflows/model/workflow-uploads";
import type { WorkflowField } from "@/shared/api/types";
import { formatAttachmentSize } from "@/shared/chat/attachment-preview";
import { ChatAttachmentIcon } from "@/shared/chat/chat-attachment-preview";
import { cx } from "@/shared/lib/cx";
import { Textarea } from "@/shared/ui/core/textarea";
import { DateTimeInput } from "@/shared/ui/date-time-input";
import { Check, Paperclip, Plus, X } from "@/shared/ui/icons";
import { Input } from "@/shared/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/ui/select";

// Radix Select reserves "" for "nothing chosen", so an optional select offers
// clearing through this sentinel and stores "" in the answers.
const noSelection = "__workflow_no_selection__";
const textareaMaxHeight = 360;

/**
 * The shared SelectTrigger pins its border with `!important` utilities, which
 * plain feature CSS cannot outrank; these variant utilities can, so a select
 * shows focus and an invalid answer the way the dialog's other fields do.
 */
export const workflowSelectTriggerClassName =
  "workflow-field__select focus-visible:!border-[var(--workspace-native-accent)] aria-invalid:!border-[var(--workspace-native-danger)]";

/** A field that takes files gets them through these; others ignore them. */
export type WorkflowFieldFileHandlers = {
  uploads: readonly WorkflowUpload[];
  onAdd: (files: File[]) => void;
  onRemove: (localId: string) => void;
};

export function WorkflowFormField({
  error,
  field,
  files,
  idPrefix,
  onChange,
  value,
}: {
  error?: string;
  field: WorkflowField;
  files?: WorkflowFieldFileHandlers;
  idPrefix: string;
  onChange: (value: WorkflowAnswer) => void;
  value: WorkflowAnswer | undefined;
}) {
  const [dragging, setDragging] = useState(false);
  const takesFiles = Boolean(field.acceptsFiles && files);
  const controlId = `${idPrefix}-${field.key}`;
  const labelId = `${controlId}-label`;
  const helpId = `${controlId}-help`;
  const errorId = `${controlId}-error`;
  const describedBy =
    [field.description ? helpId : null, error ? errorId : null]
      .filter(Boolean)
      .join(" ") || undefined;
  const controlProps = {
    "aria-describedby": describedBy,
    "aria-invalid": error ? true : undefined,
    "aria-required": field.required || undefined,
  };
  const text = typeof value === "string" ? value : "";

  let control;
  switch (field.type) {
    case "textarea":
      control = (
        <GrowingTextarea
          id={controlId}
          placeholder={field.placeholder}
          value={text}
          onChange={onChange}
          {...controlProps}
        />
      );
      break;
    case "select":
      control = (
        <Select
          value={text}
          onValueChange={(next) => onChange(next === noSelection ? "" : next)}
        >
          <SelectTrigger
            id={controlId}
            aria-labelledby={labelId}
            className={workflowSelectTriggerClassName}
            {...controlProps}
          >
            <SelectValue
              placeholder={
                field.placeholder ??
                (field.required ? "Choose an option" : "No preference")
              }
            />
          </SelectTrigger>
          <SelectContent className="workflow-select-menu">
            {field.required ? null : (
              <SelectItem value={noSelection}>No preference</SelectItem>
            )}
            {(field.options ?? []).map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {option.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      );
      break;
    case "multi_select": {
      const chosen = Array.isArray(value) ? value : [];
      control = (
        <div
          className="workflow-field__chips"
          role="group"
          aria-labelledby={labelId}
          aria-describedby={describedBy}
        >
          {(field.options ?? []).map((option) => {
            const pressed = chosen.includes(option.value);
            return (
              <button
                key={option.value}
                type="button"
                className="workflow-field__chip"
                aria-pressed={pressed}
                onClick={() =>
                  onChange(
                    pressed
                      ? chosen.filter((item) => item !== option.value)
                      : [...chosen, option.value],
                  )
                }
              >
                {pressed ? (
                  <Check aria-hidden size={14} />
                ) : (
                  <Plus aria-hidden size={14} />
                )}
                {option.label}
              </button>
            );
          })}
        </div>
      );
      break;
    }
    case "date":
      control = (
        <DateTimeInput
          id={controlId}
          type="date"
          className="workflow-field__control"
          value={text}
          onChange={(event) => onChange(event.target.value)}
          {...controlProps}
        />
      );
      break;
    case "url":
      control = (
        <Input
          id={controlId}
          type="url"
          inputMode="url"
          autoComplete="url"
          className="workflow-field__control"
          placeholder={field.placeholder ?? "https://"}
          value={text}
          onChange={(event) => onChange(event.target.value)}
          {...controlProps}
        />
      );
      break;
    default:
      control = (
        <Input
          id={controlId}
          type="text"
          autoComplete="off"
          className="workflow-field__control"
          placeholder={field.placeholder}
          value={text}
          onChange={(event) => onChange(event.target.value)}
          {...controlProps}
        />
      );
  }

  const labelContent = (
    <>
      {field.label}
      {field.required ? (
        <span className="workflow-field__required" aria-hidden="true">
          *
        </span>
      ) : (
        <span className="workflow-field__optional">Optional</span>
      )}
    </>
  );

  // Files dropped anywhere on a field that takes them are attached to it.
  const dropProps = takesFiles
    ? {
        onDragOver: (event: DragEvent<HTMLDivElement>) => {
          if (!event.dataTransfer.types.includes("Files")) return;
          event.preventDefault();
          setDragging(true);
        },
        onDragLeave: (event: DragEvent<HTMLDivElement>) => {
          if (!event.currentTarget.contains(event.relatedTarget as Node | null))
            setDragging(false);
        },
        onDrop: (event: DragEvent<HTMLDivElement>) => {
          if (!event.dataTransfer.files.length) return;
          event.preventDefault();
          setDragging(false);
          files?.onAdd(Array.from(event.dataTransfer.files));
        },
      }
    : {};

  return (
    <div
      className="workflow-field"
      data-dragging={dragging ? "true" : undefined}
      data-invalid={error ? "true" : undefined}
      data-workflow-field={field.key}
      {...dropProps}
    >
      {field.type === "multi_select" || field.type === "select" ? (
        <span className="workflow-field__label" id={labelId}>
          {labelContent}
        </span>
      ) : (
        <label
          className="workflow-field__label"
          id={labelId}
          htmlFor={controlId}
        >
          {labelContent}
        </label>
      )}
      {control}
      {takesFiles && files ? (
        <WorkflowFieldFiles label={field.label} {...files} />
      ) : null}
      {field.description ? (
        <p className="workflow-field__help" id={helpId}>
          {field.description}
        </p>
      ) : null}
      {error ? (
        <p className="workflow-field__error" id={errorId}>
          {error}
        </p>
      ) : null}
    </div>
  );
}

function uploadStatus(upload: WorkflowUpload): string {
  if (upload.status === "error") return "Upload failed";
  if (upload.status === "uploading") {
    return upload.progress === undefined
      ? "Uploading…"
      : `Uploading ${upload.progress}%`;
  }
  return formatAttachmentSize(upload.sizeBytes);
}

/**
 * The files attached to a field that takes them, and the button that adds
 * more. Each file uploads as soon as it is picked, so Run waits only for the
 * ones still on their way.
 */
function WorkflowFieldFiles({
  label,
  onAdd,
  onRemove,
  uploads,
}: WorkflowFieldFileHandlers & { label: string }) {
  const inputRef = useRef<HTMLInputElement>(null);
  const filesRef = useRef<HTMLDivElement>(null);
  // Removing a file removes the button that had focus, so focus moves to the
  // Remove of the file that takes its place, else the one before, else
  // Attach files.
  const removedIndexRef = useRef<number | null>(null);
  useEffect(() => {
    const index = removedIndexRef.current;
    if (index === null) return;
    removedIndexRef.current = null;
    const root = filesRef.current;
    if (!root) return;
    const removes = root.querySelectorAll<HTMLElement>(
      ".workflow-field__file-remove",
    );
    (
      removes[Math.min(index, removes.length - 1)] ??
      root.querySelector<HTMLElement>(".workflow-field__attach")
    )?.focus();
  }, [uploads]);
  return (
    <div className="workflow-field__files" ref={filesRef}>
      {uploads.length ? (
        <ul
          className="workflow-field__file-list"
          aria-label={`Files for ${label}`}
        >
          {uploads.map((upload, index) => (
            <li
              key={upload.localId}
              className="workflow-field__file"
              data-status={upload.status}
            >
              <ChatAttachmentIcon
                file={{
                  id: upload.localId,
                  name: upload.filename,
                  mediaType: upload.contentType,
                }}
                size={18}
              />
              <span
                className="workflow-field__file-name"
                title={upload.filename}
              >
                {upload.filename}
              </span>
              <span className="workflow-field__file-status" aria-live="polite">
                {uploadStatus(upload)}
              </span>
              <button
                type="button"
                className="workflow-field__file-remove"
                aria-label={`Remove ${upload.filename}`}
                onClick={() => {
                  removedIndexRef.current = index;
                  onRemove(upload.localId);
                }}
              >
                <X aria-hidden size={14} />
              </button>
            </li>
          ))}
        </ul>
      ) : null}
      <div className="workflow-field__attach-row">
        <button
          type="button"
          className="workflow-field__attach"
          onClick={() => inputRef.current?.click()}
        >
          <Paperclip aria-hidden size={14} />
          Attach files
        </button>
        <span className="workflow-field__attach-hint">
          Or drop them here. The Agent reads them as this Task's files.
        </span>
      </div>
      <input
        ref={inputRef}
        type="file"
        multiple
        hidden
        tabIndex={-1}
        onChange={(event) => {
          const picked = Array.from(event.target.files ?? []);
          event.target.value = "";
          if (picked.length) onAdd(picked);
        }}
      />
    </div>
  );
}

function GrowingTextarea({
  className,
  onChange,
  value,
  ...props
}: Omit<ComponentProps<"textarea">, "onChange" | "value"> & {
  onChange: (value: string) => void;
  value: string;
}) {
  const ref = useRef<HTMLTextAreaElement>(null);
  // Grow with the answer up to a cap, then scroll: pasted contracts and
  // notes stay readable without pushing the Run button off the dialog.
  useLayoutEffect(() => {
    const element = ref.current;
    if (!element) return;
    element.style.height = "auto";
    element.style.height = `${Math.min(element.scrollHeight + 2, textareaMaxHeight)}px`;
  }, [value]);
  return (
    <Textarea
      ref={ref}
      rows={5}
      className={cx(
        "workflow-field__control workflow-field__textarea",
        className,
      )}
      value={value}
      onChange={(event) => onChange(event.target.value)}
      {...props}
    />
  );
}
