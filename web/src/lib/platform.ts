// Which platform the browser is on, for the Mod key (⌘ on Apple platforms, Ctrl elsewhere) and the names of keys.
// Kept apart from shortcuts.ts, with no imports, so a lazy view that only names keys (views/extensions/CodeViews.tsx)
// does not pull the shortcut registry into its chunk graph and move the first screen's chunks.

export type Platform = "mac" | "other";

/** ⌘ is Mod on Apple platforms (Mac, iPad Safari asking for the desktop site), Ctrl everywhere else. */
export function platformOf(nav?: { platform?: string; userAgent?: string; userAgentData?: { platform?: string } }): Platform {
  const hint = nav?.userAgentData?.platform || nav?.platform || nav?.userAgent || "";
  return /mac|iphone|ipad|ipod/i.test(hint) ? "mac" : "other";
}

let detected: Platform | null = null;
export function currentPlatform(): Platform {
  detected ??= platformOf(typeof navigator === "undefined" ? undefined : (navigator as unknown as Parameters<typeof platformOf>[0]));
  return detected;
}

/** What the modifier is called on this platform, for messages: "needs Ctrl" / "needs ⌘". */
export const modName = (p: Platform) => (p === "mac" ? "⌘" : "Ctrl");
