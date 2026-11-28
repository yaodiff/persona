// Real Paseo + real Codex + real personad, with a local scripted Responses model.
// PERSONAD=/path/personad PASEO_BIN=/path/paseo CODEX_BIN=/path/codex node integrations/paseo/e2e/codex.mjs
// Build personad after go generate ./internal/runtimepkg. No user homes or credentials are used.
// PASEO_BACKGROUND_E2E=1 also checks a real background shell on Paseo 0.16+ and a compatible Codex app-server.
import { execFileSync, spawn } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, realpathSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:net'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { checks, startNode, until } from '../../runtime-sdk/testing/node-harness.mjs'
import { daemonTarget } from '../lib/discover.js'
import { PaseoClient } from '../lib/paseo.js'
import { startCodexModel } from './codex-model.mjs'

const { PERSONAD: personad, PASEO_BIN: paseoBin, CODEX_BIN: codexBin } = process.env
const testBackground = process.env.PASEO_BACKGROUND_E2E === '1'
if (![personad, paseoBin, codexBin].every((p) => p?.startsWith('/') && existsSync(p))) {
  throw new Error('Set PERSONAD, PASEO_BIN and CODEX_BIN to absolute executable paths')
}
let root, home, state, work, codexHome, claudeHome, paseoHome, childrenFile, model
const { check, summary } = checks()
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const daemonLog = []
let daemon, node, nodeStarting, paseo
const startup = new AbortController()
let cleaning
const alive = (pid) => { try { process.kill(pid, 0); return true } catch (error) { if (error.code === 'ESRCH') return false; if (error.code === 'EPERM') return true; throw error } }
function stopPid(pid, signal) {
  try { process.kill(pid, signal) } catch (error) { if (error.code !== 'ESRCH') throw error }
}
async function stopOwned(ids) {
  const pids = [...new Set(ids)].filter((pid) => Number.isInteger(pid) && Math.abs(pid) > 1)
  for (const pid of pids) if (alive(pid)) stopPid(pid, 'SIGTERM')
  const deadline = Date.now() + 5000
  while (pids.some(alive) && Date.now() < deadline) await sleep(50)
  for (const pid of pids) if (alive(pid)) stopPid(pid, 'SIGKILL')
  const killedBy = Date.now() + 5000
  while (pids.some(alive) && Date.now() < killedBy) await sleep(50)
  if (pids.some(alive)) throw new Error('owned processes did not exit after SIGKILL')
}
async function cleanup() {
  if (cleaning) return cleaning
  cleaning = (async () => {
    paseo?.stop()
    startup.abort(new Error('fixture stopping'))
    const results = await Promise.allSettled([
      nodeStarting?.then((h) => h.stopAndWait(), () => {}),
      (async () => {
        // The detached foreground CLI owns this group, including Paseo's supervisor and worker.
        let daemonFailure
        try { await stopOwned(daemon?.pid ? [-daemon.pid] : []) } catch (error) { daemonFailure = error }
        // Codex app-servers have separate groups, recorded by our launcher only.
        const pids = childrenFile && existsSync(childrenFile) ? readFileSync(childrenFile, 'utf8').trim().split('\n').map(Number).filter((p) => p > 1) : []
        await stopOwned(pids.map((pid) => -pid))
        if (daemonFailure) throw daemonFailure
      })(),
      model?.close(),
    ])
    const failures = results.filter((r) => r.status === 'rejected').map((r) => r.reason)
    if (failures.length) throw new AggregateError(failures, 'fixture cleanup failed')
    if (root) {
      if (process.env.KEEP === '1') console.log(`kept ${root}`)
      else rmSync(root, { recursive: true, force: true })
    }
  })()
  return cleaning
}
const deadline = setTimeout(() => { console.error('FAIL overall timeout'); cleanup().finally(() => process.exit(1)) }, 240_000)
for (const signal of ['SIGINT', 'SIGTERM']) process.once(signal, () => cleanup().finally(() => process.exit(1)))
try {
  root = mkdtempSync(join(tmpdir(), 'persona-paseo-codex-'))
  root = realpathSync(root)
  home = join(root, 'home')
  state = join(root, 'state')
  work = join(state, 'work')
  codexHome = join(state, 'codex')
  claudeHome = join(state, 'claude')
  paseoHome = join(root, 'paseo')
  for (const p of [home, state, work, codexHome, claudeHome, paseoHome]) mkdirSync(p, { recursive: true })
  childrenFile = join(root, 'codex-pids')
  const quote = (s) => `'${s.replaceAll("'", "'\\''")}'`
  const gate = join(work, 'background-finished')
  const backgroundProgram = `const fs = require('node:fs'); const gate = ${JSON.stringify(gate)}; const watcher = fs.watch(${JSON.stringify(work)}, () => { if (fs.existsSync(gate)) { watcher.close(); console.log('background finished'); } }); console.log('background ready'); if (fs.existsSync(gate)) watcher.close();`
  const backgroundCommand = `${quote(process.execPath)} -e ${quote(backgroundProgram)}`
  model = await startCodexModel(testBackground ? { backgroundCommand } : {})
  const wrapper = join(root, 'codex')
  writeFileSync(wrapper, `#!/bin/sh\nif [ "$1" = app-server ]; then printf '%s\\n' "$$" >> ${quote(childrenFile)}; fi\nexec ${quote(codexBin)} "$@"\n`)
  chmodSync(wrapper, 0o700)
  writeFileSync(join(codexHome, 'config.toml'), `model = "gpt-5.4"\nmodel_provider = "e2e"\nmodel_reasoning_effort = "low"\ncheck_for_update_on_startup = false\n[model_providers.e2e]\nname = "Local scripted model"\nbase_url = "${model.url}"\nwire_api = "responses"\nrequires_openai_auth = false\nsupports_websockets = false\n[analytics]\nenabled = false\n[feedback]\nenabled = false\n`)
  const env = {
    PATH: `${dirname(process.execPath)}:/usr/bin:/bin`, HOME: home, CODEX_HOME: codexHome,
    CLAUDE_CONFIG_DIR: claudeHome, PASEO_HOME: paseoHome, PERSONA_HOME: state,
    PERSONA_NODE_SESSION_DIR: work, LANG: 'C.UTF-8', NODE_TLS_REJECT_UNAUTHORIZED: '1',
    DISABLE_TELEMETRY: '1', DO_NOT_TRACK: '1',
  }
  const port = await new Promise((resolve, reject) => {
    const s = createServer()
    s.once('error', reject)
    s.listen(0, '127.0.0.1', () => { const p = s.address().port; s.close(() => resolve(p)) })
  })
  writeFileSync(join(paseoHome, 'config.json'), JSON.stringify({
    version: 1,
    daemon: { listen: `127.0.0.1:${port}`, relay: { enabled: false }, mcp: { injectIntoAgents: false }, browserTools: { enabled: false } },
    pluginsEnabled: false, features: { webUi: { enabled: false } },
    agents: { metadataGeneration: { providers: [] }, providers: {
      antigravity: { enabled: false }, hoplite: { enabled: false }, muse: { enabled: false },
      claude: { enabled: false }, codex: { enabled: false }, copilot: { enabled: false }, opencode: { enabled: false }, pi: { enabled: false }, omp: { enabled: false },
      'e2e-codex': { extends: 'codex', label: 'E2E Codex', enabled: true, command: [wrapper], env: { CODEX_HOME: codexHome } },
    } },
  }, null, 2))
  startup.signal.throwIfAborted()
  daemon = spawn(paseoBin, ['daemon', 'run', '--home', paseoHome], { env, detached: true, stdio: ['ignore', 'pipe', 'pipe'] })
  let daemonError
  daemon.on('error', (error) => { daemonError = error })
  daemon.stdout.on('data', (d) => daemonLog.push(String(d)))
  daemon.stderr.on('data', (d) => daemonLog.push(String(d)))
  await until('isolated Paseo listening', () => {
    if (daemonError) throw daemonError
    if (daemon.exitCode !== null) throw new Error(`Paseo exited: ${daemon.exitCode}`)
    return daemonTarget(paseoHome).target?.port === port
  }, 30_000)
  check('Paseo uses the isolated loopback address', daemonTarget(paseoHome).target?.host === '127.0.0.1')
  paseo = new PaseoClient({ home: paseoHome, env })
  paseo.start()
  await until('Paseo client connected', () => paseo.connected, 20_000)
  nodeStarting = startNode({ personad, home, env, signal: startup.signal })
  node = await nodeStarting
  await node.call('engine.use', { engine: 'paseo' })
  await until('Codex provider available', async () => (await node.call('launchers.list')).launchers?.some((l) => l.name === 'e2e-codex'), 30_000)
  // Register only the symlink spelling, with no workspace from which Paseo could infer the project. Git on the node
  // returns the physical checkout. This reproduces the creation route that previously allocated a duplicate (#9726).
  const git = (cwd, ...args) => execFileSync('git', ['-C', cwd, ...args], { env, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }).trim()
  git(work, 'init', '-b', 'main')
  git(work, '-c', 'user.name=Test', '-c', 'user.email=test@example.com', 'commit', '--allow-empty', '-m', 'Initial')
  const alias = join(root, 'checkout-alias')
  symlinkSync(work, alias)
  const registered = await paseo.request('project.add.request', { cwd: alias })
  const projectId = registered.project?.projectId
  if (!projectId) throw new Error(`Project registration failed: ${JSON.stringify(registered)}`)
  const projects = async () => (await paseo.request('project.list.request', {})).projects
  const workspace = async (id) => (await paseo.request('fetch_workspaces_request', {})).entries.find((w) => w.id === id)
  const before = (await projects()).length
  const prep = await node.call('worktree.prepare', { cwd: alias, kind: 'paseo' })
  check('node resolves the symlink to the physical checkout', prep.root === work, `root=${prep.root}`)
  const createdTree = await node.call('worktree.create', { cwd: alias, kind: 'paseo', branch: 'e2e/alias', path: join(prep.path, 'alias-tree') }, 150_000)
  const treeWorkspace = await workspace(createdTree.workspace_id)
  check('path-only worktree keeps the original project without adding one', treeWorkspace?.projectId === projectId && (await projects()).length === before,
    `before=${before} after=${(await projects()).length} project=${treeWorkspace?.projectId} expected=${projectId}`)
  check('new worktree contains the source commit', git(createdTree.path, 'rev-parse', 'HEAD') === git(work, 'rev-parse', 'HEAD'))
  const started = await node.call('session.start', { cwd: work, launcher: 'e2e-codex', model: 'gpt-5.4', prompt: 'E2E_CODEX_FIRST' }, 60_000)
  const sid = started.session?.session_id
  check('Persona starts a stable Paseo session', /^paseo-[0-9a-f-]{36}$/.test(sid ?? ''), sid)
  const agentId = sid.slice('paseo-'.length)
  const agent = async (id = agentId) => (await paseo.request('fetch_agent_request', { agentId: id })).agent
  const row = () => node.sessions().find((s) => s.session_id === sid)
  // A read of the agent while Paseo has it stored is refused (not_loaded, #8132): nothing read yet.
  const items = async () => (await node.call('session.items', { session_id: sid, limit: 200 })
    .catch((e) => { if (e.code === 'not_loaded') return {}; throw e })).items ?? []
  const answered = async (marker) => (await items()).some((i) => i.role === 'assistant' && i.text?.includes(`Scripted answer: ${marker}`))
  await until('real Codex completes first response', () => answered('E2E_CODEX_FIRST'), 60_000)
  await until('first turn idle', async () => (await agent())?.status === 'idle', 15_000)
  const initial = await agent()
  const directWorkspace = await workspace(initial.workspaceId)
  check('direct Agent start keeps the original project without adding one', directWorkspace?.projectId === projectId && (await projects()).length === before,
    `before=${before} after=${(await projects()).length} project=${directWorkspace?.projectId} expected=${projectId}`)
  // Make the duplicate explicitly in this disposable daemon only. Records at one physical root prefer the one registered
  // at the physical path, independent of registry order; a selected workspace still supplies its own owner (#14844).
  const duplicateProject = await paseo.request('project.add.request', { cwd: work })
  const duplicateId = duplicateProject.project?.projectId
  if (!duplicateId || duplicateId === projectId) throw new Error('Fixture could not reproduce distinct alias projects')
  const selectedProject = duplicateId
  const duplicateCount = (await projects()).length
  const duplicateTree = await node.call('worktree.create', { cwd: alias, kind: 'paseo', branch: 'e2e/duplicate-root', path: join(prep.path, 'duplicate-root') }, 150_000)
  const duplicateTreeWorkspace = await workspace(duplicateTree.workspace_id)
  check('same-root duplicate path-only worktree uses the stable project', duplicateTreeWorkspace?.projectId === selectedProject &&
    !!git(work, 'branch', '--list', 'e2e/duplicate-root') && (await projects()).length === duplicateCount,
    `project=${duplicateTreeWorkspace?.projectId} expected=${selectedProject}`)
  const explicitPrep = await node.call('worktree.prepare', { workspace_id: createdTree.workspace_id, kind: 'paseo' })
  const explicitTree = await node.call('worktree.create', { workspace_id: createdTree.workspace_id, kind: 'paseo', branch: 'e2e/explicit', path: join(explicitPrep.path, 'explicit-tree') }, 150_000)
  const explicitWorkspace = await workspace(explicitTree.workspace_id)
  check('explicit linked workspace keeps its project despite the duplicate', explicitWorkspace?.projectId === projectId && (await projects()).length === duplicateCount,
    `before=${duplicateCount} after=${(await projects()).length} project=${explicitWorkspace?.projectId} expected=${projectId}`)

  const thread = initial.persistence?.sessionId ?? initial.runtimeInfo?.sessionId
  const rollout = readdirSync(codexHome, { recursive: true }).find((p) => p.endsWith(`${thread}.jsonl`))
  check('real Codex wrote its isolated rollout', !!rollout && JSON.parse(readFileSync(join(codexHome, rollout), 'utf8').split('\n')[0]).payload?.id === thread, thread)
  check('real Codex thread attached to the Persona session', !!thread && row()?.agent === 'codex', JSON.stringify(row()))
  const archiveAt = node.events.length
  await node.call('session.close', { session_id: sid })
  await until('Paseo archives the agent', async () => !!(await agent())?.archivedAt, 15_000)
  await until('archived row settles', () => row()?.status === 'offline', 10_000).catch(() => {})
  const retained = check('archived Codex keeps its Paseo id', row()?.status === 'offline', JSON.stringify(node.sessions()))
  // codex_thread is private to the runtime protocol; the hub sees its effect as one row despite the real rollout.
  check('archived conversation has exactly one row, never a rollout row', node.sessions().filter((s) => s.cwd === work).length === 1 && node.sessions().some((s) => s.session_id === sid), JSON.stringify(node.sessions().map((s) => s.session_id)))
  if (!retained) throw new Error('archive identity regression reproduced; stopping before resume under a missing id')
  // A read no chat asked for (the manager's after a turn, an app's background sync) must not have Paseo load the archived
  // agent, which would start Codex again for nothing (#8132): refused. Opening its chat (a watch) reads it.
  const launches = () => existsSync(childrenFile) ? readFileSync(childrenFile, 'utf8').trim().split('\n').filter(Boolean).length : 0
  const launched = launches()
  const background = await node.call('session.items', { session_id: sid, limit: 200 }).then(() => 'answered', (e) => e.code ?? e.message)
  check('a background read of the archived agent is refused and starts no Codex', background === 'not_loaded' && launches() === launched,
    `${background}, ${launches() - launched} Codex started`)
  await node.call('session.watch', { session_id: sid, since_seq: 0 })
  check('archived conversation remains readable once its chat is opened', await answered('E2E_CODEX_FIRST'))
  await node.call('session.unwatch', { session_id: sid })
  await node.call('session.send', { session_id: sid, text: 'E2E_CODEX_SECOND', client_msg_id: 'codex-e2e-second' }, 60_000)
  await until('reply after archive', () => answered('E2E_CODEX_SECOND'), 45_000)
  const restored = await agent()
  check('message restores the same agent, label and Codex thread', !restored.archivedAt && restored.labels?.['persona.session'] === sid && restored.persistence?.sessionId === thread && row()?.session_id === sid)
  await until('second turn idle', async () => (await agent())?.status === 'idle', 15_000)
  await node.call('session.close', { session_id: sid })
  await until('archived again', () => row()?.status === 'offline', 15_000)
  check('no rollout duplicate throughout archive and message restore', !node.events.slice(archiveAt).some((e) => e.event === 'sessions.changed' && e.params.sessions?.some((s) => s.cwd === work && s.session_id !== sid)))
  await node.call('engine.use', { engine: 'herdr' })
  await until('bridge stopped', () => !node.sessions().some((s) => s.source_kind === 'runtime' && s.session_id === sid), 15_000)
  const reconnectAt = node.events.length
  await node.call('engine.use', { engine: 'paseo' })
  await until('archive restored after reconnect', () => row()?.status === 'offline', 30_000)
  check('bridge reconnect restores the single archived row', node.sessions().filter((s) => s.cwd === work).length === 1)
  const resumed = await node.call('session.start', { cwd: work, resume_session_id: sid }, 60_000)
  await until('resume restores archived Codex agent', async () => !(await agent()).archivedAt, 15_000)
  check('session.start resumes the same archived agent and thread', resumed.session?.session_id === sid && (await agent()).persistence?.sessionId === thread && (await agent()).labels?.['persona.session'] === sid)
  await node.call('session.send', { session_id: sid, text: 'E2E_CODEX_THIRD', client_msg_id: 'codex-e2e-third' }, 60_000)
  await until('reply after bridge reconnect', () => answered('E2E_CODEX_THIRD'), 45_000)
  check('message after reconnect restores the original agent', !(await agent()).archivedAt && (await agent()).persistence?.sessionId === thread && (await agent()).labels?.['persona.session'] === sid)
  const duplicate = (from) => node.events.slice(from).some((e) => e.event === 'sessions.changed' && e.params.sessions?.some((s) => s.cwd === work && s.session_id !== sid))
  // Switching engines intentionally exposes ordinary history; inspect only the connected archive intervals.
  check('no rollout duplicate after bridge reconnect settled', !duplicate(node.events.findIndex((e, i) => i >= reconnectAt && e.event === 'sessions.changed' && e.params.sessions?.some((s) => s.session_id === sid))))
  const otherWork = join(work, 'paseo-originated')
  mkdirSync(otherWork)
  const created = await paseo.request('agent.create.request', { agentId: randomUUID(), config: { provider: 'e2e-codex', cwd: otherWork, model: 'gpt-5.4' }, initialPrompt: 'E2E_CODEX_NATIVE' }, { timeoutMs: 60_000 })
  const nativeId = created.agent.id
  await until('Paseo-originated Codex completes', async () => (await agent(nativeId))?.status === 'idle', 45_000)
  await until('Paseo-originated runtime listed', () => node.sessions().some((s) => s.session_id === `paseo-${nativeId}`), 15_000)
  check('Paseo-originated agent remains unlabeled', !(await agent(nativeId)).labels?.['persona.session'])
  await paseo.request('archive_agent_request', { agentId: nativeId })
  await until('Paseo-originated archived runtime leaves list', () => !node.sessions().some((s) => s.session_id === `paseo-${nativeId}`), 15_000)
  check('Paseo-originated archive behavior is unchanged', !node.sessions().some((s) => s.session_id === `paseo-${nativeId}`))
  check('all four scripted turns reached the local model', model.requests.length === 4, `${model.requests.length} requests`)
  if (testBackground) {
    await node.call('session.modes', { session_id: sid, permission_mode: 'bypassPermissions' })
    await node.call('session.send', { session_id: sid, text: 'E2E_CODEX_BACKGROUND', client_msg_id: 'codex-e2e-background' }, 60_000)
    await until('real background command outlives its foreground reply', async () => {
      const current = await agent()
      return current.status === 'idle' && current.backgroundWork?.count > 0
    }, 30_000)
    const inventory = await paseo.request('agent.background_work.list.request', { agentId })
    const native = inventory.items.find((task) => task.command?.includes('background ready'))
    check('Paseo reports the real background command after the turn ends', native?.kind === 'shell')
    await until('Persona lists the native task without an open chat', () => row()?.background?.some((task) => task.id === native?.id), 15_000)
    check('Persona keeps the native task identity and command', row().background.some((task) => task.id === native.id && task.command?.includes('background ready')))
    writeFileSync(gate, 'finish\n')
    await until('Paseo observes the background command exit', async () => (await agent()).backgroundWork?.count === 0, 15_000)
    await until('Persona clears the completed native task', () => !row()?.background?.some((task) => task.id === native.id), 15_000)
    check('the completed native task leaves Persona without an open chat', !row()?.background?.length)
  }
  // The Paseo-originated fixture above registers its own project; count from here so only this start is measured.
  const beforeDuplicateStart = (await projects()).length
  const duplicateStart = await node.call('session.start', { cwd: work, launcher: 'e2e-codex', prompt: 'E2E_CODEX_DUPLICATE' }, 60_000)
  const duplicateAgentId = duplicateStart.session?.session_id?.replace(/^paseo-/, '')
  const duplicateAgent = duplicateAgentId ? await agent(duplicateAgentId) : undefined
  const duplicateAgentWorkspace = duplicateAgent ? await workspace(duplicateAgent.workspaceId) : undefined
  check('same-root duplicate direct Agent start uses the stable project', duplicateAgentWorkspace?.projectId === selectedProject &&
    (await projects()).length === beforeDuplicateStart,
    `before=${beforeDuplicateStart} after=${(await projects()).length} project=${duplicateAgentWorkspace?.projectId} expected=${selectedProject}`)
} catch (error) {
  check('real Codex archive lifecycle completed', false, error.stack)
  console.error('Paseo log:', daemonLog.join('').slice(-16000))
  console.error('Node log:', node?.log.join('').slice(-16000))
} finally {
  clearTimeout(deadline)
  const failed = summary()
  try { await cleanup() } catch (error) { console.error('FAIL cleanup', error); process.exit(1) }
  process.exit(failed ? 1 : 0)
}
