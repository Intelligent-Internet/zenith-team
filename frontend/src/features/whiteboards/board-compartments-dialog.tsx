import { useState } from "react";
import type { BoardItem } from "./board-api";
import { boardFragmentTag } from "./board-shape";

export type BoardCompartmentsChange = {
  title: string;
  sections: string[];
  dividers?: number[];
};

const maxSections = 4;
const sectionNames: Record<string, string[]> = {
  class: ["Attributes", "Methods", "Section 3", "Section 4"],
  entity: ["Columns", "Section 2", "Section 3", "Section 4"],
};

/**
 * Edits a class's or entity's name and sections, one member or column per
 * line, or a fragment's operator and its operands: each operand's guard, and
 * for later operands where their dashed line sits below the fragment's top.
 */
export function BoardCompartmentsDialog({
  item,
  onSave,
  onClose,
}: {
  item: BoardItem;
  onSave: (change: BoardCompartmentsChange) => void;
  onClose: () => void;
}) {
  const shape = item.properties.shape ?? "class";
  const fragment = shape === "fragment";
  const [title, setTitle] = useState(item.properties.title ?? "");
  const [sections, setSections] = useState<string[]>(
    item.properties.sections?.length ? item.properties.sections : [""],
  );
  const [dividers, setDividers] = useState<string[]>(
    (item.properties.dividers ?? []).map((divider) =>
      String(Math.round(divider)),
    ),
  );
  const [error, setError] = useState("");
  const names = sectionNames[shape] ?? sectionNames.class;
  const setSection = (index: number, value: string) =>
    setSections((current) =>
      current.map((section, i) => (i === index ? value : section)),
    );
  const add = () => {
    if (sections.length >= maxSections) return;
    if (fragment) {
      const last = Number(dividers.at(-1) ?? boardFragmentTag);
      setDividers([
        ...dividers,
        String(Math.round((last + Math.max(item.height, last + 48)) / 2)),
      ]);
      setSections([...sections, "[else]"]);
    } else setSections([...sections, ""]);
  };
  const remove = (index: number) => {
    setSections(sections.filter((_, i) => i !== index));
    if (fragment)
      setDividers(dividers.filter((_, i) => i !== Math.max(0, index - 1)));
  };
  return (
    <div className="wb-modal-backdrop">
      <form
        className="wb-dialog wb-compartments-dialog"
        role="dialog"
        aria-modal="true"
        aria-label={fragment ? "Edit fragment" : `Edit ${shape}`}
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            event.stopPropagation();
            onClose();
          }
        }}
        onSubmit={(event) => {
          event.preventDefault();
          if (!fragment) {
            onSave({ title, sections });
            return;
          }
          const lines = dividers.map(Number);
          if (
            lines.some(
              (line, i) =>
                !Number.isFinite(line) ||
                line <= (i ? lines[i - 1] : 0) ||
                line > 100000,
            )
          ) {
            setError(
              "Each operand's line must sit below the one before it, inside the fragment.",
            );
            return;
          }
          onSave({ title, sections, dividers: lines });
        }}
      >
        <h2>{fragment ? "Edit fragment" : `Edit ${shape}`}</h2>
        <label>
          {fragment ? "Operator" : shape === "entity" ? "Table" : "Name"}
          <input
            autoFocus
            maxLength={500}
            value={title}
            placeholder={fragment ? "alt, opt, loop, par…" : undefined}
            onChange={(event) => setTitle(event.target.value)}
          />
        </label>
        {sections.map((section, index) => (
          <fieldset key={index} className="wb-compartment-field">
            <div className="wb-compartment-field__head">
              <span>
                {fragment
                  ? index === 0
                    ? "First operand guard"
                    : `Operand ${index + 1} guard`
                  : names[index]}
              </span>
              {sections.length > 1 && (fragment ? index > 0 : true) && (
                <button
                  type="button"
                  aria-label={`Remove ${fragment ? `operand ${index + 1}` : names[index]}`}
                  onClick={() => remove(index)}
                >
                  Remove
                </button>
              )}
            </div>
            {fragment ? (
              <input
                aria-label={`Operand ${index + 1} guard`}
                maxLength={2000}
                value={section}
                placeholder="[condition]"
                onChange={(event) => setSection(index, event.target.value)}
              />
            ) : (
              <textarea
                aria-label={names[index]}
                maxLength={2000}
                value={section}
                placeholder={
                  shape === "entity"
                    ? "PK id uuid\nemail text"
                    : index === 0
                      ? "- id: UUID\n- total: Money"
                      : "+ place(): void"
                }
                onChange={(event) => setSection(index, event.target.value)}
              />
            )}
            {fragment && index > 0 && (
              <label>
                Line below the fragment's top
                <input
                  type="number"
                  min={1}
                  step={1}
                  value={dividers[index - 1] ?? ""}
                  onChange={(event) =>
                    setDividers(
                      dividers.map((divider, i) =>
                        i === index - 1 ? event.target.value : divider,
                      ),
                    )
                  }
                />
              </label>
            )}
          </fieldset>
        ))}
        {sections.length < maxSections && (
          <button type="button" className="wb-compartment-add" onClick={add}>
            {fragment ? "Add operand" : "Add section"}
          </button>
        )}
        {error && <p role="alert">{error}</p>}
        <footer>
          <button type="button" onClick={onClose}>
            Cancel
          </button>
          <button className="wb-primary">Save</button>
        </footer>
      </form>
    </div>
  );
}
