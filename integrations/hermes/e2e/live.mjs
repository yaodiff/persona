// PERSONAD=<personad binary> HERMES_BIN=<ACP-enabled Hermes CLI> node integrations/hermes/e2e/live.mjs
import { spawn } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { once } from 'node:events'
import { mkdir, readFile, writeFile } from 'node:fs/promises'
import { createServer } from 'node:http'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { checks, startNode, until } from '../../runtime-sdk/testing/node-harness.mjs'

const { PERSONAD: personad, HERMES_BIN: hermes } = process.env
if (!personad || !hermes) {
  console.error('Set PERSONAD and HERMES_BIN to the two local executables')
  process.exit(2)
}

const node = await startNode({ personad })
const { check, summary } = checks()
const provider = await startProvider()
let companion
let companionLog = ''
let stopped
let sid
const profile = join(node.home, 'hermes-profile')
const cwd = join(node.home, 'workspace')
const secondCwd = join(node.home, 'another-workspace')
const fixture = join(cwd, 'permission-fixture')
const env = {
  PATH: process.env.PATH, HOME: node.home, HERMES_HOME: profile,
  NO_PROXY: '127.0.0.1', TERM: 'dumb',
}

async function connect() {
  companion = spawn(process.execPath, [fileURLToPath(new URL('../companion.mjs', import.meta.url)),
    '--hermes-bin', hermes, '--socket', node.runtimeSocket], { env, stdio: ['ignore', 'ignore', 'pipe'] })
  companion.stderr.on('data', (data) => { companionLog += data })
  stopped = once(companion, 'close')
  return until('the Hermes launcher', async () => {
    if (companion.exitCode !== null) throw new Error('Hermes companion exited before registering')
    return ((await node.call('launchers.list')).launchers ?? []).find((l) => l.name === 'hermes')
  }, 25_000)
}

async function disconnect() {
  if (!companion) return
  companion.kill()
  await stopped
  companion = undefined
}

try {
  await Promise.all([mkdir(profile), mkdir(cwd), mkdir(secondCwd)])
  await writeFile(join(profile, 'config.yaml'), `model:
  provider: custom
  default: hermes-fixture
  base_url: ${provider.url}/v1
  api_key: fixture
  context_length: 131072
agent:
  max_turns: 4
approvals:
  mode: manual
`)
  const launcher = await connect()
  check('the SDK registers the Hermes launcher on the real node', launcher.source === 'hermes')
  const proposedID = randomUUID()
  const { session } = await node.call('session.start', { launcher: 'hermes', cwd, session_id: proposedID })
  sid = session.session_id
  const row = await until('the Hermes session', async () =>
    ((await node.call('sessions.list')).sessions ?? []).find((s) => s.session_id === sid))
  check('the row has the Hermes runtime identity and advertised capabilities',
    sid === proposedID && row.agent === 'hermes' && row.agent_label === 'Hermes Agent' && row.space_kind === 'hermes' &&
    row.source_kind === 'runtime' && row.capabilities?.send === true && row.capabilities?.interrupt === true && !row.pane_id)
  const request = (method, params) => node.call(method, { session_id: sid, ...params })
  const page = async (params) => {
    const result = await request('session.items', params)
    return { ...result, items: result.items ?? [] }
  }
  const send = (text, client_msg_id = randomUUID()) => request('session.send', { text, client_msg_id })
  const waitText = (text) => until(text, async () => (await page()).items.some((i) => i.text?.trim() === text), 35_000)
  await request('session.watch', { since_seq: 0 })
  const id = randomUUID()
  await send('First fixture message', id)
  await send('First fixture message', id)
  await waitText('Hermes test reply')
  await until('the done state', () => node.status(sid) === 'done', 10_000)
  const first = await page()
  check('a repeated client message is delivered once',
    first.items.filter((i) => i.text === 'First fixture message').length === 1 && provider.calls.get('First fixture message') === 1)
  check('the node shows live text and a finished turn', node.deltas(sid).some(Boolean) && node.status(sid) === 'done')
  check('the node pages committed history', (await page({ after_seq: 1 })).items.some((i) => i.text === 'Hermes test reply'))
  const terminal = await request('session.keys', { keys: ['enter'] }).then(() => 'ok', (error) => error.message)
  check('terminal keys are unsupported', /^unsupported/.test(terminal))

  node.resolve = (ix) => ({ allow: ix.kind === 'permission' })
  await send('Ask permission for the fixture')
  const approval = await until('the Hermes permission request', () => node.asked.find((ix) => ix.session_id === sid), 30_000)
  await waitText('Hermes tool turn finished')
  const file = (await readFile(fixture, 'utf8').catch(() => '')).trim()
  const toolResult = (await page()).items.find((i) => i.kind === 'tool_result')
  check('a real ACP permission reaches the hub and the approved tool runs in the workspace',
    approval.tool_name === 'Edit' && approval.agent === 'hermes' &&
    file === 'approved fixture write' && toolResult && !toolResult.is_error,
    JSON.stringify({ tool: approval.tool_name, agent: approval.agent, file, result: toolResult?.text }))
  await until('the tool turn ends', () => node.status(sid) === 'done', 10_000)

  await send('Hold the first session')
  await until('the blocked provider request', () => provider.release, 20_000)
  await until('the working state', () => node.status(sid) === 'working', 10_000)
  const { session: other } = await node.call('session.start', {
    launcher: 'hermes', cwd: secondCwd, prompt: 'Second fixture session', client_msg_id: randomUUID(),
  })
  await until('the second workspace reply', async () => ((await node.call('session.items', {
    session_id: other.session_id,
  })).items ?? []).some((i) => i.text === 'Hermes test reply'), 30_000)
  check('a second workspace completes while the first is still working', node.status(sid) === 'working' && other.cwd === secondCwd)
  await request('session.interrupt')
  provider.release()
  await waitText('已中断')
  check('interrupt settles against the real prompt outcome', (await page()).items.some((i) => i.text === '已中断'))
  check('all live item batches continue the prior cursor', node.gaps(sid).length === 0, `${node.batches(sid).length} batches`)
  await disconnect()
  await until('the launcher is absent when disconnected', async () =>
    !((await node.call('launchers.list')).launchers ?? []).some((l) => l.name === 'hermes'), 10_000)
  await connect()
  await until('the original session is rediscovered', async () =>
    ((await node.call('sessions.list')).sessions ?? []).some((s) => s.session_id === sid), 10_000)
  const reloaded = await page()
  check('a fresh companion loads persisted history from Hermes', reloaded.history !== first.history &&
    reloaded.items.some((i) => i.text === 'Hermes test reply'))
  await send('Continue after restart')
  await until('the resumed reply', async () => {
    const history = await page()
    const index = history.items.findIndex((i) => i.text === 'Continue after restart')
    return index >= 0 && history.items.slice(index + 1).some((i) => i.text === 'Hermes test reply')
  }, 30_000)
  check('the session can continue after its native history loads', true)
} catch (error) {
  check('live run', false, error.message)
  console.error(companionLog.slice(-2000))
  console.error(node.log.join('').slice(-2000))
  if (sid) {
    const recent = await node.call('session.items', { session_id: sid, limit: 12 }).catch(() => ({}))
    console.error(JSON.stringify(recent.items?.map(({ kind, text, tool_name }) => ({ kind, text, tool_name }))))
  }
} finally {
  provider.release?.()
  await disconnect()
  await node.stopAndWait()
  await provider.close()
}
process.exitCode = summary() ? 1 : 0

async function startProvider() {
  const calls = new Map()
  let release
  const server = createServer(async (req, res) => {
    if (req.method === 'GET' && req.url === '/v1/models') {
      res.writeHead(200, { 'Content-Type': 'application/json' })
      res.end(JSON.stringify({ object: 'list', data: [{ id: 'hermes-fixture', object: 'model' }] }))
      return
    }
    if (req.method !== 'POST' || req.url !== '/v1/chat/completions') {
      res.writeHead(404).end()
      return
    }
    const chunks = []
    for await (const chunk of req) chunks.push(chunk)
    const request = JSON.parse(Buffer.concat(chunks))
    const last = request.messages.at(-1)
    const { content } = last
    const main = Boolean(request.tools?.length)
    if (main && typeof content === 'string') calls.set(content, (calls.get(content) ?? 0) + 1)
    if (main && content === 'Hold the first session') await new Promise((resolve) => { release = resolve })
    const toolCall = main && content === 'Ask permission for the fixture' ? {
      id: 'fixture-call', type: 'function', function: {
        name: 'write_file', arguments: JSON.stringify({ path: 'permission-fixture', content: 'approved fixture write' }),
      },
    } : undefined
    const text = last.role === 'tool' ? 'Hermes tool turn finished' : main ? 'Hermes test reply' : 'Test session'
    const base = { id: 'chatcmpl-fixture', created: 1, model: 'hermes-fixture' }
    const reason = toolCall ? 'tool_calls' : 'stop'
    if (!request.stream) {
      res.writeHead(200, { 'Content-Type': 'application/json' })
      res.end(JSON.stringify({ ...base, object: 'chat.completion', choices: [{
        index: 0, finish_reason: reason,
        message: toolCall ? { role: 'assistant', tool_calls: [toolCall] } : { role: 'assistant', content: text },
      }] }))
      return
    }
    res.writeHead(200, { 'Content-Type': 'text/event-stream' })
    const event = (delta, finish_reason = null) => res.write('data: ' + JSON.stringify({
      ...base, object: 'chat.completion.chunk', choices: [{ index: 0, delta, finish_reason }],
    }) + '\n\n')
    if (toolCall) event({ role: 'assistant', tool_calls: [{ index: 0, ...toolCall }] })
    else {
      event({ role: 'assistant', content: text.slice(0, 7) })
      await new Promise((resolve) => setTimeout(resolve, 200))
      event({ content: text.slice(7) })
    }
    event({}, reason)
    res.end('data: [DONE]\n\n')
  })
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve))
  return {
    url: `http://127.0.0.1:${server.address().port}`, calls, get release() { return release },
    close: () => new Promise((resolve) => server.close(resolve)),
  }
}
