// The opencode runtime (docs/integrations/opencode.md): the sessions of one opencode server process in Persona through the runtime
// SDK (docs/integrations/runtimes.md). opencode loads a plugin once per location (a project directory) in the one server process
// that every TUI attaches to, so there are as many calls of `register` as directories in use, but one runtime
// connection per process: index.js hands each location's plugin context to the host made here, once per process.
// The server-wide event stream is read through one of them (the primary; another takes over when it goes away).
//
// Where things come from:
//  - the list and the conversation: opencode.db, read-only (db.js). The plugin API has no list of sessions and gives a
//    session's context only (what a compaction left), while the database holds all of it with a stable seq per message.
//  - what happens now (a turn starting and ending, text arriving, a permission asked): the plugin's event stream.
//  - control (create, send, interrupt, rename, model/mode switch, permission replies): the plugin's own session / permission API.
// The state a herdr pane shows (working, idle) is herdr's; this runtime adds the title, the last words, the model, the
// context use, the conversation, and "needs you" with a card while a permission waits. Everything opencode-specific
// comes in through `deps`, so the tests drive it with fakes.
//
// Safety boundary (#11220): opencode does not offer per-session isolated rule directories out-of-band, so this runtime
// deliberately omits `caps.supervision_policy` and fails closed (`assistant_supervision_runtime`) if any caller passes
// non-empty `supervision_instructions`.

import { createHash } from 'node:crypto'
import { homedir } from 'node:os'
import { basename, resolve } from 'node:path'
import { createRuntime, interactionId, light, page, RpcError, snippet } from '../../runtime-sdk/index.js'
import { isAssistantDir } from '../../runtime-sdk/lib/identity.js'
import { contextTokens, fromRow, imageOf, lastText, liveText, loadBlobImage, messageItems, permissionCard, promptWithContext, visibleUserText } from './map.js'
import { createMcpProxy, extractShellGates, matchShellGate } from './mcp.js'
import { isMemoryWritePath } from './memory-write.js'
import { buildLauncher, normalizeModels, PERSONA_EFFORTS, resolveModel } from './models.js'

export const RUNTIME_VERSION = '0.3.0'
const ROOTS = 200 // sessions looked at for the list (the newest roots)
const TAIL = 500 // messages read for the newest page of a conversation
const AFTER = 600 // messages read for one catch-up page
const LIST_CACHE = 3000 // ms: the list of sessions is read from the database at most this often
const POLL_EVERY = 2000 // ms: while a turn runs, look for messages the events did not announce
const STALE_MS = 60_000 // an unfinished message of a session that is running is final after this (events missed)
const SETTLE_MS = 1500 // ...and of one that is not running, after this (an interrupted step never says it completed)
const MODELS_EVERY = 10 * 60_000 // ms: the models' context windows and catalog are asked again
const VALID_MODES = new Set(['default', 'acceptEdits', 'auto', 'plan', 'bypassPermissions'])
const VALID_EFFORTS = new Set(PERSONA_EFFORTS)
const SUPERVISION_ERROR_CODE = 'assistant_supervision_runtime'

/**
 * @typedef {object} OpencodeDeps
 * @property {() => Promise<{ all(sql: string, ...params: any[]): any[] }>} openDb - opencode.db, read-only.
 * @property {() => Promise<any[]>} [discoverModels] - optional fallback when ctx.model.list is unavailable.
 * @property {(ctx: any, opts: { directory: string, name: string, config: any, sessionId: string }) => Promise<void>} [registerMcp]
 * @property {(msg: string) => void} [log]
 * @property {(path: string) => import('node:net').Socket} [connect] - tests.
 * @property {number} [pid]
 * @property {() => number} [now]
 */

/**
 * @param {OpencodeDeps} deps
 * @param {{ socketPath?: string, answerFirst?: 'auto'|'phone'|'local', listDays?: number, maxSessions?: number,
 *   retryMs?: number, pushDelayMs?: number, runtime?: string, name?: string, instance?: string,
 *   launcher?: string, displayName?: string, model?: string, assistant?: boolean, home?: string }} [options]
 */
export function createOpencodeHost(deps, options = {}) {
  const log = deps.log ?? (() => {})
  const now = deps.now ?? Date.now
  const home = options.home ?? homedir()
  const listDays = options.listDays ?? 7
  const maxSessions = options.maxSessions ?? 80
  const launcherName = options.launcher === undefined ? 'opencode' : String(options.launcher).trim()
  const displayName = typeof options.displayName === 'string' && options.displayName.trim() ? options.displayName.trim() : 'opencode'
  const assistantEnabled = options.assistant !== false
  const explicitDefaultModel = typeof options.model === 'string' ? options.model.trim() : ''
  let displayDefaultModel = explicitDefaultModel

  const sessions = new Map() // session id -> { running, runningSince, blocked: Map, lastSeq, texts, textMessage, chain, poll, title, model, effort, mode, startedByPersona }
  const createdSessions = new Map() // session id -> { id, directory, title, model, effort, time_updated, startedByPersona, assistant, mode }
  const assistants = new Map() // session id -> { cwd, mode, memoryDir }
  const closedAssistantSessions = new Set() // native rows may outlive a closed MCP binding
  const mcpWiredDirs = new Map() // directory -> route
  const locations = new Set() // { ctx, directory, abort? } of every registered location, in registration order
  const roots = new Map() // session id -> { root, directory } (a child session answers for its root)
  const windows = new Map() // "provider\0model", "provider/model", and "model" -> context window
  const seen = new Set() // ids of events handled (every location's subscription delivers the same ones)
  const derived = new Map() // session id -> { rev, last, model, tokens, hasAssistant } (per max seq)
  let catalog = []
  let gateShellRules = []
  let gateShellRulesReady = false
  let primary // the location whose context reads the event stream
  let started = false
  let stopped = false
  let db
  let dbFailedAt = 0
  let listCache = { at: 0, rows: [] }
  let modelsAt = 0

  const isAssistant = (id) => assistantEnabled && assistants.has(id)

  const mcpProxy = createMcpProxy({
    tools: async (binding) => {
      const asst = binding?.assistant === true
      const list = await rt.tools({ assistant: asst })
      if (asst) {
        gateShellRules = extractShellGates(list)
        gateShellRulesReady = true
      }
      return list
    },
    call: async (binding, name, args) => {
      const asst = binding?.assistant === true
      const sid = binding?.sessionId || binding?.session?.id || ''
      if (!sid || (asst && !assistants.has(sid))) {
        return { content: [{ type: 'text', text: 'Persona session is not active' }], is_error: true }
      }
      if (asst && binding?.directory && !await unambiguousAssistantOwner(binding.directory, sid)) {
        return { content: [{ type: 'text', text: 'Assistant tool identity is ambiguous in this directory. Use the OpenCode companion for isolated assistant sessions.' }], is_error: true }
      }
      return rt.callTool({ name, args, session_id: sid, assistant: asst })
    },
    log,
  })

  const initialLaunchers = launcherName
    ? [buildLauncher({ name: launcherName, displayName, models: catalog, defaultModel: displayDefaultModel })]
    : undefined

  const rt = createRuntime({
    listSessions,
    items,
    cursor: async (id) => {
      const v = await view(id)
      return { to_seq: v.toSeq, history: v.history }
    },
    image,
    item,
    send,
    interrupt,
    rename,
    start,
    models,
    modes,
    hasLocalClient: (id) => !isAssistant(id), // assistant chats always ask phone first; TUI sessions default to local client
  }, {
    runtime: options.runtime ?? `opencode-${deps.pid ?? process.pid}`,
    name: options.name ?? 'opencode',
    agent: 'opencode',
    version: RUNTIME_VERSION,
    ...(launcherName ? { launcher: launcherName, launcherModel: displayDefaultModel || undefined, launchers: initialLaunchers } : {}),
    assistant: assistantEnabled,
    readonly: true,
    modelSwitch: true,
    // Security boundary (#11220): intentionally DO NOT set supervisionPolicy: true.
    socketPath: options.socketPath,
    answerFirst: options.answerFirst ?? 'auto',
    log,
    connect: deps.connect,
    instance: options.instance,
    retryMs: options.retryMs,
    pushDelayMs: options.pushDelayMs,
    onHub: (connected) => {
      if (!connected) {
        gateShellRulesReady = false
        return
      }
      if (assistantEnabled) void refreshGateRules()
    },
  })

  function rejectSupervisionPolicy(params) {
    if (typeof params?.supervision_instructions === 'string' && params.supervision_instructions.trim() !== '') {
      throw new RpcError('opencode does not support isolated supervision policy channels', SUPERVISION_ERROR_CODE)
    }
  }

  async function refreshGateRules({ throwOnError = false } = {}) {
    try {
      const list = await rt.tools({ assistant: true })
      gateShellRules = extractShellGates(list)
      gateShellRulesReady = true
    } catch (err) {
      gateShellRulesReady = false
      log(`persona: opencode assistant tools: ${err.message}`)
      if (throwOnError) {
        throw new RpcError(`获取助手工具策略失败：${err.message}`, 'opencode')
      }
    }
  }

  async function ensureGateRules() {
    if (!gateShellRulesReady) {
      await refreshGateRules({ throwOnError: rt.hubConnected })
    }
  }

  // ---- the database ----

  /** The database, opened on first use; null for a minute after a failure to open it. */
  async function database() {
    if (db) return db
    if (dbFailedAt && now() - dbFailedAt < 60_000) return null
    try {
      db = await deps.openDb()
      dbFailedAt = 0
    } catch (err) {
      dbFailedAt = now()
      log(`persona: opencode database: ${err.message}`)
    }
    return db ?? null
  }

  /**
   * "time_busy," when session_v2 has it (opencode stamps it when a turn starts; older versions have no such column),
   * else "": selecting a column that is not there would fail the query, and rows() gives [] for that, an empty list.
   * Asked once per database opened.
   */
  let busyFor
  let busyCol = ''
  async function busyColumn() {
    const d = await database()
    if (!d || busyFor === d) return busyCol
    const cols = await rows('pragma table_info(session_v2)')
    if (!cols.length) return '' // not read: ask again next time
    busyFor = d
    busyCol = cols.some((c) => c.name === 'time_busy') ? 'time_busy,' : ''
    return busyCol
  }

  /** Rows of a query; [] when the database is not there or the query failed (logged). */
  async function rows(sql, ...params) {
    const d = await database()
    if (!d) return []
    try {
      return d.all(sql, ...params)
    } catch (err) {
      log(`persona: opencode query: ${err.message}`)
      if (/not an? (database|open)|closed/i.test(String(err.message))) db = undefined
      return []
    }
  }

  /** The root of a session: a subagent's session answers for the session that started it. */
  async function rootOf(id) {
    const c = roots.get(id)
    if (c) return c
    let cur = id
    for (let i = 0; i < 6; i++) {
      const [r] = await rows('select parent_id from session_v2 where id = ?', cur)
      if (!r) return { root: id } // not in the database (yet): its own root, and not remembered
      if (!r.parent_id) {
        const out = { root: cur }
        roots.set(id, out)
        return out
      }
      cur = r.parent_id
    }
    return { root: id }
  }

  // ---- the sessions ----

  const st = (id) => {
    let s = sessions.get(id)
    if (!s) {
      s = {
        running: false,
        runningSince: 0,
        blocked: new Map(),
        lastSeq: undefined,
        texts: new Map(),
        textMessage: '',
        chain: Promise.resolve(),
        poll: undefined,
        title: undefined,
        model: undefined,
        effort: undefined,
        mode: undefined,
        startedByPersona: false,
      }
      sessions.set(id, s)
    }
    return s
  }

  const isPinned = (id) => {
    const s = sessions.get(id)
    return !!s && (s.running || s.blocked.size > 0)
  }

  async function derive(id, rev) {
    const c = derived.get(id)
    if (c && c.rev === rev) return c
    const recent = await rows("select seq, type, data from session_message where session_id = ? and type in ('assistant', 'user') order by seq desc limit 8", id)
    const msgs = recent.map((r) => ({ ...fromRow(r), seq: r.seq })).reverse()
    const lastAssistant = [...msgs].reverse().find((m) => m.type === 'assistant')
    const out = {
      rev,
      last: lastText(msgs),
      model: lastAssistant?.model?.id ?? '',
      provider: lastAssistant?.model?.providerID ?? '',
      tokens: contextTokens(lastAssistant),
      hasAssistant: !!lastAssistant,
    }
    derived.set(id, out)
    if (derived.size > 400) derived.delete(derived.keys().next().value)
    return out
  }

  /** The newest root sessions in the database, kept for LIST_CACHE ms. */
  async function recentRoots() {
    if (now() - listCache.at < LIST_CACHE) return listCache.rows
    const list = await rows(
      `select id, title, directory, time_updated, time_idle, ${await busyColumn()} model from session_v2 where parent_id is null and time_archived is null order by time_updated desc limit ?`, ROOTS)
    listCache = { at: now(), rows: list }
    return list
  }

  async function sessionRow(r) {
    const [m] = await rows('select max(seq) as seq from session_message where session_id = ?', r.id)
    const rev = m?.seq ?? 0
    const d = await derive(r.id, rev)
    const s = sessions.get(r.id)
    const created = createdSessions.get(r.id)
    let title = s?.title || r.title || created?.title || ''
    if (!title) {
      const [u] = await rows("select data from session_message where session_id = ? and type = 'user' order by seq limit 1", r.id)
      if (u) title = snippet(visibleUserText(fromRow({ data: u.data }).text ?? ''))
    }
    const rawDir = r.directory ?? created?.directory ?? ''
    if (assistantEnabled && rawDir && isAssistantDir(rawDir, home) && !assistants.has(r.id)) {
      assistants.set(r.id, { cwd: rawDir, mode: 'default', memoryDir: '', activeAt: r.time_updated ?? now() })
    }
    // Preserve the original assistant directory for the chat's whole life (#2368).
    const directory = assistants.get(r.id)?.cwd || rawDir
    if (s?.running && typeof r.time_idle === 'number' && typeof r.time_busy === 'number' &&
      r.time_idle > r.time_busy && r.time_idle > s.runningSince) {
      log(`persona: opencode session ${r.id} went idle without an event; marking its turn over`)
      ended(r.id)
    }
    const status = s?.blocked.size ? 'blocked' : s?.running ? 'working' : d.hasAssistant ? 'done' : 'idle'
    const row = {
      session_id: r.id,
      title: title || basename(directory),
      cwd: directory,
      status,
      updated_at: new Date(r.time_updated ?? created?.time_updated ?? now()).toISOString(),
      rev,
      space_id: directory,
      space_name: basename(directory),
      space_kind: 'opencode',
    }
    if (d.last) row.last_text = d.last
    const model = s?.model || d.model || modelOf(r.model) || created?.model || ''
    if (model) row.model = model
    const effort = s?.effort || created?.effort || ''
    if (effort) row.effort = effort
    const modelEntry = model ? catalog.find((m) => m.id === model || (d.provider && m.id === `${d.provider}/${model}`)) : undefined
    if (Array.isArray(modelEntry?.efforts) && modelEntry.efforts.length > 0) row.efforts = modelEntry.efforts
    if (d.tokens > 0) {
      row.context_tokens = d.tokens
      const w = windows.get(`${d.provider}\0${d.model}`) ?? windows.get(`${d.provider}/${d.model}`) ?? windows.get(d.model)
      if (w) row.context_window = w
    }
    if (s?.startedByPersona || created?.startedByPersona) {
      row.started_by_persona = true
      if (launcherName) row.launcher = launcherName
    }
    const mode = s?.mode || assistants.get(r.id)?.mode || created?.mode
    if (mode && mode !== 'default') row.permission_mode = mode
    return row
  }

  function modelOf(json) {
    try {
      if (!json) return ''
      const parsed = JSON.parse(json)
      if (parsed?.providerID && parsed?.id) return `${parsed.providerID}/${parsed.id}`
      return parsed?.id ?? ''
    } catch {
      return ''
    }
  }

  async function currentModelOf(id) {
    const s = sessions.get(id)
    if (s?.model) return s.model
    const created = createdSessions.get(id)
    if (created?.model) return created.model
    const [m] = await rows('select max(seq) as seq from session_message where session_id = ?', id)
    if (m?.seq > 0) {
      const d = await derive(id, m.seq)
      if (d?.model) return d.provider ? `${d.provider}/${d.model}` : d.model
    }
    const [r] = await rows('select model from session_v2 where id = ?', id)
    return modelOf(r?.model)
  }

  async function listSessions() {
    const recent = await recentRoots()
    const ids = new Set(recent.map((r) => r.id))
    const extra = []
    for (const [id] of sessions) {
      if (!ids.has(id) && isPinned(id)) {
        const [r] = await rows(`select id, title, directory, time_updated, time_idle, ${await busyColumn()} model from session_v2 where id = ? and parent_id is null`, id)
        if (r) {
          ids.add(id)
          extra.push(r)
        }
      }
    }
    // Include newly started sessions not yet flushed into opencode.db.
    for (const [id, c] of createdSessions) {
      if (ids.has(id)) continue
      ids.add(id)
      extra.push({
        id: c.id,
        title: c.title || null,
        directory: c.directory,
        time_updated: c.time_updated,
        time_idle: null,
        model: c.model ? JSON.stringify({ id: c.model }) : null,
      })
    }
    const picked = rt.recent([...recent, ...extra], { days: listDays, max: maxSessions, pinned: (r) => isPinned(r.id), id: (r) => r.id, updatedAt: (r) => r.time_updated })
    const out = []
    for (const r of picked) out.push(await sessionRow(r))
    return out
  }

  // ---- the conversation ----

  /** A message is final once opencode says it completed; one still running is not pushed (it is rewritten in place). */
  function settled(r, s) {
    if (r.type !== 'assistant' && r.type !== 'shell' && r.type !== 'compaction') return true
    let t
    try {
      t = JSON.parse(r.data)
    } catch {
      return true
    }
    if (r.type === 'compaction') return t.status !== 'pending'
    if (typeof t.time?.completed === 'number' || t.finish) return true
    if (r.type === 'shell' && t.status === 'running') return false
    if (r.type === 'shell' && t.status) return true
    return now() - (r.time_updated ?? 0) > (s?.running ? STALE_MS : SETTLE_MS)
  }

  /** The rows up to the first message still being written: the conversation as far as it is final. */
  function settledPrefix(list, id) {
    const s = sessions.get(id)
    const out = []
    for (const r of list) {
      if (!settled(r, s)) break
      out.push(r)
    }
    return out
  }

  async function view(id) {
    const [r] = await rows('select revert from session_v2 where id = ?', id)
    // Not opencode's (the database is open and has no such session, and we did not just create it):
    // the node asks every runtime about a session nobody listed and gives it to the first that answers,
    // so an empty page would take another runtime's for ours.
    if (!r && db && !createdSessions.has(id)) throw new RpcError(`opencode 里没有这个会话（${id}）`, 'not_found')
    return { toSeq: await maxSettledBefore(id), history: historyOf(id, r?.revert) }
  }

  /** The largest seq below the first unfinished message of a session (all of them when none is). */
  async function maxSettledBefore(id) {
    const open = await rows("select seq, type, data, time_updated from session_message where session_id = ? and type in ('assistant', 'shell', 'compaction') order by seq desc limit 3", id)
    const s = sessions.get(id)
    let first
    for (const r of open.reverse()) if (!settled(r, s)) { first = r.seq; break }
    const [m] = first === undefined
      ? await rows('select max(seq) as seq from session_message where session_id = ?', id)
      : await rows('select max(seq) as seq from session_message where session_id = ? and seq < ?', id, first)
    return m?.seq ?? 0
  }

  /** The conversation's identity: another one when opencode reverted messages (they are hidden, not rewritten). */
  const historyOf = (id, revert) => `opencode:${id}${revert ? `:r${createHash('sha256').update(String(revert)).digest('hex').slice(0, 8)}` : ''}`

  async function items(params) {
    const id = params.session_id
    const v = await view(id)
    const upto = v.toSeq
    const q = (extra, ...p) => rows(`select id, seq, type, data, time_updated from session_message where session_id = ? and seq <= ? ${extra}`, id, upto, ...p)
    let list
    let windowFull = false
    let afterFull = false
    if (typeof params.through_seq === 'number' && params.through_seq > 0) {
      list = await q('and seq >= ? and seq <= ? order by seq', params.from_seq ?? 1, params.through_seq)
    } else if (params.after_seq !== undefined && params.after_seq !== null) {
      list = await q('and seq > ? order by seq limit ?', params.after_seq, AFTER)
      afterFull = list.length === AFTER
    } else {
      const before = params.before_seq > 0 ? params.before_seq : Number.MAX_SAFE_INTEGER
      list = (await q('and seq < ? order by seq desc limit ?', before, TAIL)).reverse()
      windowFull = list.length === TAIL
    }
    const all = list.flatMap((r) => messageItems(fromRow(r), r.seq))
    const pg = page(all, params, { history: v.history, toSeq: v.toSeq })
    if (windowFull && pg.has_more === false) pg.has_more = true
    if (afterFull && !pg.more_after) {
      pg.to_seq = list.at(-1).seq
      pg.more_after = true
    }
    return pg
  }

  function splitItemID(itemID) {
    const [seqText, idxText] = String(itemID).split('#')
    const seq = Number(seqText)
    const idx = Number(idxText)
    if (!Number.isSafeInteger(seq) || !Number.isSafeInteger(idx) || seq < 1) throw new RpcError(`bad item id ${itemID}`, 'bad_request')
    return { seq, idx }
  }

  async function messageAt(id, seq) {
    const [r] = await rows('select id, seq, type, data from session_message where session_id = ? and seq = ?', id, seq)
    return r ? fromRow(r) : undefined
  }

  async function item(params) {
    const { seq } = splitItemID(params.item_id)
    const msg = await messageAt(params.session_id, seq)
    const it = msg && messageItems(msg, seq).find((x) => x.id === params.item_id)
    if (!it) throw new RpcError(`no item ${params.item_id}`, 'not_found')
    return it
  }

  async function image(params) {
    const { seq, idx } = splitItemID(params.item_id)
    const msg = await messageAt(params.session_id, seq)
    const inline = imageOf(msg, idx)
    if (inline) return inline
    const blobImg = await loadBlobImage(rows, { sessionId: params.session_id, msg, seq, idx })
    if (blobImg) return blobImg
    throw new RpcError(`no image ${params.item_id}`, 'not_found')
  }

  /** Push what became final since the last push (the items only when the phone watches the session). */
  function commit(id) {
    const s = st(id)
    s.chain = s.chain.then(async () => {
      if (stopped) return
      if (s.lastSeq === undefined) {
        s.lastSeq = await maxSettledBefore(id) // first sight: the conversation so far is what the phone fetches
        return
      }
      const list = settledPrefix(await rows('select id, seq, type, data, time_updated from session_message where session_id = ? and seq > ? order by seq limit ?', id, s.lastSeq, AFTER), id)
      if (list.length === 0) return
      const toSeq = list.at(-1).seq
      const [r] = await rows('select revert from session_v2 where id = ?', id)
      if (sessions.get(id) !== s) return // deleted meanwhile (forget): nothing more of it goes out
      const fresh = rt.watching(id) ? list.flatMap((m) => messageItems(fromRow(m), m.seq)).map(light) : [] // cut as pages are
      rt.append(id, { items: fresh, fromSeq: s.lastSeq, toSeq, history: historyOf(id, r?.revert) })
      s.lastSeq = toSeq
      s.updatedAt = now()
      listCache.at = 0
    }).catch((err) => log(`persona: opencode commit ${id}: ${err.message}`))
    return s.chain
  }

  let commitTimers = new Map()
  /** Commit soon (a burst of events is one commit). */
  function commitSoon(id, ms = 120, key = id) {
    if (commitTimers.has(key)) return
    const t = setTimeout(() => {
      commitTimers.delete(key)
      void commit(id)
    }, ms)
    t.unref?.()
    commitTimers.set(key, t)
  }

  // ---- control ----

  function ctxFor(directory) {
    for (const l of locations) if (l.directory === directory) return l.ctx
    return primary?.ctx ?? [...locations][0]?.ctx
  }

  async function directoryOf(id) {
    const a = assistants.get(id)
    if (a?.cwd) return a.cwd
    const c = createdSessions.get(id)
    if (c?.directory) return c.directory
    const [r] = await rows('select directory from session_v2 where id = ?', id)
    return r?.directory ?? ''
  }

  async function control(id, fn) {
    const dir = await directoryOf(id)
    const ctx = ctxFor(dir)
    if (!ctx) throw new RpcError('opencode 没有在运行', 'not_running')
    try {
      return await fn(ctx, dir)
    } catch (err) {
      if (err instanceof RpcError) throw err
      throw new RpcError(`opencode：${err?.message ?? err}`, /not ?found/i.test(String(err?.message)) ? 'not_found' : 'opencode')
    }
  }

  async function unambiguousAssistantOwner(directory, sessionId) {
    if (closedAssistantSessions.has(sessionId) || assistants.get(sessionId)?.cwd !== directory) return false
    const ids = new Set()
    for (const [id, session] of assistants) if (session.cwd === directory && !closedAssistantSessions.has(id)) ids.add(id)
    for (const [id, session] of createdSessions) if (session.directory === directory && !closedAssistantSessions.has(id)) ids.add(id)
    // Include native/TUI sessions that never went through Persona's start. If
    // the authoritative inventory is unavailable, an HTTP token cannot prove
    // which same-directory session is calling it, so no tool is executed.
    const native = await database()
    if (!native) return false
    try {
      for (const row of native.all('select id, directory from session_v2 where parent_id is null')) {
        if (row.directory === directory && !closedAssistantSessions.has(row.id)) ids.add(row.id)
      }
    } catch { return false }
    return ids.size === 1 && ids.has(sessionId)
  }

  /** Register our loopback HTTP MCP server for an assistant directory when the plugin context supports MCP registration. */
  async function ensureAssistantMcp(ctx, directory, sessionId) {
    if (!assistantEnabled || !directory) return
    if (!sessionId || closedAssistantSessions.has(sessionId)) return
    if (mcpWiredDirs.has(directory)) return
    const registerFn = typeof deps.registerMcp === 'function'
      ? (config) => deps.registerMcp(ctx, { directory, name: 'persona', config, sessionId })
      : typeof ctx?.mcp?.add === 'function'
        ? (config) => ctx.mcp.add({ name: 'persona', config, directory })
        : null
    if (!registerFn) return
    try {
      // OpenCode registers MCP per directory, not per session. Freeze the first
      // owner and check ambiguity on every execution; never retarget a token.
      const route = await mcpProxy.allocate({ assistant: true, directory, sessionId })
      await registerFn(route.remoteConfig)
      route.ownerSession = sessionId
      mcpWiredDirs.set(directory, route)
    } catch (err) {
      log(`persona: opencode mcp add (${directory}): ${err?.message ?? err}`)
    }
  }

  function content(params) {
    const files = (params.images ?? []).filter((i) => i?.data).map((i, n) => {
      const mime = i.mime || 'image/jpeg'
      return { uri: `data:${mime};base64,${i.data}`, name: `image-${n + 1}.${mime.split('/')[1] || 'jpg'}` }
    })
    const text = typeof params.text === 'string' ? params.text : ''
    if (!text.trim() && files.length === 0) throw new RpcError('消息是空的', 'bad_request')
    return { text, files }
  }

  async function applyModelAndMode(ctx, id, { model, effort, permission_mode }) {
    const s = st(id)
    const normalizedEffort = typeof effort === 'string' && effort.trim() ? effort.trim().toLowerCase() : ''
    if (normalizedEffort && !VALID_EFFORTS.has(normalizedEffort)) {
      throw new RpcError(`不支持的思考深度 "${effort}"`, 'bad_request')
    }
    if (model || normalizedEffort) {
      if (catalog.length === 0) await refreshModels(ctx, { force: true }).catch(() => {})
      const targetModelInput = model || await currentModelOf(id)
      const resolved = targetModelInput ? resolveModel(targetModelInput, catalog) : null
      if (resolved) {
        const modelRef = {
          providerID: resolved.providerID,
          id: resolved.modelID,
          ...(normalizedEffort ? { variant: normalizedEffort } : (s.effort ? { variant: s.effort } : {})),
        }
        if (typeof ctx.session?.switchModel === 'function') {
          await ctx.session.switchModel({ sessionID: id, model: modelRef })
        } else if (typeof ctx.session?.model === 'function') {
          await ctx.session.model({ sessionID: id, model: modelRef })
        }
        s.model = resolved.id
        if (normalizedEffort) s.effort = normalizedEffort
        const c = createdSessions.get(id)
        if (c) {
          c.model = resolved.id
          if (normalizedEffort) c.effort = normalizedEffort
        }
      } else if (normalizedEffort) {
        throw new RpcError('无法确定当前会话的模型，请同时指定 model 后再切换思考深度', 'bad_request')
      }
    }
    if (permission_mode) {
      if (!VALID_MODES.has(permission_mode)) {
        throw new RpcError(`不支持的权限模式 "${permission_mode}"`, 'bad_request')
      }
      const prevMode = s.mode || assistants.get(id)?.mode || 'default'
      const wantAgent = permission_mode === 'plan' ? 'plan' : (prevMode === 'plan' ? 'build' : '')
      if (wantAgent && typeof ctx.session?.switchAgent === 'function') {
        try {
          await ctx.session.switchAgent({ sessionID: id, agent: wantAgent })
        } catch (err) {
          if (permission_mode === 'plan') {
            throw new RpcError(`opencode 无法切换到只读模式（plan）：${err?.message ?? err}`, 'readonly_unsupported')
          }
          throw err
        }
      }
      s.mode = permission_mode
      const a = assistants.get(id)
      if (a) a.mode = permission_mode
      const c = createdSessions.get(id)
      if (c) c.mode = permission_mode
    }
  }

  async function start(params = {}, onSessionId) {
    rejectSupervisionPolicy(params)
    const cwd = typeof params.cwd === 'string' ? params.cwd.trim() : ''
    if (!cwd) throw new RpcError('需要选择一个目录', 'bad_request')
    const ctx = ctxFor(cwd)
    if (!ctx) throw new RpcError('opencode 没有在运行', 'not_running')
    if (typeof ctx.session?.create !== 'function') {
      throw new RpcError('当前 opencode 版本不支持通过插件新建会话', 'unsupported')
    }

    const reqMode = typeof params.permission_mode === 'string' && params.permission_mode
      ? params.permission_mode
      : (typeof params.mode === 'string' ? params.mode : '')
    if (reqMode && !VALID_MODES.has(reqMode)) {
      throw new RpcError(`不支持的权限模式 "${reqMode}"`, 'bad_request')
    }
    const reqEffort = typeof params.effort === 'string' && params.effort.trim() ? params.effort.trim().toLowerCase() : ''
    if (reqEffort && !VALID_EFFORTS.has(reqEffort)) {
      throw new RpcError(`不支持的思考深度 "${params.effort}"`, 'bad_request')
    }

    let chosenModel = null
    if (typeof params.model === 'string' && params.model.trim()) {
      if (catalog.length === 0) await refreshModels(ctx, { force: true })
      chosenModel = resolveModel(params.model, catalog)
    }

    const isAsst = assistantEnabled && (params.assistant === true || isAssistantDir(cwd, home))
    if (isAsst) await ensureGateRules()
    const modelRef = chosenModel
      ? { providerID: chosenModel.providerID, id: chosenModel.modelID, ...(reqEffort ? { variant: reqEffort } : {}) }
      : undefined
    const createPayload = {
      location: { directory: cwd },
      ...(modelRef ? { model: modelRef } : {}),
      ...(reqMode === 'plan' ? { agent: 'plan' } : {}),
    }

    let created
    try {
      created = await ctx.session.create(createPayload)
    } catch (err) {
      if (reqMode === 'plan') {
        throw new RpcError(`opencode 无法以只读模式（plan）新建会话：${err?.message ?? err}`, 'readonly_unsupported')
      }
      throw new RpcError(`opencode：${err?.message ?? err}`, 'opencode')
    }
    const info = created?.data ?? created ?? {}
    const id = typeof info.id === 'string' && info.id.trim() ? info.id.trim() : ''
    if (!id) throw new RpcError('opencode 没有返回新会话 ID', 'opencode')

    onSessionId?.(id)

    const s = st(id)
    s.startedByPersona = true
    if (chosenModel) s.model = chosenModel.id
    if (reqEffort) s.effort = reqEffort
    if (reqMode) s.mode = reqMode
    const createdAt = now()
    createdSessions.set(id, {
      id,
      directory: cwd,
      title: typeof info.title === 'string' ? info.title : '',
      model: chosenModel?.id || (info.model?.providerID && info.model?.id ? `${info.model.providerID}/${info.model.id}` : info.model?.id || ''),
      effort: reqEffort || '',
      time_updated: createdAt,
      startedByPersona: true,
      assistant: isAsst,
      mode: reqMode || (isAsst ? 'default' : ''),
    })

    if (isAsst) {
      assistants.set(id, { cwd, mode: reqMode || 'default', memoryDir: '', activeAt: createdAt })
      await ensureAssistantMcp(ctx, cwd, id)
    }

    // If create() ignored `model` and switchModel() is available, apply it explicitly.
    if (chosenModel && typeof ctx.session.switchModel === 'function') {
      const createdModel = info.model?.id
      const createdProv = info.model?.providerID
      if (createdModel !== chosenModel.modelID || (createdProv && createdProv !== chosenModel.providerID)) {
        try {
          await ctx.session.switchModel({ sessionID: id, model: modelRef })
        } catch (err) {
          throw new RpcError(`opencode：${err?.message ?? err}`, 'opencode')
        }
      }
    }

    const hasInput = Boolean(
      (typeof params.prompt === 'string' && params.prompt.trim()) ||
      (Array.isArray(params.images) && params.images.some((i) => i?.data)),
    )
    if (hasInput) {
      await send({
        session_id: id,
        text: params.prompt ?? '',
        images: params.images,
        client_msg_id: params.client_msg_id,
        assistant: isAsst,
        mode: reqMode || undefined,
      })
    }

    listCache.at = 0
    rt.sessionsChanged(0)
    const rowModel = createdSessions.get(id)?.model || ''
    const modelEntry = rowModel ? catalog.find((m) => m.id === rowModel) : undefined
    const row = {
      session_id: id,
      title: (typeof info.title === 'string' && info.title.trim()) || basename(cwd) || 'opencode',
      cwd,
      status: hasInput ? 'working' : 'idle',
      updated_at: new Date(createdAt).toISOString(),
      rev: 0,
      space_id: cwd,
      space_name: basename(cwd),
      space_kind: 'opencode',
      started_by_persona: true,
      ...(launcherName ? { launcher: launcherName } : {}),
      ...(rowModel ? { model: rowModel } : {}),
      ...(reqEffort ? { effort: reqEffort } : {}),
      ...(Array.isArray(modelEntry?.efforts) && modelEntry.efforts.length ? { efforts: modelEntry.efforts } : {}),
      ...(reqMode && reqMode !== 'default' ? { permission_mode: reqMode } : {}),
    }
    return { session: row }
  }

  async function send(params) {
    rejectSupervisionPolicy(params)
    const id = params.session_id
    const { text: rawText, files } = content(params) // once per client_msg_id: the SDK's
    const isAsst = assistantEnabled && (params.assistant === true || assistants.has(id))
    let text = rawText
    if (isAsst) {
      await ensureGateRules()
      const prev = assistants.get(id) ?? { cwd: await directoryOf(id), mode: 'default', memoryDir: '' }
      const nextMode = params.mode || params.permission_mode || prev.mode || 'default'
      const nextMemoryDir = typeof params.memory_dir === 'string' && params.memory_dir ? params.memory_dir : prev.memoryDir
      assistants.set(id, { cwd: prev.cwd, mode: nextMode, memoryDir: nextMemoryDir, activeAt: now() })
      if (typeof params.context === 'string' && params.context.trim()) {
        text = promptWithContext(rawText, params.context)
      }
    }
    await control(id, async (ctx, dir) => {
      if (isAsst) await ensureAssistantMcp(ctx, assistants.get(id)?.cwd || dir, id)
      const reqMode = params.mode || params.permission_mode || ''
      if (params.model || params.effort || reqMode) {
        await applyModelAndMode(ctx, id, { model: params.model, effort: params.effort, permission_mode: reqMode })
      }
      // Into the running turn while it works (opencode's steer), a new turn when idle: its default.
      await ctx.session.prompt({ sessionID: id, text, ...(files.length ? { files } : {}) })
    })
  }

  async function modes(params) {
    const id = params.session_id
    await control(id, async (ctx) => {
      await applyModelAndMode(ctx, id, {
        model: params.model,
        effort: params.effort,
        permission_mode: params.permission_mode,
      })
    })
    listCache.at = 0
    rt.sessionsChanged(0)
  }

  async function interrupt(params) {
    await control(params.session_id, (ctx) => ctx.session.interrupt({ sessionID: params.session_id }))
  }

  async function rename(params) {
    const title = typeof params.title === 'string' ? params.title.trim() : ''
    if (!title) throw new RpcError('名字是空的', 'bad_request')
    await control(params.session_id, (ctx) => ctx.session.update({ sessionID: params.session_id, title }))
    st(params.session_id).title = title
    const c = createdSessions.get(params.session_id)
    if (c) c.title = title
  }

  // ---- what happens in the server ----

  function publishCatalog(rawList) {
    const normalized = normalizeModels(rawList, explicitDefaultModel)
    for (const [k, w] of normalized.windows) windows.set(k, w)
    catalog = normalized.models
    displayDefaultModel = explicitDefaultModel || normalized.defaultModel || displayDefaultModel
    if (launcherName) {
      rt.launchersChanged([buildLauncher({ name: launcherName, displayName, models: catalog, defaultModel: displayDefaultModel })])
    }
    return catalog
  }

  async function refreshModels(ctx, { force = false } = {}) {
    if (!force && now() - modelsAt < MODELS_EVERY && catalog.length > 0) return catalog
    modelsAt = now()
    try {
      let raw
      if (typeof ctx?.model?.list === 'function') {
        raw = await ctx.model.list()
      } else if (typeof deps.discoverModels === 'function') {
        raw = await deps.discoverModels({ refresh: force })
      } else {
        return catalog
      }
      return publishCatalog(raw)
    } catch (err) {
      log(`persona: opencode models: ${err.message}`)
      modelsAt = 0
      if (force) throw new RpcError(`获取 opencode 模型列表失败：${err.message}`, 'opencode')
      return catalog
    }
  }

  async function models(params = {}) {
    const ctx = primary?.ctx ?? [...locations][0]?.ctx
    const list = await refreshModels(ctx, { force: params.refresh === true || catalog.length === 0 })
    return { agent: 'opencode', models: list }
  }

  /** A turn is over: the row is done, nothing waits for it, and what it wrote is pushed. */
  function ended(id) {
    rt.streamEnd(id)
    running(id, false)
    for (const [rid] of st(id).blocked) resolvePermission(id, rid) // nothing waits for a turn that is over
    commitSoon(id, 0)
    commitSoon(id, SETTLE_MS + 200, `${id}:late`) // a step that was cut off settles a moment later
  }

  function running(id, on) {
    const s = st(id)
    if (s.running === on) return
    s.running = on
    if (on) s.runningSince = now()
    clearInterval(s.poll)
    s.poll = undefined
    if (on) {
      s.poll = setInterval(() => rt.watching(id) && commitSoon(id, 0), POLL_EVERY)
      s.poll.unref?.()
    } else {
      s.texts.clear()
      s.textMessage = ''
    }
    listCache.at = 0
    rt.sessionsChanged(0)
  }

  const MEMORY_EXTRA_LIST_KEYS = ['patterns', 'pattern', 'files', 'paths']
  const MEMORY_META_DENY_KEYS = ['files', 'paths', 'patch', 'patchText', 'patch_text', 'diff']
  const MEMORY_META_PATH_KEYS = ['filepath', 'filePath', 'path', 'file_path']

  function collectPathField(val, out) {
    if (typeof val === 'string') {
      if (!val) return false
      out.push(val)
      return true
    }
    if (Array.isArray(val)) {
      for (let i = 0; i < val.length; i++) {
        if (!(i in val) || typeof val[i] !== 'string' || !val[i]) return false
        out.push(val[i])
      }
      return true
    }
    return false
  }

  function isMemoryWrite(root, action, data, mode) {
    const a = assistants.get(root)
    if (!a?.memoryDir || a.mode === 'plan' || mode === 'plan') return false
    if (action !== 'write' && action !== 'edit') return false
    const rawResources = data?.resources
    if (!Array.isArray(rawResources) || rawResources.length === 0) return false
    const files = []
    for (let i = 0; i < rawResources.length; i++) {
      if (!(i in rawResources) || typeof rawResources[i] !== 'string' || !rawResources[i]) return false
      files.push(rawResources[i])
    }
    for (let i = 0; i < MEMORY_EXTRA_LIST_KEYS.length; i++) {
      const key = MEMORY_EXTRA_LIST_KEYS[i]
      if (Object.prototype.hasOwnProperty.call(data, key) && data[key] !== undefined) {
        if (!collectPathField(data[key], files)) return false
      }
    }
    if (Object.prototype.hasOwnProperty.call(data, 'metadata') && data.metadata !== undefined) {
      const meta = data.metadata
      if (!meta || typeof meta !== 'object' || Array.isArray(meta)) return false
      for (let i = 0; i < MEMORY_META_DENY_KEYS.length; i++) {
        const key = MEMORY_META_DENY_KEYS[i]
        if (Object.prototype.hasOwnProperty.call(meta, key) && meta[key] !== undefined) return false
      }
      for (let i = 0; i < MEMORY_META_PATH_KEYS.length; i++) {
        const key = MEMORY_META_PATH_KEYS[i]
        if (!Object.prototype.hasOwnProperty.call(meta, key) || meta[key] === undefined) continue
        if (typeof meta[key] !== 'string' || !meta[key]) return false
        files.push(meta[key])
      }
    }
    for (let i = 0; i < files.length; i++) {
      if (!isMemoryWritePath(files[i], a.memoryDir, a.cwd)) return false
    }
    return true
  }

  /** The permission a session asked for: a card on the phone while the opencode dialog waits too; first answer wins. */
  async function onPermission(ctx, data) {
    const { root } = await rootOf(data.sessionID)
    const s = st(root)
    const ac = new AbortController()
    s.blocked.set(data.id, ac)
    listCache.at = 0
    rt.sessionsChanged(0)

    const action = String(data.action ?? '')
    const resources = Array.isArray(data.resources) ? data.resources.map(String) : []
    const asst = isAssistant(root)
    const mode = s.mode || assistants.get(root)?.mode || 'default'
    const isShell = action === 'shell' || action === 'bash'

    // Assistant safety & mode rules (docs/integrations/runtimes.md "Assistant").
    if (asst) {
      if (isShell) {
        if (!gateShellRulesReady) await refreshGateRules()
        const gate = matchShellGate(resources[0] ?? '', gateShellRules)
        if (gate) {
          try {
            await ctx.permission.reply({
              sessionID: data.sessionID,
              requestID: data.id,
              decision: 'reject',
              message: `这里用 ${gate.name} 工具：参数就是 ${gate.command} 后面那些词；shell 里的 ${gate.command} 拿不到凭据。`,
            })
          } catch (err) {
            log(`persona: opencode gate reject: ${err.message}`)
          }
          resolvePermission(root, data.id)
          return
        }
        if (!gateShellRulesReady) {
          try {
            await ctx.permission.reply({
              sessionID: data.sessionID,
              requestID: data.id,
              decision: 'reject',
              message: '助手工具门禁尚未就绪，拒绝自动执行 shell 命令。',
            })
          } catch (err) {
            log(`persona: opencode gate fail-closed reject: ${err.message}`)
          }
          resolvePermission(root, data.id)
          return
        }
      }
      if (mode === 'plan' && (action === 'write' || action === 'edit' || isShell)) {
        try {
          await ctx.permission.reply({
            sessionID: data.sessionID,
            requestID: data.id,
            decision: 'reject',
            message: '当前处于只读模式（plan），不允许写入或执行命令。',
          })
        } catch (err) {
          log(`persona: opencode plan reject: ${err.message}`)
        }
        resolvePermission(root, data.id)
        return
      }
      const acceptEditAuto = mode === 'acceptEdits' && (action === 'write' || action === 'edit')
      if (isMemoryWrite(root, action, data, mode) || acceptEditAuto || mode === 'bypassPermissions') {
        try {
          await ctx.permission.reply({ sessionID: data.sessionID, requestID: data.id, decision: 'once' })
        } catch (err) {
          log(`persona: opencode auto-allow: ${err.message}`)
        }
        resolvePermission(root, data.id)
        return
      }
    } else if (mode === 'plan' && (action === 'write' || action === 'edit' || isShell)) {
      try {
        await ctx.permission.reply({
          sessionID: data.sessionID,
          requestID: data.id,
          decision: 'reject',
          message: '当前处于只读模式（plan），不允许写入或执行命令。',
        })
      } catch (err) {
        log(`persona: opencode plan reject: ${err.message}`)
      }
      resolvePermission(root, data.id)
      return
    } else if ((mode === 'acceptEdits' && (action === 'write' || action === 'edit')) || mode === 'bypassPermissions') {
      try {
        await ctx.permission.reply({ sessionID: data.sessionID, requestID: data.id, decision: 'once' })
      } catch (err) {
        log(`persona: opencode auto-allow: ${err.message}`)
      }
      resolvePermission(root, data.id)
      return
    }

    const askPhone = asst ? rt.hubConnected : rt.phoneFirst(root)
    if (!askPhone) return // the dialog in the TUI is where it is answered
    const card = permissionCard(data)
    const save = Array.isArray(data.save) ? data.save.map(String) : []
    let res
    try {
      res = await rt.ask({
        id: interactionId('opencode', root, 'permission', data.id), kind: 'permission', session_id: root,
        tool_use_id: data.source?.id, ...card, ...(save.length ? { always_label: `总是允许 ${save.join('、')}` } : {}),
      }, { signal: ac.signal })
    } catch (err) {
      if (!ac.signal.aborted) log(`persona: opencode permission card: ${err.message}`)
      return
    }
    if (ac.signal.aborted || !s.blocked.has(data.id)) return // answered in the TUI meanwhile
    let decision
    let message
    if (res?.allow === true) decision = res.always === true && save.length ? 'always' : 'once'
    else if (res?.allow === false) {
      decision = 'reject'
      message = res.message?.trim() || undefined
    } else return // handed back to the TUI
    try {
      await ctx.permission.reply({ sessionID: data.sessionID, requestID: data.id, decision, ...(message ? { message } : {}) })
    } catch (err) {
      log(`persona: opencode permission reply: ${err.message}`)
    }
  }

  /**
   * A session deleted in opencode: nothing of it stays behind. Its polling and pending commits stop (they would make
   * it again), and its cards leave the phone (an aborted ask withdraws its card).
   */
  function forget(id) {
    const s = sessions.get(id)
    if (s) {
      clearInterval(s.poll)
      for (const ac of s.blocked.values()) ac.abort()
    }
    for (const key of [id, `${id}:late`]) {
      clearTimeout(commitTimers.get(key))
      commitTimers.delete(key)
    }
    const asst = assistants.get(id)
    sessions.delete(id)
    createdSessions.delete(id)
    assistants.delete(id)
    if (asst?.cwd) {
      closedAssistantSessions.add(id)
      const route = mcpWiredDirs.get(asst.cwd)
      if (route?.ownerSession === id) {
        route.release?.()
        mcpWiredDirs.delete(asst.cwd)
      }
    }
    roots.delete(id)
    derived.delete(id)
    listCache.at = 0
    rt.streamEnd(id)
    rt.sessionsChanged()
  }

  function resolvePermission(root, requestID) {
    const s = sessions.get(root)
    const ac = s?.blocked.get(requestID)
    if (!ac) return
    ac.abort()
    s.blocked.delete(requestID)
    listCache.at = 0
    rt.sessionsChanged(0)
  }

  async function onEvent(ctx, ev) {
    if (ev?.id) {
      if (seen.has(ev.id)) return
      seen.add(ev.id)
      if (seen.size > 4000) seen.delete(seen.values().next().value)
    }
    const d = ev?.data ?? {}
    const id = d.sessionID
    if (typeof id !== 'string') return
    switch (ev.type) {
      case 'permission.asked':
        return void onPermission(ctx, d)
      case 'permission.replied':
      case 'form.replied':
      case 'form.cancelled': {
        const { root } = await rootOf(id)
        return resolvePermission(root, d.requestID ?? d.formID ?? d.id)
      }
      case 'form.created': { // a question: the dialog in the TUI answers it (the plugin API cannot); the row says "needs you"
        const { root } = await rootOf(id)
        const s = st(root)
        s.blocked.set(d.id ?? d.formID ?? `form-${now()}`, new AbortController())
        listCache.at = 0
        return rt.sessionsChanged(0)
      }
    }
    // Everything below is about a session the user works in; a subagent's own turns are not (its root's are).
    const { root } = await rootOf(id)
    if (root !== id) return
    if (ev.type === 'session.deleted') return forget(id)
    if (st(id).lastSeq === undefined) void commit(id) // the conversation as it is now is the baseline of what is pushed
    switch (ev.type) {
      case 'session.created':
      case 'session.renamed':
        if (ev.type === 'session.renamed' && typeof d.title === 'string') st(id).title = d.title
        listCache.at = 0
        return rt.sessionsChanged()
      case 'session.inbox.enqueued':
        if (d.item?.type === 'user') running(id, true)
        return
      case 'session.execution.started':
        return running(id, true)
      case 'session.inbox.delivered':
      case 'session.tool.success':
      case 'session.tool.failed':
        return commitSoon(id)
      case 'session.text.started':
        if (d.assistantMessageID !== st(id).textMessage) {
          st(id).texts.clear()
          st(id).textMessage = d.assistantMessageID
        }
        return
      case 'session.text.delta': {
        const s = st(id)
        if (d.assistantMessageID !== s.textMessage) {
          s.texts.clear()
          s.textMessage = d.assistantMessageID
        }
        s.texts.set(d.ordinal ?? 0, (s.texts.get(d.ordinal ?? 0) ?? '') + (d.delta ?? ''))
        if (!s.running) running(id, true)
        return rt.stream(id, liveText(s.texts))
      }
      case 'session.text.ended':
      case 'session.step.ended':
      case 'session.step.failed':
        rt.streamEnd(id)
        return commitSoon(id)
      case 'session.execution.succeeded':
      case 'session.execution.failed':
      case 'session.execution.interrupted':
        return ended(id)
    }
  }

  async function consume(entry) {
    let delay = 1000
    while (!entry.abort.signal.aborted && !stopped) {
      try {
        for await (const ev of entry.ctx.event.subscribe({ signal: entry.abort.signal })) {
          if (entry.abort.signal.aborted) break
          delay = 1000
          onEvent(entry.ctx, ev).catch((err) => log(`persona: opencode event ${ev?.type}: ${err.message}`))
        }
      } catch (err) {
        if (!entry.abort.signal.aborted) log(`persona: opencode events: ${err.message}; reconnecting`)
      }
      if (entry.abort.signal.aborted || stopped) break
      await new Promise((r) => {
        const t = setTimeout(r, delay)
        t.unref?.()
      })
      delay = Math.min(delay * 2, 30_000)
    }
  }

  function elect() {
    primary = [...locations].find((l) => !l.abort.signal.aborted)
    if (primary && !primary.reading) {
      primary.reading = true
      void refreshModels(primary.ctx)
      void consume(primary)
    }
  }

  function stopHost() {
    stopped = true
    started = false
    for (const l of locations) l.abort.abort()
    locations.clear()
    primary = undefined
    for (const t of commitTimers.values()) clearTimeout(t)
    commitTimers = new Map()
    for (const s of sessions.values()) {
      clearInterval(s.poll)
      for (const ac of s.blocked.values()) ac.abort()
    }
    for (const route of mcpWiredDirs.values()) route.release?.()
    mcpWiredDirs.clear()
    mcpProxy.stop()
    rt.stop()
    try {
      db?.close()
    } catch {}
    db = undefined
  }

  return {
    /**
     * A location's plugin context (opencode calls the plugin once per project directory in the server process).
     * Returns what to call when the location goes away.
     */
    register(ctx) {
      const entry = { ctx, directory: ctx.location?.directory ?? '', abort: new AbortController(), reading: false }
      locations.add(entry)
      if (!started) {
        started = true
        stopped = false
        rt.start()
      }
      if (!primary) elect()
      return () => {
        entry.abort.abort()
        locations.delete(entry)
        if (primary === entry) {
          primary = undefined
          elect()
        }
        if (locations.size === 0) stopHost()
      }
    },
    stop: stopHost,
    runtime: rt,
    sessions,
    assistants,
    /** Handle one event (tests). */
    onEvent,
    commit,
  }
}
