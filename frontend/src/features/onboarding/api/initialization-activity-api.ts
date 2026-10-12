import { boardlyApi } from "@/shared/api/boardly-api";
import type { ConversationTranscriptEntryDto } from "@/shared/api/types";
import { ConversationSocketSession } from "@/shared/realtime/conversation-socket.service";

export const initializationActivityApi = boardlyApi.injectEndpoints({
  endpoints: (builder) => ({
    getInitializationActivity: builder.query<
      {
        entries: ConversationTranscriptEntryDto[];
        unavailable: boolean;
        providerActive: boolean;
      },
      { companyId: string; conversationId: string; runId: string }
    >({
      queryFn: () => ({
        data: { entries: [], unavailable: false, providerActive: false },
      }),
      keepUnusedDataFor: 0,
      // Refetches must not erase the stream while its cursor remains advanced.
      merge: () => {},
      async onCacheEntryAdded(
        { companyId, conversationId, runId },
        { cacheDataLoaded, cacheEntryRemoved, updateCachedData },
      ) {
        try {
          await cacheDataLoaded;
        } catch {
          return;
        }
        const socket = new ConversationSocketSession(
          () => companyId,
          undefined,
          conversationId,
        );
        const unsubscribe = socket.onRunEvent((event) => {
          if (event.runId !== runId) return;
          if (event.kind === "delta" || event.kind === "message") {
            updateCachedData((draft) => {
              draft.providerActive = true;
            });
          }
          if (event.kind !== "message") return;
          if (
            !event.message.parts.some(
              (part) => part.type === "toolCall" || part.type === "toolResult",
            )
          )
            return;
          updateCachedData((draft) => {
            const index = draft.entries.findIndex(
              (entry) => entry.itemKey === event.message.itemKey,
            );
            if (index < 0) draft.entries.push(event.message);
            else draft.entries[index] = event.message;
          });
        });
        let disposed = false;
        let recovering = false;
        const recover = async () => {
          if (disposed || recovering) return;
          recovering = true;
          try {
            await socket.resumeRun(runId, { conversationId });
            if (disposed) return;
            await socket.connect();
            if (!disposed)
              updateCachedData((draft) => {
                draft.unavailable = false;
              });
          } catch {
            if (!disposed)
              updateCachedData((draft) => {
                draft.unavailable = true;
              });
          } finally {
            recovering = false;
          }
        };
        const unsubscribeRecovery = socket.onRecovery(() => {
          void recover();
        });
        void recover();
        // Durable catch-up also covers a missed live frame and reconnect failures.
        const timer = setInterval(() => {
          void recover();
        }, 5000);
        await cacheEntryRemoved;
        disposed = true;
        clearInterval(timer);
        unsubscribe();
        unsubscribeRecovery();
        socket.disconnect();
      },
    }),
  }),
});

export const { useGetInitializationActivityQuery } = initializationActivityApi;
