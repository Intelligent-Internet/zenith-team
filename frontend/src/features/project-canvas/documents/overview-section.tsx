import { type ReactNode } from "react";
import { BlockSettings } from "./block-settings";
import { BlockViewSelect } from "./block-view-select";

export function OverviewSection({
  title,
  value,
  options,
  onChange,
  children,
}: {
  title: string;
  value: string;
  options: readonly (readonly [string, string])[];
  onChange: (value: string) => void;
  children: ReactNode;
}) {
  return (
    <section className="po-overview-section" aria-label={title}>
      <header className="po-overview-section-heading">
        <h2>{title}</h2>
        <BlockSettings
          label={`${title} view`}
          description="Only your view on this device."
        >
          <BlockViewSelect
            value={value}
            options={options}
            label={`${title} view`}
            onChange={onChange}
          />
        </BlockSettings>
      </header>
      {children}
    </section>
  );
}
