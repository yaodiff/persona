import assert from 'node:assert/strict'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { createAgyRuntime } from '../lib/runtime.js'

const CARBON = 'gemini-4-carbon-high'
const CARBON_MEDIUM = 'gemini-4-carbon-medium'
const NEON = 'gemini-4-neon-high'
const NEON_XHIGH = 'gemini-4-neon-xhigh'
const models = [
  { modelId: CARBON, name: 'Gemini 4 Carbon (High)' },
  { modelId: CARBON_MEDIUM, name: 'Gemini 4 Carbon (Medium)' },
  { modelId: NEON, name: 'Gemini 4 Neon (High)' },
  { modelId: NEON_XHIGH, name: 'Gemini 4 Neon (Extra High)' },
]
const rawId = '11111111-2222-3333-4444-555555555555'
const sessionId = `agy-${rawId}`
const fake = fileURLToPath(new URL('./fake-acp-models.mjs', import.meta.url))

async function fixture(t, { transcriptModel, effortFallback } = {}) {
  const home = mkdtempSync(join(tmpdir(), 'agy-legacy-effort-'))
  const agyDir = join(home, 'settings')
  mkdirSync(agyDir)
  writeFileSync(join(agyDir, 'settings.json'), JSON.stringify({ model: NEON }))
  writeFileSync(join(home, 'catalog.json'), JSON.stringify({ models, resumeModel: NEON, effortFallback }))
  writeFileSync(join(home, 'calls.jsonl'), '')
  const agyBin = join(home, 'agy')
  writeFileSync(agyBin, `#!/bin/sh\nexec '${process.execPath}' '${fake}' '${home}' "$@"\n`, { mode: 0o700 })
  const brainDir = join(agyDir, 'brain', rawId)
  if (transcriptModel) {
    const logs = join(brainDir, '.system_generated', 'logs')
    mkdirSync(logs, { recursive: true })
    const steps = [CARBON, transcriptModel, '<synthetic>'].map((model, step_index) => ({
      type: 'PLANNER_RESPONSE', step_index, model, input_tokens: 100, content: 'Reply',
    }))
    writeFileSync(join(logs, 'transcript_full.jsonl'), steps.map(step => JSON.stringify(step)).join('\n') + '\n')
  }
  let n, runtime
  const connect = async () => {
    n = await startFakeNode(t)
    runtime = createAgyRuntime({ home, agyDir, agyBin, personadBin: join(home, 'missing'), socketPath: n.path, log: () => {} })
    runtime.start()
    await n.ready
  }
  await connect()
  t.after(async () => { await runtime.stop(); rmSync(home, { recursive: true, force: true }) })
  const restart = async () => {
    await runtime.stop()
    await connect()
  }
  const start = (params) => n.peer.call('session.start', { cwd: home, ...params })
  const session = () => n.last('runtime.sessions')?.sessions.find(row => row.session_id === sessionId)
  const calls = () => readFileSync(join(home, 'calls.jsonl'), 'utf8').trim().split('\n').filter(Boolean).map(JSON.parse)
  const withSession = (method, params) => n.peer.call(`session.${method}`, { session_id: sessionId, ...params })
  const promptModels = () => calls().filter(call => call.method === 'fake/prompt_model').map(call => call.model)
  const promptModel = () => until('native prompt model', () => promptModels()[0])
  const setModels = () => calls().filter(call => call.method === 'session/set_model').map(call => call.params.modelId)
  return { brainDir, calls, promptModel, promptModels, restart, session, setModels, start, withSession }
}

test('restarting the companion after the first prompt restores the selected model', async (t) => {
  const { brainDir, promptModels, restart, session, start, withSession } = await fixture(t)
  await start({ assistant: true, model: CARBON })
  // Native storage can first appear after session/new has already accepted the model.
  mkdirSync(brainDir, { recursive: true })
  await withSession('send', { assistant: true, text: 'First prompt' })
  await until('first turn finishes', () => session()?.status === 'done')
  assert.deepEqual(promptModels(), [CARBON])

  await restart()
  await withSession('send', { assistant: true, text: 'Continue after restart' })
  await until('resumed prompt reaches ACP', () => promptModels().length === 2)
  assert.deepEqual(promptModels(), [CARBON, CARBON])
})

test('a cold send restores the last advertised model of an older session', async (t) => {
  const { promptModel, withSession } = await fixture(t, { transcriptModel: CARBON.toUpperCase() })
  await withSession('send', { text: 'Continue an existing conversation' })
  assert.equal(await promptModel(), CARBON)
})

test('legacy effort-only modes select the matching model in the saved family after resume', async (t) => {
  const { calls, withSession, promptModel, setModels } = await fixture(t, { transcriptModel: CARBON.toUpperCase() })
  await withSession('modes', { effort: 'medium' })
  await withSession('send', { text: 'Use the new effort' })
  assert.equal(await promptModel(), CARBON_MEDIUM)
  assert.deepEqual(setModels(), [CARBON_MEDIUM])
  assert.ok(!calls().some(call => call.method === 'session/set_config_option'))
})

test('an unlisted effort keeps its legacy request but a native family switch is reverted', async (t) => {
  const { calls, withSession, promptModel, setModels } = await fixture(t, {
    transcriptModel: CARBON, effortFallback: { xhigh: NEON_XHIGH },
  })
  await withSession('modes', { effort: 'xhigh' })
  assert.deepEqual(calls().filter(call => call.method === 'session/set_config_option').map(call => call.params.value), ['xhigh'])
  assert.deepEqual(setModels(), [CARBON, CARBON])
  await withSession('send', { text: 'Keep the original family' })
  assert.equal(await promptModel(), CARBON)
})
