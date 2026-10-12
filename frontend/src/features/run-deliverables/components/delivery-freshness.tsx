import { Link } from "react-router";
import type { RunOutput } from "../model/run-deliverable";

export function DeliveryFreshness({ output }: { output: RunOutput }) {
  if (output.freshness !== "stale") return null;
  return (
    <div
      role="status"
      className="my-2 flex flex-wrap items-center gap-2 text-sm"
    >
      <strong>Stale</strong>
      <span>
        The shared content or suggestions have changed since this delivery.
      </span>
      {output.liveUrl && (
        <Link className="underline" to={output.liveUrl}>
          Open latest
        </Link>
      )}
    </div>
  );
}
