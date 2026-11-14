// The Paseo bridge (docs/integrations/paseo.md): the agents of the Paseo daemon on this machine as the sessions of its engine in
// Persona, through the runtime SDK. Paseo runs every agent through its SDK, with no terminal; the bridge follows its
// directory of agents (one subscription), its pending requests (cards on the phone, withdrawn when answered in Paseo),
// and — only while the phone watches a session — that agent's timeline (live text; items for agents that are not
// Claude Code's). An agent running Claude Code names its transcript: the node shows it from there, as in herdr.
// Everything Paseo-specific comes in through deps.paseo, so the tests drive it with a fake daemon.

import { createHash, randomUUID } from 'node:crypto'
import { readFileSync, realpathSync, statSync } from 'node:fs'
import { homedir } from 'node:os'
import { basename, dirname, isAbsolute, join, resolve } from 'node:path'
import { createRuntime, light, page, RpcError, snippet } from '../../runtime-sdk/index.js'
import {
  AUTO_ACCEPT, AUTO_ACCEPT_ROOTS, CHECK_LABEL, EFFORTS, KEPT, KEPT_LABEL, RUNTIME, SESSION_LABEL, WORKSPACE_ID, agentRoot, agentRow, appAgent,
  archivedUnderOwnId, autoAcceptOf, claudeId, codexThreadOf, defaultProvider, formModel, interactionOf, launchersOf, linkedWorktree,
  parentAgentId, paseoCheckouts, paseoMode, projectSpaces, promptKey, providerRoots, responseOf, rewindOf, sessionIdOf, slotNameOf,
  spaceIdOf, strayRecord, timelineItems, transcriptOf, workspaceRow,
} from './map.js'
import { createLineage } from './lineage.js'
import { personaMcpServers } from './mcp.js'
import { PaseoClient } from './paseo.js'

export const BRIDGE_VERSION = '0.1.0'
const AWAY_GRACE = 30_000 // ms: Paseo gone this long and the bridge leaves the node (its sessions go with it)
const CALL_WAIT = 25_000 // ms: the node waits 30 s for a start or a send; Paseo's answer may take longer
const LIST_MAX = 300 // sessions listed at most (those running or waiting always)
const REFRESH_DELAY = 120 // ms: a watched agent's timeline is read again this long after it moved
const LAST_TEXT_DELAY = 3000 // ms: an agent's last words are read again this long after it changed
const PAGE = 200 // agents in a page of Paseo's directory
const PAGES_MAX = 10 // directory pages read at connect
// Agents archived in Paseo have their Claude session named to the node this long: its list of sessions not running
// goes back 7 days. The ones archived before the bridge was told are read at connect (loadArchived).
const UNLISTED_DAYS = 8
const ARCHIVED_MAX = 500 // archived agents kept (the latest)
// Those that keep their own id (map.js archivedUnderOwnId) are rows of the bridge's own, not running, beside the
// LIST_MAX: as many as the node lists sessions not running, the latest 80 archived in the last 7 days.
const ARCHIVED_DAYS = 7
const ARCHIVED_ROWS = 80
// Paseo tells that an agent was deleted only to the client that deleted it (whatBecameOf). Any other sees an agent that
// ran closed, and Paseo is asked for it this long after, twice: it closes an agent before it removes its record. Of an
// agent that did not run nothing is seen: the directory is read again this often (sweep).
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
// workspace.archive waits for Paseo to remove a worktree it made (its worktree removal takes 2 minutes at most, then
// its teardown), within the node's 140 s for the call.
const ARCHIVE_WAIT = 135_000 // ms
const WORKSPACES_DELAY = 200 // ms: workspace updates come in bursts (each agent's turn moves its workspace's status)
// What Paseo 0.13 says when an agent config has MCP servers and its provider takes none (agent-manager requireExternalMcpSupport).
const MCP_UNSUPPORTED = /does not support MCP servers/i
// What Paseo says when a provider cannot preapprove MCP tools (provider-options ToolPolicyUnsupportedError, agent-manager).
const POLICY_UNSUPPORTED = /cannot preapprove exact MCP tools/i
const REWIND_WAIT = 120_000 // ms: Paseo answers a rewind once it is done (a Claude Code fork, files put back)
// A launcher check (check): the probe's answer within this, and the rest of the node's 90 s for creating and cleaning up.
const CHECK_WAIT = 70_000 // ms

/**
 * @param {{ paseo?: object, log?: (msg: string) => void, connect?: (path: string) => import('node:net').Socket }} [deps]
 *   paseo: a PaseoClient (default: one for options.home).
 * @param {{ home?: string, socketPath?: string, mcpConfig?: string, allowTools?: string[], retryMs?: number, pushDelayMs?: number,
 *   refreshMs?: number, awayGrace?: number, callWait?: number, listMax?: number, pageSize?: number, closedChecks?: number[],
 *   sweepMs?: number, claudeHome?: string, claudeEnv?: object }} [options]
 *   allowTools: the Persona tools (full MCP names) an Agent Persona created calls without a card (the node's, #4966).
 *   sweepMs: 0 never reads the directory again.
 *   claudeHome, claudeEnv: where Claude Code's transcripts are looked for (lineage.js): home's .claude and env's
 *   $CLAUDE_CONFIG_DIR (default: this process's).
 */
export function createPaseoBridge(deps = {}, options = {}) {
  const log = deps.log ?? (() => {})
  const paseo = deps.paseo ?? new PaseoClient({ home: options.home, log, appVersion: `persona-paseo-bridge/${BRIDGE_VERSION}` })
  const awayGrace = options.awayGrace ?? AWAY_GRACE
  const callWait = options.callWait ?? CALL_WAIT
  const listMax = options.listMax ?? LIST_MAX
  const pageSize = options.pageSize ?? PAGE
  const closedChecks = options.closedChecks ?? CLOSED_CHECKS
  const sweepMs = options.sweepMs ?? SWEEP_EVERY

  const agents = new Map() // agent id → { agent, project }
  const bySession = new Map() // session id → agent id
  let configProviders = {} // provider id → { extends, label } (nothing else of the daemon's config is kept)
  let entries = [] // the providers snapshot
  let roots = {}
  let labels = {} // provider id → its label
  let launchers = []
  const watches = new Map() // session id → watch (see watchChanged)
  const asks = new Map() // permission id → { abort, agentId }
  const allowTools = new Set(options.allowTools ?? [])
  const handedBack = new Set() // permission ids the phone handed back to the computer: Paseo's own app answers them
  const echoes = new Map() // Paseo message id → the client_msg_id the phone sent it with
  const lastTexts = new Map() // agent id → its last words (agents whose content is not a transcript)
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
    // modes() changes a session's model too (set_agent_model_request).
    modelSwitch: true,
    socketPath: options.socketPath,
    connect: deps.connect,
    log,
    retryMs: options.retryMs,
    pushDelayMs: options.pushDelayMs,
    refreshMs: options.refreshMs,
    onHub: (connected) => {
      if (connected) askPending()
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
   * Which agent each session id is, when two claim one id: the one whose own id it is (not only kept, withKept), then
   * the one not closed, else the latest.
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
      const better = (a) => [a[KEPT] ? 0 : 1, a.status !== 'closed' ? 1 : 0, Date.parse(a.updatedAt) || 0]
      const [x, y] = [better(agent), better(cur)]
      const i = x.findIndex((v, n) => v !== y[n])
      if (i < 0 || x[i] < y[i]) return
    }
    bySession.set(id, agent.id)
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
    agent = withKept(agent, prev ?? archived.get(agent.id), !loading)
    archived.delete(agent.id) // back from the archive: listed again
    const oldSession = prev ? sessionIdOf(prev.agent, roots) : undefined
    agents.set(agent.id, { agent, project: project ?? prev?.project })
    if (oldSession && oldSession !== sessionIdOf(agent, roots)) reindex()
    else {
      index(agent)
      retarget()
    }
    // Its requests: new ones to the phone, those no longer pending off it.
    const pending = new Set((agent.pendingPermissions ?? []).map((r) => r?.id))
    for (const r of agent.pendingPermissions ?? []) askFor(agent.id, r)
    for (const [id, a] of asks) if (a.agentId === agent.id && !pending.has(id)) a.abort()
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
   * that conversation itself (lineage.js; lines: whether that may be read now). The id rides on the bridge's copy (KEPT)
   * until Paseo's record has the label (pin).
   */
  function withKept(agent, prev, lines) {
    if (agentRoot(agent, roots) !== 'claude' || agent.labels?.[SESSION_LABEL]) {
      keptIds.delete(agent.id)
      return agent
    }
    const now = claudeId(agent)
    if (!now) return agent
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
   * An agent leaves the list. record: its record when Paseo archived it, when that is why, with its placement (project;
   * else the one it was listed with).
   */
  function remove(agentId, record, project) {
    const a = agents.get(agentId)
    if (record) noteArchived(record, project ?? a?.project)
    if (!a) return
    agents.delete(agentId)
    timelines.delete(agentId)
    lastTexts.delete(agentId)
    for (const [id, x] of asks) if (x.agentId === agentId) x.abort()
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

  /**
   * What became of an agent Paseo's list no longer has, or may no longer have. A removal from the list does not say
   * why: archived, deleted, or only hidden (its provider switched off). And Paseo tells that an agent was deleted only
   * to the client that deleted it: any other sees an agent that ran closed (closedSoon), and of one that did not,
   * nothing (sweep). Asked for the agent, Paseo answers with an archived one's record, and with none for a deleted one.
   * Only an archived agent's session stays (noteArchived): a deleted conversation is gone, as it is in Paseo, and an
   * update of it that comes late does not list it again.
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
   * The agents deleted in Paseo that nothing told of (whatBecameOf): its directory is read again, and Paseo is asked
   * for each agent listed here that the directory no longer has, and for each archived one kept here that its archive
   * no longer has. Asked, not taken as gone: an agent that changed between the reading of two pages is on neither.
   * The archived ones it still has are taken as it has them now: Paseo tells no list of a change to one (a rename in
   * its own app).
   */
  async function sweep() {
    if (sweeping || !runtimeOn || !paseo.connected) return
    sweeping = true
    try {
      const listed = [...agents.keys()]
      const named = [...archived.keys()]
      const missing = []
      if (listed.length) {
        const there = new Set()
        let cursor
        for (let i = 0; ; i++) {
          if (i === PAGES_MAX) return // more agents than are read at connect: which of them are gone cannot be told
          const p = await paseo.request('fetch_agents_request', { page: cursor ? { limit: pageSize, cursor } : { limit: pageSize } }, { timeoutMs: 60_000 })
          if (!Array.isArray(p?.entries)) return
          for (const e of p.entries) if (e?.agent?.id) there.add(e.agent.id)
          if (!p.pageInfo?.hasMore || !p.pageInfo.nextCursor) break
          cursor = p.pageInfo.nextCursor
        }
        missing.push(...listed.filter((id) => !there.has(id)))
      }
      if (named.length) {
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
      for (const id of missing) {
        if (!paseo.connected) return
        if (agents.has(id) || archived.has(id)) await whatBecameOf(id)
      }
    } catch (err) {
      log(`paseo: reading its directory again: ${err.message}`)
    } finally {
      sweeping = false
    }
  }

  /**
   * The Claude sessions of the agents archived lately that are not in the list again under another agent. Nothing
   * (the node keeps what it was told before) until Paseo's archive was read once: the bridge was just started and
   * knows none of them yet. Not the transcript of an agent that keeps an id of its own: its row names it (archivedRows),
   * and named here it would be listed under the transcript's id too while that row is not there.
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
    const version = String(info?.version ?? '')
    log(`paseo: connected to Paseo ${version || '(version unknown)'}`)
    try {
      const cfg = await paseo.request('get_daemon_config_request', {})
      configProviders = keepProviders(cfg?.config?.providers)
      await paseo.subscribe('session.events.set_subscription.request', { events: EVENTS }, onSessionEvent)
      const snap = await paseo.request('get_providers_snapshot_request', {})
      entries = Array.isArray(snap?.entries) ? snap.entries : []
      providersChanged()
      void settleProviders()
      await loadAgents()
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
    // sessions unnamed.
    void loadArchived().catch((err) => log(`paseo: reading its archived agents: ${err.message}`)).then(() => {
      archiveRead = true
      rt.sessionsChanged()
    })
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
    const take = (list) => {
      for (const e of list ?? []) {
        if (!e?.agent?.id) continue
        seen.add(e.agent.id)
        upsert(e.agent, e.project)
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
      if (!info?.hasMore) for (const id of [...agents.keys()]) if (!seen.has(id)) remove(id)
    } finally {
      loading = false
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
    const take = (list) => {
      for (const d of list ?? []) if (keepWorkspace(d)) seen.add(d.id)
    }
    const first = await paseo.subscribe('fetch_workspaces_request', { subscribe: {}, page: { limit: pageSize } }, onWorkspaceUpdate, { timeoutMs: 60_000 })
    take(first?.entries)
    let info = first?.pageInfo
    for (let i = 1; info?.hasMore && info.nextCursor && i < PAGES_MAX; i++) {
      const p = await paseo.request('fetch_workspaces_request', { page: { limit: pageSize, cursor: info.nextCursor } }, { timeoutMs: 60_000 })
      take(p?.entries)
      info = p?.pageInfo
    }
    // Gone while Paseo was away.
    if (!info?.hasMore) for (const id of [...workspaces.keys()]) if (!seen.has(id)) workspaces.delete(id)
    workspacesRead = true
    scheduleWorkspaces(0)
  }

  /** Keep a workspace Paseo lists; one being archived or in the background is none of the list's. */
  function keepWorkspace(d) {
    if (typeof d?.id !== 'string' || !d.id) return false
    if (d.background || d.archivingAt) {
      workspaces.delete(d.id)
      return false
    }
    workspaces.set(d.id, d)
    return true
  }

  function onWorkspaceUpdate(inner) {
    const p = inner.payload
    if (inner.type !== 'workspace_update' || !p) return
    if (p.kind === 'upsert' && p.workspace) keepWorkspace(p.workspace)
    else if (p.kind === 'remove' && p.id) workspaces.delete(p.id)
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

  function onAgentUpdate(inner) {
    const p = inner.payload
    if (inner.type !== 'agent_update' || !p) return
    if (p.kind === 'upsert' && p.agent) {
      upsert(p.agent, p.project ?? undefined)
      pin(agents.get(p.agent.id)?.agent)
    } else if (p.kind === 'remove' && p.agentId) {
      // Paseo also sends a removal for every change of an agent it never listed (its own internal ones, which name
      // the others): only one that was listed is asked about.
      if (!agents.has(p.agentId)) return
      remove(p.agentId)
      void whatBecameOf(p.agentId)
    }
    rt.sessionsChanged()
  }

  function onSessionEvent(inner) {
    const p = inner.payload ?? {}
    switch (inner.type) {
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
        const a = agents.get(p.agentId)?.agent
        if (a) a.pendingPermissions = (a.pendingPermissions ?? []).filter((r) => r?.id !== p.requestId)
        asks.get(p.requestId)?.abort()
        handedBack.delete(p.requestId)
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

  /** Put a pending request on the phone (once); the answer goes back to Paseo. Answered in Paseo: withdrawn. */
  function askFor(agentId, request) {
    if (!request?.id || asks.has(request.id) || handedBack.has(request.id)) return
    if (ownTool(agentId, request)) return allowOwn(agentId, request)
    if (!rt.connected) return
    const sessionId = sessionOf(agentId)
    if (!sessionId) return
    const ac = new AbortController()
    const entry = { agentId, abort: () => ac.abort(new Error('answered elsewhere')) }
    asks.set(request.id, entry)
    withPlan(agentId, request).then((r) => rt.ask(interactionOf(sessionId, r), { signal: ac.signal })).then(async (res) => {
      if (asks.get(request.id) !== entry) return
      asks.delete(request.id)
      const agent = agents.get(agentId)?.agent
      const root = agentRoot(agent, roots)
      const out = responseOf(request, res, root)
      if (!out) {
        // Handed back (or no hub to ask: asked again when it is back): Paseo's app answers it meanwhile.
        if (rt.hubConnected) handedBack.add(request.id)
        return
      }
      try {
        await paseo.request('agent_permission_response', { agentId, response: out.response },
          { requestId: request.id, expect: 'agent_permission_resolved', timeoutMs: 30_000 })
        const modeId = paseoMode(root, out.setMode)
        if (modeId) {
          const r = await paseo.request('set_agent_mode_request', { agentId, modeId })
          if (r?.accepted === false) log(`paseo: mode ${modeId} for ${agentId}: ${r.error ?? 'refused'}`)
        }
      } catch (err) {
        log(`paseo: answer ${request.id} of ${agentId}: ${err.message}`)
      }
    }, () => {
      if (asks.get(request.id) === entry) asks.delete(request.id)
    })
  }

  /**
   * Whether request is Claude Code asking for one of the Agent's own Persona tools (allowTools) in an Agent Persona
   * started (it carries SESSION_LABEL): those run without a card, as the node allows them in the Claude Agents it starts
   * (docs/integrations/claude-integration.md "Persona tools Agents call without a card"). An agent made in Paseo's own
   * app (one whose id the bridge kept, KEPT_LABEL, too), every other tool and every other provider's request still go
   * to the phone.
   */
  function ownTool(agentId, request) {
    if (!allowTools.has(request.name) || request.provider !== 'claude' || request.kind === 'question' || request.kind === 'plan') return false
    const labels = agents.get(agentId)?.agent?.labels
    const label = labels?.[SESSION_LABEL]
    return typeof label === 'string' && label !== '' && !labels[KEPT_LABEL]
  }

  /**
   * The Agent's own Persona tools (allowTools) as Paseo's preapproval for a new Claude Code agent (toolPolicy): those
   * of the servers it is given, by server and tool (`mcp__<server>__<tool>`). Undefined when there are none.
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
        unfinished.set(id, isRunning(a) || (a?.pendingPermissions?.length ?? 0) > 0 || (created.get(id) ?? []).some(busy))
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

  /** What agentRow takes beside the agent: its place, its last words, the agent that created it and its own fleet. */
  function rowOptions(agentId, s, fam) {
    const a = agents.get(agentId)
    const spaceId = s.idOf(a?.project)
    const by = fam.creator.get(agentId)
    return { project: a?.project, roots, labels, lastText: lastTexts.get(agentId), spaceId, spaceName: s.names.get(spaceId),
      parent: by && sessionOf(by), quiet: Boolean(a) && !workspaceOwn(a.agent), fleet: fam.fleet.get(agentId), canon }
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
    const busy = (r) => r.status === 'working' || r.status === 'blocked'
    const delegated = (r) => fam.creator.has(agentOf.get(r.session_id))
    const top = (r) => {
      let at = agentOf.get(r.session_id)
      for (let by = fam.creator.get(at); by !== undefined; by = fam.creator.get(at)) at = by
      return at
    }
    const kept = new Set(rows.filter((r) => delegated(r) && (busy(r) || rt.touched(r.session_id))).map(top))
    const own = rt.recent(rows.filter((r) => !delegated(r)), { days: 3650, max: listMax, pinned: (r) => busy(r) || kept.has(agentOf.get(r.session_id)) })
    const tops = new Set(own.map((r) => agentOf.get(r.session_id)))
    const under = rt.recent(rows.filter((r) => delegated(r) && tops.has(top(r))), { days: 3650, max: Math.max(0, listMax - own.length), pinned: busy })
    const listed = new Map([...own, ...under].map((r) => [r.session_id, r]))
    const rowOfSession = new Map(rows.map((r) => [r.session_id, r]))
    for (const r of under) {
      for (let by = fam.creator.get(agentOf.get(r.session_id)); by !== undefined; by = fam.creator.get(by)) {
        const sid = sessionOf(by)
        if (listed.has(sid)) break
        if (rowOfSession.has(sid)) listed.set(sid, rowOfSession.get(sid))
      }
    }
    // Then the archived agents that keep their own id, not running (archivedRows).
    return [...listed.values(), ...archivedRows(s)].sort((a, b) => Date.parse(b.updated_at) - Date.parse(a.updated_at))
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

  /** An agent's whole conversation as items (read again only when the agent moved). */
  async function timeline(agent) {
    const key = `${agent.updatedAt}|${agent.status}`
    const c = timelines.get(agent.id)
    if (c && c.key === key && (agent.status !== 'running' || Date.now() - c.at < 500)) {
      keepTimeline(agent.id, c)
      return c
    }
    const res = await fetchTimeline(agent.id)
    const tl = timelineItems(res.entries, { maxSeq: res.window?.maxSeq, running: isRunning(res.agent ?? agent), echoes })
    const out = { key, at: Date.now(), tl, history: history(agent.id, res.epoch), epoch: res.epoch }
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

  async function items(params) {
    const agent = agentFor(params.session_id)
    const t = await timeline(agent)
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
    const agent = agentFor(sessionId)
    const t = await timeline(agent)
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
    const t = await timeline(agent)
    const it = t.tl.items.find((x) => x.id === params.item_id)
    if (!it) throw new RpcError(`no item ${params.item_id}`, 'not_found')
    return it
  }

  /** A promise's value, or TIMEOUT once ms passed (the promise goes on; its failure is logged). */
  function within(p, ms, what) {
    let timer
    return Promise.race([
      p.finally(() => clearTimeout(timer)),
      new Promise((r) => {
        timer = setTimeout(() => {
          p.catch((err) => log(`paseo: ${what}: ${err.message}`))
          r(TIMEOUT)
        }, ms)
        timer.unref?.()
      }),
    ])
  }

  async function send(params) {
    const agent = agentFor(params.session_id)
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
    const res = await within(paseo.request('send_agent_message_request', req, { timeoutMs: 120_000 }), callWait, `send to ${agent.id}`)
    if (res === TIMEOUT) return // the agent is still starting (loading a closed one): Paseo has the message
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
   * Where approval is Paseo's Auto Accept (Grok), Allow all and Ask are that switch on and off (set_agent_feature).
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
      const modeId = paseoMode(root, mode)
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
    if (!params.cwd || !params.prompt || !isDir(params.cwd)) throw new RpcError('A check needs its empty directory and a prompt', 'bad_params')
    const started = Date.now()
    const before = await projectIds()
    let workspace
    let agentId
    try {
      const ws = await paseo.request('workspace.create.request', {
        source: { kind: 'directory', path: params.cwd }, background: true, title: 'Persona launcher check', idempotencyKey: randomUUID(),
      }, { timeoutMs: 30_000 })
      if (!ws?.workspace?.id) throw new RpcError(`Paseo did not make the check's workspace: ${ws?.error ?? 'no reason given'}`, 'paseo')
      workspace = ws.workspace
      agentId = randomUUID()
      const config = { provider, cwd: params.cwd }
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
  function workspaceFor(named, cwd, { strict = false } = {}) {
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
      const c = project.checkout
      if (!samePath(c.worktreeRoot ?? c.cwd, cwd) && !samePath(agent.cwd, cwd)) continue
      consider(ws, [strayRecord(project) ? 0 : 1, Date.parse(agent.updatedAt) || 0])
    }
    for (const d of workspaces.values()) {
      // A worktree by its placement; one Paseo just made has none yet, only its kind.
      if (!linkedWorktree(d.project) && !(d.workspaceKind === 'worktree' && !d.project)) continue
      const c = d.project?.checkout
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

  /** Paseo's worktrees root: its config's worktrees.root, else <Paseo home>/worktrees (its config.js resolveWorktreesRoot). */
  function worktreesBase() {
    const home = options.home ?? paseo.home
    if (typeof home !== 'string' || !home) throw new RpcError('Paseo home unknown', 'unavailable')
    let root
    try {
      root = JSON.parse(readFileSync(join(home, 'config.json'), 'utf8'))?.worktrees?.root
    } catch {}
    if (typeof root === 'string' && root.trim()) {
      let r = root.trim()
      if (r === '~' || r.startsWith('~/')) r = join(homedir(), r.slice(1))
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
    if (!paseo.connected) throw new RpcError('Paseo is not connected (not running, or restarting). Nothing was created.', 'unavailable')
    const named = typeof params.workspace_id === 'string' && params.workspace_id ? params.workspace_id : undefined
    const from = named ? workspaces.get(named) : undefined
    if (named && !from) throw new RpcError(`Paseo no longer has this workspace (${named}): refresh the list and choose again. Nothing was created.`, 'workspace_missing')
    const cwd = from?.workspaceDirectory || (typeof params.cwd === 'string' ? params.cwd : '')
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
    const req = { source }
    if (typeof params.key === 'string' && params.key) req.idempotencyKey = params.key.slice(0, 512)
    const res = await paseo.request('workspace.create.request', req, { timeoutMs: CREATE_WAIT })
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
    workspaces.delete(id)
    scheduleWorkspaces(0)
    return { archived_at: res.archivedAt }
  }

  /**
   * A new session (the form, or the machine's engine starting one): a new agent of the provider the launcher names
   * (a launcher of the login shell's that Paseo does not have: its default provider). A replacement uses its own
   * model and effort defaults; the mode and first message still apply. Its id is chosen here (paseo-<agent id>)
   * and kept on the agent as a label.
   */
  async function start(params) {
    if (params.resume) return takeOver(params)
    if (!paseo.connected) throw new RpcError('Paseo 现在没有连上（没在运行，或正在重启），这次没有新建。', 'unavailable')
    const cwd = typeof params.cwd === 'string' ? params.cwd : ''
    if (!cwd || !isDir(cwd)) throw new RpcError(`没有这个目录：${cwd || '（空）'}`, 'bad_request')
    // Read-only (plan): only a provider holding plan mode starts it, and nothing falls back to a writable one (#3238).
    const readonly = params.permission_mode === 'plan'
    let provider = launchers.some((l) => l.name === params.launcher) ? params.launcher : undefined
    if (readonly && provider && !holdsPlan(provider)) {
      throw new RpcError(`Paseo 的 ${provider} 没有只读（plan）模式，这次没有新建。选一个 Claude Code 的启动器再试。`, 'readonly_unsupported')
    }
    if (!provider) {
      provider = defaultProvider(readonly ? entries.filter((e) => holdsPlan(e?.provider)) : entries, roots)
      if (!provider && readonly) {
        throw new RpcError('Paseo 里没有能以只读（plan）模式启动的 provider（要 Claude Code 的），这次没有新建。', 'readonly_unsupported')
      }
      if (!provider) throw new RpcError('Paseo 里没有能用的 provider：在 Paseo 的设置里打开一个再试', 'unavailable')
      if (params.launcher) log(`paseo: Paseo has no provider ${params.launcher}; starting with ${provider}`)
    }
    const root = roots[provider] ?? provider
    const config = { provider, cwd }
    // Model ids and effort belong to the requested launcher, including when a tier resolved them upstream. Only
    // Paseo knows the actual replacement, so leave both unset here and let that provider apply its own defaults.
    const replacedLauncher = params.launcher && provider !== params.launcher
    if (!replacedLauncher && typeof params.model === 'string' && params.model) config.model = params.model
    const modeId = paseoMode(root, params.permission_mode)
    if (modeId) config.modeId = modeId
    // Allow all where approval is Paseo's Auto Accept (Grok and its other ACP providers, OpenCode): started with it on.
    else if (params.permission_mode === 'bypassPermissions' && AUTO_ACCEPT_ROOTS.has(root)) config.featureValues = { [AUTO_ACCEPT]: true }
    if (!replacedLauncher && EFFORTS.includes(params.effort)) {
      const opts = thinkingOptions({ provider, model: config.model })
      if (!opts.length || opts.includes(params.effort)) config.thinkingOptionId = params.effort
    }
    const prompt = typeof params.prompt === 'string' ? params.prompt : ''
    const workspaceId = workspaceFor(params.workspace_id, cwd, { strict: true })
    // The Persona session is fixed for this start: the row handed to the node while Paseo is still creating, the label,
    // the tools' PERSONA_SESSION_ID and the listed session are all it, whichever Paseo agent ends up holding it.
    let agentId = randomUUID()
    const sessionId = `${RUNTIME}-${agentId}`
    // One creation request, under a Paseo agent id of its own (the retry's is new; the Persona session stays). Claude
    // Code is given the Agent's own Persona tools as preapproved (Paseo's toolPolicy), so it never asks for them.
    const attempt = (withTools, withPolicy) => {
      const mcpServers = withTools ? personaMcpServers(options.mcpConfig, sessionId, log) : undefined
      const req = { agentId, config: mcpServers ? { ...config, mcpServers } : config, labels: { [SESSION_LABEL]: sessionId } }
      const policy = mcpServers && withPolicy && root === 'claude' ? preapproved(mcpServers) : undefined
      if (policy) req.config.toolPolicy = policy
      if (prompt.trim()) req.initialPrompt = prompt
      if (workspaceId) req.workspaceId = workspaceId
      return paseo.request('agent.create.request', req, { timeoutMs: 0 }).then((res) => ({ res, withTools: Boolean(mcpServers), policy: Boolean(policy) }))
    }
    // A provider that takes no MCP servers fails the creation outright (Paseo closes that session before it has an
    // agent): it gets the agent as it did before the Persona tools, in one more request, and for that error only. One
    // that cannot preapprove them gets them without, asking as before (allowOwn answers those asks).
    const created = attempt(true, true).then(({ res, withTools, policy }) => {
      if (!withTools || res?.agent) return res
      if (policy && POLICY_UNSUPPORTED.test(res?.error ?? '')) {
        log(`paseo: provider ${provider} cannot preapprove the Agent's own tools; creating the agent again without that`)
        agentId = randomUUID()
        return attempt(true, false).then((again) => again.res)
      }
      if (!MCP_UNSUPPORTED.test(res?.error ?? '')) return res
      log(`paseo: provider ${provider} takes no MCP servers; creating the agent again without the Persona tools`)
      agentId = randomUUID()
      return attempt(false, false).then((again) => again.res)
    })
    created.then((res) => res?.agent && upsert(res.agent), () => {})
    const res = await within(created, callWait, `start ${agentId}`)
    if (res !== TIMEOUT && !res?.agent) throw new RpcError(`Paseo 没能新建：${res?.error ?? '原因不明'}`, 'start_failed')
    if (readonly && res !== TIMEOUT && res.agent.currentModeId && res.agent.currentModeId !== modeId) {
      // Paseo started it in another mode after all: it does not run on as a writable agent.
      await paseo.request('archive_agent_request', { agentId }).catch(() => {})
      remove(agentId)
      throw new RpcError(`Paseo 没有以只读（plan）模式启动（实际：${res.agent.currentModeId}），已经归档。`, 'readonly_unsupported')
    }
    if (res !== TIMEOUT) {
      upsert(res.agent)
      const row = rowOf(res.agent)
      if (row) return { session: row }
    }
    // Still being created (its provider starting): the row it will have.
    return {
      session: {
        session_id: sessionId, cwd, title: prompt.trim() ? snippet(prompt) : basename(cwd),
        status: prompt.trim() ? 'working' : 'idle', updated_at: new Date().toISOString(), rev: Date.now(),
        ...placeOf(workspaceId, cwd), agent: appAgent(root), launcher: provider,
        ...(config.model ? { model: config.model } : {}),
      },
    }
  }

  /** Whether provider starts in plan mode: one running Claude Code whose Paseo modes (when listed) include plan. */
  function holdsPlan(provider) {
    if (!provider || (roots[provider] ?? provider) !== 'claude') return false
    const e = entries.find((x) => x?.provider === provider)
    return !Array.isArray(e?.modes) || !e.modes.length || e.modes.some((m) => m?.id === 'plan')
  }

  /**
   * Take a Claude session over (the machine's engine continuing a session not running anywhere): the Paseo agent
   * that has it already, else Paseo imports it — its conversation goes on in Paseo under the same id. A session an
   * archived agent keeps under its own id (archivedRows) is that agent, back from the archive with the id on its
   * label: Paseo imports a session it has an archived record of into that record. Refused when a claude Paseo does
   * not run has it open (two writers of one transcript).
   */
  async function takeOver(params) {
    const id = params.resume
    if (!paseo.connected) throw new RpcError('Paseo 现在没有连上（没在运行，或正在重启），这次没有发送。', 'unavailable')
    let mine
    for (const { agent } of agents.values()) {
      if (claudeId(agent) === id && agentRoot(agent, roots) === 'claude' && (!mine || mine.status === 'closed')) mine = agent
    }
    mine ??= agents.get(bySession.get(id))?.agent // a session listed under an id of its own (Persona's label)
    const kept = mine ? undefined : archivedOf(id)
    if (params.permission_mode === 'plan' && (mine ?? kept)?.currentModeId !== 'plan') {
      // A takeover keeps the session's own mode (an import sets none): it cannot promise read-only (caps.readonly).
      throw new RpcError('Paseo 接着已有的会话时不能保证只读（plan）模式，这次没有发送。', 'readonly_unsupported')
    }
    if (mine) {
      const row = rowOf(mine)
      if (row) return { session: row }
    }
    if (params.open_elsewhere) {
      throw new RpcError('这个会话正在别处的 Claude Code 里开着（Paseo 之外），Paseo 再接着它就会有两个进程写同一个会话。先在那边关掉它。这次没有发送。', 'open_elsewhere')
    }
    const cwd = kept?.cwd || (typeof params.cwd === 'string' ? params.cwd : '')
    if (!cwd || !isDir(cwd)) throw new RpcError(`接不上这个会话：它的目录不在了（${cwd || '未知'}）`, 'bad_request')
    // The provider: the archived agent's own (Paseo finds its record by provider and session), else the launcher's
    // when it runs Claude Code, else the one this machine's Claude agents use most lately.
    const own = kept?.persistence?.provider ?? kept?.provider
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
    const imp = { provider, sessionId: kept ? (transcriptOf(kept, roots) ?? codexThreadOf(kept, roots)) : id, cwd, labels: { [SESSION_LABEL]: id } }
    const workspaceId = workspaceFor(kept?.workspaceId, cwd)
    if (workspaceId) imp.workspaceId = workspaceId
    const res = await paseo.request('import_agent_request', imp,
      { expect: 'status:agent_resumed', timeoutMs: callWait })
    if (!res?.agent) throw new RpcError('Paseo 没有接上这个会话', 'start_failed')
    upsert(res.agent)
    const row = rowOf(res.agent)
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
    // An archived agent that keeps its id is followed too: a message takes it back from the archive (archivedRows).
    const agentId = bySession.get(sessionId) ?? archivedOf(sessionId)?.id
    if (!agentId) return
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
    if (!w || !paseo.connected) return
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
      clearTimeout(awayTimer)
      clearInterval(sweepTimer)
      for (const t of closedTimers) clearTimeout(t)
      closedTimers.clear()
      for (const t of lastTextTimers.values()) clearTimeout(t)
      lastTextTimers.clear()
      clearTimeout(workspacesTimer)
      workspacesTimer = undefined
      for (const a of asks.values()) a.abort()
      rt.stop()
      runtimeOn = false
      paseo.stop()
    },
    runtime: rt,
    paseo,
    /** Tests: the bridge's view. */
    state: { agents, bySession, archived, watches, asks, handedBack, echoes, timelines, workspaces, get launchers() { return launchers } },
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
