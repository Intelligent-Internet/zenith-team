import { getNextRoute } from "@/app/session/session-routing";
import { ACCOUNT_INBOX_SCOPE } from "@/features/notifications/model/notification";
import {
  activateSessionCompany,
  readSessionSnapshot,
} from "@/shared/session/session";
import { companyService } from "@/shared/api/company.service";
import { boardlyApi } from "@/shared/api/boardly-api";
import { notificationService } from "@/features/notifications/api/notification.service";
import { activateNotification } from "./notification-navigation";
import { toast } from "@/shared/ui/sonner";
import store from "@/state/store";

let activationSequence = 0;

// Both native and in-app clicks resolve membership and content again. A stale
// notification never borrows the company currently selected in the UI.
export async function activateAccountNotification({
  notificationId,
  userId,
}: {
  notificationId: string;
  userId?: string;
}): Promise<string | null> {
  const sequence = ++activationSequence;
  const original = readSessionSnapshot();
  if (
    original.status !== "authenticated" ||
    !original.userId ||
    (userId && userId !== original.userId)
  ) {
    toast.error("Sign in with the account that received this notification.");
    return null;
  }
  const stillCurrent = () => {
    const current = readSessionSnapshot();
    return (
      sequence === activationSequence &&
      current.status === "authenticated" &&
      current.userId === original.userId &&
      current.company?.id === original.company?.id
    );
  };
  try {
    const item = await notificationService.get(
      ACCOUNT_INBOX_SCOPE,
      notificationId,
    );
    const companyId = item.companyId;
    const company = companyId
      ? await companyService.getCompanyById(companyId)
      : null;
    if (!stillCurrent()) return null;
    const path = await activateNotification({
      companyId: companyId ?? ACCOUNT_INBOX_SCOPE,
      notificationId,
      item,
      onChanged: () => {
        store.dispatch(boardlyApi.util.invalidateTags(["Inbox"]));
      },
    });
    if (!stillCurrent()) return null;
    if (!company) return path;
    const session = await activateSessionCompany(company, original.userId);
    if (!session || sequence !== activationSequence) return null;
    return session.nextStep === "complete" ? path : getNextRoute(session);
  } catch {
    if (stillCurrent()) {
      store.dispatch(boardlyApi.util.invalidateTags(["Company", "Inbox"]));
      toast.error(
        "This notification is no longer available, or you no longer have access to its company.",
      );
    }
    return null;
  }
}
