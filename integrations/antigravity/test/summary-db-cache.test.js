import assert from 'node:assert/strict'
import { appendFileSync, mkdtempSync, mkdirSync, renameSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { test } from 'node:test'
import { createSummaryStateStore } from '../lib/summary-db.js'
import { createAgyRuntime } from '../lib/runtime.js'

const RAW = '44444444-5555-4666-8777-888888888888'
const ALL_COLUMNS = `conversation_id, status, not_fully_idle, killed, step_count,
  title, workspace_uris, last_modified_time`

// 700 real summary rows plus a large placeholder table so the database is many
// megabytes and reopening it throws away SQLite's warm page cache.
function createFixture(dir, { rows = 700, bulkRows = 3000, bulkPadKb = 2 } = {}) {
  const dbPath = join(dir, 'conversation_summaries.db')
  const writer = new DatabaseSync(dbPath)
  writer.exec('PRAGMA journal_mode = WAL')
  writer.exec(`CREATE TABLE conversation_summaries (
    conversation_id TEXT PRIMARY KEY, status TEXT, not_fully_idle INTEGER, killed INTEGER,
    step_count INTEGER, title TEXT, workspace_uris TEXT, last_modified_time TEXT)`)
  const insert = writer.prepare(`INSERT INTO conversation_summaries (${ALL_COLUMNS}) VALUES (?,?,?,?,?,?,?,?)`)
  writer.exec('CREATE TABLE bulk (id INTEGER PRIMARY KEY, pad TEXT)')
  const bulk = writer.prepare('INSERT INTO bulk (pad) VALUES (?)')
  const pad = 'x'.repeat(bulkPadKb * 1024)
  writer.exec('BEGIN')
  for (let i = 0; i < rows; i++) {
    insert.run(`conv-${i}`, 'CASCADE_RUN_STATUS_IDLE', 0, 0, i, `Title ${i}`,
      JSON.stringify(['file:///workspace/demo', `file:///workspace/demo-${i}`]), '2026-01-01T00:00:00Z')
  }
  insert.run(RAW, 'CASCADE_RUN_STATUS_IDLE', 0, 0, 2, 'Watched session', '[]', '2026-01-01T00:00:10Z')
  for (let i = 0; i < bulkRows; i++) bulk.run(pad)
  writer.exec('COMMIT')
  return { dbPath, writer }
}

function withFixture(fn) {
  const dir = mkdtempSync(join(tmpdir(), 'agy-summary-db-'))
  const fixture = createFixture(dir)
  try {
    return fn({ dir, ...fixture })
  } finally {
    try { fixture.writer.close() } catch {}
    rmSync(dir, { recursive: true, force: true })
  }
}

test('100 cached reads open and scan the database once and reuse parsed rows', () => {
  withFixture(({ dbPath }) => {
    const store = createSummaryStateStore(dbPath)
    try {
      const first = store.states()
      assert.equal(first.size, 701)
      assert.deepEqual(first.get('conv-12').WorkspaceURIs,
        ['file:///workspace/demo', 'file:///workspace/demo-12'])
      for (let i = 0; i < 99; i++) assert.equal(store.states(), first, 'unchanged signature returns the same Map')
      assert.deepEqual(store.stats, {
        opens: 1, closes: 0, fullScans: 1, pointLookups: 0, cacheHits: 99, reopens: 0, probes: 0,
      })
    } finally {
      store.close()
    }
  })
})

test('a WAL write invalidates the snapshot and exposes the latest state', () => {
  withFixture(({ dbPath, writer }) => {
    const store = createSummaryStateStore(dbPath)
    try {
      assert.equal(store.states().get('conv-1').status, 'CASCADE_RUN_STATUS_IDLE')
      writer.prepare("UPDATE conversation_summaries SET status='CASCADE_RUN_STATUS_RUNNING' WHERE conversation_id='conv-1'").run()
      const updated = store.states()
      assert.equal(updated.get('conv-1').status, 'CASCADE_RUN_STATUS_RUNNING')
      assert.ok(store.stats.fullScans >= 2, 'WAL signature change forces a rescan')
      // Unchanged again: cached.
      const scans = store.stats.fullScans
      store.states()
      assert.equal(store.stats.fullScans, scans)
    } finally {
      store.close()
    }
  })
})

test('replacing the database file (new inode) closes and reopens the long connection', () => {
  const dir = mkdtempSync(join(tmpdir(), 'agy-summary-replace-'))
  const dbPath = join(dir, 'conversation_summaries.db')
  const writer = new DatabaseSync(dbPath)
  writer.exec('CREATE TABLE conversation_summaries (conversation_id TEXT PRIMARY KEY, status TEXT, not_fully_idle INTEGER, killed INTEGER, step_count INTEGER, title TEXT, workspace_uris TEXT, last_modified_time TEXT)')
  writer.prepare('INSERT INTO conversation_summaries VALUES (?,?,?,?,?,?,?,?)').run(RAW, 'IDLE', 0, 0, 2, 'old', '[]', 'x')
  writer.close()
  const store = createSummaryStateStore(dbPath)
  try {
    assert.equal(store.states().get(RAW).status, 'IDLE')
    assert.equal(store.stats.opens, 1)

    const replacement = join(dir, 'replacement.db')
    const next = new DatabaseSync(replacement)
    next.exec('CREATE TABLE conversation_summaries (conversation_id TEXT PRIMARY KEY, status TEXT, not_fully_idle INTEGER, killed INTEGER, step_count INTEGER, title TEXT, workspace_uris TEXT, last_modified_time TEXT)')
    next.prepare('INSERT INTO conversation_summaries VALUES (?,?,?,?,?,?,?,?)').run(RAW, 'REPLACED', 0, 0, 9, 'new', '[]', 'y')
    next.close()
    rmSync(`${dbPath}-wal`, { force: true })
    rmSync(`${dbPath}-shm`, { force: true })
    renameSync(replacement, dbPath)

    const snapshot = store.states()
    assert.equal(snapshot.get(RAW).status, 'REPLACED')
    assert.equal(snapshot.get(RAW).step_count, 9)
    assert.equal(store.stats.opens, 2)
    assert.equal(store.stats.reopens, 1)
  } finally {
    store.close()
    rmSync(dir, { recursive: true, force: true })
  }
  assert.equal(store.connected, false)
})

test('nativeStateOf cold reads use a prepared primary-key point lookup', () => {
  withFixture(({ dbPath }) => {
    const store = createSummaryStateStore(dbPath)
    try {
      const row = store.get(RAW) // cold: no snapshot -> point query
      assert.equal(row.conversation_id, RAW)
      assert.equal(row.step_count, 2)
      assert.equal(store.stats.pointLookups, 1)
      assert.equal(store.stats.fullScans, 0)
      // A full snapshot is now warm; gets are served from the Map.
      store.states()
      const before = store.stats.pointLookups
      assert.equal(store.get(RAW).title, 'Watched session')
      assert.equal(store.stats.pointLookups, before)
    } finally {
      store.close()
    }
  })
})

test('checkedStates preserves agy exclusive-lock read failures while cached states stay lock-free', () => {
  const dir = mkdtempSync(join(tmpdir(), 'agy-summary-lock-'))
  const dbPath = join(dir, 'conversation_summaries.db')
  // Default rollback journal on purpose: BEGIN EXCLUSIVE must block readers.
  const writer = new DatabaseSync(dbPath)
  writer.exec('CREATE TABLE conversation_summaries (conversation_id TEXT PRIMARY KEY, status TEXT, not_fully_idle INTEGER, killed INTEGER, step_count INTEGER, title TEXT, workspace_uris TEXT, last_modified_time TEXT)')
  writer.prepare('INSERT INTO conversation_summaries VALUES (?,?,?,?,?,?,?,?)').run(RAW, 'IDLE', 0, 0, 1, 't', '[]', 'x')
  const store = createSummaryStateStore(dbPath)
  try {
    assert.equal(store.checkedStates().get(RAW).status, 'IDLE')
    writer.exec('BEGIN EXCLUSIVE')
    assert.throws(() => store.checkedStates(), /database is locked/)
    assert.equal(store.connected, false)
    assert.equal(store.stats.opens, store.stats.closes)
    // The pure snapshot accessor does not probe the lock and keeps serving cache.
    assert.equal(store.states().get(RAW).status, 'IDLE')
    writer.exec('ROLLBACK')
    assert.equal(store.checkedStates().get(RAW).status, 'IDLE')
  } finally {
    try { writer.exec('ROLLBACK') } catch {}
    writer.close()
    store.close()
    rmSync(dir, { recursive: true, force: true })
  }
})

test('transient exclusive lock during cold or reopened probe/states does not poison store or skew open/close stats', () => {
  const dir = mkdtempSync(join(tmpdir(), 'agy-summary-poison-'))
  const dbPath = join(dir, 'conversation_summaries.db')
  const writer = new DatabaseSync(dbPath)
  writer.exec('CREATE TABLE conversation_summaries (conversation_id TEXT PRIMARY KEY, status TEXT, not_fully_idle INTEGER, killed INTEGER, step_count INTEGER, title TEXT, workspace_uris TEXT, last_modified_time TEXT)')
  writer.prepare('INSERT INTO conversation_summaries VALUES (?,?,?,?,?,?,?,?)').run(RAW, 'IDLE', 0, 0, 1, 't', '[]', 'x')
  const store = createSummaryStateStore(dbPath)
  try {
    // 1. Warm snapshot, then lose connection under exclusive lock so probe() must reopen while locked.
    assert.equal(store.checkedStates().get(RAW).status, 'IDLE')
    writer.exec('BEGIN EXCLUSIVE')
    assert.throws(() => store.point(RAW), /database is locked/)
    assert.equal(store.connected, false)
    // states() serves cached snapshot, then probe() calls connection() while locked -> must fail cleanly without poisoning db/probeStmt.
    assert.throws(() => store.checkedStates(), /database is locked/)
    assert.equal(store.connected, false)
    writer.exec('ROLLBACK')
    // After lock release, checkedStates() must recover instead of throwing TypeError on undefined probeStmt.
    assert.equal(store.checkedStates().get(RAW).status, 'IDLE')
    assert.equal(store.connected, true)

    // 2. Cold probe() under exclusive lock must not leave a half-initialized connection or corrupt stats.
    const cold = createSummaryStateStore(dbPath)
    try {
      writer.exec('BEGIN EXCLUSIVE')
      assert.throws(() => cold.probe(), /database is locked/)
      assert.equal(cold.connected, false)
      assert.equal(cold.stats.opens, 0)
      assert.equal(cold.stats.closes, 0)
      assert.throws(() => cold.states(), /database is locked/)
      assert.equal(cold.stats.opens, 0)
      assert.equal(cold.stats.closes, 0)
      writer.exec('ROLLBACK')
      cold.probe()
      assert.equal(cold.checkedStates().get(RAW).status, 'IDLE')
    } finally {
      cold.close()
      assert.equal(cold.stats.opens, cold.stats.closes)
    }
  } finally {
    try {
      try { writer.exec('ROLLBACK') } catch {}
      writer.close()
      store.close()
    } finally {
      rmSync(dir, { recursive: true, force: true })
    }
    assert.equal(store.stats.opens, store.stats.closes)
  }
})

test('close releases the long-lived connection exactly once per open', () => {
  withFixture(({ dbPath }) => {
    const store = createSummaryStateStore(dbPath)
    store.states()
    assert.equal(store.connected, true)
    store.close()
    assert.equal(store.connected, false)
    assert.equal(store.stats.opens, store.stats.closes)
    store.close() // idempotent
    assert.equal(store.stats.closes, store.stats.opens)
  })
})

test('100 watched ticks evaluate SQLite once and the poll short-circuit follows transcript and WAL changes', async (t) => {
  const home = mkdtempSync(join(tmpdir(), 'agy-tick-'))
  const agyDir = join(home, 'config')
  const logs = join(agyDir, 'brain', RAW, '.system_generated', 'logs')
  mkdirSync(logs, { recursive: true })
  const transcript = join(logs, 'transcript_full.jsonl')
  const line = (obj) => JSON.stringify({ created_at: '2026-01-01T00:00:00Z', ...obj }) + '\n'
  writeFileSync(transcript,
    line({ step_index: 0, type: 'USER_INPUT', source: 'USER_EXPLICIT', content: 'hi' }) +
    line({ step_index: 1, type: 'PLANNER_RESPONSE', status: 'DONE', content: 'hello' }))
  const fixture = createFixture(agyDir)

  const runtime = createAgyRuntime({ home, agyDir, log: () => {}, socketPath: join(home, 'none.sock') })
  t.after(async () => {
    await runtime.stop().catch(() => {})
    rmSync(home, { recursive: true, force: true })
  })
  const store = runtime.summaryDb()

  for (let i = 0; i < 100; i++) runtime.pollAppend(RAW)
  assert.equal(runtime.appendPollStats.checks, 100)
  assert.equal(runtime.appendPollStats.skips, 99, 'first tick evaluates, identical later ticks skip')
  assert.equal(store.stats.opens, 1)
  assert.equal(store.stats.fullScans, 1)

  // Appending to the transcript changes transcriptVersion -> one real re-evaluation.
  appendFileSync(transcript, line({ step_index: 2, type: 'PLANNER_RESPONSE', status: 'DONE', content: 'more' }))
  runtime.pollAppend(RAW)
  assert.equal(runtime.appendPollStats.skips, 99, 'transcript change must invalidate the short-circuit')
  runtime.pollAppend(RAW)
  assert.equal(runtime.appendPollStats.skips, 100, 'new unchanged fingerprint skips again')

  // A database/WAL change also invalidates.
  const before = runtime.appendPollStats.skips
  fixture.writer.prepare("UPDATE conversation_summaries SET status='WORKING' WHERE conversation_id=?").run(RAW)
  runtime.pollAppend(RAW)
  assert.equal(runtime.appendPollStats.skips, before)

  await runtime.stop()
  fixture.writer.close()
  assert.equal(store.connected, false, 'stop() closes the summary database handle')
  assert.equal(store.stats.opens, store.stats.closes)
})

test('offline micro-benchmark: cached gate is dramatically cheaper than open-scan-parse-close every tick', () => {
  withFixture(({ dbPath }) => {
    const countRows = () => {
      const db = new DatabaseSync(dbPath, { readOnly: true })
      try {
        db.exec('PRAGMA query_only = 1; PRAGMA busy_timeout = 50')
        const rows = db.prepare(`SELECT ${ALL_COLUMNS} FROM conversation_summaries`).all()
        for (const row of rows) {
          try { JSON.parse(row.workspace_uris) } catch {}
        }
        return rows.length
      } finally {
        db.close()
      }
    }
    assert.equal(countRows(), 701)

    const store = createSummaryStateStore(dbPath)
    store.states()
    const N = 100
    let t0 = performance.now()
    for (let i = 0; i < N; i++) countRows()
    const legacyMs = performance.now() - t0

    t0 = performance.now()
    for (let i = 0; i < N; i++) store.states()
    const cachedMs = performance.now() - t0
    store.close()

    console.log(`summary DB x${N}: legacy open/full-scan/parse/close ${legacyMs.toFixed(1)}ms; ` +
      `stat-snapshot cache ${cachedMs.toFixed(2)}ms; speedup ${(legacyMs / Math.max(cachedMs, 0.001)).toFixed(1)}x`)
    assert.ok(cachedMs < 250, `cached 100 reads took ${cachedMs}ms`)
    assert.ok(legacyMs / Math.max(cachedMs, 0.001) >= 3, 'cache must be at least ~3x faster in the tight loop')
  })
})
