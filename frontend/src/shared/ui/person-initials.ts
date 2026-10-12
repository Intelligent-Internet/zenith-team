// A person's initials for an avatar without a photo: the first letters of
// their first and last names, so "Mai Owner" reads "MO" on every page. A
// one-word name gives one letter; an empty name gives "".
export function personInitials(name: string | null | undefined): string {
  const words = (name ?? "").trim().split(/\s+/u).filter(Boolean);
  const first = words[0];
  if (!first) return "";
  const last = words.length > 1 ? words.at(-1) : undefined;
  return [first, last]
    .map((word) => (word ? Array.from(word)[0] : ""))
    .join("")
    .toLocaleUpperCase();
}
