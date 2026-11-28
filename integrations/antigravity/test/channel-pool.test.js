import assert from 'node:assert/strict'
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:http'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { setTimeout as delay } from 'node:timers/promises'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { toAgyId } from '../lib/map.js'
import { createAgyRuntime } from '../lib/runtime.js'

const FAKE_ACP_SOURCE = `
import { appendFileSync, existsSync, mkdirSync, unlinkSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { createInterface } from 'node:readline'

const home = process.argv[2]
const recordFile = join(home, 'events.jsonl')
const record = (ev) => appendFileSync(recordFile, JSON.stringify({ pid: process.pid, ...ev }) + '\\n')

record({
  kind: 'spawn',
  argv: process.argv.slice(3),
  key: process.env.GEMINI_API_KEY || '',
  base: process.env.GOOGLE_GEMINI_BASE_URL || '',
  provider: process.env.PERSONA_AGY_MODEL_PROVIDER || '',
  ext: process.env.PERSONA_ASSISTANT_BIN || '',
  oldKey: process.env.OLD_KEY || '',
  newKey: process.env.NEW_KEY || '',
})

if (process.argv.includes('models')) {
  const isProvider = process.argv.join(' ').includes('"modelProvider":"gemini"')
  if (isProvider) {
    console.log('gemini-4-neon-high\\tGemini 4 Neon High')
    console.log('gateway-special-high\\tGateway Special High')
  } else {
    console.log('native-oauth-model\\tNative OAuth Model')
  }
  process.exit(0)
}

const send = (obj) => process.stdout.write(JSON.stringify(obj) + '\\n')
let seq = 0
let authed = ''

process.on('SIGTERM', () => {
  record({ kind: 'exit', signal: 'SIGTERM' })
  process.exit(0)
})

createInterface({ input: process.stdin }).on('line', async (line) => {
  const msg = JSON.parse(line)
  record({ kind: 'rpc', method: msg.method, params: msg.params })
  if (msg.method === 'initialize') {
    send({
      jsonrpc: '2.0',
      id: msg.id,
      result: {
        protocolVersion: 1,
        agentInfo: { name: 'fake-pool', version: '1.0.0' },
        authMethods: [{ id: 'oauth-personal' }, { id: 'gemini-api-key' }],
      },
    })
    return
  }
  if (msg.method === 'authenticate') {
    authed = msg.params?.methodId || ''
    if (authed === 'oauth-personal') writeFileSync(join(home, 'oauth-ready'), 'ready')
    record({ kind: 'auth', methodId: authed })
    send({ jsonrpc: '2.0', id: msg.id, result: {} })
    return
  }
  if (msg.method.startsWith('session/') && existsSync(join(home, 'require-oauth')) && !existsSync(join(home, 'oauth-ready'))) {
    send({ jsonrpc: '2.0', id: msg.id, error: { code: -32000, message: 'Authentication required' } })
    return
  }
  if (msg.method === 'session/new') {
    seq++
    const id = '11111111-2222-3333-4444-' + String(process.pid % 100000).padStart(5, '0') + String(seq).padStart(7, '0')
    const brain = join(home, '.gemini', 'antigravity', 'brain', id)
    mkdirSync(brain, { recursive: true })
    writeFileSync(join(brain, 'transcript.json'), JSON.stringify({ steps: [] }))
    const models = process.env.GEMINI_API_KEY
      ? { availableModels: [{ modelId: 'managed-leaked-model', name: 'Managed Leaked Model' }] }
      : undefined
    send({ jsonrpc: '2.0', id: msg.id, result: { sessionId: id, ...(models ? { models } : {}) } })
    return
  }
  if (msg.method === 'session/resume') {
    send({ jsonrpc: '2.0', id: msg.id, result: {} })
    return
  }
  if (msg.method === 'session/prompt') {
    const sid = msg.params?.sessionId
    const promptText = (msg.params?.prompt || []).map((b) => b.text || '').join(' ')
    // Support hold:<file> so tests can keep a turn active while a new channel_rev starts.
    const holdMatch = /hold:(\\S+)/.exec(promptText)
    if (holdMatch) {
      const gateFile = holdMatch[1]
      record({ kind: 'holding', sessionId: sid })
      while (!existsSync(gateFile)) {
        await new Promise((r) => setTimeout(r, 15))
      }
    }
    const reply = 'reply:' + (authed || 'oauth') + ':' + (process.env.GOOGLE_GEMINI_BASE_URL || 'native') + ':' + promptText
    if (process.env.GEMINI_API_KEY) {
      process.stderr.write('stderr diagnostic with secret ' + process.env.GEMINI_API_KEY + '\\n')
    }
    send({
      jsonrpc: '2.0',
      method: 'session/update',
      params: {
        sessionId: sid,
        update: { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: reply } },
      },
    })
    const logsDir = join(home, 'agy', 'brain', sid, '.system_generated', 'logs')
    mkdirSync(logsDir, { recursive: true })
    const rows = [
      { step_index: 0, source: 'USER_EXPLICIT', type: 'USER_INPUT', status: 'DONE', content: promptText },
      { step_index: 1, source: 'MODEL', type: 'PLANNER_RESPONSE', status: 'DONE', content: reply },
    ]
    const jsonl = rows.map((r) => JSON.stringify(r)).join('\\n') + '\\n'
    writeFileSync(join(logsDir, 'transcript.jsonl'), jsonl)
    writeFileSync(join(logsDir, 'transcript_full.jsonl'), jsonl)
    record({
      kind: 'prompt_done',
      sessionId: sid,
      reply,
      authed,
      key: process.env.GEMINI_API_KEY || '',
      base: process.env.GOOGLE_GEMINI_BASE_URL || '',
      provider: process.env.PERSONA_AGY_MODEL_PROVIDER || '',
      ext: process.env.PERSONA_ASSISTANT_BIN || '',
      oldKey: process.env.OLD_KEY || '',
      newKey: process.env.NEW_KEY || '',
    })
    send({ jsonrpc: '2.0', id: msg.id, result: { stopReason: 'end_turn' } })
    return
  }
  if (msg.method === 'session/set_model' || msg.method === 'session/set_config_option') {
    const marker = process.env.FAKE_REJECT_MODEL_ONCE
    if (marker && existsSync(marker)) {
      writeFileSync(marker + '.tripped', String(process.pid))
      try { unlinkSync(marker) } catch {}
      send({ jsonrpc: '2.0', id: msg.id, error: { code: -32000, message: 'unknown model' } })
      return
    }
    send({ jsonrpc: '2.0', id: msg.id, result: {} })
    return
  }
  if (msg.id !== undefined) {
    send({ jsonrpc: '2.0', id: msg.id, result: {} })
  }
})
`

async function poolFixture(t, options = {}) {
  const home = mkdtempSync(join(tmpdir(), 'agy-pool-'))
  const agyDir = join(home, 'agy')
  mkdirSync(agyDir, { recursive: true })
  const scriptPath = join(home, 'fake-acp.mjs')
  const bin = join(home, 'fake-agy')
  writeFileSync(scriptPath, FAKE_ACP_SOURCE)
  writeFileSync(join(home, 'events.jsonl'), '')
  writeFileSync(bin, `#!/bin/sh\nexec '${process.execPath}' '${scriptPath}' '${home}' "$@"\n`)
  chmodSync(bin, 0o700)
  const logs = []
  const node = await startFakeNode(t, { features: ['assistant_pids'] })
  const runtime = createAgyRuntime({
    home,
    agyDir,
    agyBin: bin,
    socketPath: node.path,
    sharedAcpIdleMs: options.sharedAcpIdleMs ?? 300_000,
    assistantAcpIdleMs: options.assistantAcpIdleMs ?? 300_000,
    exportPollMs: options.exportPollMs ?? 20,
    env: options.env ?? { ...process.env },
    fetchImpl: options.fetchImpl,
    log: (m) => logs.push(m),
  })
  t.after(async () => {
    await runtime.stop()
    rmSync(home, { recursive: true, force: true })
  })
  runtime.start()
  await node.ready
  const events = () => readFileSync(join(home, 'events.jsonl'), 'utf8').split('\n').filter(Boolean).map(JSON.parse)
  return { home, agyDir, bin, node, runtime, logs, events }
}

test('native OAuth and multiple managed API-key channels coexist concurrently in isolated pools', async (t) => {
  // Even if the host shell accidentally had GEMINI_API_KEY set, the native OAuth pool strips it.
  const { home, node, logs, events } = await poolFixture(t, {
    env: {
      ...process.env,
      GEMINI_API_KEY: 'ambient-leaked-key',
      GOOGLE_GEMINI_BASE_URL: 'https://ambient.example.com',
      PERSONA_AGY_MODEL_PROVIDER: '',
    },
  })

  const chAlpha = {
    id: 'gw-alpha',
    auth_mode: 'gemini-api-key',
    rev: 'r1',
    env: {
      GOOGLE_GEMINI_BASE_URL: 'https://alpha.example.com',
      GEMINI_API_KEY: 'secret-alpha-001',
      PERSONA_AGY_MODEL_PROVIDER: 'gemini',
    },
  }
  const chBeta = {
    id: 'gw-beta',
    auth_mode: 'gemini-api-key',
    rev: 'r1',
    env: {
      GOOGLE_GEMINI_BASE_URL: 'https://beta.example.com',
      GEMINI_API_KEY: 'secret-beta-002',
      PERSONA_AGY_MODEL_PROVIDER: 'gemini',
    },
  }

  const nativeStart = await node.peer.call('session.start', { cwd: home })
  const alphaStart = await node.peer.call('session.start', { cwd: home, channel: chAlpha })
  const betaStart = await node.peer.call('session.start', { cwd: home, channel: chBeta })

  await Promise.all([
    node.peer.call('session.send', { session_id: nativeStart.session.session_id, text: 'hello-native' }),
    node.peer.call('session.send', { session_id: alphaStart.session.session_id, text: 'hello-alpha', channel: chAlpha }),
    node.peer.call('session.send', { session_id: betaStart.session.session_id, text: 'hello-beta', channel: chBeta }),
  ])

  await until('all three turns completed', () => events().filter((e) => e.kind === 'prompt_done').length === 3)

  const all = events()
  const doneNative = all.find((e) => e.kind === 'prompt_done' && e.sessionId === toAgyId(nativeStart.session.session_id))
  const doneAlpha = all.find((e) => e.kind === 'prompt_done' && e.sessionId === toAgyId(alphaStart.session.session_id))
  const doneBeta = all.find((e) => e.kind === 'prompt_done' && e.sessionId === toAgyId(betaStart.session.session_id))

  // Three distinct ACP child processes (different PIDs).
  assert.equal(new Set([doneNative.pid, doneAlpha.pid, doneBeta.pid]).size, 3)

  // Native pool never authenticated with gemini-api-key and stripped ambient provider env vars.
  const nativeSpawn = all.find((e) => e.kind === 'spawn' && e.pid === doneNative.pid)
  assert.equal(nativeSpawn.key, '')
  assert.equal(nativeSpawn.base, '')
  assert.equal(nativeSpawn.provider, '')
  assert.equal(doneNative.authed, '')
  assert.match(doneNative.reply, /^reply:oauth:native:hello-native$/)

  // Each managed channel pool authenticated with gemini-api-key and received only its own env.
  assert.equal(doneAlpha.authed, 'gemini-api-key')
  assert.equal(doneAlpha.key, 'secret-alpha-001')
  assert.match(doneAlpha.reply, /^reply:gemini-api-key:https:\/\/alpha\.example\.com:hello-alpha$/)

  assert.equal(doneBeta.authed, 'gemini-api-key')
  assert.equal(doneBeta.key, 'secret-beta-002')
  assert.match(doneBeta.reply, /^reply:gemini-api-key:https:\/\/beta\.example\.com:hello-beta$/)

  // Channel secrets are redacted from all companion logs.
  const combinedLogs = logs.join('\n')
  assert.ok(!combinedLogs.includes('secret-alpha-001'), 'alpha secret leaked into logs')
  assert.ok(!combinedLogs.includes('secret-beta-002'), 'beta secret leaked into logs')
})

test('superseded channel_rev pool keeps running active turns and is reaped once idle', async (t) => {
  const { home, node, events } = await poolFixture(t, { sharedAcpIdleMs: 50 })
  const gateFile = join(home, 'release-r1')

  const chR1 = {
    id: 'gw-rotating',
    auth_mode: 'gemini-api-key',
    rev: 'r1',
    env: {
      GOOGLE_GEMINI_BASE_URL: 'https://r1.example.com',
      GEMINI_API_KEY: 'secret-r1',
      PERSONA_AGY_MODEL_PROVIDER: 'gemini',
    },
  }
  const chR2 = {
    id: 'gw-rotating',
    auth_mode: 'gemini-api-key',
    rev: 'r2',
    env: {
      GOOGLE_GEMINI_BASE_URL: 'https://r2.example.com',
      GEMINI_API_KEY: 'secret-r2',
      PERSONA_AGY_MODEL_PROVIDER: 'gemini',
    },
  }

  // Start a session on r1 and hold its turn in flight.
  const s1 = await node.peer.call('session.start', { cwd: home, channel: chR1 })
  await node.peer.call('session.send', { session_id: s1.session.session_id, text: `hold:${gateFile}`, channel: chR1 })
  await until('r1 turn is holding', () => events().some((e) => e.kind === 'holding' && e.sessionId === toAgyId(s1.session.session_id)))
  const r1Pid = events().find((e) => e.kind === 'holding').pid

  // Meanwhile, a new session starts on r2 (superseding r1) and finishes immediately.
  const s2 = await node.peer.call('session.start', { cwd: home, channel: chR2 })
  await node.peer.call('session.send', { session_id: s2.session.session_id, text: 'hello-r2', channel: chR2 })
  await until('r2 turn completed', () => events().some((e) => e.kind === 'prompt_done' && e.sessionId === toAgyId(s2.session.session_id)))
  const r2Pid = events().find((e) => e.kind === 'prompt_done' && e.sessionId === toAgyId(s2.session.session_id)).pid
  assert.notEqual(r1Pid, r2Pid, 'r2 must run in a new isolated pool')

  // Wait longer than the reaper interval: r1 is still active so it MUST NOT be killed.
  await delay(150)
  assert.equal(events().some((e) => e.kind === 'exit' && e.pid === r1Pid), false, 'active superseded pool was killed')

  // Release the held turn on r1 and verify it finishes cleanly.
  writeFileSync(gateFile, 'go')
  await until('r1 turn completed cleanly', () => events().some((e) => e.kind === 'prompt_done' && e.sessionId === toAgyId(s1.session.session_id)))

  // Once idle, the superseded r1 pool is automatically reaped.
  await until('superseded r1 pool reaped after idle', () => events().some((e) => e.kind === 'exit' && e.pid === r1Pid))
})

test('launchers.models isolates native OAuth catalog from managed channel catalog', async (t) => {
  const gwServer = createServer((req, res) => {
    if (req.headers['x-goog-api-key'] !== 'secret-models-key') {
      res.writeHead(401).end('{}')
      return
    }
    res.setHeader('content-type', 'application/json')
    res.end(JSON.stringify({
      models: [{ name: 'models/gateway-special', baseModelId: 'gateway-special', supportedGenerationMethods: ['generateContent'] }],
    }))
  })
  await new Promise((resolve) => gwServer.listen(0, '127.0.0.1', resolve))
  t.after(() => gwServer.close())
  const gwBase = `http://127.0.0.1:${gwServer.address().port}`

  const { node } = await poolFixture(t)
  const nativeRes = await node.peer.call('launchers.models', { launcher: 'agy', refresh: true })
  assert.deepEqual(nativeRes.models.map((m) => m.id), ['native-oauth-model'])

  const managedRes = await node.peer.call('launchers.models', {
    launcher: 'agy',
    refresh: true,
    channel: {
      id: 'gw-models',
      auth_mode: 'gemini-api-key',
      rev: 'r1',
      env: { GOOGLE_GEMINI_BASE_URL: gwBase, GEMINI_API_KEY: 'secret-models-key', PERSONA_AGY_MODEL_PROVIDER: 'gemini' },
    },
  })
  assert.deepEqual(managedRes.models.map((m) => m.id), ['gateway-special-high'])

  // Re-querying native models still returns the native catalog, completely unpolluted.
  const nativeAgain = await node.peer.call('launchers.models', { launcher: 'agy' })
  assert.deepEqual(nativeAgain.models.map((m) => m.id), ['native-oauth-model'])
})

test('managed channel session/new models never pollute native OAuth catalog and ambient provider env never leaks across channels', async (t) => {
  const trapHits = []
  const trapServer = createServer((req, res) => {
    trapHits.push({ url: req.url, key: req.headers['x-goog-api-key'] || '' })
    res.writeHead(200, { 'content-type': 'application/json' }).end(JSON.stringify({ models: [] }))
  })
  await new Promise((resolve) => trapServer.listen(0, '127.0.0.1', resolve))
  t.after(() => trapServer.close())
  const trapBase = `http://127.0.0.1:${trapServer.address().port}`

  const fetched = []
  const fetchImpl = async (url, init) => {
    fetched.push({ url: String(url), key: init?.headers?.['x-goog-api-key'] || '' })
    return new Response(JSON.stringify({
      models: [{ name: 'models/gemini-4-neon', baseModelId: 'gemini-4-neon', supportedGenerationMethods: ['generateContent'] }],
    }), { status: 200, headers: { 'content-type': 'application/json' } })
  }

  // Simulate ambient shell env vars that would hijack a direct managed channel or leak into OAuth models.
  const prevBase = process.env.GOOGLE_GEMINI_BASE_URL
  const prevKey = process.env.GEMINI_API_KEY
  process.env.GOOGLE_GEMINI_BASE_URL = trapBase
  process.env.GEMINI_API_KEY = 'ambient-shell-key'
  t.after(() => {
    if (prevBase === undefined) delete process.env.GOOGLE_GEMINI_BASE_URL
    else process.env.GOOGLE_GEMINI_BASE_URL = prevBase
    if (prevKey === undefined) delete process.env.GEMINI_API_KEY
    else process.env.GEMINI_API_KEY = prevKey
  })

  const { home, node, events } = await poolFixture(t, {
    env: {
      ...process.env,
      GEMINI_API_KEY: 'ambient-shell-key',
      GOOGLE_GEMINI_BASE_URL: trapBase,
      PERSONA_AGY_MODEL_PROVIDER: '',
    },
    fetchImpl,
  })

  // 1. Native OAuth models discovery strips ambient provider env vars.
  const nativeBefore = await node.peer.call('launchers.models', { launcher: 'agy', refresh: true })
  assert.deepEqual(nativeBefore.models.map((m) => m.id), ['native-oauth-model'])
  const oauthModelSpawn = events().find((e) => e.kind === 'spawn' && Array.isArray(e.argv) && e.argv.includes('models'))
  assert.ok(oauthModelSpawn, 'expected native models subprocess spawn')
  assert.equal(oauthModelSpawn.key, '')
  assert.equal(oauthModelSpawn.base, '')
  assert.equal(oauthModelSpawn.provider, '')

  // 2. Direct managed API-key channel (no GOOGLE_GEMINI_BASE_URL) must NOT inherit ambient GOOGLE_GEMINI_BASE_URL
  // in either launchers.models or ACP child spawn, and its session/new models must NOT pollute native OAuth catalog.
  const chDirect = {
    id: 'gw-direct',
    auth_mode: 'gemini-api-key',
    rev: 'r1',
    env: { GEMINI_API_KEY: 'direct-channel-secret', PERSONA_AGY_MODEL_PROVIDER: 'gemini' },
  }
  const directModels = await node.peer.call('launchers.models', { launcher: 'agy', refresh: true, channel: chDirect })
  assert.ok(directModels.models.length > 0)
  assert.equal(trapHits.length, 0, 'managed channel secret leaked to ambient GOOGLE_GEMINI_BASE_URL')
  assert.ok(fetched.some((f) => f.url.startsWith('https://generativelanguage.googleapis.com/') && f.key === 'direct-channel-secret'))

  const started = await node.peer.call('session.start', { cwd: home, channel: chDirect })
  await node.peer.call('session.send', { session_id: started.session.session_id, text: 'hi-direct', channel: chDirect })
  await until('direct turn completed', () => events().some((e) => e.kind === 'prompt_done' && e.sessionId === toAgyId(started.session.session_id)))
  const directDone = events().find((e) => e.kind === 'prompt_done' && e.sessionId === toAgyId(started.session.session_id))
  const directSpawn = events().find((e) => e.kind === 'spawn' && e.pid === directDone.pid)
  assert.equal(directSpawn.key, 'direct-channel-secret')
  assert.equal(directSpawn.base, '', 'managed channel ACP inherited ambient GOOGLE_GEMINI_BASE_URL')

  // Native OAuth catalog must still be ['native-oauth-model'], NOT polluted with 'managed-leaked-model'.
  const nativeAfter = await node.peer.call('launchers.models', { launcher: 'agy' })
  assert.deepEqual(nativeAfter.models.map((m) => m.id), ['native-oauth-model'])
})

test('assistant bound to managed channel keeps its channel credentials after ACP exit when follow-up omits channel', async (t) => {
  const { home, node, events } = await poolFixture(t)
  const assistantDir = join(home, '.persona', 'assistant')
  mkdirSync(assistantDir, { recursive: true })
  const chAsst = {
    id: 'gw-asst',
    auth_mode: 'gemini-api-key',
    rev: 'r1',
    env: {
      GOOGLE_GEMINI_BASE_URL: 'https://asst-gw.example.com',
      GEMINI_API_KEY: 'secret-asst-999',
      PERSONA_AGY_MODEL_PROVIDER: 'gemini',
    },
  }

  const started = await node.peer.call('session.start', { cwd: assistantDir, channel: chAsst })
  const sid = started.session.session_id
  const rawId = toAgyId(sid)
  await node.peer.call('session.send', { session_id: sid, text: 'first-turn', channel: chAsst })
  await until('first assistant turn completed', () => events().some((e) => e.kind === 'prompt_done' && e.sessionId === rawId))
  const firstPid = events().find((e) => e.kind === 'prompt_done' && e.sessionId === rawId).pid

  // Kill the assistant ACP process so acp.proc becomes null while assistantAcps still holds the binding.
  process.kill(firstPid, 'SIGKILL')
  await delay(100)

  // Follow-up call supplies extra env WITHOUT channel: must preserve chAsst credentials on respawn.
  await node.peer.call('session.send', { session_id: sid, text: 'second-turn', env: { PERSONA_EXTRA: '1' } })
  await until('second assistant turn completed', () => events().filter((e) => e.kind === 'prompt_done' && e.sessionId === rawId).length === 2)
  const secondDone = events().filter((e) => e.kind === 'prompt_done' && e.sessionId === rawId)[1]
  assert.notEqual(secondDone.pid, firstPid)
  assert.equal(secondDone.authed, 'gemini-api-key')
  assert.equal(secondDone.key, 'secret-asst-999')
  assert.match(secondDone.reply, /^reply:gemini-api-key:https:\/\/asst-gw\.example\.com:second-turn$/)
})

test('recycleForModel refreshes the managed channel catalog rather than the default OAuth catalog', async (t) => {
  const gwServer = createServer((req, res) => {
    if (req.headers['x-goog-api-key'] !== 'secret-recycle-key') {
      res.writeHead(401).end('{}')
      return
    }
    res.setHeader('content-type', 'application/json')
    res.end(JSON.stringify({
      models: [{ name: 'models/gateway-special', baseModelId: 'gateway-special', supportedGenerationMethods: ['generateContent'] }],
    }))
  })
  await new Promise((resolve) => gwServer.listen(0, '127.0.0.1', resolve))
  t.after(() => gwServer.close())
  const gwBase = `http://127.0.0.1:${gwServer.address().port}`

  const { home, node, events } = await poolFixture(t)
  const rejectOnce = join(home, 'reject-model-once')
  writeFileSync(rejectOnce, '1')
  const ch = {
    id: 'gw-recycle',
    auth_mode: 'gemini-api-key',
    rev: 'r1',
    env: {
      GOOGLE_GEMINI_BASE_URL: gwBase,
      GEMINI_API_KEY: 'secret-recycle-key',
      PERSONA_AGY_MODEL_PROVIDER: 'gemini',
      FAKE_REJECT_MODEL_ONCE: rejectOnce,
    },
  }

  // First session/set_model fails with "unknown model"; recycleForModel must refresh ch's catalog
  // (finding gateway-special-high, which does NOT exist in native OAuth catalog), recycle ACP, and succeed.
  const started = await node.peer.call('session.start', { cwd: home, channel: ch, model: 'gateway-special-high' })
  assert.ok(started.session.session_id)
  const trippedPid = Number(readFileSync(rejectOnce + '.tripped', 'utf8'))
  assert.ok(trippedPid > 0)
  assert.ok(events().some((e) => e.kind === 'exit' && e.pid === trippedPid), 'expected stale managed ACP to be recycled')
})

test('assistant respawn preserves both channel creds and assistant extension env, updates channelSpec, and rotates when warm idle', async (t) => {
  const { home, node, events } = await poolFixture(t)
  const assistantDir = join(home, '.persona', 'assistant')
  mkdirSync(assistantDir, { recursive: true })
  const chR1 = {
    id: 'gw-asst-rot',
    auth_mode: 'gemini-api-key',
    rev: 'r1',
    env: {
      GOOGLE_GEMINI_BASE_URL: 'https://r1-asst.example.com',
      GEMINI_API_KEY: 'secret-asst-r1',
      PERSONA_AGY_MODEL_PROVIDER: 'gemini',
    },
  }

  // 1. Start assistant with both managed channel and assistant extension env.
  const started = await node.peer.call('session.start', {
    cwd: assistantDir,
    channel: chR1,
    env: { PERSONA_ASSISTANT_BIN: '/opt/persona-ext' },
  })
  const sid = started.session.session_id
  const rawId = toAgyId(sid)
  await node.peer.call('session.send', { session_id: sid, text: 't1', channel: chR1 })
  await until('t1 done', () => events().some((e) => e.kind === 'prompt_done' && e.sessionId === rawId))
  const pid1 = events().find((e) => e.kind === 'prompt_done' && e.sessionId === rawId).pid

  // 2. Kill ACP; follow-up omitting BOTH env and channel must preserve BOTH chR1 creds and PERSONA_ASSISTANT_BIN.
  process.kill(pid1, 'SIGKILL')
  await delay(100)
  await node.peer.call('session.send', { session_id: sid, text: 't2' })
  await until('t2 done', () => events().filter((e) => e.kind === 'prompt_done' && e.sessionId === rawId).length === 2)
  const done2 = events().filter((e) => e.kind === 'prompt_done' && e.sessionId === rawId)[1]
  assert.notEqual(done2.pid, pid1)
  assert.equal(done2.key, 'secret-asst-r1')
  assert.equal(done2.ext, '/opt/persona-ext')

  // 3. Kill ACP again; respawn with same poolKey (rev r1) but updated env must update acp.channelSpec
  // so a subsequent respawn with spec === null retains the new key.
  process.kill(done2.pid, 'SIGKILL')
  await delay(100)
  const chR1Updated = {
    ...chR1,
    env: { ...chR1.env, GEMINI_API_KEY: 'secret-asst-r1-updated' },
  }
  await node.peer.call('session.send', { session_id: sid, text: 't3', channel: chR1Updated })
  await until('t3 done', () => events().filter((e) => e.kind === 'prompt_done' && e.sessionId === rawId).length === 3)
  const done3 = events().filter((e) => e.kind === 'prompt_done' && e.sessionId === rawId)[2]
  assert.equal(done3.key, 'secret-asst-r1-updated')
  assert.equal(done3.ext, '/opt/persona-ext')

  process.kill(done3.pid, 'SIGKILL')
  await delay(100)
  await node.peer.call('session.send', { session_id: sid, text: 't4', env: { PERSONA_ASSISTANT_BIN: '/opt/persona-ext', PERSONA_EXTRA: '1' } })
  await until('t4 done', () => events().filter((e) => e.kind === 'prompt_done' && e.sessionId === rawId).length === 4)
  const done4 = events().filter((e) => e.kind === 'prompt_done' && e.sessionId === rawId)[3]
  assert.equal(done4.key, 'secret-asst-r1-updated')
  assert.equal(done4.ext, '/opt/persona-ext')

  // 4. Warm idle assistant (NOT killed!) receives send() with rotated rev r2: must retire old PID and rotate to r2.
  const chR2 = {
    id: 'gw-asst-rot',
    auth_mode: 'gemini-api-key',
    rev: 'r2',
    env: {
      GOOGLE_GEMINI_BASE_URL: 'https://r2-asst.example.com',
      GEMINI_API_KEY: 'secret-asst-r2',
      PERSONA_AGY_MODEL_PROVIDER: 'gemini',
    },
  }
  await node.peer.call('session.send', { session_id: sid, text: 't5', channel: chR2 })
  await until('t5 done', () => events().filter((e) => e.kind === 'prompt_done' && e.sessionId === rawId).length === 5)
  const done5 = events().filter((e) => e.kind === 'prompt_done' && e.sessionId === rawId)[4]
  assert.notEqual(done5.pid, done4.pid, 'warm idle assistant must rotate to new PID on channel rev change')
  assert.equal(done5.key, 'secret-asst-r2')
  assert.equal(done5.ext, '/opt/persona-ext')
  assert.match(done5.reply, /^reply:gemini-api-key:https:\/\/r2-asst\.example\.com:t5$/)
})

test('assistant env strips native provider vars (cannot hijack managed or OAuth ACP) and replaces snapshots cleanly', async (t) => {
  const { home, node, events } = await poolFixture(t)
  const assistantDir = join(home, '.persona', 'assistant')
  mkdirSync(assistantDir, { recursive: true })

  // 1. Managed channel assistant: provider keys in params.env cannot override managed ch.env.
  const chSafe = {
    id: 'gw-asst-safe',
    auth_mode: 'gemini-api-key',
    rev: 'r1',
    env: {
      GOOGLE_GEMINI_BASE_URL: 'https://safe-gw.example.com',
      GEMINI_API_KEY: 'secret-asst-safe',
      PERSONA_AGY_MODEL_PROVIDER: 'gemini',
    },
  }
  const evilEnv = {
    GOOGLE_GEMINI_BASE_URL: 'https://evil.example',
    GEMINI_API_KEY: 'evil-key',
    PERSONA_AGY_MODEL_PROVIDER: 'evil',
    PERSONA_ASSISTANT_BIN: '/bin/asst',
    OLD_KEY: 'old-1',
  }
  const managed = await node.peer.call('session.start', { cwd: assistantDir, channel: chSafe, env: evilEnv })
  const managedSid = managed.session.session_id
  const managedRaw = toAgyId(managedSid)
  await node.peer.call('session.send', { session_id: managedSid, text: 'm1', channel: chSafe, env: evilEnv })
  await until('m1 done', () => events().some((e) => e.kind === 'prompt_done' && e.sessionId === managedRaw))
  const m1 = events().find((e) => e.kind === 'prompt_done' && e.sessionId === managedRaw)
  assert.equal(m1.key, 'secret-asst-safe')
  assert.equal(m1.base, 'https://safe-gw.example.com')
  assert.equal(m1.provider, 'gemini')
  assert.equal(m1.ext, '/bin/asst')
  assert.equal(m1.oldKey, 'old-1')
  assert.equal(m1.newKey, '')

  // 2. Full-snapshot replacement: new env replaces assistantEnv (removing OLD_KEY) and still strips provider vars.
  process.kill(m1.pid, 'SIGKILL')
  await delay(100)
  await node.peer.call('session.send', {
    session_id: managedSid,
    text: 'm2',
    env: { NEW_KEY: '1', GEMINI_API_KEY: 'evil-respawn', GOOGLE_GEMINI_BASE_URL: 'https://evil.example' },
  })
  await until('m2 done', () => events().filter((e) => e.kind === 'prompt_done' && e.sessionId === managedRaw).length === 2)
  const m2 = events().filter((e) => e.kind === 'prompt_done' && e.sessionId === managedRaw)[1]
  assert.equal(m2.key, 'secret-asst-safe')
  assert.equal(m2.base, 'https://safe-gw.example.com')
  assert.equal(m2.oldKey, '', 'new snapshot must drop removed OLD_KEY')
  assert.equal(m2.newKey, '1')
  assert.equal(m2.ext, '', 'new snapshot must drop omitted PERSONA_ASSISTANT_BIN')

  // Omitting env on subsequent respawn preserves the previous assistantEnv snapshot.
  process.kill(m2.pid, 'SIGKILL')
  await delay(100)
  await node.peer.call('session.send', { session_id: managedSid, text: 'm3' })
  await until('m3 done', () => events().filter((e) => e.kind === 'prompt_done' && e.sessionId === managedRaw).length === 3)
  const m3 = events().filter((e) => e.kind === 'prompt_done' && e.sessionId === managedRaw)[2]
  assert.equal(m3.key, 'secret-asst-safe')
  assert.equal(m3.oldKey, '')
  assert.equal(m3.newKey, '1')

  // 3. OAuth assistant (default channel): provider keys in params.env never leak into OAuth ACP.
  const oauth = await node.peer.call('session.start', { cwd: assistantDir, env: evilEnv })
  const oauthSid = oauth.session.session_id
  const oauthRaw = toAgyId(oauthSid)
  await node.peer.call('session.send', { session_id: oauthSid, text: 'o1', env: evilEnv })
  await until('o1 done', () => events().some((e) => e.kind === 'prompt_done' && e.sessionId === oauthRaw))
  const o1 = events().find((e) => e.kind === 'prompt_done' && e.sessionId === oauthRaw)
  assert.equal(o1.authed, '')
  assert.equal(o1.key, '')
  assert.equal(o1.base, '')
  assert.equal(o1.provider, '')
  assert.equal(o1.ext, '/bin/asst')
  assert.equal(o1.oldKey, 'old-1')

  // OAuth respawn with evil env also strips provider vars and replaces snapshot.
  process.kill(o1.pid, 'SIGKILL')
  await delay(100)
  await node.peer.call('session.send', {
    session_id: oauthSid,
    text: 'o2',
    env: { GOOGLE_GEMINI_BASE_URL: 'https://evil.example', GEMINI_API_KEY: 'evil-key', NEW_KEY: '2' },
  })
  await until('o2 done', () => events().filter((e) => e.kind === 'prompt_done' && e.sessionId === oauthRaw).length === 2)
  const o2 = events().filter((e) => e.kind === 'prompt_done' && e.sessionId === oauthRaw)[1]
  assert.equal(o2.authed, '')
  assert.equal(o2.key, '')
  assert.equal(o2.base, '')
  assert.equal(o2.provider, '')
  assert.equal(o2.oldKey, '')
  assert.equal(o2.newKey, '2')
})


test('runtime native pool recovers auth-required session creation with advertised personal OAuth', async t => {
  const { home, node, events } = await poolFixture(t)
  writeFileSync(join(home, 'require-oauth'), 'required')
  const started = await node.peer.call('session.start', { cwd: home })
  assert.ok(started.session.session_id)
  assert.deepEqual(events().filter(event => event.kind === 'auth').map(event => event.methodId), ['oauth-personal'])
  assert.equal(events().filter(event => event.kind === 'rpc' && event.method === 'session/new').length, 2)
})

test('runtime managed pool never uses personal OAuth when session creation requires authentication', async t => {
  const { home, node, events } = await poolFixture(t)
  writeFileSync(join(home, 'require-oauth'), 'required')
  await assert.rejects(node.peer.call('session.start', { cwd: home, channel: {
    id: 'managed-auth-required', auth_mode: 'gemini-api-key', rev: 'r1',
    env: { GEMINI_API_KEY: 'mock-managed-key', PERSONA_AGY_MODEL_PROVIDER: 'gemini' },
  } }), /Authentication required/)
  assert.deepEqual(events().filter(event => event.kind === 'auth').map(event => event.methodId), ['gemini-api-key'])
  assert.equal(events().filter(event => event.kind === 'rpc' && event.method === 'session/new').length, 1)
})
