export const businessContextAssetPath = "assets/general/business_context.md";

export function isBusinessContextAssetPath(path: string) {
  const normalizedPath = path.replaceAll("\\", "/");
  return (
    normalizedPath === businessContextAssetPath ||
    normalizedPath.endsWith(`/${businessContextAssetPath}`)
  );
}
