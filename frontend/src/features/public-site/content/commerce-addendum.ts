import commerceAddendumArtifact from "@/features/public-site/content/commerce-addendum.json";
import type { LegalDocument } from "@/features/public-site/model/legal-document";

export const commerceAddendum =
  commerceAddendumArtifact satisfies LegalDocument;
