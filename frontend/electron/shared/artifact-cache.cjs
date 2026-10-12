const artifactCacheChannels = {
  read: "desktop:read-artifact-file",
  cancel: "desktop:cancel-artifact-file",
};

function isArtifactRequestId(value) {
  return (
    typeof value === "string" &&
    /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(
      value,
    )
  );
}

function isArtifactFileRequest(value) {
  return (
    value &&
    typeof value === "object" &&
    Object.keys(value).every((key) =>
      ["requestId", "companyId", "artifactId", "runId", "ordinal"].includes(
        key,
      ),
    ) &&
    [value.requestId, value.companyId, value.artifactId].every(
      isArtifactRequestId,
    ) &&
    (value.runId === undefined || isArtifactRequestId(value.runId)) &&
    Number.isInteger(value.ordinal) &&
    value.ordinal >= 0 &&
    value.ordinal <= 31
  );
}

module.exports = {
  artifactCacheChannels,
  isArtifactFileRequest,
  isArtifactRequestId,
};
