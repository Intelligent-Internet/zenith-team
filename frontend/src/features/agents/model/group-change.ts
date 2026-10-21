import type { ProjectPermission } from "@/shared/permissions";

import type { GroupChangePreview } from "../api/groups-api";

// The counts and rows of a Group change, kept apart from the markup. Every
// count comes from the server's preview; nothing here guesses what a change
// does.

/**
 * Whether a Group change asks before it saves:
 * someone's runs stop, someone loses a Project, or it moves access on a
 * Project the viewer can't see, so nobody can say. Anything else saves
 * straight away.
 */
export function changeAsksFirst(preview: GroupChangePreview): boolean {
  return (
    preview.runsStopping > 0 ||
    preview.projectsHidden ||
    preview.projects.some((project) =>
      project.people.some((person) => person.after.length === 0),
    )
  );
}

/** "1 project", "2 projects". */
export function counted(count: number, one: string, many: string): string {
  return `${count} ${count === 1 ? one : many}`;
}

/**
 * How a person's access moves: they gain something (+), keep every
 * Project with less (lowered), or lose at least one Project (−).
 */
export type PersonChange = "gain" | "lowered" | "loss";

/** One person in a preview, with every Project their access moves on. */
export type PersonRow = {
  userId: string;
  change: PersonChange;
  /**
   * The Projects it moves on, in the preview's order, except that the
   * Projects they lose come first.
   */
  projectIds: string[];
  /** What they hold on the first of those Projects after the change. */
  after: readonly ProjectPermission[];
  /** Their runs that stop, on all of those Projects. */
  runsStopping: number;
};

type PersonMoves = {
  userId: string;
  gains: boolean;
  lost: string[];
  kept: string[];
  /** What they hold after the change on the first of `kept`. */
  keptAfter: readonly ProjectPermission[];
  runsStopping: number;
};

/**
 * A preview's people, one row each, so nobody's name shows twice: a
 * person who loses one Project and keeps another with less gets one
 * minus row that leads with the Project they lose. Only a person who
 * gains on one Project and gives up access on another would get two
 * rows, a plus and a minus or arrow; no Group change does both today.
 */
export function personRows(
  projects: GroupChangePreview["projects"],
): PersonRow[] {
  const people = new Map<string, PersonMoves>();
  for (const project of projects) {
    for (const person of project.people) {
      const gains = person.after.some(
        (permission) => !person.before.includes(permission),
      );
      const key = `${person.userId}:${gains ? "gain" : "less"}`;
      const moves = people.get(key) ?? {
        userId: person.userId,
        gains,
        lost: [],
        kept: [],
        keptAfter: [],
        runsStopping: 0,
      };
      if (person.after.length === 0) {
        moves.lost.push(project.projectId);
      } else {
        if (moves.kept.length === 0) moves.keptAfter = person.after;
        moves.kept.push(project.projectId);
      }
      moves.runsStopping += person.runsStopping;
      people.set(key, moves);
    }
  }
  return [...people.values()].map((moves): PersonRow => {
    const loses = moves.lost.length > 0;
    return {
      userId: moves.userId,
      change: moves.gains ? "gain" : loses ? "loss" : "lowered",
      projectIds: [...moves.lost, ...moves.kept],
      // A lost Project leads, so a minus never sits beside a preset.
      after: loses ? [] : moves.keptAfter,
      runsStopping: moves.runsStopping,
    };
  });
}

/**
 * The first title the viewer's lists can name, and how many more there
 * are. `count` is how many there are in all; one the viewer cannot name
 * yet still counts, so a mark never disappears for want of a title.
 */
export function firstAndMore(
  titles: readonly string[],
  count: number,
): { first: string | null; more: number } | null {
  if (count === 0) return null;
  const [first = null] = titles;
  return { first, more: first ? count - 1 : count };
}

/** "Payroll", "Payroll +2", or the number alone when none can be named. */
export function firstAndMoreText({
  first,
  more,
}: {
  first: string | null;
  more: number;
}): string {
  if (!first) return String(more);
  return more > 0 ? `${first} +${more}` : first;
}
