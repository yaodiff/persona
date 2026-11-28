import assert from 'node:assert/strict'
import { mkdirSync, mkdtempSync, rmSync, utimesSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { createAgyRuntime } from '../lib/runtime.js'
import { readTranscriptAsync, stepsToItems, stepsToItemsAsync } from '../lib/map.js'

function makeLargeTranscript(agyDir, rawId, { targetBytes = 11 * 1024 * 1024, stepCount = 260, settled = true } = {}) {
  const brainDir = join(agyDir, 'brain', rawId)
  const logs = join(brainDir, '.system_generated', 'logs')
  mkdirSync(logs, { recursive: true })
  const pairs = Math.max(1, Math.ceil(stepCount / 2))
  const padSize = Math.max(256, Math.floor(targetBytes / pairs))
  const pad = 'x'.repeat(padSize)
  const lines = []
  let idx = 0
  for (let i = 0; i < stepCount; i += 2) {
    lines.push(JSON.stringify({
      step_index: idx++,
      type: 'USER_INPUT',
      source: 'USER_EXPLICIT',
      status: 'DONE',
      created_at: `2026-11-28T00:${String(Math.floor(i / 60)).padStart(2, '0')}:${String(i % 60).padStart(2, '0')}Z`,
      content: `<USER_REQUEST>\nPrompt ${i} for ${rawId}\n</USER_REQUEST>`,
    }))
    lines.push(JSON.stringify({
      step_index: idx++,
      type: 'PLANNER_RESPONSE',
      status: 'DONE',
      created_at: `2026-11-28T00:${String(Math.floor((i + 1) / 60)).padStart(2, '0')}:${String((i + 1) % 60).padStart(2, '0')}Z`,
      content: `Reply ${i} for ${rawId} ${pad}`,
    }))
  }
  const file = join(logs, 'transcript_full.jsonl')
  writeFileSync(file, lines.join('\n') + '\n')
  if (settled) {
    const past = new Date(Date.now() - 10_000)
    utimesSync(file, past, past)
  }
  return { brainDir, file, stepCount: idx }
}

test('watched >10MB session survives cold listSessions scans over >64MiB, reuses snapshot in session.item, and unprotects on unwatch', async (t) => {
  const home = mkdtempSync(join(tmpdir(), 'agy-cache-stall-'))
  t.after(() => rmSync(home, { recursive: true, force: true }))
  const agyDir = join(home, '.gemini', 'antigravity-cli')

  const hotId = '11111111-1111-4111-8111-111111111111'
  const hot = makeLargeTranscript(agyDir, hotId, { targetBytes: 11 * 1024 * 1024, stepCount: 220 })
  const coldIds = [
    '22222222-2222-4222-8222-222222222221',
    '22222222-2222-4222-8222-222222222222',
    '22222222-2222-4222-8222-222222222223',
    '22222222-2222-4222-8222-222222222224',
    '22222222-2222-4222-8222-222222222225',
    '22222222-2222-4222-8222-222222222226',
  ]

  const node = await startFakeNode(t)
  const runtime = createAgyRuntime({ agyBin: join(home, 'missing-agy'), agyDir, home, socketPath: node.path, log: () => {} })
  t.after(() => runtime.stop())
  runtime.start()
  await node.ready

  const hotSid = `agy-${hotId}`
  await node.peer.call('session.watch', { session_id: hotSid })
  const warmPage = await node.peer.call('session.items', { session_id: hotSid, limit: 20 })
  assert.equal(warmPage.items.length, 20)
  const hotBefore = await readTranscriptAsync(hot.brainDir, true)
  assert.equal(hotBefore.steps.length, hot.stepCount)

  // Now create 6 cold >11MB transcripts (>66MB total, exceeding 64MiB cache budget) and trigger listSessions scan
  for (const cid of coldIds) {
    makeLargeTranscript(agyDir, cid, { targetBytes: 11 * 1024 * 1024, stepCount: 220 })
  }
  const beforeList = node.all('runtime.sessions').length
  await node.peer.call('session.rename', { session_id: hotSid, title: 'Hot Session' })
  await until('cold listSessions scanned all 7 sessions', () => node.all('runtime.sessions').length > beforeList && (node.last('runtime.sessions')?.sessions?.length ?? 0) >= 1 + coldIds.length, 15_000)

  // Watched hot session must remain in transcriptCache (exact same cached steps reference, no cold reread)
  const hotAfter = await readTranscriptAsync(hot.brainDir, true)
  assert.ok(hotAfter.steps === hotBefore.steps, 'watched hot transcript must not be evicted by cold listSessions scan')

  // Warm items() and non-truncated session.item() should serve consistent results from cache
  const warmPage2 = await node.peer.call('session.items', { session_id: hotSid, limit: 20 })
  assert.deepEqual(warmPage2.items.map((it) => it.id), warmPage.items.map((it) => it.id))
  const userItem = warmPage2.items.find((it) => it.role === 'user' && !it.truncated)
  assert.ok(userItem, 'expected a non-truncated user item')
  const fetchedItem = await node.peer.call('session.item', { session_id: hotSid, item_id: userItem.id })
  assert.deepEqual(fetchedItem, userItem)

  // Unwatch releases protection so cache remains strictly bounded under >64MiB of new watched sessions
  await node.peer.call('session.unwatch', { session_id: hotSid })
  for (const cid of coldIds) {
    await node.peer.call('session.watch', { session_id: `agy-${cid}` })
    await node.peer.call('session.items', { session_id: `agy-${cid}`, limit: 5 })
  }
  const hotEvicted = await readTranscriptAsync(hot.brainDir, true)
  assert.ok(hotEvicted.steps !== hotBefore.steps, 'unwatched session should be evictable once >64MiB of other watched sessions fill the bounded cache')
  for (const cid of coldIds) {
    await node.peer.call('session.unwatch', { session_id: `agy-${cid}` })
  }
})

test('cold >12MB transcript read and mapping yields event loop so concurrent session.unwatch is not head-of-line blocked', async (t) => {
  const home = mkdtempSync(join(tmpdir(), 'agy-el-yield-'))
  t.after(() => rmSync(home, { recursive: true, force: true }))
  const agyDir = join(home, '.gemini', 'antigravity-cli')

  const watchId = '33333333-3333-4333-8333-333333333331'
  const coldId = '33333333-3333-4333-8333-333333333332'
  makeLargeTranscript(agyDir, watchId, { targetBytes: 64 * 1024, stepCount: 10 })
  const cold = makeLargeTranscript(agyDir, coldId, { targetBytes: 14 * 1024 * 1024, stepCount: 2400 })

  // Verify stepsToItemsAsync matches sync stepsToItems identically
  const coldTr = await readTranscriptAsync(cold.brainDir, true, { cold: true })
  const syncRes = stepsToItems(coldTr.steps, { brainDir: cold.brainDir, full: false, includeMedia: true, skipCache: true })
  const asyncRes = await stepsToItemsAsync(coldTr.steps, { brainDir: cold.brainDir, full: false, includeMedia: true, skipCache: true })
  assert.equal(asyncRes.items.length, syncRes.items.length)
  assert.equal(asyncRes.toSeq, syncRes.toSeq)

  // Fresh cold session not yet in cache for runtime RPC test
  const coldId2 = '33333333-3333-4333-8333-333333333333'
  makeLargeTranscript(agyDir, coldId2, { targetBytes: 14 * 1024 * 1024, stepCount: 2400 })

  const node = await startFakeNode(t)
  const runtime = createAgyRuntime({ agyBin: join(home, 'missing-agy'), agyDir, home, socketPath: node.path, log: () => {} })
  t.after(() => runtime.stop())
  runtime.start()
  await node.ready

  const watchSid = `agy-${watchId}`
  const coldSid2 = `agy-${coldId2}`
  await node.peer.call('session.watch', { session_id: watchSid })

  let ticksDuringCold = 0
  let coldDone = false
  const ticker = () => {
    if (coldDone) return
    ticksDuringCold++
    setImmediate(ticker)
  }
  setImmediate(ticker)

  let unwatchFinishedBeforeCold = false
  const coldItemsPromise = node.peer.call('session.items', { session_id: coldSid2, limit: 20 }).then((res) => {
    coldDone = true
    return res
  })
  const coldItemsCoalesced = node.peer.call('session.items', { session_id: coldSid2, limit: 10 })
  const unwatchPromise = node.peer.call('session.unwatch', { session_id: watchSid }).then((res) => {
    unwatchFinishedBeforeCold = !coldDone
    return res
  })

  const [itemsRes, coalescedRes, unwatchRes] = await Promise.all([coldItemsPromise, coldItemsCoalesced, unwatchPromise])
  assert.equal(unwatchRes.ok, true)
  assert.equal(itemsRes.items.length, 20)
  assert.equal(coalescedRes.items.length, 10)
  assert.equal(unwatchFinishedBeforeCold, true, 'session.unwatch must complete before cold >12MB session.items finishes')
  assert.ok(ticksDuringCold >= 2, `expected event loop to yield during cold >12MB load (saw ${ticksDuringCold} ticks)`)
})
