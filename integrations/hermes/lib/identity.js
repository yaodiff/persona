import { randomUUID } from 'node:crypto'
import { mkdir, readFile, readdir, rename, rm, writeFile } from 'node:fs/promises'
import { homedir } from 'node:os'
import { join, resolve } from 'node:path'

export const nativeID = /^[a-zA-Z0-9_-]{1,120}$/
export const personaID = /^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$/

export function profileHome(env) {
  const home = env.HOME || homedir()
  const configured = env.HERMES_HOME?.trim()
  if (configured === '~') return home
  if (configured?.startsWith('~/')) return resolve(home, configured.slice(2))
  if (configured) return resolve(configured)
  return process.platform === 'win32'
    ? join(env.LOCALAPPDATA || join(home, 'AppData', 'Local'), 'hermes')
    : join(home, '.hermes')
}

// Hermes owns transcript storage. This only binds Persona's start identity to the native ACP ID.
export function identities(home, log) {
  const dir = join(home, 'persona-acp')
  const records = new Map()
  return {
    get: (raw) => records.get(raw),
    async load() {
      const files = await readdir(dir).catch((error) => {
        if (error.code === 'ENOENT') return []
        throw error
      })
      for (const file of files) {
        if (!file.endsWith('.json')) continue
        const raw = file.slice(0, -5)
        if (!nativeID.test(raw)) continue
        try {
          const value = JSON.parse(await readFile(join(dir, file), 'utf8'))
          if (value?.version === 1 && typeof value.id === 'string' && personaID.test(value.id)) {
            records.set(raw, value.id)
          } else {
            log('Ignored an invalid Hermes session identity record')
          }
        } catch {
          log('Could not read a Hermes session identity record')
        }
      }
    },
    async save(raw, id) {
      await mkdir(dir, { recursive: true, mode: 0o700 })
      const destination = join(dir, raw + '.json')
      const temporary = destination + '.' + randomUUID() + '.tmp'
      try {
        await writeFile(temporary, JSON.stringify({ version: 1, id }), { mode: 0o600, flag: 'wx' })
        await rename(temporary, destination)
      } finally {
        await rm(temporary, { force: true })
      }
      records.set(raw, id)
    },
  }
}
