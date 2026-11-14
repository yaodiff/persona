// What the first screen's part of the client catalog holds (web/i18n-catalogs.ts client-startup). Each entry of
// src/i18n/startup-namespaces.json is a namespace ("chat-chatview": all of it) or the start of a key in one
// ("settings.avatar": every key of settings that begins with "avatar", so its plural forms and the suffixes a wrapper
// adds come along). A namespace whose first-screen code reads a few of many keys lists just those, so the rest of its
// words load after the first screen. Used by the build (i18n-catalogs.ts, startup-preload.ts) and the dev server's
// catalog modules; no client code imports it.

/** Whether the key `namespace.name` is in the first screen's part of the catalog, given the list's `entries`. */
export function startupMatcher(entries: readonly string[]): (key: string) => boolean {
  const whole = new Set(entries.filter((e) => !e.includes(".")));
  const starts = entries.filter((e) => e.includes("."));
  return (key) => whole.has(key.slice(0, key.indexOf("."))) || starts.some((s) => key.startsWith(s));
}
