import { useRef, useState, type SelectHTMLAttributes } from "react";
import {
  Check,
  ChevronDown,
  Image,
  Info,
  Layers,
  Loader2,
  LockKeyhole,
  MessageSquare,
  Mic,
  Music,
  Search,
  Settings2,
  Text,
  Video,
  X,
} from "@/shared/ui/icons";
import { Tooltip } from "@/shared/ui/tooltip";
import {
  findStudioModel,
  studioDisplayName,
  type StudioModel,
  type StudioModelCatalog,
} from "@/shared/api/studio-model-catalog";
import {
  studioNodeKindLabel,
  canGenerateStudioNode,
  isStudioNodeRunning,
  type StudioGraphNode,
  type StudioBootstrap,
} from "@/shared/studio-graph/model";
import {
  fieldsFor,
  fieldValue,
  metadataPatch,
  studioModelMetadataPatch,
  videoRoute,
  type Field,
  type Metadata,
} from "../model-settings";
import "./studio-node-settings.css";
import { StudioGenerateButton } from "@/shared/studio-graph/studio-generate-button";
import { StudioOutputVersions } from "./studio-output-versions";

function StudioSettingsSelect({
  children,
  ...props
}: SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <span className="studio-settings__select-control">
      <select {...props}>{children}</select>
      <ChevronDown size={14} aria-hidden />
    </span>
  );
}

function StudioSettingFields({
  fields,
  metadata,
  model,
  onMetadata,
}: {
  fields: Field[];
  metadata: Metadata;
  model?: StudioModel;
  onMetadata: (patch: Metadata) => void;
}) {
  return (
    <div className="studio-settings__fields">
      {fields.map((field) => {
        const value = fieldValue(metadata, field);
        const change = (value: unknown) =>
          onMetadata(metadataPatch(field.key, value, model?.family));
        return (
          <label
            key={field.key}
            className={
              field.type === "boolean"
                ? "studio-settings__toggle"
                : field.type === "text"
                  ? "studio-settings__wide-field"
                  : undefined
            }
          >
            <span>{field.label}</span>
            {field.options ? (
              <StudioSettingsSelect
                value={String(value ?? "")}
                onChange={(event) => change(event.target.value)}
              >
                <option value="">Default</option>
                {value != null && !field.options.includes(String(value)) && (
                  <option value={String(value)}>
                    {String(value)} (current)
                  </option>
                )}
                {field.options.map((option) => (
                  <option key={option} value={option}>
                    {option.includes("_") || /^[a-z]+$/.test(option)
                      ? studioDisplayName(option)
                      : option}
                  </option>
                ))}
              </StudioSettingsSelect>
            ) : field.type === "boolean" ? (
              field.fixed ? (
                <span className="studio-settings__fixed-value">
                  <Check size={13} /> Included
                </span>
              ) : (
                <StudioSettingsSelect
                  value={
                    value === true || value === "Yes"
                      ? "true"
                      : value === false || value === "No"
                        ? "false"
                        : ""
                  }
                  onChange={(event) =>
                    change(
                      event.target.value === ""
                        ? null
                        : event.target.value === "true",
                    )
                  }
                >
                  <option value="">Default</option>
                  <option value="true">On</option>
                  <option value="false">Off</option>
                </StudioSettingsSelect>
              )
            ) : field.key === "negative_prompt" ||
              field.key === "lyrics_prompt" ? (
              <textarea
                rows={3}
                placeholder={
                  field.key === "negative_prompt"
                    ? "Describe what to avoid…"
                    : "Write your lyrics…"
                }
                value={String(value ?? "")}
                onChange={(event) => change(event.target.value)}
              />
            ) : (
              <input
                type={field.type}
                min={field.min}
                max={field.max}
                step={field.step}
                placeholder="Default"
                value={
                  typeof value === "number" || typeof value === "string"
                    ? value
                    : ""
                }
                onChange={(event) =>
                  change(
                    field.type === "number"
                      ? event.target.value === ""
                        ? null
                        : Number(event.target.value)
                      : event.target.value,
                  )
                }
              />
            )}
          </label>
        );
      })}
    </div>
  );
}

export function StudioNodeSettings({
  node,
  bootstrap,
  catalog,
  catalogError,
  onRetryCatalog,
  label,
  metadata,
  dirty,
  saving = false,
  saveError,
  modelSaving = false,
  restoring = false,
  onRestoreVersion,
  generationPending = false,
  onGenerate,
  connectionUnavailable = false,
  onLabel,
  onMetadata,
  onSave,
  onDiscard,
  onClose,
  onError,
  conflict = false,
  onKeepDraft,
}: {
  node: StudioGraphNode;
  bootstrap: StudioBootstrap;
  catalog: StudioModelCatalog | null;
  catalogError: string | null;
  onRetryCatalog: () => void;
  label: string;
  metadata: Metadata;
  dirty: boolean;
  saving?: boolean;
  saveError?: string;
  modelSaving?: boolean;
  restoring?: boolean;
  onRestoreVersion: (nodeId: string, versionId: string) => Promise<void>;
  generationPending?: boolean;
  onGenerate: (nodeId: string) => Promise<void>;
  connectionUnavailable?: boolean;
  onLabel: (value: string) => void;
  onMetadata: (patch: Metadata) => void;
  onSave: () => Promise<void>;
  onDiscard: () => void;
  onClose: () => void;
  onError: (error: unknown) => void;
  conflict?: boolean;
  onKeepDraft?: () => Promise<void>;
}) {
  const [pickerOpen, setPickerOpen] = useState(false);
  const modelTrigger = useRef<HTMLButtonElement>(null);
  const closeModelPicker = () => {
    setPickerOpen(false);
    modelTrigger.current?.focus();
  };
  const [search, setSearch] = useState("");
  const [saveFeedback, setSaveFeedback] = useState<{
    state: "saved" | "error";
    message: string;
  } | null>(null);
  const editMetadata = (patch: Metadata) => {
    setSaveFeedback(null);
    onMetadata(patch);
  };
  const editLabel = (value: string) => {
    setSaveFeedback(null);
    onLabel(value);
  };
  const models = catalog?.models[node.raw.nodeType] ?? [];
  const modelId = String(metadata.model ?? metadata.model_name ?? "");
  const model = findStudioModel(models, modelId);
  const route = videoRoute(metadata, node.id, bootstrap);
  const fields = fieldsFor(model, route, metadata);
  if (node.kind === "infographic") {
    fields.push(
      { key: "language", label: "Language", type: "text" },
      { key: "style", label: "Style", type: "text" },
    );
  }
  const generative = [
    "text",
    "image",
    "infographic",
    "video",
    "audio",
    "voice",
  ].includes(node.kind);
  const locked =
    connectionUnavailable ||
    node.readOnly ||
    restoring ||
    modelSaving ||
    generationPending ||
    isStudioNodeRunning(node);
  const persist = async (save: () => Promise<void>) => {
    if (!dirty || locked || saving) return;
    setSaveFeedback(null);
    try {
      await save();
    } catch (error: unknown) {
      setSaveFeedback({
        state: "error",
        message:
          error instanceof Error && error.message
            ? error.message
            : typeof error === "string" && error
              ? error
              : "Changes could not be saved. Your edits are still here; try saving again.",
      });
      onError(error);
    }
  };
  const NodeIcon = {
    image: Image,
    infographic: Image,
    video: Video,
    audio: Music,
    voice: Mic,
    comment: MessageSquare,
    node_group: Layers,
    text: Text,
    unknown: Layers,
  }[node.kind];
  const primaryFields = fields.filter((field) =>
    [
      "ratio",
      "resolution",
      "duration",
      "duration_seconds",
      "quality",
      "output_format",
      "audio_included",
      "language",
      "style",
    ].includes(field.key),
  );
  const advancedFields = fields.filter(
    (field) => !primaryFields.includes(field),
  );
  const selectModel = (next: StudioModel) => {
    editMetadata(studioModelMetadataPatch(next, model, metadata, route));
    closeModelPicker();
    setSearch("");
  };
  const filtered = models.filter((item) =>
    `${item.label} ${item.id} ${item.provider} ${item.description}`
      .toLowerCase()
      .includes(search.toLowerCase()),
  );
  const voice = model?.voices.find(
    (voice) => voice.value === (metadata.voice ?? metadata.voice_name),
  );
  return (
    <aside
      className="studio-mode-panel__inspector studio-node-settings"
      aria-label="Studio node inspector"
      onKeyDown={(event) => {
        if (event.key !== "Escape") return;
        event.stopPropagation();
        if (pickerOpen) closeModelPicker();
        else onClose();
      }}
    >
      <div className="studio-settings__heading">
        <div className="studio-settings__node-icon">
          <NodeIcon size={20} />
        </div>
        <div className="studio-settings__heading-copy">
          <strong>{studioNodeKindLabel(node.kind)}</strong>
          <div
            className="studio-settings__node-state"
            data-status={node.raw.status}
            aria-live="polite"
          >
            {generative &&
              (generationPending ||
                !["draft", "success"].includes(node.raw.status)) && (
                <span className="studio-settings__status">
                  {node.raw.status === "cancelling"
                    ? "Cancelling"
                    : generationPending || isStudioNodeRunning(node)
                      ? "Generating"
                      : studioDisplayName(node.raw.status)}
                </span>
              )}
            {node.readOnly ? (
              <span>
                <LockKeyhole size={11} /> Read-only
              </span>
            ) : conflict || saveError ? (
              <span>
                <Info size={11} /> Not saved
              </span>
            ) : dirty || saving || modelSaving ? (
              <span>
                <Loader2 size={11} className="studio-settings__saving-icon" />{" "}
                Saving…
              </span>
            ) : (
              <span>
                <Check size={11} /> Saved
              </span>
            )}
          </div>
        </div>
        <Tooltip label="Close settings">
          <button type="button" onClick={onClose} aria-label="Close inspector">
            <X size={17} />
          </button>
        </Tooltip>
      </div>
      <form
        onInvalidCapture={(event) => {
          const input = event.target;
          if (!(input instanceof HTMLInputElement)) return;
          const advanced = input.closest("details");
          if (advanced) advanced.open = true;
          input.scrollIntoView({ block: "nearest" });
          setSaveFeedback({ state: "error", message: input.validationMessage });
        }}
        onSubmit={(event) => {
          event.preventDefault();
          if (conflict) return;
          void persist(onSave);
        }}
      >
        <fieldset disabled={locked}>
          <label className="studio-settings__name-field">
            <span>Name</span>
            <input
              aria-label="Node name"
              value={label}
              placeholder="Untitled node"
              onChange={(event) => editLabel(event.target.value)}
            />
          </label>
          {generative && (
            <section className="studio-settings__section">
              <div className="studio-settings__section-heading">
                <h3>Model</h3>
                {model?.pro && (
                  <span className="studio-settings__pro">Pro</span>
                )}
              </div>
              <Tooltip
                disabled={!model}
                label={model ? model.description || model.label : ""}
              >
                <button
                  ref={modelTrigger}
                  className="studio-settings__model-button"
                  type="button"
                  aria-label={`Model: ${model?.label || studioDisplayName(modelId)}`}
                  aria-expanded={pickerOpen}
                  aria-controls="studio-model-options"
                  disabled={!models.length}
                  onClick={() => setPickerOpen(!pickerOpen)}
                >
                  <span className="studio-settings__model-name">
                    <strong>
                      {model?.label ||
                        (modelId
                          ? studioDisplayName(modelId)
                          : catalogError
                            ? "Models unavailable"
                            : catalog
                              ? "Choose model"
                              : "Loading models…")}
                    </strong>
                  </span>
                  <ChevronDown size={16} />
                </button>
              </Tooltip>
              {catalogError && (
                <div className="studio-settings__catalog-error">
                  <p>
                    <Info size={14} /> Models could not be loaded.
                  </p>
                  <button type="button" onClick={onRetryCatalog}>
                    Try again
                  </button>
                </div>
              )}
              {pickerOpen && (
                <div className="studio-settings__model-picker">
                  <div className="studio-settings__search">
                    <Search size={14} />
                    <input
                      aria-label="Search models"
                      placeholder="Search models…"
                      value={search}
                      onChange={(event) => setSearch(event.target.value)}
                      onKeyDown={(event) => {
                        if (event.key === "Enter") event.preventDefault();
                      }}
                      autoFocus
                    />
                  </div>
                  <div
                    className="studio-settings__model-options"
                    id="studio-model-options"
                  >
                    {filtered.map((item) => (
                      <Tooltip
                        key={item.id}
                        label={item.description || item.label}
                      >
                        <button
                          type="button"
                          aria-pressed={model?.id === item.id}
                          onClick={() => selectModel(item)}
                        >
                          <span className="studio-settings__option-copy">
                            <strong>{item.label}</strong>
                          </span>
                          {item.pro && <small>Pro</small>}
                          {model?.id === item.id && <Check size={14} />}
                        </button>
                      </Tooltip>
                    ))}
                    {!filtered.length && (
                      <p className="studio-settings__empty">
                        No models match “{search}”.
                      </p>
                    )}
                  </div>
                </div>
              )}
            </section>
          )}
          {node.kind !== "node_group" && (
            <section className="studio-settings__section">
              <h3 className="studio-settings__section-title">
                {node.kind === "comment" ? "Note" : "Prompt"}
              </h3>
              <label className="studio-settings__prompt-field">
                <textarea
                  aria-label={node.kind === "comment" ? "Note" : "Prompt"}
                  value={String(metadata.prompt ?? "")}
                  placeholder={
                    node.kind === "comment"
                      ? "Write a note for your team…"
                      : "Describe what you want to create…"
                  }
                  onChange={(event) =>
                    editMetadata({ prompt: event.target.value })
                  }
                />
              </label>
            </section>
          )}
          {model?.voices.length ? (
            <section className="studio-settings__section">
              <h3 className="studio-settings__section-title">Voice</h3>
              {catalog?.voiceProfiles.length ? (
                <label>
                  Voice profile
                  <StudioSettingsSelect
                    value={String(metadata.voice_profile ?? "")}
                    onChange={(event) => {
                      const profile = event.target.value;
                      const speaker = model.profileVoices[profile];
                      editMetadata({
                        voice_profile: profile || null,
                        ...(typeof speaker === "string"
                          ? { voice: speaker, voice_name: speaker }
                          : {}),
                      });
                    }}
                  >
                    <option value="">Custom</option>
                    {catalog.voiceProfiles.map((profile) => (
                      <option key={profile.value} value={profile.value}>
                        {profile.label}
                      </option>
                    ))}
                  </StudioSettingsSelect>
                </label>
              ) : null}
              <label>
                Speaker
                <StudioSettingsSelect
                  value={String(metadata.voice ?? metadata.voice_name ?? "")}
                  onChange={(event) =>
                    editMetadata({
                      voice: event.target.value,
                      voice_name: event.target.value,
                    })
                  }
                >
                  <option value="">Default</option>
                  {model.voices.map((voice) => (
                    <option key={voice.value} value={voice.value}>
                      {voice.label}
                    </option>
                  ))}
                </StudioSettingsSelect>
              </label>
              {voice?.previewUrl && (
                <audio
                  aria-label={`${voice.label} voice sample`}
                  src={voice.previewUrl}
                  controls
                  preload="none"
                />
              )}
            </section>
          ) : null}
          {primaryFields.length > 0 && (
            <section className="studio-settings__section studio-settings__section--output">
              <h3 className="studio-settings__section-title">Output</h3>
              <StudioSettingFields
                fields={primaryFields}
                metadata={metadata}
                model={model}
                onMetadata={editMetadata}
              />
            </section>
          )}
          <StudioOutputVersions
            node={node}
            disabled={locked || saving || conflict}
            restoring={restoring}
            onRestore={onRestoreVersion}
            onError={onError}
          />
          {advancedFields.length > 0 && (
            <details className="studio-settings__advanced">
              <summary>
                <Settings2 size={14} />
                <span>Advanced settings</span>
                <small>{advancedFields.length}</small>
                <ChevronDown size={14} />
              </summary>
              <StudioSettingFields
                fields={advancedFields}
                metadata={metadata}
                model={model}
                onMetadata={editMetadata}
              />
            </details>
          )}
        </fieldset>
        {node.readOnly ? (
          <p className="studio-settings__notice">
            <LockKeyhole size={14} /> This node is read-only.
          </p>
        ) : (
          <div className="studio-settings__footer">
            {conflict || saveError || saveFeedback ? (
              <div className="studio-settings__recovery">
                <p
                  className="studio-settings__save-feedback"
                  data-state="error"
                  role="alert"
                >
                  <Info size={14} />
                  <span>
                    {conflict
                      ? "Some fields changed elsewhere. Your edits are kept."
                      : saveError || saveFeedback?.message}
                  </span>
                </p>
                <div className="studio-settings__recovery-actions">
                  {conflict ? (
                    <>
                      <button
                        type="button"
                        disabled={saving || locked}
                        onClick={() => {
                          setSaveFeedback(null);
                          onDiscard();
                        }}
                      >
                        Load latest
                      </button>
                      <button
                        type="button"
                        disabled={saving || locked || !onKeepDraft}
                        onClick={() => onKeepDraft && void persist(onKeepDraft)}
                      >
                        Keep my changes
                      </button>
                    </>
                  ) : (
                    <button type="submit" disabled={saving || locked}>
                      Retry save
                    </button>
                  )}
                </div>
              </div>
            ) : null}
            {connectionUnavailable && (
              <p className="studio-settings__notice">
                Reconnect Studio to save your edits.
              </p>
            )}
            {canGenerateStudioNode(node) ? (
              <div className="studio-settings__generate">
                <StudioGenerateButton
                  node={node}
                  pending={generationPending}
                  disabledReason={
                    connectionUnavailable
                      ? "Reconnect Studio to generate."
                      : modelSaving || restoring
                        ? "Wait for your changes to finish saving."
                        : conflict
                          ? "Resolve your changes before generating."
                          : undefined
                  }
                  onGenerate={onGenerate}
                  onError={onError}
                />
              </div>
            ) : null}
          </div>
        )}
      </form>
    </aside>
  );
}
