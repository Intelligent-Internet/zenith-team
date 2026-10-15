export type ChatMentionMember = {
  connector?: { connectorId: string; eligibleAgentIds: string[] };
  skill?: { skillId: string; versionId: string };
  id: string;
  name: string;
  description: string;
  avatarSrc?: string | null;
  kind: "agent" | "human";
  broadcastScope?: "everyone" | "here";
  // Why this member cannot be tagged yet, as the short chip the list shows
  // ("Not set up"). Choosing it hands the member to onUnavailableSelect
  // instead of inserting a mention.
  unavailable?: string;
};

export type ChatMentionQuery = {
  trigger: "@" | "$";
  start: number;
  end: number;
  text: string;
};

export function findChatMentionQuery(
  text: string,
  selectionStart: number,
  selectionEnd: number,
): ChatMentionQuery | null {
  if (selectionStart !== selectionEnd) return null;
  const match = text
    .slice(0, selectionStart)
    .match(/(?:^|[\s([{])([@$])([\p{L}\p{M}\p{N}_.-]*)$/u);
  if (!match) return null;
  return {
    trigger: match[1] as "@" | "$",
    start: selectionStart - match[2].length - 1,
    end: selectionStart,
    text: match[2],
  };
}

export function findChatMentionRanges(
  text: string,
  members: readonly Pick<ChatMentionMember, "name">[],
): Array<{ start: number; end: number }> {
  const names = [
    ...new Set(members.map((member) => member.name).filter(Boolean)),
  ].sort((left, right) => right.length - left.length);
  const ranges: Array<{ start: number; end: number }> = [];

  for (const match of text.matchAll(/(?:^|[\s([{])@/gu)) {
    const start = match.index + match[0].length - 1;
    if (start < (ranges.at(-1)?.end ?? 0)) continue;

    const name = names.find((candidate) => {
      const end = start + 1 + candidate.length;
      return (
        text.slice(start + 1, end).toLocaleLowerCase() ===
          candidate.toLocaleLowerCase() &&
        !/^[\p{L}\p{M}\p{N}_@]/u.test(text.slice(end))
      );
    });
    if (name) ranges.push({ start, end: start + 1 + name.length });
  }

  return ranges;
}

export function filterChatMentionMembers(
  members: readonly ChatMentionMember[],
  query: string,
) {
  const search = normalizeMentionSearch(query);
  return members.filter((member) =>
    normalizeMentionSearch(`${member.name} ${member.description}`).includes(
      search,
    ),
  );
}

function normalizeMentionSearch(value: string) {
  return value
    .normalize("NFD")
    .replace(/\p{M}/gu, "")
    .replace(/[đĐ]/g, "d")
    .toLocaleLowerCase();
}
