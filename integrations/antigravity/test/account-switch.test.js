// A new agy sign-in reaches every resident agy acp process: agy keeps the account a process read at startup and writes it
// back at its hourly refresh, so a process started before the sign-in has to go (#10368, #17860).
import assert from 'node:assert/strict'
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { createAgyRuntime } from '../lib/runtime.js'
import { credentialStampReader, keychainStamp } from '../lib/sign-in.js'

const MODELS = [{ modelId: 'gemini-flash-high', name: 'Gemini Flash (High)' }]
const QUOTA = /Individual quota reached/
const WAIT = 8000

async function fixture(t, { tokenFile = true, quota } = {}) {
  const home = mkdtempSync(join(tmpdir(), 'agy-account-'))
  t.after(() => rmSync(home, { recursive: true, force: true }))
  const agyDir = join(home, 'settings')
  mkdirSync(agyDir)
  writeFileSync(join(agyDir, 'settings.json'), JSON.stringify({ model: 'Gemini Flash (High)' }))
  const account = (value) => writeFileSync(join(home, 'account.json'), JSON.stringify({ models: MODELS, ...value }))
  let generation = 0
  // A sign-in rewrites the CLI's account; where the CLI keeps it in a token file, that file changes too. The file only
  // stands for the credential: nothing reads what is in it.
  const signIn = (id, extra = {}) => {
    account({ id, ...extra })
    tokenFile && writeFileSync(join(agyDir, 'antigravity-oauth-token'), `test sign-in ${++generation}`)
  }
  signIn('first', quota ? { quota } : {})
  const bin = join(home, 'agy')
  const fake = fileURLToPath(new URL('./fake-acp-stale.mjs', import.meta.url))
  writeFileSync(bin, `#!/bin/sh\nexec '${process.execPath}' '${fake}' '${home}' "$@"\n`)
  chmodSync(bin, 0o755)
  const n = await startFakeNode(t)
  const logs = []
  const runtime = createAgyRuntime({
    home, agyDir, agyBin: bin, personadBin: join(home, 'no-personad'), jarvisdBin: join(home, 'no-personad'),
    stateHome: join(home, '.persona'), socketPath: n.path, log: (m) => logs.push(m),
    // Idle retirement stays out of the way; a short assistant idle time only makes the reaper look often.
    sharedAcpIdleMs: 600_000, assistantAcpIdleMs: 200, credentialCheckMs: 50,
  })
  t.after(() => runtime.stop())
  runtime.start()
  await n.ready
  const calls = () => existsSync(join(home, 'calls.jsonl'))
    ? readFileSync(join(home, 'calls.jsonl'), 'utf8').trim().split('\n').map((line) => JSON.parse(line)) : []
  const processes = () => [...new Set(calls().map((call) => call.pid))]
  const alive = (pid) => {
    try {
      process.kill(pid, 0)
      return true
    } catch {
      return false
    }
  }
  const hold = (on) => on ? writeFileSync(join(home, 'hold'), '') : rmSync(join(home, 'hold'), { force: true })
  const row = (id) => n.last('runtime.sessions')?.sessions.find((entry) => entry.session_id === id)
  const start = async (prompt = 'Work') => (await n.peer.call('session.start', { cwd: home, prompt })).session.session_id
  const send = (id, text) => n.peer.call('session.send', { session_id: id, text })
  const replied = (id, text) => until(`the reply ${text}`, () => row(id)?.last_text === text && row(id).status !== 'working', WAIT)
  const failedWithQuota = (id) => until('the quota error', () => QUOTA.test(row(id)?.turn_error || ''), WAIT)
  return { n, home, agyDir, logs, calls, processes, alive, hold, row, start, send, replied, failedWithQuota, signIn, account }
}

test('a sign-in made while agy acp runs replaces the idle process, and the next turn runs on the new account', async (t) => {
  const f = await fixture(t)
  const id = await f.start()
  await f.replied(id, 'OK (first)')
  const [old] = f.processes()

  f.signIn('second')
  // Gone before it could refresh its token and write the first account back.
  await until('the old agy acp process to exit', () => !f.alive(old), WAIT)
  assert.ok(f.logs.some((line) => /replacing the process \(the Antigravity CLI signed in again\)/.test(line)))

  await f.send(id, 'Again')
  await f.replied(id, 'OK (second)')
  assert.equal(f.processes().length, 2)
  assert.deepEqual(f.calls().filter((call) => call.method === 'session/cancel'), [])
})

test('a sign-in during a running turn waits for the turn to end, then replaces the process', async (t) => {
  const f = await fixture(t)
  f.hold(true)
  const id = await f.start()
  await until('the running turn', () => f.calls().some((call) => call.method === 'session/prompt'), WAIT)
  const [old] = f.processes()

  f.signIn('second')
  await until('the sign-in change to be seen', () => f.logs.some((line) => /agy sign-in changed under 1 running agy acp process/.test(line)), WAIT)
  assert.equal(f.alive(old), true, 'the running turn keeps its process')
  assert.deepEqual(f.calls().filter((call) => call.method === 'session/cancel'), [])

  f.hold(false)
  await f.replied(id, 'OK (first)')
  await until('the old agy acp process to exit', () => !f.alive(old), WAIT)
  assert.deepEqual(f.logs.filter((line) => /prompt error/.test(line)), [])
  await f.send(id, 'Again')
  await f.replied(id, 'OK (second)')
})

test('a quota error replaces the process once idle when the sign-in change is not visible', async (t) => {
  // No token file: the CLI keeps its sign-in where Persona cannot stamp it, so only the quota error tells.
  const f = await fixture(t, { tokenFile: false, quota: 'exhausted' })
  const id = await f.start()
  await f.failedWithQuota(id)
  f.signIn('second')
  const [old] = f.processes()
  await until('the old agy acp process to exit', () => !f.alive(old), WAIT)
  assert.ok(f.logs.some((line) => /replacing the process \(agy reported a quota or sign-in error\)/.test(line)))

  await f.send(id, 'Again')
  await f.replied(id, 'OK (second)')
  assert.equal(f.processes().length, 2)
})

test('an account that really is out of quota is not replaced again and again', async (t) => {
  const f = await fixture(t, { tokenFile: false, quota: 'exhausted' })
  const id = await f.start()
  await f.failedWithQuota(id)
  const [old] = f.processes()
  await until('the first replacement', () => !f.alive(old), WAIT)

  for (const text of ['Again', 'And again']) {
    await f.send(id, text)
    await until(`the quota error after ${text}`, () => f.calls().filter((call) => call.method === 'session/prompt').length === (text === 'Again' ? 2 : 3), WAIT)
    await until('the turn to settle', () => f.row(id)?.status !== 'working', WAIT)
  }
  assert.equal(f.processes().length, 2, 'the fresh process on the same account stays')
  assert.equal(f.logs.filter((line) => /replacing the process/.test(line)).length, 1)
})

test('the sign-in stamp follows the token file without reading it', async (t) => {
  const dir = mkdtempSync(join(tmpdir(), 'agy-stamp-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const read = credentialStampReader({ agyDir: dir })
  assert.equal(await read(), 'file:none keychain:off')
  writeFileSync(join(dir, 'antigravity-oauth-token'), 'one')
  const first = await read()
  assert.match(first, /^file:\d+:3:[\d.]+ keychain:off$/)
  writeFileSync(join(dir, 'antigravity-oauth-token'), 'second')
  assert.notEqual(await read(), first)
})

test('the keychain stamp is the item dates from security, never its secret', async (t) => {
  const dir = mkdtempSync(join(tmpdir(), 'agy-keychain-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const args = join(dir, 'args')
  const security = (body) => {
    const program = join(dir, 'security')
    writeFileSync(program, `#!/bin/sh\necho "$@" >> '${args}'\n${body}\n`)
    chmodSync(program, 0o755)
    return { program }
  }
  const item = [
    'keychain: "/Users/me/Library/Keychains/login.keychain-db"',
    'class: "genp"',
    'attributes:',
    '    "acct"<blob>="antigravity"',
    '    "cdat"<timedate>=0x32303236313131323136333930345A00  "20261112163904Z\\000"',
    '    "mdat"<timedate>=0x32303236313132383130353334305A00  "20261128105340Z\\000"',
    '    "svce"<blob>="gemini"',
  ].map((line) => `echo '${line}'`).join('\n')
  assert.equal(await keychainStamp(security(item)), 'keychain:20261112163904Z:20261128105340Z')
  assert.equal(await keychainStamp(security('exit 44')), 'keychain:none')
  assert.equal(await keychainStamp(security('exit 1')), null, 'a failed read is unknown, not a sign-in change')
  for (const line of readFileSync(args, 'utf8').trim().split('\n')) {
    assert.equal(line, 'find-generic-password -s gemini -a antigravity')
  }
  const unread = await credentialStampReader({ agyDir: dir, keychain: true, platform: 'darwin', security: security('exit 1') })()
  assert.equal(unread, null)
})
