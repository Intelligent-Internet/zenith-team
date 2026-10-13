import {
  AlertTriangle,
  DatabaseZap,
  RefreshCcw,
  ShieldAlert,
} from "@/shared/ui/icons";
import { Link } from "react-router";
import { useEffect, useRef } from "react";

import { shouldUseDesktopHashRouter } from "@/shared/desktop/desktop-environment.service";
import { ScheduleSourceCard } from "@/features/scheduled/components/schedule-source-card";
import {
  formatPreviewTrigger,
  formatScheduleDateTime,
} from "@/features/scheduled/model/schedule-format";
import type { ScheduleFormValues } from "@/features/scheduled/model/schedule-form";
import {
  DAILY_BRIEF_PRESET_KEY,
  type SchedulePreviewResponse,
} from "@/shared/api/schedule.service";
import type {
  ConnectorCatalogResponseV1,
  ConnectorItemV1,
  PluginCatalogResponseV1,
  PluginItemV1,
} from "@/shared/api/types";
import { cx } from "@/shared/lib/cx";
import type { LocalPluginIconContext } from "@/shared/lib/plugin-icons";

type ScheduleConnectorSource = {
  connector: ConnectorItemV1;
  plugin?: PluginItemV1;
};

export function ScheduleEditorSources({
  agentName,
  catalog,
  companyId,
  isError,
  isLoading,
  onChange,
  onRetry,
  onEditStep,
  pluginCatalog,
  preview,
  values,
}: {
  agentName: string;
  catalog?: ConnectorCatalogResponseV1;
  companyId: string;
  isError: boolean;
  isLoading: boolean;
  onChange: (next: ScheduleFormValues) => void;
  onRetry: () => void;
  onEditStep: (step: 1 | 2) => void;
  pluginCatalog?: PluginCatalogResponseV1;
  preview?: SchedulePreviewResponse;
  values: ScheduleFormValues;
}) {
  const executableConnectors = (catalog?.connectors ?? []).filter(
    (connector) =>
      connector.status === "connected" &&
      connector.effectiveEnabled &&
      connector.canExecute,
  );
  const presentationPlugins = presentationPluginsByConnectorId(
    pluginCatalog?.plugins ?? [],
    executableConnectors,
  );
  const connectorSources = executableConnectors.map((connector) => ({
    connector,
    plugin: presentationPlugins.get(connector.id),
  }));
  const iconContext = schedulePluginIconContext(companyId, pluginCatalog);
  const availableConnectorIds = new Set(
    executableConnectors.map((connector) => connector.id),
  );
  const selectedIds = new Set(values.sourceIds);
  const selectedMissingCount = values.sourceIds.filter(
    (sourceId) => !availableConnectorIds.has(sourceId),
  ).length;
  const toggleSource = (connectorId: string) => {
    const nextSelectedIds = new Set(values.sourceIds);
    if (nextSelectedIds.has(connectorId)) {
      nextSelectedIds.delete(connectorId);
    } else {
      nextSelectedIds.add(connectorId);
    }
    onChange({ ...values, sourceIds: [...nextSelectedIds] });
  };

  return (
    <div className="grid gap-6">
      <div>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <h2 className="text-sm font-semibold text-[var(--workspace-native-text)]">
              Connected sources
            </h2>
            <p className="mt-1 max-w-2xl text-xs leading-5 text-[var(--workspace-native-text-muted)]">
              Choose the connections this schedule can use. Only selected
              connections will be available during each run.
            </p>
          </div>
          {catalog ? (
            <span className="rounded-full border border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] px-3 py-1.5 text-xs font-bold capitalize text-[var(--workspace-native-text-muted)]">
              {catalog.activeProvider} · {connectorSources.length} connected
            </span>
          ) : null}
        </div>

        <SourceCatalogState
          iconContext={iconContext}
          isError={isError}
          isLoading={isLoading}
          onRetry={onRetry}
          connectorSources={connectorSources}
          selectedIds={selectedIds}
          requiresSource={values.presetKey === DAILY_BRIEF_PRESET_KEY}
          toggleSource={toggleSource}
        />

        {selectedMissingCount > 0 ? (
          <div
            className="mt-3 flex flex-wrap items-center justify-between gap-3 rounded-md border border-[var(--workspace-divider-strong)] bg-[var(--workspace-approval-soft)] p-3 text-xs leading-5 text-[var(--workspace-native-approval)]"
            role="alert"
          >
            <p className="flex min-w-0 items-start gap-2">
              <AlertTriangle aria-hidden className="mt-0.5 size-4 shrink-0" />
              <span>
                {selectedMissingCount} previously selected source
                {selectedMissingCount === 1 ? " is" : "s are"} no longer
                executable for this agent and provider.
              </span>
            </p>
            <button
              className="shrink-0 rounded-lg border border-[var(--workspace-divider-strong)] bg-[var(--workspace-panel-bg)] px-2.5 py-1.5 font-bold text-[var(--workspace-native-approval)] transition hover:bg-[var(--workspace-approval-soft)]"
              type="button"
              onClick={() =>
                onChange({
                  ...values,
                  sourceIds: values.sourceIds.filter((sourceId) =>
                    availableConnectorIds.has(sourceId),
                  ),
                })
              }
            >
              Remove unavailable
            </button>
          </div>
        ) : null}
      </div>

      {values.sourceIds.length > 0 ? (
        <div className="flex items-start gap-3 rounded-lg border border-[var(--workspace-divider-strong)] bg-[var(--workspace-approval-soft)] p-4">
          <ShieldAlert
            aria-hidden
            className="mt-0.5 size-5 shrink-0 text-[var(--workspace-native-approval)]"
          />
          <div>
            <h3 className="text-sm font-semibold text-[var(--workspace-native-text)]">
              Review unattended access
            </h3>
            <p className="mt-1 text-xs leading-5 text-[var(--workspace-native-text-muted)]">
              Each selected Connector exposes its reviewed tool contract during
              the run, including tools that may write data. Keep the
              instructions narrow and grant only the Connectors this schedule
              actually needs.
            </p>
          </div>
        </div>
      ) : null}

      {preview ? (
        <SchedulePreviewPanel
          agentName={agentName}
          onEditStep={onEditStep}
          preview={preview}
          values={values}
        />
      ) : null}
    </div>
  );
}

function SourceCatalogState({
  iconContext,
  isError,
  isLoading,
  onRetry,
  connectorSources,
  selectedIds,
  requiresSource,
  toggleSource,
}: {
  iconContext?: LocalPluginIconContext;
  isError: boolean;
  isLoading: boolean;
  onRetry: () => void;
  connectorSources: ScheduleConnectorSource[];
  selectedIds: Set<string>;
  requiresSource: boolean;
  toggleSource: (connectorId: string) => void;
}) {
  if (isLoading) {
    return (
      <div className="mt-4 grid gap-3 sm:grid-cols-2">
        {[0, 1, 2, 3].map((index) => (
          <div
            className="h-20 animate-pulse rounded-lg border border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)]"
            key={index}
          />
        ))}
      </div>
    );
  }

  if (isError) {
    return (
      <div className="mt-4 flex flex-wrap items-center justify-between gap-4 rounded-lg border border-[var(--workspace-divider)] bg-[var(--workspace-danger-soft)] p-4">
        <p className="text-xs leading-5 text-[var(--workspace-native-danger)]">
          Connected sources could not be loaded.
        </p>
        <button
          className="inline-flex items-center gap-2 rounded-md border border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] px-3 py-2 text-xs font-bold text-[var(--workspace-native-text)]"
          type="button"
          onClick={onRetry}
        >
          <RefreshCcw aria-hidden className="size-3.5" />
          Retry
        </button>
      </div>
    );
  }

  if (connectorSources.length === 0) {
    return (
      <div className="mt-4 flex items-start gap-3 rounded-lg border border-dashed border-[var(--workspace-divider)] bg-[var(--workspace-control-bg)] p-4">
        <DatabaseZap
          aria-hidden
          className="mt-0.5 size-5 shrink-0 text-[var(--workspace-native-text-muted)]"
        />
        <div>
          <h3 className="text-sm font-semibold text-[var(--workspace-native-text)]">
            No executable Connectors
          </h3>
          <p className="mt-1 text-xs leading-5 text-[var(--workspace-native-text-muted)]">
            {requiresSource
              ? "Daily Brief needs at least one connected source. Connect a source in Plugins settings or its native provider, then refresh this list."
              : "Connect a source in Plugins settings or its native provider, or continue without external data."}
          </p>
          <Link
            className="mt-2 inline-flex text-xs font-bold text-[var(--workspace-native-accent)] hover:text-[var(--workspace-accent-hover)]"
            to="/settings/plugins"
          >
            Open Plugins settings
          </Link>
        </div>
      </div>
    );
  }

  return (
    <div className="mt-4 grid gap-3 sm:grid-cols-2">
      {connectorSources.map(({ connector, plugin }) => {
        const selected = selectedIds.has(connector.id);
        return (
          <ScheduleSourceCard
            connector={connector}
            iconContext={iconContext}
            key={connector.id}
            plugin={plugin}
            selected={selected}
            onToggle={() => toggleSource(connector.id)}
          />
        );
      })}
    </div>
  );
}

function presentationPluginsByConnectorId(
  plugins: PluginItemV1[],
  connectors: ConnectorItemV1[],
) {
  const connectorsByIdentity = new Map(
    connectors.map((connector) => [
      connectorIdentityKey(connector.source, connector.nativeId),
      connector,
    ]),
  );
  const pluginsByConnectorId = new Map<string, PluginItemV1>();
  for (const plugin of plugins) {
    if (!plugin.installed || !plugin.enabled) continue;
    for (const identity of plugin.connectorIdentities ?? []) {
      const connector = connectorsByIdentity.get(
        connectorIdentityKey(identity.source, identity.nativeId),
      );
      if (connector && !pluginsByConnectorId.has(connector.id)) {
        pluginsByConnectorId.set(connector.id, plugin);
      }
    }
  }
  return pluginsByConnectorId;
}

function connectorIdentityKey(source: string, nativeId: string) {
  return `${source}\u0000${nativeId}`;
}

function schedulePluginIconContext(
  companyId: string,
  catalog: PluginCatalogResponseV1 | undefined,
) {
  if (
    !shouldUseDesktopHashRouter() ||
    catalog?.runtime?.online !== true ||
    !catalog.runtime.id
  ) {
    return undefined;
  }
  return {
    companyId,
    runtimeId: catalog.runtime.id,
  } satisfies LocalPluginIconContext;
}

export function SchedulePreviewPanel({
  heading = "Ready to save",
  agentName,
  onEditStep,
  preview,
  values,
}: {
  heading?: string;
  agentName: string;
  onEditStep: (step: 1 | 2) => void;
  preview: SchedulePreviewResponse;
  values: ScheduleFormValues;
}) {
  const panelRef = useRef<HTMLElement>(null);
  useEffect(() => {
    panelRef.current?.scrollIntoView({ block: "start", behavior: "instant" });
  }, [preview]);
  const timeZone =
    preview.normalizedTrigger.type === "cron"
      ? preview.normalizedTrigger.timeZone
      : undefined;

  return (
    <section
      ref={panelRef}
      className="scroll-mt-5 rounded-lg border border-[var(--workspace-native-accent)] bg-[var(--workspace-accent-soft)] p-5"
    >
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <p className="text-xs font-bold uppercase tracking-[0.16em] text-[var(--workspace-native-accent)]">
            {heading}
          </p>
          <h2 className="mt-1 text-base font-semibold text-[var(--workspace-native-text)]">
            {formatPreviewTrigger(preview)}
          </h2>
        </div>
        <span className="rounded-full bg-[var(--workspace-accent-soft)] px-3 py-1 text-xs font-bold capitalize text-[var(--workspace-native-accent)]">
          {preview.provider} · {preview.sources.length} source
          {preview.sources.length === 1 ? "" : "s"}
        </span>
      </div>
      <div className="mt-4 grid gap-4 border-y border-[var(--workspace-divider)] py-4 text-sm">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <h3 className="break-words font-semibold text-[var(--workspace-native-text)]">
              {values.name}
            </h3>
            <p className="mt-1 text-[var(--workspace-native-text-muted)]">
              Agent: {agentName} · {preview.provider}
            </p>
          </div>
          <button
            className="shrink-0 text-[var(--workspace-native-accent)] hover:underline"
            type="button"
            onClick={() => onEditStep(1)}
          >
            Edit schedule
          </button>
        </div>
        <div>
          <h3 className="font-semibold text-[var(--workspace-native-text)]">
            Instructions
          </h3>
          <p className="mt-1 whitespace-pre-wrap break-words leading-6 text-[var(--workspace-native-text-muted)]">
            {values.instructions}
          </p>
        </div>
        <div>
          <h3 className="font-semibold text-[var(--workspace-native-text)]">
            Allowed connections
          </h3>
          {preview.sources.length ? (
            <ul className="mt-2 grid gap-2">
              {preview.sources.map((source) => (
                <li
                  key={source.id}
                  className="rounded-md border border-[var(--workspace-divider)] bg-[var(--workspace-panel-bg)] px-3 py-2"
                >
                  <strong className="font-medium">{source.name}</strong>
                  <span className="ml-2 text-[var(--workspace-native-text-muted)]">
                    {source.provider} · {source.source}
                  </span>
                </li>
              ))}
            </ul>
          ) : (
            <p className="mt-1 text-[var(--workspace-native-text-muted)]">
              No external connections.
            </p>
          )}
          {preview.sources.length ? (
            <p className="mt-2 text-xs leading-5 text-[var(--workspace-native-text-muted)]">
              Each connection grants its available tools, which may read or
              change data. Individual tool permissions are managed in the
              provider and are not listed here.
            </p>
          ) : null}
        </div>
        <div className="flex items-center justify-between gap-3">
          <p className="text-[var(--workspace-native-text-muted)]">
            Time zone:{" "}
            {timeZone ?? Intl.DateTimeFormat().resolvedOptions().timeZone}
          </p>
          <button
            className="shrink-0 text-[var(--workspace-native-accent)] hover:underline"
            type="button"
            onClick={() => onEditStep(2)}
          >
            Edit trigger
          </button>
        </div>
      </div>
      <p className="mt-4 text-xs leading-5 text-[var(--workspace-native-text-muted)]">
        Each run starts fresh and can use only these reviewed connections. It
        cannot use local files, Company Memory or Wiki, create Tasks, or ask
        interactive questions.
      </p>
      <div className="mt-4 grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
        {preview.nextOccurrences.map((occurrence, index) => (
          <div
            className={cx(
              "rounded-md border border-[var(--workspace-divider)] bg-[var(--workspace-panel-bg)] px-3 py-2",
              index === 0 &&
                "border-[var(--workspace-native-accent)] bg-[var(--workspace-accent-soft)]",
            )}
            key={occurrence}
          >
            <span className="block text-xs font-bold uppercase tracking-[0.12em] text-[var(--workspace-native-text-muted)]">
              {index === 0 ? "Next run" : `Occurrence ${index + 1}`}
            </span>
            <strong className="mt-1 block text-xs font-bold text-[var(--workspace-native-text-muted)]">
              {formatScheduleDateTime(occurrence, timeZone)}
            </strong>
            {preview.occurrenceDetails?.[index] ? (
              <span className="mt-1 block text-xs leading-5 text-[var(--workspace-native-text-muted)]">
                {formatUTCOffset(
                  preview.normalizedTrigger.type === "once"
                    ? -new Date(occurrence).getTimezoneOffset() * 60
                    : preview.occurrenceDetails[index].utcOffsetSeconds,
                )}
                {preview.occurrenceDetails[index].repeatedLocalTime
                  ? " · Repeated local time"
                  : ""}
                {preview.occurrenceDetails[index].offsetChanged
                  ? " · Clock offset changes"
                  : ""}
                {preview.occurrenceDetails[index].skippedLocalTime
                  ? ` · Skipped ${preview.occurrenceDetails[index].skippedLocalTime}`
                  : ""}
              </span>
            ) : null}
          </div>
        ))}
      </div>
    </section>
  );
}

function formatUTCOffset(seconds: number) {
  const minutes = Math.abs(seconds) / 60;
  return `UTC${seconds < 0 ? "−" : "+"}${String(Math.floor(minutes / 60)).padStart(2, "0")}:${String(minutes % 60).padStart(2, "0")}`;
}
