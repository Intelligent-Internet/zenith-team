import {
  Fragment,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import { useReactFlow, useStore } from "@xyflow/react";
import { CanvasToolGuide } from "@/shared/canvas/canvas-tool-guide";
import {
  SelectionPointer,
  StickyNote,
  Text,
  Square,
  Layers,
  Pencil,
  Undo2,
  Minus,
  Maximize2,
  Plus,
  ChevronDown,
  Check,
  type IconComponent,
} from "@/shared/ui/icons";

const tools = [
  {
    id: "select",
    label: "Select (S)",
    title: "Select",
    description:
      "Drag a box to select objects and ink. Cmd/Ctrl+A selects all; Cmd/Ctrl+C/V copies and pastes. Esc returns to moving.",
    caption: "Select and move together",
    Icon: SelectionPointer,
  },
  {
    id: "pen",
    label: "Pen (P)",
    title: "Pen",
    description:
      "Open the drawing tray to choose Pen or Highlighter, a color and stroke width. Drag on the canvas to draw.",
    caption: "Sketch an idea",
    Icon: Pencil,
  },
  {
    id: "text",
    label: "Text (T)",
    title: "Text",
    description:
      "Click the canvas and type. Choose a background to make a note. Drag the top edge to move it.",
    caption: "Write a thought",
    Icon: Text,
  },
  {
    id: "shape",
    label: "Shape",
    title: "Shape",
    description:
      "Choose a shape, then drag to size it. Hold Shift for equal sides. Double-click a shape to add text.",
    caption: "Drag to place a shape",
    Icon: Square,
  },
  {
    id: "frame",
    label: "Frame (F)",
    title: "Frame",
    description:
      "Drag from corner to corner to frame objects. Move the frame to carry them together. Click its name to rename it.",
    caption: "Frame a group of ideas",
    Icon: Layers,
  },
] as const;

export type BoardTool = "move" | "highlighter" | (typeof tools)[number]["id"];
type Guide = {
  id: string;
  label: string;
  title?: string;
  description: string;
  caption: string;
  Icon: IconComponent;
};

const undoGuide: Guide = {
  id: "undo",
  label: "Undo",
  description:
    "Undo your last change to the canvas. Other people’s edits stay in place.",
  caption: "Go back one change",
  Icon: Undo2,
};
const redoGuide: Guide = {
  id: "redo",
  label: "Redo",
  description: "Restore the last change you undid.",
  caption: "Bring a change back",
  Icon: Undo2,
};
const zoomGuides: Guide[] = [
  {
    id: "zoom-out",
    label: "Zoom out",
    description: "Zoom out to see more of the board.",
    caption: "See the bigger picture",
    Icon: Minus,
  },
  {
    id: "fit",
    label: "Fit board",
    description: "Center all objects and fit them on screen.",
    caption: "See the whole board",
    Icon: Maximize2,
  },
  {
    id: "zoom-in",
    label: "Zoom in",
    description: "Zoom in for a closer look.",
    caption: "Explore the details",
    Icon: Plus,
  },
];

export function BoardToolbar({
  tool,
  onToolChange,
  trayOpen,
  onTrayOpenChange,
  readOnlyReason,
  canUndo,
  canRedo,
  onUndo,
  onRedo,
  children,
}: {
  tool: BoardTool;
  onToolChange: (tool: BoardTool) => void;
  /** Whether the active drawing or placement tool's options tray is open. */
  trayOpen: boolean;
  onTrayOpenChange: (open: boolean) => void;
  readOnlyReason?: string;
  canUndo: boolean;
  canRedo: boolean;
  onUndo: () => void;
  onRedo: () => void;
  children?: ReactNode;
}) {
  const [activeGuide, setActiveGuide] = useState<string | null>(null);
  const dock = useRef<HTMLDivElement>(null);
  const toolbar = useRef<HTMLDivElement>(null);
  const [optionsOffset, setOptionsOffset] = useState(0);
  const drawing = tool === "pen" || tool === "highlighter";
  const active = (id: BoardTool) => tool === id || (id === "pen" && drawing);

  useEffect(() => {
    if (!trayOpen) return;
    const dismiss = (event: PointerEvent) => {
      if (!(event.target instanceof Element)) return;
      if (dock.current?.contains(event.target)) return;
      const colorPicker = event.target.closest(".wb-color-picker");
      if (
        colorPicker &&
        dock.current?.closest(".wb-editor")?.contains(colorPicker)
      )
        return;
      onTrayOpenChange(false);
    };
    // Capture before canvas drawing or placement gestures stop pointer
    // propagation; the gesture itself still runs with the tool kept active.
    document.addEventListener("pointerdown", dismiss, true);
    return () => document.removeEventListener("pointerdown", dismiss, true);
  }, [trayOpen, onTrayOpenChange]);

  useLayoutEffect(() => {
    const dockElement = dock.current;
    const toolbarElement = toolbar.current;
    if (!dockElement || !toolbarElement) return;
    const measure = () =>
      setOptionsOffset(
        toolbarElement.getBoundingClientRect().top -
          dockElement.getBoundingClientRect().top,
      );
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(dockElement);
    observer.observe(toolbarElement);
    return () => observer.disconnect();
  }, []);

  const guideButton = (
    guide: Guide,
    onClick: () => void,
    disabledReason?: string,
    pressed?: boolean,
    expanded?: boolean,
  ) => (
    <CanvasToolGuide
      key={guide.id}
      tool={guide}
      preview={<BoardToolPreview guide={guide} />}
      containerSelector=".wb-editor"
      sideOffset={18 + optionsOffset}
      open={activeGuide === guide.id}
      onOpenChange={(open) =>
        setActiveGuide((current) =>
          open ? guide.id : current === guide.id ? null : current,
        )
      }
      disabledReason={disabledReason}
      pressed={pressed}
      expanded={expanded}
      onClick={onClick}
    >
      <guide.Icon
        size={20}
        className={guide.id === "redo" ? "wb-redo-icon" : undefined}
        aria-hidden
      />
    </CanvasToolGuide>
  );

  return (
    <>
      <div ref={dock} className="wb-tool-dock nodrag nopan nowheel">
        {children}
        <div
          ref={toolbar}
          className="wb-toolbar"
          role="toolbar"
          aria-label="Whiteboard tools"
        >
          {tools.map((guide) => (
            <Fragment key={guide.id}>
              {guideButton(
                guide,
                () => {
                  if (guide.id === "select") {
                    onTrayOpenChange(false);
                    onToolChange(tool === "select" ? "move" : "select");
                    return;
                  }
                  // Clicking the active tool toggles its tray; the tool
                  // stays active either way.
                  if (!active(guide.id)) onToolChange(guide.id);
                  onTrayOpenChange(!active(guide.id) || !trayOpen);
                },
                guide.id === "select" ? undefined : readOnlyReason,
                active(guide.id),
                guide.id === "select"
                  ? undefined
                  : active(guide.id) && trayOpen,
              )}
              {guide.id === "select" && (
                <span className="wb-tool-divider" aria-hidden="true" />
              )}
            </Fragment>
          ))}
          <span className="wb-tool-divider" />
          {guideButton(
            undoGuide,
            onUndo,
            readOnlyReason || (!canUndo ? "No changes to undo." : undefined),
          )}
          {guideButton(
            redoGuide,
            onRedo,
            readOnlyReason || (!canRedo ? "No changes to redo." : undefined),
          )}
        </div>
      </div>
      <BoardZoomControls />
    </>
  );
}

export function BoardZoomControls() {
  const flow = useReactFlow();
  const zoom = useStore((state) => state.transform[2]);
  const minZoom = useStore((state) => state.minZoom);
  const maxZoom = useStore((state) => state.maxZoom);
  const percentage = Math.round(zoom * 100);
  const trigger = useRef<HTMLButtonElement>(null);
  const [container, setContainer] = useState<HTMLElement>();
  const [activeGuide, setActiveGuide] = useState<string | null>(null);
  const onFit = () => void flow.fitView({ padding: 0.2, maxZoom: 1 });
  const guideButton = (
    guide: Guide,
    onClick: () => void,
    disabledReason?: string,
  ) => (
    <CanvasToolGuide
      tool={guide}
      preview={<BoardToolPreview guide={guide} />}
      containerSelector=".wb-editor"
      open={activeGuide === guide.id}
      onOpenChange={(open) =>
        setActiveGuide((current) =>
          open ? guide.id : current === guide.id ? null : current,
        )
      }
      disabledReason={disabledReason}
      onClick={onClick}
    >
      <guide.Icon size={18} aria-hidden />
    </CanvasToolGuide>
  );

  return (
    <div
      className="wb-zoom-controls nodrag nopan nowheel"
      role="toolbar"
      aria-label="Board view"
    >
      {guideButton(
        zoomGuides[0],
        () => void flow.zoomOut(),
        zoom <= minZoom ? "Minimum zoom reached." : undefined,
      )}
      <DropdownMenu.Root
        onOpenChange={(open) => {
          setActiveGuide(null);
          if (open)
            setContainer(
              trigger.current?.closest<HTMLElement>(".wb-editor") ?? undefined,
            );
        }}
      >
        <DropdownMenu.Trigger asChild>
          <button
            ref={trigger}
            type="button"
            className="wb-zoom-percentage"
            aria-label={`Zoom level: ${percentage}%`}
            title="Choose zoom level"
          >
            <span>{percentage}%</span>
            <ChevronDown size={12} aria-hidden />
          </button>
        </DropdownMenu.Trigger>
        <DropdownMenu.Portal container={container}>
          <DropdownMenu.Content
            className="wb-zoom-menu"
            side="top"
            align="end"
            sideOffset={12}
            collisionPadding={12}
          >
            <DropdownMenu.Label className="wb-zoom-menu__label">
              Zoom
            </DropdownMenu.Label>
            <DropdownMenu.RadioGroup
              value={String(percentage)}
              onValueChange={(value) => void flow.zoomTo(Number(value) / 100)}
            >
              {[1, 5, 10, 25, 50, 75, 100, 150, 200, 300, 400, 800]
                .filter(
                  (value) => value >= minZoom * 100 && value <= maxZoom * 100,
                )
                .map((value) => (
                  <DropdownMenu.RadioItem
                    key={value}
                    value={String(value)}
                    className="wb-zoom-menu__item"
                  >
                    <span>{value}%</span>
                    {value === 100 && (
                      <span className="wb-zoom-menu__hint">Actual size</span>
                    )}
                    <DropdownMenu.ItemIndicator className="wb-zoom-menu__check">
                      <Check size={14} />
                    </DropdownMenu.ItemIndicator>
                  </DropdownMenu.RadioItem>
                ))}
            </DropdownMenu.RadioGroup>
            <DropdownMenu.Separator className="wb-zoom-menu__separator" />
            <DropdownMenu.Item className="wb-zoom-menu__item" onSelect={onFit}>
              <Maximize2 size={16} aria-hidden /> Fit board
            </DropdownMenu.Item>
          </DropdownMenu.Content>
        </DropdownMenu.Portal>
      </DropdownMenu.Root>
      {guideButton(
        zoomGuides[2],
        () => void flow.zoomIn(),
        zoom >= maxZoom ? "Maximum zoom reached." : undefined,
      )}
      <span className="wb-zoom-divider" aria-hidden />
      {guideButton(zoomGuides[1], onFit)}
    </div>
  );
}

function PreviewNote({ secondary = false }: { secondary?: boolean }) {
  return (
    <div className="wb-tool-preview__note" data-secondary={secondary}>
      <StickyNote size={14} aria-hidden />
      <strong>{secondary ? "Next step" : "New idea"}</strong>
      <span>{secondary ? "Try it together." : "Let’s explore this."}</span>
    </div>
  );
}

function BoardToolPreview({ guide }: { guide: Guide }) {
  const { id } = guide;
  let illustration;
  if (id === "pen") {
    illustration = (
      <svg className="wb-tool-preview__ink" viewBox="0 0 200 80" fill="none">
        <path
          d="M12 54 C32 8 54 8 70 44 S108 74 124 34 S156 10 188 44"
          pathLength="1"
        />
      </svg>
    );
  } else if (id === "text" || id === "highlighter") {
    illustration = (
      <div className="wb-tool-preview__text">
        <span>Sketch an idea</span>
      </div>
    );
  } else if (id === "shape") {
    illustration = (
      <div className="wb-tool-preview__shapes">
        <div />
        <div />
      </div>
    );
  } else {
    illustration = (
      <div className="wb-tool-preview__ideas">
        <PreviewNote />
        <PreviewNote secondary />
      </div>
    );
  }
  return (
    <div className="wb-tool-preview" data-tool={id} aria-hidden="true">
      <div className="wb-tool-preview__scene">{illustration}</div>
      <span className="wb-tool-preview__caption">{guide.caption}</span>
    </div>
  );
}
