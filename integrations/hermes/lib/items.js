import { appToolName, clipInput, snippet } from '../../runtime-sdk/index.js'

const tools = { execute: 'Bash', terminal: 'Bash', read: 'Read', edit: 'Edit', write: 'Write',
  patch: 'Edit', search: 'Grep', web_extract: 'WebFetch', todo: 'TodoWrite' }

export const toolName = (update) => appToolName(update.kind ?? update.title ?? 'Tool', tools)

export function transcript() {
  return { items: [], segment: null, activeTools: new Set(), lastText: '' }
}

export function addItem(state, item) {
  const seq = state.items.length + 1
  const value = { id: `${seq}#0`, seq, ts: new Date().toISOString(), ...item }
  state.items.push(value)
  if (item.role === 'user' && item.kind === 'text') state.lastText = snippet(item.text)
  if (item.role === 'assistant' && item.kind === 'text') state.lastText = snippet(item.text)
  return value
}

export function flush(state) {
  if (!state.segment) return
  const { role, kind, text } = state.segment
  state.segment = null
  addItem(state, { role, kind, text })
}

export function restoreFailures(state, failures) {
  if (!failures.length) return state
  const users = state.items.flatMap((item, index) => isUserText(item) ? [index] : [])
  const restored = transcript()
  const copy = ({ id, seq, ...item }) => addItem(restored, item)
  let copied = 0
  for (const failure of failures) {
    const begin = users[failure.userIndex] ?? state.items.length
    // Classify the failed input on its first reload, before a retry of the same text
    // can occupy the same native position.
    failure.persisted ??= state.items[begin]?.text === failure.items[0].text
    const end = failure.persisted ? (users[failure.userIndex + 1] ?? state.items.length) : begin
    state.items.slice(copied, begin).forEach(copy)
    if (failure.persisted && begin < end) {
      const user = copy(state.items[begin])
      if (failure.items[0].client_msg_id) user.client_msg_id = failure.items[0].client_msg_id
      state.items.slice(begin + 1, end).forEach(copy)
      copy(failure.items.at(-1))
    } else {
      failure.items.forEach(copy)
    }
    copied = end
  }
  state.items.slice(copied).forEach(copy)
  return restored
}

export function isUserText(item) {
  return item.role === 'user' && item.kind === 'text'
}

export function reconcileReplay(previous, next) {
  if (!previous) return next
  const oldInputs = previous.items.filter(isUserText)
  const newInputs = next.items.filter(isUserText)
  // ACP replay does not carry Persona's client IDs. Keep them on the matching
  // input prefix, without attaching an old ID to a different native turn.
  for (let i = 0; i < Math.min(oldInputs.length, newInputs.length); i++) {
    if (oldInputs[i].text !== newInputs[i].text) break
    if (oldInputs[i].client_msg_id && !newInputs[i].client_msg_id) {
      newInputs[i].client_msg_id = oldInputs[i].client_msg_id
    }
  }
  const comparable = (list) => list.map(({ id, seq, ts, ...value }) => value)
  return JSON.stringify(comparable(previous.items)) === JSON.stringify(comparable(next.items)) ? previous : next
}

function chunk(state, update, role, kind) {
  if (update.content?.type !== 'text' || !update.content.text) return
  const message = update.messageId
  if (state.segment?.kind !== kind || state.segment?.role !== role || state.segment?.message !== message) {
    flush(state)
    state.segment = { role, kind, message, text: '' }
  }
  state.segment.text += update.content.text
}

function toolText(update) {
  return (update.content ?? []).map(({ content }) => content?.text).filter(Boolean).join('\n')
}

export function toolInput(update) {
  if (update.rawInput && typeof update.rawInput === 'object') return clipInput(update.rawInput)
  const text = toolText(update)
  if (update.kind === 'execute' && text.startsWith('$ ')) return clipInput({ command: text.slice(2) })
  const path = update.locations?.[0]?.path
  return path ? { file_path: path } : {}
}

export function applyUpdate(state, update, replay) {
  switch (update.sessionUpdate) {
    case 'user_message_chunk':
      if (replay) chunk(state, update, 'user', 'text')
      break
    case 'agent_message_chunk':
      chunk(state, update, 'assistant', 'text')
      break
    case 'agent_thought_chunk':
      chunk(state, update, 'assistant', 'thinking')
      break
    case 'tool_call': {
      flush(state)
      state.activeTools.add(update.toolCallId)
      addItem(state, { role: 'assistant', kind: 'tool_use', tool_use_id: update.toolCallId,
        tool_name: toolName(update), tool_input: toolInput(update) })
      break
    }
    case 'tool_call_update': {
      if (!['completed', 'failed'].includes(update.status) || !state.activeTools.delete(update.toolCallId)) break
      flush(state)
      const fallback = typeof update.rawOutput === 'string' ? update.rawOutput : JSON.stringify(update.rawOutput ?? '')
      const text = toolText(update)
      addItem(state, { role: 'user', kind: 'tool_result', tool_use_id: update.toolCallId,
        text: text || fallback, is_error: update.status === 'failed' })
      break
    }
  }
}
