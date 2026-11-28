import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { startFakeNode } from '../../runtime-sdk/testing/fake-node.js'
import * as compat from '../lib/compat.js'
import { toAgyId } from '../lib/map.js'

// A chat's Persona tools come from the node's own program (`<program> mcp`). A Persona Link install calls it
// persona-node and has no personad (#14582): the node names it with --personad-bin, and a companion started some other
// way finds persona-node where personad would be.
const FAKE = fileURLToPath(new URL('./fake-acp-policy.mjs', import.meta.url))
const COMPANION = fileURLToPath(new URL('../companion.mjs', import.meta.url))
const NOT_FOUND = 'no Persona node program'

function fixture(t) {
  const home = mkdtempSync(join(tmpdir(), 'agy-node-program-'))
  const stops = []
  t.after(async () => {
    for (const stop of stops.reverse()) await stop()
    rmSync(home, { recursive: true, force: true })
  })
  // A program file; nothing here runs it.
  const program = (dir, name) => {
    mkdirSync(dir, { recursive: true })
    writeFileSync(join(dir, name), '', { mode: 0o700 })
    return join(dir, name)
  }
  return { home, stops, program, bin: join(home, '.persona', 'bin') }
}

// The companion as the node starts it, with a fake agy that records what it is asked.
async function startCompanion(t, f, args = []) {
  const record = join(f.home, 'requests.jsonl')
  writeFileSync(record, '')
  const agy = join(f.home, 'agy')
  writeFileSync(agy, `#!/bin/sh\nexec '${process.execPath}' '${FAKE}' --record '${record}' "$@"\n`, { mode: 0o700 })
  const node = await startFakeNode(t)
  const env = { ...process.env, HOME: f.home }
  delete env.PERSONA_HOME
  delete env.JARVIS_HOME
  const child = spawn(process.execPath, [COMPANION, '--socket', node.path, '--agy-bin', agy, '--no-media-guard', '--no-update', ...args],
    { env, stdio: ['ignore', 'ignore', 'pipe'] })
  const stderr = []
  child.stderr.on('data', (chunk) => stderr.push(String(chunk)))
  const exited = new Promise((resolve) => child.once('exit', resolve))
  f.stops.push(async () => { child.kill('SIGTERM'); await exited })
  await node.ready
  // The MCP servers a new chat is loaded with: the runtime resumes it with them, and not at all with none.
  const servers = async () => {
    const session = (await node.peer.call('session.start', { cwd: f.home })).session.session_id
    return readFileSync(record, 'utf8').trim().split('\n').filter(Boolean).map(JSON.parse)
      .findLast((entry) => entry.method === 'session/resume' && entry.params?.sessionId === toAgyId(session))?.params.mcpServers ?? []
  }
  return { servers, log: () => stderr.join('') }
}

// How a chat's persona MCP server runs the node program: Agents reach it through mcp-proxy.mjs (#15822).
const runs = (server) => server.args.slice(-3)

test('nodeProgram takes the node\'s program, else personad, else persona-node beside it', (t) => {
  const f = fixture(t)
  const { nodeProgram } = compat
  assert.equal(nodeProgram(f.home, {}), '')
  const link = f.program(f.bin, 'persona-node')
  assert.equal(nodeProgram(f.home, {}), link)
  const personad = f.program(f.bin, 'personad')
  assert.equal(nodeProgram(f.home, {}), personad)
  const named = f.program(join(f.home, 'elsewhere'), 'persona-node')
  assert.equal(nodeProgram(f.home, { personadBin: named }), named)
})

test('the companion hands its chats the program --personad-bin names', async (t) => {
  const f = fixture(t)
  const named = f.program(join(f.home, 'elsewhere'), 'persona-node') // in no state directory: only the option finds it
  const companion = await startCompanion(t, f, ['--personad-bin', named])
  assert.deepEqual((await companion.servers()).map((s) => [s.name, runs(s)]), [['persona', ['--agent', named, 'mcp']]], companion.log())
})

test('a companion started without --personad-bin finds persona-node where personad would be', async (t) => {
  const f = fixture(t)
  const link = f.program(f.bin, 'persona-node')
  const companion = await startCompanion(t, f)
  assert.deepEqual((await companion.servers()).map(runs), [['--agent', link, 'mcp']], companion.log())
  assert.ok(!companion.log().includes(NOT_FOUND))
})

test('without a node program the chats get no Persona tools, and the companion says so once', async (t) => {
  const f = fixture(t)
  const companion = await startCompanion(t, f)
  assert.deepEqual([...await companion.servers(), ...await companion.servers()], [])
  assert.equal(companion.log().split(NOT_FOUND).length - 1, 1, companion.log())
})
