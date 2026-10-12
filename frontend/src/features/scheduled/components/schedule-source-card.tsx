import { Check } from "@/shared/ui/icons";

import { ScheduledGlassPanel } from "@/features/scheduled/components/scheduled-glass-panel";
import type { ConnectorItemV1, PluginItemV1 } from "@/shared/api/types";
import { cx } from "@/shared/lib/cx";
import {
  type LocalPluginIconContext,
  safePluginIconUrl,
} from "@/shared/lib/plugin-icons";
import { AppAvatar } from "@/shared/ui/app-avatar";

export function ScheduleSourceCard({
  connector,
  iconContext,
  plugin,
  onToggle,
  selected,
}: {
  connector: ConnectorItemV1;
  iconContext?: LocalPluginIconContext;
  plugin?: PluginItemV1;
  onToggle: () => void;
  selected: boolean;
}) {
  return (
    <ScheduledGlassPanel
      density={selected ? "dense" : "regular"}
      className="min-h-20"
      selectionState={selected ? "selected" : "unselected"}
      contentClassName={cx(
        "p-0 transition",
        selected && "bg-[var(--workspace-accent-soft)]",
      )}
    >
      <button
        aria-pressed={selected}
        className="group flex min-h-20 w-full items-center gap-3 rounded-lg p-3 text-left transition hover:bg-[var(--workspace-hover-bg)]"
        type="button"
        onClick={onToggle}
      >
        {/* The connector's own logo first, then the mapped plugin's. */}
        <AppAvatar
          iconUrl={safePluginIconUrl(connector.iconUrl) ?? plugin?.iconUrl}
          local={
            plugin && {
              provider: plugin.provider,
              nativeId: plugin.nativeId,
              context: iconContext,
            }
          }
          name={connector.name}
          size={40}
        />
        <span className="min-w-0 flex-1">
          <strong className="block truncate text-sm font-bold text-[var(--workspace-native-text)]">
            {connector.name}
          </strong>
          <span className="mt-1 block truncate text-xs capitalize text-[var(--workspace-native-text-muted)]">
            {connector.provider} · {connector.source.replaceAll("_", " ")}
            {plugin ? ` · ${plugin.name}` : ""}
          </span>
        </span>
        <span
          className={cx(
            "grid size-6 shrink-0 place-items-center rounded-full border transition",
            selected
              ? "border-[var(--workspace-native-accent)] bg-[var(--workspace-accent-fill)] text-[var(--workspace-accent-foreground)]"
              : "border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] text-transparent",
          )}
        >
          <Check aria-hidden className="size-3.5" />
        </span>
      </button>
    </ScheduledGlassPanel>
  );
}
