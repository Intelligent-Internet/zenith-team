import type {
  AgentDto,
  AgentUnavailableReason,
  RuntimeDto,
} from "@/shared/api/types";

// An Agent is a template the company owns; a binding is how one member runs
// it on one of their own computers (ADR 0014). Everything here answers for
// the viewer alone: what they have set up, whether it can run right now, and
// which of their runtimes they could choose. Nobody else's machine is ever
// named, because the API never sends one.

// What the roster row and the detail sidebar call the viewer's setup.
export function agentBindingLabel(agent: Pick<AgentDto, "binding">): string {
  const runtime = agent.binding?.runtime;
  if (!runtime) {
    return "Not set up on your computer";
  }
  return runtime.customName?.trim() || runtime.name;
}

// The short chip beside an Agent: what the viewer can expect if they ask it
// to work right now.
export function agentAvailabilityLabel(
  agent: Pick<AgentDto, "binding" | "runtimeAvailable" | "unavailableReason">,
): string {
  if (agent.runtimeAvailable) {
    return "Ready";
  }
  return AGENT_UNAVAILABLE_LABEL[unavailableReason(agent)];
}

// Why the Agent cannot run for the viewer. A status frame turns an Agent
// off before the refetch brings its reason. That frame means the computer
// went offline, and a bound Agent is never "not set up".
function unavailableReason(
  agent: Pick<AgentDto, "binding" | "unavailableReason">,
): NonNullable<AgentUnavailableReason> {
  return (
    agent.unavailableReason ?? (agent.binding ? "runtime_offline" : "not_bound")
  );
}

const AGENT_UNAVAILABLE_LABEL: Record<
  NonNullable<AgentUnavailableReason>,
  string
> = {
  agent_archived: "Archived",
  not_bound: "Not set up",
  runtime_unusable: "Computer can't run it",
  runtime_offline: "Computer offline",
  runtime_signed_out: "Signed out",
};

// The sentence under the chip, for a viewer deciding what to do about it.
export function agentAvailabilityHelp(
  agent: Pick<AgentDto, "binding" | "runtimeAvailable" | "unavailableReason">,
): string {
  if (agent.runtimeAvailable) {
    return `Runs on ${agentBindingLabel(agent)}.`;
  }
  const runtime = agentBindingLabel(agent);
  switch (unavailableReason(agent)) {
    case "agent_archived":
      return "This agent is archived. Restore it before setting it up.";
    case "not_bound":
      return "Set this agent up on one of your computers to run it.";
    case "runtime_unusable":
      return `${runtime} can't run it right now. Open Computers for the reason.`;
    case "runtime_offline":
      return `${runtime} is offline. Start Zenith Team there, then refresh.`;
    default:
      return `Sign in to the provider on ${runtime}, then refresh.`;
  }
}

// The API's refusal to run an Agent the viewer has not set up (ADR 0014).
// Every layer a send can fail in — an RTK Query error, an ApiClientError, a
// rejected chat receipt — carries the code as `code`.
export function isAgentBindingRequired(error: unknown): boolean {
  return (
    typeof error === "object" &&
    error !== null &&
    (error as { code?: unknown }).code === "agent_binding_required"
  );
}

// Sends to an Agent the viewer may not have set up yet. When they have not —
// known up front, or learned from the server's refusal — it offers the setup
// first and sends the same words once it is done. Closing the setup without
// finishing it rejects, so the caller keeps the draft and can say why
// nothing was sent.
export async function sendWithAgentSetup<T>(
  send: () => Promise<T>,
  agent: { bound: boolean; name: string },
  setUp: () => Promise<boolean>,
): Promise<T> {
  if (!agent.bound && !(await setUp())) {
    throw agentNotSetUpError(agent.name);
  }
  try {
    return await send();
  } catch (error) {
    if (!isAgentBindingRequired(error)) {
      throw error;
    }
    if (!(await setUp())) {
      throw agentNotSetUpError(agent.name);
    }
    return send();
  }
}

function agentNotSetUpError(agentName: string): Error {
  return Object.assign(
    new Error(
      `${agentName} is not set up on your computer yet, so nothing was sent.`,
    ),
    { code: "agent_binding_required" },
  );
}

// A runtime the viewer could set this Agent up on, and why not when they
// cannot. Only the viewer's own runtimes are ever listed, because the roster
// they come from is theirs alone.
export type BindingRuntimeChoice = {
  runtime: RuntimeDto;
  selectable: boolean;
  // Why it cannot be chosen, in the words the row shows under its name.
  // Empty when it can.
  reason: string;
};

export function bindingRuntimeChoices(
  provider: AgentDto["provider"],
  runtimes: RuntimeDto[],
): BindingRuntimeChoice[] {
  return runtimes
    .filter((runtime) => runtime.provider === provider)
    .map((runtime) => ({
      runtime,
      selectable: bindingChoiceState(runtime) !== "runtime_unusable",
      reason: bindingChoiceReason(runtime),
    }))
    .sort(
      (left, right) =>
        CHOICE_RANK[bindingChoiceState(left.runtime)] -
          CHOICE_RANK[bindingChoiceState(right.runtime)] ||
        runtimeChoiceName(left.runtime).localeCompare(
          runtimeChoiceName(right.runtime),
        ),
    );
}

// Where one of the viewer's computers stands for a new binding, with the
// server's precedence: unusable, then offline, then signed out. A binding to
// an offline or signed-out computer is allowed — the work simply waits for
// it — so only an unusable one is refused. The row chip, the list order and
// the note under the list all read this one answer.
type BindingChoiceState =
  "ready" | "runtime_unusable" | "runtime_offline" | "runtime_signed_out";

function bindingChoiceState(runtime: RuntimeDto): BindingChoiceState {
  if (runtime.unusableReason) return "runtime_unusable";
  if (runtime.status !== "online") return "runtime_offline";
  if (runtime.signIn?.state === "signed_out") return "runtime_signed_out";
  return "ready";
}

// Ready computers first, then ones the Agent can wait for, then the ones
// that cannot take it at all.
const CHOICE_RANK: Record<BindingChoiceState, number> = {
  ready: 0,
  runtime_offline: 1,
  runtime_signed_out: 1,
  runtime_unusable: 2,
};

function bindingChoiceReason(runtime: RuntimeDto): string {
  if (!runtime.unusableReason) {
    return "";
  }
  return (
    runtime.unusableReason.message?.trim() ||
    "This computer can't run agents right now"
  );
}

// The chip beside one of the viewer's computers, in the words the Agent's
// own chip uses (agentAvailabilityLabel), so the setup dialog and the card
// it returns to never call the same computer two different things.
export function bindingChoiceStateLabel(runtime: RuntimeDto): string {
  const state = bindingChoiceState(runtime);
  return state === "ready"
    ? runtime.signIn?.state === "signed_in"
      ? "Ready"
      : "Sign-in unknown"
    : AGENT_UNAVAILABLE_LABEL[state];
}

// What choosing a computer that is not ready means for the Agent, said once
// under the list rather than on every row: the setup is kept and the runs
// wait. Empty when the computer is ready or cannot be chosen at all.
export function bindingChoiceWarning(
  runtime: RuntimeDto,
  agentName: string,
): string {
  const name = runtimeChoiceName(runtime);
  switch (bindingChoiceState(runtime)) {
    case "ready":
      return runtime.signIn?.state === "signed_in"
        ? ""
        : `Sign-in has not been confirmed on ${name}. You can save this setup, then refresh the computer to check.`;
    case "runtime_offline":
      return `${name} is offline. ${agentName}'s runs will wait until it is back online.`;
    case "runtime_signed_out": {
      const provider = runtime.provider === "claude" ? "Claude Code" : "Codex";
      return `${provider} is not signed in on ${name}. ${agentName}'s runs will wait until you sign in there.`;
    }
    default:
      return "";
  }
}

export function runtimeChoiceName(runtime: RuntimeDto): string {
  return runtime.customName?.trim() || runtime.name;
}
