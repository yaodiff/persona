// The web terminal's clipboard, the pure parts (docs/integrations/terminal.md section 18). Pasting or dropping a file: which files a
// clipboard / drop carries, the name they are uploaded under, whether the pane they were meant for is still the one
// that is driven when the upload ends, and the line of escaped paths that is then typed into the pane. Copying: what
// to say after a copy and what a program's OSC 52 write may put on the clipboard. The browser's side (the upload
// request, reading and writing the clipboard) is in views/session/terminal/clipboard.ts.
import { t } from "../i18n";
import { escapePastePath } from "./paste-path";

export { dragHasFiles, escapePastePath } from "./paste-path";

/** The hub's limit for a terminal upload (`proto.MaxTerminalUploadBytes`). */
export const MAX_TERMINAL_UPLOAD = 50 * 1024 * 1024;

/** What goes into the terminal for uploaded files: escaped paths, space-separated, and a space after the last. */
export function pastedPaths(paths: string[]): string {
  return paths.map(escapePastePath).join(" ") + " ";
}

/** The files of a paste or a drop: `files`, else the `file` items (some browsers only fill those). */
export function transferFiles(data: Pick<DataTransfer, "files" | "items"> | null | undefined): File[] {
  if (!data) return [];
  const files = Array.from(data.files ?? []);
  if (files.length) return files;
  const out: File[] = [];
  for (const item of Array.from(data.items ?? [])) {
    if (item.kind !== "file") continue;
    const f = item.getAsFile();
    if (f) out.push(f);
  }
  return out;
}

const MIME_EXT: Record<string, string> = {
  "image/png": ".png",
  "image/jpeg": ".jpg",
  "image/gif": ".gif",
  "image/webp": ".webp",
  "image/heic": ".heic",
  "image/heif": ".heif",
  "image/bmp": ".bmp",
  "image/tiff": ".tiff",
  "image/svg+xml": ".svg",
  "application/pdf": ".pdf",
};

/**
 * The name a file is uploaded under: its own, and for a clipboard image that has none (or none with an extension) a
 * name from its type, so the agent that opens the path knows what it is. The machine cleans the name again.
 */
export function uploadName(file: Pick<File, "name" | "type">): string {
  const name = file.name.trim();
  const ext = MIME_EXT[file.type.toLowerCase()];
  if (!name) return `image${ext ?? ""}`;
  if (ext && !/\.[A-Za-z0-9]{1,8}$/.test(name)) return name + ext;
  return name;
}

/** Why a file is not uploaded, in words for the toast; null when it may go. */
export function refuseUpload(file: Pick<File, "name" | "size">): string | null {
  if (file.size <= 0) return t("lib-terminal-clipboard.empty_file", { name: file.name || t("lib-terminal-clipboard.file") });
  if (file.size > MAX_TERMINAL_UPLOAD) return t("lib-terminal-clipboard.too_large", { name: file.name || t("lib-terminal-clipboard.file"), mb: MAX_TERMINAL_UPLOAD >> 20 });
  return null;
}

/** How long an upload may take: a minute and 10 ms per kilobyte (a slow link carries 100 KB/s). */
export function uploadTimeoutMs(size: number): number {
  return 60_000 + Math.ceil(size / 100);
}

/** What the page holds when an upload ends. A feed belongs to one pane: leaving it (another session, the panel closed) disposes it. */
export interface PasteState {
  disposed: boolean;
  /** Whether the keys still go into the pane ("Take control" active, not watching). */
  driving: boolean;
}

/**
 * Whether the paths may still be typed when an upload ends: only into the pane the files were meant for ("changed"
 * once its feed is gone: nothing is pasted into whatever is open now), and only while this browser still drives it
 * ("watching": taken over, or switched to "View only", meanwhile).
 */
export function pasteVerdict(now: PasteState): "ok" | "changed" | "watching" {
  if (now.disposed) return "changed";
  return now.driving ? "ok" : "watching";
}

/** "Uploading shot.png" / "Uploading shot.png and 3 more files". */
export function uploadingText(names: string[]): string {
  if (names.length <= 1) return t("lib-terminal-clipboard.uploading_single", { name: names[0] ?? t("lib-terminal-clipboard.file") });
  return t("lib-terminal-clipboard.uploading_many", { name: names[0], count: names.length });
}

// ── copying ──

/** wrote: the async clipboard took it. legacy: only the old execCommand way worked. failed: neither did. */
export type CopyOutcome = "wrote" | "legacy" | "failed";

export interface ClipboardEnv {
  /** navigator.clipboard.writeText, where the page may use it (HTTPS, localhost); undefined where it may not. */
  writeText?: (text: string) => Promise<void>;
  /** The textarea + execCommand("copy") way; whether it worked. */
  legacyCopy: (text: string) => boolean;
}

/** Puts text on the clipboard: the async API first, then the old way (a refused write, a page without HTTPS). */
export async function copyWith(text: string, env: ClipboardEnv): Promise<CopyOutcome> {
  if (env.writeText) {
    try {
      await env.writeText(text);
      return "wrote";
    } catch {
      // no permission or focus: the old way may still work inside the same key press
    }
  }
  try {
    return env.legacyCopy(text) ? "legacy" : "failed";
  } catch {
    return "failed";
  }
}

/** What to tell the user after a copy; a page without HTTPS says so, since that is why the browser holds back. */
export function copyNotice(outcome: CopyOutcome, secure: boolean): { text: string; tone: "ok" | "error" } {
  if (outcome === "wrote") return { text: t("lib-terminal-clipboard.copied"), tone: "ok" };
  if (outcome === "legacy")
    return { text: secure ? t("lib-terminal-clipboard.copied") : t("lib-terminal-clipboard.copied_legacy"), tone: "ok" };
  return { text: secure ? t("lib-terminal-clipboard.copy_denied") : t("lib-terminal-clipboard.copy_insecure"), tone: "error" };
}

/** The most text a program may put on the clipboard with OSC 52. */
export const OSC52_MAX_CHARS = 1 << 20;

/** The text of an OSC 52 write to copy; null for nothing (an empty or undecodable payload must not clear the clipboard) or too much. */
export function osc52Text(text: string): string | null {
  return text.length > 0 && text.length <= OSC52_MAX_CHARS ? text : null;
}
