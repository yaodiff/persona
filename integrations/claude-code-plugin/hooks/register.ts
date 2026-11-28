import type { EngineInterface, Register } from 'claude-code'
import {
  BASE,
  CAPABILITIES,
  COMPACTION_REPORT_TIMEOUT_MS,
  PERMISSION_BODY_MOST,
  PERMISSION_GRACE_MS,
  PERMISSION_MAX_ERRORS,
  PERMISSION_MAX_WAIT_MS,
  PERMISSION_RECONNECT_MS,
  PERMISSION_RETRY_MS,
  PERMISSION_WAIT_MS,
  POLL_WAIT_MS,
  SUBMIT_QUEUE_SETTLE_MS,
  applyStepOverride,
  buildCompactionPayload,
  buildPermissionPayload,
  clearAgentPreTools,
  createClassicHookObservation,
  createPreToolCache,
  finishClassicTurn,
  hasPermissionDecision,
  homeDir,
  isToolCompleted,
  markToolCompleted,
  mergeOverride,
  nextBackoff,
  noteClassicPreToolUse,
  noteTurnStepToolUses,
  readPermissionAnswer,
  readPoll,
  recordPreTool,
  socketPath,
  takePreTool,
  toolCallKey,
  verdict,
  type ActiveTurn,
  type CompactionTrigger,
  type InterruptRequest,
  type Message,
  type PermissionAnswer,
  type PollAnswer,
  type PreToolCache,
  type SessionOverride,
  type SettingsUpdate,
} from './bridge'

// The plugin README documents the process-bound poll, owner delivery and permission authorization.

type Engine = EngineInterface

type StepEffort = { at_ms: number; level?: string; reset?: boolean }

type PermissionAuth = { sock: string; session: string }

/** Time a main-loop tool call gets to reach classic.PreToolUse before the session counts as passed over. */
const CLASSIC_HOOK_GRACE_MS = 2_000

type ActivePerm = {
  reqId: string
  sock: string
  sessionId: string
  toolUseId: string
  toolName: string
  toolKey: string
  agentId: string
  ambiguous: boolean
  cancelled: boolean
  cancelPromise: Promise<'cancelled'>
  cancel: () => void
}

export const register: Register = on => {
  // One poll loop per load: a reload runs session.start again in a fresh environment, and the old loop goes with
  // its environment. A /clear keeps the loop: each poll names the session open now.
  let loop = 0
  let activeTurn: ActiveTurn | undefined
  let observed: { session: string; effort: StepEffort } | undefined
  let override: SessionOverride | undefined
  let permissionAuth: PermissionAuth | undefined
  let classicHooks = createClassicHookObservation()
  let classicTurn = 0
  let wakePoll: (() => void) | undefined
  let compacting = false
  let compactTrigger: CompactionTrigger = 'manual'
  let compactionReport = Promise.resolve()
  const preToolCache = createPreToolCache()
  const activePerms = new Map<string, ActivePerm>()
  let permSeq = 0

  on('turn.start', async ($, e, next) => {
    if (e.agentId === undefined) {
      classicTurn++
      const session = await $.session.id().catch(() => '')
      activeTurn = { session, turnId: e.turnId }
    }
    return next(e)
  })

  on('turn.complete', ($, e, next) => {
    if (e.agentId === undefined && activeTurn?.turnId === e.turnId) {
      activeTurn = undefined
    }
    if (e.agentId === undefined) {
      if (compacting) {
        compacting = false
        compactionReport = queueCompactionReport($, compactionReport, 'end', compactTrigger)
      }
      classicTurn++
      const wasPassedOver = classicHooks.passedOver
      classicHooks = finishClassicTurn(classicHooks)
      if (classicHooks.passedOver) {
        permissionAuth = undefined
        if (!wasPassedOver) wakePoll?.()
      }
    }
    try {
      const agentId = e.agentId ?? ''
      clearAgentPreTools(preToolCache, agentId)
      handleTurnDone($, activePerms, e.turnId, agentId)
    } catch {
      // Turn cleanup never blocks turn completion.
    }
    return next(e)
  })

  on('session.compact', async ($, e, next) => {
    if (e.agentId !== undefined) return next(e)
    compacting = true
    compactTrigger = e.trigger
    compactionReport = queueCompactionReport($, compactionReport, 'start', compactTrigger)
    try {
      return await next(e)
    } finally {
      compacting = false
      compactionReport = queueCompactionReport($, compactionReport, 'end', compactTrigger)
    }
  })

  on('turn.step', async function* ($, e, next) {
    let step = e
    const observingTurn = classicTurn
    if (e.agentId === undefined) {
      const session = activeTurn?.turnId === e.turnId ? activeTurn.session : await $.session.id().catch(() => '')
      if (session !== '') activeTurn = { session, turnId: e.turnId }
      if (override !== undefined) {
        const sessionModel = override.model !== undefined ? await $.session.model().catch(() => undefined) : undefined
        const applied = applyStepOverride(e, override, session, sessionModel)
        override = applied.nextOverride
        step = applied.step
      }
      const at = await $.clock.now().catch(() => 0)
      const level = override?.effort === 'auto' ? 'auto' : step.effort
      if (at > 0 && session !== '') observed = { session, effort: { at_ms: at, ...(typeof level === 'string' ? { level } : {}) } }
    }
    const output = yield* next(step)
    try {
      classicHooks = noteTurnStepToolUses(classicHooks, e.agentId, output)
      if (e.agentId === undefined && !classicHooks.passedOver && classicHooks.toolUses > 0) {
        void sleep($, CLASSIC_HOOK_GRACE_MS)
          .then(() => {
            if (
              observingTurn !== classicTurn ||
              classicHooks.passedOver ||
              classicHooks.toolUses === 0 ||
              classicHooks.preToolUses > 0
            ) {
              return
            }
            classicHooks = { ...classicHooks, passedOver: true }
            permissionAuth = undefined
            wakePoll?.()
          })
          .catch(() => {
            // A missing clock only delays the turn.complete fallback.
          })
      }
    } catch {
      // Observation never changes the step.
    }
    return output
  })

  on('classic.PreToolUse', (_$, e, next) => {
    try {
      classicHooks = noteClassicPreToolUse(classicHooks, e)
      recordPreTool(preToolCache, e)
      return next(e)
    } catch {
      return next(e)
    }
  }).catch((_$, e, next) => next(e))

  on('classic.PostToolUse', ($, e, next) => {
    try {
      handleToolDone($, preToolCache, activePerms, e, 'post_tool_use')
      return next(e)
    } catch {
      return next(e)
    }
  }).catch((_$, e, next) => next(e))

  on('classic.PermissionDenied', ($, e, next) => {
    try {
      handleToolDone($, preToolCache, activePerms, e, 'permission_denied')
      return next(e)
    } catch {
      return next(e)
    }
  }).catch((_$, e, next) => next(e))

  on('classic.PermissionRequest', async ($, e, next) => {
    type CoreResult = Awaited<ReturnType<typeof next>>
    try {
      const rec = asRecord(e)
      const sessionId = typeof rec?.session_id === 'string' ? rec.session_id : ''
      const auth = permissionAuth
      if (auth === undefined || sessionId === '' || auth.session !== sessionId) {
        return next(e)
      }
      const toolName = typeof rec?.tool_name === 'string' ? rec.tool_name : ''
      if (toolName === '') return next(e)
      const agentId = typeof rec?.agent_id === 'string' ? rec.agent_id : ''
      const toolKey = toolCallKey(toolName, rec?.tool_input, toolName === 'AskUserQuestion', agentId)
      const matchingActive = [...activePerms.values()].filter(
        p => p.sessionId === sessionId && p.toolKey === toolKey,
      )
      const hasActiveSameKey = matchingActive.length > 0
      const explicitToolUseId = typeof rec?.tool_use_id === 'string' ? rec.tool_use_id : ''
      let toolUseId = explicitToolUseId
      let ambiguous = false
      if (toolUseId === '') {
        const paired = takePreTool(preToolCache, toolName, rec?.tool_input, agentId, hasActiveSameKey)
        toolUseId = paired.id
        ambiguous = paired.ambiguous
        if (ambiguous) {
          for (const prev of matchingActive) {
            if (prev.toolUseId === '') prev.ambiguous = true
          }
        }
      }
      if (isToolCompleted(preToolCache, toolUseId)) {
        return next(e)
      }
      const now = await $.clock.now().catch(() => 0)
      const reqId = `p_${now}_${++permSeq}`
      let resolveCancel!: () => void
      const cancelPromise = new Promise<'cancelled'>(resolve => {
        resolveCancel = () => resolve('cancelled')
      })
      const entry: ActivePerm = {
        reqId,
        sock: auth.sock,
        sessionId,
        toolUseId,
        toolName,
        toolKey,
        agentId,
        ambiguous,
        cancelled: false,
        cancelPromise,
        cancel() {
          if (!entry.cancelled) {
            entry.cancelled = true
            resolveCancel()
          }
        },
      }
      activePerms.set(reqId, entry)

      // Start core (local command hooks) immediately and give fast local allow/deny hooks a brief grace window.
      let coreDone = false
      let coreResult: CoreResult = {}
      let coreHasDecision = false
      let resolveCoreDecision!: () => void
      const coreDecisionPromise = new Promise<'core'>(resolve => {
        resolveCoreDecision = () => resolve('core')
      })
      const coreP: Promise<CoreResult> = Promise.resolve().then(() => next(e))
      const coreSettled = coreP.then(
        val => {
          coreDone = true
          coreResult = val
          coreHasDecision = hasPermissionDecision(val)
          if (coreHasDecision) resolveCoreDecision()
          return 'core_settled' as const
        },
        () => {
          coreDone = true
          coreResult = {}
          coreHasDecision = false
          return 'core_settled' as const
        },
      )

      const passthrough = (): CoreResult | Promise<CoreResult> => (coreDone ? coreResult : coreP)

      try {
        // If core settles quickly with a decision (e.g. a local allow hook), return it with zero HTTP round trips.
        // If core settles quickly without a decision (the common case with no local hooks), open the card right away.
        await Promise.race([coreSettled, sleep($, PERMISSION_GRACE_MS), cancelPromise])
        if (entry.cancelled || isToolCompleted(preToolCache, toolUseId)) {
          return passthrough()
        }
        if (coreDone && coreHasDecision) {
          return coreResult
        }

        const payload = buildPermissionPayload(sessionId, reqId, toolUseId, rec ?? {}, PERMISSION_WAIT_MS)
        const body = JSON.stringify(payload)
        if (body.length > PERMISSION_BODY_MOST) {
          return passthrough()
        }

        const startedAt = await $.clock.now().catch(() => now)
        let errors = 0
        let connected = false
        let disconnectStartedAt: number | undefined
        while (!entry.cancelled) {
          const currentNow = await $.clock.now().catch(() => startedAt)
          if (currentNow - startedAt >= PERMISSION_MAX_WAIT_MS) {
            void notifyPermissionDone($, entry.sock, {
              session_id: sessionId,
              req_id: reqId,
              ...(toolUseId !== '' ? { tool_use_id: toolUseId } : {}),
              reason: 'timeout',
            })
            return passthrough()
          }
          const fetchP = fetchPermission($, entry.sock, body).then(answer => ({ kind: 'http' as const, answer }))
          const winner = await Promise.race([fetchP, coreDecisionPromise, cancelPromise])
          if (winner === 'core') {
            void notifyPermissionDone($, entry.sock, {
              session_id: sessionId,
              req_id: reqId,
              ...(toolUseId !== '' ? { tool_use_id: toolUseId } : {}),
              reason: 'local_hook',
            })
            return coreResult
          }
          if (winner === 'cancelled' || entry.cancelled) {
            return passthrough()
          }
          const ans = winner.answer
          if (ans.action === 'decide') {
            return { decision: ans.decision } as unknown as CoreResult
          }
          if (ans.action === 'passthrough') {
            return passthrough()
          }
          if (ans.action === 'refused') {
            if (permissionAuth?.session === sessionId) permissionAuth = undefined
            return passthrough()
          }
          if (ans.action === 'renew') {
            connected = true
            disconnectStartedAt = undefined
            errors = 0
            continue
          }
          // Transient error (e.g. node restarting): an initial connection fails fast; a request that already reached
          // the node keeps retrying across the 2-minute hub reclaim window (matching nodeclient.ResumeWait).
          if (!connected) {
            errors++
            if (errors >= PERMISSION_MAX_ERRORS) {
              return passthrough()
            }
          } else {
            const failNow = await $.clock.now().catch(() => currentNow)
            if (disconnectStartedAt === undefined) disconnectStartedAt = failNow
            if (failNow - disconnectStartedAt >= PERMISSION_RECONNECT_MS) {
              return passthrough()
            }
          }
          const retryWinner = await Promise.race([
            sleep($, PERMISSION_RETRY_MS).then(() => 'retry' as const),
            coreDecisionPromise,
            cancelPromise,
          ])
          if (retryWinner === 'core') {
            void notifyPermissionDone($, entry.sock, {
              session_id: sessionId,
              req_id: reqId,
              ...(toolUseId !== '' ? { tool_use_id: toolUseId } : {}),
              reason: 'local_hook',
            })
            return coreResult
          }
          if (retryWinner === 'cancelled' || entry.cancelled) {
            return passthrough()
          }
        }
        return passthrough()
      } catch {
        return passthrough()
      } finally {
        activePerms.delete(reqId)
      }
    } catch {
      return next(e)
    }
  }).catch((_$, e, next) => next(e))

  on('session.start', async ($, e, next) => {
    const started = await next(e)
    observed = undefined
    const mine = ++loop
    void run(
      $,
      () => mine === loop,
      () => activeTurn,
      turnId => {
        if (activeTurn?.turnId === turnId) activeTurn = undefined
      },
      session => observed?.session === session ? observed.effort : undefined,
      () => classicHooks.passedOver,
      wake => {
        wakePoll = wake
      },
      async (session, update) => {
        const sessionModel = update.model !== undefined ? await $.session.model().catch(() => undefined) : undefined
        override = mergeOverride(override, session, update, sessionModel)
      },
      auth => {
        permissionAuth = auth
      },
    )
    return started
  })
}

function handleToolDone(
  $: Engine,
  preToolCache: PreToolCache,
  activePerms: Map<string, ActivePerm>,
  raw: unknown,
  reason: 'post_tool_use' | 'permission_denied',
): void {
  const rec = asRecord(raw)
  const toolUseId = typeof rec?.tool_use_id === 'string' ? rec.tool_use_id : ''
  const toolName = typeof rec?.tool_name === 'string' ? rec.tool_name : ''
  const agentId = typeof rec?.agent_id === 'string' ? rec.agent_id : ''
  if (toolUseId !== '') markToolCompleted(preToolCache, toolUseId)
  if (activePerms.size === 0) return

  const sessionId = typeof rec?.session_id === 'string' ? rec.session_id : ''
  const matched: ActivePerm[] = []
  if (toolUseId !== '') {
    for (const entry of activePerms.values()) {
      if (entry.toolUseId === toolUseId && (sessionId === '' || entry.sessionId === sessionId)) {
        matched.push(entry)
      }
    }
  }
  if (matched.length === 0 && toolName !== '') {
    const key = toolCallKey(toolName, rec?.tool_input, toolName === 'AskUserQuestion', agentId)
    const unpaired: ActivePerm[] = []
    for (const entry of activePerms.values()) {
      if (
        !entry.ambiguous &&
        entry.toolUseId === '' &&
        entry.toolKey === key &&
        (sessionId === '' || entry.sessionId === sessionId)
      ) {
        unpaired.push(entry)
      }
    }
    // Only match by (agent_id, tool, input) when unambiguous and not marked ambiguous; never guess across identical calls.
    if (unpaired.length === 1 && unpaired[0] !== undefined) {
      matched.push(unpaired[0])
    }
  }
  for (const entry of matched) {
    entry.cancel()
    activePerms.delete(entry.reqId)
    if (entry.sock !== '' && entry.sessionId !== '') {
      void notifyPermissionDone($, entry.sock, {
        session_id: entry.sessionId,
        req_id: entry.reqId,
        ...(toolUseId !== '' || entry.toolUseId !== '' ? { tool_use_id: toolUseId || entry.toolUseId } : {}),
        ...(toolName !== '' ? { tool_name: toolName } : {}),
        ...(agentId !== '' ? { agent_id: agentId } : {}),
        reason,
      })
    }
  }
}

function handleTurnDone(
  $: Engine,
  activePerms: Map<string, ActivePerm>,
  turnId: string,
  agentId: string,
): void {
  if (activePerms.size === 0) return
  const byTarget = new Map<string, { sock: string; sessionId: string; reqIds: string[] }>()
  for (const entry of [...activePerms.values()]) {
    if (entry.agentId !== agentId) continue
    entry.cancel()
    activePerms.delete(entry.reqId)
    if (entry.sock === '' || entry.sessionId === '') continue
    const targetKey = `${entry.sock}\0${entry.sessionId}`
    let group = byTarget.get(targetKey)
    if (group === undefined) {
      group = { sock: entry.sock, sessionId: entry.sessionId, reqIds: [] }
      byTarget.set(targetKey, group)
    }
    group.reqIds.push(entry.reqId)
  }
  for (const group of byTarget.values()) {
    void notifyPermissionDone($, group.sock, {
      session_id: group.sessionId,
      req_ids: group.reqIds,
      turn_id: turnId,
      ...(agentId !== '' ? { agent_id: agentId } : {}),
      reason: 'turn_complete',
    })
  }
}

async function run(
  $: Engine,
  current: () => boolean,
  getActiveTurn: () => ActiveTurn | undefined,
  clearActiveTurn: (turnId: string) => void,
  observedEffort: (session: string) => StepEffort | undefined,
  classicPassedOver: () => boolean,
  setWakePoll: (wake: (() => void) | undefined) => void,
  onSettings: (session: string, update: SettingsUpdate) => Promise<void>,
  setPermissionAuth: (auth: PermissionAuth | undefined) => void,
): Promise<void> {
  const home = homeDir(await $.env.get('USERPROFILE'), await $.env.get('HOME'))
  const sock = socketPath(home, await $.env.get('PERSONA_HOME'))
  if (sock === undefined) return
  const pluginVersion = await manifestVersion($)
  const engineVersion = await $.session
    .version()
    .then(v => v.version)
    .catch(() => '')
  let backoff = 0
  let reachable: boolean | undefined
  let previousSession: string | undefined
  let reset: StepEffort | undefined
  while (current()) {
    const session = await $.session.id()
    if (previousSession !== session) {
      previousSession = session
      const at = await $.clock.now().catch(() => 0)
      reset = at > 0 ? { at_ms: at, reset: true } : undefined
    }
    setWakePoll(() => {
      void $.http
        .fetch(`${BASE}/poll`, post(sock, {
          session_id: session,
          plugin_version: pluginVersion,
          engine_version: engineVersion,
          capabilities: CAPABILITIES,
          classic_passed_over: true,
          wait_ms: 0,
        }))
        .catch(() => {
          // The regular poll loop reconnects.
        })
    })
    let answer: PollAnswer
    try {
      const res = await $.http.fetch(`${BASE}/poll`, post(sock, {
        session_id: session,
        plugin_version: pluginVersion,
        engine_version: engineVersion,
        capabilities: CAPABILITIES,
        observed_effort: observedEffort(session) ?? reset,
        ...(classicPassedOver() ? { classic_passed_over: true } : {}),
        wait_ms: POLL_WAIT_MS,
      }))
      answer = readPoll(res.status, res.text)
    } catch {
      answer = { kind: 'unavailable' }
    }
    if (!current()) return
    const now = answer.kind === 'messages'
    if (now !== reachable) {
      reachable = now
      $.ui.log(now ? 'Persona node reached' : `Persona node not reachable (${answer.kind})`, { to: 'debug' })
    }
    if (answer.kind !== 'messages') {
      setPermissionAuth(undefined)
      backoff = answer.kind === 'refused' ? answer.retryMs : nextBackoff(backoff)
      await sleep($, backoff)
      continue
    }
    backoff = 0
    setPermissionAuth(answer.permissions ? { sock, session } : undefined)
    if (answer.interrupt !== undefined) {
      await applyInterrupt($, sock, session, answer.interrupt, getActiveTurn, clearActiveTurn)
    }
    if (answer.settings !== undefined) await applySettings($, sock, session, answer.settings, onSettings)
    for (const m of answer.messages) await deliver($, sock, session, m)
    if (answer.retryMs > 0) await sleep($, answer.retryMs)
  }
}

async function fetchPermission($: Engine, sock: string, body: string): Promise<PermissionAnswer> {
  try {
    const res = await $.http.fetch(`${BASE}/permission`, {
      method: 'POST',
      socketPath: sock,
      headers: { 'content-type': 'application/json' },
      body,
    })
    return readPermissionAnswer(res.status, res.text)
  } catch {
    return { action: 'unavailable' }
  }
}

async function notifyPermissionDone($: Engine, sock: string, body: Record<string, unknown>): Promise<void> {
  try {
    await $.http.fetch(`${BASE}/permission-done`, post(sock, body))
  } catch {
    // The node went away; its own watcher withdraws the card.
  }
}

async function reportCompaction($: Engine, phase: 'start' | 'end', trigger: CompactionTrigger): Promise<void> {
  const sock = socketPath(
    homeDir(await $.env.get('USERPROFILE'), await $.env.get('HOME')),
    await $.env.get('PERSONA_HOME'),
  )
  const session = await $.session.id().catch(() => '')
  if (sock === undefined || session === '') return
  try {
    await $.http.fetch(
      `${BASE}/compact`,
      post(sock, buildCompactionPayload(session, phase, trigger), COMPACTION_REPORT_TIMEOUT_MS),
    )
  } catch {
    // The compaction itself neither waits for this report nor fails with it.
  }
}

/** Orders start before end without making the compaction wait for either report. */
function queueCompactionReport(
  $: Engine,
  previous: Promise<void>,
  phase: 'start' | 'end',
  trigger: CompactionTrigger,
): Promise<void> {
  return previous.then(() => reportCompaction($, phase, trigger)).catch(() => {})
}

async function applyInterrupt(
  $: Engine,
  sock: string,
  session: string,
  req: InterruptRequest,
  getActiveTurn: () => ActiveTurn | undefined,
  clearActiveTurn: (turnId: string) => void,
): Promise<void> {
  let ack: { submitted: boolean; reason?: string }
  if ((await $.clock.now()) > req.deadlineMs) {
    ack = { submitted: false, reason: 'late' }
  } else {
    const turn = getActiveTurn()
    if (turn === undefined || turn.session !== session || turn.turnId === '') {
      ack = { submitted: false, reason: 'no_turn' }
    } else {
      try {
        await $.turn.abort({ turnId: turn.turnId })
        clearActiveTurn(turn.turnId)
        ack = { submitted: true }
      } catch (err) {
        const msg = String(err)
        if (msg.includes('no turn is running') || msg.includes('is already ending')) {
          clearActiveTurn(turn.turnId)
          ack = { submitted: true, reason: 'already_settled' }
        } else {
          ack = { submitted: false, reason: 'failed' }
        }
      }
    }
  }
  try {
    await $.http.fetch(`${BASE}/ack`, post(sock, { session_id: session, id: req.id, ...ack }))
  } catch {
    // The node went away before receiving the ack.
  }
}

async function applySettings(
  $: Engine,
  sock: string,
  session: string,
  update: SettingsUpdate,
  onSettings: (session: string, update: SettingsUpdate) => Promise<void>,
): Promise<void> {
  let ok = true
  try {
    await onSettings(session, update)
  } catch {
    ok = false
  }
  const ack = ok ? { submitted: true } : { submitted: false, reason: 'failed' }
  try {
    await $.http.fetch(`${BASE}/ack`, post(sock, { session_id: session, id: update.id, ...ack }))
  } catch {
    // The node went away before receiving the ack.
  }
}

/**
 * Submits one message as the person's words, if it may go in now, and tells the node what became of it.
 * When a turn is running, Claude Code enqueues the prompt synchronously (`queue-operation: enqueue`) and only resolves
 * `$.prompt.submit` once the queued prompt enters its own turn. The settle window releases the poll loop for interrupts
 * and further prompts; it is not a submission acknowledgement. The continuation reports the actual result later, while
 * the node can confirm the queued prompt from the transcript.
 */
async function deliver($: Engine, sock: string, session: string, m: Message): Promise<void> {
  const report = async (outcome: string) => {
    const ack = outcome === 'ok' ? { submitted: true } : { submitted: false, reason: outcome }
    try {
      await $.http.fetch(`${BASE}/ack`, post(sock, { session_id: session, id: m.id, ...ack }))
    } catch {
      // The node went away: it reads the transcript to learn what became of the message.
    }
  }
  let outcome: string = verdict(m, await $.clock.now())
  if (outcome === 'ok' && typeof m.text === 'string') {
    try {
      const pending = $.prompt.submit({ text: m.text, asUser: true })
      const result = pending.then(
        submitted => (submitted.drop !== undefined ? 'dropped' : 'ok'),
        () => 'failed',
      )
      outcome = await Promise.race([
        result,
        sleep($, SUBMIT_QUEUE_SETTLE_MS).then(() => 'pending'),
      ])
      if (outcome === 'pending') {
        void result.then(report)
        return
      }
    } catch {
      outcome = 'failed'
    }
  }
  await report(outcome)
}

async function manifestVersion($: Engine): Promise<string> {
  try {
    const manifest: unknown = JSON.parse(await $.fs.read(`${$.plugin.root}/.claude-plugin/plugin.json`))
    const version = (manifest as { version?: unknown }).version
    return typeof version === 'string' ? version : ''
  } catch {
    return ''
  }
}

function asRecord(v: unknown): Record<string, unknown> | undefined {
  return typeof v === 'object' && v !== null && !Array.isArray(v) ? (v as Record<string, unknown>) : undefined
}

function post(socket: string, body: Record<string, unknown>, timeoutMs?: number) {
  return {
    method: 'POST',
    socketPath: socket,
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(body),
    ...(timeoutMs !== undefined ? { timeoutMs } : {}),
  }
}

function sleep($: Engine, ms: number): Promise<void> {
  return new Promise(resolve => {
    $.clock.after(ms, resolve)
  })
}
