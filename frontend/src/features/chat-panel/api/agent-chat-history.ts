import { type DurableRunTimeline } from "@/features/chat-panel/model/chat-timeline";
import { conversationService, runService } from "@/shared/api";
import { requireActiveCompanyId } from "@/shared/api/active-company";
import type { RunDto } from "@/shared/api/types";

const RUN_TRANSCRIPT_PAGE_SIZE = 200;

// A failed Run must never keep the transcript from loading: when the Run list
// cannot be read the history still renders, with whatever failures the
// lifecycle frames already reported.
export async function loadConversationRuns(
  conversationId: string,
): Promise<RunDto[] | null> {
  try {
    return await runService.listAllRuns(requireActiveCompanyId(), {
      conversationId,
    });
  } catch (error) {
    console.warn("Conversation runs could not be loaded", error);
    return null;
  }
}

type DurableConversationTranscript = {
  timelines: Record<string, DurableRunTimeline>;
  throughSeq: number;
};

export async function loadDurableConversationTranscript(
  companyId: string,
  conversationId: string,
  current: Readonly<Record<string, DurableRunTimeline>>,
  includedRunIds: readonly string[],
  currentThroughSeq: number,
  reloadFromStart: boolean,
): Promise<DurableConversationTranscript> {
  const itemsByRun = new Map(
    includedRunIds.flatMap((runId) => {
      const timeline = current[runId];
      return timeline
        ? [[runId, new Map(timeline.items.map((item) => [item.seq, item]))]]
        : [];
    }),
  );
  let afterSeq = reloadFromStart ? 0 : currentThroughSeq;

  while (true) {
    const page = await conversationService.getTranscript(
      companyId,
      conversationId,
      {
        afterSeq,
        limit: RUN_TRANSCRIPT_PAGE_SIZE,
      },
    );
    let nextAfterSeq = afterSeq;
    for (const item of page.items) {
      if (item.seq <= nextAfterSeq) {
        throw new Error("Conversation transcript page is not ascending");
      }
      const runItems = itemsByRun.get(item.runId) ?? new Map();
      runItems.set(item.seq, item);
      itemsByRun.set(item.runId, runItems);
      nextAfterSeq = item.seq;
    }

    if (
      nextAfterSeq === afterSeq &&
      (page.hasMore || afterSeq < page.throughSeq)
    ) {
      throw new Error("Conversation transcript pagination did not advance");
    }
    if (page.throughSeq < nextAfterSeq) {
      throw new Error("Conversation transcript head does not cover its page");
    }
    afterSeq = nextAfterSeq;
    if (page.hasMore || afterSeq < page.throughSeq) {
      continue;
    }
    return {
      timelines: Object.fromEntries(
        [...itemsByRun].map(([runId, items]) => {
          const ordered = [...items.values()].sort(
            (left, right) => left.seq - right.seq,
          );
          return [
            runId,
            {
              hasOlder: false,
              items: ordered,
              throughSeq: ordered.at(-1)?.seq ?? 0,
            },
          ];
        }),
      ),
      throughSeq: page.throughSeq,
    };
  }
}
