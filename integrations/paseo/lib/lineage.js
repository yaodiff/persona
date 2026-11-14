// Where a Paseo agent's Claude conversation began (docs/integrations/paseo.md "Session IDs"). Paseo forks an agent's
// Claude Code session whenever it rewinds it, and so when the user goes on after an API error in Paseo's app (it drops
// the failed turn first): the fork is a new transcript beside the old one, and every record it copied names the session
// it came from (forkedFrom: {sessionId, messageUuid}). The bridge follows those back when it did not see the agent move
// (it was not running then). Only a transcript's first records are read, never what was said.

import { closeSync, existsSync, openSync, readdirSync, readSync, realpathSync } from 'node:fs'
import { homedir } from 'node:os'
import { dirname, join } from 'node:path'
import { CLAUDE_ID } from './map.js'

const HEAD_MOST = 8 << 20 // bytes of a transcript's first records read at most (a first prompt may carry an image)
const HEAD_LINES = 3 // records read for the time the conversation began
const DEPTH = 64 // sessions followed back at most
const BEGAN_SLACK = 60_000 // ms a conversation may seem to begin before its agent was created (two processes' clocks)
const MISS_AGAIN = 60_000 // ms before a transcript not found is looked for in every project folder again

/** The folder Claude Code keeps a directory's sessions in, under projects/: every character but letters and digits "-". */
export const projectKey = (cwd) => cwd.replace(/[^a-zA-Z0-9]/g, '-')

/**
 * A transcript's first records: the session its first record was copied from (parent: a fork's), and when its
 * conversation began (startedAt: a fork copies its conversation from the start). Undefined when it cannot be read.
 */
export function readHead(path, id) {
  let fd
  try {
    fd = openSync(path, 'r')
    const chunks = []
    let size = 0
    let lines = 0
    const buf = Buffer.alloc(64 << 10)
    while (lines < HEAD_LINES && size < HEAD_MOST) {
      const n = readSync(fd, buf, 0, buf.length, size)
      if (n <= 0) break
      chunks.push(Buffer.from(buf.subarray(0, n)))
      size += n
      for (let i = 0; i < n; i++) if (buf[i] === 10) lines++
    }
    const text = Buffer.concat(chunks).toString('utf8').split('\n')
    if (lines < HEAD_LINES && size >= HEAD_MOST) text.pop() // cut off: the last line is not whole
    const records = text.slice(0, HEAD_LINES).map((l) => {
      try {
        return JSON.parse(l)
      } catch {
        return undefined
      }
    })
    if (!records[0]) return undefined
    const from = records[0].forkedFrom?.sessionId
    const parent = typeof from === 'string' && CLAUDE_ID.test(from) && from !== id ? from : undefined
    const startedAt = records.map((r) => Date.parse(r?.timestamp)).find((t) => Number.isFinite(t))
    return { parent, startedAt }
  } catch {
    return undefined
  } finally {
    if (fd !== undefined) closeSync(fd)
  }
}

/**
 * The reader. dirs: Claude Code config directories to look in besides env's $CLAUDE_CONFIG_DIR and home's .claude (those
 * Paseo's providers set).
 */
export function createLineage({ dirs = () => [], home = homedir(), env = process.env } = {}) {
  const heads = new Map() // path → its head: a transcript's first records never change
  const missed = new Map() // session id → when it was not found
  const plain = new Set() // sessions read that are no fork of their agent's own (rootOf): asked again for each change

  const configDirs = () => [...new Set([env.CLAUDE_CONFIG_DIR, join(home, '.claude'), ...dirs()].filter(Boolean)
    .map((d) => (d.startsWith('~/') ? join(home, d.slice(2)) : d)))]

  function real(p) {
    try {
      return realpathSync(p)
    } catch {
      return p
    }
  }

  /** Session id's transcript: in folder (a fork is written beside its source), else cwd's project folder, else any. */
  function find(id, cwd, folder) {
    const name = `${id}.jsonl`
    const near = folder ? [join(folder, name)] : []
    for (const d of configDirs()) {
      for (const c of new Set([cwd, real(cwd)])) if (typeof c === 'string' && c) near.push(join(d, 'projects', projectKey(c), name))
    }
    const found = near.find((p) => existsSync(p))
    if (found || Date.now() - (missed.get(id) ?? 0) < MISS_AGAIN) return found
    for (const d of configDirs()) {
      let folders
      try {
        folders = readdirSync(join(d, 'projects'))
      } catch {
        continue
      }
      for (const f of folders) {
        const p = join(d, 'projects', f, name)
        if (existsSync(p)) return p
      }
    }
    if (missed.size > 2000) missed.clear()
    missed.set(id, Date.now())
    return undefined
  }

  function head(path, id) {
    if (heads.has(path)) return heads.get(path)
    const h = readHead(path, id)
    if (h) {
      if (heads.size > 4000) heads.clear()
      heads.set(path, h)
    }
    return h
  }

  /**
   * The first session of the line Claude session id was forked from: where the conversation of an agent created at
   * createdAt began, when that agent began it itself (a conversation that began before the agent was imported or handed
   * over from another agent). Not followed past a session another agent has (taken): its fork is that agent's branch.
   * Undefined when id is the line's first, began elsewhere, or cannot be read. A session whose transcript is gone ends
   * the line: its id is known from its fork's records.
   */
  function rootOf(id, { cwd, createdAt, taken = () => false } = {}) {
    if (plain.has(id)) return undefined
    const path = find(id, cwd)
    let h = path && head(path, id)
    const created = Date.parse(createdAt)
    if (!h?.parent || !Number.isFinite(created) || !(h.startedAt >= created - BEGAN_SLACK)) {
      if (h) { // read, and no fork of its agent's own: it stays so (an agent's every change asks again)
        if (plain.size > 4000) plain.clear()
        plain.add(id)
      }
      return undefined
    }
    const folder = dirname(path)
    const seen = new Set([id])
    let root
    for (let i = 0; i < DEPTH && h?.parent && !seen.has(h.parent) && !taken(h.parent); i++) {
      root = h.parent
      seen.add(root)
      const p = find(root, cwd, folder)
      h = p ? head(p, root) : undefined
    }
    return root
  }

  return { rootOf }
}
