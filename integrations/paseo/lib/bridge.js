// The Paseo bridge (docs/integrations/paseo.md): the agents of the Paseo daemon on this machine as the sessions of its engine in
// Persona, through the runtime SDK. Paseo runs every agent through its SDK, with no terminal; the bridge follows its
// directory of agents (one subscription), its pending requests (cards on the phone, withdrawn when answered in Paseo),
// and — only while the phone watches a session — that agent's timeline (live text; items for agents that are not
// Claude Code's). An agent running Claude Code names its transcript: the node shows it from there, as in herdr.
// Everything Paseo-specific comes in through deps.paseo, so the tests drive it with a fake daemon.

import { createHash, randomUUID } from 'node:crypto'
import { execFile } from 'node:child_process'
import { readFileSync, realpathSync, statSync } from 'node:fs'
import { homedir } from 'node:os'
import { basename, dirname, isAbsolute, join, resolve, sep } from 'node:path'
import { promisify } from 'node:util'
import { createRuntime, interactionId, light, page, RpcError, snippet } from '../../runtime-sdk/index.js'
import {
  AUTO_ACCEPT, AUTO_ACCEPT_ROOTS, CHECK_LABEL, EFFORTS, KEPT, KEPT_LABEL, RUNTIME, SESSION_ID, SESSION_LABEL, WORKSPACE_ID, agentRoot, agentRow,
  appAgent, archivedUnderOwnId, asksForTool, autoAcceptOf, claudeId, codexThreadOf, defaultProvider, effortFor, formModel, interactionOf,
  launchersOf, linkedWorktree, parentAgentId, paseoCheckouts, paseoMode, projectSpaces, promptKey, providerRoots, responseOf, rewindOf,
  sameKindProvider, sessionIdOf, slotNameOf, spaceIdOf, strayRecord, timelineItems, transcriptOf, workspacePinTime, workspaceRow,
} from './map.js'
import { createLineage } from './lineage.js'
import { createBackgroundWork } from './background-work.js'
import { personaMcpServers } from './mcp.js'
import { PaseoClient } from './paseo.js'

const AWAY_GRACE = 30_000 // ms: Paseo gone this long and the bridge leaves the node (its sessions go with it)
const CALL_WAIT = 25_000 // ms: the node waits 30 s for a start or a send; Paseo's answer may take longer
// A start answered before Paseo was done with it (start, takeOver: CALL_WAIT passed, or the connection dropped) goes on
// in the background this long at most, Paseo away and back included; then it counts as failed. A failed one's row
// says why this long, as the node's own failed starts do (node/starting.go startFailedFor).
const START_GIVE_UP = 10 * 60_000 // ms
const START_FAILED_FOR = 10 * 60_000 // ms
const LIST_MAX = 300 // sessions listed at most (those running or waiting always)
const REFRESH_DELAY = 120 // ms: a watched agent's timeline is read again this long after it moved
const LAST_TEXT_DELAY = 3000 // ms: an agent's last words are read again this long after it changed
const PAGE = 200 // agents in a page of Paseo's directory
const PAGES_MAX = 10 // directory pages read at connect
const FEATURES_MAX = PAGE * PAGES_MAX // agents whose last features are kept at most: as many as the directory is read for
// Agents archived in Paseo have their Claude session named to the node this long: its list of sessions not running
// goes back 7 days. The ones archived before the bridge was told are read at connect (loadArchived).
const UNLISTED_DAYS = 8
const ARCHIVED_MAX = 500 // archived agents kept (the latest)
// Those that keep their own id (map.js archivedUnderOwnId) are rows of the bridge's own, not running, beside the
// LIST_MAX: as many as the node lists sessions not running, the latest 80 archived in the last 7 days.
const ARCHIVED_DAYS = 7
const ARCHIVED_ROWS = 80
// Paseo 0.15 tells every client that an agent was deleted, archived ones too (the list's removal: whatBecameOf). Should
// that be missed (Paseo 0.14 told only the client that deleted it), an agent that ran is seen closed, and Paseo is asked
// for it this long after, twice: it closes an agent before it removes its record. And the directory is read again this
// often (sweep), which also takes what Paseo has now of an agent whose update was lost.
const CLOSED_CHECKS = [1000, 5000] // ms
const SWEEP_EVERY = 120_000 // ms
// Whole timelines kept at most (the most recently read; a watched agent's stays whatever the count): each is an
// agent's whole conversation, and the directory holds up to PAGES_MAX x PAGE agents.
export const TIMELINES_MAX = 16
const EVENTS = ['agent_permission_request', 'agent_permission_resolved', 'providers_snapshot_update', 'status.daemon_config_changed']
const TIMEOUT = Symbol('timeout')
// workspace.create waits for git worktree add (Paseo gives it 2 minutes), not for the workspace's setup, within the node's
// 140 s for the call.
const CREATE_WAIT = 135_000 // ms
const readGit = promisify(execFile)
// workspace.archive waits for Paseo to remove a worktree it made (its worktree removal takes 2 minutes at most, then
// its teardown), within the node's 140 s for the call.
const ARCHIVE_WAIT = 135_000 // ms
const WORKSPACES_DELAY = 200 // ms: workspace updates come in bursts (each agent's turn moves its workspace's status)
// What Paseo 0.13 says when an agent config has MCP servers and its provider takes none (agent-manager requireExternalMcpSupport).
const MCP_UNSUPPORTED = /does not support MCP servers/i
// What Paseo says when a provider cannot preapprove MCP tools (provider-options ToolPolicyUnsupportedError, agent-manager).
const POLICY_UNSUPPORTED = /cannot preapprove exact MCP tools/i
const REWIND_WAIT = 120_000 // ms: Paseo answers a rewind once it is done (a Claude Code fork, files put back)
// A card's time on the phone, asked for (the node's own most, 30 minutes): Paseo waits on a request for as long as it
// takes, so a card whose time ran out goes up again (dismissed).
const CARD_LIFE = 30 * 60 // s
const EXPIRY_SLACK = 60_000 // ms: a dismissal this near the end is its time running out (the hub's clock may be ahead)
// The hub answers at once, from its record, for a card it ended already (a gap, or a bridge before this one, left it
// behind); a person takes longer to see a card and hand it back.
const AT_ONCE = 5000 // ms
// A launcher check (check): the probe's answer within this, and the rest of the node's 90 s for creating and cleaning up.
const CHECK_WAIT = 70_000 // ms
// A read of an agent Paseo let go that no watch asked for (timeline): refused as not_loaded, which the hub words.
const NOT_LOADED = "This agent isn't running in Paseo: its conversation is read while its chat is open. Nothing was read."

/**
 * @param {{ paseo?: object, log?: (msg: string) => void, connect?: (path: string) => import('node:net').Socket }} [deps]
 *   paseo: a PaseoClient (default: one for options.home).
 * @param {{ home?: string, socketPath?: string, mcpConfig?: string, allowTools?: string[], retryMs?: number, pushDelayMs?: number,
 *   refreshMs?: number, awayGrace?: number, callWait?: number, startGiveUp?: number, listMax?: number, pageSize?: number,
 *   closedChecks?: number[], sweepMs?: number, claudeHome?: string, claudeEnv?: object, resumeWaitMs?: number, atOnceMs?: number }} [options]
 *   allowTools: the Persona tools (full MCP names) an Agent Persona created calls without a card (the node's, #4966).
 *   startGiveUp: how long a start answered before Paseo was done goes on in the background (START_GIVE_UP).
 *   sweepMs: 0 never reads the directory again.
 *   resumeWaitMs: the runtime SDK's wait for the next node under a card (tests). atOnceMs: how soon after a card went up
 *   (or the hub was back) a dismissal is the hub's own answer, not a person's (AT_ONCE; tests).
 *   claudeHome, claudeEnv: where Claude Code's transcripts are looked for (lineage.js): home's .claude and env's
 *   $CLAUDE_CONFIG_DIR (default: this process's).
 */
export function createPaseoBridge(deps = {}, options = {}) {
  const log = deps.log ?? (() => {})
  const paseo = deps.paseo ?? new PaseoClient({ home: options.home, log })
  const awayGrace = options.awayGrace ?? AWAY_GRACE
  const callWait = options.callWait ?? CALL_WAIT
  const startGiveUp = options.startGiveUp ?? START_GIVE_UP
  const listMax = options.listMax ?? LIST_MAX
  const pageSize = options.pageSize ?? PAGE
  const closedChecks = options.closedChecks ?? CLOSED_CHECKS
  const sweepMs = options.sweepMs ?? SWEEP_EVERY
  const atOnce = options.atOnceMs ?? AT_ONCE

  const agents = new Map() // agent id → { agent, project }
  const bySession = new Map() // session id → agent id
  const background = createBackgroundWork({
    request: (id) => paseo.request('agent.background_work.list.request', { agentId: id }, { timeoutMs: 30_000 }),
    changed: () => rt.sessionsChanged(), log,
  })
  let configProviders = {} // provider id → { extends, label } (nothing else of the daemon's config is kept)
  let entries = [] // the providers snapshot
  let roots = {}
  let labels = {} // provider id → its label
  let launchers = []
  const watches = new Map() // session id → watch (see watchChanged)
  // permission id → its card while it is on the phone, and then while its answer is on its way to Paseo (askFor):
  // { agentId, abort, round, fresh, since, askedAt, epoch, answer?, sending? }
  const asks = new Map()
  const allowTools = new Set(options.allowTools ?? [])
  const handedBack = new Set() // permission ids a person handed back to the computer: Paseo's own app answers them
  // permission id → { agentId, round, used, since }: the round its card goes up in, whether that round's id went up
  // already, and when it first did here. Round 0's id is the one any bridge gives the request (a card a node restart
  // left on the hub is taken up again under it); a later round's is this bridge's own (boot), never one an earlier
  // bridge used.
  const rounds = new Map()
  const boot = randomUUID()
  let hubEpoch = 0 // bumped whenever the node or its hub comes or goes (onHub): a card up across that may be ended there
  let hubBackAt = 0 // when a node with a hub was last there again
  const echoes = new Map() // Paseo message id → the client_msg_id the phone sent it with
  const lastTexts = new Map() // agent id → its last words (agents whose content is not a transcript)
  const featuresSeen = new Map() // agent id → the features Paseo last listed for it, the latest last (withFeatures)
  // agent id → { agent, project, transcript?, at }: archived Claude agents and Persona-owned Codex agents (noteArchived),
  // the latest last
  const archived = new Map()
  let archiveRead = false // Paseo's archive was read since the bridge started: before that it cannot say which they are
  const deleted = new Set() // agents Paseo no longer has (whatBecameOf): an update of one that comes late is not it back
  const closedTimers = new Set()
  let sweepTimer
  let sweeping = false
  const lastTextTimers = new Map()
  const rewinding = new Set() // agent ids a rewind is under way for
  // agent id → the session id it keeps (withKept) until Paseo's record has the label; pinning: those whose label is on
  // its way to Paseo (pin). loading: the directory is being read, the lines of the agents it lists are read after it.
  const keptIds = new Map()
  const pinning = new Set()
  let loading = false
  // The readings of Paseo's directories under way (loadAgents, loadWorkspaces, sweep), each { kind: 'agents' |
  // 'workspaces', told: the ids Paseo told of meanwhile, cards: whether a card's event counts }. A page can be older
  // than what Paseo told while it was read, so a reading takes no page's copy of those (told).
  const readings = new Set()
  const lineage = createLineage({ dirs: () => Object.values(configProviders).map((p) => p.configDir).filter(Boolean),
    home: options.claudeHome, env: options.claudeEnv })
  const timelines = new Map() // agent id → { key, at, tl, history } (the last whole read), the least recently read first
  const canonical = new Map() // path → the path with symbolic links resolved (canon)
  // Paseo's workspaces (its directory of them, followed like its agents): id → WorkspaceDescriptorPayload. Those with
  // no agents too; not background ones, which Paseo shows nobody unless asked.
  const workspaces = new Map()
  let workspacesRead = false // read since the bridge connected to this Paseo: before that, an unknown id is no news
  let workspacesTimer
  let workspacesSaid // the rows told to the node last (JSON), so an update that changes nothing it lists says nothing
  // session id → a start the node was answered before Paseo was done with it (start, takeOver; #8130): { row, work,
  // failed, again, readonly }. work: the agent once Paseo has it. Its row is listed until then (inFlightRows), and a
  // message to it waits for the agent (send); once Paseo refused it, failed: { error, at } says why on that row for
  // START_FAILED_FOR.
  const inFlight = new Map()
  // Session ids and agent ids currently starting or resuming with permission_mode='plan' (reference-counted):
  // an unsolicited non-plan agent_update from Paseo must not expose a writable agent before verification.
  const pendingReadonly = new Map()
  const pendingReadonlyAgents = new Map()
  // Active start/takeOver operations from T=0 until settled (closing the 0..callWait blind spot before inFlight).
  const pendingOps = new Map()
  const beginReadonly = (sid) => { if (sid) pendingReadonly.set(sid, (pendingReadonly.get(sid) ?? 0) + 1) }
  const endReadonly = (sid) => {
    if (!sid) return
    const n = (pendingReadonly.get(sid) ?? 0) - 1
    if (n > 0) pendingReadonly.set(sid, n)
    else pendingReadonly.delete(sid)
  }
  const beginReadonlyAgent = (aid) => { if (aid) pendingReadonlyAgents.set(aid, (pendingReadonlyAgents.get(aid) ?? 0) + 1) }
  const endReadonlyAgent = (aid) => {
    if (!aid) return
    const n = (pendingReadonlyAgents.get(aid) ?? 0) - 1
    if (n > 0) pendingReadonlyAgents.set(aid, n)
    else pendingReadonlyAgents.delete(aid)
  }
  const archiveReadonlyViolation = (agentId, reason) =>
    paseo.request('archive_agent_request', { agentId }).catch((err) => {
      log(`paseo: archiving non-readonly agent ${agentId} (${reason}): ${err?.message ?? err}`)
    })
  // The connections to Paseo that came up, and the last one whose directory was read (onPaseo): a request asked again
  // after a reconnect waits for both (whenBack), and first looks there for what the lost answer would have said.
  let connections = 0
  let setUp = 0
  const backWaiters = new Set()
  let runtimeOn = false
  let awayTimer
  let started = false

  const meta = {
    runtime: RUNTIME,
    name: 'Paseo',
    agent: RUNTIME,
    // runtime.hello's version is Paseo's (the engine's version in node.info), read at each hello.
    version: '',
    launchers: [],
    resume: true,
    // start() runs permission_mode plan in Paseo's plan mode or refuses it (holdsPlan).
    readonly: true,
    backend_workspace_root: true,
    // start() runs only Paseo's own providers: a launcher it lacks gets one of the same kind, said on the answer, or a
    // refusal (sameKindProvider); as the machine's engine its providers are all that a start there can run.
    sameKind: true,
    // modes() changes a session's model too (set_agent_model_request).
    modelSwitch: true,
    socketPath: options.socketPath,
    connect: deps.connect,
    log,
    retryMs: options.retryMs,
    pushDelayMs: options.pushDelayMs,
    refreshMs: options.refreshMs,
    resumeWaitMs: options.resumeWaitMs,
    // After every hello and whenever the node's hub comes or goes: a card up across it may have been ended on the hub
    // (dismissed); the requests not on the phone go up once a hub is there.
    onHub: (connected) => {
      hubEpoch++
      if (!connected) return
      hubBackAt = Date.now()
      askPending()
    },
  }
  const rt = createRuntime({
    listSessions, unlistedTranscripts, items, cursor, item, send, interrupt, start, rename, close, modes, rewind, check, models, seen, watchChanged,
    createWorkspace, prepareWorkspace, renameWorkspace, archiveWorkspace,
  }, meta)

  // ---- Paseo's state ----

  function keepProviders(providers) {
    const out = {}
    for (const [id, p] of Object.entries(providers ?? {})) {
      // Only what says which provider is which, and where its Claude Code keeps its sessions (a directory, for the
      // lines of forks, withKept): a provider's other env and its command (keys, gateways) are not kept.
      const dir = p?.env?.CLAUDE_CONFIG_DIR
      out[id] = { extends: typeof p?.extends === 'string' ? p.extends : undefined, label: typeof p?.label === 'string' ? p.label : undefined,
        configDir: typeof dir === 'string' && dir ? dir : undefined }
    }
    return out
  }

  function providersChanged() {
    roots = providerRoots(configProviders, entries)
    labels = {}
    for (const e of entries) if (e?.provider && e.label) labels[e.provider] = e.label
    for (const [id, p] of Object.entries(configProviders)) if (p.label && !labels[id]) labels[id] = p.label
    // One launcher per provider, Paseo's default first (the form's first choice).
    const def = defaultProvider(entries, roots)
    launchers = launchersOf(entries, roots).sort((a, b) => (b.name === def) - (a.name === def))
    rt.launchersChanged(launchers)
    reindex()
  }

  /**
   * Which agent each session id is, when two claim one id: of two on one Claude session (sharedId), the one at work,
   * else the newer, which a message goes to; of any other two, the one whose own id it is (not only kept, withKept),
   * then the one not closed, else the latest.
   */
  function reindex() {
    bySession.clear()
    for (const { agent } of agents.values()) index(agent)
    retarget()
  }

  function index(agent) {
    const id = sessionIdOf(agent, roots)
    if (!id) return
    const cur = agents.get(bySession.get(id))?.agent
    if (cur && cur.id !== agent.id) {
      const shared = claudeId(agent) !== undefined && claudeId(agent) === claudeId(cur)
      const better = shared ? (a) => [atWork(a) ? 1 : 0, Date.parse(a.createdAt) || 0]
        : (a) => [a[KEPT] ? 0 : 1, a.status !== 'closed' ? 1 : 0, Date.parse(a.updatedAt) || 0]
      const [x, y] = [better(agent), better(cur)]
      const i = x.findIndex((v, n) => v !== y[n])
      if (i < 0 || x[i] < y[i]) return
    }
    bySession.set(id, agent.id)
  }

  /** Whether an agent is at work: running, or waiting for an answer (a second one writing its transcript must not start). */
  function atWork(a) {
    return a?.status === 'running' || a?.status === 'initializing' || Boolean(a?.activeTurn) || (a?.pendingPermissions?.length ?? 0) > 0
  }

  function sessionOf(agentId) {
    const a = agents.get(agentId)?.agent
    const id = a ? sessionIdOf(a, roots) : undefined
    return id && bySession.get(id) === agentId ? id : undefined
  }

  function upsert(agent, project) {
    if (!agent?.id || deleted.has(agent.id) || agent.labels?.[CHECK_LABEL]) return // a launcher check's probe is no session
    const prev = agents.get(agent.id)
    if (prev && (Date.parse(prev.agent.updatedAt) || 0) > (Date.parse(agent.updatedAt) || 0) && prev.agent.status === agent.status) return
    if (agent.archivedAt) return remove(agent.id, agent, project)
    if ((pendingReadonly.size > 0 || pendingReadonlyAgents.size > 0) && agent.currentModeId !== 'plan') {
      const rawLabel = agent.labels?.[SESSION_LABEL]
      const explicitLabel = typeof rawLabel === 'string' && SESSION_ID.test(rawLabel) ? rawLabel : undefined
      const baseSid = sessionIdOf(agent, roots)
      const cid = !explicitLabel && agentRoot(agent, roots) === 'claude' ? claudeId(agent) : undefined
      const sharedSid = cid ? sharedId(agent, cid) : undefined
      const prevObj = prev ?? archived.get(agent.id)
      const prevSid = prevObj ? sessionIdOf(prevObj.agent, roots) : undefined
      const keptSid = keptIds.get(agent.id)
      const lineageSid = !explicitLabel && cid && !sharedSid && !keptSid && !loading
        ? lineage.rootOf(cid, { cwd: agent.cwd, createdAt: agent.createdAt, taken: (s) => takenBy(s, agent.id, true) })
        : undefined
      const readonlyPending = pendingReadonlyAgents.has(agent.id)
        || [explicitLabel, baseSid, sharedSid, prevSid, keptSid, lineageSid, cid].some((s) => s && pendingReadonly.has(s))
      if (readonlyPending) {
        void archiveReadonlyViolation(agent.id, `non-plan update (${agent.currentModeId || 'unknown'}) while readonly pending`)
        remove(agent.id)
        return
      }
    }
    agent = withFeatures(withKept(agent, prev ?? archived.get(agent.id), !loading))
    archived.delete(agent.id) // back from the archive: listed again
    const oldSession = prev ? sessionIdOf(prev.agent, roots) : undefined
    agents.set(agent.id, { agent, project: project ?? prev?.project })
    background.observe(agent)
    // Moved to another id, or one of two on a Claude session (its turn ended: the other may be the one again).
    if ((oldSession && oldSession !== sessionIdOf(agent, roots)) || sharesSession(agent)) reindex()
    else {
      index(agent)
      retarget()
    }
    // The agent is its session now: a start of it answered before Paseo was done (track) is over, whatever it said
    // (keep a pending read-only start tracked until its response finishes verification, in case mode downgrades first).
    const sid = sessionOf(agent.id)
    if (sid && !pendingReadonly.has(sid)) inFlight.delete(sid)
    // Its requests: new ones to the phone, those no longer pending off it (and forgotten).
    const pending = new Set((agent.pendingPermissions ?? []).map((r) => r?.id))
    for (const r of agent.pendingPermissions ?? []) askFor(agent.id, r)
    for (const [id, a] of asks) if (a.agentId === agent.id && !pending.has(id)) a.abort()
    for (const [id, c] of rounds) if (c.agentId === agent.id && !pending.has(id)) forgetRequest(id)
    if (agent.status === 'closed' && prev && prev.agent.status !== 'closed') closedSoon(agent.id)
    if (!transcriptOf(agent, roots) && agent.status !== 'closed' && prev?.agent.updatedAt !== agent.updatedAt) lastTextSoon(agent.id)
    for (const [sid, w] of watches) if (w.agentId === agent.id) scheduleRefresh(sid)
    // Its placement names a space, which may file a worktree's own project record under its repository's (spaces).
    if (!prev || prev.project?.projectKey !== agents.get(agent.id)?.project?.projectKey) scheduleWorkspaces()
  }

  /**
   * An agent of Claude Code without Persona's label keeps the session id it was listed under when Paseo moves it to
   * another Claude session (docs/integrations/paseo.md "Session IDs"): Paseo forks the session to rewind it, and so when
   * the user goes on after an API error in its app. Seen here (prev: the copy listed before), the id it had; not seen
   * (the bridge was not running), the first session of the line its transcript was forked from, when the agent began
   * that conversation itself (lineage.js; lines: whether that may be read now). One on a Claude session an older agent
   * has takes that agent's id instead (sharedId). The id rides on the bridge's copy (KEPT) until Paseo's record has the
   * label (pin).
   */
  function withKept(agent, prev, lines) {
    if (agentRoot(agent, roots) !== 'claude' || agent.labels?.[SESSION_LABEL]) {
      keptIds.delete(agent.id)
      return agent
    }
    const now = claudeId(agent)
    if (!now) return agent
    // On a Claude session an agent created before it has: that conversation's id, whatever its own line says.
    const shared = sharedId(agent, now)
    if (shared === now) {
      keptIds.delete(agent.id)
      return unkept(agent)
    }
    if (shared) {
      keptIds.set(agent.id, shared)
      return { ...agent, [KEPT]: shared }
    }
    let id = keptIds.get(agent.id)
    if (!id) {
      const was = prev && !prev.agent.labels?.[SESSION_LABEL] ? sessionIdOf(prev.agent, roots) : undefined
      id = was && was !== now ? was
        : lines ? lineage.rootOf(now, { cwd: agent.cwd, createdAt: agent.createdAt, taken: (s) => takenBy(s, agent.id, true) }) : undefined
    }
    // Another agent's own since (Paseo imported that earlier session as an agent of its own): it keeps its current id.
    if (id && takenBy(id, agent.id, false)) keptIds.delete(agent.id)
    else if (id && id !== now) {
      keptIds.set(agent.id, id)
      return { ...agent, [KEPT]: id }
    }
    return agent
  }

  /**
   * The id of the conversation an agent of Claude Code continues when an agent created before it has the same Claude
   * session (listed or archived): Paseo moved the conversation to another provider, or imported it again, as a new
   * agent beside the old one. The oldest such agent's id (its label, the id kept for it, else its Claude session), so
   * the conversation keeps the id Persona knows it by (its to-do's link, a pin, its archive mark), and the newer agent
   * is the one listed under it (index). Undefined when no agent created before it has that session.
   */
  function sharedId(agent, now) {
    const created = Date.parse(agent.createdAt)
    if (!Number.isFinite(created)) return undefined
    let first
    for (const m of [agents, archived]) {
      for (const [aid, { agent: a }] of m) {
        if (aid === agent.id || claudeId(a) !== now || agentRoot(a, roots) !== 'claude') continue
        const at = Date.parse(a.createdAt)
        if (at < created && (!first || at < Date.parse(first.createdAt))) first = a
      }
    }
    return first ? sessionIdOf(first, roots) : undefined
  }

  /** Whether another agent Paseo lists has an agent's Claude session (sharedId). */
  function sharesSession(agent) {
    const now = claudeId(agent)
    if (!now) return false
    for (const [aid, { agent: a }] of agents) if (aid !== agent.id && claudeId(a) === now) return true
    return false
  }

  /** An agent without the id the bridge kept on its copy (KEPT). */
  function unkept(agent) {
    if (!(KEPT in agent)) return agent
    const { [KEPT]: _, ...rest } = agent
    return rest
  }

  /**
   * What Paseo sends of an agent it has not loaded (Paseo let its process go, Paseo restarted, a change to its stored
   * record) is that record's projection, which lists no features (agent-projections buildStoredAgentPayload). The
   * switches it listed while it had the agent loaded are still set, and Paseo sets them again when it loads the agent:
   * the last ones seen stand in for them, so a Grok row still says Allow all (#8133). A list of features, an empty one
   * too, is what Paseo has now. Kept while the agent is listed (remove drops them), FEATURES_MAX at most; a bridge started
   * while Paseo has not loaded the agent has seen none.
   */
  function withFeatures(agent) {
    if (Array.isArray(agent.features)) {
      featuresSeen.delete(agent.id) // the latest last: the oldest go first when there are too many
      featuresSeen.set(agent.id, agent.features)
      while (featuresSeen.size > FEATURES_MAX) featuresSeen.delete(featuresSeen.keys().next().value)
      return agent
    }
    const seen = featuresSeen.get(agent.id)
    return seen ? { ...agent, features: seen } : agent
  }

  /**
   * Whether an agent other than self (listed or archived) has session id: as its Claude session or its label, and,
   * with kept, as the id the bridge keeps for it.
   */
  function takenBy(id, self, kept) {
    for (const m of [agents, archived]) {
      for (const [aid, { agent }] of m) {
        if (aid !== self && (claudeId(agent) === id || agent.labels?.[SESSION_LABEL] === id || (kept && agent[KEPT] === id))) return true
      }
    }
    return false
  }

  /**
   * The id an agent keeps (withKept) onto Paseo's record, once, when Paseo tells of a change to it (the agent runs or
   * was just moved, so its record changes anyway): its label, beside KEPT_LABEL (not one Persona started). From then on
   * it is the agent's whatever the bridge sees, and keeps it archived. A refusal is tried again at its next change.
   */
  function pin(agent) {
    const id = agent?.[KEPT]
    if (!id || agent.archivedAt || agent.labels?.[SESSION_LABEL] || pinning.has(agent.id) || !paseo.connected) return
    pinning.add(agent.id)
    paseo.request('update_agent_request', { agentId: agent.id, labels: { [SESSION_LABEL]: id, [KEPT_LABEL]: '1' } }).then((res) => {
      if (res?.accepted === false) throw new Error(res.error ?? 'refused')
      log(`paseo: agent ${agent.id} keeps session ${id} (its Claude session moved to ${claudeId(agent)})`)
    }).catch((err) => {
      pinning.delete(agent.id)
      log(`paseo: keeping session ${id} on agent ${agent.id}: ${err.message}`)
    })
  }

  /** The directory was read: the agents it lists that moved while the bridge did not see it keep their line's first id. */
  function keepLines() {
    for (const [id, e] of agents) {
      const agent = withKept(e.agent, undefined, true)
      if (agent !== e.agent) agents.set(id, { ...e, agent })
    }
    reindex()
  }

  /**
   * Paseo's archive was read: an agent on a Claude session that an archived agent created before it has takes that
   * one's id (sharedId), archived or listed, the oldest first so that each finds the ids of those before it.
   */
  function keepShared() {
    const byAge = [...archived.entries()].sort(([, x], [, y]) => (Date.parse(x.agent.createdAt) || 0) - (Date.parse(y.agent.createdAt) || 0))
    for (const [id, u] of byAge) {
      const agent = withKept(u.agent, u, true)
      if (agent !== u.agent) archived.set(id, { ...u, agent })
    }
    keepLines()
  }

  /**
   * An agent leaves the list. record: its record when Paseo archived it, when that is why, with its placement (project;
   * else the one it was listed with).
   */
  function remove(agentId, record, project) {
    background.forget(agentId)
    const a = agents.get(agentId)
    if (record) noteArchived(record, project ?? a?.project)
    if (!a) return
    agents.delete(agentId)
    timelines.delete(agentId)
    lastTexts.delete(agentId)
    featuresSeen.delete(agentId)
    for (const [id, x] of asks) if (x.agentId === agentId) x.abort()
    for (const [id, c] of rounds) if (c.agentId === agentId) forgetRequest(id)
    reindex()
    scheduleWorkspaces()
  }

  /**
   * An agent archived in Paseo (closing a session from Persona archives it) is not in Paseo's list any more, but its
   * conversation is still the user's, and for an agent of Claude Code its transcript is still on this machine: it
   * stays among the sessions not running. Left alone, the node takes it for a script's one-shot run: Paseo runs Claude
   * Code's command line itself, and its transcripts say so (#4521). One whose id is its transcript's is named to the
   * node (unlistedTranscripts); one that keeps an id of its own (a session Persona started) is a row of the bridge's,
   * under that id (archivedRows, #4771). Not an agent another agent created: its creator's conversation says what it
   * did, and an orchestrator's dozens, archived with it, would push the user's own sessions out of that list.
   */
  function noteArchived(agent, project) {
    if (!agent?.id || parentAgentId(agent)) return
    agent = withKept(agent, archived.get(agent.id) ?? agents.get(agent.id), true)
    const transcript = transcriptOf(agent, roots)
    if (!transcript && !archivedUnderOwnId(agent, roots)) return
    const known = archived.get(agent.id)?.project
    archived.delete(agent.id) // the latest last: the oldest go first when there are too many
    archived.set(agent.id, { agent, project: project ?? known, transcript, at: Date.parse(agent.archivedAt) || Date.now() })
    while (archived.size > ARCHIVED_MAX) archived.delete(archived.keys().next().value)
  }

  /**
   * The archived agent listed as session id (archivedRows), the one archived last; undefined for any other id. Its
   * calls reach it whether or not the list has room for its row.
   */
  function archivedOf(sessionId) {
    let found
    for (const u of archived.values()) {
      if (archivedUnderOwnId(u.agent, roots) && sessionIdOf(u.agent, roots) === sessionId) found = u.agent
    }
    return found
  }

  /** The archived agent whose Claude session is transcript (unlistedTranscripts), the one archived last; else undefined. */
  function archivedWith(transcript) {
    let found
    for (const u of archived.values()) if (u.transcript === transcript) found = u.agent
    return found
  }

  /**
   * What became of an agent Paseo's list no longer has, or may no longer have. A removal from the list does not say
   * why: archived, deleted, or only hidden (its provider switched off); of an archived agent kept here, deleted or its
   * record changed. Asked for the agent, Paseo answers with an archived one's record, and with none for a deleted one.
   * Should a removal be missed (Paseo 0.14 told a deletion only to the client that made it), an agent that ran is seen
   * closed (closedSoon), and the directory is read again (sweep). Only an archived agent's session stays
   * (noteArchived): a deleted conversation is gone, as it is in Paseo, and an update of it that comes late does not list
   * it again.
   */
  async function whatBecameOf(agentId) {
    const was = agents.get(agentId)
    let p
    try {
      p = await paseo.request('fetch_agent_request', { agentId })
    } catch (err) {
      log(`paseo: asking what became of agent ${agentId}: ${err.message}`)
      return
    }
    if (!p || p.error) return
    if (!p.agent) {
      deleted.add(agentId)
      if (deleted.size > 1000) deleted.delete(deleted.values().next().value)
      keptIds.delete(agentId)
      const named = archived.delete(agentId)
      if (!agents.has(agentId) && !named) return
      remove(agentId)
    } else if (p.agent.id === agentId && p.agent.archivedAt && agents.get(agentId) === was) {
      // Archived, and nothing was heard of it since Paseo was asked (an update would be it back from the archive).
      if (!was && archived.has(agentId)) return
      remove(agentId, p.agent, p.project ?? undefined)
    } else return
    rt.sessionsChanged()
  }

  /** An agent that ran is closed: stopped, or deleted in another client, which Paseo can say a moment later (whatBecameOf). */
  function closedSoon(agentId) {
    for (const ms of closedChecks) {
      const t = setTimeout(() => {
        closedTimers.delete(t)
        if (agents.get(agentId)?.agent.status === 'closed' && paseo.connected) void whatBecameOf(agentId)
      }, ms)
      t.unref?.()
      closedTimers.add(t)
    }
  }

  /**
   * Paseo's directory read again, for what nothing told of: an agent deleted (whatBecameOf), an update lost or undone by
   * an older copy. Paseo is asked for each agent listed here that the directory no longer has, and for each archived
   * one kept here that its archive no longer has. Asked, not taken as gone: an agent that changed between the reading of
   * two pages is on neither. An agent it lists that differs from the copy here is taken as it has it now, unless Paseo
   * told of the agent while the pages were read (told: the page may be the older then). The archived ones it still has
   * are taken as it has them now: Paseo tells no list of a change to one (a rename in its own app). And its archive is
   * read as at connect until that succeeds once (readArchive).
   */
  async function sweep() {
    if (sweeping || !runtimeOn || !paseo.connected) return
    sweeping = true
    // A card's event counts too: the directory's update that carries the card can come after the page. The agents are
    // listed here, and that update comes (at connect it may never come: loadAgents).
    const reading = { kind: 'agents', told: new Set(), cards: true }
    readings.add(reading)
    try {
      const listed = [...agents.keys()]
      const named = [...archived.keys()]
      const missing = []
      if (listed.length) {
        const there = new Set()
        let changed = false
        let whole = false
        let cursor
        for (let i = 0; i < PAGES_MAX; i++) {
          const p = await paseo.request('fetch_agents_request', { page: cursor ? { limit: pageSize, cursor } : { limit: pageSize } }, { timeoutMs: 60_000 })
          if (!Array.isArray(p?.entries)) break
          for (const e of p.entries) {
            const id = e?.agent?.id
            if (!id) continue
            there.add(id)
            // As upsert keeps it: a page lists no features of an agent Paseo has not loaded, and those here stand (withFeatures).
            const was = agents.get(id)
            const now = Array.isArray(was?.agent.features) && !Array.isArray(e.agent.features) ? { ...e.agent, features: was.agent.features } : e.agent
            if (reading.told.has(id) || JSON.stringify(was?.agent) === JSON.stringify(now)) continue
            upsert(e.agent, e.project ?? undefined)
            if (agents.get(id) !== was) changed = true // one upsert refuses (a launcher check's probe) is no change
          }
          if (!p.pageInfo?.hasMore || !p.pageInfo.nextCursor) {
            whole = true
            break
          }
          cursor = p.pageInfo.nextCursor
        }
        if (changed) rt.sessionsChanged()
        // More agents than are read at connect (or a page Paseo did not give): which of them are gone cannot be told.
        if (whole) missing.push(...listed.filter((id) => !there.has(id) && !reading.told.has(id)))
      }
      if (!archiveRead) await readArchive()
      else if (named.length) {
        const there = new Set()
        let changed = false
        for (const e of await readArchived()) {
          there.add(e.agent.id)
          const u = archived.get(e.agent.id)
          if (u && u.agent.updatedAt !== e.agent.updatedAt) {
            archived.set(e.agent.id, { ...u, agent: withKept(e.agent, u, true), project: e.project ?? u.project })
            changed = true
          }
        }
        if (changed) rt.sessionsChanged()
        missing.push(...named.filter((id) => !there.has(id)))
      }
      background.refresh()
      for (const id of missing) {
        if (!paseo.connected) return
        if (agents.has(id) || archived.has(id)) await whatBecameOf(id)
      }
    } catch (err) {
      log(`paseo: reading its directory again: ${err.message}`)
    } finally {
      sweeping = false
      readings.delete(reading)
    }
  }

  /**
   * The Claude sessions of the agents archived lately that are not in the list again under another agent. Nothing
   * (the node keeps what it was told before) until Paseo's archive was read once (readArchive): the bridge was just
   * started, or that read failed, and it knows none of them yet. Not the transcript of an agent that keeps an id of its
   * own: its row names it (archivedRows), and named here it would be listed under the transcript's id too while that row
   * is not there.
   */
  function unlistedTranscripts() {
    if (!archiveRead) return undefined
    const listed = new Set()
    for (const { agent } of agents.values()) {
      const t = transcriptOf(agent, roots)
      if (t) listed.add(t)
    }
    const since = Date.now() - UNLISTED_DAYS * 86_400_000
    const out = new Set()
    const own = new Set()
    for (const [id, u] of archived) {
      if (u.at < since) archived.delete(id)
      else if (!u.transcript) continue // Codex rows name their thread, never a Claude transcript.
      else if (archivedUnderOwnId(u.agent, roots)) own.add(u.transcript)
      else if (!listed.has(u.transcript)) out.add(u.transcript)
    }
    for (const t of own) out.delete(t)
    return [...out]
  }

  /**
   * The rows of the agents archived lately that keep their own id (map.js archivedUnderOwnId, #4771): not running,
   * the transcript their content, as a closed agent's (the node reads it, and lists no row of its own for it). A
   * message to one goes to the agent, which Paseo takes back from the archive under its labels: the session goes on
   * under the same id. Not one whose id or transcript an agent Paseo lists has (that agent is the session); of two
   * with one id or one transcript, the one archived last. Beside the list's budget: the latest ARCHIVED_ROWS of the
   * last ARCHIVED_DAYS, and those the phone touched.
   */
  function archivedRows(s) {
    const transcripts = new Set()
    const threads = new Set()
    for (const { agent } of agents.values()) {
      const t = transcriptOf(agent, roots)
      if (t) transcripts.add(t)
      const thread = codexThreadOf(agent, roots)
      if (thread) threads.add(thread)
    }
    const ids = new Set()
    const rows = []
    for (const u of [...archived.values()].reverse()) {
      if (!archivedUnderOwnId(u.agent, roots)) continue
      const sid = sessionIdOf(u.agent, roots)
      const thread = codexThreadOf(u.agent, roots)
      if (bySession.has(sid) || ids.has(sid) || transcripts.has(u.transcript) || (thread && threads.has(thread))) continue
      ids.add(sid)
      if (u.transcript) transcripts.add(u.transcript)
      if (thread) threads.add(thread)
      const spaceId = s.idOf(u.project)
      const row = agentRow(u.agent, { project: u.project, roots, labels, spaceId, spaceName: s.names.get(spaceId), canon, archived: true })
      if (row) rows.push(row)
    }
    return rt.recent(rows, { days: ARCHIVED_DAYS, max: ARCHIVED_ROWS })
  }

  // ---- connecting ----

  paseo.on('connected', (info) => void onPaseo(info))
  paseo.on('disconnected', (why) => {
    log(`paseo: disconnected (${why})`)
    background.disconnect()
    // The live text ends with the connection: Paseo tells a new connection none of what was written meanwhile, nor the
    // end of a turn that ended meanwhile. What comes after the reconnect is a live text of its own.
    for (const [sessionId, w] of watches) {
      if (w.live.text) rt.streamEnd(sessionId)
      w.live = { key: '', text: '' }
    }
    clearTimeout(awayTimer)
    // Paseo restarting (an update) comes back in seconds: the sessions stay meanwhile. Longer, and the engine is away.
    awayTimer = setTimeout(() => {
      if (paseo.connected || !runtimeOn) return
      log('paseo: away; leaving the node until it is back')
      rt.stop()
      runtimeOn = false
    }, awayGrace)
    awayTimer.unref?.()
  })

  async function onPaseo(info) {
    clearTimeout(awayTimer)
    const connection = ++connections
    const nativeBackground = info?.features?.agentBackgroundWork === true
    background.connect(nativeBackground)
    const version = String(info?.version ?? '')
    log(`paseo: connected to Paseo ${version || '(version unknown)'}`)
    try {
      const cfg = await paseo.request('get_daemon_config_request', {})
      configProviders = keepProviders(cfg?.config?.providers)
      await paseo.subscribe('session.events.set_subscription.request', { events: nativeBackground ? [...EVENTS, 'agent.background_work.update'] : EVENTS }, onSessionEvent)
      const snap = await paseo.request('get_providers_snapshot_request', {})
      entries = Array.isArray(snap?.entries) ? snap.entries : []
      providersChanged()
      void settleProviders()
      await loadAgents()
      // Its directory is read: what a request asked again after the reconnect waits for (whenBack).
      if (connection === connections) {
        setUp = connection
        for (const w of backWaiters) w.done()
      }
      // Not needed to list and drive its agents: a Paseo that cannot say has its workspaces unlisted, as before.
      workspacesRead = false
      await loadWorkspaces().catch((err) => log(`paseo: reading its workspaces: ${err.message}`))
    } catch (err) {
      log(`paseo: reading its state: ${err.message}; connecting again`)
      paseo.ws?.close(4000, 'setup failed')
      return
    }
    // Paseo's version is the runtime's: a new one says hello again.
    if (runtimeOn && meta.version !== version) {
      rt.stop()
      runtimeOn = false
    }
    meta.version = version
    if (!runtimeOn) {
      runtimeOn = true
      rt.start()
    }
    for (const id of watches.keys()) void follow(id)
    askPending()
    rt.sessionsChanged(0)
    // Not needed to list what Paseo lists, so not waited for: a Paseo that cannot say leaves its archived agents'
    // sessions unnamed meanwhile.
    void readArchive().catch((err) => log(`paseo: reading its archived agents: ${err.message}`))
  }

  /**
   * Paseo's archive read (loadArchived), at connect and by the sweep until that succeeds once: until then the node is
   * told nothing of the archived agents' sessions (unlistedTranscripts), and keeps what it was told before. A read that
   * failed is no news that there are none.
   */
  async function readArchive() {
    await loadArchived()
    archiveRead = true
    keepShared()
    rt.sessionsChanged()
  }

  /**
   * Providers still loading their models (Paseo warms them up when asked): their update comes as an event; asked again
   * meanwhile too, so the form's models come even if it does not.
   */
  async function settleProviders() {
    for (let i = 0; i < 20 && paseo.connected && entries.some((e) => e?.enabled && e.status === 'loading'); i++) {
      await new Promise((r) => setTimeout(r, 1500))
      try {
        const snap = await paseo.request('get_providers_snapshot_request', {})
        if (Array.isArray(snap?.entries) && JSON.stringify(snap.entries) !== JSON.stringify(entries)) {
          entries = snap.entries
          providersChanged()
          rt.sessionsChanged()
        }
      } catch {}
    }
  }

  async function loadAgents() {
    deleted.clear() // the directory is read afresh: what it lists is there
    const seen = new Set()
    // What the subscription tells of an agent while the pages are read is newer than a page's copy of it: no page's
    // copy of those is taken (told), and they count as seen. Paseo sends what changed while it built the first page
    // right after its reply, often in the same read, so it is handled before this goes on with the page; and the end of
    // a turn leaves updatedAt as it was, so upsert cannot tell the older copy (an agent left Working, an answered card
    // back, an archived agent listed again). Only the subscription's own updates count: a card's event can come before
    // the subscription is made, and then no update with the card follows, so skipping the page would keep the copy from
    // before the reconnect (the sweep counts cards).
    const reading = { kind: 'agents', told: new Set(), cards: false }
    readings.add(reading)
    const take = (list) => {
      for (const e of list ?? []) {
        if (!e?.agent?.id) continue
        seen.add(e.agent.id)
        if (!reading.told.has(e.agent.id)) upsert(e.agent, e.project)
      }
    }
    // The lines of forks are read once every agent is known (keepLines): none is followed into another's session.
    loading = true
    try {
      const first = await paseo.subscribe('fetch_agents_request', { subscribe: {}, page: { limit: pageSize } }, onAgentUpdate, { timeoutMs: 60_000 })
      take(first?.entries)
      let info = first?.pageInfo
      for (let i = 1; info?.hasMore && info.nextCursor && i < PAGES_MAX; i++) {
        const p = await paseo.request('fetch_agents_request', { page: { limit: pageSize, cursor: info.nextCursor } }, { timeoutMs: 60_000 })
        take(p?.entries)
        info = p?.pageInfo
      }
      // Gone while Paseo was away.
      if (!info?.hasMore) for (const id of [...agents.keys()]) if (!seen.has(id) && !reading.told.has(id)) remove(id)
    } finally {
      loading = false
      readings.delete(reading)
    }
    keepLines()
  }

  /**
   * The agents archived before the bridge was told (it was not running, or Paseo was away): kept as the ones archived
   * since are (noteArchived).
   */
  async function loadArchived() {
    for (const e of (await readArchived()).reverse()) if (!agents.has(e.agent.id)) noteArchived(e.agent, e.project)
  }

  /**
   * The agents archived lately, the latest first, with their placements ({ agent, project }): the directory leaves
   * them out unless asked, and then lists them among the others. Read newest first, until they are older than their
   * sessions are kept for (noteArchived).
   */
  async function readArchived() {
    const since = Date.now() - UNLISTED_DAYS * 86_400_000
    const found = []
    let cursor
    for (let i = 0; i < PAGES_MAX; i++) {
      const p = await paseo.request('fetch_agents_request', { filter: { includeArchived: true }, sort: [{ key: 'updated_at', direction: 'desc' }],
        page: cursor ? { limit: pageSize, cursor } : { limit: pageSize } }, { timeoutMs: 60_000 })
      const list = p?.entries ?? []
      for (const e of list) if (e?.agent?.id && e.agent.archivedAt) found.push({ agent: e.agent, project: e.project ?? undefined })
      const oldest = Date.parse(list.at(-1)?.agent?.updatedAt)
      if (!p?.pageInfo?.hasMore || !p.pageInfo.nextCursor || !(oldest > since)) break
      cursor = p.pageInfo.nextCursor
    }
    return found
  }

  /**
   * Paseo's workspaces, followed as its agents are (one subscription, the first page with it, the rest read after): its
   * checkouts, worktrees and folders, agents in them or not (docs/integrations/paseo.md "Spaces and workspaces").
   */
  async function loadWorkspaces() {
    const seen = new Set()
    // As for the agents (loadAgents): what the subscription tells of a workspace while the pages are read (one archived,
    // one made) is newer than a page's copy, which is not taken for it; it counts as seen.
    const reading = { kind: 'workspaces', told: new Set(), cards: false }
    readings.add(reading)
    const take = (list) => {
      for (const d of list ?? []) if (reading.told.has(d?.id) || keepWorkspace(d)) seen.add(d.id)
    }
    let info
    try {
      const first = await paseo.subscribe('fetch_workspaces_request', { subscribe: {}, page: { limit: pageSize } }, onWorkspaceUpdate, { timeoutMs: 60_000 })
      take(first?.entries)
      info = first?.pageInfo
      for (let i = 1; info?.hasMore && info.nextCursor && i < PAGES_MAX; i++) {
        const p = await paseo.request('fetch_workspaces_request', { page: { limit: pageSize, cursor: info.nextCursor } }, { timeoutMs: 60_000 })
        take(p?.entries)
        info = p?.pageInfo
      }
      // Gone while Paseo was away.
      if (!info?.hasMore) for (const id of [...workspaces.keys()]) if (!seen.has(id) && !reading.told.has(id)) forgetWorkspace(id)
    } finally {
      readings.delete(reading)
    }
    workspacesRead = true
    scheduleWorkspaces(0)
  }

  /** Keep a workspace Paseo lists; one being archived or in the background is none of the list's. */
  function keepWorkspace(d) {
    if (typeof d?.id !== 'string' || !d.id) return false
    if (d.background || d.archivingAt) {
      forgetWorkspace(d.id)
      return false
    }
    const changed = Boolean(workspacePinTime(workspaces.get(d.id))) !== Boolean(workspacePinTime(d))
    workspaces.set(d.id, d)
    // Retention reads this map before the coalesced catalog push. A quick pin then unpin can leave that catalog
    // unchanged but must still release conversations retained by an intervening session push.
    if (changed) rt.sessionsChanged()
    return true
  }

  function forgetWorkspace(id) {
    const pinned = workspacePinTime(workspaces.get(id))
    workspaces.delete(id)
    if (pinned) rt.sessionsChanged()
  }

  function onWorkspaceUpdate(inner) {
    const p = inner.payload
    if (inner.type !== 'workspace_update' || !p) return
    told('workspaces', p.kind === 'upsert' ? p.workspace?.id : p.id)
    if (p.kind === 'upsert' && p.workspace) keepWorkspace(p.workspace)
    else if (p.kind === 'remove' && p.id) forgetWorkspace(p.id)
    scheduleWorkspaces()
  }

  function scheduleWorkspaces(delay = WORKSPACES_DELAY) {
    if (workspacesTimer) {
      if (delay > 0) return
      clearTimeout(workspacesTimer)
    }
    workspacesTimer = setTimeout(() => {
      workspacesTimer = undefined
      pushWorkspaces()
    }, delay)
    workspacesTimer.unref?.()
  }

  /**
   * The node's list of Paseo's workspaces (runtime.workspaces): told when what it lists changed, not at every status
   * Paseo sends (a workspace's status moves with each of its agents' turns).
   */
  function pushWorkspaces() {
    if (!workspacesRead) return
    const rows = workspaceRows()
    const said = JSON.stringify(rows)
    if (said === workspacesSaid) return
    workspacesSaid = said
    rt.workspacesChanged(rows)
  }

  /** Every workspace as a runtime.workspaces row, in the space its agents' rows name. */
  function workspaceRows() {
    const s = spaces()
    const rows = []
    for (const d of workspaces.values()) {
      const row = rowOfWorkspace(d, s)
      if (row) rows.push(row)
    }
    return rows.sort((a, b) => (a.workspace_id < b.workspace_id ? -1 : a.workspace_id > b.workspace_id ? 1 : 0))
  }

  function rowOfWorkspace(d, s = spaces()) {
    const spaceId = s.idOf(d.project)
    return workspaceRow(d, { spaceId, spaceName: spaceId ? s.names.get(spaceId) : undefined, canon })
  }

  /** Paseo told of id (an agent's or a workspace's, by kind): the readings under way take no page's copy of it. */
  function told(kind, id, card = false) {
    if (typeof id !== 'string' || !id) return
    for (const r of readings) if (r.kind === kind && (r.cards || !card)) r.told.add(id)
  }

  function onAgentUpdate(inner) {
    const p = inner.payload
    if (inner.type !== 'agent_update' || !p) return
    told('agents', p.kind === 'upsert' ? p.agent?.id : p.agentId)
    if (p.kind === 'upsert' && p.agent) {
      upsert(p.agent, p.project ?? undefined)
      pin(agents.get(p.agent.id)?.agent)
    } else if (p.kind === 'remove' && p.agentId) {
      // Paseo also sends a removal for every change of an agent it never listed (its own internal ones, which name
      // the others): only one that was listed is asked about, and an archived one kept here (Paseo 0.15 tells every
      // client of a deletion, archived agents too), which stays until Paseo says it is gone.
      if (!agents.has(p.agentId)) {
        if (archived.has(p.agentId)) void whatBecameOf(p.agentId)
        return
      }
      remove(p.agentId)
      void whatBecameOf(p.agentId)
    }
    rt.sessionsChanged()
  }

  function onSessionEvent(inner) {
    const p = inner.payload ?? {}
    if (inner.type === 'agent_permission_request' || inner.type === 'agent_permission_resolved') told('agents', p.agentId, true)
    switch (inner.type) {
      case 'agent.background_work.update':
        // This inventory is newer than any directory page currently being read.
        if (agents.has(p.agentId) && agents.get(p.agentId).agent.status !== 'closed') {
          told('agents', p.agentId)
          background.update(p)
        }
        break
      case 'agent_permission_request': {
        const a = agents.get(p.agentId)?.agent
        if (a && p.request?.id && !(a.pendingPermissions ?? []).some((r) => r?.id === p.request.id)) {
          a.pendingPermissions = [...(a.pendingPermissions ?? []), p.request]
        }
        askFor(p.agentId, p.request)
        rt.sessionsChanged(50)
        break
      }
      case 'agent_permission_resolved': {
        // Answered: in Paseo's own app (the card leaves the phone), or the bridge's own answer confirmed.
        const a = agents.get(p.agentId)?.agent
        if (a) a.pendingPermissions = (a.pendingPermissions ?? []).filter((r) => r?.id !== p.requestId)
        asks.get(p.requestId)?.abort()
        forgetRequest(p.requestId)
        rt.sessionsChanged(50)
        break
      }
      case 'providers_snapshot_update':
        if (Array.isArray(p.entries)) {
          entries = p.entries
          providersChanged()
          rt.sessionsChanged()
        }
        break
      case 'status':
        if (p.status === 'daemon_config_changed' && p.config) {
          configProviders = keepProviders(p.config.providers)
          providersChanged()
          rt.sessionsChanged()
        }
        break
    }
  }

  // ---- cards ----

  /**
   * Put a pending request on the phone (once); the answer goes back to Paseo. Answered in Paseo: withdrawn. With no hub
   * it waits for one (onHub). One whose answer could not reach Paseo (it was away) has that answer sent again once
   * Paseo is back and still asks.
   */
  function askFor(agentId, request) {
    if (!request?.id || handedBack.has(request.id)) return
    const had = asks.get(request.id)
    if (had) {
      if (had.answer && !had.sending && paseo.connected) void deliver(request.id, had)
      return
    }
    if (ownTool(agentId, request)) return allowOwn(agentId, request)
    if (!rt.hubConnected) return
    const sessionId = sessionOf(agentId)
    if (!sessionId) return
    let card = rounds.get(request.id)
    if (!card) rounds.set(request.id, card = { agentId, round: 0, used: true, since: undefined })
    const ac = new AbortController()
    const entry = {
      agentId, round: card.round, fresh: !card.used, since: undefined, askedAt: 0, epoch: hubEpoch,
      abort: () => {
        if (asks.get(request.id) === entry) asks.delete(request.id)
        ac.abort(new Error('answered elsewhere'))
      },
    }
    card.used = true
    asks.set(request.id, entry)
    withPlan(agentId, request).then((r) => {
      entry.askedAt = Date.now()
      entry.epoch = hubEpoch
      card.since ??= entry.askedAt
      entry.since = card.since
      return rt.ask(cardOf(sessionId, r, entry.round), { signal: ac.signal, timeoutSeconds: CARD_LIFE })
    }).then((res) => {
      if (asks.get(request.id) !== entry) return
      const agent = agents.get(agentId)?.agent
      const out = responseOf(request, res, agentRoot(agent, roots), modesOf(agent))
      if (out) {
        entry.answer = out
        return deliver(request.id, entry)
      }
      asks.delete(request.id)
      if (res?.dismiss) dismissed(agentId, request, entry)
      else handedBack.add(request.id) // an answer that says nothing Paseo takes: its own app answers it
    }, () => {
      if (asks.get(request.id) === entry) asks.delete(request.id)
    })
  }

  /**
   * A request's card: in round 0 under the id any bridge gives it (interactionOf: the hub hangs a bridge's ask after a
   * node restart on the card the phone already shows), in a later round under one of this bridge's own, a new card.
   */
  function cardOf(sessionId, request, round) {
    const ix = interactionOf(sessionId, request)
    if (round > 0) ix.id = interactionId(RUNTIME, sessionId, ix.kind, `${request.id}\0${boot}\0${round}`)
    return ix
  }

  /** An agent's modes with their labels: as its record lists them, else as its provider does. */
  function modesOf(agent) {
    if (agent?.availableModes?.length) return agent.availableModes
    return entries.find((e) => e?.provider === agent?.provider)?.modes ?? []
  }

  /** A Codex agent's own mode, Persona's default there: its provider's default in Paseo, else its first short of full access. */
  function ownMode(agent) {
    return entries.find((e) => e?.provider === agent?.provider)?.defaultModeId || modesOf(agent).find((m) => m?.id && m.id !== 'full-access')?.id
  }

  /**
   * A card came back dismissed. Only a person's "Return to computer" leaves the request to Paseo's own app: one that
   * came while the card was up, the hub there since it went up and before its time ran out. With no hub now it goes up
   * again, under the same id, once a hub is back (onHub). One whose time ran out, or that the hub ended while it was
   * away or before this card went up (then its answer comes at once), goes up again as a new card while Paseo still
   * asks: a request Paseo waits on keeps its home on the phone (#8128).
   */
  function dismissed(agentId, request, e) {
    if (!rt.hubConnected) return
    const now = Date.now()
    const gap = e.epoch !== hubEpoch
    // When the hub last got the card: as it went up, or again when the hub was back after the gap.
    const sent = gap ? Math.max(e.askedAt, hubBackAt) : e.askedAt
    const expired = now - e.since >= CARD_LIFE * 1000 - EXPIRY_SLACK
    // A card this bridge just made for the request was never on the hub: its dismissal is a person's, however soon.
    const ended = (gap || !e.fresh) && now - sent < atOnce
    if (!expired && !ended) {
      handedBack.add(request.id)
      return
    }
    log(`paseo: the card for ${request.id} of ${agentId} ${expired ? 'ran out of time' : 'was ended on the hub'}; a new one while Paseo still asks`)
    askAgain(agentId, request.id, e.round)
  }

  /**
   * The request on the phone again, as a new card (the round after the one given), while Paseo still lists it: the hub
   * keeps how the last card ended, an answer included, under its id.
   */
  function askAgain(agentId, requestId, round) {
    const card = rounds.get(requestId)
    if (card?.round === round) {
      card.round++
      card.used = false
      card.since = undefined
    }
    const r = (agents.get(agentId)?.agent.pendingPermissions ?? []).find((x) => x?.id === requestId)
    if (r) askFor(agentId, r)
  }

  /**
   * The phone's answer to Paseo. The card stays pending (asks) until Paseo confirms it, so an update of the agent that
   * still lists the request meanwhile does not put it on the phone again. One that failed because Paseo was away waits
   * there and goes again once Paseo is back and still asks (askFor). One Paseo refused goes up again as a new card.
   */
  async function deliver(requestId, e) {
    e.sending = true
    const root = agentRoot(agents.get(e.agentId)?.agent, roots)
    try {
      await paseo.request('agent_permission_response', { agentId: e.agentId, response: e.answer.response },
        { requestId, expect: 'agent_permission_resolved', timeoutMs: 30_000 })
    } catch (err) {
      e.sending = false
      if (asks.get(requestId) !== e) return // answered or gone meanwhile
      if (err?.code === 'disconnected') {
        log(`paseo: the answer to ${requestId} of ${e.agentId} waits for Paseo: ${err.message}`)
        return
      }
      asks.delete(requestId)
      log(`paseo: answer ${requestId} of ${e.agentId}: ${err.message}; asking again`)
      askAgain(e.agentId, requestId, e.round)
      return
    }
    if (asks.get(requestId) === e) asks.delete(requestId)
    const modeId = paseoMode(root, e.answer.setMode)
    if (!modeId) return
    try {
      const r = await paseo.request('set_agent_mode_request', { agentId: e.agentId, modeId })
      if (r?.accepted === false) log(`paseo: mode ${modeId} for ${e.agentId}: ${r.error ?? 'refused'}`)
    } catch (err) {
      log(`paseo: mode ${modeId} for ${e.agentId}: ${err.message}`)
    }
  }

  /** A request that is no longer pending (answered, or its agent gone): nothing kept of its cards. */
  function forgetRequest(requestId) {
    rounds.delete(requestId)
    handedBack.delete(requestId)
  }

  /**
   * Whether request asks for one of the Agent's own Persona tools (allowTools; any provider, asksForTool) in an Agent
   * Persona started (it carries SESSION_LABEL): those run without a card, as the node allows them in the Claude Agents
   * it starts (docs/integrations/claude-integration.md "Persona tools Agents call without a card"). An agent made in
   * Paseo's own app (one whose id the bridge kept, KEPT_LABEL, too) and every other tool still go to the phone.
   */
  function ownTool(agentId, request) {
    if (request.kind === 'question' || request.kind === 'plan') return false
    const labels = agents.get(agentId)?.agent?.labels
    const label = labels?.[SESSION_LABEL]
    if (typeof label !== 'string' || label === '' || labels[KEPT_LABEL]) return false
    return [...allowTools].some((name) => asksForTool(request, name))
  }

  /**
   * The Agent's own Persona tools (allowTools) as Paseo's preapproval for a new Claude Code agent (toolPolicy): those
   * of the servers it is given, by server and tool (`mcp__<server>__<tool>`). Undefined when there are none. Never for
   * Codex or OpenCode (docs/lessons/paseo.md): ownTool answers their asks.
   */
  function preapproved(mcpServers) {
    const grants = []
    for (const name of allowTools) {
      const m = /^mcp__(.+?)__(.+)$/.exec(name)
      if (m && Object.hasOwn(mcpServers, m[1])) grants.push({ kind: 'mcp', server: m[1], tool: m[2] })
    }
    return grants.length ? { preapproved: grants } : undefined
  }

  /**
   * Allows one of the Agent's own tools (ownTool) here, once, with nothing remembered and no mode changed. Kept among
   * asks until Paseo no longer lists it, so a refresh meanwhile does not answer it twice; a failed answer is tried again
   * at the next refresh.
   */
  function allowOwn(agentId, request) {
    const entry = { agentId, abort: () => { if (asks.get(request.id) === entry) asks.delete(request.id) } }
    asks.set(request.id, entry)
    paseo.request('agent_permission_response', { agentId, response: { behavior: 'allow' } },
      { requestId: request.id, expect: 'agent_permission_resolved', timeoutMs: 30_000 })
      .catch((err) => {
        log(`paseo: allow ${request.name} of ${agentId}: ${err.message}`)
        entry.abort()
      })
  }

  /**
   * A plan request with its plan: Claude Code gives Paseo's permission no input for ExitPlanMode (the plan is in the
   * tool call), so it is read from the call in the agent's timeline, as Paseo's own app shows it.
   */
  async function withPlan(agentId, request) {
    const has = typeof request.metadata?.planText === 'string' || typeof request.input?.plan === 'string'
    if (request.kind !== 'plan' || has) return request
    try {
      const res = await paseo.request('fetch_agent_timeline_request', { agentId, direction: 'tail', limit: 40 }, { timeoutMs: 15_000 })
      const useId = request.metadata?.toolUseId
      const call = (res?.entries ?? []).map((e) => e.item).reverse()
        .find((it) => it?.type === 'tool_call' && it.detail?.type === 'plan' && (!useId || it.callId === useId))
      if (typeof call?.detail?.text === 'string') return { ...request, metadata: { ...request.metadata, planText: call.detail.text } }
    } catch (err) {
      log(`paseo: the plan of ${agentId}: ${err.message}`)
    }
    return request
  }

  /** Every request still pending that is not on the phone (after a reconnect, the hub back). */
  function askPending() {
    for (const { agent } of agents.values()) for (const r of agent.pendingPermissions ?? []) askFor(agent.id, r)
  }

  // ---- the driver ----

  /** The agent a session is: the one Paseo lists under its id, else one archived that keeps it (archivedOf). */
  function agentFor(sessionId) {
    const id = bySession.get(sessionId)
    const a = id ? agents.get(id) : undefined
    if (a) return a.agent
    const kept = archivedOf(sessionId)
    if (kept) return kept
    if (!paseo.connected) throw new RpcError('Paseo 现在没有连上（没在运行，或正在重启），稍后再试。这次什么都没做。', 'unavailable')
    throw new RpcError(`Paseo 里没有这个会话（${sessionId}）：可能已经归档或删除了`, 'not_found')
  }

  /**
   * Who created whom among the agents listed (Paseo's create_agent), by agent id. An agent's creator counts while it
   * is listed and in the same workspace: as in Paseo, a delegated agent in a workspace of its own is that workspace's
   * own. Agents that created one another in a ring have none, each a row of its own. So the links never loop.
   */
  function creators() {
    const of = new Map()
    for (const { agent } of agents.values()) {
      const p = agents.get(parentAgentId(agent))?.agent
      if (p && p.id !== agent.id && p.workspaceId === agent.workspaceId && sessionOf(p.id) && sessionOf(agent.id)) of.set(agent.id, p.id)
    }
    for (const start of [...of.keys()]) {
      const ring = new Set()
      let at = start
      while (at !== undefined && !ring.has(at)) {
        ring.add(at)
        at = of.get(at)
      }
      if (at === start) for (const id of ring) of.delete(id)
    }
    return of
  }

  /**
   * The agents and the ones they created: each one's creator (creators), and for each creator those of its own that
   * have not finished: running, waiting for an answer, or waiting for agents of their own (its row's background work).
   */
  function family() {
    const creator = creators()
    const created = new Map()
    for (const [id, by] of creator) created.set(by, [...(created.get(by) ?? []), id])
    const unfinished = new Map()
    const busy = (id) => {
      if (!unfinished.has(id)) {
        const a = agents.get(id)?.agent
        unfinished.set(id, isRunning(a) || background.busy(id) || (a?.pendingPermissions?.length ?? 0) > 0 || (created.get(id) ?? []).some(busy))
      }
      return unfinished.get(id)
    }
    const fleet = new Map()
    for (const [by, ids] of created) {
      const still = ids.filter(busy).map((id) => agents.get(id).agent)
      if (still.length) fleet.set(by, still)
    }
    return { creator, fleet }
  }

  /**
   * Whether Paseo shows on a workspace what an agent did: one the user started, or one created from another workspace
   * (that workspace's own agent, in Paseo's sidebar too). Any other agent another one created is quiet (rowStatus):
   * under its creator, in a ring, or left behind by a creator that is gone.
   */
  function workspaceOwn(agent) {
    const by = parentAgentId(agent)
    if (!by) return true
    const p = agents.get(by)?.agent
    return Boolean(p) && p.id !== agent.id && p.workspaceId !== agent.workspaceId
  }

  /**
   * What agentRow takes beside the agent: its place, its last words, the agent that created it, its own fleet, and the
   * thinking options of its model (the efforts modes takes for it).
   */
  function rowOptions(agentId, s, fam) {
    const a = agents.get(agentId)
    const spaceId = s.idOf(a?.project)
    const by = fam.creator.get(agentId)
    return { project: a?.project, roots, labels, lastText: lastTexts.get(agentId), spaceId, spaceName: s.names.get(spaceId),
      parent: by && sessionOf(by), quiet: Boolean(a) && !workspaceOwn(a.agent), fleet: fam.fleet.get(agentId), nativeBackground: background.items(agentId), backgroundPending: background.pending(agentId), canon,
      thinking: a && thinkingOptions(a.agent) }
  }

  async function listSessions() {
    const rows = []
    const s = spaces()
    const fam = family()
    const agentOf = new Map() // session id → its agent, for the rows
    for (const [sid, agentId] of bySession) {
      const a = agents.get(agentId)
      const row = a && agentRow(a.agent, rowOptions(agentId, s, fam))
      if (row && row.session_id === sid) {
        rows.push(row)
        agentOf.set(sid, agentId)
      }
    }
    // The user's own agents first: an orchestrator's delegated agents (dozens, soon closed) do not push them off the
    // list. Then the delegated agents of those listed, however many creators lie between. One that works, waits or is
    // open on the phone keeps the agent its line of creators starts at, and every creator between is listed with it:
    // no row names a parent that is not there.
    const busy = (r) => r.status === 'working' || r.status === 'blocked' || (r.background?.length ?? 0) > 0
    const pinnedWorkspaces = new Set([...workspaces.values()].filter((w) => workspacePinTime(w)).map((w) => w.id))
    const pinned = (r) => pinnedWorkspaces.has(r.workspace_id)
    const retained = (r) => busy(r) || pinned(r)
    const delegated = (r) => fam.creator.has(agentOf.get(r.session_id))
    const top = (r) => {
      let at = agentOf.get(r.session_id)
      for (let by = fam.creator.get(at); by !== undefined; by = fam.creator.get(at)) at = by
      return at
    }
    const kept = new Set(rows.filter((r) => delegated(r) && (retained(r) || rt.touched(r.session_id))).map(top))
    const own = rt.recent(rows.filter((r) => !delegated(r)), { days: 3650, max: listMax, pinned: (r) => retained(r) || kept.has(agentOf.get(r.session_id)) })
    const tops = new Set(own.map((r) => agentOf.get(r.session_id)))
    const under = rt.recent(rows.filter((r) => delegated(r) && tops.has(top(r))), { days: 3650, max: Math.max(0, listMax - own.length), pinned: retained })
    const listed = new Map([...own, ...under].map((r) => [r.session_id, r]))
    const rowOfSession = new Map(rows.map((r) => [r.session_id, r]))
    for (const r of under) {
      for (let by = fam.creator.get(agentOf.get(r.session_id)); by !== undefined; by = fam.creator.get(by)) {
        const sid = sessionOf(by)
        if (listed.has(sid)) break
        if (rowOfSession.has(sid)) listed.set(sid, rowOfSession.get(sid))
      }
    }
    // Then the archived agents that keep their own id, not running (archivedRows), and the starts Paseo is still on or
    // did not start (inFlightRows): one of them continuing an archived agent is that session now.
    const starts = inFlightRows()
    const startIds = new Set(starts.map((r) => r.session_id))
    return [...listed.values(), ...archivedRows(s).filter((r) => !startIds.has(r.session_id)), ...starts]
      .sort((a, b) => Date.parse(b.updated_at) - Date.parse(a.updated_at))
  }

  const history = (agentId, epoch) => `paseo:${agentId}:${epoch ?? ''}`

  /** A timeline page from Paseo: the whole of it (after: only from there on). Errors come back in the answer. */
  async function fetchTimeline(agentId, after) {
    const req = { agentId, limit: 0, direction: after ? 'after' : 'tail' }
    if (after) req.cursor = after
    const res = await paseo.request('fetch_agent_timeline_request', req, { timeoutMs: 60_000 })
    if (res?.error) throw new RpcError(`Paseo：${res.error}`, 'paseo')
    return res
  }

  /**
   * Whether Paseo let the agent go: closed (its idle runtime collected) or archived (archivedRows). Paseo loads such an
   * agent to answer any request about its timeline, relaunching its provider (0.15.4 ensureAgentLoaded: no read leaves
   * it stored), so only a person loads one: a watch of its chat, a send, a rewind, a mode change (#8132).
   */
  const unloaded = (agent) => agent?.status === 'closed' || Boolean(agent?.archivedAt)

  /**
   * An agent's whole conversation as items (read again only when the agent moved). load: the read may load an agent
   * Paseo let go (unloaded), as a watch's may. Any other read of one (the manager's, an app's background sync) gets the
   * last whole read while that is all of it (taken once it stopped, at the updatedAt it has now: a stored conversation
   * does not change), else is refused (not_loaded), asking Paseo nothing.
   */
  async function timeline(agent, o = {}) {
    const key = `${agent.updatedAt}|${agent.status}`
    const c = timelines.get(agent.id)
    if (c && c.key === key && (agent.status !== 'running' || Date.now() - c.at < 500)) {
      keepTimeline(agent.id, c)
      return c
    }
    if (unloaded(agent) && !o.load) {
      if (c?.stopped && c.updatedAt === agent.updatedAt) {
        keepTimeline(agent.id, c)
        return c
      }
      throw new RpcError(NOT_LOADED, 'not_loaded')
    }
    const res = await fetchTimeline(agent.id)
    const running = isRunning(res.agent ?? agent)
    const tl = timelineItems(res.entries, { maxSeq: res.window?.maxSeq, running, echoes })
    // When the read is all of the conversation: Paseo answers with the agent as it is once loaded.
    const out = { key, at: Date.now(), tl, history: history(agent.id, res.epoch), epoch: res.epoch,
      updatedAt: res.agent?.updatedAt ?? agent.updatedAt, stopped: !running }
    keepTimeline(agent.id, out)
    if (tl.lastText) lastTexts.set(agent.id, tl.lastText)
    return out
  }

  /**
   * An agent's timeline as the most recently read; beyond TIMELINES_MAX, the least recently read go, except those of
   * the agents the phone watches (read again and again while it does).
   */
  function keepTimeline(agentId, t) {
    timelines.delete(agentId)
    timelines.set(agentId, t)
    if (timelines.size <= TIMELINES_MAX) return
    const watched = new Set([...watches.values()].map((w) => w.agentId))
    for (const id of timelines.keys()) {
      if (timelines.size <= TIMELINES_MAX) break
      if (!watched.has(id)) timelines.delete(id)
    }
  }

  const isRunning = (a) => a?.status === 'running' || a?.status === 'initializing' || Boolean(a?.activeTurn)

  /**
   * A session Paseo is still starting, or did not start (track), has no agent to read yet: its chat is empty rather
   * than an error, as for the node's own starts. The agent's content, once it is there, is another history.
   */
  const notYet = (sessionId) => (inFlightOf(sessionId) ? `paseo:starting:${sessionId}` : undefined)

  // A read may load an agent Paseo let go (timeline) while its session is watched: every app watches a chat it opens
  // (the web and Android just after their first page, which the watch's backlog sends again), and the manager the turn
  // of a message a channel delivered; nothing else watches.
  const mayLoad = (sessionId) => watches.has(sessionId)

  async function items(params) {
    const empty = notYet(params.session_id)
    if (empty) return page([], params, { history: empty, toSeq: 0 })
    const agent = agentFor(params.session_id)
    const t = await timeline(agent, { load: mayLoad(params.session_id) })
    const res = page(t.tl.items, params, { history: t.history, toSeq: t.tl.toSeq })
    const w = watches.get(params.session_id)
    if (w && !w.transcript && params.after_seq !== undefined && typeof res.to_seq === 'number') {
      // The SDK catching a watch up: live pushes go on from here.
      w.last = Math.max(w.last, res.to_seq)
      w.safe = Math.min(w.safe ?? Infinity, t.tl.safeSeq, w.last)
      w.epoch = t.epoch
    }
    return res
  }

  async function cursor(sessionId) {
    const empty = notYet(sessionId)
    if (empty) return { to_seq: 0, history: empty }
    const agent = agentFor(sessionId)
    const t = await timeline(agent, { load: mayLoad(sessionId) }) // asked by a watch as it starts
    const w = watches.get(sessionId)
    if (w && !w.transcript) {
      w.last = t.tl.toSeq
      w.safe = t.tl.safeSeq
      w.epoch = t.epoch
    }
    return { to_seq: t.tl.toSeq, history: t.history }
  }

  async function item(params) {
    const agent = agentFor(params.session_id)
    const t = await timeline(agent, { load: mayLoad(params.session_id) })
    const it = t.tl.items.find((x) => x.id === params.item_id)
    if (!it) throw new RpcError(`no item ${params.item_id}`, 'not_found')
    return it
  }

  /** A promise's value, or TIMEOUT once ms passed: the promise goes on, and the caller takes what it comes to. */
  function within(p, ms) {
    let timer
    return Promise.race([
      p.finally(() => clearTimeout(timer)),
      new Promise((r) => {
        timer = setTimeout(() => r(TIMEOUT), ms)
        timer.unref?.()
      }),
    ])
  }

  /**
   * Paseo connected and its directory read on that connection (onPaseo): a request asked again after a reconnect
   * (repeat) waits for this. Rejects once until passed, or when the bridge stops.
   */
  function whenBack(until) {
    if (!started) return Promise.reject(new Error('the bridge stopped'))
    if (paseo.connected && setUp === connections) return Promise.resolve()
    return new Promise((resolve, reject) => {
      const w = {
        done: () => end(resolve),
        fail: (err) => end(() => reject(err)),
        timer: setTimeout(() => w.fail(new Error('Paseo was not back in time')), Math.max(0, until - Date.now())),
      }
      const end = (settle) => {
        clearTimeout(w.timer)
        backWaiters.delete(w)
        settle()
      }
      w.timer.unref?.()
      backWaiters.add(w)
    })
  }

  /**
   * A request whose answer a dropped connection must not lose (an agent's creation, a session's import): asked again
   * after each reconnect, until Paseo answers it or until passes. Before asking again it looks in Paseo's directory,
   * read again by then (look): what the lost answer would have said is there when Paseo did it (the agent it made).
   */
  async function repeat(what, ask, until, look) {
    for (let again = false; ; again = true) {
      if (again) {
        await whenBack(until)
        const found = look()
        if (found) return found
      }
      try {
        return await ask()
      } catch (err) {
        if (err?.code !== 'disconnected' || Date.now() >= until) throw err
        log(`paseo: ${what}: the connection to Paseo dropped before it answered; asking again once it is back`)
      }
    }
  }

  /**
   * A start the node was answered for before Paseo was done with it (start, takeOver): its row is listed meanwhile
   * (inFlightRows), until Paseo has the agent, whose own row takes over. One Paseo did not start says why on that row
   * (and in the log): what the phone was told had started does not just vanish.
   */
  function track(sessionId, row, work, what, again, readonly = false) {
    const entry = { row, work, failed: undefined, again, readonly: Boolean(readonly), done: false }
    inFlight.set(sessionId, entry)
    work.then(() => {
      entry.done = true
      if (inFlight.get(sessionId) === entry) inFlight.delete(sessionId)
      rt.sessionsChanged()
    }, (err) => {
      entry.done = true
      log(`paseo: ${what}: failed after the start was answered: ${err.message}`)
      // Not when another start of the session came since, nor while an agent Paseo lists is the session after all (an
      // outcome Paseo could not tell). Its agent being listed took the entry away (upsert): one the start then archived
      // (read-only in another mode, off the list) still says why.
      const now = inFlight.get(sessionId)
      if (now && now !== entry) return
      if (bySession.has(sessionId)) {
        if (now === entry) inFlight.delete(sessionId)
        return
      }
      entry.failed = { error: err.message, at: Date.now() }
      inFlight.set(sessionId, entry)
      rt.sessionsChanged()
    })
  }

  /** The start of session id that Paseo is still on, or did not start lately (track), when no agent it lists has the id. */
  function inFlightOf(sessionId) {
    return bySession.has(sessionId) && !pendingReadonly.has(sessionId) ? undefined : inFlight.get(sessionId)
  }

  /**
   * The rows of the starts answered before Paseo was done (track) that no agent it lists has yet: starting while it
   * is on them, as the node lists its own starts (node/starting.go); once one failed, not running with start_error,
   * kept START_FAILED_FOR from the failure (updated_at), as the apps show a start that failed.
   */
  function inFlightRows() {
    const now = Date.now()
    const rows = []
    for (const [sid, e] of inFlight) {
      if (e.failed && now - e.failed.at > START_FAILED_FOR) inFlight.delete(sid)
      else if (!bySession.has(sid)) rows.push(e.failed ? failedRow(e) : e.row)
    }
    return rows
  }

  function failedRow(e) {
    const { starting: _, ...row } = e.row
    return { ...row, status: 'offline', start_error: `Paseo did not start this session: ${e.failed.error}`.slice(0, 600),
      updated_at: new Date(e.failed.at).toISOString(), rev: e.failed.at }
  }

  /**
   * A message from the phone. To a session Paseo is still creating or importing (a start answered before Paseo was
   * done, track: the first message after a takeover comes this way) it waits for the agent and goes to it then; when
   * that is after the call's wait the answer is queued (it has not reached the agent yet), and it goes once Paseo has
   * the agent. Refused, nothing sent, when that start failed; a takeover that failed is tried again instead.
   */
  async function send(params) {
    const end = Date.now() + callWait
    const sid = params.session_id
    const notStarted = (error) => new RpcError(`This session did not start (${error}). Nothing was sent.`, 'start_failed')
    let e = inFlightOf(sid)
    if (e?.failed && e.again) {
      // A takeover that failed: this message takes the session over again.
      await e.again(callWait)
      e = inFlightOf(sid)
    }
    if (e) {
      if (e.failed) throw notStarted(e.failed.error)
      const got = await within(e.work, Math.max(end - Date.now(), 1000)).catch((err) => { throw notStarted(err.message) })
      if (got === TIMEOUT) {
        e.work.then(() => deliverMessage(params, startGiveUp)).catch((err) => log(`paseo: the message to ${sid} that waited for its start: ${err.message}`))
        return { queued: true }
      }
    } else {
      const activeOp = pendingOps.get(sid)
      if (activeOp) {
        const got = await within(activeOp.promise, Math.max(end - Date.now(), 1000)).catch((err) => { throw notStarted(err.message) })
        if (got === TIMEOUT) {
          activeOp.promise.then(() => deliverMessage(params, startGiveUp)).catch((err) => log(`paseo: the message to ${sid} that waited for its start: ${err.message}`))
          return { queued: true }
        }
        await activeOp.done
      }
    }
    return deliverMessage(params, Math.max(end - Date.now(), 1000))
  }

  /**
   * A message to the agent a session is (send): Paseo's answer within wait. Later than that Paseo has it, as when it
   * loads a closed agent first; a refusal it sends after the wait nobody waits for any more, so the log says it.
   */
  async function deliverMessage(params, wait) {
    const agent = agentFor(params.session_id)
    if (params.a2a_wake_on_send && agent.archivedAt) {
      throw new RpcError('The receiving agent was archived', 'a2a_session_busy', { delivered: 'none' })
    }
    const text = typeof params.text === 'string' ? params.text : ''
    const images = (params.images ?? []).filter((i) => i?.data).map((i) => ({ data: i.data, mimeType: i.mime || 'image/jpeg' }))
    if (!text.trim() && images.length === 0) throw new RpcError('消息是空的', 'bad_request')
    // The same id for a retry of one message: Paseo delivers a message id once.
    const messageId = params.client_msg_id ? uuidOf(`persona:${agent.id}:${params.client_msg_id}`) : randomUUID()
    if (params.client_msg_id) {
      echoes.set(messageId, params.client_msg_id)
      while (echoes.size > 1000) echoes.delete(echoes.keys().next().value)
    }
    // Into the turn when it works (as Claude Code takes a message typed while it works), a new turn otherwise. An
    // archived agent (archivedRows) Paseo takes back from the archive first, its labels and so its session id kept.
    const req = { agentId: agent.id, text, messageId, activeTurnBehavior: 'steer' }
    if (images.length) req.images = images
    const sent = paseo.request('send_agent_message_request', req, { timeoutMs: 120_000 })
    const res = await within(sent, wait)
    if (res === TIMEOUT) {
      sent.then((late) => {
        if (late?.accepted === false) log(`paseo: Paseo refused the message to ${agent.id} after the wait: ${late.error ?? 'no reason given'}`)
      }, (err) => log(`paseo: send to ${agent.id}: ${err.message}`))
      return
    }
    if (res?.accepted === false) throw new RpcError(`Paseo 没有收下这条消息：${res.error ?? '原因不明'}`, 'rejected')
  }

  async function interrupt(params) {
    const agent = agentFor(params.session_id)
    if (agent.archivedAt) return // archived in Paseo: nothing runs
    const res = await paseo.request('cancel_agent_request', { agentId: agent.id })
    if (res?.error) throw new RpcError(`Paseo：${res.error}`, 'paseo')
  }

  async function rename(params) {
    const agent = agentFor(params.session_id)
    const title = String(params.title ?? '').trim()
    if (!title) return
    const res = await paseo.request('update_agent_request', { agentId: agent.id, name: title })
    if (res?.accepted === false) throw new RpcError(`Paseo 没有改名：${res.error ?? '原因不明'}`, 'rejected')
    // Paseo renames an archived agent's record and tells no list (it lists the agents it has): its row takes the
    // name from here.
    const u = agent.archivedAt ? archived.get(agent.id) : undefined
    if (u) u.agent = { ...u.agent, title }
  }

  /**
   * The user saw a session in Persona (#6299): Paseo's own unread mark (requiresAttention) is cleared too, so the session
   * is not unread in Paseo's app as well. A loaded agent's with clear_agent_attention; one Paseo has not loaded through
   * its workspace (workspace.clear_attention, which edits the stored record), and only when no other agent there is
   * flagged: clear_agent_attention would load, so start, a stored agent. Never one waiting for an answer, which Paseo
   * keeps flagged until it is answered.
   */
  async function seen(params) {
    const agent = agents.get(bySession.get(params.session_id))?.agent
    if (!agent || agent.requiresAttention !== true || agent.archivedAt || (agent.pendingPermissions?.length ?? 0) > 0) return
    if (agent.status !== 'closed') {
      // Paseo answers only a request that has an id, and says nothing when it fails: a short wait.
      await paseo.request('clear_agent_attention', { agentId: agent.id }, { timeoutMs: 10_000 })
      return
    }
    const ws = agent.workspaceId
    if (!ws || [...agents.values()].some(({ agent: a }) => a.id !== agent.id && a.workspaceId === ws && a.requiresAttention === true)) return
    const res = await paseo.request('workspace.clear_attention.request', { workspaceId: ws }, { timeoutMs: 10_000 })
    if (res?.success === false) throw new RpcError(`Paseo did not clear it: ${res.error ?? 'no reason given'}`, 'paseo')
  }

  /**
   * Closing a session is archiving its agent: it stops, and leaves Paseo's list. A Claude conversation stays among the
   * sessions not running: under its own id when it keeps one (archivedRows), else as its transcript. One archived
   * already is closed.
   */
  async function close(params) {
    // A start that failed (track): closing it takes its row away, there is no agent to archive.
    if (inFlightOf(params.session_id)?.failed) {
      inFlight.delete(params.session_id)
      return
    }
    const agent = agentFor(params.session_id)
    if (params.force && !(agent.pendingPermissions?.length > 0)) throw new RpcError('The Agent is no longer waiting for confirmation', 'agent_busy')
    if (agent.archivedAt) return
    // Its placement as it is listed now: Paseo tells the list of the archive before it answers.
    const project = agents.get(agent.id)?.project
    // The CLI's archive --force sends this same request; the daemon closes the provider runtime.
    const res = await paseo.request('archive_agent_request', { agentId: agent.id })
    if (res?.error) throw new RpcError(`Paseo: ${res.error}`, 'paseo')
    if (!res?.archivedAt) throw new RpcError('Paseo did not confirm the archive', 'paseo')
    remove(agent.id, { ...agent, archivedAt: res.archivedAt }, project)
  }

  /** The models Paseo lists for a provider, as it says them ([] when it has not said). */
  function modelsOf(provider) {
    return entries.find((x) => x?.provider === provider)?.models ?? []
  }

  /** The thinking options of an agent's model ([] when Paseo has not said). */
  function thinkingOptions(agent) {
    const list = modelsOf(agent.provider)
    const m = list.find((x) => x?.id === agent.model) ?? list.find((x) => x?.isDefault)
    return (m?.thinkingOptions ?? []).map((t) => t?.id).filter(Boolean)
  }

  /**
   * The phone's model, permission mode and effort for a session, in that order (an effort is the new model's): Paseo's
   * set_agent_model / set_agent_mode / set_agent_thinking. Claude Code takes a new model at once, Codex at its next turn.
   * Where approval is Paseo's Auto Accept (Grok), Allow all and the launcher's own are that switch on and off
   * (set_agent_feature); on Codex they are full-access and its provider's own default mode (#8261).
   */
  async function modes(params) {
    const agent = agentFor(params.session_id)
    const model = typeof params.model === 'string' ? params.model.trim() : ''
    if (agent.archivedAt && (params.permission_mode || params.effort || model)) {
      // Paseo changes the modes of an agent it has loaded, never of an archived one.
      throw new RpcError('This session is archived in Paseo: send it a message to continue it, then change its mode. Nothing was changed.', 'not_running')
    }
    if (model) {
      const known = modelsOf(agent.provider).filter((m) => m?.id)
      if (known.length && !known.some((m) => m.id === model || (m.aliases ?? []).includes(model))) {
        throw new RpcError(`Paseo's ${agent.provider} has no model ${model}. Nothing was changed.`, 'bad_request')
      }
      const res = await paseo.request('set_agent_model_request', { agentId: agent.id, modelId: model })
      if (res?.accepted === false) throw new RpcError(`Paseo did not switch the model: ${res.error ?? 'no reason given'}`, 'rejected')
    }
    const mode = params.permission_mode
    if (mode) {
      const root = agentRoot(agent, roots)
      const modeId = paseoMode(root, mode) ?? (root === 'codex' && mode === 'default' ? ownMode(agent) : undefined)
      const autoAccept = autoAcceptOf(agent) !== undefined || AUTO_ACCEPT_ROOTS.has(root)
      if (!modeId && autoAccept && (mode === 'bypassPermissions' || mode === 'default')) {
        const res = await paseo.request('set_agent_feature_request', { agentId: agent.id, featureId: AUTO_ACCEPT, value: mode === 'bypassPermissions' })
        if (res?.accepted === false) throw new RpcError(`Paseo 没有换模式：${res.error ?? '原因不明'}`, 'rejected')
      } else {
        if (!modeId) throw new RpcError('这个 Agent 没有这个权限模式', 'unsupported')
        if ((agent.availableModes ?? []).length && !agent.availableModes.some((m) => m?.id === modeId)) {
          throw new RpcError(`Paseo 里这个 Agent 没有「${mode}」模式`, 'bad_request')
        }
        const res = await paseo.request('set_agent_mode_request', { agentId: agent.id, modeId })
        if (res?.accepted === false) throw new RpcError(`Paseo 没有换模式：${res.error ?? '原因不明'}`, 'rejected')
      }
    }
    const effort = params.effort
    if (effort) {
      const opts = thinkingOptions(model ? { provider: agent.provider, model } : agent)
      if (!EFFORTS.includes(effort) || (opts.length && !opts.includes(effort))) {
        throw new RpcError(`${model || agent.model || '这个模型'} 没有「${effort}」这一档思考强度`, 'bad_request')
      }
      const res = await paseo.request('set_agent_thinking_request', { agentId: agent.id, thinkingOptionId: effort })
      if (res?.accepted === false) throw new RpcError(`Paseo 没有换思考强度：${res.error ?? '原因不明'}`, 'rejected')
    }
  }

  /**
   * Back to before one of the user's messages (Paseo's agent.rewind.request; restore code_and_conversation puts the
   * files back too, Paseo's "both"). The message is found in Paseo's timeline as the app saw it: its text, the how-manieth
   * with that text counting from the end, and how many of the user's came after it. A conversation that moved since is
   * refused, never cut elsewhere. Claude Code's rewind forks its session: an agent listed under its Claude session id keeps
   * that id on its label first (keepSessionId), so it stays the same session in Persona.
   */
  async function rewind(params) {
    const agent = agentFor(params.session_id)
    const restore = params.restore
    const prompt = promptKey(params.prompt)
    if ((restore !== 'conversation' && restore !== 'code_and_conversation') || !prompt || !Number.isInteger(params.occurrence) ||
        params.occurrence < 1 || (params.prompts_after !== undefined && (!Number.isInteger(params.prompts_after) || params.prompts_after < 0))) {
      throw new RpcError('A rewind needs the message, its current position and what to restore', 'bad_params')
    }
    if (agent.archivedAt) {
      throw new RpcError('This session is archived in Paseo: send it a message to continue it, then rewind it. Nothing was changed.', 'rewind_unsupported')
    }
    const can = rewindOf(agent)
    if (!can.conversation || (restore === 'code_and_conversation' && !can.code)) {
      throw new RpcError(`Paseo's ${agent.provider} cannot rewind this way. Nothing was changed.`, 'rewind_unsupported')
    }
    if (rewinding.has(agent.id) || isRunning(agent) || (agent.pendingPermissions?.length ?? 0) > 0) {
      throw new RpcError('Wait for the current turn to finish before rewinding. Nothing was changed.', 'rewind_busy')
    }
    rewinding.add(agent.id)
    try {
      const res = await fetchTimeline(agent.id)
      const prompts = (res.entries ?? []).filter((e) => e?.item?.type === 'user_message')
        .map((e) => ({ id: e.item.messageId, key: promptKey(e.item.text) })).filter((p) => p.key)
      const target = prompts.filter((p) => p.key === prompt).at(-params.occurrence)
      if (!target?.id || (params.prompts_after !== undefined && prompts.length - 1 - prompts.indexOf(target) !== params.prompts_after)) {
        throw new RpcError('The selected message moved. Reload the conversation before rewinding. Nothing was changed.', 'rewind_mismatch')
      }
      await keepSessionId(agent)
      const mode = restore === 'code_and_conversation' ? 'both' : 'conversation'
      const out = await paseo.request('agent.rewind.request', { agentId: agent.id, messageId: target.id, mode }, { timeoutMs: REWIND_WAIT })
      if (!out?.ok) throw new RpcError(`Paseo did not rewind: ${out?.error ?? 'no reason given'}`, 'rewind_failed')
      // Another history: read whole again (a watch too, besides Paseo's own replacement event).
      timelines.delete(agent.id)
      for (const [sid, w] of watches) {
        if (w.agentId !== agent.id) continue
        w.safe = undefined
        scheduleRefresh(sid)
      }
      rt.sessionsChanged(0)
      return { prompt: String(params.prompt).trim(), restore, code_restored: mode === 'both', input_cleared: true }
    } finally {
      rewinding.delete(agent.id)
    }
  }

  /**
   * Before a rewind, which forks Claude Code's session: an agent listed under its Claude session id, or under one the
   * bridge keeps for it (no label of Persona's), keeps that id on the persona.session label, as one Persona started or
   * took over does, so Persona's session is the same once Paseo moved the agent to the fork; KEPT_LABEL says the bridge
   * kept it (pin). Paseo merges labels; a stored agent's record is rewritten without telling any list, so the snapshot
   * here takes it too.
   */
  async function keepSessionId(agent) {
    if (agentRoot(agent, roots) !== 'claude' || agent.labels?.[SESSION_LABEL]) return
    const id = sessionIdOf(agent, roots)
    if (!id) return
    const labels = { [SESSION_LABEL]: id, [KEPT_LABEL]: '1' }
    const res = await paseo.request('update_agent_request', { agentId: agent.id, labels })
    if (res?.accepted === false) throw new RpcError(`Paseo did not keep the session's id: ${res.error ?? 'no reason given'}`, 'rewind_failed')
    agent.labels = { ...(agent.labels ?? {}), ...labels }
  }

  /**
   * A launcher's models as Paseo has them now (the form's picker, Settings): refresh asks Paseo to look again first,
   * which pushes the providers that changed too.
   */
  async function models(params = {}) {
    const provider = params.launcher
    if (!launchers.some((l) => l.name === provider)) throw new RpcError(`Paseo has no provider ${provider ?? ''} that can start agents`, 'not_found')
    if (params.refresh) {
      await paseo.request('refresh_providers_snapshot_request', { providers: [provider] }, { timeoutMs: 60_000 })
    }
    const res = await paseo.request('list_provider_models_request', { provider }, { timeoutMs: 60_000 })
    if (res?.error) throw new RpcError(`Paseo: ${res.error}`, 'paseo')
    return { agent: appAgent(roots[provider] ?? provider), models: (res?.models ?? []).filter((m) => m?.id && m.isSelectable !== false).map(formModel) }
  }

  /**
   * Settings › Models "Test" for one of Paseo's providers: one real prompt, answered through Paseo with the provider's
   * own login, in the node's empty check directory. The probe is an agent of its own in a background workspace there
   * (Paseo's app does not list it, nor does the bridge: CHECK_LABEL), in plan mode where the provider has it, without
   * Persona's tools. Afterwards the probe is deleted and its workspace archived, and the project Paseo made for the
   * directory removed when the check made it.
   */
  async function check(params) {
    const provider = params.launcher
    if (!launchers.some((l) => l.name === provider)) throw new RpcError(`Paseo has no provider ${provider ?? ''} that can start agents`, 'not_found')
    const cwd = expandHome(params.cwd)
    if (!cwd || !params.prompt || !isDir(cwd)) throw new RpcError('A check needs its empty directory and a prompt', 'bad_params')
    const started = Date.now()
    const before = await projectIds()
    let workspace
    let agentId
    try {
      const ws = await paseo.request('workspace.create.request', {
        source: { kind: 'directory', path: cwd }, background: true, title: 'Persona launcher check', idempotencyKey: randomUUID(),
      }, { timeoutMs: 30_000 })
      if (!ws?.workspace?.id) throw new RpcError(`Paseo did not make the check's workspace: ${ws?.error ?? 'no reason given'}`, 'paseo')
      workspace = ws.workspace
      agentId = randomUUID()
      const config = { provider, cwd }
      if (holdsPlan(provider)) config.modeId = 'plan'
      const created = await paseo.request('agent.create.request', {
        agentId, config, workspaceId: workspace.id, initialPrompt: params.prompt, labels: { [CHECK_LABEL]: '1' },
      }, { timeoutMs: 30_000 })
      if (!created?.agent) throw new RpcError(`Paseo did not start ${provider}: ${created?.error ?? 'no reason given'}`, 'start_failed')
      const done = await paseo.request('wait_for_finish_request', { agentId, timeoutMs: CHECK_WAIT }, { timeoutMs: CHECK_WAIT + 5000 })
      const answer = typeof done?.lastMessage === 'string' ? done.lastMessage.trim() : ''
      const model = done?.final?.model ?? done?.final?.runtimeInfo?.model ?? created.agent.model ?? undefined
      const result = { ok: done?.status === 'idle' && answer !== '', agent: appAgent(roots[provider] ?? provider), millis: Date.now() - started }
      if (model) result.model = String(model)
      if (answer) result.answer = answer.slice(0, 400)
      if (!result.ok) {
        result.error = done?.status === 'permission' ? `${provider} asked for a permission instead of answering`
          : done?.status === 'timeout' ? `${provider} did not answer within ${Math.round(CHECK_WAIT / 1000)} s`
            : done?.error || `${provider} gave no answer`
      }
      return result
    } finally {
      if (agentId) await paseo.request('delete_agent_request', { agentId }).catch((err) => log(`paseo: deleting the check's agent: ${err.message}`))
      if (workspace?.id) await paseo.request('archive_workspace_request', { workspaceId: workspace.id }).catch((err) => log(`paseo: archiving the check's workspace: ${err.message}`))
      // Its project only when this check made it: the directory may lie in a repository Paseo already had.
      if (workspace?.projectId && before && !before.has(workspace.projectId)) {
        await paseo.request('project.remove.request', { projectId: workspace.projectId })
          .catch((err) => log(`paseo: removing the check's project: ${err.message}`))
      }
    }
  }

  /** The ids of Paseo's projects now; undefined when Paseo cannot say (nothing is removed then). */
  async function projectIds() {
    try {
      const res = await paseo.request('project.list.request', {})
      return new Set((res?.projects ?? []).map((p) => p?.projectId ?? p?.id).filter(Boolean))
    } catch (err) {
      log(`paseo: listing its projects: ${err.message}`)
      return undefined
    }
  }

  // Match the Go node's proto.ExpandHome for the current user's home directory.
  function expandHome(p) {
    if (p === '~') return homedir()
    if (typeof p === 'string' && p[0] === '~' && (p[1] === '/' || p[1] === sep)) {
      return join(homedir(), p.slice(2))
    }
    return p
  }

  function isDir(p) {
    try {
      return statSync(p).isDirectory()
    } catch {
      return false
    }
  }

  function rowOf(agent) {
    return agentRow(agent, rowOptions(agent.id, spaces(), family()))
  }

  /**
   * A path as compared: symbolic links resolved (a home under another mount), else as written. One that cannot be
   * resolved now (a volume away for a moment) is asked again the next time, not kept as written.
   */
  function canon(p) {
    let c = canonical.get(p)
    if (c === undefined) {
      try {
        c = realpathSync(p)
      } catch {
        return resolve(p)
      }
      if (canonical.size > 1000) canonical.clear()
      canonical.set(p, c)
    }
    return c
  }

  /**
   * The project spaces of the agents and workspaces Persona knows (map.js projectSpaces): a repository Paseo has a
   * workspace of, with no agents, still takes its worktrees' own project records in.
   */
  const spaces = () => projectSpaces([...agents.values(), ...[...workspaces.values()].map((d) => ({ project: d.project }))], canon)

  function samePath(a, b) {
    if (typeof a !== 'string' || typeof b !== 'string' || !a || !b) return false
    return resolve(a) === resolve(b) || canon(a) === canon(b)
  }

  /**
   * The Paseo workspace a new or continued agent goes into: the one the form names when Paseo has it, agents in it or
   * not (a session's space was its workspace before #2784). One named that Paseo no longer has is refused when the
   * choice was the user's (strict: a stale catalog row), rather than the agent going elsewhere; a space (Paseo's
   * project) or a folder names no workspace. Else the worktree workspace at its directory: handed a worktree's directory
   * alone, Paseo files it under a project record of its own (#2784), while Paseo's own lookup by path takes the worktree
   * workspace there (a worktree Persona had it make, #6115, too). A checkout or a plain folder gets a new workspace of
   * its project, as in Paseo. Several workspaces at that worktree: the one under its repository's project record, then
   * the latest.
   */
  function workspaceFor(named, cwd, { strict = false, projectId } = {}) {
    if (typeof named === 'string' && named) {
      if (workspaces.has(named) || [...agents.values()].some(({ agent }) => agent.workspaceId === named)) return named
      if (strict && workspacesRead && WORKSPACE_ID.test(named)) {
        throw new RpcError(`Paseo no longer has this workspace (${named}): refresh the list and choose again. Nothing was started.`, 'workspace_missing')
      }
    }
    let best
    const consider = (ws, rank) => {
      if (!best || rank[0] > best.rank[0] || (rank[0] === best.rank[0] && rank[1] > best.rank[1])) best = { ws, rank }
    }
    for (const { agent, project } of agents.values()) {
      const ws = agent.workspaceId
      if (typeof ws !== 'string' || !ws || agent.archivedAt || !linkedWorktree(project)) continue
      const owner = workspaces.get(ws)?.projectId ?? project?.projectKey ?? project?.projectId
      if (projectId && owner !== projectId) continue
      const c = project.checkout
      if (!samePath(c.worktreeRoot ?? c.cwd, cwd) && !samePath(agent.cwd, cwd)) continue
      consider(ws, [strayRecord(project) ? 0 : 1, Date.parse(agent.updatedAt) || 0])
    }
    for (const d of workspaces.values()) {
      if (projectId && d.projectId !== projectId) continue
      const c = d.project?.checkout
      // Once cwd-only resolution names the project, its exact directory workspace is reusable too; legacy duplicates
      // under another project are not. Without that resolution, keep the existing worktree-only lookup unchanged.
      const exactOwned = projectId && samePath(d.workspaceDirectory, cwd)
      // A worktree by its placement; one Paseo just made has none yet, only its kind.
      if (!exactOwned && !linkedWorktree(d.project) && !(d.workspaceKind === 'worktree' && !d.project)) continue
      if (!samePath(d.workspaceDirectory, cwd) && !(c && samePath(c.worktreeRoot ?? c.cwd, cwd))) continue
      consider(d.id, [strayRecord(d.project) ? 0 : 1, Date.parse(d.activityAt ?? d.createdAt) || 0])
    }
    return best?.ws
  }

  /**
   * Where a session still being created is listed: its workspace's project and name (the workspace's own, agents in it
   * or not); a new workspace, the project of an agent at the same directory; else the folder's own space.
   */
  function placeOf(workspaceId, cwd) {
    const s = spaces()
    const d = workspaceId ? workspaces.get(workspaceId) : undefined
    const at = d?.project && s.idOf(d.project)
    if (at) return { space_id: at, space_name: s.names.get(at) ?? d.project.projectName, slot_name: slotNameOf(d.project, cwd), workspace_id: workspaceId }
    const known = [...agents.values()]
    const a = (workspaceId && known.find(({ agent }) => agent.workspaceId === workspaceId)) ||
      known.find(({ agent, project }) => spaceIdOf(project) && (samePath(agent.cwd, cwd) || samePath(project.checkout?.cwd, cwd)))
    const space = s.idOf(a?.project)
    if (!space) return { space_id: cwd, space_name: basename(cwd) }
    const place = { space_id: space, space_name: s.names.get(space) ?? a.project.projectName }
    return workspaceId ? { ...place, slot_name: slotNameOf(a.project, cwd), workspace_id: workspaceId } : place
  }

  // ---- workspaces: worktrees made by Paseo (workspace.prepare, workspace.create) ----

  /**
   * Where Paseo puts the worktrees of a repository (workspace.prepare; repo_root its main checkout, physical): beside
   * one it made of that repository already, Paseo's own word; else where its worktree code puts them (map.js
   * paseoCheckouts). For the dialog to show: the folder Paseo reports once it made one is the worktree's.
   */
  async function prepareWorkspace(params) {
    const root = canon(String(params.repo_root || params.cwd || ''))
    if (!isAbsolute(root)) throw new RpcError('workspace.prepare: the repository is required', 'bad_request')
    for (const d of workspaces.values()) {
      const c = d.project?.checkout
      const tree = c?.worktreeRoot ?? d.workspaceDirectory
      if (d.worktreeSlug && c?.mainRepoRoot && tree && canon(c.mainRepoRoot) === root) return { checkouts: dirname(canon(tree)) }
    }
    return { checkouts: paseoCheckouts(worktreesBase(), root) }
  }

  const physicalPath = (path) => {
    try { return realpathSync(path) } catch { return resolve(path) }
  }

  /**
   * Resolve creation ownership from a fresh, complete project list. Path aliases are the same directory; a linked
   * worktree prefers its main checkout's project. Names and remotes never identify a project (clones stay separate).
   * Duplicate records at one physical root (symlink spellings such as /home/me and /mnt/home/me) resolve to one stable pick:
   * the record registered at the physical path itself, then the one with more workspaces, then the lowest ID.
   * Distinct non-main roots stay ambiguous. An unavailable list is never permission to allocate another project.
   */
  async function projectAt(dir) {
    let res
    try {
      res = await paseo.request('project.list.request', {}, { timeoutMs: 10_000 })
    } catch (err) {
      throw new RpcError(`Cannot read Paseo projects: ${err.message}. Retry before creating anything.`, 'project_unavailable')
    }
    if (!Array.isArray(res?.projects) || res.error || res.projects.some((p) => typeof p?.projectId !== 'string' || !p.projectId ||
      typeof p.projectRootPath !== 'string' || !isAbsolute(p.projectRootPath))) {
      throw new RpcError('Paseo did not return a complete project list. Retry before creating anything.', 'project_unavailable')
    }
    if (!res.projects.length) return undefined
    // Do not use the display cache: a symlink may have moved since the last session list.
    const roots = new Set([physicalPath(dir)])
    let checkoutRoot
    let mainRoot
    try {
      // Older Git versions do not support --path-format. The common directory is relative to this command's cwd.
      const { stdout } = await readGit('git', ['-C', dir, 'rev-parse', '--show-toplevel', '--git-common-dir'],
        { encoding: 'utf8', timeout: 5000, maxBuffer: 64 * 1024, env: { ...process.env, LC_ALL: 'C' } })
      const [top, common] = stdout.trim().split('\n')
      if (!top || !common) throw new Error('Git returned no checkout identity')
      checkoutRoot = physicalPath(top)
      roots.add(checkoutRoot)
      const gitDir = physicalPath(resolve(dir, common))
      mainRoot = basename(gitDir) === '.git' ? dirname(gitDir) : gitDir
      roots.add(mainRoot)
    } catch (err) {
      if (err.code !== 128 || !/not a git repository/i.test(err.stderr ?? '')) {
        throw new RpcError('Cannot identify this checkout. Retry before creating anything.', 'project_unavailable')
      }
    }
    const matches = new Map() // physical registered root -> active projects at that exact root
    for (const p of res.projects) {
      if (p.archivedAt) continue
      const root = physicalPath(p.projectRootPath)
      if (!roots.has(root)) continue
      if (!matches.has(root)) matches.set(root, new Map())
      matches.get(root).set(p.projectId, p)
    }
    const workspaceCount = (projectId) => {
      let count = 0
      for (const d of workspaces.values()) if (d.projectId === projectId) count++
      return count
    }
    // Several records at one physical root are symlink aliases of the same directory, not a real ambiguity.
    const pick = (root) => {
      const found = [...matches.get(root).values()]
      if (found.length === 1) return found[0].projectId
      const rank = (p) => [resolve(p.projectRootPath) === root ? 0 : 1, -workspaceCount(p.projectId)]
      found.sort((a, b) => {
        const [ra, rb] = [rank(a), rank(b)]
        return ra[0] - rb[0] || ra[1] - rb[1] || (a.projectId < b.projectId ? -1 : a.projectId > b.projectId ? 1 : 0)
      })
      log(`paseo: resolved ${found.length} duplicate projects at ${root} to ${found[0].projectId}`)
      return found[0].projectId
    }
    // Only a linked worktree's legacy project yields to its main checkout's. A distinct project explicitly registered
    // at a selected subdirectory of the main checkout stays ambiguous.
    if (mainRoot && checkoutRoot !== mainRoot && matches.has(mainRoot)) return pick(mainRoot)
    if (matches.size > 1) {
      throw new RpcError('More than one Paseo project matches this folder. Choose a workspace in the intended project.', 'project_ambiguous')
    }
    const [only] = matches.keys()
    return only === undefined ? undefined : pick(only)
  }

  /** Paseo's worktrees root: its config's worktrees.root, else <Paseo home>/worktrees (its config.js resolveWorktreesRoot). */
  function worktreesBase() {
    const home = options.home ?? paseo.home
    if (typeof home !== 'string' || !home) throw new RpcError('Paseo home unknown', 'unavailable')
    let root
    try {
      root = JSON.parse(readFileSync(join(home, 'config.json'), 'utf8'))?.worktrees?.root
    } catch {}
    if (typeof root === 'string' && root.trim()) {
      const r = expandHome(root.trim())
      return isAbsolute(r) ? resolve(r) : resolve(home, r)
    }
    return join(home, 'worktrees')
  }

  /**
   * A worktree Paseo makes as a workspace of its own (workspace.create): from the workspace named (its folder), else the
   * folder given; an existing branch checked out (create_branch false), else a new one cut from base; its folder named
   * name ("" Paseo's choice: from the branch, or a generated name with no branch either). No agent goes with it: Paseo
   * renames a new branch after a first agent's prompt, and the session Persona starts in it comes after. Paseo answers
   * once git made the checkout; the workspace's own setup runs after.
   */
  async function createWorkspace(params) {
    const deadline = Date.now() + CREATE_WAIT
    if (!paseo.connected) throw new RpcError('Paseo is not connected (not running, or restarting). Nothing was created.', 'unavailable')
    const named = typeof params.workspace_id === 'string' && params.workspace_id ? params.workspace_id : undefined
    const from = named ? workspaces.get(named) : undefined
    if (named && !from) throw new RpcError(`Paseo no longer has this workspace (${named}): refresh the list and choose again. Nothing was created.`, 'workspace_missing')
    const cwd = expandHome(from?.workspaceDirectory || (typeof params.cwd === 'string' ? params.cwd : ''))
    if (!cwd || !isDir(cwd)) throw new RpcError(`No such folder: ${cwd || '(none)'}`, 'bad_request')
    const branch = typeof params.branch === 'string' ? params.branch.trim() : ''
    const source = { kind: 'worktree', cwd }
    if (params.create_branch === false) {
      if (!branch) throw new RpcError('workspace.create: the branch to check out is required', 'bad_request')
      Object.assign(source, { action: 'checkout', refName: branch })
    } else {
      source.action = 'branch-off'
      if (typeof params.base === 'string' && params.base.trim()) source.refName = params.base.trim()
      if (branch) source.branchName = branch
    }
    if (typeof params.name === 'string' && params.name.trim()) source.worktreeSlug = params.name.trim()
    if (from && !from.projectId) throw new RpcError('Paseo did not report this workspace\'s project. Refresh the list.', 'project_unavailable')
    const projectId = from ? from.projectId : await projectAt(cwd)
    if (projectId) source.projectId = projectId
    else if (!from) source.cwd = physicalPath(cwd)
    const req = { source }
    if (typeof params.key === 'string' && params.key) req.idempotencyKey = params.key.slice(0, 512)
    const res = await paseo.request('workspace.create.request', req, { timeoutMs: Math.max(deadline - Date.now(), 1000) })
    const d = res?.workspace
    if (!d?.id) throw new RpcError(`Paseo did not create the worktree: ${res?.error || 'no reason given'}`, res?.errorCode || 'create_failed')
    // Its answer has no placement yet; the update Paseo sends of it (or sent already) is kept over it.
    if (!workspaces.get(d.id)?.project) keepWorkspace(d)
    scheduleWorkspaces(0)
    const row = rowOfWorkspace(workspaces.get(d.id) ?? d)
    if (!row) throw new RpcError('Paseo did not report the created worktree', 'create_failed')
    return { workspace: row }
  }

  /**
   * A workspace's title (workspace_id: workspace.title.set.request) or a space's name (space_id, a Paseo project:
   * project.rename.request, its custom name). Paseo sends the workspaces' descriptors again but not the agents' placements,
   * which name the space and the workspace too: they are brought up to date here, so the rows say the new name at
   * once. Neither renames a branch, a folder or an agent.
   */
  async function renameWorkspace(params) {
    if (!paseo.connected) throw new RpcError('Paseo is not connected (not running, or restarting). Nothing was renamed.', 'unavailable')
    const title = typeof params.title === 'string' ? params.title.trim() : ''
    if (!title) throw new RpcError('workspace.rename: the name is required', 'bad_request')
    const placed = () => [...agents.values(), ...archived.values()]
    if (typeof params.workspace_id === 'string' && params.workspace_id) {
      const id = params.workspace_id
      if (!workspaces.has(id)) throw new RpcError(`Paseo no longer has this workspace (${id}): refresh the list and choose again. Nothing was renamed.`, 'workspace_missing')
      const res = await paseo.request('workspace.title.set.request', { workspaceId: id, title }, { timeoutMs: 30_000 })
      if (!res?.accepted) throw new RpcError(`Paseo did not rename the workspace: ${res?.error || 'no reason given'}`, 'rename_failed')
      const name = res.title ?? title
      const d = workspaces.get(id)
      if (d) workspaces.set(id, { ...d, title: name, name, ...(d.project ? { project: { ...d.project, workspaceName: name } } : {}) })
      for (const e of placed()) if (e.agent?.workspaceId === id && e.project) e.project = { ...e.project, workspaceName: name }
      rt.sessionsChanged()
      scheduleWorkspaces(0)
      const row = workspaces.get(id) && rowOfWorkspace(workspaces.get(id))
      return row ? { workspace: row } : {}
    }
    const space = typeof params.space_id === 'string' ? params.space_id : ''
    const s = spaces()
    const known = [...placed(), ...[...workspaces.values()].map((d) => ({ project: d.project }))].some(({ project }) => spaceIdOf(project) === space)
    if (!space || (!s.names.has(space) && !known)) {
      throw new RpcError(`Paseo has no project ${space || '(none)'} Persona knows of: refresh the list and try again. Nothing was renamed.`, 'workspace_missing')
    }
    const res = await paseo.request('project.rename.request', { projectId: space, customName: title }, { timeoutMs: 30_000 })
    if (!res?.accepted) throw new RpcError(`Paseo did not rename the project: ${res?.error || 'no reason given'}`, 'rename_failed')
    const name = res.customName ?? title
    for (const e of placed()) if (spaceIdOf(e.project) === space) e.project = { ...e.project, projectName: name }
    for (const [id, d] of workspaces) {
      if (spaceIdOf(d.project) === space || (!d.project && d.projectId === space)) {
        workspaces.set(id, { ...d, projectDisplayName: name, ...(d.project ? { project: { ...d.project, projectName: name } } : {}) })
      }
    }
    rt.sessionsChanged()
    scheduleWorkspaces(0)
    return { space_name: spaces().names.get(space) ?? name }
  }

  /**
   * A workspace archived in Paseo (archive_workspace_request): its agents archived, the workspace off its list, and a
   * worktree Paseo made removed from the disk, its branch kept (Paseo's archiveByScope). Paseo removes such a folder
   * whatever is in it: the node has had the user confirm what would be lost before it asks.
   */
  async function archiveWorkspace(params) {
    if (!paseo.connected) throw new RpcError('Paseo is not connected (not running, or restarting). Nothing was archived.', 'unavailable')
    const id = typeof params.workspace_id === 'string' ? params.workspace_id : ''
    if (!id || !workspaces.has(id)) throw new RpcError(`Paseo no longer has this workspace (${id || '(none)'}): refresh the list. Nothing was archived.`, 'workspace_missing')
    const res = await paseo.request('archive_workspace_request', { workspaceId: id }, { timeoutMs: ARCHIVE_WAIT })
    if (res?.error || !res?.archivedAt) throw new RpcError(`Paseo did not archive the workspace: ${res?.error || 'no reason given'}`, 'archive_failed')
    forgetWorkspace(id)
    scheduleWorkspaces(0)
    return { archived_at: res.archivedAt }
  }

  /**
   * A new session (the form, or the machine's engine starting one): a new agent of the provider the launcher names.
   * A launcher Paseo has no usable provider of (none of that name, or one switched off or failing) starts on one of
   * the same kind of agent (agent_type, the node's word for what the launcher starts), chosen here and said on the
   * answer (instead); with none of that kind it is refused, never run as another kind of agent or on Paseo's default
   * (docs/integrations/paseo.md "A launcher Paseo does not have"). No launcher named: Paseo's default provider. The
   * model stays only with a provider that offers it; an effort the model does not offer goes down to the nearest
   * lower one it does (effortFor), and the answer says so. The mode and first message apply as asked. Its id is the
   * one the hub named (session_id) when it can be (namedSession), else paseo-<agent id>, kept on the agent as a label.
   */
  async function start(params) {
    if (params.resume) return takeOver(params)
    if (!paseo.connected) throw new RpcError('Paseo 现在没有连上（没在运行，或正在重启），这次没有新建。', 'unavailable')
    const cwd = expandHome(typeof params.cwd === 'string' ? params.cwd : '')
    if (!cwd || !isDir(cwd)) throw new RpcError(`没有这个目录：${cwd || '（空）'}`, 'bad_request')
    // Read-only (plan): only a provider holding plan mode starts it, and nothing falls back to a writable one (#3238).
    const readonly = params.permission_mode === 'plan'
    const asked = typeof params.launcher === 'string' ? params.launcher : ''
    // What the start asked for that it does not run with, for the node to say (the row says what it runs with).
    const instead = {}
    let provider = launchers.some((l) => l.name === asked) ? asked : undefined
    if (readonly && provider && !holdsPlan(provider)) {
      throw new RpcError(`Paseo 的 ${provider} 没有只读（plan）模式，这次没有新建。选一个 Claude Code 的启动器再试。`, 'readonly_unsupported')
    }
    if (!provider && asked) {
      const kind = kindOf(asked, params.agent_type)
      provider = sameKindProvider(launchers, roots, kind, (p) => !readonly || holdsPlan(p))
      if (!provider && readonly && sameKindProvider(launchers, roots, kind)) {
        throw new RpcError(`Paseo has no provider for ${asked} that starts read-only (plan): nothing was started. Pick a Claude Code launcher.`, 'readonly_unsupported')
      }
      if (!provider) throw new RpcError(noKindMessage(asked, kind), 'launcher_kind_missing')
      instead.launcher = asked
      log(`paseo: Paseo has no usable provider ${asked}; starting on ${provider}, of the same kind (${kind})`)
    }
    if (!provider) {
      provider = defaultProvider(readonly ? entries.filter((e) => holdsPlan(e?.provider)) : entries, roots)
      if (!provider && readonly) {
        throw new RpcError('Paseo 里没有能以只读（plan）模式启动的 provider（要 Claude Code 的），这次没有新建。', 'readonly_unsupported')
      }
      if (!provider) throw new RpcError('Paseo 里没有能用的 provider：在 Paseo 的设置里打开一个再试', 'unavailable')
    }
    const root = roots[provider] ?? provider
    const config = { provider, cwd }
    // A model id is the asked launcher's (a tier may have resolved it upstream): one standing in keeps it only when it
    // offers that model too, else it runs its own default and the answer says so.
    const model = typeof params.model === 'string' ? params.model : ''
    if (model && (!instead.launcher || offersModel(provider, model))) config.model = model
    else if (model) instead.model = model
    const modeId = paseoMode(root, params.permission_mode)
    if (modeId) config.modeId = modeId
    // Allow all where approval is Paseo's Auto Accept (Grok and its other ACP providers, OpenCode): started with it on.
    else if (params.permission_mode === 'bypassPermissions' && AUTO_ACCEPT_ROOTS.has(root)) config.featureValues = { [AUTO_ACCEPT]: true }
    if (EFFORTS.includes(params.effort)) {
      // The model's thinking options, as Paseo listed them: an effort it does not offer goes down to one it does. None
      // listed (Paseo has not said): as asked.
      const opts = thinkingOptions({ provider, model: config.model })
      const effort = opts.length ? effortFor(params.effort, opts) : params.effort
      if (effort) config.thinkingOptionId = effort
      if (effort !== params.effort) {
        instead.effort = params.effort
        log(`paseo: ${provider}'s model does not offer effort ${params.effort}; starting at ${effort ?? 'its default'}`)
      }
    }
    const prompt = typeof params.prompt === 'string' ? params.prompt : ''
    const implicitWorkspace = !(typeof params.workspace_id === 'string' && params.workspace_id)
    let projectId
    let projectError
    if (implicitWorkspace) {
      try {
        projectId = await projectAt(cwd)
      } catch (err) {
        projectError = err
      }
    }
    // Main-project preference is best effort for an implicit start: an existing worktree workspace remains usable when
    // project discovery fails. With no such fallback, keep the original creation error instead of allocating blindly.
    let workspaceId = workspaceFor(params.workspace_id, cwd, { strict: true, projectId })
    if (!workspaceId && projectError) throw projectError
    // The Persona session is fixed for this start: the id the hub named for it (session_id: the manager names the
    // sessions it starts, to find one whose answer it lost), else paseo-<the first Paseo agent id>. The row handed to
    // the node while Paseo is still creating, the label, the tools' PERSONA_SESSION_ID and the listed session are all
    // it, whichever Paseo agent ends up holding it.
    let agentId = randomUUID()
    const sessionId = namedSession(params.session_id) ?? `${RUNTIME}-${agentId}`
    const createdAgentIds = new Set([agentId])
    const startDeadline = Date.now() + callWait
    const until = Date.now() + startGiveUp
    let tries = 0
    // One creation request, under a Paseo agent id of its own (the retry's is new; the Persona session stays). Claude
    // Code is given the Agent's own Persona tools as preapproved (Paseo's toolPolicy), so it never asks for them. Each
    // request has an idempotency key of its own, the same each time it is asked: Paseo's creation service keeps a
    // request by its key across connections, so one asked again after a reconnect (repeat) gets the agent Paseo made
    // for it, or waits for the one it is still making, never a second agent.
    const attempt = async (withTools, withPolicy) => {
      const a2aToken = withTools ? await rt.a2aMcpToken(sessionId).catch(() => undefined) : undefined
      const mcpServers = withTools ? personaMcpServers(options.mcpConfig, sessionId, log, a2aToken) : undefined
      const req = { agentId, idempotencyKey: `persona:${sessionId}:${++tries}`, config: mcpServers ? { ...config, mcpServers } : config,
        labels: { [SESSION_LABEL]: sessionId } }
      const policy = mcpServers && withPolicy && root === 'claude' ? preapproved(mcpServers) : undefined
      if (policy) req.config.toolPolicy = policy
      // Never send initialPrompt before read-only (plan) mode is verified (caps.readonly contract).
      if (prompt.trim() && !params.backend_workspace_root && !readonly) req.initialPrompt = prompt
      if (workspaceId) req.workspaceId = workspaceId
      // Paseo answers once the agent's provider is up, which has no bound of its own: the start gives up at until.
      const ask = () => paseo.request('agent.create.request', req, { timeoutMs: Math.max(until - Date.now(), 1000) })
      return repeat(`creating agent ${req.agentId}`, ask, until, () => listedAs(sessionId))
        .then((res) => ({ res, withTools: Boolean(mcpServers), policy: Boolean(policy) }))
    }
    const trackAgentId = (aid) => {
      if (!createdAgentIds.has(aid)) {
        createdAgentIds.add(aid)
        if (readonly) beginReadonlyAgent(aid)
      }
    }
    let readonlyCleaned = false
    const cleanupReadonly = () => {
      if (!readonly || readonlyCleaned) return
      readonlyCleaned = true
      endReadonly(sessionId)
      for (const aid of createdAgentIds) endReadonlyAgent(aid)
    }
    if (readonly) {
      beginReadonly(sessionId)
      beginReadonlyAgent(agentId)
    }
    let finishOp
    let rejectOp
    let markOpDone
    let opSettled = false
    const opPromise = new Promise((resolve, reject) => { finishOp = resolve; rejectOp = reject })
    opPromise.catch(() => {})
    const opDone = new Promise((resolve) => { markOpDone = resolve })
    const opEntry = { readonly, promise: opPromise, done: opDone, row: undefined }
    const settleOp = (agent, err) => {
      if (opSettled) return
      opSettled = true
      if (pendingOps.get(sessionId) === opEntry) pendingOps.delete(sessionId)
      cleanupReadonly()
      if (err) rejectOp(err)
      else finishOp(agent)
      markOpDone()
    }
    pendingOps.set(sessionId, opEntry)
    let readonlyHandedOff = false
    const clientMsgId = (typeof params.client_msg_id === 'string' && params.client_msg_id) || `readonly-start:${sessionId}`
    try {
      // A provider that takes no MCP servers fails the creation outright (Paseo closes that session before it has an
      // agent): it gets the agent as it did before the Persona tools, in one more request, and for that error only. One
      // that cannot preapprove them gets them without, asking as before (allowOwn answers those asks).
      const created = (async () => {
        if (!workspaceId) {
          if (!implicitWorkspace) projectId = await projectAt(cwd)
          if (projectId) {
            // agent.create has no projectId field. Create the directory workspace with explicit ownership, then start
            // in it. Keep this inside the start's existing wait/late-result flow, with one key across reconnects.
            const req = { source: { kind: 'directory', path: cwd, projectId }, idempotencyKey: `persona:workspace:${sessionId}` }
            const made = await repeat(`creating workspace for ${sessionId}`,
              () => paseo.request('workspace.create.request', req, { timeoutMs: Math.max(until - Date.now(), 1000) }), until, () => undefined)
            const d = made?.workspace
            if (params.backend_workspace_root && (!d?.id || d.projectId !== projectId || !samePath(d.workspaceDirectory, cwd))) {
              throw new RpcError('Paseo could not open the prepared task directory', 'backend_workspace_start_refused')
            }
            if (!d?.id || d.projectId !== projectId) {
              throw new RpcError(`Paseo did not create the workspace in the selected project: ${made?.error || 'no matching workspace returned'}`, 'create_failed')
            }
            if (!workspaces.get(d.id)?.project) keepWorkspace(d)
            workspaceId = d.id
            scheduleWorkspaces(0)
          } else {
            // No existing Paseo project yet: canonicalize cwd so Paseo registers the physical root, not a symlink alias.
            config.cwd = physicalPath(cwd)
          }
        }
        return attempt(true, true)
      })().then(({ res, withTools, policy }) => {
        if (!withTools || res?.agent) return res
        if (policy && POLICY_UNSUPPORTED.test(res?.error ?? '')) {
          log(`paseo: provider ${provider} cannot preapprove the Agent's own tools; creating the agent again without that`)
          agentId = randomUUID()
          trackAgentId(agentId)
          return attempt(true, false).then((again) => again.res)
        }
        if (!MCP_UNSUPPORTED.test(res?.error ?? '')) return res
        log(`paseo: provider ${provider} takes no MCP servers; creating the agent again without the Persona tools`)
        agentId = randomUUID()
        trackAgentId(agentId)
        return attempt(false, false).then((again) => again.res)
      })
      created.then((res) => !readonly && res?.agent && upsert(res.agent), () => {})
      // Paseo started a read-only start in another or unknown mode after all: archive it so it never runs writable.
      const notReadonly = async (agent) => {
        if (!readonly || (modeId && agent.currentModeId === modeId)) return false
        await archiveReadonlyViolation(agent.id, `started in ${agent.currentModeId || 'unknown'} mode`)
        remove(agent.id)
        return true
      }
      const deliverReadonlyPrompt = async (agent, waitMs) => {
        const live = agents.get(agent.id)?.agent
        if (!live || live.currentModeId !== modeId) {
          await archiveReadonlyViolation(agent.id, `mode changed before initial prompt (${live?.currentModeId || 'evicted'})`)
          remove(agent.id)
          throw new RpcError(`Paseo 没有保持只读（plan）模式（实际：${live?.currentModeId || 'unknown'}），已经归档。`, 'readonly_unsupported')
        }
        const liveRow = rowOf(live)
        if (!liveRow) throw new RpcError('Paseo started the read-only agent without a session id', 'start_failed')
        await deliverMessage({ session_id: liveRow.session_id, text: prompt, client_msg_id: clientMsgId }, waitMs)
      }
      // The row the session will have while Paseo is still creating it.
      const placeholder = () => ({
        session_id: sessionId, cwd, title: prompt.trim() ? snippet(prompt) : basename(cwd), status: 'idle', starting: true,
        updated_at: new Date().toISOString(), rev: Date.now(), ...placeOf(workspaceId, cwd), agent: appAgent(root), launcher: provider,
        started_by_persona: true, ...(config.model ? { model: config.model } : {}),
        // Labelled as the listed row will be: a provider the apps do not know carries its label (#8142).
        ...(appAgent(root) === RUNTIME && labels[provider] ? { agent_label: labels[provider] } : {}),
        ...(EFFORTS.includes(config.thinkingOptionId) ? { effort: config.thinkingOptionId } : {}),
      })
      const said = Object.keys(instead).length ? { instead } : {}
      const res = params.backend_workspace_root ? await created : await within(created, callWait)
      if (res === TIMEOUT) {
        // Still being created (its provider starting, Paseo restarting): listed as starting until Paseo is done (track).
        // Then the agent's row takes over, or this one says why it did not start.
        const row = placeholder()
        readonlyHandedOff = true
        opEntry.row = row
        const tracked = created.then(async (late) => {
          if (!late?.agent) throw new Error(late?.error || 'no reason given')
          if (await notReadonly(late.agent)) throw new Error(`it came up in ${late.agent.currentModeId || 'unknown'} mode, not read-only (plan), and was archived`)
          upsert(late.agent)
          if (readonly && prompt.trim()) {
            await deliverReadonlyPrompt(late.agent, startGiveUp)
          }
          return late.agent
        }).then((agent) => {
          settleOp(agent)
          return agent
        }, (err) => {
          settleOp(undefined, err)
          throw err
        })
        track(sessionId, row, tracked, `starting ${sessionId}`, undefined, readonly)
        return { session: row, ...said }
      }
      if (!res?.agent) throw new RpcError(`Paseo 没能新建：${res?.error ?? '原因不明'}`, 'start_failed')
      if (await notReadonly(res.agent)) {
        throw new RpcError(`Paseo 没有以只读（plan）模式启动（实际：${res.agent.currentModeId || 'unknown'}），已经归档。`, 'readonly_unsupported')
      }
      upsert(res.agent)
      const row = rowOf(res.agent)
      if (params.backend_workspace_root) {
        if (!row || !samePath(res.agent.cwd, cwd) || !samePath(row.cwd, cwd)) {
          throw new RpcError('Paseo returned an agent outside the prepared task directory', 'backend_workspace_cwd_mismatch')
        }
        if (prompt.trim()) {
          await deliverMessage({ session_id: row.session_id, text: prompt, client_msg_id: `workspace-start:${sessionId}` }, startGiveUp)
        }
      } else if (readonly && prompt.trim()) {
        await deliverReadonlyPrompt(res.agent, Math.max(startDeadline - Date.now(), 1000))
      }
      // The effort it runs at, while Paseo's record does not say it yet.
      if (row && !row.effort && config.thinkingOptionId && EFFORTS.includes(config.thinkingOptionId)) row.effort = config.thinkingOptionId
      settleOp(res.agent)
      return { session: row ?? placeholder(), ...said }
    } catch (err) {
      if (!readonlyHandedOff) settleOp(undefined, err)
      throw err
    } finally {
      if (!readonlyHandedOff && !opSettled) settleOp(undefined, new Error('start did not complete'))
    }
  }

  /**
   * The session id the hub named for a start (session.start's session_id), when it can be this start's: one the node
   * takes, that no agent, archived row or other start has. Else the start picks its own (paseo-<agent id>), and the
   * hub finds a start whose answer it lost among the sessions Persona started instead (started_by_persona).
   */
  function namedSession(id) {
    if (typeof id !== 'string' || !SESSION_ID.test(id)) return undefined
    if (bySession.has(id) || inFlight.has(id) || pendingOps.has(id) || takenBy(id, undefined, true)) {
      log(`paseo: session ${id} is someone else's already; this start gets an id of its own`)
      return undefined
    }
    return id
  }

  /** What Paseo's directory lists under session id now, as a creation's answer would have it ({agent}); else undefined. */
  function listedAs(sessionId) {
    const agent = agents.get(bySession.get(sessionId))?.agent
    return agent ? { agent } : undefined
  }

  /** Whether provider starts in plan mode: one running Claude Code whose Paseo modes (when listed) include plan. */
  function holdsPlan(provider) {
    if (!provider || (roots[provider] ?? provider) !== 'claude') return false
    const e = entries.find((x) => x?.provider === provider)
    return !Array.isArray(e?.modes) || !e.modes.length || e.modes.some((m) => m?.id === 'plan')
  }

  /**
   * The kind of agent launcher name starts, as a Paseo root (claude, codex, omp…): the node's word for it
   * (agent_type, which it sends for every launcher Paseo does not have), else the root of Paseo's own provider of that
   * name (one switched off or failing), else what the name says, as the node's launcherAgentType reads it (pi,
   * cursor, agy, codex, traex and omp by name, else Claude Code). '' for Paseo's catch-all agent, which names no kind.
   */
  function kindOf(name, agentType) {
    if (typeof agentType === 'string' && agentType) return agentType === RUNTIME ? '' : agentType
    if (entries.some((e) => e?.provider === name)) return roots[name] ?? name
    return ['pi', 'cursor', 'agy', 'codex', 'traex', 'omp'].includes(name) ? name : 'claude'
  }

  /** Whether provider lists model (by its id or an alias); false while Paseo has listed none for it. */
  function offersModel(provider, model) {
    return modelsOf(provider).some((m) => m?.id === model || (m?.aliases ?? []).includes(model))
  }

  /** Why a start of launcher name, of agent kind kind, was refused: what Paseo has of it, and what to do. */
  function noKindMessage(name, kind) {
    const own = entries.find((e) => e?.provider === name)
    const why = !own ? `Paseo has no provider ${name}` : !own.enabled ? `Paseo's ${name} is turned off` : `Paseo's ${name} cannot start agents now (${own.status || 'not ready'})`
    const label = kind === 'claude' ? 'Claude Code' : kind === 'codex' ? 'Codex' : kind || 'its kind'
    return `${why}, and none of its other providers runs ${label}: nothing was started, and no other kind of agent runs in its place. ` +
      `Turn one on in Paseo's settings, or pick one of its providers.`
  }

  /**
   * Take a Claude session over (the machine's engine continuing a session not running anywhere): the Paseo agent
   * that has it already, else Paseo imports it — its conversation goes on in Paseo under the same id. A session an
   * archived agent keeps under its own id (archivedRows) is that agent, back from the archive with the id on its
   * label: Paseo imports a session it has an archived record of into that record. So is the Claude session of an
   * archived agent named to the node (unlistedTranscripts): imported with that agent's own provider, directory and
   * workspace, as Paseo finds its record by provider and session; with another provider Paseo made a second agent of
   * it beside the archived one. Refused when a claude Paseo does not run has it open (two writers of one transcript).
   */
  async function takeOver(params, wait = callWait) {
    const id = params.resume
    const readonly = params.permission_mode === 'plan'
    const deadline = Date.now() + wait
    if (!paseo.connected) throw new RpcError('Paseo 现在没有连上（没在运行，或正在重启），这次没有发送。', 'unavailable')
    // Resolve the actual active receiver first (bySession), so shared-session agents validate and return the real target.
    let mine = agents.get(bySession.get(id))?.agent
    if (!mine) {
      for (const { agent } of agents.values()) {
        if (claudeId(agent) === id && agentRoot(agent, roots) === 'claude' && (!mine || mine.status === 'closed')) mine = agent
      }
      if (mine) {
        const activeId = bySession.get(sessionIdOf(mine, roots))
        if (activeId) mine = agents.get(activeId)?.agent ?? mine
      }
    }
    const kept = mine ? undefined : archivedOf(id)
    const was = mine || kept ? undefined : archivedWith(id)
    if (readonly && (mine ?? kept ?? was)?.currentModeId !== 'plan') {
      // A takeover keeps the session's own mode (an import sets none): it cannot promise read-only (caps.readonly).
      throw new RpcError('Paseo 接着已有的会话时不能保证只读（plan）模式，这次没有发送。', 'readonly_unsupported')
    }
    if (mine) {
      const row = rowOf(mine)
      if (row) return { session: row }
    }
    // Check both active pre-timeout ops (0..callWait) and tracked in-flight ops.
    const activeOp = pendingOps.get(id)
    const going = inFlightOf(id)
    const goingActive = going && !going.failed && !going.done
    if (activeOp || goingActive) {
      const opReadonly = activeOp ? activeOp.readonly : going.readonly
      if (readonly && !opReadonly) {
        throw new RpcError('Paseo 接着已有的会话时不能保证只读（plan）模式，这次没有发送。', 'readonly_unsupported')
      }
      if (!readonly && opReadonly) {
        // Do not bind a writable takeover to a readonly op (which force-archives non-plan results and disables retries).
        const waited = activeOp?.done ?? going.work.then(() => {}, () => {})
        const settled = await within(waited, Math.max(deadline - Date.now(), 0))
        if (settled === TIMEOUT) {
          throw new RpcError('这个会话正在以只读（plan）模式恢复中，请稍后再试。', 'session_busy')
        }
        // Yield once so track() settles inFlight; never spin if the op or deadline did not advance.
        await Promise.resolve()
        if (going && inFlight.get(id) === going && !going.done) inFlight.delete(id)
        const left = deadline - Date.now()
        if (left <= 0 || (activeOp && pendingOps.get(id) === activeOp)) {
          throw new RpcError('这个会话正在以只读（plan）模式恢复中，请稍后再试。', 'session_busy')
        }
        return takeOver(params, left)
      }
      if (goingActive) return { session: going.row }
      const joined = await within(activeOp.promise, Math.max(deadline - Date.now(), 0))
      if (joined === TIMEOUT) {
        const lateGoing = inFlightOf(id)
        if (lateGoing && !lateGoing.failed && !lateGoing.done) return { session: lateGoing.row }
        if (activeOp.row) return { session: activeOp.row }
        throw new RpcError('Paseo 正在接管这个会话，请稍后再试。', 'timeout')
      }
      await activeOp.done
      const joinedRow = rowOf(joined)
      if (!joinedRow) throw new RpcError('Paseo 接上了这个会话，但它没有会话 id', 'start_failed')
      return { session: joinedRow }
    }
    if (params.open_elsewhere) {
      throw new RpcError('这个会话正在别处的 Claude Code 里开着（Paseo 之外），Paseo 再接着它就会有两个进程写同一个会话。先在那边关掉它。这次没有发送。', 'open_elsewhere')
    }
    const from = kept ?? was
    const cwd = expandHome(from?.cwd || (typeof params.cwd === 'string' ? params.cwd : ''))
    if (!cwd || !isDir(cwd)) throw new RpcError(`接不上这个会话：它的目录不在了（${cwd || '未知'}）`, 'bad_request')
    // The provider: the archived agent's own (Paseo finds its record by provider and session), else the launcher's
    // when it runs Claude Code, else the one this machine's Claude agents use most lately.
    const own = from?.persistence?.provider ?? from?.provider
    let provider = own && entries.some((e) => e.provider === own && e.enabled) ? own : undefined
    if (kept && agentRoot(kept, roots) === 'codex' && !provider) throw new RpcError('Provider unavailable', 'unavailable')
    provider ??= launchers.some((l) => l.name === params.launcher && roots[l.name] === 'claude') ? params.launcher : undefined
    if (!provider) {
      let latest
      for (const { agent } of agents.values()) {
        if (agentRoot(agent, roots) === 'claude' && entries.some((e) => e.provider === agent.provider && e.enabled) &&
          (!latest || (Date.parse(agent.updatedAt) || 0) > (Date.parse(latest.updatedAt) || 0))) latest = agent
      }
      provider = latest?.provider ?? defaultProvider(entries.filter((e) => roots[e.provider] === 'claude'), roots)
    }
    if (!provider) throw new RpcError('Paseo 里没有跑 Claude Code 的 provider，接不了这个会话', 'unavailable')
    const transcript = kept ? transcriptOf(kept, roots) : id
    const thread = kept ? codexThreadOf(kept, roots) : undefined
    const imp = { provider, sessionId: transcript ?? thread, cwd, labels: { [SESSION_LABEL]: id } }
    const workspaceId = workspaceFor(from?.workspaceId, cwd)
    if (workspaceId) imp.workspaceId = workspaceId
    // The import has no limit of the node's call (a long conversation, a provider slow to start, #8130): past the
    // call's wait the session is listed as starting until Paseo is done (track), and the message that follows waits
    // for it (send). Paseo imports a session once and refuses the next import of it, so an import asked again after a
    // reconnect takes the agent its directory has for it by then.
    const until = Date.now() + startGiveUp
    const ask = async () => {
      try {
        const res = await paseo.request('import_agent_request', imp, { expect: 'status:agent_resumed', timeoutMs: Math.max(until - Date.now(), 1000) })
        if (!res?.agent) throw new Error('Paseo did not say which agent took the session over')
        return res.agent
      } catch (err) {
        const listed = /already imported/i.test(err?.message ?? '') && agents.get(bySession.get(id))?.agent
        if (listed) return listed
        throw err
      }
    }
    if (inFlight.get(id)?.failed) inFlight.delete(id)
    const rowPlaceholder = {
      session_id: id, cwd, title: (from?.title ?? '').trim() || basename(cwd), status: 'idle', starting: true,
      updated_at: new Date().toISOString(), rev: Date.now(), ...placeOf(workspaceId, cwd), agent: appAgent(roots[provider] ?? provider),
      launcher: provider, started_by_persona: true, ...(transcript ? { transcript } : thread ? { codex_thread: thread } : {}),
    }
    let markOpDone
    const opDone = new Promise((resolve) => { markOpDone = resolve })
    const opEntry = { readonly, promise: null, done: opDone, row: rowPlaceholder }
    const fromAgentId = from?.id
    if (readonly) {
      beginReadonly(id)
      if (fromAgentId) beginReadonlyAgent(fromAgentId)
    }
    const imported = repeat(`continuing session ${id}`, ask, until, () => agents.get(bySession.get(id))?.agent).then(async (agent) => {
      if (readonly && agent.currentModeId !== 'plan') {
        await archiveReadonlyViolation(agent.id, `resumed in ${agent.currentModeId || 'unknown'} mode`)
        remove(agent.id)
        for (const [aid, u] of archived) {
          if (aid === from?.id || aid === agent.id) {
            archived.set(aid, { ...u, agent: { ...u.agent, currentModeId: agent.currentModeId ?? null } })
          }
        }
        throw new RpcError(`Paseo 没有以只读（plan）模式恢复该会话（实际：${agent.currentModeId || 'unknown'}），已经归档。`, 'readonly_unsupported')
      }
      upsert(agent)
      return agent
    }).finally(() => {
      if (pendingOps.get(id) === opEntry) pendingOps.delete(id)
      if (readonly) {
        endReadonly(id)
        if (fromAgentId) endReadonlyAgent(fromAgentId)
      }
      markOpDone()
    })
    opEntry.promise = imported
    pendingOps.set(id, opEntry)
    const res = await within(imported, Math.max(deadline - Date.now(), 0))
    if (res === TIMEOUT) {
      // Should it fail, the next message to the session takes it over again (send), as one did before the import
      // had a row: the session is still the user's conversation, which a start that failed was not.
      track(id, rowPlaceholder, imported, `continuing session ${id}`, readonly ? undefined : (left) => takeOver(params, left), readonly)
      return { session: rowPlaceholder }
    }
    const row = rowOf(res)
    if (!row) throw new RpcError('Paseo 接上了这个会话，但它没有会话 id', 'start_failed')
    return { session: row }
  }

  // ---- watching: live text, and new items for agents whose content is not a transcript ----

  /**
   * The phone watches a session (or stops). Paseo sends an agent's timeline only to a subscription for it: one per
   * watched session.
   */
  function watchChanged(sessionId, watching, o) {
    const old = watches.get(sessionId)
    if (!watching) {
      if (old) {
        watches.delete(sessionId)
        clearTimeout(old.timer)
        paseo.release(old.sub)
        rt.streamEnd(sessionId)
      }
      return
    }
    // An archived agent that keeps its id is followed too: a message takes it back from the archive (archivedRows). One
    // Paseo is still creating or importing (track) has no agent yet: its watch waits, and follows the agent once Paseo
    // lists it (retarget), so its first turn's live text is not lost.
    const agentId = bySession.get(sessionId) ?? archivedOf(sessionId)?.id
    if (!agentId && !inFlightOf(sessionId)) return
    if (old) {
      old.transcript = o?.transcript === true
      return
    }
    watches.set(sessionId, { agentId, transcript: o?.transcript === true, sub: undefined, last: 0, safe: undefined, epoch: undefined,
      timer: undefined, fetching: false, again: false, live: { key: '', text: '' } })
    void follow(sessionId)
  }

  /**
   * Watched sessions another agent now has (bySession moved: one closed, another took the session on): the watch
   * leaves the old agent's timeline and follows the new one's, whose content is another history.
   */
  function retarget() {
    for (const [sessionId, w] of watches) {
      const agentId = bySession.get(sessionId)
      if (!agentId || agentId === w.agentId) continue
      clearTimeout(w.timer)
      paseo.release(w.sub)
      if (w.live.text) rt.streamEnd(sessionId)
      watches.set(sessionId, { agentId, transcript: w.transcript, sub: undefined, last: 0, safe: undefined, epoch: undefined,
        timer: undefined, fetching: false, again: false, live: { key: '', text: '' }, switched: true })
      void follow(sessionId)
      scheduleRefresh(sessionId)
    }
  }

  async function follow(sessionId) {
    const w = watches.get(sessionId)
    if (!w || !w.agentId || !paseo.connected) return
    paseo.release(w.sub) // followed again on this connection (an old one's subscriptions are gone with it: a no-op)
    w.sub = undefined
    try {
      const res = await paseo.subscribe('agent.timeline.set_subscription.request', { agentIds: [w.agentId] }, (inner) => onTimeline(sessionId, w, inner))
      // Gone meanwhile, or followed twice (a reconnect's follow while another was under way): one subscription.
      if (watches.get(sessionId) !== w || w.sub !== undefined) paseo.release(res?.subscriptionId)
      else w.sub = res?.subscriptionId
    } catch (err) {
      log(`paseo: follow ${w.agentId}: ${err.message}`)
    }
  }

  function onTimeline(sessionId, w, inner) {
    if (watches.get(sessionId) !== w) return
    const p = inner.payload ?? {}
    if (inner.type === 'agent.timeline.replacement') {
      // Its history was rewritten (a rewind): read it whole; the new epoch is another history, which the phone takes
      // again (the epoch it had is kept, so that refresh sees the change).
      w.safe = undefined
      timelines.delete(w.agentId)
      scheduleRefresh(sessionId)
      return
    }
    if (inner.type !== 'agent_stream') return
    const ev = p.event ?? {}
    if (ev.type === 'timeline' && ev.item?.type === 'assistant_message') {
      // Paseo sends what was added since the last chunk: the live text is all of this message's so far.
      const key = `${ev.turnId ?? ''}|${ev.item.messageId ?? ''}`
      if (w.live.key !== key) w.live = { key, text: '' }
      w.live.text += ev.item.text ?? ''
      if (w.live.text) rt.stream(sessionId, w.live.text)
    } else if (ev.type === 'timeline' || ev.type?.startsWith('turn_')) {
      if (w.live.text) rt.streamEnd(sessionId)
      w.live = { key: '', text: '' }
    }
    if (ev.type?.startsWith('turn_')) rt.sessionsChanged(0)
    if (!w.transcript) scheduleRefresh(sessionId)
  }

  function scheduleRefresh(sessionId) {
    const w = watches.get(sessionId)
    if (!w || w.transcript || w.timer) return
    w.timer = setTimeout(() => {
      w.timer = undefined
      void refresh(sessionId)
    }, REFRESH_DELAY)
    w.timer.unref?.()
  }

  /** Push what a watched agent's timeline has new: read from where something can still change, push what is final. */
  async function refresh(sessionId) {
    const w = watches.get(sessionId)
    if (!w || w.transcript) return
    if (w.fetching) {
      w.again = true
      return
    }
    w.fetching = true
    try {
      const agent = agents.get(w.agentId)?.agent
      if (!agent || !paseo.connected) return
      // Paseo let it go while its chat was open: a stored conversation does not change, and reading it would load it
      // again each time (#8132). A watch moved to another agent (switched) reads the one it shows now, as opening does.
      if (unloaded(agent) && !w.switched) return
      // Another agent's timeline (switched) is read whole, whatever a page read of it set meanwhile.
      const incremental = !w.switched && w.epoch !== undefined && w.safe !== undefined
      let res = await fetchTimeline(agent.id, incremental ? { epoch: w.epoch, seq: w.safe } : undefined)
      if (watches.get(sessionId) !== w) return
      const moved = !incremental || res.reset || res.staleCursor || res.gap || res.epoch !== w.epoch
      if (moved && incremental) {
        res = await fetchTimeline(agent.id)
        if (watches.get(sessionId) !== w) return
      }
      const tl = timelineItems(res.entries, { maxSeq: res.window?.maxSeq, running: isRunning(res.agent ?? agent), echoes })
      const h = history(agent.id, res.epoch)
      if (moved && (w.switched || (w.epoch !== undefined && w.epoch !== res.epoch))) {
        // Another history: the phone takes the whole conversation again.
        rt.append(sessionId, { items: [], toSeq: tl.toSeq, history: h })
        w.last = tl.toSeq
      } else {
        const fresh = tl.items.filter((it) => it.seq > w.last)
        if (fresh.length || tl.toSeq > w.last) {
          rt.append(sessionId, { items: fresh.map(light), fromSeq: w.last, toSeq: Math.max(tl.toSeq, w.last), history: h })
          w.last = Math.max(w.last, tl.toSeq)
        }
      }
      w.epoch = res.epoch
      w.switched = false
      w.safe = Math.min(tl.safeSeq, w.last)
      if (tl.lastText) lastTexts.set(agent.id, tl.lastText)
    } catch (err) {
      log(`paseo: timeline of ${w.agentId}: ${err.message}`)
    } finally {
      w.fetching = false
      if (w.again) {
        w.again = false
        scheduleRefresh(sessionId)
      }
    }
  }

  /** An agent whose content is not a transcript: its last words for the list, read a while after it changed. */
  function lastTextSoon(agentId) {
    if (lastTextTimers.has(agentId)) return
    const t = setTimeout(async () => {
      lastTextTimers.delete(agentId)
      const agent = agents.get(agentId)?.agent
      if (!agent || agent.status === 'closed' || !paseo.connected) return // reading a closed agent would load it
      try {
        const res = await paseo.request('fetch_agent_timeline_request', { agentId, direction: 'tail', limit: 8 }, { timeoutMs: 30_000 })
        const { lastText } = timelineItems(res?.entries, { maxSeq: res?.window?.maxSeq, running: isRunning(res?.agent ?? agent) })
        if (lastText && lastText !== lastTexts.get(agentId)) {
          lastTexts.set(agentId, lastText)
          rt.sessionsChanged()
        }
      } catch {}
    }, LAST_TEXT_DELAY)
    t.unref?.()
    lastTextTimers.set(agentId, t)
  }

  return {
    /** Connect to Paseo; the bridge joins the node once Paseo answers. */
    start() {
      if (started) return
      started = true
      if (sweepMs > 0) {
        sweepTimer = setInterval(() => void sweep(), sweepMs)
        sweepTimer.unref?.()
      }
      paseo.start()
    },
    stop() {
      started = false
      background.disconnect()
      clearTimeout(awayTimer)
      clearInterval(sweepTimer)
      for (const t of closedTimers) clearTimeout(t)
      closedTimers.clear()
      for (const t of lastTextTimers.values()) clearTimeout(t)
      lastTextTimers.clear()
      clearTimeout(workspacesTimer)
      workspacesTimer = undefined
      for (const a of asks.values()) a.abort()
      for (const w of backWaiters) w.fail(new Error('the bridge stopped'))
      rt.stop()
      runtimeOn = false
      paseo.stop()
    },
    runtime: rt,
    paseo,
    /** Tests: the bridge's view. */
    state: { agents, bySession, archived, watches, asks, handedBack, echoes, timelines, workspaces, inFlight, get launchers() { return launchers } },
  }
}

/** A UUID (v5-shaped) that is the same for the same text. */
export function uuidOf(text) {
  const h = createHash('sha256').update(text).digest()
  h[6] = (h[6] & 0x0f) | 0x50
  h[8] = (h[8] & 0x3f) | 0x80
  const x = h.subarray(0, 16).toString('hex')
  return `${x.slice(0, 8)}-${x.slice(8, 12)}-${x.slice(12, 16)}-${x.slice(16, 20)}-${x.slice(20)}`
}
