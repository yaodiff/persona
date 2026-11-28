import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, readFileSync, rmSync, utimesSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { basename, join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { test } from 'node:test'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { nativeWorking, transcriptState, transcriptStatus } from '../lib/map.js'
import { createAgyRuntime } from '../lib/runtime.js'

const ID = '11111111-2222-3333-4444-555555555555'
const SESSION = `agy-${ID}`
const at = '2026-01-02T11:59:00Z'
const step = (type, fields = {}) => ({ step_index: 0, type, status: 'DONE', created_at: at, ...fields })
const idle = { status: 'CASCADE_RUN_STATUS_IDLE', step_count: 1 }

test('native transcript stages distinguish external work from completed replies', () => {
  const cases = [
    ['user message alone', [step('USER_INPUT')], 'unknown'],
    ['unfinished response alone', [step('PLANNER_RESPONSE', { status: 'RUNNING', content: 'Reading' })], 'unknown'],
    ['tool request alone', [step('PLANNER_RESPONSE', { content: 'Running tests', tool_calls: [{ name: 'run_command' }] })], 'unknown'],
    ['completed tool alone', [step('GENERIC', { content: 'Tests passed' })], 'unknown'],
    ['final reply', [step('PLANNER_RESPONSE', { content: 'Finished' })], 'done', idle],
    ['metadata after final reply', [step('PLANNER_RESPONSE', { content: 'Finished' }), step('CHECKPOINT')], 'done', idle],
    ['progress narration while running', [step('PLANNER_RESPONSE', { content: 'Running the experiment; results will follow.' })], 'working', { status: 'CASCADE_RUN_STATUS_RUNNING' }],
    ['background work while idle', [step('PLANNER_RESPONSE', { content: 'Running the experiment' })], 'working', { ...idle, not_fully_idle: 1 }],
    ['killed conversation', [step('PLANNER_RESPONSE', { content: 'Running the experiment' })], 'idle', { status: 'CASCADE_RUN_STATUS_RUNNING', killed: 1 }],
    ['reply without native state', [step('PLANNER_RESPONSE', { content: 'Running the experiment' })], 'unknown'],
    ['unrecognized native state', [step('PLANNER_RESPONSE', { content: 'Running the experiment' })], 'unknown', { status: 'FUTURE_STATUS' }],
    ['idle summary predates narration', [step('PLANNER_RESPONSE', { step_index: 1, content: 'Running the experiment' })], 'unknown', idle],
    ['idle summary predates user input', [step('USER_INPUT', { step_index: 1 })], 'unknown', idle],
    ['stopped after a tool result', [step('GENERIC')], 'idle', idle],
    ['error', [step('ERROR_MESSAGE')], 'idle'],
    ['interrupted step', [step('GENERIC', { status: 'CANCELLED' })], 'idle'],
    ['empty history', [], 'unknown'],
    ['unfinished history without a time', [step('GENERIC', { created_at: undefined })], 'unknown'],
    ['stale unfinished history', [step('USER_INPUT', { created_at: '2025-12-01T00:00:00Z' })], 'unknown'],
    ['old completed reply', [step('PLANNER_RESPONSE', { content: 'Finished', created_at: '2025-12-01T00:00:00Z' })], 'done', idle],
  ]
  for (const [name, steps, expected, summary] of cases) {
    assert.equal(transcriptStatus(steps, summary), expected, name)
  }
})

async function fixture(t, steps, summary, options = {}) {
  const home = mkdtempSync(join(tmpdir(), 'persona-agy-status-'))
  const agyDir = join(home, 'agy')
  const logs = join(agyDir, 'brain', ID, '.system_generated', 'logs')
  mkdirSync(agyDir, { recursive: true })
  const transcript = join(logs, 'transcript.jsonl')
  const write = (values) => {
    mkdirSync(logs, { recursive: true })
    const records = values.map((value, index) => options.noIndex ? value : { ...value, step_index: index })
    writeFileSync(transcript, records.map((value) => JSON.stringify(value)).join('\n') + '\n')
  }
  if (!options.noTranscript) write(steps)
  if (options.oldTranscript) {
    const old = new Date(Date.now() - 30 * 86400_000)
    utimesSync(transcript, old, old)
  }
  if (options.internal || options.meta) {
    mkdirSync(join(agyDir, 'cache'), { recursive: true })
    writeFileSync(join(agyDir, 'cache', 'conversation_metadata.json'), JSON.stringify({ conversations: { [ID]: options.meta ?? { is_internal: true } } }))
  }
  const summaryFile = join(agyDir, 'conversation_summaries.db')
  let db
  const state = (value) => {
    if (!db) {
      db = new DatabaseSync(summaryFile)
      db.exec('CREATE TABLE conversation_summaries (conversation_id TEXT PRIMARY KEY, status TEXT, not_fully_idle NUMERIC, killed NUMERIC, step_count INTEGER, title TEXT, workspace_uris TEXT, last_modified_time TEXT)')
    }
    db.prepare('INSERT OR REPLACE INTO conversation_summaries VALUES (?, ?, ?, ?, ?, ?, ?, ?)')
      .run(ID, value.status, value.not_fully_idle ?? 0, value.killed ?? 0, value.step_count ?? steps.length,
        value.title ?? '', value.workspace_uris ?? '[]', value.last_modified_time ?? '')
  }
  if (summary) state(summary)
  const binary = join(home, 'fake-agy.mjs')
  writeFileSync(binary, `#!/usr/bin/env node
if (process.argv.includes('models')) { console.log('test-model\\tTest Model'); process.exit(0) }
import { createInterface } from 'node:readline'
createInterface({ input: process.stdin }).on('line', (line) => {
  const request = JSON.parse(line)
  if (request.id === undefined || request.method === 'session/prompt') return
  process.stdout.write(JSON.stringify({ jsonrpc: '2.0', id: request.id, result: {} }) + '\\n')
})
`, { mode: 0o700 })
  const node = await startFakeNode(t)
  const lines = []
  const runtime = createAgyRuntime({ home, agyDir, agyBin: binary, socketPath: node.path, listDays: 1, maxSessions: 1, log: (line) => lines.push(line) })
  t.after(async () => {
    await runtime.stop()
    db?.close()
    rmSync(home, { recursive: true, force: true })
  })
  runtime.start()
  await node.ready
  await until('native conversation listed', () => options.expectEmpty ? node.last('runtime.sessions')?.sessions.length === 0 : node.last('runtime.sessions')?.sessions.length)
  const refresh = async () => {
    const before = node.all('runtime.sessions').length
    await node.peer.call('session.rename', { session_id: SESSION, title: 'Renamed' })
    await until('inventory refreshed', () => node.all('runtime.sessions').length > before)
    return node.last('runtime.sessions').sessions[0]?.status
  }
  return { node, write, state, refresh, summaryFile, database: () => db, logs: lines }
}

test('untitled conversations take their title from the first line of the preview', { timeout: 15_000 }, async (t) => {
  const first = 'Draft the release checklist for the sample project and list every open question'
  const Preview = `<USER_REQUEST>\n\n  ${first}  \nSecond line\n</USER_REQUEST>`
  const { node } = await fixture(t, [step('PLANNER_RESPONSE', { content: 'Done' })], idle, { meta: { summary: { Title: '', Preview } } })
  const row = node.last('runtime.sessions').sessions[0]
  assert.equal(row.title, first.slice(0, 60).trimEnd() + '\u2026')
  // A title from the prompt says so, as the node's own prompt titles do, so Persona's naming may replace it.
  assert.equal(row.title_source, 'prompt')
})

test('a short preview is the whole title of an untitled conversation', { timeout: 15_000 }, async (t) => {
  const { node } = await fixture(t, [step('PLANNER_RESPONSE', { content: 'Done' })], idle, { meta: { summary: { Title: '', Preview: 'Summarize brief.md' } } })
  assert.equal(node.last('runtime.sessions').sessions[0].title, 'Summarize brief.md')
})

test('a preview that opens with Persona context leaves the folder name as the title', { timeout: 15_000 }, async (t) => {
  // agy keeps only the request's first line, so the context block that opens it is never there whole.
  for (const Preview of ['Content of persona-context:///snapshot.txt:', '<persona-context kind="snapshot">', '<' + 'jarvis-context kind="snapshot">']) {
    const { node } = await fixture(t, [step('PLANNER_RESPONSE', { content: 'Done' })], idle, { meta: { summary: { Title: '', Preview } } })
    const row = node.last('runtime.sessions').sessions[0]
    assert.equal(row.title, basename(row.cwd))
    assert.equal(row.title_source, undefined)
  }
})

test('runtime inventory follows an external tool turn through its final reply', { timeout: 15_000 }, async (t) => {
  const created_at = new Date().toISOString()
  const { node, write, state, refresh } = await fixture(t, [step('GENERIC', { created_at, content: 'Tool output' })], { status: 'CASCADE_RUN_STATUS_RUNNING' })
  assert.equal(node.last('runtime.sessions').sessions[0].status, 'working')
  write([step('PLANNER_RESPONSE', { created_at, content: 'Finished' })])
  state(idle)
  assert.equal(await refresh(), 'done')
})

test('runtime inventory never presents a user question as an unanswered reply', { timeout: 15_000 }, async (t) => {
  const created_at = new Date().toISOString()
  const previous = step('PLANNER_RESPONSE', { created_at, content: 'Which configuration should I use?' })
  const { node, write, state, refresh } = await fixture(t, [previous], idle)
  assert.equal(node.last('runtime.sessions').sessions[0].last_text, previous.content)
  const turn = [
    previous,
    step('USER_INPUT', { created_at, source: 'USER_EXPLICIT', content: 'What were the results?' }),
    step('PLANNER_RESPONSE', { created_at, thinking: 'Inspect the logs.', tool_calls: [{ name: 'run_command' }] }),
    step('GENERIC', { created_at, content: 'Retry the command?' }),
  ]
  write(turn)
  state({ ...idle, step_count: turn.length })
  assert.equal(await refresh(), 'idle')
  assert.equal(node.last('runtime.sessions').sessions[0].last_text, '')

  const reply = step('PLANNER_RESPONSE', { created_at, content: 'Should I retry with the other configuration?' })
  write([...turn, reply])
  state({ ...idle, step_count: turn.length + 1 })
  assert.equal(await refresh(), 'done')
  assert.equal(node.last('runtime.sessions').sessions[0].last_text, reply.content)
})

test('owned ACP turn stays working while the native transcript has the previous reply', { timeout: 15_000 }, async (t) => {
  const created_at = new Date().toISOString()
  const { node } = await fixture(t, [step('PLANNER_RESPONSE', { created_at, content: 'Previous reply' })], idle)
  assert.equal(node.last('runtime.sessions').sessions[0].status, 'done')
  await node.peer.call('session.send', { session_id: SESSION, text: 'Continue', client_msg_id: 'owned-turn' })
  await until('owned ACP turn running', () => node.last('runtime.sessions')?.sessions[0]?.status === 'working')
})

test('native running state keeps narration working until the conversation becomes idle', { timeout: 15_000 }, async (t) => {
  const created_at = new Date().toISOString()
  const narration = step('PLANNER_RESPONSE', { created_at, content: 'Running the experiment; results will follow.' })
  const { node, write, state, refresh, summaryFile } = await fixture(t, [narration], { status: 'CASCADE_RUN_STATUS_RUNNING' })
  const original = readFileSync(summaryFile)
  assert.equal(node.last('runtime.sessions').sessions[0].status, 'working')
  assert.equal(await refresh(), 'working', 'the narration alone does not complete the turn')
  assert.deepEqual(readFileSync(summaryFile), original, 'inventory leaves the native database unchanged')
  write([narration, step('GENERIC', { created_at, content: 'Tool output' })])
  assert.equal(await refresh(), 'working')
  write([narration, step('GENERIC', { created_at }), step('PLANNER_RESPONSE', { created_at, content: 'Finished' })])
  state({ ...idle, step_count: 3 })
  assert.equal(await refresh(), 'done')
  write([narration, step('GENERIC', { created_at }), step('PLANNER_RESPONSE', { created_at, content: 'Finished' }), step('USER_INPUT', { created_at })])
  assert.equal(await refresh(), 'unknown', 'a stale idle summary does not establish a new execution')
})

test('old completed user input with no running state stays unknown', { timeout: 15_000 }, async (t) => {
  const created_at = new Date(Date.now() - 8 * 3600_000).toISOString()
  const { node, state, refresh } = await fixture(t, [step('USER_INPUT', { created_at })], { status: '' })
  assert.equal(node.last('runtime.sessions').sessions[0].status, 'unknown')
  assert.equal(await refresh(), 'unknown')
  state({ status: 'CASCADE_RUN_STATUS_RUNNING' })
  assert.equal(await refresh(), 'working')
  state({ status: '' })
  assert.equal(await refresh(), 'unknown')
})

test('native running conversations are discovered before transcript export', { timeout: 15_000 }, async (t) => {
  const { node, state, refresh } = await fixture(t, [], {
    status: 'CASCADE_RUN_STATUS_RUNNING', title: 'External experiment', workspace_uris: '["file:///work/example"]',
  }, { noTranscript: true })
  const row = node.last('runtime.sessions').sessions[0]
  assert.equal(row.session_id, SESSION)
  assert.equal(row.status, 'working')
  assert.equal(row.title, 'External experiment')
  assert.equal(row.cwd, '/work/example')
  state(idle)
  assert.equal(await refresh(), undefined, 'an inactive conversation without a transcript leaves the live inventory')
})

for (const internal of [false, true]) {
  test(`native running state survives history limits with internal metadata ${internal}`, { timeout: 15_000 }, async (t) => {
    const old = new Date(Date.now() - 30 * 86400_000).toISOString()
    const { node } = await fixture(t, [step('GENERIC', { created_at: old })], {
      status: 'CASCADE_RUN_STATUS_RUNNING', last_modified_time: old, workspace_uris: 'invalid JSON',
    }, { oldTranscript: true, internal })
    assert.equal(node.last('runtime.sessions').sessions[0].status, 'working')
  })
}

test('unavailable native summary stores cannot turn narration into completion', { timeout: 15_000 }, async (t) => {
  const created_at = new Date().toISOString()
  const { node, state, refresh, summaryFile, database } = await fixture(t, [step('PLANNER_RESPONSE', { created_at, content: 'Continuing the experiment' })])
  assert.equal(node.last('runtime.sessions').sessions[0].status, 'unknown', 'missing database')
  writeFileSync(summaryFile, 'invalid database')
  assert.equal(await refresh(), 'unknown', 'unreadable database')
  rmSync(summaryFile)
  state(idle)
  const db = database()
  db.exec('BEGIN EXCLUSIVE')
  try {
    assert.equal(await refresh(), 'unknown', 'locked database')
  } finally {
    db.exec('ROLLBACK')
  }
  db.exec('DROP TABLE conversation_summaries; CREATE TABLE conversation_summaries (conversation_id TEXT PRIMARY KEY, status TEXT)')
  assert.equal(await refresh(), 'unknown', 'unsupported older schema')
})

const unknownReports = (logs) => logs.filter((line) => line.startsWith('agy unknown status:'))

test('unknown transcript states name the missing evidence', () => {
  const reply = step('PLANNER_RESPONSE', { content: 'Running the experiment' })
  const cases = [
    ['empty history', [], idle, 'no_relevant_step'],
    ['metadata alone', [step('CHECKPOINT'), step('SYSTEM_MESSAGE')], idle, 'no_relevant_step'],
    ['reply without native state', [reply], undefined, 'no_summary'],
    ['unrecognized native state', [reply], { status: 'FUTURE_STATUS' }, 'summary_not_idle'],
    ['empty native state', [reply], { status: '' }, 'summary_not_idle'],
    ['reply without a step index', [step('PLANNER_RESPONSE', { step_index: undefined, content: 'Finished' })], idle, 'no_step_index'],
    ['idle summary predates narration', [step('PLANNER_RESPONSE', { step_index: 1, content: 'Finished' })], idle, 'summary_behind'],
    ['idle summary without a step count', [reply], { status: 'CASCADE_RUN_STATUS_IDLE' }, 'summary_behind'],
  ]
  for (const [name, steps, summary, reason] of cases) {
    assert.deepEqual(transcriptState(steps, summary), { status: 'unknown', reason }, name)
    assert.equal(transcriptStatus(steps, summary), 'unknown', name)
  }
  assert.deepEqual(transcriptState([step('PLANNER_RESPONSE', { content: 'Finished' })], idle), { status: 'done' })
  assert.deepEqual(transcriptState([step('ERROR_MESSAGE')]), { status: 'idle' }, 'an error needs no summary')
})

const unknownCases = [
  ['no_summary', [step('PLANNER_RESPONSE', { content: 'Private narration' })], undefined],
  ['summary_not_idle', [step('PLANNER_RESPONSE', { content: 'Private narration' })], { status: '' }],
  ['summary_behind', [step('USER_INPUT', { content: 'Private question' }), step('PLANNER_RESPONSE', { content: 'Private narration' })], { ...idle, step_count: 1 }],
  ['no_step_index', [step('PLANNER_RESPONSE', { step_index: undefined, content: 'Private narration' })], idle, { noIndex: true }],
  ['no_relevant_step', [step('CHECKPOINT', { content: 'Private summary' })], idle],
]
for (const [reason, steps, summary, options] of unknownCases) {
  test(`runtime reports ${reason} as the reason a session is unknown`, { timeout: 15_000 }, async (t) => {
    const created_at = new Date().toISOString()
    const { node, logs } = await fixture(t, steps.map((value) => ({ ...value, created_at })), summary, options)
    assert.equal(node.last('runtime.sessions').sessions[0].status, 'unknown')
    assert.deepEqual(unknownReports(logs), [`agy unknown status: ${reason} 1`])
    assert.ok(!logs.some((line) => /Private|[0-9a-f]{8}-[0-9a-f]{4}/.test(line)), 'reasons and counts only')
  })
}

test('a failed summary read is reported apart from missing summaries', { timeout: 15_000 }, async (t) => {
  const created_at = new Date().toISOString()
  const { node, state, refresh, summaryFile, database, logs } = await fixture(t, [step('PLANNER_RESPONSE', { created_at, content: 'Private reply' })])
  assert.equal(node.last('runtime.sessions').sessions[0].status, 'unknown')
  assert.deepEqual(unknownReports(logs), ['agy unknown status: no_summary 1'], 'no database is missing evidence')
  assert.ok(!logs.some((line) => line.includes('unreadable')), 'a missing database is not a failed read')

  writeFileSync(summaryFile, 'invalid database')
  assert.equal(await refresh(), 'unknown', 'the status itself stays unknown')
  assert.ok(logs.includes('agy native summaries unreadable: file is not a database'))
  assert.deepEqual(unknownReports(logs).at(-1), 'agy unknown status: read_failed 1')

  rmSync(summaryFile)
  state(idle)
  assert.equal(await refresh(), 'done')
  assert.ok(logs.some((line) => /^agy native summary reads recovered after \d+ failure\(s\)$/.test(line)))
  assert.deepEqual(unknownReports(logs).at(-1), 'agy unknown status: none')

  const db = database()
  db.exec('BEGIN EXCLUSIVE')
  try {
    assert.equal(await refresh(), 'unknown', 'locked database')
  } finally {
    db.exec('ROLLBACK')
  }
  assert.ok(logs.includes('agy native summaries unreadable: database is locked'))
  assert.deepEqual(unknownReports(logs).at(-1), 'agy unknown status: read_failed 1')
  const reports = unknownReports(logs).length
  assert.equal(await refresh(), 'done')
  await refresh()
  assert.deepEqual(unknownReports(logs).slice(reports), ['agy unknown status: none'], 'an unchanged tally is not logged again')

  db.exec('DROP TABLE conversation_summaries; CREATE TABLE conversation_summaries (conversation_id TEXT PRIMARY KEY, status TEXT)')
  assert.equal(await refresh(), 'unknown', 'unsupported older schema')
  assert.ok(logs.includes('agy native summaries unreadable: SQL logic error'), 'the result code, not the statement detail')
  assert.ok(!logs.some((line) => /Private|[0-9a-f]{8}-[0-9a-f]{4}/.test(line)), 'reasons and counts only')
})

test('completed availability workspaces stay hidden after a companion restart', async (t) => {
  const missing = join(tmpdir(), 'persona-check-availability-missing')
  const { node } = await fixture(t, [step('PLANNER_RESPONSE', { content: 'OK' })], {
    ...idle, workspace_uris: JSON.stringify([`file://${missing}`]),
  }, { expectEmpty: true })
  assert.equal(node.last('runtime.sessions').sessions.length, 0)
})

test('a real workspace sharing the check prefix remains visible', async (t) => {
  const real = mkdtempSync(join(tmpdir(), 'persona-check-'))
  t.after(() => rmSync(real, { recursive: true, force: true }))
  const { node } = await fixture(t, [step('PLANNER_RESPONSE', { content: 'Answer' })], {
    ...idle, workspace_uris: JSON.stringify([`file://${real}`]),
  })
  assert.equal(node.last('runtime.sessions').sessions.length, 1)
})

test('lagging summaries never truncate freshly exported steps by timestamp alone', async (t) => {
  const steps = [step('USER_INPUT', { source: 'USER_EXPLICIT', content: 'Question' }), step('PLANNER_RESPONSE', { content: 'Answer' })]
  const { node, state, refresh } = await fixture(t, steps, { ...idle, last_modified_time: '2026-01-03T00:00:00Z' })
  assert.equal(node.last('runtime.sessions').sessions[0].last_text, 'Answer')
  const history = node.last('runtime.sessions').sessions[0].rev
  state({ ...idle, step_count: 2, last_modified_time: '2026-01-03T00:00:01Z' })
  await refresh()
  assert.equal(node.last('runtime.sessions').sessions[0].rev, history)
})

test('canceling and busy native states count as working while unspecified and unknown states stay fail-closed', () => {
  const reply = step('PLANNER_RESPONSE', { content: 'Running the experiment' })
  for (const status of ['CASCADE_RUN_STATUS_RUNNING', 'CASCADE_RUN_STATUS_CANCELING', 'CASCADE_RUN_STATUS_BUSY']) {
    assert.equal(nativeWorking({ status }), true, `${status} is nativeWorking`)
    assert.deepEqual(transcriptState([reply], { status }), { status: 'working' }, `${status} maps to working`)
    assert.equal(nativeWorking({ status, killed: 1 }), false, `killed ${status} is not working`)
    assert.deepEqual(transcriptState([reply], { status, killed: 1 }), { status: 'idle' }, `killed ${status} maps to idle`)
  }
  assert.equal(nativeWorking({ status: 'CASCADE_RUN_STATUS_UNSPECIFIED' }), false)
  assert.deepEqual(
    transcriptState([reply], { status: 'CASCADE_RUN_STATUS_UNSPECIFIED', step_count: 1 }),
    { status: 'unknown', reason: 'summary_not_idle' },
  )
})

test('empty conversations with an idle zero-step summary report idle without relaxing non-empty or non-zero controls', () => {
  assert.deepEqual(transcriptState([], { status: 'CASCADE_RUN_STATUS_IDLE', step_count: 0 }), { status: 'idle' })
  assert.deepEqual(transcriptState([], { status: 'CASCADE_RUN_STATUS_IDLE', step_count: 1 }), { status: 'unknown', reason: 'no_relevant_step' })
  assert.deepEqual(transcriptState([], undefined), { status: 'unknown', reason: 'no_relevant_step' })
  assert.deepEqual(transcriptState([], { status: '', step_count: 0 }), { status: 'unknown', reason: 'no_relevant_step' })
  assert.deepEqual(transcriptState([], { status: 'CASCADE_RUN_STATUS_UNSPECIFIED', step_count: 0 }), { status: 'unknown', reason: 'no_relevant_step' })
  assert.deepEqual(transcriptState([step('CHECKPOINT')], { status: 'CASCADE_RUN_STATUS_IDLE', step_count: 0 }), { status: 'unknown', reason: 'no_relevant_step' })
  assert.deepEqual(transcriptState([step('CHECKPOINT')], { status: 'CASCADE_RUN_STATUS_IDLE', step_count: 1 }), { status: 'unknown', reason: 'no_relevant_step' })
})

test('runtime reports canceling and busy native summaries as working and logs sanitized unrecognized status tokens once', { timeout: 15_000 }, async (t) => {
  const created_at = new Date().toISOString()
  const { node, state, refresh, logs } = await fixture(t, [step('PLANNER_RESPONSE', { created_at, content: 'Private narration' })], {
    status: 'CASCADE_RUN_STATUS_CANCELING', step_count: 1,
  })
  assert.equal(node.last('runtime.sessions').sessions[0].status, 'working')
  assert.deepEqual(unknownReports(logs), [])

  state({ status: 'CASCADE_RUN_STATUS_BUSY', step_count: 1 })
  assert.equal(await refresh(), 'working')
  assert.deepEqual(unknownReports(logs), [])

  state({ status: 'CASCADE_RUN_STATUS_UNSPECIFIED', step_count: 1 })
  assert.equal(await refresh(), 'unknown')
  assert.equal(await refresh(), 'unknown')
  assert.deepEqual(unknownReports(logs), ['agy unknown status: summary_not_idle 1'])
  assert.deepEqual(
    logs.filter((line) => line.startsWith('agy unrecognized summary status:')),
    ['agy unrecognized summary status: CASCADE_RUN_STATUS_UNSPECIFIED'],
    'enum token logged once without duplicates',
  )

  state({ status: 'Private 12345678-1234-4123-8123-1234567890ab', step_count: 1 })
  assert.equal(await refresh(), 'unknown')
  assert.ok(logs.includes('agy unrecognized summary status: <other>'))
  assert.ok(!logs.some((line) => /Private|12345678-1234/.test(line)), 'never logs non-enum status text or IDs')
})

test('freshly started Persona session stays idle when agy creates an empty transcript file before writing a summary row', { timeout: 15_000 }, async (t) => {
  const home = mkdtempSync(join(tmpdir(), 'persona-agy-empty-start-'))
  const agyDir = join(home, 'agy')
  const freshId = '22222222-3333-4444-8555-666666666666'
  mkdirSync(agyDir, { recursive: true })
  const binary = join(home, 'fake-agy.mjs')
  writeFileSync(binary, `#!/usr/bin/env node
if (process.argv.includes('models')) { console.log('test-model\\tTest Model'); process.exit(0) }
import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { createInterface } from 'node:readline'
createInterface({ input: process.stdin }).on('line', (line) => {
  const req = JSON.parse(line)
  if (req.id === undefined) return
  if (req.method === 'session/new') {
    const logs = join(${JSON.stringify(agyDir)}, 'brain', '${freshId}', '.system_generated', 'logs')
    mkdirSync(logs, { recursive: true })
    writeFileSync(join(logs, 'transcript.jsonl'), '')
    process.stdout.write(JSON.stringify({ jsonrpc: '2.0', id: req.id, result: { sessionId: '${freshId}' } }) + '\\n')
    return
  }
  process.stdout.write(JSON.stringify({ jsonrpc: '2.0', id: req.id, result: {} }) + '\\n')
})
`, { mode: 0o700 })
  const node = await startFakeNode(t)
  const logs = []
  const runtime = createAgyRuntime({ home, agyDir, agyBin: binary, socketPath: node.path, log: (line) => logs.push(line) })
  t.after(async () => {
    await runtime.stop()
    rmSync(home, { recursive: true, force: true })
  })
  runtime.start()
  await node.ready
  const started = await node.peer.call('session.start', { cwd: home })
  assert.equal(started.session.session_id, `agy-${freshId}`)
  await until('fresh session listed as idle', () => {
    const row = node.last('runtime.sessions')?.sessions.find((s) => s.session_id === `agy-${freshId}`)
    return row?.status === 'idle'
  })
  assert.deepEqual(unknownReports(logs), [])
})
