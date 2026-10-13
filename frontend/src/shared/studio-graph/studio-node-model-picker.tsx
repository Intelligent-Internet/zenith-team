import { useRef, useState } from "react";
import {
  findStudioModel,
  studioRecord,
  studioDisplayName,
  type StudioModel,
} from "@/shared/api/studio-model-catalog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/shared/ui/dropdown-menu";
import { Check, ChevronDown, Loader2 } from "@/shared/ui/icons";
import type { StudioGraphNode } from "./model";
import "./studio-node-model-picker.css";

export function StudioNodeModelPicker({
  node,
  models,
  onChange,
  onError,
}: {
  node: StudioGraphNode;
  models: StudioModel[];
  onChange?: (nodeId: string, model: StudioModel) => Promise<void>;
  onError?: (error: unknown) => void;
}) {
  const trigger = useRef<HTMLButtonElement>(null);
  const [container, setContainer] = useState<HTMLElement | undefined>();
  const [open, setOpen] = useState(false);
  const [pending, setPending] = useState<string | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  let metadata: Record<string, unknown> = {};
  try {
    metadata = studioRecord(JSON.parse(node.raw.metadataJson));
  } catch {
    // A node without valid metadata can still show the available models.
  }
  const current = findStudioModel(
    models,
    metadata.model ?? metadata.model_name,
  );
  const locked =
    node.readOnly ||
    !onChange ||
    ["executing", "submitted", "cancelling"].includes(node.raw.status);
  const select = async (model: StudioModel) => {
    if (pending || locked || !onChange) return;
    if (current?.id === model.id) {
      setOpen(false);
      return;
    }
    setPending(model.id);
    setFailure(null);
    try {
      await onChange(node.id, model);
      setOpen(false);
    } catch (error) {
      setFailure(
        error instanceof Error ? error.message : "Could not change model.",
      );
      onError?.(error);
    } finally {
      setPending(null);
    }
  };
  return (
    <DropdownMenu
      modal={false}
      open={open}
      onOpenChange={(next) => {
        if (next) {
          setContainer(
            trigger.current?.closest<HTMLElement>(".studio-mode-shell") ??
              undefined,
          );
          setFailure(null);
        }
        setOpen(next);
      }}
    >
      <DropdownMenuTrigger asChild>
        <button
          ref={trigger}
          type="button"
          className="studio-graph-node__model nodrag nopan"
          aria-label={`Choose model for ${node.label}`}
          title={`Model: ${node.model}`}
          onClick={(event) => event.stopPropagation()}
        >
          <span>{node.model}</span>
          <ChevronDown size={12} aria-hidden />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent
        container={container}
        className="studio-node-model-menu w-[280px] max-w-[calc(100vw_-_32px)] max-h-[min(420px,_var(--radix-dropdown-menu-content-available-height))] [scrollbar-width:thin] nodrag nopan !rounded-2xl !p-1.5 !shadow-[0_12px_32px_rgba(24,32,40,0.12),0_2px_6px_rgba(24,32,40,0.04)]"
        aria-label={`Models for ${node.label}`}
        align="end"
        sideOffset={8}
        collisionPadding={16}
        onClick={(event) => event.stopPropagation()}
        onPointerDown={(event) => event.stopPropagation()}
        onKeyDown={(event) => event.stopPropagation()}
      >
        {models.map((model) => (
          <DropdownMenuItem
            key={model.id}
            className="studio-node-model-menu__item"
            textValue={model.label}
            disabled={locked || pending !== null}
            onSelect={(event) => {
              event.preventDefault();
              void select(model);
            }}
            data-selected={current?.id === model.id}
          >
            <span className="studio-node-model-menu__identity">
              <strong>{model.label}</strong>
              <small>{studioDisplayName(model.provider)}</small>
            </span>
            {model.pro ? (
              <span className="[border:1px_solid_var(--workspace-divider)] rounded-[4px] p-[1px_4px] text-workspace-muted text-[length:9px]">
                Pro
              </span>
            ) : null}
            {pending === model.id ? (
              <Loader2
                className="studio-node-model-menu__loading"
                size={16}
                aria-label="Saving model"
              />
            ) : current?.id === model.id ? (
              <Check size={16} aria-label="Current model" />
            ) : null}
          </DropdownMenuItem>
        ))}
        {!models.length ? (
          <p className="p-[10px] text-workspace-muted text-[length:12px]">
            Models are unavailable.
          </p>
        ) : null}
        {failure ? (
          <p
            className="p-[10px] text-workspace-muted text-[length:12px]"
            role="alert"
          >
            {failure}
          </p>
        ) : null}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
