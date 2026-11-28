// The module host (#13434, part of #13430 "Persona Core and modules"; docs/web/web.md "Modules"). Persona Core is the
// app without its optional features; each optional feature is a module under src/modules/<id>/ that reaches the shell
// only through what it registers here. This file knows no module: src/modules/index.ts (the module list, the only place
// module ids appear) registers each module's static descriptor, and the shell reads the registry back (its routes, its
// sidebar entries, its titles), never a module's code.
//
// A module is present only while it is enabled: the hub serves it (the hub flag rule below) and this browser has not
// switched it off (Settings › General › Optional features, core/module-switch.ts). A module that is not enabled
// is absent: no sidebar entry, its addresses resolve as unknown ones, and neither its code chunk nor its strings are
// requested. Its code and its strings load on first use (loadModule), never at start.
// General owns the permanent browser switches. Labs admits only reversible trials with a stable default and a tracked exit.
//
// Field names follow the module contract of #13430 section 4 (`id`, `api`, `needs`, `wants`, `feature`, `shell_places`,
// `settings`, `chat_card_kinds`, `composer_actions`, `chat_accessories`, `i18n_namespaces`), in this codebase's camelCase:
//   id → id · api → api · needs → needs · wants → wants · feature → feature
//   shell_places → routes + extensionPages + navItems + kbKinds + standalonePages
//   settings → settingsTabs + settingsRows · chat_card_kinds → cardRenderers · composer_actions + chat_accessories → chatSlots
//   i18n_namespaces → i18nNamespaces (+ locales)
// Served: id, api (checked against MODULE_API), needs (the ESLint boundary), feature, routes, navItems, extensionPages
// (reserved `/extension/<id>/…` pages, also shell_places), kbKinds (the knowledge detail's `kb.detail` slot, also
// shell_places), standalonePages (a page drawn without the app shell, served by main.tsx; also shell_places),
// settingsRows (rows in a core Settings page's section, SETTINGS_ROW_PLACES; also `settings`), navItems' `shown`,
// i18nNamespaces / locales, chatSlots (#13846: ChatView,
// Composer and Interactions draw what a module's code fills, ChatSlotOutlet), settingsTabs (Settings draws the module's
// page under its own category) and cardRenderers (a pending card of a kind the core has no card for). Declared but not
// yet served: wants. registerModule refuses a descriptor that fills it, or that names a slot, a tab or a card kind the
// shell does not have or keeps for itself, so nothing is silently dropped; loadModule refuses code that does not fill
// what its descriptor names.
// The other contract fields (hub_routes, mcp_tools, prompt_sections, background_jobs, …) belong to the server.
//
// The hub flag rule (agreed for every client on #13844): a module's `feature` is `module.<id>` (moduleFeature). A hub
// that publishes module flags also announces the marker MODULES_FEATURE. A module with a `feature` is off only when the
// hello has the marker and lacks the module's flag: an older hub (no marker) or a hub never heard from leaves it on, so
// defaults never change and nothing waits on the hello to draw. Each hub's last module flags are remembered per hub
// (origin and reverse-proxy subpath, #14431) and used on reload until the new hello arrives, so an off module is neither
// drawn nor fetched while waiting for the hello.
import type { ComponentType, ReactElement, ReactNode, RefObject } from "react";
import { useSyncExternalStore } from "react";
import { addCatalogs, type CatalogLoaders } from "../i18n";
import type { IconName } from "../ui/icons";
import type { UIColor } from "../lib/extension-ui-v6";
import type { InboxItem } from "../lib/inbox";
import type { MailAccount } from "../lib/agent-settings";
import type { FileContent } from "../lib/files";
import type { Route } from "../lib/router";
import type { KBDetail, KBEntry } from "../lib/kb";
import type { State } from "../lib/store";
import type { Interaction, Item, Session } from "../lib/types";
import { moduleSwitchedOff, subscribeModuleSwitches } from "./module-switch";
import { readHubModules, rememberHubModules } from "./module-hub-storage";

import { MODULES_FEATURE } from "./module-flags";

// The hub flag names (the rule above) live in module-flags.ts, so a descriptor can name its flag without the host.
export { MODULES_FEATURE, moduleFeature } from "./module-flags";

/** The typed chat slots a module may fill (#13430 `composer_actions`, `chat_accessories`), each drawn by the chat page
 *  where the comment on ChatSlotComponents says. */
export type ChatSlot = "composer.trailingAction" | "chat.tail" | "chat.bar" | "interaction.asked";
export const CHAT_SLOTS: readonly ChatSlot[] = "composer.trailingAction chat.tail chat.bar interaction.asked".split(" ") as ChatSlot[];

/** What a chat page hands every chat slot: which conversation it shows, and what of it a slot may read or do. */
export interface ChatPageProps {
  mid: string;
  sid: string;
  /** This page's own id: the same conversation may be open twice (a page and a side panel). */
  owner: string;
  session: Session | undefined;
  /** Persona's own conversation, not an Agent's. */
  assistant: boolean;
  /** Who answers here, in words (the Agent's title, or Persona's name). */
  label: string;
  /** Persona's name. */
  persona: string;
  /** Whether a message can be sent from here now (online, and the session takes one). */
  canSend: boolean;
  /** The transcript as the page has it, read when asked. */
  items: () => readonly Item[];
  /** The cards waiting on the user in this conversation. */
  cards: readonly Interaction[];
  /** Stops the Agent's turn, where it can be stopped. */
  onStopAgent?: () => void;
}

/** What a module offers at the composer's trailing end (`composer.trailingAction`): drawn by the composer in its own
 *  style (a tool button; with an empty box and nothing to stop, a `primary` one takes the Send button's place, round). */
export interface ComposerAction {
  icon: IconName;
  /** Its accessible name. */
  name: string;
  /** Its tooltip. */
  label: string;
  /** A primary action draws stronger, and takes the Send button's place while there is nothing to send or stop. */
  primary: boolean;
  onClick: () => void;
  /** Its own `data-` attributes (a test id), over the composer's `data-testid="composer-action"`. */
  attributes?: Readonly<Record<`data-${string}`, string>>;
}

/** Where a module's `chat.bar` is drawn: over the composer (last in the dock above it), or in the composer's place. */
export type ChatBarPlace = "dock" | "composer";

/**
 * The components a module's code gives for the chat slots its descriptor names:
 * - `composer.trailingAction`: mounted for as long as the page shows the conversation (nothing of its own drawn, or a
 *   dialog); it tells the page its action with `offer` (undefined: none now). The first module with an offer has the
 *   place.
 * - `chat.tail`: at the end of the thread, after the last message.
 * - `chat.bar`: drawn twice, with `place`: in the dock over the composer, and where the composer is. While one says
 *   `take(true)`, the composer (its draft kept) and the Assistant's action rows give it their place.
 * - `interaction.asked`: in a pending card's kicker row, after its kind: a mark about the card (or nothing).
 */
export interface ChatSlotComponents {
  "composer.trailingAction"?: ComponentType<ChatPageProps & { offer: (action: ComposerAction | undefined) => void }>;
  "chat.tail"?: ComponentType<ChatPageProps>;
  "chat.bar"?: ComponentType<ChatPageProps & { place: ChatBarPlace; take: (taken: boolean) => void }>;
  "interaction.asked"?: ComponentType<{ interaction: Interaction }>;
}

/** A page of the module (#13430 `shell_places`): every address under `/<base>` and under each alias opens it. */
export interface ModuleRoute {
  /** The address' first segment (`health` for `/health/…`). Must not be one of the core's. */
  base: string;
  /** Older addresses that open the same page, as their leading segments (`["tracks", "health"]` for `/tracks/health/…`). */
  aliases?: readonly (readonly string[])[];
  /** The window title while the page is open. Its key must be in the first screen's catalog: it is read before the
   *  module's own strings are in. */
  title: () => string;
}

/** A sidebar entry of the module (#13430 `shell_places`). Its label is read before the module's code or strings load,
 *  so its keys live in the shell's own namespaces (sidebar.json), not the module's. */
export interface ModuleNavItem {
  /** The entry's key (Settings › General › Sidebar pages keeps the order and the hidden ones by it). */
  key: string;
  /** The address it opens, in the app (`/health`), and the route base it lights up for. */
  path: string;
  icon: IconName;
  label: () => string;
  short?: () => string;
  /** Its icon's color with Colored sidebar icons (lib/sidebar-icons.ts). */
  color?: UIColor;
  /** The core page it comes right after, in the default order. */
  after: string;
  /** When the entry shows at all while the module is on (a browser's own choice, what the hub reads): read with each
   *  look at the sidebar, `subscribe` calls back when it may have changed. None: always. Its address still opens. */
  shown?: { get: () => boolean; subscribe: (listener: () => void) => () => void };
}

/** The places in core Settings pages where a module adds rows (#13404: the Lark connector's section of Settings ›
 *  Connectors). Each is drawn by the page where its comment says, with the rows of the enabled modules naming it. */
export type SettingsRowPlace = "connectors.lark";
export const SETTINGS_ROW_PLACES: readonly SettingsRowPlace[] = ["connectors.lark"];

/** A Settings category of the module (#13430 `settings`), in one of Settings' groups. Its words are read before the
 *  module's strings are in (the Settings sidebar, its search), so its keys live in the shell's namespaces. */
export interface ModuleSettingsTab {
  /** Its `?tab=` value: the module's id, or the id and a hyphenated suffix (`garden`, `garden-plots`). */
  key: string;
  label: () => string;
  /** The one line under it on a phone's list of categories. */
  hint: () => string;
  icon: IconName;
  /** The group it is listed in, after the group's own categories (views/settings/nav.ts NAV_GROUPS). */
  group: "assistant" | "device" | "hub" | "labs";
}

/** What a module's Settings page receives: which of its categories is open. */
export interface ModuleSettingsProps {
  tab: string;
}

/** What a module's card renderer receives: the pending card (its `kind` is one the module names in `cardRenderers`). */
export interface ModuleCardProps {
  interaction: Interaction;
}

/** What a module's page receives: the address' segments after its base, and the shell's sidebar toggle. */
export interface ModulePageProps {
  path: readonly string[];
  onToggleSidebar?: () => void;
}

/** What a module's page drawn under a reserved extension-page id receives (`/extension/<id>/<ui>`). */
export interface ModuleExtensionPageProps {
  extension: string;
  ui: string;
  onToggleSidebar?: () => void;
}

/** The parts of a knowledge entry's page that a module draws for an entry of a kind it names (`kbKinds`, the
 *  `kb.detail` slot): the core lays the page out and puts each part where it goes. All optional. */
export interface ModuleKBDetailParts {
  /** Buttons after "Ask Persona". */
  actions?: ReactNode;
  /** Sections first in the page's body. */
  lead?: ReactNode;
  /** Sections after the entry's own facts and imported notes. */
  sections?: ReactNode;
  /** Sections after the code directories. */
  tail?: ReactNode;
  /** Dialogs the parts open. */
  dialogs?: ReactNode;
  /** The module shows the entry's summary itself: the page leaves its profile out. */
  hideProfile?: boolean;
}

/** What the `kb.detail` slot's component receives: the entry and its page's means, and `children`, the page laid out
 *  around the parts it gives. It renders `children(parts)`; before its code is in, the page draws with no parts. */
export interface ModuleKBDetailProps {
  kind: string;
  id: string;
  /** Opened from a search. */
  query?: string;
  /** The entry's detail; undefined until read. */
  detail?: KBDetail;
  entry: KBEntry;
  /** The credentials and assistant the page reads and writes with. */
  auth: { token: string; tokenOwner: string; headers: Record<string, string> };
  /** Whether the page still belongs to the same owner and assistant. */
  current: () => boolean;
  manager: string;
  /** Reads the detail again. */
  reload: (signal?: AbortSignal) => Promise<boolean>;
  onSaved: () => void;
  onOpen: (e: KBEntry) => void;
  /** The page's scroller. */
  scroller: RefObject<HTMLDivElement | null>;
  children: (parts: ModuleKBDetailParts) => ReactNode;
}

/** File kinds offered to optional previews. The viewer keeps the file and its download action. */
export type ModuleFileKind = "office" | "workbook";

export interface ModuleFilePreviewProps {
  mid: string;
  cwd?: string;
  file: FileContent;
  onDownload: () => void;
}

export interface ModuleMailboxSettingsProps {
  manager?: string;
  hideInboxSwitch?: boolean;
  reloadKey?: number;
  onHasAccounts?: (hasAccounts: boolean) => void;
}

export interface ModuleMailboxInboxSwitchProps {
  manager?: string;
  grouped?: boolean;
}

/** A module's code, loaded on first use. */
export interface ModuleCode {
  mailbox?: {
    Body: ComponentType<{ item: InboxItem; onLatest?: () => void }>;
    Compose: ComponentType<{ accounts: MailAccount[]; onClose: () => void }>;
    Settings: ComponentType<ModuleMailboxSettingsProps>;
    InboxSwitch: ComponentType<ModuleMailboxInboxSwitchProps>;
    accounts: (manager?: string) => Promise<MailAccount[]>;
  };
  FilePreview?: ComponentType<ModuleFilePreviewProps>;
  /** The page its routes open. */
  Page?: ComponentType<ModulePageProps>;
  /** The page its `extensionPages` ids open. */
  ExtensionPage?: ComponentType<ModuleExtensionPageProps>;
  /** The parts of a knowledge entry's page for the `kbKinds` it names. */
  KBDetail?: ComponentType<ModuleKBDetailProps>;
  /** What it draws in the chat slots its descriptor names (`chatSlots`). */
  chat?: ChatSlotComponents;
  /** Its Settings pages, by `settingsTabs` key. */
  settings?: Readonly<Record<string, ComponentType<ModuleSettingsProps>>>;
  /** Its pending cards, by `cardRenderers` kind. */
  cards?: Readonly<Record<string, ComponentType<ModuleCardProps>>>;
  /** Its rows in core Settings pages, by `settingsRows` place. */
  settingsRows?: Partial<Readonly<Record<SettingsRowPlace, ComponentType>>>;
  /** The page its `standalonePages` open (main.tsx draws what it returns as the whole page, with no app shell), given
   *  the page's address. It takes from the address what must not stay there (a token in the fragment) before App
   *  could read it; null when the address is not one of its pages after all, and the app opens instead. */
  Standalone?: (address: string) => ReactElement | null;
}

/** A module's static descriptor: small, imported at start by the module list, naming what it contributes. */
export interface ModuleDescriptor {
  /** Lower-case, hyphenated (#13430 section 2): the directory under src/modules. */
  id: string;
  /** The hub feature flag that says the hub serves it, `moduleFeature(id)` (`module.<id>`); none for a module that
   *  does not depend on the hub. Off only on a hub that publishes module flags (MODULES_FEATURE) without this one. */
  feature?: string;
  /** The module API it is written against (#13430 `api`); MODULE_API when absent. Another version is refused. */
  api?: number;
  /** Other modules it may import (ESLint holds it to this list: eslint.modules.config.mjs). */
  needs?: readonly string[];
  /** Declared, not yet served: modules it uses when they are there (#13430 `wants`). */
  wants?: readonly string[];
  /** Its name and one line on what it does, for the switch in Settings › General › Optional features (shell namespaces). */
  title: () => string;
  description: () => string;
  routes?: readonly ModuleRoute[];
  /** Reserved extension-page ids it draws (`/extension/<id>/<ui>`, #13430 `shell_places`): a page the hub serves as a
   *  declarative extension page of its own, under an id no extension can have (ids starting with "-"). */
  extensionPages?: readonly string[];
  navItems?: readonly ModuleNavItem[];
  /** Pages it draws on their own, without the app shell: no sign-in flow, no sidebar (#14404, #13430 `shell_places`),
   *  each the first segment of its addresses (`screen` for `/screen/<machine>`). Before App loads, main.tsx asks
   *  src/modules/standalone.ts (the modules that have one) and draws `ModuleCode.Standalone`; there the hub's flag is
   *  read from one hello of its own (hubServesHere). Inside the app, standaloneAt / standaloneOn. The code comes from
   *  the module's `standalone.tsx`, which the build preloads on these addresses and lets load without waiting for the
   *  rest of the catalog (vite.config.ts): that file and what it imports statically read only the first screen's core
   *  strings, and the module's own. Not a core address, nor another module's page. */
  standalonePages?: readonly string[];
  /** Knowledge entry kinds whose page it adds parts to (`kb.detail`, #13430 `shell_places`): `ModuleCode.KBDetail`.
   *  One module per kind. */
  kbKinds?: readonly string[];
  /** Supplies live mailbox actions alongside archived Inbox items. Only one provider may own them. */
  mailbox?: true;
  /** File kinds drawn by this module's FilePreview. Each kind has one owner. */
  filePreviews?: readonly ModuleFileKind[];
  /** Its Settings categories; its code's `settings` draws each. */
  settingsTabs?: readonly ModuleSettingsTab[];
  /** Places in core Settings pages it adds rows to (SETTINGS_ROW_PLACES); its code's `settingsRows` draws each. */
  settingsRows?: readonly SettingsRowPlace[];
  /** The chat slots it fills; its code's `chat` draws each. */
  chatSlots?: readonly ChatSlot[];
  /** The pending-card kinds it renders, none the core has a card for; its code's `cards` draws each. */
  cardRenderers?: readonly string[];
  /** Its namespaces (`<id>` or `<id>-…`), loaded with its code from the shared or module-local locale directory. */
  i18nNamespaces?: readonly string[];
  /** Those namespaces' catalog, one loader per language (`?catalog=module-<id>`, web/i18n-catalogs.ts). */
  locales?: CatalogLoaders;
  /** Its code: a dynamic import(), so it is its own chunk. */
  load: () => Promise<ModuleCode>;
}

/** Whether a module is there now: "on", or "off" (switched off here, or a hub that publishes module flags lacks its
 *  flag). */
export type ModuleState = "on" | "off";

/** What the host reads of the hub: the hello's features, whether it loaded, and whether the hello arrived. The shell
 *  connects the store (connectModuleHub, App.tsx); the router imports this file, so it does not import the store itself.
 *  Until a hello arrives, any remembered module flags for this hub apply; an unseen hub has every module on. */
type HubState = Pick<State, "features" | "loaded"> & Partial<Pick<State, "conn">>;
const NO_HUB: HubState = { features: [], loaded: false };
let hubState: () => HubState = () => NO_HUB;
let offHubConn: (() => void) | undefined;

const hasLiveHello = (s: Pick<State, "features"> & Partial<Pick<State, "loaded" | "conn">>) =>
  Boolean(s.features.length || s.loaded || s.conn === "online");

export function connectModuleHub(get: () => HubState, subscribe: (l: () => void) => () => void): void {
  offHubConn?.();
  hubState = get;
  // Both hello and reset() replace state.features with a new array reference, so watching reference changes persists
  // every hello's module flags and forgets them on reset() even before a hello has flushed.
  let lastFeatures: readonly string[] = get().features;
  offHubConn = subscribe(() => {
    const s = get();
    if (s.features !== lastFeatures) rememberHubModules((lastFeatures = s.features));
    if (enabledKey() !== lastEnabled) changed();
  });
  changed();
}

const MODULE_ID = /^[a-z][a-z0-9]*(-[a-z0-9]+)*$/;

/** The module API this shell serves (#13430 `api`). */
export const MODULE_API = 1;

/** The pending-card kinds the core draws itself (views/chat/Interactions.tsx InteractionCard): no module renders them. */
export const CORE_CARD_KINDS: ReadonlySet<string> = new Set("question plan permission screen elicitation".split(" "));


/** The first segments of the core's own addresses (lib/router.ts parse, and what the hub serves beside the app): a
 *  module's base or standalone page may not be one. lib/router.test.ts keeps it in step with parse. */
export const CORE_PATHS: ReadonlySet<string> = new Set([
  "s", "t", "persona", "j", "workspace-board", "requests", "overview", "history", "tracks", "assistant", "inbox",
  "starred", "routines", "kb", "space", "machines", "terminal", "a", "agents", "compare", "settings", "subscriptions",
  "extension", "plugin", "todos", "lark-oauth", "api", "login", "share", "admin", "assets",
]);
/** Core addresses whose deeper paths a module may take as an alias: `/tracks/<slug>` of a space it has its own page for. */
const ALIASABLE: ReadonlySet<string> = new Set(["tracks"]);

const routePaths = (r: ModuleRoute): (readonly string[])[] => [[r.base], ...(r.aliases ?? [])];
/** Whether two paths would route the same address: one is a prefix of the other. */
const overlaps = (a: readonly string[], b: readonly string[]) => a.slice(0, Math.min(a.length, b.length)).every((x, i) => x === b[i]);
const registry = new Map<string, ModuleDescriptor>();
const listeners = new Set<() => void>();
let revision = 0;
let lastEnabled = "";
const changed = () => {
  lastEnabled = enabledKey();
  revision++;
  listeners.forEach((l) => l());
};

/** Registers a module's descriptor (src/modules/index.ts). A second registration of the same id, a malformed id, or a
 *  route base another module already claims is a programming error. */
export function registerModule(d: ModuleDescriptor): void {
  const fail = (msg: string): never => {
    throw new Error(`module "${d.id}" ${msg}`);
  };
  const checkUnique = (items: readonly string[] | undefined, what: string, of: (m: ModuleDescriptor) => readonly string[] | undefined) => {
    for (const x of items ?? []) {
      const other = [...registry.values()].find((m) => of(m)?.includes(x));
      if (other) fail(`${what} "${x}", which "${other.id}" has`);
    }
  };
  if (!MODULE_ID.test(d.id)) throw new Error(`module id "${d.id}" is not lower-case and hyphenated`);
  if (registry.has(d.id)) fail("is registered twice");
  if ((d.api ?? MODULE_API) !== MODULE_API) fail(`is written for module API ${d.api}; this shell serves ${MODULE_API}`);
  if (d.wants?.length) fail("contributes wants, which the web shell does not serve yet");
  for (const slot of d.chatSlots ?? []) {
    if (!CHAT_SLOTS.includes(slot)) fail(`fills the chat slot "${slot}", which the web shell does not have`);
  }
  for (const place of d.settingsRows ?? []) {
    if (!SETTINGS_ROW_PLACES.includes(place)) throw new Error(`module "${d.id}" adds rows to the Settings place "${place}", which the web shell does not have`);
  }
  for (const tab of d.settingsTabs ?? []) {
    if (tab.key !== d.id && !tab.key.startsWith(`${d.id}-`)) fail(`claims the Settings category "${tab.key}", which is not its id or its id and a suffix`);
  }
  for (const kind of d.cardRenderers ?? []) {
    if (CORE_CARD_KINDS.has(kind)) fail(`renders the card kind "${kind}", which the core draws`);
  }
  checkUnique(d.cardRenderers, "renders the card kind", (m) => m.cardRenderers);
  const standaloneOwner = (base: string) => [...registry.values()].find((m) => m.standalonePages?.includes(base));
  for (const r of d.routes ?? []) {
    for (const path of routePaths(r)) {
      if (path.length === 1 && CORE_PATHS.has(path[0]!)) fail(`claims /${path[0]}, a core address`);
      const page = standaloneOwner(path[0]!);
      if (page) fail(`claims /${path.join("/")}, which "${page.id}" draws as a page of its own`);
      if (path.length > 1 && CORE_PATHS.has(path[0]!) && !ALIASABLE.has(path[0]!)) fail(`claims /${path.join("/")}, under the core address /${path[0]}`);
      for (const other of registry.values()) {
        const taken = (other.routes ?? []).flatMap(routePaths).find((o) => overlaps(o, path));
        if (taken) fail(`claims /${path.join("/")}, which "${other.id}" has (/${taken.join("/")})`);
      }
    }
  }
  for (const id of d.extensionPages ?? []) {
    if (!/^-[a-z][a-z0-9-]*$/.test(id)) fail(`claims the extension page id "${id}", which is not a reserved one (it must start with "-")`);
  }
  for (const base of d.standalonePages ?? []) {
    if (!/^[a-z][a-z0-9-]*$/.test(base)) fail(`claims the page of its own "${base}", which is not one address segment`);
    if (CORE_PATHS.has(base)) fail(`claims /${base} as a page of its own, a core address`);
    const other = standaloneOwner(base);
    if (other) fail(`claims /${base} as a page of its own, which "${other.id}" has`);
    const routed = [...registry.values(), d].find((m) => (m.routes ?? []).flatMap(routePaths).some((p) => p[0] === base));
    if (routed) fail(`claims /${base} as a page of its own, which "${routed.id}" routes in the app`);
  }
  checkUnique(d.extensionPages, "claims the extension page id", (m) => m.extensionPages);
  checkUnique(d.kbKinds, "claims the knowledge kind", (m) => m.kbKinds);
  checkUnique(d.filePreviews, "previews the file kind", (m) => m.filePreviews);
  if (d.mailbox && registeredModules().some((m) => m.mailbox)) fail("the mailbox already has a provider");
  registry.set(d.id, d);
  changed();
}

/** Every registered module, in registration order (enabled or not). */
export function registeredModules(): readonly ModuleDescriptor[] {
  return [...registry.values()];
}

export function moduleById(id: string): ModuleDescriptor | undefined {
  return registry.get(id);
}

/** Whether the hub serves a module with this `feature` (the hub flag rule): it names none, the live hello (or before it,
 *  this hub's remembered answer) publishes no module flags, or it includes `feature`. */
export function hubServes(feature: string | undefined, s: Pick<State, "features"> & Partial<Pick<State, "loaded" | "conn">> = hubState()): boolean {
  if (!feature) return true;
  if (hasLiveHello(s)) return !s.features.includes(MODULES_FEATURE) || s.features.includes(feature);
  const kept = readHubModules();
  return !kept || kept.includes(feature);
}

/** Whether `d` is there given the hub's hello features (or remembered flags before hello) and this browser's switch. */
export function moduleState(d: ModuleDescriptor, s: HubState = hubState()): ModuleState {
  if (moduleSwitchedOff(d.id)) return "off";
  return hubServes(d.feature, s) ? "on" : "off";
}

export const moduleOn = (id: string, s?: HubState) => {
  const d = registry.get(id);
  return d !== undefined && moduleState(d, s) === "on";
};

/** The enabled modules, for code outside React. */
export function enabledModules(s?: HubState): ModuleDescriptor[] {
  return [...registry.values()].filter((d) => moduleState(d, s) === "on");
}

/** The route a path's segments open when they are an enabled module's page; "absent" when they are the
 *  address of a module that is not there (the router answers it as an unknown address); null when no module has it. */
export function moduleRouteAt(segments: readonly string[]): Extract<Route, { name: "module" }> | "absent" | null {
  for (const d of registry.values()) {
    for (const r of d.routes ?? []) {
      const prefix = routePaths(r).find((p) => p.length <= segments.length && p.every((x, i) => segments[i] === x));
      if (!prefix) continue;
      if (moduleState(d) === "off") return "absent";
      return { name: "module", module: d.id, base: r.base, path: segments.slice(prefix.length) };
    }
  }
  return null;
}

const moduleWith = (has: (d: ModuleDescriptor) => boolean | undefined): ModuleDescriptor | "absent" | null => {
  for (const d of registry.values()) if (has(d)) return moduleState(d) === "off" ? "absent" : d;
  return null;
};

/** The module that draws an extension-page id: its descriptor while it is on, "absent" while it is off
 *  (the router answers the address as an unknown one), null for an id no module has. */
export function moduleExtensionAt(extension: string): ModuleDescriptor | "absent" | null {
  return moduleWith((d) => d.extensionPages?.includes(extension));
}

/** The module that adds parts to a knowledge entry's page of this kind: its descriptor while it is on (or pending),
 *  "absent" while it is off, null for a kind no module names. */
export function moduleKBKindAt(kind: string): ModuleDescriptor | "absent" | null {
  return moduleWith((d) => d.kbKinds?.includes(kind));
}

/** The module that draws the page at these address segments on its own (`standalonePages`): its descriptor while it
 *  is on, "absent" while it is off (main.tsx drops the address' fragment, so a token meant for the page is not taken
 *  for a sign-in, and the app answers the address as an unknown one), null for an address no module has. Inside the
 *  app; before App, main.tsx asks src/modules/standalone.ts, which does the same with this browser's switch. */
export function standaloneAt(segments: readonly string[]): ModuleDescriptor | "absent" | null {
  const base = segments[0];
  if (!base) return null;
  for (const d of registry.values()) {
    if (!d.standalonePages?.includes(base)) continue;
    return moduleState(d) === "off" ? "absent" : d;
  }
  return null;
}

/** Whether an enabled module draws the page at /<base> on its own: what the core's links to it ask before they show. */
export function standaloneOn(base: string, s?: Pick<State, "features" | "loaded">): boolean {
  return enabledModules(s).some((d) => d.standalonePages?.includes(base));
}

/** A module route's address in the app. */
export function modulePath(r: { base: string; path: readonly string[] }): string {
  return `/${[r.base, ...r.path].map(encodeURIComponent).join("/")}`;
}

/** A module page's window title. */
export function moduleTitle(r: { module: string; base: string }): string | undefined {
  return registry.get(r.module)?.routes?.find((x) => x.base === r.base)?.title();
}

/** A module route's sidebar entry: its module's entry for that base. */
export function moduleNavItem(r: { module: string; base: string }): ModuleNavItem | undefined {
  return registry.get(r.module)?.navItems?.find((n) => n.path === `/${r.base}`);
}

/** The sidebar key a module route lights. */
export function moduleNavKey(r: { module: string; base: string }): string {
  return moduleNavItem(r)?.key ?? `module:${r.module}`;
}

/** The enabled modules' sidebar entries, those with a `shown` condition only while it holds. */
export function enabledNavItems(s?: Pick<State, "features" | "loaded">): ModuleNavItem[] {
  return enabledModules(s).flatMap((d) => d.navItems ?? []).filter((n) => !n.shown || n.shown.get());
}

/** Calls `l` when what is enabled may have changed: a registration, the switch, the hub's features. */
export function subscribeModules(l: () => void): () => void {
  listeners.add(l);
  const offSwitch = subscribeModuleSwitches(() => {
    lastEnabled = enabledKey();
    l();
  });
  const onChange = () => {
    if (enabledKey() !== lastEnabled) changed();
  };
  // the sidebar entries' own conditions (`shown`)
  const offShown = [...registry.values()].flatMap((d) => d.navItems ?? []).flatMap((n) => (n.shown ? [n.shown.subscribe(onChange)] : []));
  return () => {
    listeners.delete(l);
    offSwitch();
    offShown.forEach((off) => off());
  };
}

const shownKey = (d: ModuleDescriptor) => (d.navItems ?? []).map((n) => (n.shown && !n.shown.get() ? "-" : "+")).join("");
const enabledKey = () => [...registry.values()].map((d) => `${d.id}:${moduleState(d)}${shownKey(d)}`).join(",");
const snapshot = () => `${revision}|${enabledKey()}`;

/** Re-renders the caller when the enabled modules change; returns a key of their states. */
export function useModuleStates(): string {
  return useSyncExternalStore(subscribeModules, snapshot, snapshot);
}

const loads = new Map<string, Promise<ModuleCode>>();
/** Each module's code once it is in, for what draws it in place (the chat slots) rather than as a lazy page. */
const loaded = new Map<string, ModuleCode>();

/** What `code` must fill for `d`: every chat slot, Settings category and card kind the descriptor names. */
function checkCode(d: ModuleDescriptor, code: ModuleCode): ModuleCode {
  const miss = (keys: readonly string[] | undefined, have: Readonly<Record<string, unknown>> | undefined, what: string) =>
    (keys ?? []).filter((x) => !have?.[x]).map((x) => `the ${what} "${x}"`);
  const missing = [
    ...(d.mailbox && !code.mailbox ? ["the mailbox"] : []),
    ...miss(d.chatSlots, code.chat as Readonly<Record<string, unknown>> | undefined, "chat slot"),
    ...(d.filePreviews?.length && !code.FilePreview ? ["the file preview"] : []),
    ...miss(d.settingsTabs?.map((x) => x.key), code.settings, "Settings category"),
    ...miss(d.settingsRows, code.settingsRows, "Settings rows"),
    ...miss(d.cardRenderers, code.cards, "card kind"),
    ...(d.kbKinds?.length && !code.KBDetail ? [`the knowledge kind "${d.kbKinds[0]}"`] : []),
    ...(d.standalonePages?.length && !code.Standalone ? [`the page of its own /${d.standalonePages[0]}`] : []),
  ];
  if (missing.length) throw new Error(`module "${d.id}" names ${missing.join(", ")}, which its code does not draw`);
  return code;
}

/** Loads a module's strings and code once (again after a failure). Refused for a module that is not on, so an absent
 *  module's chunk is never requested. */
export function loadModule(id: string): Promise<ModuleCode> {
  const d = registry.get(id);
  if (!d) return Promise.reject(new Error(`no module "${id}"`));
  if (moduleState(d) !== "on") return Promise.reject(new Error(`module "${id}" is not enabled`));
  let p = loads.get(id);
  if (!p) {
    // strings first: a module's code may read them as it loads (module-scope t() calls)
    p = (d.locales ? addCatalogs(d.locales) : Promise.resolve())
      .then(() => {
        // The switch or hub flags may have changed while the catalog was in flight.
        if (moduleState(d) !== "on") throw new Error(`module "${id}" is not enabled`);
        return d.load();
      })
      .then((code) => checkCode(d, code));
    p.then(
      (code) => {
        loaded.set(id, code);
        changed();
      },
      () => loads.delete(id),
    );
    loads.set(id, p);
  }
  return p;
}

/** A module's code if it is in already (loadModule resolved), for drawing in place. */
export function loadedModule(id: string): ModuleCode | undefined {
  return loaded.get(id);
}

/** The enabled modules that name a chat slot, each with what its code draws there once it is in. */
export function chatSlotFillers<S extends ChatSlot>(slot: S, s?: Pick<State, "features" | "loaded">): { id: string; Component: NonNullable<ChatSlotComponents[S]> | undefined }[] {
  return enabledModules(s)
    .filter((d) => d.chatSlots?.includes(slot))
    .map((d) => ({ id: d.id, Component: loaded.get(d.id)?.chat?.[slot] as NonNullable<ChatSlotComponents[S]> | undefined }));
}

export { enabledSettingsTabs } from "./module-settings";

/** The enabled modules that add rows to a Settings place, each with what its code draws there once it is in. */
export function settingsRowFillers(place: SettingsRowPlace, s?: Pick<State, "features" | "loaded">): { id: string; Component: ComponentType | undefined }[] {
  return enabledModules(s)
    .filter((d) => d.settingsRows?.includes(place))
    .map((d) => ({ id: d.id, Component: loaded.get(d.id)?.settingsRows?.[place] }));
}

/** The enabled module that renders a card kind. */
export function cardRendererOf(kind: string, s?: Pick<State, "features" | "loaded">): ModuleDescriptor | undefined {
  return enabledModules(s).find((d) => d.cardRenderers?.includes(kind));
}

/** Tests only: forget every module and load. */
export function resetModulesForTest(): void {
  registry.clear();
  loads.clear();
  loaded.clear();
  changed();
}
