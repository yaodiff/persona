// A fake personad node for a runtime's unit tests: listens on a temp runtime socket, records what the runtime says and
// lets the test call it like the node does. Real end-to-end checks use node-harness.mjs instead.

import { randomBytes } from 'node:crypto'
import { createServer } from 'node:net'
import { mkdtempSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { Peer, RpcError } from '../lib/wire.js'

/**
 * @param {{ after?: (fn: () => void) => void }} [t] - a node:test context (cleans up after the test), or nothing.
 * @param {{ path?: string, hub?: boolean, features?: string[], tools?: object[], strict?: boolean }} [opts] - the socket
 *   path (default: a fresh temp one); whether hello says a hub is connected (default true; setHub changes it and tells
 *   the runtime); the node's features; the hub's tools mcp.tools answers (n.tools changes them); strict: a card for a
 *   session the runtime's last list does not have is refused, as the node refuses it.
 * @returns {Promise<{ path: string, ready: Promise<void>, hello: any, peer: Peer, peers: Set<Peer>, events: {event: string, params: any}[],
 *   last(event: string): any, all(event: string): any[], asked: any[], refused: any[], resolve: (ix: any) => any,
 *   input(request: import('../index.js').SessionInput): Promise<import('../index.js').TypedInput>, close(): void }>}
 *   Also: tools / toolCalls / callTool (how mcp.call answers), refuseHello (an RpcError hello answers with, or none),
 *   holdHello (a promise the next hellos wait on before they are answered, or none),
 *   offer (a package {name, hash, version, files} this node carries: hello offers it to a runtime running another hash
 *   of it, runtime.package answers it; offerUpdates false leaves only runtime.package) and packageCalls (the runtime.package calls).
 */
export async function startFakeNode(t, opts = {}) {
  const usePipe = !opts.path && process.platform === 'win32'
  const dir = opts.path || usePipe ? undefined : mkdtempSync('/tmp/jrt-') // unix socket paths are short on macOS: not under $TMPDIR
  const path = opts.path ?? (usePipe ? `\\\\.\\pipe\\jrt-${randomBytes(6).toString('hex')}` : join(dir, 'runtime.sock'))
  const n = { path, events: [], hello: undefined, peer: undefined, peers: new Set(), asked: [], refused: [], resolve: () => ({ dismiss: true }), hub: opts.hub ?? true, hellos: 0,
    tools: opts.tools ?? [], toolCalls: [], callTool: (params) => ({ content: [{ type: 'text', text: `${params.name} ran` }], is_error: false }),
    refuseHello: undefined, offer: undefined, offerUpdates: true, packageCalls: [] }
  let connected
  n.ready = new Promise((r) => { connected = r })
  const server = createServer((socket) => {
    const p = new Peer(socket, {
      prefix: 'b',
      onRequest: async (method, params) => {
        if (method === 'runtime.hello') {
          n.hellos++
          if (n.holdHello) await n.holdHello
          if (n.refuseHello) throw n.refuseHello
          n.hello = params
          n.peer = p
          setImmediate(connected)
          const res = { protocol: 1, node_version: 'fake', hub: n.hub, features: opts.features ?? ['prefer_running', 'resume_argv', 'handover', 'input'] }
          if (n.offerUpdates !== false && n.offer && params?.package?.name === n.offer.name && params.package.hash !== n.offer.hash) {
            res.update = { name: n.offer.name, hash: n.offer.hash, version: n.offer.version ?? 'fake' }
          }
          return res
        }
        if (method === 'runtime.package') {
          n.packageCalls.push(params)
          if (!n.offer || params?.name !== n.offer.name) throw new RpcError(`no package ${params?.name}`, 'not_found')
          return n.offer
        }
        if (method === 'runtime.a2a_mcp' && n.a2aMcpToken) return { token: n.a2aMcpToken(params.session_id) }
        if (method === 'interaction.create') {
          const sid = params.interaction?.session_id
          if (opts.strict && !n.last('runtime.sessions')?.sessions?.some((s) => s.session_id === sid)) {
            n.refused.push(params.interaction)
            throw new Error(`session ${sid} is not listed by this runtime`)
          }
          n.asked.push(params.interaction)
          return n.resolve(params.interaction)
        }
        if (method === 'mcp.tools') return { tools: n.tools }
        if (method === 'mcp.call') {
          n.toolCalls.push(params)
          return n.callTool(params)
        }
        throw new Error(`unexpected ${method}`)
      },
      onEvent: (event, params) => n.events.push({ event, params }),
    })
    n.peers.add(p)
    void p.done.finally(() => n.peers.delete(p))
  })
  await new Promise((r) => server.listen(path, r))
  n.input = (request) => n.peer.call('session.input', request)
  n.last = (event) => n.events.filter((e) => e.event === event).at(-1)?.params
  n.all = (event) => n.events.filter((e) => e.event === event).map((e) => e.params)
  /** The hub comes or goes: the runtime is told, as a resident node tells it. */
  n.setHub = (connected) => {
    n.hub = connected
    n.peer?.notify('runtime.hub', { connected })
  }
  /** Answer the next hellos as a node does when another process holds the runtime id (undefined: accept again). */
  n.conflict = (on = true) => {
    n.refuseHello = on ? new RpcError('another process is connected to this node as runtime', 'runtime_conflict') : undefined
  }
  n.close = () => {
    server.close()
    if (dir) rmSync(dir, { recursive: true, force: true })
  }
  t?.after?.(n.close)
  return n
}

/** Wait until ok() is truthy (it may be async), or fail after ms. */
export async function until(what, ok, ms = 3000) {
  const end = Date.now() + ms
  for (;;) {
    const v = await ok()
    if (v) return v
    if (Date.now() > end) throw new Error(`timed out waiting for ${what}`)
    await new Promise((r) => setTimeout(r, 10))
  }
}
