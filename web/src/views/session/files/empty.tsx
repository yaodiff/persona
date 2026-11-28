// The empty state the files / Git panels and the app shell share. Its own file, so the shell's first screen does not
// carry the rest of the panel parts in parts.tsx (#17321).
import { Icon, type IconName } from "../../../ui/icons";

export function Empty({ icon, title, detail, action }: { icon: IconName; title: string; detail?: React.ReactNode; action?: React.ReactNode }) {
  return (
    <div className="flex flex-1 flex-col items-center justify-center gap-2 px-6 py-12 text-center" style={{ animation: "fade-in 200ms ease-out both" }}>
      <span className="mb-1 flex size-10 items-center justify-center rounded-full bg-field text-ink-3">
        <Icon name={icon} size={18} />
      </span>
      <div className="text-[13.5px] font-medium text-ink">{title}</div>
      {/* words wrap between words; only a string too long for a line on its own (a path, an id) breaks inside it */}
      {detail && <div className="max-w-80 text-[12px] leading-relaxed [overflow-wrap:anywhere] text-ink-3">{detail}</div>}
      {action && <div className="mt-2">{action}</div>}
    </div>
  );
}
