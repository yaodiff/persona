import { mailReading } from "../../lib/mail";
import { getState } from "../../lib/store";
import { INBOX_SOURCES_FEATURE } from "../../lib/subscriptions";
import { t } from "../../i18n";
import { LOCAL_SHORTCUTS, SHORTCUTS } from "../../lib/shortcuts";
import { registeredModules } from "../../core/module-host";
import { enabledSettingsTabs } from "../../core/module-settings";
import { MODULE_FEATURES_ID } from "../../core/module-features";
import { INTEGRATIONS_SETTINGS_REGISTRATION } from "./integrations-registration";
import { isLegacyIntegrationTab } from "./integrations-route";
import { tabInfo, TABS, type Tab } from "./nav";

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
  /** Optional item sub-route to open when selecting this search result. */
  sub?: string;
}

const path = (...parts: string[]) => parts.join(" › ");
const words = (...parts: string[]) => parts.join(" / ");

// Each agent's own settings the node lists by their catalog names (lib/agent-settings.ts settingTitle), found in that
// agent's section of Agents.
const AGENT_SETTINGS: { name: string; anchor: string; keys: string[] }[] = [
  { name: "Claude Code", anchor: "settings-claude", keys: ["effortLevel", "alwaysThinkingEnabled", "awaySummaryEnabled", "language", "promptSuggestionEnabled", "spinnerTipsEnabled", "prefersReducedMotion", "autoUpdatesChannel"] },
  { name: "Codex", anchor: "settings-codex", keys: ["model", "model_reasoning_effort", "service_tier", "model_reasoning_summary", "model_verbosity", "approval_policy", "sandbox_mode", "hide_agent_reasoning", "show_raw_agent_reasoning"] },
  { name: "Antigravity", anchor: "settings-agy", keys: ["model", "agentMode"] },
  { name: "Grok", anchor: "settings-grok", keys: ["models.default", "models.default_reasoning_effort", "ui.permission_mode", "ui.yolo", "ui.compact_mode"] },
];

/** What the search box matches beyond the categories (a curated static index): the pages' group headings and, by their
 *  labels, the settings on them (audit item 18), each in the page's own words, so every language finds them. Every
 *  static row label of the pages search.test.ts reads has an entry here. */
const SHARED_INDEX: SearchGroup[] = [
  // General › Appearance
  { tab: "general", group: t("settings.appearance"), anchor: "appearance" },
  { tab: "general", group: words(t("settings.theme_light"), t("settings.theme_dark")), hint: t("language.system"), anchor: "theme" },
  { tab: "general", group: t("ui-look.text_size"), anchor: "text-size" },
  { tab: "general", group: t("language.label"), anchor: "language" },
  { tab: "general", group: t("settings.reply_motion"), anchor: "reply-motion" },
  { tab: "general", group: t("settings.starred_messages"), anchor: "starred-messages" },
  { tab: "general", group: t("ui-look.recent_chats"), anchor: "recent-chats" },
  { tab: "general", group: t("ui-look.sidebar_usage"), anchor: "sidebar-usage" },
  { tab: "general", group: t("settings.needs_you_auto_open"), anchor: "needs-you-auto-open" },
  { tab: "general", group: t("settings.start_page"), anchor: "start-page" },
  { tab: "general", group: t("settings.start_page_open_on"), hint: t("settings.start_page_main"), anchor: "open-on" },
  { tab: "general", group: t("settings.sidebar_pages"), anchor: "sidebar-pages" },
  // General › Translation (its language and what it translates show once it is on)
  { tab: "general", group: t("settings-nav.tab_translate"), anchor: "settings-translation" },
  { tab: "general", group: t("settings-translationsettings.auto"), anchor: "translate-auto" },
  { tab: "general", group: t("settings-translationsettings.model"), anchor: "translate-model" },
  { tab: "general", group: t("settings-translationsettings.target"), hint: words(t("settings-translationsettings.mail"), t("settings-translationsettings.mail_switch"), t("settings-translationsettings.lark_chats")), anchor: "settings-translation" },
  // General › Agents
  { tab: "general", group: t("settings.agent_preferences"), hint: words(t("terminal-full.mode_group"), t("settings.sidebar_terminals")), anchor: "agent-preferences" },
  { tab: "general", group: t("settings-agentopen.label"), hint: words(t("settings-agentopen.panel"), t("settings-agentopen.page")), anchor: "agent-open" },
  { tab: "general", group: t("seen-rows.label"), hint: words(t("seen-rows.stay"), t("seen-rows.leave")), anchor: "seen-rows" },
  { tab: "general", group: t("context.details"), anchor: "agent-details" },
  // General › hubs, this browser, about
  { tab: "general", group: t("hubs.group_title"), hint: t("hubs.add_hub"), anchor: "hubs" },
  { tab: "general", group: t("settings.this_browser"), hint: words(t("settings.hub"), t("settings.linked_hubs")), anchor: "this-browser" },
  { tab: "general", group: t("settings.hub_address"), anchor: "hub-address" },
  { tab: "general", group: t("settings.computers"), anchor: "computers" },
  { tab: "general", group: t("settings.install_app"), anchor: "install-app" },
  { tab: "general", group: t("settings.disconnect_browser"), hint: t("disconnect.sign_out"), anchor: "disconnect" },
  { tab: "general", group: t("settings.source_code"), hint: t("settings.about_persona"), anchor: "about-persona" },
  // Advanced › Appearance (#13398): the non-default looks. General keeps the default appearance and text size.
  { tab: "advanced", group: t("settings.appearance"), anchor: "appearance" },
  { tab: "advanced", group: t("ui-look.surface_style"), hint: words(t("ui-look.surface_default"), t("ui-look.surface_liquid_glass"), t("ui-look.glass_translucency")), anchor: "surface-style" },
  { tab: "advanced", group: t("ui-look.accent"), anchor: "theme-color" },
  { tab: "advanced", group: t("ui-extensionthemepicker.label"), anchor: "extension-theme" },
  { tab: "advanced", group: t("ui-look.font"), hint: words(t("ui-look.font_sans"), t("ui-look.font_mono"), t("ui-look.font_browser")), anchor: "font" },
  { tab: "advanced", group: t("ui-look.sidebar_icons"), hint: words(t("ui-look.sidebar_icons_monochrome"), t("ui-look.sidebar_icons_colored")), anchor: "sidebar-icons" },
  { tab: "advanced", group: t("ui-look.phone_nav"), hint: words(t("ui-look.phone_nav_switch"), t("ui-look.phone_nav_tabs")), anchor: "phone-nav" },
  // the assistant's profile
  { tab: "persona", group: t("settings-assistantsettings.routines"), hint: t("settings-assistantsettings.proactive_title"), anchor: "routines" },
  { tab: "persona", group: t("settings-assistantsettings.quiet_tasks"), anchor: "quiet-tasks" },
  { tab: "persona", group: t("settings-assistantsettings.background_folded"), anchor: "background-folded" },
  { tab: "persona", group: t("settings-assistantsettings.relay_replies"), anchor: "relay-replies" },
  { tab: "persona", group: t("settings-assistantsettings.recent_task_chats"), anchor: "recent-task-chats" },
  { tab: "persona", group: t("settings-assistantsettings.next_suggestions"), anchor: "next-suggestions" },
  { tab: "persona", group: t("settings-assistantsettings.dashboards"), anchor: "dashboards" },
  { tab: "persona", group: t("settings-assistantsettings.claude_interactive"), anchor: "claude-interactive" },
  { tab: "persona", group: t("settings-assistantsettings.start_requested"), hint: t("settings-assistantsettings.start_title"), anchor: "start-requested" },
  { tab: "persona", group: t("settings-assistantsettings.workspace_preference"), anchor: "workspace-preference" },
  { tab: "persona", group: t("settings-assistantsettings.trust_title"), anchor: "task-trust" },
  {
    tab: "persona",
    group: t("settings-assistantsettings.agents_title"),
    hint: words(
      t("settings-assistantsettings.auto_naming"), t("settings-assistantsettings.auto_naming_max"), t("settings-assistantsettings.auto_naming_periodic"),
      t("settings-assistantsettings.auto_naming_interval"), t("settings-assistantsettings.main_supervises"), t("settings-assistantsettings.auto_supervision_mode"), t("settings-assistantsettings.supervision_triage"), t("settings-assistantsettings.delivery_gate"), t("settings-assistantsettings.agent_tasks"), t("settings-assistantsettings.close_ended_terminals"),
      t("settings-assistantsettings.inbox_signals"), t("settings-assistantsettings.idle_close"), t("settings-assistantsettings.archive_closed"),
    ),
    anchor: "agents",
  },
  { tab: "persona", group: t("settings-assistantsettings.inbox_todo_forward"), anchor: "inbox-todo-forward" },
  { tab: "persona", group: t("settings-assistantsettings.default_permissions"), hint: words(t("settings-directionnotes.title"), t("settings-directionnotes.permissions")), anchor: "default-permissions" },
  { tab: "persona", group: t("settings-assistantsettings.readonly_mode"), anchor: "readonly-mode" },
  { tab: "persona", group: t("settings-assistantsettings.approval_mirror_main"), anchor: "approval-mirror-main" },
  {
    tab: "persona",
    group: t("settings-assistantsettings.owner_approval_title"),
    hint: words(
      t("settings-assistantsettings.owner_approval_switch"),
      t("settings-assistantsettings.owner_approval_persistent"),
      t("settings-assistantsettings.owner_approval_phrases"),
    ),
    anchor: "owner-reply-approval",
  },
  { tab: "persona", group: t("settings-standing.title"), anchor: "standing-authorizations" },
  { tab: "persona", group: t("settings-assistantsettings.meeting_attendance"), hint: t("settings-assistantsettings.meetings_title"), anchor: "meetings" },
  { tab: "persona", group: t("settings-assistantsettings.recordings_title"), hint: words(t("settings-assistantsettings.recording_days"), t("settings-assistantsettings.recording_audio_days"), t("settings-assistantsettings.recording_text_days"), t("settings-assistantsettings.minutes_days")), anchor: "recordings" },
  { tab: "persona", group: t("settings-assistantsettings.knows_title"), hint: words(t("settings-assistantsettings.about_me"), t("settings-assistantsettings.memory")), anchor: "knows" },
  { tab: "persona", group: t("settings-directionnotes.title"), anchor: "direction-notes" },
  { tab: "persona", group: t("settings-assistantsettings.task_lessons"), hint: t("settings-assistantsettings.task_lessons_hint"), anchor: "task-lessons" },
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
  { tab: "models", group: t("execution.runtimes_title"), anchor: "settings-runtimes" },
  { tab: "models", group: t("execution.channels_title"), hint: words(t("execution.channel_base_url"), t("execution.rollback")), anchor: "settings-execution-channels" },
  { tab: "models", group: t("settings-modelprofiles.title"), anchor: "model-profiles" },
  { tab: "agents", group: "Claude Code", anchor: "settings-claude" },
  { tab: "agents", group: "Codex", anchor: "settings-codex" },
  { tab: "agents", group: "Antigravity", anchor: "settings-agy" },
  { tab: "agents", group: "omp", anchor: "settings-omp" },
  { tab: "agents", group: "Grok", anchor: "settings-grok" },
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
  // the hub's switch (#8157), by its group, which it draws on any hub and while it loads (LowDistractionSettings.tsx)
  { tab: "notifications", group: t("settings-notifications.low_distraction"), hint: t("settings-notifications.low_distraction_hint"), anchor: "low-distraction" },
  // Shortcuts: each command by its name
  { tab: "shortcuts", group: t("settings-shortcutsettings.enable_all"), anchor: "shortcuts-all" },
  ...SHORTCUTS.map((s): SearchGroup => ({ tab: "shortcuts", group: s.label, anchor: `shortcut-${s.id}` })),
  { tab: "shortcuts", group: t("settings-shortcutsettings.terminal_title"), anchor: "shortcuts-terminal" },
  ...LOCAL_SHORTCUTS.map((s): SearchGroup => ({ tab: "shortcuts", group: s.label, anchor: `shortcut-${s.id}` })),
  // Labs: each module's settings by their names and choices (Labs.tsx; the form marks a field's row
  // configuration-<document>-<field>)
  { tab: "labs", group: t("settings-nav.tab_labs"), hint: t("settings-labs.trial"), anchor: "labs" },
  { tab: "labs", group: path(t("settings-labs.work_area"), t("settings-labs.work_title")), anchor: "configuration-assistant-work-work_guide" },
  { tab: "labs", group: path(t("settings-labs.work_area"), t("settings-labs.todos_v2_title")), anchor: "configuration-assistant-todos-v2-todos_v2" },
  { tab: "labs", group: path(t("settings-labs.work_area"), t("settings-labs.integrations_v2_title")), anchor: "configuration-assistant-integrations-v2-integrations_v2" },
  { tab: "labs", group: path(t("settings-labs.area_agents"), t("settings-labs.terminal_renderer")), hint: words(t("settings-labs.terminal_renderer_standard"), t("settings-labs.terminal_renderer_webgl")), anchor: "configuration-terminal-renderer" },
  // This hub
  { tab: "hub", group: t("settings-recovery.title"), hint: t("settings-recovery.entry_hint"), anchor: "hub-recovery" },
  { tab: "hub", group: t("settings-hubsettings.version"), hint: words(t("settings-hubsettings.last_check"), t("settings-hubsettings.branch"), t("settings-hubsettings.current_commit"), t("settings-hubsettings.latest_commit")), anchor: "hub-version" },
  { tab: "hub", group: t("settings-hubsettings.channel"), hint: words(t("settings-hubsettings.source"), t("settings-hubsettings.web_dev")), anchor: "hub-channel" },
  { tab: "hub", group: t("settings-hubsettings.update"), hint: words(t("settings-hubsettings.update_version"), t("settings-hubsettings.target_commit"), t("settings-hubsettings.backup_created_at")), anchor: "hub-update" },
  { tab: "hub", group: t("settings-hubsettings.auto_update"), hint: t("settings-hubsettings.computer_auto"), anchor: "hub-auto-update" },
  { tab: "hub", group: t("settings-hubsettings.dev_backup"), hint: words(t("settings-hubsettings.backup_folder"), t("settings-hubsettings.backup_retain")), anchor: "hub-dev-backup" },
  { tab: "agents", group: t("settings.show_botmux"), hint: t("settings.show_botmux_hint"), anchor: "botmux-display" },
  { tab: "agents", group: t("settings.sort_by_activity"), hint: t("settings.sort_by_activity_hint"), anchor: "agents-sort-by-activity" },
  { tab: "agents", group: t("settings.show_agents_filter"), hint: t("settings.show_agents_filter_hint"), anchor: "agents-show-filter" },
  { tab: "agents", group: t("settings.use_herdr_tab_name"), hint: t("settings.use_herdr_tab_name_hint"), anchor: "herdr-tab-session-name" },
  { tab: "agents", group: t("herdr-sessions.title"), hint: t("herdr-sessions.footer"), anchor: "settings-herdr-sessions" },
  { tab: "hub", group: t("workspace-board.setting_label"), anchor: "workflow-board" },
  { tab: "hub", group: t("settings-hubsettings.mirrors"), hint: words(t("settings-hubsettings.registry_mirror"), t("settings-hubsettings.catalog_mirror")), anchor: "mirrors" },
  // Devices: the list (each with its own token, revoked there) and pairing a new one
  { tab: "devices", group: t("settings.device_tokens"), anchor: "devices" },
  { tab: "devices", group: t("settings.revoke"), anchor: "devices" },
  { tab: "devices", group: t("settings-connectdevice.add"), anchor: "devices" },
];

/** General › Optional features follows the registry, including modules switched off on this browser. */
function moduleSwitchEntries(): SearchGroup[] {
  const modules = registeredModules();
  if (!modules.length) return [];
  return [
    { tab: "general", group: t("settings-features.features_group"), hint: t("settings-features.area_features"), anchor: `configuration-${MODULE_FEATURES_ID}-modules` },
    ...modules.map((d): SearchGroup => ({ tab: "general", group: d.title(), hint: d.description(), anchor: `configuration-${MODULE_FEATURES_ID}-${d.id}` })),
  ];
}

// What is typed and what is matched, compared without case or spaces: "timezone" finds "Time zone", "apikey" "API Key".
const fold = (s: string) => s.toLocaleLowerCase().replace(/\s+/g, "");

export const SEARCH_INDEX: SearchGroup[] = [...SHARED_INDEX, ...INTEGRATIONS_SETTINGS_REGISTRATION.searchGroups(false)];
const UNIFIED_INDEX: SearchGroup[] = [...SHARED_INDEX, ...INTEGRATIONS_SETTINGS_REGISTRATION.searchGroups(true)];

/** The search box's matches: categories by label / hint, and page groups and settings by their heading or label.
 *  When `integrationsV2` is true, collapses `channels`/`connectors`/`extensions`/`sources` into `integrations` and remaps
 *  their indexed groups so quick search jumps directly to the unified Integrations & Extensions view. */
export function searchSettings(query: string, integrationsV2 = false, hasInboxSources?: boolean): { tabs: Tab[]; groups: SearchGroup[] } {
  const q = fold(query);
  if (!q) return { tabs: [], groups: [] };
  const featureEntries = moduleSwitchEntries();
  const [features] = featureEntries;
  // The exact group name and its short alias both open General. The broader category hints can also contain "features".
  if (features && [features.group, features.hint].some((label) => label && fold(label) === q)) {
    return { tabs: [], groups: [features] };
  }
  const has = (...texts: (string | undefined)[]) => texts.some((s) => s && fold(s).includes(q));
  const unifiedVisible = INTEGRATIONS_SETTINGS_REGISTRATION.isVisible({ integrationsV2 });
  const searchableTabs = [...TABS, ...enabledSettingsTabs().map((m) => m.key)].filter((k) =>
    unifiedVisible ? !isLegacyIntegrationTab(k) : k !== INTEGRATIONS_SETTINGS_REGISTRATION.id,
  );
  const tabMatches = (k: Tab) => {
    const info = tabInfo(k);
    if (has(info.label, info.hint)) return true;
    // When integrations_v2 is enabled, searching legacy tab names ("Channels", "Connectors", "Extensions", "Inbox Sources") still finds Integrations.
    if (unifiedVisible && k === INTEGRATIONS_SETTINGS_REGISTRATION.id) {
      return has(...INTEGRATIONS_SETTINGS_REGISTRATION.searchKeywords());
    }
    return false;
  };
  const showSources = integrationsV2 && (hasInboxSources ?? getState().features.includes(INBOX_SOURCES_FEATURE));
  const indexed = integrationsV2 ? UNIFIED_INDEX.filter((g) => {
    if (g.sub === "-sources") return showSources;
    if (g.tab === "persona" && g.anchor === "inbox-todo-forward") return !showSources;
    return true;
  }) : SEARCH_INDEX;
  const showMailbox = mailReading().kind === "full";
  return {
    tabs: searchableTabs.filter(tabMatches),
    groups: [...indexed, ...featureEntries].filter((g) => (g.anchor !== "mail" || showMailbox) && has(g.group, g.hint)),
  };
}
