// A fake `personad mcp` for the MCP proxy tests: lists two tools and answers a tools/call with the name and arguments
// it received. Usage: node fake-mcp-server.mjs
import { createInterface } from 'node:readline'

const tools = [
  { name: 'todo_create', description: 'Add a to-do', inputSchema: { type: 'object', properties: { title: { type: 'string' } } } },
  { name: 'agents_list', description: 'List the Agents', inputSchema: { type: 'object', properties: {} } },
]
const send = (msg) => process.stdout.write(JSON.stringify(msg) + '\n')
createInterface({ input: process.stdin }).on('line', (line) => {
  const msg = JSON.parse(line)
  if (msg.method === 'tools/list') send({ jsonrpc: '2.0', id: msg.id, result: { tools } })
  else if (msg.method === 'tools/call') {
    const { name, arguments: args } = msg.params
    if (!tools.some((t) => t.name === name)) send({ jsonrpc: '2.0', id: msg.id, error: { code: -32602, message: `unknown tool ${name}` } })
    else send({ jsonrpc: '2.0', id: msg.id, result: { content: [{ type: 'text', text: JSON.stringify({ name, args }) }] } })
  } else if (msg.id !== undefined) send({ jsonrpc: '2.0', id: msg.id, result: {} })
})
