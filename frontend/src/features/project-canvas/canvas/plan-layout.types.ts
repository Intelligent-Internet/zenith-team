export type PlanLayoutRequest = {
  requestId: number;
  taskIds: string[];
  edges: { id: string; source: string; target: string }[];
};
export type PlanLayoutResponse =
  | {
      requestId: number;
      ok: true;
      positions: [string, { x: number; y: number }][];
      cycleTaskIds: string[];
    }
  | { requestId: number; ok: false };
