import type { ComponentProps, ReactNode } from "react";
import { cx } from "@/shared/lib/cx";
import { Button } from "@/shared/ui/button";

type IconButtonProps = Omit<ComponentProps<typeof Button>, "children"> & {
  children: ReactNode;
};

export function IconButton({ className, children, ...props }: IconButtonProps) {
  return (
    <Button
      className={cx(
        "!grid !size-9 !min-w-9 place-items-center !justify-center !p-0 [&_svg]:!size-4",
        className,
      )}
      size="auto"
      variant="ghost"
      {...props}
    >
      {children}
    </Button>
  );
}
