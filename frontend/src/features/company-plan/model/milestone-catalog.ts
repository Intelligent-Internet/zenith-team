import type { TaskMilestoneTypeDto } from "@/shared/api/types";

/**
 * The client's view of the milestone catalogue.
 *
 * The server owns the catalogue and its version; this file owns only how each
 * outcome is spelled for a person. It is keyed by `TaskMilestoneTypeDto`, which
 * is generated from the OpenAPI enum, so adding or removing a milestone on the
 * server fails the typecheck here instead of silently rendering a raw slug.
 *
 * `label` is what the milestone board and the Task form both show, and `prompt`
 * is how the same outcome is put to a founder at signup, where "Customer
 * support" reads better as something you might want help with.
 */
type MilestoneDisplay = {
  label: string;
  prompt: string;
  order: number;
};

const milestoneDisplay: Record<TaskMilestoneTypeDto, MilestoneDisplay> = {
  positioning: {
    label: "Positioning",
    prompt: "Work out who we serve and why they should care",
    order: 1,
  },
  brand: {
    label: "Brand kit",
    prompt: "Marketing, content, and brand",
    order: 2,
  },
  presence: {
    label: "Company presence",
    prompt: "Get the company online — website and email",
    order: 3,
  },
  finance: {
    label: "Finance and legal",
    prompt: "Incorporation, banking, and bookkeeping",
    order: 4,
  },
  product: {
    label: "Product",
    prompt: "Build a product or service",
    order: 5,
  },
  launch: {
    label: "Launch",
    prompt: "Ship a launch",
    order: 6,
  },
  growth: {
    label: "Growth",
    prompt: "Find customers and grow revenue",
    order: 7,
  },
  sales_outbound: {
    label: "Sales outbound",
    prompt: "Prospecting and outreach",
    order: 8,
  },
  operations: {
    label: "Operations",
    prompt: "Operations and automation",
    order: 9,
  },
  support: {
    label: "Customer support",
    prompt: "Customer support and retention",
    order: 10,
  },
};

export const milestoneTypes = (
  Object.keys(milestoneDisplay) as TaskMilestoneTypeDto[]
).sort(
  (left, right) => milestoneDisplay[left].order - milestoneDisplay[right].order,
);

export function milestoneLabel(type: TaskMilestoneTypeDto): string {
  return milestoneDisplay[type]?.label ?? humanizeMilestoneType(type);
}

export function milestonePrompt(type: TaskMilestoneTypeDto): string {
  return milestoneDisplay[type]?.prompt ?? humanizeMilestoneType(type);
}

export function milestoneOrder(type: TaskMilestoneTypeDto): number {
  return milestoneDisplay[type]?.order ?? Number.MAX_SAFE_INTEGER;
}

/**
 * A Task can carry a milestone this build has never heard of — an older client
 * against a newer catalogue — so every lookup falls back to something readable
 * rather than an empty heading.
 */
export function humanizeMilestoneType(value: string): string {
  return value
    .replace(/[-_]+/g, " ")
    .replace(/\b\w/g, (character) => character.toUpperCase());
}
