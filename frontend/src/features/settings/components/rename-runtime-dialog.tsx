import { type FormEvent, useState } from "react";

import {
  getCeoAgentApiErrorMessage,
  useRenameRuntimeMutation,
} from "@/features/agent-setup/api/ceo-agent-api";
import {
  RUNTIME_CUSTOM_NAME_MAX_LENGTH,
  normalizeRuntimeCustomName,
} from "@/features/settings/model/runtime-management";
import type { RuntimeDto } from "@/shared/api/types";
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

type RenameRuntimeDialogProps = {
  companyId: string;
  onOpenChange: (open: boolean) => void;
  open: boolean;
  runtime: RuntimeDto;
};

// Renames the machine: the name lands on every runtime this daemon
// registered in the company (`applyToMachine` is always sent), and an empty
// name restores the daemon's own. The form mounts fresh with the dialog, so
// the draft always starts from the current name.
export function RenameRuntimeDialog({
  companyId,
  onOpenChange,
  open,
  runtime,
}: RenameRuntimeDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className="profile-delete-dialog runtime-dialog"
        overlayClassName="profile-delete-dialog__overlay"
        showCloseButton={false}
      >
        <RenameRuntimeForm
          companyId={companyId}
          runtime={runtime}
          onDone={() => onOpenChange(false)}
        />
      </DialogContent>
    </Dialog>
  );
}

function RenameRuntimeForm({
  companyId,
  onDone,
  runtime,
}: {
  companyId: string;
  onDone: () => void;
  runtime: RuntimeDto;
}) {
  const [draft, setDraft] = useState(runtime.customName ?? "");
  const [error, setError] = useState<string | null>(null);
  const [renameRuntime, mutation] = useRenameRuntimeMutation();
  const clearing = draft.trim() === "";
  const defaultComputerName =
    runtime.deviceInfo?.split(" · ")[0]?.trim() || runtime.name;

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const normalized = normalizeRuntimeCustomName(draft);
    if (normalized.error) {
      setError(normalized.error);
      return;
    }
    setError(null);
    try {
      await renameRuntime({
        companyId,
        payload: { applyToMachine: true, customName: normalized.value },
        runtimeId: runtime.id,
      }).unwrap();
      onDone();
    } catch (caught) {
      setError(
        getCeoAgentApiErrorMessage(caught, "The machine could not be renamed."),
      );
    }
  }

  return (
    <form className="profile-delete-dialog__surface" onSubmit={submit}>
      <DialogHeader className="profile-delete-dialog__header">
        <span className="profile-delete-dialog__icon runtime-dialog__icon">
          <Icon name="edit" />
        </span>
        <DialogTitle>Rename computer</DialogTitle>
        <DialogDescription>
          This name applies to every provider registered by this computer. Leave
          it empty to restore the default computer name.
        </DialogDescription>
      </DialogHeader>

      <label className="runtime-dialog__field">
        <span>Computer name</span>
        <input
          autoComplete="off"
          autoFocus
          disabled={mutation.isLoading}
          maxLength={RUNTIME_CUSTOM_NAME_MAX_LENGTH * 2}
          placeholder={defaultComputerName}
          spellCheck={false}
          value={draft}
          onChange={(event) => {
            setDraft(event.target.value);
            setError(null);
          }}
        />
        <small>
          Up to {RUNTIME_CUSTOM_NAME_MAX_LENGTH} characters. Default computer
          name: {defaultComputerName}.
        </small>
      </label>

      {error ? (
        <p className="profile-delete-dialog__error" role="alert">
          {error}
        </p>
      ) : null}

      <DialogFooter className="profile-delete-dialog__footer">
        <DialogClose asChild>
          <button
            className="profile-delete-dialog__button profile-delete-dialog__button--cancel"
            disabled={mutation.isLoading}
            type="button"
          >
            Cancel
          </button>
        </DialogClose>
        <button
          className="profile-delete-dialog__button runtime-dialog__submit"
          disabled={mutation.isLoading}
          type="submit"
        >
          {mutation.isLoading
            ? "Saving…"
            : clearing
              ? "Use default computer name"
              : "Save name"}
        </button>
      </DialogFooter>
    </form>
  );
}
