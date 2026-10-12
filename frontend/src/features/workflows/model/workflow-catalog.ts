import type {
  WorkflowAppState,
  WorkflowCategory,
  WorkflowConnectorType,
  WorkflowDeliverableType,
  WorkflowReadiness,
  WorkflowSummary,
} from "@/shared/api/types";
import type { IconName } from "@/shared/ui/icon";

/**
 * How the Workflow catalog's closed vocabularies are spelled for a person.
 *
 * The server owns the enums; this file owns only their labels and icons. Each
 * map is keyed by the generated enum, so a value added or removed on the
 * server fails the typecheck here instead of rendering a raw slug.
 */

type CategoryDisplay = { label: string; icon: IconName };

// Written in the enum's order, which is the page's section order; the order
// list below is read from this literal so the two cannot drift.
const workflowCategoryDisplay: Record<WorkflowCategory, CategoryDisplay> = {
  finance: { label: "Finance", icon: "dollar-circle" },
  legal: { label: "Legal", icon: "shield-tick" },
  sales: { label: "Sales", icon: "graph" },
  marketing: { label: "Marketing", icon: "volume-high" },
  research: { label: "Research", icon: "discover" },
  productivity: { label: "Productivity", icon: "task-square" },
  writing: { label: "Writing", icon: "edit-2" },
  recruiting: { label: "Recruiting", icon: "user-add" },
  media: { label: "Media", icon: "video" },
  ecommerce: { label: "E-commerce", icon: "box" },
  product: { label: "Product & engineering", icon: "code-1" },
  real_estate: { label: "Real estate", icon: "home-2" },
};

export const workflowCategoryOrder = Object.keys(
  workflowCategoryDisplay,
) as WorkflowCategory[];

// A desktop build can be older than the server it talks to, so a category the
// catalog added since reads as its humanized slug with a generic icon.
function workflowCategoryDisplayFor(
  category: WorkflowCategory,
): CategoryDisplay {
  return Object.hasOwn(workflowCategoryDisplay, category)
    ? workflowCategoryDisplay[category]
    : {
        label:
          category.charAt(0).toUpperCase() +
          category.slice(1).replace(/_/g, " "),
        icon: "category",
      };
}

export function workflowCategoryLabel(category: WorkflowCategory): string {
  return workflowCategoryDisplayFor(category).label;
}

export function workflowCategoryIcon(category: WorkflowCategory): IconName {
  return workflowCategoryDisplayFor(category).icon;
}

export function isWorkflowCategory(value: unknown): value is WorkflowCategory {
  return (
    typeof value === "string" && Object.hasOwn(workflowCategoryDisplay, value)
  );
}

/**
 * The page's section and filter order for a catalog: the known categories,
 * then any a newer server added, in the order the catalog first names them,
 * so their Workflows still get a section of their own.
 */
export function workflowCategoryOrderFor(
  workflows: readonly Pick<WorkflowSummary, "categories">[],
): WorkflowCategory[] {
  const added = new Set(
    workflows
      .flatMap((workflow) => workflow.categories)
      .filter((category) => !Object.hasOwn(workflowCategoryDisplay, category)),
  );
  return [...workflowCategoryOrder, ...added];
}

const workflowDeliverableLabels: Record<WorkflowDeliverableType, string> = {
  report: "Report",
  document: "Document",
  spreadsheet: "Spreadsheet",
  presentation: "Presentation",
  pdf: "PDF",
  csv: "CSV",
  image: "Image",
  video: "Video",
  web_app: "Web app",
  email_drafts: "Email drafts",
};

export function workflowDeliverableLabel(
  deliverable: WorkflowDeliverableType,
): string {
  return workflowDeliverableLabels[deliverable];
}

// Only apps whose genuine brand mark ships in `@/shared/ui/icon` carry one;
// the rest render as a text chip rather than a borrowed glyph.
type ConnectorDisplay = {
  label: string;
  brand?: Extract<
    IconName,
    "gmail" | "google-calendar" | "google-drive" | "slack" | "linkedIn"
  >;
};

const workflowConnectorDisplay: Record<
  WorkflowConnectorType,
  ConnectorDisplay
> = {
  gmail: { label: "Gmail", brand: "gmail" },
  google_calendar: { label: "Google Calendar", brand: "google-calendar" },
  google_drive: { label: "Google Drive", brand: "google-drive" },
  slack: { label: "Slack", brand: "slack" },
  notion: { label: "Notion" },
  linear: { label: "Linear" },
  jira: { label: "Jira" },
  asana: { label: "Asana" },
  clickup: { label: "ClickUp" },
  github: { label: "GitHub" },
  hubspot: { label: "HubSpot" },
  salesforce: { label: "Salesforce" },
  apollo: { label: "Apollo" },
  gong: { label: "Gong" },
  fireflies: { label: "Fireflies" },
  zendesk: { label: "Zendesk" },
  stripe: { label: "Stripe" },
  shopify: { label: "Shopify" },
  xero: { label: "Xero" },
  linkedin: { label: "LinkedIn", brand: "linkedIn" },
  figma: { label: "Figma" },
  firecrawl: { label: "Firecrawl" },
  ashby: { label: "Ashby" },
  typeform: { label: "Typeform" },
};

// A desktop build can be older than the server it talks to, so an app the
// catalog added since renders as its humanized slug instead of failing.
export function workflowConnectorDisplayFor(
  connector: WorkflowConnectorType,
): ConnectorDisplay {
  return (
    workflowConnectorDisplay[connector] ?? {
      label: connector
        .split("_")
        .map((word) => word.charAt(0).toUpperCase() + word.slice(1))
        .join(" "),
    }
  );
}

export function isWorkflowConnectorType(
  value: unknown,
): value is WorkflowConnectorType {
  return (
    typeof value === "string" && Object.hasOwn(workflowConnectorDisplay, value)
  );
}

/** Who and what an app's readiness is spelled against. */
export type WorkflowAppStateContext = {
  /** The Agent the Workflow would run as. */
  agentName: string;
  /** The app, spelled for a person ("Gmail"). */
  appName: string;
  /** The Agent's provider, spelled for a person ("Codex"). */
  providerName: string;
  /** The readiness discovery state, which says why an app is `unknown`. */
  discovery?: WorkflowReadiness["discovery"]["state"];
};

/**
 * How one app's readiness reads on a chip. Readiness is read off the
 * viewer's own computer, where the Workflow's Run would execute (ADR 0014).
 * `tone` groups the states for styling; `label` is the chip's short state
 * (empty for `unknown`, which renders as a plain chip) and `viewerLabel`
 * replaces it for a person who can't act on the app; `detail` is the longer
 * sentence behind it, and `memberNext` what such a person can do about it. `action` is read after
 * the chip's name when the chip opens Settings, as in "Gmail Connected —
 * manage"; it is empty when the label already names the action.
 */
type WorkflowAppStateDisplay = {
  tone: "ready" | "pending" | "attention" | "muted" | "plain";
  label: (context: WorkflowAppStateContext) => string;
  viewerLabel?: (context: WorkflowAppStateContext) => string;
  action: string;
  detail: (context: WorkflowAppStateContext) => string;
  memberNext?: (context: WorkflowAppStateContext) => string;
};

const workflowAppStateDisplay: Record<
  WorkflowAppState,
  WorkflowAppStateDisplay
> = {
  ready: {
    tone: "ready",
    label: () => "Connected",
    action: "manage",
    detail: ({ agentName, appName }) =>
      `${appName} is connected — ${agentName} can use it in this workflow.`,
  },
  pending: {
    tone: "pending",
    label: () => "Checking…",
    action: "check again",
    detail: ({ agentName, appName }) =>
      `${appName} is connected, and Zenith Team is still confirming it for ${agentName}. This finishes when your computer next reports its apps.`,
  },
  needs_connection: {
    tone: "attention",
    label: () => "Connect",
    viewerLabel: () => "Not connected",
    action: "",
    detail: ({ agentName, appName, providerName }) =>
      `${appName} isn't connected in ${providerName} on your computer yet, so ${agentName} can't use it in this workflow.`,
    memberNext: ({ appName }) =>
      `Ask an owner or admin to let you connect ${appName}.`,
  },
  review_required: {
    tone: "attention",
    label: () => "Review",
    viewerLabel: () => "Needs review",
    action: "",
    detail: ({ appName, providerName }) =>
      `${providerName} changed the tools ${appName} offers, so Runs can't use it until they're reviewed.`,
    memberNext: ({ appName }) =>
      `An owner or admin reviews ${appName}'s changed tools for the company.`,
  },
  disabled_for_agent: {
    tone: "muted",
    label: ({ agentName }) => `Off for ${agentName}`,
    action: "details",
    detail: ({ agentName, appName }) =>
      `${appName} is connected, but turned off in ${agentName}'s capabilities.`,
    memberNext: ({ agentName }) =>
      `Ask an owner or admin, or whoever created ${agentName}, to turn it on.`,
  },
  provider_managed: {
    tone: "muted",
    label: () => "Claude.ai only",
    action: "details",
    detail: ({ appName }) =>
      `${appName} is managed in Claude.ai, whose connectors Zenith Team Runs can't use.`,
    memberNext: ({ appName }) =>
      `Run this workflow with a Codex Agent to use ${appName}.`,
  },
  not_offered: {
    tone: "muted",
    label: ({ providerName }) => `Not on ${providerName}`,
    action: "",
    detail: ({ appName, providerName }) =>
      `${providerName} doesn't offer ${appName} to Zenith Team Runs. The workflow still runs without it.`,
  },
  unknown: {
    tone: "plain",
    label: () => "",
    action: "",
    detail: ({ agentName, appName, discovery }) =>
      discovery === "unbound"
        ? `${agentName} isn't set up on your computer yet, so ${appName} can't be checked for it.`
        : discovery === "missing"
          ? `The computer you set ${agentName} up on hasn't reported its apps yet.`
          : `Zenith Team can't see the apps ${agentName} would use on your computer right now.`,
  },
};

// A state a newer server added reads as `unknown`: a plain chip, no link.
export function workflowAppStateDisplayFor(
  state: WorkflowAppState,
): WorkflowAppStateDisplay {
  return workflowAppStateDisplay[state] ?? workflowAppStateDisplay.unknown;
}

/**
 * The states a viewer who can act on them follows from a chip to Settings ›
 * Plugins, for their own computer: manage a connected app, connect or
 * install one, review its changed tools, check a pending one again, or read
 * why it is managed elsewhere or off for this Agent. An app the provider
 * doesn't offer, or one nobody can see yet, has nothing to do there; its chip
 * only explains the state.
 */
export function isActionableWorkflowAppState(state: WorkflowAppState) {
  return (
    state === "ready" ||
    state === "pending" ||
    state === "needs_connection" ||
    state === "review_required" ||
    state === "provider_managed" ||
    state === "disabled_for_agent"
  );
}

/**
 * What the viewer may do about an app on their own computer: connect,
 * install or manage it (`plugins.manage`, which every role holds since the
 * 2026-09-29 owner decision), and review its changed tools, which the
 * Company accepts for everyone (`connectors.manage`).
 */
export type WorkflowAppAccess = { connect: boolean; review: boolean };

/** Whether the viewer can act on an app in this state. */
export function canActOnWorkflowApp(
  state: WorkflowAppState,
  access: WorkflowAppAccess,
) {
  return state === "review_required" ? access.review : access.connect;
}

/** The launch dialog's hand-off to Settings › Plugins for one app. */
export type WorkflowAppLink = {
  agentId: string;
  app: WorkflowConnectorType;
  runtimeId: string;
  workflowKey: string;
};

const workflowKeyPattern = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;
const uuidPattern =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function workflowAppConnectPath(link: WorkflowAppLink): string {
  const params = new URLSearchParams({
    runtimeId: link.runtimeId,
    app: link.app,
    workflow: link.workflowKey,
    agent: link.agentId,
  });
  return `/settings/plugins?${params.toString()}`;
}

/**
 * Reads {@link workflowAppConnectPath}'s parameters back. Any missing or
 * malformed value yields null, and the Plugins page then renders as usual.
 * The runtime comes from readiness, not the link, so it is not required.
 */
export function readWorkflowAppLink(
  params: URLSearchParams,
): Omit<WorkflowAppLink, "runtimeId"> | null {
  const app = params.get("app");
  const workflowKey = params.get("workflow") ?? "";
  const agentId = params.get("agent") ?? "";
  if (
    !isWorkflowConnectorType(app) ||
    workflowKey.length > 39 ||
    !workflowKeyPattern.test(workflowKey) ||
    !uuidPattern.test(agentId)
  ) {
    return null;
  }
  return { agentId, app, workflowKey };
}

/** The section a Workflow is listed under: its first category. */
export function primaryWorkflowCategory(
  workflow: Pick<WorkflowSummary, "categories">,
): WorkflowCategory | undefined {
  return workflow.categories[0];
}

/**
 * Case-insensitive search over the name, the description and every category
 * label. Each whitespace-separated term must match somewhere, so "legal
 * review" finds "Contract review" in Legal.
 */
export function workflowMatchesQuery(
  workflow: WorkflowSummary,
  query: string,
): boolean {
  const terms = query.trim().toLocaleLowerCase().split(/\s+/).filter(Boolean);
  if (!terms.length) return true;
  const haystack = [
    workflow.name,
    workflow.description,
    ...workflow.categories.map(workflowCategoryLabel),
  ]
    .join(" ")
    .toLocaleLowerCase();
  return terms.every((term) => haystack.includes(term));
}

/** "A", "A and B", "A, B and C". */
export function joinNames(names: readonly string[]): string {
  if (names.length <= 1) return names.join("");
  return `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`;
}

/** "A", "A or B", "A, B or C". */
export function joinAlternatives(names: readonly string[]): string {
  if (names.length <= 1) return names.join("");
  return `${names.slice(0, -1).join(", ")} or ${names[names.length - 1]}`;
}

/** A requirement's apps, spelled for a person ("Xero or Stripe"). */
export function workflowAppAlternatives(
  apps: readonly WorkflowConnectorType[],
): string {
  return joinAlternatives(
    apps.map((app) => workflowConnectorDisplayFor(app).label),
  );
}
