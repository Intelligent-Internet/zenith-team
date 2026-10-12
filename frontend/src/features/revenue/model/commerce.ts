import type {
  components as ApiComponents,
  operations as ApiOperations,
} from "@/shared/api/generated/api.generated";

export type CommerceEnvironment =
  ApiComponents["schemas"]["CommerceEnvironment"];
export type CommerceSummary =
  ApiOperations["getCommerceSummary"]["responses"][200]["content"]["application/json"];
export type CommerceTermsState = CommerceSummary["terms"];
export type CommerceAccountSummary = CommerceSummary["accounts"][number];
export type CommerceAccountStatus = CommerceAccountSummary["status"];
export type CommerceSyncStatus = CommerceAccountSummary["syncStatus"];
export type CommerceRequirementsSummary =
  CommerceAccountSummary["requirements"];

export type AcceptCommerceTermsRequest =
  ApiOperations["acceptCommerceTerms"]["requestBody"]["content"]["application/json"];
export type CommerceTermsAcceptance =
  ApiOperations["acceptCommerceTerms"]["responses"][200]["content"]["application/json"];
export type CreateCommerceAccountSessionRequest =
  ApiOperations["createCommerceAccountSession"]["requestBody"]["content"]["application/json"];
export type CommerceAccountSession =
  ApiOperations["createCommerceAccountSession"]["responses"][200]["content"]["application/json"];
export type CommerceOnboardingLink =
  ApiOperations["createCommerceOnboardingLink"]["responses"][200]["content"]["application/json"];
export type CommerceDashboardLink =
  ApiOperations["createCommerceDashboardLink"]["responses"][200]["content"]["application/json"];

export type CommerceManifestPage =
  ApiOperations["listCommerceManifests"]["responses"][200]["content"]["application/json"];
export type CommerceManifestReview = CommerceManifestPage["manifests"][number];
export type CommerceManifestStatus =
  ApiOperations["approveCommerceManifest"]["responses"][200]["content"]["application/json"];
export type CommerceManifestState = CommerceManifestStatus["state"];
export type CommerceManifestOffer =
  CommerceManifestReview["manifest"]["offers"][number];
export type ApproveCommerceManifestRequest =
  ApiOperations["approveCommerceManifest"]["requestBody"]["content"]["application/json"];

export function commerceAccountForEnvironment(
  summary: CommerceSummary | undefined,
  environment: CommerceEnvironment,
) {
  return summary?.accounts.find(
    (account) => account.environment === environment,
  );
}

export function commerceAccountIsReady(
  account: CommerceAccountSummary | undefined,
) {
  return account?.status === "ready" && account.syncStatus === "current";
}

export function commerceDashboardLinkIsValid(
  url: string,
  environment: CommerceEnvironment,
) {
  if (url.length === 0 || url.length > 4096) return false;
  try {
    const parsed = new URL(url);
    const accountPath = "acct_[A-Za-z0-9]{8,255}/dashboard";
    const expectedPath =
      environment === "sandbox"
        ? new RegExp(`^/test/${accountPath}$`)
        : new RegExp(`^/${accountPath}$`);
    return (
      parsed.protocol === "https:" &&
      parsed.host === "dashboard.stripe.com" &&
      parsed.username === "" &&
      parsed.password === "" &&
      parsed.search === "" &&
      parsed.hash === "" &&
      expectedPath.test(parsed.pathname)
    );
  } catch {
    return false;
  }
}
