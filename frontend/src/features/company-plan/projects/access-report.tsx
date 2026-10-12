import { useMemo } from "react";
import { Link } from "react-router";

import { useListActiveCompanyMembersQuery } from "@/features/agents/api/team-directory-api";
import { EyeOff, ListTodo, Play, type IconComponent } from "@/shared/ui/icons";

import {
  useGetProjectAccessReportQuery,
  type ProjectAccessReport as Report,
} from "../api/project-api";
import { TaskMemberAvatar } from "../components/task-assignee";
import { AccessChip } from "./access/project-access-counts";
import { teamProjectAccessPath } from "./access/team-access-path";
import "./access/project-access.css";
import "./managed-projects.css";

type Reason = Report["items"][number]["people"][number]["reasons"][number];

const REASONS: Record<Reason, { Icon: IconComponent; label: string }> = {
  task_assignee: { Icon: ListTodo, label: "Open tasks" },
  run_authorizer: { Icon: Play, label: "Ran lately" },
};

// People who can't see a restricted Project they were working in: the
// assignee of an open Task there, or someone who ran work there lately
// (GET /projects/access-report, ADR 0017). Holders of "projects.manage"
// repair it from the Team page's Project access. One compact row each:
// the person, an icon per reason, the Project with a crossed-out eye, and
// Open. It names people and Projects only, never a Task or a chat.
export function ProjectAccessReport({
  companyId,
  onOpen,
}: {
  companyId: string;
  /** Opens a Project's access in place; without it, a link to Team. */
  onOpen?: (projectId: string) => void;
}) {
  const report = useGetProjectAccessReportQuery(companyId, {
    skip: !companyId,
  });
  const members = useListActiveCompanyMembersQuery(companyId, {
    skip: !companyId,
  });
  const actorById = useMemo(
    () =>
      new Map(
        (members.currentData?.items ?? []).map((member) => [
          member.actor.id,
          member.actor,
        ]),
      ),
    [members.currentData],
  );
  const items = report.currentData?.items ?? [];
  if (report.isError || items.length === 0) return null;
  return (
    <section aria-label="No access to their work">
      <ul className="access-report__list">
        {items.flatMap((entry) =>
          entry.people.map((person) => {
            const actor = actorById.get(person.userId);
            const name = actor?.name ?? "A member";
            return (
              <li
                key={`${entry.projectId}:${person.userId}`}
                className="access-report__row"
              >
                <TaskMemberAvatar
                  member={{ name, avatarUrl: actor?.avatarUrl ?? null }}
                />
                <strong className="access-report__name" title={name}>
                  {name}
                </strong>
                <span className="project-access-chips">
                  {person.reasons.map((reason) => {
                    const { Icon, label } = REASONS[reason];
                    return (
                      <AccessChip
                        key={reason}
                        icon={Icon}
                        label={label}
                        tooltip={label}
                      />
                    );
                  })}
                </span>
                <span className="access-report__project">
                  <AccessChip
                    icon={EyeOff}
                    label={`No access to ${entry.title}`}
                    tone="warn"
                    tooltip="No access"
                  >
                    {entry.title}
                  </AccessChip>
                </span>
                {onOpen ? (
                  <button
                    aria-label={`Open access for ${entry.title}`}
                    className="dashboard-button"
                    type="button"
                    onClick={() => onOpen(entry.projectId)}
                  >
                    Open
                  </button>
                ) : (
                  <Link
                    aria-label={`Open access for ${entry.title}`}
                    className="dashboard-button"
                    to={teamProjectAccessPath(entry.projectId)}
                  >
                    Open
                  </Link>
                )}
              </li>
            );
          }),
        )}
      </ul>
    </section>
  );
}
