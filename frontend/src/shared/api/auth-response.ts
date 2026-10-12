import type {
  ClientLoginResponse,
  PasswordAuthResponse,
} from "@/shared/api/types";

// Signup and login answer with the same status code for both outcomes, so the
// branch key is the `result` discriminator rather than the presence of a
// status field.
export function isLoginResponse(
  response: PasswordAuthResponse,
): response is ClientLoginResponse {
  return response.result === "session";
}
