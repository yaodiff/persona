// What an Agent's model pill and panel read (#5032, docs/web/web.md "Model pill and model panel"): the launchers of the
// computer it runs on (the panel's providers), each with the models its agent lists in its own /model menu
// (lib/launcher-models.ts), and what the pill says. The panel's own data and what a pick sends are built from these in
// lib/session-panel.ts, which loads with the panel.
//
// A list read from the agent takes seconds the first time, so what was read is kept per computer, launcher and host for the
// page's life: the pill and a later opening show the last list at once, and an opening reads every list again behind it
// (the session's own first, at most three at a time).
import { useEffect, useLayoutEffect, useRef, useSyncExternalStore } from "react";
import { t } from "../i18n";
import { ApiError, credentialOwner, errorText, onCredentialChange } from "./api";
import { agentId, sessionAgent } from "./agent-kind";
import { modelShort } from "./context-usage";
import { sessionCan, activityOf, stateOf } from "./format";
import { runsAssistant, type AssistantLauncher } from "./assistant-chat";
import { SUPERVISE_PURPOSE } from "./background-supervisor";
import {
  defaultModel, effortVariantBase, forgetCustomModel, launcherModels, listedModel, machineLaunchers, modelName, withoutCustom, withoutEffortQualifier, type AgentModel, type LauncherDefault, type LauncherModels, type UnavailableLauncher,
} from "./launcher-models";
import { canSwitchModel, isCurrentModel, namesWholeModel, rowModelIds, sessionLauncher } from "./model-switch";
import { hasEffortOnly, hasModes } from "./session-actions";
import { speedId } from "./session-speed";
import type { Launcher, Session } from "./types";
import { chosenManager, defaultKnown, defaultManager, onDefaultManager, onManagerChoice } from "./manager-choice";

/** One launcher's list as read: `list` undefined until a read answered, null from an older hub (it has no list). */
export interface ModelList {
  list?: LauncherModels | null;
  /** A read is on its way. */
  loading?: boolean;
  /** The first read or a requested refresh failed. */
  error?: string;
}

// ── what was read, kept for the page's life ──────────────────────────────────

const lists = new Map<string, ModelList>();
const machines = new Map<string, { launchers?: Launcher[]; loading?: boolean }>();
const reads = new Map<string, { promise: Promise<void>; hidden: Set<string> }>();
const machineReads = new Map<string, Promise<void>>();
// Typed IDs belong to the launcher across hosts and purposes. Each read keeps the deletions it overlapped even after they finish.
type ModelForget = { restore: Map<string, (now: LauncherModels) => LauncherModels>; promise: Promise<string> };
const forgetting = new Map<string, Map<string, ModelForget>>();
let version = 0;
const subscribers = new Set<() => void>();
const changed = () => {
  version++;
  for (const f of subscribers) f();
};
const subscribe = (f: () => void) => {
  subscribers.add(f);
  return () => void subscribers.delete(f);
};
type CatalogScope = { owner: string; manager?: string };
const catalogScope = (purpose?: "assistant"): CatalogScope => ({ owner: credentialOwner(),
  manager: (purpose === "assistant" ? chosenManager() : null) ?? (defaultKnown() ? defaultManager() : undefined) });
const launcherKeyParts = (mid: string, launcher: string, scope: CatalogScope) =>
  [scope.owner, scope.manager ?? "", mid, launcher];
const listKey = (mid: string, launcher: string, purpose?: "assistant", scope = catalogScope(purpose), host = "") =>
  JSON.stringify([...launcherKeyParts(mid, launcher, scope), purpose ?? "", host]);
const machineKey = (mid: string, scope = catalogScope()) => JSON.stringify([scope.owner, scope.manager ?? "", mid]);

/** Changes when the assistant's catalog must be loaded again, including when the default manager becomes known. */
export const assistantCatalogScope = (): string => JSON.stringify(catalogScope("assistant"));
export const agentCatalogScope = (): string => JSON.stringify(catalogScope());
onManagerChoice(changed);
onDefaultManager(changed);
onCredentialChange(changed);

/** Draws again whenever a list or a computer's launchers change. */
export function useModelLists(): number {
  return useSyncExternalStore(subscribe, () => version, () => version);
}

export const modelList = (mid: string, launcher: string, purpose?: "assistant", host?: string): ModelList =>
  lists.get(listKey(mid, launcher, purpose, undefined, host)) ?? {};

/** The launchers of `mid` as last read; undefined before the first answer. */
export const machineLauncherList = (mid: string): Launcher[] | undefined => machines.get(machineKey(mid))?.launchers;

/** What a Persona chat's pill and panel read of its computer (`?purpose=assistant`, #5033): the launchers that can run
 *  assistant chats, the main chat's default (Model configuration › Main chat), and what can't run them, with why. */
export interface AssistantLaunchers {
  launchers: AssistantLauncher[];
  def?: LauncherDefault | null;
  unavailable: UnavailableLauncher[];
}

const assistantMachines = new Map<string, { info?: AssistantLaunchers; loading?: boolean; error?: string }>();
const assistantReads = new Map<string, Promise<void>>();

/** The Persona chats' launchers of `mid` as last read (`info` undefined before the first answer), plus the in-flight
 *  read's identity for a caller that must know whether its refresh joined that exact request. */
export const assistantLaunchers = (mid: string): { info?: AssistantLaunchers; loading?: boolean; error?: string; read?: Promise<void> } => ({
  ...(assistantMachines.get(machineKey(mid, catalogScope("assistant"))) ?? {}),
  read: assistantReads.get(machineKey(mid, catalogScope("assistant"))),
});

const mainDefaults = new Map<string, LauncherDefault | null>();
const mainReads = new Map<string, Promise<void>>();
const superviseDefaults = new Map<string, LauncherDefault | null>();
const superviseReads = new Map<string, Promise<void>>();

/** What the background supervisor's own model purpose resolves to on `mid` (`?purpose=assistant.supervise`: Main chat's
 *  default until it is set apart); undefined before the first answer, null when the hub gave none. */
export const superviseDefault = (mid: string): LauncherDefault | null | undefined => superviseDefaults.get(machineKey(mid, catalogScope("assistant")));

/** Reads it: once, or `again` (a push changed the conversation, an opening of the panel). */
export function readSuperviseDefault(mid: string, o: { again?: boolean } = {}): Promise<void> {
  return readPurposeDefault(mid, SUPERVISE_PURPOSE, superviseDefaults, superviseReads, o);
}

/** Main-only settings are separate from the new-conversation catalog's default. */
export const mainDefault = (mid: string): LauncherDefault | null | undefined => mainDefaults.get(machineKey(mid, catalogScope("assistant"))) ?? undefined;
export function readMainDefault(mid: string, o: { again?: boolean } = {}): Promise<void> {
  return readPurposeDefault(mid, "assistant.main", mainDefaults, mainReads, o);
}

function readPurposeDefault(mid: string, purpose: string, defaults: Map<string, LauncherDefault | null>, pending: Map<string, Promise<void>>, o: { again?: boolean }): Promise<void> {
  const scope = catalogScope("assistant");
  const key = machineKey(mid, scope);
  const reading = pending.get(key);
  if (reading) return reading;
  if (defaults.has(key) && !o.again) return Promise.resolve();
  const read = machineLaunchers(mid, { purpose, manager: scope.manager }).then(
    (r) => void defaults.set(key, r.default ?? null),
    () => undefined, // An older hub has no main purpose: retain the shared default.
  ).finally(() => {
    pending.delete(key);
    changed();
  });
  pending.set(key, read);
  return read;
}

/** Reads them: once (a pill that needs the default), or `again` behind the ones shown (an opening of the panel). */
export function readAssistantLaunchers(mid: string, o: { again?: boolean } = {}): Promise<void> {
  const scope = catalogScope("assistant");
  const key = machineKey(mid, scope);
  const reading = assistantReads.get(key);
  if (reading) return reading;
  const had = assistantMachines.get(key);
  if (had?.info && !o.again) return Promise.resolve();
  assistantMachines.set(key, { info: had?.info, loading: true });
  changed();
  const read = machineLaunchers(mid, { purpose: "assistant", manager: scope.manager }).then(
    (r) => void assistantMachines.set(key, { info: { launchers: (r.launchers ?? []).filter(runsAssistant), def: r.default, unavailable: r.unavailable ?? [] } }),
    // the ones shown stay; with none, say why
    (e) => void assistantMachines.set(key, had?.info ? { info: had.info } : { error: errorText(e) }),
  ).finally(() => {
    assistantReads.delete(key);
    changed();
  });
  assistantReads.set(key, read);
  return read;
}

/** Calls `read` after a push moved conversation `s`'s revision (#6542), once per turn instead of once per write: `rev`
 *  moves whenever the transcript does (proto Session.Rev), many times a minute while a turn streams, and what these
 *  reads show (the computer's launchers, Main chat's default) only matters for the next message. A move while the
 *  conversation works waits until it stops; a move while it doesn't reads at once. A move while `enabled` is false
 *  (no computer yet) is kept and read once it turns true. Another conversation (machine and session id) starts afresh:
 *  its revision is not a move, and nothing owed carries over. `read` may return a cleanup, run before the next read,
 *  when the conversation changes or `enabled` turns false, and on unmount, not when the conversation starts working: a
 *  turn starting must not drop what the last read left pending (a trailing read, #12778 review). */
export function useRevisionReread(s: Session | undefined, enabled: boolean, read: () => void | (() => void)): void {
  const rev = s?.rev;
  const who = `${s?.machine_id ?? ""}/${s?.session_id ?? ""}`;
  const working = Boolean(s && stateOf(s) === "working" && !s.background_working);
  const seen = useRef({ who, rev });
  const owed = useRef(false);
  const latest = useRef(read);
  const cleanup = useRef<(() => void) | undefined>(undefined);
  useLayoutEffect(() => {
    latest.current = read;
  });
  useEffect(() => {
    const done = () => {
      cleanup.current?.();
      cleanup.current = undefined;
    };
    if (seen.current.who !== who) {
      seen.current = { who, rev };
      owed.current = false;
      done();
      return;
    }
    if (rev !== undefined && rev !== seen.current.rev) {
      seen.current.rev = rev;
      owed.current = true;
    }
    if (!enabled) done();
    if (!owed.current || working || !enabled) return;
    owed.current = false;
    done();
    cleanup.current = latest.current() || undefined;
  }, [who, rev, working, enabled]);
  useEffect(() => () => {
    cleanup.current?.();
    cleanup.current = undefined;
  }, []);
}

// at most three lists are read at a time (each may start the agent's CLI on the computer); `first` jumps the queue
const MAX_READS = 3;
const queue: (() => void)[] = [];
let running = 0;
function slot(first: boolean): Promise<() => void> {
  return new Promise((go) => {
    const start = () => {
      running++;
      go(() => {
        running--;
        queue.shift()?.();
      });
    };
    if (running < MAX_READS) start();
    else if (first) queue.unshift(start);
    else queue.push(start);
  });
}

/** Reads a launcher's list once, `again` on opening, or with `refresh` to skip the computer's cache. */
export function readModels(mid: string, launcher: string, o: { again?: boolean; first?: boolean; purpose?: "assistant"; host?: string; refresh?: boolean } = {}): Promise<void> {
  const scope = catalogScope(o.purpose);
  const key = listKey(mid, launcher, o.purpose, scope, o.host);
  const reading = reads.get(key);
  if (reading) return reading.promise;
  const had = lists.get(key);
  if (had?.list !== undefined && !o.again && !o.refresh) return Promise.resolve();
  lists.set(key, { list: had?.list, loading: true });
  changed();
  const sharedKey = JSON.stringify(launcherKeyParts(mid, launcher, scope));
  const hidden = new Set(forgetting.get(sharedKey)?.keys());
  const read = slot(Boolean(o.first)).then(async (done) => {
    try {
      if (scope.owner !== credentialOwner()) return;
      // This read owns a queued generation; the per-key cache above, not an older GET, coalesces its callers.
      const r = await launcherModels(mid, launcher, {
        purpose: o.purpose, host: o.host, manager: scope.manager, refresh: o.refresh, dedupe: false,
      });
      for (const id of hidden) {
        if (r?.models.some((m) => m.id === id && m.custom)) {
          forgetting.get(sharedKey)?.get(id)?.restore.set(key, withoutCustom(r, id).restore);
        }
      }
      lists.set(key, { list: r && hidden.size ? { ...r, models: r.models.filter((m) => !(m.custom && hidden.has(m.id))) } : r });
    } catch (e) {
      // Keep the catalog on a failed refresh. A new omp start still needs the node to confirm its capability.
      const list = lists.get(key)?.list;
      const error = list === undefined || o.refresh ? errorText(e) : undefined;
      lists.set(key, {
        ...(list !== undefined && { list: list?.supports_start_model ? { ...list, supports_start_model: false } : list }),
        ...(error && { error }),
      });
    } finally {
      done();
      reads.delete(key);
      changed();
    }
  });
  reads.set(key, { promise: read, hidden });
  return read;
}

/** Reads the computer's launchers (the providers), the last ones shown meanwhile. */
function readLaunchers(mid: string): Promise<void> {
  const scope = catalogScope();
  const key = machineKey(mid, scope);
  const reading = machineReads.get(key);
  if (reading) return reading;
  const had = machines.get(key);
  machines.set(key, { launchers: had?.launchers, loading: true });
  changed();
  const read = machineLaunchers(mid, { purpose: "agent.new", manager: scope.manager }).then(
    (r) => void machines.set(key, { launchers: r.launchers ?? [] }),
    () => void machines.set(key, { launchers: had?.launchers }),
  ).finally(() => {
    machineReads.delete(key);
    changed();
  });
  machineReads.set(key, read);
  return read;
}

/** Reuse this computer's catalog for the pill's launcher titles. Opening the panel refreshes it. */
export function readAgentLauncherNames(mid: string): Promise<void> {
  return machineLauncherList(mid) ? Promise.resolve() : readLaunchers(mid);
}

/** Read the panel's lists again behind the ones shown, starting with the session's own launcher in its catalog host. */
export function readForPanel(mid: string, own: string, host?: string): void {
  void readModels(mid, own, { again: true, first: true, host });
  void readLaunchers(mid).then(() => {
    for (const l of machineLauncherList(mid) ?? []) if (l.name !== own) void readModels(mid, l.name, { again: true });
  });
}

/** Forgets an ID typed before (#1644): off the list at once, back where it was when the hub says no. "": done; else why not. */
export async function forgetModel(mid: string, launcher: string, id: string, purpose?: "assistant", host?: string): Promise<string> {
  const scope = catalogScope(purpose);
  const sharedKey = JSON.stringify(launcherKeyParts(mid, launcher, scope));
  const pending = forgetting.get(sharedKey) ?? new Map<string, ModelForget>();
  const existing = pending.get(id);
  if (existing) return existing.promise;
  if (!lists.get(listKey(mid, launcher, purpose, scope, host))?.list) return "";
  const prefix = sharedKey.slice(0, -1) + ",";
  const keys = () => [...lists.keys()].filter((key) => key.startsWith(prefix));
  const mutation: ModelForget = {
    restore: new Map(),
    promise: Promise.resolve().then(async () => {
      try {
        if (scope.owner !== credentialOwner()) return "";
        await forgetCustomModel(mid, launcher, id, { manager: scope.manager });
        return "";
      } catch (e) {
        for (const key of keys()) {
          reads.get(key)?.hidden.delete(id);
          const now = lists.get(key);
          const restore = mutation.restore.get(key);
          if (now?.list && restore) lists.set(key, { ...now, list: restore(now.list) });
        }
        changed();
        return t("composer.forget_failed", { id, error: errorText(e) });
      } finally {
        pending.delete(id);
        if (!pending.size) forgetting.delete(sharedKey);
      }
    }),
  };
  pending.set(id, mutation);
  forgetting.set(sharedKey, pending);
  for (const key of keys()) {
    const current = lists.get(key);
    if (current?.list) {
      const removed = withoutCustom(current.list, id);
      mutation.restore.set(key, removed.restore);
      lists.set(key, { ...current, list: removed.list });
    }
    reads.get(key)?.hidden.add(id);
  }
  changed();
  return mutation.promise;
}

// ── the session's model, from what was read ──────────────────────────────────

/** The catalog host for a session owned by the AGY companion. Its session has no terminal host. */
export function sessionModelHost(s: Session): "agy" | undefined {
  return s.source_kind === "runtime" && s.source_instance_id === "agy" ? "agy" : undefined;
}

/** The engine a session runs (AgentMark ids: claude, codex, agy…; one of Paseo's providers is that provider, grok…). */
export const sessionEngine = (s: Session): string => agentId(sessionAgent(s).agent);

/** The hub moves a running Agent between Claude Code and Codex with the user's next message (#5054,
 *  docs/protocol/protocol.md). */
export const ENGINE_SWITCH_FEATURE = "engine_switch";

/** Whether `s` may move to the other engine on a hub with ENGINE_SWITCH_FEATURE: a running Claude Code Agent whose model
 *  the app switches in a terminal (as for another launcher, #5035), or a Codex Agent in a terminal (its own row: a Codex
 *  app thread or a runtime's session has a source; a runtime's Claude Code switches its model there, not its engine,
 *  #8142). The node refuses the rest (`switch_unsupported`). */
export function engineSwitchable(s: Session): boolean {
  const engine = sessionEngine(s);
  if (engine === "claude") return !s.source_kind && canSwitchModel(s);
  return (engine === "codex" || engine === "agy") && !s.assistant && !s.source_kind && Boolean(s.pane_id) && sessionCan(s, "send") && activityOf(s) !== "offline";
}

/** What the pill and the panel can change on the session now: its model (Claude Code in a terminal, a runtime that says
 *  so), its effort (the composer's chips used to; a Codex thread on its app-server too). Neither: the pill is a label. */
export function sessionSwitches(s: Session): { model: boolean; effort: boolean } {
  const live = activityOf(s) !== "offline";
  return { model: canSwitchModel(s), effort: live && (hasModes(s) || hasEffortOnly(s)) };
}

/** The row of `models` the session runs: the one its model names (an alias or what an alias stands for; the default entry
 *  only when no other row names it); with no model reported yet, the default entry, which is what runs then. An exact
 *  match (any case) is preferred; a gateway's routed echo or other 1M spelling folds to its row only when nothing exact
 *  does (isCurrentModel). */
export function currentModelRow(models: AgentModel[], model: string | undefined): AgentModel | undefined {
  if (!model) return defaultModel(models);
  const c = model.toLowerCase();
  const exact = models.filter((m) => rowModelIds(m).some((v) => v?.toLowerCase() === c));
  const hits = exact.length ? exact : models.filter((m) => isCurrentModel(m, model));
  return hits.find((m) => !m.default) ?? hits[0];
}

/** A model ID said briefly where no list names it: Claude's as "opus 5.5", GPT's as their list would; gateway paths keep
 *  their routing prefix so the upstream channel stays visible (#10125). */
export function modelLabel(id: string): string {
  const trimmed = id.trim();
  const claude = modelShort(trimmed);
  return claude !== trimmed ? claude : modelName({ id: trimmed });
}

export function pillModel(row: AgentModel | undefined, active: string | undefined, fallback = active): Pick<PillSetup, "model" | "effort"> {
  if (!row || !namesWholeModel(row, active)) return { model: fallback ? modelLabel(fallback) : undefined };
  const base = effortVariantBase(row.id, row.default_effort);
  if (!base) return { model: modelName(row) };
  const name = row.name?.trim() || row.id;
  return { model: name === row.id ? modelLabel(base) : withoutEffortQualifier(name), effort: row.default_effort };
}

/** One setup as the pill draws it. */
export interface PillSetup {
  /** The engine (AgentMark ids: claude, codex…). */
  engine: string;
  /** What the engine is called when the app has no name of its own for it: a runtime provider's label ("Grok", the
   *  row's agent_label, #8142); its mark and its name in the tooltip. */
  engineLabel?: string;
  /** The provider (launcher) name: its tile, and the tooltip. */
  provider: string;
  /** Explicit Agent launcher name, matching its panel row; absent when only an inferred engine is known. */
  launcherName?: string;
  /** The model's name; absent while not known (no reply yet and no list to say what the launcher runs by default). */
  model?: string;
  effort?: string;
  speed?: string;
}

/** What the pill says: the engine, the launcher and the model the session runs (its list's name for it once read), the
 *  effort and speed; `loading` while the list is read the first time, `failed` when that failed. */
export interface PillView extends PillSetup {
  loading: boolean;
  failed: boolean;
}

/** A message refused because the switch to another launcher it was to carry out first could not be (#5035,
 *  docs/protocol/protocol.md "Switching a running Agent to another provider": `switch_launcher_missing`,
 *  `switch_failed`…): nothing was typed and the switch was dropped. */
export const switchRefused = (e: unknown): boolean => e instanceof ApiError && e.status === 409 && Boolean(e.code?.startsWith("switch_"));

/** The engine the waiting switch moves the conversation to (#5054): `pending_switch.agent`, "" when none waits or it
 *  stays on its engine (another launcher of it, #5035). */
export function pendingEngine(s: Session): string {
  const ps = s.pending_switch;
  if (!ps?.launcher || ps.launcher === sessionLauncher(s) || !ps.agent) return "";
  const to = agentId(ps.agent);
  return to === sessionEngine(s) ? "" : to;
}

/** The switch to another launcher waiting for the next message (#5035), as the pill draws it: that launcher, the model
 *  picked (its list's name for it once read; none picked: the launcher's default), the effort picked or the one it runs
 *  with now. A switch to the other engine (#5054) draws that engine, and no effort of the engine it leaves: the one
 *  picked, else the model's own default. null: none waits. */
export function pendingView(s: Session, listOf: (launcher: string) => ModelList): PillSetup | null {
  const ps = s.pending_switch;
  if (!ps?.launcher || ps.launcher === sessionLauncher(s)) return null;
  const models = listOf(ps.launcher).list?.models ?? [];
  const row = ps.model ? listedModel(models, ps.model) : defaultModel(models);
  const to = pendingEngine(s);
  const out: PillSetup = { engine: to || sessionEngine(s), provider: ps.launcher, model: row ? modelName(row) : ps.model ? modelLabel(ps.model) : t("chat-modelpill.default_model") };
  const effort = ps.effort || (to ? row?.default_effort : s.effort);
  if (effort) out.effort = effort;
  return out;
}

export function pillView(s: Session, e: ModelList, displayModel?: string): PillView {
  const active = s.model || displayModel;
  const out: PillView = { ...pillModel(currentModelRow(e.list?.models ?? [], active), active), engine: sessionEngine(s), provider: sessionLauncher(s), loading: e.list === undefined && Boolean(e.loading), failed: e.list === undefined && Boolean(e.error) };
  if (s.agent_label) out.engineLabel = s.agent_label;
  if (s.effort) out.effort = s.effort;
  if (s.service_tier) out.speed = speedId(s.service_tier);
  return out;
}

/** Forgets every list and read kept (tests: each starts with nothing read). */
export function resetModelListsForTests(): void {
  lists.clear();
  machines.clear();
  reads.clear();
  machineReads.clear();
  forgetting.clear();
  queue.length = 0;
  running = 0;
  changed();
}

/** Subscribes to every change of what was read (views/NewAgentModel.tsx useModelList follows one list with it). */
export const subscribeModelLists = subscribe;
