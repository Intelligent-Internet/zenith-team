import { useSearchParams } from "react-router";
import { LayoutGrid, Layers } from "@/shared/ui/icons";
import "./results-view-switch.css";

export function ResultsViewSwitch() {
  const [params, setParams] = useSearchParams();
  const view = params.get("view") === "gallery" ? "gallery" : "canvas";
  return (
    <div className="results-view-switch" role="group" aria-label="Results view">
      {(
        [
          ["canvas", "Canvas", Layers],
          ["gallery", "Gallery", LayoutGrid],
        ] as const
      ).map(([value, label, ViewIcon]) => (
        <button
          key={value}
          type="button"
          aria-pressed={view === value}
          onClick={() => {
            const next = new URLSearchParams(params);
            if (value === "gallery") next.set("view", "gallery");
            else next.delete("view");
            setParams(next);
          }}
        >
          <ViewIcon size={16} aria-hidden="true" />
          {label}
        </button>
      ))}
    </div>
  );
}
