export function filenameFromGeneratedFilePath(path: string) {
  const segments = generatedFilePathSegments(path);
  return segments[segments.length - 1] ?? path;
}

export function parentDirectoryFromGeneratedFilePath(path: string) {
  const segments = generatedFilePathSegments(path);
  return segments.length > 1
    ? segments.slice(0, -1).join("/")
    : "Workspace root";
}

export function splitGeneratedFilename(path: string) {
  const filename = filenameFromGeneratedFilePath(path);
  const extensionIndex = filename.lastIndexOf(".");

  if (extensionIndex <= 0 || extensionIndex === filename.length - 1) {
    return { extension: "", stem: filename };
  }

  return {
    extension: filename.slice(extensionIndex),
    stem: filename.slice(0, extensionIndex),
  };
}

function generatedFilePathSegments(path: string) {
  return path.replace(/\\/g, "/").split("/").filter(Boolean);
}
