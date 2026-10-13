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
          className="chat-emoji-picker z-[360] w-[min(352px,_calc(100vw_-_24px))] h-[min(_440px,_var(--radix-popover-content-available-height,_440px),_calc(100dvh_-_24px)_)] overflow-hidden [border:1px_solid_var(--workspace-divider-strong,_var(--input-border))] rounded-[10px] [background:var(--workspace-overlay-bg,_var(--popover))] text-[var(--workspace-native-text,_var(--popover-foreground))] [box-shadow:var(--workspace-overlay-shadow,_0_8px_28px_rgb(0_0_0_/_18%))] [outline:none]"
          collisionPadding={12}
          onCloseAutoFocus={onCloseAutoFocus}
          side="top"
          sideOffset={8}
        >
          <Suspense
            fallback={
              <div
                className="grid h-full place-items-center text-[var(--workspace-native-text-muted,_var(--text-secondary))] text-[length:13px]"
                role="status"
              >
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
