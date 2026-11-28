import { randomUUID } from 'node:crypto'
import { stat } from 'node:fs/promises'
import { basename, isAbsolute, resolve } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import { createRuntime, interactionId, light, page, RpcError } from '../../runtime-sdk/index.js'
import { HermesAcp } from './acp.js'
import { identities, nativeID, personaID, profileHome } from './identity.js'
import { addItem, applyUpdate, flush, isUserText, reconcileReplay, restoreFailures, toolInput, toolName, transcript } from './items.js'

const RUNTIME = 'hermes'
const LABEL = 'Hermes Agent'
const CANCEL_TIMEOUT = 15_000
const now = () => new Date().toISOString()
const cancelled = { outcome: 'cancelled' }

function timestamp(value) {
  const ms = typeof value === 'string' && /^\d+(\.\d+)?$/.test(value) ? Number(value) * 1000 : Date.parse(value)
  return Number.isFinite(ms) && Math.abs(ms) <= 8.64e15 ? new Date(ms).toISOString() : undefined
}

function promptText(params, value, optional = false) {
  if (params.images?.length) throw new RpcError('Hermes sessions accept text messages only', 'unsupported')
  if (params.model || params.effort || (params.permission_mode && params.permission_mode !== 'default')) {
    throw new RpcError('Configure the model and approval mode in the Hermes profile', 'unsupported')
  }
  const text = typeof value === 'string' ? value.trim() : ''
  if (!text && !optional) throw new RpcError('A message is required', 'invalid_params')
  return text
}

export function createHermesRuntime({
  bin = 'hermes', env = process.env, socketPath, log = () => {},
  scanMs = 10_000, idleMs = 60_000, retryMs,
} = {}) {
  const executable = bin.includes('/') || bin.includes('\\') ? resolve(bin) : bin
  const home = profileHome(env)
  const profileEnv = { ...env, HERMES_HOME: home }
  const saved = identities(home, log)
  const sessions = new Map()
  const nativeSessions = new Map()
  const starts = new Map()
  const workers = new Set()
  let catalog
  let scanning
  let scanTimer
  let stopping
  let stopped = false

  const rt = createRuntime({ listSessions, listHistory, items, item, cursor, start, send, interrupt }, {
    runtime: RUNTIME, name: LABEL, launcher: RUNTIME,
    launchers: [{ name: RUNTIME, agent: RUNTIME, display_name: LABEL, prompt_optional: true }],
    socketPath, retryMs, log,
  })

  function worker(cwd, s) {
    const acp = new HermesAcp({
      bin: executable, cwd, env: s ? profileEnv : { ...profileEnv, HERMES_ACP_SKIP_CONFIGURED_MCP: '1' },
      onUpdate: (params) => {
        if (s?.acp === acp && params?.sessionId === s.raw && params.update) onUpdate(s, params.update)
      },
      onPermission: (params) => s?.acp === acp ? permission(s, params) : cancelled,
      onExit: () => {
        if (s?.acp === acp) {
          s.acp = undefined
          s.turn?.abort.abort()
          if (!s.turn && !s.persisted && sessions.has(s.id)) {
            s.status = 'offline'
            rt.sessionsChanged()
          }
        }
        if (catalog === acp) catalog = undefined
        workers.delete(acp)
      },
    })
    workers.add(acp)
    return acp
  }

  function state(raw, cwd, info = {}) {
    return {
      raw, cwd, title: info.title || basename(cwd) || LABEL,
      updated: timestamp(info.updatedAt) ?? now(), status: 'idle', history: randomUUID(), rev: 0,
      seen: new Set(), failures: [], startedHere: false, persisted: false, model: '',
    }
  }

  function row(s) {
    return {
      session_id: s.id, cwd: s.cwd, title: s.title, status: s.turn ? 'working' : s.status,
      updated_at: s.updated, history: s.history, rev: s.rev, last_text: s.content?.lastText,
      model: s.model, started_by_persona: s.startedHere, turn_error: s.turnError,
    }
  }

  function listSessions() {
    const all = [...sessions.values()].filter((s) => !s.starting)
    return rt.recent(all.map(row), { pinned: ({ session_id }) => {
      const s = sessions.get(session_id)
      return Boolean(s.turn || !s.persisted)
    } })
  }

  async function listHistory(params) {
    await scan()
    // Hermes caps its native catalog, so older sessions may be outside its listing window.
    return rt.history([...sessions.values()].filter((s) => !s.starting).map(row), params, { complete: false })
  }

  async function scan() {
    if (scanning) return scanning
    scanning = (async () => {
      if (stopped) return
      if (!catalog) {
        const acp = worker()
        catalog = acp
        try { await acp.initialize() } catch (error) { await acp.stop(); throw error }
      }
      const acp = catalog
      if (!acp || stopped) return
      const cursors = new Set()
      let cursor
      do {
        const result = await acp.call('session/list', cursor ? { cursor } : {})
        if (!Array.isArray(result?.sessions)) throw new Error('Hermes ACP returned an invalid session list')
        if (stopped) return
        for (const info of result.sessions) {
          if (typeof info.sessionId !== 'string' || !nativeID.test(info.sessionId) ||
              typeof info.cwd !== 'string' || !isAbsolute(info.cwd)) continue
          let s = nativeSessions.get(info.sessionId)
          if (!s) {
            s = state(info.sessionId, info.cwd, info)
            const id = saved.get(s.raw)
            s.startedHere = Boolean(id && !sessions.has(id))
            s.id = s.startedHere ? id : `${RUNTIME}-${s.raw}`
            if (sessions.has(s.id)) continue
            nativeSessions.set(s.raw, s)
            sessions.set(s.id, s)
          }
          s.persisted = true
          if (!s.turn) {
            s.title = info.title || s.title
            const updated = timestamp(info.updatedAt)
            if (updated && updated > s.updated) s.updated = updated
            scheduleIdle(s)
          }
        }
        cursor = result.nextCursor
        if (cursor && cursors.has(cursor)) throw new Error('Hermes ACP repeated a session list cursor')
        cursors.add(cursor)
      } while (cursor)
      rt.sessionsChanged()
    })().finally(() => { scanning = undefined })
    return scanning
  }

  async function requireSession(id) {
    let s = sessions.get(id)
    if (!s && typeof id === 'string' && id.startsWith(`${RUNTIME}-`)) {
      await scan()
      s = sessions.get(id)
    }
    if (!s || s.starting) throw new RpcError('Hermes session not found', 'not_found')
    return s
  }

  function scheduleIdle(s) {
    if (!s.acp || !s.persisted || s.turn || s.loading || s.idleTimer || stopped) return
    s.idleTimer = setTimeout(() => {
      s.idleTimer = undefined
      if (s.acp) discardWorker(s, s.acp)
    }, idleMs)
    s.idleTimer.unref()
  }

  function keepWorker(s) {
    clearTimeout(s.idleTimer)
    s.idleTimer = undefined
  }

  function discardWorker(s, acp) {
    if (s.acp === acp) s.acp = undefined
    void acp.stop().catch(() => log('Could not stop a retired Hermes worker'))
  }

  function loaded(s) {
    if (s.loading) return s.loading
    if (s.acp?.writable) return Promise.resolve(s.acp)
    if (s.acp) discardWorker(s, s.acp)
    s.loading = (async () => {
      if (stopped) throw new Error('Hermes runtime has stopped')
      if (!s.persisted) await scan()
      if (stopped) throw new Error('Hermes runtime has stopped')
      if (!s.persisted) throw new RpcError('Hermes has no saved transcript for this session', 'not_found')
      const acp = worker(s.cwd, s)
      s.acp = acp
      s.replay = transcript()
      try {
        await acp.initialize()
        const result = await acp.call('session/load', { sessionId: s.raw, cwd: s.cwd, mcpServers: [] })
        if (!result) throw new RpcError('Hermes could not load this session', 'not_found')
        flush(s.replay)
        s.model = result.models?.currentModelId ?? s.model
        const previous = s.content
        const next = reconcileReplay(previous, restoreFailures(s.replay, s.failures))
        if (next !== previous) {
          if (previous) s.history = randomUUID()
          s.content = next
          publish(s, 0)
        }
        return acp
      } catch (error) {
        discardWorker(s, acp)
        throw error
      } finally {
        s.replay = undefined
      }
    })().finally(() => {
      s.loading = undefined
      scheduleIdle(s)
    })
    return s.loading
  }

  async function content(s) {
    if (s.loading) await s.loading
    if (!s.content) await loaded(s)
    return s.content.items
  }

  async function items(params) {
    const s = await requireSession(params.session_id)
    const all = await content(s)
    return page(all, params, { history: s.history, toSeq: all.length })
  }

  async function item(params) {
    const s = await requireSession(params.session_id)
    const value = (await content(s)).find((i) => i.id === params.item_id)
    if (!value) throw new RpcError('Hermes item not found', 'not_found')
    return value
  }

  async function cursor(id) {
    const s = await requireSession(id)
    return { to_seq: (await content(s)).length, history: s.history }
  }

  function publish(s, fromSeq) {
    const toSeq = s.content.items.length
    s.rev++
    rt.append(s.id, {
      items: s.content.items.slice(fromSeq).map(light), fromSeq, toSeq, history: s.history,
    })
    rt.sessionsChanged()
  }

  function onUpdate(s, update) {
    if (update.sessionUpdate === 'session_info_update' && update.title) {
      s.title = update.title
      rt.sessionsChanged()
    }
    if (s.replay) {
      applyUpdate(s.replay, update, true)
      return
    }
    // Live content belongs to a prompt sent by this companion. An attached Hermes
    // gateway viewer can receive other clients' chunks without their prompt outcome.
    const turn = s.turn
    if (!turn?.dispatched || !s.content) return
    const before = s.content.items.length
    const previous = s.content.segment
    applyUpdate(s.content, update, false)
    const segment = s.content.segment
    if (previous?.kind === 'text' && segment?.kind !== 'text') {
      turn.livePrefix = ''
      rt.streamEnd(s.id)
    }
    if (segment?.kind === 'text' && update.sessionUpdate === 'agent_message_chunk') {
      if (previous?.kind === 'text' && previous !== segment) turn.livePrefix += previous.text
      rt.stream(s.id, turn.livePrefix + segment.text)
    }
    if (s.content.items.length !== before) publish(s, before)
  }

  async function permission(s, params) {
    const turn = s.turn
    const call = params?.toolCall
    const options = params?.options
    if (params?.sessionId !== s.raw || !turn || !call?.toolCallId || !Array.isArray(options)) return cancelled
    const response = await rt.ask({
      id: interactionId(RUNTIME, s.id, 'permission', `${turn.key}:${call.toolCallId}`), kind: 'permission', session_id: s.id,
      tool_name: toolName(call), tool_input: toolInput(call), summary: call.title,
    }, { signal: turn.abort.signal })
    if (turn.abort.signal.aborted || response?.dismiss) return cancelled
    const selected = options.find((option) => option.kind === (response?.allow ? 'allow_once' : 'reject_once'))
    return selected ? { outcome: 'selected', optionId: selected.optionId } : cancelled
  }

  async function createSession(params, text, markDriven) {
    if (stopped) throw new Error('Hermes runtime has stopped')
    if (typeof params.cwd !== 'string' || !isAbsolute(params.cwd) ||
        !(await stat(params.cwd).catch(() => null))?.isDirectory()) {
      throw new RpcError('An existing workspace directory is required', 'invalid_params')
    }
    if (stopped) throw new Error('Hermes runtime has stopped')
    // Bind handlers when session/new gives us the worker's native session ID.
    const s = state('', params.cwd)
    const acp = worker(params.cwd, s)
    s.acp = acp
    try {
      await acp.initialize()
      const result = await acp.call('session/new', { cwd: params.cwd, mcpServers: [] })
      if (typeof result?.sessionId !== 'string' || !nativeID.test(result.sessionId) || nativeSessions.has(result.sessionId)) {
        throw new Error('Hermes ACP returned an invalid or duplicate session ID')
      }
      s.raw = result.sessionId
      const proposed = params.session_id
      s.id = typeof proposed === 'string' && personaID.test(proposed) && !sessions.has(proposed)
        ? proposed : `${RUNTIME}-${s.raw}`
      if (sessions.has(s.id)) throw new Error('Hermes ACP returned a conflicting session ID')
      // Reserve the binding before the async write, but publish the row only after it is durable.
      s.starting = true
      nativeSessions.set(s.raw, s)
      sessions.set(s.id, s)
      await saved.save(s.raw, s.id)
      if (stopped || acp.closed) throw new Error('Hermes ACP disconnected during startup')
      s.content = transcript()
      s.model = result.models?.currentModelId ?? ''
      s.startedHere = true
      s.starting = false
      markDriven?.(s.id)
      if (text) await begin(s, text, params.client_msg_id)
      rt.sessionsChanged()
      return { session: row(s) }
    } catch (error) {
      if (sessions.get(s.id) === s) sessions.delete(s.id)
      if (nativeSessions.get(s.raw) === s) nativeSessions.delete(s.raw)
      await acp.stop()
      throw error
    }
  }

  async function start(params, markDriven) {
    if (params.resume || (params.launcher && params.launcher !== RUNTIME)) {
      throw new RpcError('Select a Hermes session or the Hermes launcher', 'unsupported')
    }
    const text = promptText(params, params.prompt, true)
    const key = params.session_id || params.client_msg_id
    if (!key) return createSession(params, text, markDriven)
    let request = starts.get(key)
    if (!request) {
      const previous = sessions.get(params.session_id)
      if (previous?.startedHere && !previous.starting) {
        if (previous.cwd !== params.cwd) throw new RpcError('The requested session ID is already in use', 'invalid_params')
        return { session: row(previous) }
      }
      request = createSession(params, text, markDriven).catch((error) => {
        starts.delete(key)
        throw error
      })
      starts.set(key, request)
      if (starts.size > 500) starts.delete(starts.keys().next().value)
    }
    return request
  }

  async function send(params) {
    const text = promptText(params, params.text)
    await begin(await requireSession(params.session_id), text, params.client_msg_id)
  }

  function seen(s, key) {
    return key && (s.seen.has(key) || s.content?.items.some((item) => item.client_msg_id === key))
  }

  async function begin(s, text, key) {
    if (seen(s, key)) return
    if (s.turn) throw new RpcError('Hermes is already processing a message', 'turn_running', { delivered: 'none' })
    keepWorker(s)
    let settle
    const turn = { key: key || randomUUID(), abort: new AbortController(), livePrefix: '',
      done: new Promise((resolve) => { settle = resolve }) }
    s.turn = turn
    s.turnError = undefined
    rt.sessionsChanged()
    const signal = turn.abort.signal
    const notDelivered = () => new RpcError('Hermes did not receive the message', 'not_delivered', { delivered: 'none' })
    let cancelWait
    const interrupted = new Promise((_, reject) => {
      cancelWait = () => reject(notDelivered())
      signal.addEventListener('abort', cancelWait, { once: true })
    })
    const release = () => {
      s.turn = undefined
      scheduleIdle(s)
      rt.sessionsChanged()
      settle()
    }
    try {
      turn.acp = await Promise.race([loaded(s), interrupted])
      if (signal.aborted || !turn.acp.writable || stopped) throw notDelivered()
      // Loading may have restored the ID from a previously failed turn.
      if (seen(s, key)) return release()
      if (key) s.seen.add(key)
      if (s.seen.size > 500) s.seen.delete(s.seen.values().next().value)
      const from = s.content.items.length
      turn.from = from
      addItem(s.content, { role: 'user', kind: 'text', text, ...(key ? { client_msg_id: key } : {}) })
      s.updated = now()
      publish(s, from)
      const prompt = turn.acp.call('session/prompt', { sessionId: s.raw, prompt: [{ type: 'text', text }] }, 0)
      turn.dispatched = true
      void finish(s, turn, prompt).then(settle, () => {
        log('Could not publish the final Hermes turn state')
        settle()
      })
    } catch (error) {
      release()
      throw error
    } finally {
      signal.removeEventListener('abort', cancelWait)
    }
  }

  async function finish(s, turn, prompt) {
    let stopReason
    try { stopReason = (await prompt)?.stopReason } catch {}
    turn.abort.abort()
    const from = s.content.items.length
    flush(s.content)
    s.content.activeTools.clear()
    // These transcript markers are localized by the hub.
    if (stopReason === 'cancelled') {
      addItem(s.content, { role: 'system', kind: 'notice', text: '已中断' })
    } else if (stopReason !== 'end_turn') {
      s.turnError = '出错了'
      addItem(s.content, { role: 'system', kind: 'notice', text: s.turnError })
      if (!turn.acp.writable) {
        // Retain the dispatched turn locally until a fresh native replay can tell
        // whether Hermes had saved its input before the disconnect.
        const localOnly = s.failures.filter((failure) => !failure.persisted).length
        const userIndex = s.content.items.slice(0, turn.from).filter(isUserText).length - localOnly
        s.failures.push({ userIndex, items: s.content.items.slice(turn.from) })
      }
    }
    s.turn = undefined
    s.status = stopReason ? 'done' : 'offline'
    s.updated = now()
    rt.streamEnd(s.id)
    publish(s, from)
    scheduleIdle(s)
  }

  async function interrupt({ session_id }) {
    const s = await requireSession(session_id)
    const turn = s.turn
    if (!turn) return { settled: true }
    turn.abort.abort()
    if (!turn.dispatched) {
      await turn.done
      return
    }
    turn.acp.notify('session/cancel', { sessionId: s.raw })
    const timeout = new AbortController()
    try {
      await Promise.race([
        turn.done,
        delay(CANCEL_TIMEOUT, undefined, { signal: timeout.signal, ref: false }).then(() => {
          throw new RpcError('Hermes has not confirmed that the turn ended', 'timeout')
        }),
      ])
      return { settled: true }
    } finally {
      timeout.abort()
    }
  }

  return {
    async start() {
      await saved.load()
      await scan()
      if (stopped) return
      rt.start()
      scanTimer = setInterval(() => {
        void scan().catch(() => log('Could not refresh Hermes ACP sessions'))
      }, scanMs)
      scanTimer.unref()
    },
    stop() {
      return stopping ??= (async () => {
        stopped = true
        clearInterval(scanTimer)
        rt.stop()
        for (const s of sessions.values()) {
          keepWorker(s)
          s.turn?.abort.abort()
        }
        await Promise.all([...workers].map((acp) => acp.stop()))
      })()
    },
  }
}
