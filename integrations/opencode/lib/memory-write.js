// Fail-closed containment check for Assistant memory-directory auto-approval.
// Mirrors integrations/dsh/lib/dsh.js: resolves symlinks component-by-component
// (catching dangling symlinks and kernel namei vs. lexical path.resolve divergence)
// and never falls back to memoryDir when cwd is missing.

import { lstatSync, readlinkSync } from 'node:fs'
import { dirname, isAbsolute, join, parse, relative, resolve, sep } from 'node:path'

const PATH_SEG_RE = sep === '\\' ? /[\\/]+/ : /\/+/
const badWinPath = (p) => sep === '\\' && (/^[A-Za-z]:(?![\\/])/.test(p) || /^[/\\](?![/\\])/.test(p))
const hasTildePrefix = (p) => p === '~' || /^~[^/\\]*[/\\]/.test(p)

function realPath(p, base = '', st = { hops: 0 }) {
  if (typeof p !== 'string' || !p || p.includes('\0') || badWinPath(p)) return null
  let cur
  if (isAbsolute(p)) {
    cur = parse(p).root
    p = p.slice(cur.length)
  } else {
    if (!base) return null
    cur = base
  }
  for (const seg of p.split(PATH_SEG_RE)) {
    if (!seg || seg === '.') continue
    if (seg === '..') {
      cur = dirname(cur)
      continue
    }
    const next = join(cur, seg)
    let isLink = false
    try {
      isLink = lstatSync(next).isSymbolicLink()
    } catch (err) {
      if (err?.code !== 'ENOENT' && err?.code !== 'ENOTDIR') return null
    }
    if (isLink) {
      if (++st.hops > 40) return null
      let target
      try {
        target = readlinkSync(next)
      } catch {
        return null
      }
      cur = realPath(target, cur, st)
      if (!cur) return null
    } else {
      cur = next
    }
  }
  return cur
}

function isInside(root, p) {
  if (!p) return false
  const rel = relative(root, p)
  return rel !== '' && rel !== '..' && !rel.startsWith(`..${sep}`) && !rel.startsWith('../') && !isAbsolute(rel)
}

export function isMemoryWritePath(file, memoryDir, cwd) {
  if (
    typeof file !== 'string' || !file ||
    typeof memoryDir !== 'string' || !memoryDir ||
    file.includes('\0') || memoryDir.includes('\0') ||
    hasTildePrefix(file) ||
    !isAbsolute(memoryDir) || badWinPath(file) || badWinPath(memoryDir)
  ) return false
  const root = realPath(memoryDir)
  if (!root || root === dirname(root)) return false
  let kTarget
  let lexTarget
  if (isAbsolute(file)) {
    kTarget = realPath(file)
    lexTarget = realPath(resolve(file))
  } else {
    if (typeof cwd !== 'string' || !cwd || !isAbsolute(cwd) || cwd.includes('\0') || badWinPath(cwd)) return false
    const realBase = realPath(cwd)
    if (!realBase) return false
    kTarget = realPath(file, realBase)
    lexTarget = realPath(resolve(cwd, file))
  }
  return isInside(root, kTarget) && isInside(root, lexTarget)
}
