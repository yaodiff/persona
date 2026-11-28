import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  acpPermissionCard,
  contextTokens,
  fromRow,
  imageOf,
  lastText,
  liveText,
  messageItems,
  permissionCard,
  promptWithContext,
  resultText,
  toolInput,
  toolName,
  v1ImageOf,
  v1SessionItems,
  visibleUserText,
} from '../lib/map.js'

const T = 1_790_000_000_000

test('tools get the names and arguments the apps draw', () => {
  assert.deepEqual(['shell', 'read', 'write', 'edit', 'grep', 'glob', 'subagent', 'websearch', 'webfetch', 'skill', 'patch'].map(toolName),
    ['Bash', 'Read', 'Write', 'Edit', 'Grep', 'Glob', 'Agent', 'WebSearch', 'WebFetch', 'Skill', 'patch'])
  assert.deepEqual(toolInput('shell', { command: 'ls', description: 'list', timeout: 5, extra: 1 }), { command: 'ls', description: 'list', timeout: 5 })
  assert.deepEqual(toolInput('read', { path: '/a', offset: 2, limit: 10 }), { file_path: '/a', offset: 2, limit: 10 })
  assert.deepEqual(toolInput('write', { path: '/a', content: 'x' }), { file_path: '/a', content: 'x' })
  assert.deepEqual(toolInput('edit', { path: '/a', oldString: 'o', newString: 'n' }), { file_path: '/a', old_string: 'o', new_string: 'n' })
  assert.deepEqual(toolInput('grep', { pattern: 'x', path: '.' }), { pattern: 'x', path: '.' })
  assert.deepEqual(toolInput('subagent', { description: 'd', prompt: 'p', agent: 'explore', background: false }), { description: 'd', prompt: 'p', subagent_type: 'explore' })
  assert.deepEqual(toolInput('weird', { a: 1 }), { a: 1 })
})

test('a user message gives its text and its attachments', () => {
  const items = messageItems({ type: 'user', time: { created: T }, text: 'look', files: [{ mime: 'image/png', name: 'a.png', data: 'AAAA' }, { mime: 'text/plain', name: 'n.txt', data: 'x' }] }, 7)
  assert.deepEqual(items.map((i) => [i.id, i.role, i.kind, i.text ?? i.image?.mime]), [
    ['7#0', 'user', 'text', 'look'], ['7#1', 'user', 'image', 'image/png'], ['7#2', 'user', 'text', '［附件 n.txt］']])
  assert.deepEqual(items[1].image, { mime: 'image/png', data: '', lazy: true }, 'a placeholder in Item.image: the bytes come from session.image')
  assert.equal(items[0].seq, 7)
  assert.deepEqual(imageOf({ type: 'user', files: [{ mime: 'image/png', data: 'AAAA' }] }, 1), { mime: 'image/png', data: 'AAAA' })
  assert.equal(imageOf({ type: 'user', files: [{ mime: 'image/png', data: 'AAAA' }] }, 2), undefined)
})

test('an assistant message: reasoning, text, tool calls with their results, ids that do not move when a result arrives', () => {
  const part = (extra) => ({ type: 'tool', id: 'call_1', name: 'shell', state: { status: 'pending', input: { command: 'ls' } }, ...extra })
  const running = { type: 'assistant', time: { created: T }, content: [{ type: 'reasoning', text: 'hmm' }, { type: 'text', text: 'Looking.' }, part(), part({ id: 'call_2', name: 'read', state: { status: 'running', input: { path: '/a' } } })] }
  const a = messageItems(running, 9)
  assert.deepEqual(a.map((i) => [i.id, i.kind]), [['9#0', 'thinking'], ['9#2', 'text'], ['9#4', 'tool_use'], ['9#6', 'tool_use']])
  const done = structuredClone(running)
  done.content[2].state = { status: 'completed', input: { command: 'ls' }, content: [{ type: 'text', text: 'a\nb' }] }
  done.content[3].state = { status: 'error', input: { path: '/a' }, error: { message: 'no such file' } }
  const b = messageItems(done, 9)
  assert.deepEqual(b.map((i) => [i.id, i.kind]), [['9#0', 'thinking'], ['9#2', 'text'], ['9#4', 'tool_use'], ['9#5', 'tool_result'], ['9#6', 'tool_use'], ['9#7', 'tool_result']])
  assert.deepEqual(b.filter((i) => i.kind === 'tool_use').map((i) => i.id), a.filter((i) => i.kind === 'tool_use').map((i) => i.id))
  assert.equal(b[3].text, 'a\nb')
  assert.equal(b[3].tool_use_id, 'call_1')
  assert.equal(b[3].is_error, false)
  assert.equal(b[5].is_error, true)
  assert.equal(b[5].text, 'no such file')
  assert.equal(b[2].tool_name, 'Bash')
  assert.deepEqual(b[2].tool_input, { command: 'ls' })
})

test('failures, interruptions, compactions and user shell commands', () => {
  assert.deepEqual(messageItems({ type: 'assistant', content: [], finish: 'error', error: { type: 'provider.auth', message: 'not available' } }, 3).map((i) => i.text), ['出错了：not available'])
  assert.deepEqual(messageItems({ type: 'assistant', content: [{ type: 'text', text: 'partial' }], finish: 'error', error: { type: 'aborted', message: 'Step interrupted' } }, 3).map((i) => i.text), ['partial', '已中断'])
  assert.deepEqual(messageItems({ type: 'assistant', content: [{ type: 'text', text: 'half' }], finish: 'length' }, 3).map((i) => i.text), ['half', '输出达到上限，这一轮停了'])
  assert.deepEqual(messageItems({ type: 'assistant', content: [], finish: 'content-filter' }, 3).map((i) => i.text), ['这一轮被拦下了'])
  assert.deepEqual(messageItems({ type: 'assistant', content: [{ type: 'text', text: 'done' }], finish: 'stop' }, 3).map((i) => i.text), ['done'])
  assert.deepEqual(messageItems({ type: 'idle', outcome: 'interrupted' }, 4).map((i) => i.text), ['已中断'])
  assert.deepEqual(messageItems({ type: 'idle', outcome: 'succeeded' }, 4), [])
  assert.deepEqual(messageItems({ type: 'compaction', status: 'completed' }, 5).map((i) => i.text), ['已压缩上下文'])
  assert.deepEqual(messageItems({ type: 'compaction', status: 'pending' }, 5), [])
  const sh = messageItems({ id: 'msg_x', type: 'shell', command: 'echo hi', status: 'completed', exit: 1, output: { output: 'hi\n' } }, 6)
  assert.deepEqual(sh.map((i) => [i.kind, i.tool_name ?? i.text, i.is_error]), [['tool_use', 'Bash', undefined], ['tool_result', 'hi\n', true]])
  for (const type of ['system', 'synthetic', 'model-switched', 'agent-switched', 'location-switched']) assert.deepEqual(messageItems({ type, text: 'x' }, 1), [])
})

test('rows, list helpers and cards', () => {
  assert.deepEqual(fromRow({ id: 'm1', type: 'user', data: '{"text":"hi","time":{"created":1}}' }), { text: 'hi', time: { created: 1 }, id: 'm1', type: 'user' })
  assert.deepEqual(fromRow({ id: 'm2', type: 'user', data: '{oops' }), { id: 'm2', type: 'user' })
  assert.equal(lastText([{ type: 'assistant', content: [{ type: 'text', text: 'first' }] }, { type: 'user', text: 'q' }, { type: 'assistant', content: [{ type: 'tool', name: 'shell' }, { type: 'text', text: 'last  words' }] }]), 'last words')
  assert.equal(contextTokens({ tokens: { input: 100, output: 50, reasoning: 5, cache: { read: 400, write: 10 } } }), 510)
  assert.equal(contextTokens({}), 0)
  assert.equal(liveText(new Map([[1, 'B'], [0, 'A']])), 'AB')
  assert.equal(resultText({ status: 'error', error: 'boom' }), 'boom')
  assert.deepEqual(permissionCard({ action: 'shell', resources: ['rm -rf build'] }), { tool_name: 'Bash', tool_input: { command: 'rm -rf build' }, summary: 'rm -rf build' })
  assert.deepEqual(permissionCard({ action: 'edit', resources: ['/a/b.js'] }).tool_input, { file_path: '/a/b.js' })
  assert.deepEqual(permissionCard({ action: 'mcp_x', resources: ['a', 'b'] }).tool_input, { resources: ['a', 'b'] })
})

test('context snapshot stripping hides attribute-less persona-context while preserving forwarded blocks', () => {
  const wrapped = promptWithContext('帮我看下今天的日程', '当前时间：2026-11-20')
  assert.match(wrapped, /^<persona-context>\n当前时间：2026-11-20\n<\/persona-context>\n\n帮我看下今天的日程$/)
  assert.equal(visibleUserText(wrapped), '帮我看下今天的日程')
  assert.equal(visibleUserText('<jarvis-context>\nlegacy\n</jarvis-context>\n\nhi'), 'hi')

  const forwarded = '<persona-context kind="forward" author="alice">\nquoted text\n</persona-context>\n\n请总结'
  assert.equal(visibleUserText(forwarded), forwarded)

  const items = messageItems({ type: 'user', time: { created: T }, text: wrapped }, 12)
  assert.equal(items.length, 1)
  assert.equal(items[0].text, '帮我看下今天的日程')
})

test('v1SessionItems, v1ImageOf, and acpPermissionCard map opencode 1.x DB and ACP permissions', () => {
  const messages = [
    { id: 'm1', time_created: T, data: JSON.stringify({ role: 'user', time: { created: T } }) },
    {
      id: 'm2',
      time_created: T + 1,
      data: JSON.stringify({
        role: 'assistant',
        modelID: 'claude-sonnet-4-6',
        providerID: 'opencode-go',
        tokens: { input: 200, cache: { read: 800 } },
        time: { created: T + 1, completed: T + 2 },
      }),
    },
  ]
  const parts = new Map([
    ['m1', [
      { id: 'p1', data: JSON.stringify({ type: 'text', text: promptWithContext('看这张图', 'ctx') }) },
      { id: 'p2', data: JSON.stringify({ type: 'file', mime: 'image/png', filename: 'a.png', url: 'data:image/png;base64,QUFB' }) },
      { id: 'p3', data: JSON.stringify({ type: 'file', mime: 'text/plain', filename: 'note.txt' }) },
    ]],
    ['m2', [
      { id: 'p4', data: JSON.stringify({ type: 'reasoning', text: 'thinking...' }) },
      { id: 'p5', data: JSON.stringify({ type: 'tool', callID: 'call_1', tool: 'bash', state: { status: 'completed', input: { command: 'pwd' }, output: '/tmp\n' } }) },
      { id: 'p6', data: JSON.stringify({ type: 'text', text: 'Done.' }) },
    ]],
  ])
  const res = v1SessionItems(messages, parts, { clientMsgIdFor: (ordinal, txt) => (ordinal === 1 && txt === '看这张图' ? 'cm_1' : undefined) })
  assert.equal(res.toSeq, 2)
  assert.equal(res.model, 'claude-sonnet-4-6')
  assert.equal(res.provider, 'opencode-go')
  assert.equal(res.tokens, 1000)
  assert.equal(res.lastText, 'Done.')
  assert.equal(res.items[0].text, '看这张图')
  assert.equal(res.items[0].client_msg_id, 'cm_1')
  assert.deepEqual(res.items[1].image, { mime: 'image/png', data: '', lazy: true })
  assert.equal(res.items[2].text, '［附件 note.txt］')
  assert.deepEqual(res.items.slice(3).map((i) => i.kind), ['thinking', 'tool_use', 'tool_result', 'text'])
  assert.deepEqual(v1ImageOf(parts.get('m1').map((p) => JSON.parse(p.data)), 1), { mime: 'image/png', data: 'QUFB' })

  const card = acpPermissionCard({
    toolCall: { title: 'bash', kind: 'execute', rawInput: { command: 'rm -rf dist' } },
    options: [{ optionId: 'always', kind: 'allow_always', name: 'Always allow' }],
  })
  assert.equal(card.tool_name, 'Bash')
  assert.equal(card.raw_name, 'bash')
  assert.equal(card.summary, 'rm -rf dist')
  assert.equal(card.always_label, '总是允许 Bash')
})

test('assistant preview filters standalone botmux lines in both database formats', () => {
  const marker = 'BOTMUX_NOTHING_TO_SEND'
  const words = 'The manual calls it ' + marker
  const texts = [words + '\n  ' + marker + '\r\nDetails.', marker]
  const messages = texts.map((text, i) => ({ id: 'm' + i, type: 'assistant', content: [{ type: 'text', text }] }))
  assert.equal(lastText(messages), words + ' Details.')
  const rows = messages.map(({ id }) => ({ id, data: JSON.stringify({ role: 'assistant' }) }))
  const parts = new Map(messages.map(({ id, content }) => [id, content.map(p => ({ data: JSON.stringify(p) }))]))
  const result = v1SessionItems(rows, parts)
  assert.equal(result.lastText, words + ' Details.')
  assert.deepEqual(result.items.map(i => i.text), texts)
})
