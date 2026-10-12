import type { components } from "@/shared/api/generated/api.generated";
import { PROJECT_PERMISSION_LABELS } from "@/shared/permissions";

type ApprovalExecution = components["schemas"]["ApprovalExecution"];
type PlanSkip = components["schemas"]["PlanSkip"];

const OPERATION_COPY: Record<PlanSkip["operation"], string> = {
  update: "Not renamed",
  archive: "Not archived",
  tasks: "Its tasks were left as they were",
};

// The Project changes a Company Plan apply left undone because the person
// applying it lacked a Project permission (ADR 0017); the rest applied.
export function PlanApplySkipped({
  execution,
}: {
  execution: ApprovalExecution | null | undefined;
}) {
  const skipped = planSkips(execution?.result);
  if (skipped.length === 0) return null;
  return (
    <section
      aria-label="Changes not applied"
      className="company-plan-approval__skipped"
    >
      <p>Some changes were not applied:</p>
      <ul>
        {skipped.map((skip) => (
          <li key={`${skip.key}:${skip.operation}`}>
            <strong>{skip.key}</strong> · {OPERATION_COPY[skip.operation]} ·
            needs “{PROJECT_PERMISSION_LABELS[skip.permission]}”
          </li>
        ))}
      </ul>
    </section>
  );
}

// `result` is the domain's own value; a Company Plan apply returns a
// PlanApplyResult whose `skipped` lists what it held back.
function planSkips(result: unknown): PlanSkip[] {
  if (!result || typeof result !== "object" || !("skipped" in result)) {
    return [];
  }
  const skipped = (result as { skipped: unknown }).skipped;
  if (!Array.isArray(skipped)) return [];
  return skipped.filter(
    (item): item is PlanSkip =>
      !!item &&
      typeof item === "object" &&
      typeof (item as PlanSkip).key === "string" &&
      (item as PlanSkip).operation in OPERATION_COPY &&
      (item as PlanSkip).permission in PROJECT_PERMISSION_LABELS,
  );
}
