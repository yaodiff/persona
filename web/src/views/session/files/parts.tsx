// Small pieces the files / Git panels and the file viewer share: a sub-view header with a back button, status
// badges, a two-way switch, empty states, file glyphs, and a windowed list for long code / diffs.
import { useEffect, useLayoutEffect, useState } from "react";
import { gitTone, glyphOf } from "../../../lib/files";
import { cx, IconButton } from "../../../ui/atoms";
import { Icon, type IconName } from "../../../ui/icons";
import { t } from "../../../i18n";
import { SegmentedTrack } from "../../../ui/Segmented";

/** A panel header whose leading square is a back button (sub-views); PanelHeader's look otherwise. */
export function BackHeader({
  title,
  subtitle,
  actions,
  onBack,
  onClose,
}: {
  title: React.ReactNode;
  subtitle?: React.ReactNode;
  actions?: React.ReactNode;
  onBack: () => void;
  onClose: () => void;
}) {
  return (
    <header className="flex h-14 shrink-0 items-center gap-2 border-b border-line px-3">
      <IconButton icon="chevronLeft" label={t("session-files-parts.back")} onClick={onBack} iconSize={16} />
      <div className="min-w-0 flex-1">
        <div className="truncate text-[13.5px] font-semibold text-ink">{title}</div>
        {subtitle && <div className="truncate text-[11.5px] text-ink-3">{subtitle}</div>}
      </div>
      {actions}
      <IconButton icon="x" label={t("session-files-parts.close_panel")} onClick={onClose} />
    </header>
  );
}

const TONE: Record<ReturnType<typeof gitTone>, { badge: string; dot: string; label: string }> = {
  add: { badge: "bg-green-tint text-green", dot: "bg-green", label: t("session-files-parts.tone_added") },
  del: { badge: "bg-red-tint text-red", dot: "bg-red", label: t("session-files-parts.tone_deleted") },
  moved: { badge: "bg-field text-ink-2", dot: "bg-ink-3", label: t("session-files-parts.tone_moved") },
  changed: { badge: "bg-orange-tint text-orange", dot: "bg-orange", label: t("session-files-parts.tone_changed") },
};

const LETTER: Record<string, string> = {
  M: t("session-files-parts.modified"),
  A: t("session-files-parts.added"),
  D: t("session-files-parts.deleted"),
  R: t("session-files-parts.renamed"),
  C: t("session-files-parts.copied"),
  U: t("session-files-parts.untracked"),
  "!": t("session-files-parts.conflict"),
  T: t("session-files-parts.type_changed"),
};

/** A change's letter, colored like editors do. */
export function GitBadge({ letter }: { letter: string }) {
  return (
    <span
      title={LETTER[letter] ?? letter}
      className={cx("inline-flex size-[18px] shrink-0 items-center justify-center rounded-[5px] font-mono text-[10.5px] font-bold", TONE[gitTone(letter)].badge)}
    >
      {letter}
    </span>
  );
}

/** A folder that holds a change: a dot in the change's color. */
export function GitDot({ letter }: { letter: string }) {
  return <span role="img" aria-label={t("session-files-parts.contains", { change: LETTER[letter] ?? t("session-files-parts.changes") })} className={cx("size-1.5 shrink-0 rounded-full", TONE[gitTone(letter)].dot)} />;
}

/** Two (or a few) ways to show the same thing; single-line labels. */
export function Segmented<T extends string>({ value, options, onChange, label }: { value: T; options: [T, string][]; onChange: (v: T) => void; label: string }) {
  return (
    <SegmentedTrack role="tablist" label={label} className="flex h-7 shrink-0 items-center rounded-[8px] p-0.5" thumbClassName="rounded-[6px]">
      {options.map(([v, text]) => (
        <button
          key={v}
          type="button"
          role="tab"
          aria-selected={v === value}
          data-selected={v === value}
          onClick={() => onChange(v)}
          className={cx(
            "relative z-[1] h-6 whitespace-nowrap rounded-[6px] px-2 text-[12px] font-medium transition-colors duration-150",
            v === value ? "text-ink" : "text-ink-3 hover:text-ink",
          )}
        >
          {text}
        </button>
      ))}
    </SegmentedTrack>
  );
}

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

// File-type glyphs not in the shared icon set (same 24-unit stroke grammar as ui/icons.tsx).
const GLYPH = {
  code: <path d="m16 18 6-6-6-6M8 6l-6 6 6 6" />,
  sheet: (
    <>
      <rect x="3" y="3" width="18" height="18" rx="2" />
      <path d="M3 9h18M3 15h18M9 3v18" />
    </>
  ),
  data: <path d="M8 3H7a2 2 0 0 0-2 2v5a2 2 0 0 1-2 2 2 2 0 0 1 2 2v5a2 2 0 0 0 2 2h1M16 21h1a2 2 0 0 0 2-2v-5a2 2 0 0 1 2-2 2 2 0 0 1-2-2V5a2 2 0 0 0-2-2h-1" />,
};

/** The icon of a name in a listing: folders filled in the accent color, files by kind. */
export function FileGlyph({ name, dir, size = 16 }: { name: string; dir?: boolean; size?: number }) {
  if (dir) return <Icon name="folder" size={size} className="shrink-0 text-accent" style={{ fill: "color-mix(in srgb, currentColor 18%, transparent)" }} />;
  const g = glyphOf(name);
  if (g === "code" || g === "data" || g === "sheet") {
    return (
      <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.9} strokeLinecap="round" strokeLinejoin="round" aria-hidden className="shrink-0 text-ink-3">
        {GLYPH[g]}
      </svg>
    );
  }
  return <Icon name={g === "image" ? "image" : g === "doc" ? "book" : "read"} size={size} className="shrink-0 text-ink-3" />;
}

/**
 * Rows [start, end) of a fixed-height list worth rendering in a scroller: what is on screen plus some margin, moved in
 * steps so scrolling re-renders every few rows, not on every pixel. `top` is where the rows start inside the scroller.
 */
export function useWindow(scroller: React.RefObject<HTMLElement | null>, count: number, rowH: number, top = 0): [number, number] {
  const [win, setWin] = useState<[number, number]>([0, Math.min(count, 96)]);
  useLayoutEffect(() => {
    const el = scroller.current;
    if (!el) return;
    let raf = 0;
    const STEP = 16,
      OVER = 40;
    const update = () => {
      raf = 0;
      const first = Math.floor((el.scrollTop - top) / rowH);
      const s = Math.max(0, Math.floor((first - OVER) / STEP) * STEP);
      const e = Math.min(count, Math.ceil((first + el.clientHeight / rowH + OVER) / STEP) * STEP);
      setWin((w) => (w[0] === s && w[1] === e ? w : [s, e]));
    };
    const later = () => {
      if (!raf) raf = requestAnimationFrame(update);
    };
    update();
    el.addEventListener("scroll", later, { passive: true });
    const ro = new ResizeObserver(later);
    ro.observe(el);
    return () => {
      el.removeEventListener("scroll", later);
      ro.disconnect();
      cancelAnimationFrame(raf);
    };
  }, [scroller, count, rowH, top]);
  // the count can shrink before the effect above catches up (formatted JSON → its one-line source)
  return [Math.min(win[0], count), Math.min(win[1], count)];
}

/** A boolean kept in this browser (like the app's @AppStorage). */
export function useStoredFlag(key: string, initial: boolean): [boolean, (v: boolean) => void] {
  const [v, setV] = useState(() => {
    const s = localStorage.getItem(key);
    return s === null ? initial : s === "1";
  });
  useEffect(() => {
    localStorage.setItem(key, v ? "1" : "0");
  }, [key, v]);
  return [v, setV];
}
