import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { appendFileSync, chmodSync, mkdirSync, mkdtempSync, readFileSync, readlinkSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { test } from 'node:test'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { packageHash } from '../../runtime-sdk/lib/package.js'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { PACKAGE_NAME } from '../install.mjs'
import { createAgyRuntime } from '../lib/runtime.js'
import { AgyAcpClient } from '../lib/acp.js'

const FAKE = fileURLToPath(new URL('./fake-acp-stream.mjs', import.meta.url))
const RAW = '11111111-2222-4333-8444-555555555555'
const SESSION = `agy-${RAW}`

async function fixture(t, options = {}, startParams = {}) {
  const directory = mkdtempSync(join(tmpdir(), 'agy-stream-'))
  writeFileSync(join(directory, 'updates.jsonl'), '')
  writeFileSync(join(directory, 'calls.jsonl'), '')
  const win = process.platform === 'win32'
  const binary = join(directory, 'agy')
  writeFileSync(binary, `#!/bin/sh\nexec '${process.execPath}' '${FAKE}' '${directory}' "$@"\n`)
  chmodSync(binary, 0o700)
  mkdirSync(join(directory, 'config/brain', RAW), { recursive: true })
  const node = await startFakeNode(t)
  const runtime = createAgyRuntime({ ...(win ? { agyBin: process.execPath, agyArgs: [FAKE, directory, 'acp'] } : { agyBin: binary }), agyDir: join(directory, 'config'), home: directory, socketPath: node.path, log: () => {}, ...options })
  t.after(async () => { await runtime.stop(); rmSync(directory, { recursive: true, force: true }) })
  runtime.start()
  await node.ready
  await node.peer.call('session.start', { cwd: directory, ...startParams })
  await node.peer.call('session.watch', { session_id: SESSION })
  await until('initial session is listed', () => sessionStatus(node))
  const calls = () => readFileSync(join(directory, 'calls.jsonl'), 'utf8').split('\n').filter(Boolean).map(JSON.parse)
  const update = (value) => appendFileSync(join(directory, 'updates.jsonl'), `${JSON.stringify(value)}\n`)
  const chunk = async (text, expected = text) => {
    update({ sessionUpdate: 'agent_message_chunk', content: { type: 'text', text } })
    await until(`live text ${expected}`, () => node.last('session.delta')?.text === expected, 15_000)
  }
  const promptCount = (count) => until(`${count} prompts started`, () => calls().filter((r) => r.method === 'session/prompt').length === count, 15_000)
  return { node, directory, calls, update, chunk, promptCount, runtime }
}

test('AGY companion publishes background lifecycles and clears running work when ACP exits', { timeout: 20_000 }, async (t) => {
  const { node, directory, update } = await fixture(t)
  const logs = join(directory, 'config', 'brain', RAW, '.system_generated', 'logs')
  mkdirSync(logs, { recursive: true })
  const file = join(logs, 'transcript.jsonl')
  const row = () => node.last('runtime.sessions')?.sessions.find(session => session.session_id === SESSION)
  const started = new Date(Date.now() - 60_000).toISOString()
  const ended = new Date().toISOString()
  const taskID = n => `${RAW}/task-${n}`
  const step = (step_index, type, data) => ({ step_index, type, created_at: started, ...data })
  const start = (index, n, description) => step(index, 'GENERIC', {
    status: 'RUNNING',
    content: `Created At: ${started}\nTool is running as a background task with task id: ${taskID(n)}\nTask Description: ${description}`,
  })
  const append = (...steps) => appendFileSync(file, steps.map(record => JSON.stringify(record)).join('\n') + '\n')
  append(step(0, 'PLANNER_RESPONSE', { tool_calls: [
    { name: 'run_command', args: { CommandLine: JSON.stringify('echo ready') } },
    { name: 'schedule', args: { CronExpression: JSON.stringify('*/30 * * * *') } },
    { name: 'run_workflow' },
    { name: 'invoke_subagent' },
  ] }),
  start(2, 2, 'Cron: */30 * * * * Check status'), start(1, 1, 'Wait for ready'),
  start(3, 3, 'Run checks'), start(4, 4, 'Review changes'))
  const running = await until('background tasks on the companion row', () => row()?.background?.length === 4 && row(), 5000)
  assert.deepEqual(running.background.map(task => [task.id, task.kind, task.started_at]), [
    [taskID(1), 'shell', started], [taskID(2), 'monitor', started],
    [taskID(3), 'workflow', started], [taskID(4), 'agent', started],
  ])
  assert.equal(running.background[0].command, 'echo ready')
  assert.equal(running.background[1].description, 'Cron: */30 * * * * Check status')
  assert.equal(running.loop, undefined)

  append(step(5, 'SYSTEM_MESSAGE', { created_at: ended, content: `Task id "${taskID(1)}" finished with result: Ready` }),
    step(6, 'PLANNER_RESPONSE', { created_at: ended, tool_calls: [
      { name: 'manage_task', args: { Action: JSON.stringify('kill'), TaskId: JSON.stringify(taskID(2)) } },
      { name: 'manage_task', args: { Action: 'status', TaskId: taskID(4) } },
    ] }))
  await until('completion and kill are reported', () => row()?.background_ended?.length === 2, 5000)
  // The native export updates an earlier step in place, without increasing the transcript sequence.
  append(step(3, 'GENERIC', { status: 'ERROR', updated_at: ended, content: 'Workflow failed' }))
  const finished = await until('same-sequence outcome is published', () => row()?.background_ended?.length === 3 && row(), 5000)
  assert.deepEqual(finished.background_ended.map(task => [task.id, task.kind, task.status, task.ended_at]), [
    [taskID(1), 'shell', 'completed', ended], [taskID(2), 'monitor', 'killed', ended],
    [taskID(3), 'workflow', 'failed', ended],
  ])
  assert.equal(finished.background_ended[2].description, 'Run checks')
  assert.deepEqual(finished.background.map(task => task.id), [taskID(4)])

  update({ exit: true })
  const stopped = await until('exit clears the running task', () => row() && !row().background && row(), 5000)
  assert.deepEqual(stopped.background_ended, finished.background_ended)
  assert.deepEqual(stopped.agy_pane_background, finished.background)
})

test('AGY companion clears TimerCondition-cancelled and expired one-shot timers on quiet sessions', { timeout: 20_000 }, async (t) => {
  const { node, directory } = await fixture(t)
  const logs = join(directory, 'config', 'brain', RAW, '.system_generated', 'logs')
  mkdirSync(logs, { recursive: true })
  const file = join(logs, 'transcript.jsonl')
  const row = () => node.last('runtime.sessions')?.sessions.find(session => session.session_id === SESSION)
  const taskID = n => `${RAW}/task-${n}`
  const append = (...steps) => appendFileSync(file, steps.map(record => JSON.stringify(record)).join('\n') + '\n')

  // 1) TimerCondition="any" cancelled silently by another task's completion notice.
  const started = new Date(Date.now() - 5_000).toISOString()
  const finishedAt = new Date(Date.now() - 2_000).toISOString()
  append(
    { step_index: 0, type: 'PLANNER_RESPONSE', created_at: started, tool_calls: [
      { name: 'run_command', args: { CommandLine: 'npm test' } },
      { name: 'schedule', args: { DurationSeconds: '180', TimerCondition: JSON.stringify('any') } },
    ] },
    { step_index: 1, type: 'GENERIC', status: 'RUNNING', created_at: started, content: `Created At: ${started}\nTool is running as a background task with task id: ${taskID(1)}\nTask Description: Run tests` },
    { step_index: 2, type: 'GENERIC', status: 'RUNNING', created_at: started, content: `Created At: ${started}\nTool is running as a background task with task id: ${taskID(2)}\nTask Description: Timer: 3m, Prompt: Recheck` },
  )
  await until('two tasks initially running', () => row()?.background?.length === 2, 5000)

  append({
    step_index: 3,
    type: 'SYSTEM_MESSAGE',
    created_at: finishedAt,
    content: `<SYSTEM_MESSAGE>\n[Message] timestamp=${finishedAt} sender=${taskID(1)} priority=MESSAGE_PRIORITY_HIGH content=Task id "${taskID(1)}" finished with result: ok\n</SYSTEM_MESSAGE>`,
  })
  const afterCancel = await until('TimerCondition timer cleared without manage_task(list)', () => row() && !row().background && row().background_ended?.length === 2 && row(), 5000)
  assert.deepEqual(afterCancel.background_ended.map(task => [task.id, task.status]), [
    [taskID(1), 'completed'],
    [taskID(2), 'killed'],
  ])

  // 2) One-shot timer whose deadline + 60s grace passes with NO further transcript writes clears automatically.
  const soonStart = new Date(Date.now() - 60_600).toISOString()
  append(
    { step_index: 4, type: 'PLANNER_RESPONSE', created_at: soonStart, tool_calls: [{ name: 'schedule', args: { DurationSeconds: 1 } }] },
    { step_index: 5, type: 'GENERIC', status: 'RUNNING', created_at: soonStart, content: `Created At: ${soonStart}\nTool is running as a background task with task id: ${taskID(3)}\nTask Description: Timer: 1s, Prompt: Quiet expiry` },
  )
  await until('one-shot timer listed while inside grace window', () => row()?.background?.some(task => task.id === taskID(3)), 5000)
  const afterExpiry = await until('expired one-shot timer clears without transcript update', () => row() && !row().background && row().background_ended?.some(task => task.id === taskID(3)) && row(), 5000)
  assert.equal(afterExpiry.background_ended.find(task => task.id === taskID(3))?.status, 'completed')
})

test('AGY streams one assistant message at a time across tool calls', { timeout: 20_000 }, async (t) => {
  const { node, update, chunk, promptCount } = await fixture(t)
  await node.peer.call('session.send', { session_id: SESSION, text: 'Make the change.' })
  await promptCount(1)
  await chunk('Preparing ')
  await chunk('the change.', 'Preparing the change.')
  update({ sessionUpdate: 'tool_call', toolCallId: 'tool-1', title: 'Read' })
  update({ sessionUpdate: 'tool_call_update', toolCallId: 'tool-1', status: 'completed' })
  await chunk('The change is ')
  await chunk('complete.', 'The change is complete.')
  update({ end: true })
  await until('live text cleared at completion', () => node.last('session.delta')?.text === '', 15_000)
})

for (const withTool of [true, false]) {
  test(`AGY keeps text before a following thought in event order (tool: ${withTool})`, { timeout: 20_000 }, async (t) => {
    const { node, update, chunk, promptCount, runtime } = await fixture(t)
    await node.peer.call('session.send', { session_id: SESSION, text: 'Inspect.' })
    await promptCount(1)
    await chunk('I will inspect the file.')
    update({ sessionUpdate: 'agent_thought_chunk', content: { type: 'text', text: 'Check its contents first.' } })
    await until('thought after text is pushed', () => node.last('session.items')?.items.some((item) => item.kind === 'thinking'))
    if (withTool) {
      update({ sessionUpdate: 'tool_call', toolCallId: 'read', name: 'read_file' })
      await until('tool after thought is pushed', () => node.last('session.items')?.items.some((item) => item.kind === 'tool_use'))
    }
    update({ end: true })
    await until('turn settles before the final order is checked', () => runtime.idle() && node.last('session.delta')?.text === '')
    const page = await node.peer.call('session.items', { session_id: SESSION })
    assert.equal(page.items.filter((item) => item.role === 'assistant' && item.kind === 'text' && item.text === 'I will inspect the file.').length, 1)
    assert.deepEqual(page.items.map((item) => [item.role, item.kind, item.text ?? item.tool_name]), [
      ['user', 'text', 'Inspect.'],
      ['assistant', 'text', 'I will inspect the file.'],
      ['assistant', 'thinking', 'Check its contents first.'],
      ...withTool ? [['assistant', 'tool_use', 'Read']] : [],
    ])
  })
}

test('AGY normalizes closed local file:// links while streaming without rewriting incomplete URLs', { timeout: 20_000 }, async (t) => {
  const { node, update, chunk, promptCount } = await fixture(t)
  await node.peer.call('session.send', { session_id: SESSION, text: 'Show the file.' })
  await promptCount(1)
  await chunk('See [notes.md](file:///work/repo/no')
  await chunk('tes.md#L5) and ![pic](file:///work/repo/pic.png).', 'See [notes.md](/work/repo/notes.md#L5) and ![pic](/work/repo/pic.png).')
  update({ end: true })
  await until('live text cleared at completion', () => node.last('session.delta')?.text === '', 15_000)
})

test('a queued prompt does not receive the executing prompt text and starts with an empty stream', { timeout: 20_000 }, async (t) => {
  const { node, update, chunk, promptCount } = await fixture(t)
  await node.peer.call('session.send', { session_id: SESSION, text: 'First request.' })
  await promptCount(1)
  await chunk('First reply')
  await node.peer.call('session.send', { session_id: SESSION, text: 'Second request.' })
  await chunk(' complete.', 'First reply complete.')
  update({ end: true })
  await promptCount(2)
  await until('queued prompt clears the previous text', () => node.last('session.delta')?.text === '', 15_000)
  await chunk('Second reply.')
  update({ end: true })
  await until('second prompt completes', () => node.last('session.delta')?.text === '', 15_000)
})

test('each prompt clears any live text before its first response', { timeout: 20_000 }, async (t) => {
  const { node, update, chunk, promptCount } = await fixture(t)
  await node.peer.call('session.send', { session_id: SESSION, text: 'First request.' })
  await promptCount(1)
  await chunk('First reply.')
  update({ end: true })
  await until('first prompt finishes', () => node.last('session.delta')?.text === '', 15_000)
  const before = node.all('session.delta').length
  await node.peer.call('session.send', { session_id: SESSION, text: 'Second request.' })
  await promptCount(2)
  await until('new prompt publishes its empty boundary', () => node.all('session.delta').length > before, 15_000)
  assert.equal(node.last('session.delta').text, '')
  await chunk('First reply.')
  assert.equal(node.last('session.delta').text, 'First reply.', 'a new turn may legitimately repeat earlier words')
  update({ end: true })
})

test('AGY completion retires the cached reply after the viewer leaves', { timeout: 20_000 }, async (t) => {
  const { node, update, chunk, promptCount } = await fixture(t)
  await node.peer.call('session.send', { session_id: SESSION, text: 'First request.' })
  await promptCount(1)
  await chunk('Previous reply.')
  await node.peer.call('session.unwatch', { session_id: SESSION })
  update({ end: true })
  await until('unwatched completion clears the reply', () => node.last('session.delta')?.text === '', 2000)
  await node.peer.call('session.watch', { session_id: SESSION })
  await node.peer.call('session.send', { session_id: SESSION, text: 'Second request.' })
  await promptCount(2)
  assert.equal(node.last('session.delta').text, '')
  await chunk('New reply.')
  update({ end: true })
})

function transcripts(directory, compact, complete) {
  const logs = join(directory, 'config/brain', RAW, '.system_generated/logs')
  mkdirSync(logs, { recursive: true })
  for (const [file, rows] of [['transcript.jsonl', compact], ['transcript_full.jsonl', complete]]) {
    writeFileSync(join(logs, file), rows.map((row) => JSON.stringify(row)).join('\n') + '\n')
  }
}

const userStep = (index, text) => ({ step_index: index, source: 'USER_EXPLICIT', type: 'USER_INPUT', status: 'DONE', content: text })
const replyStep = (index, text) => ({ step_index: index, source: 'MODEL', type: 'PLANNER_RESPONSE', status: 'DONE', content: text })

function nativeState(directory, status, stepCount, notFullyIdle = 0) {
  const db = new DatabaseSync(join(directory, 'config/conversation_summaries.db'))
  try {
    db.exec('CREATE TABLE IF NOT EXISTS conversation_summaries (conversation_id TEXT PRIMARY KEY, status TEXT, not_fully_idle INTEGER, killed INTEGER, step_count INTEGER, title TEXT, workspace_uris TEXT, last_modified_time TEXT)')
    db.prepare("INSERT OR REPLACE INTO conversation_summaries VALUES (?, ?, ?, 0, ?, '', '[]', '')")
      .run(RAW, `CASCADE_RUN_STATUS_${status}`, notFullyIdle ? 1 : 0, stepCount)
  } finally {
    db.close()
  }
}

const sessionStatus = (node) => node.last('runtime.sessions')?.sessions.find((session) => session.session_id === SESSION)?.status

test('autonomous wakes stream work and settle only once native work is idle and exported', async (t) => {
  const { node, directory, calls, update, chunk, runtime } = await fixture(t, { exportPollMs: 20 })
  const prior = [userStep(0, 'Run the workflow.'), replyStep(1, 'Workflow started.')]
  transcripts(directory, prior, prior)
  nativeState(directory, 'IDLE', 2)

  update({ sessionUpdate: 'agent_thought_chunk', content: { text: 'Inspect the result.' } })
  await until('autonomous working status before native activity', () => sessionStatus(node) === 'working', 1000)
  await until('autonomous thought', () => node.last('session.items')?.items.some((item) => item.kind === 'thinking' && item.text === 'Inspect the result.'))
  await chunk('Reading the result.')
  update({ sessionUpdate: 'tool_call', toolCallId: 'read', name: 'read_file' })
  await until('autonomous tool and pre-tool text', () => node.last('session.items')?.items.some((item) => item.kind === 'tool_use'))
  assert.ok(node.last('session.items').items.some((item) => item.text === 'Reading the result.'))
  update({ sessionUpdate: 'tool_call_update', toolCallId: 'read', status: 'completed', rawInput: { Path: 'result.txt' }, rawOutput: 'Found it.' })
  await until('autonomous tool result', () => node.last('session.items')?.items.some((item) => item.kind === 'tool_result'))
  const live = node.last('session.items').items
  assert.equal(live.find((item) => item.kind === 'tool_use').tool_use_id, live.find((item) => item.kind === 'tool_result').tool_use_id)
  assert.match(JSON.stringify(live.find((item) => item.kind === 'tool_use').tool_input), /result\.txt/)
  await chunk('Workflow finished.')
  assert.equal(calls().filter((call) => call.method === 'session/prompt').length, 0)

  const wake = { step_index: 2, type: 'SYSTEM_MESSAGE', content: 'The workflow finished.' }
  const partial = [...prior, wake, replyStep(3, 'Reading the result.')]
  transcripts(directory, partial, partial)
  await new Promise((resolve) => setTimeout(resolve, 80))
  assert.equal(node.last('session.delta').text, 'Workflow finished.', 'an idle summary and a partial export do not settle the turn')

  const complete = [...partial, { ...replyStep(4, ''), thinking: 'Inspect the result.', tool_calls: [{ name: 'read_file', args: { Path: 'result.txt' } }] },
    { step_index: 5, type: 'GENERIC', status: 'DONE', content: 'Found it.' }, replyStep(6, 'Workflow finished.')]
  nativeState(directory, 'RUNNING', 7)
  transcripts(directory, complete, complete)
  await new Promise((resolve) => setTimeout(resolve, 80))
  assert.equal(node.last('session.delta').text, 'Workflow finished.', 'a running summary does not settle even with the reply exported')
  nativeState(directory, 'IDLE', 7)
  await until('native export replaces autonomous work', () => sessionStatus(node) === 'done' && node.last('session.delta')?.text === '' && runtime.idle())
  const final = await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(final.items.filter((item) => item.kind === 'tool_use').length, 1)
  assert.equal(final.items.filter((item) => item.text === 'Workflow finished.').length, 1)
})

test('autonomous wake retires when idle with background work so a follow-up wake preserves the prior reply', { timeout: 20_000 }, async (t) => {
  const { node, directory, update, chunk, runtime } = await fixture(t, { exportPollMs: 20, autonomousIdleMs: 5000 })
  const prior = [userStep(0, 'Start background jobs.'), replyStep(1, 'Both jobs launched.')]
  transcripts(directory, prior, prior)
  nativeState(directory, 'IDLE', 2, 1)

  await chunk('First job completed with full report.')
  const afterFirst = [
    ...prior,
    { step_index: 2, type: 'SYSTEM_MESSAGE', content: 'Job 1 completed.' },
    replyStep(3, 'First job completed with full report.'),
  ]
  transcripts(directory, afterFirst, afterFirst)
  nativeState(directory, 'IDLE', 4, 1)
  await until('first autonomous wake retires while background work remains active', () => runtime.idle() && node.last('session.delta')?.text === '', 1000)
  assert.equal(sessionStatus(node), 'working', 'not_fully_idle still keeps session status working')

  // Second background task finishes shortly after, well inside autonomousIdleMs.
  update({ sessionUpdate: 'agent_thought_chunk', content: { text: 'Inspecting job 2 output.' } })
  await until('second wake emits its own boundary before resumed thinking', () => {
    const page = node.last('session.items')
    return page?.items?.some((item) => item.kind === 'thinking' && item.text === 'Inspecting job 2 output.')
  }, 2000)
  await chunk('Checking job 2 logs now.')

  const live = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(
    live.items.map((item) => item.wake ? 'wake' : [item.role, item.kind, item.text]),
    [
      ['user', 'text', 'Start background jobs.'],
      ['assistant', 'text', 'Both jobs launched.'],
      ['system', 'turn_duration', undefined],
      'wake',
      ['assistant', 'text', 'First job completed with full report.'],
      ['system', 'turn_duration', undefined],
      'wake',
      ['assistant', 'thinking', 'Inspecting job 2 output.'],
    ],
  )
  assert.equal(node.last('session.delta').text, 'Checking job 2 logs now.')
})

for (const mode of ['thought-then-progress', 'direct-progress', 'late-flush-race']) {
  test(`an active prompt spanning a background wake preserves the prior reply (${mode})`, { timeout: 20_000 }, async (t) => {
    const { node, directory, update, chunk, promptCount, runtime } = await fixture(t, { exportPollMs: 20 }, { assistant: true })
    await node.peer.call('session.send', { session_id: SESSION, text: 'Run checks and report back.', assistant: true })
    await promptCount(1)

    // First complete reply delivered during the turn.
    const firstReply = 'Here is the complete first report with all findings.'
    await chunk(firstReply)
    const withFirstReply = [
      userStep(0, 'Run checks and report back.'),
      { ...replyStep(1, ''), tool_calls: [{ name: 'run_command', args: { CommandLine: 'npm test' } }] },
      { step_index: 2, type: 'GENERIC', status: 'RUNNING', content: 'Background task started.' },
      replyStep(3, firstReply),
    ]
    const withSystemWake = [
      ...withFirstReply,
      { step_index: 4, type: 'SYSTEM_MESSAGE', source: 'SYSTEM', status: 'DONE', content: 'Background task finished.' },
    ]

    if (mode === 'late-flush-race') {
      // Export only has the completed reply when the woken thought arrives; SYSTEM_MESSAGE lands right after.
      transcripts(directory, withFirstReply, withFirstReply)
      update({ sessionUpdate: 'agent_thought_chunk', content: { text: 'Reviewing completed task.' } })
      await until('resumed thought pushed before SYSTEM_MESSAGE flush', () =>
        node.all('session.items').some((batch) => batch.items.some((item) => item.kind === 'thinking' && item.text === 'Reviewing completed task.')),
      )
      await chunk('Analyzing background output now.')
      transcripts(directory, withSystemWake, withSystemWake)
      await until('late-flushed SYSTEM_MESSAGE retroactively inserts wake boundary', async () => {
        const page = await node.peer.call('session.items', { session_id: SESSION })
        return page.items.some((item) => item.wake === true)
      }, 2000)
    } else {
      transcripts(directory, withSystemWake, withSystemWake)
      // An idle SYSTEM_MESSAGE alone must not emit a wake before resumed model work.
      await new Promise((resolve) => setTimeout(resolve, 60))
      const beforeResume = await node.peer.call('session.items', { session_id: SESSION })
      assert.equal(beforeResume.items.some((item) => item.wake === true), false)

      if (mode === 'thought-then-progress') {
        update({ sessionUpdate: 'agent_thought_chunk', content: { text: 'Reviewing completed task.' } })
        await until('wake and resumed thought pushed', () =>
          node.all('session.items').some((batch) => batch.items.some((item) => item.kind === 'thinking' && item.text === 'Reviewing completed task.')),
        )
      }
      await chunk('Analyzing background output now.')
    }

    const live = await node.peer.call('session.items', { session_id: SESSION })
    const summary = live.items.map((item) => item.wake ? 'wake' : [item.role, item.kind, item.text])
    assert.deepEqual(summary, [
      ['user', 'text', 'Run checks and report back.'],
      ['assistant', 'text', firstReply],
      'wake',
      ...(mode === 'direct-progress' ? [] : [['assistant', 'thinking', 'Reviewing completed task.']]),
    ])
    assert.equal(node.last('session.delta').text, 'Analyzing background output now.')

    // Verify order on the wire: wake notice is delivered before the new live progress delta (for non-race modes).
    if (mode !== 'late-flush-race') {
      const relevant = node.events.filter((entry) =>
        (entry.event === 'session.items' && entry.params.items?.some((item) => item.wake === true)) ||
        (entry.event === 'session.delta' && entry.params.text === 'Analyzing background output now.'),
      )
      assert.equal(relevant[0]?.event, 'session.items', 'wake notice must reach watcher before new progress delta')
    }

    // Complete the prompt and ensure both replies survive unexported settle and final native export.
    update({ end: true })
    await until('prompt settles with both replies preserved', () => runtime.idle() && node.last('session.delta')?.text === '', 2000)
    const settled = await node.peer.call('session.items', { session_id: SESSION })
    assert.deepEqual(
      settled.items.filter((item) => item.wake || item.kind === 'text').map((item) => item.wake ? 'wake' : item.text),
      ['Run checks and report back.', firstReply, 'wake', 'Analyzing background output now.'],
    )

    const finalExport = [...withSystemWake, replyStep(5, 'Analyzing background output now.')]
    transcripts(directory, finalExport, finalExport)
    await until('native export replaces projected turn cleanly', async () => {
      const page = await node.peer.call('session.items', { session_id: SESSION })
      return page.history !== settled.history
    }, 2000)
    const nativePage = await node.peer.call('session.items', { session_id: SESSION })
    assert.deepEqual(
      nativePage.items.filter((item) => item.wake || item.kind === 'text').map((item) => item.wake ? 'wake' : item.text),
      ['Run checks and report back.', firstReply, 'wake', 'Analyzing background output now.'],
    )
  })
}

test('mid-turn narration without SYSTEM_MESSAGE does not emit a wake, while chained wakes keep every prior reply visible', { timeout: 20_000 }, async (t) => {
  const { node, directory, update, chunk, promptCount, runtime } = await fixture(t, { exportPollMs: 20 }, { assistant: true })
  await node.peer.call('session.send', { session_id: SESSION, text: 'Run multi-stage workflow.', assistant: true })
  await promptCount(1)

  // Normal mid-turn narration before a tool call (no SYSTEM_MESSAGE) must NOT emit a wake boundary.
  await chunk('Starting stage 1.')
  const narrationSteps = [userStep(0, 'Run multi-stage workflow.'), replyStep(1, 'Starting stage 1.')]
  transcripts(directory, narrationSteps, narrationSteps)
  update({ sessionUpdate: 'tool_call', toolCallId: 'bg-1', name: 'run_command' })
  await until('tool call pushed', () => node.all('session.items').some((b) => b.items.some((i) => i.kind === 'tool_use')))
  assert.equal((await node.peer.call('session.items', { session_id: SESSION })).items.some((i) => i.wake === true), false)

  // Stage 1 completes with a full reply, then wake #1 arrives -> reply 2 -> wake #2 -> progress 3.
  await chunk('Stage 1 report complete.')
  const wake1Steps = [
    ...narrationSteps,
    { ...replyStep(2, ''), tool_calls: [{ name: 'run_command', args: {} }] },
    { step_index: 3, type: 'GENERIC', status: 'RUNNING', content: 'running' },
    replyStep(4, 'Stage 1 report complete.'),
    { step_index: 5, type: 'SYSTEM_MESSAGE', source: 'SYSTEM', status: 'DONE', content: 'Stage 1 task finished.' },
  ]
  transcripts(directory, wake1Steps, wake1Steps)
  await chunk('Stage 2 report complete.')

  const wake2Steps = [
    ...wake1Steps,
    replyStep(6, 'Stage 2 report complete.'),
    { step_index: 7, type: 'SYSTEM_MESSAGE', source: 'SYSTEM', status: 'DONE', content: 'Stage 2 task finished.' },
  ]
  transcripts(directory, wake2Steps, wake2Steps)
  update({ sessionUpdate: 'agent_thought_chunk', content: { text: 'Finishing stage 3.' } })
  await chunk('Wrapping up stage 3.')

  const live = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(
    live.items.filter((i) => i.wake || i.kind === 'text').map((i) => i.wake ? 'wake' : i.text),
    ['Run multi-stage workflow.', 'Starting stage 1.', 'Stage 1 report complete.', 'wake', 'Stage 2 report complete.', 'wake'],
  )
  assert.equal(node.last('session.delta').text, 'Wrapping up stage 3.')
  update({ end: true })
  await until('prompt settles', () => runtime.idle() && node.last('session.delta')?.text === '', 2000)
})

test('autonomous wakes retire after inactivity without native export', async (t) => {
  const { node, directory, update, chunk, runtime } = await fixture(t, { autonomousIdleMs: 600, exportPollMs: 20, exportSettleTimeoutMs: 100 })
  nativeState(directory, 'IDLE', 0)
  update({ sessionUpdate: 'tool_call_update', toolCallId: 'read', name: 'read_file', status: 'completed', rawOutput: 'Found it.' })
  await until('a tool update can start the wake', () => node.last('session.items')?.items.some((item) => item.kind === 'tool_result'), 1000)
  await chunk('Still awake.')
  await until('inactivity retires the stream and working status', () => node.last('session.delta')?.text === '' && sessionStatus(node) !== 'working' && runtime.idle(), 1500)
  assert.deepEqual((await node.peer.call('session.items', { session_id: SESSION })).items, [])
  await new Promise((resolve) => setTimeout(resolve, 150))
  await chunk('Awake again.')
  const complete = [{ step_index: 0, type: 'SYSTEM_MESSAGE' }, replyStep(1, 'Awake again.')]
  transcripts(directory, complete, complete)
  nativeState(directory, 'IDLE', 2)
  await until('first normal export settles after the ephemeral wake expires', () => runtime.idle() && node.last('session.delta')?.text === '', 300)
})

for (const status of ['RUNNING', 'IDLE']) {
  test(`a wake resumes after a long tool pause with native status ${status}`, async (t) => {
    const { node, directory, update, chunk, runtime } = await fixture(t, { autonomousIdleMs: 600, exportPollMs: 20 })
    nativeState(directory, status, 0)
    update({ sessionUpdate: 'tool_call', toolCallId: 'slow-tool', name: 'run_command' })
    await until('slow tool is live', () => node.last('session.items')?.items.some((item) => item.kind === 'tool_use'))
    await until('stream retires during the tool pause', () => runtime.idle() && sessionStatus(node) !== 'working')
    if (status === 'IDLE') {
      update({ sessionUpdate: 'tool_call_update', toolCallId: 'slow-tool', status: 'completed', rawOutput: 'Finished.' })
      await until('resumed tool is complete', () => node.last('session.items')?.items.some((item) => item.kind === 'tool_result'))
    }
    await chunk('Command finished.')

    const complete = [{ step_index: 0, type: 'SYSTEM_MESSAGE' },
      { ...replyStep(1, ''), tool_calls: [{ name: 'run_command' }] },
      { step_index: 2, type: 'GENERIC', status: 'DONE', content: 'Finished.' }, replyStep(3, 'Command finished.')]
    transcripts(directory, complete, complete)
    nativeState(directory, 'IDLE', 4)
    await until('the single native wake export settles the resumed stream', () => runtime.idle() && node.last('session.delta')?.text === '', 300)
  })
}

test('an autonomous wake stays live while its permission card awaits an answer', async (t) => {
  const { node, calls, update, chunk, runtime } = await fixture(t, { autonomousIdleMs: 300, exportPollMs: 20 })
  const permissionId = 'wake-permission'
  const answer = () => calls().find((call) => call.id === permissionId)?.result?.outcome
  let answerCard
  node.resolve = () => new Promise((resolve) => { answerCard = resolve })
  await chunk('Waiting for permission.')
  update({ permission: permissionId })
  await until('permission card reaches the phone', () => answerCard)
  await new Promise((resolve) => setTimeout(resolve, 400))
  assert.equal(answer(), undefined, 'the wake must not cancel the card after its inactivity deadline')
  assert.equal(runtime.idle(), false)

  answerCard({ allow: true })
  await until('ACP permission answer', () => answer())
  assert.deepEqual(answer(), { outcome: 'selected', optionId: 'allow' })
  await new Promise((resolve) => setTimeout(resolve, 60))
  assert.equal(runtime.idle(), false, 'answering the card gives the wake a fresh inactivity window')
  await chunk('Continuing.', 'Waiting for permission.Continuing.')
  await until('wake settles after the answer and subsequent inactivity', () => runtime.idle() && node.last('session.delta')?.text === '')
})

test('empty updates cannot start an autonomous wake', async (t) => {
  const { node, update, runtime } = await fixture(t)
  const before = node.all('session.delta').length
  update({ sessionUpdate: 'agent_message_chunk', content: { text: ' \n ' } })
  update({ sessionUpdate: 'agent_thought_chunk', content: { text: ' \t ' } })
  update({ sessionUpdate: 'tool_call_update', status: 'completed' })
  update({ sessionUpdate: 'session_info_update', title: 'Updates delivered' })
  await until('ACP updates delivered', () => node.last('runtime.sessions')?.sessions[0]?.title === 'Updates delivered')
  assert.notEqual(sessionStatus(node), 'working')
  assert.equal(node.all('session.delta').length, before)
  assert.equal(runtime.idle(), true)
})

test('an installed update exits as soon as the autonomous wake retires', async (t) => {
  const store = mkdtempSync(join(tmpdir(), 'agy-wake-update-'))
  t.after(() => rmSync(store, { recursive: true, force: true }))
  const exits = []
  const { node, directory, chunk } = await fixture(t, {
    idleCheckMs: 10_000, retryMs: 20, exportPollMs: 20,
    update: { name: PACKAGE_NAME, hash: 'running', store, exit: (hash) => exits.push(hash) },
  })
  await chunk('Wake reply.')
  const files = [{ path: 'antigravity/companion.mjs', data: Buffer.from('export {}\n') }]
  const hash = packageHash(files)
  node.offer = { name: PACKAGE_NAME, hash, files: files.map((file) => ({ ...file, data: file.data.toString('base64') })) }
  node.peer.close(new Error('node upgraded'))
  await until('package installed while wake is active', () => {
    try { return readlinkSync(join(store, 'current')) === hash } catch { return false }
  })
  assert.deepEqual(exits, [])
  const steps = [replyStep(0, 'Wake reply.')]
  transcripts(directory, steps, steps)
  nativeState(directory, 'IDLE', 1)
  await until('wake retirement triggers exit without waiting for the idle poll', () => exits.length, 1500)
  assert.deepEqual(exits, [hash])
})

test('a user send takes over an autonomous wake without waiting for it', async (t) => {
  const { node, update, chunk, promptCount, runtime } = await fixture(t)
  update({ sessionUpdate: 'tool_call', toolCallId: 'read', name: 'read_file' })
  await chunk('Background progress.')
  const sent = await node.peer.call('session.send', { session_id: SESSION, text: 'Next request.' })
  assert.notEqual(sent?.queued, true)
  await promptCount(1)
  await chunk('User reply.')
  update({ end: true })
  await until('user turn settles independently', () => node.last('session.delta')?.text === '' && sessionStatus(node) !== 'working' && runtime.idle())
  const page = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(page.items.map((item) => item.text), ['Next request.', 'User reply.'])
})

test('a policy-changing send cannot resume AGY during an autonomous wake', async (t) => {
  const { node, calls, chunk } = await fixture(t)
  await chunk('Background progress.')
  const resumes = calls().filter((call) => call.method === 'session/resume').length
  await assert.rejects(node.peer.call('session.send', {
    session_id: SESSION, text: 'Next request.', supervision_instructions: 'Ask before editing files.',
  }), /Wait for the current turn to finish before changing supervision policy/)
  assert.equal(calls().filter((call) => call.method === 'session/resume').length, resumes)
  assert.equal(calls().some((call) => call.method === 'session/prompt'), false)
  assert.equal(node.last('session.delta').text, 'Background progress.')
})

test('interrupt settles an autonomous wake even when the native summary still says running', async (t) => {
  const { node, directory, calls, update, chunk, runtime } = await fixture(t)
  nativeState(directory, 'RUNNING', 0)
  update({ sessionUpdate: 'tool_call', toolCallId: 'read', name: 'read_file' })
  await chunk('Background progress.')
  await node.peer.call('session.interrupt', { session_id: SESSION })
  await until('native cancel requested', () => calls().some((call) => call.method === 'session/cancel'))
  await until('interrupt retires autonomous work', () => node.last('session.delta')?.text === '' && sessionStatus(node) !== 'working' && runtime.idle())
  assert.deepEqual((await node.peer.call('session.items', { session_id: SESSION })).items, [])
})

test('an autonomous wake preserves a previous unexported reply through interrupt and export', async (t) => {
  const { node, directory, update, chunk, promptCount, runtime } = await fixture(t)
  await node.peer.call('session.send', { session_id: SESSION, text: 'Run the workflow.' })
  await promptCount(1)
  await chunk('Workflow started.')
  update({ end: true })
  await until('prompt reply held until export', () => runtime.idle() && node.last('session.items')?.items.some((item) => item.text === 'Workflow started.'))
  const prior = node.last('session.items').history

  update({ sessionUpdate: 'tool_call', toolCallId: 'read', name: 'read_file' })
  await until('autonomous tool over the pending reply', () => node.last('session.items')?.items.some((item) => item.kind === 'tool_use'))
  assert.equal(node.last('session.items').history, prior, 'a wake over a pending reply appends')
  assert.ok(node.last('session.items').from_seq > 0)
  await node.peer.call('session.interrupt', { session_id: SESSION })
  await until('prior transcript restored and autonomous tool removed', () => node.last('session.items')?.history !== prior)
  assert.equal(node.last('session.items').from_seq, 0)
  assert.deepEqual(node.last('session.items').items.map((item) => item.text), ['Run the workflow.', 'Workflow started.'])

  await chunk('Workflow finished.')
  const complete = [userStep(0, 'Run the workflow.'), { step_index: 1, type: 'SYSTEM_MESSAGE', content: 'Progress update.' },
    replyStep(2, 'Workflow started.'), { step_index: 3, type: 'SYSTEM_MESSAGE', content: 'The workflow finished.' }, replyStep(4, 'Workflow finished.')]
  transcripts(directory, complete, complete)
  nativeState(directory, 'IDLE', 5)
  await until('both replies are exported despite a mid-prompt system message', () => runtime.idle() && node.last('session.items')?.to_seq === 5)
  const final = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(final.items.filter((item) => !item.wake && item.kind !== 'turn_duration').map((item) => item.text), ['Run the workflow.', 'Workflow started.', 'Workflow finished.'])
  assert.deepEqual(final.items.filter((item) => item.wake).map((item) => item.seq), [5])
  assert.equal(final.items.at(-1).kind, 'turn_duration')
})

test('a text-only wake preserves the unexported prompt and opens its own live exchange', async (t) => {
  const { node, directory, update, chunk, promptCount, runtime } = await fixture(t, { exportPollMs: 20 })
  await node.peer.call('session.send', { session_id: SESSION, text: 'Run the workflow.' })
  await promptCount(1)
  await chunk('Workflow started.')
  update({ end: true })
  await until('prompt reply held until export', () => runtime.idle() && node.last('session.items')?.items.some((item) => item.text === 'Workflow started.'))
  const prior = await node.peer.call('session.items', { session_id: SESSION })
  await chunk('Workflow finished.')
  const live = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(live.items.slice(0, prior.items.length), prior.items)
  assert.deepEqual(live.items.slice(prior.items.length).map((item) => [item.kind, item.wake]), [['notice', true]])
  assert.equal(node.last('session.items')?.items.at(-1)?.id, live.items.at(-1).id)

  const exported = [userStep(0, 'Run the workflow.'), replyStep(1, 'Workflow started.'), replyStep(2, 'Workflow finished.')]
  transcripts(directory, exported, exported)
  nativeState(directory, 'IDLE', 3)
  await until('both exact replies land in one markerless export', () => runtime.idle())
  const settled = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(settled.items.filter((item) => item.kind !== 'turn_duration').map(item => item.wake ? 'wake' : item.text),
    ['Run the workflow.', 'Workflow started.', 'wake', 'Workflow finished.'])
  assert.equal(settled.items.at(-1).kind, 'turn_duration')
})

test('live and native wakes keep one identity across batched notifications and a watched file attachment', async (t) => {
  const { node, directory, chunk, runtime } = await fixture(t, { exportPollMs: 20 })
  const steps = readFileSync(new URL('./fixtures/autonomous-wake.jsonl', import.meta.url), 'utf8').trim().split('\n').map(line => JSON.parse(line))
  const visible = () => node.peer.call('session.items', { session_id: SESSION })
  transcripts(directory, steps.slice(0, 3), steps.slice(0, 3))
  nativeState(directory, 'IDLE', 3)
  const prior = await visible()
  await chunk(steps[5].content)
  const live = await visible()
  assert.deepEqual(live.items.slice(0, prior.items.length), prior.items)
  assert.ok(live.items.at(-1).wake)
  const wakeID = live.items.at(-1).id
  assert.equal(node.last('session.items').items.at(-1).id, wakeID)
  transcripts(directory, steps.slice(0, 6), steps.slice(0, 6))
  nativeState(directory, 'IDLE', 6)
  await until('one completed native wake replaces the stream', () => runtime.idle() && node.last('session.items')?.to_seq === 6)
  const final = await visible()
  assert.deepEqual(final.items.slice(0, prior.items.length), prior.items)
  assert.deepEqual(final.items.filter(item => item.wake).map(item => item.id), [wakeID])
  assert.equal(final.items.filter(item => item.kind !== 'turn_duration').at(-1).text, steps[5].content)
  assert.equal(final.items.at(-1).kind, 'turn_duration')
})

test('an observed ACP wake keeps its boundary when the native export omits its scheduler notice', async (t) => {
  const { node, directory, chunk, runtime } = await fixture(t, { exportPollMs: 20 })
  const steps = readFileSync(new URL('./fixtures/autonomous-wake.jsonl', import.meta.url), 'utf8').trim().split('\n').map(line => JSON.parse(line))
  const priorSteps = steps.slice(0, 3)
  transcripts(directory, priorSteps, priorSteps)
  nativeState(directory, 'IDLE', 3)
  const read = () => node.peer.call('session.items', { session_id: SESSION })
  const prior = await read()
  await chunk('The check is complete.')
  const live = await read()
  const wake = live.items.find(item => item.wake)
  assert.ok(wake)
  const exported = [...priorSteps, { ...steps[5], step_index: 3 }]
  transcripts(directory, exported, exported)
  nativeState(directory, 'IDLE', 4)
  await until('matching native reply settles the observed ACP wake', () => runtime.idle())
  const final = await read()
  assert.deepEqual(final.items.slice(0, prior.items.length), prior.items)
  assert.deepEqual(final.items.filter(item => item.wake).map(item => item.id), [wake.id])
  assert.equal(final.items.filter(item => item.kind !== 'turn_duration').at(-1).text, 'The check is complete.')
  assert.equal(final.items.at(-1).kind, 'turn_duration')
  const receipt = readFileSync(join(directory, 'config/brain', RAW, '.persona-observed-wakes'), 'utf8')
  assert.equal(receipt, readFileSync(new URL('./fixtures/observed-wake.txt', import.meta.url), 'utf8'))

  await runtime.stop()
  const cold = await startFakeNode(t)
  const next = createAgyRuntime({ agyBin: join(directory, 'agy'), agyDir: join(directory, 'config'),
    home: directory, socketPath: cold.path, log: () => {} })
  t.after(() => next.stop())
  next.start()
  await cold.ready
  const coldPage = () => cold.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual((await coldPage()).items, final.items)
  // Reusing the same indices and timestamps in a native replacement branch is not that observed wake.
  transcripts(directory, [...exported, ...exported.slice(2)], [...exported, ...exported.slice(2)])
  assert.deepEqual((await coldPage()).items.filter(item => item.wake), [])
})

test('a late export of one wake cannot settle the next wake', async (t) => {
  const { node, directory, update, chunk, runtime } = await fixture(t, { autonomousIdleMs: 1300, exportPollMs: 20, exportSettleTimeoutMs: 500 })
  const announce = (toolCallId) => update({ sessionUpdate: 'tool_call', toolCallId, name: 'read_file' })
  announce('first-read')
  await chunk('Workflow finished.')
  await until('first wake times out unexported', () => runtime.idle() && node.last('session.delta')?.text === '')

  announce('second-read')
  await chunk('Workflow finished.')
  const first = [{ step_index: 0, type: 'SYSTEM_MESSAGE' },
    { ...replyStep(1, ''), tool_calls: [{ name: 'read_file' }] }, replyStep(2, 'Workflow finished.')]
  transcripts(directory, first, first)
  nativeState(directory, 'IDLE', 3)
  await new Promise((resolve) => setTimeout(resolve, 550))
  assert.equal(runtime.idle(), false, 'expiry does not reassign the earlier wake with the same tool and reply')
  assert.equal(node.last('session.delta').text, 'Workflow finished.')

  const next = [...first, { step_index: 3, type: 'SYSTEM_MESSAGE' }, replyStep(4, 'Workflow finished.')]
  transcripts(directory, next, next)
  nativeState(directory, 'IDLE', 5)
  await new Promise((resolve) => setTimeout(resolve, 100))
  assert.equal(runtime.idle(), false, 'the second wake cannot borrow the first wake\'s tool call')

  const complete = [...next.slice(0, -1), { ...replyStep(4, ''), tool_calls: [{ name: 'read_file' }] }, replyStep(5, 'Workflow finished.')]
  transcripts(directory, complete, complete)
  nativeState(directory, 'IDLE', 6)
  await until('second wake settles with its own export', () => runtime.idle() && node.last('session.delta')?.text === '', 400)
  const final = await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(final.items.filter((item) => item.kind === 'tool_use').length, 2)
})

for (const exportSettleTimeoutMs of [450, 1100]) {
  test(`a chained timeout protects the next wake with a ${exportSettleTimeoutMs} ms export window`, async (t) => {
    const { node, directory, chunk, runtime } = await fixture(t, { autonomousIdleMs: 700, exportPollMs: 20, exportSettleTimeoutMs })
    const reply = 'Workflow finished.'
    const steps = []
    const exportWake = () => {
      steps.push({ step_index: steps.length, type: 'SYSTEM_MESSAGE' }, replyStep(steps.length + 1, reply))
      transcripts(directory, steps, steps)
      nativeState(directory, 'IDLE', steps.length)
    }
    const retired = () => runtime.idle() && node.last('session.delta')?.text === ''

    await chunk(reply)
    await until('the first wake times out', retired)
    const firstExpiry = Date.now() + exportSettleTimeoutMs
    await chunk(reply)
    exportWake()
    await until('the second wake also times out', retired)
    await new Promise((resolve) => setTimeout(resolve, Math.max(0, firstExpiry - Date.now() + 30)))

    await chunk(reply)
    exportWake()
    await new Promise((resolve) => setTimeout(resolve, 100))
    assert.equal(runtime.idle(), false, 'the second wake export cannot settle the third wake')
    assert.equal(node.last('session.delta').text, reply)

    exportWake()
    await until('the third wake settles on its own export', retired, 350)
  })
}

test('a live wake discards only unobserved missed boundaries on expiry', async (t) => {
  const exportSettleTimeoutMs = 1100
  const { node, directory, chunk, runtime } = await fixture(t, { autonomousIdleMs: 800, exportPollMs: 20, exportSettleTimeoutMs })
  const reply = 'Workflow finished.'
  const retired = () => runtime.idle() && node.last('session.delta')?.text === ''

  await chunk(reply)
  await until('the first wake times out', retired)
  await chunk(reply)
  await until('the ephemeral second wake times out', retired)
  const expiresAt = Date.now() + exportSettleTimeoutMs

  await chunk(reply)
  const first = [{ step_index: 0, type: 'SYSTEM_MESSAGE' }, replyStep(1, reply)]
  transcripts(directory, first, first)
  nativeState(directory, 'IDLE', first.length)
  await new Promise((resolve) => setTimeout(resolve, 550))
  await chunk(' ', `${reply} `)
  await new Promise((resolve) => setTimeout(resolve, Math.max(0, expiresAt - Date.now() + 50)))
  assert.equal(runtime.idle(), false, 'expiry cannot reassign the first wake export')
  await chunk(' ', `${reply}  `)

  const complete = [...first, { step_index: 2, type: 'SYSTEM_MESSAGE' }, replyStep(3, reply)]
  transcripts(directory, complete, complete)
  nativeState(directory, 'IDLE', complete.length)
  await until('the third wake settles without an export for the second wake', retired, 350)
})

test('unknown and archived sessions cannot start autonomous work', async (t) => {
  const { node, update, chunk, runtime } = await fixture(t)
  const unknown = '66666666-2222-4333-8444-555555555555'
  await node.peer.call('session.watch', { session_id: `agy-${unknown}` })
  update({ sessionId: unknown, sessionUpdate: 'agent_message_chunk', content: { text: 'Unknown session.' } })
  await chunk('Known session.')
  assert.equal(node.all('session.delta').some((delta) => delta.text === 'Unknown session.'), false)
  await node.peer.call('session.close', { session_id: SESSION })
  await until('close retires autonomous work', () => node.last('session.delta')?.text === '' && runtime.idle() && !sessionStatus(node))

  update({ sessionUpdate: 'agent_message_chunk', content: { text: 'Archived session.' } })
  await new Promise((resolve) => setTimeout(resolve, 100))
  assert.equal(node.all('session.delta').some((delta) => delta.text === 'Archived session.'), false)
  assert.equal(runtime.idle(), true)
})

test('watched native turns push new content after an older quota error without ACP updates', async (t) => {
  const { node, directory, calls } = await fixture(t)
  const old = [userStep(0, 'First request.'), { step_index: 1, type: 'ERROR_MESSAGE', content: 'Quota exhausted.' }]
  transcripts(directory, old, old)
  await until('old quota is pushed', () => node.last('session.items')?.to_seq === 2)
  const next = [...old, userStep(2, 'Second request.'), replyStep(3, 'Current progress.')]
  transcripts(directory, next, next)
  await until('external turn is pushed', () => node.last('session.items')?.to_seq === 4)
  assert.equal(node.last('session.items').from_seq, 2)
  assert.deepEqual(node.last('session.items').items.map((item) => item.text), ['Second request.', 'Current progress.'])
  assert.equal(calls().some((call) => call.method === 'session/prompt'), false)

  await node.peer.call('session.unwatch', { session_id: SESSION })
  const before = node.all('session.items').length
  const final = [...next, replyStep(4, 'Current result.')]
  transcripts(directory, final, final)
  await new Promise((resolve) => setTimeout(resolve, 600))
  assert.equal(node.all('session.items').length, before)
  await node.peer.call('session.watch', { session_id: SESSION, since_seq: 4 })
  await until('rewatching catches up', () => node.last('session.items')?.to_seq === 5)
  assert.equal(node.last('session.items').items.at(-1).text, 'Current result.')
})

test('a native planner completion at the same index replaces watched content', async (t) => {
  const { node, directory } = await fixture(t)
  const pending = { ...replyStep(1, ''), thinking: 'Reviewing the change.', created_at: '2026-01-02T12:00:01Z' }
  const old = [userStep(0, 'Review the change.'), pending]
  transcripts(directory, old, old)
  await until('initial work is pushed', () => node.last('session.items')?.to_seq === 2)
  const history = node.last('session.items').history
  const complete = [old[0], { ...pending, content: 'Review complete.' }]
  transcripts(directory, complete, complete)
  await until('same-index completion is pushed', () => node.last('session.items')?.items.some((item) => item.text === 'Review complete.'))
  assert.equal(node.last('session.items').to_seq, 2)
  assert.notEqual(node.last('session.items').history, history)
})

test('normal AGY pages recover the complete native reply through the same source as full items', async (t) => {
  const { node, directory } = await fixture(t)
  const complete = '## Report\n' + 'A complete result with Unicode 字段.\n'.repeat(80) + 'End of report.'
  transcripts(directory,
    [userStep(0, 'Read the report.'), { ...replyStep(1, '## Report\n<truncated 4840 bytes>\nEnd of report.'), truncated_fields: ['content'] }],
    [userStep(0, 'Read the report.'), replyStep(1, complete)])
  const page = await node.peer.call('session.items', { session_id: SESSION })
  const reply = page.items.find((item) => item.role === 'assistant' && item.kind === 'text')
  assert.equal(reply.text, complete)
  assert.equal(reply.full_size, undefined)
  assert.equal(page.to_seq, 2)
  assert.notEqual(page.history, `agy:${RAW}`, 'compact transcript caches use a different history')
  const full = await node.peer.call('session.item', { session_id: SESSION, item_id: reply.id })
  assert.equal(full.text, complete)
})

test('live AGY items apply Persona page limits to the complete native reply', async (t) => {
  const { node, directory } = await fixture(t)
  const complete = '## Report\n' + 'A detailed report row.\n'.repeat(2000) + 'Literal example: <truncated 7 bytes>.'
  transcripts(directory,
    [userStep(0, 'Read the report.'), { ...replyStep(1, '## Report\n<truncated 40000 bytes>\nEnd.'), truncated_fields: ['content'] }],
    [userStep(0, 'Read the report.'), replyStep(1, complete)])
  await until('the reply is pushed', () => node.last('session.items')?.to_seq === 2)
  const reply = node.last('session.items').items.find((item) => item.role === 'assistant' && item.kind === 'text')
  assert.equal(reply.full_size, Buffer.byteLength(complete))
  assert.ok(reply.text.startsWith('## Report\nA detailed report row.'))
  const full = await node.peer.call('session.item', { session_id: SESSION, item_id: reply.id })
  assert.equal(full.text, complete)
  assert.ok(full.text.includes('Literal example: <truncated 7 bytes>.'))
})

test('AGY watch cursors follow the complete export while its compact export is ahead', async (t) => {
  const { node, directory } = await fixture(t)
  const old = [userStep(0, 'First request.'), replyStep(1, 'First reply.')]
  const nextUser = userStep(2, 'Second request.')
  transcripts(directory, [...old, nextUser], old)
  const page = await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(page.to_seq, 2)
  await until('the initial complete export is pushed', () => node.last('session.items')?.to_seq === 2)
  await node.peer.call('session.unwatch', { session_id: SESSION })
  await node.peer.call('session.watch', { session_id: SESSION })
  const complete = [...old, nextUser, replyStep(3, 'Second reply.')]
  transcripts(directory, complete, complete)
  await until('the complete export catches up', () => node.last('session.items')?.to_seq === 4)
  assert.deepEqual(node.last('session.items').items.map((item) => item.seq), [3, 4])
})

test('owned ACP turns publish the user prompt, working state and live tool steps before native transcript export', { timeout: 20_000 }, async (t) => {
  const { node, directory, update, promptCount } = await fixture(t)
  const started = await node.peer.call('session.start', { cwd: directory, assistant: true })
  assert.equal(started.session.status, 'idle')

  await node.peer.call('session.send', { session_id: SESSION, text: 'Inspect the repository.', client_msg_id: 'msg-1', assistant: true })
  await promptCount(1)
  await until('working session published', () => node.last('runtime.sessions')?.sessions.find((s) => s.session_id === SESSION)?.status === 'working')

  const liveInitial = await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(liveInitial.items.length, 1)
  assert.equal(liveInitial.items[0].role, 'user')
  assert.equal(liveInitial.items[0].text, 'Inspect the repository.')
  assert.equal(liveInitial.items[0].client_msg_id, 'msg-1')

  update({ sessionUpdate: 'agent_thought_chunk', content: { type: 'text', text: 'Checking files first.' } })
  update({ sessionUpdate: 'tool_call', toolCallId: 'tool-live-1', name: 'run_command', rawInput: { CommandLine: 'ls', toolSummary: 'List files' } })
  await until('live thought and tool call pushed', () => {
    const latest = node.last('session.items')
    return latest?.items?.some((it) => it.kind === 'thinking') && latest?.items?.some((it) => it.kind === 'tool_use' && it.tool_name === 'Bash')
  })

  const exported = [userStep(0, 'Inspect the repository.'), replyStep(1, 'Inspection complete.')]
  transcripts(directory, exported, exported)
  update({ end: true })
  await until('native export replaces live steps', () => node.last('session.items')?.items?.some((it) => it.role === 'assistant' && it.text === 'Inspection complete.'))
  const finalPage = await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(finalPage.items[0].client_msg_id, 'msg-1')
  assert.equal(finalPage.items.at(-1).text, 'Inspection complete.')
})

test('live thinking chunks, parallel tool updates and settling keep history stable without rewriting prior turns (#11129)', { timeout: 25_000 }, async (t) => {
  const { node, directory, update, promptCount } = await fixture(t)
  const prior = [
    { ...userStep(0, 'Old prompt 1'), created_at: '2026-01-01T00:00:00Z' },
    { ...replyStep(1, 'Old answer 1'), created_at: '2026-01-01T00:00:01Z' },
    { ...userStep(2, 'Old prompt 2'), created_at: '2026-01-01T00:00:02Z' },
    { ...replyStep(3, 'Old answer 2'), created_at: '2026-01-01T00:00:03Z' },
  ]
  transcripts(directory, prior, prior)
  await node.peer.call('session.unwatch', { session_id: SESSION })
  await node.peer.call('session.watch', { session_id: SESSION })

  await node.peer.call('session.send', { session_id: SESSION, text: 'Investigate the jump.', client_msg_id: 'msg-jump' })
  await promptCount(1)
  await until('live turn user prompt pushed', () => node.last('session.items')?.to_seq === 5)

  const liveHistory = node.last('session.items').history
  const startBatchCount = node.all('session.items').length
  // Seed simulated client state from the initial live page before incremental updates begin.
  const mergedById = new Map((await node.peer.call('session.items', { session_id: SESSION })).items.map((it) => [it.id, it]))
  let covered = 5

  // 1. Multiple thinking chunks separated beyond the 150ms throttle update the same thinking item in place.
  update({ sessionUpdate: 'agent_thought_chunk', content: { type: 'text', text: 'Step 1: ' } })
  await until('first thinking chunk pushed', () => node.last('session.items')?.items?.some((it) => it.kind === 'thinking' && it.text === 'Step 1:'))
  const firstThought = node.last('session.items').items.find((it) => it.kind === 'thinking')
  assert.equal(node.last('session.items').history, liveHistory)

  update({ sessionUpdate: 'agent_thought_chunk', content: { type: 'text', text: 'reading runtime.js.' } })
  await until('second thinking chunk updates same item', () =>
    node.last('session.items')?.items?.some((it) => it.id === firstThought.id && it.text === 'Step 1: reading runtime.js.'),
  )
  const secondThoughtBatch = node.last('session.items')
  assert.equal(secondThoughtBatch.history, liveHistory)
  assert.equal(secondThoughtBatch.from_seq, firstThought.seq)
  assert.equal(secondThoughtBatch.to_seq, firstThought.seq)
  assert.deepEqual(secondThoughtBatch.items.map((it) => it.id), [firstThought.id])

  // 2. Parallel tool calls, an in-place argument update, and out-of-order completion stay incremental and paired.
  update({ sessionUpdate: 'tool_call', toolCallId: 'tc-1', name: 'read_file', rawInput: {} })
  update({ sessionUpdate: 'tool_call', toolCallId: 'tc-2', name: 'run_command', rawInput: { CommandLine: 'ls' } })
  await until('both parallel tool calls pushed', () => {
    const allLive = node.all('session.items').slice(startBatchCount).flatMap((b) => b.items)
    return allLive.some((it) => it.kind === 'tool_use' && it.tool_name === 'Read') &&
      allLive.some((it) => it.kind === 'tool_use' && it.tool_name === 'Bash')
  })

  update({ sessionUpdate: 'tool_call_update', toolCallId: 'tc-1', status: 'in_progress', rawInput: { Path: '/tmp/a.js' } })
  await until('tool call input updated in place', () =>
    node.all('session.items').slice(startBatchCount).flatMap((b) => b.items)
      .some((it) => it.kind === 'tool_use' && it.tool_name === 'Read' && it.tool_input?.file_path === '/tmp/a.js'),
  )

  update({ sessionUpdate: 'tool_call_update', toolCallId: 'tc-2', status: 'completed', rawOutput: 'ok-2' })
  update({ sessionUpdate: 'tool_call_update', toolCallId: 'tc-1', status: 'failed', rawOutput: 'err-1' })
  await until('both out-of-order tool results pushed', () => {
    const allLive = node.all('session.items').slice(startBatchCount).flatMap((b) => b.items)
    return allLive.some((it) => it.kind === 'tool_result' && it.text === 'ok-2') &&
      allLive.some((it) => it.kind === 'tool_result' && it.text === 'err-1' && it.is_error === true)
  })

  // 3. A second thinking phase after tools appends chronologically without mutating the first thinking item.
  update({ sessionUpdate: 'agent_thought_chunk', content: { type: 'text', text: 'Step 2: wrapping up.' } })
  await until('second thinking block pushed', () =>
    node.all('session.items').slice(startBatchCount).flatMap((b) => b.items)
      .some((it) => it.kind === 'thinking' && it.text === 'Step 2: wrapping up.'),
  )

  // 4. Streaming final text and ending the turn before disk export settles incrementally under the same history.
  update({ sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: 'Fixed cleanly.' } })
  await until('final reply streams', () => node.last('session.delta')?.text === 'Fixed cleanly.')
  update({ end: true })
  await until('settled reply pushed incrementally', () =>
    node.last('session.items')?.items?.some((it) => it.role === 'assistant' && it.kind === 'text' && it.text === 'Fixed cleanly.'),
  )

  const liveBatches = node.all('session.items').slice(startBatchCount)
  assert.ok(liveBatches.length >= 4, `expected multiple incremental batches, got ${liveBatches.length}`)
  for (const batch of liveBatches) {
    assert.equal(batch.history, liveHistory, 'live progress and settling must not mutate history')
    assert.ok(batch.from_seq > 0, `expected incremental continuation batch, got from_seq=${batch.from_seq}`)
    assert.ok(batch.from_seq <= covered, `batch must remain continuous (from_seq=${batch.from_seq} > covered=${covered})`)
    assert.equal(batch.items.some((it) => it.seq <= 4), false, 'prior turns must not be re-transmitted during live updates')
    for (const it of batch.items) mergedById.set(it.id, it)
    covered = Math.max(covered, batch.to_seq)
  }

  const merged = [...mergedById.values()].sort((a, b) => a.seq - b.seq)
  assert.deepEqual(merged.filter((it) => it.kind !== 'turn_duration').slice(0, 4).map((it) => it.text), ['Old prompt 1', 'Old answer 1', 'Old prompt 2', 'Old answer 2'])
  const thoughts = merged.filter((it) => it.kind === 'thinking')
  assert.deepEqual(thoughts.map((it) => it.text), ['Step 1: reading runtime.js.', 'Step 2: wrapping up.'])
  const readUse = merged.find((it) => it.kind === 'tool_use' && it.tool_name === 'Read')
  const bashUse = merged.find((it) => it.kind === 'tool_use' && it.tool_name === 'Bash')
  const resultsByCall = new Map(merged.filter((it) => it.kind === 'tool_result').map((it) => [it.tool_use_id, it]))
  assert.equal(readUse.tool_input.file_path, '/tmp/a.js')
  assert.equal(resultsByCall.get(readUse.tool_use_id)?.text, 'err-1')
  assert.equal(resultsByCall.get(readUse.tool_use_id)?.is_error, true)
  assert.equal(resultsByCall.get(bashUse.tool_use_id)?.text, 'ok-2')
  assert.equal(resultsByCall.get(bashUse.tool_use_id)?.is_error, false)
  assert.equal(merged.filter((it) => it.kind !== 'turn_duration').at(-1).text, 'Fixed cleanly.')

  // 5. Only once native transcript_full.jsonl actually flushes does the history change, replacing the projected steps.
  const flushed = [
    ...prior,
    { ...userStep(4, 'Investigate the jump.'), created_at: '2026-01-01T00:00:04Z' },
    { ...replyStep(5, 'Fixed cleanly.'), thinking: 'Step 1: reading runtime.js.\nStep 2: wrapping up.', created_at: '2026-01-01T00:00:05Z' },
  ]
  transcripts(directory, flushed, flushed)
  await until('native disk export replaces synthetic settled history once', () => {
    const latest = node.last('session.items')
    return latest && latest.history !== liveHistory && latest.to_seq === 6
  })
  assert.equal(node.last('session.items').from_seq, 0)
  assert.equal(node.last('session.items').items.filter((it) => it.kind !== 'turn_duration').at(-1).text, 'Fixed cleanly.')
  assert.equal(node.last('session.items').items.at(-1).kind, 'turn_duration')
})

test('trailing thinking chunks arriving right before turn completion are not dropped on settle (#11129)', { timeout: 20_000 }, async (t) => {
  const { node, update, promptCount } = await fixture(t)
  await node.peer.call('session.send', { session_id: SESSION, text: 'Quick check.' })
  await promptCount(1)
  await until('user prompt pushed', () => node.last('session.items')?.to_seq === 1)
  const liveHistory = node.last('session.items').history

  update({ sessionUpdate: 'agent_thought_chunk', content: { type: 'text', text: 'First half' } })
  await until('first thinking chunk pushed', () => node.last('session.items')?.items?.some((it) => it.kind === 'thinking' && it.text === 'First half'))
  const thoughtItem = node.last('session.items').items.find((it) => it.kind === 'thinking')
  const afterFirstThought = node.all('session.items').length

  // Immediately append a second thinking chunk and end the turn before the 150ms thought throttle fires.
  update({ sessionUpdate: 'agent_thought_chunk', content: { type: 'text', text: ' + second half.' } })
  update({ sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: 'All good.' } })
  update({ end: true })

  await until('settled batch includes final reply', () =>
    node.last('session.items')?.items?.some((it) => it.role === 'assistant' && it.kind === 'text' && it.text === 'All good.'),
  )
  const followBatches = node.all('session.items').slice(afterFirstThought)
  for (const batch of followBatches) assert.equal(batch.history, liveHistory)
  const updatedThought = followBatches.flatMap((b) => b.items).findLast((it) => it.id === thoughtItem.id)
  assert.equal(updatedThought?.text, 'First half + second half.')
  const page = await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(page.history, liveHistory)
  assert.equal(page.items.find((it) => it.id === thoughtItem.id)?.text, 'First half + second half.')
})

test('main chat sends append under the same history; each native export changes it once (#12014)', { timeout: 25_000 }, async (t) => {
  const { node, directory, update, chunk, promptCount } = await fixture(t, {}, { assistant: true })
  let exported = [userStep(0, 'Old prompt.'), replyStep(1, 'Old answer.')]
  transcripts(directory, exported, exported)
  await until('prior turn pushed', () => node.last('session.items')?.to_seq === 2)

  for (const n of [1, 2]) {
    const idle = await node.peer.call('session.items', { session_id: SESSION })
    const pushed = node.all('session.items').length
    await node.peer.call('session.send', { session_id: SESSION, text: `Request ${n}.`, assistant: true })
    await promptCount(n)
    const sentBatch = () => node.all('session.items').slice(pushed).find((batch) => batch.items.some((item) => item.role === 'user'))
    await until('the sent message is pushed', sentBatch)
    const sent = sentBatch()
    assert.equal(sent.history, idle.history, 'a send is not a history rewrite')
    assert.equal(sent.from_seq, idle.to_seq)
    assert.equal(sent.items.some((item) => item.seq <= idle.to_seq), false, 'earlier turns are not sent again')
    assert.equal((await node.peer.call('session.items', { session_id: SESSION })).history, idle.history)

    await chunk(`Reply ${n}.`)
    update({ end: true })
    await until('the settled reply is pushed', () => node.last('session.items')?.items.some((item) => item.text === `Reply ${n}.`))
    for (const batch of node.all('session.items').slice(pushed)) {
      assert.equal(batch.history, idle.history, 'live and settled steps append')
      assert.ok(batch.from_seq > 0)
    }

    exported = [...exported, userStep(exported.length, `Request ${n}.`), replyStep(exported.length + 1, `Reply ${n}.`)]
    transcripts(directory, exported, exported)
    await until('the native export replaces the projected turn', () => node.last('session.items')?.history !== idle.history)
    await new Promise((resolve) => setTimeout(resolve, 600))
    const changes = node.all('session.items').slice(pushed).filter((batch) => batch.history !== idle.history)
    assert.equal(new Set(changes.map((batch) => batch.history)).size, 1, 'one history change per turn')
    assert.equal(changes[0].from_seq, 0)
    assert.deepEqual(changes[0].items.filter((item) => item.kind === 'text').map((item) => item.text),
      exported.map((step) => step.content))
  }
})

for (const partialTools of [false, true]) {
  test(`partial export preserves the final reply (tools: ${partialTools})`, async (t) => {
    const { node, directory, update, chunk, promptCount } = await fixture(t)
    await node.peer.call('session.send', { session_id: SESSION, text: 'Inspect.' })
    await promptCount(1)
    await chunk('Checking first.')
    update({ sessionUpdate: 'tool_call', toolCallId: 'read', name: 'read_file' })
    update({ sessionUpdate: 'tool_call_update', toolCallId: 'read', status: 'completed', rawOutput: 'Found it.' })
    await chunk('Final answer.')
    const partial = [userStep(0, 'Inspect.')]
    if (partialTools) partial.push(replyStep(1, 'Checking first.'),
      { ...replyStep(2, 'Checking a file.'), tool_calls: [{ name: 'read_file', args: {} }] },
      { step_index: 3, type: 'GENERIC', status: 'DONE', content: 'Found it.' })
    transcripts(directory, partial, partial)
    update({ end: true })
    await until('turn settled', () => node.last('session.delta')?.text === '')
    const page = await node.peer.call('session.items', { session_id: SESSION })
    assert.equal(page.items.at(-1).text, 'Final answer.')
    assert.ok(page.items.at(-1).seq > partial.at(-1).step_index + 1)
    assert.ok(page.items.some((item) => item.text === 'Checking first.'))
    const full = [...partial, replyStep(partial.at(-1).step_index + 1, 'Final answer.')]
    transcripts(directory, full, full)
    await until('final export replaces pending history', () => node.last('session.items') && node.last('session.items').history !== page.history)
    const finalItems = (await node.peer.call('session.items', { session_id: SESSION })).items
    assert.equal(finalItems.filter((item) => item.kind !== 'turn_duration').at(-1).text, 'Final answer.')
    assert.equal(finalItems.at(-1).kind, 'turn_duration')
  })
}

test('pre-tool text is committed and partial user export keeps live history stable', async (t) => {
  const { node, directory, update, chunk, promptCount } = await fixture(t)
  await node.peer.call('session.send', { session_id: SESSION, text: 'Inspect.' })
  await promptCount(1)
  const before = await node.peer.call('session.items', { session_id: SESSION })
  await chunk('Checking first.')
  update({ sessionUpdate: 'tool_call', toolCallId: 'read', name: 'read_file' })
  await until('tool pushed', () => node.last('session.items')?.items.some((item) => item.kind === 'tool_use'))
  const page = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(page.items.filter((item) => item.kind === 'text').map((item) => item.text), ['Inspect.', 'Checking first.'])
  assert.equal(node.last('session.delta').text, '')
  transcripts(directory, [userStep(0, 'Inspect.')], [userStep(0, 'Inspect.')])
  const partial = await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(partial.history, before.history)
  assert.deepEqual(partial.items, page.items)
  update({ end: true })
})

test('queued sends acknowledge the queue and defer options until execution', async (t) => {
  const { node, calls, update, chunk, promptCount } = await fixture(t)
  await node.peer.call('session.send', { session_id: SESSION, text: 'First.' })
  await promptCount(1)
  const count = calls().length
  const result = await node.peer.call('session.send', { session_id: SESSION, text: 'Second.', model: 'next-model', mode: 'plan' })
  assert.equal(result.queued, true)
  assert.equal(calls().slice(count).some((call) => call.method === 'session/set_model' || call.method === 'session/set_mode'), false)
  await chunk('First answer.')
  update({ end: true })
  await promptCount(2)
  const nextCalls = calls().slice(count)
  assert.ok(nextCalls.some((call) => call.method === 'session/set_model'))
  assert.ok(nextCalls.some((call) => call.method === 'session/set_mode'))
  await chunk('Second answer.')
  update({ end: true })
})

test('plan-mode starts advertise readonly support', async (t) => {
  const { node, directory, calls } = await fixture(t)
  assert.equal(node.hello.caps.readonly, true)
  const result = await node.peer.call('session.start', { cwd: directory, permission_mode: 'plan', readonly: true })
  assert.equal(result.session.permission_mode, 'plan')
  assert.ok(calls().some((call) => call.method === 'session/set_mode' && call.params.modeId === 'plan'))
})


for (const text of ['invalid-once', 'invalid-always']) {
  test(`stale sessions get one clean resume retry: ${text}`, async (t) => {
    const { node, calls, update, chunk, promptCount } = await fixture(t)
    const before = calls().length
    await node.peer.call('session.send', { session_id: SESSION, text })
    await promptCount(2)
    const recovery = calls().slice(before).filter((call) => ['session/prompt', 'session/resume'].includes(call.method))
    assert.deepEqual(recovery.map((call) => call.method), ['session/prompt', 'session/resume', 'session/prompt'])
    if (text === 'invalid-always') {
      await until('retry failure published', () => node.last('runtime.sessions')?.sessions.find((s) => s.session_id === SESSION)?.turn_error === 'Invalid session')
      assert.equal(calls().filter((call) => call.method === 'session/prompt').length, 2)
    } else {
      await chunk('The session resumed.')
      update({ end: true })
      await until('retried turn ends', () => node.last('runtime.sessions')?.sessions.find((s) => s.session_id === SESSION)?.status !== 'working')
    }
  })
}

for (const secondReply of ['Second answer.', 'First answer.']) {
  test(`a previous turn exported during its successor cannot replace ${secondReply}`, async (t) => {
    const { node, directory, update, chunk, promptCount } = await fixture(t)
    const idle = await node.peer.call('session.items', { session_id: SESSION })
    await node.peer.call('session.send', { session_id: SESSION, text: 'First.' })
    await promptCount(1)
    await chunk('First answer.')
    await node.peer.call('session.send', { session_id: SESSION, text: 'Second.' })
    update({ end: true })
    await promptCount(2)
    await chunk(secondReply)
    const first = [userStep(0, 'First.'), replyStep(1, 'First answer.')]
    transcripts(directory, first, first)
    update({ end: true })
    await until('second turn settled', () => node.last('session.delta')?.text === '')
    const page = await node.peer.call('session.items', { session_id: SESSION })
    assert.deepEqual(page.items.filter((item) => item.kind === 'text').map((item) => item.text),
      ['First.', 'First answer.', 'Second.', secondReply])
    assert.equal(page.history, idle.history, 'a send behind an unexported turn appends too (#12014)')
    for (const batch of node.all('session.items')) assert.equal(batch.history, idle.history)
    const full = [...first, userStep(2, 'Second.'), replyStep(3, secondReply)]
    transcripts(directory, full, full)
    await until('second export replaces pending', () => node.last('session.items') && node.last('session.items').history !== page.history)
    const pushedItems = node.last('session.items').items
    assert.equal(pushedItems.filter((item) => item.kind !== 'turn_duration').at(-1).text, secondReply)
    assert.equal(pushedItems.at(-1).kind, 'turn_duration')
  })
}


for (const externalReply of [false, true]) {
  test(`a native successor supersedes pending live history (reply: ${externalReply})`, async (t) => {
    const { node, directory, update, chunk, promptCount } = await fixture(t)
    await node.peer.call('session.send', { session_id: SESSION, text: 'First.' })
    await promptCount(1)
    await chunk('First answer.')
    update({ end: true })
    await until('first reply settled', () => node.last('session.delta')?.text === '')
    const pending = await node.peer.call('session.items', { session_id: SESSION })
    const steps = [userStep(0, 'First.'), replyStep(1, 'First answer.'), userStep(2, 'External question.')]
    if (externalReply) steps.push(replyStep(3, 'External answer.'))
    transcripts(directory, steps, steps)
    const page = await node.peer.call('session.items', { session_id: SESSION })
    assert.notEqual(page.history, pending.history)
    assert.deepEqual(page.items.filter((item) => item.kind !== 'turn_duration').map((item) => item.text), steps.map((step) => step.content))
  })
}

test('compact truncated replies eventually release pending history without losing wholly unexported replies', async (t) => {
  const { node, directory, update, chunk, promptCount } = await fixture(t, { exportSettleTimeoutMs: 150, exportPollMs: 20 })
  await node.peer.call('session.send', { session_id: SESSION, text: 'First.' })
  await promptCount(1)
  await chunk('First answer with complete details.')
  update({ end: true })
  await until('reply settled', () => node.last('session.delta')?.text === '')
  await new Promise((resolve) => setTimeout(resolve, 200))
  const pending = await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(pending.items.at(-1).text, 'First answer with complete details.')
  const logs = join(directory, 'config/brain', RAW, '.system_generated/logs')
  mkdirSync(logs, { recursive: true })
  writeFileSync(join(logs, 'transcript.jsonl'), JSON.stringify(userStep(0, 'First.')) + '\n')
  assert.equal((await node.peer.call('session.items', { session_id: SESSION })).items.at(-1).text, 'First answer with complete details.')
  writeFileSync(join(logs, 'transcript.jsonl'), [userStep(0, 'First.'),
    { ...replyStep(1, 'First answer <truncated 20 bytes>'), truncated_fields: ['content'] }].map(JSON.stringify).join('\n') + '\n')
  const page = await node.peer.call('session.items', { session_id: SESSION })
  assert.notEqual(page.history, pending.history)
  assert.equal(page.items.at(-1).text, 'First answer <truncated 20 bytes>')
})

test('a rejected initial preparation leaves idle history and error state unchanged', async (t) => {
  const { node, calls } = await fixture(t)
  const before = await node.peer.call('session.items', { session_id: SESSION })
  const batches = node.all('session.items').length
  await assert.rejects(node.peer.call('session.send', { session_id: SESSION, text: 'Do not publish this.', model: 'invalid-model' }), /Unknown model/)
  const after = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(after, before)
  const inventories = node.all('runtime.sessions').length
  await node.peer.call('session.rename', { session_id: SESSION, title: 'Checked' })
  await until('inventory refreshed', () => node.all('runtime.sessions').length > inventories)
  const row = node.last('runtime.sessions').sessions.find((row) => row.session_id === SESSION)
  assert.equal(row.status, 'idle')
  assert.equal(row.turn_error, undefined)
  assert.equal(node.all('session.items').slice(batches).flatMap((batch) => batch.items).some((item) => item.text === 'Do not publish this.'), false)
  assert.equal(calls().some((call) => call.method === 'session/prompt'), false)
})


test('preparation does not expose a user message before accepting the send', async (t) => {
  const { node } = await fixture(t)
  const before = await node.peer.call('session.items', { session_id: SESSION })
  let release, entered = false
  const gate = new Promise((resolve) => { release = resolve })
  const original = AgyAcpClient.prototype.setModel
  t.mock.method(AgyAcpClient.prototype, 'setModel', async function (...args) {
    entered = true
    await gate
    return original.apply(this, args)
  })
  const rejected = assert.rejects(node.peer.call('session.send', { session_id: SESSION, text: 'Not accepted.', model: 'invalid-model' }), /Unknown model/)
  try {
    await until('model preparation started', () => entered)
    assert.deepEqual(await node.peer.call('session.items', { session_id: SESSION }), before)
  } finally {
    release()
    await rejected
  }
})

test('whitespace before a tool call does not allocate a text step', async (t) => {
  const { node, update, promptCount } = await fixture(t)
  await node.peer.call('session.send', { session_id: SESSION, text: 'Inspect.' })
  await promptCount(1)
  update({ sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: '  \n' } })
  update({ sessionUpdate: 'tool_call', toolCallId: 'read', name: 'read_file' })
  await until('tool pushed', () => node.last('session.items')?.items.some((item) => item.kind === 'tool_use'))
  const page = await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(page.items.find((item) => item.kind === 'tool_use').seq, 2)
  assert.equal(page.to_seq, 2)
  update({ end: true })
})

test('a queued preparation failure remains visible after its queued acknowledgement', async (t) => {
  const { node, update, chunk, promptCount } = await fixture(t)
  await node.peer.call('session.send', { session_id: SESSION, text: 'First.' })
  await promptCount(1)
  assert.equal((await node.peer.call('session.send', { session_id: SESSION, text: 'Queued.', model: 'invalid-model' })).queued, true)
  await chunk('First answer.')
  update({ end: true })
  await until('queued failure published', () => node.last('runtime.sessions')?.sessions.find((row) => row.session_id === SESSION)?.turn_error?.includes('Unknown model'))
  const row = node.last('runtime.sessions').sessions.find((row) => row.session_id === SESSION)
  assert.notEqual(row.status, 'working')
})

for (const truncated of [false, true]) {
  test(`expiry preserves final replies over pre-tool narration (truncated: ${truncated})`, async (t) => {
    const { node, directory, update, chunk, promptCount } = await fixture(t, { exportSettleTimeoutMs: 150, exportPollMs: 20 })
    await node.peer.call('session.send', { session_id: SESSION, text: 'Inspect.' })
    await promptCount(1)
    await chunk('Preparing.')
    update({ sessionUpdate: 'tool_call', toolCallId: 'read', name: 'read_file' })
    await chunk('Final answer.')
    const narration = truncated ? { ...replyStep(1, 'Preparing <truncated 8 bytes>'), truncated_fields: ['content'] } : replyStep(1, 'Preparing.')
    const partial = [userStep(0, 'Inspect.'), narration]
    transcripts(directory, partial, partial)
    update({ end: true })
    await until('reply settled', () => node.last('session.delta')?.text === '')
    await new Promise((resolve) => setTimeout(resolve, 200))
    assert.equal((await node.peer.call('session.items', { session_id: SESSION })).items.at(-1).text, 'Final answer.')
  })
}

const PNG = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aF8sAAAAASUVORK5CYII=', 'base64')

for (const prompt of ['', 'Check the photo.']) {
  test(`start keeps ${prompt ? 'text and an image' : 'an image-only prompt'} through shifted native export`, async (t) => {
    const data = PNG.toString('base64')
    const client_msg_id = 'client-start'
    const { node, directory, update, promptCount, runtime } = await fixture(t, {}, {
      prompt, client_msg_id, images: [{ mime: 'image/png', data }],
    })
    await promptCount(1)
    const read = () => node.peer.call('session.items', { session_id: SESSION, turns: 1 })
    const checkUser = async (page) => {
      const user = page.items.filter((item) => item.role === 'user')
      assert.deepEqual(user.map((item) => item.kind), prompt ? ['text', 'image'] : ['image'])
      assert.ok(user.every((item) => item.client_msg_id === client_msg_id && item.seq === user[0].seq))
      const { id, tool_use_id, image } = user.at(-1)
      assert.equal(tool_use_id, undefined)
      assert.deepEqual(image, { mime: 'image/png', data: '', lazy: true })
      assert.deepEqual(await node.peer.call('session.image', { session_id: SESSION, item_id: id }), { mime: 'image/png', data })
      return id
    }
    const live = await read()
    const liveId = await checkUser(live)
    const steps = [
      { step_index: 0, type: 'CHECKPOINT' },
      { ...userStep(1, prompt ? `/plan ${prompt}` : ''), media: [{ mime_type: 'image/png', data }] },
      replyStep(2, 'Checked.'),
      { step_index: 3, type: 'CHECKPOINT' },
    ]
    transcripts(directory, steps, steps)
    nativeState(directory, 'IDLE', steps.length)
    update({ end: true })
    await until('native export settles', () => runtime.idle() && sessionStatus(node) === 'done')
    const native = await read()
    assert.notEqual(await checkUser(native), liveId)
    assert.notEqual(native.history, live.history)
    assert.deepEqual(native.items.map((item) => item.kind), [...(prompt ? ['text'] : []), 'image', 'text', 'turn_duration', 'notice'])
    assert.equal(native.items.at(-1).text, '上下文已压缩')
    assert.equal(node.last('runtime.sessions').sessions.find((row) => row.session_id === SESSION).last_text, 'Checked.')
  })
}

test('runtime pages and pushes lazy tool media, keeping Read metadata and turn pagination', async (t) => {
  const { node, directory } = await fixture(t)
  const brain = join(directory, 'config/brain', RAW)
  const screenshot = join(brain, 'screen.png')
  const steps = [
    { ...userStep(0, 'Check the screenshot.'), created_at: '2026-01-01T00:00:00Z' },
    {
      step_index: 1,
      source: 'MODEL',
      type: 'PLANNER_RESPONSE',
      status: 'DONE',
      created_at: '2026-01-01T00:00:01Z',
      tool_calls: [{ name: 'read_file', args: { Path: '/work/shots/ui.png' } }],
    },
    {
      step_index: 2,
      source: 'MODEL',
      type: 'GENERIC',
      status: 'DONE',
      created_at: '2026-01-01T00:00:02Z',
      content: 'Read image /work/shots/ui.png',
      media: [{ mime_type: 'application/pdf', uri: 'not-an-image.pdf' }, { mime_type: 'image/png', uri: 'screen.png' }],
    },
    { ...replyStep(3, 'Looks clean.'), created_at: '2026-01-01T00:00:03Z' },
    { ...userStep(4, '<USER_REQUEST>\n<attached_images>\n/tmp/only-photo.png\n</attached_images>\n</USER_REQUEST>'),
      media: [{ mime_type: 'image/png', uri: 'file:///tmp/./only-photo.png' }] },
    replyStep(5, 'Second reply.'),
  ]
  transcripts(directory, steps, steps)
  const recent = await node.peer.call('session.items', { session_id: SESSION, turns: 1 })
  assert.match(recent.history, /:lazy-media:checkpoints(?:$|:)/)
  assert.equal(recent.has_more, true)
  assert.deepEqual(recent.items.map((item) => item.text), ['<attached_images>\n/tmp/only-photo.png\n</attached_images>', 'Second reply.'])
  const paged = await node.peer.call('session.items', { session_id: SESSION, turns: 1, before_seq: recent.items[0].seq })
  assert.equal(paged.has_more, false)
  assert.equal(paged.to_seq, 4)
  const readUse = paged.items.find((it) => it.kind === 'tool_use')
  const readResult = paged.items.find((it) => it.kind === 'tool_result')
  const image = paged.items.find((it) => it.kind === 'image')
  assert.equal(readUse?.tool_name, 'Read')
  assert.deepEqual(readUse?.tool_input, { file_path: '/work/shots/ui.png' })
  assert.equal(readResult?.tool_use_id, readUse?.tool_use_id)
  assert.equal(readResult?.is_error, false)
  assert.deepEqual(image, { id: readResult.id.replace(/#0$/, '#media1'), seq: readResult.seq, role: 'assistant', kind: 'image',
    tool_use_id: readUse.tool_use_id, ts: readResult.ts, image: { mime: 'image/png', data: '', lazy: true } })
  await until('media placeholder pushed', () => node.all('session.items').some((batch) => batch.items.some((item) => item.id === image.id)))
  assert.deepEqual(node.all('session.items').flatMap((batch) => batch.items).find((item) => item.id === image.id), image)
  const params = { session_id: SESSION, item_id: image.id }
  assert.deepEqual(await node.peer.call('session.item', params), image)
  await assert.rejects(node.peer.call('session.image', params), { code: 'not_found' })
  writeFileSync(screenshot, PNG)
  assert.deepEqual(await node.peer.call('session.image', params), { mime: 'image/png', data: PNG.toString('base64') })
  await assert.rejects(node.peer.call('session.image', { ...params, item_id: recent.items[0].id.replace(/#0$/, '#media0') }), { code: 'not_found' })
})

test('runtime session.image reads only referenced local images and bounds file and inline data', { timeout: 15_000 }, async (t) => {
  const { node, directory } = await fixture(t)
  const brain = join(directory, 'config/brain', RAW)
  const max = 1152 * 1024
  const assets = join(brain, 'assets')
  const outside = join(directory, 'outside assets')
  const filename = process.platform === 'win32' ? 'capture.png' : 'capture-09:30.png'
  const absolute = join(outside, filename)
  mkdirSync(assets)
  mkdirSync(outside)
  writeFileSync(absolute, PNG)
  const atCap = Buffer.concat([PNG, Buffer.alloc(max - PNG.length)])
  writeFileSync(join(assets, 'at-cap.png'), atCap)
  writeFileSync(join(assets, 'oversized.png'), Buffer.concat([PNG, Buffer.alloc(max + 1 - PNG.length)]))
  symlinkSync('assets', join(brain, 'inside-link'), 'dir')
  symlinkSync(outside, join(brain, 'outside-link'), 'dir')
  symlinkSync('at-cap.png', join(assets, 'final-link.png'), 'file')
  const allowed = [
    { mime_type: 'image/png', uri: absolute },
    { mime_type: 'image/png', uri: pathToFileURL(absolute).href },
    { mime_type: 'image/png', uri: 'inside-link/at-cap.png' },
    { mime_type: 'image/jpeg', data: '/9j/' },
    { mime_type: 'image/gif', data: Buffer.from('GIF87a').toString('base64') },
    { mime_type: 'image/gif', data: Buffer.from('GIF89a').toString('base64') },
    { mime_type: 'image/webp', data: Buffer.from('RIFF\0\0\0\0WEBPVP8').toString('base64') },
    { mime_type: 'image/png', data: atCap.toString('base64') },
  ]
  const invalidURIs = ['missing.png', 'assets', 'assets/oversized.png', 'assets/final-link.png', `outside-link/${filename}`,
    `../../../outside assets/${filename}`, 'https://example.test/image.png', 'file://remote/image.png',
    `${pathToFileURL(absolute).href}?download`, `file://${pathToFileURL(absolute).pathname.replace(/capture/, '%2fcapture')}`, 'assets/\0.png']
  if (process.platform !== 'win32') {
    execFileSync('mkfifo', [join(assets, 'named-pipe.png')])
    invalidURIs.push('assets/named-pipe.png')
  }
  const denied = [
    ...invalidURIs.map((uri) => ({ mime_type: 'image/png', uri, data: PNG.toString('base64') })),
    { mime_type: 'image/jpeg', uri: absolute },
    { mime_type: 'image/jpeg', data: PNG.toString('base64') },
    { mime_type: 'image/gif', data: 'R0lGODlhYh==' },
    { mime_type: 'image/gif', data: 'R0lG\nODlh' },
    { mime_type: 'image/jpeg', data: '_9j_' },
    { mime_type: 'image/gif', data: Buffer.from('GIF88a').toString('base64') },
    { mime_type: 'image/webp', data: Buffer.from('RIFF\0\0\0\0WEBPVP').toString('base64') },
    { mime_type: 'image/webp', data: Buffer.from('NOPE\0\0\0\0WEBPVP8').toString('base64') },
    { mime_type: 'image/png', data: Buffer.concat([PNG, Buffer.alloc(max + 1 - PNG.length)]).toString('base64') },
  ]
  const steps = [userStep(0, 'Capture images.'), { step_index: 1, type: 'GENERIC', media: [...allowed, ...denied] }]
  transcripts(directory, steps, steps)
  const paged = await node.peer.call('session.items', { session_id: SESSION })
  const imageItems = paged.items.filter((item) => item.kind === 'image')
  assert.equal(imageItems.length, allowed.length + denied.length)
  const fetch = (item_id, extra = {}) => node.peer.call('session.image', { session_id: SESSION, item_id, ...extra })
  for (const [index, entry] of allowed.entries()) {
    const data = entry.data ?? (entry.uri === 'inside-link/at-cap.png' ? atCap : PNG).toString('base64')
    assert.deepEqual(await fetch(imageItems[index].id), { mime: entry.mime_type, data }, `reference ${index}`)
  }
  for (const [index, entry] of denied.entries()) {
    await assert.rejects(fetch(imageItems[allowed.length + index].id), { code: 'not_found' }, entry.uri ?? `inline reference ${index}`)
  }
  await assert.rejects(fetch(absolute, { uri: absolute }), { code: 'not_found' })
  await assert.rejects(fetch('2#0', { uri: absolute }), { code: 'not_found' })
  await assert.rejects(fetch(imageItems[0].id, { session_id: 'agy-22222222-3333-4444-8555-666666666666' }), { code: 'not_found' })
  const traversal = `agy-${RAW}/../${RAW}`
  await assert.rejects(fetch(imageItems[0].id, { session_id: traversal }), { code: 'not_found' })
  await assert.rejects(node.peer.call('session.items', { session_id: traversal }), { code: 'not_found' })
})
