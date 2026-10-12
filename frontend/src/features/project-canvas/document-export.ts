export function downloadDocument(
  value: string,
  filename: string,
  type: string,
) {
  const object = URL.createObjectURL(new Blob([value], { type }));
  const link = document.createElement("a");
  link.href = object;
  link.download = filename;
  link.click();
  setTimeout(() => URL.revokeObjectURL(object), 1000);
}
