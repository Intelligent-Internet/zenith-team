import { readSessionSnapshot } from "@/shared/session/session";

export function requireActiveCompanyId(): string {
  const companyId = readSessionSnapshot().company?.id;
  if (!companyId) {
    throw new Error("An active Company is required for this request.");
  }
  return companyId;
}
