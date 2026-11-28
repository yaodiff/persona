import assert from 'node:assert/strict'
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { describe, it } from 'node:test'
import { setTimeout as delay } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { AgyAcpClient } from '../lib/acp.js'
import { mapElicitationAnswer, mapElicitationRequest, mapToolInput, toPersonaId } from '../lib/map.js'
import { createAgyRuntime } from '../lib/runtime.js'

const FAKE = fileURLToPath(new URL('./fake-acp-cancel.mjs', import.meta.url))
const SESSION = '0c7d2c4e-5b1a-4f3e-9a6d-2f8b1c3d4e5f'
const SLOW = 15_000

function scratch(t) {
  const dir = mkdtempSync(join(tmpdir(), 'agy-elicit-'))
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
  return { dir, record }
}

function received(record) {
  return readFileSync(record, 'utf8').split('\n').filter(Boolean).map((line) => JSON.parse(line))
}

function elicitAnswers(record) {
  return received(record).filter((m) => typeof m.id === 'string' && m.id.startsWith('elicit-'))
}

async function agyRuntime(t, options = {}) {
  const { dir, record } = scratch(t)
  const agyBin = join(dir, 'agy')
  writeFileSync(agyBin, `#!/bin/sh\nexec '${process.execPath}' '${FAKE}' --record '${record}' "$@"\n`)
  chmodSync(agyBin, 0o755)
  const agyDir = join(dir, 'config')
  mkdirSync(join(agyDir, 'brain', SESSION), { recursive: true })
  const n = await startFakeNode(t, options.node)
  options.beforeStart?.(agyDir)
  const runtime = createAgyRuntime({ agyBin, agyDir, home: dir, socketPath: n.path, log: () => {}, ...options.runtime })
  t.after(() => runtime.stop())
  runtime.start()
  await n.ready
  const start = async (prompt, params) => {
    const { session } = await n.peer.call('session.start', { cwd: dir, prompt, ...params })
    assert.equal(session.session_id, toPersonaId(SESSION))
    return session.session_id
  }
  return { n, record, start, idle: () => runtime.idle() }
}

describe('antigravity ask_question elicitation mapping', () => {
  const agyTwoQuestions = {
    sessionId: SESSION,
    toolCallId: 'call_215155766166005893',
    mode: 'form',
    message: 'The agent has 2 questions.',
    requestedSchema: {
      type: 'object',
      properties: {
        q1: {
          type: 'string',
          title: 'Pick a fruit',
          oneOf: [
            { const: '1', title: '(Recommended) Apple' },
            { const: '2', title: 'Banana' },
          ],
        },
        q1_other: { type: 'string', title: 'Other', description: 'Optional free-form answer to: Pick a fruit' },
        q2: {
          type: 'array',
          title: 'Pick colors',
          items: {
            anyOf: [
              { const: '1', title: 'Red' },
              { const: '2', title: 'Green, Lime' },
              { const: '3', title: 'Blue' },
            ],
          },
        },
        q2_other: { type: 'string', title: 'Other', description: 'Optional free-form answer to: Pick colors' },
      },
    },
  }

  it('maps agy single-select and multi-select elicitation schemas and answers', () => {
    const mapped = mapElicitationRequest(agyTwoQuestions)
    assert.deepEqual(mapped.questions, [
      {
        question: 'Pick a fruit',
        multi_select: false,
        options: [{ label: '(Recommended) Apple' }, { label: 'Banana' }],
      },
      {
        question: 'Pick colors',
        multi_select: true,
        options: [{ label: 'Red' }, { label: 'Green, Lime' }, { label: 'Blue' }],
      },
    ])

    assert.deepEqual(
      mapElicitationAnswer(mapped, {
        answers: {
          'Pick a fruit': '(Recommended) Apple',
          'Pick colors': 'Red, Green, Lime, Purple',
        },
      }),
      {
        action: 'accept',
        content: {
          q1: '1',
          q2: ['1', '2'],
          q2_other: 'Purple',
        },
      },
    )

    assert.deepEqual(
      mapElicitationAnswer(mapped, {
        answers: {
          'Pick a fruit': 'Mango only',
          'Pick colors': 'Cyan',
        },
      }),
      {
        action: 'accept',
        content: {
          q1_other: 'Mango only',
          q2_other: 'Cyan',
        },
      },
    )
  })

  it('returns cancel on cancel, dismiss, or empty answers and rejects url mode', () => {
    const mapped = mapElicitationRequest(agyTwoQuestions)
    assert.deepEqual(mapElicitationAnswer(mapped, { cancel: true }), { action: 'cancel' })
    assert.deepEqual(mapElicitationAnswer(mapped, { dismiss: true }), { action: 'cancel' })
    assert.deepEqual(mapElicitationAnswer(mapped, { answers: {} }), { action: 'cancel' })
    assert.equal(mapElicitationRequest({ mode: 'url', message: 'Open URL', url: 'https://example.com' }), null)
    assert.equal(mapElicitationRequest({ mode: 'form', requestedSchema: { type: 'object', properties: {} } }), null)
  })

  it('maps generic form schemas without _other fields', () => {
    const mapped = mapElicitationRequest({
      mode: 'form',
      message: 'Configure target',
      requestedSchema: {
        type: 'object',
        properties: {
          env: { type: 'string', title: 'Environment', enum: ['staging', 'prod'] },
          replicas: { type: 'integer', title: 'Replicas' },
          confirm: { type: 'boolean', title: 'Deploy now?' },
        },
      },
    })
    assert.deepEqual(mapped.questions, [
      { question: 'Environment', multi_select: false, options: [{ label: 'staging' }, { label: 'prod' }] },
      { question: 'Replicas', multi_select: false, options: [] },
      { question: 'Deploy now?', multi_select: false, options: [{ label: 'Yes' }, { label: 'No' }] },
    ])
    assert.deepEqual(
      mapElicitationAnswer(mapped, { answers: { Environment: 'prod', Replicas: '3', 'Deploy now?': 'Yes' } }),
      { action: 'accept', content: { env: 'prod', replicas: 3, confirm: true } },
    )
  })

  it('normalizes ask_question tool inputs from both exported JSON strings and live arrays', () => {
    const raw = [{ is_multi_select: true, options: ['Red', 'Blue'], question: 'Pick colors' }]
    const expected = { questions: [{ question: 'Pick colors', multiSelect: true, options: [{ label: 'Red' }, { label: 'Blue' }] }] }
    assert.deepEqual(mapToolInput('ask_question', { questions: JSON.stringify(raw) }), expected)
    assert.deepEqual(mapToolInput('ask_question', { questions: raw }), expected)
  })
})

describe('antigravity ACP and runtime elicitation bridge', () => {
  it('advertises form elicitation only when a handler is registered and pauses inactivity timer', async (t) => {
    const { record } = scratch(t)
    let replyElicit
    const acp = new AgyAcpClient({
      bin: process.execPath,
      args: [FAKE, '--record', record, 'acp'],
      promptInactivityMs: 350,
      onElicitationRequest: () => new Promise((resolve) => { replyElicit = resolve }),
    })
    t.after(() => acp.stop())
    const prompt = acp.prompt(SESSION, [{ type: 'text', text: `elicit ${JSON.stringify({ endOnAnswer: true })}` }])
    await until('elicitation request received', () => replyElicit, SLOW)
    const init = received(record).find((m) => m.method === 'initialize')
    assert.deepEqual(init.params.clientCapabilities, { elicitation: { form: {} } })
    assert.equal(acp.permissionRequests, 1)
    await delay(450)
    assert.equal(acp.pending.size, 1, 'prompt inactivity timer stays paused while waiting on question')
    replyElicit({ action: 'accept', content: { q1: '1' } })
    assert.deepEqual(await prompt, { stopReason: 'end_turn' })
    assert.equal(acp.permissionRequests, 0)
  })

  it('bridges ask_question elicitation to a phone question card even in bypassPermissions mode', async (t) => {
    const { n, record, start, idle } = await agyRuntime(t)
    n.resolve = (ix) => {
      assert.equal(ix.kind, 'question')
      assert.equal(ix.tool_name, 'AskUserQuestion')
      assert.equal(ix.tool_use_id, 'call_q_bypass')
      assert.equal(ix.can_cancel, true)
      assert.deepEqual(ix.questions, [
        {
          question: 'Pick a fruit',
          multi_select: false,
          options: [{ label: '(Recommended) Apple' }, { label: 'Banana' }],
        },
      ])
      return { answers: { 'Pick a fruit': 'Banana' } }
    }
    await start(`elicit ${JSON.stringify({ toolCallId: 'call_q_bypass', endOnAnswer: true })}`, {
      permission_mode: 'bypassPermissions',
    })
    await until('elicitation reply sent to ACP', () => elicitAnswers(record).length === 1, SLOW)
    assert.deepEqual(elicitAnswers(record)[0].result, { action: 'accept', content: { q1: '2' } })
    await until('runtime settles idle after question turn', () => idle(), SLOW)
  })

  it('supports custom free-text answers and question cancellation without interrupting the turn', async (t) => {
    const { n, record, start, idle } = await agyRuntime(t)
    n.resolve = () => ({ answers: { 'Pick a fruit': 'Mango' } })
    const session_id = await start(`elicit ${JSON.stringify({ toolCallId: 'call_custom', endOnAnswer: true })}`)
    await until('custom answer sent', () => elicitAnswers(record).length === 1, SLOW)
    assert.deepEqual(elicitAnswers(record)[0].result, { action: 'accept', content: { q1_other: 'Mango' } })
    await until('first turn settled', () => idle(), SLOW)

    n.resolve = () => ({ cancel: true })
    await n.peer.call('session.send', {
      session_id,
      text: `elicit ${JSON.stringify({ toolCallId: 'call_cancel', endOnAnswer: true })}`,
    })
    await until('cancel reply sent', () => elicitAnswers(record).length === 2, SLOW)
    assert.deepEqual(elicitAnswers(record)[1].result, { action: 'cancel' })
    assert.equal(received(record).filter((m) => m.method === 'session/cancel').length, 0)
    await until('second turn settled', () => idle(), SLOW)
  })

  it('withdraws an open question card and replies cancel when interrupted', async (t) => {
    const { n, record, start, idle } = await agyRuntime(t)
    n.resolve = () => new Promise(() => {})
    const session_id = await start(`elicit ${JSON.stringify({ toolCallId: 'call_interrupt', endOnAnswer: false })}`, {
      assistant: true,
    })
    await until('question card is waiting', () => n.asked.length === 1, SLOW)
    assert.equal(n.asked[0].kind, 'question')
    assert.equal(idle(), false)
    assert.equal((await n.peer.call('session.interrupt', { session_id })).settled, true)
    assert.deepEqual(elicitAnswers(record)[0]?.result, { action: 'cancel' })
    await until('interaction.cancel sent to node', () => n.all('interaction.cancel').some((c) => c.id === n.asked[0].id), SLOW)
    assert.equal(idle(), true)
  })
})
