import { join } from "node:path";
import { readFile } from "node:fs/promises";

export type DesktopLocale = "en" | "zh-Hans" | "ja";
export type DesktopStrings = Readonly<Record<string, string>>;

/** The three shipped languages; everything else reads English. */
export function desktopLocale(systemLocale: string): DesktopLocale {
  const locale = systemLocale.toLowerCase();
  if (locale.startsWith("zh")) return "zh-Hans";
  if (locale.startsWith("ja")) return "ja";
  return "en";
}

/** A key missing from a translation falls back to English rather than showing a blank label. */
export async function loadStrings(directory: string, locale: DesktopLocale): Promise<DesktopStrings> {
  const read = async (name: string): Promise<Record<string, string>> => JSON.parse(await readFile(join(directory, `${name}.json`), "utf8")) as Record<string, string>;
  const english = await read("en");
  return locale === "en" ? english : { ...english, ...await read(locale) };
}
