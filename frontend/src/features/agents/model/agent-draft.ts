import type {
  AgentCreateRequest,
  AgentDto,
  AgentProvider,
  RuntimeDto,
} from "@/shared/api/types";
import { runtimeReadyToRun } from "@/features/agent-setup/model/ceo-agent-setup";
import {
  foldAvatarVariants,
  foldVariantForKey,
} from "@/shared/ui/agent-avatar/fold-variants";

export const AGENT_NAME_MAX_LENGTH = 120;
export const AGENT_TITLE_MAX_LENGTH = 120;
export const AGENT_INSTRUCTIONS_MAX_LENGTH = 32_768;
export const AGENT_CAP_MIN = 1;
export const AGENT_CAP_MAX = 10;
export const AGENT_CAP_DEFAULT = 3;

/** The looks the dialog offers: the eight Fold palettes, by `avatarKey`. */
export const agentLookChoices = foldAvatarVariants;

/** The whole form: strings for inputs, empty means "not set". */
export type AgentDraft = {
  accentColor: string;
  avatarKey: string;
  instructions: string;
  maxConcurrentRuns: number;
  model: string;
  name: string;
  // The provider the Agent is written for. It cannot change later, so the
  // dialog asks for it up front (ADR 0014).
  provider: AgentProvider;
  reportsToAgentId: string;
  // Optionally, one of the caller's own computers to set the new Agent up on
  // right away. Empty creates the template and leaves the setup for later.
  runtimeId: string;
  thinkingLevel: string;
  title: string;
};

// The caller's own computer to propose for a provider: one that can run it
// now (online and not signed out), the preferred id first. Without one the
// draft proposes no setup. Every runtime on the roster is theirs (ADR 0014).
function firstReadyRuntime(
  runtimes: readonly RuntimeDto[],
  provider: AgentProvider,
  preferredId?: string,
): string {
  const ready = runtimes.filter(
    (runtime) => runtime.provider === provider && runtimeReadyToRun(runtime),
  );
  return (
    (ready.find((runtime) => runtime.id === preferredId) ?? ready[0])?.id ?? ""
  );
}

/**
 * A blank draft is written for the root's provider, reports to the root and
 * proposes the caller's ready computer for that provider. Duplicating
 * copies every portable field of the template, its provider, model and
 * thinking level included; the copy reports to the template's manager (to
 * the template itself when that is the root). Either may switch provider
 * before it is created (applyDraftProviderChange).
 */
export function createAgentDraft({
  root,
  runtimes,
  template,
}: {
  root: AgentDto | null;
  runtimes: readonly RuntimeDto[];
  template?: AgentDto | null;
}): AgentDraft {
  if (template) {
    // A copy starts on the template's provider, so the model and thinking
    // level travel with it until the provider changes.
    return {
      accentColor: template.accentColor ?? "",
      avatarKey: foldVariantForKey(template.avatarKey)?.key ?? "",
      instructions: template.instructions,
      maxConcurrentRuns: template.maxConcurrentRuns,
      model: template.model ?? "",
      name: `${template.name} (copy)`,
      provider: template.provider,
      reportsToAgentId: template.reportsTo?.id ?? template.id,
      runtimeId: firstReadyRuntime(
        runtimes,
        template.provider,
        template.binding?.runtime.id,
      ),
      thinkingLevel: template.thinkingLevel ?? "",
      title: template.title,
    };
  }
  const provider = root?.provider ?? "codex";
  return {
    accentColor: "",
    avatarKey: "",
    instructions: "",
    maxConcurrentRuns: AGENT_CAP_DEFAULT,
    model: "",
    name: "",
    provider,
    reportsToAgentId: root?.id ?? "",
    runtimeId: firstReadyRuntime(runtimes, provider, root?.binding?.runtime.id),
    thinkingLevel: "",
    title: "",
  };
}

/**
 * A look is a Fold palette: choosing one sets the avatar key and the matching
 * accent colour together, so lanes and cards tint like the character.
 * Clearing it returns both to "picked from the agent's id".
 */
export function applyDraftLookChange(
  draft: AgentDraft,
  lookKey: string,
): AgentDraft {
  const look = foldVariantForKey(lookKey);
  if (!look) return { ...draft, accentColor: "", avatarKey: "" };
  return { ...draft, accentColor: look.color, avatarKey: look.key };
}

/**
 * Model and thinking level are one provider's vocabulary, so switching the
 * provider drops both and proposes the caller's first ready computer for
 * the new one. None leaves the Agent a template to set up later.
 */
export function applyDraftProviderChange(
  draft: AgentDraft,
  provider: AgentProvider,
  runtimes: readonly RuntimeDto[],
): AgentDraft {
  if (provider === draft.provider) return draft;
  return {
    ...draft,
    model: "",
    provider,
    runtimeId: firstReadyRuntime(runtimes, provider),
    thinkingLevel: "",
  };
}

/**
 * The computer is only where the creator sets the Agent up; the provider
 * stays, so the model and thinking level (one provider's vocabulary) stay
 * too. A model this computer does not list still shows as the saved value.
 */
export function applyDraftRuntimeChange(
  draft: AgentDraft,
  runtimeId: string,
): AgentDraft {
  if (runtimeId === draft.runtimeId) return draft;
  return { ...draft, runtimeId };
}

export type AgentDraftProblem =
  | "name_required"
  | "title_required"
  | "manager_required"
  | "runtime_provider_mismatch"
  | "runtime_offline";

export function agentDraftProblems(
  draft: AgentDraft,
  runtime: RuntimeDto | null,
): AgentDraftProblem[] {
  const problems: AgentDraftProblem[] = [];
  if (!draft.name.trim()) problems.push("name_required");
  if (!draft.title.trim()) problems.push("title_required");
  if (!draft.reportsToAgentId) problems.push("manager_required");
  // Setting the new Agent up straight away is optional: without a computer
  // it is a template nobody runs yet (ADR 0014). What is refused is naming
  // one that cannot run it.
  if (runtime && runtime.provider !== draft.provider) {
    problems.push("runtime_provider_mismatch");
  } else if (runtime && runtime.status !== "online") {
    problems.push("runtime_offline");
  }
  return problems;
}

export function buildAgentCreateRequest(draft: AgentDraft): AgentCreateRequest {
  const request: AgentCreateRequest = {
    instructions: draft.instructions.trim(),
    model: optionalOverride(draft.model),
    name: draft.name.trim(),
    provider: draft.provider,
    reportsToAgentId: draft.reportsToAgentId,
    thinkingLevel: optionalOverride(draft.thinkingLevel),
    title: draft.title.trim(),
  };
  if (draft.runtimeId) request.runtimeId = draft.runtimeId;
  if (draft.avatarKey) request.avatarKey = draft.avatarKey;
  if (draft.accentColor) request.accentColor = draft.accentColor;
  if (draft.maxConcurrentRuns !== AGENT_CAP_DEFAULT) {
    request.maxConcurrentRuns = draft.maxConcurrentRuns;
  }
  return request;
}

function optionalOverride(value: string): string | null {
  const trimmed = value.trim();
  return trimmed.length > 0 ? trimmed : null;
}
