import type { ApprovalDto } from "@/shared/api/types";

export type CompanyPlanPresentation = NonNullable<ApprovalDto["presentation"]>;
type CompanyPlanFieldChange = NonNullable<
  CompanyPlanPresentation["changes"][number]["fields"]
>[number];
export type CompanyPlanReviewTask =
  CompanyPlanPresentation["plan"]["tasks"][number];
export type CompanyPlanReview = NonNullable<
  ReturnType<typeof companyPlanReview>
>;

export function companyPlanReview(presentation: CompanyPlanPresentation) {
  if (
    presentation.plan?.formatVersion !== 5 ||
    !Array.isArray(presentation.plan.tasks)
  ) {
    return null;
  }

  const { tasks, projects } = presentation.plan;
  const touchedTasks = new Set<string>();
  const touchedProjects = new Set<string>();
  for (const change of presentation.changes) {
    if (change.entityType === "project") touchedProjects.add(change.key);
    if (change.entityType === "task" && change.operation !== "delete") {
      touchedTasks.add(change.key);
    }
    if (change.entityType === "dependency") {
      for (const key of change.key.split("->")) touchedTasks.add(key.trim());
    }
  }

  // Walk the complete graph before filtering so unchanged prerequisites still
  // establish the order of the tasks being reviewed. Human tasks stay in it.
  const tasksByKey = new Map(tasks.map((task) => [task.key, task]));
  const visited = new Set<string>();
  const ordered: CompanyPlanReviewTask[] = [];
  const visit = (task: CompanyPlanReviewTask) => {
    if (visited.has(task.key)) return;
    visited.add(task.key);
    for (const dependency of task.dependsOn) {
      const prerequisite = tasksByKey.get(dependency.taskKey);
      if (prerequisite) visit(prerequisite);
    }
    if (touchedTasks.has(task.key)) {
      ordered.push(task);
      touchedProjects.add(task.projectKey);
    }
  };
  tasks.forEach(visit);

  return {
    projects: projects.filter((project) => touchedProjects.has(project.key)),
    tasks: ordered,
  };
}

export type CompanyPlanChangeCounts = {
  projects: { create: number; update: number; archive: number };
  tasks: { create: number; update: number; delete: number };
  dependencies: { create: number; delete: number };
};

export function companyPlanChangeCounts(
  presentation: CompanyPlanPresentation,
): CompanyPlanChangeCounts {
  const counts: CompanyPlanChangeCounts = {
    projects: { create: 0, update: 0, archive: 0 },
    tasks: { create: 0, update: 0, delete: 0 },
    dependencies: { create: 0, delete: 0 },
  };
  for (const change of presentation.changes) {
    if (change.entityType === "project") {
      if (change.operation !== "delete") counts.projects[change.operation] += 1;
    } else if (change.entityType === "task") {
      if (change.operation !== "archive") counts.tasks[change.operation] += 1;
    } else if (change.operation === "create" || change.operation === "delete") {
      counts.dependencies[change.operation] += 1;
    }
  }
  return counts;
}

export function companyPlanChangeSummary(counts: CompanyPlanChangeCounts) {
  const parts = [
    counted(counts.projects.create, "new project"),
    counted(counts.projects.update, "project", "updated"),
    counted(counts.projects.archive, "project", "to archive"),
    counted(counts.tasks.create, "new task"),
    counted(counts.tasks.update, "task", "updated"),
    counted(counts.tasks.delete, "task", "to remove"),
  ].filter(Boolean);
  return (
    parts.join(" · ") ||
    counted(
      counts.dependencies.create + counts.dependencies.delete,
      "dependency change",
    ) ||
    "No changes"
  );
}

export function companyPlanApplySummary(counts: CompanyPlanChangeCounts) {
  const creates = [
    counted(counts.projects.create, "project"),
    counted(counts.tasks.create, "task"),
  ].filter(Boolean);
  const updates = [
    counted(counts.projects.update, "project"),
    counted(counts.tasks.update, "task"),
  ].filter(Boolean);
  return (
    [
      creates.length ? `Creates ${creates.join(" and ")}.` : "",
      updates.length ? `Updates ${updates.join(" and ")}.` : "",
      counts.projects.archive
        ? `Archives ${counted(counts.projects.archive, "project")}.`
        : "",
      counts.tasks.delete
        ? `Removes ${counted(counts.tasks.delete, "task")}.`
        : "",
      !creates.length &&
      !updates.length &&
      (counts.dependencies.create || counts.dependencies.delete)
        ? "Updates task dependencies."
        : "",
    ]
      .filter(Boolean)
      .join(" ") || "No changes to apply."
  );
}

function counted(count: number, noun: string, suffix = "") {
  return count
    ? `${count} ${noun}${count === 1 ? "" : "s"}${suffix ? ` ${suffix}` : ""}`
    : "";
}

export type CompanyPlanChangeLine = {
  key: string;
  summary: string;
  details: string[];
  destructive: boolean;
};

export function companyPlanChangeLines(
  presentation: CompanyPlanPresentation,
  agentNames: ReadonlyMap<string, string>,
): CompanyPlanChangeLine[] {
  const taskTitles = new Map(
    presentation.plan.tasks.map((task) => [task.key, task.title]),
  );
  const projectTitles = new Map(
    presentation.plan.projects.map((project) => [project.key, project.title]),
  );
  const taskName = (key: string) => taskTitles.get(key) ?? key;
  return presentation.changes.flatMap<CompanyPlanChangeLine>((change) => {
    if (change.entityType === "task" && change.operation === "create")
      return [];

    const titles = change.entityType === "project" ? projectTitles : taskTitles;
    const title =
      titles.get(change.key) ??
      change.fields?.find((field) => field.field === "title")?.before ??
      change.key;
    let summary: string;
    if (change.entityType === "dependency") {
      const [dependent, prerequisite] = change.key
        .split("->")
        .map((key) => key.trim());
      summary =
        dependent && prerequisite
          ? `${taskName(dependent)} ${change.operation === "delete" ? "no longer depends on" : "depends on"} ${taskName(prerequisite)}`
          : change.key;
    } else {
      const action = {
        create: "New project",
        update: "Update",
        archive: "Archive project",
        delete: "Remove task",
      }[change.operation];
      summary = `${action}: ${title}`;
    }

    return [
      {
        key: `${change.entityType}:${change.operation}:${change.key}`,
        summary,
        destructive:
          change.operation === "archive" ||
          (change.entityType === "task" && change.operation === "delete"),
        details: [
          ...(change.reason ? [change.reason] : []),
          // A new project's full brief is already available under Plan details.
          ...(change.operation !== "create"
            ? (change.fields ?? []).map((field) =>
                changeFieldLine(field, agentNames),
              )
            : []),
        ],
      },
    ];
  });
}

function changeFieldLine(
  field: CompanyPlanFieldChange,
  agentNames: ReadonlyMap<string, string>,
) {
  if (field.field === "agentId") {
    const name = (id: string) =>
      id ? (agentNames.get(id) ?? "Unavailable agent") : "Human work";
    return `Agent: ${name(field.before)} → ${name(field.after)}`;
  }
  const label =
    field.field === "milestoneType"
      ? "Milestone"
      : field.field
          .replace(/([a-z])([A-Z])/g, "$1 $2")
          .replaceAll("_", " ")
          .replace(/^./, (letter) => letter.toUpperCase());
  return `${label}: ${field.before || "None"} → ${field.after || "None"}`;
}
