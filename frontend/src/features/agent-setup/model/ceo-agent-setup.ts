import type {
  AgentCreateRequest,
  AgentDto,
  AgentPage,
  AgentProvider,
  AgentRuntimeUpdateRequest,
  RuntimeDto,
  RuntimeModel,
} from "@/shared/api/types";
import { ceoAccentColor } from "@/shared/ui/agent-avatar/agent-appearance";

const CEO_AVATAR_KEY = "fold-tangerine";

export const CEO_PROVIDER_OPTIONS: ReadonlyArray<{
  label: string;
  value: AgentProvider;
}> = [
  { label: "Codex", value: "codex" },
  { label: "Claude", value: "claude" },
];

// The draft behind the CEO Agent form: which provider the Agent is written
// for, which of the founder's own computers it is set up on, and the
// overrides. runtimeId is the binding, not a property of the Agent
// (ADR 0014): an empty one creates the template without setting it up
// anywhere.
export type AgentRuntimeDraft = {
  model: string;
  provider: AgentProvider;
  runtimeId: string;
  thinkingLevel: string;
};

const FOUNDING_CEO_INSTRUCTIONS = `You are Andrew, the founder's company coordinator. Build and maintain assets/general/business_context.md as the shared Company context. Never invent an ICP, sales channel, fulfillment model, mission, or values; keep unknowns explicitly TBD.

After saving the context file, show it to the founder and use ask_human to wait for confirmation or corrections. Only after the founder confirms it, propose focused Tasks from the milestone catalog through the Company Plan flow. The founder reviews that plan before the other Agents start their work. Do not do the other Agents' work yourself. Ask for service connections only when the planned work needs them.

Use the available Zenith Team tools for product operations and treat successful tool results as the only proof that Zenith Team state changed.`;

export function createCeoAgentRequest(
  draft: AgentRuntimeDraft,
): AgentCreateRequest {
  return {
    // The CEO Agent's look is the Tangerine Fold palette everywhere the
    // shell draws it; storing the key keeps the API and the CLI in step.
    accentColor: ceoAccentColor,
    avatarKey: CEO_AVATAR_KEY,
    instructions: FOUNDING_CEO_INSTRUCTIONS,
    model: optionalOverride(draft.model),
    name: "Andrew",
    provider: draft.provider,
    ...(draft.runtimeId ? { runtimeId: draft.runtimeId } : {}),
    thinkingLevel: optionalOverride(draft.thinkingLevel),
    title: "Chief Executive Agent",
  };
}

// An update carries the template's own settings. Where the Agent runs is the
// member's binding, changed through its own route (ADR 0014).
export function createAgentRuntimeUpdate(
  agent: Pick<AgentDto, "revision">,
  draft: Pick<AgentRuntimeDraft, "model" | "thinkingLevel">,
): AgentRuntimeUpdateRequest {
  return {
    expectedRevision: agent.revision,
    model: optionalOverride(draft.model),
    thinkingLevel: optionalOverride(draft.thinkingLevel),
  };
}

export function findCeoAgent(agents: AgentDto[]): AgentDto | null {
  return agents.find((agent) => agent.reportsTo === null) ?? null;
}

export function findCeoAgentInPage(
  page: AgentPage | undefined,
): AgentDto | null {
  return findCeoAgent(page?.items ?? []);
}

export function findRuntime(
  runtimes: RuntimeDto[],
  runtimeId: string,
): RuntimeDto | null {
  return runtimes.find((runtime) => runtime.id === runtimeId) ?? null;
}

export function providerLabel(provider: AgentProvider): string {
  return (
    CEO_PROVIDER_OPTIONS.find((option) => option.value === provider)?.label ??
    provider
  );
}

type ThinkingLevelChoice = NonNullable<
  RuntimeModel["reasoning"]
>["levels"][number];

// The thinking levels a provider documents, offered when none of the
// viewer's computers has listed its models. Claude Code takes exactly these;
// for Codex the exact set belongs to each model, and the daemon checks it.
const PROVIDER_THINKING_LEVELS: Record<
  AgentProvider,
  readonly ThinkingLevelChoice[]
> = {
  claude: [
    { label: "Low", value: "low" },
    { label: "Medium", value: "medium" },
    { label: "High", value: "high" },
    { label: "Extra high", value: "xhigh" },
    { label: "Max", value: "max" },
  ],
  codex: [
    { label: "None", value: "none" },
    { label: "Minimal", value: "minimal" },
    { label: "Low", value: "low" },
    { label: "Medium", value: "medium" },
    { label: "High", value: "high" },
    { label: "Extra high", value: "xhigh" },
  ],
};

export function providerThinkingLevels(
  provider: AgentProvider,
): readonly ThinkingLevelChoice[] {
  return PROVIDER_THINKING_LEVELS[provider] ?? [];
}

// Every runtime on this list is the viewer's own (ADR 0014), so the label
// says what it can do rather than whose it is.
export function runtimeOptionLabel(runtime: RuntimeDto): string {
  const status = runtime.status === "online" ? "Online" : "Offline";
  const signIn = runtimeSignInLabel(runtime);
  const signInLabel = signIn ? ` · ${signIn}` : "";
  return `${runtimeDisplayName(runtime)} · ${providerLabel(runtime.provider)} · ${status}${signInLabel} · ${runtimeIdentity(runtime)}`;
}

// Signed-in is the quiet default in compact pickers. A missing observation
// is unknown, not evidence that the provider is authenticated.
export function runtimeSignInLabel(
  runtime: Pick<RuntimeDto, "signIn">,
): "Not signed in" | "Sign-in unknown" | null {
  switch (runtime.signIn?.state) {
    case "signed_out":
      return "Not signed in";
    case "signed_in":
      return null;
    default:
      return "Sign-in unknown";
  }
}

// A runtime a picker should prefer: online, and not known to be signed out.
export function runtimeReadyToRun(
  runtime: Pick<RuntimeDto, "status" | "signIn">,
): boolean {
  return runtime.status === "online" && runtime.signIn?.state !== "signed_out";
}

// The one place a runtime is named for a person: the name they gave the
// machine when there is one, else the daemon's own name. Takes the roster
// row and the agent's embedded `runtime` ref alike, so every picker, card and
// notice renders the rename without a runtime read.
export function runtimeDisplayName(
  runtime: Pick<RuntimeDto, "name"> & { customName?: string | null },
): string {
  const customName = runtime.customName?.trim();
  return customName ? customName : runtime.name;
}

// The computer alone: a registered runtime's name already carries its
// provider ("Codex (MacBook Pro)"), so a label that also names the provider
// uses this instead.
export function runtimeMachineName(
  runtime: Pick<RuntimeDto, "name"> & {
    customName?: string | null;
    deviceInfo?: string | null;
  },
): string {
  return (
    runtime.customName?.trim() ||
    runtime.deviceInfo?.split(" · ")[0]?.trim() ||
    /^(?:Codex|Claude(?: Code)?) \((.+)\)$/.exec(runtime.name)?.[1] ||
    runtimeDisplayName(runtime)
  );
}

// An unusable runtime is offline because its provider CLI cannot run on that
// machine; it must not look merely offline.
export function runtimeStateLabel(
  runtime: Pick<RuntimeDto, "status" | "unusableReason">,
): "Online" | "Offline" | "Unusable" {
  if (runtime.status === "online") return "Online";
  return runtime.unusableReason ? "Unusable" : "Offline";
}

export function runtimeIdentity(runtime: RuntimeDto): string {
  return `Runtime ${runtime.id.slice(0, 8)}`;
}

function optionalOverride(value: string): string | null {
  const trimmed = value.trim();
  return trimmed.length > 0 ? trimmed : null;
}
