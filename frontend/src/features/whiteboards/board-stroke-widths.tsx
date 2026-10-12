export function BoardStrokeWidths({
  width,
  widths,
  highlighter = false,
  onChange,
  disabled = false,
}: {
  width: number;
  widths: number[];
  highlighter?: boolean;
  onChange: (width: number) => void;
  disabled?: boolean;
}) {
  return (
    <div className="wb-stroke-widths" role="group" aria-label="Stroke width">
      {widths.map((value) => (
        <button
          key={value}
          type="button"
          aria-label={`Stroke width: ${value} px`}
          aria-pressed={width === value}
          title={`${value} px`}
          disabled={disabled}
          onClick={() => onChange(value)}
        >
          <span
            style={{ height: Math.min(24, highlighter ? value / 2 : value) }}
            aria-hidden="true"
          />
        </button>
      ))}
    </div>
  );
}
