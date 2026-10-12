import {
  chatCompanyModeOptions,
  type ChatCompanyModeOption,
} from "@/features/chat-panel/model/chat-company-mode-options";
import {
  chatSelectorCheckIconOffsetClassName,
  chatSelectorCheckIconSizeClassName,
  chatSelectorContentPaddingClassName,
  chatSelectorGridClassName,
  type ChatSelectorLayout,
} from "@/features/chat-panel/model/chat-selector-layout";
import { cx } from "@/shared/lib/cx";
import { GradientCtaButton } from "@/shared/ui/gradient-cta-button";
import { Icon } from "@/shared/ui/icon";
import { ContentPanel } from "@/shared/ui/content-panel";

type ChatCompanyModeSelectProps = {
  className?: string;
  layout?: ChatSelectorLayout;
  options?: ChatCompanyModeOption[];
  readOnly?: boolean;
  selectedId: ChatCompanyModeOption["id"];
  submitClassName?: string;
  submitDisabled?: boolean;
  submitLabel?: string;
  onSelect: (optionId: ChatCompanyModeOption["id"]) => void;
  onSubmit: () => void;
};

export { chatCompanyModeOptions };

export function ChatCompanyModeSelect({
  className,
  layout = "compact",
  options = chatCompanyModeOptions,
  readOnly = false,
  selectedId,
  submitClassName,
  submitDisabled = false,
  submitLabel = "Let's Go",
  onSelect,
  onSubmit,
}: ChatCompanyModeSelectProps) {
  const isWide = layout === "wide";
  const isSubmitDisabled = readOnly || submitDisabled;

  return (
    <div className={cx("grid gap-2", className)}>
      <ContentPanel
        className={cx(
          "w-full min-w-0 overflow-hidden rounded-xl",
          isWide ? "min-h-[548px]" : "min-h-[420px]",
        )}
        contentClassName={cx(
          "pointer-events-auto h-full w-full flex-col overflow-hidden rounded-xl text-white",
          chatSelectorContentPaddingClassName[layout],
        )}
        aria-label="Choose company operating mode"
      >
        <div
          className={cx(
            "grid min-h-0 flex-1",
            chatSelectorGridClassName[layout],
          )}
        >
          {options.map((option) => (
            <CompanyModeCard
              key={option.id}
              layout={layout}
              option={option}
              readOnly={readOnly}
              selected={selectedId === option.id}
              onSelect={() => onSelect(option.id)}
            />
          ))}
        </div>

        <GradientCtaButton
          type="button"
          disabled={isSubmitDisabled}
          className={cx(
            "rounded-xl",
            isWide
              ? "mt-[14px] h-[54px] !w-[280px] gap-3 !px-6 !text-lg"
              : "mt-3 h-12 !w-[280px] gap-2.5 !px-5 !text-base",
            submitClassName,
          )}
          onClick={onSubmit}
        >
          {submitLabel}
        </GradientCtaButton>
      </ContentPanel>
    </div>
  );
}

function CompanyModeCard({
  layout,
  option,
  readOnly,
  selected,
  onSelect,
}: {
  layout: ChatSelectorLayout;
  option: ChatCompanyModeOption;
  readOnly: boolean;
  selected: boolean;
  onSelect: () => void;
}) {
  const isWide = layout === "wide";

  return (
    <button
      type="button"
      disabled={readOnly}
      className={cx(
        "chat-company-mode-option-card relative flex w-full min-w-0 flex-col items-center overflow-hidden rounded-[20px] border border-white/30 bg-white/10 text-center text-white outline-none transition hover:bg-white/[0.11]",
        selected && "chat-company-mode-option-card--selected",
        readOnly && "cursor-default",
        isWide ? "h-full px-5 pb-5 pt-5" : "h-full rounded-xl px-3 pb-4 pt-4",
      )}
      aria-pressed={selected}
      onClick={onSelect}
    >
      {selected ? (
        <span
          className={cx(
            "absolute text-white",
            chatSelectorCheckIconOffsetClassName[layout],
          )}
          aria-hidden="true"
        >
          <Icon
            name="check-circle"
            className={chatSelectorCheckIconSizeClassName[layout]}
          />
        </span>
      ) : null}

      <span className="mt-0 text-white" aria-hidden="true">
        <Icon name={option.icon} className={isWide ? "size-10" : "size-8"} />
      </span>
      <h3
        className={cx(
          "mb-0 font-bold leading-none tracking-normal text-white",
          isWide ? "mt-3 text-[22px]" : "mt-2 text-[18px]",
        )}
      >
        {option.label}
      </h3>
      <p
        className={cx(
          "mx-auto mb-0 font-medium leading-[1.35] text-white/88",
          isWide
            ? "mt-3 max-w-[330px] text-[15px]"
            : "mt-2 max-w-[168px] text-xs",
        )}
      >
        {option.description}
      </p>

      <div
        className={cx(
          "flex flex-wrap justify-center",
          isWide ? "mt-4 gap-2" : "mt-3 gap-1.5",
        )}
      >
        {option.tags.map((tag) => (
          <span
            key={tag}
            className={cx(
              "rounded-full bg-[rgba(244,224,91,0.13)] font-bold leading-none text-[#ffe45e]",
              isWide ? "px-3 py-1.5 text-xs" : "px-2 py-1 text-[12px]",
            )}
          >
            {tag}
          </span>
        ))}
      </div>

      <p
        className={cx(
          "mb-0 mt-auto font-medium leading-[1.35] text-white/40",
          isWide ? "pt-3 text-xs" : "pt-2 text-xs",
        )}
      >
        {option.footer}
      </p>
    </button>
  );
}
