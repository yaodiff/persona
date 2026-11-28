// Pure mapping from pi session entries to Persona items (docs/integrations/pi.md "Mapping"). No I/O: the runtime (pi.js) hands in the
// session's entries (file order) and its current branch; the tests feed recorded shapes.
//
// seq: an entry's 1-based position in the session file (pi appends only, so it is stable and grows). A page shows only
// the entries on the current branch (pi's sessions are trees: editing an earlier message branches off); history names
// the branch, so moving to another one makes the app reload the page instead of splicing two branches together.

import { customTypeMatches } from './compat.js'
import { assistantPreview } from '../../runtime-sdk/lib/assistant-preview.js'
import { appToolName, clipInput, snippet } from '../../runtime-sdk/index.js'

// pi's tools under the names the apps draw: the SDK's common ones, plus pi's own (toolInput renames the arguments).
const OWN_TOOLS = { find: 'Glob', ask_user: 'AskUserQuestion' }

/** Tool name the apps render. */
export const toolName = (name) => appToolName(name, OWN_TOOLS)

// Where a session last ran, as the extension records it in the session (a custom entry, not in the model's context):
// "herdr", "terminal" (pi's TUI outside herdr) or "pi-web". A session that ran in a terminal is resumed in the
// machine's terminal engine (herdr), one from pi-web in pi-web (docs/integrations/pi.md "Who manages sessions, and where offline sessions resume").
export const HOST_ENTRY = 'persona-host'

// A tool the user allowed for the rest of a session on the phone ("Always allow" on an approval card): a custom entry too, so a
// session resumed in another pi process does not ask about it again.
export const ALLOW_ENTRY = 'persona-allow'

/** Whether a session's entries hold "Always allow" for tool (pi's own name). */
export function alwaysAllowed(entries, tool) {
  return entries.some((e) => e?.type === 'custom' && customTypeMatches(e.customType, ALLOW_ENTRY) && e.data?.tool === tool)
}

/** The host a session's entries last recorded, undefined when none did. */
export function lastHost(entries) {
  for (let i = entries.length - 1; i >= 0; i--) {
    const e = entries[i]
    if (e?.type === 'custom' && customTypeMatches(e.customType, HOST_ENTRY) && typeof e.data?.host === 'string') return e.data.host
  }
  return undefined
}

/** A pi tool call's arguments in the shape the apps read for that tool (Claude's argument names). */
export function toolInput(name, args) {
  const a = args && typeof args === 'object' ? args : {}
  switch (name) {
    case 'read':
      return clipInput(dropUndefined({ file_path: a.path, offset: a.offset, limit: a.limit }))
    case 'write':
      return clipInput(dropUndefined({ file_path: a.path, content: a.content }))
    case 'edit': {
      if (!Array.isArray(a.edits)) {
        return clipInput(dropUndefined({ file_path: a.path, old_string: a.oldText, new_string: a.newText }))
      }
      if (a.edits.length === 1) {
        return clipInput(dropUndefined({ file_path: a.path, old_string: a.edits[0]?.oldText, new_string: a.edits[0]?.newText }))
      }
      return clipInput(dropUndefined({ file_path: a.path, edits: a.edits.map((e) => dropUndefined({ old_string: e?.oldText, new_string: e?.newText })) }))
    }
    default:
      return clipInput(a)
  }
}

/** The apps' tool name for a call, telling a multi-edit (Claude's MultiEdit) from a single one. */
export function callName(name, args) {
  if (name === 'edit' && Array.isArray(args?.edits) && args.edits.length > 1) return 'MultiEdit'
  return toolName(name)
}

function dropUndefined(o) {
  return Object.fromEntries(Object.entries(o).filter(([, v]) => v !== undefined))
}

const ts = (ms) => new Date(typeof ms === 'number' ? ms : Date.parse(ms) || Date.now()).toISOString()

function textOf(content) {
  if (typeof content === 'string') return content
  return (content ?? []).filter((b) => b?.type === 'text').map((b) => b.text).join('\n')
}

/**
 * The items of one entry at seq (its 1-based file position). Images are placeholders (lazy): their data comes from
 * session.image. With full, nothing is cut (session.item); otherwise the SDK's page cuts long text.
 */
export function entryItems(entry, seq) {
  const out = []
  const at = ts(entry?.timestamp)
  const add = (it) => out.push({ id: `${seq}#${out.length}`, seq, ts: at, ...it })
  if (entry?.type === 'compaction') {
    add({ role: 'system', kind: 'notice', text: '已压缩上下文' })
    return out
  }
  if (entry?.type === 'branch_summary') {
    add({ role: 'system', kind: 'notice', text: '从另一个分支回来了' })
    return out
  }
  if (entry?.type === 'custom_message' && entry.display !== false) {
    const t = textOf(entry.content)
    if (t) add({ role: 'system', kind: 'notice', text: snippet(t) })
    return out
  }
  if (entry?.type !== 'message') return out // model / thinking level changes, labels, session names, usage…
  const m = entry.message ?? {}
  switch (m.role) {
    case 'user': {
      const content = typeof m.content === 'string' ? [{ type: 'text', text: m.content }] : m.content ?? []
      for (const b of content) {
        if (b?.type === 'text' && b.text) add({ role: 'user', kind: 'text', text: b.text })
        else if (b?.type === 'image') add({ role: 'user', kind: 'image', image: placeholder(b.mimeType) })
      }
      break
    }
    case 'assistant': {
      for (const b of m.content ?? []) {
        if (b?.type === 'thinking' && b.thinking && !b.redacted) add({ role: 'assistant', kind: 'thinking', text: b.thinking })
        else if (b?.type === 'text' && b.text) add({ role: 'assistant', kind: 'text', text: b.text })
        else if (b?.type === 'toolCall') {
          add({ role: 'assistant', kind: 'tool_use', tool_name: callName(b.name, b.arguments), tool_use_id: b.id, tool_input: toolInput(b.name, b.arguments) })
        }
      }
      if (m.stopReason === 'aborted') add({ role: 'system', kind: 'notice', text: '已中断' })
      else if (m.stopReason === 'error') add({ role: 'system', kind: 'notice', text: m.errorMessage ? `出错了：${snippet(m.errorMessage)}` : '出错了' })
      // The output limit stopped the turn mid-way: say so, or the phone shows half a sentence (or nothing) as the answer.
      else if (m.stopReason === 'length') add({ role: 'system', kind: 'notice', text: '输出达到上限，这一轮停了' })
      break
    }
    case 'toolResult': {
      add({ role: 'user', kind: 'tool_result', tool_use_id: m.toolCallId, is_error: m.isError === true, text: textOf(m.content) })
      // The result's images are its call's too (the apps show them on its card).
      for (const b of Array.isArray(m.content) ? m.content : []) if (b?.type === 'image') add({ role: 'user', kind: 'image', tool_use_id: m.toolCallId, image: placeholder(b.mimeType) })
      break
    }
    case 'bashExecution': {
      // A command the user ran with ! in pi: a Bash call and its output.
      const id = `bash-${entry.id}`
      add({ role: 'assistant', kind: 'tool_use', tool_name: 'Bash', tool_use_id: id, tool_input: { command: m.command } })
      add({ role: 'user', kind: 'tool_result', tool_use_id: id, is_error: m.exitCode !== 0 && m.exitCode !== undefined, text: m.cancelled ? `${m.output ?? ''}\n（已取消）` : m.output ?? '' })
      break
    }
    case 'custom':
      if (m.display !== false) {
        const t = textOf(m.content)
        if (t) add({ role: 'system', kind: 'notice', text: snippet(t) })
      }
      break
    // system (the session's system prompt), branchSummary / compactionSummary (shown by their entries): not in the chat
  }
  return out
}

/** An image item's payload in a page: its bytes come from session.image (docs/protocol/protocol.md "Data types" Item.image). */
const placeholder = (mime) => ({ mime: typeof mime === 'string' && mime ? mime : 'image/png', data: '', lazy: true })

/** The image block the item at index of an entry stands for (as entryItems numbers them), undefined for others. */
export function imageOf(entry, index) {
  const m = entry?.message
  if (m?.role === 'user') {
    let i = 0
    for (const b of typeof m.content === 'string' ? [] : m.content ?? []) {
      if (!((b?.type === 'text' && b.text) || b?.type === 'image')) continue // no item for it
      if (i++ === index) return b.type === 'image' ? b : undefined
    }
  }
  if (m?.role === 'toolResult') return (Array.isArray(m.content) ? m.content : []).filter((b) => b?.type === 'image')[index - 1] // after the result
  return undefined
}

/**
 * The branch as items: every entry of the current branch with its file position as seq. entries is the session's
 * entries in file order (getEntries), branch the current branch root → leaf (getBranch).
 */
export function branchItems(entries, branch) {
  const pos = new Map(entries.map((e, i) => [e.id, i + 1]))
  const out = []
  for (const e of branch) {
    const seq = pos.get(e.id)
    if (seq) out.push(...entryItems(e, seq))
  }
  return out.sort((a, b) => a.seq - b.seq)
}

/**
 * The branch's identity: the latest entry on it that does not follow the entry before it in the file (where the
 * branch left the file's order: an edit, a jump back in the tree), and its leaf when the leaf has entries after it
 * (a jump back to an earlier point, nothing written yet). Unchanged while entries are only appended.
 */
export function branchKey(entries, branch) {
  const prev = new Map(entries.map((e, i) => [e.id, entries[i - 1]?.id ?? null]))
  let key = 'root'
  for (const e of branch) {
    if (e.parentId !== null && e.parentId !== undefined && prev.get(e.id) !== e.parentId) key = e.id
  }
  const leaf = branch.at(-1)?.id
  if (leaf !== undefined && entries.some((e) => e.parentId === leaf)) key += `@${leaf}`
  return key
}

/** The last thing the assistant said on a branch, one line (the list's last_text). */
export function lastText(branch) {
  for (let i = branch.length - 1; i >= 0; i--) {
    const m = branch[i]?.message
    if (m?.role === 'assistant') {
      const t = textOf(m.content)
      if (t) {
        const preview = assistantPreview(t)
        if (preview !== undefined) return preview
      }
    }
  }
  return ''
}

/** The session's first user message, one line (a title when it has no name). */
export function firstText(branch) {
  for (const e of branch) {
    if (e?.message?.role === 'user') {
      const t = textOf(e.message.content)
      if (t) return snippet(t)
    }
  }
  return ''
}

/** The live text of an assistant message being generated (all its text blocks so far). */
export const partialText = (message) => textOf(message?.content)
