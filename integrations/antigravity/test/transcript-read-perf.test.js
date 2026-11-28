import assert from 'node:assert/strict'
import fs, {
  appendFileSync,
  closeSync,
  mkdirSync,
  mkdtempSync,
  openSync,
  renameSync,
  rmSync,
  utimesSync,
  writeFileSync,
  writeSync,
} from 'node:fs'
import { syncBuiltinESMExports } from 'node:module'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { describe, it } from 'node:test'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import {
  readTranscript,
  rememberObservedWake,
  stepsToItems,
} from '../lib/map.js'
import { createAgyRuntime } from '../lib/runtime.js'

function makeBrainDir(t) {
  const root = mkdtempSync(join(tmpdir(), 'agy-perf-'))
  const logs = join(root, '.system_generated', 'logs')
  mkdirSync(logs, { recursive: true })
  t.after(() => rmSync(root, { recursive: true, force: true }))
  return { root, logs, file: join(logs, 'transcript_full.jsonl') }
}

function makeStep(i, content, extra = {}) {
  return {
    step_index: i,
    source: i % 2 === 0 ? 'USER_EXPLICIT' : 'MODEL',
    type: i % 2 === 0 ? 'USER_INPUT' : 'PLANNER_RESPONSE',
    status: 'DONE',
    created_at: `2026-11-26T00:${String(Math.floor(i / 60) % 60).padStart(2, '0')}:${String(i % 60).padStart(2, '0')}Z`,
    content,
    ...extra,
  }
}

function line(step) {
  return JSON.stringify(step) + '\n'
}

// Wrap real node:fs read operations and sync ESM bindings so we measure exact
// bytes read from real temporary files without modifying production I/O paths.
function installReadMeter(t, targetPrefix) {
  const origOpen = fs.openSync
  const origRead = fs.readSync
  const origReadFile = fs.readFileSync
  const origClose = fs.closeSync
  const trackedFds = new Set()
  let insideReadFile = 0
  const stats = { opens: 0, reads: 0, readFiles: 0, bytesRead: 0 }
  const matches = (p) => typeof p === 'string' && p.startsWith(targetPrefix)

  fs.openSync = function (path, ...rest) {
    const fd = origOpen.call(this, path, ...rest)
    if (matches(path) && insideReadFile === 0) {
      trackedFds.add(fd)
      stats.opens++
    }
    return fd
  }
  fs.readSync = function (fd, ...rest) {
    const n = origRead.call(this, fd, ...rest)
    if (trackedFds.has(fd) && n > 0) {
      stats.reads++
      stats.bytesRead += n
    }
    return n
  }
  fs.closeSync = function (fd) {
    trackedFds.delete(fd)
    return origClose.call(this, fd)
  }
  fs.readFileSync = function (path, ...rest) {
    const hit = matches(path)
    if (hit) insideReadFile++
    try {
      const data = origReadFile.call(this, path, ...rest)
      if (hit) {
        stats.readFiles++
        stats.bytesRead += Buffer.isBuffer(data) ? data.length : Buffer.byteLength(String(data))
      }
      return data
    } finally {
      if (hit) insideReadFile--
    }
  }
  syncBuiltinESMExports()

  t.after(() => {
    fs.openSync = origOpen
    fs.readSync = origRead
    fs.readFileSync = origReadFile
    fs.closeSync = origClose
    syncBuiltinESMExports()
  })

  return {
    stats,
    reset() {
      stats.opens = 0
      stats.reads = 0
      stats.readFiles = 0
      stats.bytesRead = 0
    },
  }
}

function installFakeNow(t, initialMs) {
  const origNow = Date.now
  let current = initialMs
  Date.now = () => current
  t.after(() => { Date.now = origNow })
  return {
    get: () => current,
    set: (ms) => { current = ms },
    advance: (deltaMs) => { current += deltaMs },
  }
}

describe('companion transcript read performance, integrity, backoff, and bounded caches', () => {
  it('hits fileMeta cache with zero content reads inside the old 1000ms settle window while detecting same-size fast rewrites', (t) => {
    const { root, file } = makeBrainDir(t)
    const pad = 'p'.repeat(500)
    const rows = Array.from({ length: 220 }, (_, i) => line(makeStep(i, `step-${i}-${pad}`)))
    writeFileSync(file, rows.join(''))
    const st = fs.statSync(file, { bigint: true })
    const fileSize = Number(st.size)
    assert.ok(fileSize > 120 * 1024, `expected >120KB fixture, got ${fileSize}`)

    const meter = installReadMeter(t, root)
    // Position fake clock 150ms after file mtime/ctime: past the sub-second racy window,
    // yet still well inside the legacy 1000ms settle threshold.
    const touchMs = Math.max(Number(st.mtimeMs), Number(st.ctimeMs))
    const clock = installFakeNow(t, touchMs + 150)

    meter.reset()
    const cold = readTranscript(root, true)
    assert.equal(cold.steps.length, 220)
    assert.equal(meter.stats.bytesRead, fileSize)

    // Repeat reads at +150ms, +350ms, +550ms, +750ms, +900ms (all < 1000ms since mtime).
    for (const delta of [0, 200, 200, 200, 150]) {
      clock.advance(delta)
      meter.reset()
      const hit = readTranscript(root, true)
      assert.equal(hit, cold, 'unchanged fileMeta must return cached transcript object')
      assert.equal(meter.stats.bytesRead, 0, 'unchanged fileMeta hit must read 0 content bytes')
      assert.equal(meter.stats.opens, 0, 'unchanged fileMeta hit must not open the transcript file')
      assert.equal(meter.stats.readFiles, 0, 'unchanged fileMeta hit must not call readFileSync')
    }

    // Same-size rewrite AFTER racy window (mtime advances -> fileMeta changes) must still be detected.
    const rowsEdited = rows.slice()
    rowsEdited[42] = line(makeStep(42, `EDIT-${42}-${pad.slice(0, pad.length - 0)}`.slice(0, `step-42-${pad}`.length)))
    assert.equal(Buffer.byteLength(rowsEdited.join('')), fileSize)
    writeFileSync(file, rowsEdited.join(''))
    const bumped = new Date(touchMs + 2000)
    utimesSync(file, bumped, bumped)
    clock.set(touchMs + 2150)
    const afterRewritten = readTranscript(root, true)
    assert.notEqual(afterRewritten, cold)
    assert.ok(afterRewritten.steps[42].content.startsWith('EDIT-42-'))
    assert.notEqual(afterRewritten.revision, cold.revision)

    // Same-size fast rewrite INSIDE the racy window (same mtime tick, 0ms elapsed) must still be detected.
    const fast = makeBrainDir(t)
    const fastA = Array.from({ length: 24 }, (_, i) => line(makeStep(i, `ORIG-${String(i).padStart(2, '0')}-${'a'.repeat(400)}`)))
    writeFileSync(fast.file, fastA.join(''))
    const fastSt = fs.statSync(fast.file, { bigint: true })
    clock.set(Math.max(Number(fastSt.mtimeMs), Number(fastSt.ctimeMs)))
    const firstFast = readTranscript(fast.root, true)
    const fastB = fastA.slice()
    fastB[10] = line(makeStep(10, `EDIT-10-${'a'.repeat(400)}`))
    assert.equal(Buffer.byteLength(fastB.join('')), Buffer.byteLength(fastA.join('')))
    writeFileSync(fast.file, fastB.join(''))
    const secondFast = readTranscript(fast.root, true)
    assert.notEqual(secondFast, firstFast)
    assert.ok(secondFast.steps[10].content.startsWith('EDIT-10-'))
    assert.notEqual(secondFast.revision, firstFast.revision)
  })

  it('appends on large files read only tail anchor + delta instead of re-reading full prefix, preserving partial lines, late arrivals, rewind, wake, truncation, and inode replacement', (t) => {
    const { root, logs, file } = makeBrainDir(t)
    const pad = 'x'.repeat(500)
    const initialRows = Array.from({ length: 300 }, (_, i) => line(makeStep(i, `base-${i}-${pad}`)))
    writeFileSync(file, initialRows.join(''))
    const initStat = fs.statSync(file, { bigint: true })
    const initSize = Number(initStat.size)
    assert.ok(initSize > 160 * 1024, `expected >160KB fixture, got ${initSize}`)

    const meter = installReadMeter(t, root)
    const clock = installFakeNow(t, Math.max(Number(initStat.mtimeMs), Number(initStat.ctimeMs)) + 100)

    const base = readTranscript(root, true)
    assert.equal(base.steps.length, 300)

    // Perform 8 ordinary appends + 1 partial-line split + 1 late arrival + 1 rewind replacement.
    let totalAppendBytesRead = 0
    for (let i = 300; i < 308; i++) {
      const added = line(makeStep(i, `delta-${i}-${pad}`))
      appendFileSync(file, added)
      const st = fs.statSync(file, { bigint: true })
      clock.set(Math.max(Number(st.mtimeMs), Number(st.ctimeMs)) + 100)
      meter.reset()
      const next = readTranscript(root, true)
      assert.equal(next.steps.length, i + 1)
      assert.equal(next.steps[0], base.steps[0], 'earlier step objects stay reused')
      assert.equal(next.steps[i].content, `delta-${i}-${pad}`)
      assert.ok(
        meter.stats.bytesRead <= 4096 + Buffer.byteLength(added) + 128,
        `append ${i} read ${meter.stats.bytesRead} bytes (expected <= tailAnchor+delta ~${4096 + Buffer.byteLength(added)}, fileSize=${initSize})`,
      )
      totalAppendBytesRead += meter.stats.bytesRead
    }
    assert.ok(totalAppendBytesRead < initSize * 0.5, `8 appends should read far less than 1x full file (${totalAppendBytesRead} vs ${initSize})`)

    // Partial trailing line split across two writes (model reply after completed step 307 so wake can be observed).
    const partialRaw = JSON.stringify(makeStep(308, `partial-308-${pad}`, { type: 'PLANNER_RESPONSE', source: 'MODEL' }))
    appendFileSync(file, partialRaw.slice(0, 40))
    let st = fs.statSync(file, { bigint: true })
    clock.set(Math.max(Number(st.mtimeMs), Number(st.ctimeMs)) + 100)
    meter.reset()
    const tPart = readTranscript(root, true)
    assert.equal(tPart.steps.length, 308)
    assert.ok(meter.stats.bytesRead <= 4096 + 64, `partial append read ${meter.stats.bytesRead} bytes`)

    appendFileSync(file, partialRaw.slice(40) + '\n')
    st = fs.statSync(file, { bigint: true })
    clock.set(Math.max(Number(st.mtimeMs), Number(st.ctimeMs)) + 100)
    meter.reset()
    const tComplete = readTranscript(root, true)
    assert.equal(tComplete.steps.length, 309)
    assert.equal(tComplete.steps[308].content, `partial-308-${pad}`)
    assert.ok(meter.stats.bytesRead <= 4096 + Buffer.byteLength(partialRaw) + 64)

    // Wake receipt applies without re-reading transcript content.
    rememberObservedWake(root, tComplete.steps, 308)
    meter.reset()
    const tWake = readTranscript(root, true)
    assert.match(tWake.revision, /:wake:/)
    assert.equal(meter.stats.opens, 0, 'wake receipt refresh must not re-open transcript')

    // Out-of-order late arrival (310 then 309) stays fast and updates revision.
    appendFileSync(file, line(makeStep(310, 'late-second', { type: 'GENERIC', source: 'MODEL' })))
    st = fs.statSync(file, { bigint: true })
    clock.set(Math.max(Number(st.mtimeMs), Number(st.ctimeMs)) + 100)
    const t310 = readTranscript(root, true)
    appendFileSync(file, line(makeStep(309, 'late-first', { type: 'GENERIC', source: 'MODEL' })))
    st = fs.statSync(file, { bigint: true })
    clock.set(Math.max(Number(st.mtimeMs), Number(st.ctimeMs)) + 100)
    meter.reset()
    const tLate = readTranscript(root, true)
    assert.deepEqual(tLate.steps.slice(-2).map((s) => s.step_index), [309, 310])
    assert.notEqual(tLate.revision, t310.revision)
    assert.ok(meter.stats.bytesRead < 8192)

    // Rewind replacement appended incrementally.
    appendFileSync(file, line(makeStep(305, 'rewound-305', { type: 'PLANNER_RESPONSE', source: 'MODEL', created_at: '2026-11-26T01:00:00Z' })))
    st = fs.statSync(file, { bigint: true })
    clock.set(Math.max(Number(st.mtimeMs), Number(st.ctimeMs)) + 100)
    meter.reset()
    const tRewind = readTranscript(root, true)
    assert.equal(tRewind.steps.length, 306)
    assert.equal(tRewind.steps[305].content, 'rewound-305')
    assert.ok(tRewind.branch)
    assert.ok(meter.stats.bytesRead < 8192)

    // Truncation falls back to full parse.
    writeFileSync(file, initialRows.slice(0, 3).join(''))
    st = fs.statSync(file, { bigint: true })
    clock.set(Math.max(Number(st.mtimeMs), Number(st.ctimeMs)) + 100)
    const tTrunc = readTranscript(root, true)
    assert.equal(tTrunc.steps.length, 3)

    // Atomic inode replacement via rename falls back to full parse.
    const tmp = join(logs, 'transcript_full.jsonl.next')
    writeFileSync(tmp, line(makeStep(0, 'atomic-replaced')))
    renameSync(tmp, file)
    const tAtomic = readTranscript(root, true)
    assert.equal(tAtomic.steps.length, 1)
    assert.equal(tAtomic.steps[0].content, 'atomic-replaced')
  })

  it('detects length-changing prefix rewrite immediately via tail anchor and catches same-length mid-file in-place edit + append via deferred idle and periodic verification', (t) => {
    const pad = 'm'.repeat(400)

    // 1. Length-changing prefix rewrite + append on >64KB file -> immediate tail-anchor mismatch fallback.
    {
      const { root, file } = makeBrainDir(t)
      const rows = Array.from({ length: 220 }, (_, i) => line(makeStep(i, `ORIG-${String(i).padStart(3, '0')}-${pad}`)))
      writeFileSync(file, rows.join(''))
      const st0 = fs.statSync(file, { bigint: true })
      const clock = installFakeNow(t, Math.max(Number(st0.mtimeMs), Number(st0.ctimeMs)) + 100)
      const before = readTranscript(root, true)
      assert.equal(before.steps.length, 220)

      const rewritten = rows.slice()
      rewritten[5] = line(makeStep(5, `EXPANDED_PREFIX_REWRITE-${pad}`))
      rewritten.push(line(makeStep(220, `TAIL-${pad}`)))
      writeFileSync(file, rewritten.join(''))
      const st1 = fs.statSync(file, { bigint: true })
      clock.set(Math.max(Number(st1.mtimeMs), Number(st1.ctimeMs)) + 100)

      const after = readTranscript(root, true)
      assert.equal(after.steps.length, 221)
      assert.ok(after.steps[5].content.startsWith('EXPANDED_PREFIX_REWRITE-'))
      assert.notEqual(after.revision, before.revision)
    }

    // 2. Same-length in-place edit outside tail anchor + append on >64KB file -> caught by deferred idle verify.
    {
      const { root, file } = makeBrainDir(t)
      const rows = Array.from({ length: 220 }, (_, i) => line(makeStep(i, `ORIG-${String(i).padStart(3, '0')}-${pad}`)))
      writeFileSync(file, rows.join(''))
      const st0 = fs.statSync(file, { bigint: true })
      assert.ok(Number(st0.size) > 100 * 1024)
      const meter = installReadMeter(t, root)
      const clock = installFakeNow(t, Math.max(Number(st0.mtimeMs), Number(st0.ctimeMs)) + 100)

      const before = readTranscript(root, true)
      assert.equal(before.steps[50].content, `ORIG-050-${pad}`)

      // In-place overwrite at step 50 (far outside 4KB tail anchor) + append step 220.
      const offset50 = Buffer.byteLength(rows.slice(0, 50).join(''))
      const edited50 = Buffer.from(line(makeStep(50, `EDIT-050-${pad}`)))
      assert.equal(edited50.length, Buffer.byteLength(rows[50]))
      const fd = openSync(file, 'r+')
      try {
        writeSync(fd, edited50, 0, edited50.length, offset50)
      } finally {
        closeSync(fd)
      }
      appendFileSync(file, line(makeStep(220, `TAIL-220-${pad}`)))
      const st1 = fs.statSync(file, { bigint: true })
      const t1 = Math.max(Number(st1.mtimeMs), Number(st1.ctimeMs))
      clock.set(t1 + 100)

      // Immediate read takes O(delta) fast path.
      meter.reset()
      const fast = readTranscript(root, true)
      assert.equal(fast.steps.length, 221)
      assert.equal(fast.steps[220].content, `TAIL-220-${pad}`)
      assert.ok(meter.stats.bytesRead < 8192, `fast append read ${meter.stats.bytesRead} bytes`)

      // Once file settles past deferred idle verify threshold (10s), deferred full verify catches the edit.
      clock.set(t1 + 11_000)
      const healed = readTranscript(root, true)
      assert.equal(healed.steps.length, 221)
      assert.equal(healed.steps[50].content, `EDIT-050-${pad}`, 'deferred verification must detect mid-file in-place edit')
      assert.notEqual(healed.revision, fast.revision)

      // After verification completes, subsequent unchanged reads return to 0 bytes read.
      meter.reset()
      const settledHit = readTranscript(root, true)
      assert.equal(settledHit, healed)
      assert.equal(meter.stats.bytesRead, 0)
    }

    // 3. Same-length in-place edit outside tail anchor + continuous appends -> caught by periodic append verification.
    {
      const { root, file } = makeBrainDir(t)
      const rows = Array.from({ length: 220 }, (_, i) => line(makeStep(i, `ORIG-${String(i).padStart(3, '0')}-${pad}`)))
      writeFileSync(file, rows.join(''))
      const st0 = fs.statSync(file, { bigint: true })
      const clock = installFakeNow(t, Math.max(Number(st0.mtimeMs), Number(st0.ctimeMs)) + 100)

      const before = readTranscript(root, true)
      const offset40 = Buffer.byteLength(rows.slice(0, 40).join(''))
      const edited40 = Buffer.from(line(makeStep(40, `EDIT-040-${pad}`)))
      assert.equal(edited40.length, Buffer.byteLength(rows[40]))
      const fd = openSync(file, 'r+')
      try {
        writeSync(fd, edited40, 0, edited40.length, offset40)
      } finally {
        closeSync(fd)
      }

      // Append across the periodic verification boundary (32 fast appends).
      let latest = before
      for (let i = 220; i < 253; i++) {
        appendFileSync(file, line(makeStep(i, `APPEND-${i}`)))
        const st = fs.statSync(file, { bigint: true })
        clock.set(Math.max(Number(st.mtimeMs), Number(st.ctimeMs)) + 100)
        latest = readTranscript(root, true)
      }
      assert.equal(latest.steps[40].content, `EDIT-040-${pad}`, 'periodic verification during growth must detect mid-file in-place edit')
      assert.notEqual(latest.revision, before.revision)
    }
  })

  it('backs off watched polling when idle, stays responsive on activity/nativeWorking, reads zero bytes when unchanged, and cleans up timers', async (t) => {
    const home = mkdtempSync(join(tmpdir(), 'agy-watch-backoff-'))
    const agyDir = join(home, 'agy')
    const rawId = 'aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee'
    const sid = `agy-${rawId}`
    const logs = join(agyDir, 'brain', rawId, '.system_generated', 'logs')
    mkdirSync(logs, { recursive: true })
    const file = join(logs, 'transcript_full.jsonl')
    const pad = 'w'.repeat(400)
    const initial = Array.from({ length: 200 }, (_, i) => line(makeStep(i, `watch-${i}-${pad}`)))
    writeFileSync(file, initial.join(''))
    const st0 = fs.statSync(file, { bigint: true })

    const db = new DatabaseSync(join(agyDir, 'conversation_summaries.db'))
    db.exec('CREATE TABLE conversation_summaries (conversation_id TEXT PRIMARY KEY, status TEXT, not_fully_idle INTEGER, killed INTEGER, step_count INTEGER, title TEXT, workspace_uris TEXT, last_modified_time TEXT)')
    const setSummary = (status, stepCount) => {
      db.prepare('INSERT OR REPLACE INTO conversation_summaries VALUES (?, ?, ?, ?, ?, ?, ?, ?)')
        .run(rawId, status, 0, 0, stepCount, 'Watch Backoff', JSON.stringify([`file://${home}`]), '2026-11-26T00:00:00Z')
    }
    setSummary('CASCADE_RUN_STATUS_IDLE', 200)

    // Deterministic watchTimer DI: zero real sleep.
    let nextId = 1
    const scheduled = []
    const activeTimers = new Map()
    const watchTimer = {
      set(fn, ms) {
        const id = nextId++
        const entry = { id, fn, ms }
        scheduled.push(ms)
        activeTimers.set(id, entry)
        return id
      },
      clear(id) {
        activeTimers.delete(id)
      },
    }
    const fireLatest = () => {
      assert.equal(activeTimers.size, 1, `expected exactly 1 active watch timer, got ${activeTimers.size}`)
      const [entry] = activeTimers.values()
      activeTimers.delete(entry.id)
      entry.fn()
    }

    const meter = installReadMeter(t, logs)
    const clock = installFakeNow(t, Math.max(Number(st0.mtimeMs), Number(st0.ctimeMs)) + 100)
    const node = await startFakeNode(t)
    const runtime = createAgyRuntime({
      home,
      agyDir,
      agyBin: join(home, 'missing-agy'),
      socketPath: node.path,
      watchPollMs: 400,
      watchIdleMaxMs: 2000,
      watchTimer,
      log: () => {},
    })
    t.after(async () => {
      await runtime.stop()
      db.close()
      rmSync(home, { recursive: true, force: true })
    })
    runtime.start()
    await node.ready
    await until('watched session listed', () => node.last('runtime.sessions')?.sessions?.some((s) => s.session_id === sid))

    await node.peer.call('session.watch', { session_id: sid })
    assert.deepEqual(scheduled, [400], 'initial watch must start at active interval (400ms)')

    // Quiet idle ticks must back off exponentially up to watchIdleMaxMs (2000ms) and read 0 transcript bytes.
    meter.reset()
    fireLatest()
    fireLatest()
    fireLatest()
    fireLatest()
    assert.deepEqual(scheduled, [400, 800, 1600, 2000, 2000], 'idle watched session must back off 400->800->1600->2000')
    assert.equal(meter.stats.bytesRead, 0, 'idle watched ticks must read 0 transcript bytes')

    // Appending a new step snaps delay back to 400ms and reads only delta.
    const appendedLine = line(makeStep(200, 'external-update'))
    appendFileSync(file, appendedLine)
    const st1 = fs.statSync(file, { bigint: true })
    clock.set(Math.max(Number(st1.mtimeMs), Number(st1.ctimeMs)) + 100)
    meter.reset()
    fireLatest()
    assert.equal(scheduled.at(-1), 400, 'new transcript append must reset poll interval to 400ms')
    assert.ok(meter.stats.bytesRead > 0 && meter.stats.bytesRead <= 4096 + Buffer.byteLength(appendedLine) + 128)

    // External running status (nativeWorking) keeps poll responsive at 400ms even before new lines land.
    setSummary('CASCADE_RUN_STATUS_RUNNING', 201)
    meter.reset()
    fireLatest()
    fireLatest()
    assert.deepEqual(scheduled.slice(-2), [400, 400], 'nativeWorking session must stay at 400ms')
    assert.equal(meter.stats.bytesRead, 0, 'unchanged nativeWorking ticks must still read 0 bytes')

    // Returning to idle resumes backoff.
    setSummary('CASCADE_RUN_STATUS_IDLE', 201)
    fireLatest()
    fireLatest()
    assert.deepEqual(scheduled.slice(-2), [800, 1600])

    // Unwatch clears timer completely.
    await node.peer.call('session.unwatch', { session_id: sid })
    assert.equal(activeTimers.size, 0, 'unwatch must clear active poll timer')

    // Active live Persona turn must short-circuit transcriptOf without disk reads and bump watch backoff upon completion.
    const fakeAcp = join(home, 'fake-acp.mjs')
    writeFileSync(fakeAcp, `
import { existsSync, watch, writeFileSync } from 'node:fs'
import { createInterface } from 'node:readline'
if (process.argv.includes('models')) { console.log('test-model\\tTest Model'); process.exit(0) }
const dir = process.argv[2]
const send = (m) => process.stdout.write(JSON.stringify({ jsonrpc: '2.0', ...m }) + '\\n')
const waitRelease = () => new Promise((resolve) => {
  if (existsSync(dir + '/release')) return resolve()
  const w = watch(dir, () => { if (existsSync(dir + '/release')) { w.close(); resolve() } })
})
createInterface({ input: process.stdin }).on('line', async (line) => {
  const req = JSON.parse(line)
  if (req.id === undefined) return
  if (req.method === 'session/prompt') {
    const sid = req.params.sessionId
    send({ method: 'session/update', params: { sessionId: sid, update: { sessionUpdate: 'tool_call', toolCallId: 'tc1', name: 'run_command', rawInput: { CommandLine: 'echo ok' } } } })
    writeFileSync(dir + '/held', '')
    await waitRelease()
    send({ method: 'session/update', params: { sessionId: sid, update: { sessionUpdate: 'agent_message_chunk', content: { text: 'Live done.' } } } })
    send({ id: req.id, result: { stopReason: 'end_turn' } })
    return
  }
  send({ id: req.id, result: {} })
})
`)
    const liveScheduled = []
    const liveTimers = new Map()
    let liveNextId = 1
    const liveWatchTimer = {
      set(fn, ms) { const id = liveNextId++; liveScheduled.push(ms); liveTimers.set(id, { id, fn, ms }); return id },
      clear(id) { liveTimers.delete(id) },
    }
    const liveNode = await startFakeNode(t)
    const liveRt = createAgyRuntime({
      home,
      agyDir,
      agyBin: process.execPath,
      agyArgs: [fakeAcp, home],
      socketPath: liveNode.path,
      watchPollMs: 400,
      watchIdleMaxMs: 2000,
      exportPollMs: 400,
      watchTimer: liveWatchTimer,
      log: () => {},
    })
    t.after(() => liveRt.stop())
    liveRt.start()
    await liveNode.ready
    await until('live fixture listed', () => liveNode.last('runtime.sessions')?.sessions?.some((s) => s.session_id === sid))
    await liveNode.peer.call('session.watch', { session_id: sid })
    // Back off while idle before starting a turn.
    const fireLive = () => {
      const [entry] = liveTimers.values()
      liveTimers.delete(entry.id)
      entry.fn()
    }
    fireLive()
    fireLive()
    assert.equal(liveScheduled.at(-1), 1600)

    await liveNode.peer.call('session.send', { session_id: sid, text: 'Run live check' })
    await until('live prompt held', () => fs.existsSync(join(home, 'held')))
    try {
      // During active live turn, watched poll skips syncTranscriptAppend and session.items reads 0 transcript bytes on unchanged file.
      meter.reset()
      fireLive()
      assert.equal(meter.stats.bytesRead, 0, 'watched poll during live turn must read 0 transcript bytes')
      const livePage = await liveNode.peer.call('session.items', { session_id: sid, limit: 10 })
      assert.ok(livePage.items.some((it) => it.text === 'Run live check'))
      assert.equal(meter.stats.bytesRead, 0, 'live turn session.items must read 0 transcript bytes on unchanged file')
    } finally {
      writeFileSync(join(home, 'release'), '')
    }
    await until('live turn finished and bumped watch poll back to 400ms', () => liveScheduled.at(-1) === 400)
  })

  it('enforces accurate tool_calls/tool_input weights in itemsCache and bounds listSummaryBySession and volatileWakes', async (t) => {
    // 1. itemsCache must account for large tool_calls / tool_input payloads under the 64MB byte budget.
    const bigArg = (ch, len) => ({
      step_index: 0,
      source: 'MODEL',
      type: 'PLANNER_RESPONSE',
      status: 'DONE',
      created_at: '2026-11-26T00:00:00Z',
      tool_calls: [{ name: 'write_to_file', args: { TargetFile: '/tmp/a.txt', CodeContent: ch.repeat(len) } }],
    })
    // 18M chars UTF-16 = ~36MB in step.tool_calls; two entries = ~72MB > 64MB budget.
    const stepsA = [bigArg('a', 18 * 1024 * 1024)]
    const stepsB = [bigArg('b', 18 * 1024 * 1024)]
    const resA = stepsToItems(stepsA, { full: false, cacheSlot: 'tool-slot-a|0|0', cacheKey: 'tool-slot-a|0|0|h1' })
    assert.equal(stepsToItems(stepsA, { full: false, cacheSlot: 'tool-slot-a|0|0', cacheKey: 'tool-slot-a|0|0|h1' }), resA)
    const resB = stepsToItems(stepsB, { full: false, cacheSlot: 'tool-slot-b|0|0', cacheKey: 'tool-slot-b|0|0|h1' })
    assert.equal(stepsToItems(stepsB, { full: false, cacheSlot: 'tool-slot-b|0|0', cacheKey: 'tool-slot-b|0|0|h1' }), resB)
    const resAReload = stepsToItems(stepsA, { full: false, cacheSlot: 'tool-slot-a|0|0', cacheKey: 'tool-slot-a|0|0|h1' })
    assert.notEqual(resAReload, resA, 'itemsCache must evict older tool_calls-heavy entry when >64MB budget')

    // 2. listSummaryBySession enforces byte budget and keeps newest sessions hot in listSessions.
    const home = mkdtempSync(join(tmpdir(), 'agy-summary-bound-'))
    const agyDir = join(home, 'agy')
    const ids = [
      '11111111-1111-4111-8111-111111111111',
      '22222222-2222-4222-8222-222222222222',
      '33333333-3333-4333-8333-333333333333',
      '44444444-4444-4444-8444-444444444444',
    ]
    const bigText = 's'.repeat(12_000) // ~24KB UTF-16 per step
    const baseTime = Date.now() - 60_000
    for (let idx = 0; idx < ids.length; idx++) {
      const logs = join(agyDir, 'brain', ids[idx], '.system_generated', 'logs')
      mkdirSync(logs, { recursive: true })
      const f = join(logs, 'transcript_full.jsonl')
      writeFileSync(f, line(makeStep(0, `prompt-${idx}`)) + line(makeStep(1, `reply-${idx}-${bigText}`)))
      const ts = new Date(baseTime + idx * 5000)
      utimesSync(f, ts, ts)
    }
    const node = await startFakeNode(t)
    const runtime = createAgyRuntime({
      home,
      agyDir,
      agyBin: join(home, 'missing-agy'),
      socketPath: node.path,
      maxListSummaryBytes: 40 * 1024, // fits only 1 session (~25KB), must evict older sessions
      log: () => {},
    })
    t.after(async () => {
      await runtime.stop()
      rmSync(home, { recursive: true, force: true })
    })
    runtime.start()
    await node.ready
    await until('all 4 sessions listed', () => node.last('runtime.sessions')?.sessions?.length === 4)
    const sessions = node.last('runtime.sessions').sessions
    assert.equal(sessions[0].session_id, `agy-${ids[3]}`, 'newest session stays first in listSessions output')
    assert.equal(sessions[3].session_id, `agy-${ids[0]}`, 'oldest session stays last in listSessions output')
    // Verify the runtime reports bounded listSummaryBySession bytes via clean close/stop and re-list.
    assert.ok(runtime._debugStats, 'expected bounded cache debug stats for deterministic assertion')
    assert.ok(runtime._debugStats().listSummaryBytes <= 40 * 1024, `listSummaryBytes exceeded cap: ${runtime._debugStats().listSummaryBytes}`)
    assert.equal(runtime._debugStats().listSummaryCount, 1, 'only 1 session fits under 40KB listSummaryBytes cap')
    assert.equal(runtime._debugStats().listSummaryIds[0], ids[3], 'newest session must remain cached, not evicted by older sessions')

    // Closing that session clears its entry and zeroes bytes.
    await node.peer.call('session.close', { session_id: `agy-${ids[3]}` })
    assert.equal(runtime._debugStats().listSummaryBytes, 0)

    // 3. volatileWakes remains bounded when many unwritable brainDirs record observed wakes.
    const wakeSteps = [
      makeStep(0, 'hi'),
      makeStep(1, 'first reply'),
      makeStep(2, 'wake reply', { type: 'PLANNER_RESPONSE', source: 'MODEL' }),
    ]
    for (let i = 0; i < 200; i++) {
      rememberObservedWake(join(home, 'nonexistent-brain', `dir-${i}`), wakeSteps, 2)
    }
  })

  it('caches 130k-step single transcript and two alternating transcripts under 64MiB raw with zero unchanged reads while still enforcing the object-weight ceiling', (t) => {
    const writeBulkLines = (file, count, makeLine) => {
      const fd = openSync(file, 'w')
      try {
        let buf = ''
        for (let i = 0; i < count; i++) {
          buf += makeLine(i)
          if (buf.length >= 1 << 20) {
            writeSync(fd, buf)
            buf = ''
          }
        }
        if (buf) writeSync(fd, buf)
      } finally {
        closeSync(fd)
      }
    }
    const touchMsOf = (file) => {
      const st = fs.statSync(file, { bigint: true })
      return { size: Number(st.size), touchMs: Math.max(Number(st.mtimeMs), Number(st.ctimeMs)) }
    }

    // 1. Single high-step transcript (130k steps, ~36.4MB raw < 64MiB): unchanged polls read 0 B and appends stay O(delta).
    {
      const { root, file } = makeBrainDir(t)
      const body = 'y'.repeat(150)
      writeBulkLines(file, 130_000, (i) => line(makeStep(i, body)))
      const { size, touchMs } = touchMsOf(file)
      assert.ok(size > 34 * 1024 * 1024 && size < 64 * 1024 * 1024, `expected ~36.4MB raw fixture under 64MiB, got ${size}`)

      const meter = installReadMeter(t, root)
      const clock = installFakeNow(t, touchMs + 5000)

      meter.reset()
      const cold = readTranscript(root, true)
      assert.equal(cold.steps.length, 130_000)
      assert.equal(meter.stats.bytesRead, size)

      meter.reset()
      for (let k = 0; k < 5; k++) {
        clock.advance(400)
        const hit = readTranscript(root, true)
        assert.equal(hit, cold, '130k-step transcript under 64MiB raw must remain cached')
      }
      assert.equal(meter.stats.bytesRead, 0, 'unchanged polls on 130k-step transcript must read 0 bytes')
      assert.equal(meter.stats.opens, 0)
      assert.equal(meter.stats.readFiles, 0)

      // Incremental append on the same high-step transcript stays on the O(anchor + delta) fast path.
      const added = line(makeStep(130_000, 'tail-delta'))
      appendFileSync(file, added)
      const afterMeta = touchMsOf(file)
      clock.set(afterMeta.touchMs + 100)
      meter.reset()
      const next = readTranscript(root, true)
      assert.equal(next.steps.length, 130_001)
      assert.equal(next.steps[0], cold.steps[0])
      assert.equal(next.steps[130_000].content, 'tail-delta')
      assert.ok(
        meter.stats.bytesRead <= 4096 + Buffer.byteLength(added) + 128,
        `append on 130k-step transcript read ${meter.stats.bytesRead} bytes`,
      )
    }

    // 2. Two alternating high-step transcripts (~56MB raw total < 64MiB): both stay cached with 0 bytes read.
    {
      const pairRoot = mkdtempSync(join(tmpdir(), 'agy-perf-pair-'))
      t.after(() => rmSync(pairRoot, { recursive: true, force: true }))
      const makeChild = (name) => {
        const root = join(pairRoot, name)
        const logs = join(root, '.system_generated', 'logs')
        mkdirSync(logs, { recursive: true })
        return { root, file: join(logs, 'transcript_full.jsonl') }
      }
      const a = makeChild('a')
      const b = makeChild('b')
      const body = 'y'.repeat(150)
      writeBulkLines(a.file, 100_000, (i) => line(makeStep(i, body)))
      writeBulkLines(b.file, 100_000, (i) => line(makeStep(i, body)))
      const metaA = touchMsOf(a.file)
      const metaB = touchMsOf(b.file)
      const totalRaw = metaA.size + metaB.size
      assert.ok(totalRaw > 50 * 1024 * 1024 && totalRaw < 64 * 1024 * 1024, `expected ~56MB raw pair under 64MiB, got ${totalRaw}`)

      const meter = installReadMeter(t, pairRoot)
      const clock = installFakeNow(t, Math.max(metaA.touchMs, metaB.touchMs) + 5000)

      const coldA = readTranscript(a.root, true)
      const coldB = readTranscript(b.root, true)
      assert.equal(coldA.steps.length, 100_000)
      assert.equal(coldB.steps.length, 100_000)

      meter.reset()
      for (let round = 0; round < 3; round++) {
        clock.advance(1000)
        assert.equal(readTranscript(a.root, true), coldA, `round ${round}: transcript A must not thrash`)
        clock.advance(1000)
        assert.equal(readTranscript(b.root, true), coldB, `round ${round}: transcript B must not thrash`)
      }
      assert.equal(meter.stats.bytesRead, 0, 'alternating reads on 56MB raw pair must read 0 bytes')
      assert.equal(meter.stats.opens, 0)
      assert.equal(meter.stats.readFiles, 0)
    }

    // 3. Object-weight hard upper bound (256MiB) still bounds degenerate low-byte/high-step transcripts.
    {
      const boundRoot = mkdtempSync(join(tmpdir(), 'agy-perf-weight-'))
      t.after(() => rmSync(boundRoot, { recursive: true, force: true }))
      const makeChild = (name) => {
        const root = join(boundRoot, name)
        const logs = join(root, '.system_generated', 'logs')
        mkdirSync(logs, { recursive: true })
        return { root, file: join(logs, 'transcript_full.jsonl') }
      }
      // Two 550k-line "{}\n" files: ~1.65MB raw each (3.3MB total << 64MiB), but ~136MiB weight each (>256MiB combined).
      const d1 = makeChild('d1')
      const d2 = makeChild('d2')
      const tinyBlock = '{}\n'.repeat(10_000)
      writeBulkLines(d1.file, 55, () => tinyBlock)
      writeBulkLines(d2.file, 55, () => tinyBlock)
      const meta1 = touchMsOf(d1.file)
      const meta2 = touchMsOf(d2.file)
      assert.ok(meta1.size + meta2.size < 4 * 1024 * 1024)

      const meter = installReadMeter(t, boundRoot)
      const clock = installFakeNow(t, Math.max(meta1.touchMs, meta2.touchMs) + 5000)

      const first1 = readTranscript(d1.root, true)
      assert.equal(first1.steps.length, 550_000)
      meter.reset()
      assert.equal(readTranscript(d1.root, true), first1, 'single 136MiB-weight transcript fits under 256MiB object budget')
      assert.equal(meter.stats.bytesRead, 0)

      // Loading d2 (~136MiB weight) pushes combined weight past 256MiB and must evict d1 even though raw total is 3.3MB.
      const first2 = readTranscript(d2.root, true)
      assert.equal(first2.steps.length, 550_000)
      meter.reset()
      assert.equal(readTranscript(d2.root, true), first2, 'newer 136MiB-weight transcript stays cached')
      assert.equal(meter.stats.bytesRead, 0)

      meter.reset()
      const reloaded1 = readTranscript(d1.root, true)
      assert.notEqual(reloaded1, first1, 'older degenerate transcript must be evicted by 256MiB object-weight budget')
      assert.equal(meter.stats.bytesRead, meta1.size)

      // A single degenerate file >256MiB weight (1.06M "{}\n" lines = 3.18MB raw << 64MiB) must be refused admission.
      const huge = makeChild('huge')
      writeBulkLines(huge.file, 106, () => tinyBlock)
      const metaHuge = touchMsOf(huge.file)
      assert.ok(metaHuge.size < 4 * 1024 * 1024)
      clock.set(metaHuge.touchMs + 5000)

      const h1 = readTranscript(huge.root, true)
      assert.equal(h1.steps.length, 1_060_000)
      meter.reset()
      const h2 = readTranscript(huge.root, true)
      assert.notEqual(h2, h1, 'single degenerate transcript exceeding 256MiB object-weight cap must not be admitted')
      assert.equal(meter.stats.bytesRead, metaHuge.size)
    }
  })
})
