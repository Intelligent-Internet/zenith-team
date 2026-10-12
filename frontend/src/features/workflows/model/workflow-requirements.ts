import { workflowAppAlternatives } from "@/features/workflows/model/workflow-catalog";
import type {
  WorkflowAnswers,
  WorkflowFiles,
} from "@/features/workflows/model/workflow-template";
import type {
  Workflow,
  WorkflowAppState,
  WorkflowConnectorType,
  WorkflowReadiness,
  WorkflowRequirement,
} from "@/shared/api/types";

/**
 * States in which an app will not reach the chosen Agent's Run. `pending`
 * confirms itself when the computer next reports (the daemon refreshes its
 * apps before it takes a Run), and `unknown` cannot be judged, so neither
 * holds a launch; the Task footer still tells the Run to stop when a required
 * app is missing.
 */
const unavailableAppStates: ReadonlySet<WorkflowAppState> = new Set([
  "needs_connection",
  "review_required",
  "disabled_for_agent",
  "provider_managed",
  "not_offered",
]);

/** A requirement the chosen Agent cannot meet yet, with what would meet it. */
export type WorkflowRequirementBlocker = {
  requirement: WorkflowRequirement;
  /** The requirement's apps, spelled for a person ("Xero or Stripe"). */
  appNames: string;
  /** The field that stands in for the apps, when there is one. */
  orField?: { key: string; label: string; pageIndex: number };
};

function answered(answer: WorkflowAnswers[string] | undefined): boolean {
  if (Array.isArray(answer)) return answer.length > 0;
  return typeof answer === "string" && answer.trim() !== "";
}

/** Whether an app in this state will not reach the chosen Agent's Run. */
export function isUnavailableWorkflowAppState(state: WorkflowAppState) {
  return unavailableAppStates.has(state);
}

function orFieldOf(workflow: Workflow, key: string | undefined) {
  if (!key) return undefined;
  for (const [pageIndex, page] of workflow.pages.entries()) {
    const field = page.fields.find((candidate) => candidate.key === key);
    if (field) return { key, label: field.label, pageIndex };
  }
  return undefined;
}

/**
 * The requirements readiness shows the chosen Agent cannot meet. Without
 * readiness for that Agent (still loading, failed, or the viewer has not set
 * it up, so no computer of theirs can be read) nothing is held: the page
 * cannot know. The launch sets the Agent up first and checks again; the
 * Run's own footer rule stops it otherwise. A stand-in field is answered by
 * its text or, when it takes files, by a file attached to it.
 */
export function workflowRequirementBlockers(
  workflow: Workflow,
  readiness: WorkflowReadiness | undefined,
  answers: WorkflowAnswers,
  files: WorkflowFiles = {},
): WorkflowRequirementBlocker[] {
  if (!readiness || readiness.discovery.state === "unbound") return [];
  const states = new Map(
    readiness.apps.map((entry) => [entry.app, entry.state]),
  );
  const blockers: WorkflowRequirementBlocker[] = [];
  for (const requirement of workflow.requirements) {
    const orField = orFieldOf(workflow, requirement.orField);
    if (
      orField &&
      (answered(answers[orField.key]) || (files[orField.key]?.length ?? 0) > 0)
    )
      continue;
    const unavailable = requirement.anyOf.every((app) => {
      const state = states.get(app);
      return state !== undefined && unavailableAppStates.has(state);
    });
    if (!unavailable) continue;
    blockers.push({
      requirement,
      appNames: workflowAppAlternatives(requirement.anyOf),
      ...(orField ? { orField } : {}),
    });
  }
  return blockers;
}

/** A requirement "Schedule it" cannot meet. */
export type WorkflowScheduleBlocker = Omit<
  WorkflowRequirementBlocker,
  "orField"
> & {
  /**
   * No app of the requirement shows as missing either: readiness is loading
   * or failed, or the apps are `unknown`, so none can be confirmed.
   */
  unconfirmed: boolean;
  /**
   * The viewer has not set the chosen Agent up on their computer, so none of
   * its apps can be read yet; the setup comes before any app.
   */
  unbound: boolean;
};

/**
 * The requirements "Schedule it" cannot meet. A Schedule Run reads only the
 * apps it was given, so a requirement is met only when readiness for the
 * chosen Agent shows one of its apps `ready` or `pending`; a stand-in field
 * never counts. Unlike the launch, missing readiness holds it: without it (or
 * with every app `unknown`, as for an Agent the viewer has not set up) no
 * requirement can be confirmed.
 */
export function workflowScheduleBlockers(
  workflow: Workflow,
  readiness: WorkflowReadiness | undefined,
): WorkflowScheduleBlocker[] {
  const unbound = readiness?.discovery.state === "unbound";
  const states = new Map<WorkflowConnectorType, WorkflowAppState>(
    readiness && !unbound
      ? readiness.apps.map((entry) => [entry.app, entry.state])
      : [],
  );
  return workflow.requirements.flatMap((requirement) => {
    const reached = requirement.anyOf.some((app) => {
      const state = states.get(app);
      return state === "ready" || state === "pending";
    });
    if (reached) return [];
    const unavailable = requirement.anyOf.some((app) => {
      const state = states.get(app);
      return state !== undefined && unavailableAppStates.has(state);
    });
    return [
      {
        requirement,
        appNames: workflowAppAlternatives(requirement.anyOf),
        unconfirmed: !unavailable,
        unbound,
      },
    ];
  });
}

/** One sentence saying what a blocker needs, for the page. */
export function workflowBlockerSentence(
  blocker: WorkflowRequirementBlocker,
  agentName: string,
): string {
  return blocker.orField
    ? `Needs ${blocker.appNames} connected for ${agentName}, or an answer to “${blocker.orField.label}”.`
    : `Needs ${blocker.appNames} connected for ${agentName}.`;
}
