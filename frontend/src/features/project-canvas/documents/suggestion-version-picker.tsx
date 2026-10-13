import type { CSSProperties } from "react";
import { FoldAvatar } from "@/shared/ui/agent-avatar/fold-avatar";
import { Tooltip } from "@/shared/ui/tooltip";
import { ChevronDown } from "@/shared/ui/icons";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from "@/shared/ui/dropdown-menu";
import type { SuggestionVersionOption } from "./page-suggestion-points";

const petalRadius = 62;
const petalSize = 40;
const flowerSize = 2 * petalRadius + petalSize;

export function SuggestionVersionPicker({
  options,
  selectedId,
  disabled,
  onSelect,
}: {
  options: SuggestionVersionOption[];
  selectedId: string;
  disabled: boolean;
  onSelect: (id: string) => void;
}) {
  const selected = options.find((option) => option.id === selectedId);
  if (!selected) return null;
  const conflicts = options.filter((option) => option.conflicting);
  const petals = conflicts.filter((option) => option.id !== selectedId);
  // Keep crowded sets selectable instead of overlapping the avatar hit targets.
  const crowded = petals.length > 8;
  const hasCompetingVersions =
    options.length > 1 && options.some((option) => option.conflicting);
  const selectedLabel = `${selected.agentName}${selected.total > 1 ? ` · Version ${selected.number} of ${selected.total}` : ""}`;
  if (!hasCompetingVersions) {
    return (
      <Tooltip label={selectedLabel} side="left">
        <button
          type="button"
          className="pc-version-picker pc-version-picker-single"
          disabled={disabled}
          aria-label={`Review ${selected.agentName}’s suggestion`}
          onClick={() => onSelect(selectedId)}
        >
          <span className="pc-version-picker-avatar">
            <FoldAvatar agentId={selected.agentId} size={28} motion="still" />
          </span>
        </button>
      </Tooltip>
    );
  }
  return (
    <DropdownMenu modal={false}>
      <Tooltip label={selectedLabel} side="left">
        <DropdownMenuTrigger asChild>
          <button
            type="button"
            className="pc-version-picker"
            disabled={disabled}
            aria-label={`Choose suggestion version. ${selected.agentName} selected. ${options.length} ${options.length === 1 ? "version" : "versions"}.`}
          >
            <span className="pc-version-picker-avatar">
              <FoldAvatar agentId={selected.agentId} size={28} motion="still" />
            </span>
            <ChevronDown size={13} className="pc-version-picker-arrow" />
            {options.length > 1 && (
              <span className="pc-version-count">{options.length}</span>
            )}
          </button>
        </DropdownMenuTrigger>
      </Tooltip>
      <DropdownMenuContent
        align="center"
        sideOffset={crowded ? 8 : -(flowerSize + 38) / 2}
        collisionPadding={12}
        style={{ "--flower-size": `${flowerSize}px` } as CSSProperties}
        className={`pc-version-menu ${crowded ? "pc-version-menu-crowded" : ""} !min-w-0 !overflow-visible !border-0 !bg-transparent !p-0 !shadow-none !animate-none`}
      >
        <DropdownMenuRadioGroup
          className="pc-version-nodes"
          value={selectedId}
          onValueChange={onSelect}
        >
          {petals.map((option, index) => (
            <Tooltip
              key={option.id}
              label={`${option.agentName}${option.total > 1 ? ` · Version ${option.number} of ${option.total}` : ""}`}
              side="left"
              openDelay={100}
            >
              <DropdownMenuRadioItem
                value={option.id}
                disabled={disabled}
                className="pc-version-option"
                style={
                  {
                    "--petal-x": `${Math.cos(-Math.PI / 2 + (index * 2 * Math.PI) / petals.length) * petalRadius}px`,
                    "--petal-y": `${Math.sin(-Math.PI / 2 + (index * 2 * Math.PI) / petals.length) * petalRadius}px`,
                    "--petal-delay": `${index * 25}ms`,
                  } as CSSProperties
                }
                aria-label={`${option.agentName}${option.total > 1 ? `, version ${option.number} of ${option.total}` : ""}`}
              >
                <FoldAvatar agentId={option.agentId} size={28} motion="still" />
              </DropdownMenuRadioItem>
            </Tooltip>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
