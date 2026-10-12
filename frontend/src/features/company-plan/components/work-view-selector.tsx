import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import {
  Check,
  ChevronDown,
  Columns3,
  GitBranch,
  PanelLeft,
  Table2,
} from "@/shared/ui/icons";
import type { WorkView } from "@/state";

const views = [
  {
    id: "board",
    label: "Board",
    icon: Columns3,
    description: "Tasks grouped into status columns",
  },
  {
    id: "table",
    label: "Table",
    icon: Table2,
    description: "Compare task details in rows",
  },
  {
    id: "next_up",
    label: "Focus",
    icon: PanelLeft,
    description: "Decisions and the next actionable work",
  },
  {
    id: "dependencies",
    label: "Dependencies",
    icon: GitBranch,
    description: "See prerequisites and what they unlock",
  },
] as const;

export function WorkViewSelector({
  value,
  onChange,
}: {
  value: WorkView;
  onChange: (view: WorkView) => void;
}) {
  const current = views.find((view) => view.id === value) ?? views[0];
  return (
    <DropdownMenu.Root>
      <DropdownMenu.Trigger
        className="work-view-selector"
        aria-label={`View mode, ${current.label}`}
      >
        <current.icon size={16} />
        {current.label}
        <ChevronDown size={13} />
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content
          className="workspace-dashboard-menu"
          align="end"
          sideOffset={7}
          collisionPadding={12}
        >
          <DropdownMenu.Label>View mode</DropdownMenu.Label>
          <DropdownMenu.RadioGroup
            value={value}
            onValueChange={(id) => {
              const view = views.find((view) => view.id === id);
              if (view) onChange(view.id);
            }}
          >
            {views.map((view) => (
              <DropdownMenu.RadioItem key={view.id} value={view.id}>
                <view.icon size={17} />
                <span>
                  {view.label}
                  <small>{view.description}</small>
                </span>
                {value === view.id ? <Check size={15} /> : null}
              </DropdownMenu.RadioItem>
            ))}
          </DropdownMenu.RadioGroup>
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}
