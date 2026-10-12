import {
  readStorageJson,
  writeStorageJson,
  removeStorageItem,
} from "@/shared/lib/browser-storage";
import { invitationReviewPath } from "./invitation-link";

const KEY = "zenith.pending-invitation.v1";
type PendingInvitation = {
  token: string;
  companyName: string;
  maskedEmail: string;
  expiresAt: number;
};
export function readPendingInvitation(): PendingInvitation | null {
  return readStorageJson<PendingInvitation | null>(
    "session",
    KEY,
    (value) => {
      if (!value || typeof value !== "object") return null;
      const item = value as Partial<PendingInvitation>;
      if (
        typeof item.token !== "string" ||
        !/^[A-Za-z0-9_-]{43}$/.test(item.token) ||
        typeof item.companyName !== "string" ||
        typeof item.maskedEmail !== "string" ||
        typeof item.expiresAt !== "number" ||
        !Number.isFinite(item.expiresAt) ||
        item.expiresAt <= Date.now()
      )
        return null;
      return item as PendingInvitation;
    },
    null,
  );
}
export function rememberInvitation(
  token: string,
  companyName: string,
  maskedEmail: string,
) {
  return writeStorageJson("session", KEY, {
    token,
    companyName,
    maskedEmail,
    expiresAt: Date.now() + 60 * 60 * 1000,
  });
}
export function invitationReturnPath() {
  const pending = readPendingInvitation();
  return pending ? invitationReviewPath(pending.token) : null;
}
export function clearPendingInvitation() {
  removeStorageItem("session", KEY);
}
