import { EvaluationError, type EvaluationEnvironment } from "./expression";
import { scheduleEvaluation } from "./evaluation-pool";

// A client owns one current computation. The shared pool bounds active workers;
// cancellation removes queued snapshots or terminates the executing worker.
export function createEvaluationClient() {
  const sessionId = crypto.randomUUID();
  let generation = 0,
    disposed = false;
  let active: AbortController | undefined;
  const cancel = () => {
    active?.abort();
    active = undefined;
  };
  return {
    async evaluate(
      expression: unknown,
      environment: EvaluationEnvironment,
      signal?: AbortSignal,
    ): Promise<unknown> {
      if (disposed) throw new EvaluationError("evaluation.disposed");
      if (signal?.aborted) throw new EvaluationError("evaluation.cancelled");
      cancel();
      const controller = new AbortController();
      active = controller;
      const abort = () => controller.abort();
      signal?.addEventListener("abort", abort, { once: true });
      try {
        const value = await scheduleEvaluation(
          {
            protocolVersion: 1,
            sessionId,
            generation: ++generation,
            expression,
            environment,
          },
          controller.signal,
        );
        if (controller.signal.aborted)
          throw new EvaluationError("evaluation.cancelled");
        return value;
      } finally {
        signal?.removeEventListener("abort", abort);
        if (active === controller) active = undefined;
      }
    },
    cancel,
    dispose() {
      disposed = true;
      cancel();
    },
  };
}
