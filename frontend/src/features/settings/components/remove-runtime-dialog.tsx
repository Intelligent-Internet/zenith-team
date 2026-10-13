import { useMemo, useState } from "react";

import {
  getCeoAgentApiErrorMessage,
  useDeleteRuntimeMutation,
  useListCompanyAgentsQuery,
  useListRuntimeAgentsQuery,
  useRemoveRuntimeMutation,
} from "@/shared/api/agent-runtime-api";
import { AgentRuntimeSelect } from "@/features/agent-setup/components/agent-runtime-controls";
import {
  providerLabel,
  runtimeDisplayName,
} from "@/features/agent-setup/model/ceo-agent-setup";
import {
  buildRemovePlan,
  DROP_BINDINGS,
  orderRemoveTargets,
  removeTargetOptionLabel,
} from "@/features/settings/model/runtime-management";
import type { RuntimeDto, RuntimeRemovalResponse } from "@/shared/api/types";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { Icon } from "@/shared/ui/icon";

type RemoveRuntimeDialogProps = {
  companyId: string;
  onOpenChange: (open: boolean) => void;
  onRemoved: (receipt: RuntimeRemovalResponse | null) => void;
  open: boolean;
  runtime: RuntimeDto;
  runtimes: RuntimeDto[];
};

// The removal plan. Opening re-reads the caller's own bindings on the
// runtime (the archive-blockers pattern) and shows them, named from the
// company roster. With agents set up there, the member moves them to another
// of their computers of the same provider, or removes them and their queued
// work there (ADR 0014). Nobody else's setup lives on the machine.
export function RemoveRuntimeDialog({
  onOpenChange,
  open,
  ...props
}: RemoveRuntimeDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className="profile-delete-dialog runtime-dialog"
        overlayClassName="profile-delete-dialog__overlay"
        showCloseButton={false}
      >
        <RemoveRuntimeForm {...props} />
      </DialogContent>
    </Dialog>
  );
}

function RemoveRuntimeForm({
  companyId,
  onRemoved,
  runtime,
  runtimes,
}: Omit<RemoveRuntimeDialogProps, "onOpenChange" | "open">) {
  const blockersQuery = useListRuntimeAgentsQuery(
    { companyId, runtimeId: runtime.id },
    { refetchOnMountOrArgChange: true },
  );
  // Only names and roles: the bindings alone decide the plan.
  const rosterQuery = useListCompanyAgentsQuery(companyId);
  const [destination, setDestination] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [deleteRuntime, deleteMutation] = useDeleteRuntimeMutation();
  const [removeRuntime, removeMutation] = useRemoveRuntimeMutation();
  const blockers = useMemo(
    () => blockersQuery.currentData?.items ?? [],
    [blockersQuery.currentData?.items],
  );
  const agentById = useMemo(
    () =>
      new Map(
        (rosterQuery.currentData?.items ?? []).map((agent) => [
          agent.id,
          agent,
        ]),
      ),
    [rosterQuery.currentData?.items],
  );
  const targets = useMemo(
    () => orderRemoveTargets(runtime, runtimes),
    [runtime, runtimes],
  );
  const displayName = runtimeDisplayName(runtime);
  const provider = providerLabel(runtime.provider);
  const plan = buildRemovePlan(blockers, destination);
  const busy =
    deleteMutation.isLoading ||
    removeMutation.isLoading ||
    blockersQuery.isFetching;
  const canConfirm =
    !busy &&
    blockersQuery.currentData !== undefined &&
    plan.kind !== "needs_target";
  const one = blockers.length === 1;

  async function confirm() {
    setError(null);
    try {
      if (plan.kind === "delete") {
        await deleteRuntime({ companyId, runtimeId: runtime.id }).unwrap();
        onRemoved(null);
      } else if (plan.kind === "remove" || plan.kind === "drop") {
        const receipt = await removeRuntime({
          companyId,
          payload: plan.payload,
          runtimeId: runtime.id,
        }).unwrap();
        onRemoved(receipt);
      }
    } catch (caught) {
      // `runtime_remove_blocked` carries the reason in its detail: quit
      // Zenith Team on that computer, or stop its zen daemon, first.
      setError(
        getCeoAgentApiErrorMessage(caught, "The runtime could not be removed."),
      );
    }
  }

  return (
    <div className="profile-delete-dialog__surface">
      <DialogHeader className="profile-delete-dialog__header">
        <span className="profile-delete-dialog__icon">
          <Icon name="delete" />
        </span>
        <DialogTitle>Remove {displayName}?</DialogTitle>
        <DialogDescription>
          {blockersQuery.currentData === undefined
            ? "Checking which agents run on this runtime…"
            : blockers.length === 0
              ? "You have no agents set up on it. Its run history stays readable, and the computer registers it again if Zenith Team starts there again."
              : `${one ? "1 agent is" : `${blockers.length} agents are`} set up on this runtime for you. Move ${one ? "it" : "them"} to another of your ${provider} computers, where queued work follows, or remove ${one ? "it" : "them"} from your setup. The agents stay in the company, and the run history stays readable.`}
        </DialogDescription>
      </DialogHeader>

      {blockersQuery.isError && blockersQuery.currentData === undefined ? (
        <p className="profile-delete-dialog__error" role="alert">
          {getCeoAgentApiErrorMessage(
            blockersQuery.error,
            "The agents on this runtime could not be loaded.",
          )}
        </p>
      ) : null}

      {blockers.length > 0 ? (
        <>
          <ul
            aria-label="Agents set up on this runtime"
            className="runtime-dialog__blockers grid max-h-[168px] gap-[6px] m-[16px_0_0] overflow-y-auto [border:1px_solid_var(--workspace-divider,_rgba(255,_255,_255,_0.1))] rounded-[12px] p-[10px_12px] [list-style:none]"
          >
            {blockers.map((binding) => {
              const agent = agentById.get(binding.agentId);
              return (
                <li key={binding.id}>
                  <strong>{agent?.name ?? "Agent"}</strong>
                  <span>{agent?.title}</span>
                </li>
              );
            })}
          </ul>
          <label className="runtime-dialog__field grid gap-[8px] mt-[20px] text-[var(--workspace-native-text-muted,_rgba(255,_255,_255,_0.7))] text-workspace-body leading-workspace-body text-left">
            <span>What happens to the agents</span>
            <AgentRuntimeSelect
              aria-label="What happens to the agents"
              disabled={busy}
              value={destination}
              onChange={(event) => setDestination(event.target.value)}
            >
              <option value="">Choose</option>
              {targets.map((target) => (
                <option key={target.id} value={target.id}>
                  {`Move to ${removeTargetOptionLabel(runtime, target)}`}
                </option>
              ))}
              <option value={DROP_BINDINGS}>
                Remove them (queued work is cancelled)
              </option>
            </AgentRuntimeSelect>
            {targets.length === 0 ? (
              <small>
                You have no other {provider} computer to move them to.
              </small>
            ) : null}
          </label>
        </>
      ) : null}

      {error ? (
        <p className="profile-delete-dialog__error" role="alert">
          {error}
        </p>
      ) : null}

      <DialogFooter className="profile-delete-dialog__footer">
        <DialogClose asChild>
          <button
            className="profile-delete-dialog__button profile-delete-dialog__button--cancel"
            disabled={busy}
            type="button"
          >
            Cancel
          </button>
        </DialogClose>
        <button
          className="profile-delete-dialog__button runtime-dialog__confirm"
          disabled={!canConfirm}
          type="button"
          onClick={() => void confirm()}
        >
          {busy
            ? "Working…"
            : plan.kind === "remove"
              ? "Move agents and remove"
              : "Remove runtime"}
        </button>
      </DialogFooter>
    </div>
  );
}
