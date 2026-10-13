import { useReactFlow, type Viewport } from "@xyflow/react";
import { Maximize2, Minus, Plus } from "@/shared/ui/icons";

export type ViewCamera = { current: Viewport | undefined };

export function GraphControls() {
  const flow = useReactFlow();
  return (
    <div className="pc-viewport-tools">
      <button
        className="pc-icon-button"
        aria-label="Zoom out"
        onClick={() => void flow.zoomOut()}
      >
        <Minus size={17} />
      </button>
      <button
        className="pc-icon-button"
        aria-label="Zoom in"
        onClick={() => void flow.zoomIn()}
      >
        <Plus size={17} />
      </button>
      <button
        className="pc-icon-button"
        aria-label="Fit all nodes"
        onClick={() => void flow.fitView({ padding: 0.16, maxZoom: 1 })}
      >
        <Maximize2 size={17} />
      </button>
    </div>
  );
}
