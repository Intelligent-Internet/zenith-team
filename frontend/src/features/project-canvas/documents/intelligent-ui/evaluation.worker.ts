import { EvaluationError } from "./expression";
import { createCompiledExpressionCache } from "./compiled-expression-cache";
import type {
  EvaluationRequest,
  EvaluationResponse,
} from "./evaluation-protocol";

const definitions = createCompiledExpressionCache();

self.onmessage = async ({ data }: MessageEvent<EvaluationRequest>) => {
  if (
    data.protocolVersion !== 1 ||
    typeof data.sessionId !== "string" ||
    !Number.isSafeInteger(data.generation)
  )
    return;
  const identity = { protocolVersion: 1 as const, sessionId: data.sessionId };
  let response: EvaluationResponse;
  try {
    response = {
      ...identity,
      generation: data.generation,
      ok: true,
      value: (await definitions.compile(data.expression))(data.environment),
    };
  } catch (error) {
    response = {
      ...identity,
      generation: data.generation,
      ok: false,
      code: error instanceof EvaluationError ? error.code : "evaluation.failed",
    };
  }
  self.postMessage(response);
};
