import { PublicLegalDocument } from "@/features/public-site/components/public-legal-document";
import { termsOfService } from "@/features/public-site/content/terms-of-service";

export function TermsOfServicePage() {
  return <PublicLegalDocument document={termsOfService} path="/terms" />;
}
