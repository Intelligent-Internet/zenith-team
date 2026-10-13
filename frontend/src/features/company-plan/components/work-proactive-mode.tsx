import { X, Zap } from "@/shared/ui/icons";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogTitle,
  DialogTrigger,
} from "@/shared/ui/core/dialog";
import { Switch } from "@/shared/ui/switch";
import "@/features/company-plan/styles/work-proactive-mode.css";

export type ProactiveModeStatus =
  | { type: "error"; message: string }
  | { type: "loading" | "read-only" | "saving" }
  | null;

export function WorkProactiveMode({
  enabled,
  disabled,
  onEnabledChange,
  onRetry,
  status,
}: {
  enabled: boolean;
  disabled: boolean;
  onEnabledChange: (checked: boolean) => void;
  onRetry: () => void;
  status: ProactiveModeStatus;
}) {
  const stateLabel =
    status?.type === "loading"
      ? "Loading…"
      : status?.type === "saving"
        ? "Saving…"
        : status?.type === "error"
          ? "Unavailable"
          : enabled
            ? "On"
            : "Off";

  return (
    <Dialog>
      <DialogTrigger asChild>
        <button
          className="work-proactive-mode__trigger"
          title="Manage Proactive Mode"
          type="button"
        >
          <Zap aria-hidden="true" size={15} />
          <span>Proactive Mode</span>
          <span
            className="work-proactive-mode__state"
            data-enabled={stateLabel === "On"}
          >
            {stateLabel}
          </span>
        </button>
      </DialogTrigger>
      <DialogContent
        className="z-[221] flex w-[min(380px,_calc(100vw_-_32px))] max-w-[min(380px,_calc(100vw_-_32px))] max-h-[calc(100dvh_-_48px)] flex-col gap-[16px] overflow-y-auto [border:1px_solid_var(--workspace-divider,_#e5dfe8)] rounded-[var(--workspace-radius-overlay,_14px)] p-[24px] [background:var(--workspace-overlay-bg,_#fff)] text-[var(--workspace-native-text,_#2d2631)] [box-shadow:var(--workspace-overlay-shadow,_0_16px_48px_rgb(37_28_45_/_12%))] [font-family:var(--workspace-font-body,_sans-serif)]"
        overlayClassName="work-proactive-mode__overlay"
        showCloseButton={false}
      >
        <header className="flex shrink-0 items-center gap-[12px]">
          <DialogTitle className="min-w-0 flex-1 m-0 [font-family:var(--workspace-font-heading,_sans-serif)] text-workspace-title font-[700] leading-workspace-title">
            Proactive Mode
          </DialogTitle>
          <Switch
            aria-label="Proactive Mode"
            checked={enabled}
            disabled={disabled}
            onCheckedChange={onEnabledChange}
          />
          <DialogClose asChild>
            <button
              aria-label="Close Proactive Mode"
              className="work-proactive-mode__close"
              title="Close"
              type="button"
            >
              <X aria-hidden="true" size={18} />
            </button>
          </DialogClose>
        </header>
        <DialogDescription className="m-0 text-[var(--workspace-native-text,_#2d2631)] text-workspace-caption leading-workspace-caption">
          Keep routine CEO approvals, completed Task merges, and outcome reports
          moving without manual confirmation.
        </DialogDescription>
        <ul className="grid gap-[6px] m-0 pl-[18px] text-[var(--workspace-native-text-muted,_#706875)] text-workspace-label leading-workspace-label [list-style:disc]">
          <li>Auto-approve CEO chat</li>
          <li>Merge completed Tasks</li>
          <li>Send outcome reports</li>
        </ul>
        {status ? (
          <div
            className="work-proactive-mode__status"
            data-error={status.type === "error"}
            role={status.type === "error" ? "alert" : "status"}
          >
            <span>
              {status.type === "error"
                ? status.message
                : status.type === "loading"
                  ? "Loading setting..."
                  : status.type === "saving"
                    ? "Saving setting..."
                    : "Only Company owners can change this setting."}
            </span>
            {status.type === "error" ? (
              <button
                className="work-proactive-mode__retry"
                type="button"
                onClick={onRetry}
              >
                Retry
              </button>
            ) : null}
          </div>
        ) : null}
      </DialogContent>
    </Dialog>
  );
}
