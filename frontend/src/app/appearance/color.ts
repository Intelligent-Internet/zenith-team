function channels(color: string): number[] {
  return [1, 3, 5].map((offset) =>
    Number.parseInt(color.slice(offset, offset + 2), 16),
  );
}

export function hslToHex(
  hue: number,
  saturation: number,
  lightness: number,
): string {
  const light = lightness / 100;
  const chroma = (1 - Math.abs(2 * light - 1)) * (saturation / 100);
  const sector = (((hue % 360) + 360) % 360) / 60;
  const secondary = chroma * (1 - Math.abs((sector % 2) - 1));
  const offset = light - chroma / 2;
  const rgb = [
    [chroma, secondary, 0],
    [secondary, chroma, 0],
    [0, chroma, secondary],
    [0, secondary, chroma],
    [secondary, 0, chroma],
    [chroma, 0, secondary],
  ][Math.floor(sector)];

  return `#${rgb
    .map((channel) =>
      Math.round((channel + offset) * 255)
        .toString(16)
        .padStart(2, "0"),
    )
    .join("")}`.toUpperCase();
}

export function mixColor(
  color: string,
  target: string,
  amount: number,
): string {
  const targetChannels = channels(target);
  return `#${channels(color)
    .map((channel, index) =>
      Math.round(channel + (targetChannels[index] - channel) * amount)
        .toString(16)
        .padStart(2, "0"),
    )
    .join("")}`.toUpperCase();
}

function luminance(color: string): number {
  const [red, green, blue] = channels(color).map((channel) => {
    const value = channel / 255;
    return value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4;
  });
  return red * 0.2126 + green * 0.7152 + blue * 0.0722;
}

export function contrastRatio(foreground: string, background: string): number {
  const values = [luminance(foreground), luminance(background)];
  return (Math.max(...values) + 0.05) / (Math.min(...values) + 0.05);
}

export function contrastingText(background: string): string {
  if (
    contrastRatio("#000000", background) < contrastRatio("#FFFFFF", background)
  ) {
    return "#FFFFFF";
  }
  return contrastRatio("#17191C", background) >= 4.5 ? "#17191C" : "#000000";
}

export function readableColor(
  color: string,
  backgrounds: string[],
  minimumRatio = 4.5,
): string {
  const minimumContrast = (candidate: string) =>
    Math.min(
      ...backgrounds.map((background) => contrastRatio(candidate, background)),
    );
  if (minimumContrast(color) >= minimumRatio) return color;

  const target =
    minimumContrast("#000000") > minimumContrast("#FFFFFF")
      ? "#000000"
      : "#FFFFFF";
  let lower = 0;
  let upper = 1;
  for (let step = 0; step < 16; step += 1) {
    const midpoint = (lower + upper) / 2;
    if (minimumContrast(mixColor(color, target, midpoint)) >= minimumRatio)
      upper = midpoint;
    else lower = midpoint;
  }
  return mixColor(color, target, upper);
}
