import { useMemo } from "react";
import { Link } from "react-router";
import { useCompanyPermissions } from "@/shared/permissions";
import { useGetWorkspaceAnalyticsSummaryQuery } from "@/shared/api/workspace-api";
import { cx } from "@/shared/lib/cx";
import { ArrowUpRight } from "@/shared/ui/icons";
import { cost, count, duration, tokens } from "../model/format";
import "./analytics-summary-card.css";

export function AnalyticsSummaryCard({
  companyId,
  agentId,
  projectId,
  className,
}: {
  companyId: string;
  agentId?: string;
  projectId?: string;
  className?: string;
}) {
  const scope = useCompanyPermissions().can("usage.read") ? "company" : "mine";
  const period = useMemo(() => {
    const end = new Date();
    end.setHours(0, 0, 0, 0);
    end.setDate(end.getDate() + 1);
    const start = new Date(end);
    start.setDate(start.getDate() - 30);
    return { startAt: start.toISOString(), endAt: end.toISOString() };
  }, []);
  const query = useGetWorkspaceAnalyticsSummaryQuery({
    companyId,
    scope,
    ...period,
    timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone,
    agentId,
    projectId,
  });
  const metrics = query.currentData?.current;
  const params = new URLSearchParams({
    scope,
    tab: agentId ? "agents" : "projects",
  });
  if (agentId) params.set("agent", agentId);
  if (projectId) params.set("project", projectId);
  return (
    <section
      className={cx("analytics-summary-card", className)}
      aria-label="Usage summary"
    >
      <header className="analytics-summary-card__header">
        <div className="analytics-summary-card__heading">
          <h3>
            {scope === "mine"
              ? "Your usage"
              : projectId
                ? "Project work"
                : "Agent work"}
          </h3>
          <p>Last 30 days</p>
        </div>
        <Link
          className="analytics-summary-card__link"
          to={`/analytics?${params}`}
        >
          Open in Analytics <ArrowUpRight size={14} aria-hidden="true" />
        </Link>
      </header>
      {query.isError ? (
        <p className="analytics-summary-card__status" role="alert">
          Usage could not be loaded.{" "}
          <button type="button" onClick={() => void query.refetch()}>
            Retry
          </button>
        </p>
      ) : !metrics ? (
        <p className="analytics-summary-card__status" role="status">
          Loading usage…
        </p>
      ) : (
        <dl className="analytics-summary-card__metrics">
          {[
            ["Runs", count(metrics.runs?.total)],
            ["Tokens", tokens(metrics)],
            ["Run time", duration(metrics.time?.runTimeMs)],
            ["Cost", cost(metrics.cost, metrics.runs?.withoutUsage)],
          ].map(([label, value]) => (
            <div className="analytics-summary-card__metric" key={label}>
              <dt>{label}</dt>
              <dd>{value}</dd>
            </div>
          ))}
        </dl>
      )}
    </section>
  );
}
