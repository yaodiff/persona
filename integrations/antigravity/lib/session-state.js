import { randomBytes } from 'node:crypto'
import { constants, lstatSync, mkdirSync, readFileSync, renameSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

const SESSION_STATE = '.persona-session.json'
const UUID = /^[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}$/i

// Only explicit Persona starts get this marker. A native summary or an empty brain directory is not ownership.
export function createSessionStateStore(agyDir, identity) {
  function directory(rawId, create = false) {
    if (!UUID.test(rawId)) throw new Error('Invalid Persona native session id')
    if (create) mkdirSync(agyDir, { recursive: true, mode: 0o700 })
    if (!statSync(agyDir).isDirectory()) throw new Error('Invalid Antigravity configuration directory')
    let dir = agyDir
    for (const part of ['brain', rawId]) {
      dir = join(dir, part)
      if (create) {
        try { mkdirSync(dir, { mode: 0o700 }) } catch (error) { if (error.code !== 'EEXIST') throw error }
      }
      if (!lstatSync(dir).isDirectory()) throw new Error('Invalid Persona session directory')
    }
    return dir
  }

  return {
    read(rawId) {
      try {
        const file = join(directory(rawId), SESSION_STATE)
        if (!lstatSync(file).isFile()) return
        const saved = JSON.parse(readFileSync(file, 'utf8'))
        if (saved.version !== 1 || saved.raw_id !== rawId || saved.runtime !== identity.runtime
          || saved.launcher !== identity.launcher || saved.prefix !== identity.prefix
          || !Number.isFinite(saved.created_at) || saved.created_at <= 0
          || typeof saved.cwd !== 'string' || !saved.cwd
          || typeof saved.awaiting_first_message !== 'boolean'
          || typeof saved.assistant !== 'boolean' || typeof saved.deferred !== 'boolean'
          || ['model', 'explicit_model', 'title', 'supervision_policy'].some((key) => saved[key] !== undefined && typeof saved[key] !== 'string')
          || saved.mode_info != null && (typeof saved.mode_info !== 'object' || Array.isArray(saved.mode_info))) return
        return saved
      } catch { /* An absent or damaged marker never promotes a native internal session. */ }
    },
    write(rawId, state) {
      const dir = directory(rawId, true)
      const temp = join(dir, `${SESSION_STATE}.${process.pid}.${randomBytes(8).toString('hex')}`)
      try {
        writeFileSync(temp, JSON.stringify({ ...state, ...identity, raw_id: rawId, version: 1 }) + '\n', {
          flag: constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW,
          mode: 0o600,
        })
        renameSync(temp, join(dir, SESSION_STATE))
      } finally {
        rmSync(temp, { force: true })
      }
    },
  }
}

export { SESSION_STATE }
