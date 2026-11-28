import assert from 'node:assert/strict'
import { Writable } from 'node:stream'
import { test } from 'node:test'
import { setTimeout as delay } from 'node:timers/promises'
import { AgyAcpClient } from '../lib/acp.js'
import { isWaitingOnlyOnBackground, settleBackgroundWaitOnce } from '../lib/runtime.js'

// Focused coverage for the "foreground reply finished, only a non-daemon background task keeps
// session/prompt open" state. Every predicate case is fail-closed; T1-T5 map to the RCA fix matrix.

const RAW = '11111111-2222-4333-8444-555555555555'
const NOW = 1_900_000_000_000
const REPLY = 'Here is the finished foreground answer.'
const iso = (ms) => new Date(ms).toISOString()

function completedStep(over = {}) {
  return { step_index: 11, type: 'PLANNER_RESPONSE', status: 'DONE', content: REPLY, created_at: iso(NOW - 20_000), ...over }
}

// Every condition holds unless a test overrides one axis.
function fixture(over = {}) {
  const turn = {
    streamingText: REPLY,
    startedAt: NOW - 60_000,
    lastSessionUpdateAt: NOW - 10_000, // 10 s of silence > 5 s grace
    activeThoughtStep: null,
    pendingThought: '',
    liveSteps: [],
    exportedBaseMax: 9,
    ...over.turn,
  }
  const exported = {
    steps: [
      { step_index: 10, type: 'USER_INPUT', status: 'DONE', content: 'do it', created_at: iso(NOW - 59_000) },
      completedStep(over.lastStep),
    ],
    ...over.exported,
  }
  const tasks = over.tasks ?? { running: [{ id: `${RAW}/task-1`, kind: 'shell', started_at: iso(NOW - 5_000) }] }
  const options = { now: NOW, graceMs: 5_000, tasks, ...over.options }
  return { rawId: RAW, turn, exported, options }
}

/** An AgyAcpClient backed by an in-memory child process (no spawn) with a short watchdog. */
function memoryAcp(inactivityMs = 120) {
  const acp = new AgyAcpClient({ bin: 'agy', promptInactivityMs: inactivityMs, log() {} })
  const writes = []
  const stdin = new Writable({ write(chunk, _enc, cb) { writes.push(String(chunk)); cb() } })
  acp.proc = { pid: 4321, stdin, exitCode: 0, signalCode: null }
  const cancelCount = () => writes.filter((line) => line.includes('"method":"session/cancel"')).length
  const update = (sessionId = RAW) => acp._onMessage({ method: 'session/update', params: { sessionId } }, acp.proc)
  return { acp, writes, cancelCount, update, stop: () => acp.stop() }
}

test('T1 positive: settled reply + live fresh background task pauses the watchdog and settles once', async () => {
  const good = fixture()
  assert.equal(isWaitingOnlyOnBackground(good.rawId, good.turn, good.exported, good.options), true)

  // The foreground close runs each effect exactly once per delivered step index.
  const calls = []
  const effects = {
    pause: () => calls.push('pause'),
    endStream: () => calls.push('endStream'),
    remember: (index) => calls.push(`remember:${index}`),
    flush: () => calls.push('flush'),
    notify: () => calls.push('notify'),
  }
  assert.equal(settleBackgroundWaitOnce(good.turn, 11, effects), true)
  assert.deepEqual(calls, ['pause', 'endStream', 'remember:11', 'flush', 'notify'])
  assert.equal(good.turn.waitingOnBackground, true)
  assert.equal(good.turn.bgSettledIndex, 11)
  assert.equal(settleBackgroundWaitOnce(good.turn, 11, effects), false, 'the same step index settles only once')
  assert.equal(calls.length, 5, 'idempotent re-entry must not repeat foreground effects')

  // A genuinely paused ACP prompt clears the inactivity timer and sends no session/cancel past the window.
  const mem = memoryAcp()
  try {
    const outcome = mem.acp._rawCall('session/prompt', { sessionId: RAW }, 0)
    outcome.catch(() => {}) // stop() settles it
    await delay(20)
    assert.equal(mem.acp.setPromptPaused(RAW, true), true)
    await delay(300) // > 120 ms inactivity window
    assert.equal(mem.cancelCount(), 0, 'paused background wait must not fire the watchdog cancel')
    assert.equal(mem.acp.pending.size, 1, 'the session/prompt stays pending for the background task')
  } finally {
    await mem.stop()
  }
})

test('T2 wake re-arms: a session/update resumes the watchdog and a later real stall still cancels', async () => {
  const mem = memoryAcp(120)
  try {
    const outcome = mem.acp._rawCall('session/prompt', { sessionId: RAW }, 0)
    const settled = outcome.then(() => 'resolved', (error) => ({ inactive: error.promptInactive === true, message: error.message }))
    await delay(20)
    mem.acp.setPromptPaused(RAW, true)
    await delay(260)
    assert.equal(mem.cancelCount(), 0, 'paused before the wake: no cancel')

    // Background task finishes and wakes the model: any session/update leaves backgroundWaiting and re-arms.
    mem.update()
    await delay(300) // a fresh 120 ms window elapses with no further activity
    assert.equal(mem.cancelCount(), 1, 'the watchdog must re-arm after a session/update and cancel a real stall')
    const result = await Promise.race([settled, delay(500).then(() => 'still-pending')])
    assert.notEqual(result, 'still-pending', 'the re-armed watchdog must reject the prompt')
    assert.equal(result.inactive, true)
    assert.match(result.message, /prompt inactive for 120ms/)
  } finally {
    await mem.stop()
  }
})

test('T3 long-thinking / no completed disk reply never pauses (mutation M1 guard)', () => {
  // An active thought block blocks on condition 2 even if everything else looks settled.
  const thinking = fixture({ turn: { activeThoughtStep: { step_index: 12, thinking: 'long reasoning…' } } })
  assert.equal(isWaitingOnlyOnBackground(thinking.rawId, thinking.turn, thinking.exported, thinking.options), false)

  // M1 isolation: no active thought, but the last disk step is not a completed matched reply.
  const streaming = fixture({ lastStep: { status: 'RUNNING', content: undefined } })
  delete streaming.exported.steps[1].content
  assert.equal(isWaitingOnlyOnBackground(streaming.rawId, streaming.turn, streaming.exported, streaming.options), false)

  const mismatch = fixture({ lastStep: completedStep({ content: 'a different final text' }) })
  assert.equal(isWaitingOnlyOnBackground(mismatch.rawId, mismatch.turn, mismatch.exported, mismatch.options), false)

  // A fresh update inside the grace window also waits for more streaming.
  const fresh = fixture({ turn: { lastSessionUpdateAt: NOW - 500 } })
  assert.equal(isWaitingOnlyOnBackground(fresh.rawId, fresh.turn, fresh.exported, fresh.options), false)
})

test('T4 no running background task never pauses (mutation M2 guard)', () => {
  const none = fixture({ tasks: { running: [], ended: [] } })
  assert.equal(isWaitingOnlyOnBackground(none.rawId, none.turn, none.exported, none.options), false)

  // An only-ended task list is equally insufficient.
  const endedOnly = fixture({ tasks: { running: [], ended: [{ id: `${RAW}/task-9`, ended_at: iso(NOW - 1000) }] } })
  assert.equal(isWaitingOnlyOnBackground(endedOnly.rawId, endedOnly.turn, endedOnly.exported, endedOnly.options), false)

  // A malformed task extraction fails closed rather than throwing.
  const malformed = fixture()
  malformed.options.tasks = { running: 'not-an-array' }
  assert.equal(isWaitingOnlyOnBackground(malformed.rawId, malformed.turn, malformed.exported, malformed.options), false)
})

test('T5 stale ghost background task from before this turn never pauses (mutation M4 time bound)', () => {
  const ghost = fixture({
    tasks: { running: [{ id: `${RAW}/task-old`, kind: 'shell', started_at: iso(NOW - 3_600_000) }] }, // 1 h ago, << turn.startedAt
  })
  assert.equal(isWaitingOnlyOnBackground(ghost.rawId, ghost.turn, ghost.exported, ghost.options), false)

  // A task with an unparseable timestamp cannot prove it belongs to this turn/process.
  const badTime = fixture({ tasks: { running: [{ id: `${RAW}/task-x`, started_at: 'not-a-date' }] } })
  assert.equal(isWaitingOnlyOnBackground(badTime.rawId, badTime.turn, badTime.exported, badTime.options), false)

  // Condition 6: an open permission / rewind / cancel block suppresses the settle even with all five signals.
  const blocked = fixture({ options: { blocked: true } })
  assert.equal(isWaitingOnlyOnBackground(blocked.rawId, blocked.turn, blocked.exported, blocked.options), false)
})
