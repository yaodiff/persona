import { managedProvider } from "../lib/compatibility";
// Settings (docs/web/web.md): appearance, where the assistant runs, "Models" (every launcher: rc-file and PATH ones synced, the
// built-in claude, Persona ones kept by the hub — docs/integrations/launchers.md), this browser and the other devices, about. The same data as the app's Settings.
import { BASE, stripBase } from "../lib/base";
import { href, subscribeRoute } from "../lib/router";
import { takeOverlayEntry } from "../lib/back-close";
import { useScrollMemory } from "../lib/scroll-memory";
import { Suspense, useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import { api, enc, errorText } from "../lib/api";
import { groupDevices, type DeviceGroupKey } from "../lib/devices";
import { ago } from "../lib/format";
import { agentName, isTraeCli } from "../lib/agent-kind";
import { install, useInstall, type InstallState } from "../lib/pwa";
import { useStore } from "../lib/store";
import { composing } from "../lib/send-history";
import { Button, cx, Dialog, EASE, IconButton, Loading, Menu, Spinner, toast, useMenu } from "../ui/atoms";
import { NoticeDot } from "../ui/NoticeDot";
import { useHasUnseenLabs } from "../lib/labs-seen";
import { Icon, type IconName } from "../ui/icons";
import { ExtensionThemePicker } from "../ui/ExtensionThemePicker";
import { setTheme, useTheme, type Theme } from "./theme";
import { Group, Row, Pill, RowConfirm, SettingsHint, Switch } from "./settings/SettingsParts";
import { LanguageRow } from "./settings/LanguageRow";
import { FontRows, PhoneNavRow, RecentChatsRow, SidebarIconsRow, SidebarUsageRow, SurfaceStyleRow, TextSizeRow, ThemeColorPicker } from "./settings/LookSettings";
import { setStarredEnabled, useStarredSetting } from "../lib/starred-summary";
import { setNeedsYouAutoOpen, useNeedsYouAutoOpen } from "../lib/needs-you-auto-open";
import { setReplyMotionEnabled, useReplyMotionSetting } from "../ui/stream/motion";
import { setAgentDetails, useAgentDetails } from "../lib/context-usage";
import { setSidebarTerminals, useSidebarTerminals } from "../lib/sidebar-terminals";
import { HubList } from "./Hubs";
import { useDisconnect } from "./DisconnectBrowser";
import { KnowledgeSelection } from "./settings/KnowledgeSelection";
import { SavedPrompts } from "./settings/SavedPrompts";
import { useInboxSourcesShown } from "../lib/subscriptions";
import { ModelConfiguration } from "./settings/ModelConfiguration";
import { RuntimesCard } from "./settings/RuntimesCard";
import { ExecutionChannelsCard } from "./settings/ExecutionChannelsCard";
import { ChannelAliasNote, ChannelAliasPill, launcherWriteFailed } from "./settings/ChannelAliasNote";
import { HiddenAgentDirs } from "./settings/HiddenAgentDirs";
import { HerdrSessions } from "./settings/HerdrSessions";
import { AgentSettings } from "./settings/AgentSettings";
import { SidebarPagesGroup } from "./settings/SidebarPages";
import { StartPageGroup } from "./settings/StartPage";
import { AgentsDisplayRow } from "./settings/AgentsDisplayRow";
import { AgentGroup } from "./settings/AgentGroup";
import { AssistantSettings } from "./settings/AssistantSettings";
import { PersonaPicker } from "./assistant/PersonaPicker";
import { TriageSettings } from "./settings/TriageSettings";
import { TranslationSettings } from "./settings/TranslationSettings";
import { ShortcutSettings } from "./settings/ShortcutSettings";
import { NotificationSettings } from "./settings/NotificationSettings";
import { DesktopSettings } from "./settings/DesktopSettings";
import { DevModeRows } from "./settings/DevModeSettings";
import { ChannelList, ChannelPage } from "./settings/Channels";
import { ConnectDevice } from "./settings/ConnectDevice";
import { settingsTarget } from "../lib/channels";
import { routeSub } from "../lib/extension-market";
import { otherManager, ownedSub, pinManagerAt, sameManager, useChosenManager, useShownManager } from "../lib/manager-choice";
import { HubSettings } from "./settings/HubSettings";
import { RecoverySettings } from "./settings/RecoverySettings";
import { ManagersSettings } from "./settings/ManagersSettings";
import { LauncherQuota } from "./settings/LauncherQuota";
import { LauncherExtensionSections } from "./settings/ExtensionView";
import { hubHasExt } from "../lib/extension-api";
import { hostOf, type ProviderView } from "../lib/providers";
import { referenceState } from "../lib/secret-reference";
import { forgetProviderNames, providerIdNote, providerTitle } from "../lib/provider-names";
import { harnessGroups, harnessLabel, scopeSummary } from "../lib/provider-scope";
import { foundAliases, type EditDraft, type FoundAlias } from "../lib/provider-paste";
import { dropImportLink, importLinkHeld, onImportLink } from "../lib/import-link";
import { linkable } from "../lib/provider-link";
import { ProviderLinkCopy } from "./settings/ProviderLinkCopy";
import { builtinRows, loadLaunchers, rcFilesOf, rcGroups, syncStateText, type LauncherSync } from "../lib/launcher-list";
import { TerminalModeRow } from "./settings/TerminalModeSetting";
import { AgentOpenRow } from "./settings/AgentOpenSetting";
import { SeenRowsRow } from "./settings/SeenRowsSetting";
import { AGENT_KINDS, ASSISTANT_TABS, categoryState, FROM_LIST, INTEGRATIONS_SETTINGS_REGISTRATION, integrationChildState, integrationParent, isCoreTab, isModuleTab, itemIsPage, itemState, navGroups, openedFromSub, redirectLegacySettings, settingsAt, settingsHref, SENT_SUB, stepsToList, tabInfo, TAB_INFO, type AgentTab, type SettingsSection, type Tab } from "./settings/nav";
import { decodeIntegrationsRoute, integrationHref, integrationManager, integrationScope, isIntegrationDetailRoute, isIntegrationTab, isLegacyIntegrationTab, readIntegrationsAddress, type IntegrationsRoute, type IntegrationView } from "./settings/integrations-route";
import { useOtherIntegrationsMode } from "./settings/integrations-mode";
import { moduleFeaturesSource } from "../core/module-features";
import { useModuleStates } from "../core/module-host";
import { ModuleSettingsPage } from "../core/ModuleSettingsPage";
import { searchSettings, type SearchGroup } from "./settings/search";
import { GroupAction, GroupFooter, GroupTitle, hasFooter } from "./settings/SettingsGroups";
import { SHEET_QUERY, useMedia } from "../ui/overlay";
import { lazyDialog, lazyOverlay, useOpened } from "./LazyOverlay";
import { lazyPage } from "../lib/lazy-page";
import { formatList, t } from "../i18n";
import { settingsPrefill } from "../lib/settings-prefill";

// ── types (docs/integrations/launchers.md) ───────────────────────────

interface LauncherDetail {
  agent?: string;
  base_url?: string;
  auth?: string;
  models?: { role: string; env: string; model: string }[];
  context_window?: number;
  compact_window?: number;
  effort?: string;
  timeout_ms?: number;
  args?: string[];
  env?: { key: string; value: string }[];
  error?: string;
}

/** GET /api/v1/launchers' entry: a managed provider's fields (lib/providers.ts) plus the ~/.zshrc ones'. */
interface LauncherView extends ProviderView {
  /** zshrc: an rc file's (~/.zshrc, ~/.bashrc); path: a wrapper script on the computer's PATH (#5954); builtin: plain claude (no edit, can't be hidden). */
  source: "zshrc" | "path" | "persona" | "agy" | "builtin" | string;
  agent?: "claude" | "codex" | "agy" | string;
  /** Managed: pi can use it too on the computers in its scope (docs/integrations/pi.md "Persona's model providers"). */
  pi?: boolean;
  /** A managed channel's read-only alias (docs/assistant/execution-spec.md): edited on the Channels card. */
  channel?: string;
  machines?: string[];
  hidden?: boolean;
  /** zshrc: the rc files it was found in; absent from older hubs (all ~/.zshrc). */
  rc_files?: string[];
  /** Machines of `machines` that did not answer this time: their entry is from their last list. */
  stale?: string[];
  /** builtin: Claude Code's version per machine. */
  versions?: Record<string, string>;
  definitions?: Record<string, string>;
  details?: Record<string, LauncherDetail>;
}

interface LauncherCheck {
  ok: boolean;
  agent?: string;
  model?: string;
  answer?: string;
  millis: number;
  error?: string;
}

interface DeviceToken {
  id: string;
  name: string;
  kind: string;
  role: string;
  scopes?: string[];
  created_at?: string;
  last_seen?: string;
  /** Set once revoked: the hub keeps listing it (lib/devices.ts). */
  revoked_at?: string;
}

const enLauncher = (n: string) => `/launchers/${enc(n)}`;

/** What the current assistant's pages ask of its manager besides their merged lists and configuration editors (which
 *  name it themselves): the model providers, session triage, mail accounts, the chat connector (all its routes: accounts, status, message display), the
 *  chat bot, and the messages sent as you. Pinned to the current assistant while one of those pages is open. */
const ASSISTANT_PREFIXES = ["/launchers", "/agents/triage", "/mail", "/lark", "/bots", "/connectors/sent"];

export function tokens(n: number): string {
  if (n >= 1_000_000 && n % 100_000 === 0) return `${n % 1_000_000 === 0 ? n / 1_000_000 : (n / 1_000_000).toFixed(1)}M`;
  if (n >= 1000) return `${Math.floor(n / 1000)}K`;
  return String(n);
}

// ── page ────────────────────────────────────────────────

// The Extensions and Connectors pages, with what only they draw (the market, an extension's own settings and sign-in, the
// hub's connectors, the Lark bot, the sent-message rules), are about 44% of what the Settings chunk renders (#8897):
// chunks of their own, loaded when one of them is opened, or ahead of it when the pointer or the focus reaches its row in
// the sidebar (NavRow). Every other category draws with the Settings chunk as before.
const Extensions = lazyPage(() => import("./settings/Extensions").then((m) => m.Extensions));
const Connectors = lazyPage(() => import("./settings/Connectors").then((m) => m.Connectors));
const Integrations = lazyPage(() => import("./settings/Integrations").then((m) => m.Integrations));
const SourcesUI = lazyPage(() => import("./subscriptions/SourcesUI").then((m) => m.SourcesUI));
// Labs and the shared configuration form load on first use.
const LabsSettings = lazyPage(() => import("./settings/Labs").then((m) => m.LabsSettings));
const ModuleFeaturesForm = lazyPage(() => import("./settings/configuration/ConfigurationForm").then((m) => m.ConfigurationForm));
const TAB_PAGES: Partial<Record<Tab, { preload: () => Promise<unknown> }>> = { extensions: Extensions, connectors: Connectors, integrations: Integrations, sources: SourcesUI };
// a failed load is not kept (lazyPage): opening the page asks again, and PageBoundary shows the failure then
const preloadTab = (k: Tab) => void TAB_PAGES[k]?.preload().catch(() => {});

function readSettingsLocation() {
  return { path: stripBase(location.pathname), search: location.search, hash: location.hash, state: history.state as unknown };
}

function popSettingsItem() {
  if (!takeOverlayEntry()) return history.back();
  history.replaceState(null, "", location.href);
  history.go(-2);
}

export function Settings({ onToggleSidebar }: { onToggleSidebar: () => void }) {
  // a module switched on or off adds or takes away its categories (core/module-host.ts settingsTabs)
  useModuleStates();
  const phone = useMedia(SHEET_QUERY);
  // list pages (channels / connectors / extensions / integrations) keep their list beside the open item from this width
  const twoCol = useMedia("(min-width: 900px)");
  // The current assistant's group (docs/assistant/managers.md "The current assistant"): its pages read and write that
  // assistant only. Their merged lists are filtered by it, its configuration editors name it, and what else they ask of
  // its manager (ASSISTANT_PREFIXES) names it while the page is open.
  const picked = useChosenManager();
  const targetManager = useShownManager();
  const integrationsMode = useStore((s) =>
    s.assistant && sameManager(s.assistant.manager, targetManager) ? s.assistant.integrations_v2 === true : "loading",
  );
  const integrationsV2 = integrationsMode === true;
  const [address, setAddress] = useState(readSettingsLocation);
  const integrationAddress = useMemo(() => readIntegrationsAddress(address.search, address.path), [address.search, address.path]);
  const integration = integrationAddress?.route;
  const namedManager = integration && integrationManager(integration.scope);
  const other = namedManager && namedManager !== targetManager ? namedManager : undefined;
  const otherMode = useOtherIntegrationsMode(other);
  const mode = other ? otherMode : integrationsMode;
  const unavailable = integration?.view.kind === "unavailable" || mode === "unavailable";
  const redirect = redirectLegacySettings(address.search, address.hash, address.path, mode === "unavailable" ? "loading" : mode, integrationAddress);
  useLayoutEffect(() => {
    if (!redirect) return;
    history.replaceState(history.state, "", BASE + redirect);
    setAddress(readSettingsLocation());
  }, [redirect]);
  // External in-app navigation to /settings (e.g. "Change in Settings" from the sidebar while Settings is already open).
  useEffect(() => subscribeRoute(() => {
    const path = stripBase(location.pathname);
    if (path === "/settings" || path.startsWith("/settings/")) {
      setAddress(readSettingsLocation());
    }
  }), []);
  const canonical = redirect ? new URL(redirect, location.origin) : address;
  const at = unavailable ? { tab: "integrations" as const, sub: null, section: null } : settingsAt(canonical.search, canonical.hash);
  const listed = phone && !at.tab;
  const tab: Tab = at.tab ?? "general";
  const linkedManager = at.section === "repositories" || at.section === "knowledge-items"
    ? new URLSearchParams(canonical.search).get("manager") ?? undefined : undefined;
  const manager = namedManager ?? linkedManager ?? otherManager(picked ?? undefined);
  const loadingIntegration = mode === "loading" && (tab === "integrations" || Boolean(namedManager));
  const canShowIntegration = !unavailable && !loadingIntegration;
  const parent = integration ? integrationParent(address.state) : null;
  // one scroller shows the list and every page: each keeps its own spot, and Back to the list lands where it was (#5133).
  // An item that opens in a dialog over its category's page (a model purpose) is not a page: the key leaves it out, so
  // opening and closing it leave the page where it was (#4282).
  const scroller = useRef<HTMLDivElement>(null);
  useScrollMemory(scroller, listed ? "settings" : `settings/${tab}/${namedManager ? `${namedManager}/` : ""}${itemIsPage(tab) ? (at.sub ?? "") : ""}`);
  const { sub } = at;
  const view = integration?.view;
  const legacyChannel = view?.kind === "channel" || view?.kind === "builtin"
    ? view.id === "feishu-bot" ? "feishu" : view.id
    : undefined;
  // The host excludes both the Settings navigation and the app sidebar (including a resized sidebar).
  const [wideIntegrations, setWideIntegrations] = useState(false);
  useLayoutEffect(() => {
    const host = scroller.current;
    if (!host || tab !== "integrations" || phone) return;
    // Reserve 440px for the detail beside the 300px list, including its page padding.
    const measure = () => setWideIntegrations(host.clientWidth >= 740);
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(host);
    return () => observer.disconnect();
  }, [tab, phone]);
  const extensionPrefill = tab === "extensions" || tab === "integrations" ? settingsPrefill(canonical.search) : undefined;
  useLayoutEffect(() => (
    ASSISTANT_TABS.has(tab) && canShowIntegration ? pinManagerAt(ASSISTANT_PREFIXES, manager) : undefined
  ), [tab, manager, canShowIntegration]);
  // Another assistant made current with one of the group's items open (a connector, an extension, a channel, a model
  // purpose): that item is the last assistant's, so the page goes back to its list, which then lists the new one's.
  const pickSeen = useRef(picked);
  useLayoutEffect(() => {
    if (pickSeen.current === picked) return;
    pickSeen.current = picked;
    if (!ASSISTANT_TABS.has(tab) || !sub || namedManager) return;
    history.replaceState(categoryState(history.state), "", BASE + settingsHref(tab, null, phone));
    setAddress(readSettingsLocation());
  }, [tab, sub, phone, picked, namedManager]);
  useEffect(() => {
    if (!at.section || listed) return;
    const section = document.getElementById(`settings-${at.section}`);
    if (!section) {
      setScrollAnchor(at.section);
      return;
    }
    section.scrollIntoView?.({ block: "start" });
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(() => section.scrollIntoView?.({ block: "start" }));
    observer.observe(section);
    if (scroller.current) observer.observe(scroller.current);
    const timer = setTimeout(() => observer.disconnect(), 500);
    return () => { clearTimeout(timer); observer.disconnect(); };
  }, [tab, at.section, listed]);
  // "Extensions" only on a hub with the extension base (docs/extensions/extensions.md "The hub's API"); a link to it still opens it.
  const hasExtensions = useStore((s) => hubHasExt(s.features, "manager"));
  const hasChannels = useStore((s) => s.features.includes("channels"));
  // Inbox Sources (#5278) only with a manager that draws them; a link to it still opens it
  const hasSources = useInboxSourcesShown();
  const shown = (k: Tab) => {
    const visibility = { integrationsV2, activeTab: tab, hasChannels, hasExtensions, hasSources };
    if (k === INTEGRATIONS_SETTINGS_REGISTRATION.id) return INTEGRATIONS_SETTINGS_REGISTRATION.isVisible(visibility);
    if (isLegacyIntegrationTab(k)) return INTEGRATIONS_SETTINGS_REGISTRATION.isLegacyTabVisible(k, visibility);
    return true;
  };
  const contentId = useId();
  const configurationOwner = useRef<string | null>(new URLSearchParams(location.search).get("manager"));
  const openIntegration = (
    next: IntegrationsRoute,
    action: "push" | "replace" | "root" = "push",
    legacyHref?: string,
    anchor?: string,
  ) => {
    const owner = integrationManager(next.scope);
    const nextMode = !owner || owner === targetManager ? integrationsMode : owner === namedManager ? mode : "loading";
    const useUnified = typeof nextMode === "boolean" ? nextMode : tab === "integrations" || !legacyHref;
    let to = useUnified || !legacyHref ? integrationHref(next, useUnified) : legacyHref;
    if (anchor) to += `#${anchor}`;
    const state: unknown = history.state;
    if (action !== "root" && to === stripBase(location.pathname) + location.search + location.hash) return;
    if (listed) history.pushState({ [FROM_LIST]: true }, "", BASE + to);
    else if (action === "push" && integration && integration.view.kind !== "unavailable") {
      const child = integrationChildState(state, integration);
      if (takeOverlayEntry()) history.replaceState(child, "", BASE + to);
      else history.pushState(child, "", BASE + to);
    } else {
      history.replaceState(action === "replace" ? state : categoryState(state), "", BASE + to);
    }
    setAddress(readSettingsLocation());
  };
  const pick = (k: Tab, id: string | null = null, section?: SettingsSection, options?: { anchor?: string; manager?: string }) => {
    configurationOwner.current = new URLSearchParams(location.search).get("manager") ?? configurationOwner.current;
    if (isIntegrationTab(k)) {
      if (k === tab && !id && parent?.view.kind === "list" && parent.scope.kind === "current") return popSettingsItem();
      return openIntegration(decodeIntegrationsRoute(k, id), id ? "push" : "root", settingsHref(k, id, phone), options?.anchor);
    }
    const state: unknown = history.state;
    // Out of an item's page opened on its category's page: back to that page's entry, as Back does, so neither a copy of
    // it nor the item is left to go back to. A sheet open over the item has an entry of its own on top: off with both,
    // its marker taken first so its close does not go back once more.
    if (k === tab && sub && !id && openedFromSub(state)) {
      return popSettingsItem();
    }
    // the hub's page keeps its own choice of manager (the download mirrors); the current assistant's pages follow the pick
    let to = settingsHref(k, id, phone, k === "hub" ? configurationOwner.current : options?.manager, section);
    if (options?.anchor) to += `#${options.anchor}`;
    // from the phone's list the category goes on top of it, and an item's page on top of its category's page, so Back
    // returns to each; another item of the category keeps the entry's way back, another category only the way to the list
    if (listed) history.pushState({ [FROM_LIST]: true }, "", BASE + to);
    else if (k === tab && !sub && id && itemIsPage(k)) {
      if (takeOverlayEntry()) history.replaceState(itemState(state), "", BASE + to);
      else history.pushState(itemState(state), "", BASE + to);
    } else {
      const nextState = k === tab && sub && id && openedFromSub(state) ? state : categoryState(state);
      history.replaceState(nextState, "", BASE + to);
    }
    setAddress(readSettingsLocation());
  };
  const openLegacyIntegration = (k: "extensions" | "channels" | "connectors", id: string, fromList = false) => {
    const next = decodeIntegrationsRoute(k, id);
    if (next.scope.kind === "current" && integration) next.scope = integration.scope;
    if (integration && new URLSearchParams(address.search).has("filter")) next.filter = integration.filter;
    // The old pages own their item spellings, including the legacy Feishu channel.
    let to = settingsHref(k, id, phone, integrationManager(next.scope));
    if (next.filter !== "all") to += `&filter=${next.filter}`;
    const sibling = fromList && sub && isIntegrationDetailRoute(next.view);
    return openIntegration(next, sibling ? "replace" : "push", to);
  };
  const openLegacyItem = (k: "extensions" | "channels" | "connectors", id: string | null) => {
    if (!id) return backIntegration();
    openLegacyIntegration(k, id, true);
  };
  const toList = () => {
    configurationOwner.current = new URLSearchParams(location.search).get("manager") ?? configurationOwner.current;
    const steps = stepsToList(history.state);
    if (steps) return steps === 1 ? history.back() : history.go(-steps);
    history.replaceState(null, "", href({ name: "settings" }));
    setAddress(readSettingsLocation());
  };
  // On a phone the top bar's Back steps out of an item's page (a connector, a channel, an extension) to where the page's
  // own Back went, so that page draws none (audit item 64): its category's page, or, from a page inside an extension's
  // or the market's, that page; from a category it goes to the list. The Lark bot's settings are a dialog over the
  // Connectors list, not a page, so its address leaves the top bar going to the list.
  const itemOpen = phone && !!sub && itemIsPage(tab) && !(tab === "connectors" && sub === "feishu-bot");
  const integrationUp = integration && (parent || integration.view.kind !== "list") && !(tab === "sources" && !sub && !parent)
    && !(tab === "connectors" && sub === "feishu-bot" && !parent);
  const up = integrationUp ? { label: integrationBackLabel(tab, parent?.view) }
    : itemOpen ? { label: t("settings.back_to", { name: tabInfo(tab).label }) } : null;
  const backIntegration = () => {
    if (!integration) return;
    if (parent) return popSettingsItem();
    const scope = unavailable ? integrationScope() : integration.scope;
    const filter = integration.filter === "all" && tab === "channels" ? "channels"
      : integration.filter === "all" && tab === "connectors" ? "data" : integration.filter;
    openIntegration({ scope, view: { kind: tab === "sources" ? "data-flow" : "list" }, filter }, "root");
  };
  const goBack = () => {
    if (integrationUp) backIntegration();
    else if (itemOpen) pick(tab);
    else toList();
  };
  // Settings search: filters the sidebar categories, and jumps to a group or a setting's row inside a page (scroll +
  // brief highlight).
  const [query, setQuery] = useState("");
  const search = searchSettings(query, integrationsV2);
  // Inbox Sources' relationship view (#5741) takes the wide page while it is shown; the list keeps its width.
  const [sourcesGraph, setSourcesGraph] = useState(false);
  // Wide extension plugin_page views (code/diff/split/columns/graph/scene/map) expand to the full extension page width.
  const [extensionPageWide, setExtensionPageWide] = useState(false);
  const [scrollAnchor, setScrollAnchor] = useState<string | null>(null);
  useEffect(() => {
    if (!scrollAnchor) return;
    const box = scroller.current;
    const find = () => box?.querySelector<HTMLElement>(`[data-settings-group="${scrollAnchor}"], [data-settings-row="${scrollAnchor}"]`) ?? document.getElementById(scrollAnchor);
    let card: HTMLElement | null = null;
    let prev = "";
    let timer: ReturnType<typeof setTimeout> | undefined;
    let observer: MutationObserver | undefined;
    let resize: ResizeObserver | undefined;
    let settleTimer: ReturnType<typeof setTimeout> | undefined;
    // a group's card is lit, a row itself; a row is brought to the middle, a group to the top
    const show = (el: HTMLElement) => {
      clearTimeout(timer);
      const row = el.hasAttribute("data-settings-row");
      const align = () => el.scrollIntoView?.({ block: row ? "center" : "start" });
      align();
      if (typeof ResizeObserver !== "undefined") {
        resize = new ResizeObserver(align);
        resize.observe(el);
        // The viewport can keep its height while async groups above the target grow.
        if (box?.firstElementChild) resize.observe(box.firstElementChild);
        settleTimer = setTimeout(() => resize?.disconnect(), 500);
      }
      card = row ? el : el.querySelector<HTMLElement>(":scope > div") ?? el;
      prev = card.style.boxShadow;
      card.style.boxShadow = "inset 0 0 0 2px var(--accent)";
      timer = setTimeout(() => {
        if (card) card.style.boxShadow = prev;
        setScrollAnchor(null);
      }, 1200);
    };
    const found = find();
    if (found) show(found);
    else if (box && typeof MutationObserver !== "undefined") {
      // a page that reads what it shows first (Devices, hub) draws the group or row a moment later
      observer = new MutationObserver(() => {
        const el = find();
        if (!el) return;
        observer?.disconnect();
        show(el);
      });
      observer.observe(box, { childList: true, subtree: true });
      timer = setTimeout(() => {
        observer?.disconnect();
        setScrollAnchor(null);
      }, 3000);
    } else setScrollAnchor(null);
    return () => {
      observer?.disconnect();
      clearTimeout(timer);
      resize?.disconnect();
      clearTimeout(settleTimer);
      if (card) card.style.boxShadow = prev;
    };
  }, [scrollAnchor, tab]);
  const goGroup = (g: SearchGroup) => {
    setQuery("");
    const agent = g.tab === "agents" ? AGENT_KINDS.find((id) => g.anchor === `settings-${id}`) : undefined;
    pick(g.tab, g.sub ?? null, agent);
    setScrollAnchor(g.anchor);
  };
  const openSettingsUrl = (url: string, manager?: string) => {
    const target = settingsTarget(url);
    if (!target || !(isCoreTab(target.tab) || isModuleTab(target.tab))) return;
    const owner = manager ?? namedManager;
    if (owner && isIntegrationTab(target.tab)) {
      const next = decodeIntegrationsRoute(target.tab, target.sub);
      if (next.scope.kind === "current") next.scope = integrationScope(owner);
      if (integration) next.filter = integration.filter;
      openIntegration(next, "push", undefined, target.anchor);
    } else {
      pick(target.tab as Tab, target.sub, undefined, { anchor: target.anchor, manager: owner });
    }
    if (target.anchor) setScrollAnchor(target.anchor);
  };
  const onExtensionPage = view?.kind === "page";
  const integrationsSplit =
    !listed &&
    !phone &&
    wideIntegrations &&
    tab === "integrations" &&
    canShowIntegration &&
    Boolean(integration && isIntegrationDetailRoute(integration.view));
  return (
    <div className="flex h-full min-h-0 min-w-0 flex-col overflow-hidden">
      <header className="flex h-14 shrink-0 items-center gap-2 border-b border-line px-3 sm:px-4">
        {phone && at.tab ? (
          <>
            <IconButton icon="chevronLeft" label={up ? up.label : t("settings.back")} onClick={goBack} />
            <h1 className="min-w-0 truncate text-[14px] font-semibold text-ink">{tabInfo(tab).label}</h1>
          </>
        ) : (
          <>
            <IconButton icon="sidebar" label={t("settings.sidebar")} onClick={onToggleSidebar} className="lg:hidden" />
            <h1 className="shrink-0 whitespace-nowrap text-[14px] font-semibold text-ink">{t("settings.title")}</h1>
          </>
        )}
      </header>
      <div className="flex min-h-0 min-w-0 flex-1">
        {!phone && (
          <SettingsNav tab={tab} query={query} onQuery={setQuery} search={search} shown={shown} integrationsV2={integrationsV2} onPick={pick} onGroup={goGroup} contentId={contentId} />
        )}
        <div
          ref={scroller}
          id={contentId}
          role={listed ? undefined : "region"}
          aria-label={listed ? undefined : tabInfo(tab).label}
          tabIndex={0}
          data-integrations-split-host={integrationsSplit ? "" : undefined}
          className={cx(
            "min-h-0 min-w-0 flex-1 focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent",
            integrationsSplit ? "flex h-full overflow-hidden" : "overflow-y-auto overscroll-contain",
          )}
        >
          {listed ? (
            <div className="px-4 py-5" style={{ animation: `fade-up 320ms ${EASE} both` }}>
              <SettingsList shown={shown} integrationsV2={integrationsV2} onPick={pick} />
            </div>
          ) : (
            <div
              key={ASSISTANT_TABS.has(tab) ? `${tab}@${namedManager ?? linkedManager ?? targetManager}` : tab}
              className={
                integrationsSplit
                  ? "flex h-full min-h-0 min-w-0 flex-1 overflow-hidden"
                  : cx(
                      "mx-auto px-5 py-6 sm:px-8",
                      onExtensionPage && extensionPageWide
                        ? "max-w-[1680px]"
                        : tab === "integrations" || tab === "channels" || tab === "connectors" || tab === "extensions" || (tab === "sources" && sourcesGraph)
                          ? "max-w-[1120px]"
                          : "max-w-[760px]",
                    )
              }
              style={{ animation: `fade-up 320ms ${EASE} both` }}
            >
              {tab === "general" && <General onModel={() => pick("models", "translate", "model-config")} />}
              {tab === "advanced" && <Advanced />}
              {tab === "persona" && <AssistantProfile manager={linkedManager} onTriageModel={() => pick("models", "agent.triage", "model-config")} />}
              {tab === "avatar" && (
                <Group title={t("settings.avatar")} footer={t("settings.avatar_footer")} anchor="avatar">
                  <PersonaPicker onExtensions={() => pick("extensions")} />
                </Group>
              )}
              {tab === "notifications" && <NotificationSettings />}
              {tab === "knowledge" && <KnowledgeSelection manager={linkedManager} />}
              {tab === "prompts" && <SavedPrompts />}
              {tab === "sources" && canShowIntegration && (
                <Suspense fallback={<Loading />}>
                  {sub && !phone && (
                    <Button tone="quiet" icon="chevronLeft" onClick={backIntegration} className="-ml-2 mb-3">
                      {integrationBackLabel(tab, parent?.view)}
                    </Button>
                  )}
                  <SourcesUI key={manager} manager={manager} title={TAB_INFO.sources.label}
                    state={integration?.view.kind === "source" ? integration.view.state : undefined} onGraph={setSourcesGraph} />
                </Suspense>
              )}
              {tab === "models" && <>
                <section id="settings-providers" className="scroll-mt-4">
                  <h2 className="mb-2 px-1 text-[15px] font-semibold text-ink">{TAB_INFO.models.label}</h2>
                  <Models />
                </section>
                {/* the four dimensions (docs/assistant/execution-spec.md): the engines, then the channels they run on */}
                <section id="settings-runtimes" className="scroll-mt-4"><RuntimesCard /></section>
                <section id="settings-execution-channels" className="scroll-mt-4"><ExecutionChannelsCard /></section>
                <section id="settings-model-config" className="scroll-mt-4">
                  {/* no section: the row is on the page in view, which stays where it is (#4282) */}
                  <ModelConfiguration open={sub} onOpen={(id) => pick("models", id)} onProviders={() => pick("models", null, "providers")} />
                </section>
              </>}
              {tab === "agents" && <>
                <Group title={t("settings.agent_preferences")} anchor="agents-display"><AgentsDisplayRow /></Group>
                <AgentSections section={at.section} />
              </>}
              {unavailable && (
                <Group>
                  <div className="px-4 py-3">
                    {!phone && <Button tone="quiet" icon="chevronLeft" onClick={backIntegration} className="-ml-2">{integrationBackLabel(tab, parent?.view)}</Button>}
                    <SettingsHint>{t(integration?.view.kind === "unavailable" ? "settings-integrations.link_conflict" : "settings.start_page_unavailable")}</SettingsHint>
                  </div>
                </Group>
              )}
              {loadingIntegration && !unavailable && <Loading />}
              {tab === "integrations" && integration && canShowIntegration && (
                <Suspense fallback={integrationsSplit ? <div className="p-6"><Loading /></div> : <Loading />}>
                  <Integrations
                    route={integration}
                    prefill={extensionPrefill}
                    twoCol={integrationsSplit}
                    headerBack={phone}
                    parentView={parent?.view}
                    onBack={backIntegration}
                    onOpen={openIntegration}
                    onPageWide={setExtensionPageWide}
                    onSettings={openSettingsUrl}
                  />
                </Suspense>
              )}
              {tab === "channels" && canShowIntegration && (legacyChannel ? (twoCol ? (
                <div className="flex gap-6">
                  <div className="w-[300px] shrink-0"><ChannelList manager={namedManager} openBeside={sub} onOpen={(id) => openLegacyItem("channels", id)} onMarketItem={(market, id) => openLegacyIntegration("extensions", routeSub({ kind: "item", market, id })!)} /></div>
                  <div className="min-w-0 flex-1"><ChannelPage key={sub} id={legacyChannel} manager={manager} onBack={backIntegration} onSettings={openSettingsUrl} /></div>
                </div>
              ) : (
                <ChannelPage key={sub} id={legacyChannel} manager={manager} onBack={backIntegration} headerBack={phone} onSettings={openSettingsUrl} />
              )) : (
                <ChannelList manager={namedManager} onOpen={(id) => openLegacyItem("channels", id)} onMarketItem={(market, id) => openLegacyIntegration("extensions", routeSub({ kind: "item", market, id })!)} />
              ))}
              {tab === "connectors" && canShowIntegration && <Suspense fallback={<Loading />}>{sub && sub !== SENT_SUB && sub !== "feishu-bot" && twoCol ? (
                <div className="flex gap-6">
                  <div className="w-[300px] shrink-0"><Connectors manager={namedManager} open={null} openBeside={sub} onOpen={(id) => openLegacyItem("connectors", id)} onChannel={(id, owner) => openLegacyIntegration("channels", ownedSub(id, owner))} /></div>
                  <div className="min-w-0 flex-1"><Connectors manager={namedManager} open={sub} onOpen={(id) => openLegacyItem("connectors", id)} onChannel={(id, owner) => openLegacyIntegration("channels", ownedSub(id, owner))} /></div>
                </div>
              ) : (
                <Connectors manager={namedManager} open={sub} headerBack={phone} onOpen={(id) => openLegacyItem("connectors", id)} onChannel={(id, owner) => openLegacyIntegration("channels", ownedSub(id, owner))} />
              )}</Suspense>}
              {tab === "extensions" && canShowIntegration && <Suspense fallback={<Loading />}>{view?.kind === "extension" && twoCol ? (
                <div className="flex gap-6">
                  <div className="w-[300px] shrink-0"><Extensions manager={namedManager} open={null} openBeside={sub} onOpen={(id) => openLegacyItem("extensions", id)} onBack={backIntegration} onChannel={(id, owner) => openLegacyIntegration("channels", ownedSub(id, owner))} onConnector={(id, owner) => openLegacyIntegration("connectors", ownedSub(id, owner))} prefill={extensionPrefill} /></div>
                  <div className="min-w-0 flex-1"><Extensions manager={namedManager} open={sub} onOpen={(id) => openLegacyItem("extensions", id)} onBack={backIntegration} onChannel={(id, owner) => openLegacyIntegration("channels", ownedSub(id, owner))} onConnector={(id, owner) => openLegacyIntegration("connectors", ownedSub(id, owner))} prefill={extensionPrefill} /></div>
                </div>
              ) : (
                <Extensions manager={namedManager} open={sub} headerBack={phone} onOpen={(id) => openLegacyItem("extensions", id)} onBack={backIntegration} onChannel={(id, owner) => openLegacyIntegration("channels", ownedSub(id, owner))} onConnector={(id, owner) => openLegacyIntegration("connectors", ownedSub(id, owner))} onPageWide={setExtensionPageWide} prefill={extensionPrefill} />
              )}</Suspense>}
              {tab === "shortcuts" && <ShortcutSettings />}
              {tab === "labs" && <Suspense fallback={<Loading />}><LabsSettings /></Suspense>}
              {tab === "devices" && <Devices />}
              {tab === "hub" && (sub === "recovery" ? <RecoverySettings onBack={() => pick("hub")} /> : <>
                <HubSettings onOpenRecovery={() => pick("hub", "recovery")} />
                <AgentGroup />
              </>)}
              {tab === "managers" && <ManagersSettings />}
              {!isCoreTab(tab) && <ModuleSettingsPage tab={tab} />}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

function integrationBackLabel(tab: Tab, parent?: IntegrationView): string {
  if (parent?.kind === "data-flow") return t("settings.back_to", { name: t("subscriptions.view_graph") });
  if (parent?.kind === "catalog") return t("settings.back_to", { name: t("settings-extensionmarket.market_title") });
  if (parent && parent.kind !== "list") return t("settings-extensionview.back");
  return t("settings.back_to", { name: tabInfo(tab).label });
}

const AGENT_LABEL: Record<AgentTab, string> = { claude: "Claude Code", codex: "Codex", agy: "Antigravity", omp: "omp", grok: "Grok" };

function isAgentSection(section: SettingsSection | null): section is AgentTab {
  return section != null && (AGENT_KINDS as readonly string[]).includes(section);
}

/** The original stacked launcher sections. Each heading folds its own settings; all start open. */
function AgentSections({ section }: { section: SettingsSection | null }) {
  const [closed, setClosed] = useState<ReadonlySet<string>>(() => new Set());
  useEffect(() => {
    if (!isAgentSection(section)) return;
    setClosed((cur) => {
      if (!cur.has(section)) return cur;
      const next = new Set(cur);
      next.delete(section);
      return next;
    });
  }, [section]);
  const toggle = (id: AgentTab) => {
    setClosed((cur) => {
      const next = new Set(cur);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };
  return (
    <>
      <HiddenAgentDirs />
      <HerdrSessions />
      {AGENT_KINDS.map((id) => {
        const shown = !closed.has(id);
        return (
          <section key={id} id={`settings-${id}`} className="scroll-mt-4">
            <button
              type="button"
              aria-expanded={shown}
              aria-controls={`settings-${id}-body`}
              aria-label={t(shown ? "settings-agentsettings.hide_agent" : "settings-agentsettings.show_agent", { agent: AGENT_LABEL[id] })}
              onClick={() => toggle(id)}
              className="mb-2 flex w-full items-center gap-1 px-1 py-0.5 text-left"
            >
              <h2 className="text-[15px] font-semibold text-ink">{AGENT_LABEL[id]}</h2>
              <Icon name="chevronRight" size={14} className={cx("shrink-0 text-ink-3 transition-transform", shown && "rotate-90")} />
            </button>
            <div id={`settings-${id}-body`} hidden={!shown}>
              <AgentSettings agent={id} />
            </div>
          </section>
        );
      })}
    </>
  );
}

/** The desktop left sidebar: a sticky "Settings" title and search box, then the grouped categories. */
function SettingsNav({ tab, query, onQuery, search, shown, integrationsV2, onPick, onGroup, contentId }: {
  tab: Tab;
  query: string;
  onQuery: (q: string) => void;
  search: ReturnType<typeof searchSettings>;
  shown: (k: Tab) => boolean;
  integrationsV2: boolean;
  onPick: (k: Tab) => void;
  onGroup: (g: SearchGroup) => void;
  contentId: string;
}) {
  const nav = useRef<HTMLElement>(null);
  const labsUnseen = useHasUnseenLabs();
  // A deep link or a width change can select a category below the sidebar's fold.
  useEffect(() => {
    nav.current?.querySelector('[aria-current="page"]')?.scrollIntoView?.({ block: "nearest" });
  }, [tab, query]);
  const tabs = search.tabs.filter(shown);
  const groups = search.groups.filter((g) => shown(g.tab));
  const none = !!query && !tabs.length && !groups.length;
  return (
    <aside className="flex w-[236px] shrink-0 flex-col border-r border-line bg-inset">
      <div className="shrink-0 border-b border-line px-3 pt-3 pb-2.5">
        <div className="text-[14px] font-semibold text-ink">{t("settings.title")}</div>
        <label className="mt-2 flex h-8 items-center gap-2 rounded-control bg-surface px-2.5 shadow-btn focus-within:shadow-[inset_0_0_0_1px_var(--accent)]">
          <Icon name="search" size={13} stroke={2} className="shrink-0 text-ink-3" />
          <input
            value={query}
            onChange={(e) => onQuery(e.target.value)}
            // Esc clears what was typed; Enter opens the first result (not while an input method is composing, nor the Enter
            // that confirms its text, which Safari sends with isComposing already false)
            onKeyDown={(e) => {
              if (e.key === "Escape" && query) {
                e.preventDefault();
                e.stopPropagation();
                onQuery("");
              } else if (e.key === "Enter" && query && !composing(e.nativeEvent)) {
                e.preventDefault();
                if (tabs.length) onPick(tabs[0]);
                else if (groups.length) onGroup(groups[0]);
              }
            }}
            placeholder={t("settings.search_placeholder_settings")}
            aria-label={t("settings.search_settings")}
            className="min-w-0 flex-1 bg-transparent text-[13px] text-ink outline-none placeholder:text-ink-3"
          />
          {query && <IconButton icon="x" label={t("settings.clear")} size={22} iconSize={12} onClick={() => onQuery("")} />}
        </label>
      </div>
      <nav ref={nav} aria-label={t("settings.categories")} className="min-h-0 flex-1 overflow-y-auto overscroll-contain p-2">
        {query ? (
          <>
            {tabs.map((k) => <NavRow key={k} k={k} selected={tab === k} notice={k === "labs" && labsUnseen} contentId={contentId} onPick={onPick} />)}
            {groups.length > 0 && <div className="px-3 pt-3 pb-1 text-[11px] font-medium text-ink-3">{t("settings.search_in_page")}</div>}
            {groups.map((g) => <GroupResult key={`${g.tab}/${g.anchor}/${g.group}`} g={g} onGroup={onGroup} />)}
            {none && <p className="px-3 py-3 text-[12.5px] leading-relaxed text-ink-3">{t("settings.search_no_results")}</p>}
          </>
        ) : (
          navGroups(integrationsV2).map((g) => {
            const its = g.tabs.filter(shown);
            if (!its.length) return null;
            return (
              <div key={g.key} data-settings-nav-group={g.key}>
                <div className="flex min-h-7 items-center gap-1 pt-2 pr-1 pl-3 text-[11px] font-medium text-ink-3">
                  <span className="min-w-0 flex-1"><GroupTitle group={g.key} compact /></span>
                  <GroupAction group={g.key} />
                </div>
                {its.map((k) => <NavRow key={k} k={k} selected={tab === k} notice={k === "labs" && labsUnseen} contentId={contentId} onPick={onPick} />)}
                {hasFooter(g.key) && <p className="px-3 pt-1 pb-1.5 text-[11px] leading-snug text-ink-3"><GroupFooter group={g.key} /></p>}
              </div>
            );
          })
        )}
      </nav>
    </aside>
  );
}

function NavRow({ k, selected, notice, contentId, onPick }: { k: Tab; selected: boolean; notice: boolean; contentId: string; onPick: (k: Tab) => void }) {
  const info = tabInfo(k);
  return (
    <button
      type="button"
      onClick={() => onPick(k)}
      onPointerEnter={() => preloadTab(k)}
      onFocus={() => preloadTab(k)}
      aria-current={selected ? "page" : undefined}
      aria-controls={contentId}
      aria-label={notice ? `${info.label}, ${t("sidebar.labs_unseen")}` : undefined}
      className={cx(
        "relative flex h-8 w-full items-center gap-2 rounded-[7px] px-2.5 text-left text-[13px] transition-colors",
        selected ? "bg-accent-tint font-medium text-accent-ink" : "text-ink-2 hover:bg-hover hover:text-ink",
      )}
    >
      {selected && <span aria-hidden className="absolute left-0 top-1/2 h-4 w-[2px] -translate-y-1/2 rounded-full bg-accent" />}
      <Icon name={info.icon} size={15} className="shrink-0" />
      <span className="truncate" title={info.label}>{info.label}</span>
      {notice && <NoticeDot />}
    </button>
  );
}

// a result wraps rather than cutting the setting's name ("Agents › Claude Code › Thinking effort" is longer than the sidebar)
function GroupResult({ g, onGroup }: { g: SearchGroup; onGroup: (g: SearchGroup) => void }) {
  return (
    <button type="button" onClick={() => onGroup(g)} className="flex w-full items-center gap-1.5 rounded-[7px] px-3 py-1.5 text-left text-[12.5px] text-ink-2 transition-colors hover:bg-hover hover:text-ink">
      <span className="min-w-0 break-words">
        <span className="font-medium text-ink">{tabInfo(g.tab).label}</span>
        <span className="mx-1 text-ink-3">›</span>
        <span>{g.group}</span>
      </span>
    </button>
  );
}

/** The phone's first page of Settings: the categories in grouped cards, each opening its page (as on iOS). */
function SettingsList({ shown, integrationsV2, onPick }: { shown: (k: Tab) => boolean; integrationsV2: boolean; onPick: (k: Tab) => void }) {
  const labsUnseen = useHasUnseenLabs();
  return (
    <nav aria-label={t("settings.categories")}>
      {navGroups(integrationsV2).map((g) => {
        const tabs = g.tabs.filter(shown);
        if (!tabs.length) return null;
        return (
          <Group key={g.key} anchor={`nav-${g.key}`} title={<GroupTitle group={g.key} />} right={<GroupAction group={g.key} />} footer={hasFooter(g.key) ? <GroupFooter group={g.key} /> : undefined}>
            {tabs.map((k) => (
              <Row key={k} icon={tabInfo(k).icon} label={tabInfo(k).label} hint={tabInfo(k).hint} onClick={() => onPick(k)}>
                {k === "labs" && labsUnseen ? <><NoticeDot /><span className="sr-only">{t("sidebar.labs_unseen")}</span></> : undefined}
              </Row>
            ))}
          </Group>
        );
      })}
    </nav>
  );
}

const host = hostOf;

// ── General ─────────────────────────────────────────────

function General({ onModel }: { onModel: () => void }) {
  const theme = useTheme();
  const replyMotion = useReplyMotionSetting();
  const starred = useStarredSetting();
  const autoOpenNeedsYou = useNeedsYouAutoOpen();
  const features = moduleFeaturesSource();

  const themes: { k: Theme; label: string; icon: IconName }[] = [
    { k: "system", label: t("language.system"), icon: "monitor" },
    { k: "light", label: t("settings.theme_light"), icon: "sun" },
    { k: "dark", label: t("settings.theme_dark"), icon: "moon" },
  ];

  return (
    <>
      <Group title={t("settings.appearance")} anchor="appearance">
        <div className="flex gap-2 p-3" data-settings-row="theme">
          {themes.map((th) => (
            <button
              key={th.k}
              type="button"
              onClick={() => setTheme(th.k)}
              aria-pressed={theme === th.k}
              className={cx(
                "flex flex-1 items-center justify-center gap-2 whitespace-nowrap rounded-control px-3 py-2 text-[12.5px] transition-[background-color,box-shadow,color] duration-150",
                theme === th.k ? "bg-accent-tint font-medium text-accent-ink shadow-[inset_0_0_0_1px_var(--accent)]" : "text-ink-2 shadow-hairline hover:bg-hover",
              )}
            >
              <Icon name={th.icon} size={14} />
              {th.label}
            </button>
          ))}
        </div>
        <TextSizeRow />
        <LanguageRow />
        <Row label={t("settings.reply_motion")} hint={t("settings.reply_motion_hint")} wrapHint anchor="reply-motion">
          <Switch on={replyMotion} onChange={setReplyMotionEnabled} label={t("settings.reply_motion")} />
        </Row>
        <RecentChatsRow />
        <Row label={t("settings.starred_messages")} hint={t("settings.starred_messages_hint")} wrapHint anchor="starred-messages">
          <Switch on={starred} onChange={setStarredEnabled} label={t("settings.starred_messages")} />
        </Row>
        <SidebarUsageRow />
        <Row label={t("settings.needs_you_auto_open")} hint={t("settings.needs_you_auto_open_hint")} wrapHint stackOnNarrow anchor="needs-you-auto-open">
          <Switch on={autoOpenNeedsYou} onChange={setNeedsYouAutoOpen} label={t("settings.needs_you_auto_open")} />
        </Row>
      </Group>

      <DesktopSettings />
      <StartPageGroup />
      <SidebarPagesGroup />
      {features && (
        <div className="mb-6">
          <Suspense fallback={<Loading />}><ModuleFeaturesForm source={features} /></Suspense>
        </div>
      )}

      <section id="settings-translation" className="scroll-mt-4">
        <TranslationSettings onConfigure={onModel} />
      </section>

      <AgentPreferences />
      <HubList />
      <About />
      <Group title={t("settings.about_persona")} anchor="about-persona">
        <Row label={t("settings.source_code")} stackOnNarrow>
          <a href="https://github.com/nwverse/persona" target="_blank" rel="noopener noreferrer" className="touch-hit break-all text-accent-ink underline underline-offset-2">
            github.com/nwverse/persona
          </a>
        </Row>
        <DevModeRows />
      </Group>
    </>
  );
}

/** Settings › Advanced › Appearance (#13398): the non-default looks — theme color, extension theme, interface/code
 *  fonts and colored sidebar icons. General keeps the default appearance (Match system / Light / Dark), text size,
 *  language and behavior rows. These are the same components reading the same browser storage as before, so an
 *  existing choice and the untouched default look are unchanged. */
function Advanced() {
  return (
    <Group title={t("settings.appearance")} anchor="appearance">
      <SurfaceStyleRow />
      <ThemeColorPicker />
      <ExtensionThemePicker />
      <FontRows />
      <SidebarIconsRow />
      <PhoneNavRow />
    </Group>
  );
}

/** The assistant's profile (the current assistant's): About me, memory, skills, scheduled tasks and where it runs, then
 *  session triage. Its avatar is the hub's (the Avatar page), and what was about this browser is under General. */
function AssistantProfile({ onTriageModel, manager }: { onTriageModel: () => void; manager?: string }) {
  return (
    <>
      <AssistantSettings bare manager={manager} />
      <TriageSettings onModel={onTriageModel} />
    </>
  );
}

/** This browser's own switches for Agents (where they open, the terminal, the sidebar, the details shown). */
function AgentPreferences() {
  const workBrowserReady = useStore((s) => s.features.includes("sessions_dimensions"));
  const sidebarTerminals = useSidebarTerminals();
  const agentDetails = useAgentDetails();
  return (
    <Group title={t("settings.agent_preferences")} anchor="agent-preferences">
      <AgentOpenRow />
      <SeenRowsRow />
      <TerminalModeRow />
      {!workBrowserReady && <Row label={t("settings.sidebar_terminals")} hint={t("settings.sidebar_terminals_hint")} wrapHint>
        <Switch on={sidebarTerminals} onChange={setSidebarTerminals} label={t("settings.sidebar_terminals")} />
      </Row>}
      <Row label={t("context.details")} hint={t("context.details_hint")} wrapHint anchor="agent-details">
        <Switch on={agentDetails} onChange={setAgentDetails} label={t("context.details")} />
      </Row>
    </Group>
  );
}

/** Settings › General's line under "Runs on": the computer by name; none yet ("" from the hub) says so; nothing before it is read. */
export function assistantFooter(machine: string | undefined, loaded: boolean): string | undefined {
  if (machine) return t("settings.assistant_footer", { machine });
  return loaded ? t("settings.assistant_footer_none") : undefined;
}

function About() {
  const machines = useStore((s) => s.machines);
  const hubName = useStore((s) => s.hubName);
  // interconnected hubs (docs/archive/operations/plan-hub-mesh.md 2.6): this page talks to one hub and sees its whole group
  const mesh = useStore((s) => s.mesh);
  const others = mesh.filter((h) => !h.self);
  const disconnect = useDisconnect();
  return (
    <Group title={t("settings.this_browser")} footer={t("settings.this_browser_footer")} anchor="this-browser">
      <Row label={t("settings.hub_address")} mono anchor="hub-address">
        {location.host}
      </Row>
      {hubName && <Row label={t("settings.hub")}>{hubName}</Row>}
      {others.length > 0 && (
        <Row label={t("settings.linked_hubs")} hint={t("settings.linked_hubs_hint")} wrapHint>
          {others.map((h) => (
            <span key={h.hub_id} className="flex items-center gap-1.5">
              <span className={cx("size-1.5 rounded-full", h.online === false ? "bg-ink-3" : "bg-green")} />
              {h.name || h.hub_id}
            </span>
          ))}
        </Row>
      )}
      <Row label={t("settings.computers")} anchor="computers">{t("settings.computers_online", { online: machines.filter((m) => m.online).length, total: machines.length })}</Row>
      <InstallRow />
      <div className="flex justify-end px-4 py-2.5" data-settings-row="disconnect">
        <Button tone="quiet" icon="logout" onClick={disconnect.start} busy={disconnect.busy} className="text-red enabled:hover:text-red">
          {t("settings.disconnect_browser")}
        </Button>
      </div>
      {disconnect.dialog}
    </Group>
  );
}

// Installing the web client as an app (lib/pwa.ts): the browser's prompt where it has one, else where to find it.
const INSTALL_HINT: Record<InstallState, string> = {
  installed: t("settings.install_installed"),
  prompt: t("settings.install_prompt"),
  ios: t("settings.install_ios"),
  safari: t("settings.install_safari"),
  insecure: t("settings.install_insecure"),
  menu: t("settings.install_menu"),
};

function InstallRow() {
  const state = useInstall();
  return (
    <Row label={t("settings.install_app")} hint={INSTALL_HINT[state]} wrapHint anchor="install-app">
      {state === "installed" && <Pill tone="green">{t("settings.installed")}</Pill>}
      {state === "prompt" && (
        <Button icon="download" onClick={() => void install()}>
          {t("settings.install")}
        </Button>
      )}
    </Row>
  );
}

// ── Models ──────────────────────────────────────────────

// The provider wizard and the import open on a click, so their chunks (with ProviderPaste and ProviderScope, which
// only they use) load on the first open, not with every Settings page: about 110 KB of the Settings chunk (#8840).
// The wizard stays mounted once opened, as before: it keeps what each opening started (ProviderWizard.tsx).
const ProviderWizard = lazyOverlay(() => import("./settings/ProviderWizard").then((m) => m.ProviderWizard));
const ProviderImport = lazyDialog(() => import("./settings/ProviderImport").then((m) => m.ProviderImport));

/** Forgets `add` / `launcher` once what they opened is closed, so going back or reloading does not open it again. */
function dropLinkParams() {
  const q = new URLSearchParams(location.search);
  if (!q.has("add") && !q.has("launcher")) return;
  q.delete("add");
  q.delete("launcher");
  history.replaceState(history.state, "", `${location.pathname}?${q.toString()}`);
}

function Models() {
  const machines = useStore((s) => s.machines);
  const [list, setList] = useState<LauncherView[] | null>(null);
  const [sync, setSync] = useState<LauncherSync[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [query, setQuery] = useState("");
  // an import link this page was opened with (#5420, lib/import-link.ts): Add opens on it. Read without taking it
  // (StrictMode runs this twice), then forgotten there once mounted; closing the wizard forgets it here too.
  const [link, setLink] = useState(importLinkHeld);
  // `?add=1` opens the wizard, `?launcher=<name>` one provider: where "Connect a model" / "Change the key" lead (lib/readiness.ts)
  const [open, setOpen] = useState<string | null>(() => (link ? null : new URLSearchParams(location.search).get("launcher")));
  const [editing, setEditing] = useState<LauncherView | "new" | null>(() => (link || new URLSearchParams(location.search).get("add") === "1" ? "new" : null));
  // the provider whose import link is being copied (#5420)
  const [copying, setCopying] = useState<LauncherView | null>(null);
  const [importing, setImporting] = useState(false);
  // the import opened for one alias the wizard found (#5193); null: the computer's whole list
  const [importAlias, setImportAlias] = useState<FoundAlias | null>(null);
  // what a stored provider opens with when a paste goes on with it
  const [editDraft, setEditDraft] = useState<EditDraft | null>(null);
  // One opened while the page is up (pasted over it) opens Add in place of a provider's details, but never over a
  // dialog with something typed in it (the wizard, the copy dialog, the alias import): it waits, held, until that closes.
  const busy = editing !== null || copying !== null || importing;
  const wizardOpened = useOpened(editing !== null);
  const busyNow = useRef(busy);
  busyNow.current = busy;
  const takeLink = (l: string) => {
    dropImportLink();
    setOpen(null);
    setImportAlias(null);
    setEditDraft(null);
    setLink(l);
    setEditing("new");
  };
  useEffect(() => {
    dropImportLink();
    return onImportLink((l) => (busyNow.current ? toast(t("settings-providerlink.link_waits"), "info") : takeLink(l)));
  }, []);
  useEffect(() => {
    const l = importLinkHeld();
    if (!busy && l) takeLink(l);
  }, [busy]);

  // the list read again; rejects when it couldn't be
  const refresh = () => loadLaunchers<LauncherView>().then((r) => (setList(r.launchers), setSync(r.sync ?? []), r.launchers));
  const load = () => {
    setLoading(true);
    setError("");
    return refresh()
      .catch((e) => setError(errorText(e)))
      .finally(() => setLoading(false));
  };
  useEffect(() => void load(), []);
  const found = useMemo(() => foundAliases(list ?? [], machines), [list, machines]);
  const closeWizard = () => (setEditing(null), setEditDraft(null), setLink(null), dropLinkParams());
  // The wizard swaps to a stored provider the paste matched; one added since this list loaded is in a fresh one.
  const editWith = async (name: string, draft: EditDraft) => {
    const find = (ls?: LauncherView[] | null) => ls?.find((l) => managedProvider(l.source) && l.name === name);
    let l = find(list);
    if (!l) {
      try {
        l = find(await refresh());
      } catch (e) {
        return toast(t("settings.launchers_failed", { error: errorText(e) }), "error");
      }
    }
    if (!l) return toast(t("settings-providerpaste.provider_missing", { name }), "error");
    setLink(null);
    setEditDraft(draft);
    setEditing(l);
  };

  const name = (id: string) => sync.find((s) => s.machine === id)?.name || machines.find((m) => m.id === id)?.name || id;
  const shown = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!list || !q) return list ?? [];
    return list.filter((l) => [l.name, l.display_name, providerTitle(l), isTraeCli(l.name) ? "traecli" : undefined, l.model, l.small_model, l.base_url,
      ...(l.machines ?? []).map(name)].some((v) => v?.toLowerCase().includes(q)));
  }, [list, query, machines, sync]);
  const managed = shown.filter((l) => managedProvider(l.source));
  const builtin = builtinRows(shown);
  const rc = rcGroups(shown);
  // a wrapper script on PATH or a runtime's launcher (agy, omp): a computer's, but not from an rc file
  const onPath = shown.filter((l) => !managedProvider(l.source) && l.source !== "builtin" && l.source !== "zshrc" && !l.hidden);
  const hidden = shown.filter((l) => !managedProvider(l.source) && l.hidden);
  // the machines that did not answer this time, named on the entries they gave before
  const staleMark = (ids?: string[]) =>
    ids?.length ? <Pill tone="orange">{t("settings.stale", { machines: formatList(ids.map(name), { type: "conjunction", style: "narrow" }) })}</Pill> : null;

  const row = (l: LauncherView) => (
    <button key={l.name} type="button" onClick={() => setOpen(l.name)} className="flex w-full items-center gap-3 px-4 py-2.5 text-left transition-colors hover:bg-hover">
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className={cx("truncate text-[13px] font-medium text-ink", !l.display_name && providerTitle(l) === l.name && "font-mono")}>{providerTitle(l)}</span>
          {managedProvider(l.source) && <Pill tone="accent">Persona</Pill>}
          {l.channel && <ChannelAliasPill channel={l.channel} />}
          {managedProvider(l.source) && l.pi && <span title={t("settings.pi_too_title")}><Pill>{t("settings.pi_too")}</Pill></span>}
          {l.source === "agy" && <Pill>Antigravity Runtime</Pill>}
          {l.kind === "api" && <Pill>{t("settings.direct_api")}</Pill>}
          {staleMark(l.stale)}
        </div>
        <div className="mt-0.5 flex min-w-0 items-center gap-2 text-[11.5px] text-ink-3">
          {l.display_name && l.name !== "traex" && <span className="shrink-0 font-mono">{l.name}</span>}
          {l.model && <span className="truncate font-mono text-ink-2">{l.model}</span>}
          {host(l.base_url) && <span className="truncate">{host(l.base_url)}</span>}
        </div>
      </div>
      {l.machines && l.machines.length > 0 && <span className="hidden max-w-[200px] shrink-0 truncate text-[11.5px] text-ink-3 sm:block">{formatList(l.machines.map(name), { type: "conjunction", style: "narrow" })}</span>}
      <Icon name="chevronRight" size={12} stroke={2} className="shrink-0 text-ink-3" />
    </button>
  );

  return (
    <>
      <div className="mb-8">
        <h2 className="mb-1 px-1 text-[15px] font-semibold text-ink">{t("settings-nav.tab_providers")}</h2>
        <div className="mb-4 flex items-center gap-2">
        <label className="flex h-9 min-w-0 flex-1 items-center gap-2 rounded-control bg-surface px-3 shadow-btn focus-within:shadow-[inset_0_0_0_1px_var(--accent)]">
          <Icon name="search" size={13} stroke={2} className="text-ink-3" />
          <input value={query} onChange={(e) => setQuery(e.target.value)} placeholder={t("settings.search_placeholder")} className="min-w-0 flex-1 bg-transparent text-[13px] text-ink outline-none placeholder:text-ink-3" aria-label={t("settings.search_providers")} />
          {query && <IconButton icon="x" label={t("settings.clear")} size={22} iconSize={12} onClick={() => setQuery("")} />}
        </label>
        {list && (
          // for people who already run claude from aliases on a computer: turns them into providers (the dialog explains when there is no computer)
          <Button tone="secondary" icon="download" title={t("settings-providerimport.title")} onClick={() => (setImportAlias(null), setImporting(true))}>
            <span className="max-sm:sr-only">{t("settings-providerimport.import")}</span>
          </Button>
        )}
        <Button tone="accent" icon="plus" onClick={() => setEditing("new")}>
          <span className="max-sm:hidden">{t("settings.add_provider")}</span>
          <span className="sm:hidden">{t("settings.add_provider_short")}</span>
        </Button>
      </div>
      {!list && !error && <Loading />}
      {error && <div className="mb-4 flex items-center gap-3">
        <p role="alert" className="min-w-0 flex-1 text-[13px] break-words text-red">{t("settings.launchers_failed", { error })}</p>
        <Button icon="retry" onClick={() => void load()} busy={loading}>{t("settings.retry")}</Button>
      </div>}
      {list && query && shown.length === 0 && <p className="py-10 text-center text-[13px] text-ink-3">{t("settings.no_matching_providers", { query })}</p>}
      {list && (!query || managed.length > 0) && (
        <Group title={t("settings.managed_title", { count: managed.length })} footer={managed.length ? t("settings.managed_footer") : undefined}>
          {managed.length ? <ManagedRows list={managed} row={row} /> : <p className="px-4 py-3 text-[12.5px] text-ink-3">{t("settings.managed_empty")}</p>}
        </Group>
      )}
      {builtin.length > 0 && (
        <Group title={t("settings.builtin_title", { count: builtin.length })} footer={t("settings.builtin_footer")}>
          {builtin.map((b) => (
            // its page tests it and hides it in Persona (#4384), as an rc-file launcher's does
            <button key={`${b.name}/${b.machine}`} type="button" onClick={() => setOpen(b.name)} className="flex w-full items-center gap-3 px-4 py-2.5 text-left transition-colors hover:bg-hover">
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span className="truncate text-[13px] font-medium text-ink">{t("settings.builtin_row", { name: b.name, machine: name(b.machine) })}</span>
                  {b.stale && <Pill tone="orange">{t("settings.stale_mark")}</Pill>}
                </div>
                <div className="mt-0.5 truncate font-mono text-[11.5px] text-ink-3">{b.version ? `Claude Code ${b.version}` : "Claude Code"}</div>
              </div>
              <Icon name="chevronRight" size={12} stroke={2} className="shrink-0 text-ink-3" />
            </button>
          ))}
        </Group>
      )}
      {rc.map((g, i) => (
        <Group key={g.files} title={t("settings.zshrc_title", { file: g.files, count: g.launchers.length })} footer={i === rc.length - 1 ? t("settings.zshrc_footer") : undefined}>
          {g.launchers.map(row)}
        </Group>
      ))}
      {onPath.length > 0 && (
        <Group title={t("settings.path_title", { count: onPath.length })} footer={t("settings.path_footer")}>
          {onPath.map(row)}
        </Group>
      )}
      {hidden.length > 0 && (
        <Group title={t("settings.hidden_title", { count: hidden.length })} footer={t("settings.hidden_footer")}>
          {hidden.map(row)}
        </Group>
      )}
      {list && !query && sync.length > 0 && (
        <Group title={t("settings.sync_title", { count: sync.length })} footer={t("settings.sync_footer")}>
          {sync.map((s) => {
            const st = syncStateText(s);
            return (
              <div key={s.machine} className="flex items-center gap-3 px-4 py-2.5">
                <div className="min-w-0 flex-1">
                  <div className="truncate text-[13px] text-ink">{name(s.machine)}</div>
                  {st.detail && <div className={cx("mt-0.5 truncate text-[11.5px]", s.state === "error" ? "text-red" : "text-ink-3", s.state === "ok" && "font-mono")} title={st.detail}>{st.detail}</div>}
                </div>
                <Pill tone={st.tone}>{st.label}</Pill>
              </div>
            );
          })}
        </Group>
      )}
      <LauncherDetailDialog
        name={open}
        onClose={() => (setOpen(null), dropLinkParams())}
        onChanged={() => void load()}
        onEdit={(l) => {
          setOpen(null);
          setEditing(l);
        }}
        onCopyLink={(l) => {
          setOpen(null);
          setCopying(l);
        }}
      />
      {wizardOpened && <ProviderWizard open={editing !== null} existing={editing} draft={editDraft} link={link} found={found} onClose={closeWizard} onSaved={() => void load()}
        onImportAlias={(a) => (closeWizard(), setImportAlias(a), setImporting(true))} onEditWith={(name, draft) => void editWith(name, draft)} />}
      <ProviderImport open={importing} machine={importAlias?.machine} alias={importAlias?.name} onClose={() => setImporting(false)} onImported={() => void load()} />
      <ProviderLinkCopy provider={copying} onClose={() => (setCopying(null), dropLinkParams())} />
      </div>
    </>
  );
}

export function LauncherDetailDialog({ name, onClose, onChanged, onEdit, onCopyLink }: {
  name: string | null;
  onClose: () => void;
  onChanged: () => void;
  onEdit: (l: LauncherView) => void;
  /** "Copy import link" in its ⋯ (#5420): this dialog closes and the copy dialog opens for the provider. */
  onCopyLink?: (l: LauncherView) => void;
}) {
  const machines = useStore((s) => s.machines);
  const more = useMenu();
  const [l, setL] = useState<LauncherView | null>(null);
  const [error, setError] = useState("");
  const [checks, setChecks] = useState<Record<string, LauncherCheck | null>>({});
  const [confirm, setConfirm] = useState(false);
  // what still names an rc-file launcher, asked when its Hide is confirmed: they keep running on it (#4661)
  const [uses, setUses] = useState<string[] | null>(null);
  const [showDef, setShowDef] = useState(false);
  const mname = (id: string) => machines.find((m) => m.id === id)?.name ?? id;
  // extensions draw a section here (docs/extensions/extensions.md "Declarative UI" launcher_section)
  const hasExtensions = useStore((s) => hubHasExt(s.features, "manager"));

  // The dialog stays mounted from one launcher to the next: an answer or a test result for a name it no longer shows
  // is dropped, or the body (and Delete) would be another launcher's under this one's title (#5374).
  const shown = useRef(name);
  shown.current = name;
  useEffect(() => {
    setL(null);
    setError("");
    setChecks({});
    setConfirm(false);
    setUses(null);
    setShowDef(false);
    more.close();
    if (!name) return;
    let live = true;
    api<{ launcher: LauncherView }>(enLauncher(name))
      .then((r) => live && setL(r.launcher))
      .catch((e) => live && setError(errorText(e)));
    return () => {
      live = false;
    };
  }, [name]);

  // machines whose launcher sets the same, once
  const groups = useMemo(() => {
    const out: { ids: string[]; d: LauncherDetail }[] = [];
    for (const [id, d] of Object.entries(l?.details ?? {}).sort()) {
      const g = out.find((x) => JSON.stringify(x.d) === JSON.stringify(d));
      if (g) g.ids.push(id);
      else out.push({ ids: [id], d });
    }
    return out;
  }, [l]);

  const testOn = managedProvider(l?.source) ? [""] : (l?.machines ?? []).filter((id) => machines.find((m) => m.id === id)?.online !== false);
  const running = Object.values(checks).some((c) => c === null);
  const check = async () => {
    if (!l) return;
    const asked = name;
    setChecks(Object.fromEntries(testOn.map((id) => [id, null])));
    await Promise.all(
      testOn.map(async (id) => {
        let c: LauncherCheck;
        try {
          c = (await api<{ check: LauncherCheck }>(`${enLauncher(l.name)}/check${id ? `?machine=${enc(id)}` : ""}`, { method: "POST" })).check;
        } catch (e) {
          c = { ok: false, millis: 0, error: errorText(e) };
        }
        if (shown.current === asked) setChecks((x) => ({ ...x, [id]: c }));
      }),
    );
  };

  const askRemove = () => {
    setConfirm((c) => !c);
    if (!l || !name || managedProvider(l.source) || uses !== null) return;
    const asked = name;
    api<{ uses?: string[] }>(`${enLauncher(name)}/uses`)
      .then((r) => shown.current === asked && setUses(r.uses ?? []))
      .catch(() => {}); // an older hub: the plain confirmation, as before
  };

  const remove = async () => {
    if (!l) return;
    try {
      await api(enLauncher(l.name), { method: "DELETE" });
      forgetProviderNames();
      toast(managedProvider(l.source) ? t("settings.deleted", { name: providerTitle(l) }) : t("settings.hid", { name: providerTitle(l) }), "ok");
      onChanged();
      onClose();
    } catch (e) {
      launcherWriteFailed(e, l.channel);
    }
  };
  const restore = async () => {
    if (!l) return;
    try {
      await api(`${enLauncher(l.name)}/restore`, { method: "POST" });
      forgetProviderNames();
      toast(t("settings.restored", { name: providerTitle(l) }), "ok");
      onChanged();
      onClose();
    } catch (e) {
      toast(errorText(e), "error");
    }
  };

  return (
    <Dialog
      open={name !== null}
      onClose={onClose}
      width={620}
      // a provider's display name with its startup ID after it (#5029); an rc alias, or one not read yet, by its ID
      title={l && providerIdNote(l) ? <>{providerTitle(l)} <span className="font-mono text-[11.5px] font-normal text-ink-3">{providerIdNote(l)}</span></> : <span className="font-mono">{name ? providerTitle(l ?? { name }) : name}</span>}
      footer={
        l && (
          <>
            {l.channel ? null : l.hidden ? (
              <Button tone="secondary" onClick={() => void restore()}>
                {t("settings.restore")}
              </Button>
            ) : (
              <span className="relative">
                <Button tone="quiet" icon={managedProvider(l.source) ? "x" : undefined} onClick={askRemove} className="text-red enabled:hover:text-red">
                  {managedProvider(l.source) ? t("settings.delete_provider") : t("settings.hide_in_persona")}
                </Button>
                {/* the confirmation comes up right at the button (like the app), not as another dialog */}
                {confirm && (
                  <span className="absolute bottom-full left-0 mb-1.5 flex flex-col items-start gap-1 rounded-control bg-surface p-1 shadow-overlay" style={{ animation: `pop-in 160ms ${EASE} both` }}>
                    {!managedProvider(l.source) && !!uses?.length && <span className="w-72 px-1 text-[12px] text-ink-2">{t("settings.hide_uses", { uses: uses.join(", ") })}</span>}
                    <Button tone="danger" onClick={() => void remove()}>
                      {managedProvider(l.source) ? t("settings.confirm_delete") : t("settings.confirm_hide")}
                    </Button>
                  </span>
                )}
              </span>
            )}
            <span className="flex-1" />
            {onCopyLink && linkable(l) && (
              <>
                <IconButton icon="more" label={t("settings-providerlink.more")} size={32} onClick={more.open} active={!!more.anchor} />
                {more.anchor && (
                  <Menu anchor={more.anchor} onClose={more.close} side="top" align="right" width={220}
                    items={[{ key: "copy-link", label: t("settings-providerlink.copy_link"), icon: "link", onSelect: () => onCopyLink(l) }]} />
                )}
              </>
            )}
            {managedProvider(l.source) && !l.channel && (
              <Button tone="secondary" onClick={() => onEdit(l)}>
                {t("settings.edit")}
              </Button>
            )}
            <Button tone="primary" icon={running ? undefined : "run"} busy={running} disabled={testOn.length === 0} onClick={() => void check()}>
              {Object.keys(checks).length ? t("settings.test_again") : t("settings.test")}
            </Button>
          </>
        )
      }
    >
      {!l && !error && <Loading />}
      {error && <p className="text-[13px] text-red">{error}</p>}
      {l && (
        <div className="flex max-h-[62vh] flex-col gap-4 overflow-y-auto pr-1">
          {l.channel && <ChannelAliasNote channel={l.channel} onLeave={onClose} />}
          <div className="flex flex-wrap items-center gap-1.5">
            <Pill tone={managedProvider(l.source) ? "accent" : undefined}>{managedProvider(l.source) ? t("settings.managed") : l.source === "agy" ? "Antigravity Runtime (agy acp)" : l.source === "path" ? t("settings.path_script") : t("settings.zshrc_function", { file: rcFilesOf(l).join(" / ") })}</Pill>
            {(l.machines ?? []).map((id) => (
              <Pill key={id} tone={l.stale?.includes(id) ? "orange" : undefined}>{l.stale?.includes(id) ? `${mname(id)} · ${t("settings.stale_mark")}` : mname(id)}</Pill>
            ))}
            {l.hidden && <Pill>{t("settings.hidden")}</Pill>}
          </div>

          {/* how much is left comes before trying it, as in the app */}
          {l.kind !== "api" && (managedProvider(l.source) || l.source === "zshrc" || l.source === "path") && <LauncherQuota name={l.name} />}

          {Object.keys(checks).length > 0 && (
            <KV title={t("settings.availability")}>
              {Object.entries(checks).map(([id, c]) => (
                <div key={id} className="flex items-start gap-2.5 py-1.5">
                  <span className="mt-0.5 flex size-4 shrink-0 items-center justify-center">{c === null ? <Spinner /> : <Icon name={c.ok ? "check" : "x"} size={13} stroke={2.6} className={c.ok ? "text-green" : "text-red"} />}</span>
                  <div className="min-w-0 flex-1">
                    <div className="flex items-baseline gap-2 text-[12.5px]">
                      <span className="font-medium text-ink">{id ? mname(id) : t("settings.hub_machine_title")}</span>
                      {c && <span className="text-ink-3 tabular-nums">{t("settings.elapsed_seconds", { seconds: (c.millis / 1000).toFixed(c.millis >= 10_000 ? 0 : 1) })}</span>}
                    </div>
                    {c === null ? (
                      <div className="text-[11.5px] text-ink-3">{t("settings.requesting")}</div>
                    ) : c.ok ? (
                      <div className="truncate text-[11.5px] text-ink-3">{[c.model, c.answer && t("settings.answered", { answer: c.answer })].filter(Boolean).join(" · ")}</div>
                    ) : (
                      <pre className="mt-0.5 whitespace-pre-wrap break-all font-mono text-[11px] text-red">{c.error}</pre>
                    )}
                  </div>
                </div>
              ))}
            </KV>
          )}

          {managedProvider(l.source) && (
            <KV title={t("settings.config")}>
              <KVRow k={t("settings.access")} v={l.kind === "api" ? t("settings.direct_api") : l.agent === "codex" ? "Codex" : l.agent === "agy" ? "Antigravity (agy)" : t("settings.launcher")} />
              {l.kind === "api" && <KVRow k={t("settings.protocol")} v={l.api === "chat" ? t("settings-providerwizard.protocol_chat") : l.api === "responses" ? "Responses" : "Messages"} />}
              {l.base_url && <KVRow k="Base URL" v={l.base_url} mono />}
              <KVRow k={t("settings.model")} v={l.model} mono />
              {l.kind !== "api" && (!l.agent || l.agent === "claude") && l.small_model && <KVRow k={t("settings.small_model")} v={l.small_model} mono />}
              {l.kind !== "api" && (!l.agent || l.agent === "claude") && l.subagent_model && <KVRow k={t("settings.subagent_model")} v={l.subagent_model} mono />}
              {l.kind !== "api" && (!l.agent || l.agent === "claude") && l.custom_model && <KVRow k={t("settings.custom_model")} v={l.custom_model} mono />}
              {l.kind !== "api" && (!l.agent || l.agent === "claude") && l.custom_model_name && <KVRow k={t("settings.custom_model_name")} v={l.custom_model_name} />}
              <KVRow k="API Key" v={l.key_reference ? referenceState(l.key_reference) : l.has_key ? t("settings.key_set_hidden") : t("settings.not_set")} />
              {l.share && <KVRow k={t("settings.devices")} v={scopeSummary(l, mname)} />}
              {l.share && l.key_machines?.length ? <KVRow k={t("settings.machine_keys")} v={formatList(l.key_machines.map(mname), { type: "conjunction", style: "narrow" })} /> : null}
              {l.api !== "responses" && l.api !== "chat" && (!l.agent || l.agent === "claude") && <KVRow k={t("settings.auth")} v={l.auth === "bearer" ? "Bearer" : "x-api-key"} />}
              {l.kind !== "api" && l.args?.length ? <KVRow k={t("settings.extra_args")} v={l.args.join(" ")} mono /> : null}
              {l.models?.length ? <KVRow k={t("settings.model_list")} v={formatList(l.models.map((m) => m.id), { type: "conjunction", style: "narrow" })} mono sub={t("settings.model_count", { count: l.models.length })} /> : null}
              {([["opus_model", "Opus"], ["sonnet_model", "Sonnet"], ["haiku_model", "Haiku"], ["fable_model", "Fable"]] as const).map(([k, tier]) => l[k] && <KVRow key={k} k={t("settings.tier_model", { tier })} v={l[k]} mono />)}
              {l.subagent_force && <KVRow k={t("settings.subagent")} v={t("settings.subagent_force")} />}
              {l.has_headers && <KVRow k={t("settings.headers")} v={t("settings.key_set_hidden")} />}
              {l.has_env && <KVRow k={t("settings.env")} v={t("settings.key_set_hidden")} />}
              {l.proxy && <KVRow k={t("settings.proxy")} v={l.proxy} mono />}
              {l.no_proxy && <KVRow k="NO_PROXY" v={l.no_proxy} mono />}
              {l.timeout_ms ? <KVRow k={t("settings.timeout")} v={`${l.timeout_ms} ms`} mono /> : null}
              {l.compact_window ? <KVRow k={t("settings.compact_window")} v={tokens(l.compact_window)} /> : null}
              {l.max_context ? <KVRow k={t("settings.max_context")} v={tokens(l.max_context)} /> : null}
              {l.effort && <KVRow k={t("settings.default_effort")} v={l.effort} />}
              {l.origin?.startsWith("zshrc:") && <KVRow k={t("settings.source")} v={l.origin.slice("zshrc:".length)} mono sub={t("settings.from_zshrc")} />}
            </KV>
          )}

          {hasExtensions && <LauncherExtensionSections launcher={l.name} machine={managedProvider(l.source) ? undefined : testOn[0] || undefined} />}

          {groups.map((g) => (
            <DetailBlock key={g.ids.join()} d={g.d} place={groups.length > 1 ? formatList(g.ids.map(mname), { type: "conjunction", style: "narrow" }) : undefined} />
          ))}

          {l.definitions && Object.keys(l.definitions).length > 0 && (
            <div>
              <button type="button" onClick={() => setShowDef((x) => !x)} className="flex items-center gap-1.5 text-[12px] font-medium text-ink-2 hover:text-ink">
                <Icon name="chevron" size={10} stroke={2.4} style={{ transform: showDef ? "none" : "rotate(-90deg)", transition: "transform 150ms" }} />
                {l.source === "path" ? t("settings.script_source") : t("settings.function_source")}
                <span className="font-normal text-ink-3">{t("settings.function_source_note")}</span>
              </button>
              {showDef &&
                Object.entries(l.definitions).map(([id, def]) => (
                  <div key={id} className="mt-2">
                    {Object.keys(l.definitions!).length > 1 && <div className="mb-1 text-[11px] text-ink-3">{mname(id)}</div>}
                    <pre className="overflow-x-auto rounded-control bg-inset p-3 font-mono text-[11px] leading-relaxed text-ink-2 shadow-hairline">{def}</pre>
                  </div>
                ))}
            </div>
          )}
        </div>
      )}
    </Dialog>
  );
}

/** Persona's providers by harness (Claude Code, Codex, …, direct API), each under a small header; one harness alone
 *  needs none, so the list stays as it was. */
function ManagedRows({ list, row }: { list: LauncherView[]; row: (l: LauncherView) => React.ReactNode }) {
  const groups = harnessGroups(list);
  if (groups.length < 2) return <>{list.map(row)}</>;
  return (
    <>
      {groups.map((g) => (
        <div key={g.harness} role="group" aria-label={harnessLabel(g.harness)} className="divide-y divide-line">
          <div className="bg-inset px-4 py-1.5 text-[11px] font-medium text-ink-3">{harnessLabel(g.harness)}</div>
          {g.items.map(row)}
        </div>
      ))}
    </>
  );
}

function KV({ title, children }: { title: React.ReactNode; children: React.ReactNode }) {
  return (
    <div>
      <div className="mb-1 text-[11.5px] font-medium text-ink-3">{title}</div>
      <div className="divide-y divide-line rounded-control bg-inset px-3 shadow-hairline">{children}</div>
    </div>
  );
}

function KVRow({ k, v, mono, sub }: { k: React.ReactNode; v?: React.ReactNode; mono?: boolean; sub?: string }) {
  return (
    <div className="flex items-baseline gap-3 py-1.5">
      <div className={cx("shrink-0", sub ? "w-[220px]" : "w-[104px]")}>
        <div className="text-[12px] text-ink-2">{k}</div>
        {sub && <div className="font-mono text-[10px] break-all text-ink-3">{sub}</div>}
      </div>
      <div className={cx("min-w-0 flex-1 break-all text-[12.5px] text-ink", mono && "font-mono")}>{v || "—"}</div>
    </div>
  );
}

function DetailBlock({ d, place }: { d: LauncherDetail; place?: string }) {
  const titled = (s: string) => (place ? `${s} · ${place}` : s);
  if (d.error) return <KV title={titled(t("settings.run"))}><p className="py-2 text-[12px] text-ink-3">{d.error}</p></KV>;
  const agent = d.agent === "codex" || isTraeCli(d.agent) ? agentName(d.agent) : d.agent === "agy" ? "Antigravity" : "Claude Code";
  return (
    <>
      <KV title={titled(t("settings.run"))}>
        <KVRow k="Agent" v={agent} />
        {d.base_url && <KVRow k={t("settings.gateway")} v={d.base_url} mono />}
        {d.auth && <KVRow k="Key" v={t("settings.key_set", { auth: d.auth })} mono />}
      </KV>
      {d.models?.length ? (
        <KV title={titled(t("settings.model"))}>
          {d.models.map((m) => (
            <KVRow key={m.role + m.env} k={m.role} sub={m.env} v={m.model} mono />
          ))}
        </KV>
      ) : null}
      {(d.context_window || d.compact_window || d.effort || d.timeout_ms) && (
        <KV title={titled(t("settings.context"))}>
          {d.context_window ? <KVRow k={t("settings.context_window")} v={`${tokens(d.context_window)} tokens`} /> : null}
          {d.compact_window ? <KVRow k={t("settings.compact_window")} v={`${tokens(d.compact_window)} tokens`} /> : null}
          {d.effort ? <KVRow k={t("settings.effort")} v={d.effort} /> : null}
          {d.timeout_ms ? <KVRow k={t("settings.request_timeout")} v={d.timeout_ms >= 60_000 ? t("settings.minutes", { count: Math.round(d.timeout_ms / 60_000) }) : t("settings.seconds", { count: Math.round(d.timeout_ms / 1000) })} /> : null}
        </KV>
      )}
      {d.args?.length ? (
        <KV title={titled(t("settings.launch_args"))}>
          <p className="py-2 font-mono text-[11.5px] break-all text-ink">{d.args.join(" ")}</p>
        </KV>
      ) : null}
      {d.env?.length ? (
        <KV title={titled(t("settings.other_settings", { count: d.env.length }))}>
          {d.env.map((e) => (
            <KVRow key={e.key} k={<span className="font-mono text-[11px]">{e.key}</span>} v={e.value} mono />
          ))}
        </KV>
      ) : null}
    </>
  );
}

// ── Devices ─────────────────────────────────────────────

function Devices() {
  const [data, setData] = useState<{ tokens: DeviceToken[]; current?: string; online: Set<string> } | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [confirm, setConfirm] = useState<string | null>(null);
  const [revokingOthers, setRevokingOthers] = useState(false);
  const [connecting, setConnecting] = useState(false);
  const load = () => {
    setLoading(true);
    return api<{ tokens: DeviceToken[]; current?: string; online?: string[] }>("/auth/tokens")
      .then((r) => (setData({ tokens: r.tokens ?? [], current: r.current, online: new Set(r.online ?? []) }), setError("")))
      .catch((e) => setError(errorText(e)))
      .finally(() => setLoading(false));
  };
  useEffect(() => void load(), []);

  const revoke = async (id: string) => {
    try {
      await api(`/auth/tokens/${enc(id)}`, { method: "DELETE" });
      toast(t("settings.revoked_toast"), "ok");
      setConfirm(null);
      void load();
    } catch (e) {
      toast(errorText(e), "error");
    }
  };
  const { groups, revoked } = groupDevices(data?.tokens ?? [], data?.current, data?.online);
  const otherBrowsers = (groups.find((g) => g.key === "browsers")?.items ?? []).filter((tok) => tok.id !== data?.current);
  const revokeOtherBrowsers = async () => {
    if (revokingOthers || otherBrowsers.length === 0) return;
    setRevokingOthers(true);
    let ok = 0;
    let firstError = "";
    try {
      for (const tok of otherBrowsers) {
        try {
          await api(`/auth/tokens/${enc(tok.id)}`, { method: "DELETE" });
          ok++;
        } catch (e) {
          if (!firstError) firstError = errorText(e);
        }
      }
      setConfirm(null);
      if (ok > 0) toast(t("settings.revoked_other_browsers_toast", { count: ok }), "ok");
      if (firstError) toast(firstError, "error");
      void load();
    } finally {
      setRevokingOthers(false);
    }
  };

  const kind: Record<string, string> = {
    phone: t("settings.kind_phone"),
    watch: t("settings.kind_watch"),
    other: t("settings.kind_cli"),
    web: t("settings.kind_web"),
    link: t("settings.kind_link"),
  };
  const roles: Record<string, string> = { answer: t("settings.role_answer"), scoped: t("settings.role_scoped") };
  const groupTitle = (key: DeviceGroupKey, count: number): string => {
    if (key === "devices") return t("settings.devices_title", { count });
    if (key === "browsers") return t("settings.devices_group_browsers", { count });
    if (key === "links") return t("settings.devices_group_links", { count });
    return t("settings.devices_group_integrations", { count });
  };
  const iconFor = (tok: DeviceToken) =>
    tok.kind === "phone" ? "phone" : tok.kind === "watch" ? "watch" : tok.kind === "web" ? "web" : tok.kind === "link" ? "link" : "key";

  // A revoked credential is shown greyed, with when it was revoked and no "Revoke" (iOS DeviceCredentials.swift).
  const row = (tok: DeviceToken, gone: boolean) => (
    <div key={tok.id} className="flex items-center gap-3 px-4 py-2.5">
      <Icon name={iconFor(tok)} size={15} className="shrink-0 text-ink-3" />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className={cx("truncate text-[13px]", gone ? "text-ink-3" : "text-ink")}>{tok.name || tok.id}</span>
          {tok.id === data?.current && <Pill tone="green">{t("settings.this_browser")}</Pill>}
          {!gone && tok.id !== data?.current && data?.online.has(tok.id) && <Pill tone="green">{t("settings.device_online")}</Pill>}
          {tok.role !== "full" && <Pill>{roles[tok.role] ?? tok.role}</Pill>}
        </div>
        <div className="mt-0.5 truncate text-[11.5px] text-ink-3">
          {kind[tok.kind] ?? tok.kind}
          {gone && tok.revoked_at ? ` · ${t("settings.revoked_ago", { time: ago(tok.revoked_at) })}` : ""}
          {!gone && tok.last_seen ? ` · ${t("settings.used_ago", { time: ago(tok.last_seen) })}` : ""}
          {tok.created_at ? ` · ${t("settings.added_ago", { time: ago(tok.created_at) })}` : ""}
        </div>
        {tok.scopes?.length ? (
          <div className="mt-0.5 break-words text-xs text-ink-3">{tok.scopes.join(" · ")}</div>
        ) : null}
      </div>
      {!gone && tok.id !== data?.current && (
        <RowConfirm
          open={confirm === tok.id}
          confirm={t("settings.confirm_revoke")}
          onCancel={() => setConfirm(null)}
          onConfirm={() => void revoke(tok.id)}
          trigger={
            <Button tone="quiet" onClick={() => setConfirm(tok.id)} className="whitespace-nowrap text-red enabled:hover:text-red">
              {t("settings.revoke")}
            </Button>
          }
        />
      )}
    </div>
  );

  const connectButton = (
    <Button tone="quiet" icon="plus" onClick={() => setConnecting(true)} className="whitespace-nowrap">
      {t("settings-connectdevice.add")}
    </Button>
  );
  const revokeOthersControl = otherBrowsers.length > 0 ? (
    <RowConfirm
      open={confirm === "__other_browsers__"}
      confirm={t("settings.confirm_revoke_other_browsers")}
      onCancel={() => setConfirm(null)}
      onConfirm={() => void revokeOtherBrowsers()}
      trigger={
        <Button
          tone="quiet"
          busy={revokingOthers}
          onClick={() => setConfirm("__other_browsers__")}
          className="whitespace-nowrap text-red enabled:hover:text-red"
        >
          {t("settings.revoke_other_browsers")}
        </Button>
      }
    />
  ) : null;

  return (
    <>
      {!data && !error && <Loading />}
      {error && <div className="mb-4 flex items-center gap-3">
        <p role="alert" className="min-w-0 flex-1 text-[13px] break-words text-red">{error}</p>
        <Button icon="retry" onClick={() => void load()} busy={loading}>{t("settings.retry")}</Button>
      </div>}
      {data && groups.length === 0 && (
        <Group
          anchor="devices"
          title={t("settings.devices_title", { count: 0 })}
          footer={t("settings.devices_footer")}
          right={connectButton}
        >
          <p className="px-4 py-3 text-[12.5px] text-ink-3">{t("settings.no_devices")}</p>
        </Group>
      )}
      {data && groups.map((group, idx) => {
        const isFirst = idx === 0;
        const isLast = idx === groups.length - 1;
        const right = isFirst && group.key === "browsers" && revokeOthersControl
          ? <div className="flex items-center gap-2">{revokeOthersControl}{connectButton}</div>
          : isFirst
            ? connectButton
            : group.key === "browsers"
              ? revokeOthersControl
              : undefined;
        return (
          <Group
            key={group.key}
            anchor={isFirst ? "devices" : undefined}
            title={groupTitle(group.key, group.items.length)}
            footer={isLast ? t("settings.devices_footer") : undefined}
            right={right}
          >
            {group.items.map((tok) => row(tok, false))}
          </Group>
        );
      })}
      {data && revoked.length > 0 && <Group title={t("settings.revoked_title", { count: revoked.length })}>{revoked.map((tok) => row(tok, true))}</Group>}
      <ConnectDevice open={connecting} onClose={() => setConnecting(false)} onPaired={() => void load()} />
    </>
  );
}
