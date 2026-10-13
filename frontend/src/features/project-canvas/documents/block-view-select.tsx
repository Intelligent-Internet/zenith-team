import { Icon } from "@/shared/ui/icon";

export function BlockViewSelect({
  label = "Block view",
  value,
  options,
  onChange,
}: {
  label?: string;
  value: string;
  options: readonly (readonly [string, string])[];
  onChange: (value: string) => void;
}) {
  return (
    <label className="pc-view-select">
      <Icon name="setting-2" className="size-3.5" />
      <span>View</span>
      <select
        aria-label={label}
        value={value}
        onChange={(event) => onChange(event.target.value)}
      >
        {options.map(([key, label]) => (
          <option key={key} value={key}>
            {label}
          </option>
        ))}
      </select>
    </label>
  );
}
