import { useCallback, useRef, useState, type ReactNode } from "react";

import { BindAgentDialog } from "@/features/agents/components/bind-agent-dialog";
import type { AgentDto } from "@/shared/api/types";

type OpenOptions = {
  // What the viewer was trying to do. It runs once the Agent is set up, so
  // "Set Lyra up on my computer" can finish as "run this task".
  onBound?: () => void;
  // The viewer closed the dialog without setting the Agent up, so whatever
  // was waiting on the setup (a chat send) stops waiting.
  onDismiss?: () => void;
};

// One bind dialog for a screen, opened from wherever the viewer meets an
// Agent they have not set up yet: a roster row, an Agent's page, a task's
// run button, a new task form, a chat composer (ADR 0014).
export function useBindAgentDialog(companyId: string): {
  dialog: ReactNode;
  openBindDialog: (agent: AgentDto, options?: OpenOptions) => void;
} {
  const [request, setRequest] = useState<{
    agent: AgentDto;
    options: OpenOptions;
  } | null>(null);
  // Closing follows a successful setup too, so the dialog remembers which
  // of the two ended it.
  const bound = useRef(false);

  const openBindDialog = useCallback(
    (agent: AgentDto, options: OpenOptions = {}) => {
      bound.current = false;
      setRequest({ agent, options });
    },
    [],
  );

  const dialog = request ? (
    <BindAgentDialog
      agent={request.agent}
      companyId={companyId}
      onBound={() => {
        bound.current = true;
        request.options.onBound?.();
      }}
      onOpenChange={(open) => {
        if (open) return;
        if (!bound.current) request.options.onDismiss?.();
        setRequest(null);
      }}
      open
    />
  ) : null;

  return { dialog, openBindDialog };
}
