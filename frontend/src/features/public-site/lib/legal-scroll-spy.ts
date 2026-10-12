export type LegalSectionPosition = {
  id: string;
  top: number;
};

export function findActiveLegalSectionId(
  sections: LegalSectionPosition[],
  readingLine: number,
) {
  if (sections.length === 0) {
    return null;
  }

  let activeSectionId = sections[0].id;

  for (const section of sections) {
    if (section.top > readingLine) {
      break;
    }

    activeSectionId = section.id;
  }

  return activeSectionId;
}
