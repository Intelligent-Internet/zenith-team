import {
  isCommerceApprovalReturnContext,
  type CommerceApprovalReturnContext,
} from "@/features/revenue/model/commerce-approval-route";
import {
  readStorageJson,
  removeStorageItem,
  writeStorageJson,
} from "@/shared/lib/browser-storage";

const COMMERCE_APPROVAL_STORAGE_KEY =
  "boardly.commerce-approval-continuation.v1";
const COMMERCE_APPROVAL_TTL_MS = 10 * 60 * 1_000;
const UUID_PATTERN =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const SHA256_PATTERN = /^[0-9a-f]{64}$/;

export type PendingCommerceApproval = {
  companyId: string;
  digest: string;
  expiresAt: number;
  manifestId: string;
  returnContext: CommerceApprovalReturnContext | null;
  userId: string;
};

function normalizePendingCommerceApproval(
  value: unknown,
): PendingCommerceApproval | null {
  if (!value || typeof value !== "object") {
    return null;
  }

  const pending = value as Partial<PendingCommerceApproval>;
  if (
    typeof pending.companyId !== "string" ||
    !UUID_PATTERN.test(pending.companyId) ||
    typeof pending.digest !== "string" ||
    !SHA256_PATTERN.test(pending.digest) ||
    typeof pending.expiresAt !== "number" ||
    !Number.isFinite(pending.expiresAt) ||
    typeof pending.manifestId !== "string" ||
    !UUID_PATTERN.test(pending.manifestId) ||
    typeof pending.userId !== "string" ||
    !UUID_PATTERN.test(pending.userId) ||
    (pending.returnContext !== undefined &&
      pending.returnContext !== null &&
      !isCommerceApprovalReturnContext(pending.returnContext))
  ) {
    return null;
  }

  return {
    companyId: pending.companyId,
    digest: pending.digest,
    expiresAt: pending.expiresAt,
    manifestId: pending.manifestId,
    returnContext: pending.returnContext ?? null,
    userId: pending.userId,
  };
}

export function writePendingCommerceApproval(
  pending: Omit<PendingCommerceApproval, "expiresAt">,
) {
  return writeStorageJson("session", COMMERCE_APPROVAL_STORAGE_KEY, {
    ...pending,
    expiresAt: Date.now() + COMMERCE_APPROVAL_TTL_MS,
  } satisfies PendingCommerceApproval);
}

export function readPendingCommerceApproval() {
  const pending = readStorageJson<PendingCommerceApproval | null>(
    "session",
    COMMERCE_APPROVAL_STORAGE_KEY,
    normalizePendingCommerceApproval,
    null,
  );
  if (!pending || pending.expiresAt <= Date.now()) {
    clearPendingCommerceApproval();
    return null;
  }
  return pending;
}

export function clearPendingCommerceApproval() {
  removeStorageItem("session", COMMERCE_APPROVAL_STORAGE_KEY);
}
