// Minimal JSON-RPC 2.0 stdio client for `opencode acp` (zero external dependencies).
// Handles initialization, session/new, session/load, session/prompt, session/set_config_option,
// session/cancel, session/close, streaming `session/update` notifications, and server-initiated
// `session/request_permission` callbacks.

import { spawn } from 'node:child_process'
import { EventEmitter } from 'node:events'
import { homedir } from 'node:os'

const DEFAULT_RPC_TIMEOUT_MS = 60_000
const STOP_GRACE_MS = 3_000
const childExited = (child) => !child || child.exitCode != null || child.signalCode != null

// Dedicated workers must not inherit a user's fixed/public server listener.
// Keep ordinary ACP arguments untouched, and never mutate the supplied array.
export function privateAcpArgs(args = ['acp']) {
  const result = []
  for (let index = 0; index < args.length; index++) {
    const arg = args[index]
    if (arg === '-p' || arg === '--port' || arg === '--hostname') { index++; continue }
    if (arg === '--mdns' || arg === '--no-mdns') {
      if (args[index + 1] === 'true' || args[index + 1] === 'false') index++
      continue
    }
    if (/^(?:-p\d+|--(?:port|hostname|(?:no-)?mdns)=)/.test(arg)) continue
    result.push(arg)
  }
  return [...result, '--port', '0', '--hostname', '127.0.0.1', '--no-mdns']
}

export class OpencodeAcpClient extends EventEmitter {
  constructor(options = {}) {
    super()
    this.bin = options.bin || 'opencode'
    this.args = Array.isArray(options.args) ? options.args : ['acp']
    this.cwd = options.cwd || homedir()
    this.env = options.env || process.env
    this.log = options.log || (() => {})
    this._spawn = options.spawn || spawn
    this.processGroup = options.processGroup === true && process.platform !== 'win32'
    this.onPermissionRequest = options.onPermissionRequest || (async () => ({ outcome: { outcome: 'cancelled' } }))
    this.proc = null
    this.pending = new Map() // id -> { resolve, reject, timer }
    this.loadedSessions = new Set()
    this.generation = 0
    this.nextId = 1
    this.buf = ''
    this.starting = null
    this.capabilities = null
    this.agentInfo = null
    this.stopped = false
  }

  get pid() {
    return this.proc?.pid ?? 0
  }

  get busy() {
    return Boolean(this.starting) || this.pending.size > 0
  }

  async ensureStarted(timeoutMs = DEFAULT_RPC_TIMEOUT_MS) {
    if (this.stopped) throw new Error('opencode ACP client stopped')
    if (!childExited(this.proc) && !this.proc.killed && this.capabilities) {
      return { capabilities: this.capabilities, agentInfo: this.agentInfo }
    }
    if (this.starting) return this.starting
    this.starting = this._start(timeoutMs).finally(() => {
      this.starting = null
    })
    return this.starting
  }

  async _start(timeoutMs) {
    const args = this.args.some((a) => a === '--cwd' || a.startsWith('--cwd=')) ? [...this.args] : [...this.args, '--cwd', this.cwd]
    const child = this._spawn(this.bin, args, {
      cwd: this.cwd,
      env: this.env,
      stdio: ['pipe', 'pipe', 'pipe'],
      ...(this.processGroup ? { detached: true } : {}),
    })
    this.proc = child
    this.generation++
    this.loadedSessions.clear()
    this.buf = ''
    this.capabilities = null
    this.agentInfo = null
    this.emit('process', child.pid ?? 0)

    child.stdin?.on('error', (err) => {
      this.log(`opencode acp stdin error: ${err?.message ?? err}`)
      if (this.proc === child) {
        this._failAll(new Error(`opencode acp stdin error: ${err?.message ?? err}`))
        this._clearProc()
        this._terminateChild(child)
      }
    })
    child.stdout?.setEncoding('utf8')
    child.stdout?.on('data', (chunk) => { if (this.proc === child) this._onStdout(chunk) })
    child.stderr?.setEncoding('utf8')
    child.stderr?.on('data', (chunk) => {
      for (const line of String(chunk).split(/\r?\n/)) {
        const trimmed = line.trim()
        if (trimmed) this.log(`opencode acp stderr: ${trimmed}`)
      }
    })
    child.on('error', (err) => {
      if (this.proc === child) {
        this._failAll(new Error(`opencode acp failed: ${err.message}`))
        this._clearProc()
      }
    })
    child.on('exit', (code, signal) => {
      // A native launcher may have spawned a server beneath itself. A crashed
      // group leader must not leave that server driving the retired session.
      if (this.processGroup) {
        try { process.kill(-child.pid, 'SIGKILL') } catch {}
      }
      const reason = signal ? `signal ${signal}` : `exit code ${code}`
      if (this.proc === child) {
        this._failAll(new Error(`opencode acp exited (${reason})`))
        this._clearProc()
      }
    })

    let init
    try {
      init = await this._callRaw('initialize', {
        protocolVersion: 1,
        clientCapabilities: {},
        clientInfo: { name: 'persona', version: '0.1.0' },
      }, timeoutMs)
    } catch (err) {
      if (this.proc === child) this._clearProc()
      this._terminateChild(child)
      throw err
    }
    this.capabilities = init?.agentCapabilities || {}
    this.agentInfo = init?.agentInfo || null
    return { capabilities: this.capabilities, agentInfo: this.agentInfo }
  }

  _signal(child, signal) {
    // Only assistant clients opt into a new process group. This never targets
    // a user's existing server or the ordinary shared Agent process tree.
    if (this.processGroup && child.pid > 1) {
      try { process.kill(-child.pid, signal); return } catch {}
    }
    child.kill(signal)
  }

  _terminateChild(child) {
    if (childExited(child)) return
    try { this._signal(child, 'SIGTERM') } catch {}
    const killTimer = setTimeout(() => {
      if (!childExited(child)) {
        try { this._signal(child, 'SIGKILL') } catch {}
      }
    }, STOP_GRACE_MS)
    killTimer.unref?.()
    child.once('exit', () => clearTimeout(killTimer))
  }

  _clearProc() {
    this.proc = null
    this.loadedSessions.clear()
    this.capabilities = null
    this.emit('process', 0)
  }

  _failAll(err) {
    for (const [id, entry] of this.pending) {
      clearTimeout(entry.timer)
      this.pending.delete(id)
      entry.reject(err)
    }
  }

  _onStdout(chunk) {
    this.buf += chunk
    let nl
    while ((nl = this.buf.indexOf('\n')) >= 0) {
      const line = this.buf.slice(0, nl).trim()
      this.buf = this.buf.slice(nl + 1)
      if (!line) continue
      let msg
      try {
        msg = JSON.parse(line)
      } catch {
        this.log(`opencode acp non-json: ${line.slice(0, 200)}`)
        continue
      }
      this._handleMessage(msg)
    }
  }

  _handleMessage(msg) {
    // Response to our RPC request
    if (msg.id !== undefined && msg.method === undefined) {
      const entry = this.pending.get(msg.id)
      if (!entry) return
      clearTimeout(entry.timer)
      this.pending.delete(msg.id)
      if (msg.error) {
        const err = new Error(msg.error.message || JSON.stringify(msg.error))
        err.code = msg.error.code
        err.data = msg.error.data
        entry.reject(err)
      } else {
        entry.resolve(msg.result)
      }
      return
    }
    // Server-initiated request (e.g. session/request_permission)
    if (msg.id !== undefined && typeof msg.method === 'string') {
      const expectedProc = this.proc
      const expectedGen = this.generation
      void this._handleServerRequest(msg, expectedProc, expectedGen).catch((err) => {
        this.log(`opencode acp server request failed: ${err?.message ?? err}`)
      })
      return
    }
    // Notification (e.g. session/update)
    if (typeof msg.method === 'string') {
      if (msg.method === 'session/update') this.emit('update', msg.params)
      this.emit('notification', msg.method, msg.params)
    }
  }

  _safeWriteFor(expectedProc, expectedGen, obj) {
    if (!expectedProc || this.proc !== expectedProc || this.generation !== expectedGen) {
      return false
    }
    try {
      this._write(obj)
      return true
    } catch (err) {
      this.log(`opencode acp write dropped: ${err?.message ?? err}`)
      return false
    }
  }

  async _handleServerRequest(msg, expectedProc = this.proc, expectedGen = this.generation) {
    try {
      let result
      if (msg.method === 'session/request_permission') {
        result = await this.onPermissionRequest(msg.params)
      } else {
        this._safeWriteFor(expectedProc, expectedGen, {
          jsonrpc: '2.0',
          id: msg.id,
          error: { code: -32601, message: `Method not found: ${msg.method}` },
        })
        return
      }
      this._safeWriteFor(expectedProc, expectedGen, {
        jsonrpc: '2.0',
        id: msg.id,
        result: result ?? { outcome: { outcome: 'cancelled' } },
      })
    } catch (err) {
      this._safeWriteFor(expectedProc, expectedGen, {
        jsonrpc: '2.0',
        id: msg.id,
        error: { code: -32000, message: err?.message ?? String(err) },
      })
    }
  }

  _write(obj) {
    if (!this.proc?.stdin || this.proc.stdin.destroyed || !this.proc.stdin.writable) {
      throw new Error('opencode acp stdin is closed')
    }
    this.proc.stdin.write(`${JSON.stringify(obj)}\n`)
  }

  _callRaw(method, params, timeoutMs = DEFAULT_RPC_TIMEOUT_MS) {
    return new Promise((resolve, reject) => {
      const id = this.nextId++
      let timer
      if (timeoutMs > 0) {
        timer = setTimeout(() => {
          this.pending.delete(id)
          reject(new Error(`opencode acp ${method} timed out after ${timeoutMs}ms`))
        }, timeoutMs)
      }
      this.pending.set(id, { resolve, reject, timer })
      try {
        this._write({ jsonrpc: '2.0', id, method, params })
      } catch (err) {
        clearTimeout(timer)
        this.pending.delete(id)
        reject(err)
      }
    })
  }

  async call(method, params, timeoutMs = DEFAULT_RPC_TIMEOUT_MS, signal) {
    if (signal?.aborted) {
      const err = new Error('opencode acp call aborted')
      err.name = 'AbortError'
      throw err
    }
    await this.ensureStarted()
    if (signal?.aborted) {
      const err = new Error('opencode acp call aborted')
      err.name = 'AbortError'
      throw err
    }
    return this._callRaw(method, params, timeoutMs)
  }

  notify(method, params) {
    if (childExited(this.proc) || this.proc.killed) return false
    try {
      this._write({ jsonrpc: '2.0', method, params })
      return true
    } catch {
      return false
    }
  }

  async newSession({ cwd, mcpServers = [], systemPrompt }, timeoutMs = DEFAULT_RPC_TIMEOUT_MS) {
    const res = await this.call('session/new', { cwd, mcpServers, ...(systemPrompt === undefined ? {} : { systemPrompt }) }, timeoutMs)
    if (res?.sessionId) this.loadedSessions.add(res.sessionId)
    return res
  }

  async loadSession({ sessionId, cwd, mcpServers = [] }, timeoutMs = DEFAULT_RPC_TIMEOUT_MS) {
    const res = await this.call('session/load', { sessionId, cwd, mcpServers }, timeoutMs)
    this.loadedSessions.add(sessionId)
    return res
  }

  async resumeSession({ sessionId, cwd, mcpServers = [], systemPrompt }, timeoutMs = DEFAULT_RPC_TIMEOUT_MS) {
    // resume reconnects without replaying the transcript as session/update.
    const res = await this.call('session/resume', { sessionId, cwd, mcpServers, ...(systemPrompt === undefined ? {} : { systemPrompt }) }, timeoutMs)
    this.loadedSessions.add(sessionId)
    return res
  }

  async setConfigOption(sessionId, configId, value, timeoutMs = DEFAULT_RPC_TIMEOUT_MS) {
    return this.call('session/set_config_option', { sessionId, configId, value }, timeoutMs)
  }

  async prompt(sessionId, prompt, timeoutMs = 0, signal) {
    return this.call('session/prompt', { sessionId, prompt }, timeoutMs, signal)
  }

  cancel(sessionId) {
    return this.notify('session/cancel', { sessionId })
  }

  async closeSession(sessionId, timeoutMs = 5_000) {
    this.loadedSessions.delete(sessionId)
    if (childExited(this.proc) || this.proc.killed) return
    try {
      await this._callRaw('session/close', { sessionId }, timeoutMs)
    } catch {
      // best effort cleanup
    }
  }

  async stopChild() {
    const child = this.proc
    this._failAll(new Error('opencode acp stopped'))
    if (!child) return
    this._clearProc()
    if (childExited(child)) return
    await new Promise((resolve) => {
      const done = () => {
        clearTimeout(killTimer)
        resolve()
      }
      child.once('exit', done)
      const killTimer = setTimeout(() => {
        if (childExited(child)) { done(); return }
        try { this._signal(child, 'SIGKILL') } catch { done() }
      }, STOP_GRACE_MS)
      killTimer.unref?.()
      try { this._signal(child, 'SIGTERM') } catch { done() }
    })
  }

  async stop() {
    this.stopped = true
    await this.stopChild()
  }
}
