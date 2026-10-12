export type ChatSelectorLayout = "compact" | "wide";

export const chatSelectorContentPaddingClassName = {
  compact: "p-[14px]",
  wide: "p-[18px]",
} satisfies Record<ChatSelectorLayout, string>;

export const chatSelectorGridClassName = {
  compact: "grid-cols-2 gap-[10px]",
  wide: "grid-cols-2 gap-5",
} satisfies Record<ChatSelectorLayout, string>;

export const chatSelectorCheckIconOffsetClassName = {
  compact: "right-3 top-3 size-5",
  wide: "right-5 top-5 size-6",
} satisfies Record<ChatSelectorLayout, string>;

export const chatSelectorCheckIconSizeClassName = {
  compact: "size-5",
  wide: "size-6",
} satisfies Record<ChatSelectorLayout, string>;
