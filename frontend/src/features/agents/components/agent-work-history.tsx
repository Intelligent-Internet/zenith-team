import { count, duration, sum } from "@/features/analytics/model/format";
import { Check, ChevronRight, History, Terminal } from "@/shared/ui/icons";
import { useState } from "react";
import { Link } from "react-router";
import { useListAgentRecentRunsInfiniteQuery } from "@/features/agent-setup/api/ceo-agent-api";
import { AgentRuntimeSelect } from "@/features/agent-setup/components/agent-runtime-controls";
import type { AgentDto, TaskDto, RunDto } from "@/shared/api/types";

export function AgentWorkHistory({
  agent,
  title = "Run history",
  compact = false,
  historyHref,
}: {
  agent: AgentDto;
  tasks: TaskDto[];
  title?: string;
  compact?: boolean;
  historyHref?: string;
}) {
  const [runType, setRunType] = useState<RunDto["runType"] | "all">("all");
  const query = useListAgentRecentRunsInfiniteQuery({
    companyId: agent.companyId,
    agentId: agent.id,
    ...(runType === "all" ? {} : { runType }),
  });
  const runs = query.isError
    ? []
    : (query.currentData?.pages.flatMap((page) => page.items) ?? []);

  if (historyHref && query.currentData && !query.isError && !runs.length) {
    return null;
  }

  return (
    <section className="agent-history" data-compact={compact || undefined}>
      <div className="agent-section-heading">
        <h2>
          {!compact ? <History size={15} /> : null}
          {title}
        </h2>
        {historyHref ? (
          <Link to={historyHref}>
            View history <ChevronRight aria-hidden size={13} />
          </Link>
        ) : (
          <AgentRuntimeSelect
            aria-label="Filter run history by type"
            value={runType}
            onChange={(event) => {
              const value = event.target.value;
              if (
                value === "all" ||
                value === "task" ||
                value === "direct_chat" ||
                value === "schedule"
              )
                setRunType(value);
            }}
          >
            <option value="all">All runs</option>
            <option value="task">Task</option>
            <option value="direct_chat">Direct chat</option>
            <option value="schedule">Schedule</option>
          </AgentRuntimeSelect>
        )}
      </div>
      {query.isError && !runs.length ? (
        <p role="status" className="agent-inline-empty">
          Recent runs could not be loaded.{" "}
          <button type="button" onClick={() => void query.refetch()}>
            Retry
          </button>
        </p>
      ) : !query.currentData ? (
        <p role="status" className="agent-inline-empty">
          Loading runs…
        </p>
      ) : runs.length ? (
        <div className="agent-history-list">
          {(historyHref ? runs.slice(0, 3) : runs).map((run) => (
            <Link key={run.id} to={`/runs/${run.id}`}>
              {!compact ? (
                <span className="agent-history-id">
                  <Terminal size={15} />
                  <span title={run.id}>{run.id.slice(0, 8)}</span>
                </span>
              ) : null}
              <span className="agent-history-copy">
                <strong>
                  {run.source?.title ?? "Source title unavailable"}
                </strong>
                <small>
                  {!compact ? (
                    <>
                      {run.runType === "task"
                        ? "Task"
                        : run.runType === "schedule"
                          ? "Schedule"
                          : "Direct chat"}{" "}
                      ·{" "}
                      {run.revision === null
                        ? "Revision unavailable"
                        : `Revision ${run.revision}`}{" "}
                      · Attempt {run.attempt} · {duration(run.durationMs)} ·{" "}
                      {run.usageRestricted
                        ? "Usage restricted"
                        : run.usage
                          ? `${count(sum(run.usage.inputTokens, run.usage.outputTokens))} tokens`
                          : "Usage unknown"}{" "}
                      ·{" "}
                    </>
                  ) : null}
                  {new Date(run.createdAt).toLocaleString(undefined, {
                    month: "short",
                    day: "numeric",
                    hour: "numeric",
                    minute: "2-digit",
                  })}
                </small>
              </span>
              <span className="agent-run-status" data-status={run.status}>
                {run.status === "completed" ? <Check size={12} /> : null}
                {run.status === "completed"
                  ? "Finished"
                  : run.status.replaceAll("_", " ")}
              </span>
              <ChevronRight size={13} />
            </Link>
          ))}
          {query.isError ? (
            <p role="status">Some runs could not be loaded. Try again.</p>
          ) : null}
          {!historyHref && query.hasNextPage ? (
            <button
              type="button"
              disabled={query.isFetchingNextPage}
              onClick={() => void query.fetchNextPage()}
            >
              {query.isFetchingNextPage ? "Loading…" : "Load older runs"}
            </button>
          ) : null}
        </div>
      ) : (
        <p className="agent-inline-empty">
          No runs recorded yet. Execution history will appear here.
        </p>
      )}
    </section>
  );
}
