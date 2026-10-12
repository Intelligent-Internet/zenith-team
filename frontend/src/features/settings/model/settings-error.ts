import { getApiErrorCode, getApiErrorMessage } from "@/lib/api-client";

export const PROFILE_SETTINGS_LOAD_FALLBACK_MESSAGE =
  "Profile could not be loaded.";
export const PROFILE_SETTINGS_SAVE_FALLBACK_MESSAGE =
  "Profile could not be saved. Please try again.";
export const COMPANY_SETTINGS_LOAD_FALLBACK_MESSAGE =
  "Company settings could not be loaded.";
export const COMPANY_SETTINGS_SAVE_FALLBACK_MESSAGE =
  "Company settings could not be saved. Please try again.";
export const ACCOUNT_DELETION_FALLBACK_MESSAGE =
  "Account could not be deleted. Please try again.";
export const ACCOUNT_DELETION_DISABLED_MESSAGE =
  "Account deletion is disabled in this environment.";
export const ACCOUNT_DELETION_FORBIDDEN_MESSAGE =
  "This account cannot be deleted.";

export function getProfileSettingsLoadErrorMessage(error: unknown): string {
  return getApiErrorMessage(error, PROFILE_SETTINGS_LOAD_FALLBACK_MESSAGE);
}

export function getProfileSettingsSaveErrorMessage(error: unknown): string {
  return getApiErrorMessage(error, PROFILE_SETTINGS_SAVE_FALLBACK_MESSAGE);
}

export function getCompanySettingsLoadErrorMessage(error: unknown): string {
  return getApiErrorMessage(error, COMPANY_SETTINGS_LOAD_FALLBACK_MESSAGE);
}

export function getCompanySettingsSaveErrorMessage(error: unknown): string {
  return getApiErrorMessage(error, COMPANY_SETTINGS_SAVE_FALLBACK_MESSAGE);
}

export function getAccountDeletionErrorMessage(error: unknown): string {
  const code = getApiErrorCode(error);

  if (code === "ACCOUNT_DELETION_DISABLED") {
    return ACCOUNT_DELETION_DISABLED_MESSAGE;
  }

  if (code === "ACCOUNT_DELETION_FORBIDDEN") {
    return ACCOUNT_DELETION_FORBIDDEN_MESSAGE;
  }

  return getApiErrorMessage(error, ACCOUNT_DELETION_FALLBACK_MESSAGE);
}
