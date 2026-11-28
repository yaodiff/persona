// The file-type glyph the files panel and the palette's file results share. Its own file, so the palette (an idle-tier
// page) does not carry the rest of the panel parts in parts.tsx (#17321).
import { glyphOf } from "../../../lib/files";
import { Icon } from "../../../ui/icons";

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

/** The icon of a name in a listing: restrained neutral folder and file glyphs by kind. */
export function FileGlyph({ name, dir, size = 16 }: { name: string; dir?: boolean; size?: number }) {
  if (dir) return <Icon name="folder" size={size} className="shrink-0 text-ink-3" />;
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
