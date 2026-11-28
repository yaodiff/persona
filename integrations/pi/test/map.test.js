import { test } from 'node:test'
import assert from 'node:assert/strict'
import { branchItems, branchKey, callName, entryItems, imageOf, lastText, toolInput, toolName } from '../lib/map.js'

const msg = (id, parentId, message) => ({ type: 'message', id, parentId, timestamp: '2026-11-01T10:00:00.000Z', message })

test('user, assistant (thinking, text, tool calls) and tool results become the apps\' items', () => {
  const user = entryItems(msg('u', null, { role: 'user', content: [{ type: 'text', text: 'fix it' }, { type: 'image', data: 'AAAA', mimeType: 'image/png' }] }), 3)
  assert.deepEqual(user.map((i) => [i.id, i.role, i.kind]), [['3#0', 'user', 'text'], ['3#1', 'user', 'image']])
  assert.deepEqual(user[1].image, { mime: 'image/png', data: '', lazy: true }, 'a placeholder in Item.image: the bytes come from session.image')

  const asst = entryItems(msg('a', 'u', { role: 'assistant', stopReason: 'toolUse', content: [
    { type: 'thinking', thinking: 'look first' },
    { type: 'text', text: 'Reading.' },
    { type: 'toolCall', id: 'call_1', name: 'read', arguments: { path: 'a.go', offset: 3 } },
    { type: 'toolCall', id: 'call_2', name: 'edit', arguments: { path: 'a.go', edits: [{ oldText: 'x', newText: 'y' }] } },
    { type: 'toolCall', id: 'call_3', name: 'edit', arguments: { path: 'a.go', edits: [{ oldText: 'a', newText: 'b' }, { oldText: 'c', newText: 'd' }] } },
  ] }), 4)
  assert.deepEqual(asst.map((i) => i.kind), ['thinking', 'text', 'tool_use', 'tool_use', 'tool_use'])
  assert.deepEqual(asst[2].tool_input, { file_path: 'a.go', offset: 3 })
  assert.deepEqual([asst[3].tool_name, asst[3].tool_input], ['Edit', { file_path: 'a.go', old_string: 'x', new_string: 'y' }])
  assert.equal(asst[4].tool_name, 'MultiEdit')

  const res = entryItems(msg('r', 'a', { role: 'toolResult', toolCallId: 'call_1', toolName: 'read', isError: false, content: [{ type: 'text', text: 'package a' }, { type: 'image', data: 'BB', mimeType: 'image/jpeg' }] }), 5)
  assert.deepEqual(res.map((i) => [i.kind, i.tool_use_id ?? '']), [['tool_result', 'call_1'], ['image', 'call_1']], 'a result\'s image belongs to its call')
  assert.deepEqual(res[1].image, { mime: 'image/jpeg', data: '', lazy: true })
  assert.equal(imageOf(msg('r', 'a', { role: 'toolResult', content: 'plain text output' }), 1), undefined, 'a text-only result has no image')
  assert.equal(imageOf(msg('r', 'a', { role: 'toolResult', content: [{ type: 'text', text: 'x' }, { type: 'image', data: 'BB', mimeType: 'image/jpeg' }] }), 1).data, 'BB')
  assert.equal(imageOf(msg('u', null, { role: 'user', content: [{ type: 'text', text: 'fix it' }, { type: 'image', data: 'AAAA', mimeType: 'image/png' }] }), 1).data, 'AAAA')
})

test('interrupted and failed turns, compactions and the user\'s own commands leave something to see; the rest does not', () => {
  assert.equal(entryItems(msg('a', null, { role: 'assistant', stopReason: 'aborted', content: [] }), 1)[0].text, '已中断')
  assert.match(entryItems(msg('a', null, { role: 'assistant', stopReason: 'error', errorMessage: 'rate limited', content: [] }), 1)[0].text, /rate limited/)
  assert.deepEqual(entryItems(msg('a', null, { role: 'assistant', stopReason: 'length', content: [{ type: 'text', text: '一半' }] }), 1).map((i) => i.text),
    ['一半', '输出达到上限，这一轮停了'], 'a turn cut by the output limit says so')
  assert.deepEqual(entryItems(msg('a', null, { role: 'assistant', stopReason: 'stop', content: [{ type: 'text', text: '好了' }] }), 1).map((i) => i.text),
    ['好了'], 'a turn the model ended itself adds no notice')
  assert.equal(entryItems({ type: 'compaction', id: 'c', parentId: 'a', summary: 's', firstKeptEntryId: 'a' }, 2)[0].text, '已压缩上下文')
  const bash = entryItems(msg('b', null, { role: 'bashExecution', command: 'ls', output: 'a.go', exitCode: 0, cancelled: false, truncated: false }), 3)
  assert.deepEqual(bash.map((i) => [i.kind, i.tool_name ?? '', i.is_error ?? false]), [['tool_use', 'Bash', false], ['tool_result', '', false]])
  assert.deepEqual(entryItems(msg('s', null, { role: 'system', content: 'You are pi' }), 4), [])
  assert.deepEqual(entryItems({ type: 'model_change', id: 'm', parentId: null }, 5), [])
  assert.equal(toolName('bash'), 'Bash')
  assert.deepEqual(toolInput('write', { path: 'x', content: 'y' }), { file_path: 'x', content: 'y' })
  assert.equal(callName('edit', { path: 'a.go', oldText: 'before', newText: 'after' }), 'Edit')
  assert.deepEqual(toolInput('edit', { path: 'a.go', oldText: 'before', newText: 'after' }), {
    file_path: 'a.go',
    old_string: 'before',
    new_string: 'after',
  })
  assert.equal(callName('edit', { path: 'a.go', edits: [{ oldText: 'a', newText: 'b' }, { oldText: 'c', newText: 'd' }] }), 'MultiEdit')
  assert.deepEqual(toolInput('edit', { path: 'a.go', edits: [null] }), { file_path: 'a.go' })
})

test('a branch shows only its entries, at their file positions; its key changes only when it leaves the file order', () => {
  const e = [
    { type: 'model_change', id: 'm', parentId: null },
    msg('u1', 'm', { role: 'user', content: 'one' }),
    msg('a1', 'u1', { role: 'assistant', content: [{ type: 'text', text: 'first answer' }] }),
    msg('u2', 'a1', { role: 'user', content: 'two' }),
    msg('a2', 'u2', { role: 'assistant', content: [{ type: 'text', text: 'second answer' }] }),
  ]
  const main = e
  assert.equal(branchKey(e, main), 'root')
  assert.deepEqual(branchItems(e, main).map((i) => i.seq), [2, 3, 4, 5])
  assert.equal(lastText(main), 'second answer')
  // The user edits "two": a new entry whose parent is a1, appended after a2.
  const edited = [...e, msg('u2b', 'a1', { role: 'user', content: 'two, again' })]
  const branch = [e[0], e[1], e[2], edited[5]]
  assert.equal(branchKey(edited, branch), 'u2b', 'the branch left the file order at u2b')
  assert.deepEqual(branchItems(edited, branch).map((i) => i.seq), [2, 3, 6])
  // Appending on that branch keeps its key.
  const more = [...edited, msg('a2b', 'u2b', { role: 'assistant', content: [{ type: 'text', text: 'ok' }] })]
  assert.equal(branchKey(more, [...branch, more[6]]), 'u2b')
  // Back on the first branch (/tree), nothing written: its key again; at an earlier point of it, another one.
  assert.equal(branchKey(more, main), 'root')
  assert.equal(branchKey(more, main.slice(0, 3)), 'root@a1', 'a leaf with entries after it is a branch of its own')
  assert.equal(branchKey(e, main.slice(0, 3)), 'root@a1')
})

test('assistant preview filters standalone botmux lines before flattening the branch', () => {
  const marker = 'BOTMUX_NOTHING_TO_SEND'
  const message = (text) => ({ message: { role: 'assistant', content: [{ type: 'text', text }] } })
  const words = 'The manual calls it ' + marker
  const branch = [message(words + '\n  ' + marker + '\r\nDetails.'), message(marker)]
  assert.equal(lastText(branch), words + ' Details.')
})
