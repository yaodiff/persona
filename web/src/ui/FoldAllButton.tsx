// The fold-all button (docs/design/DESIGN_SYSTEM.md 3.4): the Agents title row's and a to-do's Decisions header's
// (#6447). Its icon and label name the next action: corners in (`foldToggle`) for Collapse all, out (`unfoldToggle`,
// `expand`) for Expand all. The label shows in a tooltip under the button, on hover after a short pause and on
// keyboard focus, with no second line: the owner found "Collapse all" / "Expand all" needs no explaining. Its parent is
// the tooltip's positioning box (`relative`), and the tooltip follows the button so the `peer` variants reach it.
import { cx } from "./atoms";
import { Icon } from "./icons";

// shown 300 ms after the pointer rests (`visibility` switches after the delay), at once on keyboard focus
const TIP =
  "pointer-events-none invisible absolute top-full right-0 z-30 mt-1 w-max max-w-60 rounded-control bg-tooltip-bg px-2.5 py-1.5 text-xs leading-snug text-tooltip-fg shadow-overlay transition-[visibility] peer-hover/fold:visible peer-hover/fold:delay-300 peer-focus-visible/fold:visible";

export function FoldAllButton({ label, expand, onClick, className, testId }: { label: string; expand: boolean; onClick: () => void; className?: string; testId?: string }) {
  return (
    <>
      <button type="button" aria-label={label} aria-expanded={!expand} data-testid={testId} onClick={onClick} className={cx("peer/fold", className)}>
        <Icon name={expand ? "unfoldToggle" : "foldToggle"} size={12} stroke={2} />
      </button>
      {/* the button's own name, already spoken through aria-label */}
      <span aria-hidden data-testid={testId && `${testId}-tip`} className={TIP}>{label}</span>
    </>
  );
}
