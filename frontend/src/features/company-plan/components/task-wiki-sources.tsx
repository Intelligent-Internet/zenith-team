import { RunWikiSources } from "@/features/company-wiki/agent-reads/run-wiki-sources";
import { useGetTaskLatestRunQuery } from "../api/task-detail-api";

/**
 * The Wiki pages the Task's latest Run read. Shares the latest-run query with
 * the latest update above it, and renders nothing until a Run exists.
 */
export function TaskWikiSources({
  companyId,
  taskId,
}: {
  companyId: string;
  taskId: string;
}) {
  const run = useGetTaskLatestRunQuery({ companyId, taskId }).currentData;
  if (!run) return null;
  return (
    <RunWikiSources
      companyId={companyId}
      runId={run.id}
      agentId={run.agentId}
      runStatus={run.status}
    />
  );
}
