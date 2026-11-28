// A machine running a Persona node probes every new listener in an Agent's process tree with `GET /` (User-Agent
// persona-ports, server/internal/node/ports.go), and these tests run inside one there (docs/lessons/testing.md). Each fake
// agy's native service answers the probe and keeps running: one that died on it made its agy acp process exit mid-test
// (`agy acp exited (1)`, #12198).
import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { until } from '../../runtime-sdk/testing/fake-node.js'

for (const fake of ['fake-acp-stale.mjs', 'fake-acp-usage.mjs', 'fake-acp-rewind.mjs']) {
  test(`${fake} answers the node's port probe and keeps running`, async (t) => {
    const dir = mkdtempSync(join(tmpdir(), 'agy-probe-'))
    let child
    // One hook, so the fake has exited before its directory goes (after hooks run in the order they were added).
    t.after(async () => {
      if (child && child.exitCode === null && child.signalCode === null) {
        const exited = once(child, 'exit')
        child.kill()
        await exited
      }
      rmSync(dir, { recursive: true, force: true })
    })
    writeFileSync(join(dir, 'account.json'), JSON.stringify({ models: [] })) // fake-acp-stale.mjs reads it as it starts
    writeFileSync(join(dir, 'trajectory.json'), '{}') // so does fake-acp-rewind.mjs
    const log = join(dir, 'acp.log')
    child = spawn(process.execPath, [fileURLToPath(new URL(`./${fake}`, import.meta.url)), dir, 'acp', '--log-file', log],
      { stdio: ['pipe', 'ignore', 'inherit'] })
    const port = await until('the native port', () => existsSync(log) && readFileSync(log, 'utf8').match(/random port at (\d+)/)?.[1])
    const res = await fetch(`http://127.0.0.1:${port}/`, { headers: { 'User-Agent': 'persona-ports', Accept: 'text/html' } })
    assert.equal(res.status, 404)
    await res.text()
    assert.equal(child.exitCode, null, 'the fake is still running')
  })
}
