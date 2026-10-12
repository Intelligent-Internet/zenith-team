/**
 * Encodes plain text as a link label: characters that would end the link,
 * open markdown syntax, or split a table cell become numeric entities, which
 * CommonMark readers decode back to the literal character.
 */
export function encodeWikiLinkLabel(label: string) {
  return label
    .replace(/\s+/g, " ")
    .replace(/[&<>[\]\\`*_~|]/g, (char) => `&#${char.charCodeAt(0)};`);
}
