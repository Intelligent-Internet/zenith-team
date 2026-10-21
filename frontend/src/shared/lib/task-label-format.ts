import type { CompanyDetail, TaskDto } from "@/shared/api/types";

// The one label every surface shows for a Task: the company's task prefix
// and the Task's number, from this one display formatter.
// Until Company data has loaded there is no prefix, and the number alone is
// still the same identity; the key is a separate stable slug and never
// stands in for the label.
export function formatTaskLabel(
  task: Pick<TaskDto, "number">,
  prefix?: CompanyDetail["taskPrefix"],
) {
  return prefix ? `${prefix}-${task.number}` : `Task ${task.number}`;
}

export type LabelTask = (task: Pick<TaskDto, "number">) => string;
