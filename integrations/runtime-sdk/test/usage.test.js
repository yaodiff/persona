import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createRuntime } from '../index.js'
import { startFakeNode, until } from '../testing/fake-node.js'

const driver = { listSessions: async () => [{ session_id: 's1', status: 'idle' }], items: async () => ({ items: [], to_seq: 0 }) }
const report = () => ({ session_id: 's1', ts: new Date().toISOString(), model: 'toy', request_id: 'r1', input: 10, output: 20, cache_read: 30, cache_write: 40, reasoning: 7 })

test('usage reports require negotiated support and send per-request counts without adding reasoning twice', async t => {
  const n = await startFakeNode(t, { features: ['usage'] })
  const rt = createRuntime(driver, { runtime: 'toy', socketPath: n.path })
  t.after(() => rt.stop())
  const p = report()
  assert.equal(rt.usage(p), false, 'offline reports are not replayed')
  rt.start()
  await until('listed', () => n.last('runtime.sessions'))
  assert.equal(rt.nodeHas('usage'), true)
  assert.equal(n.last('usage.report'), undefined)
  assert.equal(rt.usage({ ...p, agent: 'spoofed', launcher: 'spoofed' }), true)
  await until('usage', () => n.last('usage.report'))
  assert.deepEqual(n.last('usage.report'), p)
  for (const invalid of [{ input: -1 }, { output: 1.5 }, { reasoning: 21 }, { input: Number.MAX_SAFE_INTEGER }, { ts: 'invalid' }, { ts: '2026' }, { model: '🚀'.repeat(100) }, { session_id: '' }]) {
    assert.throws(() => rt.usage({ ...p, ...invalid }), TypeError)
  }
})

test('an older node receives no usage events', async t => {
  const n = await startFakeNode(t, { features: [] })
  const rt = createRuntime(driver, { runtime: 'toy', socketPath: n.path })
  t.after(() => rt.stop())
  rt.start()
  await until('listed', () => n.last('runtime.sessions'))
  assert.equal(rt.usage(report()), false)
  assert.equal(n.last('usage.report'), undefined)
})

const windowReport = () => ({
  provider: 'agy', name: 'Antigravity', sampled_at: '2026-11-20T23:00:00.000Z',
  windows: [{ label: '5h', window_minutes: 300, used_percent: 25, resets_at: '2026-11-21T04:00:00.000Z' }],
})

test('usage window reports are optional, validated and stored without a connected hub', async t => {
  const n = await startFakeNode(t, { features: ['usage_windows'] })
  const rt = createRuntime(driver, { runtime: 'toy', socketPath: n.path })
  t.after(() => rt.stop())
  const p = windowReport()
  assert.equal(rt.usageWindows(p), false, 'offline reports are not replayed')
  rt.start()
  await until('listed', () => n.last('runtime.sessions'))
  assert.equal(rt.nodeHas('usage_windows'), true)
  n.setHub(false)
  assert.equal(rt.usageWindows(p), true, 'the node keeps local snapshots while its hub is away')
  await until('usage windows', () => n.last('usage.windows'))
  assert.deepEqual(n.last('usage.windows'), p)
  assert.equal(rt.usageWindows({ ...p, account_key: 'acct-digest-1' }), true)
  await until('account_key usage windows', () => n.last('usage.windows')?.account_key === 'acct-digest-1')
  assert.equal(rt.usageWindows({ ...p, windows: [] }), true, 'an empty snapshot clears the previous account')
  await until('empty usage windows', () => n.last('usage.windows')?.windows?.length === 0)
  for (const invalid of [
    { provider: '' }, { name: '' }, { account_key: '' }, { account_key: 'x'.repeat(129) }, { sampled_at: 'invalid' }, { windows: {} },
    { windows: [{ ...p.windows[0], window_minutes: 0 }] },
    { windows: [{ ...p.windows[0], used_percent: -1 }] },
    { windows: [{ ...p.windows[0], used_percent: Number.NaN }] },
    { windows: [{ ...p.windows[0], resets_at: 'invalid' }] },
  ]) {
    assert.throws(() => rt.usageWindows({ ...p, ...invalid }), TypeError)
  }
})

test('an older node receives no usage window events', async t => {
  const n = await startFakeNode(t, { features: ['usage'] })
  const rt = createRuntime(driver, { runtime: 'toy', socketPath: n.path })
  t.after(() => rt.stop())
  rt.start()
  await until('listed', () => n.last('runtime.sessions'))
  assert.equal(rt.usageWindows(windowReport()), false)
  assert.equal(n.last('usage.windows'), undefined)
})
