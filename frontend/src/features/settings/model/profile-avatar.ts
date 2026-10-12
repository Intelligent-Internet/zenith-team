// Personal avatars travel with the User projection, so upload only a small,
// re-encoded thumbnail rather than the original photo and its metadata.
export async function createProfileAvatarDataUrl(file: File): Promise<string> {
  let image: ImageBitmap;
  try {
    image = await createImageBitmap(file);
  } catch {
    throw new Error("This image could not be opened. Choose another image.");
  }
  try {
    const canvas = document.createElement("canvas");
    const context = canvas.getContext("2d");
    if (!context)
      throw new Error("Your avatar could not be prepared. Try again.");
    const side = Math.min(image.width, image.height);
    for (const size of [256, 192, 128]) {
      canvas.width = canvas.height = Math.min(size, side);
      context.drawImage(
        image,
        (image.width - side) / 2,
        (image.height - side) / 2,
        side,
        side,
        0,
        0,
        canvas.width,
        canvas.height,
      );
      const pixels = context.getImageData(
        0,
        0,
        canvas.width,
        canvas.height,
      ).data;
      const hasTransparency = pixels.some(
        (value, index) => index % 4 === 3 && value < 255,
      );
      const url = canvas.toDataURL(
        hasTransparency ? "image/png" : "image/jpeg",
        0.85,
      );
      if (url.slice(url.indexOf(",") + 1).length <= 128 * 1024) return url;
    }
    throw new Error("This image is too detailed. Choose a smaller image.");
  } finally {
    image.close();
  }
}
