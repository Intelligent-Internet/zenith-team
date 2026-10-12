import * as Popover from "@radix-ui/react-popover";
import { useTheme } from "next-themes";
import { lazy, Suspense, useState, type ReactElement } from "react";
import {
  Clock3,
  Coffee,
  Flag,
  Globe,
  Hash,
  Lightbulb,
  Smile,
  Sparkles,
} from "@/shared/ui/icons";
import { Icon } from "@/shared/ui/icon";

import "@/shared/chat/chat-emoji-picker.css";

type EmojiSelectionHandler = (emoji: string, label: string) => void;

const EmojiPicker = lazy(async () => {
  const {
    default: Picker,
    Categories,
    EmojiStyle,
    SkinTonePickerLocation,
    SuggestionMode,
    Theme,
  } = await import("emoji-picker-react");

  return {
    default: function FullEmojiPicker({
      onSelect,
    }: {
      onSelect: EmojiSelectionHandler;
    }) {
      const { forcedTheme, resolvedTheme } = useTheme();

      return (
        <Picker
          autoFocusSearch
          categoryIcons={{
            [Categories.SUGGESTED]: <Clock3 size={20} />,
            [Categories.CUSTOM]: <Sparkles size={20} />,
            [Categories.SMILEYS_PEOPLE]: <Smile size={20} />,
            [Categories.ANIMALS_NATURE]: <Icon name="leaf" size={20} />,
            [Categories.FOOD_DRINK]: <Coffee size={20} />,
            [Categories.TRAVEL_PLACES]: <Globe size={20} />,
            [Categories.ACTIVITIES]: <Icon name="award" size={20} />,
            [Categories.OBJECTS]: <Lightbulb size={20} />,
            [Categories.SYMBOLS]: <Hash size={20} />,
            [Categories.FLAGS]: <Flag size={20} />,
          }}
          emojiStyle={EmojiStyle.NATIVE}
          height="100%"
          onEmojiClick={({ emoji, names }) =>
            onSelect(emoji, names[names.length - 1] ?? emoji)
          }
          previewConfig={{
            defaultCaption: "Pick an emoji",
            showPreview: true,
          }}
          searchPlaceholder="Search all emoji"
          skinTonePickerLocation={SkinTonePickerLocation.SEARCH}
          suggestedEmojisMode={SuggestionMode.RECENT}
          theme={
            (forcedTheme ?? resolvedTheme) === "dark" ? Theme.DARK : Theme.LIGHT
          }
          width="100%"
        />
      );
    },
  };
});

type ChatEmojiPickerProps = {
  children: ReactElement;
  onSelect: EmojiSelectionHandler;
  onOpenChange?: (open: boolean) => void;
  onCloseAutoFocus?: (event: Event) => void;
  label?: string;
  align?: "start" | "center" | "end";
};

export function ChatEmojiPicker({
  children,
  onSelect,
  onOpenChange,
  onCloseAutoFocus,
  label = "Choose an emoji",
  align = "end",
}: ChatEmojiPickerProps) {
  const [open, setOpen] = useState(false);
  const changeOpen = (nextOpen: boolean) => {
    setOpen(nextOpen);
    onOpenChange?.(nextOpen);
  };

  return (
    <Popover.Root open={open} onOpenChange={changeOpen}>
      <Popover.Trigger asChild>{children}</Popover.Trigger>
      <Popover.Portal>
        <Popover.Content
          align={align}
          aria-label={label}
          className="chat-emoji-picker"
          collisionPadding={12}
          onCloseAutoFocus={onCloseAutoFocus}
          side="top"
          sideOffset={8}
        >
          <Suspense
            fallback={
              <div className="chat-emoji-picker__loading" role="status">
                Loading emoji…
              </div>
            }
          >
            <EmojiPicker
              onSelect={(emoji, emojiLabel) => {
                onSelect(emoji, emojiLabel);
                changeOpen(false);
              }}
            />
          </Suspense>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
