// Standalone ACP companion runtime for opencode (docs/integrations/opencode.md).
// Connects to ~/.persona/runtime.sock without requiring a resident `opencode serve` process:
//  - Discovers available models from `opencode models --verbose` (and merges ACP session configOptions)
//  - Starts new Agent sessions (`session.start`) and Persona Assistant conversations (`assistant: true`) via `opencode acp`
//  - Loads `AGENTS.md` natively from the requested working directory (`~/.persona/assistant` for Assistant chats)
//  - Exposes Persona MCP tools per session through a private loopback HTTP MCP proxy (`lib/mcp.js`), with strict
//    assistant vs. ordinary Agent tool scoping and command-gate enforcement
//  - Reads existing sessions from `opencode.db` (both 1.x `session`/`message`/`part` and 2.x `session_v2`/`session_message`),
//    listing sessions not owned by this companion as `offline` so a live `opencode serve` plugin wins (`prefer_running`)
//    and herdr TUI rows continue merging cleanly via `mergeOpencodeRow`.
//
// Assistant ACP processes and MCP routes are private per session. Process
// isolation alone does not prove an authoritative policy channel. A pinned
// version/config/ACP self-check must pass before supervision is advertised.

import { assistantPreview } from '../../runtime-sdk/lib/assistant-preview.js'
import { createHash } from 'node:crypto'
import { homedir } from 'node:os'
import { basename, resolve } from 'node:path'
import { createRuntime, interactionId, light, page, RpcError, snippet } from '../../runtime-sdk/index.js'
import { isAssistantDir } from '../../runtime-sdk/lib/identity.js'
import { OpencodeAcpClient, privateAcpArgs } from './acp.js'
import { createAssistantWorkers } from './assistant-workers.js'
import { isMemoryWritePath } from './memory-write.js'
import { createSupervisionStore, policyEnvironment, supportsSystemPrompt } from './supervision.js'
import { defaultDbPath, openReadOnly } from './db.js'
import {
  acpPermissionCard,
  acpToolRawName,
  contextTokens,
  fromRow,
  imageOf,
  lastText,
  loadBlobImage,
  messageItems,
  promptWithContext,
  toolInput,
  toolName,
  v1ImageOf,
  v1SessionItems,
  visibleUserText,
} from './map.js'
import { createMcpProxy, extractShellGates, matchShellGate } from './mcp.js'
import {
  buildLauncher,
  discoverCliModels,
  normalizeModels,
  parseAcpEffortOptions,
  parseAcpModelOptions,
  PERSONA_EFFORTS,
  readConfiguredModel,
  resolveModel,
} from './models.js'

export const COMPANION_VERSION = '0.4.0'
const ROOTS = 200
const LIST_CACHE = 3000
const MODELS_EVERY = 5 * 60_000
const VALID_MODES = new Set(['default', 'acceptEdits', 'auto', 'plan', 'bypassPermissions'])
const VALID_EFFORTS = new Set(PERSONA_EFFORTS)
const SUPERVISION_ERROR_CODE = 'assistant_supervision_runtime'
const INTERRUPT_SETTLE_MS = 10_000
const THOUGHT_PUSH_MS = 150
const DEFAULT_ACP_IDLE_MS = 5 * 60_000

/**
 * Build the child env for `opencode acp`, ensuring `bash`, `edit`, and `write` always route
 * through `session/request_permission` so command gates, plan mode, memory writes, acceptEdits,
 * and bypassPermissions are enforced by the companion rather than bypassed by opencode defaults.
 */
export function buildAcpEnv(baseEnv = process.env) {
  let perm = {}
  if (typeof baseEnv?.OPENCODE_PERMISSION === 'string' && baseEnv.OPENCODE_PERMISSION.trim()) {
    try {
      const parsed = JSON.parse(baseEnv.OPENCODE_PERMISSION)
      if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) perm = { ...parsed }
    } catch {
      // ignore malformed user env
    }
  }
  perm.bash = 'ask'
  perm.edit = 'ask'
  perm.write = 'ask'
  return { ...baseEnv, OPENCODE_PERMISSION: JSON.stringify(perm) }
}

/**
 * @param {{
 *   bin?: string,
 *   args?: string[],
 *   dbPath?: string,
 *   openDb?: () => Promise<{ all(sql: string, ...params: any[]): any[], close(): void }>,
 *   configDir?: string,
 *   home?: string,
 *   env?: NodeJS.ProcessEnv,
 *   socketPath?: string,
 *   connect?: (path: string) => import('node:net').Socket,
 *   runtime?: string,
 *   name?: string,
 *   launcher?: string,
 *   displayName?: string,
 *   model?: string,
 *   assistant?: boolean,
 *   listDays?: number,
 *   maxSessions?: number,
 *   retryMs?: number,
 *   pushDelayMs?: number,
 *   refreshMs?: number,
 *   instance?: string,
 *   log?: (msg: string) => void,
 *   now?: () => number,
 *   acp?: OpencodeAcpClient,
 *   acpIdleMs?: number,
 *   mcpProxy?: ReturnType<typeof createMcpProxy>,
 *   assistantAcpFactory?: (options: object) => OpencodeAcpClient,
 *   policyStore?: ReturnType<typeof createSupervisionStore>,
 *   policyDir?: string,
 *   maxAssistantWorkers?: number,
 *   assistantIdleMs?: number,
 *   assistantReapMs?: number,
 *   discoverModels?: (opts?: { refresh?: boolean }) => Promise<any[]>,
 * }} [options]
 */
export function createOpencodeCompanion(options = {}) {
  const home = options.home || homedir()
  const env = options.env || process.env
  const bin = options.bin || 'opencode'
  const log = options.log || (() => {})
  const now = options.now || Date.now
  const listDays = options.listDays ?? 7
  const maxSessions = options.maxSessions ?? 80
  const acpIdleMs = options.acpIdleMs ?? DEFAULT_ACP_IDLE_MS
  const runtimeId = options.runtime || 'opencode'
  const label = options.name || 'opencode'
  const launcherName = options.launcher === undefined ? 'opencode' : String(options.launcher).trim()
  const displayName = typeof options.displayName === 'string' && options.displayName.trim() ? options.displayName.trim() : label
  const assistantEnabled = options.assistant !== false
  const openDb = options.openDb || (() => openReadOnly(options.dbPath || defaultDbPath(env, home)))
  const discoverModelsFn = options.discoverModels || (() => discoverCliModels({ bin, env }))

  const explicitDefaultModel = typeof options.model === 'string' && options.model.trim()
    ? options.model.trim()
    : readConfiguredModel({ configDir: options.configDir, env, home })
  let displayDefaultModel = explicitDefaultModel

  let catalog = []
  let catalogAt = 0
  let catalogFlight
  const windows = new Map()
  let gateShellRules = []
  let gateShellRulesReady = false

  // Sessions created or actively driven by this companion over ACP.
  const live = new Map()
  const closedSessions = new Set()

  let db
  let dbFailedAt = 0
  let schemaKind = '' // 'v2' | 'v1' | ''
  let listCache = { at: 0, rows: [] }
  let stopped = false
  let acpIdleTimer = null

  let assistantWorkers
  let assistantReaper
  const acpOptions = { bin, args: options.args, cwd: home, env: buildAcpEnv(env), log }
  const policyStore = options.policyStore || createSupervisionStore({ home, bin, env: acpOptions.env, directory: options.policyDir })
  let supervisionReady = null
  const sharedAcp = options.acp || new OpencodeAcpClient(acpOptions)
  const accepts = (client, worker, id) => {
    const session = live.get(id)
    return session && !closedSessions.has(id) && (worker
      ? !worker.retiring && worker.session === session && session.worker === worker
      : !session.assistant)
  }
  function connectAcp(client, worker = null) {
    client.onPermissionRequest = (params) => accepts(client, worker, params?.sessionId)
      ? handlePermissionRequest(params)
      : { outcome: { outcome: 'cancelled' } }
    client.on('update', (params) => {
      if (accepts(client, worker, params?.sessionId)) onAcpUpdate(params)
    })
    if (worker) client.on('process', (pid) => {
      const session = worker.session
      if (!session || worker.retiring || session.worker !== worker) return
      rt.assistantPid(session.id, pid)
      if (!pid) {
        session.loadedGeneration = 0
        if (session.running || session.asks.size) {
          session.processFailure = true
          session.turnError = 'OpenCode execution process exited unexpectedly'
          session.abort?.abort()
          for (const ask of session.asks) ask.abort()
          rt.sessionsChanged(0)
        }
      }
    })
    return client
  }
  connectAcp(sharedAcp)

  function isAcpBusy() {
    if (sharedAcp.busy) return true
    for (const s of live.values()) {
      if (!s.assistant && (s.running || s.queued > 0 || s.asks.size > 0)) return true
    }
    return false
  }

  function cancelAcpIdleStop() {
    if (acpIdleTimer) {
      clearTimeout(acpIdleTimer)
      acpIdleTimer = null
    }
  }

  function scheduleAcpIdleStop() {
    cancelAcpIdleStop()
    if (stopped || acpIdleMs <= 0 || !sharedAcp.pid || isAcpBusy()) return
    acpIdleTimer = setTimeout(() => {
      acpIdleTimer = null
      if (stopped || isAcpBusy() || !sharedAcp.pid) return
      log('stopping idle opencode acp child')
      void sharedAcp.stopChild().catch((err) => log(`idle opencode acp stop: ${err?.message ?? err}`))
    }, acpIdleMs)
    acpIdleTimer.unref?.()
  }

  const mcpProxy = options.mcpProxy || createMcpProxy({
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
      const s = binding?.session
      const sid = s?.id || binding?.sessionId || ''
      if (!sid || closedSessions.has(sid)) {
        return { content: [{ type: 'text', text: 'Conversation is not available' }], is_error: true }
      }
      const asst = Boolean(s?.assistant ?? binding?.assistant)
      return rt.callTool({ name, args, session_id: sid, assistant: asst }, { signal: s?.abort?.signal })
    },
    log,
  })

  const initialLaunchers = launcherName
    ? [buildLauncher({ name: launcherName, displayName, models: catalog, defaultModel: displayDefaultModel })]
    : undefined

  const rt = createRuntime({
    listSessions,
    items,
    cursor,
    item,
    image,
    start,
    send,
    interrupt,
    rename,
    close,
    models,
    modes,
    hasLocalClient: () => false,
  }, {
    runtime: runtimeId,
    name: label,
    agent: 'opencode',
    version: COMPANION_VERSION,
    ...(launcherName ? { launcher: launcherName, launcherModel: displayDefaultModel || undefined, launchers: initialLaunchers } : {}),
    assistant: assistantEnabled,
    readonly: true,
    modelSwitch: true,
    // No hello is sent until the pinned binary/config/ACP startup probe settles.
    get supervisionPolicy() { return Boolean(supervisionReady) },
    requires: ['prefer_running'],
    socketPath: options.socketPath,
    connect: options.connect,
    instance: options.instance,
    retryMs: options.retryMs,
    pushDelayMs: options.pushDelayMs ?? 50,
    refreshMs: options.refreshMs ?? 15_000,
    log,
    onHub: (connected) => {
      if (!connected) {
        gateShellRulesReady = false
        return
      }
      for (const worker of assistantWorkers?.workers ?? []) {
        if (worker.session && !worker.retiring) rt.assistantPid(worker.session.id, worker.acp.pid)
      }
      if (assistantEnabled) void refreshGateRules()
    },
  })

  assistantWorkers = createAssistantWorkers({
    limit: options.maxAssistantWorkers ?? 8,
    idleMs: options.assistantIdleMs ?? 300_000,
    now,
    async create(worker, session) {
      const config = { ...acpOptions, args: privateAcpArgs(acpOptions.args), processGroup: true }
      let allocated
      try {
        if (session && !session.mcpRoute) {
          session.mcpRoute = allocated = await mcpProxy.allocate({ session, sessionId: session.id, assistant: true })
        }
        if (session?.policyRecord) {
          if (!supervisionReady) throw new RpcError('OpenCode follow-up instructions are unavailable', SUPERVISION_ERROR_CODE)
          const checkedEnv = await policyStore.verify(session.policyRecord, session.mcpRoute)
          config.env = supervisionReady.channel === 'append'
            ? policyEnvironment(acpOptions.env, null, session.mcpRoute)
            : checkedEnv
        }
        const client = options.assistantAcpFactory ? options.assistantAcpFactory(config) : new OpencodeAcpClient(config)
        if (client === sharedAcp || [...assistantWorkers.workers].some((other) => other !== worker && other.acp === client)) {
          throw new Error('Assistant execution needs a dedicated ACP client')
        }
        return connectAcp(client, worker)
      } catch (error) {
        if (allocated) { allocated.release(); session.mcpRoute = null }
        if (session?.policyRecord) throw new RpcError('OpenCode follow-up instruction self-check failed. Nothing was sent.', SUPERVISION_ERROR_CODE)
        throw error
      }
    },
    busy: (session) => session.running || session.queued > 0 || session.asks.size > 0 || session.forking || session.admitting > 0 || session.policyFlight,
    retiring(worker) {
      const session = worker.session
      if (!session || session.worker !== worker) return
      rt.assistantPid(session.id, 0)
      session.worker = null
      session.loadedGeneration = 0
      session.mcpRoute?.release()
      session.mcpRoute = null
    },
  })

  async function withSessionWorker(session, fn) {
    // Cover admission itself, before the serialized pool grants a lease. A
    // concurrent role change must not retire a process whose control is queued.
    session.admitting++
    try {
      if (!session.assistant) return await fn(sharedAcp)
      const worker = await assistantWorkers.acquire(session)
      try { return await fn(worker.acp) } finally { assistantWorkers.release(worker) }
    } finally { session.admitting-- }
  }

  function requestedPolicy(params) {
    if (!Object.hasOwn(params, 'supervision_instructions')) return undefined
    if (typeof params.supervision_instructions !== 'string') throw new RpcError('Invalid follow-up instructions', SUPERVISION_ERROR_CODE)
    const text = params.supervision_instructions
    if (text && !supervisionReady) {
      throw new RpcError('opencode does not support isolated supervision policy channels', SUPERVISION_ERROR_CODE)
    }
    return text
  }

  function nativePolicy(s) {
    if (supervisionReady?.channel !== 'append') return {}
    return { systemPrompt: s.policy ? { mode: 'append', content: [{ type: 'text', text: s.policy }] } : null }
  }

  function verifyWorkerPolicy(s, initialized) {
    if (!s.policy) return
    if (!supervisionReady || initialized.agentInfo?.version !== supervisionReady.version ||
      supervisionReady.channel === 'append' && !supportsSystemPrompt(initialized.capabilities)) {
      throw new RpcError('OpenCode follow-up instruction capability changed. Nothing was sent.', SUPERVISION_ERROR_CODE)
    }
  }

  async function synchronizePolicy(s, text, interruptVersion) {
    if (text === undefined || text === s.policy) {
      try { await policyStore.check(s.policyRecord) } catch { throw new RpcError('OpenCode follow-up instructions are unavailable. Nothing was sent.', SUPERVISION_ERROR_CODE) }
      if (text === '') await policyStore.removeForSession(s.id)
      return null
    }
    if (s.running || s.queued || s.asks.size || s.admitting > 1 || s.worker?.leases || s.policyFlight) throw new RpcError('Wait for this conversation to finish before changing its follow-up instructions', 'busy')
    s.policyFlight = true
    const previous = { text: s.policy, record: s.policyRecord, worker: s.worker, loadedGeneration: s.loadedGeneration }
    let next, oldFileRemoved = false
    const cancelled = () => stopped || closedSessions.has(s.id) || interruptVersion !== s.interruptVersion
    const rollback = async () => {
      try {
        if (s.worker && s.worker !== previous.worker) await assistantWorkers.discard(s.worker)
        if (stopped || closedSessions.has(s.id)) {
          s.policy = ''; s.policyRecord = null; s.loadedGeneration = 0
          await Promise.all([policyStore.remove(previous.record), policyStore.remove(next)])
          return
        }
        s.policy = previous.text
        s.policyRecord = previous.record
        s.loadedGeneration = s.worker === previous.worker ? previous.loadedGeneration : 0
        if (oldFileRemoved && previous.text) {
          s.policyRecord = await policyStore.create(previous.text)
          await policyStore.bind(s.policyRecord, s.id)
        }
        await policyStore.remove(next)
      } finally { s.policyFlight = false }
    }
    try {
      next = await policyStore.create(text)
      await policyStore.bind(next, s.id)
      if (cancelled()) throw new Error('Input cancelled')
      if (s.worker) await assistantWorkers.discard(s.worker)
      s.policy = text
      s.policyRecord = next
      if (cancelled()) throw new Error('Input cancelled')
      const worker = await assistantWorkers.acquire(s)
      try { await ensureLoaded(s, worker.acp) } finally { assistantWorkers.release(worker) }
      if (cancelled()) throw new Error('Input cancelled')
      // Keep the role-change lock until the admitting caller either queues its
      // first input synchronously or restores the old role after interruption.
      return {
        async retireOldFile() {
          await policyStore.remove(previous.record); oldFileRemoved = true
          await policyStore.removeForSession(s.id, next)
        },
        accept() { s.policyFlight = false },
        rollback,
      }
    } catch {
      await rollback().catch(() => {})
      throw new RpcError('OpenCode could not change its follow-up instructions. Nothing was sent.', SUPERVISION_ERROR_CODE, { delivered: 'none' })
    }
  }

  async function refreshGateRules({ throwOnError = false } = {}) {
    try {
      if (!rt.hubConnected) throw new Error('Persona tool rules are unavailable')
      const list = await rt.tools({ assistant: true })
      if (!rt.hubConnected) throw new Error('Persona tool rules are unavailable')
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

  // ---- models ----

  function publishCatalog(rawList, fallbackDefault = '') {
    const def = explicitDefaultModel || fallbackDefault
    const normalized = normalizeModels(rawList, def)
    for (const [k, w] of normalized.windows) windows.set(k, w)
    if (normalized.models.length > 0) {
      catalog = normalized.models
      displayDefaultModel = explicitDefaultModel || normalized.defaultModel || displayDefaultModel
    }
    if (launcherName) {
      rt.launchersChanged([buildLauncher({ name: launcherName, displayName, models: catalog, defaultModel: displayDefaultModel })])
    }
    return catalog
  }

  async function models(params = {}) {
    if (!params.refresh && catalog.length > 0 && now() - catalogAt < MODELS_EVERY) {
      return { agent: 'opencode', models: catalog }
    }
    if (!catalogFlight) {
      catalogFlight = Promise.resolve()
        .then(() => discoverModelsFn({ refresh: params.refresh === true }))
        .then((raw) => {
          catalogAt = now()
          publishCatalog(raw)
          return { agent: 'opencode', models: catalog }
        })
        .finally(() => {
          catalogFlight = undefined
        })
    }
    return catalogFlight
  }

  function applyConfigOptions(s, configOptions, { preserveMode = false } = {}) {
    if (!Array.isArray(configOptions)) return
    const { models: acpModels, currentModel } = parseAcpModelOptions(configOptions)
    if (currentModel) s.model = currentModel
    const { configId: effortConfigId, currentEffort, efforts: acpEfforts } = parseAcpEffortOptions(configOptions)
    if (effortConfigId) s.effortConfigId = effortConfigId
    if (acpEfforts.length > 0) s.acpEfforts = acpEfforts
    if (currentEffort) s.effort = currentEffort
    const modeOpt = configOptions.find((c) => c && (c.id === 'mode' || c.category === 'mode'))
    if (!preserveMode) {
      if (modeOpt?.currentValue === 'plan') s.mode = 'plan'
      else if (modeOpt?.currentValue && s.mode === 'plan') s.mode = 'default'
    }
    if (acpModels.length > 0 && catalog.length === 0) {
      publishCatalog(acpModels, currentModel)
    }
  }

  // ---- read-only database (supports both 1.x session/message/part and 2.x session_v2/session_message) ----

  async function database() {
    if (db) return db
    if (dbFailedAt && now() - dbFailedAt < 60_000) return null
    try {
      db = await openDb()
      dbFailedAt = 0
      schemaKind = ''
    } catch (err) {
      dbFailedAt = now()
      log(`persona: opencode db: ${err.message}`)
    }
    return db ?? null
  }

  async function queryRows(sql, ...params) {
    const d = await database()
    if (!d) return []
    try {
      return d.all(sql, ...params)
    } catch (err) {
      log(`persona: opencode query: ${err.message}`)
      if (/not an? (database|open)|closed/i.test(String(err.message))) {
        db = undefined
        schemaKind = ''
      }
      return []
    }
  }

  async function detectSchema() {
    if (schemaKind) return schemaKind
    const tables = new Set((await queryRows("select name from sqlite_master where type = 'table'")).map((r) => r.name))
    if (tables.has('session_v2') && tables.has('session_message')) schemaKind = 'v2'
    else if (tables.has('session') && tables.has('message') && tables.has('part')) schemaKind = 'v1'
    return schemaKind
  }

  async function readDbSessionMeta(id) {
    const kind = await detectSchema()
    if (kind === 'v2') {
      const [r] = await queryRows('select id, title, directory, model, revert, time_updated from session_v2 where id = ? and parent_id is null', id)
      return r ? { ...r, _schema: 'v2' } : null
    }
    if (kind === 'v1') {
      const [r] = await queryRows('select id, title, directory, model, revert, time_updated from session where id = ? and parent_id is null', id)
      return r ? { ...r, _schema: 'v1' } : null
    }
    return null
  }

  function parseSessionModelJson(raw) {
    if (!raw) return ''
    try {
      const parsed = JSON.parse(raw)
      if (parsed?.providerID && parsed?.id) return `${parsed.providerID}/${parsed.id}`
      if (parsed?.providerID && parsed?.modelID) return `${parsed.providerID}/${parsed.modelID}`
      return parsed?.id || parsed?.modelID || ''
    } catch {
      return ''
    }
  }

  async function readDbItems(id, clientMsgIdFor) {
    const meta = await readDbSessionMeta(id)
    if (!meta) return null
    if (meta._schema === 'v2') {
      const rows = await queryRows('select id, seq, type, data, time_updated from session_message where session_id = ? order by seq', id)
      let userOrdinal = 0
      const all = []
      const msgs = []
      for (const r of rows) {
        const msg = fromRow(r)
        msgs.push({ ...msg, seq: r.seq })
        if (msg.type === 'user') userOrdinal++
        all.push(...messageItems(msg, r.seq, { clientMsgIdFor, userOrdinal }))
      }
      const toSeq = rows.at(-1)?.seq ?? 0
      const lastAssistant = [...msgs].reverse().find((m) => m.type === 'assistant')
      const revertHash = meta.revert ? `:r${createHash('sha256').update(String(meta.revert)).digest('hex').slice(0, 8)}` : ''
      return {
        meta,
        items: all,
        toSeq,
        history: `opencode:${id}${revertHash}`,
        lastText: lastText(msgs),
        model: lastAssistant?.model?.providerID && lastAssistant?.model?.id
          ? `${lastAssistant.model.providerID}/${lastAssistant.model.id}`
          : (lastAssistant?.model?.id || parseSessionModelJson(meta.model)),
        tokens: contextTokens(lastAssistant),
        hasAssistant: !!lastAssistant,
        userCount: userOrdinal,
      }
    }
    const messages = await queryRows('select id, time_created, time_updated, data from message where session_id = ? order by time_created asc, rowid asc', id)
    const parts = await queryRows('select id, message_id, time_created, time_updated, data from part where session_id = ? order by time_created asc, rowid asc', id)
    const byMsg = new Map()
    for (const p of parts) {
      let arr = byMsg.get(p.message_id)
      if (!arr) { arr = []; byMsg.set(p.message_id, arr) }
      arr.push(p)
    }
    const mapped = v1SessionItems(messages, byMsg, { clientMsgIdFor })
    const revertHash = meta.revert ? `:r${createHash('sha256').update(String(meta.revert)).digest('hex').slice(0, 8)}` : ''
    const qualifiedModel = mapped.provider && mapped.model
      ? `${mapped.provider}/${mapped.model}`
      : (mapped.model || parseSessionModelJson(meta.model))
    return {
      meta,
      items: mapped.items,
      toSeq: mapped.toSeq,
      history: `opencode:${id}${revertHash}`,
      lastText: mapped.lastText,
      model: qualifiedModel,
      tokens: mapped.tokens,
      hasAssistant: mapped.hasAssistant,
      userCount: mapped.userCount,
      messages,
      partsByMessage: byMsg,
    }
  }

  // ---- live session state ----

  function createLiveState(id, cwd, { assistant = false, title = '', model = '', mode = 'default', effort = '', startedByPersona = false } = {}) {
    const s = {
      id,
      cwd,
      title: title || basename(cwd) || 'opencode',
      model,
      mode: mode || 'default',
      effort: effort || '',
      effortConfigId: '',
      acpEfforts: [],
      assistant,
      memoryDir: '',
      startedByPersona,
      loadedGeneration: 0,
      worker: null,
      processFailure: false,
      interruptVersion: 0,
      policy: '',
      policyRecord: null,
      policyFlight: false,
      admitting: 0,
      running: false,
      queued: 0,
      tail: Promise.resolve(),
      abort: null,
      mcpRoute: null,
      liveItems: [],
      liveSeq: 0,
      pushedSeq: 0,
      activeText: '',
      activeThought: '',
      activeThoughtItem: null,
      thoughtTimer: null,
      activeTools: new Map(), // toolCallId -> { item, rawName }
      lastText: '',
      turnError: '',
      contextTokens: 0,
      contextWindow: 0,
      updatedAt: now(),
      history: `opencode:${id}`,
      asks: new Set(),
      sentClientMsgIds: new Map(), // userOrdinal -> { text, id }
      userInputCount: 0,
      hasAssistant: false,
      activeTurnDone: null,
    }
    live.set(id, s)
    return s
  }

  function clientMsgIdLookup(s) {
    if (!s) return undefined
    return (ordinal, text) => {
      const rec = s.sentClientMsgIds.get(ordinal)
      if (rec && (!rec.text || rec.text === text)) return rec.id
      return undefined
    }
  }

  function appendLiveItem(s, item) {
    const seq = ++s.liveSeq
    const full = { id: `${seq}#0`, seq, ts: new Date(now()).toISOString(), ...item }
    s.liveItems.push(full)
    s.updatedAt = now()
    return full
  }

  function pushLiveUpdate(s, item) {
    if (stopped || closedSessions.has(s.id)) return
    if (!rt.watching(s.id) || !item) return
    if (item.seq <= s.pushedSeq) {
      rt.append(s.id, { items: [light(item)], toSeq: s.pushedSeq, history: `${s.history}:live`, update: true })
    } else {
      pushLiveSnapshot(s)
    }
  }

  function flushActiveThought(s) {
    if (s.thoughtTimer) {
      clearTimeout(s.thoughtTimer)
      s.thoughtTimer = null
    }
    if (s.activeThoughtItem) {
      s.activeThoughtItem.text = s.activeThought
      pushLiveUpdate(s, s.activeThoughtItem)
      s.activeThoughtItem = null
      s.activeThought = ''
    } else if (s.activeThought) {
      appendLiveItem(s, { role: 'assistant', kind: 'thinking', text: s.activeThought })
      s.activeThought = ''
    }
  }

  function flushLiveText(s) {
    flushActiveThought(s)
    if (s.activeText) {
      appendLiveItem(s, { role: 'assistant', kind: 'text', text: s.activeText })
      s.lastText = assistantPreview(s.activeText) ?? s.lastText
      s.hasAssistant = true
      s.activeText = ''
    }
  }

  function onAcpUpdate(params) {
    const id = params?.sessionId
    const upd = params?.update
    if (!id || !upd) return
    const s = live.get(id)
    if (!s || closedSessions.has(id)) return
    s.updatedAt = now()
    switch (upd.sessionUpdate) {
      case 'config_option_update':
        applyConfigOptions(s, upd.configOptions)
        rt.sessionsChanged()
        break
      case 'session_info_update':
        if (typeof upd.title === 'string' && upd.title.trim()) {
          s.title = upd.title.trim()
          rt.sessionsChanged()
        }
        break
      case 'agent_thought_chunk': {
        const txt = upd.content?.text
        if (typeof txt === 'string' && txt) {
          s.activeThought += txt
          if (!s.activeThoughtItem) {
            s.activeThoughtItem = appendLiveItem(s, { role: 'assistant', kind: 'thinking', text: s.activeThought })
            pushLiveSnapshot(s)
          } else {
            s.activeThoughtItem.text = s.activeThought
            if (!s.thoughtTimer) {
              s.thoughtTimer = setTimeout(() => {
                s.thoughtTimer = null
                if (s.activeThoughtItem) pushLiveUpdate(s, s.activeThoughtItem)
              }, THOUGHT_PUSH_MS)
              s.thoughtTimer.unref?.()
            }
          }
        }
        break
      }
      case 'agent_message_chunk': {
        const txt = upd.content?.text
        if (typeof txt === 'string' && txt) {
          flushActiveThought(s)
          s.activeText += txt
          s.hasAssistant = true
          rt.stream(id, s.activeText)
        }
        break
      }
      case 'tool_call': {
        rt.streamEnd(id)
        flushLiveText(s)
        const callId = upd.toolCallId || `call_${now()}`
        const rawName = acpToolRawName(upd)
        const item = appendLiveItem(s, {
          role: 'assistant',
          kind: 'tool_use',
          tool_name: toolName(rawName),
          tool_use_id: callId,
          tool_input: toolInput(rawName, upd.rawInput),
        })
        s.activeTools.set(callId, { item, rawName })
        pushLiveSnapshot(s)
        break
      }
      case 'tool_call_update': {
        const callId = upd.toolCallId
        const tracked = callId ? s.activeTools.get(callId) : undefined
        if (tracked && upd.rawInput && typeof upd.rawInput === 'object') {
          tracked.item.tool_input = toolInput(tracked.rawName, upd.rawInput)
          pushLiveUpdate(s, tracked.item)
        }
        if (upd.status === 'completed' || upd.status === 'failed' || upd.status === 'error') {
          let resultStr = ''
          if (Array.isArray(upd.content)) {
            resultStr = upd.content
              .map((c) => c?.content?.text ?? c?.text ?? '')
              .filter(Boolean)
              .join('\n')
          } else if (typeof upd.rawOutput?.output === 'string') {
            resultStr = upd.rawOutput.output
          } else if (typeof upd.rawOutput === 'string') {
            resultStr = upd.rawOutput
          }
          appendLiveItem(s, {
            role: 'user',
            kind: 'tool_result',
            tool_use_id: callId || `call_${now()}`,
            is_error: upd.status === 'failed' || upd.status === 'error',
            text: resultStr,
          })
          if (callId) s.activeTools.delete(callId)
          pushLiveSnapshot(s)
        }
        break
      }
      case 'usage_update':
        if (typeof upd.used === 'number' && upd.used > 0) s.contextTokens = upd.used
        if (typeof upd.size === 'number' && upd.size > 0) s.contextWindow = upd.size
        break
    }
  }

  function pushLiveSnapshot(s) {
    if (stopped || closedSessions.has(s.id)) return
    if (!rt.watching(s.id)) return
    if (s.liveSeq <= s.pushedSeq) return
    const fresh = s.liveItems
      .filter((it) => it.seq > s.pushedSeq)
      .map((it) => (it.image ? { ...it, image: { mime: it.image.mime, data: '', lazy: true } } : light(it)))
    const fromSeq = s.pushedSeq
    s.pushedSeq = s.liveSeq
    rt.append(s.id, { items: fresh, fromSeq, toSeq: s.liveSeq, history: `${s.history}:live` })
  }

  // ---- session listing and reading ----

  function effortsForModel(s) {
    if (s.acpEfforts?.length > 0) return s.acpEfforts
    if (!s.model) return []
    const entry = catalog.find((m) => m.id === s.model)
    return Array.isArray(entry?.efforts) ? entry.efforts : []
  }

  function liveRow(s) {
    const status = s.asks.size > 0
      ? 'blocked'
      : (s.running || s.queued > 0 ? 'working' : (s.hasAssistant || s.liveItems.length > 0 ? 'done' : 'idle'))
    const efforts = effortsForModel(s)
    const lastText = s.activeText ? assistantPreview(s.activeText) ?? s.lastText : s.lastText
    const row = {
      session_id: s.id,
      title: s.title || basename(s.cwd) || 'opencode',
      cwd: s.cwd,
      status,
      updated_at: new Date(s.updatedAt).toISOString(),
      rev: Math.max(s.liveSeq, s.liveItems.length),
      space_id: s.cwd,
      space_name: basename(s.cwd),
      space_kind: 'opencode',
      ...(lastText ? { last_text: lastText } : {}),
      ...(s.model ? { model: s.model } : {}),
      ...(s.effort ? { effort: s.effort } : {}),
      ...(efforts.length ? { efforts } : {}),
      ...(s.startedByPersona ? { started_by_persona: true } : {}),
      ...(launcherName ? { launcher: launcherName } : {}),
      ...(s.mode && s.mode !== 'default' ? { permission_mode: s.mode } : {}),
      ...(s.turnError ? { turn_error: s.turnError } : {}),
    }
    if (s.contextTokens > 0) {
      row.context_tokens = s.contextTokens
      const w = s.contextWindow || windows.get(s.model)
      if (w > 0) row.context_window = w
    }
    return row
  }

  async function dbRows() {
    if (now() - listCache.at < LIST_CACHE) return listCache.rows
    const kind = await detectSchema()
    let rows = []
    if (kind === 'v2') {
      rows = await queryRows('select id, title, directory, time_updated, model from session_v2 where parent_id is null and time_archived is null order by time_updated desc limit ?', ROOTS)
    } else if (kind === 'v1') {
      rows = await queryRows('select id, title, directory, time_updated, model from session where parent_id is null and time_archived is null order by time_updated desc limit ?', ROOTS)
    }
    listCache = { at: now(), rows }
    return rows
  }

  async function listSessions() {
    const rows = []
    const seenIds = new Set()
    for (const [id, s] of live) {
      if (closedSessions.has(id)) continue
      seenIds.add(id)
      rows.push(liveRow(s))
    }
    for (const r of await dbRows()) {
      if (seenIds.has(r.id) || closedSessions.has(r.id)) continue
      seenIds.add(r.id)
      const directory = r.directory || ''
      const model = parseSessionModelJson(r.model)
      // Sessions only on disk (not actively run by this companion) are reported as `offline` so a running
      // `opencode serve` plugin takes ownership via `prefer_running`.
      rows.push({
        session_id: r.id,
        title: (typeof r.title === 'string' && r.title.trim()) || basename(directory) || 'opencode',
        cwd: directory,
        status: 'offline',
        updated_at: new Date(r.time_updated ?? now()).toISOString(),
        rev: 0,
        space_id: directory,
        space_name: basename(directory),
        space_kind: 'opencode',
        ...(model ? { model } : {}),
      })
    }
    return rt.recent(rows, {
      days: listDays,
      max: maxSessions,
      pinned: (r) => r.status === 'working' || r.status === 'blocked',
      id: (r) => r.session_id,
      updatedAt: (r) => Date.parse(r.updated_at) || 0,
    })
  }

  async function sessionContent(id) {
    const s = live.get(id)
    const dbData = await readDbItems(id, clientMsgIdLookup(s))
    if (s && (s.running || !dbData || dbData.items.length < s.liveItems.length)) {
      const placeholderItems = s.liveItems.map((it) => (
        it.image ? { ...it, image: { mime: it.image.mime, data: '', lazy: true } } : it
      ))
      return {
        items: placeholderItems,
        fullItems: s.liveItems,
        toSeq: s.liveSeq,
        history: `${s.history}:live`,
        dbData,
      }
    }
    if (dbData) {
      return {
        items: dbData.items,
        fullItems: dbData.items,
        toSeq: dbData.toSeq,
        history: dbData.history,
        dbData,
      }
    }
    if (s) {
      return { items: [], fullItems: [], toSeq: 0, history: s.history, dbData: null }
    }
    throw new RpcError(`opencode 里没有这个会话（${id}）`, 'not_found')
  }

  async function items(params) {
    const c = await sessionContent(params.session_id)
    return page(c.items, params, { history: c.history, toSeq: c.toSeq })
  }

  async function cursor(sessionId) {
    const c = await sessionContent(sessionId)
    return { to_seq: c.toSeq, history: c.history }
  }

  async function item(params) {
    const c = await sessionContent(params.session_id)
    const found = c.fullItems.find((it) => it.id === params.item_id)
    if (!found) throw new RpcError(`no item ${params.item_id}`, 'not_found')
    return found
  }

  async function image(params) {
    const c = await sessionContent(params.session_id)
    const found = c.fullItems.find((it) => it.id === params.item_id)
    if (found?.image?.data) return { mime: found.image.mime, data: found.image.data }
    const [seqText, idxText] = String(params.item_id).split('#')
    const seq = Number(seqText)
    const idx = Number(idxText)
    if (c.dbData?.meta?._schema === 'v1' && Number.isSafeInteger(seq) && seq >= 1) {
      const msg = c.dbData.messages?.[seq - 1]
      const rawParts = msg ? (c.dbData.partsByMessage.get(msg.id) ?? []).map((p) => {
        try { return JSON.parse(p.data) } catch { return {} }
      }) : []
      const img = v1ImageOf(rawParts, idx)
      if (img) return img
    } else if (c.dbData?.meta?._schema === 'v2' && Number.isSafeInteger(seq) && seq >= 1) {
      const [r] = await queryRows('select id, seq, type, data from session_message where session_id = ? and seq = ?', params.session_id, seq)
      if (r) {
        const msg = fromRow(r)
        const img = imageOf(msg, idx) || await loadBlobImage(queryRows, { sessionId: params.session_id, msg, seq, idx })
        if (img) return img
      }
    }
    throw new RpcError(`no image ${params.item_id}`, 'not_found')
  }

  // ---- permissions ----

  const MEMORY_RAW_INPUT_KEYS = new Set([
    'filePath', 'path', 'file_path',
    'content', 'oldString', 'old_string', 'newString', 'new_string',
    'replaceAll', 'replace_all', 'edits', 'tool',
  ])
  const MEMORY_EDIT_ITEM_KEYS = new Set([
    'filePath', 'path', 'file_path',
    'oldString', 'old_string', 'newString', 'new_string',
    'replaceAll', 'replace_all',
  ])
  const MEMORY_PATH_KEYS = ['filePath', 'path', 'file_path']
  const MEMORY_ALLOWED_TITLES = new Set(['', 'write', 'edit', 'write_file', 'edit_file'])

  function isMemoryWrite(s, rawName, toolCall) {
    if (!s?.assistant || !s.memoryDir || s.mode === 'plan') return false
    if (rawName !== 'write' && rawName !== 'edit') return false
    if (!toolCall || typeof toolCall !== 'object' || Array.isArray(toolCall)) return false
    if (Object.prototype.hasOwnProperty.call(toolCall, 'title') && toolCall.title !== undefined) {
      if (typeof toolCall.title !== 'string') return false
      const titleWord = (toolCall.title.trim().split(/\s+/)[0] || '').toLowerCase()
      if (!MEMORY_ALLOWED_TITLES.has(titleWord)) return false
    }
    const rawInput = toolCall.rawInput
    if (!rawInput || typeof rawInput !== 'object' || Array.isArray(rawInput)) return false
    for (const key of Object.keys(rawInput)) {
      if (!MEMORY_RAW_INPUT_KEYS.has(key)) return false
    }
    if (Object.prototype.hasOwnProperty.call(rawInput, 'tool') && rawInput.tool !== undefined) {
      if (typeof rawInput.tool !== 'string') return false
      const explicit = rawInput.tool.trim().toLowerCase()
      if (explicit !== 'write' && explicit !== 'edit') return false
    }
    const files = []
    for (let i = 0; i < MEMORY_PATH_KEYS.length; i++) {
      const key = MEMORY_PATH_KEYS[i]
      if (!Object.prototype.hasOwnProperty.call(rawInput, key) || rawInput[key] === undefined) continue
      if (typeof rawInput[key] !== 'string' || !rawInput[key]) return false
      files.push(rawInput[key])
    }
    if (files.length === 0) return false
    if (Object.prototype.hasOwnProperty.call(rawInput, 'edits') && rawInput.edits !== undefined) {
      if (!Array.isArray(rawInput.edits)) return false
      for (let i = 0; i < rawInput.edits.length; i++) {
        if (!(i in rawInput.edits)) return false
        const edit = rawInput.edits[i]
        if (!edit || typeof edit !== 'object' || Array.isArray(edit)) return false
        for (const key of Object.keys(edit)) {
          if (!MEMORY_EDIT_ITEM_KEYS.has(key)) return false
        }
        for (let j = 0; j < MEMORY_PATH_KEYS.length; j++) {
          const key = MEMORY_PATH_KEYS[j]
          if (!Object.prototype.hasOwnProperty.call(edit, key) || edit[key] === undefined) continue
          if (typeof edit[key] !== 'string' || !edit[key]) return false
          files.push(edit[key])
        }
      }
    }
    if (Object.prototype.hasOwnProperty.call(toolCall, 'locations') && toolCall.locations !== undefined) {
      if (!Array.isArray(toolCall.locations)) return false
      for (let i = 0; i < toolCall.locations.length; i++) {
        if (!(i in toolCall.locations)) return false
        const loc = toolCall.locations[i]
        if (!loc || typeof loc !== 'object' || Array.isArray(loc) || typeof loc.path !== 'string' || !loc.path) return false
        files.push(loc.path)
      }
    }
    if (Object.prototype.hasOwnProperty.call(toolCall, 'content') && toolCall.content !== undefined) {
      if (!Array.isArray(toolCall.content)) return false
      for (let i = 0; i < toolCall.content.length; i++) {
        if (!(i in toolCall.content)) return false
        const item = toolCall.content[i]
        if (!item || typeof item !== 'object' || Array.isArray(item)) return false
        if (item.type === 'diff' || (Object.prototype.hasOwnProperty.call(item, 'path') && item.path !== undefined)) {
          if (typeof item.path !== 'string' || !item.path) return false
          files.push(item.path)
        }
      }
    }
    for (let i = 0; i < files.length; i++) {
      if (!isMemoryWritePath(files[i], s.memoryDir, s.cwd)) return false
    }
    return true
  }

  function pickOption(options, kind) {
    const list = Array.isArray(options) ? options : []
    return list.find((o) => o?.kind === kind)
  }

  async function handlePermissionRequest(params) {
    const id = params?.sessionId
    const s = live.get(id)
    if (!s || closedSessions.has(id)) return { outcome: { outcome: 'cancelled' } }
    const options = Array.isArray(params?.options) ? params.options : []
    const allowOnce = pickOption(options, 'allow_once') ?? options[0]
    const allowAlways = pickOption(options, 'allow_always') ?? allowOnce
    const rejectOnce = pickOption(options, 'reject_once') ?? pickOption(options, 'reject_always')
    const card = acpPermissionCard(params)
    const rawInput = params?.toolCall?.rawInput && typeof params.toolCall.rawInput === 'object' ? params.toolCall.rawInput : {}
    const rawCommand = typeof rawInput.command === 'string'
      ? rawInput.command
      : (typeof rawInput.cmd === 'string'
        ? rawInput.cmd
        : (typeof card.tool_input?.command === 'string' ? card.tool_input.command : ''))
    const isShell = card.raw_name === 'bash' || card.raw_name === 'shell'

    // 1. Assistant command gates are never auto-approved, even in bypassPermissions.
    // Match against the full, unclipped raw command and fail closed if gate rules could not be loaded.
    if (s.assistant && isShell) {
      if (!gateShellRulesReady) await refreshGateRules()
      const gate = matchShellGate(rawCommand, gateShellRules)
      if (gate) {
        return rejectOnce
          ? { outcome: { outcome: 'selected', optionId: rejectOnce.optionId } }
          : { outcome: { outcome: 'cancelled' } }
      }
      if (!gateShellRulesReady || !rt.hubConnected) {
        return rejectOnce
          ? { outcome: { outcome: 'selected', optionId: rejectOnce.optionId } }
          : { outcome: { outcome: 'cancelled' } }
      }
    }

    // 2. Plan mode rejects writes and shell execution.
    if (s.mode === 'plan' && ['write', 'edit', 'bash', 'shell'].includes(card.raw_name)) {
      return rejectOnce
        ? { outcome: { outcome: 'selected', optionId: rejectOnce.optionId } }
        : { outcome: { outcome: 'cancelled' } }
    }

    // 3. Auto-approve: assistant memory writes (outside plan), acceptEdits (write/edit), or bypassPermissions.
    const acceptEditAuto = s.mode === 'acceptEdits' && (card.raw_name === 'write' || card.raw_name === 'edit')
    if (isMemoryWrite(s, card.raw_name, params?.toolCall) || acceptEditAuto || s.mode === 'bypassPermissions') {
      return allowOnce
        ? { outcome: { outcome: 'selected', optionId: allowOnce.optionId } }
        : { outcome: { outcome: 'cancelled' } }
    }

    // 4. Ask the phone.
    if (!rt.hubConnected) return { outcome: { outcome: 'cancelled' } }
    const ac = new AbortController()
    s.asks.add(ac)
    rt.sessionsChanged(0)
    try {
      const res = await rt.ask({
        id: interactionId(runtimeId, id, 'permission', params?.toolCall?.toolCallId || `${now()}`),
        kind: 'permission',
        session_id: id,
        tool_use_id: params?.toolCall?.toolCallId,
        tool_name: card.tool_name,
        tool_input: card.tool_input,
        summary: card.summary,
        ...(card.always_label ? { always_label: card.always_label } : {}),
      }, { signal: ac.signal })
      if (res?.allow === true) {
        const picked = res.always === true ? allowAlways : allowOnce
        return picked
          ? { outcome: { outcome: 'selected', optionId: picked.optionId } }
          : { outcome: { outcome: 'cancelled' } }
      }
      if (res?.allow === false) {
        return rejectOnce
          ? { outcome: { outcome: 'selected', optionId: rejectOnce.optionId } }
          : { outcome: { outcome: 'cancelled' } }
      }
      return { outcome: { outcome: 'cancelled' } }
    } catch {
      return { outcome: { outcome: 'cancelled' } }
    } finally {
      s.asks.delete(ac)
      rt.sessionsChanged(0)
    }
  }

  // ---- session lifecycle & control ----

  function assertSessionOpen(id) {
    if (stopped || closedSessions.has(id)) throw new RpcError('opencode 会话已关闭', 'closed')
  }

  async function ensureLoaded(s, acp, { isCancelled } = {}) {
    if (acp === sharedAcp) cancelAcpIdleStop()
    const initialized = await acp.ensureStarted()
    assertSessionOpen(s.id)
    if (isCancelled?.()) return
    verifyWorkerPolicy(s, initialized)
    if (s.loadedGeneration === acp.generation && acp.loadedSessions.has(s.id)) return
    const desiredMode = s.mode
    if (!s.mcpRoute) {
      const route = s.assistant
        ? await mcpProxy.allocate({ session: s, sessionId: s.id, assistant: true })
        : await mcpProxy.forSession(s)
      if (stopped || closedSessions.has(s.id)) {
        route?.release()
        throw new RpcError('opencode 会话已关闭', 'closed')
      }
      if (s.mcpRoute && s.mcpRoute !== route) {
        if (!route?.path || route.path !== s.mcpRoute.path) route?.release()
      } else {
        s.mcpRoute = route
      }
      if (isCancelled?.()) return
    }
    const desired = { model: s.model, effort: s.effort, permission_mode: desiredMode }
    const res = await (s.assistant ? acp.resumeSession({
      sessionId: s.id, cwd: s.cwd, mcpServers: [s.mcpRoute.acpServer], ...nativePolicy(s),
    }) : acp.loadSession({
      sessionId: s.id, cwd: s.cwd, mcpServers: [s.mcpRoute.acpServer],
    }))
    if (stopped || closedSessions.has(s.id)) {
      s.mcpRoute?.release()
      s.mcpRoute = null
      if (!stopped && !s.assistant) void acp.closeSession(s.id).catch(() => {}).finally(() => { if (!stopped) scheduleAcpIdleStop() })
      throw new RpcError('opencode 会话已关闭', 'closed')
    }
    if (isCancelled?.()) return
    applyConfigOptions(s, res?.configOptions, { preserveMode: desiredMode !== 'default' })
    if (s.assistant) {
      try {
        await applySessionConfig(s, {
          ...desired, forceModel: Boolean(desired.model), forceEffort: Boolean(desired.effort), forceMode: true, isCancelled,
        }, acp)
        assertSessionOpen(s.id)
        if (isCancelled?.()) {
          s.model = desired.model
          s.effort = desired.effort
          s.mode = desiredMode
          s.loadedGeneration = 0
          return
        }
        s.loadedGeneration = acp.generation
        rt.assistantPid(s.id, acp.pid)
        return
      } catch (err) {
        s.model = desired.model
        s.effort = desired.effort
        s.mode = desiredMode
        s.loadedGeneration = 0
        throw err
      }
    }
    // Never silently downgrade a user-requested plan mode after an ACP child restart.
    if (desiredMode === 'plan') {
      let modeRes
      try {
        modeRes = await acp.setConfigOption(s.id, 'mode', 'plan')
      } catch (err) {
        assertSessionOpen(s.id)
        throw new RpcError(`opencode 无法恢复只读模式（plan）：${err.message}`, 'readonly_unsupported')
      }
      assertSessionOpen(s.id)
      if (isCancelled?.()) return
      applyConfigOptions(s, modeRes?.configOptions, { preserveMode: true })
      s.mode = 'plan'
    } else if (desiredMode && desiredMode !== 'default') {
      s.mode = desiredMode
    }
    s.loadedGeneration = acp.generation
  }

  async function applySessionConfig(s, { model, effort, permission_mode, forceModel = false, forceEffort = false, forceMode = false, isCancelled }, acp = s.worker?.acp || sharedAcp) {
    if (permission_mode) {
      assertSessionOpen(s.id)
      if (isCancelled?.()) return
      if (!VALID_MODES.has(permission_mode)) {
        throw new RpcError(`不支持的权限模式 "${permission_mode}"`, 'bad_request')
      }
      const targetNative = permission_mode === 'plan' ? 'plan' : 'build'
      const prevNative = s.mode === 'plan' ? 'plan' : 'build'
      if (forceMode || targetNative !== prevNative || permission_mode === 'plan') {
        let res
        try {
          res = await acp.setConfigOption(s.id, 'mode', targetNative)
        } catch (err) {
          assertSessionOpen(s.id)
          if (permission_mode === 'plan') {
            throw new RpcError(`opencode 无法切换到只读模式（plan）：${err.message}`, 'readonly_unsupported')
          }
          throw new RpcError(`opencode 模式切换失败：${err.message}`, 'opencode')
        }
        assertSessionOpen(s.id)
        applyConfigOptions(s, res?.configOptions, { preserveMode: true })
      }
      s.mode = permission_mode
      if (isCancelled?.()) return
    }
    if (model) {
      assertSessionOpen(s.id)
      if (isCancelled?.()) return
      if (catalog.length === 0) {
        try { await models() } catch {}
        assertSessionOpen(s.id)
        if (isCancelled?.()) return
      }
      const resolved = resolveModel(model, catalog)
      if (resolved && (forceModel || resolved.id !== s.model)) {
        let res
        try {
          res = await acp.setConfigOption(s.id, 'model', resolved.id)
        } catch (err) {
          assertSessionOpen(s.id)
          throw new RpcError(`opencode 切换模型失败（${resolved.id}）：${err.message}`, 'opencode')
        }
        assertSessionOpen(s.id)
        applyConfigOptions(s, res?.configOptions, { preserveMode: true })
        s.model = resolved.id
      }
      if (isCancelled?.()) return
    }
    if (typeof effort === 'string' && effort.trim()) {
      assertSessionOpen(s.id)
      if (isCancelled?.()) return
      const normalizedEffort = effort.trim().toLowerCase()
      if (!VALID_EFFORTS.has(normalizedEffort)) {
        throw new RpcError(`不支持的思考深度 "${effort}"`, 'bad_request')
      }
      if (forceEffort || normalizedEffort !== s.effort) {
        const configId = s.effortConfigId || 'variant'
        let res
        try {
          res = await acp.setConfigOption(s.id, configId, normalizedEffort)
        } catch (err) {
          assertSessionOpen(s.id)
          throw new RpcError(`opencode 切换思考深度失败（${normalizedEffort}）：${err.message}`, 'opencode')
        }
        assertSessionOpen(s.id)
        applyConfigOptions(s, res?.configOptions, { preserveMode: true })
        s.effort = normalizedEffort
      }
    }
  }

  async function start(params = {}, onSessionId) {
    const initialPolicy = requestedPolicy(params) || ''
    const cwd = typeof params.cwd === 'string' && params.cwd.trim() ? resolve(params.cwd.trim()) : ''
    if (!cwd) throw new RpcError('需要选择一个目录', 'bad_request')

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
      if (catalog.length === 0) {
        try { await models() } catch {}
      }
      chosenModel = resolveModel(params.model, catalog)
    }

    const isAsst = assistantEnabled && (params.assistant === true || isAssistantDir(cwd, home))
    if (isAsst) await ensureGateRules()
    if (initialPolicy && !isAsst) throw new RpcError('Follow-up instructions require an Assistant conversation', SUPERVISION_ERROR_CODE)
    let worker, record
    let acp = sharedAcp
    let route
    let sessionId = ''
    let createdState
    if (!worker) cancelAcpIdleStop()
    try {
      route = await mcpProxy.allocate({ assistant: isAsst, mode: reqMode || 'default' })
      record = isAsst ? await policyStore.create(initialPolicy) : null
      const seed = { policy: initialPolicy, policyRecord: record, mcpRoute: route }
      worker = isAsst ? await assistantWorkers.reserve(seed) : null
      acp = worker?.acp || sharedAcp
      const initialized = await acp.ensureStarted()
      verifyWorkerPolicy(seed, initialized)
      const created = await acp.newSession({ cwd, mcpServers: [route.acpServer], ...(isAsst ? nativePolicy(seed) : {}) })
      sessionId = typeof created?.sessionId === 'string' ? created.sessionId.trim() : ''
      if (!sessionId) throw new RpcError('opencode ACP 没有返回新会话 ID', 'opencode')
      await policyStore.bind(record, sessionId)

      const initialTitle = typeof params.prompt === 'string' && params.prompt.trim()
        ? snippet(visibleUserText(params.prompt))
        : ''
      // Leave model empty until ACP reports its actual currentValue or setConfigOption succeeds.
      if (live.has(sessionId) || closedSessions.has(sessionId)) throw new Error('ACP returned an already-owned session')
      const s = createLiveState(sessionId, cwd, {
        assistant: isAsst,
        title: initialTitle,
        model: '',
        mode: reqMode || 'default',
        startedByPersona: true,
      })
      createdState = s
      s.policy = initialPolicy
      s.policyRecord = record
      if (worker) {
        assistantWorkers.bind(worker, s)
        rt.assistantPid(s.id, acp.pid)
      }
      s.loadedGeneration = acp.generation
      s.mcpRoute = route
      route.bind(s)
      onSessionId?.(sessionId)
      applyConfigOptions(s, created.configOptions, { preserveMode: Boolean(reqMode) })

      // Strictly apply plan mode (readonly) before any prompt runs.
      if (reqMode === 'plan') {
        await applySessionConfig(s, { permission_mode: 'plan' })
      }
      // Only override ACP's default model when the caller requested a model or an explicit default was configured.
      const targetModel = chosenModel?.id || (explicitDefaultModel && explicitDefaultModel !== s.model ? explicitDefaultModel : '')
      if (targetModel) {
        await applySessionConfig(s, {
          model: targetModel,
          forceModel: Boolean(chosenModel && chosenModel.id !== s.model),
        })
      }
      if (reqEffort) {
        await applySessionConfig(s, { effort: reqEffort })
      }

      const hasInput = Boolean(
        (typeof params.prompt === 'string' && params.prompt.trim()) ||
        (Array.isArray(params.images) && params.images.some((i) => i?.data)),
      )
      if (hasInput) {
        await send({
          session_id: sessionId,
          text: params.prompt ?? '',
          images: params.images,
          client_msg_id: params.client_msg_id,
          assistant: isAsst,
          mode: reqMode || undefined,
        })
      }

      listCache.at = 0
      rt.sessionsChanged(0)
      scheduleAcpIdleStop()
      return { session: liveRow(s) }
    } catch (err) {
      route?.release()
      if (createdState && live.get(sessionId) === createdState) {
        live.delete(sessionId)
        await acp.closeSession(sessionId).catch(() => {})
      }
      if (worker) await assistantWorkers.discard(worker)
      else scheduleAcpIdleStop()
      await policyStore.remove(record)
      if (initialPolicy && !(err instanceof RpcError)) throw new RpcError('OpenCode follow-up instructions could not be applied. Nothing was sent.', SUPERVISION_ERROR_CODE)
      if (err instanceof RpcError) throw err
      throw new RpcError(`opencode：${err?.message ?? err}`, 'opencode')
    } finally {
      if (worker) assistantWorkers.release(worker)
    }
  }

  async function resolveSession(id) {
    if (closedSessions.has(id)) throw new RpcError('opencode 会话已关闭', 'closed')
    let s = live.get(id)
    if (s) return s
    const dbData = await readDbItems(id)
    if (!dbData) throw new RpcError(`opencode 里没有这个会话（${id}）`, 'not_found')
    const cwd = dbData.meta.directory || home
    s = createLiveState(id, cwd, {
      assistant: assistantEnabled && isAssistantDir(cwd, home),
      title: dbData.meta.title || basename(cwd) || 'opencode',
      model: dbData.model || '',
      mode: 'default',
    })
    s.liveItems = [...dbData.items]
    s.liveSeq = dbData.toSeq
    s.pushedSeq = dbData.toSeq
    s.lastText = dbData.lastText
    s.hasAssistant = dbData.hasAssistant
    s.history = dbData.history
    s.userInputCount = dbData.userCount ?? 0
    return s
  }

  async function send(params) {
    const policy = requestedPolicy(params)
    const id = params.session_id
    const s = await resolveSession(id)
    const rawText = typeof params.text === 'string' ? params.text : ''
    const images = (Array.isArray(params.images) ? params.images : []).filter((i) => i?.data)
    if (!rawText.trim() && images.length === 0) {
      throw new RpcError('消息是空的', 'bad_request')
    }

    const isAsst = assistantEnabled && (params.assistant === true || s.assistant)
    if (isAsst && !s.assistant && (s.running || s.queued || s.asks.size || s.admitting)) throw new RpcError('Wait for the Agent turn to finish before changing its role', 'busy')
    if (isAsst && !s.assistant && sharedAcp.loadedSessions.has(s.id)) {
      throw new RpcError('Start a new assistant conversation instead of sharing an Agent execution process', 'busy')
    }
    s.assistant = isAsst
    if (typeof params.memory_dir === 'string' && params.memory_dir) s.memoryDir = params.memory_dir
    const reqMode = params.mode || params.permission_mode || ''
    const reqEffort = typeof params.effort === 'string' && params.effort.trim() ? params.effort.trim().toLowerCase() : ''
    if (policy && !isAsst) throw new RpcError('Follow-up instructions require an Assistant conversation', SUPERVISION_ERROR_CODE)
    if (s.policyFlight) throw new RpcError('Follow-up instructions are changing; try again shortly', 'busy')
    const interruptVersion = s.interruptVersion
    s.admitting++
    let worker, transition
    try {
      if (policy !== undefined && policy !== s.policy) {
        // Validate the whole input before committing its new process policy.
        // Rejected model/mode choices must not change a conversation's role.
        if (reqMode && !VALID_MODES.has(reqMode)) throw new RpcError('Unsupported permission mode', 'bad_request')
        if (reqEffort && !VALID_EFFORTS.has(reqEffort)) throw new RpcError('Unsupported reasoning effort', 'bad_request')
        if (params.model) {
          if (catalog.length === 0) await models()
          resolveModel(params.model, catalog)
        }
        if (isAsst) await ensureGateRules()
      }
      transition = await synchronizePolicy(s, policy, interruptVersion)
      worker = isAsst ? await assistantWorkers.acquire(s) : null
    } catch (error) {
      await transition?.rollback()
      throw error
    } finally { s.admitting-- }
    const acp = worker?.acp || sharedAcp
    const wasBusy = s.running || s.queued > 0
    s.queued++
    let enqueued = false
    try {

      const promptText = isAsst && typeof params.context === 'string' && params.context.trim()
        ? promptWithContext(rawText, params.context)
        : rawText

      // Validate model/mode/effort upfront when idle so invalid requests fail immediately instead of asynchronously.
      if (!wasBusy) {
        if (reqMode && !VALID_MODES.has(reqMode)) {
          throw new RpcError(`不支持的权限模式 "${reqMode}"`, 'bad_request')
        }
        if (reqEffort && !VALID_EFFORTS.has(reqEffort)) {
          throw new RpcError(`不支持的思考深度 "${params.effort}"`, 'bad_request')
        }
        if (params.model) {
          try {
            if (catalog.length === 0) await models().catch(() => {})
            resolveModel(params.model, catalog)
          } catch (err) {
            throw err
          }
        }
        if (isAsst) {
          try {
            await ensureGateRules()
          } catch (err) {
            throw err
          }
        }
      }

      await transition?.retireOldFile()
      if (stopped || closedSessions.has(id) || interruptVersion !== s.interruptVersion) {
        throw new RpcError('Input was interrupted before delivery', 'busy', { delivered: 'none' })
      }
      const turnDone = s.tail.catch(() => {}).then(async () => {
        s.queued = Math.max(0, s.queued - 1)
        if (stopped || closedSessions.has(id) || interruptVersion !== s.interruptVersion) {
          listCache.at = 0
          if (!stopped) rt.sessionsChanged(0)
          if (!worker) scheduleAcpIdleStop()
          return
        }
        s.running = true
        s.turnError = ''
        s.processFailure = false
        s.abort = new AbortController()
        s.activeText = ''
        s.activeThought = ''
        s.activeThoughtItem = null
        s.activeTools.clear()

        const shown = visibleUserText(rawText)
        s.userInputCount++
        if (shown) {
          if (params.client_msg_id) {
            s.sentClientMsgIds.set(s.userInputCount, { text: shown, id: params.client_msg_id })
          }
          appendLiveItem(s, {
            role: 'user',
            kind: 'text',
            text: shown,
            ...(params.client_msg_id ? { client_msg_id: params.client_msg_id } : {}),
          })
        }
        for (const img of images) {
          appendLiveItem(s, {
            role: 'user',
            kind: 'image',
            image: { mime: img.mime || img.media_type || 'image/png', data: img.data },
          })
        }
        pushLiveSnapshot(s)
        rt.sessionsChanged(0)

        const cancelledBeforePrompt = () => (
          stopped || closedSessions.has(id) || interruptVersion !== s.interruptVersion || Boolean(s.abort?.signal.aborted)
        )
        const finishPrePromptCancel = () => {
          if (stopped || closedSessions.has(id)) return
          if (s.processFailure) {
            const msg = s.turnError || 'OpenCode execution process exited unexpectedly'
            s.turnError = msg
            appendLiveItem(s, { role: 'system', kind: 'notice', text: `出错了：${msg}` })
          } else {
            appendLiveItem(s, { role: 'system', kind: 'notice', text: '已中断' })
          }
        }
        try {
          if (isAsst) await ensureGateRules()
          if (cancelledBeforePrompt()) {
            finishPrePromptCancel()
            return
          }
          await ensureLoaded(s, acp, { isCancelled: cancelledBeforePrompt })
          if (cancelledBeforePrompt()) {
            finishPrePromptCancel()
            return
          }
          await applySessionConfig(s, {
            model: params.model, effort: reqEffort, permission_mode: reqMode, isCancelled: cancelledBeforePrompt,
          }, acp)
          if (cancelledBeforePrompt()) {
            finishPrePromptCancel()
            return
          }
          const promptBlocks = []
          if (promptText) promptBlocks.push({ type: 'text', text: promptText })
          for (const img of images) {
            promptBlocks.push({
              type: 'image',
              data: img.data,
              mimeType: img.mime || img.media_type || 'image/png',
            })
          }
          const res = await acp.prompt(id, promptBlocks, 0, s.abort?.signal)
          flushLiveText(s)
          if (res?.stopReason === 'cancelled' && !stopped && !closedSessions.has(id)) {
            appendLiveItem(s, { role: 'system', kind: 'notice', text: '已中断' })
          }
        } catch (err) {
          flushLiveText(s)
          if (stopped || closedSessions.has(id)) {
            // Session was closed or companion stopped; avoid appending synthetic error/interrupt items.
          } else if (s.abort?.signal.aborted && !s.processFailure) {
            appendLiveItem(s, { role: 'system', kind: 'notice', text: '已中断' })
          } else {
            const msg = s.processFailure && s.turnError ? s.turnError : snippet(err?.message ?? String(err))
            s.turnError = msg
            appendLiveItem(s, { role: 'system', kind: 'notice', text: `出错了：${msg}` })
          }
        } finally {
          if (!stopped && !closedSessions.has(id)) rt.streamEnd(id)
          if (s.thoughtTimer) {
            clearTimeout(s.thoughtTimer)
            s.thoughtTimer = null
          }
          for (const ac of s.asks) ac.abort()
          s.asks.clear()
          s.running = false
          s.abort = null
          s.updatedAt = now()
          listCache.at = 0
          if (!stopped && !closedSessions.has(id)) pushLiveSnapshot(s)
          if (!stopped) rt.sessionsChanged(0)
          if (!worker && !stopped) scheduleAcpIdleStop()
        }
      }).finally(() => { if (worker) assistantWorkers.release(worker) })
      s.tail = turnDone
      s.activeTurnDone = turnDone
      enqueued = true
      transition?.accept()
      return wasBusy ? { queued: true } : {}
    } catch (error) {
      try { await transition?.rollback() } catch {
        throw new RpcError('OpenCode could not restore its follow-up instructions. Nothing was sent.', SUPERVISION_ERROR_CODE, { delivered: 'none' })
      }
      throw error
    } finally {
      if (!enqueued) {
        s.queued = Math.max(0, s.queued - 1)
        if (worker) assistantWorkers.release(worker)
        if (!stopped) rt.sessionsChanged(0)
        if (!worker) scheduleAcpIdleStop()
      }
    }
  }

  async function interrupt(params) {
    const id = params.session_id
    const s = live.get(id)
    if (!s) return { settled: true }
    s.interruptVersion++
    s.abort?.abort()
    for (const ac of s.asks) ac.abort()
    s.asks.clear()
    const acp = s.worker?.acp || (!s.assistant ? sharedAcp : null)
    acp?.cancel(id)
    if (!s.running && s.queued === 0) return { settled: true }
    let timer
    const settled = await Promise.race([
      s.tail.then(() => true, () => true),
      new Promise((r) => {
        timer = setTimeout(() => r(false), INTERRUPT_SETTLE_MS)
        timer.unref?.()
      }),
    ]).finally(() => clearTimeout(timer))
    return settled ? { settled: true } : {}
  }

  async function modes(params) {
    const s = await resolveSession(params.session_id)
    if (s.policyFlight) throw new RpcError('Follow-up instructions are changing; try again shortly', 'busy')
    if (!s.assistant) cancelAcpIdleStop()
    try {
      await withSessionWorker(s, async (acp) => {
        await ensureLoaded(s, acp)
        await applySessionConfig(s, {
          model: params.model, effort: params.effort, permission_mode: params.permission_mode,
        }, acp)
      })
      listCache.at = 0
      rt.sessionsChanged(0)
    } finally {
      if (!s.assistant) scheduleAcpIdleStop()
    }
  }

  async function rename(params) {
    const title = typeof params.title === 'string' ? params.title.trim() : ''
    if (!title) throw new RpcError('名字是空的', 'bad_request')
    const s = await resolveSession(params.session_id)
    s.title = title
    s.updatedAt = now()
    rt.sessionsChanged(0)
  }

  async function close(params) {
    const id = params.session_id
    const s = live.get(id)
    closedSessions.add(id)
    if (s) {
      if (s.thoughtTimer) {
        clearTimeout(s.thoughtTimer)
        s.thoughtTimer = null
      }
      s.abort?.abort()
      for (const ac of s.asks) ac.abort()
      s.asks.clear()
      s.interruptVersion++
      if (s.assistant) {
        const worker = s.worker
        worker?.acp.cancel(id)
        if (worker) await assistantWorkers.discard(worker)
      } else {
        sharedAcp.cancel(id)
        await sharedAcp.closeSession(id)
      }
      s.mcpRoute?.release()
      s.mcpRoute = null
      await policyStore.remove(s.policyRecord)
      s.policyRecord = null
      s.policy = ''
      live.delete(id)
    }
    if (!s || s.assistant) await policyStore.removeForSession(id)
    listCache.at = 0
    rt.streamEnd(id)
    rt.sessionsChanged(0)
    scheduleAcpIdleStop()
  }

  async function startCompanion() {
    stopped = false
    // A supplied ACP object cannot establish that the configured binary is the
    // same executable. Tests and custom embedders must supply their own probe.
    supervisionReady = assistantEnabled && (!options.acp || options.policyStore) ? await policyStore.probe() : null
    if (stopped) return
    if (!assistantReaper) {
      assistantReaper = setInterval(() => { void assistantWorkers.reap().catch((err) => log(`opencode worker retirement: ${err.message}`)) }, options.assistantReapMs ?? Math.min(30_000, Math.max(10, (options.assistantIdleMs ?? 300_000) / 2)))
      assistantReaper.unref?.()
    }
    rt.start()
    void models().catch((err) => log(`persona: opencode initial models: ${err.message}`))
  }

  async function stopCompanion() {
    stopped = true
    cancelAcpIdleStop()
    clearInterval(assistantReaper)
    assistantReaper = null
    for (const s of live.values()) {
      if (s.thoughtTimer) {
        clearTimeout(s.thoughtTimer)
        s.thoughtTimer = null
      }
      s.abort?.abort()
      for (const ac of s.asks) ac.abort()
      s.asks.clear()
      s.mcpRoute?.release()
    }
    await Promise.allSettled([assistantWorkers.stop(), sharedAcp.stop()])
    await policyStore.stop()
    supervisionReady = null
    live.clear()
    mcpProxy.stop()
    rt.stop()
    try { db?.close() } catch {}
    db = undefined
  }

  return {
    start: startCompanion,
    stop: stopCompanion,
    runtime: rt,
    acp: sharedAcp,
    assistantWorkers,
    live,
    models,
  }
}
