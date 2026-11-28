// The model panel's data for an Agent's chat (#5032, docs/web/web.md "Model pill and model panel"), from what
// lib/session-models.ts read: the computer's launchers are the providers, each with its list, and the setup the session
// runs now; and what a pick sends. The session's own launcher switches its model at once. On a hub that switches a
// running Agent to another launcher (`launcher_switch`, #5035), a Claude Code Agent in a terminal may also pick another
// launcher of its engine: the pick is recorded and the next message carries it out (the agent restarts on that launcher
// and keeps the conversation). On a hub that also moves it to the other engine (`engine_switch`, #5054), a Claude Code or
// Codex Agent in a terminal may pick the other engine's launchers the same way: the next message starts that engine in
// the same terminal with a handoff of the conversation (the panel asks first, EngineSwitchDialog.tsx). Without it another
// engine needs a new conversation, and an agent that can't change its model while it runs (a Codex thread) keeps its
// effort and speed only. Loaded with the panel, not with the composer.
import { t } from "../i18n";
import { activityOf, sessionCan } from "./format";
import { agentId, agentName, providerAgent } from "./agent-kind";
import { ALL_EFFORTS, defaultModel, effortVariantBase, launcherAgent, modelName, typedModelOffered, type AgentModel, type LauncherModels } from "./launcher-models";
import { launcherBlock } from "./launcher-health";
import { validModelId } from "./model-id";
import { cleanSetup, type PanelConfig, type PanelModel, type PanelProvider, type PanelState } from "./model-panel";
import { canSwitchModel, isCurrentModel, modelBusy, sessionLauncher } from "./model-switch";
import { launcherTitle } from "./provider-names";
import { currentModelRow, engineSwitchable, modelLabel, pendingEngine, sessionEngine, sessionSwitches, type ModelList } from "./session-models";
import { speedId, speedOptions, speedTier } from "./session-speed";
import type { Launcher, Session } from "./types";

/** The efforts a row's slider offers. Antigravity lists one model per thinking level, so its rows have none (a pick
 *  takes the variant's own); the model a thread runs takes the thread's own levels (a Codex thread says them); an ID
 *  typed by hand into Claude Code takes any, as nobody knows. */
function rowEfforts(engine: string, m: AgentModel, listed: boolean, current: boolean, s: Session): string[] {
  if (engine === "agy") return [];
  if (current && !sessionSwitches(s).effort) return [];
  if (current && s.efforts?.length) return s.efforts;
  if (listed && !m.custom) return m.efforts ?? [];
  return engine === "claude" ? ALL_EFFORTS : [];
}

/** A row of the panel; `listed` false: the model the session runs, which its list doesn't name. */
function panelModel(m: AgentModel, engine: string, current: boolean, s: Session, listed = true): PanelModel {
  const out: PanelModel = { id: m.id, name: modelName(m), efforts: rowEfforts(engine, m, listed, current, s) };
  if (m.description) out.description = m.description;
  if (m.resolved) out.resolved = m.resolved;
  if (m.aliases?.length) out.aliases = m.aliases;
  if (m.default_effort && (!current || s.effort || effortVariantBase(m.id, m.default_effort))) out.defaultEffort = m.default_effort;
  if (m.custom) out.custom = true;
  if (m.price) out.price = m.price;
  if (current && activityOf(s) !== "offline" && sessionCan(s, "send")) {
    const speeds = speedOptions(s);
    if (speeds.length) out.speeds = engine === "claude" ? speeds.map((choice) => choice.id === "fast" ? { ...choice, description: t("ui-modelpanel.claude_fast") } : choice) : speeds;
  }
  if (current && s.context_window) out.context = s.context_window;
  if (current && engine === "codex") {
    if (sessionSwitches(s).effort) out.effortNote = t("chat-modelpanel.effort_next_turn");
    else if (s.state === "offline" || s.status === "offline") out.effortUnavailable = t("chat-modelpanel.effort_offline");
    else if (s.parent_thread_id) out.effortUnavailable = t("chat-modelpanel.effort_parent");
    else if (s.capabilities?.send === false) out.effortUnavailable = t("chat-modelpanel.effort_read_only");
    else if (s.source_kind === "codex_app_server" || s.space_kind === "codex_app") out.effortUnavailable = t("chat-modelpanel.effort_model_fixed");
    else out.effortUnavailable = t("chat-modelpanel.effort_terminal");
  }
  return out;
}

export interface SessionPanel {
  providers: PanelProvider[];
  state: PanelState;
}

/** The hub switches a running Agent to another launcher (#5035): the panel offers other launchers only on such a hub. */
export const LAUNCHER_SWITCH_FEATURE = "launcher_switch";

export interface PanelOptions {
  /** The hub has LAUNCHER_SWITCH_FEATURE. */
  switching?: boolean;
  /** The hub has ENGINE_SWITCH_FEATURE (lib/session-models.ts). */
  engineSwitch?: boolean;
  /** The engine each launcher runs, as the panel's providers say (a pick from a list not read yet still knows it). */
  engineOf?: (launcher: string) => string | undefined;
  /** Display-only advertised model for a newly launched session before `s.model` arrives; never used by `pickPlan`. */
  displayModel?: string;
}

/** Whether `s` may move to another launcher of its engine: on a hub that does it, a Claude Code Agent whose model the
 *  app switches in a terminal (a runtime's session, Paseo's too, switches only its model; the node refuses the rest,
 *  `switch_unsupported`, #8142). */
export const switchesLauncher = (s: Session, o: PanelOptions): boolean => Boolean(o.switching) && !s.source_kind && sessionEngine(s) === "claude" && canSwitchModel(s);

const SWITCH_ENGINES = new Set(["claude", "codex", "agy"]);

/** The engine a conversation moves to from `engine` ("" for an engine that doesn't switch). Kept for single-target callers. */
export const otherEngine = (engine: string): string => (engine === "claude" ? "codex" : engine === "codex" ? "claude" : "");

/** Whether `s` may move to a launcher of `engine`, another engine, with its next message (#5054). */
export const switchesEngine = (s: Session, engine: string, o: PanelOptions): boolean => {
  const from = sessionEngine(s);
  return Boolean(o.engineSwitch) && engine !== "" && engine !== from && SWITCH_ENGINES.has(from) && SWITCH_ENGINES.has(engine) && engineSwitchable(s);
};

/** The panel's providers and state for `s`: `launchers` the computer's (undefined before they are read), `listOf` what
 *  was read for each. The session's own launcher is always there, at its place in the list or first. */
export function sessionPanel(s: Session, launchers: Launcher[] | undefined, listOf: (launcher: string) => ModelList, o: PanelOptions = {}): SessionPanel {
  const own = sessionLauncher(s);
  const engine = sessionEngine(s);
  const modelSwitch = canSwitchModel(s);
  const switching = switchesLauncher(s, o);
  const all = launchers ?? [];
  const names = all.some((l) => l.name === own) ? all.map((l) => l.name) : [own, ...all.map((l) => l.name)];
  const ownList = listOf(own).list;
  const shownModel = s.model || o.displayModel;
  const row = currentModelRow(ownList?.models ?? [], shownModel);

  const providers = names.map((name): PanelProvider => {
    const e = listOf(name);
    const mine = name === own;
    const kind = mine ? engine : agentId(providerAgent(launcherAgent(name, all, e.list), name));
    // another launcher of the engine, on a hub that switches to it with the next message; or one of the other engine, on
    // a hub that moves the conversation there (#5054)
    const other = !mine && ((switching && kind === engine) || switchesEngine(s, kind, o));
    const launcher = all.find((l) => l.name === name);
    const defaultRow = currentModelRow(e.list?.models ?? [], launcher?.model) ?? defaultModel(e.list?.models ?? []);
    const defaultName = defaultRow ? (defaultRow.resolved ? modelLabel(defaultRow.resolved) : modelName(defaultRow)) : launcher?.model ? modelLabel(launcher.model) : undefined;
    const p: PanelProvider = { id: name, name: launcherTitle(name, all), engine: kind, models: null, defaultModel: defaultName, status: launcherBlock(launcher)?.reason };
    // the session's own provider is called what its row calls it (a Paseo provider's label, #8142)
    if (mine && s.agent_label) p.engineLabel = s.agent_label;
    if (e.error) p.error = e.error;
    if (e.loading || (e.list === undefined && !e.error)) p.loading = true;
    if (e.list === undefined) {
      // the model it runs stays there while its list is read or couldn't be: its flyout (effort, Fast, ultracode) works
      // as the old popover's tabs did with no list
      if (mine && shownModel) p.models = [panelModel({ id: shownModel, name: modelLabel(shownModel) }, kind, true, s, false)];
    } else {
      let models = (e.list?.models ?? []).map((m) => panelModel(m, kind, mine && m === row, s));
      // a launcher that fixes its own --model takes no model with a switch: its default entry, only
      if (other && e.list?.locked) models = models.filter((m) => m.id === defaultModel(e.list?.models ?? [])?.id);
      // the model it runs, when the list doesn't name it (a list that couldn't be read, an older hub, a model it lacks)
      if (mine && !row && shownModel) models.push(panelModel({ id: shownModel, name: modelLabel(shownModel) }, kind, true, s, false));
      p.models = models;
    }
    if (other) {
      if (typedModelOffered(kind, e.list)) p.typable = true;
    } else if (!mine) {
      p.unavailable = kind === engine ? t("chat-modelpanel.other_provider") : t("chat-modelpanel.other_engine", { from: agentName(engine, s.agent_label), to: agentName(kind) });
    } else if (!modelSwitch) {
      p.unavailable = t("chat-modelpanel.model_fixed", { agent: agentName(engine, s.agent_label) });
    } else {
      if (e.list?.locked) p.locked = t("composer.locked");
      if (typedModelOffered(engine, e.list)) p.typable = true;
    }
    return p;
  });

  const model = row?.id ?? shownModel;
  const state: PanelState = {
    current: model ? cleanSetup({ provider: own, model, effort: s.effort, speed: s.service_tier && speedId(s.service_tier) }) : null,
    // the models that switch at once: the session's own launcher's
    recommended: modelSwitch ? [own] : [],
  };
  const ps = s.pending_switch;
  if (ps?.launcher && ps.launcher !== own) {
    state.pending = cleanSetup({ provider: ps.launcher, model: ps.model || defaultModel(listOf(ps.launcher).list?.models ?? [])?.id || "", effort: ps.effort });
    // to the other engine: Cindy's warning, only while such a switch waits (model-selector-unified.md 1.6)
    state.note = pendingEngine(s) ? t("chat-modelpanel.engine_note") : t("chat-modelpanel.pending_note", { provider: ps.launcher });
  }
  const busy = modelSwitch ? modelBusy(s) : "";
  if (busy) state.busy = busy;
  return { providers, state };
}

/** One POST …/modes of a pick. `launcher`: a switch to another launcher with the user's next message (#5035), or the
 *  session's own to drop one that waits; `from` the launcher the session runs on as the panel saw it. */
export type ModeStep = { model: string } | { effort: string } | { service_tier: string } | { launcher: string; from: string; model?: string; effort?: string };

/** What applying `config` sends, in order. On the session's own launcher: a switch that waits is dropped first, then the
 *  model (the efforts a model takes depend on it), then the effort, then the speed; each only when it differs from the
 *  session's. On another launcher of the engine (a hub with the switch), or of the other engine (a hub that moves the
 *  conversation there, #5054): one request recording the switch, with the model unless it is that launcher's default
 *  entry, and the effort when the setup has one. null: it can't be applied here (another launcher without the switch,
 *  another engine without its switch, a model this agent can't switch to while it runs, an ID the hub would refuse). An
 *  Antigravity variant brings its own thinking level. `listOf`: each launcher's list as read. */
export function pickPlan(s: Session, config: PanelConfig, listOf: (launcher: string) => LauncherModels | null | undefined, o: PanelOptions = {}): ModeStep[] | null {
  const own = sessionLauncher(s);
  if (config.provider !== own) return switchPlan(s, config, listOf(config.provider), o);
  const models = listOf(own)?.models ?? [];
  const row = models.find((m) => m.id === config.model);
  const current = currentModelRow(models, s.model);
  const same = row ? row === current || isCurrentModel(row, s.model) : config.model === s.model;
  if (!same && (!canSwitchModel(s) || (!row && !validModelId(config.model)))) return null;
  const steps: ModeStep[] = [];
  if (s.pending_switch?.launcher && s.pending_switch.launcher !== own) steps.push({ launcher: own, from: own });
  if (!same) steps.push({ model: config.model });
  const effortAfterModel = (!same && sessionEngine(s) === "agy" ? row?.default_effort : undefined) || s.effort;
  if (config.effort && config.effort !== effortAfterModel) steps.push({ effort: config.effort });
  // The advertised tiers belong to the running model. A different model is picked on its own.
  const speed = same ? speedStep(s, config.speed) : undefined;
  if (speed) steps.push(speed);
  return steps;
}

/** The engine launcher `name` runs: what the panel's providers say, else its list's agent; undefined when neither knows. */
export function launcherEngine(name: string, list: LauncherModels | null | undefined, o: PanelOptions): string | undefined {
  return o.engineOf?.(name) || (list?.agent ? agentId(providerAgent(list.agent, name)) : undefined);
}

/** A pick on another launcher: the switch the next message carries out; nothing when that very switch waits already. */
function switchPlan(s: Session, config: PanelConfig, list: LauncherModels | null | undefined, o: PanelOptions): ModeStep[] | null {
  const kind = launcherEngine(config.provider, list, o) ?? sessionEngine(s);
  if (kind === sessionEngine(s) ? !switchesLauncher(s, o) : !switchesEngine(s, kind, o)) return null;
  const row = list?.models.find((m) => m.id === config.model);
  if (!row && !validModelId(config.model)) return null;
  const model = row?.default ? undefined : config.model;
  if (model && list?.locked) return null;
  const step: { launcher: string; from: string; model?: string; effort?: string } = { launcher: config.provider, from: sessionLauncher(s) };
  if (model) step.model = model;
  if (config.effort) step.effort = config.effort;
  const ps = s.pending_switch;
  if (ps && ps.launcher === step.launcher && (ps.model || undefined) === step.model && (ps.effort || undefined) === step.effort) return [];
  return [step];
}

/** Speed always travels alone. An unsupported saved tier cannot turn into a different speed request. */
export function speedStep(s: Session, speed: string | undefined, always = false): { service_tier: string } | undefined {
  if (!speed || activityOf(s) === "offline" || !sessionCan(s, "send")) return undefined;
  const tier = speedTier(s, speed);
  if (!tier || (!always && speedId(s.service_tier ?? "") === speed)) return undefined;
  return { service_tier: tier };
}
