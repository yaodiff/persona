#!/usr/bin/env node
// Offline micro-benchmark for the conversation_summaries.db cache (issue #16403).
// Builds 700 summary rows plus a large placeholder table, then compares the old
// open/full-scan/JSON.parse/close-per-tick path with the long-lived stat-snapshot
// gate and the real watched-poll short-circuit. Run:
//   node test/summary-db.bench.mjs [--mb=64] [--ticks=100]
import { mkdtempSync, mkdirSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { createSummaryStateStore } from '../lib/summary-db.js'
import { createAgyRuntime } from '../lib/runtime.js'

const arg = (name, fallback) => {
  const hit = process.argv.find((a) => a.startsWith(`--${name}=`))
  return hit ? Number(hit.split('=')[1]) : fallback
}
const MB = arg('mb', 32)
const TICKS = arg('ticks', 100)
const RAW = '55555555-6666-4777-8888-999999999999'
const COLS = `conversation_id, status, not_fully_idle, killed, step_count, title, workspace_uris, last_modified_time`

const dir = mkdtempSync(join(tmpdir(), 'agy-summary-bench-'))
const dbPath = join(dir, 'conversation_summaries.db')
const writer = new DatabaseSync(dbPath)
writer.exec('PRAGMA journal_mode=WAL')
writer.exec(`CREATE TABLE conversation_summaries (conversation_id TEXT PRIMARY KEY, status TEXT,
  not_fully_idle INTEGER, killed INTEGER, step_count INTEGER, title TEXT, workspace_uris TEXT, last_modified_time TEXT)`)
writer.exec('CREATE TABLE bulk (id INTEGER PRIMARY KEY, pad TEXT)')
const insert = writer.prepare(`INSERT INTO conversation_summaries (${COLS}) VALUES (?,?,?,?,?,?,?,?)`)
const bulk = writer.prepare('INSERT INTO bulk (pad) VALUES (?)')
const pad4k = 'p'.repeat(4096)
writer.exec('BEGIN')
for (let i = 0; i < 700; i++) {
  insert.run(`conv-${i}`, 'CASCADE_RUN_STATUS_IDLE', 0, 0, i, `Title ${i}`,
    JSON.stringify(['file:///workspace/demo', `file:///workspace/demo-${i}`]), '2026-01-01T00:00:00Z')
}
insert.run(RAW, 'IDLE', 0, 0, 2, 'Watched', '[]', 'x')
const bulkRows = Math.floor((MB * 1024 * 1024) / 4096)
for (let i = 0; i < bulkRows; i++) bulk.run(pad4k)
writer.exec('COMMIT')
writer.close()

const legacyTick = () => {
  const db = new DatabaseSync(dbPath, { readOnly: true })
  let parsed = 0
  try {
    db.exec('PRAGMA query_only=1; PRAGMA busy_timeout=50')
    const rows = db.prepare(`SELECT ${COLS} FROM conversation_summaries`).all()
    for (const row of rows) { JSON.parse(row.workspace_uris); parsed++ }
    return rows.length
  } finally {
    db.close()
  }
}

let t = performance.now()
for (let i = 0; i < TICKS; i++) legacyTick()
const legacyMs = performance.now() - t

const store = createSummaryStateStore(dbPath)
t = performance.now()
for (let i = 0; i < TICKS; i++) store.states()
const cachedMs = performance.now() - t
const storeStats = { ...store.stats }
store.close()

// Real runtime watched-poll path (includes the syncTranscriptAppend short-circuit).
const agyDir = dir
const logs = join(agyDir, 'brain', RAW, '.system_generated', 'logs')
mkdirSync(logs, { recursive: true })
writeFileSync(join(logs, 'transcript_full.jsonl'),
  JSON.stringify({ step_index: 0, type: 'USER_INPUT', source: 'USER_EXPLICIT', created_at: '2026-01-01T00:00:00Z', content: 'hi' }) + '\n' +
  JSON.stringify({ step_index: 1, type: 'PLANNER_RESPONSE', status: 'DONE', created_at: '2026-01-01T00:00:01Z', content: 'hello' }) + '\n')
// Reopen a writer so the fixture stays in a realistic WAL state.
const w2 = new DatabaseSync(dbPath)
const runtime = createAgyRuntime({ home: dir, agyDir, log: () => {}, socketPath: join(dir, 'n.sock') })
t = performance.now()
for (let i = 0; i < TICKS; i++) runtime.pollAppend(RAW)
const tickMs = performance.now() - t
const pollStats = { ...runtime.appendPollStats }
const tickStoreStats = { ...runtime.summaryDb().stats }
await runtime.stop()
w2.close()

const sizeMb = statSync(dbPath).size / 1048576

console.log(JSON.stringify({
  ticks: TICKS,
  targetMb: MB,
  dbSizeMb: Number(sizeMb.toFixed(1)),
  legacy: { totalMs: Number(legacyMs.toFixed(2)), perTickUs: Number((legacyMs * 1000 / TICKS).toFixed(1)) },
  statSnapshot: { totalMs: Number(cachedMs.toFixed(3)), perTickUs: Number((cachedMs * 1000 / TICKS).toFixed(2)), ...storeStats },
  runtimePollTicks: { totalMs: Number(tickMs.toFixed(2)), perTickUs: Number((tickMs * 1000 / TICKS).toFixed(1)), ...pollStats, db: tickStoreStats },
  speedupSnapshotVsLegacy: Number((legacyMs / Math.max(cachedMs, 0.0001)).toFixed(1)),
}, null, 2))

rmSync(dir, { recursive: true, force: true })
process.exit(0)
