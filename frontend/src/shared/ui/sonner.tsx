import type { CSSProperties } from "react";
import { useTheme } from "next-themes";
import { Toaster as Sonner, toast, type ToasterProps } from "sonner";
import { cx } from "@/shared/lib/cx";
import {
  CheckCircle2,
  CircleAlert,
  Info,
  LoaderCircle,
  TriangleAlert,
  X,
} from "@/shared/ui/icons";

type ToastStyle = CSSProperties & Record<`--${string}`, string>;

export function Toaster({ className, style, icons, ...props }: ToasterProps) {
  const { forcedTheme, resolvedTheme } = useTheme();
  const isDark = (forcedTheme ?? resolvedTheme) === "dark";

  return (
    <Sonner
      className={cx("toaster group zenith-team-toast", className)}
      theme={isDark ? "dark" : "light"}
      icons={{
        success: <CheckCircle2 size={20} />,
        info: <Info size={20} />,
        warning: <TriangleAlert size={20} />,
        error: <CircleAlert size={20} />,
        loading: <LoaderCircle size={20} className="animate-spin" />,
        close: <X size={16} />,
        ...icons,
      }}
      style={
        {
          "--normal-bg": "var(--workspace-overlay-bg, var(--popover))",
          "--normal-border": "var(--workspace-divider, var(--border))",
          "--normal-text":
            "var(--workspace-native-text, var(--popover-foreground))",
          "--success-bg":
            "var(--workspace-success-soft, color-mix(in srgb, var(--success-text) 10%, var(--normal-bg)))",
          "--success-border":
            "color-mix(in srgb, var(--success-text) 40%, var(--normal-bg))",
          "--success-text": `var(--workspace-native-success, ${isDark ? "#9bd568" : "#286544"})`,
          "--error-bg":
            "var(--workspace-danger-soft, color-mix(in srgb, var(--error-text) 10%, var(--normal-bg)))",
          "--error-border":
            "color-mix(in srgb, var(--error-text) 40%, var(--normal-bg))",
          "--error-text": `var(--workspace-native-danger, ${isDark ? "#f47b82" : "#b6404d"})`,
          "--warning-bg":
            "var(--workspace-approval-soft, color-mix(in srgb, var(--warning-text) 10%, var(--normal-bg)))",
          "--warning-border":
            "color-mix(in srgb, var(--warning-text) 40%, var(--normal-bg))",
          "--warning-text": `var(--workspace-native-approval, ${isDark ? "#ffd84d" : "#8b5a0a"})`,
          "--info-bg":
            "var(--workspace-info-soft, color-mix(in srgb, var(--info-text) 10%, var(--normal-bg)))",
          "--info-border":
            "color-mix(in srgb, var(--info-text) 40%, var(--normal-bg))",
          "--info-text": `var(--workspace-native-info, ${isDark ? "#67c7db" : "#3561a0"})`,
          ...style,
        } as ToastStyle
      }
      {...props}
    />
  );
}

export { toast };
