// Mandatory Assistant shell gates.
// Match declared command names and script paths.
export function shellWordsRe(words) {
  const esc = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  const alts = (Array.isArray(words) ? words : [])
    .filter((w) => typeof w === 'string' && w !== '')
    .map((w) => (w.includes('/') ? `[^\\s;&|()\`"'<>]*${esc(w)}` : `(?:[^\\s;&|()\`"'<>]*/)?${esc(w)}`))
  return alts.length ? new RegExp(`(^|[\\s;&|(\`$"'><])(${alts.join('|')})([\\s;&|)\`"'<>]|$)`) : null
}

const SHELL_BINARIES = new Set(['sh', 'bash', 'zsh', 'dash', 'ksh', 'ash'])

const ANSI_C_ESCAPES = { a: '\x07', b: '\b', e: '\x1b', E: '\x1b', f: '\f', n: '\n', r: '\r', t: '\t', v: '\v', '\\': '\\', "'": "'", '"': '"', '?': '?' }

function skipLineCont(s, i) {
  while (s[i] === '\\' && s[i + 1] === '\n') i += 2
  return i
}

function readAnsiC(command, start) {
  const n = command.length
  let i = start
  let text = ''
  while (i < n && command[i] !== "'") {
    if (command[i] === '\\' && i + 1 < n) {
      const next = command[i + 1]
      if (next === 'x') {
        const hex = /^[0-9a-fA-F]{1,2}/.exec(command.slice(i + 2))
        if (hex) {
          text += String.fromCharCode(parseInt(hex[0], 16))
          i += 2 + hex[0].length
          continue
        }
      }
      const oct = /^[0-7]{1,3}/.exec(command.slice(i + 1))
      if (oct) {
        text += String.fromCharCode(parseInt(oct[0], 8))
        i += 1 + oct[0].length
        continue
      }
      if (next === 'u' || next === 'U') {
        const uni = (next === 'u' ? /^[0-9a-fA-F]{1,4}/ : /^[0-9a-fA-F]{1,8}/).exec(command.slice(i + 2))
        if (uni) {
          const cp = parseInt(uni[0], 16)
          if (cp <= 0x10ffff) text += String.fromCodePoint(cp)
          i += 2 + uni[0].length
          continue
        }
      }
      text += ANSI_C_ESCAPES[next] ?? next
      i += 2
      continue
    }
    text += command[i++]
  }
  if (i < n) i++
  return { text, end: i }
}

function readCommandSub(command, start) {
  const n = command.length
  let i = start
  let depth = 1
  while (i < n && depth > 0) {
    const ch = command[i]
    if (ch === '\\' && i + 1 < n) {
      i += 2
      continue
    }
    if (ch === '$') {
      const next = skipLineCont(command, i + 1)
      if (command[next] === "'") {
        i = readAnsiC(command, next + 1).end
        continue
      }
      if (command[next] === '(') {
        i = readCommandSub(command, next + 1).end
        continue
      }
    }
    if (ch === "'") {
      i++
      while (i < n && command[i] !== "'") i++
      if (i < n) i++
      continue
    }
    if (ch === '"') {
      i++
      while (i < n && command[i] !== '"') {
        if (command[i] === '\\' && i + 1 < n) i += 2
        else if (command[i] === '$') {
          const next = skipLineCont(command, i + 1)
          if (command[next] === '(') i = readCommandSub(command, next + 1).end
          else i++
        } else if (command[i] === '`') {
          i++
          while (i < n && command[i] !== '`') i += command[i] === '\\' && i + 1 < n ? 2 : 1
          if (i < n) i++
        } else i++
      }
      if (i < n) i++
      continue
    }
    if (ch === '`') {
      i++
      while (i < n && command[i] !== '`') i += command[i] === '\\' && i + 1 < n ? 2 : 1
      if (i < n) i++
      continue
    }
    if (ch === '(') depth++
    else if (ch === ')') {
      depth--
      if (depth === 0) return { body: command.slice(start, i), end: i + 1 }
    }
    i++
  }
  return { body: command.slice(start, i), end: i }
}

// Parse shell words/operators and retain nested command substitutions.
function tokenizeShell(command) {
  const tokens = []
  const embedded = []
  let cur = ''
  let hasToken = false
  let i = 0
  const n = command.length
  const flush = () => {
    if (hasToken) {
      tokens.push(cur)
      cur = ''
      hasToken = false
    }
  }
  while (i < n) {
    const ch = command[i]
    // Token boundary.
    if (/[\s;&|()<>]/.test(ch)) {
      flush()
      i++
      continue
    }
    if (ch === '`') {
      i++
      const start = i
      while (i < n && command[i] !== '`') {
        if (command[i] === '\\' && i + 1 < n) i += 2
        else i++
      }
      embedded.push(command.slice(start, i))
      if (i < n) i++
      continue
    }
    if (ch === '$') {
      const next = skipLineCont(command, i + 1)
      if (command[next] === '(') {
        const sub = readCommandSub(command, next + 1)
        embedded.push(sub.body)
        i = sub.end
        continue
      }
      if (command[next] === "'") {
        hasToken = true
        const ansi = readAnsiC(command, next + 1)
        cur += ansi.text
        i = ansi.end
        continue
      }
    }
    if (ch === "'") {
      hasToken = true
      i++
      while (i < n && command[i] !== "'") {
        cur += command[i++]
      }
      if (i < n) i++
      continue
    }
    if (ch === '"') {
      hasToken = true
      i++
      while (i < n && command[i] !== '"') {
        if (command[i] === '\\' && i + 1 < n) {
          const next = command[i + 1]
          if (next === '\n') {
            i += 2
            continue
          }
          cur += (next === '"' || next === '\\' || next === '$' || next === '`') ? next : `\\${next}`
          i += 2
          continue
        }
        if (command[i] === '$') {
          const next = skipLineCont(command, i + 1)
          if (command[next] === '(') {
            const sub = readCommandSub(command, next + 1)
            embedded.push(sub.body)
            i = sub.end
            continue
          }
        }
        if (command[i] === '`') {
          i++
          const start = i
          while (i < n && command[i] !== '`') {
            if (command[i] === '\\' && i + 1 < n) i += 2
            else i++
          }
          embedded.push(command.slice(start, i))
          if (i < n) i++
          continue
        }
        cur += command[i++]
      }
      if (i < n) i++
      continue
    }
    if (ch === '\\' && i + 1 < n) {
      if (command[i + 1] === '\n') {
        i += 2
        continue
      }
      hasToken = true
      cur += command[i + 1]
      i += 2
      continue
    }
    hasToken = true
    cur += ch
    i++
  }
  flush()
  return { tokens, embedded }
}

function tokenMatchesWord(token, word) {
  if (!token || !word) return false
  const normTok = token.replace(/\\/g, '/')
  const normWord = word.replace(/\\/g, '/')
  if (normTok === normWord) return true
  return normTok.endsWith(`/${normWord}`)
}

function basenameOf(token) {
  const norm = token.replace(/\\/g, '/')
  const idx = norm.lastIndexOf('/')
  return idx >= 0 ? norm.slice(idx + 1) : norm
}

// Build command gates from the current tool inventory.
export function extractShellGates(tools) {
  const out = []
  for (const t of Array.isArray(tools) ? tools : []) {
    if (!Array.isArray(t?.shell) || t.shell.length === 0) continue
    const words = t.shell.filter((w) => typeof w === 'string' && w !== '')
    const re = shellWordsRe(words)
    if (re) out.push({ name: t.name, command: words[0], words, re })
  }
  return out
}

function shellCommandArgs(tokens, start, base) {
  const isZsh = base === 'zsh'
  const isKsh = base === 'ksh'
  const shortRe = isZsh ? /^[-+][A-Za-z0-9][A-Za-z0-9_-]*$/ : /^[-+][A-Za-z][A-Za-z0-9_-]*$/
  const out = []
  const scan = (j, hasC, hasS, mode) => {
    for (; j < tokens.length; j++) {
      const arg = tokens[j]
      if (arg === '+') continue
      if (arg === '--' || arg === '-') {
        if ((hasC || (isKsh && !hasS)) && j + 1 < tokens.length) out.push(tokens[j + 1])
        return
      }
      if (arg.startsWith('--') && arg.length > 2) {
        if (arg === '--rcfile' || arg === '--init-file' || (isZsh && arg === '--emulate')) j++
        continue
      }
      if (shortRe.test(arg)) {
        const isDash = arg[0] === '-'
        if (isZsh) {
          for (let k = 1; k < arg.length; k++) {
            const ch = arg[k]
            if (ch === 'o') {
              if (k === arg.length - 1) j++
              break
            }
            if (isDash && ch === 'c') hasC = true
          }
          continue
        }
        if (isKsh) {
          for (let k = 1; k < arg.length; k++) {
            const ch = arg[k]
            if (ch === 'o') {
              if (k === arg.length - 1) {
                const next = tokens[j + 1]
                if (next === '-' || (next && next[0] !== '-' && next[0] !== '+')) j++
              }
              break
            }
            if (ch === 'R') {
              if (k === arg.length - 1) j++
              break
            }
            if (isDash && ch === 'c') hasC = true
            if (ch === 's') hasS = isDash
          }
          continue
        }
        let oIdx = -1
        for (let k = 1; k < arg.length; k++) {
          if (arg[k] === 'o' || arg[k] === 'O') {
            oIdx = k
            break
          }
        }
        const pureLetters = /^[-+][A-Za-z]+$/.test(arg)
        if (oIdx >= 0 && oIdx < arg.length - 1 && mode === 'auto') {
          if (pureLetters) scan(j, hasC, hasS, 'cluster')
          scan(j, hasC, hasS, 'attached')
          return
        }
        if (mode === 'attached' || !pureLetters) {
          for (let k = 1; k < arg.length; k++) {
            const ch = arg[k]
            if (ch === 'o' || ch === 'O') {
              if (k === arg.length - 1) j++
              break
            }
            if (isDash && ch === 'c') hasC = true
          }
          continue
        }
        let extra = 0
        for (let k = 1; k < arg.length; k++) {
          const ch = arg[k]
          if (ch === 'o' || ch === 'O') {
            extra++
          } else if (isDash && ch === 'c') {
            hasC = true
          }
        }
        j += extra
        continue
      }
      if (hasC || (isKsh && !hasS)) out.push(arg)
      return
    }
  }
  scan(start, false, false, 'auto')
  return out
}

// Find a gate hit, including nested commands.
export function matchShellGate(command, gates, depth = 0) {
  if (depth > 4 || typeof command !== 'string' || !command.trim() || !Array.isArray(gates) || gates.length === 0) {
    return undefined
  }
  const { tokens, embedded } = tokenizeShell(command)
  for (const tok of tokens) {
    for (const g of gates) {
      const words = Array.isArray(g.words) ? g.words : (g.command ? [g.command] : [])
      if (words.some((w) => tokenMatchesWord(tok, w))) return g
    }
  }
  // Check eval, nested shells and substitutions.
  for (let i = 0; i < tokens.length; i++) {
    const base = basenameOf(tokens[i])
    if (base === 'eval' && i + 1 < tokens.length) {
      const sub = matchShellGate(tokens.slice(i + 1).join(' '), gates, depth + 1)
      if (sub) return sub
    } else if (SHELL_BINARIES.has(base)) {
      for (const inner of shellCommandArgs(tokens, i + 1, base)) {
        const sub = matchShellGate(inner, gates, depth + 1)
        if (sub) return sub
      }
    }
  }
  for (const inner of embedded) {
    const sub = matchShellGate(inner, gates, depth + 1)
    if (sub) return sub
  }
  return gates.find((g) => g.re?.test(command))
}
