import { RotateCw, Spinner } from "@/shared/ui/icons";
import { Tooltip } from "@/shared/ui/tooltip";

import { ACCESS_TOOLTIP_CLASS } from "./project-access-permission-icons";

/** A spinner while something loads; its label is for screen readers. */
export function AccessLoading({ label = "Loading" }: { label?: string }) {
  return (
    <p aria-label={label} className="project-access-notice" role="status">
      <Spinner
        aria-hidden="true"
        className="motion-safe:animate-spin"
        size={16}
      />
    </p>
  );
}

/** A load that failed: a few words and a Try again icon button. */
export function LoadFailed({
  message,
  onRetry,
}: {
  message: string;
  onRetry: () => void;
}) {
  return (
    <p className="project-access-notice" data-tone="error" role="alert">
      {message}
      <Tooltip contentClassName={ACCESS_TOOLTIP_CLASS} label="Try again">
        <button
          aria-label="Try again"
          className="project-access-retry"
          type="button"
          onClick={onRetry}
        >
          <RotateCw aria-hidden="true" size={14} />
        </button>
      </Tooltip>
    </p>
  );
}
