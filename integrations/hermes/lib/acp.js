import { spawn } from 'node:child_process'
import { createInterface } from 'node:readline'
import { setTimeout as delay } from 'node:timers/promises'

const RPC_TIMEOUT = 20_000
const STDIO_DRAIN_TIMEOUT = 1_000

export class HermesAcp {
  constructor({ bin, cwd, env, onUpdate, onPermission, onExit }) {
    this.processGroup = process.platform !== 'win32'
    const child = spawn(bin, ['acp'], { cwd, env, detached: this.processGroup, stdio: ['pipe', 'pipe', 'pipe'] })
    this.child = child
    this.pending = new Map()
    this.nextId = 0
    this.closed = false
    this.writeFailed = false
    // Provider logs can contain prompts and credentials.
    child.stderr.resume()
    const lines = createInterface({ input: child.stdout, crlfDelay: Infinity })
    const exited = Promise.withResolvers()
    const drained = Promise.withResolvers()
    this.exited = exited.promise
    this.drained = drained.promise
    let drainTimer
    const disconnect = () => {
      if (this.closed) return
      this.closed = true
      clearTimeout(drainTimer)
      lines.close()
      for (const { fail } of this.pending.values()) fail(new Error('Hermes ACP disconnected'))
      drained.resolve()
      onExit?.()
    }
    child.once('exit', () => {
      exited.resolve()
      // The process may exit before its last response is read. Bound the drain
      // in case inherited pipes keep stdio open after the process has gone.
      drainTimer = setTimeout(() => {
        disconnect()
        child.stdout.destroy()
        child.stderr.destroy()
        child.stdin.destroy()
      }, STDIO_DRAIN_TIMEOUT)
      if (this.processGroup) this.signal('SIGKILL')
    })
    child.once('close', () => {
      exited.resolve()
      disconnect()
    })
    child.once('error', () => {
      if (!child.pid) {
        exited.resolve()
        disconnect()
      }
    })
    child.stdin.once('error', () => {
      void this.stop().catch(disconnect)
    })
    lines.on('line', (line) => {
      let msg
      try { msg = JSON.parse(line) } catch { return }
      if (this.closed || !msg || typeof msg !== 'object') return
      if (msg.method === 'session/update' && msg.id == null) onUpdate?.(msg.params)
      else if (msg.method && msg.id != null) void this.answer(msg, onPermission)
      else if (msg.id != null) this.pending.get(msg.id)?.reply(msg)
    })
  }

  async initialize() {
    const hello = await this.call('initialize', {
      protocolVersion: 1, clientCapabilities: {},
      clientInfo: { name: 'persona', version: '0.1.0' },
    })
    if (hello?.protocolVersion !== 1 || hello.agentInfo?.name !== 'hermes-agent' ||
        !hello.agentCapabilities?.loadSession || !hello.agentCapabilities?.sessionCapabilities?.list) {
      throw new Error('Hermes ACP must support protocol version 1 and session loading and listing')
    }
    return hello.agentCapabilities
  }

  async answer(msg, onPermission) {
    if (msg.method !== 'session/request_permission') {
      this.write({ id: msg.id, error: { code: -32601, message: 'Method not supported' } })
      return
    }
    let outcome
    try { outcome = await onPermission?.(msg.params) } catch {}
    this.write({ id: msg.id, result: { outcome: outcome ?? { outcome: 'cancelled' } } })
  }

  get writable() {
    const input = this.child.stdin
    return !this.closed && !this.writeFailed && this.child.exitCode === null && this.child.signalCode === null &&
      !input.destroyed && !input.writableEnded && input.writable
  }

  write(message, onFailure) {
    if (!this.writable) return false
    const line = JSON.stringify({ jsonrpc: '2.0', ...message }) + '\n'
    this.child.stdin.write(line, (error) => {
      if (!error) return
      this.writeFailed = true
      onFailure?.(new Error('Hermes ACP disconnected'))
    })
    return true
  }

  notify(method, params) {
    this.write({ method, params })
  }

  call(method, params, timeout = RPC_TIMEOUT) {
    const id = ++this.nextId
    return new Promise((resolve, reject) => {
      const finish = (fn, value) => {
        if (!this.pending.delete(id)) return
        clearTimeout(timer)
        fn(value)
      }
      const fail = (error) => finish(reject, error)
      const timer = timeout > 0 ? setTimeout(() => fail(new Error(`Hermes ACP ${method} timed out`)), timeout) : null
      this.pending.set(id, {
        fail,
        reply: (msg) => msg.error
          ? fail(new Error(`Hermes ACP ${method} failed (${Number(msg.error.code) || 'unknown'})`))
          : finish(resolve, msg.result),
      })
      try {
        if (!this.write({ id, method, params }, fail)) fail(new Error('Hermes ACP disconnected'))
      } catch (error) {
        fail(error)
      }
    })
  }

  stop() {
    return this.stopping ??= this.terminate()
  }

  signal(signal) {
    const pid = this.child.pid
    if (!pid) return
    try {
      if (this.processGroup) process.kill(-pid, signal)
      else this.child.kill(signal)
    } catch (error) {
      if (error.code !== 'ESRCH') throw error
    }
  }

  async terminate() {
    // Node emits a spawn error when there is no PID. Wait for that handler to
    // reject pending calls and settle the drain before reporting a stopped worker.
    if (!this.child.pid) return this.drained
    this.child.stdin.end()
    const timer = new AbortController()
    const exited = (ms) => Promise.race([
      this.exited.then(() => true),
      delay(ms, false, { signal: timer.signal, ref: false }),
    ])
    try {
      if (!(await exited(1_000))) {
        this.signal('SIGTERM')
        if (!(await exited(2_000))) {
          this.signal('SIGKILL')
          await this.exited
        }
      }
    } finally {
      timer.abort()
    }
    await this.drained
  }
}
