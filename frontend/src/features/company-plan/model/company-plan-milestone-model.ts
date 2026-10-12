import { compareTaskReadiness } from "@/features/company-plan/model/task-work-state";
import type {
  CompanyPlanLaneAgent,
  CompanyPlanTaskLane,
} from "@/features/company-plan/model/company-plan-view-model";
import {
  milestoneLabel,
  milestoneOrder,
} from "@/features/company-plan/model/milestone-catalog";
import type { TaskDto, TaskMilestoneTypeDto } from "@/shared/api/types";

export type CompanyPlanMilestoneTask = {
  agent: CompanyPlanLaneAgent;
  task: TaskDto;
};

export type CompanyPlanMilestone = {
  completedTaskCount: number;
  name: string;
  tasks: CompanyPlanMilestoneTask[];
  type: TaskMilestoneTypeDto;
};

export function createCompanyPlanMilestones(
  lanes: CompanyPlanTaskLane[],
): CompanyPlanMilestone[] {
  const visibleTasks = lanes.flatMap((lane) =>
    lane.tasks.map((task) => ({ agent: lane.agent, task })),
  );
  const tasksByMilestone = new Map<
    TaskMilestoneTypeDto,
    CompanyPlanMilestoneTask[]
  >();
  for (const item of visibleTasks) {
    const milestoneTasks = tasksByMilestone.get(item.task.milestoneType) ?? [];
    milestoneTasks.push(item);
    tasksByMilestone.set(item.task.milestoneType, milestoneTasks);
  }

  return [...tasksByMilestone.entries()]
    .sort(([left], [right]) => milestoneOrder(left) - milestoneOrder(right))
    .map(([type, tasks]) => ({
      completedTaskCount: tasks.filter((item) => item.task.status === "done")
        .length,
      name: milestoneLabel(type),
      tasks: tasks.sort((left, right) =>
        compareTaskReadiness(left.task, right.task),
      ),
      type,
    }));
}
