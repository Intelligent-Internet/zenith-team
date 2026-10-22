import type { EvaluationEnvironment } from "./expression";

// Worker messages are ephemeral viewer state, never persisted document content.
export interface EvaluationRequest {
  protocolVersion: 1;
  sessionId: string;
  generation: number;
  expression: unknown;
  environment: EvaluationEnvironment;
}
export type EvaluationResponse = { protocolVersion: 1; sessionId: string } & (
  | { generation: number; ok: true; value: unknown }
  | { generation: number; ok: false; code: string }
);
