import type { WorkPart } from "./work-composition";

export type WorkFlowNode = WorkPart & {
  x: number;
  y: number;
  height: number;
  parentId?: string;
};
export type WorkFlowLink = { source: string; target: string; path: string };

// One scale for every ribbon, including the child Tasks. Minimum label slots
// give tiny categories room without inflating their share of usage.
export function workFlow(parts: WorkPart[]) {
  const task = parts.find((part) => part.id === "task");
  const ranked = [...(task?.children ?? [])].sort((a, b) => b.value - a.value);
  const children =
    ranked.length > 6
      ? [
          ...ranked.slice(0, 5),
          {
            id: "remaining_tasks",
            name: "Other Tasks",
            value: ranked.slice(5).reduce((sum, part) => sum + part.value, 0),
          },
        ]
      : ranked;
  const total = parts.reduce((sum, part) => sum + part.value, 0);
  const scale = total > 0 ? 204 / total : 0;
  const slot = (part: WorkPart) => Math.max(64, part.value * scale);
  const columnHeight = (items: WorkPart[]) =>
    items.reduce((sum, part) => sum + slot(part), 0) + (items.length + 1) * 14;
  const height = Math.max(370, columnHeight(parts), columnHeight(children));
  const column = (items: WorkPart[], x: number, parentId: string) => {
    const slots = items.map(slot);
    const gap =
      (height - slots.reduce((sum, slot) => sum + slot, 0)) /
      (items.length + 1);
    let cursor = gap;
    return items.map((part, index): WorkFlowNode => {
      const nodeHeight = part.value * scale;
      const node = {
        ...part,
        x,
        y: cursor + (slots[index] - nodeHeight) / 2,
        height: nodeHeight,
        parentId,
      };
      cursor += slots[index] + gap;
      return node;
    });
  };
  const root: WorkFlowNode = {
    id: "all_work",
    name: "All work",
    value: total,
    x: 105,
    y: (height - total * scale) / 2,
    height: total * scale,
  };
  const groups = column(parts, children.length ? 485 : 875, root.id);
  const leaves = column(children, 875, "task");
  const links: WorkFlowLink[] = [];
  const connect = (source: WorkFlowNode, targets: WorkFlowNode[]) => {
    let offset = source.y;
    for (const target of targets) {
      const x = source.x + 16,
        end = target.x,
        bend = (x + end) / 2;
      links.push({
        source: source.id,
        target: target.id,
        path: `M${x},${offset} C${bend},${offset} ${bend},${target.y} ${end},${target.y} L${end},${target.y + target.height} C${bend},${target.y + target.height} ${bend},${offset + target.height} ${x},${offset + target.height} Z`,
      });
      offset += target.height;
    }
  };
  connect(root, groups);
  const taskNode = groups.find((node) => node.id === "task");
  if (taskNode) connect(taskNode, leaves);
  return { root, groups, leaves, links, height, total };
}
