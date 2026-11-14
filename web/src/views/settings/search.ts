import { t } from "../../i18n";
import { LOCAL_SHORTCUTS, SHORTCUTS } from "../../lib/shortcuts";
import { TAB_INFO, TABS, type Tab } from "./nav";

// ── search ──────────────────────────────────────────────

/** A group (or a models/agents section), or one setting's row, inside a page that the search box can jump to. */
export interface SearchGroup {
  tab: Tab;
  /** The group's heading or the row's label, as the page shows it ("A › B" for a row inside a named part of the page). */
  group: string;
  /** The `data-settings-group` marker of a Group, the `data-settings-row` marker of a row, or the element id of a
   *  models/agents section. A row a page draws only sometimes is found by its group, which is always there. */
  anchor: string;
  /** More words it is found by (the choices a row offers, the rows only shown once it is on), never shown. */
  hint?: string;
}

const path = (...parts: string[]) => parts.join(" › ");
const words = (...parts: string[]) => parts.join(" / ");

// Each agent's own settings the node lists by their catalog names (lib/agent-settings.ts settingTitle), found in that
// agent's section of Agents.
const AGENT_SETTINGS: { name: string; anchor: string; keys: string[] }[] = [
  { name: "Claude Code", anchor: "settings-claude", keys: ["effortLevel", "alwaysThinkingEnabled", "awaySummaryEnabled", "language", "promptSuggestionEnabled", "spinnerTipsEnabled", "prefersReducedMotion", "autoUpdatesChannel"] },
  { name: "Codex", anchor: "settings-codex", keys: ["model", "model_reasoning_effort", "service_tier", "model_reasoning_summary", "model_verbosity", "approval_policy", "sandbox_mode", "hide_agent_reasoning", "show_raw_agent_reasoning"] },
  { name: "Antigravity", anchor: "settings-agy", keys: ["model", "agentMode"] },
];

/** What the search box matches beyond the categories (a curated static index): the pages' group headings and, by their
 *  labels, the settings on them (audit item 18), each in the page's own words, so every language finds them. Every
 *  static row label of the pages search.test.ts reads has an entry here. */
export const SEARCH_INDEX: SearchGroup[] = [
  // General › Appearance
  { tab: "general", group: t("settings.appearance"), anchor: "appearance" },
  { tab: "general", group: words(t("settings.theme_light"), t("settings.theme_dark")), hint: t("language.system"), anchor: "theme" },
  { tab: "general", group: t("ui-look.accent"), anchor: "theme-color" },
  { tab: "general", group: t("ui-look.text_size"), anchor: "text-size" },
  { tab: "general", group: t("language.label"), anchor: "language" },
  { tab: "general", group: t("settings.reply_motion"), anchor: "reply-motion" },
  { tab: "general", group: t("ui-look.recent_chats"), anchor: "recent-chats" },
  { tab: "general", group: t("settings.sidebar_pages"), anchor: "sidebar-pages" },
  // General › Translation (its language and what it translates show once it is on)
  { tab: "general", group: t("settings-nav.tab_translate"), anchor: "settings-translation" },
  { tab: "general", group: t("settings-translationsettings.auto"), anchor: "translate-auto" },
  { tab: "general", group: t("settings-translationsettings.model"), anchor: "translate-model" },
  { tab: "general", group: t("settings-translationsettings.target"), hint: words(t("settings-translationsettings.mail"), t("settings-translationsettings.mail_switch"), t("settings-translationsettings.lark_chats")), anchor: "settings-translation" },
  // General › Agents
  { tab: "general", group: t("settings.agent_preferences"), hint: words(t("terminal-full.mode_group"), t("settings.sidebar_terminals")), anchor: "agent-preferences" },
  { tab: "general", group: t("settings-agentopen.label"), hint: words(t("settings-agentopen.panel"), t("settings-agentopen.page")), anchor: "agent-open" },
  { tab: "general", group: t("context.details"), anchor: "agent-details" },
  { tab: "general", group: t("context.as_used"), anchor: "context-as-used" },
  // General › hubs, this browser, about
  { tab: "general", group: t("hubs.group_title"), hint: t("hubs.add_hub"), anchor: "hubs" },
  { tab: "general", group: t("settings.this_browser"), hint: words(t("settings.hub"), t("settings.linked_hubs")), anchor: "this-browser" },
  { tab: "general", group: t("settings.hub_address"), anchor: "hub-address" },
  { tab: "general", group: t("settings.computers"), anchor: "computers" },
  { tab: "general", group: t("settings.install_app"), anchor: "install-app" },
  { tab: "general", group: t("settings.disconnect_browser"), hint: t("disconnect.sign_out"), anchor: "disconnect" },
  { tab: "general", group: t("settings.source_code"), hint: t("settings.about_persona"), anchor: "about-persona" },
  // the assistant's profile
  { tab: "persona", group: t("settings-assistantsettings.routines"), hint: t("settings-assistantsettings.proactive_title"), anchor: "routines" },
  { tab: "persona", group: t("settings-assistantsettings.quiet_tasks"), anchor: "quiet-tasks" },
  { tab: "persona", group: t("settings-assistantsettings.background_folded"), anchor: "background-folded" },
  { tab: "persona", group: t("settings-assistantsettings.next_suggestions"), anchor: "next-suggestions" },
  { tab: "persona", group: t("settings-assistantsettings.start_requested"), hint: t("settings-assistantsettings.start_title"), anchor: "start-requested" },
  { tab: "persona", group: t("settings-assistantsettings.workspace_preference"), anchor: "workspace-preference" },
  { tab: "persona", group: t("settings-assistantsettings.trust_title"), anchor: "task-trust" },
  {
    tab: "persona",
    group: t("settings-assistantsettings.agents_title"),
    hint: words(
      t("settings-assistantsettings.auto_naming"), t("settings-assistantsettings.auto_naming_max"), t("settings-assistantsettings.auto_naming_periodic"),
      t("settings-assistantsettings.auto_naming_interval"), t("settings-assistantsettings.main_supervises"), t("settings-assistantsettings.agent_tasks"),
      t("settings-assistantsettings.inbox_signals"), t("settings-assistantsettings.idle_close"), t("settings-assistantsettings.archive_closed"),
    ),
    anchor: "agents",
  },
  { tab: "persona", group: t("settings-assistantsettings.default_permissions"), anchor: "default-permissions" },
  { tab: "persona", group: t("settings-standing.title"), anchor: "standing-authorizations" },
  { tab: "persona", group: t("settings-assistantsettings.meeting_attendance"), hint: t("settings-assistantsettings.meetings_title"), anchor: "meetings" },
  { tab: "persona", group: t("settings-assistantsettings.recordings_title"), hint: words(t("settings-assistantsettings.recording_days"), t("settings-assistantsettings.minutes_days")), anchor: "recordings" },
  { tab: "persona", group: t("settings-assistantsettings.knows_title"), hint: words(t("settings-assistantsettings.about_me"), t("settings-assistantsettings.memory")), anchor: "knows" },
  { tab: "persona", group: t("settings-repositories.title"), anchor: "repositories" },
  { tab: "persona", group: t("settings-skills.title"), anchor: "skills" },
  { tab: "persona", group: t("settings-assistantsettings.machine"), hint: t("settings-assistantsettings.runs_on_title"), anchor: "runs-on-machine" },
  { tab: "persona", group: t("settings-assistantsettings.cwd"), anchor: "runs-on-cwd" },
  { tab: "persona", group: t("settings-assistantsettings.main_place"), anchor: "runs-on" },
  { tab: "persona", group: t("settings-assistantsettings.main_runtime"), anchor: "runs-on" },
  { tab: "persona", group: t("settings-assistantsettings.timezone"), anchor: "runs-on" },
  // the assistant's profile › Session triage
  { tab: "persona", group: t("settings-triagesettings.title"), anchor: "triage" },
  { tab: "persona", group: t("settings-triagesettings.decide"), anchor: "triage-decide" },
  { tab: "persona", group: t("settings-triagesettings.auto_continue"), anchor: "triage-auto-continue" },
  { tab: "persona", group: t("settings-triagesettings.wake"), anchor: "triage-wake" },
  { tab: "persona", group: t("settings-triagesettings.tool_loop"), anchor: "triage-tool-loop" },
  { tab: "persona", group: t("settings-triagesettings.closeout"), anchor: "triage-closeout" },
  { tab: "persona", group: t("settings-triagesettings.closeout_ask"), anchor: "triage-closeout-ask" },
  { tab: "persona", group: t("settings-triagesettings.reply_reminder"), anchor: "triage-reply-reminder" },
  // the hub's
  { tab: "avatar", group: t("settings.avatar"), anchor: "avatar" },
  { tab: "hub", group: t("workspace-board.setting_group"), anchor: "agent-group" },
  // Models' sections and Agents' sections (their element ids)
  { tab: "models", group: t("settings-nav.tab_providers"), anchor: "settings-providers" },
  // a provider's key is "API Key" in every language, and people call it a key or a secret too
  { tab: "models", group: t("settings.add_provider"), hint: words("API Key", t("settings.search_key_words"), t("settings-providerimport.import")), anchor: "settings-providers" },
  { tab: "models", group: t("settings-nav.tab_model_config"), anchor: "settings-model-config" },
  { tab: "models", group: path(t("settings-nav.tab_model_config"), t("settings-modelconfiguration.effort")), hint: words(t("settings-modelconfiguration.model_provider"), t("settings-modelconfiguration.model")), anchor: "settings-model-config" },
  { tab: "models", group: t("settings-modelprofiles.title"), anchor: "model-profiles" },
  { tab: "agents", group: "Claude Code", anchor: "settings-claude" },
  { tab: "agents", group: "Codex", anchor: "settings-codex" },
  { tab: "agents", group: "Antigravity", anchor: "settings-agy" },
  { tab: "agents", group: "omp", anchor: "settings-omp" },
  ...AGENT_SETTINGS.flatMap(({ name, anchor, keys }) => keys.map((key): SearchGroup => ({ tab: "agents", group: path(name, t(`lib-agent-settings.setting_${key}`)), anchor }))),
  { tab: "knowledge", group: t("settings-knowledge.topics_title"), anchor: "knowledge-topics" },
  { tab: "knowledge", group: t("settings-knowledge.topics_switch"), anchor: "knowledge-topics-switch" },
  { tab: "knowledge", group: t("settings-knowledge.items"), anchor: "knowledge-items" },
  { tab: "knowledge", group: t("settings-knowledge.messages"), hint: words(t("settings-knowledge.include_all"), t("settings-knowledge.context")), anchor: "knowledge-messages" },
  { tab: "knowledge", group: t("settings-knowledge.docs"), anchor: "knowledge-docs" },
  // Notifications
  { tab: "notifications", group: t("settings-notifications.system_title"), anchor: "system" },
  { tab: "notifications", group: t("settings-notifications.system"), anchor: "notify-system" },
  { tab: "notifications", group: t("settings-notifications.system_always"), anchor: "notify-system-always" },
  { tab: "notifications", group: t("settings-notifications.webpush"), hint: words(t("settings-notifications.webpush_title"), t("settings-notifications.webpush_test_label")), anchor: "web-push" },
  { tab: "notifications", group: t("settings-notifications.sound"), anchor: "notify-sound" },
  { tab: "notifications", group: t("settings-notifications.input"), anchor: "notify-input" },
  { tab: "notifications", group: t("settings-notifications.done_title"), anchor: "done" },
  { tab: "notifications", group: t("settings-notifications.quiet"), hint: words(t("settings-notifications.quiet_start"), t("settings-notifications.quiet_end")), anchor: "notify-quiet" },
  // Connectors
  { tab: "connectors", group: t("settings-connectors.mail"), anchor: "mail" },
  { tab: "connectors", group: t("settings-connectors.lark"), anchor: "lark" },
  { tab: "connectors", group: t("settings-sent.entry_label"), hint: t("settings-sent.hint"), anchor: "sent" },
  // Extensions: an extension's secrets are on its page, opened from the list; a market's token under Manage markets
  { tab: "extensions", group: t("settings-extensions.title"), anchor: "extensions" },
  { tab: "extensions", group: t("settings-extensions.secrets_title"), anchor: "extensions" },
  { tab: "extensions", group: t("settings-extensions.market_title"), anchor: "market" },
  { tab: "extensions", group: t("settings-extensionmarket.manage_markets"), hint: t("settings-extensionmarket.token_optional"), anchor: "market" },
  // Shortcuts: each command by its name
  { tab: "shortcuts", group: t("settings-shortcutsettings.enable_all"), anchor: "shortcuts-all" },
  ...SHORTCUTS.map((s): SearchGroup => ({ tab: "shortcuts", group: s.label, anchor: `shortcut-${s.id}` })),
  { tab: "shortcuts", group: t("settings-shortcutsettings.terminal_title"), anchor: "shortcuts-terminal" },
  ...LOCAL_SHORTCUTS.map((s): SearchGroup => ({ tab: "shortcuts", group: s.label, anchor: `shortcut-${s.id}` })),
  // This hub
  { tab: "hub", group: t("settings-hubsettings.version"), hint: t("settings-hubsettings.last_check"), anchor: "hub-version" },
  { tab: "hub", group: t("settings-hubsettings.channel"), anchor: "hub-channel" },
  { tab: "hub", group: t("settings-hubsettings.update"), anchor: "hub-update" },
  { tab: "hub", group: t("settings-hubsettings.auto_update"), anchor: "hub-auto-update" },
  { tab: "hub", group: t("workspace-board.setting_label"), anchor: "workflow-board" },
  { tab: "hub", group: t("settings-hubsettings.mirrors"), hint: words(t("settings-hubsettings.registry_mirror"), t("settings-hubsettings.catalog_mirror")), anchor: "mirrors" },
  // Devices: the list (each with its own token, revoked there) and pairing a new one
  { tab: "devices", group: t("settings.device_tokens"), anchor: "devices" },
  { tab: "devices", group: t("settings.revoke"), anchor: "devices" },
  { tab: "devices", group: t("settings-connectdevice.add"), anchor: "devices" },
];

// What is typed and what is matched, compared without case or spaces: "timezone" finds "Time zone", "apikey" "API Key".
const fold = (s: string) => s.toLocaleLowerCase().replace(/\s+/g, "");

/** The search box's matches: categories by label / hint, and page groups and settings by their heading or label. */
export function searchSettings(query: string): { tabs: Tab[]; groups: SearchGroup[] } {
  const q = fold(query);
  if (!q) return { tabs: [], groups: [] };
  const has = (...texts: (string | undefined)[]) => texts.some((s) => s && fold(s).includes(q));
  return {
    tabs: TABS.filter((t) => has(TAB_INFO[t].label, TAB_INFO[t].hint)),
    groups: SEARCH_INDEX.filter((g) => has(g.group, g.hint)),
  };
}
