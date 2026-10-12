import type { AnalyticsBreakdown } from "@/shared/api/types";
import { metricAmount } from "./metrics";

export type WorkPart = {
  id: string;
  name: string;
  value: number;
  children?: WorkPart[];
};

export function workComposition(
  runTypes: AnalyticsBreakdown,
  tasks: AnalyticsBreakdown,
  metric: string,
): WorkPart[] {
  const amount = (row: NonNullable<AnalyticsBreakdown["rows"]>[number]) =>
    metricAmount(row, metric) ?? 0;
  const taskRows = tasks.rows ?? [];
  const deleted = taskRows.find(
    (row) => row.key?.synthetic === "deleted_tasks",
  );
  const deletedValue = deleted ? amount(deleted) : 0;
  const children: WorkPart[] = taskRows
    .filter((row) => row.key?.id)
    .map((row) => ({
      id: row.key!.id!,
      name: row.key?.label ?? "Task",
      value: amount(row),
    }))
    .filter((part) => part.value > 0);
  const names: Record<string, string> = {
    direct_chat: "Direct chats",
    task: "Tasks",
    schedule: "Schedules",
    channel_agent: "Channel chats",
  };
  const groups = (runTypes.rows ?? []).map((row) => {
    const id = row.key?.synthetic ?? row.key?.label ?? "unknown";
    if (id !== "task")
      return {
        id,
        name: names[id] ?? row.key?.label ?? "Other work",
        value: amount(row),
      };
    const value = Math.max(0, amount(row) - deletedValue);
    const remainder = Math.max(
      0,
      value - children.reduce((sum, child) => sum + child.value, 0),
    );
    return {
      id,
      name: "Tasks",
      value,
      children: [
        ...children,
        ...(remainder > 0
          ? [{ id: "other_tasks", name: "Other Tasks", value: remainder }]
          : []),
      ],
    };
  });
  if (deletedValue > 0)
    groups.push({
      id: "deleted_tasks",
      name: "Deleted Tasks",
      value: deletedValue,
    });
  return groups
    .filter((part) => part.value > 0)
    .sort((a, b) => b.value - a.value);
}

export function ringSector(
  start: number,
  end: number,
  inner: number,
  outer: number,
) {
  // Two arcs preserve a full-circle single category as well as tiny categories.
  const polar = (radius: number, angle: number) =>
    `${300 + radius * Math.cos(angle)},${300 + radius * Math.sin(angle)}`;
  const middle = (start + end) / 2;
  return `M${polar(outer, start)} A${outer},${outer} 0 0 1 ${polar(outer, middle)} A${outer},${outer} 0 0 1 ${polar(outer, end)} L${polar(inner, end)} A${inner},${inner} 0 0 0 ${polar(inner, middle)} A${inner},${inner} 0 0 0 ${polar(inner, start)} Z`;
}
