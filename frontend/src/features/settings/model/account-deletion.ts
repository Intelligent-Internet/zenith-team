export const ACCOUNT_DELETION_CONFIRMATION = "DELETE";

export function isAccountDeletionConfirmationValid(value: string): boolean {
  return value === ACCOUNT_DELETION_CONFIRMATION;
}
