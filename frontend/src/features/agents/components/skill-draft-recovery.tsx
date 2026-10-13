import { useState } from "react";
import { useAppDispatch, useAppSelector } from "@/state/store";
import { clearSkillFileDraft } from "@/state/slice/skill-file-drafts";
import { Copy, Undo2 } from "@/shared/ui/icons";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { toast } from "@/shared/ui/sonner";

export function SkillDraftCopyButton({ draftKey }: { draftKey: string }) {
  const draft = useAppSelector((state) => state.skillFileDrafts[draftKey]);
  if (!draft) return null;
  return (
    <button
      type="button"
      className="workspace-action-button"
      onClick={() =>
        void navigator.clipboard
          .writeText(
            JSON.stringify(
              {
                changes: draft.changes.map((file) => ({
                  path: file.path,
                  content: file.content,
                })),
                deletePaths: draft.deletePaths,
              },
              null,
              2,
            ),
          )
          .then(() => toast.success("Unsaved edits copied"))
          .catch(() => toast.error("Couldn’t copy your edits"))
      }
    >
      <Copy size={16} aria-hidden />
      Copy edits
    </button>
  );
}

export function SkillDraftRecovery({ draftKey }: { draftKey: string }) {
  const dispatch = useAppDispatch();
  const draft = useAppSelector((state) => state.skillFileDrafts[draftKey]);
  const [confirm, setConfirm] = useState(false);
  if (!draft) return null;
  return (
    <div className="skill-draft-recovery mt-[20px]">
      <p>Your unsaved edits are still available.</p>
      <div>
        <SkillDraftCopyButton draftKey={draftKey} />
        <button
          type="button"
          className="workspace-action-button"
          onClick={() => setConfirm(true)}
        >
          <Undo2 size={16} aria-hidden />
          Discard
        </button>
      </div>
      <Dialog open={confirm} onOpenChange={setConfirm}>
        <DialogContent className="custom-skill-dialog custom-skill-dialog--compact">
          <div className="custom-skill-dialog-header">
            <DialogTitle>Discard changes?</DialogTitle>
            <DialogDescription>
              Unsaved edits and file deletions will be cleared.
            </DialogDescription>
          </div>
          <div className="custom-skills-footer">
            <button
              type="button"
              className="custom-skill-button custom-skill-button--secondary"
              onClick={() => setConfirm(false)}
            >
              Cancel
            </button>
            <button
              type="button"
              className="custom-skill-button [background:var(--workspace-native-danger)] text-[var(--workspace-danger-foreground)]"
              onClick={() => dispatch(clearSkillFileDraft(draftKey))}
            >
              Discard changes
            </button>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  );
}
