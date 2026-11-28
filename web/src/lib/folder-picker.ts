// Helpers for New agent's breadcrumb path picker and secondary Recent folder list (#14595).

export interface BreadcrumbSegment {
  label: string;
  path: string;
}

export interface RecentFolder {
  path: string;
  name: string;
  last_used?: string;
}

const HOME_PREFIX = /^(\/(?:Users|home|data\d*\/home)\/[^/]+)(\/.*)?$/;
const WINDOWS_DRIVE = /^([A-Za-z]:)([\\/].*)?$/;
// Ephemeral worktree directory segments (Persona, git, Claude, or *_worktrees / *-worktrees).
const WORKTREE_SEGMENT = /(?:^|[\\/])(?:\.?worktrees|[^\\/]+[_-]worktrees)(?:[\\/]|$)/i;

function normalizeKey(path: string): string {
  const trimmed = path.trim();
  if (!trimmed) return "";
  if (trimmed === "/" || /^[A-Za-z]:[\\/]$/.test(trimmed)) return trimmed;
  return trimmed.replace(/[\\/]+$/, "");
}

/** Splits a directory path into clickable ancestor breadcrumb segments.
 *  Home paths (/Users/alice/Code/app, /home/alice/app, /data00/home/alice/app) start at `~`.
 *  Non-home POSIX paths start at `/`; Windows paths start at `C:\`. */
export function pathBreadcrumbs(dirPath: string): BreadcrumbSegment[] {
  const raw = dirPath.trim();
  if (!raw) return [];

  if (raw === "~" || raw.startsWith("~/")) {
    const clean = raw === "~" ? "~" : raw.replace(/\/+$/, "");
    const rest = clean.slice(1).split("/").filter(Boolean);
    const out: BreadcrumbSegment[] = [{ label: "~", path: "~" }];
    let acc = "~";
    for (const part of rest) {
      acc = `${acc}/${part}`;
      out.push({ label: part, path: acc });
    }
    return out;
  }

  const home = HOME_PREFIX.exec(raw);
  if (home) {
    const base = home[1];
    const rest = (home[2] ?? "").replace(/\/+$/, "").split("/").filter(Boolean);
    const out: BreadcrumbSegment[] = [{ label: "~", path: base }];
    let acc = base;
    for (const part of rest) {
      acc = `${acc}/${part}`;
      out.push({ label: part, path: acc });
    }
    return out;
  }

  const win = WINDOWS_DRIVE.exec(raw);
  if (win) {
    const drive = win[1];
    const sep = raw.includes("\\") ? "\\" : "/";
    const rootPath = `${drive}${sep}`;
    const rest = (win[2] ?? "").replace(/[\\/]+$/, "").split(/[\\/]+/).filter(Boolean);
    const out: BreadcrumbSegment[] = [{ label: rootPath, path: rootPath }];
    let acc = drive;
    for (const part of rest) {
      acc = `${acc}${sep}${part}`;
      out.push({ label: part, path: acc });
    }
    return out;
  }

  if (raw.startsWith("/")) {
    const clean = raw === "/" ? "/" : raw.replace(/\/+$/, "");
    const rest = clean.split("/").filter(Boolean);
    const out: BreadcrumbSegment[] = [{ label: "/", path: "/" }];
    let acc = "";
    for (const part of rest) {
      acc = `${acc}/${part}`;
      out.push({ label: part, path: acc });
    }
    return out;
  }

  return [{ label: raw, path: raw }];
}

/** Whether a path looks like an ephemeral git worktree directory. */
export function isWorktreePath(path: string): boolean {
  return WORKTREE_SEGMENT.test(path);
}

/** Deduplicates recent folders (preserving first occurrence) and orders primary folders ahead of worktree paths. */
export function orderRecentFolders<T extends RecentFolder>(recent: readonly T[] | undefined): T[] {
  if (!recent?.length) return [];
  const seen = new Set<string>();
  const primary: T[] = [];
  const worktrees: T[] = [];
  for (const item of recent) {
    const key = normalizeKey(item.path);
    if (!key || seen.has(key)) continue;
    seen.add(key);
    if (isWorktreePath(key)) worktrees.push(item);
    else primary.push(item);
  }
  return [...primary, ...worktrees];
}
