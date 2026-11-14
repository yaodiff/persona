import { SNAPSHOT_RE, SNAPSHOT_END_RE } from '../../runtime-sdk/lib/compat.js'
// Mapping between Antigravity CLI (agy) transcripts / ACP updates and Persona runtime items.
import { existsSync, readFileSync, statSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { basename, join } from 'node:path'
import { appToolName, clipInput, light, snippet } from '../../runtime-sdk/index.js'

export const RUNTIME = 'agy'
export const LABEL = 'Antigravity'
export const PREFIX = 'agy-'

const OWN_TOOLS = {
  run_command: 'Bash',
  view_file: 'Read',
  read_file: 'Read',
  write_to_file: 'Write',
  write_file: 'Write',
  replace_file_content: 'Edit',
  edit_file: 'Edit',
  grep_search: 'Grep',
  find_by_name: 'Glob',
  search_web: 'WebSearch',
  read_url_content: 'WebFetch',
  ask_question: 'AskUserQuestion',
  invoke_subagent: 'Agent',
}

export function toPersonaId(rawId) {
  const s = String(rawId ?? '').trim()
  if (!s) return ''
  return s.startsWith(PREFIX) ? s : `${PREFIX}${s}`
}

export function toAgyId(personaId) {
  const s = String(personaId ?? '').trim()
  return s.startsWith(PREFIX) ? s.slice(PREFIX.length) : s
}

const USER_REQ_RE = /<USER_REQUEST>\s*([\s\S]*?)\s*<\/USER_REQUEST>/

export function cleanUserText(raw) {
  if (typeof raw !== 'string') return ''
  const m = USER_REQ_RE.exec(raw)
  let body = m ? m[1] : raw
  let prev
  do {
    prev = body
    body = body.replace(SNAPSHOT_RE, '').replace(SNAPSHOT_END_RE, '')
  } while (body !== prev)
  return body.trim()
}

export function mapToolName(name) {
  return appToolName(name, OWN_TOOLS)
}

export function mapToolInput(name, args) {
  if (!args || typeof args !== 'object') return {}
  switch (name) {
    case 'run_command':
      return clipInput({
        command: args.CommandLine ?? args.command ?? '',
        description: args.toolSummary ?? args.Cwd ?? '',
      })
    case 'view_file':
    case 'read_file':
      return clipInput({
        file_path: args.AbsolutePath ?? args.Path ?? args.file_path ?? '',
      })
    case 'write_to_file':
    case 'write_file':
      return clipInput({
        file_path: args.TargetFile ?? args.Path ?? args.file_path ?? '',
        content: args.CodeContent ?? args.Content ?? args.content ?? '',
      })
    case 'replace_file_content':
    case 'edit_file':
      return clipInput({
        file_path: args.TargetFile ?? args.Path ?? args.file_path ?? '',
        old_string: args.TargetContent ?? args.Target ?? args.old_string ?? '',
        new_string: args.ReplacementContent ?? args.Replacement ?? args.new_string ?? '',
      })
    case 'grep_search':
      return clipInput({
        pattern: args.Query ?? args.Pattern ?? args.pattern ?? '',
        path: args.SearchPath ?? args.Path ?? args.path ?? '',
      })
    case 'find_by_name':
      return clipInput({
        pattern: args.Pattern ?? args.pattern ?? '',
        path: args.SearchDirectory ?? args.Path ?? args.path ?? '',
      })
    case 'search_web':
      return clipInput({
        query: args.query ?? args.Query ?? '',
      })
    case 'read_url_content':
      return clipInput({
        url: args.Url ?? args.url ?? '',
      })
    case 'invoke_subagent':
      return clipInput({
        description: args.toolSummary ?? 'subagent',
        prompt: typeof args.Subagents === 'string' ? args.Subagents : JSON.stringify(args.Subagents ?? ''),
      })
    default:
      return clipInput(args)
  }
}

export function cleanGenericOutput(content) {
  if (typeof content !== 'string') return ''
  // Strip run_command metadata header if followed by Output:
  const outIdx = content.indexOf('\nOutput:\n')
  if (outIdx >= 0 && content.startsWith('Created At:')) {
    return content.slice(outIdx + '\nOutput:\n'.length)
  }
  return content
}

/**
 * Parse a transcript.jsonl file (or array of parsed step objects) into Persona items.
 * @param {object[]} steps
 * @param {{ brainDir?: string, full?: boolean }} [opts]
 */
export function stepsToItems(steps, opts = {}) {
  const items = []
  const pendingCalls = []
  let maxSeq = 0
  let lastText = ''
  let turnError
  let afterWork = false

  for (let i = 0; i < steps.length; i++) {
    const step = steps[i]
    if (!step || typeof step !== 'object') continue
    const idx = typeof step.step_index === 'number' ? step.step_index : i
    const seq = idx + 1
    if (seq > maxSeq) maxSeq = seq
    const ts = step.created_at || new Date().toISOString()

    switch (step.type) {
      case 'USER_INPUT': {
        if (step.source && step.source !== 'USER_EXPLICIT') break
        // A new prompt answers the previous reply. Only assistant words can ask the user for a response.
        lastText = ''
        turnError = undefined
        afterWork = false
        const text = cleanUserText(step.content)
        if (!text) break
        const item = { id: `${seq}#0`, seq, role: 'user', kind: 'text', text, ts }
        items.push(opts.full ? item : light(item))
        break
      }
      case 'PLANNER_RESPONSE': {
        turnError = undefined
        let sub = 0
        if (typeof step.thinking === 'string' && step.thinking.trim()) {
          const item = { id: `${seq}#${sub++}`, seq, role: 'assistant', kind: 'thinking', text: step.thinking.trim(), ts }
          items.push(opts.full ? item : light(item))
        }
        if (typeof step.content === 'string' && step.content.trim()) {
          const text = step.content.trim()
          lastText = snippet(text)
          afterWork = true
          const item = { id: `${seq}#${sub++}`, seq, role: 'assistant', kind: 'text', text, ts }
          items.push(opts.full ? item : light(item))
        }
        const calls = Array.isArray(step.tool_calls) ? step.tool_calls : []
        if (calls.length) afterWork = true
        for (let k = 0; k < calls.length; k++) {
          const tc = calls[k]
          if (!tc || typeof tc !== 'object') continue
          const callId = `call_${seq}_${k}`
          const toolName = mapToolName(tc.name || 'tool')
          const toolInput = mapToolInput(tc.name || 'tool', tc.args)
          pendingCalls.push({ callId, idx })
          const item = {
            id: `${seq}#${sub++}`,
            seq,
            role: 'assistant',
            kind: 'tool_use',
            tool_name: toolName,
            tool_input: toolInput,
            tool_use_id: callId,
            ts,
          }
          items.push(opts.full ? item : light(item))
        }
        break
      }
      case 'GENERIC': {
        afterWork = true
        const paired = pendingCalls.shift()
        const callId = paired ? paired.callId : `call_${seq}_0`
        let rawOut = step.error || step.content || ''
        if (opts.full && opts.brainDir && Array.isArray(step.truncated_fields) && step.truncated_fields.includes('content')) {
          const outFile = join(opts.brainDir, '.system_generated', 'steps', String(idx), 'output.txt')
          if (existsSync(outFile)) {
            try {
              rawOut = readFileSync(outFile, 'utf8')
            } catch {}
          }
        }
        const text = step.error ? String(step.error) : cleanGenericOutput(rawOut)
        const isError = Boolean(step.error) || step.status === 'ERROR'
        const resItem = {
          id: `${seq}#0`,
          seq,
          role: 'user',
          kind: 'tool_result',
          tool_use_id: callId,
          text,
          is_error: isError,
          ts,
        }
        items.push(opts.full ? resItem : light(resItem))
        if (Array.isArray(step.media)) {
          step.media.forEach((m, mi) => {
            if (m && typeof m.mime_type === 'string' && m.mime_type.startsWith('image/')) {
              items.push({
                id: `${seq}#img${mi}`,
                seq,
                role: 'user',
                kind: 'image',
                mime: m.mime_type,
                lazy: true,
                ts,
              })
            }
          })
        }
        break
      }
      case 'ERROR_MESSAGE': {
        const text = step.error || step.content || 'Error'
        turnError = { turn_error: String(text), ...step.turn_error, turn_error_after_work: afterWork }
        // Clients display this shared API-error format outside the collapsed work summary.
        const message = String(text).startsWith('API Error:') ? String(text) : `API Error: ${text}`
        items.push({ id: `${seq}#0`, seq, role: 'assistant', kind: 'text', text: message, ts })
        break
      }
      default:
        break
    }
  }

  return { items, toSeq: maxSeq, lastText, turnError }
}

const transcriptCache = new Map()
const transcriptRevisions = new Map()
const MAX_CACHED_TRANSCRIPTS = 16
const MAX_TRANSCRIPT_REVISIONS = 256
const MAX_TRANSCRIPT_CACHE_BYTES = 64 * 1024 * 1024
let transcriptCacheBytes = 0

export function readTranscriptSteps(brainDir, full = false) {
  return readTranscript(brainDir, full).steps
}

export function readTranscript(brainDir, full = false) {
  const file = join(brainDir, '.system_generated', 'logs', full ? 'transcript_full.jsonl' : 'transcript.jsonl')
  const fallback = full ? join(brainDir, '.system_generated', 'logs', 'transcript.jsonl') : null
  const target = existsSync(file) ? file : fallback && existsSync(fallback) ? fallback : null
  if (!target) return { steps: [], branch: '' }
  try {
    const info = statSync(target, { bigint: true })
    const version = [info.dev, info.ino, info.size, info.mtimeNs, info.ctimeNs].join(':')
    // Fast same-size native rewrites can share a filesystem timestamp tick.
    const settled = Math.max(Number(info.mtimeMs), Number(info.ctimeMs)) <= Date.now() - 1000
    const remembered = transcriptRevisions.get(target)
    if (remembered) {
      transcriptRevisions.delete(target)
      transcriptRevisions.set(target, remembered)
    }
    const cached = transcriptCache.get(target)
    if (cached) {
      transcriptCache.delete(target)
      transcriptCacheBytes -= cached.size
      if (settled && cached.version === version) {
        transcriptCache.set(target, cached)
        transcriptCacheBytes += cached.size
        return cached.transcript
      }
    }
    const raw = readFileSync(target, 'utf8')
    const out = []
    let branch = ''
    let revision = ''
    for (const line of raw.split('\n')) {
      const trimmed = line.trim()
      if (!trimmed) continue
      try {
        const step = JSON.parse(trimmed)
        const previous = out.at(-1)
        const index = step?.step_index
        if (Number.isInteger(index) && Number.isInteger(previous?.step_index) && index <= previous.step_index) {
          // Native exports append replacement steps after a rewind. A status update keeps the step's identity.
          if (index < previous.step_index || step.type !== previous.type || step.created_at !== previous.created_at) {
            branch = createHash('sha256').update(trimmed).digest('hex').slice(0, 20)
            revision = ''
          } else if (JSON.stringify(previous) !== JSON.stringify(step)) {
            // A planner step can gain its completed response while keeping its index and branch.
            revision = createHash('sha256').update(JSON.stringify(step)).digest('hex').slice(0, 20)
          }
          while (out.length && out.at(-1).step_index >= index) out.pop()
        }
        out.push(step)
      } catch {}
    }
    const nativeRevision = revision
    const hash = createHash('sha256').update(raw).digest('hex')
    if (remembered && remembered.branch === branch) {
      const appended = raw.length >= remembered.length &&
        createHash('sha256').update(raw.slice(0, remembered.length)).digest('hex') === remembered.hash
      if (!appended) revision = hash.slice(0, 20)
      else if (nativeRevision === remembered.nativeRevision) revision = remembered.revision
    }
    // Keep small revision records independently of the parsed-body LRU, including fresh native rewrites.
    transcriptRevisions.set(target, { length: raw.length, hash, branch, nativeRevision, revision })
    while (transcriptRevisions.size > MAX_TRANSCRIPT_REVISIONS) transcriptRevisions.delete(transcriptRevisions.keys().next().value)
    const transcript = { steps: out, branch, revision }
    const size = Number(info.size)
    if (settled && size <= MAX_TRANSCRIPT_CACHE_BYTES) {
      while (transcriptCache.size >= MAX_CACHED_TRANSCRIPTS || transcriptCacheBytes + size > MAX_TRANSCRIPT_CACHE_BYTES) {
        const oldest = transcriptCache.keys().next().value
        transcriptCacheBytes -= transcriptCache.get(oldest).size
        transcriptCache.delete(oldest)
      }
      transcriptCache.set(target, { version, size, transcript })
      transcriptCacheBytes += size
    }
    return transcript
  } catch {
    return full ? readTranscript(brainDir, false) : { steps: [], branch: '' }
  }
}

export function nativeWorking(summary) {
  return !summary?.killed && (summary?.status === 'CASCADE_RUN_STATUS_RUNNING' || Boolean(summary?.not_fully_idle))
}

/** Working requires live execution evidence. A completed planner message can be progress narration. */
export function transcriptStatus(steps, summary) {
  if (summary?.killed) return 'idle'
  if (nativeWorking(summary)) return 'working'
  const last = steps.findLast((step) => ['USER_INPUT', 'PLANNER_RESPONSE', 'GENERIC', 'ERROR_MESSAGE'].includes(step?.type))
  if (!last) return 'unknown'
  if (last.type === 'ERROR_MESSAGE' || ['ERROR', 'CANCELLED', 'CANCELED'].includes(last.status)) return 'idle'
  // A summary from before a newly exported step cannot close that newer work.
  const idle = summary?.status === 'CASCADE_RUN_STATUS_IDLE' && Number.isInteger(last.step_index) && summary.step_count > last.step_index
  const reply = last.type === 'PLANNER_RESPONSE' && last.status === 'DONE' && typeof last.content === 'string' && last.content.trim() && !last.tool_calls?.length
  if (idle) return reply ? 'done' : 'idle'
  return 'unknown'
}

export function workspacePathOf(summary, fallbackCwd = '') {
  if (Array.isArray(summary?.WorkspaceURIs) && summary.WorkspaceURIs.length > 0) {
    const u = String(summary.WorkspaceURIs[0])
    if (u.startsWith('file://')) {
      try {
        return decodeURIComponent(new URL(u).pathname)
      } catch {
        return u.slice('file://'.length)
      }
    }
    return u
  }
  return fallbackCwd
}

export function personaModeToAgy(mode) {
  switch (mode) {
    case 'plan':
      return 'plan'
    case 'acceptEdits':
      return 'accept-edits'
    case 'auto':
      return 'auto'
    case 'bypassPermissions':
      return 'accept-edits'
    case 'default':
    default:
      return 'default'
  }
}

export function agyModeToPersona(mode) {
  switch (mode) {
    case 'auto':
      return 'auto'
    case 'plan':
      return 'plan'
    case 'accept-edits':
      return 'acceptEdits'
    default:
      return 'default'
  }
}
