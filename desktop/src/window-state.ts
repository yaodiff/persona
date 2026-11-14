export interface WindowState {
  readonly width: number;
  readonly height: number;
  readonly x?: number;
  readonly y?: number;
  readonly maximized: boolean;
}

export interface DisplayBounds {
  readonly x: number;
  readonly y: number;
  readonly width: number;
  readonly height: number;
}

export const DEFAULT_WINDOW = { width: 1280, height: 840, maximized: false } as const;

/** Restores only a window that is at least partly visible on a currently attached display. */
export function restoreWindow(value: unknown, displays: readonly DisplayBounds[]): WindowState {
  if (!value || typeof value !== "object") return { ...DEFAULT_WINDOW };
  const v = value as Record<string, unknown>;
  const dimension = (n: unknown, fallback: number, min: number, max: number) => typeof n === "number" && Number.isFinite(n) && n >= min && n <= max ? Math.round(n) : fallback;
  const base = {
    width: dimension(v.width, DEFAULT_WINDOW.width, 760, 7680),
    height: dimension(v.height, DEFAULT_WINDOW.height, 560, 4320),
    maximized: v.maximized === true,
  };
  if (typeof v.x !== "number" || !Number.isFinite(v.x) || typeof v.y !== "number" || !Number.isFinite(v.y)) return base;
  const x = Math.round(v.x), y = Math.round(v.y);
  const visible = displays.some(d => x < d.x + d.width && x + base.width > d.x && y >= d.y && y < d.y + d.height);
  return visible ? { ...base, x, y } : base;
}
