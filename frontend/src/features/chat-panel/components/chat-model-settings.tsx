import * as Popover from "@radix-ui/react-popover";
import { useEffect, useRef, useState } from "react";
import { Check, ChevronDown, ChevronLeft, RefreshCw } from "@/shared/ui/icons";
import {
  useGetConversationModelSettingsQuery,
  useUpdateConversationModelSettingsMutation,
} from "@/features/chat-panel/api/conversation-model-settings-api";
import {
  useListCompanyAgentsQuery,
  useListAgentRuntimesQuery,
} from "@/shared/api/agent-runtime-api";
import { useRuntimeModelDiscovery } from "@/features/agent-setup/hooks/use-runtime-model-discovery";
import { getBoardlyQueryErrorMessage } from "@/shared/api/boardly-api";
import type { ConversationModelSettingsUpdate } from "@/shared/api/types";
import { chatModelLabel } from "@/features/chat-panel/model/chat-model-label";
import { ChatThinkingSlider } from "./chat-thinking-slider";
import "@/features/chat-panel/styles/chat-model-settings.css";

type ModelChoice = Pick<
  ConversationModelSettingsUpdate,
  "model" | "thinkingLevel"
>;

export function ChatModelSettings({
  companyId,
  conversationId,
  onSavingChange,
  initiallyOpen = false,
}: {
  companyId: string;
  conversationId: string;
  onSavingChange: (saving: boolean) => void;
  initiallyOpen?: boolean;
}) {
  const [open, setOpen] = useState(initiallyOpen);
  const [view, setView] = useState<"thinking" | "models">("thinking");
  const [pendingChoice, setPendingChoice] = useState<ModelChoice | null>(null);
  const [previewThinking, setPreviewThinking] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const saving = useRef(false);
  const query = useGetConversationModelSettingsQuery(conversationId, {
    refetchOnMountOrArgChange: true,
  });
  const [update, mutation] = useUpdateConversationModelSettingsMutation();
  const settings = query.currentData;
  const choice = pendingChoice ?? settings;
  const agentsQuery = useListCompanyAgentsQuery(companyId, {
    skip: !companyId,
  });
  const runtimesQuery = useListAgentRuntimesQuery(companyId, {
    skip: !companyId,
  });
  const agent = agentsQuery.currentData?.items.find(
    (item) => item.id === settings?.agentId,
  );
  const runtime =
    runtimesQuery.currentData?.items.find(
      (item) => item.id === agent?.binding?.runtime.id,
    ) ?? null;
  const discovery = useRuntimeModelDiscovery({ companyId, runtime });
  const model =
    discovery.models.find((item) => item.id === choice?.model) ??
    (!choice?.model
      ? discovery.models.find((item) => item.default)
      : undefined);
  const thinkingOptions = model?.reasoning?.levels ?? [];
  const selectedThinking =
    previewThinking ??
    choice?.thinkingLevel ??
    model?.reasoning?.defaultLevel ??
    thinkingOptions[0]?.value;
  const previewIndex = thinkingOptions.findIndex(
    (item) => item.value === selectedThinking,
  );
  const galaxy =
    thinkingOptions.length > 1 &&
    previewIndex === thinkingOptions.length - 1 &&
    (selectedThinking === "max" || selectedThinking === "ultra");
  const modelLabel = choice?.model
    ? chatModelLabel(model?.label ?? choice.model)
    : "Provider default";
  const thinkingLabel = choice?.thinkingLevel
    ? (thinkingOptions.find((item) => item.value === choice.thinkingLevel)
        ?.label ?? choice.thinkingLevel)
    : "Default thinking";
  const discovering =
    agentsQuery.isLoading ||
    runtimesQuery.isLoading ||
    (!!runtime && ["idle", "pending", "running"].includes(discovery.status));
  const thinkingButton = useRef<HTMLButtonElement>(null);
  const selectedModelButton = useRef<HTMLButtonElement>(null);

  useEffect(() => () => onSavingChange(false), [onSavingChange]);
  useEffect(() => {
    if (!open) return;
    if (view === "models")
      selectedModelButton.current?.focus({ preventScroll: true });
    else thinkingButton.current?.focus({ preventScroll: true });
  }, [open, view]);

  async function save(values: ModelChoice) {
    if (!settings || saving.current) return;
    if (
      values.model === settings.model &&
      values.thinkingLevel === settings.thinkingLevel
    )
      return;
    saving.current = true;
    setPendingChoice(values);
    setError(null);
    onSavingChange(true);
    try {
      await update({
        conversationId,
        payload: {
          agentId: settings.agentId,
          expectedRevision: settings.revision,
          ...values,
        },
      }).unwrap();
    } catch (cause) {
      setError(
        getBoardlyQueryErrorMessage(cause, "Could not save. Try again."),
      );
    } finally {
      saving.current = false;
      setPendingChoice(null);
      onSavingChange(false);
    }
  }

  if (!settings) {
    return query.isError ? (
      <button
        type="button"
        className="chat-model-trigger"
        onClick={() => void query.refetch()}
      >
        Retry model settings
      </button>
    ) : (
      <span className="chat-model-loading">Loading model…</span>
    );
  }

  const disabled = !settings.canEdit || mutation.isLoading;
  const help = settings.canEdit
    ? "Saved for this chat. Changes apply to the next run."
    : "Only the Task assignee can change this chat’s model.";
  const catalogMessage = discovering
    ? "Loading models from your computer…"
    : discovery.message ||
      (!runtime || runtime.status !== "online"
        ? "Connect this Agent’s computer to see available models."
        : agentsQuery.isError || runtimesQuery.isError
          ? "Could not load your computer’s models. Try again."
          : "");

  return (
    <div className="chat-model-settings">
      <Popover.Root
        open={open}
        onOpenChange={(next) => {
          setOpen(next);
          if (next) {
            setView("thinking");
          }
        }}
      >
        <Popover.Trigger
          type="button"
          className="chat-model-trigger"
          aria-label={`Model and thinking: ${modelLabel}, ${thinkingLabel}`}
          title={help}
        >
          <span>{modelLabel}</span>
          <span className="chat-model-thinking-label">{thinkingLabel}</span>
          <ChevronDown size={13} aria-hidden />
        </Popover.Trigger>
        <Popover.Portal>
          <Popover.Content
            className="chat-model-popover"
            aria-label="Chat model and thinking"
            align="end"
            side="top"
            sideOffset={10}
            collisionPadding={12}
            onOpenAutoFocus={(event) => {
              event.preventDefault();
              thinkingButton.current?.focus({ preventScroll: true });
            }}
          >
            <div className="chat-model-popover-surface">
              {view === "thinking" ? (
                <div className="chat-model-thinking-panel">
                  <div className="chat-thinking-heading">
                    <button
                      ref={thinkingButton}
                      type="button"
                      className="chat-model-switch"
                      onClick={() => setView("models")}
                      aria-label="Select model"
                    >
                      <span className="chat-thinking-title">
                        <ChatThinkingLabel
                          label={
                            thinkingOptions[previewIndex]?.label ??
                            thinkingLabel
                          }
                          index={previewIndex}
                          galaxy={galaxy}
                        />
                      </span>
                      <span className="chat-model-switch-name">
                        <span>{modelLabel}</span>
                        <ChevronDown size={12} aria-hidden />
                      </span>
                    </button>
                    <button
                      type="button"
                      className="chat-model-icon-button"
                      aria-label="Refresh models"
                      disabled={discovering || runtime?.status !== "online"}
                      onClick={discovery.refresh}
                    >
                      <RefreshCw size={15} aria-hidden />
                    </button>
                  </div>
                  {thinkingOptions.length > 0 ? (
                    <ChatThinkingSlider
                      key={choice?.model ?? "default"}
                      levels={thinkingOptions}
                      value={
                        choice?.thinkingLevel ??
                        model?.reasoning?.defaultLevel ??
                        thinkingOptions[0].value
                      }
                      galaxy={galaxy}
                      disabled={disabled}
                      onPreview={setPreviewThinking}
                      onSave={(thinkingLevel) =>
                        save({ model: settings.model, thinkingLevel })
                      }
                    />
                  ) : (
                    <p className="chat-model-note">
                      {catalogMessage ||
                        (model
                          ? "This model does not offer adjustable thinking levels."
                          : "Select a model to adjust thinking.")}
                    </p>
                  )}
                </div>
              ) : (
                <div className="chat-model-list">
                  <div className="chat-model-list-heading">
                    <button
                      type="button"
                      className="chat-model-icon-button"
                      onClick={() => setView("thinking")}
                      aria-label="Back to thinking"
                    >
                      <ChevronLeft size={16} aria-hidden />
                    </button>
                    <span>Select model</span>
                    <button
                      type="button"
                      className="chat-model-icon-button"
                      title="Refresh models"
                      aria-label="Refresh models"
                      disabled={discovering || runtime?.status !== "online"}
                      onClick={discovery.refresh}
                    >
                      <RefreshCw size={15} aria-hidden />
                    </button>
                  </div>
                  <div
                    className="chat-model-options"
                    role="group"
                    aria-label="Available models"
                  >
                    {(!settings.model ||
                      !discovery.models.some(
                        (item) => item.id === settings.model,
                      )) && (
                      <button
                        ref={selectedModelButton}
                        type="button"
                        className="chat-model-option"
                        aria-pressed={!pendingChoice}
                        onClick={() => setView("thinking")}
                      >
                        <span>{chatModelLabel(settings.model)}</span>
                        <Check size={16} aria-hidden />
                      </button>
                    )}
                    {discovery.models.map((item) => (
                      <button
                        key={item.id}
                        ref={
                          item.id === choice?.model
                            ? selectedModelButton
                            : undefined
                        }
                        type="button"
                        className="chat-model-option"
                        aria-pressed={item.id === choice?.model}
                        disabled={disabled}
                        onClick={() => {
                          setView("thinking");
                          if (item.id !== settings.model)
                            void save({
                              model: item.id,
                              thinkingLevel:
                                item.reasoning?.defaultLevel ?? null,
                            });
                        }}
                      >
                        <span>{chatModelLabel(item.label)}</span>
                        {item.id === choice?.model && (
                          <Check size={16} aria-hidden />
                        )}
                      </button>
                    ))}
                  </div>
                  {catalogMessage && (
                    <p className="chat-model-note" role="status">
                      {catalogMessage}
                    </p>
                  )}
                </div>
              )}
              <span className="sr-only" role="status">
                {mutation.isLoading ? "Saving…" : ""}
              </span>
              {!settings.canEdit && (
                <p className="chat-model-note" title={help}>
                  Read only
                </p>
              )}
              {error && (
                <p className="chat-model-error" role="alert">
                  {error}
                </p>
              )}
            </div>
          </Popover.Content>
        </Popover.Portal>
      </Popover.Root>
      {error && !open && (
        <p className="chat-model-error" role="alert">
          {error}
        </p>
      )}
    </div>
  );
}

function ChatThinkingLabel({
  label,
  index,
  galaxy,
}: {
  label: string;
  index: number;
  galaxy: boolean;
}) {
  const output = useRef<HTMLOutputElement>(null);
  const previousIndex = useRef(index);

  useEffect(() => {
    const previous = previousIndex.current;
    previousIndex.current = index;
    if (
      previous === index ||
      window.matchMedia("(prefers-reduced-motion: reduce)").matches
    )
      return;
    const direction = index > previous ? 1 : -1;
    const animation = output.current?.animate(
      [
        { transform: `translateY(${direction * 7}px)`, opacity: 0 },
        { transform: `translateY(${-direction}px)`, opacity: 1, offset: 0.8 },
        { transform: "translateY(0)", opacity: 1 },
      ],
      { duration: 210, easing: "cubic-bezier(.2,.75,.25,1)" },
    );
    return () => animation?.cancel();
  }, [index]);

  return (
    <output ref={output} data-galaxy={galaxy} aria-live="polite">
      {label}
    </output>
  );
}
