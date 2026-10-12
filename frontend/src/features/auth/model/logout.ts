import { clearInvalidClientSessionState } from "@/app/session/client-session-state";
import { clearPendingEmailVerification } from "@/features/auth/model/auth-view-state";
import { authService } from "@/shared/api/auth.service";

async function clearBoardlyClientSession() {
  clearPendingEmailVerification();
  await clearInvalidClientSessionState();
}

export async function logoutBoardlySession() {
  try {
    await authService.logout();
  } finally {
    await clearBoardlyClientSession();
  }
}

export async function deleteBoardlyAccount() {
  await authService.deleteAccount();
  await clearBoardlyClientSession();
}

export const logoutLocalMockSession = logoutBoardlySession;
