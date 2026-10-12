// ElevenLabs PCM output is headerless signed 16-bit little-endian mono.
// Storage may label it audio/mpeg; use the explicit output format to wrap it
// in a WAV container that native media elements can decode without resampling.
export async function prepareStudioAudio(
  source: string,
  format: string | undefined,
  signal: AbortSignal,
) {
  const match = /^pcm_(16000|22050|24000|44100|48000)$/.exec(format ?? "");
  if (!match) return source;
  const response = await fetch(source, { signal });
  if (!response.ok) throw new Error("Audio preview could not be loaded.");
  const pcm = await response.arrayBuffer();
  if (!pcm.byteLength || pcm.byteLength % 2)
    throw new Error("Invalid PCM audio.");
  const bytes = new Uint8Array(pcm);
  // Some providers already package PCM in WAV, despite the output-format name.
  if (
    String.fromCharCode(...bytes.slice(0, 4)) === "RIFF" &&
    String.fromCharCode(...bytes.slice(8, 12)) === "WAVE"
  ) {
    return URL.createObjectURL(new Blob([pcm], { type: "audio/wav" }));
  }
  const header = new ArrayBuffer(44);
  const view = new DataView(header);
  const writeText = (offset: number, text: string) => {
    for (let index = 0; index < text.length; index++)
      view.setUint8(offset + index, text.charCodeAt(index));
  };
  const sampleRate = Number(match[1]);
  writeText(0, "RIFF");
  view.setUint32(4, 36 + pcm.byteLength, true);
  writeText(8, "WAVE");
  writeText(12, "fmt ");
  view.setUint32(16, 16, true);
  view.setUint16(20, 1, true);
  view.setUint16(22, 1, true);
  view.setUint32(24, sampleRate, true);
  view.setUint32(28, sampleRate * 2, true);
  view.setUint16(32, 2, true);
  view.setUint16(34, 16, true);
  writeText(36, "data");
  view.setUint32(40, pcm.byteLength, true);
  return URL.createObjectURL(new Blob([header, pcm], { type: "audio/wav" }));
}
