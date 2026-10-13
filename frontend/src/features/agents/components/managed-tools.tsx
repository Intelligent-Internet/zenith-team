import { managedToolObservation } from "@/features/settings/model/managed-tool-observation";
import { Video, Ellipsis, RefreshCw } from "@/shared/ui/icons";
import { useState } from "react";
import { agentBindingLabel } from "@/features/agents/model/agent-binding";
import { useLocalRuntime } from "../hooks/use-local-runtime";
import { useOperateManagedToolMutation } from "@/features/settings/api/managed-tools-api";
import type { AgentDto } from "@/shared/api/types";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/shared/ui/dropdown-menu";
import { toast } from "@/shared/ui/sonner";

export function ManagedTools({
  agent,
  editable,
  busy,
  onToggle,
}: {
  agent: AgentDto;
  editable: boolean;
  busy: boolean;
  onToggle: (id: string, enabled: boolean) => void;
}) {
  const runtime = useLocalRuntime(agent, editable);
  const { local, selected } = runtime;
  const [operate, operation] = useOperateManagedToolMutation();
  const [refreshing, setRefreshing] = useState(false);
  const renderer = selected?.tools?.find((item) => item.key === "hyperframes");
  const rendererEnabled =
    agent.capabilities.enabledTools?.includes("zenith-studio") ?? false;
  async function act(
    toolKey: string,
    action: "connect" | "repair" | "status" | "cancel",
  ) {
    const result = await operate({
      companyId: agent.companyId,
      runtimeId: agent.binding?.runtime.id ?? "",
      provider: agent.provider as "codex" | "claude",
      agentId: agent.id,
      toolKey,
      action,
    });
    if ("error" in result) {
      toast.error(
        result.error?.message ||
          "The tool operation could not start. Refresh its status and try again.",
      );
      return;
    }
    if (action === "connect") toast.success("Connect to Zenith Studio.");
    if (action === "repair") toast.success("Tool setup requested.");
    void runtime.refetch();
  }
  const toolName = "Zenith Studio";
  const description = "Create images, video, audio, and music.";
  const observation = managedToolObservation(
    selected?.tools,
    "zenith-studio",
    agent.id,
  );
  const enabled = rendererEnabled;
  const installed = observation?.state === "installed";
  const preparing =
    observation?.state === "installing" || observation?.state === "pending";
  const stale =
    selected?.status !== "online" ||
    !observation ||
    Date.now() - Date.parse(observation.observedAt) > 150_000;
  const signedIn = observation?.authState === "authenticated";
  const connecting = observation?.authState === "connecting";
  const setupLabel = !observation
    ? "Waiting"
    : preparing
      ? "Preparing"
      : installed
        ? "Installed"
        : "Setup needed";
  const setupNeedsRepair = observation?.state === "error";
  const videoNeedsRepair =
    renderer?.state === "error" ||
    renderer?.reasonCode === "prerequisites_missing";
  return (
    <article
      className="agent-capability-card managed-tool-card"
      data-enabled={enabled}
    >
      <span className="agent-capability-glyph" data-tone="accent">
        <Video size={20} />
      </span>
      <div className="agent-capability-copy">
        <h3>{toolName}</h3>
        <p title={description}>{description}</p>
      </div>
      <div className="managed-tool-actions">
        <button
          type="button"
          role="switch"
          className="agent-capability-toggle"
          aria-label={`Allow ${toolName} for this agent`}
          aria-checked={enabled}
          disabled={!editable || busy}
          title={
            editable
              ? "Save for new agent runs"
              : "Only this agent's creator, or a company owner or admin, can change capabilities"
          }
          onClick={() => onToggle("tool:zenith-studio", enabled)}
        >
          <span />
        </button>
        {/* Setting the tool up and signing in to it configure the Agent, so
            the menu is only for those who may; for anyone else its status
            is never read (useLocalRuntime above). */}
        <DropdownMenu>
          {/* The gate sits on the trigger: Radix opens the menu on
              pointerdown, which a disabled button still receives. */}
          <DropdownMenuTrigger asChild disabled={!editable}>
            <button
              type="button"
              className="managed-tool-action managed-tool-more"
              aria-label={`${toolName} options`}
            >
              <Ellipsis size={16} />
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent
            align="end"
            className="w-[250px] max-w-[calc(100vw_-_24px)]"
          >
            <div className="managed-tool-menu-header flex items-center justify-between gap-[12px]">
              <DropdownMenuLabel>
                {setupLabel} ·{" "}
                {observation ? `v${observation.version}` : "Checking version"}
              </DropdownMenuLabel>
              <button
                type="button"
                className={`managed-tool-action managed-tool-refresh${refreshing ? " is-refreshing" : ""}`}
                aria-label="Refresh status"
                title="Refresh status"
                aria-busy={refreshing}
                disabled={!local || operation.isLoading || refreshing}
                onClick={() => {
                  setRefreshing(true);
                  void Promise.all([
                    act("zenith-studio", "status"),
                    act("hyperframes", "status"),
                  ]).finally(() => setRefreshing(false));
                }}
              >
                <RefreshCw size={15} />
              </button>
            </div>
            {!local && (
              <p className="m-0 p-[4px_8px_8px] text-workspace-caption leading-workspace-caption text-workspace-muted wrap-anywhere">
                Open Zenith Team Desktop on {agentBindingLabel(agent)} to manage
                it.
              </p>
            )}
            {stale && observation && (
              <p className="m-0 p-[4px_8px_8px] text-workspace-caption leading-workspace-caption text-workspace-muted wrap-anywhere">
                Last updated by {agentBindingLabel(agent)}.
              </p>
            )}
            {observation?.state === "error" && (
              <p className="m-0 p-[4px_8px_8px] text-workspace-caption leading-workspace-caption text-workspace-muted wrap-anywhere">
                Check Node.js 22+, npm, and your network, then retry.
              </p>
            )}
            {installed && observation.authState === "error" && (
              <p className="m-0 p-[4px_8px_8px] text-workspace-caption leading-workspace-caption text-workspace-muted wrap-anywhere">
                Sign-in incomplete. Try again.
              </p>
            )}
            {renderer?.reasonCode === "prerequisites_missing" && (
              <p className="m-0 p-[4px_8px_8px] text-workspace-caption leading-workspace-caption text-workspace-muted wrap-anywhere">
                Video needs FFmpeg and ffprobe on this computer.
              </p>
            )}
            <DropdownMenuSeparator />
            <DropdownMenuItem
              disabled={
                !installed ||
                !editable ||
                !local ||
                operation.isLoading ||
                connecting
              }
              onSelect={() => void act("zenith-studio", "connect")}
            >
              {signedIn ? "Reconnect" : "Connect"}
            </DropdownMenuItem>
            {setupNeedsRepair && (
              <DropdownMenuItem
                disabled={
                  !editable || !local || operation.isLoading || connecting
                }
                onSelect={() => void act("zenith-studio", "repair")}
              >
                Fix setup
              </DropdownMenuItem>
            )}
            {videoNeedsRepair && (
              <DropdownMenuItem
                disabled={
                  !editable ||
                  !local ||
                  operation.isLoading ||
                  renderer?.state === "installing"
                }
                onSelect={() => void act("hyperframes", "repair")}
              >
                Fix video
              </DropdownMenuItem>
            )}
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </article>
  );
}
