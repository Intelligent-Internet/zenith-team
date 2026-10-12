import { Fragment, useState } from "react";
import { CanvasToolGuide } from "@/shared/canvas/canvas-tool-guide";
import {
  Cursor,
  Hand,
  ImageIcon,
  LayoutGrid,
  LoaderCircle,
  Plus,
  StickyNote,
  Text,
  Workflow,
} from "@/shared/ui/icons";
import "./studio-toolbar.css";

const tools = [
  {
    id: "add",
    label: "Add node",
    demoLabel: "Add a text node",
    description:
      "Choose a type, then a model. Your new node opens ready for a prompt.",
    icon: Plus,
  },
  {
    id: "select",
    label: "Select mode",
    demoLabel: "Drag a node",
    title: "Select & move",
    description:
      "Click a node to edit it. Drag to move it, or drag on empty canvas to select several nodes.",
    icon: Cursor,
  },
  {
    id: "pan",
    label: "Pan mode",
    demoLabel: "Drag the canvas",
    title: "Move around the canvas",
    description:
      "Drag the canvas to explore your workspace. Your nodes stay in place while your view moves.",
    icon: Hand,
  },
  {
    id: "note",
    label: "Add note",
    demoLabel: "Write a note",
    description:
      "Leave an idea, reminder or feedback on the canvas. Write your note in its settings panel.",
    icon: StickyNote,
  },
  {
    id: "group",
    label: "Add group",
    demoLabel: "Name your group",
    description:
      "Add a group to organize your workflow. Give it a name in its settings panel.",
    icon: Workflow,
  },
  {
    id: "arrange",
    label: "Arrange nodes",
    demoLabel: "Arrange in rows",
    description:
      "Tidy your canvas in one click. Nodes line up in evenly spaced rows, and the view fits them all.",
    icon: LayoutGrid,
  },
] as const;

type ToolId = (typeof tools)[number]["id"];

export function StudioToolbar({
  toolsOpen,
  creating,
  arranging,
  reconnectRequired,
  nodeCount,
  interactionMode,
  onTogglePicker,
  onInteractionModeChange,
  onAddNote,
  onAddGroup,
  onArrange,
}: {
  toolsOpen: boolean;
  creating: boolean;
  arranging: boolean;
  reconnectRequired: boolean;
  nodeCount: number;
  interactionMode: "select" | "pan";
  onTogglePicker: () => void;
  onInteractionModeChange: (mode: "select" | "pan") => void;
  onAddNote: () => void;
  onAddGroup: () => void;
  onArrange: () => void;
}) {
  const [activeGuide, setActiveGuide] = useState<ToolId | null>(null);
  const actions = {
    add: onTogglePicker,
    select: () => onInteractionModeChange("select"),
    pan: () => onInteractionModeChange("pan"),
    note: onAddNote,
    group: onAddGroup,
    arrange: onArrange,
  };
  const unavailableReason = reconnectRequired
    ? "Reconnect Studio to use this tool."
    : creating
      ? "Wait for the new node to finish being added."
      : undefined;

  return (
    <div className="studio-mode-panel__toolbar" aria-label="Studio tools">
      {tools.map((tool, index) => {
        const { id, icon: Glyph } = tool;
        let disabledReason =
          id === "select" || id === "pan" ? undefined : unavailableReason;
        if (id === "arrange" && !disabledReason) {
          if (arranging) disabledReason = "Your nodes are being arranged.";
          else if (nodeCount < 2)
            disabledReason = "Add at least two nodes to arrange them.";
        }
        const busy =
          (id === "add" && creating) || (id === "arrange" && arranging);
        return (
          <Fragment key={id}>
            {index === 1 || index === 3 ? (
              <div className="studio-mode-panel__toolbar-divider" />
            ) : null}
            <CanvasToolGuide
              tool={tool}
              preview={<StudioToolDemo tool={id} label={tool.demoLabel} />}
              containerSelector=".studio-mode-shell"
              side="right"
              open={activeGuide === id && !toolsOpen}
              onOpenChange={(open) =>
                setActiveGuide((current) =>
                  open && !toolsOpen ? id : current === id ? null : current,
                )
              }
              disabledReason={disabledReason}
              pressed={
                id === "select" || id === "pan"
                  ? interactionMode === id
                  : undefined
              }
              expanded={id === "add" ? toolsOpen : undefined}
              onClick={actions[id]}
            >
              {busy ? (
                <LoaderCircle
                  size={18}
                  className="studio-media__spinner"
                  aria-hidden
                />
              ) : (
                <Glyph size={id === "add" ? 21 : 18} aria-hidden />
              )}
            </CanvasToolGuide>
          </Fragment>
        );
      })}
    </div>
  );
}

function DemoNode({ image = false }: { image?: boolean }) {
  return (
    <div className="studio-tool-demo__node">
      <div className="studio-tool-demo__node-content" data-image={image}>
        {image ? (
          <ImageIcon size={23} />
        ) : (
          <>
            <i />
            <i />
            <i />
          </>
        )}
      </div>
    </div>
  );
}

function StudioToolDemo({ tool, label }: { tool: ToolId; label: string }) {
  return (
    <div className="studio-tool-demo" data-tool={tool} aria-hidden="true">
      <div className="studio-tool-demo__scene">
        {tool === "add" ? (
          <>
            <div className="studio-tool-demo__menu">
              <span>
                <Text size={13} /> Text
              </span>
              <span>
                <ImageIcon size={13} /> Image
              </span>
            </div>
            <div className="studio-tool-demo__created">
              <DemoNode />
            </div>
          </>
        ) : tool === "note" ? (
          <div className="studio-tool-demo__note">
            <span>
              <StickyNote size={14} /> Note
            </span>
            <p>Try a warmer palette.</p>
          </div>
        ) : tool === "group" ? (
          <div className="studio-tool-demo__group">
            <span>
              <Workflow size={14} /> Campaign
            </span>
            <div />
          </div>
        ) : (
          <>
            <div className="studio-tool-demo__first">
              <DemoNode />
            </div>
            <div className="studio-tool-demo__second">
              <DemoNode image />
            </div>
            {tool === "arrange" ? (
              <div className="studio-tool-demo__third">
                <DemoNode />
              </div>
            ) : null}
          </>
        )}
      </div>
      <span className="studio-tool-demo__cursor">
        {tool === "pan" ? <Hand size={20} /> : <Cursor size={20} />}
      </span>
      <span className="studio-tool-demo__caption">{label}</span>
    </div>
  );
}
