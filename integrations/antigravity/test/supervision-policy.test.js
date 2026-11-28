import assert from 'node:assert/strict'
import { chmodSync, existsSync, lstatSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { toAgyId, workspacePathOf } from '../lib/map.js'
import { AGY_RULE_MAX_BYTES, isPolicyDir, POLICY_DIR_NAME, policyDirOf, readPolicy, removePolicy, splitPolicy, SUPERVISION_ERROR_CODE, syncPolicy } from '../lib/policy.js'
import { createAgyRuntime } from '../lib/runtime.js'
import { AgyAcpClient } from '../lib/acp.js'

const FAKE = fileURLToPath(new URL('./fake-acp-policy.mjs', import.meta.url))
const SLOW = 15_000

function received(record) {
  return readFileSync(record, 'utf8').split('\n').filter(Boolean).map((line) => JSON.parse(line))
}

function setupAgy(t, opts = {}) {
  const dir = mkdtempSync(join(tmpdir(), 'agy-policy-'))
  const record = join(dir, 'record.jsonl')
  writeFileSync(record, '')
  t.after(() => {
    for (const m of received(record)) {
      if (m.started) {
        try { process.kill(m.started, 'SIGKILL') } catch {}
      }
    }
    rmSync(dir, { recursive: true, force: true })
  })

  const extraFlags = (opts.acpFlags || []).map((f) => `'${f}'`).join(' ')
  const agyBin = join(dir, 'agy')
  writeFileSync(agyBin, `#!/bin/sh\nexec '${process.execPath}' '${FAKE}' --record '${record}' ${extraFlags} "$@"\n`)
  chmodSync(agyBin, 0o755)

  const agyDir = join(dir, 'agy-config')
  mkdirSync(join(agyDir, 'brain'), { recursive: true })
  const assistantDir = join(dir, '.persona', 'assistant')
  mkdirSync(assistantDir, { recursive: true })
  const baseAgentsPath = join(assistantDir, 'AGENTS.md')
  const baseAgentsContent = '# Base Persona Assistant Instructions\nNever overwrite me.\n'
  writeFileSync(baseAgentsPath, baseAgentsContent)

  const policyRoot = opts.policyRoot || join(agyDir, POLICY_DIR_NAME)
  return { dir, record, agyBin, agyDir, assistantDir, baseAgentsPath, baseAgentsContent, policyRoot }
}

async function startRuntime(t, env, runtimeOpts = {}) {
  const n = await startFakeNode(t)
  const rt = createAgyRuntime({
    agyBin: env.agyBin,
    agyDir: env.agyDir,
    home: env.dir,
    policyRoot: env.policyRoot,
    socketPath: n.path,
    log: () => {},
    ...runtimeOpts,
  })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  return { n, rt }
}

test('hello advertises assistant and supervision_policy capabilities (#11173)', async (t) => {
  const env = setupAgy(t)
  const { n } = await startRuntime(t, env)
  assert.equal(n.hello.caps.assistant, true)
  assert.equal(n.hello.caps.supervision_policy, true)
})

test('supervised and ordinary chats sharing ~/.persona/assistant stay isolated and keep base AGENTS.md untouched', async (t) => {
  const env = setupAgy(t)
  const { n } = await startRuntime(t, env)

  const policy = 'SUPERVISION_ROLE_MARKER\nReview worker reports before acting.'
  const supStart = await n.peer.call('session.start', {
    cwd: env.assistantDir,
    assistant: true,
    supervision_instructions: policy,
  })
  const supPid = supStart.session.session_id
  const supRaw = toAgyId(supPid)
  const supPolicyDir = policyDirOf(env.policyRoot, supRaw)

  const ordStart = await n.peer.call('session.start', {
    cwd: env.assistantDir,
    assistant: true,
  })
  const ordPid = ordStart.session.session_id
  const ordRaw = toAgyId(ordPid)

  // Verify on-disk isolation and base AGENTS.md preservation.
  assert.equal(readFileSync(env.baseAgentsPath, 'utf8'), env.baseAgentsContent, 'shared AGENTS.md is never modified')
  assert.equal(readFileSync(join(supPolicyDir, 'AGENTS.md'), 'utf8'), policy)
  assert.equal(existsSync(policyDirOf(env.policyRoot, ordRaw)), false, 'ordinary chat gets no supervision policy dir')
  assert.equal(isPolicyDir(env.policyRoot, supPolicyDir), true)
  assert.equal(isPolicyDir(env.policyRoot, env.assistantDir), false)

  // Verify ACP additionalDirectories per session.
  const resumes = received(env.record).filter((m) => m.method === 'session/resume')
  const supResume = resumes.find((m) => m.params.sessionId === supRaw)
  const ordResume = resumes.find((m) => m.params.sessionId === ordRaw)
  assert.deepEqual(supResume?.params.additionalDirectories, [supPolicyDir])
  assert.equal(ordResume?.params.additionalDirectories, undefined, 'ordinary chat never passes additionalDirectories')

  // Send messages to both; verify user prompt text never contains supervision policy.
  await n.peer.call('session.send', {
    session_id: supPid,
    text: 'Check worker status',
    assistant: true,
    supervision_instructions: policy,
    client_msg_id: 'm-sup',
  })
  await n.peer.call('session.send', {
    session_id: ordPid,
    text: 'Hello ordinary chat',
    assistant: true,
    supervision_instructions: '',
    client_msg_id: 'm-ord',
  })
  await until('both prompts completed', () => received(env.record).filter((m) => m.ended).length >= 2, SLOW)

  const prompts = received(env.record).filter((m) => m.method === 'session/prompt')
  const supPromptText = prompts.find((m) => m.params.sessionId === supRaw)?.params.prompt?.[0]?.text || ''
  const ordPromptText = prompts.find((m) => m.params.sessionId === ordRaw)?.params.prompt?.[0]?.text || ''
  assert.equal(supPromptText.includes('SUPERVISION_ROLE_MARKER'), false, 'policy must never be concatenated into user prompt text')
  assert.equal(supPromptText.includes('Check worker status'), true)
  assert.equal(ordPromptText.includes('SUPERVISION_ROLE_MARKER'), false)
  assert.equal(readFileSync(env.baseAgentsPath, 'utf8'), env.baseAgentsContent)

  // workspacePathOf skips managed policy directories so they are never mistaken for session cwd.
  const fakeSummary = { WorkspaceURIs: [`file://${supPolicyDir}`, `file://${env.assistantDir}`] }
  assert.equal(workspacePathOf(fakeSummary, env.dir, (p) => isPolicyDir(env.policyRoot, p)), env.assistantDir)
  assert.equal(workspacePathOf({ WorkspaceURIs: [`file://${supPolicyDir}`] }, env.dir, (p) => isPolicyDir(env.policyRoot, p)), env.dir)
})

test('dynamic promotion, in-place update, >24KB split, demotion, session.modes preservation, and close cleanup', async (t) => {
  const env = setupAgy(t)
  const { n } = await startRuntime(t, env)

  // Start as an ordinary chat (no policy).
  const { session } = await n.peer.call('session.start', { cwd: env.assistantDir, assistant: true })
  const pid = session.session_id
  const raw = toAgyId(pid)
  const policyDir = policyDirOf(env.policyRoot, raw)
  assert.equal(existsSync(policyDir), false)

  // Promote dynamically on session.send.
  const policyV1 = 'POLICY_V1\nAct as supervisor.'
  await n.peer.call('session.send', { session_id: pid, text: 'turn 1', assistant: true, supervision_instructions: policyV1, client_msg_id: 't1' })
  await until('turn 1 done', () => received(env.record).filter((m) => m.ended === raw).length >= 1, SLOW)
  assert.equal(readPolicy(env.policyRoot, raw), policyV1)
  const afterPromote = received(env.record).filter((m) => m.method === 'session/resume' && m.params.sessionId === raw)
  assert.deepEqual(afterPromote.at(-1)?.params.additionalDirectories, [policyDir])

  // session.modes keeps active additionalDirectories intact.
  await n.peer.call('session.modes', { session_id: pid, permission_mode: 'default' })
  const afterModes = received(env.record).filter((m) => m.method === 'session/resume' && m.params.sessionId === raw)
  assert.deepEqual(afterModes.at(-1)?.params.additionalDirectories, [policyDir])

  // Update with a >24,000-byte CJK + ASCII policy (splits cleanly across AGENTS.md + GEMINI.md).
  const line = '规则行：请先检查任务验收标准再决定下一步。\n'
  const bigPolicy = line.repeat(Math.ceil((AGY_RULE_MAX_BYTES + 4000) / Buffer.byteLength(line, 'utf8')))
  assert.ok(Buffer.byteLength(bigPolicy, 'utf8') > AGY_RULE_MAX_BYTES)
  assert.ok(Buffer.byteLength(bigPolicy, 'utf8') <= AGY_RULE_MAX_BYTES * 2)
  await n.peer.call('session.send', { session_id: pid, text: 'turn 2', assistant: true, supervision_instructions: bigPolicy, client_msg_id: 't2' })
  await until('turn 2 done', () => received(env.record).filter((m) => m.ended === raw).length >= 2, SLOW)
  const primaryBytes = Buffer.byteLength(readFileSync(join(policyDir, 'AGENTS.md'), 'utf8'), 'utf8')
  const secondaryBytes = Buffer.byteLength(readFileSync(join(policyDir, 'GEMINI.md'), 'utf8'), 'utf8')
  assert.ok(primaryBytes > 0 && primaryBytes <= AGY_RULE_MAX_BYTES)
  assert.ok(secondaryBytes > 0 && secondaryBytes <= AGY_RULE_MAX_BYTES)
  assert.equal(readPolicy(env.policyRoot, raw), bigPolicy, 'split policy round-trips byte-for-byte')

  // Shrinking back under 24KB removes secondary GEMINI.md.
  const policyV2 = 'POLICY_V2\nShort again.'
  await n.peer.call('session.send', { session_id: pid, text: 'turn 3', assistant: true, supervision_instructions: policyV2, client_msg_id: 't3' })
  await until('turn 3 done', () => received(env.record).filter((m) => m.ended === raw).length >= 3, SLOW)
  assert.equal(readFileSync(join(policyDir, 'AGENTS.md'), 'utf8'), policyV2)
  assert.equal(existsSync(join(policyDir, 'GEMINI.md')), false, 'secondary file removed once policy fits in primary')

  // Demote back to ordinary chat via empty string: files removed, session reloaded without additionalDirectories.
  const resumesBeforeDemote = received(env.record).filter((m) => m.method === 'session/resume' && m.params.sessionId === raw).length
  await n.peer.call('session.send', { session_id: pid, text: 'turn 4', assistant: true, supervision_instructions: '', client_msg_id: 't4' })
  await until('turn 4 done', () => received(env.record).filter((m) => m.ended === raw).length >= 4, SLOW)
  assert.equal(existsSync(policyDir), false, 'demotion removes the session policy directory')
  const resumesAfterDemote = received(env.record).filter((m) => m.method === 'session/resume' && m.params.sessionId === raw)
  assert.ok(resumesAfterDemote.length > resumesBeforeDemote, 'demotion reloads the session')
  assert.equal(resumesAfterDemote.at(-1)?.params.additionalDirectories, undefined, 'demoted session drops additionalDirectories')

  // Re-promote and verify session.close cleans up the directory.
  await n.peer.call('session.send', { session_id: pid, text: 'turn 5', assistant: true, supervision_instructions: policyV1, client_msg_id: 't5' })
  await until('turn 5 done', () => received(env.record).filter((m) => m.ended === raw).length >= 5, SLOW)
  assert.equal(existsSync(policyDir), true)
  await n.peer.call('session.close', { session_id: pid })
  assert.equal(existsSync(policyDir), false, 'session.close removes the session policy directory')
})

test('busy role change is refused without altering disk policy; same policy while busy still queues', async (t) => {
  const env = setupAgy(t)
  const { n } = await startRuntime(t, env)

  const policyV1 = 'BUSY_POLICY_V1'
  const { session } = await n.peer.call('session.start', {
    cwd: env.assistantDir,
    assistant: true,
    supervision_instructions: policyV1,
  })
  const pid = session.session_id
  const raw = toAgyId(pid)

  // Start a long-running prompt ('hold').
  await n.peer.call('session.send', {
    session_id: pid,
    text: 'hold this turn',
    assistant: true,
    supervision_instructions: policyV1,
    client_msg_id: 'busy-1',
  })
  await until('prompt running', () => received(env.record).some((m) => m.method === 'session/prompt'), SLOW)

  // Attempting to change supervision_instructions while busy is refused with code 'busy'.
  await assert.rejects(
    n.peer.call('session.send', {
      session_id: pid,
      text: 'change policy now',
      assistant: true,
      supervision_instructions: 'BUSY_POLICY_V2',
      client_msg_id: 'busy-change',
    }),
    (err) => err.code === 'busy',
  )
  assert.equal(readPolicy(env.policyRoot, raw), policyV1, 'failed busy change must leave on-disk policy untouched')

  // Sending with the SAME policy while busy queues cleanly without error.
  await n.peer.call('session.send', {
    session_id: pid,
    text: 'follow-up same policy',
    assistant: true,
    supervision_instructions: policyV1,
    client_msg_id: 'busy-same',
  })

  // Interrupt cancels both the held turn and its queued follow-up.
  await n.peer.call('session.interrupt', { session_id: pid })
  await until('session stopped', () => n.last('runtime.sessions')?.sessions.find((row) => row.session_id === pid)?.status !== 'working', SLOW)
  assert.equal(received(env.record).filter((m) => m.ended === raw).length, 1)
  assert.equal(received(env.record).filter((m) => m.method === 'session/prompt').length, 1)
  assert.equal(readPolicy(env.policyRoot, raw), policyV1)
})

test('fail closed when agy acp does not advertise additionalDirectories; ordinary chats still work', async (t) => {
  const env = setupAgy(t, { acpFlags: ['--no-additional-dirs'] })
  const { n } = await startRuntime(t, env)

  // Supervised session.start fails closed with assistant_supervision_runtime and creates no ACP session.
  await assert.rejects(
    n.peer.call('session.start', {
      cwd: env.assistantDir,
      assistant: true,
      supervision_instructions: 'SUPERVISION_ONLY',
    }),
    (err) => err.code === SUPERVISION_ERROR_CODE,
  )
  assert.equal(received(env.record).filter((m) => m.method === 'session/new').length, 0)

  // Ordinary session.start and session.send without policy succeed.
  const { session } = await n.peer.call('session.start', { cwd: env.assistantDir, assistant: true })
  const pid = session.session_id
  const raw = toAgyId(pid)
  await n.peer.call('session.send', {
    session_id: pid,
    text: 'ordinary message',
    assistant: true,
    supervision_instructions: '',
    client_msg_id: 'ord-ok',
  })
  await until('ordinary turn done', () => received(env.record).filter((m) => m.ended === raw).length >= 1, SLOW)

  // Promoting the ordinary session to supervised fails closed without sending session/prompt.
  const promptsBefore = received(env.record).filter((m) => m.method === 'session/prompt').length
  await assert.rejects(
    n.peer.call('session.send', {
      session_id: pid,
      text: 'supervision message',
      assistant: true,
      supervision_instructions: 'SUPERVISION_ONLY',
      client_msg_id: 'sup-fail',
    }),
    (err) => err.code === SUPERVISION_ERROR_CODE,
  )
  assert.equal(received(env.record).filter((m) => m.method === 'session/prompt').length, promptsBefore, 'prompt must never run when policy cannot be applied')
  assert.equal(existsSync(policyDirOf(env.policyRoot, raw)), false)
})

test('fail closed on unwritable policyRoot, oversized policy (>48KB), and roll back disk policy on resume error', async (t) => {
  // 1. Unwritable policyRoot (a regular file instead of a directory).
  const badEnv = setupAgy(t)
  const fileAsRoot = join(badEnv.dir, 'not-a-dir')
  writeFileSync(fileAsRoot, 'blocker')
  badEnv.policyRoot = fileAsRoot
  const { n: badNode } = await startRuntime(t, badEnv)
  await assert.rejects(
    badNode.peer.call('session.start', {
      cwd: badEnv.assistantDir,
      assistant: true,
      supervision_instructions: 'SUPERVISION_POLICY',
    }),
    (err) => err.code === SUPERVISION_ERROR_CODE,
  )
  assert.equal(received(badEnv.record).filter((m) => m.method === 'session/prompt').length, 0)

  // 2. Oversized policy (> 48,000 bytes) fails closed and leaves prior policy intact.
  const sizeEnv = setupAgy(t)
  const { n: sizeNode } = await startRuntime(t, sizeEnv)
  const { session } = await sizeNode.peer.call('session.start', {
    cwd: sizeEnv.assistantDir,
    assistant: true,
    supervision_instructions: 'INITIAL_OK',
  })
  const raw = toAgyId(session.session_id)
  const tooBig = 'x'.repeat(AGY_RULE_MAX_BYTES * 2 + 10)
  await assert.rejects(
    sizeNode.peer.call('session.send', {
      session_id: session.session_id,
      text: 'oversized',
      assistant: true,
      supervision_instructions: tooBig,
      client_msg_id: 'too-big',
    }),
    (err) => err.code === SUPERVISION_ERROR_CODE,
  )
  assert.equal(readPolicy(sizeEnv.policyRoot, raw), 'INITIAL_OK', 'oversized policy leaves previous valid policy intact')
  assert.equal(received(sizeEnv.record).filter((m) => m.method === 'session/prompt').length, 0)

  // 3. Direct syncPolicy rejects path traversal session ids.
  assert.throws(() => syncPolicy(sizeEnv.policyRoot, '../escape', 'bad'), (err) => err.code === SUPERVISION_ERROR_CODE)

  // 4. Failed session/resume during policy change rolls back disk policy and never sends prompt.
  const failEnv = setupAgy(t, { acpFlags: ['--fail-resume'] })
  const { n: failNode } = await startRuntime(t, failEnv)
  await assert.rejects(
    failNode.peer.call('session.start', {
      cwd: failEnv.assistantDir,
      assistant: true,
      supervision_instructions: 'SHOULD_ROLL_BACK',
    }),
    /refused additionalDirectories/,
  )
  const created = received(failEnv.record).find((m) => m.method === 'session/new')
  assert.ok(created)
  assert.equal(received(failEnv.record).filter((m) => m.method === 'session/prompt').length, 0)
  assert.equal(existsSync(join(failEnv.policyRoot, '11111111-2222-4333-8444-000000000001')), false, 'failed start cleans up policy dir')
})


test('queued messages inherit a policy applied while the preceding send prepares', async (t) => {
  const env = setupAgy(t)
  const { n } = await startRuntime(t, env)
  const { session } = await n.peer.call('session.start', { cwd: env.assistantDir, assistant: true })
  const pid = session.session_id
  let release, entered = false, hold = true
  const gate = new Promise((resolve) => { release = resolve })
  const original = AgyAcpClient.prototype.ensureStarted
  const mocked = t.mock.method(AgyAcpClient.prototype, 'ensureStarted', async function () {
    if (hold) { hold = false; entered = true; await gate }
    return original.call(this)
  })
  const first = n.peer.call('session.send', { session_id: pid, text: 'First.', supervision_instructions: 'Review before acting.' })
  try {
    await until('first send preparing', () => entered, SLOW)
    assert.equal((await n.peer.call('session.send', { session_id: pid, text: 'Second.' })).queued, true)
  } finally {
    release()
    mocked.mock.restore()
  }
  await first
  await until('both prompts started', () => received(env.record).filter((call) => call.method === 'session/prompt').length === 2, SLOW)
  const resumes = received(env.record).filter((call) => call.method === 'session/resume')
  assert.deepEqual(resumes.at(-1).params.additionalDirectories, [policyDirOf(env.policyRoot, toAgyId(pid))])
})


test('rejected supervision preparation preserves an idle session without publishing a prompt', async (t) => {
  const env = setupAgy(t, { acpFlags: ['--no-additional-dirs'] })
  const { n } = await startRuntime(t, env)
  const { session } = await n.peer.call('session.start', { cwd: env.assistantDir, assistant: true })
  const pid = session.session_id
  await n.peer.call('session.watch', { session_id: pid })
  const before = await n.peer.call('session.items', { session_id: pid })
  const batches = n.all('session.items').length
  await assert.rejects(n.peer.call('session.send', {
    session_id: pid, text: 'Not accepted.', supervision_instructions: 'Review before acting.',
  }), (err) => err.code === SUPERVISION_ERROR_CODE)
  assert.deepEqual(await n.peer.call('session.items', { session_id: pid }), before)
  const inventories = n.all('runtime.sessions').length
  await n.peer.call('session.rename', { session_id: pid, title: 'Checked' })
  await until('inventory refreshed', () => n.all('runtime.sessions').length > inventories, SLOW)
  const row = n.last('runtime.sessions').sessions.find((row) => row.session_id === pid)
  assert.equal(row.status, 'idle')
  assert.equal(row.turn_error, undefined)
  assert.equal(n.all('session.items').slice(batches).flatMap((batch) => batch.items).some((item) => item.text === 'Not accepted.'), false)
})

test('splitPolicy accepts valid 24KB-48KB policies with early newlines and preserves UTF-8 boundaries', (t) => {
  // Single early newline followed by >24KB continuous payload (previously rejected because cut fell back to early \n).
  const earlyNl = `# Supervision\n${'a'.repeat(25_000)}`
  const r1 = splitPolicy(earlyNl)
  assert.equal(r1.primary + r1.secondary, earlyNl)
  assert.equal(Buffer.byteLength(r1.primary, 'utf8'), AGY_RULE_MAX_BYTES)
  assert.equal(Buffer.byteLength(r1.secondary, 'utf8'), Buffer.byteLength(earlyNl, 'utf8') - AGY_RULE_MAX_BYTES)

  // End-to-end syncPolicy + readPolicy round-trip for early-newline mid-line fallback.
  const base = mkdtempSync(join(tmpdir(), 'agy-policy-split-'))
  t.after(() => rmSync(base, { recursive: true, force: true }))
  syncPolicy(base, 'sess-early-nl', earlyNl)
  assert.equal(readPolicy(base, 'sess-early-nl'), earlyNl)

  // Byte-0 newline followed by >24KB payload falls back to safe byte cut.
  const byte0Nl = `\n${'a'.repeat(25_000)}`
  const r0 = splitPolicy(byte0Nl)
  assert.equal(r0.primary + r0.secondary, byte0Nl)
  assert.equal(Buffer.byteLength(r0.primary, 'utf8'), AGY_RULE_MAX_BYTES)
  assert.equal(Buffer.byteLength(r0.secondary, 'utf8'), 1_001)

  // CRLF straddling byte 24,000 round-trips byte-for-byte.
  const crlfStraddle = `${'a'.repeat(AGY_RULE_MAX_BYTES - 1)}\r\n${'b'.repeat(100)}`
  const rCr = splitPolicy(crlfStraddle)
  assert.equal(rCr.primary + rCr.secondary, crlfStraddle)
  assert.equal(Buffer.byteLength(rCr.primary, 'utf8'), AGY_RULE_MAX_BYTES)
  assert.equal(Buffer.byteLength(rCr.secondary, 'utf8'), 101)

  // 3-byte CJK char straddling byte 24,000 is not split mid-codepoint.
  const cjkStraddle = `${'a'.repeat(AGY_RULE_MAX_BYTES - 1)}中${'b'.repeat(100)}`
  const rCjk = splitPolicy(cjkStraddle)
  assert.equal(rCjk.primary + rCjk.secondary, cjkStraddle)
  assert.equal(Buffer.byteLength(rCjk.primary, 'utf8'), AGY_RULE_MAX_BYTES - 1)
  assert.equal(Buffer.byteLength(rCjk.secondary, 'utf8'), 103)

  // 4-byte emoji straddling byte 24,000 is not split mid-surrogate-pair.
  const emojiStraddle = `${'a'.repeat(AGY_RULE_MAX_BYTES - 2)}😀${'b'.repeat(100)}`
  const rEmoji = splitPolicy(emojiStraddle)
  assert.equal(rEmoji.primary + rEmoji.secondary, emojiStraddle)
  assert.equal(Buffer.byteLength(rEmoji.primary, 'utf8'), AGY_RULE_MAX_BYTES - 2)
  assert.equal(Buffer.byteLength(rEmoji.secondary, 'utf8'), 104)

  // Near-48KB policy with a single early newline.
  const nearLimit = `# Header\n${'b'.repeat(47_000)}`
  const r2 = splitPolicy(nearLimit)
  assert.equal(r2.primary + r2.secondary, nearLimit)
  assert.ok(Buffer.byteLength(r2.primary, 'utf8') <= AGY_RULE_MAX_BYTES)
  assert.ok(Buffer.byteLength(r2.secondary, 'utf8') <= AGY_RULE_MAX_BYTES)

  // When a newline leaves a tail <= maxBytes, split still prefers the clean line boundary.
  const balanced = `${'x'.repeat(20_000)}\n${'y'.repeat(20_000)}`
  const r3 = splitPolicy(balanced)
  assert.equal(r3.primary, `${'x'.repeat(20_000)}\n`)
  assert.equal(r3.secondary, 'y'.repeat(20_000))

  // Exact 48,000-byte 4-byte UTF-8 surrogate-pair policy splits cleanly without splitting a codepoint.
  const emojis = '😀'.repeat(12_000)
  assert.equal(Buffer.byteLength(emojis, 'utf8'), AGY_RULE_MAX_BYTES * 2)
  const r4 = splitPolicy(emojis)
  assert.equal(r4.primary + r4.secondary, emojis)
  assert.equal(Buffer.byteLength(r4.primary, 'utf8'), AGY_RULE_MAX_BYTES)
  assert.equal(Buffer.byteLength(r4.secondary, 'utf8'), AGY_RULE_MAX_BYTES)

  // safe === 0 when single codepoint exceeds maxBytes fails closed.
  assert.throws(
    () => splitPolicy('😀', 2),
    (err) => err.code === SUPERVISION_ERROR_CODE,
  )

  // Exceeding 48,000 bytes still fails closed.
  assert.throws(
    () => splitPolicy('z'.repeat(AGY_RULE_MAX_BYTES * 2 + 1)),
    (err) => err.code === SUPERVISION_ERROR_CODE,
  )
})

test('syncPolicy, readPolicy, and removePolicy refuse symlink traversal and replace/clear file symlinks safely', (t) => {
  const base = mkdtempSync(join(tmpdir(), 'agy-policy-sym-'))
  t.after(() => rmSync(base, { recursive: true, force: true }))

  const outsideDir = join(base, 'outside')
  mkdirSync(outsideDir, { recursive: true })
  const victim = join(outsideDir, 'victim.txt')
  writeFileSync(victim, 'UNTOUCHED_VICTIM')

  // 1. Symlinked policyRoot and ENOTDIR ancestor must fail closed with SUPERVISION_ERROR_CODE.
  const symRoot = join(base, 'sym-root')
  symlinkSync(outsideDir, symRoot)
  assert.throws(
    () => syncPolicy(symRoot, 'sess-1', 'POLICY'),
    (err) => err.code === SUPERVISION_ERROR_CODE,
  )
  assert.throws(
    () => syncPolicy(symRoot, 'sess-1', ''),
    (err) => err.code === SUPERVISION_ERROR_CODE,
  )
  const fileAncestorRoot = join(victim, 'persona-supervision')
  assert.throws(
    () => syncPolicy(fileAncestorRoot, 'sess-1', 'POLICY'),
    (err) => err.code === SUPERVISION_ERROR_CODE,
  )
  assert.throws(
    () => syncPolicy(fileAncestorRoot, 'sess-1', ''),
    (err) => err.code === SUPERVISION_ERROR_CODE,
  )
  assert.equal(readPolicy(fileAncestorRoot, 'sess-1'), '')
  assert.equal(readPolicy(symRoot, 'sess-1'), '')
  mkdirSync(join(outsideDir, 'sess-keep'), { recursive: true })
  writeFileSync(join(outsideDir, 'sess-keep', 'AGENTS.md'), 'KEEP_ME')
  removePolicy(symRoot, 'sess-keep')
  assert.equal(readFileSync(join(outsideDir, 'sess-keep', 'AGENTS.md'), 'utf8'), 'KEEP_ME')

  // 2. Symlinked session policy directory must fail closed on write, clear, and removePolicy without altering symlink or target.
  const realRoot = join(base, 'real-root')
  mkdirSync(realRoot, { recursive: true })
  const symSessionDir = join(realRoot, 'sess-dir-sym')
  symlinkSync(outsideDir, symSessionDir)
  assert.throws(
    () => syncPolicy(realRoot, 'sess-dir-sym', 'NEW_POLICY'),
    (err) => err.code === SUPERVISION_ERROR_CODE,
  )
  assert.throws(
    () => syncPolicy(realRoot, 'sess-dir-sym', ''),
    (err) => err.code === SUPERVISION_ERROR_CODE,
  )
  assert.equal(lstatSync(symSessionDir).isSymbolicLink(), true, 'failed clear leaves symlinked session dir untouched')
  removePolicy(realRoot, 'sess-dir-sym')
  assert.equal(lstatSync(symSessionDir).isSymbolicLink(), true, 'removePolicy refuses symlinked session dir')
  assert.equal(existsSync(join(outsideDir, 'AGENTS.md')), false)
  assert.equal(readPolicy(realRoot, 'sess-dir-sym'), '')

  // 3. Mixed regular + symlinked AGENTS.md / GEMINI.md causes readPolicy to fail closed ("") rather than returning partial policy.
  const sessMixed = 'sess-mixed'
  const mixedDir = join(realRoot, sessMixed)
  mkdirSync(mixedDir, { recursive: true })
  writeFileSync(join(mixedDir, 'AGENTS.md'), 'PRIMARY_OK')
  symlinkSync(victim, join(mixedDir, 'GEMINI.md'))
  assert.equal(readPolicy(realRoot, sessMixed), '', 'symlinked GEMINI.md must fail closed for the whole policy')
  rmSync(join(mixedDir, 'GEMINI.md'))
  rmSync(join(mixedDir, 'AGENTS.md'))
  symlinkSync(victim, join(mixedDir, 'AGENTS.md'))
  writeFileSync(join(mixedDir, 'GEMINI.md'), 'SECONDARY_OK')
  assert.equal(readPolicy(realRoot, sessMixed), '', 'symlinked AGENTS.md must fail closed for the whole policy')

  // 4. Directory at AGENTS.md or GEMINI.md fails closed without partially overwriting AGENTS.md.
  const sessBadFile = 'sess-dir-at-file'
  const badFileDir = join(realRoot, sessBadFile)
  mkdirSync(badFileDir, { recursive: true })
  writeFileSync(join(badFileDir, 'AGENTS.md'), 'KEEP_PRIMARY')
  mkdirSync(join(badFileDir, 'GEMINI.md'))
  assert.throws(
    () => syncPolicy(realRoot, sessBadFile, 'UPDATED_PRIMARY'),
    (err) => err.code === SUPERVISION_ERROR_CODE,
  )
  assert.equal(readFileSync(join(badFileDir, 'AGENTS.md'), 'utf8'), 'KEEP_PRIMARY', 'directory at GEMINI.md must fail before overwriting AGENTS.md')
  assert.equal(readPolicy(realRoot, sessBadFile), '')
  rmSync(join(badFileDir, 'GEMINI.md'), { recursive: true })
  rmSync(join(badFileDir, 'AGENTS.md'))
  mkdirSync(join(badFileDir, 'AGENTS.md'))
  assert.throws(
    () => syncPolicy(realRoot, sessBadFile, 'UPDATED_PRIMARY'),
    (err) => err.code === SUPERVISION_ERROR_CODE,
  )

  // 5. Symlinked AGENTS.md (even with matching contents!) is replaced atomically with a regular file without modifying victim.
  const sessId = 'sess-file-sym'
  const sessDir = join(realRoot, sessId)
  mkdirSync(sessDir, { recursive: true })
  const primaryPath = join(sessDir, 'AGENTS.md')
  const secondaryPath = join(sessDir, 'GEMINI.md')
  symlinkSync(victim, primaryPath)
  assert.equal(readPolicy(realRoot, sessId), '', 'readPolicy never follows file symlinks')
  syncPolicy(realRoot, sessId, 'UNTOUCHED_VICTIM')
  assert.equal(lstatSync(primaryPath).isSymbolicLink(), false, 'matching-content symlink must be replaced with regular file')
  assert.equal(lstatSync(primaryPath).isFile(), true)
  assert.equal(readFileSync(primaryPath, 'utf8'), 'UNTOUCHED_VICTIM')
  assert.equal(readFileSync(victim, 'utf8'), 'UNTOUCHED_VICTIM')

  // Replace symlinked AGENTS.md with different content -> victim stays untouched.
  rmSync(primaryPath)
  symlinkSync(victim, primaryPath)
  syncPolicy(realRoot, sessId, 'DIFFERENT_POLICY')
  assert.equal(lstatSync(primaryPath).isSymbolicLink(), false)
  assert.equal(readFileSync(primaryPath, 'utf8'), 'DIFFERENT_POLICY')
  assert.equal(readFileSync(victim, 'utf8'), 'UNTOUCHED_VICTIM')

  // 6. Dangling GEMINI.md symlink is removed when shrinking back to primary-only.
  symlinkSync(join(base, 'does-not-exist'), secondaryPath)
  assert.equal(lstatSync(secondaryPath).isSymbolicLink(), true)
  syncPolicy(realRoot, sessId, 'SHORT_POLICY')
  assert.equal(lstatSync(secondaryPath, { throwIfNoEntry: false }), undefined, 'dangling GEMINI.md symlink must be removed')

  // 7. Clearing policy with "" removes a real directory containing dangling symlinks cleanly.
  symlinkSync(join(base, 'missing-target'), secondaryPath)
  syncPolicy(realRoot, sessId, '')
  assert.equal(lstatSync(sessDir, { throwIfNoEntry: false }), undefined)
})
