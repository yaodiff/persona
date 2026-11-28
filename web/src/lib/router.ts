import { machineLocation, machinePath, type MachineLocation } from "./machine-route";
import { assistantPath } from "./compatibility";
// A tiny history router: the hub serves index.html for every path that isn't a file.
import { useSyncExternalStore } from "react";
import { takeOverlayEntry } from "./back-close";
import { BASE, stripBase } from "./base";
import { decodeURIComponentSafe } from "./uri";
import { moduleExtensionAt, moduleKBKindAt, modulePath, moduleRouteAt } from "../core/module-host";
import { healthReadOnlyRoute } from "./health-archive";

// Persona's main conversation is the front door (like the app, which opens on the assistant): `/` and `/persona` open it, `/persona/<id>` is
// one conversation, `/persona/new` starts a one-off one, and "Overview" lives at `/overview`. `/extension/<id>/<ui>` is an extension's page
// (docs/extensions/extensions.md "UI slots" nav_page).
/** What the middle of an agent's page shows (docs/web/web.md "Terminal ⇄ chat"): its conversation, or its terminal in place. */
export type SessionView = "chat" | "term";

export type Route =
  | { name: "home" }
  | { name: "request"; id: string }
  | { name: "workspace-board" }
  | { name: "persona-new" }
  // `view` is the page's `?view=chat|term`; without it the page shows what it showed last (lib/session-view.ts)
  // `at`: the message to land on (?at=<item id>, the hub's links to one message, docs/protocol/deep-links.md)
  // `standalone`: explicit full-page open for a subagent or a blocked terminal, even when the chat normally redirects
  | { name: "session"; mid: string; sid: string; view?: SessionView; work?: true; at?: string; standalone?: true }
  // one herdr tab (a slot) of a workspace; `sid` picks its pane (lib/herdr-tree.ts resolveTab)
  | { name: "tab"; mid: string; space: string; slot: string; sid?: string; view?: SessionView; work?: true; at?: string }
  // `mid` / `manager`: the computer the conversation runs on and its manager when another than the default
  // (`?machine=…&manager=…`, lib/conversation-page.ts): with several assistants a reload, a new tab or a link finds it
  // where it runs, whichever assistant is current
  // `at`: a message to open the chat at (`?at=<item id>`, a search hit's, #6283): the chat lands on it, paging back when it
  // is further up
  | { name: "persona"; sid?: string; mid?: string; manager?: string; at?: string }
  // "All chats"; `origin` filters it (lib/conversation-origins.ts): the sidebar's "Automatic" rows open it so
  | { name: "history"; origin?: string }
  // a feature module's page (core/module-host.ts, docs/web/web.md "Modules"): `/<base>/<path…>`, only while the module is
  // enabled; the module reads `path` itself
  | { name: "module"; module: string; base: string; path: string[] }
  // a domain space (docs/web/web.md "Domain spaces", #13500): `/tracks/<slug>` opens its latest conversation, `/tracks/<slug>/new`
  // its home (its conversations, SPACE.md, a box to start one), `/tracks/<slug>/<id>` one of them. A space a module
  // has its own page for (the health module's `/tracks/health…`) opens that page
  | { name: "track"; slug: string; sid?: string; fresh?: boolean }
  // the new space form (`/tracks/new`)
  | { name: "track-new" }
  | { name: "profile" }
  // `manager`: whose item it is when that is another manager than the default (`?manager=`, docs/assistant/managers.md
  // "Clients"): two managers may list the same id
  | { name: "inbox"; id?: string; manager?: string }
  | { name: "starred" }
  | { name: "routines" }
  | { name: "kb"; kind?: string; id?: string }
  | { name: "machines" }
  | ({ name: "machine"; mid: string } & MachineLocation)
  // one space of a machine by its name (lib/space-page.ts): its agents, the todos it hangs on, what can be done to it
  | { name: "space"; mid: string; space: string; workspace?: string }
  // the terminal browser (docs/integrations/terminal.md "Standalone terminal"): every pane of every machine; `id` is the terminal opened (an agent
  // session's id, or "pane:<pane>" for a plain shell); `view` is the list beside it, `?view=sessions|terminals`
  // (lib/terminal-list.ts): without it the one this browser chose last
  | { name: "terminal"; mid?: string; id?: string; view?: "sessions" | "terminals" }
  | { name: "settings" }
  | { name: "agents"; id?: string }
  // the phone's "Agents" page (#1996): the sidebar's Agents block on a page of its own, next to the assistant
  | { name: "agent-list" }
  // the agents archived from the Agents list (lib/agent-archives.ts), to find and restore
  | { name: "agent-archives" }
  // the runs of one New agent start side by side (#8916, lib/compare-runs.ts)
  | { name: "compare"; id: string }
  // view=calendar: the to-dos' calendar (#2029), its mode and the day it shows ("YYYY-MM-DD"); view=board: the read-only
  // progress board (#12713). Either view keeps the open to-do beside it.
  | { name: "todos"; id?: string; direction?: string; manager?: string; view?: "list" | "calendar" | "board"; mode?: "day" | "week" | "month"; date?: string }
  | { name: "extension"; extension: string; ui: string }
  // the source subscription pages (#2041), their first state in ?state=
  | { name: "subscriptions" }
  // where Feishu / Lark sends the owner back after allowing a bot's app to read as them (`?code=&state=`, the daily sync's
  // authorization, views/settings/LarkOAuthReturn.tsx); the page reads its query itself
  | { name: "lark-oauth" }
  // an address that is no page of the app (mistyped, or left from an older version): said so, with the way to Overview,
  // instead of Overview drawn under the wrong address (web usability audit item 42); `path` is the address as it came
  | { name: "not-found"; path: string };

/** A conversation page's `?machine=` and `?manager=` (routePath writes them), each only when there. */
function conversationQuery(search: string): { mid?: string; manager?: string; at?: string } {
  const q = new URLSearchParams(search);
  const mid = q.get("machine");
  const manager = q.get("manager");
  const at = q.get("at");
  return { ...(mid ? { mid } : {}), ...(manager ? { manager } : {}), ...(at ? { at } : {}) };
}

/** `?view=term|chat` of an agent page's address; anything else is no choice. */
export function parseView(search: string): SessionView | undefined {
  const v = new URLSearchParams(search).get("view");
  return v === "term" || v === "chat" ? v : undefined;
}

export function parse(path: string, search = ""): Route {
  // a malformed escape must still route: parse runs outside every error boundary
  const p = path.split("/").filter(Boolean).map(decodeURIComponentSafe);
  const view = parseView(search);
  const detail = () => {
    const q = new URLSearchParams(search);
    const at = q.get("at");
    return {
      ...(view && { view }),
      ...(q.get("work") === "1" && { work: true as const }),
      ...(at && { at }),
      ...(q.get("standalone") === "1" && { standalone: true as const }),
    };
  };
  if (p[0] === "s" && p[1] && p[2]) return { name: "session", mid: p[1], sid: p[2], ...detail() };
  if (p[0] === "t" && p[1] && p[2] && p[3]) return { name: "tab", mid: p[1], space: p[2], slot: p[3], sid: p[4] || undefined, ...detail() };
  if (!p.length) return { name: "persona" };
  if (assistantPath(p[0])) return p[1] === "new" && !p[2] ? { name: "persona-new" } : p[1] ? { name: "persona", sid: p[1], ...conversationQuery(search) } : { name: "persona" };
  if (p[0] === "workspace-board") return { name: "workspace-board" };
  if (p[0] === "requests" && p[1]) return { name: "request", id: p[1] };
  if (p[0] === "overview") return { name: "home" };
  if (p[0] === "lark-oauth" && !p[1]) return { name: "lark-oauth" };
  if (p[0] === "history") return p[1] ? { name: "history", origin: p[1] } : { name: "history" };
  const module = moduleRouteAt(p);
  // Existing Health data has a default-panel read-only route both when its module is switched off and when a
  // core-only build has no descriptor for it. Other absent modules keep their not-found behavior.
  const healthArchive = healthReadOnlyRoute(p);
  if (healthArchive) return healthArchive;
  if (module === "absent") return notFound(path, search);
  if (module) return module;
  if (p[0] === "tracks" && p[1] === "new" && !p[2]) return { name: "track-new" };
  if (p[0] === "tracks" && p[1]) {
    const fresh = p[2] === "new" && !p[3];
    return fresh ? { name: "track", slug: p[1], fresh: true } : { name: "track", slug: p[1], sid: p[2] || undefined };
  }
  if (p[0] === "assistant") return { name: "profile" };
  if (p[0] === "inbox") {
    const manager = p[1] ? new URLSearchParams(search).get("manager") : null;
    return manager ? { name: "inbox", id: p[1], manager } : { name: "inbox", id: p[1] };
  }
  if (p[0] === "starred" && p.length === 1) return { name: "starred" };
  if (p[0] === "routines") return { name: "routines" };
  // An entry of a kind only a feature module draws is unknown while that module is off. Existing recordings are the
  // exception: their data and Core read-only detail stay available.
  if (p[0] === "kb") return p[1] && p[1] !== "recordings" && p[2] && moduleKBKindAt(p[1]) === "absent" ? notFound(path, search) : { name: "kb", kind: p[1], id: p[2] };
  if (p[0] === "space" && p[1] && p[2]) {
    const workspace = new URLSearchParams(search).get("workspace");
    return { name: "space", mid: p[1], space: p[2], ...(workspace && { workspace }) };
  }
  if (p[0] === "machines") {
    if (!p[1]) return { name: "machines" };
    const view = machineLocation(p, search);
    return view ? { name: "machine", mid: p[1], ...view } : notFound(path, search);
  }
  if (p[0] === "terminal") {
    const list = new URLSearchParams(search).get("view");
    const shown = list === "sessions" || list === "terminals" ? { view: list as "sessions" | "terminals" } : {};
    return p[1] && p[2] ? { name: "terminal", mid: p[1], id: p[2], ...shown } : { name: "terminal", ...shown };
  }
  if (p[0] === "a") return { name: "agents", id: p[1] };
  if (p[0] === "agents") return p[1] === "archived" ? { name: "agent-archives" } : { name: "agent-list" };
  if (p[0] === "compare" && p[1] && !p[2]) return { name: "compare", id: p[1] };
  if (p[0] === "settings") return { name: "settings" };
  if (p[0] === "subscriptions") return { name: "subscriptions" };
  // /plugin/<id>/<ui>: the same page under its name before #1845 (bookmarks)
  if ((p[0] === "extension" || p[0] === "plugin") && p[1] && p[2]) {
    // a reserved id a feature module draws is unknown while the module is off (core/module-host.ts)
    return moduleExtensionAt(p[1]) === "absent" ? notFound(path, search) : { name: "extension", extension: p[1], ui: p[2] };
  }
  if (p[0] === "todos") {
    // the link's own query (#9930): reading the page's made routeAt() on a /todos?id= link open the to-do on screen
    const q = new URLSearchParams(search);
    const id = p[1] || q.get("id") || undefined;
    const manager = q.get("manager");
    const direction = q.get("direction");
    if (!id && direction) return { name: "todos", direction, ...(manager && { manager }) };
    const r: Route = { name: "todos", id, ...(id && manager && { manager }) };
    const view = q.get("view");
    if (view === "board" || view === "list") return { ...r, view };
    if (view === "calendar") {
      const mode = q.get("mode");
      const date = q.get("date");
      return {
        ...r,
        view: "calendar",
        ...(mode === "day" || mode === "week" || mode === "month" ? { mode } : {}),
        ...(date && /^\d{4}-\d{2}-\d{2}$/.test(date) ? { date } : {}),
      };
    }
    return r;
  }
  return notFound(path, search);
}

function notFound(path: string, search: string): Route {
  return { name: "not-found", path: search ? `${path}${search.startsWith("?") ? "" : "?"}${search}` : path };
}

/** The route a link's address opens in this app (`a.href`: absolute, under the sub-path); null for another origin. */
export function routeAt(url: string): Route | null {
  let u: URL;
  try {
    u = new URL(url, location.href);
  } catch {
    return null;
  }
  return u.origin === location.origin ? parse(stripBase(u.pathname), u.search) : null;
}

/** The address of a route as this page reaches it: under the sub-path the hub is served at (lib/base.ts), if any. */
export function href(r: Route, base = BASE): string {
  return base + routePath(r);
}

/** A route's path in the app, without the sub-path (what parse reads). */
export function routePath(r: Route): string {
  switch (r.name) {
    case "request":
      return `/requests/${encodeURIComponent(r.id)}`;
    case "session":
      return `/s/${encodeURIComponent(r.mid)}/${encodeURIComponent(r.sid)}${detailQuery(r)}`;
    case "tab":
      return ["/t", r.mid, r.space, r.slot, ...(r.sid ? [r.sid] : [])].map((x, i) => (i ? encodeURIComponent(x) : x)).join("/") + detailQuery(r);
    case "persona": {
      if (!r.sid) return "/";
      const q = new URLSearchParams();
      if (r.mid) q.set("machine", r.mid);
      if (r.manager) q.set("manager", r.manager);
      if (r.at) q.set("at", r.at);
      const query = q.toString();
      return `/persona/${encodeURIComponent(r.sid)}${query ? `?${query}` : ""}`;
    }
    case "persona-new":
      return "/persona/new";
    case "history":
      return r.origin ? `/history/${encodeURIComponent(r.origin)}` : "/history";
    case "module":
      return modulePath(r);
    case "track-new":
      return "/tracks/new";
    case "track": {
      const base = `/tracks/${encodeURIComponent(r.slug)}`;
      return r.sid ? `${base}/${encodeURIComponent(r.sid)}` : r.fresh ? `${base}/new` : base;
    }
    case "profile":
      return "/assistant";
    case "inbox":
      return r.id ? `/inbox/${encodeURIComponent(r.id)}${r.manager ? `?manager=${encodeURIComponent(r.manager)}` : ""}` : "/inbox";
    case "starred":
      return "/starred";
    case "routines":
      return "/routines";
    case "kb":
      return ["/kb", r.kind, r.kind && r.id].filter(Boolean).map((x, i) => (i ? encodeURIComponent(x as string) : x)).join("/");
    case "machines":
      return "/machines";
    case "machine":
      return machinePath(r.mid, r);
    case "space":
      return `/space/${encodeURIComponent(r.mid)}/${encodeURIComponent(r.space)}` + (r.workspace ? "?" + new URLSearchParams({ workspace: r.workspace }) : "");
    case "terminal": {
      const path = r.mid && r.id ? `/terminal/${encodeURIComponent(r.mid)}/${encodeURIComponent(r.id)}` : "/terminal";
      return r.view ? `${path}?view=${r.view}` : path;
    }
    case "workspace-board":
      return "/workspace-board";
    case "settings":
      return "/settings";
    case "agents":
      return r.id ? `/a/${encodeURIComponent(r.id)}` : "/a";
    case "agent-list":
      return "/agents";
    case "agent-archives":
      return "/agents/archived";
    case "compare":
      return `/compare/${encodeURIComponent(r.id)}`;
    case "todos": {
      const q = new URLSearchParams();
      if (r.view) q.set("view", r.view);
      if (r.view === "calendar") {
        if (r.mode) q.set("mode", r.mode);
        if (r.date) q.set("date", r.date);
      }
      if (r.id) q.set("id", r.id);
      else if (r.direction) q.set("direction", r.direction);
      if ((r.id || r.direction) && r.manager) q.set("manager", r.manager);
      const s = q.toString();
      return s ? `/todos?${s}` : "/todos";
    }
    case "extension":
      return `/extension/${encodeURIComponent(r.extension)}/${encodeURIComponent(r.ui)}`;
    case "subscriptions":
      return "/subscriptions";
    case "lark-oauth":
      return "/lark-oauth";
    case "not-found":
      return r.path;
    default:
      return "/overview";
  }
}

function detailQuery({ view, work, at, standalone }: { view?: SessionView; work?: true; at?: string; standalone?: true }): string {
  const q = [view && `view=${view}`, work && "work=1", at && `at=${encodeURIComponent(at)}`, standalone && "standalone=1"].filter(Boolean).join("&");
  return q ? `?${q}` : "";
}

export function inWorkView(r: Route): boolean {
  return (r.name === "session" || r.name === "tab") && r.work === true;
}

/** Keep the split view when resolving a legacy session address or following a tab inside it. */
export function withWorkView(r: Route, work: boolean): Route {
  return work && (r.name === "session" || r.name === "tab") ? { ...r, work: true } : r;
}

const listeners = new Set<() => void>();
// A replace can change a page's history state without changing its address (#12055): route readers still draw again.
let revision = 0;
// The address Back / Forward last landed on, until a page takes it (takeReturn) or the app navigates on: a page that
// remembers where it was scrolled puts it back only on a return, and a fresh visit starts at the top (#5133).
let returnedTo = "";
if (typeof addEventListener !== "undefined") {
  addEventListener("popstate", () => {
    returnedTo = location.pathname + location.search;
    revision++;
    listeners.forEach((l) => l());
  });
}

/** Whether the page now at the address was reached by Back / Forward (once: the next call says no). */
export function takeReturn(): boolean {
  const back = returnedTo !== "" && returnedTo === location.pathname + location.search;
  returnedTo = "";
  return back;
}

/** `r` as a string is a path in the app ("/settings?tab=…"), without the sub-path. `state` goes with the new entry
 *  (history.state), so Back / Forward to it bring it back and other ways to the same page do not have it. */
export function navigate(r: Route | string, replace = false, state: Record<string, unknown> | null = null) {
  const to = typeof r === "string" ? BASE + r : href(r);
  // the query is part of the address (/s/…?view=term): the same page with another view is somewhere else.
  // A replace still applies at the same address: reselecting a drill-down as a root clears its entry state (#12055).
  if (to === location.pathname + location.search && !replace) return;
  if (!replace && typeof r !== "string" && r.name === "machine") state = { ...state, machineBack: location.pathname + location.search };
  returnedTo = "";
  // From inside an open overlay (a drawer's link, a sheet's action): the new page takes the overlay's entry, so Back
  // from it goes to the page under the overlay, not to a dead copy of it (#1996). A replace keeps the entry's state:
  // an overlay open over the page keeps its marker.
  if (replace) history.replaceState(state ?? history.state, "", to);
  else if (takeOverlayEntry()) history.replaceState(state, "", to);
  else history.pushState(state, "", to);
  revision++;
  listeners.forEach((l) => l());
}

/** The route shows this agent's chat: its own address (/s/…) or its tab's (/t/…/<sid>). */
export function showsSession(r: Route, mid: string, sid: string): boolean {
  return (r.name === "session" || r.name === "tab") && r.mid === mid && r.sid === sid;
}

export function subscribeRoute(listener: () => void) {
  listeners.add(listener);
  return () => { listeners.delete(listener); };
}

export function useRoute(): Route {
  // The query is part of the route (/todos?id=…): a change to it alone must re-render too. The revision also covers
  // replacing the current entry's state at an unchanged address (#12055).
  useSyncExternalStore(
    subscribeRoute,
    () => revision,
  );
  const at = location.pathname + location.search;
  const [path, ...query] = at.split("?");
  return parse(stripBase(path!), query.join("?"));
}

// <a> that navigates in-app on a plain click and still opens a new tab with ⌘ / middle click.
export function onLinkClick(e: React.MouseEvent, to: Route, state: Record<string, unknown> | null = null) {
  if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
  e.preventDefault();
  navigate(to, false, state);
}
