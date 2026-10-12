import { Bot, ChevronDown, FileText, TextCursorInput } from "@/shared/ui/icons";
import { useEffect, useLayoutEffect, useRef } from "react";

import { agentAvailabilityLabel } from "@/features/agents/model/agent-binding";
import {
  ScheduledFieldControl,
  scheduledNativeControlClassName,
} from "@/features/scheduled/components/scheduled-field-control";
import type { ScheduleFormValues } from "@/features/scheduled/model/schedule-form";
import type { AgentDto } from "@/shared/api/types";

export function ScheduleEditorBasics({
  agents,
  error,
  onChange,
  values,
}: {
  agents: AgentDto[];
  error: string | null;
  onChange: (next: ScheduleFormValues) => void;
  values: ScheduleFormValues;
}) {
  const nameRef = useRef<HTMLInputElement>(null);
  const instructionsRef = useRef<HTMLTextAreaElement>(null);
  const agentRef = useRef<HTMLSelectElement>(null);
  const invalidField = error
    ? !values.name.trim()
      ? "name"
      : !values.instructions.trim()
        ? "instructions"
        : !values.agentId
          ? "agent"
          : null
    : null;

  useEffect(() => {
    if (invalidField === "name") nameRef.current?.focus();
    if (invalidField === "instructions") instructionsRef.current?.focus();
    if (invalidField === "agent") agentRef.current?.focus();
  }, [invalidField]);

  useLayoutEffect(() => {
    const textarea = instructionsRef.current;
    if (!textarea) return;
    textarea.style.height = "auto";
    textarea.style.height = `${Math.min(420, Math.max(160, textarea.scrollHeight))}px`;
  }, [values.instructions]);

  return (
    <div className="grid gap-6">
      <EditorField
        description="A clear name makes this schedule easy to find later."
        label="Schedule name"
        error={invalidField === "name" ? error : null}
        errorId="schedule-name-error"
        required
      >
        <ScheduledFieldControl icon={<TextCursorInput className="size-5" />}>
          <input
            ref={nameRef}
            aria-required="true"
            aria-invalid={invalidField === "name"}
            aria-describedby={
              invalidField === "name" ? "schedule-name-error" : undefined
            }
            aria-label="Schedule name"
            autoFocus
            className={scheduledNativeControlClassName}
            maxLength={240}
            placeholder="Morning operations review"
            value={values.name}
            onChange={(event) =>
              onChange({ ...values, name: event.target.value })
            }
          />
        </ScheduledFieldControl>
      </EditorField>

      <EditorField
        description="Each occurrence starts a fresh agent run with these instructions. Make the expected output explicit."
        label="Instructions"
        error={invalidField === "instructions" ? error : null}
        errorId="schedule-instructions-error"
        required
      >
        <ScheduledFieldControl
          className="h-auto"
          icon={<FileText className="size-5" />}
          multiline
        >
          <textarea
            ref={instructionsRef}
            aria-required="true"
            aria-invalid={invalidField === "instructions"}
            aria-describedby={
              invalidField === "instructions"
                ? "schedule-instructions-error"
                : undefined
            }
            aria-label="Instructions"
            className={`${scheduledNativeControlClassName} h-full resize-none leading-6`}
            maxLength={65_536}
            placeholder="Review the selected sources, summarize what changed, and list the next actions."
            value={values.instructions}
            onChange={(event) =>
              onChange({ ...values, instructions: event.target.value })
            }
          />
        </ScheduledFieldControl>
      </EditorField>

      <EditorField
        description="The selected agent determines the provider and which connected sources are compatible."
        label="Agent"
        error={invalidField === "agent" ? error : null}
        errorId="schedule-agent-error"
        required
      >
        <ScheduledFieldControl
          icon={<Bot className="size-5" />}
          trailing={<ChevronDown className="size-4" />}
        >
          <select
            ref={agentRef}
            aria-required="true"
            aria-invalid={invalidField === "agent"}
            aria-describedby={
              invalidField === "agent" ? "schedule-agent-error" : undefined
            }
            aria-label="Agent"
            className={`${scheduledNativeControlClassName} cursor-pointer appearance-none`}
            value={values.agentId}
            onChange={(event) =>
              onChange({
                ...values,
                agentId: event.target.value,
                sourceIds: [],
              })
            }
          >
            <option value="">Choose an agent</option>
            {/* A schedule fires on the viewer's own setup of the agent
                (ADR 0014), so an agent they have not set up says so rather
                than looking like an offline computer. */}
            {agents.map((agent) => (
              <option key={agent.id} value={agent.id}>
                {agent.name} · {agent.title} · {agent.provider}
                {agent.runtimeAvailable
                  ? ""
                  : ` (${agentAvailabilityLabel(agent).toLowerCase()})`}
              </option>
            ))}
          </select>
        </ScheduledFieldControl>
      </EditorField>
    </div>
  );
}

export function EditorField({
  children,
  description,
  error,
  errorId,
  label,
  required = false,
}: {
  children: React.ReactNode;
  description?: string;
  error?: string | null;
  errorId?: string;
  label: string;
  required?: boolean;
}) {
  return (
    <label className="grid gap-2">
      <span className="text-sm font-semibold text-[var(--workspace-native-text)]">
        {label}
        {required ? (
          <span className="ml-2 text-xs font-normal text-[var(--workspace-native-text-muted)]">
            Required
          </span>
        ) : null}
      </span>
      {description ? (
        <span className="text-xs leading-5 text-[var(--workspace-native-text-muted)]">
          {description}
        </span>
      ) : null}
      {children}
      {error ? (
        <span
          id={errorId}
          className="text-sm text-[var(--workspace-native-danger)]"
          role="alert"
        >
          {error}
        </span>
      ) : null}
    </label>
  );
}
