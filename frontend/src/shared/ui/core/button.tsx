import * as React from "react";
import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";

import { cn } from "./cn";

const buttonVariants = cva(
  "inline-flex items-center cursor-pointer justify-center gap-2 whitespace-nowrap rounded-full border border-transparent font-medium transition-all disabled:pointer-events-none disabled:opacity-50 [&_svg]:pointer-events-none [&_svg:not([class*='size-'])]:size-4 shrink-0 [&_svg]:shrink-0 outline-none focus-visible:!border-current",
  {
    variants: {
      variant: {
        default:
          "bg-brand-primary text-inverse hover:bg-brand-hover focus-visible:bg-brand-hover active:bg-brand-active",
        destructive:
          "bg-red-2 text-white hover:bg-red-2/90 focus-visible:bg-red-2/90",
        outline:
          "border border-brand-primary text-brand-primary bg-transparent hover:bg-state-hover-bg focus-visible:bg-state-hover-bg",
        secondary: "bg-secondary text-primary",
        ghost:
          "text-secondary hover:bg-state-hover-bg focus-visible:bg-state-hover-bg",
        link: "underline-offset-4 hover:underline focus-visible:underline",
      },
      size: {
        default: "h-10 px-4 py-2 has-[>svg]:px-3 text-sm",
        sm: "h-8 rounded-full gap-1.5 px-3 has-[>svg]:px-2.5 text-sm",
        lg: "h-10 rounded-full px-6 has-[>svg]:px-4 text-sm",
        xl: "h-12 rounded-full px-6 has-[>svg]:px-4 text-base",
        icon: "size-9",
        auto: "h-auto justify-start gap-0 whitespace-normal p-0 font-[inherit]",
      },
    },
    defaultVariants: {
      variant: "ghost",
      size: "default",
    },
  },
);

function Button({
  className,
  variant,
  size,
  asChild = false,
  ...props
}: React.ComponentProps<"button"> &
  VariantProps<typeof buttonVariants> & {
    asChild?: boolean;
  }) {
  const Comp = asChild ? Slot : "button";

  return (
    <Comp
      data-slot="button"
      data-variant={variant ?? "ghost"}
      data-size={size ?? "default"}
      className={cn(buttonVariants({ variant, size, className }))}
      {...props}
    />
  );
}

export { Button, buttonVariants };
