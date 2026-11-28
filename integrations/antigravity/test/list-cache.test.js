import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import fs, { appendFileSync, mkdirSync, mkdtempSync, rmSync, utimesSync, writeFileSync } from 'node:fs'
import { syncBuiltinESMExports } from 'node:module'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { test } from 'node:test'
import { setTimeout as delay } from 'node:timers/promises'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'

const IDS = [
  '11111111-1111-4111-8111-111111111111',
  '22222222-2222-4222-8222-222222222222',
  '33333333-3333-4333-8333-333333333333',
]

function line(step) {
  return JSON.stringify(step) + '\n'
}

test('settled list summaries survive transcript-cache eviction without hiding changes', { timeout: 30_000 }, async (t) => {
  const home = mkdtempSync(join(tmpdir(), 'agy-list-cache-'))
  const agyDir = join(home, 'agy')
  const brain = join(agyDir, 'brain')
  mkdirSync(brain, { recursive: true })
  const content = 'x'.repeat(33 * 1024 * 1024)
  for (const [index, id] of IDS.entries()) {
    const logs = join(brain, id, '.system_generated', 'logs')
    mkdirSync(logs, { recursive: true })
    const file = join(logs, 'transcript_full.jsonl')
    writeFileSync(file, line({
      step_index: 0,
      type: 'PLANNER_RESPONSE',
      status: 'DONE',
      created_at: '2026-11-25T00:00:00.000Z',
      content: index < 2 ? `session-${index}-` + content : 'unused-full',
    }) + (index === 0 ? line(null) : ''))
    const old = new Date(Date.now() - 60_000)
    utimesSync(file, old, old)
    if (index === 0) {
      const compact = join(logs, 'transcript.jsonl')
      writeFileSync(compact, line({
        step_index: 0,
        type: 'PLANNER_RESPONSE',
        status: 'DONE',
        created_at: '2026-11-25T00:00:00.000Z',
        content: 'compact-race-base',
      }) + line({
        step_index: 1,
        type: 'PLANNER_RESPONSE',
        status: 'DONE',
        created_at: '2026-11-25T00:00:01.000Z',
        content: 'compact-race',
      }))
      utimesSync(compact, old, old)
    }
    if (index === 2) {
      const compact = join(logs, 'transcript.jsonl')
      writeFileSync(compact, line({
        step_index: 0,
        type: 'PLANNER_RESPONSE',
        status: 'DONE',
        created_at: '2026-11-25T00:00:00.000Z',
        content: 'compact-before',
      }))
      utimesSync(compact, old, old)
    }
  }
  const summaryFile = join(agyDir, 'conversation_summaries.db')
  const setStepCounts = (counts) => {
    const db = new DatabaseSync(summaryFile)
    db.exec(`CREATE TABLE IF NOT EXISTS conversation_summaries (
      conversation_id TEXT PRIMARY KEY,
      status TEXT,
      not_fully_idle NUMERIC,
      killed NUMERIC,
      step_count INTEGER,
      title TEXT,
      workspace_uris TEXT,
      last_modified_time TEXT
    )`)
    for (const [index, id] of IDS.entries()) {
      db.prepare('INSERT OR REPLACE INTO conversation_summaries VALUES (?, ?, 0, 0, ?, ?, ?, ?)')
        .run(id, 'CASCADE_RUN_STATUS_IDLE', counts[index], `Session ${index}`, '[]', '')
    }
    db.close()
  }
  setStepCounts([1, 1, 1])
  const binary = join(home, 'fake-agy.mjs')
  writeFileSync(binary, `#!/usr/bin/env node
if (process.argv.includes('models')) { console.log('test-model\\tTest Model'); process.exit(0) }
import { createServer } from 'node:http'
import { writeFileSync } from 'node:fs'
import { createInterface } from 'node:readline'
const service = createServer(async (request, response) => {
  const chunks = []
  for await (const chunk of request) chunks.push(chunk)
  const params = chunks.length ? JSON.parse(Buffer.concat(chunks).toString()) : {}
  if (!request.url.endsWith('GetCascadeTrajectorySteps')) return response.end('{}')
  response.end(JSON.stringify({ steps: [{
    step_index: params.stepOffset,
    type: 'PLANNER_RESPONSE',
    status: 'DONE',
    content: 'native-completed',
  }] }))
})
await new Promise((resolve) => service.listen(0, '127.0.0.1', resolve))
const logFile = process.argv[process.argv.indexOf('--log-file') + 1]
writeFileSync(logFile, \`Language server listening on random port at \${service.address().port} for HTTP\\n\`)
createInterface({ input: process.stdin }).on('line', (line) => {
  const request = JSON.parse(line)
  if (request.id === undefined || request.method === 'session/prompt') return
  process.stdout.write(JSON.stringify({ jsonrpc: '2.0', id: request.id, result: {} }) + '\\n')
})
`, { mode: 0o700 })

  let transcriptReads = 0
  let transcriptBytes = 0
  let raceFile = ''
  let raceAfterStat = false
  let raceVersion = false
  const originalReadFileSync = fs.readFileSync
  const originalExistsSync = fs.existsSync
  const originalStatSync = fs.statSync
  fs.readFileSync = function (path, ...args) {
    if (String(path) === join(brain, IDS[2], '.system_generated', 'logs', 'transcript_full.jsonl')) {
      throw new Error('injected full transcript read failure')
    }
    const result = originalReadFileSync.call(this, path, ...args)
    if (String(path).endsWith('/transcript_full.jsonl')) {
      transcriptReads++
      transcriptBytes += result?.length || 0
    }
    return result
  }
  fs.existsSync = function (path, ...args) {
    if (raceVersion && String(path) === raceFile) {
      raceVersion = false
      return false
    }
    return originalExistsSync.call(this, path, ...args)
  }
  fs.statSync = function (path, ...args) {
    const result = originalStatSync.call(this, path, ...args)
    if (raceAfterStat && String(path) === raceFile) {
      raceAfterStat = false
      raceVersion = true
    }
    return result
  }
  syncBuiltinESMExports()
  t.after(() => {
    fs.readFileSync = originalReadFileSync
    fs.existsSync = originalExistsSync
    fs.statSync = originalStatSync
    syncBuiltinESMExports()
  })
  const { createAgyRuntime } = await import('../lib/runtime.js')
  const node = await startFakeNode(t)
  const runtime = createAgyRuntime({ home, agyDir, agyBin: binary, socketPath: node.path, listDays: 14, maxSessions: 3, log: () => {} })
  t.after(async () => {
    await runtime.stop()
    rmSync(home, { recursive: true, force: true })
  })
  // ctime, like mtime, must be outside the same-size rewrite detection window.
  await delay(1100)
  runtime.start()
  await node.ready
  await until('all transcript sources listed', () => {
    const rows = node.last('runtime.sessions')?.sessions
    return rows?.length === 3 && rows.every((row) => row.status === 'done' && row.last_text)
  }, 15_000)

  const refresh = async (title, predicate, timeout = 15_000) => {
    const before = node.all('runtime.sessions').length
    await node.peer.call('session.rename', { session_id: `agy-${IDS[0]}`, title })
    await until('inventory refreshed', () => node.all('runtime.sessions').length > before, 15_000)
    if (predicate) await until('requested row state', predicate, timeout)
  }

  transcriptReads = 0
  transcriptBytes = 0
  await refresh('unchanged')
  assert.equal(transcriptBytes, 0, 'an unchanged settled list must not re-read files evicted from the 64 MiB transcript cache')
  assert.equal(transcriptReads, 0)

  setStepCounts([1, 2, 1])
  await refresh('native-tail-before-probe', () => {
    const row = node.last('runtime.sessions')?.sessions.find((row) => row.session_id === `agy-${IDS[1]}`)
    return row?.status === 'done'
  })
  await node.peer.call('session.items', { session_id: `agy-${IDS[1]}` })
  await refresh('negative-native-tail-primed')
  const { readTranscript: evictOtherTranscript } = await import('../lib/map.js')
  evictOtherTranscript(join(brain, IDS[0]), true)
  transcriptReads = 0
  transcriptBytes = 0
  await refresh('negative-native-tail-unchanged')
  assert.equal(transcriptBytes, 0, 'a completed native tail probe with no error must not disable settled list caching')
  assert.equal(transcriptReads, 0)

  appendFileSync(join(brain, IDS[2], '.system_generated', 'logs', 'transcript.jsonl'), line({
    step_index: 1,
    type: 'PLANNER_RESPONSE',
    status: 'DONE',
    created_at: new Date().toISOString(),
    content: 'compact-after',
  }))
  await refresh('compact-fallback-changed', () => {
    const row = node.last('runtime.sessions')?.sessions.find((row) => row.session_id === `agy-${IDS[2]}`)
    return row?.rev === 2 && row?.last_text === 'compact-after' && row?.status === 'unknown'
  })

  setStepCounts([0, 1, 1])
  await refresh('native-state-changed', () => {
    const row = node.last('runtime.sessions')?.sessions.find((row) => row.session_id === `agy-${IDS[0]}`)
    return row?.status === 'unknown'
  })
  setStepCounts([1, 1, 1])
  await refresh('native-state-restored', () => {
    const row = node.last('runtime.sessions')?.sessions.find((row) => row.session_id === `agy-${IDS[0]}`)
    return row?.status === 'done'
  })

  appendFileSync(join(brain, IDS[0], '.system_generated', 'logs', 'transcript_full.jsonl'), line({
    step_index: 1,
    type: 'PLANNER_RESPONSE',
    status: 'DONE',
    created_at: '2026-11-25T00:00:01.000Z',
    content: 'after-append',
  }))
  await refresh('appended', () => {
    const row = node.last('runtime.sessions')?.sessions.find((row) => row.session_id === `agy-${IDS[0]}`)
    return row?.rev === 2 && row?.last_text === 'after-append' && row?.status === 'unknown'
  }, 5_000)
  // Keep native step_count unchanged so only the transcript version can invalidate this snapshot.

  const { readTranscript, rememberObservedWake, transcriptVersion } = await import('../lib/map.js')
  const brainDir = join(brain, IDS[0])
  const fullFile = join(brainDir, '.system_generated', 'logs', 'transcript_full.jsonl')
  await delay(1100)
  setStepCounts([2, 1, 1])
  await refresh('settled-for-revision', () => {
    const row = node.last('runtime.sessions')?.sessions.find((row) => row.session_id === `agy-${IDS[0]}`)
    return row?.status === 'done' && row?.rev === 2
  })
  const beforeRewrite = readTranscript(brainDir, true).revision
  for (let index = 0; index < 260; index++) {
    const other = join(home, `revision-${index}`)
    const logs = join(other, '.system_generated', 'logs')
    mkdirSync(logs, { recursive: true })
    writeFileSync(join(logs, 'transcript_full.jsonl'), line({
      step_index: 0,
      type: 'PLANNER_RESPONSE',
      status: 'DONE',
      created_at: '2026-11-25T00:00:00.000Z',
      content: `other-${index}`,
    }))
    readTranscript(other, true)
  }
  writeFileSync(fullFile, line({
    step_index: 0,
    type: 'PLANNER_RESPONSE',
    status: 'DONE',
    created_at: '2026-11-25T00:00:00.000Z',
    content: `session-0-` + content,
  }) + line(null) + line({
    step_index: 1,
    type: 'PLANNER_RESPONSE',
    status: 'DONE',
    created_at: '2026-11-25T00:00:01.000Z',
    content: 'after-APPEND',
  }))
  await refresh('same-step-rewrite', () => {
    const row = node.last('runtime.sessions')?.sessions.find((row) => row.session_id === `agy-${IDS[0]}`)
    return row?.status === 'done' && row?.rev === 2 && row?.last_text === 'after-APPEND'
  })
  assert.notEqual(readTranscript(brainDir, true).revision, beforeRewrite,
    'a listed cache hit must preserve the revision record needed to detect a same-step rewrite')

  raceFile = fullFile
  raceAfterStat = true
  await refresh('full-export-selected-during-read', () => {
    const row = node.last('runtime.sessions')?.sessions.find((row) => row.session_id === `agy-${IDS[0]}`)
    return row?.status === 'done' && row?.last_text === 'after-APPEND'
  })
  assert.equal(raceAfterStat, false, 'the fixture must reach the candidate stat before the version check')
  assert.equal(raceVersion, false, 'the fixture must make version selection fall back before the actual full read')
  rmSync(fullFile)
  await refresh('full-export-removed', () => {
    const row = node.last('runtime.sessions')?.sessions.find((row) => row.session_id === `agy-${IDS[0]}`)
    return row?.status === 'done' && row?.rev === 2 && row?.last_text === 'compact-race'
  })

  const beforeWake = transcriptVersion(brainDir, true)
  const transcript = readTranscript(brainDir, true)
  rememberObservedWake(brainDir, transcript.steps, 1)
  assert.notEqual(transcriptVersion(brainDir, true)?.key, beforeWake?.key, 'wake receipts must invalidate a listed transcript')
})

test('list snapshot capacity covers every selected session', { timeout: 30_000 }, async (t) => {
  const home = mkdtempSync(join(tmpdir(), 'agy-list-capacity-'))
  const agyDir = join(home, 'agy')
  const brain = join(agyDir, 'brain')
  const count = 257
  const ids = Array.from({ length: count }, (_, index) =>
    `00000000-0000-4000-8000-${String(index).padStart(12, '0')}`)
  mkdirSync(brain, { recursive: true })
  const db = new DatabaseSync(join(agyDir, 'conversation_summaries.db'))
  db.exec(`CREATE TABLE conversation_summaries (
    conversation_id TEXT PRIMARY KEY,
    status TEXT,
    not_fully_idle NUMERIC,
    killed NUMERIC,
    step_count INTEGER,
    title TEXT,
    workspace_uris TEXT,
    last_modified_time TEXT
  )`)
  const insert = db.prepare('INSERT INTO conversation_summaries VALUES (?, ?, 0, 0, 1, ?, \'[]\', \'\')')
  const old = new Date(Date.now() - 60_000)
  for (const [index, id] of ids.entries()) {
    const logs = join(brain, id, '.system_generated', 'logs')
    mkdirSync(logs, { recursive: true })
    const file = join(logs, 'transcript_full.jsonl')
    writeFileSync(file, line({
      step_index: 0,
      type: 'PLANNER_RESPONSE',
      status: 'DONE',
      created_at: '2026-11-25T00:00:00.000Z',
      content: `session-${index}`,
    }))
    utimesSync(file, old, old)
    insert.run(id, 'CASCADE_RUN_STATUS_IDLE', `Session ${index}`)
  }
  db.close()
  const binary = join(home, 'fake-agy.mjs')
  writeFileSync(binary, `#!/usr/bin/env node
if (process.argv.includes('models')) { console.log('test-model\\tTest Model'); process.exit(0) }
import { createInterface } from 'node:readline'
createInterface({ input: process.stdin }).on('line', (line) => {
  const request = JSON.parse(line)
  if (request.id !== undefined) process.stdout.write(JSON.stringify({ jsonrpc: '2.0', id: request.id, result: {} }) + '\\n')
})
`, { mode: 0o700 })

  let transcriptReads = 0
  const originalReadFileSync = fs.readFileSync
  fs.readFileSync = function (path, ...args) {
    const result = originalReadFileSync.call(this, path, ...args)
    if (String(path).endsWith('/transcript_full.jsonl')) transcriptReads++
    return result
  }
  syncBuiltinESMExports()
  t.after(() => {
    fs.readFileSync = originalReadFileSync
    syncBuiltinESMExports()
  })
  const { createAgyRuntime } = await import('../lib/runtime.js')
  const node = await startFakeNode(t)
  const runtime = createAgyRuntime({ home, agyDir, agyBin: binary, socketPath: node.path, listDays: 14, maxSessions: 300, log: () => {} })
  t.after(async () => {
    await runtime.stop()
    rmSync(home, { recursive: true, force: true })
  })
  await delay(1100)
  runtime.start()
  await node.ready
  await until('all selected sessions listed', () => {
    const rows = node.last('runtime.sessions')?.sessions
    return rows?.length === count && rows.every((row) => row.status === 'done')
  }, 15_000)

  transcriptReads = 0
  const before = node.all('runtime.sessions').length
  await node.peer.call('session.rename', { session_id: `agy-${ids[0]}`, title: 'unchanged-large-list' })
  await until('large inventory refreshed', () => node.all('runtime.sessions').length > before, 15_000)
  assert.equal(transcriptReads, 0, 'a selected list larger than 256 entries must not thrash its snapshot LRU')

  await node.peer.call('session.watch', { session_id: `agy-${ids[0]}` })
  // The watcher can read before the next list restores every retained revision baseline.
  const watchedItems = node.all('session.items').length
  writeFileSync(join(brain, ids[0], '.system_generated', 'logs', 'transcript_full.jsonl'), line({
    step_index: 0,
    type: 'PLANNER_RESPONSE',
    status: 'DONE',
    created_at: '2026-11-25T00:00:00.000Z',
    content: 'Session-0',
  }))
  await until('same-step rewrite delivered after revision pressure', () =>
    node.all('session.items').slice(watchedItems).some((event) =>
      event.items?.some((item) => item.text === 'Session-0')
    ), 5_000)
})

// P1-1 (#16001): a lagging summary_behind row must be list-cacheable once its state is provably
// static, and the .persona-turn-completion stat has to be part of the cache key.
const BEHIND_RAW = '77777777-4444-4444-8444-777777777777'
const BEHIND_SESSION = `agy-${BEHIND_RAW}`
const behindRecords = [
  { step_index: 0, type: 'USER_INPUT', source: 'USER_EXPLICIT', content: 'First question' },
  { step_index: 1, type: 'PLANNER_RESPONSE', content: 'First answer' },
  { step_index: 2, type: 'USER_INPUT', source: 'USER_EXPLICIT', content: 'Second question' },
  { step_index: 3, type: 'PLANNER_RESPONSE', content: 'Second answer' },
].map((step) => ({ ...step, status: 'DONE', created_at: '2026-01-01T00:00:00.000Z' }))

const BEHIND_FAKE_AGY = `
import { readFileSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:http'
import { createInterface } from 'node:readline'
if (process.argv.includes('models')) { console.log('test-model\\tTest Model'); process.exit(0) }
const dir = process.argv[2]
const logFile = process.argv[process.argv.indexOf('--log-file') + 1]
const send = (message) => process.stdout.write(JSON.stringify({ jsonrpc: '2.0', ...message }) + '\\n')
const server = createServer(async (request, response) => {
  const chunks = []
  for await (const chunk of request) chunks.push(chunk)
  const method = request.url.split('/').at(-1)
  if (method === 'GetCascadeTrajectory') {
    response.end(JSON.stringify(JSON.parse(readFileSync(dir + '/trajectory.json', 'utf8'))))
    return
  }
  response.writeHead(404).end('{}')
})
await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve))
writeFileSync(logFile, 'Language server listening on random port at ' + server.address().port + ' for HTTP\\n')
createInterface({ input: process.stdin }).on('line', (line) => {
  const request = JSON.parse(line)
  if (request.id !== undefined) send({ id: request.id, result: {} })
})
`

async function setupBehindListCache(t) {
  const home = mkdtempSync(join(tmpdir(), 'agy-p11-list-cache-'))
  const agyDir = join(home, 'config')
  const logs = join(agyDir, 'brain', BEHIND_RAW, '.system_generated', 'logs')
  mkdirSync(logs, { recursive: true })
  const fullFile = join(logs, 'transcript_full.jsonl')
  writeFileSync(fullFile, behindRecords.map((step) => line(step)).join(''))
  const old = new Date(Date.now() - 60_000)
  utimesSync(fullFile, old, old)

  const db = new DatabaseSync(join(agyDir, 'conversation_summaries.db'))
  db.exec('CREATE TABLE conversation_summaries (conversation_id TEXT PRIMARY KEY, status TEXT, not_fully_idle INTEGER, killed INTEGER, step_count INTEGER, title TEXT, workspace_uris TEXT, last_modified_time TEXT)')
  db.prepare('INSERT INTO conversation_summaries VALUES (?, ?, 0, 0, ?, ?, ?, ?)')
    .run(BEHIND_RAW, 'CASCADE_RUN_STATUS_IDLE', 2, 'P11', '[]', '2026-01-01T00:00:10Z')
  writeFileSync(join(home, 'trajectory.json'), JSON.stringify({ trajectory: { cascadeId: BEHIND_RAW, steps: [] } }))
  const agyBinScript = join(home, 'fake-agy.mjs')
  writeFileSync(agyBinScript, BEHIND_FAKE_AGY)

  const counts = { fullReadFile: 0 }
  const originalReadFileSync = fs.readFileSync
  fs.readFileSync = function (path, ...args) {
    if (String(path) === fullFile) counts.fullReadFile += 1
    return originalReadFileSync.call(this, path, ...args)
  }
  syncBuiltinESMExports()
  t.after(() => {
    fs.readFileSync = originalReadFileSync
    syncBuiltinESMExports()
  })

  const mapLib = await import('../lib/map.js')
  const { createAgyRuntime } = await import('../lib/runtime.js')
  const node = await startFakeNode(t)
  const runtime = createAgyRuntime({
    home, agyDir, agyBin: process.execPath, agyArgs: [agyBinScript, home],
    socketPath: node.path, listDays: 14, maxSessions: 10, log: () => {},
  })
  t.after(async () => {
    await runtime.stop()
    try { db.close() } catch {}
    rmSync(home, { recursive: true, force: true })
  })
  await delay(1100) // transcript must be older than the 1s settled window
  runtime.start()
  await node.ready
  await until('behind fixture listed', () =>
    node.last('runtime.sessions')?.sessions.some((row) => row.session_id === BEHIND_SESSION), 15_000)

  // session.items awaits the authoritative GetCascadeTrajectory negative cache (retryAt === 0).
  const items = await node.peer.call('session.items', { session_id: BEHIND_SESSION })
  const history = String(items.history).replace(/:projected:[0-9a-f]{8}$/, '')
  const markerFile = join(agyDir, 'brain', BEHIND_RAW, '.persona-turn-completion')
  const writeMarker = (suffix = '') => {
    const full = createHash('sha256').update(`${history}|3|Second answer`).digest('hex')
    writeFileSync(markerFile, full + suffix)
  }
  const setStepCount = (stepCount) =>
    db.prepare('UPDATE conversation_summaries SET step_count = ? WHERE conversation_id = ?').run(stepCount, BEHIND_RAW)
  // The listed summary cache is separate from map's 128-entry transcript cache; evict the latter so a
  // zero read can only come from listedTranscriptCache.
  const evictTranscriptCache = () => {
    for (let index = 0; index < 140; index += 1) {
      const other = join(home, `evict-${index}`, '.system_generated', 'logs')
      mkdirSync(other, { recursive: true })
      const target = join(other, 'transcript_full.jsonl')
      writeFileSync(target, line({ step_index: 0, type: 'PLANNER_RESPONSE', status: 'DONE', created_at: old.toISOString(), content: `evict ${index}` }))
      mapLib.readTranscript(join(home, `evict-${index}`), true)
    }
  }
  const refresh = async (title) => {
    const before = node.all('runtime.sessions').length
    await node.peer.call('session.rename', { session_id: BEHIND_SESSION, title })
    await until('behind list refreshed', () => node.all('runtime.sessions').length > before, 15_000)
    return node.last('runtime.sessions').sessions.find((row) => row.session_id === BEHIND_SESSION)
  }
  return { counts, markerFile, writeMarker, setStepCount, evictTranscriptCache, refresh }
}

test('a completion-marker-restored done summary_behind row lists once and invalidates when the marker changes', { timeout: 30_000 }, async (t) => {
  const api = await setupBehindListCache(t)

  api.writeMarker()
  api.evictTranscriptCache()
  api.counts.fullReadFile = 0
  const first = await api.refresh('completion-marker-first')
  assert.equal(first.status, 'done', 'the valid .persona-turn-completion marker restores done via #15782')
  assert.ok(api.counts.fullReadFile >= 1, 'the key change to a present marker is a listed-cache miss that reads once')

  api.evictTranscriptCache()
  api.counts.fullReadFile = 0
  const second = await api.refresh('completion-marker-second')
  assert.equal(second.status, 'done')
  assert.equal(api.counts.fullReadFile, 0, 'consecutive unchanged lists hit listedTranscriptCache and never re-read the transcript')

  // Change the marker's size: its (size, mtimeMs, ino) key changes immediately, forcing recompute.
  api.writeMarker(':edge-bytes-changed')
  api.evictTranscriptCache()
  api.counts.fullReadFile = 0
  const changed = await api.refresh('completion-marker-mutated')
  assert.equal(changed.status, 'done')
  assert.ok(api.counts.fullReadFile >= 1, 'modifying .persona-turn-completion must invalidate the listed cache key')
})

test('a negatively checked static summary_behind row lists with zero reads and invalidates on step_count or marker changes', { timeout: 30_000 }, async (t) => {
  const api = await setupBehindListCache(t)

  // One list after the authoritative noCutChecks entry is settled admits the static unknown row.
  await api.refresh('summary-behind-prime')
  api.evictTranscriptCache()
  api.counts.fullReadFile = 0
  const cached = await api.refresh('summary-behind-hit')
  assert.equal(cached.status, 'unknown')
  assert.equal(api.counts.fullReadFile, 0, 'a settled noCutChecks summary_behind row must hit listedTranscriptCache with no transcript read')

  // conversation_summaries.step_count is part of nativeKey, so changing it invalidates at once.
  api.setStepCount(7)
  api.evictTranscriptCache()
  api.counts.fullReadFile = 0
  const nativeChanged = await api.refresh('summary-behind-step-count')
  assert.ok(api.counts.fullReadFile >= 1, 'a step_count change must invalidate the listed cache')
  assert.ok(nativeChanged, 'a recomputed row is returned')

  // Restore a lagging count, re-prime the static row, then add the completion marker: keyed invalidation.
  api.setStepCount(2)
  await api.refresh('summary-behind-reprime')
  api.writeMarker()
  api.evictTranscriptCache()
  api.counts.fullReadFile = 0
  const markerAdded = await api.refresh('summary-behind-marker-added')
  assert.ok(api.counts.fullReadFile >= 1, 'creating .persona-turn-completion must invalidate the static summary_behind cache')
  assert.equal(markerAdded.status, 'done')
})

test('listedTranscriptCache serves background tasks without retaining full exported steps', { timeout: 30_000 }, async (t) => {
  const api = await setupBehindListCache(t)
  api.writeMarker()
  api.evictTranscriptCache()
  api.counts.fullReadFile = 0
  const first = await api.refresh('bg-summary-first')
  assert.equal(first.status, 'done')
  assert.ok(api.counts.fullReadFile >= 1, 'initial list reads full transcript once')

  api.evictTranscriptCache()
  api.counts.fullReadFile = 0
  const second = await api.refresh('bg-summary-second')
  assert.equal(second.status, 'done')
  assert.equal(api.counts.fullReadFile, 0, 'cached list reuses compact background summary with zero transcript reads')
  assert.deepEqual(second.agy_pane_background, first.agy_pane_background)
})
