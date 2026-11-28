import type { JsonObject } from "./extensions";

/** Kept small enough for an extension action's bounded open_url. */
export const SETTINGS_PREFILL_MOST = 2 * 1024;

/** A proposed settings object from a link, only after it decodes as a bounded JSON object. */
export function decodeSettingsPrefill(raw: string | null | undefined): JsonObject | undefined {
  if (!raw || raw.length > SETTINGS_PREFILL_MOST) return undefined;
  try {
    const value: unknown = JSON.parse(raw);
    return value && typeof value === "object" && !Array.isArray(value) ? value as JsonObject : undefined;
  } catch {
    return undefined;
  }
}

/** The one `set` value of a Settings URL, if it has exactly one. */
export function settingsPrefill(search: string): JsonObject | undefined {
  const values = new URLSearchParams(search).getAll("set");
  return values.length === 1 ? decodeSettingsPrefill(values[0]) : undefined;
}
