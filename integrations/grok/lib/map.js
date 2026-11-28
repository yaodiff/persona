import { appToolName } from '../../runtime-sdk/index.js'
import { assistantPreview } from '../../runtime-sdk/lib/assistant-preview.js'
import { SNAPSHOT_RE } from '../../runtime-sdk/lib/compat.js'

const NAMES = { run_terminal_command: 'Bash', read_file: 'Read', write_file: 'Write', edit_file: 'Edit', list_directory: 'LS', search_files: 'Grep', web_search: 'WebSearch', web_fetch: 'WebFetch' }
export const toolName = (name) => appToolName(name, NAMES)
export const visibleText = (text) => String(text || '').replace(SNAPSHOT_RE, '').trim()
export const contentText = (content) => (content || []).map((b) => b.type === 'content' ? b.content?.text || '' : b.text || '').filter(Boolean).join('\n')

export function addItem(state, item) {
  const seq = state.items.length + 1
  const value = { id: `${seq}#0`, seq, ts: new Date().toISOString(), ...item }
  state.items.push(value)
  state.rev++
  return value
}

export function updateItems(state, update, { replay = false } = {}) {
  if (!update) return
  const type = update.sessionUpdate
  if (type === 'user_message_chunk' && update.content?.type === 'image') {
    if (!replay && state.echo) return
    state.segment = null
    addItem(state, { role: 'user', kind: 'image', image: { mime: update.content.mimeType || 'image/png', data: update.content.data || '' } })
    return
  }
  if (type === 'agent_message_chunk' || type === 'agent_thought_chunk' || type === 'user_message_chunk') {
    const role = type === 'user_message_chunk' ? 'user' : 'assistant'
    const kind = type === 'agent_thought_chunk' ? 'thinking' : 'text'
    const text = update.content?.type === 'text' ? update.content.text || '' : ''
    if (!text) return
    if (role === 'user') {
      // The live prompt is echoed locally for client_msg_id. Native replay remains the source after a reconnect.
      if (!replay && state.echo && visibleText(text) === state.echo.text) return
      state.segment = null
      addItem(state, { role, kind, text: visibleText(text) })
      state.lastText = ''
      return
    }
    if (state.segment?.role === role && state.segment.kind === kind) { state.segment.text += text; state.rev++ }
    else {
      // A control line can arrive in several chunks. Restore the preview before this segment once it is complete.
      state.previewBeforeSegment = state.lastText
      state.segment = addItem(state, { role, kind, text })
    }
    if (kind === 'text') state.lastText = assistantPreview(state.segment.text) ?? state.previewBeforeSegment
    return
  }
  if (type === 'tool_call') {
    state.segment = null
    const name = update._meta?.['x.ai/tool']?.name || update.title || 'Tool'
    const input = update.rawInput || {}
    state.tools.set(update.toolCallId, update)
    addItem(state, { role: 'assistant', kind: 'tool_use', tool_use_id: update.toolCallId, tool_name: toolName(name), tool_input: input })
  } else if (type === 'tool_call_update') {
    state.segment = null
    const prev = state.tools.get(update.toolCallId) || {}
    const tool = {
      ...prev,
      ...update,
      content: update.content?.length ? update.content : (prev.content ?? update.content),
      rawOutput: update.rawOutput !== undefined ? update.rawOutput : prev.rawOutput,
    }
    state.tools.set(update.toolCallId, tool)
    if (['completed', 'failed'].includes(update.status) && !state.results.has(update.toolCallId)) {
      state.results.add(update.toolCallId)
      addItem(state, { role: 'user', kind: 'tool_result', tool_use_id: update.toolCallId,
        text: contentText(tool.content) || (typeof tool.rawOutput === 'string' ? tool.rawOutput : JSON.stringify(tool.rawOutput ?? {})),
        is_error: update.status === 'failed' })
    }
  }
}

export function blankItems() { return { items: [], tools: new Map(), results: new Set(), rev: 0, segment: null, lastText: '' } }

export function parseTranscript(data) {
  const state = blankItems()
  for (const line of data.split('\n')) {
    let record
    try { record = JSON.parse(line) } catch { continue }
    if (record.method === 'session/update') updateItems(state, record.params?.update, { replay: true })
  }
  return state
}

export function modelOptions(models, selected) {
  const order = ['minimal', 'low', 'medium', 'high', 'xhigh', 'max']
  return (models || []).map((m) => {
    const levels = (m._meta?.reasoningEfforts || []).map((e) => e.id || e.value).filter((e) => order.includes(e))
    return { id: m.modelId, name: m.name || m.modelId, default: m.modelId === selected,
      efforts: order.filter((e) => levels.includes(e)), default_effort: m._meta?.reasoningEffort }
  })
}
