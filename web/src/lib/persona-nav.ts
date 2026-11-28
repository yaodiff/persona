// The sidebar's entries around Persona (#1239): which row a route lights up, the pages under Persona, and how a long list
// keeps the open row. Pure, so the order and the selection are tested without a DOM.
import { parse, type Route } from "./router";
import { moduleNavKey } from "../core/module-host";

/** The route whose row the sidebar lights (#12055). An Agent's page opened from inside another page (a to-do, a
 *  knowledge project, a meeting, a Persona conversation's card) is a drill-down: the sidebar keeps the row of the page it
 *  was opened from (#9971). `from`: that page's address in the app, as the Agent's history entry names it (lib/handoff.ts
 *  sidebarPageOf). Any other page lights its own row. */
export function sidebarRoute(route: Route, from?: string): Route {
  if (!from || (route.name !== "session" && route.name !== "tab")) return route;
  const q = from.indexOf("?");
  return q < 0 ? parse(from) : parse(from.slice(0, q), from.slice(q));
}

/** The sidebar row a route selects. Persona's main conversation is one row, under `/`, `/persona` and `/persona/<main>` alike.
 *  An Agent's page opened from another page selects that page's row (`from`, sidebarRoute). */
export function sidebarActiveKey(route: Route, main?: string, from?: string): string {
  const r = sidebarRoute(route, from);
  switch (r.name) {
    case "session":
      return `s:${r.mid}/${r.sid}`;
    case "persona":
      return !r.sid || r.sid === main ? "persona" : `j:${r.sid}`;
    case "persona-new":
      return "j:new";
    case "module":
      return moduleNavKey(r);
    case "track":
      return spaceNavKey(r.slug);
    case "history":
      return r.origin ? `auto:${r.origin}` : "history";
    case "machine":
      return "machines";
    case "agents":
      return r.id ? `a:${r.id}` : "agents";
    case "terminal":
      return r.mid && r.id ? `term:${r.mid}/${r.id}` : "terminal";
    case "extension":
      return extensionNavKey(r.extension, r.ui);
    default:
      return r.name;
  }
}

/** A domain space's sidebar row (the Spaces section, views/SidebarSpaces.tsx). */
export const spaceNavKey = (slug: string) => `track:${slug}`;

/** An extension page's sidebar row (docs/extensions/extensions.md "UI slots" nav_page). */
export const extensionNavKey = (extension: string, ui: string) => `p:${extension}/${ui}`;

/** A shortened list's first `n`, and the open item after them when it is further down (#2283). */
export function firstAndCurrent<T>(list: readonly T[], n: number, isCurrent: (x: T) => boolean): T[] {
  const first = list.slice(0, n);
  const current = list.slice(n).find(isCurrent);
  return current ? [...first, current] : first;
}

/** The page entries under Persona, in the sidebar's order (the app's "Assistant" first, then the rest of its drawer). "Messages" is the lark
 * module's (src/modules/lark), right after Inbox. The extensions' pages (nav_page) go right before "Settings".
 * The enabled feature modules' pages (core/module-host.ts navItems) go right after the page each names (`after`). */
export const NAV_PAGES = ["home", "inbox", "starred", "workspace-board", "todos", "routines", "kb", "machines", "terminal", "settings"] as const;

/**
 * Where the page goes when another assistant becomes the current one (the sidebar's switcher): a page about one thing
 * of the assistant left behind (a conversation, an agent, a computer, an inbox item, a knowledge entry, a to-do) goes
 * to that thing's list, which now shows the new assistant's; a conversation or an agent goes to the new assistant's
 * main conversation. null: the page stays (a list, Settings), and shows the new assistant's by itself.
 */
export function routeAfterSwitch(route: Route): Route | null {
  switch (route.name) {
    case "persona":
      return route.sid ? { name: "persona" } : null;
    case "session":
    case "tab":
    case "space":
    case "request":
      return { name: "persona" };
    case "machine":
      return { name: "machines" };
    case "inbox":
      return route.id ? { name: "inbox" } : null;
    case "kb":
      return route.id ? { name: "kb", ...(route.kind && { kind: route.kind }) } : null;
    case "todos": {
      if (!route.id) return null;
      const { id: _id, ...list } = route;
      return list;
    }
    case "agents":
      return route.id ? { name: "agents" } : null;
    default:
      return null;
  }
}

/**
 * Whether the page at route opens again when another assistant becomes current, or a kept pick that left the roster
 * goes back to the default: a page that stays (routeAfterSwitch null) and reads the assistant's things itself, once,
 * through the routes that name the pick (lib/manager-choice.ts managerHeaderFor): All chats, the assistant's profile,
 * Scheduled tasks and the knowledge base. Opened again, it reads the new one's and nothing it shows or sends is the
 * last one's. The other pages that stay follow the pick by themselves: the assistant page and the lists filtered from
 * the store (the Overview, Agents, computers, the Inbox, To-dos and their projects), and Settings' editors, which pick
 * their own target; the rest are no assistant's (the health space, Messages, the terminals, an extension's page).
 */
export function reopensOnSwitch(route: Route): boolean {
  return route.name === "history" || route.name === "profile" || route.name === "routines" || route.name === "kb";
}
