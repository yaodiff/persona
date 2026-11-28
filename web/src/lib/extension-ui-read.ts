// The readers extension-ui.ts and extension-ui-v6.ts both use, in a module of their own so that neither imports the
// other's values (extension-ui.ts re-exports isHttps, which the views import from there).
import type { UIAction, UIPush } from "./extension-ui";

export const isObject = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v);

function hasCtrlOrSpace(url: string): boolean {
  for (let i = 0; i < url.length; i++) {
    const c = url.charCodeAt(i);
    if (c <= 0x20 || c === 0x7f) return true;
  }
  return /\s/.test(url);
}

/** Only credential-free https links with an explicit authority and host open (the hub checks too). */
export function isHttps(url?: string): boolean {
  if (!url || hasCtrlOrSpace(url) || !/^https:\/\//i.test(url)) return false;
  const auth = url.slice(8).split(/[/?#]/, 1)[0];
  if (!auth || auth.includes("@") || auth.includes("\\")) return false;
  try {
    const u = new URL(url);
    return u.protocol === "https:" && u.hostname !== "" && u.username === "" && u.password === "";
  } catch {
    return false;
  }
}

/** An action as the web uses it (an id at least), its push read too; undefined when it is not one. */
export function readAction(raw: unknown): UIAction | undefined {
  if (!isObject(raw) || typeof raw.id !== "string" || !raw.id) return undefined;
  const a: UIAction = { id: raw.id };
  if (typeof raw.confirm === "string") a.confirm = raw.confirm;
  if (raw.submit === true) a.submit = true;
  if (typeof raw.open === "string" && raw.open) a.open = raw.open;
  const p = readPush(raw.push);
  if (p) a.push = p;
  if (isObject(raw.values)) {
    const values = Object.fromEntries(Object.entries(raw.values).filter((e): e is [string, string] => typeof e[1] === "string"));
    if (Object.keys(values).length) a.values = values;
  }
  return a;
}

/** A push as the web keeps it: the state as text (an extension may send an object: it gets it back as JSON). */
export function readPush(raw: unknown): UIPush | undefined {
  if (raw === true) return {};
  if (!isObject(raw)) return undefined;
  const p: UIPush = {};
  if (typeof raw.state === "string") p.state = raw.state;
  else if (raw.state !== undefined && raw.state !== null) p.state = JSON.stringify(raw.state);
  if (typeof raw.title === "string" && raw.title) p.title = raw.title;
  if (typeof raw.ui === "string" && raw.ui) p.ui = raw.ui;
  return p;
}
