import { checkValue, EvaluationError } from "./expression";
import type {
  EvaluationRequest,
  EvaluationResponse,
} from "./evaluation-protocol";

type Job = {
  request: string;
  bytes: number;
  sessionId: string;
  generation: number;
  signal: AbortSignal;
  abort: () => void;
  timer: ReturnType<typeof setTimeout>;
  resolve: (value: unknown) => void;
  reject: (error: EvaluationError) => void;
  settled: boolean;
};
type Slot = {
  worker: Worker;
  job?: Job;
  idleTimer?: ReturnType<typeof setTimeout>;
};

// One pool per browser realm. It retains no completed values, document state or
// credentials. Pending JSON snapshots and running jobs share the memory budget.
const slots = new Set<Slot>();
const queue: Job[] = [];
let retainedBytes = 0;

function retire(slot: Slot) {
  clearTimeout(slot.idleTimer);
  slot.worker.onmessage = null;
  slot.worker.onerror = null;
  slot.worker.onmessageerror = null;
  slot.worker.terminate();
  slots.delete(slot);
}
function settle(
  job: Job,
  result: { value: unknown } | { code: string },
  discard = false,
) {
  if (job.settled) return;
  job.settled = true;
  clearTimeout(job.timer);
  job.signal.removeEventListener("abort", job.abort);
  retainedBytes -= job.bytes;
  job.request = "";
  const index = queue.indexOf(job);
  if (index !== -1) queue.splice(index, 1);
  for (const slot of slots) {
    if (slot.job !== job) continue;
    slot.job = undefined;
    if (discard) retire(slot);
    else
      slot.idleTimer = setTimeout(() => {
        if (!slot.job) retire(slot);
      }, 30000);
    break;
  }
  if ("code" in result) job.reject(new EvaluationError(result.code));
  else job.resolve(result.value);
  requestDrain();
}
function createSlot(): Slot {
  const worker = new Worker(
    new URL("./evaluation.worker.ts", import.meta.url),
    { type: "module" },
  );
  const slot: Slot = { worker };
  slots.add(slot);
  worker.onmessage = ({ data }: MessageEvent<EvaluationResponse>) => {
    const job = slot.job;
    if (!slots.has(slot) || !job) return;
    try {
      if (
        !data ||
        typeof data !== "object" ||
        Array.isArray(data) ||
        data.protocolVersion !== 1 ||
        typeof data.sessionId !== "string" ||
        !Number.isSafeInteger(data.generation) ||
        data.generation < 1 ||
        typeof data.ok !== "boolean"
      )
        throw new Error();
      // A retired generation must never fail or complete a newer computation.
      if (
        data.sessionId !== job.sessionId ||
        data.generation !== job.generation
      )
        return;
      const fields = [
        "protocolVersion",
        "sessionId",
        "generation",
        "ok",
        data.ok ? "value" : "code",
      ];
      if (
        Object.keys(data).length !== fields.length ||
        Object.keys(data).some((key) => !fields.includes(key))
      )
        throw new Error();
      if (data.ok) checkValue(data.value);
      else if (
        typeof data.code !== "string" ||
        !/^[a-zA-Z.]{1,64}$/.test(data.code)
      )
        throw new Error();
    } catch {
      settle(job, { code: "evaluation.message" }, true);
      return;
    }
    settle(job, data.ok ? { value: data.value } : { code: data.code });
  };
  worker.onerror = (event) => {
    event.preventDefault();
    if (slot.job) settle(slot.job, { code: "evaluation.worker" }, true);
    else retire(slot);
  };
  worker.onmessageerror = () => {
    if (slot.job) settle(slot.job, { code: "evaluation.message" }, true);
    else retire(slot);
  };
  return slot;
}
let draining = false;
let drainQueued = false;
function requestDrain() {
  if (drainQueued) return;
  drainQueued = true;
  queueMicrotask(() => {
    drainQueued = false;
    drain();
  });
}
function drain() {
  if (draining) return;
  draining = true;
  try {
    while (queue.length) {
      let slot = [...slots].find((candidate) => !candidate.job);
      if (!slot && slots.size >= 4) break;
      const job = queue.shift()!;
      if (job.signal.aborted) {
        settle(job, { code: "evaluation.cancelled" });
        continue;
      }
      try {
        slot ??= createSlot();
        clearTimeout(slot.idleTimer);
        slot.job = job;
        clearTimeout(job.timer);
        job.timer = setTimeout(
          () => settle(job, { code: "evaluation.timeout" }, true),
          2000,
        );
        slot.worker.postMessage(JSON.parse(job.request));
      } catch {
        settle(job, { code: "evaluation.worker" }, true);
      }
    }
  } finally {
    draining = false;
  }
}

export function scheduleEvaluation(
  request: EvaluationRequest,
  signal: AbortSignal,
): Promise<unknown> {
  if (signal.aborted)
    return Promise.reject(new EvaluationError("evaluation.cancelled"));
  let serialized: string;
  try {
    serialized = JSON.stringify(request);
  } catch {
    return Promise.reject(new EvaluationError("value.json"));
  }
  const bytes = new TextEncoder().encode(serialized).length;
  if (bytes > 2097152) return Promise.reject(new EvaluationError("value.size"));
  if (queue.length >= 4096 || retainedBytes + bytes > 33554432)
    return Promise.reject(new EvaluationError("evaluation.queue"));
  return new Promise((resolve, reject) => {
    const abort = () => settle(job, { code: "evaluation.cancelled" }, true);
    const job: Job = {
      request: serialized,
      bytes,
      sessionId: request.sessionId,
      generation: request.generation,
      signal,
      abort,
      resolve,
      reject,
      settled: false,
      timer: setTimeout(
        () => settle(job, { code: "evaluation.queueTimeout" }),
        30000,
      ),
    };
    retainedBytes += bytes;
    signal.addEventListener("abort", abort, { once: true });
    queue.push(job);
    requestDrain();
  });
}
