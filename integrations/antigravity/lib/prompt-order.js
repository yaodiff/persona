// agy requires a native workflow command at byte 0 of its first text block. The runtime builds
// context resources before that block for every other prompt, so move resources last when the
// text starts with one of agy's own workflow commands. This is the ACP boundary itself, which
// keeps the exported USER_INPUT content and the block order seen by agy in sync.
import { AgyAcpClient } from './acp.js'

const WORKFLOW = new Set([
  '/boost',
  '/teamwork',
  '/swarm',
  '/goal',
  '/plan',
  '/grill-me',
  '/learn',
  '/schedule',
  '/browser',
])

function workflowCommand(text) {
  if (typeof text !== 'string') return false
  const body = text.replace(/^[ \t\r\n\v\f]+/, '')
  const end = body.search(/[ \t\r\n\v\f]/)
  return WORKFLOW.has(body.slice(0, end < 0 ? body.length : end))
}

const prompt = AgyAcpClient.prototype.prompt
AgyAcpClient.prototype.prompt = function orderedPrompt(sessionId, blocks) {
  if (Array.isArray(blocks)) {
    const text = blocks.findIndex((block) => block?.type === 'text')
    if (text >= 0 && blocks.slice(0, text).every((block) => block?.type === 'resource') && workflowCommand(blocks[text].text)) {
      const ordered = blocks.map((block, index) => index === text ? { ...block, text: block.text.replace(/^[ \t\r\n\v\f]+/, '') } : block)
      const resources = ordered.filter((block) => block?.type === 'resource')
      blocks = [...ordered.filter((block) => block?.type !== 'resource'), ...resources]
    }
  }
  return prompt.call(this, sessionId, blocks)
}
