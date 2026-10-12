import { useState } from "react";

import type { PluginItemV1 } from "@/shared/api/types";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { Icon } from "@/shared/ui/icon";

// Uninstall confirmation for the Plugins list and a plugin's own page. It is
// open while `plugin` is set and cannot be dismissed while the change is
// pending; the caller closes it (sets `plugin` to null) when the change ends.
// Focus starts on Cancel, so Enter never uninstalls by accident. The dialog
// has no trigger of its own, so on close focus goes to `returnFocus()`: the
// control that opened it, or whatever the caller puts in its place.
export function PluginUninstallDialog({
  onCancel,
  onConfirm,
  pending,
  plugin,
  returnFocus,
  targetLabel,
}: {
  plugin: PluginItemV1 | null;
  targetLabel: string;
  pending: boolean;
  onConfirm(): void;
  onCancel(): void;
  returnFocus(): HTMLElement | null | undefined;
}) {
  // The last plugin keeps its copy on screen while the dialog animates closed.
  const [shownPlugin, setShownPlugin] = useState(plugin);
  if (plugin && plugin !== shownPlugin) setShownPlugin(plugin);
  const name = shownPlugin?.name ?? "this plugin";
  const accountHome = shownPlugin?.provider === "claude" ? "Claude" : "ChatGPT";

  return (
    <Dialog
      open={Boolean(plugin)}
      onOpenChange={(open) => {
        if (!open && !pending) onCancel();
      }}
    >
      <DialogContent
        className="profile-delete-dialog"
        overlayClassName="profile-delete-dialog__overlay"
        showCloseButton={false}
        onCloseAutoFocus={(event) => {
          const target = returnFocus();
          if (!target?.isConnected) return;
          event.preventDefault();
          target.focus();
        }}
      >
        <div className="profile-delete-dialog__surface" aria-busy={pending}>
          <DialogHeader className="profile-delete-dialog__header">
            <span className="profile-delete-dialog__icon">
              <Icon name="delete" />
            </span>
            <DialogTitle>Uninstall {name}?</DialogTitle>
            <DialogDescription>
              {name} is removed from {targetLabel}. Agents on this computer can
              no longer use its skills, apps or MCP servers. You can install it
              again at any time.
            </DialogDescription>
          </DialogHeader>
          <p className="profile-delete-dialog__confirmation">
            Uninstalling does not revoke provider accounts. Apps you connected
            through it stay connected in {accountHome} until you disconnect them
            there.
          </p>
          <DialogFooter className="profile-delete-dialog__footer">
            <button
              className="profile-delete-dialog__button profile-delete-dialog__button--cancel"
              disabled={pending}
              type="button"
              onClick={onCancel}
            >
              Cancel
            </button>
            {/* Stays focusable while pending so keyboard focus is not lost. */}
            <button
              aria-disabled={pending}
              className="profile-delete-dialog__button profile-delete-dialog__button--delete"
              type="button"
              onClick={() => {
                if (!pending) onConfirm();
              }}
            >
              {pending ? "Uninstalling…" : "Uninstall"}
            </button>
          </DialogFooter>
        </div>
      </DialogContent>
    </Dialog>
  );
}
