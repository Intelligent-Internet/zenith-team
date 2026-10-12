import { cx } from "@/shared/lib/cx";
import { GradientCtaButton } from "@/shared/ui/gradient-cta-button";

export type ChatPromptOption<TOptionId extends string> = {
  description?: string;
  id: TOptionId;
  label: string;
  preview?: string;
  recommended?: boolean;
};

export function getChatPromptOptionDisplayLabel(
  option: Pick<ChatPromptOption<string>, "label" | "recommended">,
) {
  if (!option.recommended || /\(recommend\)$/i.test(option.label.trim())) {
    return option.label;
  }

  return `${option.label} (recommend)`;
}

export function ChatPromptOptionGroup<TOptionId extends string>({
  isReadOnly = false,
  label,
  onSubmit,
  options,
  submitDisabled = false,
  submitLabel = "Submit",
  selectedOptionIds,
  onToggleOption,
  showSubmit = true,
}: {
  isReadOnly?: boolean;
  label?: string;
  onSubmit?: () => void;
  options: readonly ChatPromptOption<TOptionId>[];
  submitDisabled?: boolean;
  submitLabel?: string;
  selectedOptionIds: readonly TOptionId[];
  onToggleOption: (optionId: TOptionId) => void;
  showSubmit?: boolean;
}) {
  const isSubmitDisabled =
    isReadOnly || submitDisabled || selectedOptionIds.length === 0 || !onSubmit;

  return (
    <div
      aria-label={label}
      className={cx(
        "rounded-xl border border-white/18 bg-white/[0.055] p-3 shadow-[inset_0_1px_0_rgba(255,255,255,0.12)] backdrop-blur-sm",
        isReadOnly && "pointer-events-none",
      )}
      role={label ? "group" : undefined}
    >
      <div className="flex flex-wrap gap-x-[6px] gap-y-1.5">
        {options.map((option) => {
          const selected = selectedOptionIds.includes(option.id);

          return (
            <div className="min-w-0" key={option.id}>
              <button
                type="button"
                aria-pressed={selected}
                disabled={isReadOnly}
                className={cx(
                  "min-h-[38px] rounded-[24px] px-4 py-2 text-[14px] leading-[22px] outline-none transition duration-200",
                  selected
                    ? "bg-white font-bold text-[#212121]"
                    : "bg-white/10 font-medium text-white hover:bg-white/[0.13]",
                  isReadOnly && "cursor-default",
                  isReadOnly && !selected && "hover:bg-white/10",
                )}
                onClick={() => onToggleOption(option.id)}
              >
                {getChatPromptOptionDisplayLabel(option)}
              </button>
              {option.description ? (
                <p
                  className={cx(
                    "m-0 mt-1 max-w-64 px-2 text-xs leading-4",
                    "text-white/48",
                  )}
                >
                  {option.description}
                </p>
              ) : null}
              {option.preview ? (
                <pre
                  className={cx(
                    "m-0 mt-1 max-w-72 overflow-x-auto rounded-lg px-2 py-1.5 text-[11px] leading-4",
                    "bg-black/25 text-white/56",
                  )}
                >
                  {option.preview}
                </pre>
              ) : null}
            </div>
          );
        })}
      </div>

      {!isReadOnly && showSubmit ? (
        <GradientCtaButton
          type="button"
          size="auto"
          disabled={isSubmitDisabled}
          className="mt-3 h-10 !w-[168px] rounded-xl !px-4 !text-sm"
          onClick={onSubmit}
        >
          {submitLabel}
        </GradientCtaButton>
      ) : null}
    </div>
  );
}
