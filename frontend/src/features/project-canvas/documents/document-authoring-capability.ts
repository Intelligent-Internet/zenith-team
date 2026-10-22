export type DocumentAuthoringCapability = {
  intelligentUIAuthoringEnabled: boolean;
  reactiveReadOnly: boolean;
};

export function documentAuthoringCapability(
  schemaVersion: number,
  authoringEnabled: boolean,
): DocumentAuthoringCapability {
  return {
    intelligentUIAuthoringEnabled: authoringEnabled,
    reactiveReadOnly: schemaVersion >= 2 && !authoringEnabled,
  };
}

export function canEditWithDocumentCapability(
  canEdit: boolean,
  reactiveReadOnly: boolean,
) {
  return canEdit && !reactiveReadOnly;
}
