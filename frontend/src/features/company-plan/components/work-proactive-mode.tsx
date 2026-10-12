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
        className="work-proactive-mode__dialog"
        overlayClassName="work-proactive-mode__overlay"
        showCloseButton={false}
      >
        <header className="work-proactive-mode__header">
          <DialogTitle className="work-proactive-mode__title">
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
        <DialogDescription className="work-proactive-mode__description">
          Keep routine CEO approvals, completed Task merges, and outcome reports
          moving without manual confirmation.
        </DialogDescription>
        <ul className="work-proactive-mode__features">
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
