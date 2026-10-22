import { DocumentViewMode } from "./document-view-mode";
import { useScope } from "./intelligent-ui/reactive-values";
import { useListWorkspaceTasksQuery } from "@/shared/api/task-api";
import { BlockSettings, useBlockEditable } from "./block-settings";
import { BlockViewSelect } from "./block-view-select";
import { useContext, useEffect, useRef, useState } from "react";
import { NodeViewWrapper, type NodeViewProps } from "@tiptap/react";
import { useNavigate } from "react-router";
import {
  useGetWorkflowQuery,
  useListWorkflowsQuery,
} from "@/features/workflows/api/workflows-api";
import { WorkflowLaunchDialog } from "@/features/workflows/components/workflow-launch-dialog";

import type { Workflow } from "@/shared/api/types";
import type { components } from "@/shared/api/generated/api.generated";
import { Icon } from "@/shared/ui/icon";
import type { CanvasScope } from "../api/canvas-api";
import { WorkBlockScope } from "./work-blocks";
import { taskBlockSummary } from "./task-summary";

export function WorkflowBlock(props: NodeViewProps) {
  const scope = useContext(WorkBlockScope);
  const { template } = useScope(props);
  if (!scope)
    return (
      <NodeViewWrapper className="p-[22px_16px] [border:1px_dashed_var(--pc-line)] rounded-[10px] text-[var(--pc-muted)] text-[length:12px] leading-[1.6] text-center">
        This Workflow requires a Project.
      </NodeViewWrapper>
    );
  return <ProjectWorkflowBlock {...props} scope={scope} template={template} />;
}

function ProjectWorkflowBlock({
  node,
  editor,
  updateAttributes,
  scope,
  template,
}: NodeViewProps & { scope: CanvasScope; template: boolean }) {
  const editable = useBlockEditable(editor);
  const mode = useContext(DocumentViewMode);
  const captured = mode === "snapshot" || Boolean(node.attrs.capturedAt);
  const navigate = useNavigate();
  const key = String(node.attrs.workflowKey || "");
  const catalog = useListWorkflowsQuery(scope.companyId, {
    skip: captured || !editable,
  });
  const query = useGetWorkflowQuery(
    { companyId: scope.companyId, workflowKey: key },
    { skip: captured || template || !key },
  );
  const tasks = useListWorkspaceTasksQuery(
    { companyId: scope.companyId, options: { project_id: scope.projectId } },
    { skip: captured || template || !key },
  );
  const definition = captured
    ? (node.attrs.capturedWorkflow as Workflow | undefined)
    : query.currentData;
  const [expanded, setExpanded] = useState(node.attrs.display === "expanded");
  useEffect(
    () => setExpanded(node.attrs.display === "expanded"),
    [node.attrs.display],
  );
  const [launch, setLaunch] = useState(false);
  useEffect(() => {
    if (template || mode !== "live") setLaunch(false);
  }, [template, mode]);
  const trigger = useRef<HTMLButtonElement>(null);
  const linked: components["schemas"]["OverviewTask"][] = template
    ? []
    : captured
      ? (node.attrs.capturedTasks ?? [])
      : (tasks.currentData?.items ?? [])
          .filter((task) => task.labels.includes(`workflow:${key}`))
          .map(taskBlockSummary);
  return (
    <NodeViewWrapper
      className={`pc-has-settings pc-workflow-block pc-workflow-${node.attrs.display || "card"}`}
      data-block-id={node.attrs.blockId}
      contentEditable={false}
    >
      {template && (
        <p role="status">
          Workflow template · Runs only from rendered instances.
        </p>
      )}
      {mode === "comparison" && (
        <p className="text-[var(--pc-muted)] text-[length:11px] leading-[1.4] m-[0_0_8px]">
          Workflow · {node.attrs.display || "card"}
        </p>
      )}
      {editable && (
        <BlockSettings label="Workflow options" needed={!key}>
          <label>
            Workflow
            <select
              aria-label="Referenced Workflow"
              value={key}
              onChange={(event) =>
                updateAttributes({ workflowKey: event.target.value })
              }
            >
              <option value="">Choose a Workflow…</option>
              {catalog.currentData?.items.map((workflow) => (
                <option key={workflow.key} value={workflow.key}>
                  {workflow.name}
                </option>
              ))}
            </select>
          </label>
          <BlockViewSelect
            value={
              node.attrs.display === "list"
                ? "card"
                : node.attrs.display || "card"
            }
            options={[
              ["link", "Compact"],
              ["card", "Card"],
              ["expanded", "Playbook"],
            ]}
            onChange={(display) => {
              updateAttributes({ display });
              setExpanded(display === "expanded");
            }}
          />
        </BlockSettings>
      )}
      {template ? null : !key ? (
        <p className="p-[22px_16px] [border:1px_dashed_var(--pc-line)] rounded-[10px] text-[var(--pc-muted)] text-[length:12px] leading-[1.6] text-center">
          Choose a Workflow from Workflow options.
        </p>
      ) : query.isError && !captured ? (
        <p role="alert">
          This Workflow is unavailable.{" "}
          <button onClick={() => void query.refetch()}>Retry</button>
        </p>
      ) : !definition ? (
        <p role="status">
          {captured
            ? "The saved Workflow definition is unavailable."
            : "Loading Workflow…"}
        </p>
      ) : (
        <>
          <div className="pc-reference-heading">
            <Icon name="task-square" className="size-5" />
            <strong>{definition.name}</strong>
            <button
              aria-expanded={expanded}
              onClick={() => setExpanded(!expanded)}
            >
              {expanded ? "Collapse" : "Details"}
            </button>
            {!captured && !template && mode === "live" && (
              <button ref={trigger} onClick={() => setLaunch(true)}>
                Use Workflow
              </button>
            )}
          </div>
          {node.attrs.display !== "link" && <p>{definition.description}</p>}
          {expanded && (
            <div className="pc-workflow-details">
              <h4>What it delivers</h4>
              <ul>
                {definition.highlights.map((highlight) => (
                  <li key={highlight}>{highlight}</li>
                ))}
              </ul>
              <h4>Inputs</h4>
              <ul>
                {definition.pages.map((page) => (
                  <li key={page.title}>{page.title}</li>
                ))}
              </ul>
            </div>
          )}
          {linked.length > 0 && node.attrs.display !== "link" && (
            <div className="pc-work-rows" aria-label="Workflow Tasks">
              {linked.map((task) => (
                <div className="pc-work-row" key={task.id}>
                  <button
                    className="pc-work-title"
                    onClick={() =>
                      navigate(`/projects/${scope.projectId}?taskId=${task.id}`)
                    }
                  >
                    {task.title}
                  </button>
                  <span className={`pc-work-status is-${task.status}`}>
                    {task.status.replaceAll("_", " ")}
                  </span>
                  <span className="pc-work-assignee">
                    {task.assigneeName}
                    {task.agentName && <small>With {task.agentName}</small>}
                  </span>
                  <button
                    className="pc-work-action"
                    onClick={() =>
                      navigate(`/projects/${scope.projectId}?taskId=${task.id}`)
                    }
                  >
                    {captured ? "Open current Task" : "Open Task"}
                  </button>
                </div>
              ))}
            </div>
          )}
          {!captured && !template && tasks.isError && (
            <p role="alert">
              Workflow Tasks are unavailable.{" "}
              <button onClick={() => void tasks.refetch()}>Retry</button>
            </p>
          )}
          {captured && node.attrs.capturedAt && (
            <small>
              Definition and work captured{" "}
              {new Date(node.attrs.capturedAt).toLocaleString()}
            </small>
          )}
        </>
      )}
      {launch && !template && (
        <WorkflowLaunchDialog
          companyId={scope.companyId}
          initialProjectId={scope.projectId}
          workflowKey={key}
          onClose={() => setLaunch(false)}
          onRestoreFocus={() => trigger.current?.focus()}
        />
      )}
    </NodeViewWrapper>
  );
}
