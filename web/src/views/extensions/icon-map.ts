// The fixed icon set (schema/extension/view.schema.json NavIcon, lib/extension-ui.ts ROW_ICONS) as the web's own glyphs:
// one meaning, one icon (the apps map the same names to their SF Symbols and Phosphor icons). A row, a tile, a graph
// node (ViewBody.tsx and the rest) and a nav_page's sidebar entry (ExtensionSlots.tsx navIcon, #16281) all draw from
// ROW_ICON here: the sidebar is on the first screen and ViewBody is not, so the table lives in neither.
import type { NEW_ICONS_6, RowIcon } from "../../lib/extension-ui";
import type { IconName } from "../../ui/icons";

/** The icons UI version 6 adds (lib/extension-ui.ts NEW_ICONS_6). */
export const NEW_ICON_GLYPH: Record<(typeof NEW_ICONS_6)[number], IconName> = {
  home: "home", sofa: "sofa", light: "light", power: "power", fan: "fan", thermometer: "thermometer", droplet: "droplet", lock: "lock",
  unlock: "unlock", door: "door", window: "window", camera: "camera", video: "video", play: "play", pause: "pause", stop: "stop",
  speaker: "speaker", music: "music", tv: "tv", plug: "plug", battery: "battery", bolt: "bolt", sun: "sun", moon: "moon", cloud: "cloud", rain: "rain",
  snow: "snow", wind: "wind", leaf: "leaf", car: "car", location: "location", robot: "robot", wifi: "wifi", shield: "shield",
  trend_up: "trendUp", trend_down: "trendDown", wallet: "wallet", bank: "bank", heart: "heart", plus: "plus", minus: "minus",
  trash: "trash", edit: "pencil", refresh: "retry", share: "share", info: "info",
};

/** Every icon of the fixed vocabulary, as one of the web's icons (`plugin` is `extension` under its old name). */
export const ROW_ICON: Record<RowIcon, IconName> = {
  extension: "extension", plugin: "extension", chart: "chart", gauge: "gauge", list: "list", bell: "bell", doc: "read", calendar: "calendar",
  money: "money", person: "person", chat: "chat", mail: "mail", link: "link", folder: "folder", star: "star", check: "check",
  warning: "warning", clock: "clock", tag: "tag", search: "search", settings: "gear", branch: "branch", commit: "commit", code: "code",
  pull_request: "pullRequest", merge: "merge", file: "read",
  ...NEW_ICON_GLYPH,
};
