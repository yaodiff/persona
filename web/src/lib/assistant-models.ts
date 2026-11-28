import { assistantSwitchNeeded } from "./assistant-execution";
// What a Persona chat's model pill says (#5033, docs/web/web.md "Model pill and model panel"; docs/assistant/model-config.md
// "A conversation's own model"): the launcher, model and effort its next message runs on, and where that comes from.
// A Persona chat switches models freely: its own pick is kept on the hub (`model_pick`, feature `assistant_model_pick`)
// and holds from the next message on every device, for every Persona chat (#9248); without one the main chat, a leader
// and the background supervisor follow Model configuration › Main chat and another chat keeps what it started with. On an older hub a pick lasts for this page and goes with each message
// (lib/assistant-chat.ts), as before; the new-conversation page's pick goes with the conversation's start. First-screen
// code: the panel's own data is lib/assistant-panel.ts, loaded with the panel.
import { agentId, isMarked, providerAgent, sessionAgent } from "./agent-kind";
import { explicitLauncherModel } from "./compatibility";
import { conversationKey, launcherOffered, NEW_CONVERSATION, pickedEffort, pickedLauncher, pickedModel, pickedServiceTier, type AssistantLauncher } from "./assistant-chat";
import type { LauncherDefault } from "./launcher-models";
import { providerTitle } from "./provider-names";
import { currentModelRow, pillModel, sessionModelHost, type AssistantLaunchers, type ModelList, type PillView } from "./session-models";
import { speedId, startSpeedTier } from "./session-speed";
import type { ModelPick, Session } from "./types";

/** The hub keeps a Persona chat's pick (`model_pick`, PUT / DELETE …/conversations/{id}/model). */
export const ASSISTANT_MODEL_PICK_FEATURE = "assistant_model_pick";

/** The hub accepts a native service tier with a conversation's first message. */
export const ASSISTANT_START_SERVICE_TIER_FEATURE = "assistant_start_service_tier";

/** A Persona chat as its pill sees it. */
export interface AssistantChat {
  /** Absent: the new-conversation page. */
  session?: Session;
  isMain: boolean;
  /** Without a pick, the next message runs on Model configuration › Main chat rather than on what the conversation ran
   *  before: the main conversation, a leader and the background supervisor (hub: sendModel). Absent: the main one only. */
  followsDefault?: boolean;
  /** The default it follows when that is not Main chat's: the background supervisor's own model purpose (`assistant.supervise`,
   *  which follows Main chat until it is set apart). Absent: the one `?purpose=assistant` gave. */
  def?: LauncherDefault | null;
  /** The hub keeps picks (ASSISTANT_MODEL_PICK_FEATURE). */
  kept: boolean;
  /** The assistant's own launcher (AssistantInfo.launcher), for when nothing else says. */
  config?: string;
}

/** Where a chat's setup comes from: its own pick; the default (the main chat, the new-conversation page); or what it
 *  started with (another chat). */
export type SetupSource = "pick" | "default" | "own";

/** What the chat's next message runs on: model absent is the launcher's default entry, effort absent the default's. */
export interface AssistantSetup {
  provider: string;
  model?: string;
  effort?: string;
  /** Speed selected for this start, or acknowledged on the running native thread. */
  speed?: string;
  source: SetupSource;
}

/** The key of this page's picks for the chat (an older hub's, the new-conversation page's). */
export const assistantPickKey = (c: Pick<AssistantChat, "session">) => (c.session ? conversationKey(c.session.machine_id, c.session.session_id) : NEW_CONVERSATION);

/** The chat's own pick: `model_pick` on a hub that keeps it, else this page's (an older hub, the new-conversation page). */
export function assistantPick(c: AssistantChat): ModelPick | undefined {
  if (c.session && c.kept) return c.session.model_pick?.launcher ? c.session.model_pick : undefined;
  const key = assistantPickKey(c);
  const launcher = pickedLauncher(key);
  if (!launcher) return undefined;
  const out: ModelPick = { launcher };
  const model = pickedModel(key);
  const effort = pickedEffort(key);
  if (model) out.model = model;
  if (effort) out.effort = effort;
  return out;
}

export interface MainChatAgentMismatch {
  configuredLauncher: string;
  configuredAgent: string;
  actualLauncher: string;
  actualAgent: string;
}

// Model configuration prefixes launcher providers; a session names the same launcher without that prefix.
export const canonicalLauncher = (name: string) => name.startsWith("launcher.") ? name.slice("launcher.".length) : name;

/** Looks up a launcher in the assistant catalog by exact name or canonical name without the `launcher.` prefix. */
export function findAssistantLauncher(info: AssistantLaunchers | undefined, name: string | undefined): AssistantLauncher | undefined {
  if (!info || !name) return undefined;
  return info.launchers.find((l) => l.name === name)
    ?? info.launchers.find((l) => canonicalLauncher(l.name) === canonicalLauncher(name));
}

/** The launcher a running Persona session actually uses: its reported launcher, else its runtime instance or non-Claude engine. */
export function sessionActualLauncher(s: Session): string | undefined {
  if (s.launcher) return s.launcher;
  if (s.source_kind === "runtime" && s.source_instance_id) return s.source_instance_id;
  if (s.source_kind === "codex_app_server" || s.source_kind === "codex_history") return "codex";
  const agent = agentId(sessionAgent(s).agent);
  return agent !== "claude" ? agent : undefined;
}

/** Shared catalog host key for an assistant launcher on this conversation: the AGY companion host only for that session's own AGY launcher. */
export function assistantModelHost(session: Session | undefined, provider: string): "agy" | undefined {
  if (!session) return undefined;
  const host = sessionModelHost(session);
  if (!host) return undefined;
  const actual = sessionActualLauncher(session);
  return actual && canonicalLauncher(actual) === canonicalLauncher(provider) ? host : undefined;
}

function configuredAgentOf(
  configuredLauncher: string,
  info: AssistantLaunchers | undefined,
): { agent?: string; launcher?: AssistantLauncher; known: boolean } {
  const launcher = findAssistantLauncher(info, configuredLauncher);
  if (launcher) {
    return {
      agent: agentId(providerAgent(launcher.agent_type || launcher.agent, launcher.name)),
      launcher,
      known: true,
    };
  }
  const unavailable = info?.unavailable.find((u) => u.name === configuredLauncher || canonicalLauncher(u.name) === canonicalLauncher(configuredLauncher));
  if (unavailable) {
    const raw = providerAgent(unavailable.agent, unavailable.name);
    const bare = canonicalLauncher(unavailable.name);
    const guessed = raw ? agentId(raw) : (bare && isMarked(agentId(bare)) ? agentId(bare) : undefined);
    return { agent: guessed, known: Boolean(guessed) };
  }
  const bare = canonicalLauncher(configuredLauncher);
  const id = agentId(bare);
  if (bare && isMarked(id)) return { agent: id, known: true };
  return { known: false };
}

/** A confirmed Main-chat-setting mismatch: both launchers are known and their agent kinds differ. An unloaded list,
 *  an unknown default, another launcher of the same kind, or a conversation's own pick changes nothing. */
export function mainChatAgentMismatch(c: AssistantChat, info: AssistantLaunchers | undefined): MainChatAgentMismatch | undefined {
  const s = c.session;
  const actualLauncher = s ? sessionActualLauncher(s) : undefined;
  if (!s || !c.isMain || !actualLauncher || !info || assistantPick(c)) return undefined;
  const configuredLauncher = (c.def === undefined ? info.def : c.def)?.provider || c.config;
  if (!configuredLauncher || canonicalLauncher(configuredLauncher) === canonicalLauncher(actualLauncher)) return undefined;
  const configured = findAssistantLauncher(info, configuredLauncher);
  if (!configured) return undefined;
  const configuredAgent = agentId(providerAgent(configured.agent_type || configured.agent, configured.name));
  const actualAgent = agentId(sessionAgent(s).agent);
  if (configuredAgent === actualAgent && !assistantSwitchNeeded(s, configured, configuredAgent)) return undefined;
  return { configuredLauncher, configuredAgent, actualLauncher, actualAgent };
}

/** Whether a running default-following conversation runs on an engine/runtime that cannot silently follow the configured
 *  default (including before the assistant catalog finishes loading or when the configured engine is unavailable). */
function runningConnectionProvider(c: AssistantChat, info: AssistantLaunchers | undefined, def: LauncherDefault | null | undefined): string | undefined {
  const s = c.session;
  if (!s || !(c.followsDefault ?? c.isMain)) return undefined;
  const actualLauncher = sessionActualLauncher(s);
  const actualAgent = agentId(sessionAgent(s).agent);
  const configuredLauncher = def?.provider || c.config;
  if (!configuredLauncher) return undefined;
  if (actualLauncher && canonicalLauncher(configuredLauncher) === canonicalLauncher(actualLauncher)) return undefined;
  if (!actualLauncher && actualAgent === "claude") return undefined;
  const cfg = configuredAgentOf(configuredLauncher, info);
  if (!cfg.known || !cfg.agent) return undefined;
  if (cfg.agent === actualAgent && (!cfg.launcher || !assistantSwitchNeeded(s, cfg.launcher, cfg.agent))) return undefined;
  return actualLauncher || actualAgent;
}

/** The setup the chat's next message runs on (as the hub decides it, assistant/start.go sendModel). */
export function assistantSetup(c: AssistantChat, info: AssistantLaunchers | undefined): AssistantSetup {
  const s = c.session;
  const pick = assistantPick(c);
  const tier = s ? s.service_tier : pickedServiceTier();
  const speed = tier ? { speed: speedId(tier) } : {};
  // An Antigravity variant pick carries its effort in the new model ID.
  const sessionEffort = (model?: string) => s?.agent === "agy" && model && model !== s.model ? undefined : s?.effort;
  if (pick) return { provider: pick.launcher, model: pick.model, effort: pick.effort ?? sessionEffort(pick.model), ...speed, source: "pick" };
  const def = c.def === undefined ? info?.def : c.def;
  const mismatch = mainChatAgentMismatch(c, info);
  if (s && mismatch) return { provider: mismatch.actualLauncher, model: s.model, effort: s.effort, ...speed, source: "own" };
  const keepRunning = runningConnectionProvider(c, info, def);
  if (s && keepRunning) return { provider: keepRunning, model: s.model, effort: s.effort, ...speed, source: "own" };
  if (!s || (c.followsDefault ?? c.isMain)) {
    const actual = s ? sessionActualLauncher(s) : undefined;
    const provider = def?.provider || c.config || actual || "claude";
    const sameActual = Boolean(s && (s.launcher === provider || (!s.launcher && actual && canonicalLauncher(actual) === canonicalLauncher(provider))));
    const model = def?.model || (sameActual ? s?.model : undefined);
    const effort = sessionEffort(model);
    return { provider, model, effort: c.isMain ? def?.effort || effort : effort ?? (def?.effort || undefined), ...speed, source: "default" };
  }
  // a launcher the list no longer offers is not what the next message runs on: the hub moves the chat to the assistant's
  const actual = sessionActualLauncher(s);
  const candidate = s.launcher || actual;
  const own = candidate && (launcherOffered(info?.launchers ?? null, candidate) || (!info && actual)) ? candidate : undefined;
  return { provider: own || c.config || "claude", model: own ? s.model : undefined, effort: s.effort, ...speed, source: "own" };
}

/** What the pill draws: the launcher (its name for people), the model as its list names it (none set: its default entry,
 *  else the model it is configured with), the effort and speed. */
export function assistantPillView(c: AssistantChat, setup: AssistantSetup, info: AssistantLaunchers | undefined, e: ModelList): PillView {
  const launcher = findAssistantLauncher(info, setup.provider);
  const actual = c.session ? sessionActualLauncher(c.session) : undefined;
  const providerBare = canonicalLauncher(setup.provider);
  const matchesSession = Boolean(
    c.session && (
      (actual && canonicalLauncher(actual) === providerBare) ||
      (!actual && !launcher && !isMarked(agentId(providerBare)))
    ),
  );
  const launcherEngine = launcher ? providerAgent(launcher.agent_type || launcher.agent, launcher.name) : "";
  const providerMarked = providerBare && isMarked(agentId(providerBare)) ? agentId(providerBare) : "";
  const sessionEng = c.session ? sessionAgent(c.session).agent : "";
  const engine = agentId(
    launcherEngine ||
    e.list?.agent ||
    providerMarked ||
    (matchesSession ? sessionEng : "") ||
    sessionEng,
  );
  const sessionModel = setup.source !== "pick" && matchesSession ? c.session?.model : undefined;
  const activeModel = setup.model || sessionModel;
  const id = activeModel || explicitLauncherModel(launcher?.model);
  const models = e.list?.models ?? [];
  let row = currentModelRow(models, activeModel) ?? (id !== activeModel ? currentModelRow(models, id) : undefined);
  let displayActive = activeModel;
  let displayFallback = id;
  // When following a default without an explicit "default" pick, prefer the concrete resolved model over a generic label.
  if (!activeModel && setup.source !== "pick" && row?.id === "default" && row.resolved) {
    const resolvedId = row.resolved;
    const resolvedRow = currentModelRow(models, resolvedId);
    if (resolvedRow && resolvedRow.id !== "default") {
      row = resolvedRow;
      displayActive = resolvedId;
      displayFallback = resolvedId;
    } else {
      row = undefined;
      displayActive = resolvedId;
      displayFallback = resolvedId;
    }
  }
  const out: PillView = {
    ...pillModel(row, displayActive, displayFallback),
    engine,
    provider: providerTitle(launcher ?? { name: providerBare }),
    loading: e.list === undefined && Boolean(e.loading),
    failed: e.list === undefined && Boolean(e.error),
  };
  if (c.session?.agent_label && matchesSession && engine === agentId(sessionEng)) out.engineLabel = c.session.agent_label;
  if (setup.effort) out.effort = setup.effort;
  if (setup.speed && (c.session || startSpeedTier(row, setup.speed))) out.speed = setup.speed;
  return out;
}
