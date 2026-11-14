#!/usr/bin/env node
// Usage: node mcp-proxy.mjs <command> [args...]
//
// Stdio MCP proxy for an assistant chat's `personad mcp` (lib/compat.js mcpServers). agy defers the MCP tools over its
// declaration budget, largest schemas first, and for servers passed through ACP it never declares `call_mcp_tool`, so
// a deferred Persona tool cannot be called at all (#7333). This adds one small tool, persona_call, that calls any
// Persona tool by name; small, it is never the one deferred. Everything else passes through unchanged.
// shortcut: works around agy 1.5.8; drop it once agy declares call_mcp_tool for ACP servers.
import { spawn } from 'node:child_process'
import { createInterface } from 'node:readline'

const DISPATCH = 'persona_call'
const DISPATCH_TOOL = {
  name: DISPATCH,
  description:
    'Call a Persona tool by name. Use it for any Persona tool that is not in your tool list: Antigravity defers large ' +
    'MCP tools and cannot call deferred ones in this session (there is no call_mcp_tool). Read the tool\'s schema file ' +
    'first when you do not know its arguments. Without a name it lists the Persona tools.',
  inputSchema: {
    type: 'object',
    properties: {
      name: { type: 'string', description: 'The Persona tool, e.g. todo_create' },
      arguments: { type: 'object', description: 'Its arguments, as its schema describes them', additionalProperties: true },
    },
  },
}

const [command, ...args] = process.argv.slice(2)
const child = spawn(command, args, { stdio: ['pipe', 'pipe', 'inherit'] })
child.on('error', (err) => {
  process.stderr.write(`persona mcp proxy: ${err.message}\n`)
  process.exit(1)
})
child.on('exit', (code, signal) => process.exit(code ?? (signal ? 1 : 0)))
process.stdin.on('end', () => child.stdin.end())

let names = null // the server's tools, from its last tools/list
const send = (msg) => process.stdout.write(JSON.stringify(msg) + '\n')

createInterface({ input: process.stdin, crlfDelay: Infinity }).on('line', (line) => {
  const msg = parse(line)
  if (msg?.method === 'tools/call' && msg.params?.name === DISPATCH) {
    const { name, arguments: input } = msg.params.arguments || {}
    if (!name) { // list them; the server itself rejects a name it does not have
      const text = `The Persona tools: ${(names || []).join(', ')}.`
      send({ jsonrpc: '2.0', id: msg.id, result: { content: [{ type: 'text', text }] } })
      return
    }
    line = JSON.stringify({ ...msg, params: { ...msg.params, name, arguments: input || {} } })
  }
  child.stdin.write(line + '\n')
})

createInterface({ input: child.stdout, crlfDelay: Infinity }).on('line', (line) => {
  const msg = parse(line)
  const tools = msg?.result?.tools
  if (Array.isArray(tools)) {
    names = tools.map((t) => t.name)
    line = JSON.stringify({ ...msg, result: { ...msg.result, tools: [...tools, DISPATCH_TOOL] } })
  }
  process.stdout.write(line + '\n')
})

function parse(line) {
  try {
    return JSON.parse(line)
  } catch {
    return null
  }
}
