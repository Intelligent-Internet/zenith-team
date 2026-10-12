import { apiClient } from "@/lib/api-client";
import type { operations } from "@/shared/api/generated/api.generated";

type StudioOperation = operations["operateConversationStudio"];
type StudioRequest =
  StudioOperation["requestBody"]["content"]["application/json"];
type StudioResponse =
  StudioOperation["responses"][200]["content"]["application/json"];

// Desktop uses the same signed-in human HTTP admission as the web client.
// The service accepts only the generated operation contract; it supplies no
// daemon bearer, upstream address, project selector or command capability.
export async function operateConversationStudio(
  companyId: string,
  conversationId: string,
  request: StudioRequest,
  signal?: AbortSignal,
): Promise<StudioResponse> {
  const response = await apiClient.post<StudioResponse>(
    `/companies/${encodeURIComponent(companyId)}/conversations/${encodeURIComponent(conversationId)}/studio/operations`,
    request,
    { signal, timeout: 15_000 },
  );
  if (
    response.schemaVersion !== 2 ||
    response.requestId !== request.requestId ||
    response.operation !== request.operation
  )
    throw new Error("Studio response identity did not match the request.");
  return response;
}
