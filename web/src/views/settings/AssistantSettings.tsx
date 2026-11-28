// Settings › Assistant (docs/assistant/assistant.md "Profile and avatar", the iOS app's AssistantProfileView / AssistantNotesView /
// AssistantMemoryView, Android's assistant settings): what Persona does on its own (a line into "Scheduled tasks"), what it knows about you
// ("About me" = `notes`, saved on "Save" or when leaving; "Memory" = its memory files: read, edit the whole file, delete), and where
// it runs (read-only here). Its model is Settings › Models › Main chat, not here (#1291).
import { Suspense, useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { t } from "../../i18n";
import { BASE } from "../../lib/base";
import { settingsHref } from "./nav";
import { integrationHref, integrationScope } from "./integrations-route";
import { SUPERVISE_PURPOSE } from "../../lib/background-supervisor";
import { api, credentialOwner, enc, errorText, subscribeCredentialOwner } from "../../lib/api";
import { MANAGER_HEADER, useKnownDefault } from "../../lib/manager-choice";
import { useInboxSourcesShown } from "../../lib/subscriptions";
import { mainRuntimeDetails, type MainExecution } from "../../lib/main-runtime";
import { PERMISSION_MODES } from "../../lib/session-actions";
import { ASSISTANT_PAGES_FEATURE } from "../../lib/extension-ui";
import { ConfigurationManager } from "./ConfigurationManager";
import { AgentAutoNaming, supportsAgentAutoNaming, type AgentAutoNamingState } from "./AgentAutoNaming";
import { aboutMeSaves } from "./about-me-saves";
import { ago } from "../../lib/format";
import { MEMORY_INDEX, memoryEntries, memoryTypeLabel, memoryTypeTone, parseMemory, type MemoryEntry, type MemoryFile } from "../../lib/memory";
import { navigate } from "../../lib/router";
import { momentLabel, upcoming, type Task } from "../../lib/routines";
import { refreshAssistant, useStore } from "../../lib/store";
import { Button, cx, Dialog, Loading, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Markdown } from "../../ui/Markdown";
import { Group, Pill, Row, settingsInputClass, Switch } from "../settings/SettingsParts";
import { SkillsGroup } from "./Skills";
import { lazyPage } from "../../lib/lazy-page";
import { Tag, textareaClass } from "../assistant/parts";
import { PersonaAvatar, usePersona } from "../assistant/Persona";
import { TaskTrust } from "./TaskTrust";
import { DirectionNotes, directionNotesDeps } from "./DirectionNotes";
import { CacheKeepaliveSettings, type CacheKeepaliveState } from "./CacheKeepaliveSettings";
import { PageBoundary } from "../PageBoundary";

const StandingAuthorizations = lazyPage(() => import("./StandingAuthorizations").then((m) => m.StandingAuthorizations));
const OutboundMessages = lazyPage(() => import("./OutboundMessages").then((m) => m.OutboundMessages));
const Repositories = lazyPage(() => import("./Repositories").then((m) => m.Repositories));
const WorkspaceNaming = lazyPage(() => import("../../ui/WorkspaceNaming").then((m) => m.WorkspaceNamingSettingsGroup));

interface AssistantState extends AgentAutoNamingState, CacheKeepaliveState {
  manager?: string;
  default_permission_mode?: string;
  readonly_mode?: "sandbox" | "prompt";
  machine_id?: string;
  /** The main chat and the node it is routed through (not where it runs: `MainExecution`'s location). */
  main?: string;
  main_machine_id?: string;
  launcher?: string;
  cwd?: string;
  timezone?: string;
  /** Only once saved here (or in the app); `notes_effective` is what Persona is told (hub.json's otherwise). */
  notes?: string;
  notes_effective?: string;
  /** "Notify main chat tasks in the main chat only" (#1731): on by default; tasks opened from now on follow it. */
  main_tasks_quiet?: boolean;
  /** Show non-private approvals from other Assistant chats in the main chat too; on by default for new cards. */
  approval_mirror_main?: boolean;
  /** "Start what I asked for in the chat right away" (#2559): on by default; tasks the user asked for start without the plan card's "Start". */
  start_requested_tasks?: boolean;
  workspace_naming_version?: string;
  task_workspace_preference?: "prefer-existing" | "prefer-new";
  /** "Fold background rounds" (#4314, docs/assistant/assistant.md "Background rounds"): on by default; unset from a hub before it (on). */
  main_background_folded?: boolean;
  fold_routine_relay_replies?: boolean;
  /** "Show scheduled task chats" in the sidebar's Recent (#8324): off by default, one value for all of the user's
   *  devices; unset from a hub before it (no switch). All chats' ⋯ menu has the same switch (lib/recent-task-chats.ts). */
  recent_task_chats?: boolean;
  /** The role a meeting recording pre-selects (#2981, docs/assistant/meeting-minutes.md "Attendance fields"); only from a hub
   *  that attends meetings. */
  meeting_attendance_default?: string;
  /** How many days recordings stay (#3235), in effect; the phone drops its own older ones too. From a hub with
   *  `recording_audio_days` it is that value's old name. */
  recording_days?: number;
  /** How many days recording audio is kept (#16412), in effect: 0 means forever; the phone drops its own older recordings. */
  recording_audio_days?: number;
  /** How many days a recording's text stays in the knowledge base (#16412), in effect: 0 means forever (the default).
   *  Unset from a hub before it, whose `recording_days` cuts the text too. */
  recording_text_days?: number;
  /** How many days a meeting's minutes stay once its transcript went (#3780), in effect: 0 means forever; finite windows
   *  never below the text's days (`recording_days` from a hub before #16412). */
  minutes_days?: number;
  /** "Follow up every agent Persona starts" (#3617, docs/assistant/unified-entry.md "Agent tasks"): off by default; unset
   *  from a hub before it. */
  agent_tasks?: boolean;
  /** "Learn from finished tasks" (#12295, docs/assistant/todos.md "Lessons from accepted tasks"): off by default;
   *  unset from a hub before it. */
  task_lessons?: boolean;
  close_ended_terminals?: boolean;
  /** "Tell supervisors about new messages" (#4293, docs/assistant/unified-entry.md "Supervision (#3124)"): off
   *  by default; unset from a hub before it. */
  inbox_signals?: boolean;
  /** Deterministic opt-in auto-forwarding of PR/Issue Inbox events to the bound open to-do's Agent (#12492): off by
   *  default; unset from a hub before it. */
  inbox_todo_forward?: boolean;
  /** Unified Integrations & Extensions Labs trial (#14802): off by default; unset from a hub before it. */
  integrations_v2?: boolean;
  /** "The main chat supervises its own tasks" (#4927, #5082, docs/assistant/unified-entry.md "Background supervisor"):
   *  off by default, which means the built-in background supervisor follows up the tasks no leader supervises; unset
   *  from a hub before it. */
  main_supervises_own_tasks?: boolean;
  /** "Automatic supervision messages" as the older on/off switch (true unless the mode is off); a hub before
   *  `auto_supervision_mode` sends only this one, a hub before both neither. */
  auto_supervision_messages?: boolean;
  /** Which automatic supervision messages are sent (#16417, docs/assistant/unified-entry.md): "all" (default),
   *  "outcomes" (results and decisions, no routine check-ins) or "off"; unset from a hub before it. */
  auto_supervision_mode?: string;
  supervision_triage?: boolean;
  /** Three-tier PR/Issue delivery closeout gate (#12034): "off", "merged" (default), or "live"; unset from a hub before it. */
  delivery_gate?: string;
  /** Days a closed session stays listed before it archives itself (#3618, docs/assistant/todos.md "Closed sessions
   *  archive themselves"): 0 is never, at most 3650; unset from a hub before it. */
  archive_closed_days?: number;
  /** "Suggest the next message" (#4644, docs/assistant/assistant.md "Next-message suggestions"): off by default; unset
   *  from a hub before it. */
  next_suggestions?: boolean;
  /** "Let Persona make dashboards" (#15017): off by default (absent is off); the switch is shown only when the hub
   *  announces `assistant_pages`, and only for the default manager, which alone has Dashboards. */
  dashboards?: boolean;
  /** "Run Claude chats in interactive Claude Code" (#10058, docs/assistant/assistant.md "Interactive Claude Code"): off
   *  by default; unset from a hub before it. */
  claude_interactive?: boolean;
  /** Exact owner-reply approval and reconstructable proposal retention: both off by default; absent from an older hub. */
  owner_reply_approval?: boolean;
  persistent_owner_action_cards?: boolean;
  owner_reply_approval_phrases?: string[];
  /** Hours an idle Agent Persona started runs before Persona stops it (#4754, docs/integrations/engines.md "Agents run
   *  in a terminal"); its terminal stays. 0 is never, at most 8760; unset from a hub before it. */
  idle_close_hours?: number;
}

/** `archive_closed_days`' upper bound, the hub's (assistant/profile_write.go). */
const MAX_ARCHIVE_DAYS = 3650;
/** `idle_close_hours`' upper bound, the hub's (assistant/tasks_idle.go). */
const MAX_IDLE_HOURS = 8760;

/** `meeting_attendance_default`'s values, in the picker's order. */
const MEETING_ROLES = ["off", "listener", "participant"] as const;
/** `delivery_gate`'s values, in the picker's order. */
const DELIVERY_GATES = ["off", "merged", "live"] as const;
/** `auto_supervision_mode`'s values, in the picker's order; a hub that has only the older switch offers the first and last. */
const SUPERVISION_MODES = ["all", "outcomes", "off"] as const;
const LEGACY_SUPERVISION_MODES = ["all", "off"] as const;

/** The automatic supervision mode a hub answered: its mode, else its older switch read as all / off, else none. */
function supervisionModeOf(info: Pick<AssistantState, "auto_supervision_mode" | "auto_supervision_messages"> | null | undefined): string | undefined {
  if (typeof info?.auto_supervision_mode === "string") return info.auto_supervision_mode;
  if (typeof info?.auto_supervision_messages === "boolean") return info.auto_supervision_messages ? "all" : "off";
  return undefined;
}

/** Finite day bounds (docs/protocol/protocol.md). All but the old `recording_days` also accept 0, selected as Forever
 *  rather than typed. */
const RETENTION = {
  recording_days: { min: 1, max: 365 },
  recording_audio_days: { min: 1, max: 3650 },
  recording_text_days: { min: 1, max: 3650 },
  minutes_days: { min: 1, max: 3650 },
} as const;
type Retention = keyof typeof RETENTION;

export function AssistantSettings({ bare, manager }: { /** Without its header (the profile page has its own). */ bare?: boolean; manager?: string } = {}) {
  const owner = useSyncExternalStore(subscribeCredentialOwner, credentialOwner);
  return <ConfigurationManager manager={manager}>{(target) => <AssistantSettingsForManager key={JSON.stringify([target, owner])} manager={target} owner={owner} bare={bare} />}</ConfigurationManager>;
}

function AssistantSettingsForManager({ manager, owner, bare }: { manager: string; owner: string; bare?: boolean }) {
  const [outboundRevision, setOutboundRevision] = useState(0);
  const outboundChanged = useCallback(() => setOutboundRevision(n => n + 1), []);
  const [notesSaves] = useState(() => aboutMeSaves(manager, owner));
  const scopedApi: typeof api = (path, init) => api(path, { ...init, headers: { ...Object.fromEntries(new Headers(init?.headers)), [MANAGER_HEADER]: manager } });
  const [directionNotes] = useState(() => directionNotesDeps(scopedApi));
  const persona = usePersona();
  const defaultId = useKnownDefault();
  const hasInboxSources = useInboxSourcesShown() === true;
  const machines = useStore((s) => s.machines);
  const [info, setInfo] = useState<AssistantState | null>(null);
  // where and with what the main chat runs (docs/assistant/assistant.md "Main conversation execution on the web"): kept
  // here, out of the Assistant screen's line (audit item 80); nothing when the hub does not report it
  const [execution, setExecution] = useState<MainExecution | null>(null);
  const [error, setError] = useState("");
  const [tasks, setTasks] = useState<Task[] | null>(null);
  const [files, setFiles] = useState<MemoryFile[] | null>(null);
  const [memoryError, setMemoryError] = useState("");
  const [open, setOpen] = useState<{ file: MemoryFile; index?: boolean } | null>(null);
  const [quietBusy, setQuietBusy] = useState(false);
  const [foldBusy, setFoldBusy] = useState(false);
  const [relayBusy, setRelayBusy] = useState(false);
  const [recentBusy, setRecentBusy] = useState(false);
  const [startBusy, setStartBusy] = useState(false);
  const [permissionBusy, setPermissionBusy] = useState(false);
  const [readonlyBusy, setReadonlyBusy] = useState(false);
  const [workspaceBusy, setWorkspaceBusy] = useState(false);
  const setWorkspacePreference = async (value: "prefer-existing" | "prefer-new") => {
    setWorkspaceBusy(true);
    try {
      const r = await scopedApi<AssistantState>("/assistant", { method: "PUT", json: { task_workspace_preference: value } });
      if (r.manager !== manager || r.task_workspace_preference !== value) throw new Error(t("settings-assistantsettings.workspace_unconfirmed"));
      setInfo((i) => i ? { ...i, task_workspace_preference: r.task_workspace_preference } : i);
    } catch (e) {
      toast(t("settings-assistantsettings.save_failed", { error: errorText(e) }), "error");
    } finally { setWorkspaceBusy(false); }
  };
  const setPermission = async (mode: string) => {
    setPermissionBusy(true);
    try {
      const r = await scopedApi<AssistantState>("/assistant", { method: "PUT", json: { default_permission_mode: mode } });
      if (r.manager !== manager || r.default_permission_mode !== mode) throw new Error(t("settings-assistantsettings.default_permissions_unconfirmed"));
      setInfo((i) => i ? { ...i, default_permission_mode: r.default_permission_mode } : i);
    } catch (e) {
      toast(t("settings-assistantsettings.save_failed", { error: errorText(e) }), "error");
    } finally { setPermissionBusy(false); }
  };
  const setReadonlyMode = async (mode: "sandbox" | "prompt") => {
    setReadonlyBusy(true);
    try {
      const r = await scopedApi<AssistantState>("/assistant", { method: "PUT", json: { readonly_mode: mode } });
      if (r.manager !== manager || r.readonly_mode !== mode) throw new Error(t("settings-assistantsettings.readonly_mode_unconfirmed"));
      setInfo((i) => i ? { ...i, readonly_mode: r.readonly_mode } : i);
    } catch (e) {
      toast(t("settings-assistantsettings.save_failed", { error: errorText(e) }), "error");
    } finally { setReadonlyBusy(false); }
  };
  const [deliveryBusy, setDeliveryBusy] = useState(false);
  const setDeliveryGate = async (gate: string) => {
    setDeliveryBusy(true);
    try {
      const r = await scopedApi<AssistantState>("/assistant", { method: "PUT", json: { delivery_gate: gate } });
      if (r.manager !== manager || r.delivery_gate !== gate) throw new Error(t("settings-assistantsettings.delivery_gate_unconfirmed"));
      setInfo((i) => i ? { ...i, delivery_gate: r.delivery_gate } : i);
    } catch (e) {
      toast(t("settings-assistantsettings.save_failed", { error: errorText(e) }), "error");
    } finally { setDeliveryBusy(false); }
  };

  const [supervisionBusy, setSupervisionBusy] = useState(false);
  const supervisionMode = supervisionModeOf(info);
  const supervisionChoices: readonly string[] = typeof info?.auto_supervision_mode === "string" ? SUPERVISION_MODES : LEGACY_SUPERVISION_MODES;
  // A hub with the mode takes the mode; one with only the older switch takes the switch (all / off).
  const setSupervisionMode = async (mode: string) => {
    setSupervisionBusy(true);
    try {
      const body = typeof info?.auto_supervision_mode === "string" ? { auto_supervision_mode: mode } : { auto_supervision_messages: mode !== "off" };
      const r = await scopedApi<AssistantState>("/assistant", { method: "PUT", json: body });
      if (r.manager !== manager || supervisionModeOf(r) !== mode) throw new Error(t("settings-assistantsettings.auto_supervision_mode_unconfirmed"));
      setInfo((i) => i ? { ...i, auto_supervision_mode: r.auto_supervision_mode, auto_supervision_messages: r.auto_supervision_messages } : i);
    } catch (e) {
      toast(t("settings-assistantsettings.save_failed", { error: errorText(e) }), "error");
    } finally { setSupervisionBusy(false); }
  };

  const [supervisionTriageBusy, setSupervisionTriageBusy] = useState(false);
  const setSupervisionTriage = async (on: boolean) => {
    setSupervisionTriageBusy(true);
    try {
      const r = await scopedApi<AssistantState>("/assistant", { method: "PUT", json: { supervision_triage: on } });
      if (r.manager !== manager || r.supervision_triage !== on) throw new Error(t("settings-assistantsettings.supervision_triage_unconfirmed"));
      setInfo((i) => i ? { ...i, supervision_triage: r.supervision_triage } : i);
    } catch (e) {
      toast(t("settings-assistantsettings.save_failed", { error: errorText(e) }), "error");
    } finally { setSupervisionTriageBusy(false); }
  };

  const [meetingBusy, setMeetingBusy] = useState(false);
  const setMeetingRole = async (role: string) => {
    setMeetingBusy(true);
    try {
      const r = await scopedApi<AssistantState>("/assistant", { method: "PUT", json: { meeting_attendance_default: role } });
      if (r.manager !== manager || r.meeting_attendance_default !== role) throw new Error(t("settings-assistantsettings.meeting_attendance_unconfirmed"));
      setInfo((i) => i ? { ...i, meeting_attendance_default: r.meeting_attendance_default } : i);
    } catch (e) {
      toast(t("settings-assistantsettings.save_failed", { error: errorText(e) }), "error");
    } finally { setMeetingBusy(false); }
  };

  const [retentionBusy, setRetentionBusy] = useState(false);
  const [retentionTries, setRetentionTries] = useState(0);
  // Confirmed by the echo: the hub answers the days in effect (minutes never fewer than the recordings' own).
  const setRetention = async (field: Retention, days: number) => {
    setRetentionBusy(true);
    try {
      const r = await scopedApi<AssistantState>("/assistant", { method: "PUT", json: { [field]: days } });
      // Minutes never go before the text: forever while it is kept forever, else at least its days.
      const text = r.recording_text_days ?? r.recording_days;
      const want = field === "minutes_days" && days !== 0 ? (text === 0 ? 0 : Math.max(days, text ?? days)) : days;
      if (r.manager !== manager || r[field] !== want) throw new Error(t("settings-assistantsettings.retention_unconfirmed"));
      setInfo((i) => i ? { ...i, recording_days: r.recording_days, recording_audio_days: r.recording_audio_days,
        recording_text_days: r.recording_text_days, minutes_days: r.minutes_days } : i);
    } catch (e) {
      toast(t("settings-assistantsettings.save_failed", { error: errorText(e) }), "error");
    } finally { setRetentionBusy(false); setRetentionTries((n) => n + 1); }
  };

  const [suggestBusy, setSuggestBusy] = useState(false);
  const setSuggestions = async (on: boolean) => {
    setSuggestBusy(true);
    setInfo((i) => (i ? { ...i, next_suggestions: on } : i));
    try {
      const r = await scopedApi<AssistantState>("/assistant", { method: "PUT", json: { next_suggestions: on } });
      setInfo((i) => (i ? { ...i, next_suggestions: r.next_suggestions ?? on } : i));
    } catch (e) {
      setInfo((i) => (i ? { ...i, next_suggestions: !on } : i));
      toast(t("settings-assistantsettings.save_failed", { error: errorText(e) }), "error");
    } finally {
      setSuggestBusy(false);
    }
  };

  // Dashboards (#15017): only the owner changes it, the hub confirms the value shown
  const pagesAnnounced = useStore((s) => s.features.includes(ASSISTANT_PAGES_FEATURE));
  const [dashboardsBusy, setDashboardsBusy] = useState(false);
  const setDashboards = async (on: boolean) => {
    setDashboardsBusy(true);
    setInfo((i) => (i ? { ...i, dashboards: on } : i));
    try {
      const r = await scopedApi<AssistantState>("/assistant", { method: "PUT", json: { dashboards: on } });
      setInfo((i) => (i ? { ...i, dashboards: r.dashboards === true } : i));
    } catch (e) {
      setInfo((i) => (i ? { ...i, dashboards: !on } : i));
      toast(t("settings-assistantsettings.save_failed", { error: errorText(e) }), "error");
    } finally {
      setDashboardsBusy(false);
    }
  };

  const [interactiveBusy, setInteractiveBusy] = useState(false);
  const setInteractive = async (on: boolean) => {
    setInteractiveBusy(true);
    setInfo((i) => (i ? { ...i, claude_interactive: on } : i));
    try {
      const r = await scopedApi<AssistantState>("/assistant", { method: "PUT", json: { claude_interactive: on } });
      setInfo((i) => (i ? { ...i, claude_interactive: r.claude_interactive ?? on } : i));
    } catch (e) {
      setInfo((i) => (i ? { ...i, claude_interactive: !on } : i));
      toast(t("settings-assistantsettings.save_failed", { error: errorText(e) }), "error");
    } finally {
      setInteractiveBusy(false);
    }
  };

  const [ownerApprovalBusy, setOwnerApprovalBusy] = useState(false);
  const setOwnerApproval = async (field: "owner_reply_approval" | "persistent_owner_action_cards", on: boolean) => {
    const before = info;
    setOwnerApprovalBusy(true);
    setInfo((i) => i ? { ...i, [field]: on } : i);
    try {
      const r = await scopedApi<AssistantState>("/assistant", { method: "PUT", json: { [field]: on } });
      if (r.manager !== manager || r[field] !== on) throw new Error(t("settings-assistantsettings.owner_approval_unconfirmed"));
      setInfo((i) => i ? { ...i, [field]: r[field] } : i);
    } catch (e) {
      setInfo((i) => i && before ? { ...i, [field]: before[field] } : i);
      toast(t("settings-assistantsettings.save_failed", { error: errorText(e) }), "error");
    } finally { setOwnerApprovalBusy(false); }
  };
  const setOwnerApprovalPhrases = async (phrases: string[]) => {
    setOwnerApprovalBusy(true);
    try {
      const r = await scopedApi<AssistantState>("/assistant", { method: "PUT", json: { owner_reply_approval_phrases: phrases } });
      if (r.manager !== manager || JSON.stringify(r.owner_reply_approval_phrases) !== JSON.stringify(phrases)) {
        throw new Error(t("settings-assistantsettings.owner_approval_unconfirmed"));
      }
      setInfo((i) => i ? { ...i, owner_reply_approval_phrases: r.owner_reply_approval_phrases } : i);
    } catch (e) {
      toast(t("settings-assistantsettings.save_failed", { error: errorText(e) }), "error");
      throw e;
    } finally { setOwnerApprovalBusy(false); }
  };

  const setFolded = async (on: boolean) => {
    setFoldBusy(true);
    setInfo((i) => (i ? { ...i, main_background_folded: on } : i));
    try {
      const r = await scopedApi<AssistantState>("/assistant", { method: "PUT", json: { main_background_folded: on } });
      setInfo((i) => (i ? { ...i, main_background_folded: r.main_background_folded ?? on } : i));
      // the main conversation reads it from the store's /assistant answer
      void refreshAssistant().catch(() => {});
    } catch (e) {
      setInfo((i) => (i ? { ...i, main_background_folded: !on } : i));
      toast(t("settings-assistantsettings.save_failed", { error: errorText(e) }), "error");
    } finally {
      setFoldBusy(false);
    }
  };

  const setRelayReplies = async (on: boolean) => {
    setRelayBusy(true);
    try {
      const r = await scopedApi<AssistantState>("/assistant", { method: "PUT", json: { fold_routine_relay_replies: on } });
      if (r.manager !== manager || r.fold_routine_relay_replies !== on) throw new Error(t("settings-assistantsettings.relay_unconfirmed"));
      setInfo((i) => i ? { ...i, fold_routine_relay_replies: on } : i);
      void refreshAssistant().catch(() => {});
    } catch (e) {
      toast(t("settings-assistantsettings.save_failed", { error: errorText(e) }), "error");
    } finally { setRelayBusy(false); }
  };

  const setRecentTaskChats = async (on: boolean) => {
    setRecentBusy(true);
    setInfo((i) => (i ? { ...i, recent_task_chats: on } : i));
    try {
      const r = await scopedApi<AssistantState>("/assistant", { method: "PUT", json: { recent_task_chats: on } });
      setInfo((i) => (i ? { ...i, recent_task_chats: r.recent_task_chats ?? on } : i));
      // the sidebar reads it from the store's /assistant answer
      void refreshAssistant().catch(() => {});
    } catch (e) {
      setInfo((i) => (i ? { ...i, recent_task_chats: !on } : i));
      toast(t("settings-assistantsettings.save_failed", { error: errorText(e) }), "error");
    } finally {
      setRecentBusy(false);
    }
  };

  const setQuiet = async (on: boolean) => {
    setQuietBusy(true);
    setInfo((i) => (i ? { ...i, main_tasks_quiet: on } : i));
    try {
      const r = await scopedApi<AssistantState>("/assistant", { method: "PUT", json: { main_tasks_quiet: on } });
      setInfo((i) => (i ? { ...i, main_tasks_quiet: r.main_tasks_quiet ?? on } : i));
    } catch (e) {
      setInfo((i) => (i ? { ...i, main_tasks_quiet: !on } : i));
      toast(t("settings-assistantsettings.save_failed", { error: errorText(e) }), "error");
    } finally {
      setQuietBusy(false);
    }
  };

  const [mirrorBusy, setMirrorBusy] = useState(false);
  const setApprovalMirror = async (on: boolean) => {
    setMirrorBusy(true);
    setInfo((i) => (i ? { ...i, approval_mirror_main: on } : i));
    try {
      const r = await scopedApi<AssistantState>("/assistant", { method: "PUT", json: { approval_mirror_main: on } });
      setInfo((i) => (i ? { ...i, approval_mirror_main: r.approval_mirror_main ?? on } : i));
    } catch (e) {
      setInfo((i) => (i ? { ...i, approval_mirror_main: !on } : i));
      toast(t("settings-assistantsettings.save_failed", { error: errorText(e) }), "error");
    } finally {
      setMirrorBusy(false);
    }
  };

  const [lessonsBusy, setLessonsBusy] = useState(false);
  // "Learn from finished tasks" (#12295): shown as chosen at once, taken back with the reason when the hub refuses.
  const setTaskLessons = async (on: boolean) => {
    setLessonsBusy(true);
    setInfo((i) => (i ? { ...i, task_lessons: on } : i));
    try {
      const r = await scopedApi<AssistantState>("/assistant", { method: "PUT", json: { task_lessons: on } });
      setInfo((i) => (i ? { ...i, task_lessons: r.task_lessons ?? on } : i));
    } catch (e) {
      setInfo((i) => (i ? { ...i, task_lessons: !on } : i));
      toast(t("settings-assistantsettings.save_failed", { error: errorText(e) }), "error");
    } finally {
      setLessonsBusy(false);
    }
  };

  const [agentsBusy, setAgentsBusy] = useState(false);
  // One field of the Agents group (#3617, #3618, #4293, #5082, #12492): shown as chosen at once, taken back with the reason when the hub refuses.
  const setAgents = async (patch: { agent_tasks: boolean } | { close_ended_terminals: boolean } | { inbox_signals: boolean } | { inbox_todo_forward: boolean } | { main_supervises_own_tasks: boolean } | { archive_closed_days: number } | { idle_close_hours: number }) => {
    const before = info;
    setAgentsBusy(true);
    setInfo((i) => (i ? { ...i, ...patch } : i));
    try {
      const r = await scopedApi<AssistantState>("/assistant", { method: "PUT", json: patch });
      // what the hub saved, when it says
      setInfo((i) => (i ? { ...i, agent_tasks: r.agent_tasks ?? i.agent_tasks, close_ended_terminals: r.close_ended_terminals ?? i.close_ended_terminals, inbox_signals: r.inbox_signals ?? i.inbox_signals, inbox_todo_forward: r.inbox_todo_forward ?? i.inbox_todo_forward, main_supervises_own_tasks: r.main_supervises_own_tasks ?? i.main_supervises_own_tasks, archive_closed_days: r.archive_closed_days ?? i.archive_closed_days, idle_close_hours: r.idle_close_hours ?? i.idle_close_hours } : i));
    } catch (e) {
      setInfo((i) => (i && before ? { ...i, agent_tasks: before.agent_tasks, close_ended_terminals: before.close_ended_terminals, inbox_signals: before.inbox_signals, inbox_todo_forward: before.inbox_todo_forward, main_supervises_own_tasks: before.main_supervises_own_tasks, archive_closed_days: before.archive_closed_days, idle_close_hours: before.idle_close_hours } : i));
      toast(t("settings-assistantsettings.save_failed", { error: errorText(e) }), "error");
    } finally {
      setAgentsBusy(false);
    }
  };

  const setStartRequested = async (on: boolean) => {
    setStartBusy(true);
    setInfo((i) => (i ? { ...i, start_requested_tasks: on } : i));
    try {
      const r = await scopedApi<AssistantState>("/assistant", { method: "PUT", json: { start_requested_tasks: on } });
      setInfo((i) => (i ? { ...i, start_requested_tasks: r.start_requested_tasks ?? on } : i));
    } catch (e) {
      setInfo((i) => (i ? { ...i, start_requested_tasks: !on } : i));
      toast(t("settings-assistantsettings.save_failed", { error: errorText(e) }), "error");
    } finally {
      setStartBusy(false);
    }
  };

  const loadMemory = async () => {
    try {
      const r = await scopedApi<{ files: MemoryFile[]; dir?: string }>("/assistant/memory");
      setFiles(r.files ?? []);
      setMemoryError("");
    } catch (e) {
      setMemoryError(errorText(e));
      setFiles((f) => f ?? []);
    }
  };

  const load = () => {
    setError("");
    scopedApi<AssistantState>("/assistant").then(setInfo, (e) => setError(errorText(e)));
    scopedApi<MainExecution>("/assistant?view=main-runtime").then(setExecution, () => setExecution(null));
    scopedApi<{ tasks: Task[] }>("/assistant/tasks").then((r) => setTasks(r.tasks ?? []), () => setTasks([]));
    void loadMemory();
  };
  useEffect(load, []);

  const machine = machines.find((m) => m.id === info?.machine_id);
  const machineName = machine?.name || info?.machine_id || "";
  // a report routed through another node than the main chat's is not this main chat's (MainRuntimeBadge's rule)
  const routed = info?.main_machine_id && execution?.machine_id && execution.machine_id !== info.main_machine_id;
  const runtime = routed ? {} : mainRuntimeDetails(info?.main, execution ?? undefined);
  const entries = useMemo(() => memoryEntries(files ?? []), [files]);
  const index = files?.find((f) => f.name === MEMORY_INDEX);
  const on = tasks?.filter((task) => task.enabled).length ?? 0;
  const next = tasks ? upcoming(tasks, 1)[0] : undefined;
  const deduplicateInboxForward = info?.integrations_v2 === true && hasInboxSources;
  const inboxForwardSourcesHref = integrationHref({ scope: integrationScope(manager), view: { kind: "data-flow" }, filter: "all" });

  if (!info && !error) return <Loading />;

  return (
    <>
      {!bare && <div className="mb-6 flex items-center gap-3 px-1">
        <PersonaAvatar
          size={44}
          fallback={
            <span className="flex size-11 shrink-0 items-center justify-center rounded-full bg-ink text-surface">
              <Icon name="spark" size={18} />
            </span>
          }
        />
        <div className="min-w-0 flex-1">
          <div className="text-[15px] font-semibold text-ink">{persona.name}</div>
          <div className="truncate text-[12px] text-ink-3">{machineName ? t("settings-assistantsettings.subtitle_on", { machine: machineName }) : t("settings-assistantsettings.subtitle")}</div>
        </div>
        {machine && <Pill tone={machine.online ? "green" : undefined}>{machine.online ? t("settings-assistantsettings.online") : t("settings-assistantsettings.offline")}</Pill>}
      </div>}
      {error && <div className="mb-4 flex items-center gap-3">
        <p role="alert" className="min-w-0 flex-1 text-[13px] break-words text-red">{error}</p>
        <Button icon="retry" onClick={load}>{t("settings-assistantsettings.retry")}</Button>
      </div>}

      <Group title={t("settings-assistantsettings.proactive_title")} footer={t("settings-assistantsettings.proactive_footer")}>
        <Row
          anchor="routines"
          label={t("settings-assistantsettings.routines")}
          hint={tasks === null ? undefined : next ? t("settings-assistantsettings.next_task", { when: momentLabel(next.at, new Date()), title: next.title }) : t("settings-assistantsettings.nothing_scheduled")}
          onClick={() => navigate({ name: "routines" })}
        >
          {tasks !== null && <span>{on === 0 ? t("settings-assistantsettings.routines_off") : t("settings-assistantsettings.routines_on", { count: on })}</span>}
        </Row>
      </Group>

      <Group title={t("settings-assistantsettings.notifications_title")} footer={t("settings-assistantsettings.notifications_footer")}>
        <Row label={t("settings-assistantsettings.quiet_tasks")} anchor="quiet-tasks">
          <Switch on={info?.main_tasks_quiet ?? true} onChange={(on) => void setQuiet(on)} disabled={!info || quietBusy} label={t("settings-assistantsettings.quiet_tasks")} />
        </Row>
      </Group>

      <Group footer={t("settings-assistantsettings.background_folded_footer")} anchor="background-folded">
        <Row label={t("settings-assistantsettings.background_folded")}>
          <Switch on={info?.main_background_folded ?? true} onChange={(on) => void setFolded(on)} disabled={!info || foldBusy} label={t("settings-assistantsettings.background_folded")} />
        </Row>
      </Group>

      {typeof info?.fold_routine_relay_replies === "boolean" && (
        <Group footer={t("settings-assistantsettings.relay_replies_footer")} anchor="relay-replies">
          <Row label={t("settings-assistantsettings.relay_replies")}>
            <Switch on={info.fold_routine_relay_replies} onChange={(on) => void setRelayReplies(on)} disabled={relayBusy} label={t("settings-assistantsettings.relay_replies")} />
          </Row>
        </Group>
      )}

      {/* Show scheduled task chats in the sidebar's Recent (#8324): only from a hub that has the setting */}
      {typeof info?.recent_task_chats === "boolean" && (
        <Group title={t("settings-assistantsettings.recent_task_chats_title")} footer={t("settings-assistantsettings.recent_task_chats_footer")} anchor="recent-task-chats">
          <Row label={t("settings-assistantsettings.recent_task_chats")}>
            <Switch on={info.recent_task_chats} onChange={(on) => void setRecentTaskChats(on)} disabled={recentBusy} label={t("settings-assistantsettings.recent_task_chats")} />
          </Row>
        </Group>
      )}

      {/* The next-message suggestion for chats without one of their own (#4644): only from a hub that has the setting */}
      {typeof info?.next_suggestions === "boolean" && (
        <Group anchor="next-suggestions" footer={t("settings-assistantsettings.next_suggestions_footer")}>
          <Row label={t("settings-assistantsettings.next_suggestions")}>
            <Switch on={info.next_suggestions} onChange={(on) => void setSuggestions(on)} disabled={suggestBusy} label={t("settings-assistantsettings.next_suggestions")} />
          </Row>
        </Group>
      )}

      {/* Dashboards (#15017): only from a hub whose default manager has them, and only for that manager */}
      {pagesAnnounced && info && manager === defaultId && (
        <Group anchor="dashboards" footer={t("settings-assistantsettings.dashboards_footer")}>
          <Row label={t("settings-assistantsettings.dashboards")}>
            <Switch on={info.dashboards === true} onChange={(on) => void setDashboards(on)} disabled={dashboardsBusy} label={t("settings-assistantsettings.dashboards")} />
          </Row>
        </Group>
      )}

      <Group anchor="dispatch-tasks" title={t("settings-assistantsettings.start_title")} footer={t("settings-assistantsettings.start_footer")}>
        <Row label={t("settings-assistantsettings.start_requested")} anchor="start-requested">
          <Switch on={info?.start_requested_tasks ?? true} onChange={(on) => void setStartRequested(on)} disabled={!info || startBusy} label={t("settings-assistantsettings.start_requested")} />
        </Row>
        <Row label={t("settings-assistantsettings.workspace_preference")} hint={t("settings-assistantsettings.workspace_hint")} wrapHint stackOnNarrow anchor="workspace-preference">
          <select aria-label={t("settings-assistantsettings.workspace_preference")} value={info?.task_workspace_preference ?? "prefer-existing"}
            disabled={!info || info.manager !== manager || workspaceBusy}
            className="w-full min-w-0 max-w-full rounded-control bg-field px-3 py-2 text-[13px] text-ink shadow-inset-field sm:w-auto [@media(pointer:coarse)]:min-h-11"
            onChange={(e) => void setWorkspacePreference(e.target.value as "prefer-existing" | "prefer-new")}>
            <option value="prefer-existing">{t("settings-assistantsettings.workspace_prefer_existing")}</option>
            <option value="prefer-new">{t("settings-assistantsettings.workspace_prefer_new")}</option>
          </select>
        </Row>
        <TaskTrust manager={manager} machineName={(id) => machines.find((machine) => machine.id === id)?.name || id} />
      </Group>

      {info?.manager === manager && info.workspace_naming_version && (
        <div data-settings-group="workspace-naming">
          <PageBoundary page="workspace-naming">
            <Suspense fallback={<Loading />}>
              <WorkspaceNaming manager={manager} owner={owner} version={info.workspace_naming_version} />
            </Suspense>
          </PageBoundary>
        </div>
      )}

      {/* Who follows up the main chat's tasks (#5082), delivery closeout gate (#12034), following up agents, their tasks' new messages, stopping idle ones and tidying closed ones (#3617, #4293, #4754, #3618): each row only from a hub that has its setting */}
      {(supportsAgentAutoNaming(info) || typeof info?.main_supervises_own_tasks === "boolean" || typeof info?.delivery_gate === "string" || typeof info?.agent_tasks === "boolean" || typeof info?.close_ended_terminals === "boolean" || typeof info?.inbox_signals === "boolean" || supervisionMode !== undefined || typeof info?.supervision_triage === "boolean" || typeof info?.inbox_todo_forward === "boolean" || typeof info?.idle_close_hours === "number" || typeof info?.archive_closed_days === "number") && info && (
        <Group anchor="agents" title={t("settings-assistantsettings.agents_title")}>
          {supportsAgentAutoNaming(info) && <AgentAutoNaming info={info} manager={manager} owner={owner}
            onSaved={(patch) => setInfo((current) => current ? { ...current, ...patch } : current)} />}
          {typeof info.main_supervises_own_tasks === "boolean" && (
            <Row label={t("settings-assistantsettings.main_supervises")} hint={t("settings-assistantsettings.main_supervises_hint")} wrapHint>
              <Switch on={info.main_supervises_own_tasks} onChange={(on) => void setAgents({ main_supervises_own_tasks: on })} disabled={agentsBusy} label={t("settings-assistantsettings.main_supervises")} />
            </Row>
          )}
          {supervisionMode !== undefined && (
            <Row label={t("settings-assistantsettings.auto_supervision_mode")} hint={t("settings-assistantsettings.auto_supervision_mode_hint")} wrapHint stackOnNarrow anchor="auto-supervision-mode">
              <select aria-label={t("settings-assistantsettings.auto_supervision_mode")} value={supervisionMode}
                disabled={info.manager !== manager || supervisionBusy}
                className="min-w-0 max-w-full rounded-control bg-field px-3 py-2 text-meta text-ink shadow-inset-field pointer-coarse:min-h-11"
                onChange={(e) => void setSupervisionMode(e.target.value)}>
                {!supervisionChoices.includes(supervisionMode) && <option value={supervisionMode} disabled>{supervisionMode}</option>}
                {supervisionChoices.map((m) => <option key={m} value={m}>{t(`settings-assistantsettings.auto_supervision_mode_${m}`)}</option>)}
              </select>
            </Row>
          )}
          {typeof info.supervision_triage === "boolean" && (
            <Row label={t("settings-assistantsettings.supervision_triage")} hint={t("settings-assistantsettings.supervision_triage_hint")} wrapHint anchor="supervision-triage">
              <Switch on={info.supervision_triage} onChange={(on) => void setSupervisionTriage(on)} disabled={info.manager !== manager || supervisionTriageBusy} label={t("settings-assistantsettings.supervision_triage")} />
            </Row>
          )}
          {typeof info.delivery_gate === "string" && (
            <Row label={t("settings-assistantsettings.delivery_gate")} hint={t("settings-assistantsettings.delivery_gate_hint")} wrapHint stackOnNarrow anchor="delivery-gate">
              <select aria-label={t("settings-assistantsettings.delivery_gate")} value={info.delivery_gate}
                disabled={info.manager !== manager || deliveryBusy}
                className="min-w-0 max-w-full rounded-control bg-field px-3 py-2 text-meta text-ink shadow-inset-field pointer-coarse:min-h-11"
                onChange={(e) => void setDeliveryGate(e.target.value)}>
                {!(DELIVERY_GATES as readonly string[]).includes(info.delivery_gate) && <option value={info.delivery_gate} disabled>{info.delivery_gate}</option>}
                {DELIVERY_GATES.map((gate) => <option key={gate} value={gate}>{t(`settings-assistantsettings.delivery_gate_${gate}`)}</option>)}
              </select>
            </Row>
          )}
          {typeof info.agent_tasks === "boolean" && (
            <Row label={t("settings-assistantsettings.agent_tasks")} hint={t("settings-assistantsettings.agent_tasks_hint")} wrapHint>
              <Switch on={info.agent_tasks} onChange={(on) => void setAgents({ agent_tasks: on })} disabled={agentsBusy} label={t("settings-assistantsettings.agent_tasks")} />
            </Row>
          )}
          {/* closing an Agent's terminal when its session ends (#10355): off by default, only from a hub that has it */}
          {typeof info.close_ended_terminals === "boolean" && (
            <Row label={t("settings-assistantsettings.close_ended_terminals")} hint={t("settings-assistantsettings.close_ended_terminals_hint")} wrapHint>
              <Switch on={info.close_ended_terminals} onChange={(on) => void setAgents({ close_ended_terminals: on })} disabled={agentsBusy} label={t("settings-assistantsettings.close_ended_terminals")} />
            </Row>
          )}
          {typeof info.inbox_signals === "boolean" && (
            <Row label={t("settings-assistantsettings.inbox_signals")} hint={t("settings-assistantsettings.inbox_signals_hint")} wrapHint>
              <Switch on={info.inbox_signals} onChange={(on) => void setAgents({ inbox_signals: on })} disabled={agentsBusy} label={t("settings-assistantsettings.inbox_signals")} />
            </Row>
          )}
          {typeof info.inbox_todo_forward === "boolean" && (
            deduplicateInboxForward ? (
              <a
                data-settings-row="inbox-todo-forward"
                href={BASE + inboxForwardSourcesHref}
                onClick={(e) => {
                  if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) return;
                  e.preventDefault();
                  navigate(inboxForwardSourcesHref);
                }}
                className="flex items-center justify-between gap-3 px-4 py-2.5 text-left transition-colors hover:bg-hover pointer-coarse:min-h-11"
              >
                <div className="min-w-0 flex-1">
                  <div className="truncate text-meta text-ink">{t("settings-assistantsettings.inbox_todo_forward")}</div>
                  <div className="mt-0.5 text-submeta leading-snug text-ink-3">{t("settings-assistantsettings.inbox_todo_forward_moved_hint")}</div>
                </div>
                <div className="flex shrink-0 items-center gap-2 text-label text-ink-2">
                  <span>{t(info.inbox_todo_forward ? "settings-assistantsettings.inbox_todo_forward_on" : "settings-assistantsettings.inbox_todo_forward_off")}</span>
                  <Icon name="chevronRight" size={12} stroke={2} className="shrink-0 text-ink-3" />
                </div>
              </a>
            ) : (
              <Row anchor="inbox-todo-forward" label={t("settings-assistantsettings.inbox_todo_forward")} hint={t("settings-assistantsettings.inbox_todo_forward_hint")} wrapHint>
                <Switch on={info.inbox_todo_forward} onChange={(on) => void setAgents({ inbox_todo_forward: on })} disabled={agentsBusy} label={t("settings-assistantsettings.inbox_todo_forward")} />
              </Row>
            )
          )}
          {typeof info.idle_close_hours === "number" && (
            <Row label={t("settings-assistantsettings.idle_close")} hint={t("settings-assistantsettings.idle_close_hint")} wrapHint>
              <DaysField value={info.idle_close_hours} max={MAX_IDLE_HOURS} disabled={agentsBusy} label={t("settings-assistantsettings.idle_close")} onCommit={(hours) => void setAgents({ idle_close_hours: hours })} />
              <span className="whitespace-nowrap">{t("settings-assistantsettings.idle_close_unit")}</span>
            </Row>
          )}
          {typeof info.archive_closed_days === "number" && (
            <Row label={t("settings-assistantsettings.archive_closed")} hint={t("settings-assistantsettings.archive_closed_hint")} wrapHint>
              <DaysField value={info.archive_closed_days} disabled={agentsBusy} label={t("settings-assistantsettings.archive_closed")} onCommit={(days) => void setAgents({ archive_closed_days: days })} />
              <span className="whitespace-nowrap">{t("settings-assistantsettings.archive_closed_unit")}</span>
            </Row>
          )}
        </Group>
      )}

      <Group title={t("settings-nav.tab_model_config")}>
        <a href={BASE + settingsHref("models", "agent.new", false, null, "model-config")} className="flex items-center justify-between px-4 py-3 text-[13px] text-ink hover:bg-hover">
          <span>{t("settings-assistantsettings.task_models")}</span><Icon name="chevronRight" size={12} className="text-ink-3" />
        </a>
        <a href={BASE + settingsHref("models", "assistant", false, null, "model-config")} className="flex items-center justify-between px-4 py-3 text-[13px] text-ink hover:bg-hover">
          <span>{t("settings-assistantsettings.main_chat_model")}</span><Icon name="chevronRight" size={12} className="text-ink-3" />
        </a>
        {/* the background supervisor's own model (#5082): only from a hub that has it */}
        {typeof info?.main_supervises_own_tasks === "boolean" && (
          <a href={BASE + settingsHref("models", SUPERVISE_PURPOSE, false, null, "model-config")} className="flex items-center justify-between px-4 py-3 text-[13px] text-ink hover:bg-hover">
            <span>{t("settings-assistantsettings.background_supervisor_model")}</span><Icon name="chevronRight" size={12} className="text-ink-3" />
          </a>
        )}
      </Group>

      <Group title={t("settings-assistantsettings.default_permissions")} footer={t("settings-assistantsettings.default_permissions_hint")}>
        <Row label={t("settings-assistantsettings.default_permissions")} anchor="default-permissions">
          <select aria-label={t("settings-assistantsettings.default_permissions")} value={info?.default_permission_mode ?? ""} disabled={!info || info.manager !== manager || permissionBusy}
            className="min-w-0 max-w-full rounded-control bg-field px-3 py-2 text-[13px] text-ink shadow-inset-field"
            onChange={(e) => void setPermission(e.target.value)}>
            <option value="" disabled>{t("settings-assistantsettings.default_permissions_unset")}</option>
            {PERMISSION_MODES.map((mode) => <option key={mode.id} value={mode.id}>{mode.title}</option>)}
          </select>
        </Row>
        <Row label={t("settings-assistantsettings.readonly_mode")} hint={t("settings-assistantsettings.readonly_mode_hint")} wrapHint stackOnNarrow anchor="readonly-mode">
          <select aria-label={t("settings-assistantsettings.readonly_mode")} value={info?.readonly_mode ?? "sandbox"}
            disabled={!info || info.manager !== manager || readonlyBusy}
            className="min-w-0 max-w-full rounded-control bg-field px-3 py-2 text-meta text-ink shadow-inset-field pointer-coarse:min-h-11"
            onChange={(e) => void setReadonlyMode(e.target.value as "sandbox" | "prompt")}>
            <option value="sandbox">{t("settings-assistantsettings.readonly_mode_sandbox")}</option>
            <option value="prompt">{t("settings-assistantsettings.readonly_mode_prompt")}</option>
          </select>
        </Row>
      </Group>

      {typeof info?.approval_mirror_main === "boolean" && (
        <Group anchor="approval-routing">
          <Row label={t("settings-assistantsettings.approval_mirror_main")} hint={t("settings-assistantsettings.approval_mirror_main_hint")} wrapHint stackOnNarrow anchor="approval-mirror-main">
            <Switch on={info.approval_mirror_main} onChange={(on) => void setApprovalMirror(on)} disabled={mirrorBusy || info.manager !== manager} label={t("settings-assistantsettings.approval_mirror_main")} />
          </Row>
        </Group>
      )}

      <PageBoundary page="outbound-messages">
        <Suspense fallback={<Loading />}>
          <OutboundMessages manager={manager} changed={outboundChanged} />
        </Suspense>
      </PageBoundary>
      {typeof info?.owner_reply_approval === "boolean" && typeof info.persistent_owner_action_cards === "boolean" && Array.isArray(info.owner_reply_approval_phrases) && (
        <Group anchor="owner-reply-approval" title={t("settings-assistantsettings.owner_approval_title")} footer={t("settings-assistantsettings.owner_approval_footer")}>
          <Row label={t("settings-assistantsettings.owner_approval_switch")} hint={t("settings-assistantsettings.owner_approval_switch_hint")} wrapHint>
            <Switch on={info.owner_reply_approval} onChange={(on) => void setOwnerApproval("owner_reply_approval", on)} disabled={ownerApprovalBusy} label={t("settings-assistantsettings.owner_approval_switch")} />
          </Row>
          <Row label={t("settings-assistantsettings.owner_approval_persistent")} hint={t("settings-assistantsettings.owner_approval_persistent_hint")} wrapHint>
            <Switch on={info.persistent_owner_action_cards} onChange={(on) => void setOwnerApproval("persistent_owner_action_cards", on)} disabled={ownerApprovalBusy} label={t("settings-assistantsettings.owner_approval_persistent")} />
          </Row>
          <OwnerApprovalPhrasesField value={info.owner_reply_approval_phrases} disabled={ownerApprovalBusy} onSave={setOwnerApprovalPhrases} />
        </Group>
      )}

      <PageBoundary page="standing-authorizations">
        <Suspense fallback={<Group anchor="standing-authorizations" title={t("settings-standing.title")}><Loading /></Group>}>
          <StandingAuthorizations key={outboundRevision} manager={manager} />
        </Suspense>
      </PageBoundary>

      {typeof info?.meeting_attendance_default === "string" && (
        <Group anchor="meetings" title={t("settings-assistantsettings.meetings_title")} footer={t("settings-assistantsettings.meeting_attendance_footer")}>
          <Row label={t("settings-assistantsettings.meeting_attendance")}>
            <select aria-label={t("settings-assistantsettings.meeting_attendance")} value={info.meeting_attendance_default} disabled={info.manager !== manager || meetingBusy}
              className="min-w-0 max-w-full rounded-control bg-field px-3 py-2 text-[13px] text-ink shadow-inset-field"
              onChange={(e) => void setMeetingRole(e.target.value)}>
              {/* A role this page does not know yet stays shown as it is, not as the first option. */}
              {!(MEETING_ROLES as readonly string[]).includes(info.meeting_attendance_default) && <option value={info.meeting_attendance_default} disabled>{info.meeting_attendance_default}</option>}
              {MEETING_ROLES.map((role) => <option key={role} value={role}>{t(`settings-assistantsettings.meeting_attendance_${role}`)}</option>)}
            </select>
          </Row>
        </Group>
      )}

      {typeof info?.recording_text_days === "number" && typeof info.recording_audio_days === "number" ? (
        // A hub with separate audio and text retentions (#16412).
        <Group anchor="recordings" title={t("settings-assistantsettings.recordings_title")} footer={t("settings-assistantsettings.recordings_footer_split")}>
          <DaysRow field="recording_audio_days" label={t("settings-assistantsettings.recording_audio_days")} value={info.recording_audio_days} tries={retentionTries}
            disabled={info.manager !== manager || retentionBusy} onSave={(days) => void setRetention("recording_audio_days", days)} />
          <DaysRow field="recording_text_days" label={t("settings-assistantsettings.recording_text_days")} value={info.recording_text_days} tries={retentionTries}
            disabled={info.manager !== manager || retentionBusy} onSave={(days) => void setRetention("recording_text_days", days)} />
          {typeof info.minutes_days === "number" && <DaysRow field="minutes_days" label={t("settings-assistantsettings.minutes_days")} value={info.minutes_days} tries={retentionTries}
            disabled={info.manager !== manager || retentionBusy} onSave={(days) => void setRetention("minutes_days", days)} />}
        </Group>
      ) : typeof info?.recording_days === "number" && (
        // A manager from before minutes_days (#3780) deletes a recording's minutes with it: only one that says the minutes'
        // days promises they stay longer.
        <Group anchor="recordings" title={t("settings-assistantsettings.recordings_title")}
          footer={t(typeof info.minutes_days === "number" ? "settings-assistantsettings.recordings_footer" : "settings-assistantsettings.recordings_footer_with_minutes")}>
          <DaysRow field="recording_days" label={t("settings-assistantsettings.recording_days")} value={info.recording_days} tries={retentionTries}
            disabled={info.manager !== manager || retentionBusy} onSave={(days) => void setRetention("recording_days", days)} />
          {typeof info.minutes_days === "number" && <DaysRow field="minutes_days" label={t("settings-assistantsettings.minutes_days")} value={info.minutes_days} tries={retentionTries}
            disabled={info.manager !== manager || retentionBusy} onSave={(days) => void setRetention("minutes_days", days)} />}
        </Group>
      )}

      <Group anchor="knows" title={t("settings-assistantsettings.knows_title")} footer={t("settings-assistantsettings.knows_footer")}>
        {info && <AboutMe notesSaves={notesSaves} info={info} onSaved={(notes) => setInfo((i) => (i ? { ...i, notes, notes_effective: notes.trim() } : i))} />}
        <div className="flex items-center gap-2 px-4 pt-3 pb-1.5">
          <span className="flex-1 text-[13px] font-medium text-ink">{t("settings-assistantsettings.memory")}</span>
          {files && entries.length > 0 && <span className="text-[11.5px] text-ink-3 tabular-nums">{t("settings-assistantsettings.memory_count", { count: entries.length })}</span>}
        </div>
        {files === null ? (
          <div className="px-4 py-4">
            <Loading />
          </div>
        ) : entries.length === 0 ? (
          <p className="px-4 py-4 text-[12.5px] leading-relaxed text-ink-3">{memoryError ? t("settings-assistantsettings.memory_load_failed", { error: memoryError }) : t("settings-assistantsettings.memory_empty")}</p>
        ) : (
          entries.map((e) => <MemoryRow key={e.file.name} e={e} onOpen={() => setOpen({ file: e.file })} />)
        )}
        {index && entries.length > 0 && (
          <button type="button" onClick={() => setOpen({ file: index, index: true })} className="flex w-full items-center gap-3 whitespace-nowrap px-4 py-2.5 text-left transition-colors hover:bg-hover">
            <Icon name="list" size={14} className="shrink-0 text-ink-3" />
            <span className="min-w-0 flex-1">
              <span className="block truncate text-[13px] text-ink">{t("settings-assistantsettings.index")}</span>
              <span className="block truncate text-[11.5px] text-ink-3">{t("settings-assistantsettings.index_hint")}</span>
            </span>
            <Icon name="chevronRight" size={12} stroke={2} className="shrink-0 text-ink-3" />
          </button>
        )}
      </Group>

      <DirectionNotes deps={directionNotes} onOpenPermissions={() => {
        const select = document.querySelector<HTMLSelectElement>('[data-settings-row="default-permissions"] select');
        select?.scrollIntoView?.({ block: "center" });
        select?.focus({ preventScroll: true });
      }} />

      {/* Learning from finished tasks (#12295): off by default, only from a hub that has it; the lessons are on each
          project's page in Knowledge */}
      {typeof info?.task_lessons === "boolean" && (
        <Group anchor="task-lessons" footer={t("settings-assistantsettings.task_lessons_footer")}>
          <Row label={t("settings-assistantsettings.task_lessons")} hint={t("settings-assistantsettings.task_lessons_hint")} wrapHint>
            <Switch on={info.task_lessons} onChange={(on) => void setTaskLessons(on)} disabled={lessonsBusy || info.manager !== manager} label={t("settings-assistantsettings.task_lessons")} />
          </Row>
        </Group>
      )}

      <PageBoundary page="repositories">
        <Suspense fallback={<Group title={t("settings-repositories.title")}><Loading /></Group>}>
          <Repositories manager={manager} owner={owner} />
        </Suspense>
      </PageBoundary>


      <SkillsGroup manager={manager} />

      <Group
        anchor="runs-on"
        title={t("settings-assistantsettings.runs_on_title")}
        footer={machineName ? t("settings-assistantsettings.runs_on_footer", { machine: machineName }) : t("settings-assistantsettings.runs_on_none")}
      >
        <Row label={t("settings-assistantsettings.machine")} anchor="runs-on-machine">
          <span className="max-w-[220px] truncate">{machineName || t("settings-assistantsettings.machine_none")}</span>
        </Row>
        {runtime.place && (
          <Row label={t("settings-assistantsettings.main_place")}>
            <span data-main-runtime-place className="max-w-[220px] truncate" title={runtime.place}>{runtime.place}</span>
          </Row>
        )}
        {runtime.runtime && (
          <Row label={t("settings-assistantsettings.main_runtime")} hint={runtime.note} wrapHint>
            <span data-main-runtime-tool className="max-w-[220px] truncate" title={runtime.runtime}>{runtime.runtime}</span>
          </Row>
        )}
        <Row label={t("settings-assistantsettings.cwd")} mono anchor="runs-on-cwd">
          <span className="max-w-[300px] truncate" title={info?.cwd}>{info?.cwd || "~/.persona/assistant"}</span>
        </Row>
        {info?.timezone && <Row label={t("settings-assistantsettings.timezone")}>{info.timezone}</Row>}
      </Group>

      {info && <CacheKeepaliveSettings value={info} save={async (patch) => {
        const r = await scopedApi<AssistantState>("/assistant", { method: "PUT", json: patch });
        if (r.manager !== manager || Object.entries(patch).some(([key, v]) => r[key as keyof CacheKeepaliveState] !== v)) throw new Error(t("settings-cachekeepalive.unconfirmed"));
        setInfo((current) => current ? { ...current, cache_keepalive: r.cache_keepalive, cache_keepalive_minutes: r.cache_keepalive_minutes, cache_keepalive_max: r.cache_keepalive_max } : current);
      }} />}

      {/* Interactive Claude Code instead of claude -p (#10058): an advanced, off-by-default switch, only from a hub that has it */}
      {typeof info?.claude_interactive === "boolean" && (
        <Group anchor="claude-interactive" footer={t("settings-assistantsettings.claude_interactive_footer")}>
          <Row label={t("settings-assistantsettings.claude_interactive")}>
            <Switch on={info.claude_interactive} onChange={(on) => void setInteractive(on)} disabled={interactiveBusy} label={t("settings-assistantsettings.claude_interactive")} />
          </Row>
        </Group>
      )}

      {open && (
        <MemoryDialog
          scopedApi={scopedApi}
          key={open.file.name}
          file={open.file}
          title={open.index ? t("settings-assistantsettings.index") : entries.find((e) => e.file.name === open.file.name)?.title ?? open.file.name}
          readOnly={open.index}
          onClose={() => setOpen(null)}
          onChanged={() => void loadMemory()}
        />
      )}
    </>
  );
}

/** A whole number (days, or hours), 0 to `max` (`MAX_ARCHIVE_DAYS` by default): saved when the field is left or Enter is
 *  pressed, and only when it changed; anything else goes back to the saved value. */
function OwnerApprovalPhrasesField({ value, disabled, onSave }: { value: string[]; disabled: boolean; onSave: (phrases: string[]) => Promise<void> }) {
  const [draft, setDraft] = useState(value.join("\n"));
  const [saving, setSaving] = useState(false);
  useEffect(() => { if (!saving) setDraft(value.join("\n")); }, [value, saving]);
  const phrases = draft.split("\n").map((phrase) => phrase.trim()).filter(Boolean);
  const changed = JSON.stringify(phrases) !== JSON.stringify(value);
  const save = async () => {
    setSaving(true);
    try { await onSave(phrases); } catch { /* the parent shows the confirmed value and the error */ } finally { setSaving(false); }
  };
  return <div className="px-4 py-3">
    <label className="mb-2 block text-meta font-medium text-ink" htmlFor="owner-approval-phrases">{t("settings-assistantsettings.owner_approval_phrases")}</label>
    <textarea id="owner-approval-phrases" aria-label={t("settings-assistantsettings.owner_approval_phrases")} className={cx(textareaClass, "min-h-24 font-mono")}
      value={draft} disabled={disabled || saving} onChange={(e) => setDraft(e.target.value)} />
    <div className="mt-2 flex items-center gap-3">
      <p className="min-w-0 flex-1 text-label leading-relaxed text-ink-3">{t("settings-assistantsettings.owner_approval_phrases_hint")}</p>
      <Button disabled={disabled || saving || !changed || phrases.length === 0} onClick={() => void save()}>{t("settings-assistantsettings.owner_approval_save_phrases")}</Button>
    </div>
  </div>;
}

function DaysField({ value, max = MAX_ARCHIVE_DAYS, disabled, label, onCommit }: { value: number; max?: number; disabled?: boolean; label: string; onCommit: (days: number) => void }) {
  const [draft, setDraft] = useState(String(value));
  const [was, setWas] = useState(value);
  if (was !== value) {
    // the hub's answer (or a refused change taken back) replaces what is typed
    setWas(value);
    setDraft(String(value));
  }
  const commit = () => {
    const days = Number(draft);
    if (draft.trim() === "" || !Number.isInteger(days) || days < 0 || days > max) return setDraft(String(value));
    if (days !== value) onCommit(days);
  };
  return (
    <input
      type="number"
      inputMode="numeric"
      min={0}
      max={max}
      step={1}
      aria-label={label}
      value={draft}
      disabled={disabled}
      onChange={(e) => setDraft(e.target.value)}
      onBlur={commit}
      onKeyDown={(e) => {
        if (e.key === "Enter") e.currentTarget.blur();
      }}
      className={`${settingsInputClass} max-w-20 shrink-0 text-right tabular-nums`}
    />
  );
}

/** "About me": what Persona is told about you. Until saved here it shows hub.json's text; saving (even empty) makes this the
 *  one. Saved with "Save", and when leaving the page with changes. */
function AboutMe({ notesSaves, info, onSaved }: { notesSaves: ReturnType<typeof aboutMeSaves>; info: AssistantState; onSaved: (notes: string) => void }) {
  const original = info.notes ?? info.notes_effective ?? "";
  const [text, setText] = useState(() => notesSaves.pending?.value ?? notesSaves.saved ?? original);
  const [base, setBase] = useState(() => notesSaves.saved ?? original);
  const [busy, setBusy] = useState(false);
  const changed = text !== base;
  const latest = useRef({ text, changed });
  latest.current = { text, changed };
  const editor = useRef({ generation: 0, mounted: false });

  const acknowledge = async (value: string, promise: Promise<boolean>, quiet: boolean, notify = true) => {
    const generation = editor.current.generation;
    if (editor.current.mounted) setBusy(true);
    const current = () => notesSaves.current(generation) && (quiet || editor.current.mounted);
    try {
      if (!await promise || !current()) return;
      if (editor.current.mounted) {
        // Acknowledging A advances the baseline only; a newer edited B stays dirty.
        setBase(value);
        onSaved(value);
        void refreshAssistant().catch(() => {});
      }
      if (notify) toast(t(quiet ? "settings-assistantsettings.about_saved" : "settings-assistantsettings.saved_new_chats"), "ok");
    } catch (e) {
      if (current() && (notify || latest.current.text === value)) toast(t("settings-assistantsettings.about_save_failed", { error: errorText(e) }), "error");
    } finally {
      if (current() && editor.current.mounted) setBusy(false);
    }
  };
  const save = (value = text, quiet = false) => acknowledge(value, notesSaves.save(value), quiet);

  useEffect(() => {
    editor.current = { generation: notesSaves.attach(), mounted: true };
    // A remount may have read the hub before its predecessor's queued write committed.
    const pending = notesSaves.pending;
    if (pending) void acknowledge(pending.value, pending.promise, true, false);
    else if (notesSaves.saved !== undefined) { setBase(notesSaves.saved); onSaved(notesSaves.saved); }
    return () => {
      editor.current.mounted = false;
      const { text: value, changed: dirty } = latest.current;
      if (dirty || (notesSaves.pending && notesSaves.pending.value !== value)) void save(value, true);
      notesSaves.detach();
    };
  }, [notesSaves]);

  return (
    <div className="px-4 py-3">
      <div className="mb-2 flex items-center gap-2">
        <span className="flex-1 text-[13px] font-medium text-ink">{t("settings-assistantsettings.about_me")}</span>
        {info.notes === undefined && original && <span className="text-[11px] text-ink-3">{t("settings-assistantsettings.from_hub")}</span>}
      </div>
      <textarea
        className={cx(textareaClass, "min-h-[132px]")}
        rows={6}
        value={text}
        aria-label={t("settings-assistantsettings.about_me")}
        placeholder={t("settings-assistantsettings.about_placeholder")}
        onChange={(e) => setText(e.target.value)}
      />
      <div className="mt-2 flex items-start gap-3">
        <p className="min-w-0 flex-1 text-[11.5px] leading-relaxed text-ink-3">
          {t("settings-assistantsettings.about_note")}
          {info.notes === undefined && original ? t("settings-assistantsettings.about_note_hub") : ""}
        </p>
        {changed && <Button tone="quiet" disabled={busy} onClick={() => setText(base)}>{t("settings-assistantsettings.revert")}</Button>}
        <Button tone="primary" busy={busy} disabled={!changed} onClick={() => void save()}>{t("common.save")}</Button>
      </div>
    </div>
  );
}

/** One retention in days (#3780): saved on Enter or on leaving the field, and only as a whole number within the hub's
 *  bounds (said under it otherwise). The field shows the hub's value again after every try (`tries`), so a refused,
 *  unconfirmed or raised one (minutes never below the recordings' days) shows what is in effect. */
function DaysRow({ field, label, value, tries, disabled, onSave }: { field: Retention; label: string; value: number; tries: number; disabled: boolean; onSave: (days: number) => void }) {
  const { min, max } = RETENTION[field];
  const canForever = field !== "recording_days";
  const savedForever = canForever && value === 0;
  const [forever, setForever] = useState(savedForever);
  const [draft, setDraft] = useState(String(value));
  useEffect(() => { setDraft(String(value)); setForever(savedForever); }, [value, tries, savedForever]);
  const reset = () => { setDraft(String(value)); setForever(savedForever); };
  const days = Number(draft.trim());
  const valid = forever || /^\d+$/.test(draft.trim()) && days >= min && days <= max;
  const commit = () => {
    if (forever || !valid || disabled) return;
    if (days !== value) onSave(days);
    else setDraft(String(value));
  };
  return (
    <Row label={label} hint={valid ? undefined : t("settings-assistantsettings.days_range", { min, max })}>
      {canForever && <select aria-label={label} value={forever ? "forever" : "days"} disabled={disabled}
        className={settingsInputClass} onChange={(e) => {
          if (e.target.value === "forever") {
            if (!savedForever) onSave(0);
            else reset();
          } else {
            // Exposing the editor must not shorten retention before the user commits their intended days.
            setDraft(String(value || 365));
            setForever(false);
          }
        }}>
        <option value="forever">{t("settings-assistantsettings.forever")}</option>
        <option value="days">{t("settings-assistantsettings.days_unit")}</option>
      </select>}
      {!forever && <input type="number" inputMode="numeric" min={min} max={max} step={1} aria-label={label} aria-invalid={!valid || undefined}
        value={draft} disabled={disabled} onChange={(e) => setDraft(e.target.value)} onBlur={commit}
        onKeyDown={(e) => { if (e.key === "Enter") commit(); else if (e.key === "Escape") reset(); }}
        className={`${settingsInputClass} max-w-20 text-right tabular-nums`} />}
      {field === "recording_days" && <span>{t("settings-assistantsettings.days_unit")}</span>}
    </Row>
  );
}

function MemoryRow({ e, onOpen }: { e: MemoryEntry; onOpen: () => void }) {
  const type = memoryTypeLabel(e.type);
  return (
    <button type="button" onClick={onOpen} className="flex w-full items-center gap-3 px-4 py-2.5 text-left transition-colors hover:bg-hover">
      <span className="min-w-0 flex-1">
        <span className="flex min-w-0 items-center gap-2">
          <span className="truncate text-[13px] text-ink">{e.title}</span>
          {type && <Tag tone={memoryTypeTone(e.type)}>{type}</Tag>}
        </span>
        {e.detail && <span className="mt-0.5 block truncate text-[11.5px] text-ink-3">{e.detail}</span>}
      </span>
      <span className="shrink-0 text-[11px] text-ink-3">{ago(e.file.updated_at)}</span>
      <Icon name="chevronRight" size={12} stroke={2} className="shrink-0 text-ink-3" />
    </button>
  );
}

/** One memory rendered; "Edit" opens the whole file (frontmatter included) as text, "Delete" asks first. The index is read-only. */
function MemoryDialog({ scopedApi, file, title, readOnly, onClose, onChanged }: { scopedApi: typeof api; file: MemoryFile; title: string; readOnly?: boolean; onClose: () => void; onChanged: () => void }) {
  const [content, setContent] = useState(file.content);
  const [updated, setUpdated] = useState(file.updated_at);
  const [editing, setEditing] = useState<string | null>(null);
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const parsed = parseMemory(content);
  const type = memoryTypeLabel(parsed.type);

  const save = async () => {
    if (editing === null) return;
    setBusy(true);
    try {
      await scopedApi(`/assistant/memory/${enc(file.name)}`, { method: "PUT", json: { content: editing } });
      setContent(editing);
      setUpdated(new Date().toISOString());
      setEditing(null);
      toast(t("settings-assistantsettings.memory_saved"), "ok");
      onChanged();
    } catch (e) {
      toast(errorText(e), "error");
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    setBusy(true);
    try {
      await scopedApi(`/assistant/memory/${enc(file.name)}`, { method: "DELETE" });
      toast(t("settings-assistantsettings.deleted", { title }), "ok");
      onChanged();
      onClose();
    } catch (e) {
      toast(errorText(e), "error");
      setBusy(false);
    }
  };

  const footer = readOnly ? (
    <Button tone="quiet" onClick={onClose}>{t("settings-assistantsettings.close")}</Button>
  ) : editing !== null ? (
    <>
      <span className="mr-auto min-w-0 truncate font-mono text-[11px] text-ink-3">{file.name}</span>
      <Button tone="quiet" disabled={busy} onClick={() => setEditing(null)}>{t("common.cancel")}</Button>
      <Button tone="primary" busy={busy} disabled={editing === content} onClick={() => void save()}>{t("common.save")}</Button>
    </>
  ) : confirm ? (
    <>
      <span className="mr-auto min-w-0 text-[12px] text-ink-2">{t("settings-assistantsettings.delete_confirm", { title })}</span>
      <Button tone="quiet" disabled={busy} onClick={() => setConfirm(false)}>{t("common.cancel")}</Button>
      <Button tone="danger" busy={busy} onClick={() => void remove()}>{t("settings-assistantsettings.delete")}</Button>
    </>
  ) : (
    <>
      <Button tone="quiet" icon="trash" className="text-red enabled:hover:text-red" onClick={() => setConfirm(true)}>{t("settings-assistantsettings.delete")}</Button>
      <span className="flex-1" />
      <Button tone="secondary" icon="pencil" onClick={() => setEditing(content)}>{t("settings-assistantsettings.edit")}</Button>
    </>
  );

  return (
    <Dialog open onClose={() => !busy && onClose()} title={title} width={600} footer={footer}>
      {editing !== null ? (
        <textarea
          className={cx(textareaClass, "min-h-[320px] font-mono text-[12.5px]")}
          value={editing}
          autoFocus
          spellCheck={false}
          aria-label={t("settings-assistantsettings.edit_label", { name: file.name })}
          onChange={(e) => setEditing(e.target.value)}
        />
      ) : (
        <div className="-mx-4 max-h-[min(60vh,560px)] overflow-y-auto px-4">
          {!readOnly && (type || parsed.description || updated) && (
            <div className="mb-3 border-b border-line pb-3">
              <div className="flex flex-wrap items-center gap-2">
                {type && <Tag tone={memoryTypeTone(parsed.type)}>{type}</Tag>}
                {updated && <span className="text-[11.5px] text-ink-3">{t("settings-assistantsettings.updated", { when: ago(updated) })}</span>}
              </div>
              {parsed.description && <p className="mt-1.5 text-[12.5px] text-ink-2">{parsed.description}</p>}
            </div>
          )}
          <Markdown text={(readOnly ? content : parsed.body) || t("settings-assistantsettings.empty_body")} />
          {!readOnly && <p className="mt-3 font-mono text-[11px] text-ink-3">{file.name}</p>}
        </div>
      )}
    </Dialog>
  );
}
