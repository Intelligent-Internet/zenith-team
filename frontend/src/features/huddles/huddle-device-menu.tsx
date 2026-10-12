import { Check, ChevronUp } from "@/shared/ui/icons";
import { useEffect, useId, useRef, useState } from "react";

export function HuddleDeviceMenu({
  label,
  fallbackName,
  devices,
  selected,
  disabled,
  onSelect,
}: {
  label: string;
  fallbackName: string;
  devices: MediaDeviceInfo[];
  selected: string;
  disabled?: boolean;
  onSelect: (deviceId: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const menuId = useId();

  useEffect(() => {
    if (!open) return undefined;
    const close = (event: PointerEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) setOpen(false);
    };
    const escape = (event: KeyboardEvent) => {
      if (event.key === "Escape") setOpen(false);
    };
    document.addEventListener("pointerdown", close);
    document.addEventListener("keydown", escape);
    return () => {
      document.removeEventListener("pointerdown", close);
      document.removeEventListener("keydown", escape);
    };
  }, [open]);

  const options = [
    { id: "", name: "System default" },
    ...devices
      .filter((device) => device.deviceId && device.deviceId !== "default")
      .map((device, index) => ({
        id: device.deviceId,
        name: device.label || `${fallbackName} ${index + 1}`,
      })),
  ];

  return (
    <div className="huddle-device-menu" ref={rootRef}>
      <button
        type="button"
        className="huddle-device-trigger"
        aria-label={label}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={menuId}
        disabled={disabled}
        onClick={() => setOpen(!open)}
      >
        <ChevronUp size={14} />
      </button>
      {open && (
        <div id={menuId} className="huddle-device-popover" role="menu">
          <p>{label}</p>
          {options.map((option) => (
            <button
              key={option.id || "default"}
              type="button"
              role="menuitemradio"
              aria-checked={option.id === selected}
              onClick={() => {
                setOpen(false);
                if (option.id !== selected) onSelect(option.id);
              }}
            >
              <span>{option.name}</span>
              {option.id === selected && <Check size={15} />}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
