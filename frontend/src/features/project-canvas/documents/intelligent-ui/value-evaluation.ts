import { createEvaluationClient } from "./evaluation-client";
import type { EvaluationEnvironment } from "./expression";

// Only concurrent evaluations are shared. Completed or cancelled observations
// are never retained here, including after a source loses authorization.
const pending = new Map<
  string,
  {
    client: ReturnType<typeof createEvaluationClient>;
    promise: Promise<unknown>;
    readers: number;
  }
>();

export function evaluateValue(
  key: string,
  expression: unknown,
  environment: EvaluationEnvironment,
) {
  let job = pending.get(key);
  if (!job) {
    const client = createEvaluationClient();
    const promise = client.evaluate(expression, environment);
    job = { client, promise, readers: 0 };
    const current = job;
    pending.set(key, current);
    const finish = () => {
      if (pending.get(key) === current) pending.delete(key);
      client.dispose();
    };
    void promise.then(finish, finish);
  }
  job.readers++;
  const current = job;
  return {
    promise: current.promise,
    release() {
      if (--current.readers === 0) {
        if (pending.get(key) === current) pending.delete(key);
        current.client.dispose();
      }
    },
  };
}
