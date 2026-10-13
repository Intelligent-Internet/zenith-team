import { useState } from "react";
import type { AgentDto } from "@/shared/api/types";
import {
  type AgentSkill,
  useRemoveAgentSkillMutation,
} from "@/shared/api/agent-skill-api";
import { Trash2 } from "@/shared/ui/icons";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { toast } from "@/shared/ui/sonner";
import { Tooltip } from "@/shared/ui/tooltip";
import {
  SkillError,
  skillProblem,
  type SkillProblem,
} from "./custom-skill-help";

export function CustomSkillRemoveButton({
  agent,
  skill,
  disabled,
  hasUnsavedChanges,
  onRemoved,
}: {
  agent: AgentDto;
  skill: AgentSkill;
  disabled: boolean;
  hasUnsavedChanges: boolean;
  onRemoved: () => void;
}) {
  const [removing, setRemoving] = useState<AgentSkill | null>(null);
  const [remove, removeState] = useRemoveAgentSkillMutation();
  const [removeError, setRemoveError] = useState<SkillProblem | null>(null);
  const blocked = disabled || hasUnsavedChanges || removeState.isLoading;

  async function removeSkill() {
    if (!removing || blocked || !agent.access.configure) return;
    setRemoveError(null);
    try {
      await remove({
        companyId: agent.companyId,
        agentId: agent.id,
        skillId: removing.id,
        expectedRevision: removing.revision,
      }).unwrap();
      setRemoving(null);
      toast.success("Skill removed");
      onRemoved();
    } catch (error) {
      setRemoveError(skillProblem(error));
    }
  }

  return (
    <>
      <Tooltip
        label={
          hasUnsavedChanges ? "Save or discard edits first" : "Remove skill"
        }
      >
        <span className="skill-detail-remove inline-flex">
          <button
            type="button"
            className="workspace-action-button w-[var(--workspace-control-size,_32px)] p-0"
            aria-label="Remove skill"
            disabled={blocked}
            onClick={() => {
              setRemoveError(null);
              setRemoving(skill);
            }}
          >
            <Trash2 size={16} aria-hidden />
          </button>
        </span>
      </Tooltip>
      <Dialog
        open={!!removing}
        onOpenChange={(open) => {
          if (!open && !removeState.isLoading) setRemoving(null);
        }}
      >
        <DialogContent
          className="custom-skill-dialog custom-skill-dialog--compact"
          showCloseButton={!removeState.isLoading}
        >
          <div className="custom-skill-dialog-header">
            <DialogTitle>Remove skill?</DialogTitle>
            <DialogDescription>
              <strong>{removing?.name}</strong> will no longer be available in
              new runs.
            </DialogDescription>
          </div>
          {removeError && (
            <div className="custom-skill-dialog-body">
              <SkillError problem={removeError} />
            </div>
          )}
          <div className="custom-skills-footer">
            <button
              type="button"
              className="custom-skill-button custom-skill-button--secondary"
              disabled={removeState.isLoading}
              onClick={() => setRemoving(null)}
            >
              Cancel
            </button>
            <button
              type="button"
              className="custom-skill-button [background:var(--workspace-native-danger)] text-[var(--workspace-danger-foreground)]"
              disabled={blocked}
              onClick={() => void removeSkill()}
            >
              {removeState.isLoading ? "Removing…" : "Remove skill"}
            </button>
          </div>
        </DialogContent>
      </Dialog>
    </>
  );
}
