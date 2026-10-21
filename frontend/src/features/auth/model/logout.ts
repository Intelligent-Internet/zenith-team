import { clearInvalidClientSessionState } from "@/app/session/client-session-state";
import { clearPendingEmailVerification } from "@/features/auth/model/auth-view-state";
import { authService } from "@/shared/api/auth.service";

async function clearZenithTeamClientSession() {
  clearPendingEmailVerification();
  await clearInvalidClientSessionState();
}

export async function logoutZenithTeamSession() {
  try {
    await authService.logout();
  } finally {
    await clearZenithTeamClientSession();
  }
}

export async function deleteZenithTeamAccount() {
  await authService.deleteAccount();
  await clearZenithTeamClientSession();
}

export const logoutLocalMockSession = logoutZenithTeamSession;
