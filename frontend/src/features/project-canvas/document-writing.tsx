import type { components } from "@/shared/api/generated/api.generated";
import {
  Dialog,
  DialogContent,
  DialogTitle,
  DialogDescription,
} from "@/shared/ui/core/dialog";
import { useRef, useState } from "react";
import type { ReviewAnchor } from "./annotation-overlay";
import type { PageScope } from "./page-api";
import { useCanvasCommandMutation } from "./canvas-api";
import { useDocumentAgents } from "./use-document-agents";
import "./document-writing.css";

export const writingActions = [
  {
    id: "generate",
    label: "Generate…",
    prompt: "Generate new content at this location.",
    input: "What should the agent write?",
  },
  {
    id: "continue",
    label: "Continue writing",
    prompt:
      "Continue writing naturally from this location, using the preceding document for context.",
  },
  {
    id: "rewrite",
    label: "Rewrite…",
    prompt: "Rewrite the selected passage or anchored block.",
    input: "How should it change?",
  },
  {
    id: "expand",
    label: "Expand",
    prompt:
      "Expand the selected passage or anchored block with useful detail. Do not invent unsupported facts.",
  },
  {
    id: "shorten",
    label: "Shorten",
    prompt:
      "Make the selected passage or anchored block more concise while preserving its meaning.",
  },
  {
    id: "summarize",
    label: "Summarize",
    prompt:
      "Summarize the selected passage, or summarize the document and insert the summary at this location if nothing is selected.",
  },
  {
    id: "grammar",
    label: "Fix grammar",
    prompt:
      "Correct grammar and spelling in the selected passage or anchored block, preserving meaning and tone.",
  },
  {
    id: "tone",
    label: "Change tone…",
    prompt: "Change the tone of the selected passage or anchored block.",
    input: "Which tone? For example: professional, friendly, or direct.",
  },
  {
    id: "translate",
    label: "Translate…",
    prompt: "Translate the selected passage or anchored block.",
    input: "Which language?",
  },
  {
    id: "ask",
    label: "Ask an agent…",
    prompt: "Answer this question about the document.",
    input: "What would you like help with?",
  },
] as const;
export type WritingAction = (typeof writingActions)[number];
export type WritingRequest = {
  action: WritingAction;
  anchor: ReviewAnchor;
  target?: components["schemas"]["DocumentWritingTarget"];
};

export function DocumentWritingPrompt({
  scope,
  request,
  agentId,
  onAgentChange,
  flush,
  onClose,
  onSubmitted,
}: {
  scope: PageScope;
  request: WritingRequest;
  agentId: string;
  onAgentChange: (id: string) => void;
  flush: () => Promise<boolean>;
  onClose: () => void;
  onSubmitted: (threadId: string) => void;
}) {
  const directory = useDocumentAgents(scope);
  const availableAgents = directory.agents.filter(
    (agent) => !agent.unavailable,
  );
  const selected =
    availableAgents.find((agent) => agent.id === agentId)?.id ||
    availableAgents[0]?.id ||
    "";
  const [instruction, setInstruction] = useState("");
  const [error, setError] = useState("");
  const [sending, setSending] = useState(false);
  const [mutate] = useCanvasCommandMutation();
  const requestKey = useRef(crypto.randomUUID());
  const action = request.action;
  const needsInstruction = "input" in action;
  const submit = async () => {
    if (sending || !selected || (needsInstruction && !instruction.trim()))
      return;
    setSending(true);
    setError("");
    try {
      if (!(await flush()))
        throw new Error(
          "Wait for the document to save before asking an agent.",
        );
      const body = `${action.prompt}${instruction.trim() ? `\n${instruction.trim()}` : ""}`;
      const result = await mutate({
        ...scope,
        command: {
          type: "comment",
          pageId: scope.pageId,
          anchor: request.anchor,
          writingTarget: request.target,
          body,
          agentIds: [selected],
          requestKey: requestKey.current,
        },
      }).unwrap();
      onAgentChange(selected);
      onSubmitted(result.id);
      onClose();
    } catch (reason) {
      setError(
        reason instanceof Error
          ? reason.message
          : "The request could not be sent. Check your agent setup and project access, then retry.",
      );
    } finally {
      setSending(false);
    }
  };
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !sending) onClose();
      }}
    >
      <DialogContent
        className="z-[150] w-auto border-0 p-0 sm:max-w-none"
        overlayClassName="z-[149]"
        showCloseButton={false}
        onInteractOutside={(event) => {
          if (sending) event.preventDefault();
        }}
      >
        <DialogTitle className="sr-only">{action.label}</DialogTitle>
        <DialogDescription className="sr-only">
          Choose an agent to propose document changes for your review.
        </DialogDescription>
        <form
          className="pc-writing-prompt"
          aria-label={action.label}
          onSubmit={(event) => {
            event.preventDefault();
            void submit();
          }}
          onKeyDown={(event) => {
            if (event.key === "Escape" && !sending) {
              event.preventDefault();
              event.stopPropagation();
              onClose();
            }
          }}
        >
          <strong>{action.label.replace("…", "")}</strong>
          <label>
            Agent
            <select
              autoFocus={!needsInstruction}
              value={selected}
              disabled={sending || directory.loading}
              onChange={(event) => {
                onAgentChange(event.target.value);
                requestKey.current = crypto.randomUUID();
              }}
            >
              <option value="" disabled>
                {directory.loading ? "Loading agents…" : "Choose an agent"}
              </option>
              {directory.agents.map((agent) => (
                <option
                  key={agent.id}
                  value={agent.id}
                  disabled={!!agent.unavailable}
                >
                  {agent.name}
                  {agent.unavailable ? ` · ${agent.unavailable}` : ""}
                </option>
              ))}
            </select>
          </label>
          {needsInstruction && (
            <textarea
              aria-label={action.input}
              placeholder={action.input}
              autoFocus
              rows={3}
              maxLength={3000}
              value={instruction}
              disabled={sending}
              onChange={(event) => {
                setInstruction(event.target.value);
                requestKey.current = crypto.randomUUID();
              }}
            />
          )}
          {directory.failed && (
            <button type="button" onClick={() => void directory.retry()}>
              Retry loading agents
            </button>
          )}
          {!directory.loading &&
            !directory.failed &&
            !directory.agents.some((agent) => !agent.unavailable) && (
              <p>Set up an agent on your computer to use writing actions.</p>
            )}
          <small>
            {request.target
              ? "Changes appear as suggestions. You decide what to accept."
              : "The agent will reply in the comment thread."}
          </small>
          {error && <p role="alert">{error}</p>}
          <footer>
            <button type="button" disabled={sending} onClick={onClose}>
              Cancel
            </button>
            <button
              disabled={
                sending ||
                !selected ||
                directory.failed ||
                (needsInstruction && !instruction.trim())
              }
            >
              {sending ? "Sending…" : "Send"}
            </button>
          </footer>
        </form>
      </DialogContent>
    </Dialog>
  );
}
