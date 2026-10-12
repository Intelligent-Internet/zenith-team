import { PublicLegalDocument } from "@/features/public-site/components/public-legal-document";
import { privacyPolicy } from "@/features/public-site/content/privacy-policy";

export function PrivacyPolicyPage() {
  return <PublicLegalDocument document={privacyPolicy} path="/privacy" />;
}
