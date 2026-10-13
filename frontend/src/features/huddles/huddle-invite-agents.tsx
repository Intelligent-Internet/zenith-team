import { useState } from "react";
import { Link } from "react-router";
import { useListCompanyAgentsQuery } from "@/shared/api/agent-runtime-api";
import { getBoardlyQueryErrorMessage } from "@/shared/api/boardly-api";
import { useCompanyPermissions } from "@/shared/permissions";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { Info, UserPlus, Wallet } from "@/shared/ui/icons";
import { useSetHuddleAIMutation } from "./huddle-ai-api";
import { HuddleBudgetSettings, useHeliaStatus } from "./huddle-ai-status";
import { HuddleAgentAvatar } from "./huddle-agent-avatar";

export function HuddleInviteAgents({
  companyId,
  huddleId,
  search,
}: {
  companyId: string;
  huddleId: string;
  search: string;
}) {
  const agents = useListCompanyAgentsQuery(companyId, {
    refetchOnMountOrArgChange: true,
    refetchOnFocus: true,
  });
  const { query, status } = useHeliaStatus(companyId, huddleId);
  const [invite, mutation] = useSetHuddleAIMutation();
  const { permissions } = useCompanyPermissions();
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [budgetOpen, setBudgetOpen] = useState(false);
  const [pendingId, setPendingId] = useState("");
  const candidates = (agents.currentData?.items ?? []).filter(
    (agent) =>
      !agent.archivedAt &&
      agent.voice?.enabled &&
      `${agent.name} ${agent.title}`
        .toLocaleLowerCase()
        .includes(search.trim().toLocaleLowerCase()),
  );
  const occupied = Boolean(status && !status.disabled);
  // Company context makes the inviter the Agent's Authorizer, so only
  // someone in the call can share it; anyone else invites without it.
  const canShareContext = Boolean(status?.canShareContext);
  const sharerLeft = status?.unavailableReason === "context_sharer_left";
  const currentAgentId = status?.agent?.id;

  async function inviteAgent(id: string, done: string) {
    if (!status || mutation.isLoading) return;
    setError("");
    setNotice("");
    setPendingId(id);
    try {
      await invite({
        companyId,
        huddleId,
        body: {
          enabled: true,
          allowCompanyContext: canShareContext,
          agentId: id,
          expectedRevision: status.revision,
          requestKey: crypto.randomUUID(),
        },
      }).unwrap();
      setNotice(done);
    } catch (cause) {
      setError(getBoardlyQueryErrorMessage(cause));
    } finally {
      await query.refetch();
      setPendingId("");
    }
  }

  return (
    <section className="huddle-invite-agents" aria-label="Voice Agents">
      <strong className="huddle-invite-group-title">Voice Agents</strong>
      <div className="huddle-invite-list">
        {agents.isLoading ? (
          <p role="status">Loading Agents…</p>
        ) : agents.isError || query.isError ? (
          <p role="alert">
            Could not load Voice Agents.{" "}
            <button
              type="button"
              onClick={() => {
                void agents.refetch();
                void query.refetch();
              }}
            >
              Retry
            </button>
          </p>
        ) : candidates.length === 0 ? (
          <p>
            {search.trim()
              ? "No matching Voice Agents."
              : "Enable Voice on an Agent to invite it."}
          </p>
        ) : (
          candidates.map((agent) => (
            <button
              key={agent.id}
              type="button"
              disabled={!status?.canModerate || occupied || Boolean(pendingId)}
              aria-label={`Invite ${agent.name}`}
              onClick={() => {
                if (!occupied)
                  void inviteAgent(agent.id, `Invited ${agent.name}`);
              }}
            >
              <span className="huddle-invite-agent-name">
                <HuddleAgentAvatar size={28} profile={agent} />
                {agent.name}
              </span>
              {pendingId === agent.id ? (
                <span>Inviting…</span>
              ) : occupied && status?.agent?.id === agent.id ? (
                <span>In call</span>
              ) : (
                <UserPlus size={15} />
              )}
            </button>
          ))
        )}
      </div>
      {sharerLeft && (
        <div className="huddle-invite-context" role="status">
          <p>Company context is off: the person who shared it left the call.</p>
          {canShareContext && currentAgentId && (
            <button
              type="button"
              disabled={Boolean(pendingId)}
              onClick={() =>
                void inviteAgent(currentAgentId, "Company context shared")
              }
            >
              {pendingId === currentAgentId
                ? "Sharing…"
                : "Share Company context"}
            </button>
          )}
        </div>
      )}
      {occupied && !sharerLeft && (
        <p className="huddle-invite-hint">
          One Voice Agent at a time. Remove the current Agent to invite another.
        </p>
      )}
      {status?.canModerate && !canShareContext && (
        <p className="huddle-invite-hint huddle-invite-context-note">
          <Info size={14} aria-hidden />
          Only someone in this call can share Company context.
        </p>
      )}
      {status && !status.canModerate && (
        <p className="huddle-invite-hint">
          Only the call starter or an admin can invite an Agent.
        </p>
      )}
      {error && (
        <p className="huddle-invite-error" role="alert">
          {error}
        </p>
      )}
      {notice && (
        <p className="huddle-invite-notice" role="status">
          {notice}
        </p>
      )}
      <div className="huddle-invite-agent-actions">
        <Link to="/agents" target="_blank" rel="noopener noreferrer">
          Configure Voice
        </Link>
        {permissions.includes("company.profile.update") && (
          <button type="button" onClick={() => setBudgetOpen(true)}>
            <Wallet size={14} /> AI budget
          </button>
        )}
      </div>
      <Dialog open={budgetOpen} onOpenChange={setBudgetOpen}>
        <DialogContent
          className="huddle-dialog huddle-ai-dialog"
          aria-describedby={undefined}
        >
          <DialogHeader>
            <DialogTitle>Company AI budget</DialogTitle>
          </DialogHeader>
          <HuddleBudgetSettings companyId={companyId} />
        </DialogContent>
      </Dialog>
    </section>
  );
}
