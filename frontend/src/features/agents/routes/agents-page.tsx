import {
  MessageSquareText,
  Mic,
  Monitor,
  Copy,
  MoreHorizontal,
  LayoutGrid,
  ListTodo,
  FileText,
  Box,
  Plus,
  Settings2,
} from "@/shared/ui/icons";
import { useEffect, useRef, useState } from "react";
import { Link, useParams, useSearchParams } from "react-router";
import { readSessionSnapshot } from "@/shared/session/session";
import { useSessionSnapshot } from "@/shared/session/use-session-snapshot";
import { useListCompanyAgentsQuery } from "@/shared/api/agent-runtime-api";
import {
  WorkspaceAccessState,
  WorkspacePageFrame,
} from "@/features/common/components/workspace-page-frame";
import { AgentsDirectory } from "@/features/agents/components/agents-directory";
import { AgentProfileContent } from "@/features/agents/components/agent-profile-content";
import { SkillDetailPage } from "./skill-detail-page";
import { SkillDraftRecovery } from "../components/skill-draft-recovery";
import { EditableAgentName } from "@/features/agents/components/editable-agent-name";
import { NewAgentDialog } from "@/features/agents/components/new-agent-dialog";
import type { AgentDto } from "@/shared/api/types";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from "@/shared/ui/dropdown-menu";
import { useVoiceAgentsEnabled } from "@/features/huddles/huddle-ai-api";
import "@/features/agents/styles/agents.css";

export function AgentsPage() {
  const session = useSessionSnapshot();
  if (!session.company?.id)
    return (
      <WorkspaceAccessState
        title="Choose a company"
        description="Agents belong to a company. Pick one to see and create agents."
      />
    );
  return (
    <CompanyAgents key={session.company.id} companyId={session.company.id} />
  );
}

function CompanyAgents({ companyId }: { companyId: string }) {
  const tabsRef = useRef<HTMLElement>(null);
  const { agentId, skillId } = useParams();
  const [params, setParams] = useSearchParams();
  // One dialog serves both entry points: New agent from the roster, and
  // Duplicate from a profile, which seeds it with that agent.
  const [creation, setCreation] = useState<{ template: AgentDto | null }>();
  const query = useListCompanyAgentsQuery(companyId);
  const agents =
    query.currentData?.items.filter((agent) => !agent.archivedAt) ?? [];
  const selected = agents.find((agent) => agent.id === agentId);
  const voiceAgents = useVoiceAgentsEnabled(companyId);
  const tabs = (
    [
      "overview",
      "work",
      "instructions",
      "capabilities",
      "voice",
      "settings",
    ] as const
  ).filter((tab) => tab !== "voice" || voiceAgents);
  const tab = tabs.find((tab) => tab === params.get("tab")) ?? "overview";
  useEffect(() => {
    tabsRef.current
      ?.closest(".workspace-page-frame__content")
      ?.scrollTo({ top: 0 });
  }, [agentId, tab]);
  const dialog = creation ? (
    <NewAgentDialog
      companyId={companyId}
      template={creation.template}
      onClose={() => setCreation(undefined)}
    />
  ) : null;
  if (!query.currentData)
    return (
      <WorkspaceAccessState
        title={query.isError ? "Agents unavailable" : "Loading agents…"}
        description={
          query.isError
            ? "Check your connection and try again."
            : "Loading your company’s agents."
        }
        action={
          query.isError ? (
            <button onClick={() => void query.refetch()}>Retry</button>
          ) : undefined
        }
      />
    );
  if (agentId && !selected)
    return (
      <WorkspaceAccessState
        title="Agent unavailable"
        description="This agent may have been archived or removed."
        action={
          <>
            <Link to="/agents">All agents</Link>
            {skillId && (
              <SkillDraftRecovery
                draftKey={`${companyId}:${agentId}:${skillId}`}
              />
            )}
          </>
        }
      />
    );
  if (selected && skillId)
    return (
      <SkillDetailPage
        key={`${companyId}:${selected.id}:${skillId}`}
        agent={selected}
        skillId={skillId}
      />
    );
  if (selected)
    return (
      <WorkspacePageFrame
        ariaLabel="Agent profile"
        title={
          <>
            <FoldAvatar agentId={selected.id} size={72} paused />
            <span>
              <span className="agent-profile-name">
                <EditableAgentName agent={selected} />
                {/* The viewer's own count: Runs on their computer (ADR 0014).
                    Recent runs lists everyone's, so the label says whose. */}
                <span
                  className="agents-directory-activity"
                  data-active={selected.activeRunCount > 0}
                  title={`${selected.activeRunCount} of ${selected.maxConcurrentRuns} task slots in use on your computer`}
                >
                  {selected.activeRunCount > 0
                    ? `${selected.activeRunCount} task run${selected.activeRunCount === 1 ? "" : "s"} on your computer`
                    : "No active task runs on your computer"}
                </span>
              </span>
              <small>
                {selected.title} ·{" "}
                {readSessionSnapshot().company?.workspaceName}
              </small>
            </span>
          </>
        }
        className="agent-profile-page"
        actions={
          <>
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <button
                  className="dashboard-button"
                  type="button"
                  aria-label="Agent actions"
                >
                  <MoreHorizontal size={16} />
                </button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem
                  onSelect={() => setCreation({ template: selected })}
                >
                  <Copy size={15} /> Duplicate
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
            <Link
              className="dashboard-button dashboard-button-primary"
              title="Start a private chat"
              to={`/app?conversationKind=direct&agentId=${encodeURIComponent(selected.id)}&newSession=1`}
            >
              <MessageSquareText size={16} />
              Message {selected.name}
            </Link>
          </>
        }
      >
        {dialog}
        <nav
          ref={tabsRef}
          className="agent-profile-tabs"
          role="tablist"
          aria-label="Agent profile"
        >
          {tabs.map((value) => {
            const TabIcon = {
              overview: LayoutGrid,
              work: ListTodo,
              instructions: FileText,
              capabilities: Box,
              voice: Mic,
              settings: Settings2,
            }[value];
            return (
              <button
                key={value}
                type="button"
                role="tab"
                id={`agent-${selected.id}-${value}-tab`}
                aria-controls={`agent-${selected.id}-panel`}
                aria-selected={value === tab}
                tabIndex={value === tab ? 0 : -1}
                aria-current={value === tab ? "page" : undefined}
                onKeyDown={(event) => {
                  if (
                    !["ArrowLeft", "ArrowRight", "Home", "End"].includes(
                      event.key,
                    )
                  )
                    return;
                  event.preventDefault();
                  const index = tabs.indexOf(value);
                  const next =
                    event.key === "Home"
                      ? tabs[0]
                      : event.key === "End"
                        ? tabs[tabs.length - 1]
                        : tabs[
                            (index +
                              (event.key === "ArrowRight"
                                ? 1
                                : tabs.length - 1)) %
                              tabs.length
                          ];
                  setParams({ tab: next });
                  tabsRef.current
                    ?.querySelector<HTMLButtonElement>(
                      `#agent-${selected.id}-${next}-tab`,
                    )
                    ?.focus();
                }}
                onClick={() => setParams({ tab: value })}
              >
                <TabIcon size={14} />
                {value[0].toUpperCase() + value.slice(1)}
              </button>
            );
          })}
        </nav>
        <div
          role="tabpanel"
          id={`agent-${selected.id}-panel`}
          aria-labelledby={`agent-${selected.id}-${tab}-tab`}
        >
          <AgentProfileContent
            key={`${companyId}:${selected.id}:${tab}`}
            agent={selected}
            tab={tab}
          />
        </div>
      </WorkspacePageFrame>
    );
  return (
    <WorkspacePageFrame
      ariaLabel="Company agents"
      tourId="agents-overview"
      title="Agents"
      className="agents-directory-page collection-page"
      contentWidth="contained"
      actions={
        <>
          <Link
            className="dashboard-button agents-directory-computers-link"
            to="/settings/agents"
          >
            <Monitor size={15} />
            Computers
          </Link>
          <button
            className="dashboard-button dashboard-button-primary"
            type="button"
            onClick={() => setCreation({ template: null })}
          >
            <Plus size={16} />
            New agent
          </button>
        </>
      }
    >
      {dialog}
      {query.isError ? (
        <p role="status">
          The latest update is unavailable.{" "}
          <button onClick={() => void query.refetch()}>Retry</button>
        </p>
      ) : null}
      <AgentsDirectory
        agents={agents}
        companyId={companyId}
        onCreateAgent={() => setCreation({ template: null })}
      />
    </WorkspacePageFrame>
  );
}
