type TitlebarUnreadBadgeProps = {
  count: number;
};

export function TitlebarUnreadBadge({ count }: TitlebarUnreadBadgeProps) {
  if (count <= 0) {
    return null;
  }

  return (
    <span
      aria-hidden="true"
      className="absolute -right-1 -top-[2px] grid min-w-4 place-items-center rounded-full bg-[var(--workspace-notification-bg,#b6404d)] px-1 text-[10px] font-bold leading-4 text-[var(--workspace-notification-foreground,#fff)] shadow-[0_6px_12px_rgba(0,0,0,0.25)]"
    >
      {count > 9 ? "9+" : count}
    </span>
  );
}
