// Managed Agents events → Persona items and cards (docs/integrations/managed-agents.md "Mapping"). An event's seq is its
// position in the session's log (1-based); one event can yield several items, ids `<seq>#<index>`. Events nobody needs
// to read (status changes, spans, usage, confirmations) yield no item but still take a seq, so the log and the pages
// keep one numbering.

import { assistantPreview } from '../../runtime-sdk/lib/assistant-preview.js'
import { appToolName, clipInput, snippet } from '../../runtime-sdk/index.js'

// Words the hub recognizes and rewrites into the reader's language (docs/protocol/i18n.md, transcript.Runtime*):
// they stay in Chinese here on purpose.
export const NOTE_INTERRUPTED = '已中断' // i18n-ignore
export const NOTE_ERROR = '出错了' // i18n-ignore
export const NOTE_ERROR_LEAD = '出错了：' // i18n-ignore
export const NOTE_COMPACTED = '上下文已压缩' // i18n-ignore

const TOOL_USES = new Set(['agent.tool_use', 'agent.mcp_tool_use', 'agent.custom_tool_use'])
const TOOL_RESULTS = new Set(['agent.tool_result', 'agent.mcp_tool_result'])
const OWN_TOOLS = { AskQuestion: 'AskUserQuestion', view_multimodal: 'Read' }

const textOf = (content) =>
  typeof content === 'string' ? content : Array.isArray(content) ? content.filter((b) => b?.type === 'text' && typeof b.text === 'string').map((b) => b.text).join('') : ''

/** Tool input as an object the apps can draw (Claude's parameter names pass as they are). */
function inputOf(raw) {
  let input = raw
  if (typeof raw === 'string') {
    try {
      input = raw === '' ? {} : JSON.parse(raw)
    } catch {
      input = { arguments: raw }
    }
  }
  if (input === null || typeof input !== 'object' || Array.isArray(input)) input = { value: input }
  return clipInput(input)
}

const toolName = (ev) => {
  const name = ev.name ?? ev.tool_name ?? 'tool'
  return ev.type === 'agent.mcp_tool_use' && ev.mcp_server_name ? `mcp__${ev.mcp_server_name}__${name}` : appToolName(name, OWN_TOOLS)
}

function imageBlock(b) {
  const src = b?.source ?? {}
  if (src.type === 'base64' && typeof src.data === 'string') return { mime: src.media_type ?? 'image/png', data: src.data }
  return undefined // a file or URL source: not fetched (the apps show the text around it)
}

/**
 * Items for one event. `images` collects image data by item id (served by session.image; pages carry placeholders).
 */
export function eventItems(ev, seq, images) {
  const at = ev.processed_at ?? ev.created_at ?? new Date().toISOString()
  const out = []
  const push = (i, item) => out.push({ id: `${seq}#${i}`, seq, ts: at, ...item })
  switch (ev.type) {
    case 'user.message': {
      ;(Array.isArray(ev.content) ? ev.content : [{ type: 'text', text: textOf(ev.content) }]).forEach((b, i) => {
        if (b?.type === 'text' && typeof b.text === 'string' && b.text.trim() !== '') push(i, { role: 'user', kind: 'text', text: b.text })
        else if (b?.type === 'image') {
          const img = imageBlock(b)
          if (img) {
            images.set(`${seq}#${i}`, img)
            push(i, { role: 'user', kind: 'image', image: { mime: img.mime, data: '', lazy: true } })
          }
        }
      })
      break
    }
    case 'agent.message': {
      const text = textOf(ev.content)
      if (text.trim() !== '') push(0, { role: 'assistant', kind: 'text', text })
      break
    }
    case 'agent.thinking': {
      const text = textOf(ev.content) || (typeof ev.thinking === 'string' ? ev.thinking : '')
      if (text.trim() !== '') push(0, { role: 'assistant', kind: 'thinking', text })
      break
    }
    case 'agent.tool_use':
    case 'agent.mcp_tool_use':
    case 'agent.custom_tool_use':
      push(0, { role: 'assistant', kind: 'tool_use', tool_use_id: ev.id, tool_name: toolName(ev), tool_input: inputOf(ev.input) })
      break
    case 'agent.tool_result':
    case 'agent.mcp_tool_result':
    case 'user.tool_result':
      push(0, { role: 'user', kind: 'tool_result', tool_use_id: ev.tool_use_id ?? ev.mcp_tool_use_id, text: textOf(ev.content), is_error: ev.is_error === true })
      break
    case 'user.custom_tool_result':
      push(0, { role: 'user', kind: 'tool_result', tool_use_id: ev.custom_tool_use_id, text: textOf(ev.content), is_error: ev.is_error === true })
      break
    case 'user.tool_confirmation':
      // A denial says why in the tool's result; the agent's own tool_result follows for an allow.
      if (ev.result === 'deny') push(0, { role: 'user', kind: 'tool_result', tool_use_id: ev.tool_use_id, text: ev.deny_message ?? '', is_error: true })
      break
    case 'user.interrupt':
      push(0, { role: 'system', kind: 'notice', text: NOTE_INTERRUPTED })
      break
    case 'session.error': {
      const err = ev.error ?? {}
      if (err.retry_status?.type === 'retrying') break // the server tries again; the turn goes on
      const msg = typeof err.message === 'string' ? err.message : typeof ev.message === 'string' ? ev.message : ''
      push(0, { role: 'system', kind: 'notice', text: msg ? NOTE_ERROR_LEAD + snippet(msg) : NOTE_ERROR })
      break
    }
    case 'agent.thread_context_compacted':
      push(0, { role: 'system', kind: 'notice', text: NOTE_COMPACTED, compact: { trigger: 'auto' } })
      break
    default:
      break
  }
  return out
}

/** The session's state as its log says it: working, done, terminated, or unknown (no status event yet). */
export function logStatus(events) {
  for (let i = events.length - 1; i >= 0; i--) {
    const t = events[i]?.type
    if (t === 'session.status_running' || t === 'session.status_rescheduled') return 'working'
    if (t === 'session.status_terminated') return 'terminated'
    if (t === 'session.status_idle') {
      const reason = events[i].stop_reason?.type
      return reason === 'requires_action' ? 'waiting' : 'done'
    }
    if (t === 'user.message') return 'working' // taken, the run not started yet
  }
  return 'unknown'
}

/** The newest reply's first line, for the list. */
export function lastText(events) {
  for (let i = events.length - 1; i >= 0; i--) {
    if (events[i]?.type === 'agent.message') {
      const preview = assistantPreview(textOf(events[i].content))
      if (preview) return preview
    }
    if (events[i]?.type === 'user.message') return ''
  }
  return ''
}

/**
 * What the agent waits on a person for, and nobody answered yet: a tool call whose policy asks (a permission card),
 * and a question or plan tool (a question or plan card). Calls of other custom tools belong to whoever runs them.
 */
export function pendingAsks(events, { questionTools, planTools }) {
  const answered = new Set()
  for (const ev of events) {
    if (ev?.type === 'user.tool_confirmation' || ev?.type === 'user.tool_result') answered.add(ev.tool_use_id)
    if (ev?.type === 'user.custom_tool_result') answered.add(ev.custom_tool_use_id)
    if (TOOL_RESULTS.has(ev?.type)) answered.add(ev.tool_use_id ?? ev.mcp_tool_use_id)
  }
  // Only what the latest pause names, when it names anything: an older ask the server dropped (an interrupt) is not one.
  let blocking
  for (let i = events.length - 1; i >= 0; i--) {
    const ev = events[i]
    if (ev?.type === 'session.status_idle' || ev?.type === 'session.thread_status_idle') {
      if (ev.stop_reason?.type === 'requires_action' && Array.isArray(ev.stop_reason.event_ids)) blocking = new Set(ev.stop_reason.event_ids)
      else if (ev.type === 'session.status_idle') blocking = new Set()
      break
    }
    if (ev?.type === 'user.interrupt') {
      blocking = new Set()
      break
    }
  }
  const out = []
  for (const ev of events) {
    if (!TOOL_USES.has(ev?.type) || typeof ev.id !== 'string' || answered.has(ev.id)) continue
    if (blocking && !blocking.has(ev.id)) continue
    const name = ev.name ?? ''
    if (ev.type === 'agent.custom_tool_use') {
      if (questionTools.includes(name)) out.push({ kind: 'question', event: ev })
      else if (planTools.includes(name)) out.push({ kind: 'plan', event: ev })
    } else if (ev.evaluated_permission === 'ask') {
      out.push({ kind: 'permission', event: ev })
    }
  }
  return out
}

/** The card Persona shows for a pending ask (without id and session, which the runtime adds). */
export function cardOf(ask) {
  const ev = ask.event
  const input = inputOf(ev.input)
  if (ask.kind === 'question') {
    const questions = (Array.isArray(input.questions) ? input.questions : [input]).map((q) => ({
      question: String(q.question ?? ''),
      header: typeof q.header === 'string' ? q.header : undefined,
      multi_select: q.multiSelect === true || q.multi_select === true,
      options: (Array.isArray(q.options) ? q.options : []).map((o) => (typeof o === 'string' ? { label: o } : { label: String(o.label ?? ''), description: typeof o.description === 'string' ? o.description : undefined })),
    })).filter((q) => q.question)
    return { kind: 'question', tool_name: 'AskUserQuestion', tool_use_id: ev.id, questions }
  }
  if (ask.kind === 'plan') {
    const parts = [input.title && `# ${input.title}`, input.summary, input.plan].filter((x) => typeof x === 'string' && x.trim())
    return { kind: 'plan', tool_name: 'ExitPlanMode', tool_use_id: ev.id, plan: parts.join('\n\n') || JSON.stringify(input) }
  }
  const name = toolName(ev)
  const first = input.command ?? input.file_path ?? input.url ?? input.pattern ?? input.query ?? Object.values(input).find((v) => typeof v === 'string')
  return { kind: 'permission', tool_name: name, tool_use_id: ev.id, tool_input: input, summary: typeof first === 'string' ? snippet(first) : name }
}

/** The event that answers an ask with a Persona Resolution, or undefined when the resolution is no answer (dismiss). */
export function answerOf(ask, res) {
  const ev = ask.event
  const thread = ev.session_thread_id ? { session_thread_id: ev.session_thread_id } : {}
  if (!res || res.dismiss || res.cancel) return undefined
  if (ask.kind === 'permission') {
    if (res.allow === true) return { type: 'user.tool_confirmation', tool_use_id: ev.id, result: 'allow', ...thread }
    if (res.allow === false) return { type: 'user.tool_confirmation', tool_use_id: ev.id, result: 'deny', ...(res.message ? { deny_message: String(res.message) } : {}), ...thread }
    return undefined
  }
  let payload
  if (ask.kind === 'question') {
    if (!res.answers || typeof res.answers !== 'object') return undefined
    payload = { answers: res.answers }
  } else {
    if (res.approve === true) payload = { decision: 'approved', executionMode: res.mode ?? null, feedback: res.feedback ?? null }
    else if (res.approve === false) payload = { decision: 'changes_requested', feedback: res.feedback ?? '' }
    else return undefined
  }
  return { type: 'user.custom_tool_result', custom_tool_use_id: ev.id, content: [{ type: 'text', text: JSON.stringify(payload) }], ...thread }
}

/** Text of a streaming frame's delta (event_deltas), or ''. */
export function deltaText(frame) {
  const d = frame?.delta ?? {}
  if (typeof d.text === 'string') return d.text
  return textOf(d.content)
}
