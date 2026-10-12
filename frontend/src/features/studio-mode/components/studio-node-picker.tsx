import { useId, useState } from "react";
import type {
  StudioModel,
  StudioModelCatalog,
} from "@/shared/api/studio-model-catalog";
import type { StudioNodeKind } from "@/shared/studio-graph/model";
import {
  ChevronRight,
  ImageIcon,
  Megaphone,
  Music,
  Search,
  StickyNote,
  Text,
  Video,
  Workflow,
  X,
} from "@/shared/ui/icons";

type EditableKind = Exclude<StudioNodeKind, "unknown">;

const generationChoices = [
  {
    kind: "text",
    label: "Text",
    description: "Ideas, scripts and prompts",
    icon: Text,
  },
  {
    kind: "image",
    label: "Image",
    description: "Turn a prompt into a picture",
    icon: ImageIcon,
  },
  {
    kind: "video",
    label: "Video",
    description: "Bring your ideas into motion",
    icon: Video,
  },
  {
    kind: "voice",
    label: "Voice",
    description: "Speech with character",
    icon: Megaphone,
  },
  {
    kind: "audio",
    label: "Music",
    description: "Set the mood with sound",
    icon: Music,
  },
] as const;

export function StudioNodePicker({
  catalog,
  catalogError,
  busy,
  onCreate,
  onClose,
  welcome = false,
}: {
  catalog: StudioModelCatalog | null;
  catalogError: string | null;
  busy: boolean;
  onCreate: (kind: EditableKind, model?: StudioModel) => void;
  onClose?: () => void;
  welcome?: boolean;
}) {
  const [activeKind, setActiveKind] = useState<EditableKind | null>(
    welcome ? "image" : null,
  );
  const [search, setSearch] = useState("");
  const modelInfoId = useId();
  const active = generationChoices.find((choice) => choice.kind === activeKind);
  const models = activeKind ? (catalog?.models[activeKind] ?? []) : [];
  const filtered = models.filter((model) =>
    `${model.label} ${model.provider} ${model.description}`
      .toLowerCase()
      .includes(search.toLowerCase()),
  );
  const selectKind = (kind: EditableKind) => {
    setActiveKind(kind);
    setSearch("");
  };

  return (
    <div
      className={
        welcome
          ? "studio-mode-panel__welcome"
          : "studio-mode-panel__tool-picker"
      }
      aria-label={welcome ? "Start creating" : "Add node"}
      data-model-open={activeKind !== null}
      onMouseLeave={(event) => {
        if (
          !welcome &&
          !event.currentTarget
            .querySelector(".studio-mode-panel__model-preview")
            ?.contains(document.activeElement)
        )
          setActiveKind(null);
      }}
      onBlur={(event) => {
        if (
          !welcome &&
          !event.currentTarget.contains(event.relatedTarget) &&
          !event.currentTarget.matches(":hover")
        )
          setActiveKind(null);
      }}
    >
      {welcome ? (
        <div className="studio-mode-panel__picker-heading">
          <div>
            <span className="studio-mode-panel__eyebrow">
              {welcome ? "Your creative workspace" : "Create with Studio"}
            </span>
            <h2>{welcome ? "What will you create?" : "Add a node"}</h2>
            {welcome ? (
              <p>Start with an idea. Connect nodes to shape what comes next.</p>
            ) : null}
          </div>
          {onClose ? (
            <button
              type="button"
              aria-label="Close node picker"
              onClick={onClose}
            >
              <X size={16} aria-hidden />
            </button>
          ) : null}
        </div>
      ) : null}
      <div className="studio-mode-panel__picker-content">
        <div className="studio-mode-panel__kind-menu">
          {!welcome ? (
            <div className="studio-mode-panel__picker-heading">
              <div>
                <span className="studio-mode-panel__eyebrow">
                  {welcome ? "Your creative workspace" : "Create with Studio"}
                </span>
                <h2>{welcome ? "What will you create?" : "Add a node"}</h2>
                {welcome ? (
                  <p>
                    Start with an idea. Connect nodes to shape what comes next.
                  </p>
                ) : null}
              </div>
              {onClose ? (
                <button
                  type="button"
                  aria-label="Close node picker"
                  onClick={onClose}
                >
                  <X size={16} aria-hidden />
                </button>
              ) : null}
            </div>
          ) : null}
          <div
            className="studio-mode-panel__generation-list"
            role="tablist"
            aria-label="Generate"
            aria-orientation="vertical"
          >
            {generationChoices.map(
              ({ kind, label, description, icon: Glyph }, index) => (
                <button
                  type="button"
                  key={kind}
                  className="studio-mode-panel__generation-choice"
                  data-active={activeKind === kind}
                  data-kind={kind}
                  id={`${modelInfoId}-${kind}`}
                  role="tab"
                  aria-selected={activeKind === kind}
                  aria-controls={activeKind === kind ? modelInfoId : undefined}
                  tabIndex={
                    activeKind === kind || (activeKind === null && index === 0)
                      ? 0
                      : -1
                  }
                  aria-describedby={
                    activeKind === kind ? modelInfoId : undefined
                  }
                  disabled={busy}
                  onMouseEnter={() =>
                    !busy && activeKind !== kind && selectKind(kind)
                  }
                  onFocus={() => activeKind !== kind && selectKind(kind)}
                  onKeyDown={(event) => {
                    const direction = ["ArrowDown", "ArrowRight"].includes(
                      event.key,
                    )
                      ? 1
                      : ["ArrowUp", "ArrowLeft"].includes(event.key)
                        ? -1
                        : 0;
                    if (!direction && !["Home", "End"].includes(event.key))
                      return;
                    event.preventDefault();
                    const nextIndex =
                      event.key === "Home"
                        ? 0
                        : event.key === "End"
                          ? generationChoices.length - 1
                          : (index + direction + generationChoices.length) %
                            generationChoices.length;
                    const next = generationChoices[nextIndex];
                    selectKind(next.kind);
                    event.currentTarget.parentElement
                      ?.querySelector<HTMLButtonElement>(
                        `[data-kind="${next.kind}"]`,
                      )
                      ?.focus();
                  }}
                  onClick={() => selectKind(kind)}
                >
                  <span className="studio-mode-panel__choice-icon">
                    <Glyph size={20} aria-hidden />
                  </span>
                  <span>
                    <strong>{welcome ? label : `Generate ${label}`}</strong>
                    <small>{description}</small>
                  </span>
                  <ChevronRight size={14} aria-hidden />
                </button>
              ),
            )}
          </div>
          {!welcome ? (
            <div className="studio-mode-panel__picker-utilities">
              <button
                type="button"
                disabled={busy}
                onClick={() => onCreate("comment")}
              >
                <StickyNote size={16} aria-hidden /> Note
              </button>
              <button
                type="button"
                disabled={busy}
                onClick={() => onCreate("node_group")}
              >
                <Workflow size={16} aria-hidden /> Group
              </button>
            </div>
          ) : null}
        </div>
        {activeKind ? (
          <section
            className="studio-mode-panel__model-preview"
            id={modelInfoId}
            role="tabpanel"
            aria-labelledby={`${modelInfoId}-${activeKind}`}
            aria-label={`${active?.label ?? "Generation"} models`}
          >
            <span className="studio-mode-panel__eyebrow">
              Choose a model to add
            </span>
            <h3>
              {active?.label} <span>{models.length || ""}</span>
            </h3>
            <label className="studio-mode-panel__model-search">
              <Search size={15} aria-hidden />
              <input
                aria-label="Search models to add"
                value={search}
                placeholder="Search models…"
                onChange={(event) => setSearch(event.target.value)}
              />
            </label>
            <div className="studio-mode-panel__model-list">
              {models.length ? (
                filtered.length ? (
                  filtered.map((model) => (
                    <button
                      type="button"
                      key={model.id}
                      title={model.description || model.label}
                      disabled={busy}
                      onClick={() => onCreate(activeKind, model)}
                      aria-label={`Add ${model.label} ${active?.label?.toLowerCase()} node`}
                    >
                      <span className="studio-mode-panel__model-copy">
                        <strong>{model.label}</strong>
                        {model.pro ? <small>Pro</small> : null}
                      </span>
                      <ChevronRight size={14} aria-hidden />
                    </button>
                  ))
                ) : (
                  <p>No models match “{search}”.</p>
                )
              ) : (
                <p>
                  {catalogError
                    ? "Models could not be loaded. Close this panel and reload Studio to retry."
                    : catalog
                      ? "No models are available for this node."
                      : "Loading models…"}
                </p>
              )}
            </div>
            <small>Add a node, then write your prompt in its settings.</small>
          </section>
        ) : null}
      </div>
    </div>
  );
}
