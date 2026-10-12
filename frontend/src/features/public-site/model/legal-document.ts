export type LegalParagraph = string | readonly string[];

export type LegalSection = {
  id: string;
  title: string;
  paragraphs: readonly LegalParagraph[];
};

export type LegalDocument = {
  title: string;
  description: string;
  lastUpdated: string;
  sections: readonly LegalSection[];
};
