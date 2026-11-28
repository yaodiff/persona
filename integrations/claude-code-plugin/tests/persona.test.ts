import type { On } from 'claude-code'
import { describe, expect, mock, test } from 'claude-code/testing'
import {
  applyStepOverride,
  canonicalJson,
  clearAgentPreTools,
  createClassicHookObservation,
  createPreToolCache,
  extractPreToolUse,
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
  verdict,
} from '../hooks/bridge'
import { register } from '../hooks/register'

const SESSION = '11111111-2222-3333-4444-555555555555'

type Answer = { status: number; body: unknown } | { fail: true }
type PermResponder = (body: Record<string, unknown>, call: number) => Answer | Promise<Answer>
type StepToolUse = { tool_use_id: string; tool: string; input: Record<string, unknown> }

// The engine beneath the plugin: a session, its manifest, and a node that answers polls in the order given (then
// none: a node that went away), recording every request and every prompt the plugin submits.
function world(
  on: On,
  polls: Answer[],
  env: Record<string, string> = { HOME: '/home/me' },
  opts: {
    onPermission?: PermResponder
    corePermission?: (e: Record<string, unknown>) => unknown | Promise<unknown>
    stepToolUses?: StepToolUse[]
  } = {},
) {
  const requests: { url: string; socketPath?: string; body: Record<string, unknown> }[] = []
  const submitted: { text: string; origin: unknown }[] = []
  const aborted: string[] = []
  const steps: { model: string; effort?: string | number; agentId?: string }[] = []
  let holdSubmit = false
  let settleSubmit: ((outcome: 'reject' | 'drop') => void) | undefined
  let abortError: string | undefined
  let sessionId = SESSION
  let sessionModel = 'claude-sonnet-5-5'
  let permCalls = 0
  mock.env(on, env)
  const clock = mock.clock(on, { now: 1_000 })
  on('session.start', (_$, e) => ({ cwd: e.cwd }))
  on('session.id', () => ({ value: sessionId }))
  on('session.model', () => ({ value: sessionModel }))
  on('session.version', () => ({ value: { version: '2.1.330', base: '2.1.330' } }))
  on('fs.read', () => ({ value: '{"name":"persona","version":"0.3.2"}' }))
  on('ui.log', () => ({ value: undefined }))
  on('turn.start', (_$, e) => ({ turnId: e.turnId }))
  on('turn.complete', () => ({ text: 'done' }))
  on('turn.abort', (_$, e) => {
    if (abortError !== undefined) throw new Error(abortError)
    aborted.push(e.turnId)
    return { value: undefined }
  })
  on('tool.call', () => ({ result: 'ok' }) as never)
  on('classic.PostToolUse', () => ({}))
  on('classic.PermissionDenied', () => ({}))
  on('classic.PermissionRequest', (_$, e) => {
    if (opts.corePermission !== undefined) return opts.corePermission(e as unknown as Record<string, unknown>) as Record<string, unknown>
    return {}
  })
  on('prompt.submit', (_$, e) => {
    submitted.push({ text: e.text, origin: e.origin })
    if (holdSubmit) {
      return new Promise<{ drop: string }>((resolve, reject) => {
        settleSubmit = outcome => {
          settleSubmit = undefined
          if (outcome === 'reject') reject(new Error('submit failed'))
          else resolve({ drop: 'dropped' })
        }
      })
    }
    return { text: e.text }
  })
  on('turn.step', async function* (_$, e) {
    steps.push({ model: e.model, ...(e.effort !== undefined ? { effort: e.effort } : {}), ...(e.agentId !== undefined ? { agentId: e.agentId } : {}) })
    return {
      turnId: e.turnId,
      index: e.index,
      answer: 'ok',
      toolUses: (opts.stepToolUses ?? []) as never,
      stopReason: 'end_turn' as const,
      usage: null,
    }
  })
  on('http.fetch', async (_$, e) => {
    const rawBody = typeof e.init?.body === 'string' ? e.init.body : '{}'
    const parsed = JSON.parse(rawBody) as Record<string, unknown>
    requests.push({ url: e.url, socketPath: e.init?.socketPath, body: parsed })
    if (e.url.endsWith('/ack') || e.url.endsWith('/permission-done')) {
      return { value: { status: 200, ok: true, headers: {}, text: '{"ok":true}' } }
    }
    if (e.url.endsWith('/permission')) {
      const next = opts.onPermission !== undefined ? await opts.onPermission(parsed, ++permCalls) : { fail: true as const }
      if ('fail' in next) throw new Error('connection refused')
      return { value: { status: next.status, ok: next.status === 200, headers: {}, text: JSON.stringify(next.body) } }
    }
    const next = polls.shift() ?? { fail: true }
    if ('fail' in next) throw new Error('connection refused')
    return { value: { status: next.status, ok: next.status === 200, headers: {}, text: JSON.stringify(next.body) } }
  })
  return {
    requests,
    submitted,
    aborted,
    steps,
    clock,
    setHoldSubmit(v: boolean) {
      holdSubmit = v
    },
    finishSubmit(outcome: 'reject' | 'drop') {
      const settle = settleSubmit
      if (settle === undefined) throw new Error('no held prompt.submit')
      settle(outcome)
    },
    setAbortError(err: string | undefined) {
      abortError = err
    },
    setSessionId(id: string) {
      sessionId = id
    },
    setSessionModel(m: string) {
      sessionModel = m
    },
  }
}

const start = { cwd: '/work', surface: 'desktop', isInteractive: true } as const

function permEvent(overrides: Record<string, unknown> = {}) {
  return {
    session_id: SESSION,
    transcript_path: '/home/me/.claude/projects/work/s.jsonl',
    cwd: '/work',
    prompt_id: 'prompt-1',
    permission_mode: 'default',
    hook_event_name: 'PermissionRequest',
    tool_name: 'Bash',
    tool_input: { command: 'echo hello', description: 'print greeting' },
    ...overrides,
  }
}

describe('persona plugin', () => {
  test('loads the module and exports its registration hook', () => {
    expect(typeof register).toBe('function')
  })

  test('submits the node’s message as the person’s words and says so', async ($, on) => {
    const w = world(on, [{ status: 200, body: { messages: [{ id: 'm1', text: 'go ahead and merge', deadline_ms: 9_000 }] } }])
    await $.session.start(start)
    await w.clock.settle()

    expect(w.submitted).toEqual([{ text: 'go ahead and merge', origin: { kind: 'plugin', name: 'persona', asUser: true } }])
    const [poll, ack] = w.requests
    expect(poll?.url).toBe('http://persona/claude-plugin/v1/poll')
    expect(poll?.socketPath).toBe('/home/me/.persona/node.sock')
    expect(poll?.body).toEqual({
      session_id: SESSION,
      plugin_version: '0.3.2',
      observed_effort: { at_ms: 1000, reset: true },
      engine_version: '2.1.330',
      capabilities: ['busy_input', 'interrupt', 'settings', 'permissions'],
      wait_ms: 25_000,
    })
    expect(ack?.url).toBe('http://persona/claude-plugin/v1/ack')
    expect(ack?.body).toEqual({ session_id: SESSION, id: 'm1', submitted: true })
  })

  test('queues a busy message without blocking the poll loop and aborts the active main-loop turn on interrupt', async ($, on) => {
    const w = world(on, [
      { status: 200, body: { messages: [{ id: 'm-busy', text: 'also check edge cases', deadline_ms: 9_000 }] } },
      { status: 200, body: { messages: [], interrupt: { id: 'int-1', deadline_ms: 9_000 } } },
      { status: 200, body: { messages: [], interrupt: { id: 'int-2', deadline_ms: 9_000 } } },
    ])
    w.setHoldSubmit(true)
    await $.turn.start({ text: 'initial task', turnId: 'turn-main-1' })
    await $.turn.start({ text: 'subagent task', turnId: 'turn-sub-1', agentId: 'sub-1' })
    await $.session.start(start)
    await w.clock.settle()

    // The settle window releases polling, but cannot prove delivery or produce a final message ack.
    await w.clock.advance(250)
    await w.clock.settle()

    expect(w.submitted).toEqual([{ text: 'also check edge cases', origin: { kind: 'plugin', name: 'persona', asUser: true } }])
    expect(w.aborted).toEqual(['turn-main-1'])
    expect(w.requests.filter(r => r.url.endsWith('/ack')).map(r => r.body)).toEqual([
      { session_id: SESSION, id: 'int-1', submitted: true },
      { session_id: SESSION, id: 'int-2', submitted: false, reason: 'no_turn' },
    ])
  })

  for (const busy of [false, true]) {
    for (const outcome of ['reject', 'drop'] as const) {
      const state = busy ? 'busy' : 'idle'
      const result = outcome === 'reject' ? 'rejection' : 'drop'
      const reason = outcome === 'reject' ? 'failed' : 'dropped'
      test(`reports a delayed ${state} submit ${result} only as ${reason}`, async ($, on) => {
        const w = world(on, [
          { status: 200, body: { messages: [{ id: 'm-delayed', text: 'delayed owner follow-up', deadline_ms: 9_000 }] } },
        ])
        w.setHoldSubmit(true)
        if (busy) await $.turn.start({ text: 'initial task', turnId: 'turn-main-delayed' })
        await $.session.start(start)
        await w.clock.settle()
        await w.clock.advance(250)
        await w.clock.settle()

        w.finishSubmit(outcome)
        await w.clock.settle()

        expect(w.requests.filter(r => r.url.endsWith('/ack')).map(r => r.body)).toEqual([
          { session_id: SESSION, id: 'm-delayed', submitted: false, reason },
        ])
      })
    }
  }

  test('applies session-only model and effort switches on main-loop turn.step and yields when desktop changes model', async ($, on) => {
    const w = world(on, [
      { status: 200, body: { messages: [], settings: { id: 's1', model: 'gateway-opus-5-5[1m]', effort: 'xhigh' } } },
    ])
    await $.session.start(start)
    await w.clock.settle()

    expect(w.requests.filter(r => r.url.endsWith('/ack')).map(r => r.body)).toEqual([
      { session_id: SESSION, id: 's1', submitted: true },
    ])

    const runStep = async (input: Parameters<typeof $.turn.step>[0]) => {
      const gen = $.turn.step(input)
      while (!(await gen.next()).done) {
        // drain chunks
      }
    }

    // Main loop step gets the picked model and effort; subagent step keeps its own model.
    await runStep({ turnId: 't1', index: 0, model: 'claude-sonnet-5-5', effort: 'medium', messageCount: 1 })
    await runStep({ turnId: 't1', index: 0, model: 'claude-haiku-5-5', messageCount: 1, agentId: 'sub-1' })
    expect(w.steps).toEqual([
      { model: 'gateway-opus-5-5[1m]', effort: 'xhigh' },
      { model: 'claude-haiku-5-5', agentId: 'sub-1' },
    ])

    await w.clock.advance(2_000)
    await w.clock.settle()
    expect(w.requests.at(-1)?.body.observed_effort).toEqual({ level: 'xhigh', at_ms: 1000 })

    // Changing model in Claude Desktop UI supersedes Persona's earlier pick.
    w.setSessionModel('claude-opus-5-5')
    await runStep({ turnId: 't2', index: 0, model: 'claude-opus-5-5', effort: 'high', messageCount: 2 })
    expect(w.steps.at(-1)).toEqual({ model: 'claude-opus-5-5', effort: 'high' })
  })

  test('keeps an explicit auto choice and an active turn across a missing session id', async ($, on) => {
    const w = world(on, [
      { status: 200, body: { messages: [], settings: { id: 's1', effort: 'auto' }, retry_ms: 100 } },
      { status: 200, body: { messages: [], interrupt: { id: 'stop', deadline_ms: 9_000 } } },
    ])
    await $.session.start(start)
    await w.clock.settle()
    await $.turn.start({ text: 'task', turnId: 't1' })
    w.setSessionId('')
    const gen = $.turn.step({ turnId: 't1', index: 0, model: 'sonnet', effort: 'high', messageCount: 1 })
    while (!(await gen.next()).done) { /* drain the step */ }
    w.setSessionId(SESSION)
    await w.clock.advance(100)
    await w.clock.settle()
    expect(w.steps.at(-1)?.effort).toBeUndefined()
    expect(w.requests.filter(r => r.url.endsWith('/poll'))[1]?.body.observed_effort).toEqual({ level: 'auto', at_ms: 1000 })
    expect(w.aborted).toEqual(['t1'])
  })

  test('polls with the effective main-loop effort only for the session that ran the step', async ($, on) => {
    const reply = { status: 200, body: { messages: [], retry_ms: 100 } }
    const w = world(on, Array(10).fill(reply))
    await $.session.start(start)
    await w.clock.settle()

    const step = async (effort?: 'low' | 'high' | number, agentId?: string) => {
      const gen = $.turn.step({ turnId: 'main', index: 0, model: 'sonnet', messageCount: 1, effort, agentId })
      while (!(await gen.next()).done) { /* drain the step */ }
    }
    const poll = async () => {
      await w.clock.advance(100)
      await w.clock.settle()
      return w.requests.at(-1)?.body
    }
    await step('high')
    expect((await poll())?.observed_effort).toEqual({ level: 'high', at_ms: 1000 })
    await step('low', 'sub-1')
    expect((await poll())?.observed_effort).toEqual({ level: 'high', at_ms: 1000 })
    await step('low')
    expect((await poll())?.observed_effort).toEqual({ level: 'low', at_ms: 1200 })
    await step(4096)
    expect((await poll())?.observed_effort).toEqual({ at_ms: 1300 })
    await step()
    expect((await poll())?.observed_effort).toEqual({ at_ms: 1400 })
    await step('high')
    w.setSessionId('22222222-2222-3333-4444-555555555555')
    const cleared = await poll()
    expect(cleared?.session_id).toBe('22222222-2222-3333-4444-555555555555')
    expect(cleared?.observed_effort).toEqual({ at_ms: 1600, reset: true })
  })

  test('submits nothing late, empty or a slash command, and tells the node why', async ($, on) => {
    const w = world(on, [{
      status: 200,
      body: {
        messages: [
          { id: 'late', text: 'too late', deadline_ms: 500 },
          { id: 'cmd', text: ' /exit', deadline_ms: 9_000 },
          { id: 'empty', text: '  ', deadline_ms: 9_000 },
          { id: 'odd', text: 42, deadline_ms: 9_000 },
          { id: 'bad id!', text: 'no id', deadline_ms: 9_000 },
        ],
      },
    }])
    await $.session.start(start)
    await w.clock.settle()

    expect(w.submitted).toEqual([])
    expect(w.requests.filter(r => r.url.endsWith('/ack')).map(r => r.body)).toEqual([
      { session_id: SESSION, id: 'late', submitted: false, reason: 'late' },
      { session_id: SESSION, id: 'cmd', submitted: false, reason: 'command' },
      { session_id: SESSION, id: 'empty', submitted: false, reason: 'invalid' },
      { session_id: SESSION, id: 'odd', submitted: false, reason: 'invalid' },
    ])
  })

  test('backs off quietly while no node answers, and polls again once one does', async ($, on) => {
    const w = world(on, [{ fail: true }, { status: 404, body: {} }, { status: 200, body: { messages: [] } }])
    await $.session.start(start)
    await w.clock.settle()
    expect(w.requests.length).toBe(1)
    await w.clock.advance(2_000) // the first backoff: an old node answers 404
    expect(w.requests.length).toBe(2)
    await w.clock.advance(3_999) // the second is twice the first
    expect(w.requests.length).toBe(2)
    await w.clock.advance(1)
    expect(w.requests.length).toBe(4) // answered empty: the next poll goes at once (and finds no node)
    expect(w.submitted).toEqual([])
  })

  test('waits as long as the node asks when it does not serve this session', async ($, on) => {
    const w = world(on, [{ status: 403, body: { error: 'not_this_session', retry_ms: 300_000 } }])
    await $.session.start(start)
    await w.clock.settle()
    await w.clock.advance(299_999)
    expect(w.requests.length).toBe(1)
    await w.clock.advance(1)
    expect(w.requests.length).toBe(2)
  })

  test('finds the node where the node itself looks', async ($, on) => {
    const w = world(on, [], { USERPROFILE: 'C:\\Users\\me', HOME: '/c/Users/me', PERSONA_HOME: 'persona-data' })
    await $.session.start(start)
    await w.clock.settle()
    expect(w.requests[0]?.socketPath).toBe('C:\\Users\\me\\persona-data\\node.sock')
  })

  test('passes PermissionRequest straight through with zero HTTP calls when unauthorized', async ($, on) => {
    const w = world(on, [{ status: 200, body: { messages: [], retry_ms: 60_000 } }])
    await $.session.start(start)
    await w.clock.settle()

    const out = await $.classic.PermissionRequest(permEvent() as never)
    expect(out).toEqual({})
    expect(w.requests.filter(r => r.url.endsWith('/permission'))).toEqual([])
  })

  test('reports classic hooks passed over after a main-loop turn uses tools without classic.PreToolUse', async ($, on) => {
    const w = world(
      on,
      [
        { status: 200, body: { messages: [], permissions: true, retry_ms: 60_000 } },
        { status: 200, body: { messages: [], permissions: false, retry_ms: 60_000 } },
      ],
      { HOME: '/home/me' },
      {
        stepToolUses: [{ tool_use_id: 'toolu_skipped', tool: 'Bash', input: { command: 'echo skipped' } }],
      },
    )
    await $.session.start(start)
    await w.clock.settle()

    const firstPoll = w.requests.find(r => r.url.endsWith('/poll'))
    expect(firstPoll?.body.classic_passed_over).toBeUndefined()

    await $.turn.start({ text: 'run a tool', turnId: 'turn-passed-over' } as never)
    const step = $.turn.step({ turnId: 'turn-passed-over', index: 0, model: 'claude-sonnet-5-5', messageCount: 1 })
    while (!(await step.next()).done) {
      // drain the step
    }
    await w.clock.advance(2_000)
    await w.clock.settle()

    expect(await $.classic.PermissionRequest(permEvent({ tool_use_id: 'toolu_skipped' }) as never)).toEqual({})
    expect(w.requests.filter(r => r.url.endsWith('/permission'))).toEqual([])

    const polls = w.requests.filter(r => r.url.endsWith('/poll'))
    expect(polls.length).toBe(2)
    expect(polls[1]?.body).toMatchObject({ classic_passed_over: true })
    await $.turn.complete({ turnId: 'turn-passed-over', answer: 'done' } as never)
  })

  test('keeps classic permission bridging when classic.PreToolUse reaches the plugin', async ($, on) => {
    const w = world(
      on,
      [
        { status: 200, body: { messages: [], permissions: true, retry_ms: 60_000 } },
        { status: 200, body: { messages: [], permissions: true, retry_ms: 60_000 } },
      ],
      { HOME: '/home/me' },
      {
        stepToolUses: [{ tool_use_id: 'toolu_present', tool: 'Bash', input: { command: 'echo present' } }],
        onPermission: () => ({
          status: 200,
          body: {
            action: 'decide',
            decision: {
              behavior: 'allow',
              updatedInput: { command: 'echo present' },
            },
          },
        }),
      },
    )
    await $.session.start(start)
    await w.clock.settle()

    await $.turn.start({ text: 'run a tool', turnId: 'turn-present' } as never)
    const step = $.turn.step({ turnId: 'turn-present', index: 0, model: 'claude-sonnet-5-5', messageCount: 1 })
    while (!(await step.next()).done) {
      // drain the step
    }
    await $.tool.call({
      tool: 'Bash',
      tool_use_id: 'toolu_present',
      command: 'echo present',
      description: 'print present',
    } as never)
    await $.turn.complete({ turnId: 'turn-present', answer: 'done' } as never)

    await w.clock.advance(60_000)
    await w.clock.settle()

    const polls = w.requests.filter(r => r.url.endsWith('/poll'))
    expect(polls.length).toBe(2)
    expect(polls[1]?.body.classic_passed_over).toBeUndefined()

    const out = await $.classic.PermissionRequest(
      permEvent({ tool_use_id: 'toolu_present', tool_input: { command: 'echo present', description: 'print present' } }) as never,
    )
    expect(out).toEqual({
      decision: {
        behavior: 'allow',
        updatedInput: { command: 'echo present' },
      },
    })
    expect(w.requests.filter(r => r.url.endsWith('/permission')).length).toBe(1)
  })

  test('pairs flat PreToolUse tool_use_id, renews long polls with stable req_id, and returns phone allow/deny decisions', async ($, on) => {
    const w = world(
      on,
      [{ status: 200, body: { messages: [], permissions: true, retry_ms: 60_000 } }],
      { HOME: '/home/me' },
      {
        onPermission: (_body, call) => {
          if (call < 3) return { status: 200, body: { action: 'renew' } }
          return {
            status: 200,
            body: {
              action: 'decide',
              decision: {
                behavior: 'allow',
                updatedInput: { command: 'echo hello', description: 'print greeting' },
                updatedPermissions: [{ type: 'addRules', rules: [{ toolName: 'Bash' }], behavior: 'allow', destination: 'session' }],
              },
            },
          }
        },
      },
    )
    await $.session.start(start)
    await w.clock.settle()

    // Flat PreToolUse structure from Claude Code 2.1.330 with different key order than PermissionRequest.
    await $.tool.call({
      tool: 'Bash',
      tool_use_id: 'toolu_bash_1',
      description: 'print greeting',
      command: 'echo hello',
    } as never)

    const out = await $.classic.PermissionRequest(permEvent() as never)
    expect(out).toEqual({
      decision: {
        behavior: 'allow',
        updatedInput: { command: 'echo hello', description: 'print greeting' },
        updatedPermissions: [{ type: 'addRules', rules: [{ toolName: 'Bash' }], behavior: 'allow', destination: 'session' }],
      },
    })

    const permReqs = w.requests.filter(r => r.url.endsWith('/permission'))
    expect(permReqs.length).toBe(3)
    const reqId = permReqs[0]?.body.req_id
    expect(typeof reqId).toBe('string')
    expect(permReqs.every(r => r.body.req_id === reqId)).toBe(true)
    expect(permReqs[0]?.body.tool_use_id).toBe('toolu_bash_1')
    expect(permReqs[0]?.body.wait_ms).toBe(25_000)

    // Subsequent PostToolUse for the already-decided tool does not call /permission-done.
    await $.classic.PostToolUse({ tool_use_id: 'toolu_bash_1', tool_name: 'Bash', tool_input: { command: 'echo hello' } } as never)
    expect(w.requests.filter(r => r.url.endsWith('/permission-done'))).toEqual([])
  })

  test('lets a fast local hook decide during the grace window with zero /permission calls', async ($, on) => {
    const w = world(
      on,
      [{ status: 200, body: { messages: [], permissions: true, retry_ms: 60_000 } }],
      { HOME: '/home/me' },
      {
        corePermission: () => ({ decision: { behavior: 'allow' } }),
      },
    )
    await $.session.start(start)
    await w.clock.settle()

    const out = await $.classic.PermissionRequest(permEvent() as never)
    expect(out).toEqual({ decision: { behavior: 'allow' } })
    expect(w.requests.filter(r => r.url.endsWith('/permission'))).toEqual([])
  })

  test('races a slow local hook against /permission and withdraws the card if the local hook later decides', async ($, on) => {
    let finishCore!: (val: unknown) => void
    let releasePerm!: (ans: Answer) => void
    const w = world(
      on,
      [{ status: 200, body: { messages: [], permissions: true, retry_ms: 60_000 } }],
      { HOME: '/home/me' },
      {
        corePermission: () =>
          new Promise(resolve => {
            finishCore = resolve
          }),
        onPermission: () =>
          new Promise<Answer>(resolve => {
            releasePerm = resolve
          }),
      },
    )
    await $.session.start(start)
    await w.clock.settle()

    await $.tool.call({ tool: 'Bash', tool_use_id: 'toolu_slow', command: 'echo hello', description: 'print greeting' } as never)
    const permP = $.classic.PermissionRequest(permEvent() as never)
    await w.clock.settle()
    expect(w.requests.filter(r => r.url.endsWith('/permission')).length).toBe(0)

    await w.clock.advance(500)
    await w.clock.settle()
    expect(w.requests.filter(r => r.url.endsWith('/permission')).length).toBe(1)

    // Slow local hook decides before the phone answers.
    finishCore({ decision: { behavior: 'allow' } })
    const out = await permP
    await w.clock.settle()
    releasePerm({ status: 200, body: { action: 'passthrough' } })

    expect(out).toEqual({ decision: { behavior: 'allow' } })
    const dones = w.requests.filter(r => r.url.endsWith('/permission-done'))
    expect(dones.length).toBe(1)
    expect(dones[0]?.body.tool_use_id).toBe('toolu_slow')
    expect(dones[0]?.body.reason).toBe('local_hook')
  })

  test('withdraws the card immediately via /permission-done when computer answers first (PostToolUse) and isolates concurrent requests', async ($, on) => {
    const pending = new Map<string, (ans: Answer) => void>()
    const w = world(
      on,
      [{ status: 200, body: { messages: [], permissions: true, retry_ms: 60_000 } }],
      { HOME: '/home/me' },
      {
        onPermission: body =>
          new Promise<Answer>(resolve => {
            pending.set(String(body.tool_use_id), resolve)
          }),
      },
    )
    await $.session.start(start)
    await w.clock.settle()

    // Two concurrent distinct commands (Also waiting) each pair their exact tool_use_id.
    await $.tool.call({ tool: 'Bash', tool_use_id: 'toolu_first', command: 'echo one', description: 'first' } as never)
    await $.tool.call({ tool: 'Bash', tool_use_id: 'toolu_second', command: 'echo two', description: 'second' } as never)

    const p1 = $.classic.PermissionRequest(permEvent({ tool_input: { command: 'echo one', description: 'first' } }) as never)
    const p2 = $.classic.PermissionRequest(permEvent({ tool_input: { command: 'echo two', description: 'second' } }) as never)
    await w.clock.settle()
    expect(pending.has('toolu_first')).toBe(true)
    expect(pending.has('toolu_second')).toBe(true)

    // Computer allows toolu_first -> PostToolUse cancels only toolu_first.
    await $.classic.PostToolUse({ session_id: SESSION, tool_use_id: 'toolu_first', tool_name: 'Bash', tool_input: { command: 'echo one', description: 'first' } } as never)
    expect(await p1).toEqual({})
    await w.clock.settle()
    const dones = w.requests.filter(r => r.url.endsWith('/permission-done'))
    expect(dones.length).toBe(1)
    expect(dones[0]?.body.tool_use_id).toBe('toolu_first')
    expect(dones[0]?.body.reason).toBe('post_tool_use')

    // Phone denies toolu_second while it is still active.
    pending.get('toolu_second')?.({
      status: 200,
      body: { action: 'decide', decision: { behavior: 'deny', message: 'denied from phone', interrupt: true } },
    })
    pending.get('toolu_first')?.({ status: 200, body: { action: 'passthrough' } })
    expect(await p2).toEqual({
      decision: { behavior: 'deny', message: 'denied from phone', interrupt: true },
    })
  })

  test('does not guess tool_use_id for ambiguous concurrent identical calls and isolates PreToolUse by agent_id', async ($, on) => {
    const pending: { body: Record<string, unknown>; resolve: (ans: Answer) => void }[] = []
    const w = world(
      on,
      [{ status: 200, body: { messages: [], permissions: true, retry_ms: 60_000 } }],
      { HOME: '/home/me' },
      {
        onPermission: body =>
          new Promise<Answer>(resolve => {
            pending.push({ body, resolve })
          }),
      },
    )
    await $.session.start(start)
    await w.clock.settle()

    // Two overlapping identical calls on the same main agent -> ambiguous: leave tool_use_id unset rather than guessing.
    await $.tool.call({ tool: 'Bash', tool_use_id: 'toolu_dup_1', command: 'echo same' } as never)
    await $.tool.call({ tool: 'Bash', tool_use_id: 'toolu_dup_2', command: 'echo same' } as never)
    // Identical command on a distinct subagent stays isolated and keeps its exact tool_use_id.
    await $.tool.call({ tool: 'Bash', tool_use_id: 'toolu_sub_same', agent_id: 'sub-a', command: 'echo same' } as never)

    const p1 = $.classic.PermissionRequest(permEvent({ tool_input: { command: 'echo same' } }) as never)
    const p2 = $.classic.PermissionRequest(permEvent({ tool_input: { command: 'echo same' } }) as never)
    const pSub = $.classic.PermissionRequest(permEvent({ tool_input: { command: 'echo same' }, agent_id: 'sub-a' }) as never)
    await w.clock.settle()

    expect(pending.length).toBe(3)
    expect(pending[0]?.body.tool_use_id).toBe(undefined)
    expect(pending[1]?.body.tool_use_id).toBe(undefined)
    expect(pending[2]?.body.tool_use_id).toBe('toolu_sub_same')

    // Completing toolu_dup_1 must NOT withdraw either ambiguous main card via fallback guessing.
    await $.classic.PostToolUse({ session_id: SESSION, tool_use_id: 'toolu_dup_1', tool_name: 'Bash', tool_input: { command: 'echo same' } } as never)
    await w.clock.settle()
    expect(w.requests.filter(r => r.url.endsWith('/permission-done'))).toEqual([])

    // Phone approves p1 first (removing p1 from activePerms so only p2 remains), then PostToolUse fires for call 1:
    // because p2 is marked ambiguous, handleToolDone must NOT mis-withdraw p2 by toolKey fallback!
    pending[0]?.resolve({ status: 200, body: { action: 'decide', decision: { behavior: 'allow', updatedInput: { command: 'echo same' } } } })
    expect(await p1).toEqual({ decision: { behavior: 'allow', updatedInput: { command: 'echo same' } } })
    await $.classic.PostToolUse({ session_id: SESSION, tool_use_id: 'toolu_dup_1', tool_name: 'Bash', tool_input: { command: 'echo same' } } as never)
    await w.clock.settle()
    expect(w.requests.filter(r => r.url.endsWith('/permission-done'))).toEqual([])

    // Completing toolu_sub_same cancels ONLY the subagent card.
    await $.classic.PostToolUse({ session_id: SESSION, tool_use_id: 'toolu_sub_same', agent_id: 'sub-a', tool_name: 'Bash', tool_input: { command: 'echo same' } } as never)
    expect(await pSub).toEqual({})
    await w.clock.settle()
    const dones = w.requests.filter(r => r.url.endsWith('/permission-done'))
    expect(dones.length).toBe(1)
    expect(dones[0]?.body.tool_use_id).toBe('toolu_sub_same')

    // p2 is still alive and can still be approved from the phone.
    pending[1]?.resolve({ status: 200, body: { action: 'decide', decision: { behavior: 'allow', updatedInput: { command: 'echo same' } } } })
    pending[2]?.resolve({ status: 200, body: { action: 'passthrough' } })
    expect(await p2).toEqual({ decision: { behavior: 'allow', updatedInput: { command: 'echo same' } } })
  })

  test('retains a 2-minute reconnect window for already-registered requests during node restarts and keeps socket path for /permission-done', async ($, on) => {
    let calls = 0
    const w = world(
      on,
      [
        { status: 200, body: { messages: [], permissions: true, retry_ms: 1_000 } },
        // Second /poll fails -> clears permissionAuth while the card is active.
        { fail: true },
      ],
      { HOME: '/home/me' },
      {
        onPermission: () => {
          calls++
          if (calls === 1) return { status: 200, body: { action: 'renew' } }
          if (calls <= 12) return { fail: true } // 11 transient failures (~11 s node restart)
          return { status: 200, body: { action: 'decide', decision: { behavior: 'allow', updatedInput: { command: 'echo survive' } } } }
        },
      },
    )
    await $.session.start(start)
    await w.clock.settle()

    const permP = $.classic.PermissionRequest(permEvent({ tool_input: { command: 'echo survive' } }) as never)
    await w.clock.settle()
    // Advance across 11 seconds of node restart (> initial 3-error limit, well under 2-minute reconnect window).
    for (let i = 0; i < 12; i++) {
      await w.clock.advance(1_000)
      await w.clock.settle()
    }
    expect(await permP).toEqual({
      decision: { behavior: 'allow', updatedInput: { command: 'echo survive' } },
    })
    expect(calls).toBe(13)
  })

  test('still delivers /permission-done using the request socket even after /poll fails and clears permissionAuth', async ($, on) => {
    let releasePerm!: (ans: Answer) => void
    const w = world(
      on,
      [
        { status: 200, body: { messages: [], permissions: true, retry_ms: 100 } },
        { fail: true }, // clears permissionAuth after 100 ms
      ],
      { HOME: '/home/me' },
      {
        onPermission: () =>
          new Promise<Answer>(resolve => {
            releasePerm = resolve
          }),
      },
    )
    await $.session.start(start)
    await w.clock.settle()

    await $.tool.call({ tool: 'Bash', tool_use_id: 'toolu_offline_poll', command: 'echo hi' } as never)
    const permP = $.classic.PermissionRequest(permEvent({ tool_input: { command: 'echo hi' } }) as never)
    await w.clock.settle()

    // Let the second /poll fail and clear permissionAuth.
    await w.clock.advance(100)
    await w.clock.settle()

    // Computer answers -> PostToolUse must still send /permission-done to the request's captured socketPath.
    await $.classic.PostToolUse({ session_id: SESSION, tool_use_id: 'toolu_offline_poll', tool_name: 'Bash', tool_input: { command: 'echo hi' } } as never)
    expect(await permP).toEqual({})
    releasePerm({ status: 200, body: { action: 'passthrough' } })
    await w.clock.settle()

    const dones = w.requests.filter(r => r.url.endsWith('/permission-done'))
    expect(dones.length).toBe(1)
    expect(dones[0]?.socketPath).toBe('/home/me/.persona/node.sock')
    expect(dones[0]?.body.tool_use_id).toBe('toolu_offline_poll')
  })

  test('falls back to passthrough when a connected request stays disconnected for the full 2-minute reconnect window', async ($, on) => {
    let calls = 0
    const w = world(
      on,
      [{ status: 200, body: { messages: [], permissions: true, retry_ms: 60_000 } }],
      { HOME: '/home/me' },
      {
        onPermission: () => {
          calls++
          if (calls === 1) return { status: 200, body: { action: 'renew' } }
          return { fail: true }
        },
      },
    )
    await $.session.start(start)
    await w.clock.settle()

    const permP = $.classic.PermissionRequest(permEvent() as never)
    await w.clock.settle()
    for (let i = 0; i < 121; i++) {
      await w.clock.advance(1_000)
      await w.clock.settle()
    }
    expect(await permP).toEqual({})
    expect(calls).toBeGreaterThanOrEqual(120)
  })

  test('cancels pending main-turn requests on turn.complete without cancelling an active subagent request', async ($, on) => {
    const pending = new Map<string, (ans: Answer) => void>()
    const w = world(
      on,
      [{ status: 200, body: { messages: [], permissions: true, retry_ms: 60_000 } }],
      { HOME: '/home/me' },
      {
        onPermission: body =>
          new Promise<Answer>(resolve => {
            pending.set(String(body.tool_use_id), resolve)
          }),
      },
    )
    await $.session.start(start)
    await w.clock.settle()

    await $.tool.call({ tool: 'Bash', tool_use_id: 'toolu_main', command: 'echo main' } as never)
    await $.tool.call({ tool: 'Bash', tool_use_id: 'toolu_sub', agent_id: 'sub-1', command: 'echo sub' } as never)
    const pMain = $.classic.PermissionRequest(permEvent({ tool_input: { command: 'echo main' } }) as never)
    const pSub = $.classic.PermissionRequest(permEvent({ tool_input: { command: 'echo sub' }, agent_id: 'sub-1' }) as never)
    await w.clock.settle()

    await $.turn.complete({ turnId: 'turn-1', answer: 'done' } as never)
    expect(await pMain).toEqual({})
    await w.clock.settle()

    const dones = w.requests.filter(r => r.url.endsWith('/permission-done'))
    expect(dones.length).toBe(1)
    expect(dones[0]?.body.reason).toBe('turn_complete')
    expect(Array.isArray(dones[0]?.body.req_ids)).toBe(true)
    expect((dones[0]?.body.req_ids as string[]).length).toBe(1)

    // Subagent request is unaffected and can still be approved from the phone.
    pending.get('toolu_sub')?.({
      status: 200,
      body: { action: 'decide', decision: { behavior: 'allow', updatedInput: { command: 'echo sub' } } },
    })
    pending.get('toolu_main')?.({ status: 200, body: { action: 'passthrough' } })
    expect(await pSub).toEqual({
      decision: { behavior: 'allow', updatedInput: { command: 'echo sub' } },
    })
  })

  test('withdraws card on PermissionDenied and bridges AskUserQuestion and ExitPlanMode decisions', async ($, on) => {
    let releaseDenied!: (ans: Answer) => void
    const w = world(
      on,
      [{ status: 200, body: { messages: [], permissions: true, retry_ms: 60_000 } }],
      { HOME: '/home/me' },
      {
        onPermission: body => {
          if (body.tool_name === 'AskUserQuestion') {
            return {
              status: 200,
              body: {
                action: 'decide',
                decision: {
                  behavior: 'allow',
                  updatedInput: { ...(body.tool_input as Record<string, unknown>), answers: { 'Color?': 'Crimson' } },
                },
              },
            }
          }
          if (body.tool_name === 'ExitPlanMode') {
            return {
              status: 200,
              body: {
                action: 'decide',
                decision: {
                  behavior: 'allow',
                  updatedInput: body.tool_input as Record<string, unknown>,
                  updatedPermissions: [{ type: 'setMode', mode: 'acceptEdits', destination: 'session' }],
                },
              },
            }
          }
          return new Promise<Answer>(resolve => {
            releaseDenied = resolve
          })
        },
      },
    )
    await $.session.start(start)
    await w.clock.settle()

    // AskUserQuestion allow + updatedInput.answers.
    await $.tool.call({ tool: 'AskUserQuestion', tool_use_id: 'toolu_ask', questions: [{ question: 'Color?' }] } as never)
    const askOut = await $.classic.PermissionRequest(
      permEvent({ tool_name: 'AskUserQuestion', tool_input: { questions: [{ question: 'Color?' }] } }) as never,
    )
    expect(askOut).toEqual({
      decision: {
        behavior: 'allow',
        updatedInput: { questions: [{ question: 'Color?' }], answers: { 'Color?': 'Crimson' } },
      },
    })

    // ExitPlanMode allow + updatedInput + setMode.
    const planOut = await $.classic.PermissionRequest(
      permEvent({ tool_name: 'ExitPlanMode', tool_input: { plan: 'Step 1' } }) as never,
    )
    expect(planOut).toEqual({
      decision: {
        behavior: 'allow',
        updatedInput: { plan: 'Step 1' },
        updatedPermissions: [{ type: 'setMode', mode: 'acceptEdits', destination: 'session' }],
      },
    })

    // PermissionDenied cancels active request and sends /permission-done.
    await $.tool.call({ tool: 'Bash', tool_use_id: 'toolu_denied', command: 'rm -rf /' } as never)
    const deniedP = $.classic.PermissionRequest(permEvent({ tool_input: { command: 'rm -rf /' } }) as never)
    await w.clock.settle()
    await $.classic.PermissionDenied({ session_id: SESSION, tool_use_id: 'toolu_denied', tool_name: 'Bash' } as never)
    expect(await deniedP).toEqual({})
    releaseDenied({ status: 200, body: { action: 'passthrough' } })
    await w.clock.settle()
    const dones = w.requests.filter(r => r.url.endsWith('/permission-done'))
    expect(dones.length).toBe(1)
    expect(dones[0]?.body.tool_use_id).toBe('toolu_denied')
    expect(dones[0]?.body.reason).toBe('permission_denied')
  })

  test('falls back to passthrough after repeated transient fetch failures on /permission', async ($, on) => {
    const w = world(
      on,
      [{ status: 200, body: { messages: [], permissions: true, retry_ms: 60_000 } }],
      { HOME: '/home/me' },
      {
        onPermission: () => ({ fail: true }),
      },
    )
    await $.session.start(start)
    await w.clock.settle()

    const permP = $.classic.PermissionRequest(permEvent() as never)
    await w.clock.settle()
    await w.clock.advance(1_000)
    await w.clock.settle()
    await w.clock.advance(1_000)
    await w.clock.settle()

    expect(await permP).toEqual({})
    expect(w.requests.filter(r => r.url.endsWith('/permission')).length).toBe(3)
  })
})

describe('protocol helpers', () => {
  test('socket path', () => {
    expect(socketPath('/home/me', undefined)).toBe('/home/me/.persona/node.sock')
    expect(socketPath('/home/me', '~')).toBe('/home/me/node.sock')
    expect(socketPath('/home/me', '~/state')).toBe('/home/me/state/node.sock')
    expect(socketPath('/home/me', '/srv/persona')).toBe('/srv/persona/node.sock')
    expect(socketPath('C:\\Users\\me', undefined)).toBe('C:\\Users\\me\\.persona\\node.sock')
    expect(socketPath('C:\\Users\\me', 'D:\\persona')).toBe('D:\\persona\\node.sock')
    expect(socketPath(undefined, '/srv/persona')).toBe(undefined)
    expect(homeDir('C:\\Users\\me', '/c/Users/me')).toBe('C:\\Users\\me')
    expect(homeDir(undefined, '/Users/me')).toBe('/Users/me')
  })

  test('poll answers and override helpers', () => {
    expect(readPoll(200, '{"messages":[{"id":"a","text":"hi","deadline_ms":5}],"interrupt":{"id":"i1","deadline_ms":9},"settings":{"id":"s1","model":"opus","effort":"high"},"permissions":true,"retry_ms":10}')).toEqual({
      kind: 'messages',
      messages: [{ id: 'a', text: 'hi', deadlineMs: 5 }],
      interrupt: { id: 'i1', deadlineMs: 9 },
      settings: { id: 's1', model: 'opus', effort: 'high' },
      permissions: true,
      retryMs: 10,
    })
    expect(readPoll(200, '{"messages":[],"settings":{"id":"s2","model":"-bad"}}')).toEqual({
      kind: 'messages',
      messages: [],
      permissions: false,
      retryMs: 0,
    })
    const ov = mergeOverride(undefined, SESSION, { id: 's1', model: 'opus', effort: 'auto' }, 'claude-sonnet-5-5')
    expect(applyStepOverride({ model: 'claude-sonnet-5-5', effort: 'medium' }, ov, SESSION, 'claude-sonnet-5-5').step).toEqual({
      model: 'opus',
    })
    expect(readPoll(200, 'not json')).toEqual({ kind: 'unavailable' })
    expect(readPoll(404, '')).toEqual({ kind: 'unavailable' })
    expect(readPoll(403, '{}')).toEqual({ kind: 'refused', retryMs: 60_000 })
    expect(verdict({ id: 'a', text: 'x'.repeat(200_001), deadlineMs: 10 }, 0)).toBe('invalid')
    expect(nextBackoff(0)).toBe(2_000)
    expect(nextBackoff(40_000)).toBe(60_000)
  })

  test('permission helpers and PreToolUse FIFO cache', () => {
    expect(readPermissionAnswer(200, '{"action":"renew"}')).toEqual({ action: 'renew' })
    expect(readPermissionAnswer(200, '{"action":"passthrough"}')).toEqual({ action: 'passthrough' })
    expect(readPermissionAnswer(403, '{}')).toEqual({ action: 'refused' })
    expect(readPermissionAnswer(404, '{}')).toEqual({ action: 'passthrough' })
    expect(readPermissionAnswer(500, '')).toEqual({ action: 'unavailable' })
    expect(readPermissionAnswer(200, '{"action":"decide","decision":{"behavior":"allow","updatedInput":{"a":1}}}')).toEqual({
      action: 'decide',
      decision: { behavior: 'allow', updatedInput: { a: 1 } },
    })
    expect(readPermissionAnswer(200, '{"action":"decide","decision":{"behavior":"deny","message":"no","interrupt":true}}')).toEqual({
      action: 'decide',
      decision: { behavior: 'deny', message: 'no', interrupt: true },
    })
    expect(hasPermissionDecision({ decision: { behavior: 'allow' } })).toBe(true)
    expect(hasPermissionDecision({})).toBe(false)
    expect(canonicalJson({ b: 2, a: 1 })).toBe('{"a":1,"b":2}')

    expect(extractPreToolUse({ tool: 'Bash', tool_use_id: 't1', agent_id: 'sub-1', command: 'ls' })).toEqual({
      agentId: 'sub-1',
      toolName: 'Bash',
      toolUseId: 't1',
      toolInput: { command: 'ls' },
    })
    const cache = createPreToolCache()
    // Sequential identical calls pair cleanly once the first completes.
    recordPreTool(cache, { tool: 'Bash', tool_use_id: 't1', command: 'ls' })
    expect(takePreTool(cache, 'Bash', { command: 'ls' })).toEqual({ id: 't1', ambiguous: false })
    markToolCompleted(cache, 't1')
    expect(isToolCompleted(cache, 't1')).toBe(true)
    recordPreTool(cache, { tool: 'Bash', tool_use_id: 't2', command: 'ls' })
    expect(takePreTool(cache, 'Bash', { command: 'ls' })).toEqual({ id: 't2', ambiguous: false })

    // Concurrent identical calls on the same agent mark ambiguous and refuse to guess.
    recordPreTool(cache, { tool: 'Bash', tool_use_id: 't3', command: 'pwd' })
    recordPreTool(cache, { tool: 'Bash', tool_use_id: 't4', command: 'pwd' })
    recordPreTool(cache, { tool: 'Bash', tool_use_id: 't_sub', agent_id: 'sub-1', command: 'pwd' })
    expect(takePreTool(cache, 'Bash', { command: 'pwd' })).toEqual({ id: '', ambiguous: true })
    expect(takePreTool(cache, 'Bash', { command: 'pwd' }, 'sub-1')).toEqual({ id: 't_sub', ambiguous: false })
    clearAgentPreTools(cache, '')
    recordPreTool(cache, { tool: 'Bash', tool_use_id: 't5', command: 'pwd' })
    expect(takePreTool(cache, 'Bash', { command: 'pwd' })).toEqual({ id: 't5', ambiguous: false })
  })

  test('classic hook observation', () => {
    let observed = createClassicHookObservation()
    observed = noteTurnStepToolUses(observed, undefined, { toolUses: [1, 2] })
    expect(observed.toolUses).toBe(2)
    observed = finishClassicTurn(observed)
    expect(observed.passedOver).toBe(true)
    expect(observed.toolUses).toBe(0)

    let present = createClassicHookObservation()
    present = noteTurnStepToolUses(present, undefined, { toolUses: [1] })
    present = noteClassicPreToolUse(present, { tool_use_id: 't1' })
    present = finishClassicTurn(present)
    expect(present.passedOver).toBe(false)

    let subagent = createClassicHookObservation()
    subagent = noteTurnStepToolUses(subagent, 'sub-1', { toolUses: [1] })
    subagent = noteClassicPreToolUse(subagent, { tool_use_id: 't2', agent_id: 'sub-1' })
    subagent = finishClassicTurn(subagent)
    expect(subagent.passedOver).toBe(false)
  })
})
