// Pure logic of a session's files / Git panels and the file viewer (docs/apple/ios.md "File viewer", the session page's
// "Files" / Git; iOS: FilesPane, GitPane, FileViewer, Persona/Code). No DOM here: tested in files.test.ts.

import { BASE } from "./base";
import { decodeURIComponentSafe } from "./uri";

// ── wire types (docs/protocol/protocol.md: /machines/{mid}/fs, /file, /git/*) ─────────

export interface FsEntry {
  name: string;
  dir: boolean;
  symlink?: boolean;
  size: number;
  mtime?: string;
  /** Git ignores it (the tree dims it); only set inside a repository. */
  ignored?: boolean;
  /** A symlink's text and whether it leads nowhere (hub feature `file_info`, docs/protocol/protocol.md "File details and links"). */
  link_target?: string;
  link_broken?: boolean;
}

/** One path's details (`/stat?detail=1`, the Info dialog); an older node sends only path, kind and size. */
export interface FileInfo {
  path: string;
  /** file, dir, link (a broken symlink), denied, missing */
  kind: string;
  size?: number;
  /** Permission bits as chmod takes them. */
  mode?: number;
  owner?: string;
  group?: string;
  mtime?: string;
  ctime?: string;
  birthtime?: string;
  items?: number;
  items_more?: boolean;
  symlink?: boolean;
  link_target?: string;
  link_real?: string;
  link_broken?: boolean;
}

export interface FsListing {
  path: string;
  entries: FsEntry[];
  truncated: boolean;
}

export interface FileContent {
  path: string;
  name: string;
  size: number;
  mime?: string;
  text?: string;
  data?: string;
  truncated: boolean;
  is_dir: boolean;
  /** A directory's first 500 names; folders end with "/". */
  entries?: string[];
  offset?: number;
  /** Where the next page starts; 0 / missing: the end. */
  next?: number;
  /** Asked with stream=1 and binary: no `data`; the bytes come from fileDownloadURL (Range), `revision` pins them. */
  stream?: boolean;
  revision?: string;
}

export interface GitFile {
  path: string;
  orig_path?: string;
  /** The staged change ("." none, "?" untracked); in a commit, the change kind. */
  index: string;
  worktree: string;
  added: number;
  deleted: number;
  binary?: boolean;
}

export interface GitCommit {
  hash: string;
  short: string;
  subject: string;
  body?: string;
  author: string;
  date?: string;
  refs?: string;
}

export interface GitStatus {
  is_repo: boolean;
  root?: string;
  branch?: string;
  head?: GitCommit;
  upstream?: string;
  ahead?: number;
  behind?: number;
  files: GitFile[];
  stashes?: number;
  commits: GitCommit[];
  /** The upstream was deleted on the remote: ahead / behind are 0 and mean nothing, a push publishes the branch again. */
  gone?: boolean;
}

export interface GitDiff {
  path: string;
  diff: string;
  truncated: boolean;
}

export interface GitShow {
  commit: GitCommit;
  files: GitFile[];
  diff: string;
  truncated: boolean;
}

// ── paths ─────────────────────────────────────────────────

export function joinPath(dir: string, name: string): string {
  if (!dir) return name;
  return dir.replace(/\/+$/, "") + "/" + name.replace(/^\/+/, "");
}

// A path from a Windows computer: a drive letter or a UNC share, the same test as baseName's in lib/format.ts. Only
// there is a backslash a separator (the node answers such paths with backslashes); in any other path it is a character
// of a name.
const WINDOWS_PATH = /^[A-Za-z]:[\\/]|^\\\\/;

/** `p` without separators at its end, and where its last separator is now (-1: none). */
function lastPart(p: string): { t: string; at: number } {
  if (!WINDOWS_PATH.test(p)) {
    const t = p.replace(/\/+$/, "");
    return { t, at: t.lastIndexOf("/") };
  }
  const t = p.replace(/[\\/]+$/, "");
  return { t, at: Math.max(t.lastIndexOf("/"), t.lastIndexOf("\\")) };
}

/** The folder `p` is in: "/" for a file at the root, a Windows drive's root with its separator (`C:\`), "" for a bare name. */
export function dirName(p: string): string {
  const { t, at } = lastPart(p);
  if (at < 0) return "";
  if (at === 0) return "/";
  const dir = t.slice(0, at);
  return /^[A-Za-z]:$/.test(dir) ? t.slice(0, at + 1) : dir;
}

export function fileName(p: string): string {
  const { t, at } = lastPart(p);
  return t.slice(at + 1) || p;
}

/** The folders of `path` below `root` (["a", "b"] for root/a/b), [] for root itself, null when it is not inside. */
export function below(root: string, path: string): string[] | null {
  const r = root.replace(/\/+$/, ""),
    p = path.replace(/\/+$/, "");
  if (p === r) return [];
  if (!p.startsWith(r + "/")) return null;
  return p.slice(r.length + 1).split("/").filter(Boolean);
}

/** Whether `ref` starts with a Windows drive letter and separator (`C:\…` or `C:/…`). */
export function isWindowsDrivePath(ref: string): boolean {
  return /^[A-Za-z]:[\\/]/.test(ref.trim());
}

/** Whether `ref` is an internal hub API route (`/api/v1/…` or `${BASE}/api/v1/…`), not a local file path. */
function isHubRoutePath(ref: string): boolean {
  const lower = ref.toLowerCase();
  if (lower.startsWith("/api/v1/")) return true;
  if (BASE) {
    const prefix = `${BASE.toLowerCase()}/api/v1/`;
    if (lower.startsWith(prefix)) return true;
  }
  return false;
}

/** Line number from a Markdown link fragment (`#L12` or `#L12-L20`), or undefined. */
export function linkLine(ref: string): number | undefined {
  const hash = ref.indexOf("#");
  if (hash < 0) return undefined;
  const m = /^#L(\d{1,7})(?:-L?\d{1,7})?$/i.exec(ref.slice(hash).trim());
  const n = m ? Number(m[1]) : 0;
  return n > 0 ? n : undefined;
}

/** A path a Markdown file refers to (an image, a link), resolved against the file's folder; "" for a web URL / anchor. */
export function resolveRef(baseDir: string, ref: string): string {
  const trimmed = ref.trim().replace(/^<|>$/g, "");
  const src = trimmed.split(/[?#]/)[0];
  if (!src || !isMachineRef(trimmed)) return "";
  const path = decodeURIComponentSafe(src);
  if (WINDOWS_PATH.test(path)) return windowsResolve(path.startsWith("\\\\") ? "\\\\" + path.slice(2).split(/[\\/]+/).slice(0, 2).join("\\") : path.slice(0, 2) + "\\", path.startsWith("\\\\") ? path.slice(2).split(/[\\/]+/).slice(2).join("\\") : path.slice(3));
  if (path.startsWith("~") || path.startsWith("/")) return normalizePath(path);
  if (WINDOWS_PATH.test(baseDir)) return windowsResolve(baseDir, path);
  return normalizePath(joinPath(baseDir, path));
}

/** `rel` inside a Windows computer's folder `dir`, either separator, in backslashes; ".." never goes above the drive or
 *  the UNC share. */
function windowsResolve(dir: string, rel: string): string {
  const unc = dir.startsWith("\\\\");
  const out = dir.split(/[\\/]+/).filter(Boolean);
  const root = unc ? 2 : 1; // the drive, or the server and its share
  for (const part of rel.split(/[\\/]+/)) {
    if (!part || part === ".") continue;
    if (part !== "..") out.push(part);
    else if (out.length > root) out.pop();
  }
  return (unc ? "\\\\" : "") + out.join("\\") + (out.length === root && !unc ? "\\" : "");
}

/** A reference that points into the machine (relative, absolute, ~/ or Windows drive), not a URL, hub route or in-page anchor. */
export function isMachineRef(ref: string): boolean {
  const r = ref.trim().replace(/^<|>$/g, "");
  if (!r || r.startsWith("#") || r.startsWith("//") || isHubRoutePath(r)) return false;
  if (isWindowsDrivePath(r)) return true;
  return !/^[a-z][a-z0-9+.-]*:/i.test(r);
}

/** A path without "." parts, doubled slashes or ".." where it can go up (a file named two ways is one path). */
export function normalizePath(p: string): string {
  const abs = p.startsWith("/");
  const out: string[] = [];
  for (const part of p.split("/")) {
    if (!part || part === ".") continue;
    if (part === ".." && out.length && out[out.length - 1] !== ".." && out[out.length - 1] !== "~") out.pop();
    else if (part !== ".." || !abs) out.push(part);
  }
  return (abs ? "/" : "") + out.join("/");
}

// ── Git ───────────────────────────────────────────────────

export const isUntracked = (f: GitFile) => f.index === "?";
export const isConflicted = (f: GitFile) => f.index === "U" || f.worktree === "U";
export const isStaged = (f: GitFile) => !isUntracked(f) && f.index !== ".";
export const hasUnstaged = (f: GitFile) => !isUntracked(f) && f.worktree !== ".";

/** The one letter in the change list: untracked U, conflict !, then the staged change, then the working tree's. */
export function gitLetter(f: GitFile): string {
  if (isUntracked(f)) return "U";
  if (isConflicted(f)) return "!";
  return f.index !== "." ? f.index : f.worktree;
}

export type GitTone = "add" | "del" | "moved" | "changed";

/** How a letter is colored, like editors do: added green, deleted / conflicted red, renamed quiet, the rest changed. */
export function gitTone(letter: string): GitTone {
  if (letter === "A" || letter === "U") return "add";
  if (letter === "D" || letter === "!") return "del";
  if (letter === "R" || letter === "C") return "moved";
  return "changed";
}

/** The letter of a file, or for a folder the letter of the first change inside it; null when unchanged. */
export function gitMark(status: GitStatus | null | undefined, full: string, dir: boolean): string | null {
  if (!status?.is_repo || !status.root || !status.files.length) return null;
  const rel = below(status.root, full);
  if (!rel || !rel.length) return null;
  const r = rel.join("/");
  const f = dir ? status.files.find((x) => x.path.startsWith(r + "/")) : status.files.find((x) => x.path === r);
  return f ? gitLetter(f) : null;
}

/** The Git panel's header badge: commits ahead (↑) and behind (↓) the upstream and `*` for a dirty tree; "" when none. */
export function syncBadge(status: GitStatus | null | undefined): string {
  const parts = aheadBehind(status) ? [aheadBehind(status)] : [];
  if (status?.is_repo && status.files.length > 0) parts.push("*");
  return parts.join(" ");
}

/** "↑2 ↓1": commits ahead of and behind the upstream, "" when level (or not a repository). */
export function aheadBehind(status: GitStatus | null | undefined): string {
  if (!status?.is_repo) return "";
  const parts: string[] = [];
  if ((status.ahead ?? 0) > 0) parts.push(`↑${status.ahead}`);
  if ((status.behind ?? 0) > 0) parts.push(`↓${status.behind}`);
  return parts.join(" ");
}

/** Branch / tag names pointing at a commit ("HEAD -> main, origin/main" → ["main", "origin/main"]). */
export function refNames(refs?: string): string[] {
  return (refs ?? "")
    .split(",")
    .map((r) => r.trim())
    .map((r) => (r.startsWith("HEAD -> ") ? r.slice(8) : r))
    .filter((r) => r && r !== "HEAD");
}

export function diffTotals(files: GitFile[]): { added: number; deleted: number } {
  return files.reduce((t, f) => ({ added: t.added + (f.added || 0), deleted: t.deleted + (f.deleted || 0) }), { added: 0, deleted: 0 });
}

export type DiffKind = "add" | "del" | "context" | "hunk" | "info";

export interface DiffLine {
  kind: DiffKind;
  text: string;
  old?: number;
  new?: number;
}

/** A unified diff as lines with old / new line numbers; the file headers (diff --git, index, ---, +++) are dropped. */
export function parseDiff(diff: string): DiffLine[] {
  const out: DiffLine[] = [];
  let old = 0,
    nw = 0;
  let inHunk = false;
  for (const raw of diff.split("\n")) {
    if (raw.startsWith("diff --git")) {
      inHunk = false;
      continue;
    }
    if (!inHunk && (raw.startsWith("index ") || raw.startsWith("--- ") || raw.startsWith("+++ "))) continue;
    if (raw.startsWith("@@")) {
      // @@ -12,7 +12,9 @@ context
      const m = /^@@+ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(raw);
      if (m) {
        old = Number(m[1]);
        nw = Number(m[2]);
      }
      inHunk = true;
      out.push({ kind: "hunk", text: raw });
    } else if (inHunk && raw.startsWith("+")) {
      out.push({ kind: "add", text: raw.slice(1), new: nw++ });
    } else if (inHunk && raw.startsWith("-")) {
      out.push({ kind: "del", text: raw.slice(1), old: old++ });
    } else if (inHunk && raw.startsWith(" ")) {
      out.push({ kind: "context", text: raw.slice(1), old: old++, new: nw++ });
    } else if (/^(\\|Binary|new file|deleted file|similarity|rename |old mode|new mode)/.test(raw)) {
      out.push({ kind: "info", text: raw });
    }
  }
  return out;
}

/** Git C-quotes special path bytes even with core.quotepath off (node gitCmd / gitQuote). */
function unquoteGitPath(path: string): string | null {
  const escapes: Record<string, number> = { a: 7, b: 8, t: 9, n: 10, v: 11, f: 12, r: 13, '"': 34, "\\": 92 };
  const bytes: number[] = [];
  for (const token of path.match(/\\(?:[0-7]{1,3}|.)|[^\\]+/g) ?? []) {
    if (!token.startsWith("\\")) bytes.push(...new TextEncoder().encode(token));
    else {
      const byte = /^[0-7]/.test(token[1]) ? parseInt(token.slice(1), 8) : escapes[token[1]];
      if (byte === undefined || byte > 255) return null;
      bytes.push(byte);
    }
  }
  return new TextDecoder("utf-8", { ignoreBOM: true }).decode(new Uint8Array(bytes));
}

/** A multi-file diff (git show) cut into one diff per file, keyed by the new path. */
export function splitDiff(diff: string): Record<string, string> {
  const res: Record<string, string> = Object.create(null);
  let path: string | null = null;
  let buf: string[] = [];
  let hdr = false;
  const unq = (s: string) => (s[0] === '"' ? unquoteGitPath(s.slice(1, -1)) : s);
  const flush = () => {
    if (path !== null) res[path] = buf.join("\n");
  };
  for (const line of diff.split("\n")) {
    if (line.startsWith("diff --git ")) {
      flush();
      buf = [];
      hdr = true;
      // The -z file list uses raw paths; the diff headers may quote the same names.
      const s = line.slice(11);
      const m = (s.length - 1) >> 1;
      const q = / "b\/((?:[^"\\]|\\.)*)"$/.exec(line);
      const i = line.lastIndexOf(" b/");
      path = q ? unquoteGitPath(q[1]) : s.slice(m, m + 3) === " b/" && s.slice(2, m) === s.slice(m + 3) ? s.slice(m + 3) : i >= 0 ? line.slice(i + 3) : null;
    } else if (hdr) {
      if (line.startsWith("@@")) hdr = false;
      else if (/^(rename|copy) to /.test(line)) path = unq(line.replace(/^(rename|copy) to /, ""));
      // git ends a ---/+++ line with a tab when the name has a space (for GNU patch); the tab is not part of the name.
      else if (/^\+\+\+ "?b\//.test(line)) path = unq(line.slice(4).replace(/\t$/, ""))?.slice(2) ?? null;
    }
    buf.push(line);
  }
  flush();
  return res;
}

// ── the viewer ────────────────────────────────────────────

export type ViewKind = "directory" | "markdown" | "json" | "notebook" | "html" | "video" | "audio" | "code" | "image" | "pdf" | "sheet" | "workbook" | "office" | "binary";

const VIDEO_EXTS = ["mp4", "m4v", "mov", "webm", "mkv", "ogv", "avi"];
const AUDIO_EXTS = ["mp3", "m4a", "aac", "wav", "flac", "ogg", "oga", "opus", "caf", "aiff", "aif"];

/** Office documents drawn in the browser (docx-preview, lib/pptx.ts); the rest go through the machine's LibreOffice. */
export const OFFICE_RENDERED = ["docx", "docm", "dotx", "pptx", "pptm", "ppsx", "potx"];
const OFFICE_CONVERTED = ["doc", "dot", "ppt", "pps", "rtf", "odt", "odp", "pages", "key", "wps", "wpd"];

/** Tables: delimited text, and workbooks SheetJS reads (lib/sheets.ts). */
const DELIMITED_EXTS = ["csv", "tsv", "tab"];
const WORKBOOK_EXTS = ["xlsx", "xlsm", "xlsb", "xls", "ods", "numbers"];

export function extOf(name: string): string {
  const n = fileName(name).toLowerCase();
  const i = n.lastIndexOf(".");
  return i > 0 ? n.slice(i + 1) : "";
}

/** How the viewer shows a file. */
export function viewKind(f: FileContent): ViewKind {
  if (f.is_dir) return "directory";
  const mime = f.mime ?? "";
  if (f.text === undefined || f.text === null) {
    if (mime.startsWith("image/")) return "image";
    if (mime.startsWith("video/") || VIDEO_EXTS.includes(extOf(f.name))) return "video";
    if (mime.startsWith("audio/") || AUDIO_EXTS.includes(extOf(f.name))) return "audio";
    if (mime === "application/pdf" || extOf(f.name) === "pdf") return "pdf";
    if (WORKBOOK_EXTS.includes(extOf(f.name))) return "workbook";
    if ([...OFFICE_RENDERED, ...OFFICE_CONVERTED].includes(extOf(f.name))) return "office";
    return "binary";
  }
  const ext = extOf(f.name);
  if (ext === "rtf") return "office"; // RTF is text, but its source is markup nobody reads
  if (ext === "ipynb") return "notebook";
  if (["html", "htm", "xhtml"].includes(ext)) return "html";
  if (ext === "svg") return "image"; // drawn by <img>, where its scripts never run
  if (DELIMITED_EXTS.includes(ext) || mime === "text/csv" || mime === "text/tab-separated-values") return "sheet";
  if (["md", "markdown", "mdx", "mkd"].includes(ext) || mime === "text/markdown") return ext ? "markdown" : "code";
  if (["json", "jsonc", "json5", "geojson", "webmanifest"].includes(ext) || (mime.includes("json") && !["jsonl", "ndjson"].includes(ext))) return "json";
  return "code";
}

/** What a machine can draw a thumbnail of (node file.thumb: images; PDF, video, Office and iWork through Quick Look on
 * a Mac or pdftoppm / ffmpeg elsewhere), as iOS asks (FileThumbnails.extensions). Code and text keep their glyph; an
 * empty file has nothing to draw. */
export function thumbable(name: string, size: number): boolean {
  if (size <= 0) return false;
  const ext = extOf(name);
  return ["png", "jpg", "jpeg", "gif", "webp", "heic", "heif", "tiff", "tif", "bmp", "svg", "avif", "pdf"].includes(ext) || [...VIDEO_EXTS, ...OFFICE_RENDERED, ...OFFICE_CONVERTED, ...WORKBOOK_EXTS].includes(ext);
}

/** The icon family of a name in a listing. */
export function glyphOf(name: string): "image" | "doc" | "data" | "sheet" | "pdf" | "code" | "file" {
  const ext = extOf(name);
  if (["png", "jpg", "jpeg", "gif", "webp", "heic", "bmp", "ico", "svg", "avif", "tiff"].includes(ext)) return "image";
  if (["md", "markdown", "txt", "rst", "log", "rtf"].includes(ext)) return "doc";
  if (["json", "jsonl", "yml", "yaml", "toml", "plist", "xml", "ini", "env", "lock"].includes(ext)) return "data";
  if ([...DELIMITED_EXTS, ...WORKBOOK_EXTS].includes(ext)) return "sheet";
  if ([...OFFICE_RENDERED, ...OFFICE_CONVERTED].includes(ext)) return "doc";
  if (ext === "pdf") return "pdf";
  if (!ext || NOT_CODE_EXTS.includes(ext)) return "file";
  return "code";
}

/** Archives, installers, binaries, media and fonts: a plain file, not code (#4883: a .zip drew the <> glyph). */
const NOT_CODE_EXTS = [
  ...["zip", "gz", "tgz", "tar", "bz2", "xz", "zst", "7z", "rar", "dmg", "pkg", "iso", "deb", "rpm", "apk", "ipa", "jar", "whl"],
  ...["exe", "bin", "dll", "so", "dylib", "a", "o", "class", "wasm", "dat", "sqlite", "db"],
  ...["mp3", "wav", "flac", "m4a", "aac", "ogg", "opus", "mp4", "mov", "m4v", "webm", "mkv", "avi"],
  ...["woff", "woff2", "ttf", "otf"],
];

/** Text into lines; a final newline does not make an empty last line. */
export function splitLines(text: string): string[] {
  if (!text) return [];
  const lines = text.split(/\r?\n/);
  if (lines.length > 1 && lines[lines.length - 1] === "") lines.pop();
  return lines;
}

/** Columns a line takes in a monospaced font: wide (CJK, emoji) characters take two. */
export function columns(s: string): number {
  let n = 0;
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    if (c >= 0xd800 && c <= 0xdbff) {
      n += 2;
      i++;
    } else n += c >= 0x1100 && (c <= 0x115f || (c >= 0x2e80 && c <= 0xa4cf) || (c >= 0xac00 && c <= 0xd7a3) || (c >= 0xf900 && c <= 0xfaff) || (c >= 0xfe30 && c <= 0xfe4f) || (c >= 0xff00 && c <= 0xff60) || (c >= 0xffe0 && c <= 0xffe6)) ? 2 : 1;
  }
  return n;
}

/** A blob's bytes from base64. */
export function base64Bytes(data: string): Uint8Array<ArrayBuffer> {
  const bin = atob(data);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

/**
 * JSON re-indented without parsing its values: key order and big numbers stay exactly as written (JSON.parse would
 * put integer-like keys first and round 64-bit ids). Returns null when the text is not valid JSON.
 */
export function prettyJSON(text: string, indent = "  "): string | null {
  try {
    JSON.parse(text);
  } catch {
    return null;
  }
  let out = "";
  let depth = 0;
  const n = text.length;
  for (let i = 0; i < n; i++) {
    const c = text[i];
    if (c === '"') {
      let j = i + 1;
      while (j < n && text[j] !== '"') j += text[j] === "\\" ? 2 : 1;
      out += text.slice(i, j + 1);
      i = j;
    } else if (c === "{" || c === "[") {
      let j = i + 1;
      while (j < n && /\s/.test(text[j])) j++;
      if (text[j] === (c === "{" ? "}" : "]")) {
        out += c + text[j];
        i = j;
      } else {
        depth++;
        out += c + "\n" + indent.repeat(depth);
      }
    } else if (c === "}" || c === "]") {
      depth--;
      out += "\n" + indent.repeat(depth) + c;
    } else if (c === ",") {
      out += ",\n" + indent.repeat(depth);
    } else if (c === ":") {
      out += ": ";
    } else if (!/\s/.test(c)) {
      out += c;
    }
  }
  return out;
}

// ── Markdown files ────────────────────────────────────────

/** A fence line's run of backticks or tildes (three or more): ```` opens a block only ```` or longer ends. */
function openingFence(t: string): string {
  return /^(`{3,}|~{3,})/.exec(t)![1];
}

/** Whether `t` ends the block `fence` opened (CommonMark): the same character, at least as many, nothing after. */
function closesFence(t: string, fence: string): boolean {
  const run = /^(`+|~+)/.exec(t)?.[1];
  return !!run && run[0] === fence[0] && run.length >= fence.length && !t.slice(run.length).trim();
}

/**
 * The HTML READMEs use (centered <p>, <img width=…>, image grids in <table>) as Markdown; other tags dropped, their
 * text kept. react-markdown shows raw HTML as text. Code fences are left alone. (iOS MarkdownHTML.simplify)
 */
export function simplifyHTML(md: string): string {
  if (!md.includes("<")) return md;
  const out: string[] = [];
  let fence: string | null = null;
  let table: string[] | null = null;
  for (const line of md.split("\n")) {
    const t = line.trim();
    if (fence) {
      if (closesFence(t, fence)) fence = null;
      out.push(line);
      continue;
    }
    if (t.startsWith("```") || t.startsWith("~~~")) {
      fence = openingFence(t);
      out.push(line);
      continue;
    }
    if (table) {
      table.push(line);
      if (/<\/table>/i.test(t)) {
        out.push(...htmlTable(table.join("\n")));
        table = null;
      }
      continue;
    }
    if (/^<table\b/i.test(t)) {
      if (/<\/table>/i.test(t)) out.push(...htmlTable(t));
      else table = [line];
      continue;
    }
    if (t.startsWith("<") && !t.startsWith("<http")) {
      const s = inlineHTML(t);
      if (s) out.push(s);
      continue;
    }
    out.push(line);
  }
  if (table) out.push(...table);
  return out.join("\n");
}

function htmlTable(html: string): string[] {
  const rows = captures(/<tr\b[^>]*>([\s\S]*?)<\/tr>/gi, html)
    .map((row) => captures(/<t[dh]\b[^>]*>([\s\S]*?)<\/t[dh]>/gi, row).map((c) => inlineHTML(c).replace(/\|/g, "\\|")))
    .filter((r) => r.length);
  if (!rows.length) return [];
  const width = Math.max(...rows.map((r) => r.length));
  const line = (cells: string[]) => "| " + [...cells, ...Array(width - cells.length).fill("")].join(" | ") + " |";
  return ["", line(rows[0]), line(Array(width).fill("---")), ...rows.slice(1).map(line), ""];
}

/** Images and links into Markdown, line breaks into spaces, other tags dropped, entities decoded. */
export function inlineHTML(html: string): string {
  let s = html.replace(/<img\b[^>]*>/gi, (tag) => {
    const src = attr("src", tag);
    return src ? `![${attr("alt", tag) ?? ""}](${src})` : "";
  });
  s = s.replace(/<a\b[^>]*>([\s\S]*?)<\/a>/gi, (tag, inner: string) => {
    const text = inner.replace(/<[^>]+>/g, "");
    const href = attr("href", tag);
    return href ? `[${text}](${href})` : text;
  });
  s = s.replace(/<br\s*\/?>/gi, " ").replace(/<[^>]+>/g, "");
  for (const [k, v] of [
    ["&nbsp;", " "],
    ["&lt;", "<"],
    ["&gt;", ">"],
    ["&quot;", '"'],
    ["&amp;", "&"],
  ])
    s = s.split(k).join(v);
  return s.split(/\s+/).filter(Boolean).join(" ");
}

function attr(name: string, tag: string): string | null {
  return new RegExp(`\\b${name}\\s*=\\s*["']([^"']*)["']`, "i").exec(tag)?.[1] ?? null;
}

function captures(re: RegExp, s: string): string[] {
  return [...s.matchAll(re)].map((m) => m[1]);
}

/**
 * A long Markdown file cut between top-level blocks into pieces of about `target` lines, so the page lays out only
 * what is on screen. Never inside a code fence or display math, before an indented line (a list item's continuation,
 * indented code) or a table's rows; link definitions go with every piece so references still resolve. (iOS MarkdownChunks)
 */
export function markdownChunks(text: string, target = 40): string[] {
  const lines = text.split("\n");
  if (lines.length <= target) return [text];
  const defs = lines.filter((l) => /^ {0,3}\[[^\]]+\]:\s*\S/.test(l));
  const out: string[] = [];
  let cur: string[] = [];
  let fence: string | null = null;
  // The delimiter that ends an open display formula ($$ … $$ or \[ … \]), which may hold blank lines.
  let math: string | null = null;
  lines.forEach((line, i) => {
    const t = line.trim();
    if (fence) {
      if (closesFence(t, fence)) fence = null;
    } else if (math) {
      if (t.includes(math)) math = null;
    } else if (t.startsWith("```") || t.startsWith("~~~")) fence = openingFence(t);
    else if ((t.match(/(?<!\\)\$\$/g) ?? []).length % 2) math = "$$";
    else if (/(?<!\\)\\\[/.test(t) && !t.slice(t.lastIndexOf("\\[")).includes("\\]")) math = "\\]";
    cur.push(line);
    if (fence || math || cur.length < target || t || i + 1 >= lines.length) return;
    const next = lines[i + 1];
    if (next.startsWith(" ") || next.startsWith("\t") || !next.trim()) return;
    out.push(cur.join("\n"));
    cur = [];
  });
  if (cur.length) out.push(cur.join("\n"));
  if (!defs.length || out.length < 2) return out;
  const tail = "\n\n" + defs.join("\n");
  return out.map((c) => c + tail);
}

// ── tool steps ────────────────────────────────────────────

/** The file a Read / Edit / Write / MultiEdit / NotebookEdit step works on (Codex's file changes come as Edit), and the line Read starts at. */
export function toolFile(name: string | undefined, input: unknown): { path: string; line?: number } | null {
  if (!name || !/^(Read|Edit|Write|MultiEdit|NotebookEdit)$/.test(name)) return null;
  const inp = (input ?? {}) as Record<string, unknown>;
  const p = [inp.file_path, inp.notebook_path, inp.path].find((v) => typeof v === "string" && v.trim()) as string | undefined;
  if (!p) return null;
  const off = Number(inp.offset);
  return { path: p.trim(), ...(name === "Read" && Number.isFinite(off) && off > 1 ? { line: Math.floor(off) } : {}) };
}
