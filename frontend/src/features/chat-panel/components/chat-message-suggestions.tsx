import { ArrowUpRight, Sparkles } from "@/shared/ui/icons";

import { START_BUILDING_MESSAGE } from "@/features/home/lib/home-start-building";
import { cx } from "@/shared/lib/cx";

export type ChatMessageSuggestion = {
  id: string;
  label: string;
  message: string;
};

export const ceoMessageSuggestions: readonly ChatMessageSuggestion[] = [
  {
    id: "build-business-context",
    label: "Start building business context",
    message: START_BUILDING_MESSAGE,
  },
  {
    id: "refine-idea",
    label: "Refine my idea",
    message:
      "Help me refine my business idea. Ask me about the product, target customers, and the problem I want to solve.",
  },
  {
    id: "plan-next-steps",
    label: "Plan next steps",
    message:
      "Help me choose a first milestone and the next steps for my business. Ask for any context you need first.",
  },
];

export function ChatMessageSuggestions({
  isExpanded,
  onSelect,
  suggestions,
}: {
  isExpanded: boolean;
  onSelect: (message: string) => void;
  suggestions: readonly ChatMessageSuggestion[];
}) {
  return (
    <div
      aria-label="Suggested messages"
      className={cx(
        "workspace-chat-suggestions mb-3 flex shrink-0 flex-wrap items-center gap-2",
        isExpanded
          ? "mx-auto w-[calc(100%-48px)] max-w-[832px]"
          : "mx-4 w-[calc(100%-32px)]",
      )}
      role="group"
    >
      <span className="workspace-chat-suggestions__label mr-1 inline-flex items-center gap-1.5 text-xs text-white/60">
        <Sparkles aria-hidden="true" size={14} />
        Try a prompt
      </span>
      {suggestions.map((suggestion) => (
        <button
          className="workspace-chat-suggestion inline-flex min-h-9 items-center gap-2 rounded-lg border border-white/12 bg-white/5 px-3 py-2 text-xs font-medium text-white/78 transition-colors hover:bg-white/10"
          data-tour-id={suggestion.id}
          key={suggestion.id}
          onClick={() => onSelect(suggestion.message)}
          title={suggestion.message}
          type="button"
        >
          {suggestion.label}
          <ArrowUpRight aria-hidden="true" size={13} />
        </button>
      ))}
    </div>
  );
}
