import { Link } from "react-router";
import { useListCompanyAgentsQuery } from "@/shared/api/agent-runtime-api";
import { useGetRunDetailsQuery } from "../api/run-deliverables-api";
import {
  runCancellationCopy,
  runFailureCopy,
} from "@/shared/agent-execution/run-access-reasons";

export function RunContext({
  companyId,
  runId,
}: {
  companyId: string;
  runId: string;
}) {
  const query = useGetRunDetailsQuery(
    { companyId, runId },
    { skip: !companyId || !runId },
  );
  const agents = useListCompanyAgentsQuery(companyId, { skip: !companyId });
  const run = query.isError ? undefined : query.currentData;
  const agent = agents.currentData?.items.find(
    (agent) => agent.id === run?.agentId,
  );
  if (!run)
    return (
      <p role="status">
        {query.isError ? (
          <>
            Run details could not be loaded.{" "}
            <button type="button" onClick={() => void query.refetch()}>
              Retry
            </button>
          </>
        ) : (
          "Loading run details…"
        )}
      </p>
    );
  const conversation =
    run.runType === "task"
      ? `/app?conversationKind=task&taskId=${run.taskId}&conversationId=${run.conversationId}&agentId=${run.agentId}`
      : run.runType === "schedule"
        ? "/scheduled"
        : `/app?conversationKind=direct&conversationId=${run.conversationId}&agentId=${run.agentId}`;
  return (
    <section className="run-context" aria-label="Run details">
      <nav aria-label="Run context">
        <Link to="/agents">Agents</Link>
        <span>/</span>
        <Link to={`/agents/${run.agentId}?tab=work`}>
          {agent?.name ?? "Agent work"}
        </Link>
        {run.taskId ? (
          <Link to={`/tasks?taskId=${run.taskId}&taskView=full`}>
            View task
          </Link>
        ) : null}
        <Link to={conversation}>
          {run.runType === "schedule" ? "Schedules" : "Open conversation"}
        </Link>
      </nav>
      <h1>{run.source?.title ?? "Run details"}</h1>
      <p>
        {run.status === "completed"
          ? "Finished"
          : run.status.replaceAll("_", " ")}{" "}
        · {run.provider === "codex" ? "Codex" : "Claude Code"} · Attempt{" "}
        {run.attempt} · {new Date(run.createdAt).toLocaleString()}
      </p>
      {runFailureCopy(run.failureReason, run.failureMessage) ? (
        <p role="status">
          {runFailureCopy(run.failureReason, run.failureMessage)}
          {run.failureDetail ? ` ${run.failureDetail}` : ""}
        </p>
      ) : runCancellationCopy(run) ? (
        <p role="status">{runCancellationCopy(run)}</p>
      ) : null}
    </section>
  );
}
