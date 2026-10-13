import { RefreshCw, Settings2 } from "@/shared/ui/icons";
import { useNavigate } from "react-router";

import { useListAgentRuntimesQuery } from "@/shared/api/agent-runtime-api";
import { runtimeDisplayName } from "@/features/agent-setup/model/ceo-agent-setup";

import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/shared/ui/core/dropdown-menu";
import "@/app/shell/workspace-runner-status.css";

export function WorkspaceRunnerStatus({ companyId }: { companyId: string }) {
  const navigate = useNavigate();
  // The roster contains only the viewer's own connections (ADR 0014).
  const query = useListAgentRuntimesQuery(companyId, {
    refetchOnFocus: true,
    refetchOnReconnect: true,
    refetchOnMountOrArgChange: 15,
    skip: !companyId,
  });
  const runtimes = query.currentData?.items;
  const onlineCount =
    runtimes?.filter((runtime) => runtime.status === "online").length ?? 0;
  const tone =
    query.isError || !companyId
      ? "unavailable"
      : !runtimes
        ? "checking"
        : onlineCount > 0
          ? "online"
          : "offline";
  const label =
    tone === "unavailable"
      ? "Runner unavailable"
      : tone === "checking"
        ? "Checking runner"
        : onlineCount > 0
          ? onlineCount === 1
            ? "Runner online"
            : `${onlineCount} runners online`
          : runtimes?.length
            ? "Runner offline"
            : "No runner";

  return (
    <DropdownMenu
      onOpenChange={(open) => {
        if (open && companyId && !query.isFetching) void query.refetch();
      }}
    >
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          className="workspace-runner-status"
          data-tone={tone}
          aria-label={`${label}. Open runner status`}
        >
          <span aria-hidden="true" className="workspace-runner-dot" />
          <span aria-live="polite">{label}</span>
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent
        align="end"
        className="w-[min(380px,_calc(100vw_-_32px))] [border:1px_solid_var(--workspace-divider)] [background:var(--workspace-overlay-bg)] text-workspace-text font-workspace-body [box-shadow:var(--workspace-overlay-shadow)]"
        sideOffset={8}
      >
        <div className="workspace-runner-menu__heading">
          <strong>Runner status</strong>
          <p>
            {query.isError
              ? "We could not check your runners. The last known connections are shown below."
              : onlineCount > 0
                ? "New work can run when its assigned runner is online."
                : "Work waits until its assigned runner reconnects. Open Zenith Team on that computer and check its connection."}
          </p>
          <p role="status">
            {query.isFetching
              ? "Checking connections…"
              : query.fulfilledTimeStamp
                ? `Last checked ${new Date(query.fulfilledTimeStamp).toLocaleTimeString()}`
                : "Connections have not been checked yet."}
          </p>
        </div>
        {runtimes?.length ? (
          <ul className="workspace-runner-menu__list m-[12px_0] p-0 [list-style:none]">
            {runtimes.map((runtime) => (
              <li key={runtime.id}>
                <div>
                  <strong>{runtimeDisplayName(runtime)}</strong>
                  {/* Every computer here is the viewer's own (ADR 0014). */}
                  <span>
                    {runtime.provider} ·{" "}
                    {runtime.deviceInfo?.trim() || "Your computer"}
                  </span>
                </div>
                <span
                  data-online={!query.isError && runtime.status === "online"}
                >
                  {query.isError
                    ? `Last seen ${runtime.status}`
                    : runtime.status}
                </span>
              </li>
            ))}
          </ul>
        ) : !query.isFetching ? (
          <p className="workspace-runner-menu__empty">
            {query.isError
              ? "No saved connection details."
              : "No runners are registered for this Company."}
          </p>
        ) : null}
        <DropdownMenuItem
          disabled={!companyId || query.isFetching}
          onSelect={(event) => {
            event.preventDefault();
            void query.refetch();
          }}
        >
          <RefreshCw aria-hidden size={16} />
          Refresh connections
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={() => navigate("/settings/agents")}>
          <Settings2 aria-hidden size={16} />
          Open Computers
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
