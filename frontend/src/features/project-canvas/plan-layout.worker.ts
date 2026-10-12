/// <reference lib="webworker" />
import { createLayeredLayout } from "@/features/company-plan/model/work-dependency-canvas-model";
import { PLAN_LAYOUT_LIMIT } from "./plan-layout-limits";
import type {
  PlanLayoutRequest,
  PlanLayoutResponse,
} from "./plan-layout.types";
self.onmessage = ({ data }: MessageEvent<PlanLayoutRequest>) => {
  let result: PlanLayoutResponse = { requestId: data.requestId, ok: false };
  try {
    if (
      data.taskIds.length <= PLAN_LAYOUT_LIMIT.tasks &&
      data.edges.length <= PLAN_LAYOUT_LIMIT.edges
    ) {
      const layout = createLayeredLayout(data.taskIds, data.edges);
      result = {
        requestId: data.requestId,
        ok: true,
        positions: [...layout.positions],
        cycleTaskIds: [...layout.cycleTaskIds],
      };
    }
  } catch {
    /* The renderer keeps the task grid available when layout fails. */
  }
  self.postMessage(result);
};
