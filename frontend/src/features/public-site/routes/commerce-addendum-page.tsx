import { PublicLegalDocument } from "@/features/public-site/components/public-legal-document";
import { commerceAddendum } from "@/features/public-site/content/commerce-addendum";

export function CommerceAddendumPage() {
  return (
    <PublicLegalDocument
      document={commerceAddendum}
      path="/legal/commerce-addendum"
    />
  );
}
