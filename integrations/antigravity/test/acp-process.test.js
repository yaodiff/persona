import assert from 'node:assert/strict'
import { once } from 'node:events'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, symlinkSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { basename, dirname, isAbsolute, join, relative } from 'node:path'
import { describe, it } from 'node:test'
import { fileURLToPath } from 'node:url'
import { AgyAcpClient } from '../lib/acp.js'

const FAKE = fileURLToPath(new URL('./fake-acp-process.mjs', import.meta.url))
const TIMEOUT = { timeout: 15_000 }

/** A client running the fake agy in `mode`; every process it spawned is killed when the test ends. */
function fakeClient(t, mode, options = {}) {
  const dir = mkdtempSync(join(tmpdir(), 'agy-acp-'))
  const pidFile = join(dir, 'pids')
  const logs = []
  const client = new AgyAcpClient({
    bin: process.execPath,
    args: [FAKE, pidFile, mode, join(dir, 'marker')],
    log: (m) => logs.push(m),
    ...options,
  })
  const pids = () => (existsSync(pidFile) ? readFileSync(pidFile, 'utf8').split('\n').filter(Boolean).map(Number) : [])
  const cwds = () => (existsSync(`${pidFile}.cwds`) ? readFileSync(`${pidFile}.cwds`, 'utf8').split('\n').filter(Boolean) : [])
  const configDirs = () => (existsSync(`${pidFile}.config-dirs`) ? readFileSync(`${pidFile}.config-dirs`, 'utf8').split('\n').filter(Boolean) : [])
  t.after(() => {
    const left = new Set([...pids(), client.proc?.pid].filter(Boolean))
    client.stop()
    for (const pid of left) {
      try {
        process.kill(pid, 'SIGKILL')
      } catch {}
    }
    rmSync(dir, { recursive: true, force: true })
  })
  return { client, logs, pids, cwds, configDirs }
}

function alive(pid) {
  try {
    process.kill(pid, 0)
    return true
  } catch (err) {
    return err.code !== 'ESRCH'
  }
}

async function waitFor(cond, ms, what) {
  const deadline = Date.now() + ms
  while (!cond()) {
    if (Date.now() > deadline) throw new Error(`timed out after ${ms}ms waiting for ${what}`)
    await new Promise((r) => setTimeout(r, 20))
  }
}

function watchUnhandled(t) {
  const seen = []
  const on = (err) => seen.push(err)
  process.on('unhandledRejection', on)
  process.on('uncaughtException', on)
  t.after(() => {
    process.off('unhandledRejection', on)
    process.off('uncaughtException', on)
  })
  return seen
}

describe('agy acp process lifecycle', () => {
  it('starts agy in the empty temporary launch directory', TIMEOUT, async (t) => {
    const { client, cwds } = fakeClient(t, 'ok')
    await client.ensureStarted()
    const { directory } = client.nativeLogs.get(client.proc)
    assert.deepEqual(cwds().map((cwd) => realpathSync(cwd)), [realpathSync(directory)])
  })

  it('resolves a path-like relative agy before changing the launch directory', TIMEOUT, async (t) => {
    const { client, cwds } = fakeClient(t, 'ok', { bin: relative(process.cwd(), process.execPath) })
    await client.ensureStarted()
    assert.equal(cwds().length, 1)
  })

  it('resolves a relative PATH entry before changing the launch directory', TIMEOUT, async (t) => {
    const { client, cwds } = fakeClient(t, 'ok', {
      bin: basename(process.execPath),
      env: { PATH: relative(process.cwd(), dirname(process.execPath)) },
    })
    await client.ensureStarted()
    assert.equal(cwds().length, 1)
  })

  it('preserves symlink and parent-directory executable resolution', TIMEOUT, async (t) => {
    const root = mkdtempSync(join(tmpdir(), 'agy-link-'))
    t.after(() => rmSync(root, { recursive: true, force: true }))
    const target = join(root, 'target')
    mkdirSync(join(target, 'inner'), { recursive: true })
    symlinkSync(process.execPath, join(target, 'node'))
    symlinkSync(join(target, 'inner'), join(root, 'link'))
    // The kernel follows link into target/inner before .., so this names target/node.
    // Lexically collapsing link/.. would instead name the nonexistent root/node.
    const bin = relative(process.cwd(), root) + '/link/../node'
    const { client, cwds } = fakeClient(t, 'ok', { bin })
    await client.ensureStarted()
    assert.equal(cwds().length, 1)
  })

  it('resolves a relative ACP config directory against the launcher cwd', TIMEOUT, async (t) => {
    const { client, configDirs } = fakeClient(t, 'echo', {
      env: { ANTIGRAVITY_CLI_CONFIG_DIR: 'state/config' },
    })
    await client.ensureStarted()
    assert.deepEqual(configDirs(), [join(process.cwd(), 'state/config')])
  })

  it('passes an absolute native log path when TMPDIR is relative', TIMEOUT, async (t) => {
    const temporary = mkdtempSync(join(tmpdir(), 'agy-tmp-root-'))
    const { client } = fakeClient(t, 'echo')
    const previous = process.env.TMPDIR
    process.env.TMPDIR = relative(process.cwd(), temporary)
    t.after(() => {
      if (previous === undefined) delete process.env.TMPDIR
      else process.env.TMPDIR = previous
      rmSync(temporary, { recursive: true, force: true })
    })
    await client.ensureStarted()
    const [{ directory, file }] = [...client.nativeLogs.values()]
    assert.ok(isAbsolute(directory), directory)
    assert.ok(isAbsolute(file), file)
    assert.equal(realpathSync(dirname(file)), realpathSync(directory))
  })

  it('a missing agy fails the start instead of crashing the process', TIMEOUT, async (t) => {
    const crashes = watchUnhandled(t)
    const client = new AgyAcpClient({ bin: '/nonexistent/agy', log: () => {} })
    t.after(() => client.stop())
    await assert.rejects(client.ensureStarted(), /ENOENT/)
    assert.equal(client.proc, null)
    // Forgotten: the next call tries again, and fails the same way.
    await assert.rejects(client.listSessions(), /ENOENT/)
    assert.equal(client.proc, null)
    assert.deepEqual(crashes, [])
  })

  it('a permission answered after agy exited is dropped instead of thrown', TIMEOUT, async (t) => {
    const crashes = watchUnhandled(t)
    let answered
    const done = new Promise((r) => {
      answered = r
    })
    const { client, logs } = fakeClient(t, 'permission-exit', {
      onPermissionRequest: async () => {
        // agy goes away while the phone decides.
        const child = client.proc
        const gone = once(child, 'exit')
        child.stdin.write(`${JSON.stringify({ jsonrpc: '2.0', method: 'test/exit' })}\n`)
        await gone
        answered()
        return { outcome: { outcome: 'selected', optionId: 'allow' } }
      },
    })
    await assert.rejects(client.prompt('s1', [{ type: 'text', text: 'hi' }]), /exited/)
    await done
    await waitFor(() => logs.some((m) => /dropped/.test(m)), 2000, 'the dropped reply')
    await new Promise((r) => setTimeout(r, 50))
    assert.deepEqual(crashes, [])
    assert.equal(logs.filter((m) => /dropped/.test(m)).length, 1)
  })

  it('a write to an agy that closed its stdin fails the calls instead of crashing', TIMEOUT, async (t) => {
    const crashes = watchUnhandled(t)
    const { client, logs } = fakeClient(t, 'close-stdin')
    await client.ensureStarted()
    const first = client.listSessions()
    await waitFor(() => logs.some((m) => /stdin closed/.test(m)), 3000, 'the fake to close its stdin')
    const second = client.newSession({ cwd: '/tmp' })
    await assert.rejects(first, /EPIPE/)
    await assert.rejects(second, /EPIPE/)
    assert.equal(client.proc, null)
    assert.deepEqual(crashes, [])
  })

  it('stop() fails pending and later calls and never spawns agy again', TIMEOUT, async (t) => {
    const { client, pids } = fakeClient(t, 'ok')
    await client.ensureStarted()
    const pid = client.proc.pid
    const turn = client.prompt('s1', [{ type: 'text', text: 'hi' }]) // never answered
    await waitFor(() => client.pending.size === 1, 2000, 'the prompt to be sent')
    const next = client.listSessions() // still waiting for its start when stop() comes
    const stopped = client.stop()
    await assert.rejects(turn, /stopped/)
    await assert.rejects(next, /stopped/)
    await assert.rejects(client.listSessions(), /stopped/)
    await assert.rejects(client.ensureStarted(), /stopped/)
    assert.equal(client.proc, null)
    await stopped
    assert.equal(alive(pid), false)
    assert.deepEqual(pids(), [pid])
  })

  it('stop() kills an agy that ignores SIGTERM after the grace period', TIMEOUT, async (t) => {
    const grace = 300
    const { client } = fakeClient(t, 'ignore-term', { killGraceMs: grace })
    await client.ensureStarted()
    const pid = client.proc.pid
    client.stop()
    await waitFor(() => !alive(pid), grace + 1000, `agy ${pid} to be gone`)
  })

  it('stop() also waits for an agy that a failed initialize is still ending', TIMEOUT, async (t) => {
    const grace = 300
    const { client } = fakeClient(t, 'silent-once', { initTimeoutMs: 200, killGraceMs: grace })
    const starting = client.ensureStarted()
    const stale = client.proc.pid
    await assert.rejects(starting, /initialize timed out/)
    assert.equal(client.proc, null)
    assert.equal(alive(stale), true) // it ignores SIGTERM and is inside its grace period
    await client.stop()
    assert.equal(alive(stale), false)
  })

  for (const [mode, why] of [
    ['silent-once', /initialize timed out/],
    ['fail-once', /initialize failed/],
  ]) {
    it(`an initialize that fails (${mode}) kills that agy and the next call starts a fresh one`, TIMEOUT, async (t) => {
      const { client, pids } = fakeClient(t, mode, { initTimeoutMs: 300, killGraceMs: 300 })
      const starting = client.ensureStarted()
      const stale = client.proc.pid
      await assert.rejects(starting, why)
      assert.equal(client.proc, null)
      // The next call starts a new agy at once, while the old one may still be dying.
      assert.deepEqual(await client.listSessions(), [])
      const fresh = client.proc.pid
      assert.notEqual(fresh, stale)
      assert.equal(client.agentVersion, '9.9.9')
      await waitFor(() => !alive(stale), 2000, `the failed agy ${stale} to be killed`)
      // The old process's exit leaves the new one alone.
      assert.equal(client.proc?.pid, fresh)
      assert.deepEqual(await client.listSessions(), [])
      assert.deepEqual(pids(), [stale, fresh])
    })
  }
})

describe('ACP session recovery', () => {
  it('retains advertised auth method IDs without eagerly signing in and clears them when the process ends', TIMEOUT, async t => {
    const { client } = fakeClient(t, 'auth-methods', { channelSpec: { id: 'native', authMode: 'oauth', keyMode: false } })
    await client.ensureStarted()
    assert.deepEqual(client.authMethods, [{ id: 'oauth-personal' }, { id: 'gemini-api-key' }])
    assert.equal(client._canRefreshAuth(), true)
    await client.recycle('test', true)
    assert.deepEqual(client.authMethods, [])
    assert.equal(client._canRefreshAuth(), false)
  })

  it('ignores every event from a replaced process', () => {
    const client = new AgyAcpClient()
    const old = {}
    client.proc = {}
    let updates = 0
    let answers = 0
    client.on('update', () => updates++)
    client.pending.set(7, { resolve: () => answers++, reject: () => answers++ })
    client._onMessage({ method: 'session/update', params: { sessionId: 's1' } }, old)
    client._onMessage({ id: 7, result: {} }, old)
    client._onMessage({ id: 7, error: { message: 'stale' } }, old)
    assert.equal(updates, 0)
    assert.equal(answers, 0)
    assert.equal(client.pending.has(7), true)
    client._onMessage({ id: 7, result: {} }, client.proc)
    assert.equal(answers, 1)
  })

  it('keeps valid capacity changes local to the reporting session', () => {
    const client = new AgyAcpClient()
    client.proc = {}
    client.loadedSessions.set('first', { mcpKey: '[]' })
    const changed = []
    client.on('contextWindowChanged', (id) => changed.push(id))
    const report = (sessionId, size) => client._onMessage({
      method: 'session/update', params: { sessionId, update: { sessionUpdate: 'usage_update', used: 17, size } },
    }, client.proc)
    report('first', 524288)
    for (const size of [524288, -1, 1.5, '64000', 2 ** 31, null]) report('first', size)
    assert.deepEqual(client.loadedSessions.get('first'), { mcpKey: '[]', contextWindow: 524288 })
    report('second', 131072)
    report('first', 0)
    assert.equal(client.loadedSessions.get('second').contextWindow, 131072)
    assert.equal(client.loadedSessions.get('first').contextWindow, 0)
    assert.deepEqual(changed, ['first', 'second', 'first'])
  })

  it('resumes a deleted working directory from home', async () => {
    const client = new AgyAcpClient()
    client.ensureStarted = async () => {}
    let request
    client.call = async (method, params) => { request = { method, params }; return {} }
    await client.ensureSessionLoaded('s1', '/nonexistent/removed-worktree')
    assert.equal(request.method, 'session/resume')
    assert.equal(request.params.cwd, process.env.HOME || '/tmp')
  })

  it('anchors session workspaces and additional directories at the manager cwd', async () => {
    const client = new AgyAcpClient()
    client.ensureStarted = async () => {}
    const requests = []
    client.call = async (method, params) => { requests.push({ method, params }); return {} }
    await client.newSession({ cwd: '.', additionalDirectories: ['../shared', '/already/absolute'] })
    await client.ensureSessionLoaded('s1', '.', [], ['../shared'])
    assert.equal(requests[0].method, 'session/new')
    assert.equal(requests[0].params.cwd, process.cwd() + '/.')
    assert.deepEqual(requests[0].params.additionalDirectories, [process.cwd() + '/../shared', '/already/absolute'])
    assert.equal(requests[1].method, 'session/resume')
    assert.equal(requests[1].params.cwd, process.cwd() + '/.')
    assert.deepEqual(requests[1].params.additionalDirectories, [process.cwd() + '/../shared'])
  })

  // agy 1.5.x says this when its process no longer has the session loaded (after a model service error, for example).
  for (const message of ['Session not found', 'Invalid session', 'session_not_found', 'invalid_session', 'Failed to read session da824ba1-9904-4416-9917-491d8bcc8fd1; retry, or load it with session/load']) {
    it(`evicts cached sessions after ${message}`, async () => {
      const client = new AgyAcpClient()
      client.ensureStarted = async () => {}
      client._rawCall = async () => { throw new Error(message) }
      client.loadedSessions.set('s1', true)
      await assert.rejects(client.prompt('s1', []), (error) => error.invalidSession === true)
      assert.equal(client.loadedSessions.has('s1'), false)
    })
  }

  it('evicts a cached session when the error code identifies a missing session', async () => {
    const client = new AgyAcpClient()
    client.ensureStarted = async () => {}
    client._rawCall = async () => { throw Object.assign(new Error('Unavailable'), { code: 'SESSION_NOT_FOUND' }) }
    client.loadedSessions.set('s1', true)
    await assert.rejects(client.prompt('s1', []), (error) => error.invalidSession === true)
    assert.equal(client.loadedSessions.has('s1'), false)
  })

  it('keeps cached sessions on unrelated errors', async () => {
    const client = new AgyAcpClient()
    client.ensureStarted = async () => {}
    client._rawCall = async () => { throw new Error('quota exhausted') }
    client.loadedSessions.set('s1', true)
    await assert.rejects(client.prompt('s1', []), /quota exhausted/)
    assert.equal(client.loadedSessions.has('s1'), true)
  })

  it('ignores JSON null, primitive, and array stdout frames without crashing the companion', TIMEOUT, async (t) => {
    const unhandled = watchUnhandled(t)
    const { client } = fakeClient(t, 'null-frame')
    await client.ensureStarted()
    const sessions = await client.listSessions()
    assert.deepEqual(sessions, [])
    assert.deepEqual(unhandled, [])
  })

  it('rejects stale _start() without sending authenticate or clobbering state if stopped or replaced after initialize', TIMEOUT, async (t) => {
    // Case A: stop() races after initialize resolves while authMethod is set.
    const { client: c1 } = fakeClient(t, 'ok', { authMethod: 'gemini-api-key' })
    const rawCalls1 = []
    const origRaw1 = c1._rawCall.bind(c1)
    c1._rawCall = async (method, params, timeoutMs) => {
      rawCalls1.push(method)
      if (method === 'initialize') {
        const res = await origRaw1(method, params, timeoutMs)
        c1.stop()
        return { ...res, authMethods: [{ id: 'gemini-api-key' }] }
      }
      return origRaw1(method, params, timeoutMs)
    }
    await assert.rejects(c1.ensureStarted(), /stopped/)
    assert.deepEqual(rawCalls1, ['initialize'])
    assert.equal(c1.agentVersion, '')

    // Case B: proc is replaced after initialize resolves; stale _start must not send authenticate to the replacement proc.
    const { client: c2 } = fakeClient(t, 'ok', { authMethod: 'gemini-api-key' })
    const rawCalls2 = []
    const origRaw2 = c2._rawCall.bind(c2)
    const replacementProc = { pid: 999999, stdin: { writable: true, write() {} } }
    c2._rawCall = async (method, params, timeoutMs) => {
      rawCalls2.push(method)
      if (method === 'initialize') {
        const res = await origRaw2(method, params, timeoutMs)
        const old = c2.proc
        c2._forget(old, new Error('replaced'))
        c2._kill(old)
        c2.proc = replacementProc
        c2.agentVersion = 'replacement-version'
        return { ...res, authMethods: [{ id: 'gemini-api-key' }] }
      }
      return origRaw2(method, params, timeoutMs)
    }
    await assert.rejects(c2.ensureStarted(), /process changed during startup/)
    assert.deepEqual(rawCalls2, ['initialize'])
    assert.equal(c2.proc, replacementProc)
    assert.equal(c2.agentVersion, 'replacement-version')
    c2.proc = null
  })
})
