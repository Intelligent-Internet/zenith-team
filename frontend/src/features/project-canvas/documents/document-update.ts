export function encodeUpdate(value: Uint8Array) {
  let result = "";
  for (let i = 0; i < value.length; i += 8192)
    result += String.fromCharCode(...value.subarray(i, i + 8192));
  return btoa(result);
}
export const decodeUpdate = (value: string) =>
  Uint8Array.from(atob(value), (c) => c.charCodeAt(0));
