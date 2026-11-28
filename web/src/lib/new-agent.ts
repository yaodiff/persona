import { t } from "../i18n";
import { agentId } from "./agent-kind";
import { isEcho, type Seen } from "./echo";
import { errorText } from "./api";
import { parseUserText } from "./turns";
import type { LocalAttachment } from "./attachments";
import { uuid } from "./format";
import { currentPlatform, modName, type Platform } from "./shortcuts";
import { navigate } from "./router";
import { getState, putLaunch } from "./store";
import type { DraftImage } from "./drafts";
import type { ContextRef } from "./links";
import type { Session } from "./types";
import type { WorkspaceTarget } from "./workspace-target";

export type { WorkspaceTarget } from "./workspace-target";

export interface AgentLaunch {
  id: string;
  machineId: string;
  cwd: string;
  launcher: string;
  agentType?: string;
  /** Display-only effective target resolved at launch time (never sent to POST /sessions): keeps the sidebar Starting
   *  placeholder icon, StartingAgent header, and newly opened session composer aligned with Follow model settings. */
  displayLauncher?: string;
  displayEngine?: string;
  displayModel?: string;
  workSession?: boolean;
  /** Start with the first message when this harness takes it as part of creation. */
  inlinePrompt?: boolean;
  /** Picked in "New agent" for this start only (launcher-models.ts startPicks); none: the launcher's own. */
  model?: string;
  effort?: string;
  serviceTier?: string;
  /** Keep the launcher's native effort even when Model settings configures this launcher. */
  launcherDefaultEffort?: boolean;
  /** Claude Code's permission mode for this start ("New agent"'s "Permission mode"); none: the launcher's own. */
  permissionMode?: string;
  /** The exact runtime workspace, scoped to machine and host; absent for an ordinary folder start. */
  workspaceId?: string;
  /** The exact native desktop project, independent of terminal-host workspaces. */
  desktopProjectId?: string;
  /** An explicit host stays explicit even when it equals the machine's default; none: follow the default. */
  host?: string;
  /** Turn ultracode on once Claude is up (#3428); only ever true: off is what a new Claude is (docs/protocol/protocol.md). */
  ultracode?: boolean;
  /** Optional title source when the first message contains private project content. */
  nameHint?: string;
  text: string;
  attachments: LocalAttachment[];
  todoId?: string;
  phase: "creating" | "uploading" | "sending" | "failed" | "ready";
  session?: Session;
  outgoing?: { text: string; images: { mime: string; data: string }[] };
  uploaded: Record<string, string>;
  error?: string;
  /** The body of a shell_waiting refusal it failed with: the question the start's tab stopped on, answered from the
   *  launch's page (lib/start-continue.ts). */
  shellWaiting?: Record<string, unknown>;
  /** The start's op id (the hub does it once per id); `startPromptOpId` is for the Codex start with a first message. */
  startOpId: string;
  startPromptOpId: string;
  /** The first message's client_msg_id, its op id too. */
  messageId: string;
  /** The hub answered that it doesn't know how this went: only the person can say whether to go on. */
  uncertain?: boolean;
  /** The target is locked on retry: work-session choices or a start that the hub may already have. */
  repeatOnly?: boolean;
  /** A native session exists but desktop setup has not acknowledged completion. */
  desktopSetupIncomplete?: boolean;
  clearable?: string;
  draft?: string;
  /** Epoch ms when a launch with an initial prompt entered handoff, keeping the session working until the hub reports working (#12380). */
  handoffAt?: number;
}

/** `open: false` starts it without opening its conversation (Compare runs starts several and opens their page, #8916). */
export function launchAgent(input: Pick<AgentLaunch, "machineId" | "cwd" | "workspaceId" | "desktopProjectId" | "launcher" | "agentType" | "displayLauncher" | "displayEngine" | "displayModel" | "workSession" | "inlinePrompt" | "model" | "effort" | "serviceTier" | "launcherDefaultEffort" | "permissionMode" | "host" | "ultracode" | "text" | "nameHint" | "attachments" | "todoId">, options: { open?: boolean } = {}) {
  const launch: AgentLaunch = { ...input, id: `local-${uuid()}`, phase: "creating", uploaded: {}, ...launchOpIds() };
  putLaunch(launch);
  if (options.open !== false) navigate({ name: "session", mid: launch.machineId, sid: launch.id });
  // Let the local conversation paint before serialization, uploads and remote startup.
  setTimeout(() => void deliverLaunch(launch.id), 0);
  return launch.id;
}

/** Fresh op ids: the hub keeps its answer to an id, so a try after a refusal needs new ones (a gap keeps the old ones). */
function launchOpIds() {
  return { startOpId: uuid(), startPromptOpId: uuid(), messageId: uuid() };
}

/** The engine mark ID for a starting placeholder in the sidebar; undefined falls back to the generic agent icon. */
export function launchDisplayEngine(launch: Pick<AgentLaunch, "launcher" | "agentType" | "displayEngine" | "displayLauncher">): string | undefined {
  if (launch.displayEngine) return agentId(launch.displayEngine);
  if (launch.agentType) return agentId(launch.agentType);
  const name = (launch.displayLauncher || launch.launcher).trim().toLowerCase();
  if (!name) return undefined;
  if (name === "claude" || name === "codex" || name === "agy" || name === "omp" || name === "traex") return name;
  return undefined;
}

/**
 * The advertised start model for a newly launched session before its first transcript turn reports `session.model`.
 * Returns undefined as soon as the session reports its own model or if the launched launcher changed.
 */
export function launchModelForSession(
  launches: Record<string, AgentLaunch> | undefined,
  session: Pick<Session, "machine_id" | "session_id" | "launcher" | "model">,
): string | undefined {
  if (!launches || session.model) return undefined;
  for (const launch of Object.values(launches)) {
    if (launch.machineId !== session.machine_id || launch.session?.session_id !== session.session_id) continue;
    const expectedLauncher = launch.launcher || launch.displayLauncher;
    if (expectedLauncher && session.launcher && expectedLauncher !== session.launcher) continue;
    const model = launch.model || launch.displayModel;
    if (model) return model;
  }
  return undefined;
}

/** `user` (a user message, or an item the hub tied to a message) is the copy of a started conversation's first message. */
export function acknowledgeLaunch(mid: string, sid: string, user: Seen) {
  for (const launch of Object.values(getState().launches)) {
    if (launch.phase !== "ready" || launch.machineId !== mid || launch.session?.session_id !== sid || !launch.outgoing) continue;
    // the conversation's first message: nothing older in it can say the same, so no time to compare
    if (isEcho({ id: launch.messageId, text: parseUserText(launch.outgoing.text).text, images: launch.outgoing.images, at: 0 }, user)) putLaunch({ ...launch, outgoing: undefined });
  }
}

const delivering = new Set<string>();

/** Load transport work on intent; repeated clicks during loading or delivery share the same launch. */
export async function deliverLaunch(id: string, clearDraft?: string) {
  const launch = getState().launches[id];
  if (!launch || delivering.has(id) || (launch.phase !== "failed" && launch.phase !== "creating")) return;
  delivering.add(id);
  try {
    const delivery = await import("./new-agent-delivery");
    await delivery.deliverLaunch(id, clearDraft);
  } catch (e) {
    // A missing chunk has made no request. Keep the draft and operation IDs so Retry remains safe.
    const current = getState().launches[id];
    if (current) putLaunch({ ...current, phase: "failed", error: t("lib-new-agent.start_failed", { error: errorText(e) }) });
  } finally {
    delivering.delete(id);
  }
}

// ── opening "New agent" from another page ──────────────

/** What "New agent" opens with when another page opens it ("Knowledge"'s "Start agent"): the first message, and where if known. */
export interface NewAgentPreset {
  /** Open the work-session picker from the main conversation. */
  workSession?: boolean;
  prompt?: string;
  machineId?: string;
  cwd?: string;
  /** The exact desktop launcher and native project selected by the source page. */
  launcher?: string;
  desktopProjectId?: string;
  /** Messages forwarded to a new Agent (#3899; several at once, #3838): their card over the first message, which carries
   *  them in front; and the pictures they had, attached. */
  forwards?: ContextRef[];
  images?: DraftImage[];
  /** An exact workspace destination takes precedence over the legacy machine/folder preset. */
  workspace?: WorkspaceTarget;
  /** Hears the Agent started from this opening once its launch is ready, its first message sent (Inbox's "Hand to AI" ›
   *  "New agent…", #5933: it files the item and remembers the Agent; lib/new-agent-started.ts). Never for a cancelled
   *  sheet or a launch dropped before that. */
  onStarted?: (session: Session) => void;
}

const openers = new Set<(preset: NewAgentPreset) => void>();

/** Opens "New agent" (the app shell listens) with `preset` filled in. */
export function openNewAgent(preset: NewAgentPreset = {}) {
  openers.forEach((l) => l(preset));
}

export function onOpenNewAgent(l: (preset: NewAgentPreset) => void) {
  openers.add(l);
  return () => {
    openers.delete(l);
  };
}

/**
 * The keys that start "New agent" from the first-message box, for its placeholder: Mod ↵ (⌘ on Apple, Ctrl elsewhere, as
 * the shortcuts name it), and none on a touch-only device (a phone has no ⌘ key, so the hint would only take up room).
 */
export function startKeys(platform: Platform, touchOnly: boolean): string | null {
  return touchOnly ? null : `${modName(platform)} ↵`;
}

/** startKeys for this browser. */
export function browserStartKeys(): string | null {
  const touchOnly = typeof matchMedia === "function" && matchMedia("(hover: none) and (pointer: coarse)").matches;
  return startKeys(currentPlatform(), touchOnly);
}

/** The first message with a preset: its text first, then what was already being written (a draft isn't lost). */
export function presetPrompt(preset: string | undefined, draft: string): string {
  const p = (preset ?? "").trimEnd();
  if (!p) return draft;
  if (!draft.trim()) return `${p}\n\n`;
  if (draft.startsWith(p)) return draft;
  return `${p}\n\n${draft.trim()}`;
}
