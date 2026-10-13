import { useUpdateWorkspaceAgentMutation } from "@/shared/api/agent-api";
import { useRef, useState } from "react";
import { useAppDispatch } from "@/state/store";
import {
  agentRuntimeApi,
  getCeoAgentApiErrorMessage,
} from "@/shared/api/agent-runtime-api";

import type { AgentDto } from "@/shared/api/types";
import { Pencil } from "@/shared/ui/icons";
import { toast } from "@/shared/ui/sonner";

// The agent's name with a pencil beside it: click to edit, Enter or blur
// saves, Escape cancels. Only people who can configure the agent see the pencil.
export function EditableAgentName({ agent }: { agent: AgentDto }) {
  const dispatch = useAppDispatch();
  const [save] = useUpdateWorkspaceAgentMutation();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(agent.name);
  const done = useRef(false);

  async function commit() {
    if (done.current) return;
    done.current = true;
    const name = draft.trim();
    setEditing(false);
    if (!name || name === agent.name) return;
    try {
      const updated = await save({
        companyId: agent.companyId,
        agentId: agent.id,
        request: { expectedRevision: agent.revision, name },
      }).unwrap();
      dispatch(
        agentRuntimeApi.util.updateQueryData(
          "listCompanyAgents",
          agent.companyId,
          (page) => {
            const index = page.items.findIndex(
              (item) => item.id === updated.id,
            );
            if (index >= 0) page.items[index] = updated;
          },
        ),
      );
    } catch (error) {
      toast.error(
        getCeoAgentApiErrorMessage(error, "The name could not be saved."),
      );
    }
  }

  if (editing) {
    return (
      <input
        aria-label="Agent name"
        autoComplete="off"
        autoFocus
        className="agent-name-input"
        maxLength={120}
        value={draft}
        onBlur={() => void commit()}
        onChange={(event) => setDraft(event.target.value)}
        onKeyDown={(event) => {
          if (event.key === "Enter") void commit();
          if (event.key === "Escape") {
            done.current = true;
            setEditing(false);
          }
        }}
      />
    );
  }
  return (
    <>
      {agent.name}
      {agent.access.configure ? (
        <button
          aria-label="Rename agent"
          className="agent-name-edit"
          title="Rename agent"
          type="button"
          onClick={() => {
            done.current = false;
            setDraft(agent.name);
            setEditing(true);
          }}
        >
          <Pencil size={16} aria-hidden="true" />
        </button>
      ) : null}
    </>
  );
}
