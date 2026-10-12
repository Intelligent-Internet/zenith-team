import type { CSSProperties, Dispatch, SetStateAction } from "react";
import type { blocks } from "./document-block-commands";

type BlockMenu = {
  items: typeof blocks;
  choice: number;
  pick: (index: number) => void;
};

export function handleBlockMenuKey(
  event: KeyboardEvent,
  menu: BlockMenu & { open: boolean },
  setChoice: Dispatch<SetStateAction<number>>,
  close: () => void,
) {
  if (!menu.open) return false;
  if (event.key === "Escape") {
    close();
    return true;
  }
  if (event.key === "ArrowDown" || event.key === "ArrowUp") {
    setChoice(
      (current) =>
        (current + (event.key === "ArrowDown" ? 1 : -1) + menu.items.length) %
        Math.max(1, menu.items.length),
    );
    return true;
  }
  if (event.key === "Enter" && menu.items.length) {
    menu.pick(menu.choice);
    return true;
  }
  return false;
}

export function DocumentBlockMenu({
  items,
  choice,
  pick,
  position,
}: BlockMenu & { position: CSSProperties }) {
  return (
    <div
      className="pc-slash-menu"
      style={position}
      role="listbox"
      aria-label="Insert content"
    >
      <div className="pc-menu-caption">Insert</div>
      {items.length === 0 && <p className="pc-menu-empty">No matching items</p>}
      {items.map((block, index) => (
        <button
          role="option"
          aria-selected={index === choice}
          key={`${block.type}:${index}`}
          onMouseDown={(event) => event.preventDefault()}
          onClick={() => pick(index)}
        >
          {block.label}
        </button>
      ))}
    </div>
  );
}
