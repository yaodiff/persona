import assert from 'node:assert/strict'
import { test } from 'node:test'
import { AgyAcpClient } from '../lib/acp.js'
import { isAuthenticationRequired } from '../lib/errors.js'
import { NativeOAuthRecovery, isNativeOAuthChannel, normalizeChannel } from '../lib/pool.js'

const native = { id: 'native', authMode: 'oauth', keyMode: false, env: null }
const authError = { code: -32000, message: 'Authentication required', data: { private: 'sensitive-provider-payload' } }
const deferred = () => Promise.withResolvers()

// In-memory JSON-RPC transport: exercise real pending calls, errors, notifications and timeouts, without credentials or a CLI.
function fixture(t, { spec = native, advertised = true, recovery = new NativeOAuthRecovery(), handle, authMethod = '' } = {}) {
  const calls = [], logs = []
  const client = new AgyAcpClient({ channelSpec: spec, authRecovery: recovery, authMethod, log: text => logs.push(text) })
  let kills = 0
  client.ensureStarted = async () => {
    if (client.closed) throw new Error('stopped')
    if (!client.proc) client.proc = { stdin: { writable: true } }
    client.authMethods = advertised ? [{ id: 'oauth-personal' }] : [{ id: 'gemini-api-key' }]
  }
  client._kill = async () => { kills++ }
  client._send = msg => {
    const child = client.proc
    calls.push(msg)
    if (msg.id === undefined) return
    const reply = (result = {}) => client._onMessage({ id: msg.id, result }, child)
    const reject = (error = authError) => client._onMessage({ id: msg.id, error }, child)
    const update = (sessionId = msg.params?.sessionId, sessionUpdate = 'agent_message_chunk') => client._onMessage({
      method: 'session/update', params: { sessionId, update: { sessionUpdate, content: { type: 'text', text: 'partial' } } },
    }, child)
    queueMicrotask(() => handle({ msg, reply, reject, update, client, child }))
  }
  t.after(() => client.stop())
  return { client, calls, logs, count: method => calls.filter(call => call.method === method).length, kills: () => kills }
}

for (const method of ['session/new', 'session/resume', 'session/load', 'session/list', 'session/set_model', 'session/close', 'session/prompt']) {
  test(`native OAuth refresh safely retries ${method} once`, async t => {
    let authed = false
    const f = fixture(t, { handle({ msg, reply, reject }) {
      if (msg.method === 'authenticate') { assert.deepEqual(msg.params, { methodId: 'oauth-personal' }); authed = true; reply({ _meta: { antigravity: { tier: 'test-tier' } } }) }
      else if (!authed) reject()
      else reply({ sessionId: 'mock-session', stopReason: 'end_turn' })
    } })
    f.client.loadedSessions.set('existing', { mcpKey: '[]' })
    const result = await f.client.call(method, { sessionId: 'mock-session' }, method === 'session/prompt' ? 0 : 1000)
    assert.equal(result.sessionId, 'mock-session')
    assert.equal(f.count(method), 2)
    assert.equal(f.count('authenticate'), 1)
    assert.equal(f.client.loadedSessions.has('existing'), true, 'refresh must preserve existing session state')
    assert.equal(f.kills(), 0)
    assert.deepEqual(f.logs.map(JSON.parse).map(row => row.outcome), ['started', 'succeeded'])
    assert.ok(!f.logs.join().includes('test-tier'))
  })
}

test('parallel requests on one connection singleflight authentication', async t => {
  const release = deferred(), entered = deferred()
  let authed = false
  const f = fixture(t, { handle({ msg, reply, reject }) {
    if (msg.method === 'authenticate') { entered.resolve(); release.promise.then(() => { authed = true; reply() }) }
    else authed ? reply() : reject()
  } })
  const requests = Array.from({ length: 12 }, (_, i) => f.client.call('session/resume', { sessionId: `session-${i}` }))
  await entered.promise
  assert.equal(f.count('authenticate'), 1)
  release.resolve()
  await Promise.all(requests)
  assert.equal(f.count('authenticate'), 1)
  assert.equal(f.count('session/resume'), 24)
})

test('dedicated connections in the same pool share a flight and late stale errors do not refresh twice', async t => {
  const recovery = new NativeOAuthRecovery(), entered = deferred(), release = deferred(), late = deferred()
  let authed = false
  const handle = ({ msg, reply, reject }) => {
    if (msg.method === 'authenticate') { entered.resolve(); release.promise.then(() => { authed = true; reply() }) }
    else if (!authed && msg.params.sessionId === 'late') late.promise.then(() => reject())
    else authed ? reply() : reject()
  }
  const a = fixture(t, { recovery, handle }), b = fixture(t, { recovery, handle })
  const first = a.client.call('session/new', { sessionId: 'first' })
  const stale = b.client.call('session/resume', { sessionId: 'late' })
  await entered.promise
  release.resolve()
  await first
  late.resolve()
  await stale
  assert.equal(a.count('authenticate') + b.count('authenticate'), 1)
  assert.equal(b.count('session/resume'), 2)
})

test('separate pools refresh independently', async t => {
  const make = () => {
    let authed = false
    return fixture(t, { handle({ msg, reply, reject }) {
      if (msg.method === 'authenticate') { authed = true; reply() } else authed ? reply() : reject()
    } })
  }
  const a = make(), b = make()
  await Promise.all([a.client.call('session/new', {}), b.client.call('session/new', {})])
  assert.equal(a.count('authenticate'), 1)
  assert.equal(b.count('authenticate'), 1)
})

test('a successful authenticate followed by another auth error stops after one retry and cools down', async t => {
  const f = fixture(t, { handle: ({ msg, reply, reject }) => msg.method === 'authenticate' ? reply() : reject() })
  await assert.rejects(f.client.call('session/new', {}), error => error.authRecovery === 'retry_exhausted' && !error.data)
  await assert.rejects(f.client.call('session/new', {}), error => error.authRecovery === 'cooldown')
  assert.equal(f.count('authenticate'), 1)
  assert.equal(f.count('session/new'), 3)
})

test('authentication failure is sanitized, shared with waiters and blocked until cooldown expires', async t => {
  let now = 100, failed = true
  const recovery = new NativeOAuthRecovery({ now: () => now, cooldownMs: 60_000 })
  const f = fixture(t, { recovery, handle({ msg, reply, reject }) {
    if (msg.method === 'authenticate') failed ? reject({ code: 'private-code', message: 'sensitive-provider-payload', data: authError.data }) : reply()
    else failed ? reject() : reply()
  } })
  const results = await Promise.allSettled(Array.from({ length: 5 }, () => f.client.call('session/new', {})))
  for (const result of results) {
    assert.equal(result.status, 'rejected')
    assert.equal(result.reason.authRecovery, 'failed')
    assert.equal(result.reason.code, 'auth_required')
    assert.equal(result.reason.data, undefined)
    assert.ok(!result.reason.message.includes('sensitive-provider-payload'))
  }
  const before = f.calls.length
  await assert.rejects(f.client.call('session/new', {}), error => error.authRecovery === 'cooldown')
  assert.equal(f.calls.length, before + 1, 'cooldown must reject without another authentication or delayed retry')
  assert.equal(f.count('authenticate'), 1)
  now += 60_001
  failed = false
  // Force an immediate auth rejection on the next call to exercise recovery after the cooldown.
  const original = f.client._send.bind(f.client)
  let next = true
  f.client._send = msg => {
    if (next && msg.method === 'session/new') {
      next = false
      f.calls.push(msg)
      queueMicrotask(() => f.client._onMessage({ id: msg.id, error: authError }, f.client.proc))
    } else original(msg)
  }
  await f.client.call('session/new', {})
  assert.equal(f.count('authenticate'), 2)
  assert.ok(!f.logs.join().includes('sensitive-provider-payload'))
})

test('authentication timeout terminates the potentially interactive process and cooldown survives restart', async t => {
  const f = fixture(t, { recovery: new NativeOAuthRecovery({ timeoutMs: 30 }), handle({ msg, reject }) {
    if (msg.method !== 'authenticate') reject()
  } })
  await assert.rejects(f.client.call('session/new', {}), error => error.authRecovery === 'timeout')
  assert.equal(f.kills(), 1)
  assert.equal(f.client.proc, null)
  assert.equal(f.client.pending.size, 0)
  assert.deepEqual(f.client.authMethods, [])
  await assert.rejects(f.client.call('session/new', {}), error => error.authRecovery === 'cooldown')
  assert.equal(f.count('authenticate'), 1)
  assert.equal(f.count('session/new'), 2)
})

for (const activity of ['agent_message_chunk', 'agent_thought_chunk', 'tool_call', 'usage_update', 'permission', 'unscoped-request']) {
  test(`prompt with ${activity} is never replayed`, async t => {
    const f = fixture(t, { handle({ msg, reject, update, client, child }) {
      if (msg.method !== 'session/prompt') throw new Error('must not authenticate')
      if (activity === 'permission' || activity === 'unscoped-request') client._onMessage({
        id: 'server-request', method: 'unknown/request', params: activity === 'permission' ? { sessionId: msg.params.sessionId } : {},
      }, child)
      else update(msg.params.sessionId, activity)
      reject()
    } })
    await assert.rejects(f.client.prompt('mock-session', []), error => error.authRecovery === 'prompt_started' && error.noPromptReplay && !error.data)
    assert.equal(f.count('session/prompt'), 1)
    assert.equal(f.count('authenticate'), 0)
  })
}

test('a delayed auth rejection without output is not considered safe to replay', async t => {
  t.mock.timers.enable({ apis: ['Date'], now: 100 })
  const f = fixture(t, { handle({ reject }) { t.mock.timers.tick(1001); reject() } })
  await assert.rejects(f.client.prompt('mock-session', []), error => error.authRecovery === 'prompt_started')
  assert.equal(f.count('authenticate'), 0)
})

test('an unrelated session update does not block a safely rejected prompt', async t => {
  let authed = false
  const f = fixture(t, { handle({ msg, reply, reject, update }) {
    if (msg.method === 'authenticate') { authed = true; reply() }
    else if (!authed) { update('other-session'); reject() } else reply()
  } })
  await f.client.prompt('mock-session', [])
  assert.equal(f.count('session/prompt'), 2)
})

for (const action of ['cancel', 'replace', 'stop']) {
  test(`${action} during authentication prevents prompt replay`, async t => {
    const f = fixture(t, { handle({ msg, reply, reject, client }) {
      if (msg.method !== 'authenticate') reject()
      else {
        if (action === 'cancel') client.notify('session/cancel', { sessionId: 'mock-session' })
        if (action === 'replace') {
          const old = client.proc
          reply()
          client._forget(old, new Error('replaced'))
          client.proc = { stdin: { writable: true } }
          return
        }
        if (action === 'stop') { client.stop(); return }
        reply()
      }
    } })
    await assert.rejects(f.client.prompt('mock-session', []), error => error.noPromptReplay === true)
    assert.equal(f.count('session/prompt'), 1)
    assert.equal(f.client.promptAttempts.size, 0)
  })
}

for (const spec of [
  { ...native, id: 'managed-gateway' },
  { ...native, id: 'managed-oauth' },
  { ...native, authMode: 'gemini-api-key', keyMode: true },
  { ...native, env: { GEMINI_API_KEY: 'mock-key' } },
  { ...native, env: { GOOGLE_GEMINI_BASE_URL: 'https://gateway.example.com' } },
  { ...native, env: { PERSONA_AGY_MODEL_PROVIDER: 'gemini' } },
  undefined,
]) {
  test(`non-native or overridden channel cannot authenticate with personal OAuth: ${JSON.stringify(spec)}`, async t => {
    // Explicitly replace the fixture default when testing an absent channel declaration.
    const f = fixture(t, { spec, handle: ({ reject }) => reject() })
    f.client.channelSpec = spec
    await assert.rejects(f.client.call('session/new', {}), /Authentication required/)
    assert.equal(f.count('authenticate'), 0)
    assert.equal(isNativeOAuthChannel(spec), false)
  })
}

test('no advertised oauth-personal method means no blind authentication', async t => {
  const f = fixture(t, { advertised: false, handle: ({ reject }) => reject() })
  await assert.rejects(f.client.call('session/new', {}), /Authentication required/)
  assert.equal(f.count('authenticate'), 0)
})

test('a configured API-key authentication method prevents OAuth recovery', async t => {
  const f = fixture(t, { authMethod: 'gemini-api-key', handle: ({ reject }) => reject() })
  await assert.rejects(f.client.call('session/new', {}), /Authentication required/)
  assert.equal(f.count('authenticate'), 0)
})

test('native pool classification rejects gateway overrides even when auth_mode claims OAuth', () => {
  for (const env of [{ GEMINI_API_KEY: 'mock-key' }, { GOOGLE_GEMINI_BASE_URL: 'https://gateway.example.com' }, { PERSONA_AGY_MODEL_PROVIDER: 'gemini' }]) {
    assert.equal(isNativeOAuthChannel(normalizeChannel({ id: 'native', auth_mode: 'oauth', env })), false)
  }
  assert.equal(isNativeOAuthChannel(native), true)
})

test('auth_required protocol codes and data discriminators are recognized without inspecting arbitrary payload text', () => {
  for (const error of [{ code: 'auth_required' }, { message: 'Authentication required' }, { data: { code: 'AUTH_REQUIRED' } }, { data: { reason: 'auth_required' } }, { data: { error: { code: 'auth_required' } } }]) assert.equal(isAuthenticationRequired(error), true)
  for (const error of [{ code: 401 }, { message: 'quota exhausted' }, { data: { private: 'Authentication required' } }]) assert.equal(isAuthenticationRequired(error), false)
})

test('an auth_required error code triggers recovery and unrelated errors retain their identity', async t => {
  let first = true
  const f = fixture(t, { handle({ msg, reply, reject }) {
    if (msg.method === 'authenticate') reply()
    else if (first) { first = false; reject({ code: 'auth_required', message: 'Unavailable' }) }
    else reply()
  } })
  await f.client.call('session/load', {})
  assert.equal(f.count('authenticate'), 1)
  const unrelated = fixture(t, { handle: ({ reject }) => reject({ code: 'quota_exhausted', message: 'quota exhausted' }) })
  await assert.rejects(unrelated.client.call('session/new', {}), error => error.code === 'quota_exhausted')
  assert.equal(unrelated.count('authenticate'), 0)
})


test('simultaneous dedicated connections in the same pool share the in-flight authentication', async t => {
  const recovery = new NativeOAuthRecovery(), entered = deferred(), release = deferred()
  let authed = false
  const handle = ({ msg, reply, reject }) => {
    if (msg.method === 'authenticate') { entered.resolve(); release.promise.then(() => { authed = true; reply() }) }
    else authed ? reply() : reject()
  }
  const a = fixture(t, { recovery, handle }), b = fixture(t, { recovery, handle })
  const first = a.client.call('session/resume', { sessionId: 'first' })
  const second = b.client.call('session/resume', { sessionId: 'second' })
  await entered.promise
  release.resolve()
  await Promise.all([first, second])
  assert.equal(a.count('authenticate') + b.count('authenticate'), 1)
  assert.equal(a.count('session/resume'), 2)
  assert.equal(b.count('session/resume'), 2)
})

test('late prompt activity while authentication is pending prevents replay', async t => {
  const f = fixture(t, { handle({ msg, reply, reject, update }) {
    if (msg.method !== 'authenticate') reject()
    else { update('mock-session'); reply() }
  } })
  await assert.rejects(f.client.prompt('mock-session', []), error => error.authRecovery === 'prompt_started')
  assert.equal(f.count('authenticate'), 1)
  assert.equal(f.count('session/prompt'), 1)
})


test('closeSession does not swallow authentication recovery failures', async t => {
  const f = fixture(t, { handle: ({ reject }) => reject() })
  await assert.rejects(f.client.closeSession('mock-session'), error => error.authRecovery === 'failed')
  assert.equal(f.count('authenticate'), 1)
})
