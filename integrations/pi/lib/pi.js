import { assistantState, recordAssistant, assistantBeforeTurn } from './assistant.js'
import { extractShellGates, matchShellGate } from '../../runtime-sdk/lib/shell-gates.js'
import { wirePackageName } from '../../runtime-sdk/lib/compat.js'
import { customTypeMatches, legacyCustomType, LEGACY_RELOAD, piHostKind } from './compat.js'
// The pi runtime (docs/integrations/pi.md): the sessions of one pi process — a pi in a terminal, `pi --mode rpc`, or pi-web's
// server, which hosts many — in Persona through the runtime SDK (docs/integrations/runtimes.md). pi loads the extension once per
// session, but there is one runtime connection per process: index.js registers each session's API with the host made
// here, once per process.
//
// The same host, made with listDisk and no sessions of its own, is the pi-web companion (companion.mjs): pi-web loads
// extensions only when a session opens, so a small resident process lists the recent sessions on disk and starts or
// wakes them through pi-web's HTTP API, while the extension inside pi-web serves the ones running there. When both list
// a session the node takes the one running it. Everything pi-specific comes in through `deps`, so the tests drive it
// with fakes.

import { basename } from 'node:path'
import { readFileSync, statSync } from 'node:fs'
import { createRuntime, installCurrent, interactionId, light, page, RpcError, snippet, summaryOf, verifyPackage } from '../../runtime-sdk/index.js'
import { a2aTools } from '../../runtime-sdk/lib/a2a.js'
import { ALLOW_ENTRY, alwaysAllowed, branchItems, branchKey, callName, entryItems, firstText, HOST_ENTRY, imageOf, lastHost, lastText, partialText, toolInput, toolName } from './map.js'

export const RUNTIME_VERSION = '0.4.0'
const DISK_EVERY = 30_000 // ms: list the sessions on disk at most this often (pi-web)
const TERMINAL_HOSTS = new Set(['herdr', 'terminal']) // resumed in the machine's terminal engine, not in pi-web
const RELOAD_COMMAND = 'persona-reload' // the command a session runs on itself to load a new version (docs/integrations/pi.md "Automatic updates")
const IDLE_EVERY = 5000 // ms: how often to look whether the process is idle enough to switch versions
const INTERRUPT_SETTLE = 10_000 // ms: how long an interrupt waits for the turn to end before it reports failure
const BROWSERS_EVERY = 10_000 // ms: how often pi-web is asked which sessions a browser has open

/**
 * @typedef {object} PiDeps
 * @property {{ listAll(): Promise<object[]>, open(path: string): object }} SessionManager - pi's (sessions on disk).
 * @property {{ port: number, host: string, password?: string } | undefined} web - pi-web's HTTP API (inside its
 *   server, or the companion's).
 * @property {(url: string, init: object) => Promise<Response>} [fetch]
 * @property {(msg: string) => void} [log]
 * @property {(path: string) => import('node:net').Socket} [connect] - tests.
 * @property {number} [pid]
 * @property {string} [paneId] - the terminal pane this pi runs in (HERDR_PANE_ID of a pi in herdr; not pi-web's).
 */

/**
 * @param {PiDeps} deps
 * @param {{ socketPath?: string, answerFirst?: 'auto'|'phone'|'local', approve?: string[], listDays?: number,
 *   maxSessions?: number, retryMs?: number, pushDelayMs?: number, listDisk?: boolean, runtime?: string, interruptSettleMs?: number,
 *   agent?: string, name?: string, instancePrefix?: string, interactionPrefix?: string, resumeCommand?: string[],
 *   instance?: string, update?: { name: string, hash: string, store: string, exit?: (hash: string) => void } }} [options]
 *   listDisk: list the recent sessions on disk too, and start new ones (the companion; needs web). update: this code
 *   runs from a version in a store (selfPackage), so it takes the node's version (docs/integrations/pi.md "Automatic updates"): installs it
 *   as the store's current, then — exit given (the companion) — calls exit for its supervisor to start the new code,
 *   or reloads this process's sessions onto it once they are all idle and no card of theirs is on the phone.
 *   instance: this process's identity on the node, kept by the next version's host in the same process.
 *   agent: the agent this host serves (pi; omp for the pi fork of the same extension API). name: the agent's name in
 *   the apps. instancePrefix: the `<prefix>-<pid>` the runtime and, when instance is not given, the instance take.
 *   interactionPrefix: what a card's id starts with. resumeCommand: how its terminal resumes a session on disk.
 *   settleEvents: which events end a turn for this agent (pi's agent_settled, which also covers its automatic retry,
 *   compaction and queued continuation; omp's pi compatibility layer dispatches only the loop's agent_end).
 *   interruptSettleMs: how long an interrupt waits for the turn to end (tests).
 */
export function createPiHost(deps, options = {}) {
  const web = deps.web
  const agent = options.agent ?? 'pi'
  const settleEvents = options.settleEvents ?? ['agent_settled']
  const instancePrefix = options.instancePrefix ?? 'pi'
  const interactionPrefix = options.interactionPrefix ?? 'pi'
  // --approve: the resume runs in a herdr tab nobody watches, where pi's project Trust prompt would hold it (#9462).
  const resumeCommand = options.resumeCommand ?? ['pi', '--approve', '--session']
  const pid = deps.pid ?? process.pid
  const listDisk = options.listDisk === true && web !== undefined
  const log = deps.log ?? (() => {})
  const doFetch = deps.fetch ?? globalThis.fetch
  const listDays = options.listDays ?? 7
  const maxSessions = options.maxSessions ?? 80
  const approve = new Set(options.approve ?? [])
  const live = new Map() // session id -> { id, pi, ctx, running, pushed, key }
  let disk = { at: 0, rows: [], paths: new Map() }
  const hosts = new Map() // session file -> { mtime, host }: where each session on disk last ran
  let started = false
  const update = options.update?.store ? { ...options.update, name: wirePackageName(options.update.name) } : undefined
  let want = '' // a version installed for this process to switch to
  const bad = new Set() // versions that did not take here (the reload brought the same code back)
  let installing = false
  let retired = false // stopped to hand this process over to a new version
  let idleTimer
  let updateRetry
  let updateRetryHash = ''
  let updateOfferHash = ''
  const settleMs = options.interruptSettleMs ?? INTERRUPT_SETTLE
  // pi-web: the sessions a browser has open (its event streams), as its debug snapshot said last; undefined: not known.
  let browsers = { at: 0, open: undefined, asking: undefined }

  const rt = createRuntime({
    listSessions,
    items,
    cursor: async (id) => view(id).then((v) => ({ to_seq: v.toSeq, history: v.history })),
    image,
    item,
    send,
    interrupt,
    close,
    ...(web ? { start } : {}),
    rename,
    hasLocalClient,
  }, {
    runtime: options.runtime ?? (listDisk ? 'pi-files' : web ? 'pi-web' : `${instancePrefix}-${pid}`),
    name: options.name ?? (web ? 'Pi Web' : 'pi'),
    agent,
    version: RUNTIME_VERSION,
    assistant: Boolean(web),
    supervisionPolicy: Boolean(web),
    ...(listDisk ? {
      launcher: 'pi-web', launcherModel: agent,
      // pi keeps its own model: the display label is not a model argument, so no model/effort is sent.
      launchers: [{ name: 'pi-web', agent, model: agent, models: [{ id: 'default', name: agent, default: true }], permission_modes: ['default', 'bypassPermissions'] }],
    } : {}),
    socketPath: options.socketPath,
    answerFirst: options.answerFirst ?? 'auto',
    log,
    connect: deps.connect,
    instance: options.instance ?? (options.instancePrefix ? `${instancePrefix}-${pid}` : undefined),
    ...(update ? { package: { name: update.name, hash: update.hash }, onHello: helloUpdate, onUpdate: (offer) => void takeUpdate(offer) } : {}),
    retryMs: options.retryMs,
    pushDelayMs: options.pushDelayMs,
    // Listing sessions only on disk is safe only where the runtime running a session answers for it.
    ...(listDisk ? { requires: ['prefer_running'] } : {}),
  })

  // ---- the sessions ----

  /** A session's entries, branch and seq bookkeeping: the live one's, or from its file (pi-web). */
  async function view(id) {
    const s = live.get(id)
    let sm = s?.ctx?.sessionManager
    if (!sm) {
      const path = await diskPath(id)
      if (!path) throw new RpcError(`这个 pi 会话不在这里（${id}）`, 'not_found')
      sm = deps.SessionManager.open(path)
    }
    const entries = sm.getEntries()
    const branch = sm.getBranch()
    return { sm, entries, branch, toSeq: entries.length, history: `pi:${id}:${branchKey(entries, branch)}` }
  }

  async function diskPath(id) {
    if (!listDisk) return undefined
    if (!disk.paths.has(id)) await refreshDisk(true)
    return disk.paths.get(id)
  }

  async function refreshDisk(force = false) {
    if (!listDisk || (!force && Date.now() - disk.at < DISK_EVERY)) return
    try {
      const all = await deps.SessionManager.listAll()
      disk = { at: Date.now(), rows: all, paths: new Map(all.map((s) => [s.id, s.path])) }
    } catch (err) {
      log(`persona: list pi sessions: ${err.message}`)
    }
  }

  function liveRow(s) {
    const sm = s.ctx.sessionManager
    const branch = sm.getBranch()
    const cwd = sm.getCwd()
    const name = sm.getSessionName?.() ?? ''
    const asst = assistantState(sm)
    const row = {
      ...(asst ? { assistant: true, permission_mode: asst.mode } : {}),
      session_id: s.id,
      title: name || firstText(branch) || basename(cwd),
      cwd,
      status: s.running ? 'working' : branch.some((e) => e.message?.role === 'assistant') ? 'done' : 'idle',
      updated_at: new Date(s.updatedAt ?? Date.now()).toISOString(),
      rev: sm.getEntries().length + (s.moves ?? 0), // a move to another branch changes it too
      space_id: cwd,
      space_name: basename(cwd),
    }
    const lt = lastText(branch)
    if (lt) row.last_text = lt
    const model = s.ctx.model?.id
    if (model) row.model = model
    if (deps.paneId && s.ctx.mode === 'tui') row.pane_id = deps.paneId // the node joins it with herdr's pane
    return row
  }

  /** Where the session in file last ran (its last host entry), read once per change of the file. */
  function hostOfFile(path) {
    try {
      const mtime = statSync(path).mtimeMs
      const c = hosts.get(path)
      if (c?.mtime === mtime) return c.host
      let host
      for (const line of readFileSync(path, 'utf8').split('\n')) {
        if (![HOST_ENTRY, legacyCustomType(HOST_ENTRY)].some((type) => line.includes(`"${type}"`))) continue
        try {
          const e = JSON.parse(line)
          if (e.type === 'custom' && customTypeMatches(e.customType, HOST_ENTRY) && typeof e.data?.host === 'string') host = e.data.host
        } catch {}
      }
      hosts.set(path, { mtime, host })
      if (hosts.size > 500) hosts.delete(hosts.keys().next().value)
      return host
    } catch {
      return undefined
    }
  }

  async function listSessions() {
    const rows = [...live.values()].filter((s) => s.ctx && !assistantState(s.ctx.sessionManager)?.closed).map(liveRow)
    if (listDisk) {
      await refreshDisk()
      // The running ones always; on disk, those the phone touched and the recent ones (the SDK's rule).
      const off = rt.recent(disk.rows.filter((d) => !live.has(d.id)), { days: listDays, max: Math.max(0, maxSessions - rows.length),
        id: (d) => d.id, updatedAt: (d) => new Date(d.modified).getTime() })
      for (const d of off) {
        const host = hostOfFile(d.path)
        const resume = TERMINAL_HOSTS.has(host) && rt.nodeHas('resume_argv')
        rows.push({
          session_id: d.id,
          title: d.name || snippet(d.firstMessage ?? '') || basename(d.cwd || ''),
          cwd: d.cwd ?? '',
          status: 'offline', // on disk: pi-web opens it when the phone sends to it
          updated_at: new Date(d.modified).toISOString(),
          rev: new Date(d.modified).getTime(),
          space_id: d.cwd ?? '',
          space_name: basename(d.cwd || ''),
          // It ran in a terminal (herdr): the node resumes it there (`pi --session`), not here in pi-web.
          ...(resume ? { resume_argv: [...resumeCommand, d.path] } : {}),
        })
      }
    }
    return rows
  }

  async function items(params) {
    const v = await view(params.session_id)
    return page(branchItems(v.entries, v.branch), params, { history: v.history, toSeq: v.toSeq })
  }

  function splitItemID(itemID) {
    const [seqText, idxText] = String(itemID).split('#')
    const seq = Number(seqText)
    const idx = Number(idxText)
    if (!Number.isSafeInteger(seq) || !Number.isSafeInteger(idx) || seq < 1) throw new RpcError(`bad item id ${itemID}`, 'bad_request')
    return { seq, idx }
  }

  async function image(params) {
    const { seq, idx } = splitItemID(params.item_id)
    const v = await view(params.session_id)
    const b = imageOf(v.entries[seq - 1], idx)
    if (!b) throw new RpcError(`no image ${params.item_id}`, 'not_found')
    return { mime: b.mimeType ?? 'image/png', data: b.data }
  }

  async function item(params) {
    const { seq } = splitItemID(params.item_id)
    const v = await view(params.session_id)
    const it = entryItems(v.entries[seq - 1], seq).find((x) => x.id === params.item_id)
    if (!it) throw new RpcError(`no item ${params.item_id}`, 'not_found')
    return it
  }

  function content(params) {
    const images = (params.images ?? []).filter((i) => i?.data).map((i) => ({ type: 'image', data: i.data, mimeType: i.mime || 'image/jpeg' }))
    const text = typeof params.text === 'string' ? params.text : ''
    if (!text.trim() && images.length === 0) throw new RpcError('消息是空的', 'bad_request')
    return { text, images }
  }

  async function send(params) {
    const id = params.session_id
    const { text, images } = content(params) // once per client_msg_id: the SDK's
    const s = live.get(id)
    if (s) {
      const previous = assistantState(s.ctx.sessionManager)
      if (previous?.closed) throw new RpcError('This execution session is retired', 'conversation_retired')
      if (s.running && (params.assistant === true || previous)) {
        const policy = typeof params.supervision_instructions === 'string' ? params.supervision_instructions : previous?.policy || ''
        const mode = params.permission_mode || params.mode || previous?.mode || 'default'
        // Steering cannot refresh context; only an unchanged-role answer may join this turn.
        if (!s.asking || policy !== (previous?.policy || '') || mode !== (previous?.mode || 'default')) {
          throw new RpcError('This conversation is busy', 'busy', { delivered: 'none' })
        }
      }
      recordAssistant(s.ctx.sessionManager, params, previous)
      // The turn waits on the user's answer (ask_user): this message is the answer. Steered, it would wait behind the
      // question for as long as nobody answers it.
      if (s.asking) {
        s.asking.answer(text, images)
        return undefined
      }
      const body = images.length ? [...(text ? [{ type: 'text', text }] : []), ...images] : text
      // Into the running turn while it works (pi's steer: read after the step under way), a new turn when idle.
      if (s.running) {
        await s.pi.sendUserMessage(body, { deliverAs: 'steer' })
        return { queued: true }
      }
      await s.pi.sendUserMessage(body)
      return undefined
    }
    const path = listDisk ? await diskPath(id) : undefined
    if (!path) throw new RpcError('这个 pi 会话没有在这里运行，这次没有发送', 'not_running')
    if (TERMINAL_HOSTS.has(hostOfFile(path))) {
      // A session that ran in a terminal goes back to one (the node resumes it in herdr); this node cannot.
      throw new RpcError('这个 pi 会话是在终端（herdr）里跑的，要在 herdr 里接着它：更新这台电脑上的 personad 后再发。这次没有发送', 'not_running')
    }
    const sm = deps.SessionManager.open(path)
    if (assistantState(sm)?.closed) throw new RpcError('This execution session is retired', 'conversation_retired')
    recordAssistant(sm, params)
    // On disk: pi-web opens it and prompts (its extension load registers it here as it starts).
    await webCall(`/api/agent/${encodeURIComponent(id)}`, { type: 'prompt', message: text, ...(images.length ? { images } : {}) })
  }

  /**
   * Stops the session's turn and waits for it to end (agent_settled): {settled: true}, or an error when it did not end
   * within settleMs. Inside pi-web its own Stop does it (it also cancels the dialogs extensions have open, and takes
   * back what was queued for the turn); elsewhere pi's abort. A question of ask_user gives up either way.
   */
  async function interrupt(params) {
    const id = params.session_id
    const s = live.get(id)
    if (!s) return undefined
    if (!s.running) return { settled: true }
    const ended = settled(s, settleMs)
    let stopped = false
    if (web && !listDisk) {
      try {
        await webCall(`/api/agent/${encodeURIComponent(id)}`, { type: 'abort' })
        stopped = true
      } catch (err) {
        log(`persona: pi-web abort ${id}: ${err.message}`)
      }
    }
    if (!stopped) s.ctx.abort()
    s.asking?.withdraw()
    if (!(await ended)) throw new RpcError(`pi 的这一轮在 ${Math.round(settleMs / 1000)} 秒内没有停下来`, 'interrupt_timeout')
    return { settled: true }
  }

  /** Resolves true once the session's turn has ended (or the session closed), false after ms. */
  function settled(s, ms) {
    if (!s.running) return Promise.resolve(true)
    return new Promise((resolve) => {
      const done = (ok) => {
        clearTimeout(timer)
        s.settleWaiters?.delete(done)
        resolve(ok)
      }
      const timer = setTimeout(() => done(false), ms)
      timer.unref?.()
      ;(s.settleWaiters ??= new Set()).add(done)
    })
  }

  function wakeSettled(s) {
    for (const done of [...(s?.settleWaiters ?? [])]) done(true)
  }

  /**
   * Whether the session's own UI is open: a terminal pi's dialog (tui; rpc: the program driving it), or in pi-web a
   * browser that has the session open — not known until pi-web said, which counts as open (asked again meanwhile).
   */
  function hasLocalClient(sessionId) {
    const s = live.get(sessionId)
    if (!s?.ctx?.hasUI) return false
    if (!web || listDisk) return true
    void refreshBrowsers()
    return browsers.open ? browsers.open.has(sessionId) : true
  }

  /** pi-web's debug snapshot (read-only): which sessions have an event stream, at most every BROWSERS_EVERY. */
  function refreshBrowsers(force = false) {
    if (browsers.asking) return browsers.asking
    if (!force && Date.now() - browsers.at < BROWSERS_EVERY) return Promise.resolve()
    browsers.at = Date.now()
    const headers = {}
    if (web.password) headers.authorization = 'Basic ' + Buffer.from(`pi:${web.password}`).toString('base64')
    browsers.asking = (async () => {
      try {
        const res = await doFetch(`http://${web.host}:${web.port}/api/agent/debug`, { method: 'GET', headers, signal: AbortSignal.timeout(5000) })
        const data = res.ok ? await res.json() : undefined
        const rows = Array.isArray(data?.sessions) ? data.sessions : undefined
        browsers.open = rows ? new Set(rows.filter((r) => typeof r?.sessionId === 'string' && r.eventStreams > 0).map((r) => r.sessionId)) : undefined
      } catch {
        browsers.open = undefined // an older pi-web, or none: as before, the browser counts as open
      } finally {
        browsers.asking = undefined
      }
    })()
    return browsers.asking
  }

  async function rename(params) {
    const s = live.get(params.session_id)
    if (!s) throw new RpcError('这个 pi 会话没有在运行，改不了名', 'not_running')
    s.pi.setSessionName(params.title)
  }

  async function start(params) {
    if (!params.cwd) throw new RpcError('要选一个目录', 'bad_request')
    // No prompt is released until assistant state has been durably recorded.
    if (params.assistant === true && (params.model || params.effort || !['', 'default', 'bypassPermissions'].includes(params.permission_mode || ''))) throw new RpcError('This pi configuration is unsupported', 'readonly_unsupported')
    const prompt = typeof params.prompt === 'string' ? params.prompt.trim() : ''
    const res = await webCall('/api/agent/new', prompt && params.assistant !== true ? { cwd: params.cwd, type: 'prompt', message: prompt } : { cwd: params.cwd, type: 'ensure_session' })
    const id = res?.sessionId
    if (!id) throw new RpcError('pi-web 没有返回会话', 'start_failed')
    if (params.assistant === true) {
      await refreshDisk(true)
      const path = await diskPath(id)
      const running = live.get(id)
      // No prompt went to pi-web before recording the assistant: the switch rolls back.
      if (!running && !path) throw new RpcError('The prepared pi session is not yet available', 'assistant_supervision_runtime', { delivered: 'none' })
      recordAssistant(running?.ctx.sessionManager || deps.SessionManager.open(path), params)
      if (prompt) await send({ ...params, session_id: id, text: prompt })
    }
    // offline: it runs in pi-web, not here (the extension there lists it as running; the node takes that row).
    return { session: { session_id: id, cwd: params.cwd, title: basename(params.cwd), status: 'offline',
      space_id: params.cwd, space_name: basename(params.cwd), updated_at: new Date().toISOString() } }
  }

  async function close(params) {
    await interrupt(params)
    const s = live.get(params.session_id)
    const path = await diskPath(params.session_id)
    const sm = s?.ctx.sessionManager || (path ? deps.SessionManager.open(path) : undefined)
    if (sm && assistantState(sm)) recordAssistant(sm, { assistant: true, closed: true })
    rt.sessionsChanged(0)
  }

  async function webCall(path, body) {
    const headers = { 'content-type': 'application/json' }
    if (web.password) headers.authorization = 'Basic ' + Buffer.from(`pi:${web.password}`).toString('base64')
    const res = await doFetch(`http://${web.host}:${web.port}${path}`, { method: 'POST', headers, body: JSON.stringify(body), signal: AbortSignal.timeout(30_000) })
    const data = await res.json().catch(() => ({}))
    if (!res.ok || data?.success === false || data?.error) {
      throw new RpcError(`pi-web：${data?.error ?? res.status}`, data?.code === 'prompt_rejected' ? 'rejected' : 'pi_web')
    }
    return data
  }

  // ---- what happens in a session ----

  /** Push what a session wrote since the last push (only the phone's watched sessions get the items). */
  function commit(id) {
    const s = live.get(id)
    const sm = s?.ctx?.sessionManager
    if (!sm) return
    const entries = sm.getEntries()
    const branch = sm.getBranch()
    const key = branchKey(entries, branch)
    if (entries.length === s.pushed && key === s.key) return
    // Back on a branch already in the file (/tree): nothing was written, but the conversation is another one.
    if (entries.length === s.pushed) s.moves = (s.moves ?? 0) + 1
    const fresh = rt.watching(id) ? branchItems(entries, branch).filter((i) => i.seq > s.pushed).map(light) : [] // cut as pages are
    rt.append(id, { items: fresh, fromSeq: s.key === key ? s.pushed : undefined, toSeq: entries.length, history: `pi:${id}:${key}` })
    s.pushed = entries.length
    s.key = key
    s.updatedAt = Date.now()
  }

  /** pi's API for one session (the extension's factory argument): its events, tools and approvals. */
  function register(pi, toolDeps) {
    const assistantTools = new Set()
    let current // this API's session id (a /new in the terminal moves it to another)
    // The session's own way to load a new version: pi gives an extension reload() only in a command's context, so it
    // runs this command on itself (sendUserMessage with commands on). Only where the command exists: anything else
    // would go to the model as a message.
    let commandReady = false
    if (typeof pi.registerCommand === 'function' && typeof pi.sendUserMessage === 'function') {
      try {
        for (const name of [RELOAD_COMMAND, LEGACY_RELOAD]) pi.registerCommand(name, {
          description: 'Persona: load the new version of its extension (run by the extension itself)',
          handler: async (_args, ctx) => ctx.reload(),
        })
        commandReady = true
      } catch (err) {
        log(`persona: ${RELOAD_COMMAND}: ${err.message}`)
      }
    }
    const track = (ctx) => {
      const id = ctx.sessionManager.getSessionId()
      if (current && current !== id) drop(current)
      current = id
      let s = live.get(id)
      if (!s) {
        const entries = ctx.sessionManager.getEntries()
        s = { id, pi, ctx, running: false, pushed: entries.length, key: branchKey(entries, ctx.sessionManager.getBranch()), updatedAt: Date.now() }
        live.set(id, s)
        rt.sessionsChanged()
      }
      s.ctx = ctx
      s.pi = pi
      s.reloadable = commandReady
      return s
    }
    const drop = (id) => {
      wakeSettled(live.get(id))
      if (live.get(id)?.pi === pi) live.delete(id)
      rt.streamEnd(id)
      rt.sessionsChanged()
    }
    pi.on('session_start', (_e, ctx) => {
      if (!started) {
        started = true
        rt.start()
      }
      track(ctx)
      stampHost(pi, ctx)
      if (want) setTimeout(maybeReload, 0) // a session that opened on the old code (pi cached it): switch it too
    })
    pi.on('session_shutdown', (_e, ctx) => drop(ctx?.sessionManager?.getSessionId() ?? current))
    pi.on('before_agent_start', (event, ctx) => assistantBeforeTurn(rt, pi, ctx, event, assistantTools))
    pi.on('agent_start', (_e, ctx) => {
      track(ctx).running = true
      rt.sessionsChanged(0)
    })
    // settleEvents: pi's agent_settled, or the agent's own word for a turn that is over (omp's agent_end; the events
    // of an agent that dispatches more than one of them settle on the first, as the same turn's later ones change
    // nothing).
    for (const event of settleEvents) pi.on(event, (_e, ctx) => {
      const s = track(ctx)
      s.running = false
      wakeSettled(s)
      commit(s.id)
      rt.streamEnd(s.id)
      rt.sessionsChanged(0)
      if (want) setTimeout(maybeReload, 0)
    })
    pi.on('message_update', (e, ctx) => {
      const s = track(ctx)
      if (e?.assistantMessageEvent?.type === 'text_delta') rt.stream(s.id, partialText(e.assistantMessageEvent.partial ?? e.message))
    })
    for (const event of ['message_end', 'tool_execution_end', 'session_compact', 'session_tree']) {
      pi.on(event, (_e, ctx) => {
        const s = track(ctx)
        if (event === 'message_end') rt.streamEnd(s.id)
        commit(s.id)
      })
    }
    pi.on('session_info_changed', (_e, ctx) => {
      track(ctx)
      rt.sessionsChanged()
    })
    pi.on('tool_call', (e, ctx) => approval(track(ctx), e, ctx))
    if (toolDeps?.Type) pi.registerTool(askUserTool(toolDeps.Type, track))
    if (toolDeps?.A2AType || toolDeps?.Type) {
      for (const tool of piA2ATools(toolDeps.A2AType ?? toolDeps.Type, track)) pi.registerTool(tool)
    }
  }

  function piA2APropertySchema(Type, property) {
    if (property.type === 'boolean') return Type.Boolean()
    if (property.type === 'integer' || property.type === 'number') {
      const opts = {}
      if (property.minimum !== undefined) opts.minimum = property.minimum
      if (property.maximum !== undefined) opts.maximum = property.maximum
      const arg = Object.keys(opts).length > 0 ? opts : undefined
      const builder = property.type === 'integer' ? Type.Integer : Type.Number
      if (typeof builder === 'function') return builder.call(Type, arg)
      return typeof Type.Unsafe === 'function' ? Type.Unsafe({ ...property }) : { ...property }
    }
    return Type.String(property.enum ? { enum: property.enum } : undefined)
  }

  function piA2ATools(Type, track) {
    return a2aTools.map(({ name, description, input }) => ({
      name,
      label: name,
      description,
      parameters: Type.Object(Object.fromEntries(Object.entries(input.properties).map(([key, property]) => {
        const schema = piA2APropertySchema(Type, property)
        return [key, input.required.includes(key) ? schema : Type.Optional(schema)]
      }))),
      executionMode: 'sequential',
      async execute(_id, args, signal, _onUpdate, ctx) {
        const session = track(ctx)
        const res = await rt.callTool({ name, args, session_id: session.id }, { signal })
        return { content: res.content ?? [], details: { error: res.is_error === true } }
      },
    }))
  }

  /** Records where the session runs now when that changed (HOST_ENTRY): pi-web, herdr, or a terminal outside it. */
  function stampHost(pi, ctx) {
    const host = web ? 'pi-web' : ctx.mode === 'tui' ? (deps.paneId ? 'herdr' : 'terminal') : undefined
    if (!host || typeof pi.appendEntry !== 'function') return // RPC and one-shot runs: some program's, not a place
    if (lastHost(ctx.sessionManager.getEntries()) !== host) {
      pi.appendEntry(legacyCustomType(HOST_ENTRY), { host })
      pi.appendEntry(HOST_ENTRY, { host })
    }
  }

  /**
   * A tool the user asked to approve (options.approve): the phone or pi's own dialog decides, whichever goes first
   * (answerFirst), the phone also when pi has no dialog (pi -p, JSON mode); without either, no.
   */
  async function approval(s, e, ctx) {
    const name = e.toolName ?? e.name
    const asst = assistantState(ctx.sessionManager)
    if (asst?.closed) return { block: true, reason: 'This execution session is retired' }
    // Persona actions retain their dedicated approval/scope path even when
    // ordinary native tools use bypassPermissions. Failed rule reads stay closed.
    if (asst && ['bash', 'shell'].includes(String(name).toLowerCase())) {
      const input = e.input ?? e.args ?? {}
      const command = input.command ?? input.cmd
      if (typeof command !== 'string' || !command.trim()) return { block: true, reason: 'The shell command could not be checked. Nothing ran.' }
      try {
        if (!rt.hubConnected) throw new Error('rules unavailable')
        const tools = await rt.tools({ assistant: true, sessionId: s.id, signal: ctx.signal })
        if (!rt.hubConnected) throw new Error('rules unavailable')
        if (matchShellGate(command, extractShellGates(tools))) return { block: true, reason: 'Use the Persona action tool instead of running this command directly.' }
      } catch {
        return { block: true, reason: 'Persona command rules are unavailable. Nothing ran.' }
      }
    }
    if (asst?.mode === 'bypassPermissions') return undefined
    if (!asst && !approve.has(name)) return undefined
    const entries = ctx.sessionManager.getEntries()
    if (alwaysAllowed(entries, name)) return undefined // "Always allow" on an earlier card of this session
    const input = e.input ?? e.args ?? {}
    const summary = summaryOf(name, input)
    /** The phone's answer: { out } when it answered (out undefined = allowed), undefined when nobody did there. */
    const askPhone = async () => {
      const res = await rt.ask({
        id: interactionId(interactionPrefix, s.id, 'permission', e.toolCallId), kind: 'permission', session_id: s.id,
        tool_name: callName(name, input), tool_use_id: e.toolCallId, tool_input: toolInput(name, input), summary,
        // Offered only where it can be kept: the grant is written into the session (ALLOW_ENTRY).
        ...(typeof s.pi.appendEntry === 'function' ? { always_label: `本会话不再问 ${toolName(name)}` } : {}),
      }, { signal: ctx.signal })
      if (res?.allow === true) {
        if (res.always === true && typeof s.pi.appendEntry === 'function' && !alwaysAllowed(ctx.sessionManager.getEntries(), name)) {
          s.pi.appendEntry(legacyCustomType(ALLOW_ENTRY), { tool: name })
          s.pi.appendEntry(ALLOW_ENTRY, { tool: name })
        }
        return { out: undefined }
      }
      if (res?.allow === false) return { out: { block: true, reason: res.message?.trim() || '用户在手机上拒绝了' } }
      return undefined
    }
    if (web && !listDisk && ctx.hasUI) await refreshBrowsers() // whether a browser has it open decides who goes first
    const phoneFirst = rt.phoneFirst(s.id)
    if (phoneFirst) {
      const answered = await askPhone()
      if (answered) return answered.out
    }
    if (ctx.hasUI) {
      // Listed as needing you while the dialog waits, and taken off when the turn is stopped.
      const release = rt.waitingOnUser(s.id)
      let ok
      try {
        ok = await ctx.ui.confirm(`允许 ${name}？`, summary, { signal: ctx.signal })
      } finally {
        release()
      }
      return ok ? undefined : { block: true, reason: '用户拒绝了' }
    }
    if (!phoneFirst && rt.hubConnected) {
      const answered = await askPhone() // no dialog here: the phone's turn
      if (answered) return answered.out
    }
    return { block: true, reason: '没有人批准（没有界面，手机也没回答）' }
  }

  // ---- the node's version (docs/integrations/pi.md "Automatic updates") ----

  /** Every session of this process is between turns and no card of this runtime waits on the phone. */
  function idle() {
    return ![...live.values()].some((s) => s.running) && !rt.busy()
  }

  function helloUpdate(res) {
    const offer = res?.update
    updateOfferHash = offer?.name === update.name && typeof offer.hash === 'string' && offer.hash !== update.hash ? offer.hash : ''
    clearUpdateRetry()
  }

  function currentUpdate(offer) {
    return !retired && started && rt.ready && offer?.hash === updateOfferHash
  }

  function clearUpdateRetry() {
    clearTimeout(updateRetry)
    updateRetry = undefined
    updateRetryHash = ''
  }

  function retryUpdate(offer, pkg) {
    if (!currentUpdate(offer)) return
    clearTimeout(updateRetry)
    updateRetryHash = offer.hash
    updateRetry = setTimeout(() => takeUpdate(offer, pkg), options.retryMs ?? 1000)
    updateRetry.unref?.()
  }

  async function takeUpdate(offer, pkg) {
    if (offer?.hash !== updateRetryHash) clearUpdateRetry()
    if (!currentUpdate(offer) || offer.name !== update.name || offer.hash === update.hash || bad.has(offer.hash)) return
    if (installing) {
      retryUpdate(offer, pkg)
      return
    }
    installing = true
    try {
      if (!pkg) {
        let fetched
        try {
          fetched = await rt.fetchPackage(update.name)
          if (!currentUpdate(offer)) return
        } catch (err) {
          if (currentUpdate(offer)) {
            log(`persona: ${update.name} package fetch failed: ${err.message}; retrying`)
            retryUpdate(offer)
          }
          return
        }
        pkg = verifyPackage(fetched, update.name)
      }
      const { hash, files } = pkg
      if (!currentUpdate(offer) || hash !== offer.hash || hash === update.hash || bad.has(hash)) return
      installCurrent(update.store, hash, files, { keep: [update.hash] })
      log(`persona: ${update.name} ${hash.slice(0, 12)} from the node is installed (running ${update.hash.slice(0, 12)})`)
      clearUpdateRetry()
      if (update.exit) {
        rt.stop()
        update.exit(hash)
        return
      }
      want = hash
      clearInterval(idleTimer)
      idleTimer = setInterval(maybeReload, IDLE_EVERY)
      idleTimer.unref?.()
      maybeReload()
    } catch (err) {
      if (!currentUpdate(offer)) return
      if (err.code === 'ERR_RUNTIME_STORE_BUSY') {
        retryUpdate(offer, pkg)
        log(`persona: ${update.name} update store is busy; retrying ${offer.hash.slice(0, 12)}`)
      } else {
        bad.add(offer.hash)
        log(`persona: ${update.name} update to ${offer.hash.slice(0, 12)} failed: ${err.message}`)
      }
    } finally {
      installing = false
    }
  }

  /**
   * Hand the process over to the installed version once it is idle: this connection goes (the next version's host
   * connects as the same process), and every session runs the reload command on itself. A process with no session
   * open waits for one (pi-web opens the next on the code it cached, which then switches).
   */
  function maybeReload() {
    if (!want || retired || live.size === 0 || !idle()) return
    const sessions = [...live.values()]
    if (!sessions.every((s) => s.reloadable)) {
      log(`persona: ${update.name} ${want.slice(0, 12)} is installed; this pi cannot reload its extension by itself — it takes effect when pi restarts`)
      bad.add(want)
      want = ''
      clearInterval(idleTimer)
      clearTimeout(updateRetry)
      return
    }
    retired = true
    clearInterval(idleTimer)
    clearTimeout(updateRetry)
    log(`persona: switching ${sessions.length} session(s) to ${update.name} ${want.slice(0, 12)}`)
    rt.stop()
    for (const s of sessions) {
      try {
        const r = s.pi.sendUserMessage(`/${RELOAD_COMMAND}`, { expandPromptTemplates: true })
        r?.catch?.((err) => log(`persona: reload ${s.id}: ${err.message}`))
      } catch (err) {
        log(`persona: reload ${s.id}: ${err.message}`)
      }
    }
  }

  /**
   * ask_user: the model asks the user. The card goes to the phone whenever a hub is there, and pi's own dialog opens
   * beside it when this pi has one: the first answer wins and the other is taken back. A message sent to the session
   * meanwhile is the answer too. While it waits the session lists as needing you; stopping the turn (its signal) takes
   * the question back. Handing the card back to the computer, or its expiry, leaves the dialog waiting; closing the
   * dialog is not answering.
   */
  function askUserTool(Type, track) {
    return {
      name: 'ask_user',
      label: 'Ask user',
      description: 'Ask the user a question, optionally with options to pick from, and wait for the answer. The user may answer from their phone. Use it when you need a decision or information only the user has.',
      parameters: Type.Object({
        question: Type.String({ description: 'The question, in one or two sentences' }),
        options: Type.Optional(Type.Array(Type.String(), { description: 'Answers to pick from (the user may also write their own)' })),
      }),
      executionMode: 'sequential',
      async execute(toolCallId, params, signal, _onUpdate, ctx) {
        const s = track(ctx)
        const options = (params.options ?? []).filter((o) => typeof o === 'string' && o.trim())
        const result = (text, answer, images = []) => ({ content: [{ type: 'text', text }, ...images],
          details: { question: params.question, options, answer: answer ?? null } })
        if (signal?.aborted) return result('The question was withdrawn: the user stopped this turn.')
        const done = new AbortController() // takes back whatever still asks once there is an outcome
        let settle
        const outcome = new Promise((r) => { settle = r }) // {answer, images?} | {none: true} | {stopped: true}
        const stop = () => settle({ stopped: true })
        signal?.addEventListener('abort', stop, { once: true })
        const asking = {
          answer: (text, images = []) => settle({ answer: text, images }),
          withdraw: stop,
        }
        s.asking = asking
        const release = rt.waitingOnUser(s.id)
        let open = 0 // places still asking
        const nobody = () => { if (--open === 0) settle({ none: true }) }
        const answered = (a) => typeof a === 'string' && a.trim() ? a : undefined
        if (rt.hubConnected) {
          open++
          rt.ask({
            id: interactionId(interactionPrefix, s.id, 'question', toolCallId), kind: 'question', session_id: s.id,
            tool_name: 'AskUserQuestion', tool_use_id: toolCallId,
            questions: [{ question: params.question, header: '', multi_select: false, options: options.map((label) => ({ label, description: '' })) }],
          }, { signal: done.signal }).then((res) => {
            const a = answered(res?.answers?.[params.question])
            if (a) settle({ answer: a })
            else nobody() // handed back to the computer, expired: the dialog, if any, still asks
          }, (err) => {
            if (!done.signal.aborted) log(`persona: ask_user on the phone: ${err.message}`)
            nobody()
          })
        }
        if (ctx.hasUI) {
          open++
          const opts = { signal: done.signal }
          Promise.resolve().then(() => options.length ? ctx.ui.select(params.question, options, opts) : ctx.ui.input(params.question, undefined, opts)).then((a) => {
            const v = answered(a)
            if (v) settle({ answer: v })
            else if (!done.signal.aborted) settle({ none: true }) // closed without an answer
          }, (err) => {
            if (!done.signal.aborted) log(`persona: ask_user dialog: ${err.message}`)
            nobody()
          })
        }
        if (open === 0) settle({ none: true }) // no dialog here and no hub: nobody to ask
        try {
          const o = await outcome
          if (o.stopped) return result('The question was withdrawn: the user stopped this turn.')
          if (o.none) return result('The user did not answer.')
          const images = (o.images ?? []).map((i) => ({ type: 'image', data: i.data, mimeType: i.mimeType }))
          const said = o.answer.trim() ? o.answer : 'the attached image(s)'
          return result(`The user answered: ${said}`, o.answer, images)
        } finally {
          done.abort(new Error('answered elsewhere'))
          signal?.removeEventListener('abort', stop)
          if (s.asking === asking) s.asking = undefined
          release()
        }
      },
    }
  }

  return {
    register,
    /** Connect without a session of its own (the companion). */
    start() {
      if (!started) {
        started = true
        rt.start()
      }
    },
    stop() {
      clearInterval(idleTimer)
      clearTimeout(updateRetry)
      rt.stop()
      started = false
    },
    /** Stopped to hand over to a new version (the next version's host stops it for good, or revive takes it back). */
    get retired() {
      return retired
    },
    /**
     * The reload brought this same version back (the new one did not load): connect again, and do not try that version
     * again in this process.
     */
    revive() {
      if (!retired) return
      log(`persona: ${update?.name} ${want.slice(0, 12)} did not load; staying on ${update?.hash.slice(0, 12)}`)
      bad.add(want)
      want = ''
      retired = false
      started = true
      rt.start()
    },
    /** Whether the process could switch versions now (tests). */
    idle,
    runtime: rt,
    live,
  }
}

/**
 * Where pi-web's server listens, read from its process (`next start -p <port> -H <host>`, PORT, PI_WEB_HOSTNAME);
 * undefined when this process is not pi-web's server.
 */
export function piWebOf(argv = process.argv, env = process.env) {
  const hostKind = piHostKind(env)
  const isWeb = hostKind === 'web' || (hostKind !== 'cli' && argv.some((a) => a.includes('pi-web')) && argv.includes('start'))
  if (!isWeb) return undefined
  const i = argv.indexOf('-p')
  const port = Number(i >= 0 ? argv[i + 1] : env.PORT) || 30141
  let host = env.PI_WEB_HOSTNAME || '127.0.0.1'
  if (host === '0.0.0.0' || host === '::') host = '127.0.0.1'
  return { port, host, password: env.PI_WEB_PASSWORD || undefined }
}

/**
 * The herdr pane this process runs in, as the node writes it: HERDR_PANE_ID, with its named session in front
 * (`@<session>:<pane>`) when herdr runs it in one other than the default server — HERDR_SESSION, else the session
 * directory of HERDR_SOCKET_PATH (`…/sessions/<name>/herdr.sock`). undefined outside herdr.
 */
export function herdrPaneOf(env = process.env) {
  const pane = env.HERDR_PANE_ID
  if (!pane) return undefined
  let session = env.HERDR_SESSION || ''
  if (!session) { // as the node reads it (herdr.SocketSession): only a name that can stand in an id
    const m = /[/\\]sessions[/\\]([^/\\]+)[/\\]herdr\.sock$/.exec(env.HERDR_SOCKET_PATH || '')
    session = m && /^[A-Za-z0-9._-]{1,128}$/.test(m[1]) && m[1] !== '.' && m[1] !== '..' ? m[1] : ''
  }
  return session && session !== 'default' ? `@${session}:${pane}` : pane
}
