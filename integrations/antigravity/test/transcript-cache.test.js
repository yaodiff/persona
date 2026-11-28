import assert from 'node:assert/strict'
import { appendFileSync, mkdirSync, mkdtempSync, renameSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { describe, it } from 'node:test'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { createAgyRuntime } from '../lib/runtime.js'
import {
  readTranscript,
  rememberObservedWake,
  stepsMaxSeq,
  stepsToItems,
} from '../lib/map.js'

function makeBrainDir() {
  const root = mkdtempSync(join(tmpdir(), 'agy-cache-'))
  const logs = join(root, '.system_generated', 'logs')
  mkdirSync(logs, { recursive: true })
  return { root, logs, file: join(logs, 'transcript_full.jsonl') }
}

function line(step) {
  return JSON.stringify(step) + '\n'
}

describe('incremental transcript caching and stepsToItems prefix reuse', () => {
  it('caches active (!settled) transcripts and detects same-size fast rewrites via SHA-256', () => {
    const { root, file } = makeBrainDir()
    try {
      const s0 = { step_index: 0, type: 'USER_INPUT', source: 'USER_EXPLICIT', created_at: '2026-11-21T00:00:00Z', content: 'hello-aaa' }
      const s1 = { step_index: 1, type: 'PLANNER_RESPONSE', status: 'DONE', created_at: '2026-11-21T00:00:01Z', content: 'reply-aaa' }
      writeFileSync(file, line(s0) + line(s1))

      const first = readTranscript(root, true)
      const second = readTranscript(root, true)
      assert.equal(first.steps.length, 2)
      // Active (!settled) unchanged read hits cache and preserves object identity.
      assert.equal(second, first)
      assert.equal(second.steps[0], first.steps[0])

      // Same-size fast rewrite on the same inode within the 1s settle window must be detected.
      const s1b = { step_index: 1, type: 'PLANNER_RESPONSE', status: 'DONE', created_at: '2026-11-21T00:00:01Z', content: 'reply-bbb' }
      const rewritten = line(s0) + line(s1b)
      assert.equal(Buffer.byteLength(rewritten), Buffer.byteLength(line(s0) + line(s1)))
      writeFileSync(file, rewritten)

      const third = readTranscript(root, true)
      assert.notEqual(third, first)
      assert.equal(third.steps[1].content, 'reply-bbb')
      assert.ok(third.revision, 'same-size rewrite must advance revision')
    } finally {
      rmSync(root, { recursive: true, force: true })
    }
  })

  it('incrementally parses appended lines, partial trailing lines, rewinds, late arrivals, and wake receipts', () => {
    const { root, file } = makeBrainDir()
    try {
      const s0 = { step_index: 0, type: 'USER_INPUT', source: 'USER_EXPLICIT', created_at: '2026-11-21T00:00:00Z', content: 'start' }
      const s1 = { step_index: 1, type: 'PLANNER_RESPONSE', status: 'DONE', created_at: '2026-11-21T00:00:01Z', content: 'done one' }
      writeFileSync(file, line(s0) + line(s1))

      const t1 = readTranscript(root, true)
      assert.equal(t1.steps.length, 2)

      // Append a partial line without trailing newline first, then complete it.
      const s2Raw = JSON.stringify({ step_index: 2, type: 'PLANNER_RESPONSE', status: 'DONE', created_at: '2026-11-21T00:00:02Z', content: 'background update' })
      appendFileSync(file, s2Raw.slice(0, 20))
      const tPartial = readTranscript(root, true)
      assert.equal(tPartial.steps.length, 2)
      assert.equal(tPartial.steps[0], t1.steps[0])
      assert.equal(tPartial.steps[1], t1.steps[1])

      appendFileSync(file, s2Raw.slice(20) + '\n')
      const t2 = readTranscript(root, true)
      assert.equal(t2.steps.length, 3)
      // Earlier parsed steps kept by object identity via incremental append.
      assert.equal(t2.steps[0], t1.steps[0])
      assert.equal(t2.steps[1], t1.steps[1])
      assert.equal(t2.steps[2].content, 'background update')

      // Adding a wake receipt after caching updates revision and emits wake notice without mutating cached base steps.
      rememberObservedWake(root, t2.steps, 2)
      const tWake = readTranscript(root, true)
      assert.match(tWake.revision, /:wake:/)
      const wakeItems = stepsToItems(tWake.steps)
      assert.ok(wakeItems.items.some((it) => it.wake === true), 'expected wake notice item')

      // Appended late-arriving step (index 3 skipped, 4 arrives, then 3 arrives).
      const s4 = { step_index: 4, type: 'GENERIC', status: 'DONE', created_at: '2026-11-21T00:00:04Z', content: 'second result' }
      appendFileSync(file, line(s4))
      const t4 = readTranscript(root, true)
      assert.equal(t4.steps.at(-1).step_index, 4)

      const s3Late = { step_index: 3, type: 'GENERIC', status: 'DONE', created_at: '2026-11-21T00:00:03Z', content: 'first result' }
      appendFileSync(file, line(s3Late))
      const tLate = readTranscript(root, true)
      assert.deepEqual(tLate.steps.map((s) => s.step_index), [0, 1, 2, 3, 4])
      assert.ok(tLate.revision, 'late arrival must update revision')

      // Appended rewind replacement branch.
      const s1Rewind = { step_index: 1, type: 'PLANNER_RESPONSE', status: 'DONE', created_at: '2026-11-21T00:00:10Z', content: 'rewound reply' }
      appendFileSync(file, line(s1Rewind))
      const tRewind = readTranscript(root, true)
      assert.deepEqual(tRewind.steps.map((s) => s.step_index), [0, 1])
      assert.equal(tRewind.steps[1].content, 'rewound reply')
      assert.ok(tRewind.branch, 'rewind replacement must establish a new branch')
    } finally {
      rmSync(root, { recursive: true, force: true })
    }
  })

  it('falls back to full parse on truncation and atomic inode replacement', () => {
    const { root, logs, file } = makeBrainDir()
    try {
      const s0 = { step_index: 0, type: 'USER_INPUT', source: 'USER_EXPLICIT', created_at: '2026-11-21T00:00:00Z', content: 'first' }
      const s1 = { step_index: 1, type: 'PLANNER_RESPONSE', status: 'DONE', created_at: '2026-11-21T00:00:01Z', content: 'second' }
      writeFileSync(file, line(s0) + line(s1))
      const t1 = readTranscript(root, true)
      assert.equal(t1.steps.length, 2)

      // Truncate to a single line.
      writeFileSync(file, line(s0))
      const tTrunc = readTranscript(root, true)
      assert.equal(tTrunc.steps.length, 1)
      assert.ok(tTrunc.revision)

      // Atomic replace via rename (new inode).
      const tmp = join(logs, 'transcript_full.jsonl.tmp')
      const s0New = { step_index: 0, type: 'USER_INPUT', source: 'USER_EXPLICIT', created_at: '2026-11-21T00:00:05Z', content: 'replaced-via-rename' }
      writeFileSync(tmp, line(s0New))
      renameSync(tmp, file)
      const tRenamed = readTranscript(root, true)
      assert.equal(tRenamed.steps.length, 1)
      assert.equal(tRenamed.steps[0].content, 'replaced-via-rename')
    } finally {
      rmSync(root, { recursive: true, force: true })
    }
  })

  it('caches stepsToItems, reuses immutable prefixes on append, and never serves stale in-place live mutations', () => {
    const s0 = { step_index: 0, type: 'USER_INPUT', source: 'USER_EXPLICIT', created_at: '2026-11-21T00:00:00Z', content: 'hi' }
    const s1 = { step_index: 1, type: 'PLANNER_RESPONSE', status: 'DONE', created_at: '2026-11-21T00:00:01Z', content: 'hello' }
    const s2 = { step_index: 2, type: 'PLANNER_RESPONSE', status: 'RUNNING', created_at: '2026-11-21T00:00:02Z', content: 'streaming...' }

    const cacheKey = 'test-bucket|v1|0|1|0'
    const r1 = stepsToItems([s0, s1], { cacheKey, immutablePrefix: 2 })
    const r2 = stepsToItems([s0, s1], { cacheKey, immutablePrefix: 2 })
    assert.equal(r2, r1, 'exact immutable cache hit should return the same result object')
    assert.equal(stepsMaxSeq([s0, s1]), r1.toSeq)

    // Append a live mutable step (immutablePrefix stays 2).
    const rLive1 = stepsToItems([s0, s1, s2], { cacheKey, immutablePrefix: 2 })
    assert.equal(rLive1.items.length, 3)
    assert.equal(rLive1.items[0], r1.items[0], 'immutable prefix item should be reused')
    assert.equal(rLive1.items[1], r1.items[1], 'immutable prefix item should be reused')
    assert.equal(rLive1.items[2].text, 'streaming...')

    // Mutate the live step in place: must NOT return stale cached result.
    s2.content = 'streaming... done!'
    const rLive2 = stepsToItems([s0, s1, s2], { cacheKey, immutablePrefix: 2 })
    assert.equal(rLive2.items[0], r1.items[0], 'immutable prefix item should still be reused')
    assert.equal(rLive2.items[2].text, 'streaming... done!')
    assert.equal(stepsMaxSeq([s0, s1, s2]), rLive2.toSeq)
  })

  it('coalesces concurrent items() and cursor() requests and computes cursor() without stepsToItems', async (t) => {
    const home = mkdtempSync(join(tmpdir(), 'agy-rt-cache-'))
    const agyDir = join(home, '.gemini', 'antigravity-cli')
    const rawId = '11111111-2222-3333-4444-555555555555'
    const logs = join(agyDir, 'brain', rawId, '.system_generated', 'logs')
    mkdirSync(logs, { recursive: true })
    const steps = []
    for (let i = 0; i < 6; i++) {
      steps.push(i % 2 === 0
        ? { step_index: i, type: 'USER_INPUT', source: 'USER_EXPLICIT', created_at: `2026-11-21T00:00:0${i}Z`, content: `prompt ${i}` }
        : { step_index: i, type: 'PLANNER_RESPONSE', status: 'DONE', created_at: `2026-11-21T00:00:0${i}Z`, content: `reply ${i}` })
    }
    writeFileSync(join(logs, 'transcript_full.jsonl'), steps.map(line).join(''))

    const node = await startFakeNode(t)
    const runtime = createAgyRuntime({ home, agyDir, socketPath: node.path, log: () => {} })
    t.after(async () => {
      await runtime.stop()
      rmSync(home, { recursive: true, force: true })
    })
    runtime.start()
    await node.ready
    const sid = `agy-${rawId}`
    const [w1, w2, pAll, pAfter, pLimit] = await Promise.all([
      node.peer.call('session.watch', { session_id: sid }),
      node.peer.call('session.watch', { session_id: sid }),
      node.peer.call('session.items', { session_id: sid, limit: 80 }),
      node.peer.call('session.items', { session_id: sid, after_seq: 3 }),
      node.peer.call('session.items', { session_id: sid, limit: 2 }),
    ])
    assert.equal(w1.ok, true)
    assert.equal(w2.ok, true)
    assert.equal(pAll.to_seq, 6)
    assert.equal(pAll.items.filter((it) => it.kind !== 'turn_duration').length, 6)
    assert.equal(pAll.items.filter((it) => it.kind === 'turn_duration').length, 2)
    assert.deepEqual(pAfter.items.filter((it) => it.kind !== 'turn_duration').map((it) => it.seq), [4, 5, 6])
    assert.deepEqual(pLimit.items.map((it) => it.seq), [5, 6])
    await node.peer.call('session.unwatch', { session_id: sid })
  })
})
