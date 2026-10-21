import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type PropsWithChildren,
  type RefObject,
} from "react";
import type { FileUIPart } from "ai";
import { nanoid } from "nanoid";

import {
  readStorageJson,
  removeStorageItem,
  writeStorageJson,
} from "@/shared/lib/browser-storage";

import type { AttachmentsContext } from "@/shared/ui/core/ai-elements/prompt-input";
import type { ChatInputFile } from "./attachment-preview";
import { describeConversationSendFailure } from "@/shared/realtime/conversation-socket.service";

export type ChatTextSelection = {
  end: number;
  start: number;
};

type AttachmentDraftMetadata = {
  filename: string;
  mediaType: string;
};

type ChatSessionAttachments = AttachmentsContext & {
  missingFiles: AttachmentDraftMetadata[];
  dismissMissingFiles: () => void;
};

type PendingChatPromptMessage = {
  text: string;
  clientMessageId: string;
  requestKey: string;
};

type SendPendingChatPrompt = (
  message: PendingChatPromptMessage,
) => Promise<void>;

export type PendingChatPrompt = PendingChatPromptMessage & {
  status: "sending" | "error";
  error?: string;
};

type ChatSessionEntry = {
  attachments: Array<ChatInputFile & { id: string }>;
  missingFiles: AttachmentDraftMetadata[];
  selection?: ChatTextSelection;
  pendingPrompt?: PendingChatPrompt;
};

type ChatSessionMemory = {
  entries: Record<string, ChatSessionEntry>;
  fileInputRefs: Map<string, RefObject<HTMLInputElement | null>>;
  getEntry: (key: string) => ChatSessionEntry | undefined;
  setEntry: (
    key: string,
    update: (entry: ChatSessionEntry) => ChatSessionEntry,
  ) => void;
};

const ChatSessionMemoryContext = createContext<ChatSessionMemory | null>(null);
const ChatSessionMemoryKeyContext = createContext<string | null>(null);

export function ChatSessionMemoryProvider({
  children,
  storageScope,
}: PropsWithChildren<{ storageScope?: string }>) {
  const storageKey = storageScope
    ? `zenith-team.chat-attachment-drafts.v1:${encodeURIComponent(storageScope)}`
    : undefined;
  const pendingDraftStorageKey = storageScope
    ? `zenith-team.chat-pending-drafts.v1:${encodeURIComponent(storageScope)}`
    : undefined;
  const [entries, setEntries] = useState<Record<string, ChatSessionEntry>>(
    () => {
      const drafts = readAttachmentDrafts(storageKey);
      const pendingDrafts = readPendingDrafts(pendingDraftStorageKey);
      return Object.fromEntries(
        [
          ...new Set([...Object.keys(drafts), ...Object.keys(pendingDrafts)]),
        ].map((key) => [
          key,
          {
            attachments: [],
            missingFiles: drafts[key] ?? [],
            pendingPrompt: pendingDrafts[key],
          },
        ]),
      );
    },
  );
  const entriesRef = useRef(entries);
  const fileInputRefs = useRef(
    new Map<string, RefObject<HTMLInputElement | null>>(),
  );

  useEffect(
    () => () => {
      Object.values(entriesRef.current).forEach((entry) => {
        entry.attachments.forEach(revokeAttachmentUrl);
      });
    },
    [],
  );

  const setEntry = useCallback<ChatSessionMemory["setEntry"]>(
    (key, update) => {
      const previous = entriesRef.current[key] ?? {
        attachments: [],
        missingFiles: [],
      };
      const next = update(previous);
      if (next === previous) return;
      const updated = { ...entriesRef.current, [key]: next };
      entriesRef.current = updated;
      setEntries(updated);
      if (
        previous.pendingPrompt !== next.pendingPrompt &&
        pendingDraftStorageKey
      ) {
        // This is draft recovery only. Reload never sends a message; an explicit
        // retry uses the same request ID to reconcile an uncertain acceptance.
        writeStorageJson(
          "local",
          pendingDraftStorageKey,
          Object.fromEntries(
            Object.entries(updated).flatMap(([key, entry]) =>
              entry.pendingPrompt
                ? [
                    [
                      key,
                      {
                        text: entry.pendingPrompt.text,
                        clientMessageId: entry.pendingPrompt.clientMessageId,
                        requestKey: entry.pendingPrompt.requestKey,
                      },
                    ],
                  ]
                : [],
            ),
          ),
        );
      }
      if (
        previous.attachments !== next.attachments ||
        previous.missingFiles !== next.missingFiles
      ) {
        saveAttachmentDraft(storageKey, key, [
          ...next.attachments.map((file) => ({
            filename: file.filename ?? "Attachment",
            mediaType: file.mediaType,
          })),
          ...next.missingFiles,
        ]);
      }
    },
    [storageKey, pendingDraftStorageKey],
  );

  const getEntry = useCallback((key: string) => entriesRef.current[key], []);

  const value = useMemo<ChatSessionMemory>(
    () => ({
      entries,
      fileInputRefs: fileInputRefs.current,
      getEntry,
      setEntry,
    }),
    [entries, getEntry, setEntry],
  );

  return (
    <ChatSessionMemoryContext.Provider value={value}>
      {children}
    </ChatSessionMemoryContext.Provider>
  );
}

export function useChatSessionPendingPrompt(fallbackSessionKey: string) {
  const memory = useContext(ChatSessionMemoryContext);
  const scopedKey = useContext(ChatSessionMemoryKeyContext);
  const sessionKey = scopedKey ?? fallbackSessionKey;
  if (!memory) return undefined;

  const { getEntry, setEntry } = memory;
  const prompt = memory.entries[sessionKey]?.pendingPrompt;
  const deliver = (
    message: PendingChatPromptMessage,
    send: SendPendingChatPrompt,
  ) => {
    setEntry(sessionKey, (entry) => ({
      ...entry,
      pendingPrompt: { ...message, status: "sending" },
    }));
    void (async () => {
      try {
        await send(message);
        setEntry(sessionKey, (entry) => ({
          ...entry,
          pendingPrompt: undefined,
        }));
      } catch (error) {
        setEntry(sessionKey, (entry) => ({
          ...entry,
          pendingPrompt: {
            ...message,
            status: "error",
            error: describeConversationSendFailure(error).message,
          },
        }));
      }
    })();
  };

  return {
    prompt,
    send: (message: PendingChatPromptMessage, send: SendPendingChatPrompt) => {
      if (getEntry(sessionKey)?.pendingPrompt) {
        throw new Error("Resolve the previous message before sending another.");
      }
      deliver(message, send);
    },
    retry: (send: SendPendingChatPrompt) => {
      const current = getEntry(sessionKey)?.pendingPrompt;
      if (current?.status !== "error") return;
      deliver(
        {
          text: current.text,
          clientMessageId: current.clientMessageId,
          requestKey: current.requestKey,
        },
        send,
      );
    },
    dismiss: () => {
      setEntry(sessionKey, (entry) =>
        entry.pendingPrompt?.status === "error"
          ? { ...entry, pendingPrompt: undefined }
          : entry,
      );
    },
  };
}

function readPendingDrafts(
  storageKey: string | undefined,
): Record<string, PendingChatPrompt> {
  if (!storageKey) return {};
  return readStorageJson(
    "local",
    storageKey,
    (value: unknown) => {
      if (!value || typeof value !== "object" || Array.isArray(value))
        return {};
      return Object.fromEntries(
        Object.entries(value).flatMap(([key, draft]) => {
          if (
            !draft ||
            typeof draft !== "object" ||
            !("text" in draft) ||
            typeof draft.text !== "string" ||
            !("clientMessageId" in draft) ||
            typeof draft.clientMessageId !== "string" ||
            !("requestKey" in draft) ||
            typeof draft.requestKey !== "string"
          )
            return [];
          return [
            [
              key,
              {
                text: draft.text,
                clientMessageId: draft.clientMessageId,
                requestKey: draft.requestKey,
                status: "error" as const,
                error:
                  "The previous send was interrupted. Retry to confirm delivery.",
              },
            ],
          ];
        }),
      );
    },
    {},
  );
}

export function ChatSessionMemoryScope({
  children,
  sessionKey,
}: PropsWithChildren<{ sessionKey: string }>) {
  return (
    <ChatSessionMemoryKeyContext.Provider value={sessionKey}>
      {children}
    </ChatSessionMemoryKeyContext.Provider>
  );
}

export function useOptionalChatSessionAttachments():
  ChatSessionAttachments | undefined {
  const memory = useContext(ChatSessionMemoryContext);
  const sessionKey = useContext(ChatSessionMemoryKeyContext);
  const entries = memory?.entries;
  const fileInputRefs = memory?.fileInputRefs;
  const setEntry = memory?.setEntry;
  const fileInputRef = useMemo(
    () => ({ current: null }) satisfies RefObject<HTMLInputElement | null>,
    [],
  );

  useEffect(() => {
    if (!fileInputRefs || !sessionKey) return undefined;
    fileInputRefs.set(sessionKey, fileInputRef);
    return () => {
      if (fileInputRefs.get(sessionKey) === fileInputRef) {
        fileInputRefs.delete(sessionKey);
      }
    };
  }, [fileInputRef, fileInputRefs, sessionKey]);

  const add = useCallback(
    (files: File[] | FileList) => {
      if (!setEntry || !sessionKey) return;
      const incoming = Array.from(files);
      if (!incoming.length) return;
      const attachments = incoming.map((file) => ({
        filename: file.name,
        sizeBytes: file.size,
        id: nanoid(),
        mediaType: file.type,
        type: "file" as const,
        url: URL.createObjectURL(file),
      }));
      setEntry(sessionKey, (entry) => {
        const missingFiles = [...entry.missingFiles];
        for (const file of incoming) {
          const index = missingFiles.findIndex(
            (missing) =>
              missing.filename === file.name &&
              (!missing.mediaType ||
                !file.type ||
                missing.mediaType === file.type),
          );
          if (index !== -1) missingFiles.splice(index, 1);
        }
        return {
          ...entry,
          attachments: entry.attachments.concat(attachments),
          missingFiles,
        };
      });
    },
    [sessionKey, setEntry],
  );
  const remove = useCallback(
    (id: string) => {
      if (!setEntry || !sessionKey) return;
      setEntry(sessionKey, (entry) => {
        const attachment = entry.attachments.find((item) => item.id === id);
        if (attachment) revokeAttachmentUrl(attachment);
        return {
          ...entry,
          attachments: entry.attachments.filter((item) => item.id !== id),
        };
      });
    },
    [sessionKey, setEntry],
  );
  const clear = useCallback(() => {
    if (!setEntry || !sessionKey) return;
    setEntry(sessionKey, (entry) => {
      entry.attachments.forEach(revokeAttachmentUrl);
      return { ...entry, attachments: [], missingFiles: [] };
    });
  }, [sessionKey, setEntry]);
  const dismissMissingFiles = useCallback(() => {
    if (!setEntry || !sessionKey) return;
    setEntry(sessionKey, (entry) => ({ ...entry, missingFiles: [] }));
  }, [sessionKey, setEntry]);
  const openFileDialog = useCallback(() => {
    if (!fileInputRefs || !sessionKey) return;
    const input = fileInputRefs.get(sessionKey)?.current;
    if (!input) return;
    input.value = "";
    input.click();
  }, [fileInputRefs, sessionKey]);

  return useMemo(
    () =>
      memory && sessionKey
        ? {
            add,
            clear,
            dismissMissingFiles,
            fileInputRef,
            files: entries?.[sessionKey]?.attachments ?? [],
            missingFiles: entries?.[sessionKey]?.missingFiles ?? [],
            openFileDialog,
            remove,
          }
        : undefined,
    [
      add,
      clear,
      dismissMissingFiles,
      fileInputRef,
      entries,
      memory,
      openFileDialog,
      remove,
      sessionKey,
    ],
  );
}

export function useOptionalChatSessionTextSelection() {
  const memory = useContext(ChatSessionMemoryContext);
  const sessionKey = useContext(ChatSessionMemoryKeyContext);
  const setEntry = memory?.setEntry;
  const selection =
    memory && sessionKey ? memory.entries[sessionKey]?.selection : undefined;
  const setSelection = useCallback(
    (next: ChatTextSelection) => {
      if (!setEntry || !sessionKey) return;
      setEntry(sessionKey, (entry) => ({ ...entry, selection: next }));
    },
    [sessionKey, setEntry],
  );

  return useMemo(
    () => (memory && sessionKey ? { selection, setSelection } : undefined),
    [memory, selection, sessionKey, setSelection],
  );
}

function revokeAttachmentUrl(attachment: FileUIPart) {
  if (attachment.url?.startsWith("blob:")) {
    URL.revokeObjectURL(attachment.url);
  }
}

function readAttachmentDrafts(
  storageKey: string | undefined,
): Record<string, AttachmentDraftMetadata[]> {
  if (!storageKey) return {};
  return readStorageJson(
    "local",
    storageKey,
    (value: unknown) => {
      if (!value || typeof value !== "object" || Array.isArray(value))
        return {};
      return Object.fromEntries(
        Object.entries(value).flatMap(([key, files]) => {
          if (!Array.isArray(files)) return [];
          const metadata = files.flatMap((file: unknown) => {
            if (
              !file ||
              typeof file !== "object" ||
              !("filename" in file) ||
              !("mediaType" in file) ||
              typeof file.filename !== "string" ||
              typeof file.mediaType !== "string"
            )
              return [];
            return [{ filename: file.filename, mediaType: file.mediaType }];
          });
          return metadata.length ? [[key, metadata]] : [];
        }),
      );
    },
    {},
  );
}

function saveAttachmentDraft(
  storageKey: string | undefined,
  targetKey: string,
  files: AttachmentDraftMetadata[],
) {
  if (!storageKey) return;
  const drafts = readAttachmentDrafts(storageKey);
  if (files.length) drafts[targetKey] = files;
  else delete drafts[targetKey];
  if (Object.keys(drafts).length) writeStorageJson("local", storageKey, drafts);
  else removeStorageItem("local", storageKey);
}
